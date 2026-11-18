package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeHTTPCapture struct {
	mitmapi.UnimplementedHTTPCaptureServer
	canceled chan struct{}
	cleared  atomic.Bool
	lastList atomic.Pointer[mitmapi.ListRecordsRequest]
	lastBody atomic.Pointer[mitmapi.GetBodyRequest]
	// resets answers a stale instance with a reset frame, as the core does.
	streams atomic.Int32
}

func fakeRecord(id string, seq uint64) *mitmapi.HTTPRecord {
	return &mitmapi.HTTPRecord{
		Id: id, Instance: "inst-1", Seq: seq, CreatedSeq: seq, ConnectionId: "c1", Method: "GET", Host: "example.com", Path: "/a?b=1",
		Url: "https://example.com/a?b=1", Proto: "h2", Status: 200, Interim: []int32{103},
		RequestHeaders:  []*mitmapi.HTTPHeader{{Name: "Accept", Value: "*/*"}},
		ResponseHeaders: []*mitmapi.HTTPHeader{{Name: "Content-Type", Value: "text/plain"}},
		ResponseBytes:   5, ResponseBodySaved: true, ResponseBodyStored: 5, ResponseBodyTruncated: true,
		Timing: &mitmapi.HTTPTiming{Start: 1700000000000000000, End: 1700000000500000000, Dns: 1000},
		State:  mitmapi.RecordState_RECORD_STATE_COMPLETE, Rewritten: true,
		Connection: &mitmapi.HTTPConnection{Id: "c1", ProcessName: "curl", ProcessId: 42},
	}
}

func (f *fakeHTTPCapture) GetStatus(context.Context, *emptypb.Empty) (*mitmapi.CaptureStatus, error) {
	return &mitmapi.CaptureStatus{Instance: "inst-1", Enabled: true, BodyEnabled: true, RecordCount: 2, Total: 9007199254740993}, nil
}

func (f *fakeHTTPCapture) ListRecords(_ context.Context, r *mitmapi.ListRecordsRequest) (*mitmapi.RecordList, error) {
	f.lastList.Store(r)
	return &mitmapi.RecordList{Instance: "inst-1", Records: []*mitmapi.HTTPRecord{fakeRecord("7", 7)}, NextBefore: 7, Status: &mitmapi.CaptureStatus{Instance: "inst-1"}}, nil
}

func (f *fakeHTTPCapture) GetRecord(_ context.Context, r *mitmapi.GetRecordRequest) (*mitmapi.HTTPRecord, error) {
	if r.Id == "gone" {
		return nil, status.Error(codes.NotFound, "record not found")
	}
	if r.Instance == "old" {
		return nil, status.Error(codes.FailedPrecondition, "capture instance changed")
	}
	return fakeRecord(r.Id, 7), nil
}

