package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLatencyTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 unavailable")
	}
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	result := testTarget(latencyTarget{ID: "ipv6", Host: "::1", Port: port})
	if result.Success != 3 || result.Error != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.OneWayMS != result.MedianMS/2 {
		t.Fatalf("one-way estimate mismatch: %+v", result)
	}
	if result.Mode != "direct" {
		t.Fatalf("a TCP probe must be marked direct: %+v", result)
	}
}

func TestRuleSetStatusProxiesRuntimeTimestamp(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rulesets" || r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Errorf("unexpected runtime request: path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ruleSets":[{"tag":"geosite-cn","lastUpdatedAt":1792720440000}]}`)
	}))
	defer upstream.Close()
	s := &server{
		state:       runtimeState{State: "running"},
		cmd:         &exec.Cmd{},
		clashAddr:   strings.TrimPrefix(upstream.URL, "http://"),
		clashSecret: "runtime-secret",
	}
	recorder := httptest.NewRecorder()
	s.ruleSetStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/rulesets/status", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"lastUpdatedAt":1792720440000`) {
		t.Fatalf("unexpected rule-set status response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestStopCancelsPendingRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-kiko-core")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = check ]; then exit 0; fi\ntouch cache.db\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(binary, filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(s.dataDir, "runtime", "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.snapshot().State == "degraded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.snapshot().State != "degraded" {
		t.Fatalf("process did not enter restart wait: %+v", s.snapshot())
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "cache.db")); err != nil {
		t.Fatalf("relative core files must be created in data dir: %v", err)
	}
	s.stop(true)
	time.Sleep(1200 * time.Millisecond)
	got := s.snapshot()
	if got.State != "stopped" || got.PID != 0 || got.DesiredRunning || got.RestartIn != 0 {
		t.Fatalf("stop allowed pending restart: %+v", got)
	}
}

func TestRuntimeStopRespondsBeforeProcessExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "slow-kiko-core")
	script := "#!/bin/sh\nif [ \"$1\" = check ]; then exit 0; fi\ntrap 'sleep 1; exit 0' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(binary, filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(filepath.Join(s.dataDir, "runtime", "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = s.start(); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/runtime/stop", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	s.routes().ServeHTTP(w, r)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("stop response waited for process exit: %v", elapsed)
	}
	if w.Code != http.StatusOK || s.snapshot().State != "stopping" {
		t.Fatalf("status=%d body=%s runtime=%+v", w.Code, w.Body.String(), s.snapshot())
	}
	if err = s.start(); err == nil || !strings.Contains(err.Error(), "正在停止") {
		t.Fatalf("start during stop error=%v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.snapshot().State != "stopped" {
		time.Sleep(20 * time.Millisecond)
	}
	got := s.snapshot()
	if got.State != "stopped" || got.PID != 0 || got.DesiredRunning {
		t.Fatalf("asynchronous stop did not finish: %+v", got)
	}
}

func TestShutdownJoinsPendingAsynchronousStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "slow-kiko-core")
	script := "#!/bin/sh\nif [ \"$1\" = check ]; then exit 0; fi\ntrap 'sleep 1; exit 0' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(binary, filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(filepath.Join(s.dataDir, "runtime", "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = s.start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	s.stopAsync(true)
	started := time.Now()
	s.shutdown()
	if elapsed := time.Since(started); elapsed < 500*time.Millisecond {
		t.Fatalf("shutdown returned before pending stop completed: %v", elapsed)
	}
	got := s.snapshot()
	if got.State != "stopped" || got.PID != 0 {
		t.Fatalf("shutdown left the core running: %+v", got)
	}
}

func TestStartRejectsFailedManagedProcess(t *testing.T) {
	s := &server{
		cmd:   &exec.Cmd{},
		state: runtimeState{State: "failed"},
	}
	err := s.start()
	if err == nil || !strings.Contains(err.Error(), "进程仍在运行") {
		t.Fatalf("start error=%v", err)
	}
}

