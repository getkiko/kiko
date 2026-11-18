package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

type fakeScript struct {
	mitmapi.UnimplementedScriptServer
	evaluate atomic.Pointer[mitmapi.ScriptEvaluateRequest]
	run      atomic.Pointer[mitmapi.ScriptRunRequest]
	logsReq  atomic.Pointer[mitmapi.ScriptSubscribeRequest]
	set      atomic.Pointer[mitmapi.ScriptStoreSetRequest]
	deleted  atomic.Pointer[mitmapi.ScriptStoreKey]
	canceled chan struct{}
}

func (f *fakeScript) Evaluate(_ context.Context, r *mitmapi.ScriptEvaluateRequest) (*mitmapi.ScriptEvaluateResponse, error) {
	f.evaluate.Store(r)
	if r.Source == "bad" {
		return nil, status.Error(codes.InvalidArgument, "source is empty")
	}
	return &mitmapi.ScriptEvaluateResponse{
		Action: "respond", DurationUs: 1500, Url: "https://b.test/", HeadersChanged: true,
		Headers: []*mitmapi.ScriptHeader{{Name: "X-A", Value: "1"}}, BodyChanged: true, Body: []byte("new"), Status: 201,
		Logs:          []*mitmapi.ScriptLogLine{{Level: "info", Message: "hi", TimeMs: 5}},
		Notifications: []*mitmapi.ScriptNotification{{Title: "t", Body: "b", TimeMs: 6}},
		Changes:       []*mitmapi.ScriptChange{{Kind: "header", Op: "add", Name: "X-A", After: "1", HasAfter: true}},
		Response:      &mitmapi.ScriptResponseMessage{Status: 404, HasBody: true, Body: []byte("gone")},
		Output:        `{"ok":true}`,
		Decision:      "true",
	}, nil
}

func (f *fakeScript) Run(_ context.Context, r *mitmapi.ScriptRunRequest) (*mitmapi.ScriptRunResponse, error) {
	f.run.Store(r)
	if r.Tag == "missing" {
		return nil, status.Error(codes.NotFound, "no such script")
	}
	return &mitmapi.ScriptRunResponse{Action: "none", DurationUs: 9, Logs: []*mitmapi.ScriptLogLine{{Level: "log", Message: "x"}}, Output: "1"}, nil
}

func (f *fakeScript) GetStatus(context.Context, *emptypb.Empty) (*mitmapi.ScriptStatusList, error) {
	return &mitmapi.ScriptStatusList{
		Scripts:         []*mitmapi.ScriptState{{Tag: "a", Type: "generic", Runs: 9007199254740993, Errors: 2, Paused: true, PausedUntil: 100, LastError: "boom", LastRunMs: 7, LastDurationUs: 8}, {Tag: "tick", Type: "cron", NextRunMs: 1700000060000}},
		StorePersistent: true, StoreBytes: 10, StoreLimit: 16 << 20,
	}, nil
}

func (f *fakeScript) SubscribeLogs(r *mitmapi.ScriptSubscribeRequest, stream grpc.ServerStreamingServer[mitmapi.ScriptLogEntry]) error {
	f.logsReq.Store(r)
	if err := stream.Send(&mitmapi.ScriptLogEntry{Seq: 9007199254740994, Script: "a", Phase: "http-response", Level: "warn", Message: "m", TimeMs: 3}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(f.canceled)
	return stream.Context().Err()
}

func (f *fakeScript) SubscribeNotifications(r *mitmapi.ScriptSubscribeRequest, stream grpc.ServerStreamingServer[mitmapi.ScriptNotification]) error {
	if r.After == 99 {
		return status.Error(codes.FailedPrecondition, "HTTPS 解密未运行")
	}
	return stream.Send(&mitmapi.ScriptNotification{Seq: 4, Script: "a", Title: "T", Subtitle: "S", Body: "B", Url: "https://x", TimeMs: 1})
}

func (f *fakeScript) StoreList(_ context.Context, r *mitmapi.ScriptStoreListRequest) (*mitmapi.ScriptStoreListResponse, error) {
	info := &mitmapi.ScriptStoreInfo{Persistent: true, Bytes: 5, Limit: 100}
	if r.Namespace == "" {
		return &mitmapi.ScriptStoreListResponse{Namespaces: []*mitmapi.ScriptStoreNamespace{{Name: "a", Entries: 1, Bytes: 5}}, Info: info}, nil
	}
	return &mitmapi.ScriptStoreListResponse{Entries: []*mitmapi.ScriptStoreListEntry{{Key: "k", Value: "v", Size: 9000, Truncated: true}}, Info: info}, nil
}

func (f *fakeScript) StoreGet(_ context.Context, k *mitmapi.ScriptStoreKey) (*mitmapi.ScriptStoreValue, error) {
	return &mitmapi.ScriptStoreValue{Value: k.Namespace + "/" + k.Key, Found: k.Key != "none"}, nil
}

func (f *fakeScript) StoreSet(_ context.Context, r *mitmapi.ScriptStoreSetRequest) (*mitmapi.ScriptStoreInfo, error) {
	f.set.Store(r)
	return &mitmapi.ScriptStoreInfo{Persistent: false, Bytes: 7, Limit: 100}, nil
}

func (f *fakeScript) StoreDelete(_ context.Context, k *mitmapi.ScriptStoreKey) (*mitmapi.ScriptStoreInfo, error) {
	f.deleted.Store(k)
	return &mitmapi.ScriptStoreInfo{Bytes: 0, Limit: 100}, nil
}

func startFakeScript(t *testing.T, fake *fakeScript) *server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := grpc.NewServer()
	t.Cleanup(rpcServer.Stop)
	mitmapi.RegisterScriptServer(rpcServer, fake)
	go rpcServer.Serve(listener)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := newGRPCTestServer(t)
	s.rpc.script = mitmapi.NewScriptClient(conn)
	return s
}

