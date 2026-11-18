package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/daemon"
)

type metricPoint struct {
	Time        int64   `json:"time"`
	Upload      *uint64 `json:"upload"`
	Download    *uint64 `json:"download"`
	Memory      *uint64 `json:"memory"`
	Connections *int    `json:"connections"`
	UploadTotal uint64  `json:"uploadTotal,string"`
	DownTotal   uint64  `json:"downloadTotal,string"`
}

func (s *server) recordMetrics(state runtimeState, now time.Time) {
	if s.usage == nil {
		return
	}
	point := metricPoint{Time: now.UnixMilli(), UploadTotal: state.UploadTotal, DownTotal: state.DownloadTotal}
	if state.MetricsAvailable && state.State == "running" {
		point.Upload, point.Download, point.Memory, point.Connections = &state.UploadRate, &state.DownloadRate, &state.MemoryBytes, &state.ActiveConnections
	}
	s.usage.mu.Lock()
	s.usage.metrics = append(s.usage.metrics, point)
	if len(s.usage.metrics) > 300 {
		s.usage.metrics = append([]metricPoint{}, s.usage.metrics[len(s.usage.metrics)-300:]...)
	}
	s.usage.mu.Unlock()
}

func (s *server) metricsHistory(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		writeJSON(w, 200, map[string]any{"points": []metricPoint{}})
		return
	}
	s.usage.mu.Lock()
	points := append([]metricPoint{}, s.usage.metrics...)
	s.usage.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"points": points})
}

func (s *server) collectUsageConnections(ctx context.Context, client daemon.StartedServiceClient, started time.Time) {
	if s.usage == nil {
		return
	}
	u := s.usage
	u.collection.Lock()
	defer u.collection.Unlock()
	if ctx.Err() != nil {
		return
	}
	defer func() {
		if err := u.flush(); err != nil && s.logs != nil {
			s.logs.add("[kiko] 保存用量失败：" + err.Error())
		}
	}()
	run := usageRunID(started)
	for ctx.Err() == nil {
		attemptCtx, cancel := context.WithCancel(ctx)
		stream, err := client.SubscribeConnections(attemptCtx, &daemon.SubscribeConnectionsRequest{Interval: int64(time.Second)})
		if err == nil {
			for ctx.Err() == nil {
				frame, recvErr := stream.Recv()
				if recvErr != nil {
					err = recvErr
					break
				}
				if ctx.Err() != nil {
					break
				}
				if err = u.ingest(run, frame, time.Now()); err != nil {
					break
				}
			}
		}
		cancel()
		u.mu.Lock()
		u.connected = false
		if err != nil && ctx.Err() == nil {
			u.lastError = "用量采集等待重连：" + err.Error()
		}
		u.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *server) usageMaintenance(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer func() {
		if err := s.usage.flush(); err != nil && s.logs != nil {
			s.logs.add("[kiko] 保存用量失败：" + err.Error())
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.usage.flush(); err != nil {
				s.usage.mu.Lock()
				s.usage.lastError = "保存用量失败：" + err.Error()
				s.usage.mu.Unlock()
			}
		}
	}
}

func parseUsageQuery(r *http.Request) (usageQuery, error) {
	values := r.URL.Query()
	now := time.Now()
	q := usageQuery{From: now.Add(-time.Hour).Truncate(time.Minute).UnixMilli(), To: now.Truncate(time.Minute).Add(time.Minute).UnixMilli(), Group: "source", Filters: map[string][]string{}, Sort: "total", Limit: 50}
	for name, target := range map[string]*int64{"from": &q.From, "to": &q.To} {
		if value := values.Get(name); value != "" {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < 0 {
				return q, fmt.Errorf("%s 时间无效", name)
			}
			*target = number
		}
	}
	if q.To <= q.From {
		return q, errors.New("请选择有效时间范围")
	}
	// A minute belongs to the range when its observation minute is selected.
	q.From = time.UnixMilli(q.From).Truncate(time.Minute).UnixMilli()
	q.To = time.UnixMilli(q.To - 1).Truncate(time.Minute).Add(time.Minute).UnixMilli()
	if value := values.Get("group"); value != "" {
		q.Group = value
	}
	q.Secondary = values.Get("secondary")
	if !slices.Contains(usageDimensions, q.Group) || q.Secondary != "" && (!slices.Contains(usageDimensions, q.Secondary) || q.Secondary == q.Group) {
		return q, errors.New("分组条件无效")
	}
	// filter.<dimension> repeats for each chosen value of a dimension.
	for _, dimension := range usageDimensions {
		chosen := values["filter."+dimension]
		if len(chosen) > 256 {
			return q, errors.New("筛选值过多")
		}
		for _, value := range chosen {
			if len(value) > 8192 {
				return q, errors.New("筛选值过长")
			}
			if value != "" && !slices.Contains(q.Filters[dimension], value) {
				q.Filters[dimension] = append(q.Filters[dimension], value)
			}
		}
	}
	q.Search = values.Get("search")
	if len(q.Search) > 512 {
		return q, errors.New("搜索内容过长")
	}
	if value := values.Get("sort"); value != "" {
		if !slices.Contains([]string{"label", "upload", "download", "total", "connections"}, value) {
			return q, errors.New("排序字段无效")
		}
		q.Sort = value
	}
	q.Ascending = values.Get("order") == "asc"
	for name, target := range map[string]*int{"page": &q.Page, "limit": &q.Limit} {
		if value := values.Get(name); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 || name == "limit" && (number < 1 || number > 200) || name == "page" && number > usageMaxRecords {
				return q, errors.New("分页参数无效")
			}
			*target = number
		}
	}
	return q, nil
}

