package main

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// connectionQueryState keeps what queries share between requests: the
// process snapshot, sampled at most every 5 s, and the process metadata
// of the history, which retains ancestry across snapshots.
type connectionQueryState struct {
	mu        sync.Mutex
	processes map[int]connProcess
	sampledAt time.Time
	meta      map[string]processMeta
	metaKey   string
}

// connectionProcessEvery is how often queries read the process snapshot.
const connectionProcessEvery = 5 * time.Second

// connectionMetadata returns the process metadata of rows at revision.
func (s *server) connectionMetadata(ctx context.Context, rows []*connRow, revision uint64) (map[string]processMeta, map[int]connProcess) {
	q := &s.connQuery
	q.mu.Lock()
	defer q.mu.Unlock()
	if s.processMonitor != nil && (q.processes == nil || time.Since(q.sampledAt) >= connectionProcessEvery) {
		sampleCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		snapshot, err := s.processMonitor.sample(sampleCtx)
		cancel()
		if err == nil {
			q.processes = make(map[int]connProcess, len(snapshot.Processes))
			for _, p := range snapshot.Processes {
				q.processes[int(p.PID)] = connProcess{PID: int(p.PID), PPID: int(p.PPID), Name: p.Name, Path: p.Path, AppName: p.AppName, AppPath: p.AppPath, StartedAt: p.StartedAt}
			}
		}
		q.sampledAt = time.Now()
	}
	if key := fmt.Sprint(revision, ":", q.sampledAt.UnixNano()); key != q.metaKey {
		q.meta = captureConnectionProcesses(rows, q.processes, q.meta)
		q.metaKey = key
	}
	return q.meta, q.processes
}

// parseConnectionQuery reads the query of GET /api/v1/connections.
func parseConnectionQuery(values url.Values) (connQuery, error) {
	q := connQuery{State: values.Get("state"), Search: values.Get("search"), Filter: map[connFacet][]string{},
		Group: connGroupMode(values.Get("group")), Sort: connColumn(values.Get("sort")), Limit: 50,
		Expansion: connExpansion{keys: map[string]bool{}, collapsed: map[string]bool{}, limits: map[string]int{}}}
	if q.State == "" {
		q.State = "all"
	}
	if q.State != "all" && q.State != "active" && q.State != "closed" && q.State != "rejected" {
		return q, errors.New("state 必须是 all、active、closed 或 rejected")
	}
	if q.Group == "" {
		q.Group = groupNone
	}
	if !slices.Contains([]connGroupMode{groupNone, groupApp, groupParent, groupRootDomain, groupDomain}, q.Group) {
		return q, errors.New("group 必须是 none、app、parent、rootDomain 或 domain")
	}
	if q.Sort == "" {
		q.Sort = colCreated
	}
	if !slices.Contains(connColumns, q.Sort) {
		return q, errors.New("sort 列无效")
	}
	switch values.Get("order") {
	case "":
		q.Descending = q.Sort.descendingByDefault()
	case "asc":
	case "desc":
		q.Descending = true
	default:
		return q, errors.New("order 必须是 asc 或 desc")
	}
	// filter.<facet> repeats for each chosen value of a facet.
	for _, f := range append(slices.Clone(connFacets), facetID) {
		if len(values["filter."+string(f)]) > 256 {
			return q, errors.New("筛选值过多")
		}
		for _, value := range values["filter."+string(f)] {
			if value != "" && !slices.Contains(q.Filter[f], value) {
				q.Filter[f] = append(q.Filter[f], value)
			}
		}
	}
	for _, key := range values["expand"] {
		for _, key := range strings.Split(key, ",") {
			if key == "*" {
				q.Expansion.all = true
			} else if key != "" {
				q.Expansion.keys[key] = true
			}
		}
	}
	for _, key := range values["collapse"] {
		for _, key := range strings.Split(key, ",") {
			if key != "" {
				q.Expansion.collapsed[key] = true
			}
		}
	}
	// more=<group key>=<children>, repeated: group keys hold commas and colons.
	for _, more := range values["more"] {
		i := strings.LastIndex(more, "=")
		n, err := strconv.Atoi(more[i+1:])
		if i <= 0 || err != nil || n < 1 {
			return q, errors.New("more 格式无效")
		}
		q.Expansion.limits[more[:i]] = n
	}
	var err error
	if text := values.Get("pid"); text != "" {
		if q.PID, err = strconv.Atoi(text); err != nil || q.PID < 0 {
			return q, errors.New("pid 无效")
		}
	}
	q.App = values.Get("app")
	q.Rule = values.Get("rule")
	if text := values.Get("page"); text != "" {
		if q.Page, err = strconv.Atoi(text); err != nil || q.Page < 0 {
			return q, errors.New("page 无效")
		}
	}
	if text := values.Get("limit"); text != "" {
		if q.Limit, err = strconv.Atoi(text); err != nil || q.Limit < 1 || q.Limit > 200 {
			return q, errors.New("limit 必须在 1 到 200 之间")
		}
	}
	return q, nil
}