func TestAutoStartWaitsForConfigAndStopCancelsRetry(t *testing.T) {
	s, err := newServer("/unused/kiko-core", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.enableAutoStart(); err != nil {
		t.Fatal(err)
	}
	if err := s.start(); err == nil {
		t.Fatal("missing configuration should delay startup")
	}
	got := s.snapshot()
	if got.State != "degraded" || !got.DesiredRunning || got.RestartIn == 0 {
		t.Fatalf("auto-start did not schedule a retry: %+v", got)
	}
	s.stop(true)
	s.restartAfter(s.restartGeneration)
	got = s.snapshot()
	if got.State != "stopped" || got.DesiredRunning || got.RestartIn != 0 {
		t.Fatalf("manual stop did not cancel retry: %+v", got)
	}
	reloaded, err := newServer("/unused/kiko-core", s.dataDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.persisted.DesiredRunning {
		t.Fatal("manual stop was not persisted")
	}
}

func TestAutoStartRetryDelayCapsAtThirtySeconds(t *testing.T) {
	s, err := newServer("/unused/kiko-core", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.enableAutoStart(); err != nil {
		t.Fatal(err)
	}
	defer s.stop(true)
	for range 40 {
		s.mu.Lock()
		s.scheduleRetryLocked("startup failed")
		s.mu.Unlock()
	}
	if got := s.snapshot(); got.RestartIn != 30 || got.State != "degraded" || !got.DesiredRunning {
		t.Fatalf("retry backoff did not cap at 30 seconds: %+v", got)
	}
}

func TestAutoStartRetriesFailedCheckUntilConfigIsValid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	t.Setenv("KIKO_FAKE_READY", ready)
	binary := filepath.Join(dir, "fake-kiko-core")
	content := "#!/bin/sh\nif [ \"$1\" = check ]; then\n  test -f \"$KIKO_FAKE_READY\"\n  exit $?\nfi\nexec sleep 30\n"
	if err := os.WriteFile(binary, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(binary, filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(s.dataDir, "runtime", "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.enableAutoStart(); err != nil {
		t.Fatal(err)
	}
	defer s.stop(true)
	if err := s.start(); err == nil {
		t.Fatal("invalid configuration should delay startup")
	}
	if got := s.snapshot(); got.State != "degraded" || !got.DesiredRunning {
		t.Fatalf("failed check cleared startup intent: %+v", got)
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.snapshot(); got.State == "running" && got.PID > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("auto-start did not recover after configuration became valid: %+v", s.snapshot())
}

func TestAutoStartRetriesFatalProcessExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-kiko-core")
	content := "#!/bin/sh\nif [ \"$1\" = check ]; then exit 0; fi\necho 'FATAL[0000] startup failed'\nexit 1\n"
	if err := os.WriteFile(binary, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(binary, filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(s.dataDir, "runtime", "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.enableAutoStart(); err != nil {
		t.Fatal(err)
	}
	defer s.stop(true)
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.snapshot(); got.State == "degraded" && got.DesiredRunning && got.RestartIn > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fatal exit did not schedule retry: %+v", s.snapshot())
}

func TestEmptyLogsAreJSONArray(t *testing.T) {
	s := newLogBuffer(t.TempDir())
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, map[string]any{"lines": s.snapshot()})
	if recorder.Body.String() != "{\"lines\":[]}\n" {
		t.Fatalf("unexpected empty logs response: %s", recorder.Body.String())
	}
}

func TestFetchForwardsSubscriptionHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "clash-verge/v2.5.4" {
			t.Errorf("User-Agent = %q, want clash-verge/v2.5.4", got)
		}
		w.Header().Set("X-Amz-Meta-Subscription-Userinfo", "upload=1024; download=2048; total=4096; expire=1780000000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxies: []\n"))
	}))
	defer upstream.Close()

	s := &server{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetch", strings.NewReader(`{"url":"`+upstream.URL+`","headers":{"User-Agent":"clash-verge/v2.5.4"}}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.fetch(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		SubscriptionUserinfo string `json:"subscriptionUserinfo"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SubscriptionUserinfo != "upload=1024; download=2048; total=4096; expire=1780000000" {
		t.Fatalf("unexpected subscription userinfo: %q", result.SubscriptionUserinfo)
	}
}