func (f *fakeHTTPCapture) GetBody(r *mitmapi.GetBodyRequest, stream grpc.ServerStreamingServer[mitmapi.BodyChunk]) error {
	f.lastBody.Store(r)
	if r.Id == "gone" {
		return status.Error(codes.NotFound, "record not found")
	}
	for i, part := range []string{"he", "llo"} {
		if err := stream.Send(&mitmapi.BodyChunk{Data: []byte(part), Truncated: true, Encoding: "gzip", Total: 5, OriginalSize: 9, Saved: true, Offset: int64(i * 2), DecodeError: "bad\nbyte"}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeHTTPCapture) Clear(context.Context, *emptypb.Empty) (*mitmapi.CaptureStatus, error) {
	f.cleared.Store(true)
	return &mitmapi.CaptureStatus{Instance: "inst-1"}, nil
}

func (f *fakeHTTPCapture) SubscribeEvents(r *mitmapi.SubscribeEventsRequest, stream grpc.ServerStreamingServer[mitmapi.CapturePayload]) error {
	f.streams.Add(1)
	if r.Instance == "inst-0" {
		// The previous run: answer with a reset under the new instance.
		return stream.Send(&mitmapi.CapturePayload{Instance: "inst-1", Seq: 2, Floor: 1, Reset_: true, Events: []*mitmapi.CaptureEvent{{Kind: mitmapi.EventKind_EVENT_KIND_NEW, Record: fakeRecord("1", 1)}}})
	}
	if r.Instance != "inst-1" || r.After != 9007199254740993 {
		return status.Error(codes.InvalidArgument, "resume cursor lost")
	}
	if err := stream.Send(&mitmapi.CapturePayload{Instance: "inst-1", Seq: 9007199254740994, Floor: 3, Gap: true, Events: []*mitmapi.CaptureEvent{{Kind: mitmapi.EventKind_EVENT_KIND_DONE, Record: fakeRecord("7", 7)}}, Status: &mitmapi.CaptureStatus{Instance: "inst-1", Total: 1}}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(f.canceled)
	return stream.Context().Err()
}

func startFakeHTTPCapture(t *testing.T, fake *fakeHTTPCapture) *server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := grpc.NewServer()
	t.Cleanup(rpcServer.Stop)
	mitmapi.RegisterHTTPCaptureServer(rpcServer, fake)
	go rpcServer.Serve(listener)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := newGRPCTestServer(t)
	s.rpc.httpCapture = mitmapi.NewHTTPCaptureClient(conn)
	return s
}

func httpCaptureMux(s *server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/http/capabilities", s.httpCaptureCapabilities)
	mux.HandleFunc("GET /api/v1/http/stream", s.streamHTTPCapture)
	mux.HandleFunc("GET /api/v1/http/records", s.httpCaptureRecords)
	mux.HandleFunc("GET /api/v1/http/records/{id}", s.httpCaptureRecord)
	mux.HandleFunc("GET /api/v1/http/records/{id}/body", s.httpCaptureBody)
	mux.HandleFunc("POST /api/v1/http/clear", s.clearHTTPCapture)
	return mux
}

func TestHTTPCaptureRESTBridge(t *testing.T) {
	fake := &fakeHTTPCapture{canceled: make(chan struct{})}
	s := startFakeHTTPCapture(t, fake)
	mux := httpCaptureMux(s)
	get := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}

	w := get("GET", "/api/v1/http/capabilities")
	var capabilities map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &capabilities)
	if w.Code != 200 || capabilities["supported"] != true || capabilities["enabled"] != true {
		t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/api/v1/http/records?before=bad", 400},
		{"GET", "/api/v1/http/records?limit=501", 400},
		{"GET", "/api/v1/http/records?limit=0", 400},
		{"GET", "/api/v1/http/records/gone", 404},
		{"GET", "/api/v1/http/records/7?instance=old", 503},
		{"GET", "/api/v1/http/records/7/body?side=both", 400},
		{"GET", "/api/v1/http/records/7/body?offset=-1", 400},
		{"GET", "/api/v1/http/records/7/body?decoded=maybe", 400},
		{"GET", "/api/v1/http/records/gone/body", 404},
		{"GET", "/api/v1/http/stream?after=bad", 400},
	} {
		if got := get(tc.method, tc.path); got.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.path, got.Code, got.Body.String())
		}
	}

	w = get("GET", "/api/v1/http/records?search=ex&host=example.com&connection_id=c1&limit=20&before=9")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if req := fake.lastList.Load(); req.Search != "ex" || req.Host != "example.com" || req.ConnectionId != "c1" || req.Limit != 20 || req.Before != 9 {
		t.Fatalf("list request: %v", req)
	}
	var list struct {
		Instance   string
		NextBefore string `json:"next_before"`
		Records    []map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Instance != "inst-1" || list.NextBefore != "7" || len(list.Records) != 1 {
		t.Fatalf("list body: %v %s", err, w.Body.String())
	}
	record := list.Records[0]
	if record["id"] != "7" || record["seq"] != "7" || record["state"] != "complete" || record["rewritten"] != true || record["response_body_truncated"] != true {
		t.Fatalf("record: %v", record)
	}
	if timing, _ := record["timing"].(map[string]any); timing["start"] != float64(1700000000000000000) || timing["dns"] != float64(1000) {
		t.Fatalf("timing: %v", record["timing"])
	}
	if conn, _ := record["connection"].(map[string]any); conn["process_name"] != "curl" || conn["process_id"] != float64(42) {
		t.Fatalf("connection: %v", record["connection"])
	}

	if w = get("GET", "/api/v1/http/records/7"); w.Code != 200 || !strings.Contains(w.Body.String(), `"url":"https://example.com/a?b=1"`) {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}

	w = get("GET", "/api/v1/http/records/7/body?side=response&offset=2&length=3&decoded=true")
	if w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("body: %d %q", w.Code, w.Body.String())
	}
	for key, want := range map[string]string{"X-Truncated": "true", "X-Encoding": "gzip", "X-Total": "5", "X-Original-Size": "9", "X-Saved": "true", "X-Decode-Error": "bad byte", "Content-Type": "application/octet-stream", "X-Content-Type-Options": "nosniff"} {
		if got := w.Header().Get(key); got != want {
			t.Fatalf("header %s = %q, want %q", key, got, want)
		}
	}
	if req := fake.lastBody.Load(); req.Side != mitmapi.BodySide_BODY_SIDE_RESPONSE || req.Offset != 2 || req.Length != 3 || !req.Decoded || req.Id != "7" {
		t.Fatalf("body request: %v", req)
	}

	if w = get("POST", "/api/v1/http/clear"); w.Code != 200 || !fake.cleared.Load() {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPCaptureStreamResumeAndCancel(t *testing.T) {
	fake := &fakeHTTPCapture{canceled: make(chan struct{})}
	s := startFakeHTTPCapture(t, fake)
	httpServer := httptest.NewServer(httpCaptureMux(s))
	defer httpServer.Close()

	// A stale instance gets a reset frame.
	response, err := http.Get(httpServer.URL + "/api/v1/http/stream?instance=inst-0&after=5")
	if err != nil {
		t.Fatal(err)
	}
	var reset map[string]any
	if err := json.NewDecoder(response.Body).Decode(&reset); err != nil || reset["reset"] != true || reset["instance"] != "inst-1" || reset["seq"] != "2" || reset["floor"] != "1" {
		t.Fatalf("reset frame: %v %v", reset, err)
	}
	if rest, _ := io.ReadAll(response.Body); len(rest) != 0 {
		t.Fatalf("stream did not end: %q", rest)
	}
	response.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/v1/http/stream?instance=inst-1&after=9007199254740993", nil)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("content type %q", response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() {
		t.Fatal("missing stream frame")
	}
	var frame struct {
		Seq    string
		Floor  string
		Gap    bool
		Events []struct {
			Kind   string
			Record map[string]any
		}
	}
	if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil || frame.Seq != "9007199254740994" || !frame.Gap || frame.Floor != "3" || len(frame.Events) != 1 || frame.Events[0].Kind != "done" || frame.Events[0].Record["id"] != "7" {
		t.Fatalf("frame: %v %s", err, scanner.Text())
	}
	cancel()
	select {
	case <-fake.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP cancel did not reach gRPC")
	}
}