// queryConnections answers a page of the history, filtered, sorted and
// grouped as the client asks; an unchanged answer is 304 by its ETag.
func (s *server) queryConnections(w http.ResponseWriter, r *http.Request) {
	q, err := parseConnectionQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rows, revision := s.connections.queryRows(time.Now())
	meta, processes := s.connectionMetadata(r.Context(), rows, revision)
	page := queryConnectionPage(rows, meta, processes, q)
	page.Revision = revision
	data, err := json.Marshal(page)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeConnectionJSON(w, r, data)
}

// getConnection answers one connection of the history, for the inspector.
func (s *server) getConnection(w http.ResponseWriter, r *http.Request) {
	row := s.connections.queryRow(r.PathValue("id"), time.Now())
	if row == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "连接不在记录中"})
		return
	}
	data, _ := json.Marshal(connRowOutput(row))
	writeConnectionJSON(w, r, data)
}

// writeConnectionJSON writes data with an ETag of its hash, 304 when the
// client has it, gzipped when the client accepts it.
func writeConnectionJSON(w http.ResponseWriter, r *http.Request, data []byte) {
	sum := fnv.New64a()
	_, _ = sum.Write(data)
	tag := fmt.Sprintf(`"%x"`, sum.Sum64())
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(data) < 1024 || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	zw := gzip.NewWriter(w)
	_, _ = zw.Write(data)
	_ = zw.Close()
}

func (s *server) exportConnections(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format 必须是 csv 或 json"})
		return
	}
	rows := s.connections.snapshot()
	w.Header().Set("Cache-Control", "no-store")
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="connections-history.json"`)
		_, _ = w.Write([]byte("["))
		for index, conn := range rows {
			if r.Context().Err() != nil {
				return
			}
			data, err := protoJSON.Marshal(conn)
			if err != nil {
				return
			}
			if index > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write(data)
		}
		_, _ = w.Write([]byte("]"))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="connections-history.csv"`)
	_, _ = w.Write([]byte("\xef\xbb\xbf"))
	writer := csv.NewWriter(w)
	writer.UseCRLF = true
	_ = writer.Write([]string{"id", "created_at", "closed_at", "domain", "source", "destination", "network", "protocol", "process_path", "inbound", "outbound", "rule", "upload_bytes", "download_bytes"})
	for _, conn := range rows {
		if r.Context().Err() != nil {
			return
		}
		path := ""
		if conn.GetProcessInfo() != nil {
			path = conn.GetProcessInfo().GetProcessPath()
		}
		_ = writer.Write([]string{usageCSVLabel(conn.GetId()), strconv.FormatInt(conn.GetCreatedAt(), 10), strconv.FormatInt(conn.GetClosedAt(), 10), usageCSVLabel(conn.GetDomain()), usageCSVLabel(conn.GetSource()), usageCSVLabel(conn.GetDestination()), conn.GetNetwork(), conn.GetProtocol(), usageCSVLabel(path), usageCSVLabel(conn.GetInbound()), usageCSVLabel(conn.GetOutbound()), usageCSVLabel(conn.GetRule()), strconv.FormatInt(conn.GetUplinkTotal(), 10), strconv.FormatInt(conn.GetDownlinkTotal(), 10)})
	}
	writer.Flush()
}

// clearConnectionHistory removes records of the history, {"scope": "all"},
// "active" or "closed", without closing connections.
func (s *server) clearConnectionHistory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
		return
	}
	switch body.Scope {
	case clearAllConnections, clearActiveConnections, clearClosedConnections:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope 必须是 all、active 或 closed"})
		return
	}
	s.connections.clear(body.Scope)
	w.WriteHeader(http.StatusNoContent)
}

// connectionSettings is connection-settings.json and the body of
// /connections/settings.
type connectionSettings struct {
	// ClosedLimit is how many closed connections the history keeps; 0
	// keeps them all.
	ClosedLimit int `json:"closedLimit"`
}

const maxClosedConnectionLimit = 1_000_000

func (s *server) connectionSettingsPath() string {
	return filepath.Join(s.dataDir, "connection-settings.json")
}