func (s *server) getUsage(w http.ResponseWriter, r *http.Request) {
	query, err := parseUsageQuery(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	value, err := s.usage.query(query)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if links := s.ruleMap.links(); len(links) > 0 {
		value.RuleLinks = links
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, value)
}

// CSV cells remain text when opened in a spreadsheet, including untrusted names.
func usageCSVLabel(value string) string {
	if strings.HasPrefix(strings.TrimSpace(value), "=") || strings.HasPrefix(strings.TrimSpace(value), "+") || strings.HasPrefix(strings.TrimSpace(value), "-") || strings.HasPrefix(strings.TrimSpace(value), "@") {
		return "'" + value
	}
	return value
}

func (s *server) exportUsage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("scope") == "all" {
		s.exportAllUsage(w, r)
		return
	}
	query, err := parseUsageQuery(r)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	query.Limit, query.Page = 0, 0
	value, err := s.usage.query(query)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage-summary.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return
	}
	writer := csv.NewWriter(w)
	writer.UseCRLF = true
	headers := []string{query.Group}
	if query.Secondary != "" {
		headers = append(headers, query.Secondary)
	}
	if err := writer.Write(append(headers, "upload_bytes", "download_bytes", "total_bytes", "new_connections")); err != nil {
		return
	}
	for _, row := range value.Rows {
		labels := make([]string, len(row.Labels))
		for i, label := range row.Labels {
			labels[i] = usageCSVLabel(label)
		}
		if r.Context().Err() != nil {
			return
		}
		if err := writer.Write(append(labels, usageCSVCounts(row.usageCounters)...)); err != nil {
			return
		}
	}
	writer.Flush()
}

func (s *server) configureUsage(w http.ResponseWriter, r *http.Request) {
	var settings struct {
		RetentionDays int `json:"retentionDays"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&settings); err != nil || settings.RetentionDays != 0 {
		writeJSON(w, 400, map[string]string{"error": "用量历史保持全部保留"})
		return
	}
	if err := s.usage.configure(settings.RetentionDays, false); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, settings)
}

func (s *server) clearUsage(w http.ResponseWriter, r *http.Request) {
	if err := s.usage.configure(0, true); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
