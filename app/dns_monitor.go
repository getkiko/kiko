package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	dnsmonitorapi "github.com/sagernet/sing-box/common/dnsmonitor/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *server) dnsMonitorSupported() bool {
	s.rpc.dnsProbeOnce.Do(func() {
		if s.singBox == "" || s.dataDir == "" {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "dns-monitor-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(`{"experimental":{"dns_monitor":{"enabled":true}}}`), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.rpc.dnsSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.rpc.dnsSupported
}

func (s *server) injectDNSMonitor(experimental map[string]any) {
	if !s.dnsMonitorSupported() {
		return
	}
	options, _ := experimental["dns_monitor"].(map[string]any)
	if options == nil {
		options = map[string]any{}
		experimental["dns_monitor"] = options
	}
	options["enabled"] = true
	for key, value := range map[string]any{"history_entries": 5000, "history_bytes": 32 << 20, "history_age": "10m", "capture_wire": false} {
		if _, ok := options[key]; !ok {
			options[key] = value
		}
	}
}

func (s *server) dnsClient() dnsmonitorapi.DNSMonitorClient {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	return s.rpc.dns
}

func (s *server) requireDNSClient(w http.ResponseWriter) dnsmonitorapi.DNSMonitorClient {
	client := s.dnsClient()
	if client == nil {
		writeJSON(w, 503, map[string]string{"error": "DNS 监控等待核心连接"})
	}
	return client
}

func (s *server) dnsCapabilities(w http.ResponseWriter, r *http.Request) {
	client := s.dnsClient()
	if client == nil {
		writeJSON(w, 200, map[string]any{"supported": s.dnsMonitorSupported(), "enabled": false, "reason": "等待核心运行并建立控制连接"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cap, err := client.GetCapabilities(ctx, &emptypb.Empty{})
	if status.Code(err) == codes.Unimplemented {
		writeJSON(w, 200, map[string]any{"supported": false, "enabled": false, "reason": "当前核心需要 DNS 监控扩展"})
		return
	}
	if err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"supported": true, "enabled": cap.Enabled, "schema_version": cap.SchemaVersion, "features": cap.Features, "limitations": cap.Limitations})
}

func cursorParam(w http.ResponseWriter, r *http.Request, key string) (uint64, bool) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return 0, true
	}
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "无效游标: " + key})
		return 0, false
	}
	return number, true
}

func dnsJSON(w http.ResponseWriter, p *dnsmonitorapi.Payload, err error) {
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(p.Json)
}

func (s *server) dnsQueries(w http.ResponseWriter, r *http.Request) {
	client := s.requireDNSClient(w)
	if client == nil {
		return
	}
	before, ok := cursorParam(w, r, "before")
	if !ok {
		return
	}
	limit := uint64(100)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 || n > 500 {
			writeJSON(w, 400, map[string]string{"error": "limit 范围为 1–500"})
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := client.ListQueries(ctx, &dnsmonitorapi.ListRequest{Search: r.URL.Query().Get("search"), Status: r.URL.Query().Get("status"), Resolver: r.URL.Query().Get("resolver"), Limit: uint32(limit), Before: before}, grpc.MaxCallRecvMsgSize(16<<20))
	dnsJSON(w, p, err)
}

func (s *server) dnsQuery(w http.ResponseWriter, r *http.Request) {
	client := s.requireDNSClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := client.GetQuery(ctx, &dnsmonitorapi.GetRequest{Id: r.PathValue("id"), Instance: r.URL.Query().Get("instance")}, grpc.MaxCallRecvMsgSize(16<<20))
	dnsJSON(w, p, err)
}

func (s *server) clearDNSHistory(w http.ResponseWriter, r *http.Request) {
	client := s.requireDNSClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := client.ClearHistory(ctx, &emptypb.Empty{}, grpc.MaxCallRecvMsgSize(16<<20))
	dnsJSON(w, p, err)
}

func (s *server) streamDNS(w http.ResponseWriter, r *http.Request) {
	client := s.requireDNSClient(w)
	if client == nil {
		return
	}
	after, ok := cursorParam(w, r, "after")
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := client.SubscribeEvents(ctx, &dnsmonitorapi.SubscribeRequest{Instance: r.URL.Query().Get("instance"), After: after}, grpc.MaxCallRecvMsgSize(16<<20))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	started := false
	defer controller.SetWriteDeadline(time.Time{})
	for {
		p, recvErr := stream.Recv()
		if recvErr != nil {
			if !started && ctx.Err() == nil && recvErr != io.EOF {
				writeRPCError(w, recvErr)
			}
			return
		}
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = w.Write(append(p.Json, '\n')); err != nil {
			return
		}
		if controller.Flush() != nil {
			return
		}
	}
}