// loadConnectionSettings applies the saved settings, the defaults when
// there are none.
func (s *server) loadConnectionSettings() {
	settings := connectionSettings{ClosedLimit: defaultClosedConnectionLimit}
	_ = readJSON(s.connectionSettingsPath(), &settings)
	if settings.ClosedLimit < 0 || settings.ClosedLimit > maxClosedConnectionLimit {
		settings.ClosedLimit = defaultClosedConnectionLimit
	}
	s.connections.setClosedLimit(settings.ClosedLimit)
}

func (s *server) getConnectionSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, connectionSettings{ClosedLimit: s.connections.getClosedLimit()})
}

func (s *server) setConnectionSettings(w http.ResponseWriter, r *http.Request) {
	var settings connectionSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
		return
	}
	if settings.ClosedLimit < 0 || settings.ClosedLimit > maxClosedConnectionLimit {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("closedLimit 必须在 0 到 %d 之间", maxClosedConnectionLimit)})
		return
	}
	if err := atomicJSON(s.connectionSettingsPath(), settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.connections.setClosedLimit(settings.ClosedLimit)
	writeJSON(w, http.StatusOK, settings)
}

func (s *server) closeConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少连接 ID"})
		return
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, err := client.CloseConnection(ctx, &daemon.CloseConnectionRequest{Id: id}); err != nil {
		writeRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) closeAllConnections(w http.ResponseWriter, r *http.Request) {
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := client.CloseAllConnections(ctx, &emptypb.Empty{}); err != nil {
		writeRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// closeConnectionsRequest narrows POST /api/v1/connections/close to the
// connections named, in place of the query's filters.
type closeConnectionsRequest struct {
	IDs []string `json:"ids"`
	// named reports that the body carries the key, null included, so a
	// request that means no connections can never close them all.
	named bool
}

func (c *closeConnectionsRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	raw, ok := fields["ids"]
	c.named = ok
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, &c.IDs)
}

// closeConnectionsResult is the answer of POST /api/v1/connections/close:
// how many active connections matched and how many were closed.
type closeConnectionsResult struct {
	Matched int `json:"matched"`
	Closed  int `json:"closed"`
}

// closeMatchingConnections closes the active connections that pass the
// query of the URL (search, filter.*, pid, app, rule: what GET
// /api/v1/connections lists on its Active tab) or, when the body names ids,
// the named ones that are still active. A query without limits closes
// every connection, as DELETE /api/v1/connections does, only with all=1. It is the one
// place connections close by condition: the list's "Close listed connections"
// button and the rules' "Interrupt connections" action both come here.
func (s *server) closeMatchingConnections(w http.ResponseWriter, r *http.Request) {
	q, err := parseConnectionQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var body closeConnectionsRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
			return
		}
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	rows, _ := s.connections.queryRows(time.Now())
	var active []*connRow
	for _, row := range rows {
		if !row.Closed() && !row.Rejected() {
			active = append(active, row)
		}
	}
	targets := active
	unlimited := false
	if body.named {
		named := make(map[string]bool, len(body.IDs))
		for _, id := range body.IDs {
			named[id] = true
		}
		targets = slices.DeleteFunc(slices.Clone(active), func(row *connRow) bool { return !named[row.Conn.ID] })
	} else if len(q.Filter) == 0 && strings.TrimSpace(q.Search) == "" && q.PID == 0 && q.App == "" && q.Rule == "" {
		// Closing everything must be asked for: a request that lost its
		// limits (a misspelled key, an empty body) must not close all.
		if r.URL.Query().Get("all") != "1" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少筛选条件；关闭全部连接需要 all=1"})
			return
		}
		unlimited = true
	} else {
		targets = filterQueryRows(active, q)
	}
	result := closeConnectionsResult{Matched: len(targets)}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if unlimited {
		if _, err := client.CloseAllConnections(ctx, &emptypb.Empty{}); err != nil {
			writeRPCError(w, err)
			return
		}
		result.Closed = result.Matched
		writeJSON(w, http.StatusOK, result)
		return
	}
	var failure error
	for _, row := range targets {
		if _, err := client.CloseConnection(ctx, &daemon.CloseConnectionRequest{Id: row.Conn.ID}); err != nil {
			failure = err
			continue
		}
		result.Closed++
	}
	if failure != nil {
		writeRPCError(w, status.Errorf(status.Code(failure), "已关闭 %d / %d 条：%s", result.Closed, result.Matched, status.Convert(failure).Message()))
		return
	}
	writeJSON(w, http.StatusOK, result)
}