func TestFetchDropsCustomHeadersOnCrossOriginRedirect(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Config-Key"); got != "" {
			t.Errorf("redirect forwarded X-Config-Key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("redirect forwarded Authorization = %q", got)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Config-Key"); got != "secret" {
			t.Errorf("initial X-Config-Key = %q", got)
		}
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer origin.Close()

	payload, err := json.Marshal(map[string]any{"url": origin.URL, "headers": map[string]string{
		"X-Config-Key": "secret", "Authorization": "Bearer secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetch", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	(&server{}).fetch(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestFetchKeepsCustomHeadersOnSameOriginRedirect(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/finish", http.StatusFound)
			return
		}
		if got := r.Header.Get("X-Config-Key"); got != "secret" {
			t.Errorf("redirect X-Config-Key = %q", got)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer origin.Close()

	payload, err := json.Marshal(map[string]any{"url": origin.URL + "/start", "headers": map[string]string{"X-Config-Key": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetch", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	(&server{}).fetch(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCollectSingBoxMetrics(t *testing.T) {
	samples := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" || r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "invalid request", http.StatusUnauthorized)
			return
		}
		samples++
		if samples == 1 {
			_, _ = w.Write([]byte(`{"memory":123456,"uploadTotal":2048,"downloadTotal":8192,"connections":[{},{}]}`))
		} else {
			_, _ = w.Write([]byte(`{"memory":123456,"uploadTotal":3048,"downloadTotal":10192,"connections":[{},{}]}`))
		}
	}))
	defer api.Close()
	s := &server{state: runtimeState{State: "running"}, cmd: &exec.Cmd{}, clashAddr: strings.TrimPrefix(api.URL, "http://"), clashSecret: "test-secret"}
	s.collectSingBoxMetrics()
	got := s.snapshot()
	if !got.MetricsAvailable || got.MemoryBytes != 123456 || got.ActiveConnections != 2 || got.UploadTotal != 2048 || got.DownloadTotal != 8192 {
		t.Fatalf("unexpected metrics: %+v", got)
	}
	s.metricsSampleAt = time.Now().Add(-time.Second)
	s.metricsUploadTotal = 2048
	s.metricsDownloadTotal = 8192
	s.collectSingBoxMetrics()
	got = s.snapshot()
	if got.UploadTotal != 3048 || got.DownloadTotal != 10192 || got.UploadRate < 900 || got.UploadRate > 1100 || got.DownloadRate < 1900 || got.DownloadRate > 2100 {
		t.Fatalf("unexpected traffic metrics: %+v", got)
	}
}

func TestAPIOriginCheck(t *testing.T) {
	handler := (&server{}).routes()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9099/api/v1/unknown", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://other.example")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign origin: status=%d headers=%v", response.Code, response.Header())
	}

	request = httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:9099/api/v1/config/apply", nil)
	request.Header.Set("Origin", "kiko://localhost")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("CORS preflight still answered: headers=%v", response.Header())
	}
}

// The web console is gone; its former routes all return 404.
func TestWebConsoleRoutesRemoved(t *testing.T) {
	handler := (&server{}).routes()
	for _, path := range []string{"/", "/index.html", "/dashboard/", "/_nuxt/app.js", "/clash/version", "/api/v1/controller", "/api/v1/socket"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d", path, response.Code)
		}
	}
}