func scriptMux(s *server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/scripts/status", s.scriptStatus)
	mux.HandleFunc("POST /api/v1/scripts/evaluate", s.scriptEvaluate)
	mux.HandleFunc("POST /api/v1/scripts/run", s.scriptRun)
	mux.HandleFunc("GET /api/v1/scripts/logs", s.scriptLogs)
	mux.HandleFunc("GET /api/v1/scripts/notifications", s.scriptNotifications)
	mux.HandleFunc("GET /api/v1/scripts/store", s.scriptStoreList)
	mux.HandleFunc("GET /api/v1/scripts/store/value", s.scriptStoreGet)
	mux.HandleFunc("PUT /api/v1/scripts/store/value", s.scriptStoreSet)
	mux.HandleFunc("DELETE /api/v1/scripts/store/value", s.scriptStoreDelete)
	return mux
}

func TestScriptRESTBridge(t *testing.T) {
	fake := &fakeScript{canceled: make(chan struct{})}
	s := startFakeScript(t, fake)
	mux := scriptMux(s)
	call := func(method, path, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Status: counters are strings.
	code, st := call("GET", "/api/v1/scripts/status", "")
	scripts, _ := st["scripts"].([]any)
	if code != 200 || len(scripts) != 2 || st["store_persistent"] != true || st["store_limit"] != "16777216" {
		t.Fatalf("status: %d %v", code, st)
	}
	if first := scripts[0].(map[string]any); first["runs"] != "9007199254740993" || first["paused"] != true || first["last_error"] != "boom" || first["paused_until"] != float64(100) {
		t.Fatalf("state: %v", first)
	}

	if tick := scripts[1].(map[string]any); tick["type"] != "cron" || tick["next_run_ms"] != float64(1700000060000) || scripts[0].(map[string]any)["next_run_ms"] != nil {
		t.Fatalf("cron state: %v", tick)
	}

	// Evaluate: request body is base64, the reply carries every part.
	body := `{"source":"$done({})","type":"http-response","pattern":"^x","argument":"a=1","binary_body":true,"requires_body":true,"timeout_ms":500,"max_size":4096,"tag":"t",` +
		`"request":{"url":"https://a.test/","method":"GET","headers":[{"name":"H","value":"v"}]},` +
		`"response":{"status":200,"headers":[{"name":"C","value":"d"}],"body":"` + base64.StdEncoding.EncodeToString([]byte("old")) + `","has_body":true},` +
		`"store":[{"key":"k","value":"v"}]}`
	code, ev := call("POST", "/api/v1/scripts/evaluate", body)
	if code != 200 || ev["action"] != "respond" || ev["duration_us"] != float64(1500) || ev["url"] != "https://b.test/" || ev["status"] != float64(201) || ev["output"] != `{"ok":true}` {
		t.Fatalf("evaluate: %d %v", code, ev)
	}
	if ev["body"] != base64.StdEncoding.EncodeToString([]byte("new")) || ev["headers_changed"] != true || ev["body_changed"] != true {
		t.Fatalf("evaluate body: %v", ev)
	}
	if resp := ev["response"].(map[string]any); resp["status"] != float64(404) || resp["body"] != base64.StdEncoding.EncodeToString([]byte("gone")) {
		t.Fatalf("response: %v", resp)
	}
	if logs := ev["logs"].([]any); logs[0].(map[string]any)["message"] != "hi" || ev["notifications"].([]any)[0].(map[string]any)["title"] != "t" || ev["changes"].([]any)[0].(map[string]any)["op"] != "add" {
		t.Fatalf("evaluate lists: %v", ev)
	}
	req := fake.evaluate.Load()
	if req.Source != "$done({})" || req.Type != "http-response" || req.Pattern != "^x" || req.Argument != "a=1" || !req.BinaryBody || !req.RequiresBody || req.TimeoutMs != 500 || req.MaxSize != 4096 || req.Tag != "t" ||
		req.Request.Url != "https://a.test/" || req.Request.Headers[0].Name != "H" || string(req.Response.Body) != "old" || !req.Response.HasBody || req.Response.Status != 200 || req.Store[0].Key != "k" {
		t.Fatalf("evaluate request: %v", req)
	}
	// Rule and dns scripts: the $request input goes through, the decision comes back.
	code, ev = call("POST", "/api/v1/scripts/evaluate", `{"source":"$done(true)","type":"rule","timeout_ms":100,"rule":{"domain":"a.test","ip":"192.0.2.1","port":443,"network":"tcp","protocol":"tls","inbound":"tun","inbound_type":"tun","source_ip":"10.0.0.2","process_name":"curl","process_path":"/usr/bin/curl","user":"u","query_type":"AAAA"}}`)
	if rule := fake.evaluate.Load().Rule; code != 200 || ev["decision"] != "true" || rule == nil || rule.Domain != "a.test" || rule.Ip != "192.0.2.1" || rule.Port != 443 || rule.InboundType != "tun" ||
		rule.SourceIp != "10.0.0.2" || rule.ProcessName != "curl" || rule.ProcessPath != "/usr/bin/curl" || rule.User != "u" || rule.QueryType != "AAAA" || rule.Protocol != "tls" || rule.Network != "tcp" || rule.Inbound != "tun" {
		t.Fatalf("rule evaluate: %d %v %v", code, ev, fake.evaluate.Load())
	}
	if code, out := call("POST", "/api/v1/scripts/evaluate", `{"source":"bad","type":"generic"}`); code != 400 || out["error"] != "source is empty" {
		t.Fatalf("evaluate error: %d %v", code, out)
	}
	if code, _ := call("POST", "/api/v1/scripts/evaluate", `{"source":"x","response":{"body":"%%%"}}`); code != 400 {
		t.Fatalf("bad base64: %d", code)
	}

	// Run.
	code, run := call("POST", "/api/v1/scripts/run", `{"tag":"daily","argument":"x"}`)
	if code != 200 || run["action"] != "none" || run["output"] != "1" || run["duration_us"] != float64(9) || fake.run.Load().Tag != "daily" || fake.run.Load().Argument != "x" {
		t.Fatalf("run: %d %v", code, run)
	}
	if code, run := call("POST", "/api/v1/scripts/run", `{"tag":"tick"}`); code != 200 || run["action"] != "none" || fake.run.Load().Tag != "tick" {
		t.Fatalf("run cron: %d %v", code, run)
	}
	if code, _ := call("POST", "/api/v1/scripts/evaluate", `{"source":"x","type":"cron"}`); code != 200 || fake.evaluate.Load().Type != "cron" {
		t.Fatalf("evaluate cron: %d", code)
	}
	if code, _ := call("POST", "/api/v1/scripts/run", `{"tag":"missing"}`); code != 404 {
		t.Fatalf("run missing: %d", code)
	}
	if code, _ := call("POST", "/api/v1/scripts/run", `{}`); code != 400 {
		t.Fatalf("run without tag: %d", code)
	}

	// Store.
	code, ns := call("GET", "/api/v1/scripts/store", "")
	if code != 200 || ns["namespaces"].([]any)[0].(map[string]any)["bytes"] != "5" || ns["info"].(map[string]any)["limit"] != "100" {
		t.Fatalf("store namespaces: %d %v", code, ns)
	}
	code, entries := call("GET", "/api/v1/scripts/store?namespace=a", "")
	if first := entries["entries"].([]any)[0].(map[string]any); code != 200 || first["truncated"] != true || first["size"] != float64(9000) {
		t.Fatalf("store entries: %d %v", code, entries)
	}
	code, got := call("GET", "/api/v1/scripts/store/value?namespace=a&key=k", "")
	if code != 200 || got["value"] != "a/k" || got["found"] != true {
		t.Fatalf("store get: %d %v", code, got)
	}
	if code, _ := call("GET", "/api/v1/scripts/store/value?namespace=a", ""); code != 400 {
		t.Fatalf("store get without key: %d", code)
	}
	code, info := call("PUT", "/api/v1/scripts/store/value", `{"namespace":"@shared","key":"k","value":"v"}`)
	if set := fake.set.Load(); code != 200 || info["bytes"] != "7" || info["persistent"] != false || set.Namespace != "@shared" || set.Value != "v" {
		t.Fatalf("store set: %d %v", code, info)
	}
	if code, _ := call("PUT", "/api/v1/scripts/store/value", `{"key":"k"}`); code != 400 {
		t.Fatalf("store set without namespace: %d", code)
	}
	if code, _ := call("DELETE", "/api/v1/scripts/store/value?namespace=a&key=k", ""); code != 200 || fake.deleted.Load().Key != "k" {
		t.Fatal("store delete")
	}
}

func TestScriptRESTStreams(t *testing.T) {
	fake := &fakeScript{canceled: make(chan struct{})}
	s := startFakeScript(t, fake)
	httpServer := httptest.NewServer(scriptMux(s))
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/v1/scripts/logs?after=9007199254740993&tag=a", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("content type %q", response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() {
		t.Fatal("missing log line")
	}
	var line map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &line); err != nil || line["seq"] != "9007199254740994" || line["script"] != "a" || line["phase"] != "http-response" || line["level"] != "warn" || line["message"] != "m" {
		t.Fatalf("log line: %v %s", err, scanner.Text())
	}
	if r := fake.logsReq.Load(); r.After != 9007199254740993 || r.Tag != "a" {
		t.Fatalf("log request: %v", r)
	}
	cancel()
	select {
	case <-fake.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP cancel did not reach gRPC")
	}

	response, err = http.Get(httpServer.URL + "/api/v1/scripts/notifications")
	if err != nil {
		t.Fatal(err)
	}
	var note map[string]any
	if err := json.NewDecoder(response.Body).Decode(&note); err != nil || note["seq"] != "4" || note["title"] != "T" || note["subtitle"] != "S" || note["url"] != "https://x" || note["script"] != "a" {
		t.Fatalf("notification: %v %v", note, err)
	}
	response.Body.Close()

	// A stream that fails before its first entry answers with the error.
	response, err = http.Get(httpServer.URL + "/api/v1/scripts/notifications?after=99")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("failed stream: %d", response.StatusCode)
	}
	if response, _ = http.Get(httpServer.URL + "/api/v1/scripts/logs?after=x"); response.StatusCode != 400 {
		t.Fatalf("bad cursor: %d", response.StatusCode)
	}
}