func TestHTTPCaptureUnavailable(t *testing.T) {
	s := newGRPCTestServer(t)
	mux := httpCaptureMux(s)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/http/stream"}, {"GET", "/api/v1/http/records"}, {"GET", "/api/v1/http/records/1"},
		{"GET", "/api/v1/http/records/1/body"}, {"POST", "/api/v1/http/clear"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		var body map[string]string
		if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] == "" {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/http/capabilities", nil))
	var capabilities map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &capabilities)
	if w.Code != 200 || capabilities["enabled"] != false || capabilities["reason"] == "" {
		t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPCaptureOlderCoreCapability(t *testing.T) {
	_, address := startFakeStarted(t, "127.0.0.1:0", &fakeStarted{})
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + address, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.rpc.httpCapture = mitmapi.NewHTTPCaptureClient(conn)
	w := httptest.NewRecorder()
	s.httpCaptureCapabilities(w, httptest.NewRequest("GET", "/api/v1/http/capabilities", nil))
	var capabilities map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &capabilities)
	if w.Code != 200 || capabilities["supported"] != false {
		t.Fatalf("older core: %s", w.Body.String())
	}
}

// The core option is stripped when the probe fails and kept when it passes.
func TestInjectRuntimeStripsCaptureOnOlderCore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	section := func() map[string]any {
		return map[string]any{"mitm": map[string]any{
			"enabled": true,
			"quic":    "allow",
			"capture": map[string]any{"enabled": true, "body": false},
		}}
	}
	run := func(script string) map[string]any {
		s := &server{singBox: writeScript(t, script), dataDir: t.TempDir()}
		s.rpc.probeOnce.Do(func() {})
		out, err := s.injectRuntime(section())
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			MITM map[string]any `json:"mitm"`
		}
		if err := json.Unmarshal(out, &config); err != nil {
			t.Fatal(err)
		}
		return config.MITM
	}
	older := run("if grep -q capture \"$3\"; then exit 1; fi\nexit 0\n")
	if _, ok := older["capture"]; ok || older["quic"] != "allow" {
		t.Fatalf("older core kept capture: %v", older)
	}
	current := run("exit 0\n")
	if _, ok := current["capture"]; !ok {
		t.Fatalf("current core lost capture: %v", current)
	}
}