func TestInjectRuntimeKeepsClashControllerCredentials(t *testing.T) {
	s := &server{}
	first, err := s.injectRuntime(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.injectRuntime(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	readController := func(data []byte) (string, string) {
		var value struct {
			Experimental struct {
				ClashAPI struct {
					ExternalController string `json:"external_controller"`
					Secret             string `json:"secret"`
				} `json:"clash_api"`
			} `json:"experimental"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value.Experimental.ClashAPI.ExternalController, value.Experimental.ClashAPI.Secret
	}
	firstAddress, firstSecret := readController(first)
	secondAddress, secondSecret := readController(second)
	if firstAddress == "" || firstSecret == "" {
		t.Fatal("controller address and secret must be generated")
	}
	if firstAddress != secondAddress || firstSecret != secondSecret {
		t.Fatalf("controller credentials changed: (%q, %q) => (%q, %q)", firstAddress, firstSecret, secondAddress, secondSecret)
	}
}

func TestProxyLatencyUsesClashDelay(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxies/node-1/delay" || r.URL.Query().Get("url") != "https://www.gstatic.com/generate_204" {
			t.Fatalf("unexpected delay request: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatalf("missing authorization")
		}
		_, _ = w.Write([]byte(`{"delay":187}`))
	}))
	defer api.Close()
	s := &server{clashAddr: strings.TrimPrefix(api.URL, "http://"), clashSecret: "test-secret", state: runtimeState{State: "running"}}
	result := s.testProxyTarget(context.Background(), latencyTarget{ID: "node", Tag: "node-1"})
	if result.Error != "" || result.Success != 1 || result.MedianMS != 187 {
		t.Fatalf("unexpected proxy latency: %+v", result)
	}
}

func TestLatencyQueryUnified(t *testing.T) {
	if got := latencyQuery("https://x/204", false); got.Has("unified") {
		t.Fatalf("plain query must not ask for a unified delay: %v", got)
	}
	got := latencyQuery("https://x/204", true)
	if got.Get("unified") != "true" || got.Get("url") != "https://x/204" {
		t.Fatalf("unified query %v", got)
	}
	if got.Get("timeout") != "8000" {
		t.Fatalf("unified timeout %v, want 8000", got.Get("timeout"))
	}
	if plain := latencyQuery("https://x/204", false); plain.Get("timeout") != "5000" {
		t.Fatalf("plain timeout %v, want 5000", plain.Get("timeout"))
	}
	for _, unified := range []bool{false, true} {
		core, client, ctx := latencyCoreTimeout(unified), latencyClientTimeout(unified), latencyCtxTimeout(unified)
		if !(core < client && client < ctx) {
			t.Fatalf("budgets must nest (unified=%v): core %v client %v ctx %v", unified, core, client, ctx)
		}
	}
}

func TestProxyLatencyUnifiedRequestAndMode(t *testing.T) {
	var seen string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query().Get("unified")
		_, _ = w.Write([]byte(`{"delay":42}`))
	}))
	defer api.Close()
	s := &server{clashAddr: strings.TrimPrefix(api.URL, "http://"), clashSecret: "x", state: runtimeState{State: "running"}}
	result := s.testProxyTarget(context.Background(), latencyTarget{ID: "n", Tag: "node-1", Unified: true})
	if seen != "true" || result.Mode != "unified" || result.MedianMS != 42 {
		t.Fatalf("unified result %+v, seen %q", result, seen)
	}
	result = s.testProxyTarget(context.Background(), latencyTarget{ID: "n", Tag: "node-1"})
	if seen != "" || result.Mode != "" {
		t.Fatalf("plain result %+v, seen %q", result, seen)
	}
}

func TestProxyLatencyFallsBackWhenRuntimeStopped(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := &server{state: runtimeState{State: "stopped"}, clashAddr: "127.0.0.1:1"}
	result := s.testProxyTarget(context.Background(), latencyTarget{ID: "local", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port})
	if result.Error != "" || result.Success == 0 || result.MedianMS < 0 {
		t.Fatalf("unexpected stopped-runtime latency: %+v", result)
	}
}

func TestClashSelectors(t *testing.T) {
	var selected string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			http.Error(w, "invalid authorization", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"自动选择":{"type":"URLTest","name":"自动选择","now":"节点 B","all":["节点 A","节点 B"]},"手动选择":{"type":"Selector","name":"手动选择","now":"节点 A","all":["节点 A","节点 B"]}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/手动选择":
			var value struct {
				Name string `json:"name"`
			}
			if json.NewDecoder(r.Body).Decode(&value) != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			selected = value.Name
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	s := &server{
		clashAddr:   strings.TrimPrefix(api.URL, "http://"),
		clashSecret: "test-secret",
		logs:        newLogBuffer(t.TempDir()),
		events:      map[chan string]struct{}{},
	}

	listRecorder := httptest.NewRecorder()
	s.routes().ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/selectors", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected selector list response: %d %s", listRecorder.Code, listRecorder.Body.String())
	}
	var list struct {
		Selectors []clashSelector `json:"selectors"`
	}
	if err := json.NewDecoder(listRecorder.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Selectors) != 2 || list.Selectors[0].Name != "手动选择" ||
		list.Selectors[0].Type != "Selector" || list.Selectors[0].Now != "节点 A" ||
		list.Selectors[1].Name != "自动选择" || list.Selectors[1].Type != "URLTest" {
		t.Fatalf("unexpected selectors: %+v", list.Selectors)
	}

	selectRecorder := httptest.NewRecorder()
	selectRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/runtime/selector",
		strings.NewReader(`{"selector":"手动选择","outbound":"节点 B"}`))
	selectRequest.Header.Set("Content-Type", "application/json")
	s.routes().ServeHTTP(selectRecorder, selectRequest)
	if selectRecorder.Code != http.StatusNoContent || selected != "节点 B" {
		t.Fatalf("unexpected selector update: status=%d selected=%q body=%s",
			selectRecorder.Code, selected, selectRecorder.Body.String())
	}
}

func TestAtomicJSON(t *testing.T) {
	path := t.TempDir() + "/state/editor.json"
	want := storeState{Revision: 7, Doc: []byte(`{"version":1}`), Profiles: []byte(`[]`)}
	if err := atomicJSON(path, want); err != nil {
		t.Fatal(err)
	}
	var got storeState
	if err := readJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != want.Revision || !json.Valid(got.Doc) || !json.Valid(want.Doc) {
		t.Fatalf("got %+v", got)
	}
}

func fetchPublic(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"url": target, "publicOnly": true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetch", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	(&server{}).fetch(recorder, req)
	return recorder
}

func TestNonPublicAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true,
		"169.254.169.254": true, "fe80::1": true, "fd00::1": true, "0.0.0.0": true, "0.1.2.3": true, "::": true,
		"224.0.0.1": true, "ff02::1": true, "100.64.0.1": true, "255.255.255.255": true, "::ffff:127.0.0.1": true,
		"::ffff:10.0.0.1": true, "8.8.8.8": false, "1.1.1.1": false, "93.184.216.34": false,
		"2606:4700:4700::1111": false, "::ffff:8.8.8.8": false,
		"198.18.0.1": true, "198.19.255.254": true, "198.20.0.1": false, "192.0.0.8": true, "192.0.1.1": false,
		"240.0.0.1": true, "254.1.2.3": true, "fec0::1": true, "::1.2.3.4": true, "::8.8.8.8": true,
		"64:ff9b::7f00:1": true, "64:ff9b::a00:1": true, "64:ff9b::c0a8:101": true, "64:ff9b::a9fe:a9fe": true,
		"64:ff9b::808:808": false, "64:ff9b:1::1": true,
		// 6to4, Teredo (server, obfuscated client) and ISATAP carry an IPv4 address.
		"2002:7f00:1::1": true, "2002:a00:1::1": true, "2002:808:808::1": false,
		"2001:0:7f00:1::": true, "2001:0:808:808:0:0:80ff:fffe": true, "2001:0:808:808:0:0:f7f7:f7f7": false,
		"fe80::5efe:10.0.0.1": true, "2606:4700::5efe:10.0.0.1": true, "2606:4700::5efe:8.8.8.8": false,
	} {
		if got := nonPublicAddress(netip.MustParseAddr(address)); got != want {
			t.Errorf("nonPublicAddress(%s) = %v, want %v", address, got, want)
		}
	}
}

func TestFetchPublicOnlyRefusesLoopbackAndPrivate(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the local server must not be reached")
	}))
	defer origin.Close()
	recorder := fetchPublic(t, origin.URL)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "内网") {
		t.Fatalf("loopback: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, target := range []string{"http://192.168.1.10/m.sgmodule", "http://10.0.0.1:8080/", "http://[::1]:9/", "http://169.254.169.254/latest", "http://localhost:9/"} {
		if recorder := fetchPublic(t, target); recorder.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, body = %s", target, recorder.Code, recorder.Body.String())
		}
	}
	// Without publicOnly the same local server answers.
	hit := false
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer plain.Close()
	payload, _ := json.Marshal(map[string]any{"url": plain.URL})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetch", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	(&server{}).fetch(rec, req)
	if rec.Code != http.StatusOK || !hit {
		t.Errorf("plain fetch: status = %d hit = %v", rec.Code, hit)
	}
}

func TestFetchPublicOnlyChecksRedirectTargets(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a redirect to a local address must not be followed")
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = w.Write([]byte("public body"))
			return
		}
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer origin.Close()
	// The origin stands for a public server; the redirect target does not.
	originHost := strings.TrimPrefix(origin.URL, "http://")
	publicAllowHook = func(address string) bool { return address == originHost }
	defer func() { publicAllowHook = nil }()
	if recorder := fetchPublic(t, origin.URL+"/ok"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "public body") {
		t.Fatalf("public: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fetchPublic(t, origin.URL+"/redirect"); recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "内网") {
		t.Fatalf("redirect: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func fetchPublicRedirects(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"url": target, "publicRedirects": true})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fetch", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	(&server{}).fetch(recorder, req)
	return recorder
}

func TestFetchPublicRedirectsFreeFirstAddress(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a redirect to a local address must not be followed")
	}))
	defer local.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("public body"))
	}))
	defer public.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("lan body"))
		case "/loopback":
			http.Redirect(w, r, local.URL, http.StatusFound)
		case "/private":
			http.Redirect(w, r, "http://192.168.1.10/m.sgmodule", http.StatusFound)
		default:
			http.Redirect(w, r, public.URL, http.StatusFound)
		}
	}))
	defer origin.Close()
	// The public server stands for a public host; the origin (loopback) is
	// the address the user typed.
	publicHost := strings.TrimPrefix(public.URL, "http://")
	publicAllowHook = func(address string) bool { return address == publicHost }
	defer func() { publicAllowHook = nil }()
	if recorder := fetchPublicRedirects(t, origin.URL+"/ok"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "lan body") {
		t.Fatalf("first address: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, path := range []string{"/loopback", "/private"} {
		if recorder := fetchPublicRedirects(t, origin.URL+path); recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "内网") {
			t.Errorf("%s: status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := fetchPublicRedirects(t, origin.URL+"/public"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "public body") {
		t.Fatalf("public redirect: status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