func TestScriptRESTUnavailable(t *testing.T) {
	s := newGRPCTestServer(t)
	mux := scriptMux(s)
	for _, route := range [][2]string{{"GET", "/api/v1/scripts/status"}, {"POST", "/api/v1/scripts/evaluate"}, {"POST", "/api/v1/scripts/run"}, {"GET", "/api/v1/scripts/logs"},
		{"GET", "/api/v1/scripts/notifications"}, {"GET", "/api/v1/scripts/store"}, {"GET", "/api/v1/scripts/store/value"}, {"PUT", "/api/v1/scripts/store/value"}, {"DELETE", "/api/v1/scripts/store/value"}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(route[0], route[1], strings.NewReader("{}")))
		if w.Code != 503 {
			t.Errorf("%s %s: %d", route[0], route[1], w.Code)
		}
	}
}

func TestHTTPRewriteRunMapping(t *testing.T) {
	hits := httpRewrites([]*mitmapi.HTTPRewriteHit{{Kind: "script", Rule: "a", Action: "skipped", Side: "response", Target: "a", Run: &mitmapi.HTTPScriptRun{
		Tag: "a", Phase: "http-response", DurationUs: 1200, Error: "boom", Reason: "size", Logs: []*mitmapi.HTTPScriptLog{{Level: "warn", Message: "w"}}}}})
	data, _ := json.Marshal(hits)
	want := `[{"kind":"script","rule":"a","action":"skipped","side":"response","target":"a","run":{"tag":"a","phase":"http-response","duration_us":1200,"error":"boom","reason":"size","logs":[{"level":"warn","message":"w"}]}}]`
	if string(data) != want {
		t.Fatalf("got %s", data)
	}
	if data, _ := json.Marshal(httpRewrites([]*mitmapi.HTTPRewriteHit{{Kind: "url"}})); strings.Contains(string(data), "run") {
		t.Fatalf("run serialized for a rule hit: %s", data)
	}
}