// http_rewrite is stripped when the probe fails and kept when it passes; the
// probe config is the rewrite key alone.
func TestInjectRuntimeStripsRewriteOnOlderCore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	run := func(script string) (map[string]any, string) {
		s := &server{singBox: writeScript(t, script), dataDir: t.TempDir(), logs: newLogBuffer(t.TempDir())}
		s.rpc.probeOnce.Do(func() {})
		out, err := s.injectRuntime(map[string]any{"mitm": map[string]any{
			"enabled":      true,
			"quic":         "allow",
			"http_rewrite": map[string]any{"url": []any{map[string]any{"pattern": "a", "action": "reject"}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			MITM map[string]any `json:"mitm"`
		}
		if err := json.Unmarshal(out, &config); err != nil {
			t.Fatal(err)
		}
		return config.MITM, strings.Join(s.logs.snapshot(), "\n")
	}
	older, log := run("if grep -q http_rewrite \"$3\"; then exit 1; fi\nexit 0\n")
	if _, ok := older["http_rewrite"]; ok || older["quic"] != "allow" {
		t.Fatalf("older core kept http_rewrite: %v", older)
	}
	if !strings.Contains(log, "HTTP 改写") {
		t.Fatalf("no strip log: %q", log)
	}
	current, _ := run("exit 0\n")
	if _, ok := current["http_rewrite"]; !ok {
		t.Fatalf("current core lost http_rewrite: %v", current)
	}
}

func TestHTTPRewriteSupportedProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	log := filepath.Join(t.TempDir(), "calls")
	s := &server{singBox: writeScript(t, "cat \"$3\" >> "+log+"\nexit 0\n"), dataDir: t.TempDir()}
	if !s.httpRewriteSupported() || !s.httpRewriteSupported() {
		t.Fatal("check success must report rewrite")
	}
	if calls, _ := os.ReadFile(log); string(calls) != `{"mitm":{"enabled":false}}`+`{"mitm":{"enabled":false,"http_rewrite":{"url":[]}}}` {
		t.Fatalf("probe configs %q", calls)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "runtime", "http-rewrite-probe.json")); !os.IsNotExist(err) {
		t.Fatalf("probe config left behind: %v", err)
	}
	older := &server{singBox: writeScript(t, "if grep -q http_rewrite \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}
	if older.httpRewriteSupported() {
		t.Fatal("older core must not report rewrite")
	}
	if (&server{}).httpRewriteSupported() {
		t.Fatal("missing binary must not report rewrite")
	}
	if httpRewriteStrip(map[string]any{"enabled": true}) {
		t.Fatal("nothing to strip")
	}
}

func TestHTTPRecordConvertsRewrites(t *testing.T) {
	r := fakeRecord("1", 1)
	r.Rewritten = true
	r.Rewrites = []*mitmapi.HTTPRewriteHit{
		{Kind: "url", Rule: "u1", Action: "302", Target: "https://b.com/"},
		{Kind: "header", Rule: "h1", Action: "replace", Side: "request", Changes: []*mitmapi.HTTPRewriteChange{
			{Op: "replace", Name: "X-A", Before: "1", HadBefore: true, After: "2", HasAfter: true},
		}},
		{Kind: "map_local", Rule: "m1", Status: 404},
	}
	data, err := json.Marshal(httpRecord(r))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rewritten bool `json:"rewritten"`
		Rewrites  []struct {
			Kind    string `json:"kind"`
			Target  string `json:"target"`
			Status  int    `json:"status"`
			Side    string `json:"side"`
			Changes []struct {
				Op        string `json:"op"`
				Before    string `json:"before"`
				HadBefore bool   `json:"had_before"`
				After     string `json:"after"`
				HasAfter  bool   `json:"has_after"`
			} `json:"changes"`
		} `json:"rewrites"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Rewritten || len(got.Rewrites) != 3 || got.Rewrites[0].Target != "https://b.com/" || got.Rewrites[2].Status != 404 || got.Rewrites[1].Side != "request" {
		t.Fatalf("rewrites: %s", data)
	}
	if c := got.Rewrites[1].Changes; len(c) != 1 || c[0].Op != "replace" || !c[0].HadBefore || c[0].Before != "1" || !c[0].HasAfter || c[0].After != "2" {
		t.Fatalf("changes: %s", data)
	}
	if data, _ := json.Marshal(httpRecord(fakeRecord("2", 2))); strings.Contains(string(data), "rewrites") {
		t.Fatalf("empty rewrites serialized: %s", data)
	}
}

// body, max_body_size and "header" URL rules are stripped when only the
// body probe fails; the rest of http_rewrite stays.
func TestInjectRuntimeStripsRewriteBodyOnOlderCore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	run := func(script string) (map[string]any, string) {
		s := &server{singBox: writeScript(t, script), dataDir: t.TempDir(), logs: newLogBuffer(t.TempDir())}
		s.rpc.probeOnce.Do(func() {})
		out, err := s.injectRuntime(map[string]any{"mitm": map[string]any{
			"enabled": true,
			"http_rewrite": map[string]any{
				"url": []any{
					map[string]any{"pattern": "a", "action": "reject"},
					map[string]any{"pattern": "b", "action": "header", "target": "https://b.com"},
				},
				"body":          []any{map[string]any{"pattern": "a", "scope": "response"}},
				"max_body_size": "4096KiB",
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			MITM map[string]any `json:"mitm"`
		}
		if err := json.Unmarshal(out, &config); err != nil {
			t.Fatal(err)
		}
		return config.MITM, strings.Join(s.logs.snapshot(), "\n")
	}
	older, log := run("if grep -q '\"body\"' \"$3\"; then exit 1; fi\nexit 0\n")
	rewrite, _ := older["http_rewrite"].(map[string]any)
	urls, _ := rewrite["url"].([]any)
	if rewrite == nil || rewrite["body"] != nil || rewrite["max_body_size"] != nil || len(urls) != 1 || urls[0].(map[string]any)["action"] != "reject" {
		t.Fatalf("older core http_rewrite = %v", rewrite)
	}
	if !strings.Contains(log, "正文改写") || !strings.Contains(log, "内部改写") {
		t.Fatalf("no strip log: %q", log)
	}
	current, _ := run("exit 0\n")
	rewrite, _ = current["http_rewrite"].(map[string]any)
	if rewrite["body"] == nil || rewrite["max_body_size"] == nil || len(rewrite["url"].([]any)) != 2 {
		t.Fatalf("current core lost options: %v", rewrite)
	}
}

func TestHTTPRewriteBodyProbeAndStrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	log := filepath.Join(t.TempDir(), "calls")
	s := &server{singBox: writeScript(t, "cat \"$3\" >> "+log+"\nexit 0\n"), dataDir: t.TempDir()}
	if !s.httpRewriteBodySupported() || !s.httpRewriteBodySupported() {
		t.Fatal("check success must report rewrite body")
	}
	if calls, _ := os.ReadFile(log); string(calls) != `{"mitm":{"enabled":false}}`+`{"mitm":{"enabled":false,"http_rewrite":{"url":[]}}}`+`{"mitm":{"enabled":false,"http_rewrite":{"body":[]}}}` {
		t.Fatalf("probe configs %q", calls)
	}
	older := &server{singBox: writeScript(t, "if grep -q '\"body\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}
	if older.httpRewriteBodySupported() || !older.httpRewriteSupported() {
		t.Fatal("older core: rewrite yes, body no")
	}
	if (&server{}).httpRewriteBodySupported() {
		t.Fatal("missing binary must not report rewrite body")
	}
	// Only header rules: http_rewrite disappears with them.
	section := map[string]any{"http_rewrite": map[string]any{"url": []any{map[string]any{"action": "header"}}}}
	if got := httpRewriteBodyStrip(section); len(got) != 1 || section["http_rewrite"] != nil {
		t.Fatalf("strip = %v %v", got, section)
	}
	if got := httpRewriteBodyStrip(map[string]any{"http_rewrite": map[string]any{"url": []any{map[string]any{"action": "reject"}}}}); got != nil {
		t.Fatalf("nothing to strip, got %v", got)
	}
}
