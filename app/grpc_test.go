package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const testSecret = "grpc-secret"

type fakeStarted struct {
	daemon.UnimplementedStartedServiceServer
	mu        sync.Mutex
	selected  []string
	selectErr error
}

func (f *fakeStarted) SubscribeStatus(_ *daemon.SubscribeStatusRequest, stream grpc.ServerStreamingServer[daemon.Status]) error {
	frames := []*daemon.Status{
		{Memory: 1024, ConnectionsIn: 3, ConnectionsOut: 9, TrafficAvailable: true, UplinkTotal: 100, DownlinkTotal: 200},
		{Memory: 2048, ConnectionsIn: 4, ConnectionsOut: 9, TrafficAvailable: true, Uplink: 50, Downlink: 70, UplinkTotal: 150, DownlinkTotal: 270},
	}
	for _, frame := range frames {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func (f *fakeStarted) SubscribeGroups(_ *emptypb.Empty, stream grpc.ServerStreamingServer[daemon.Groups]) error {
	err := stream.Send(&daemon.Groups{Group: []*daemon.Group{
		{Tag: "auto", Type: "urltest", Selected: "a", Items: []*daemon.GroupItem{{Tag: "a"}, {Tag: "b"}}},
		{Tag: "proxy", Type: "selector", Selectable: true, Selected: "auto", Items: []*daemon.GroupItem{{Tag: "auto"}, {Tag: "a"}}},
	}})
	if err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

func (f *fakeStarted) SelectOutbound(_ context.Context, request *daemon.SelectOutboundRequest) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selectErr != nil {
		return nil, f.selectErr
	}
	f.selected = append(f.selected, request.GroupTag+"="+request.OutboundTag)
	return &emptypb.Empty{}, nil
}

func checkAuth(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) == 0 || values[0] != "Bearer "+testSecret {
		return status.Error(codes.Unauthenticated, "invalid authorization")
	}
	return nil
}

func startFakeStarted(t *testing.T, address string, fake daemon.StartedServiceServer) (*grpc.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := checkAuth(ctx); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := checkAuth(stream.Context()); err != nil {
				return err
			}
			return handler(srv, stream)
		}),
	)
	daemon.RegisterStartedServiceServer(server, fake)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	return server, listener.Addr().String()
}

func newGRPCTestServer(t *testing.T) *server {
	t.Helper()
	return &server{dataDir: t.TempDir(), events: map[chan string]struct{}{}, logs: newLogBuffer(t.TempDir())}
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSelectorsFromGroups(t *testing.T) {
	selectors := selectorsFromGroups(&daemon.Groups{Group: []*daemon.Group{
		{Tag: "b-auto", Type: "urltest", Selected: "x", Items: []*daemon.GroupItem{{Tag: "x"}}},
		{Tag: "z", Type: "selector", Selected: "y", Items: []*daemon.GroupItem{{Tag: "y"}, {Tag: "x"}}},
		{Tag: "fallback", Type: "fallback", Items: []*daemon.GroupItem{{Tag: "x"}}},
		{Tag: "a", Type: "Selector", Selected: "x", Items: []*daemon.GroupItem{{Tag: "x"}}},
	}})
	got, _ := json.Marshal(selectors)
	want := `[{"name":"a","type":"Selector","now":"x","all":["x"]},{"name":"z","type":"Selector","now":"y","all":["y","x"]},{"name":"b-auto","type":"URLTest","now":"x","all":["x"]},{"name":"fallback","type":"Fallback","now":"","all":["x"]}]`
	if string(got) != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	reported, _ := json.Marshal(selectorsFromGroups(&daemon.Groups{Group: []*daemon.Group{
		{Tag: "lb", Type: "loadbalance", Mode: "loadbalance", Strategy: "round_robin", Selected: "x", Items: []*daemon.GroupItem{
			{Tag: "x", State: 1, Active: true, UrlTestDelay: 80, SuccessRate: 0.9, PassiveDelay: 120},
			{Tag: "y", State: 3, SuccessRate: -1},
		}},
		{Tag: "other", Type: "direct", Items: []*daemon.GroupItem{{Tag: "x"}}},
	}}))
	wantReported := `[{"name":"lb","type":"LoadBalance","now":"x","all":["x","y"],"mode":"loadbalance","strategy":"round_robin","members":[{"name":"x","state":"up","active":true,"delayMs":80,"successRate":0.9,"latencyMs":120},{"name":"y","state":"down","successRate":-1}]}]`
	if string(reported) != wantReported {
		t.Fatalf("got %s\nwant %s", reported, wantReported)
	}
	if empty, _ := json.Marshal(selectorsFromGroups(&daemon.Groups{})); string(empty) != "[]" {
		t.Fatalf("empty selectors must marshal as [], got %s", empty)
	}
}

func TestInjectAPIServiceIsStableAndReplacesUserService(t *testing.T) {
	s := &server{}
	s.rpc.probeOnce.Do(func() { s.rpc.supported = true })
	build := func() (map[string]any, []map[string]any) {
		out, err := s.injectRuntime(map[string]any{"services": []any{
			map[string]any{"type": "api", "tag": apiServiceTag, "listen": "0.0.0.0", "secret": "user"},
			map[string]any{"type": "api", "tag": legacyAPIServiceTag, "listen": "0.0.0.0", "secret": "old"},
			map[string]any{"type": "derp", "tag": "derp-1"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Experimental struct {
				ClashAPI struct {
					ExternalController string `json:"external_controller"`
				} `json:"clash_api"`
			} `json:"experimental"`
			Services []map[string]any `json:"services"`
		}
		if err = json.Unmarshal(out, &value); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"clash": value.Experimental.ClashAPI.ExternalController}, value.Services
	}
	first, services := build()
	if len(services) != 2 || services[0]["tag"] != "derp-1" {
		t.Fatalf("services=%v", services)
	}
	api := services[1]
	if api["tag"] != apiServiceTag || api["type"] != "api" || api["listen"] != "127.0.0.1" || api["secret"] == "user" || api["secret"] == "" {
		t.Fatalf("api=%v", api)
	}
	port := strconv.Itoa(int(api["listen_port"].(float64)))
	if strings.HasSuffix(first["clash"].(string), ":"+port) {
		t.Fatalf("api port %s must differ from clash %v", port, first["clash"])
	}
	_, again := build()
	if again[1]["listen_port"] != api["listen_port"] || again[1]["secret"] != api["secret"] {
		t.Fatalf("api credentials changed: %v => %v", api, again[1])
	}
}

func TestInjectAPIServiceSkippedWhenUnsupported(t *testing.T) {
	s := &server{}
	s.rpc.probeOnce.Do(func() {})
	out, err := s.injectRuntime(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"services"`) {
		t.Fatalf("unsupported core must not receive api service: %s", out)
	}
}

func TestLoadRuntimeControlRestoresAPICredentials(t *testing.T) {
	s := &server{dataDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(s.dataDir, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"services":[{"type":"derp","tag":"d","listen_port":1},{"type":"api","tag":"kiko-api","listen_port":4321,"secret":"abc"}]}`
	if err := os.WriteFile(filepath.Join(s.dataDir, "runtime", "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	s.loadRuntimeControl()
	if s.rpc.port != 4321 || s.rpc.secret != "abc" {
		t.Fatalf("port=%d secret=%q", s.rpc.port, s.rpc.secret)
	}
}

func TestProbeAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	write := func(exit int) *server {
		dir := t.TempDir()
		binary := filepath.Join(dir, "fake-kiko-core")
		script := "#!/bin/sh\nexit " + strconv.Itoa(exit) + "\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return &server{singBox: binary, dataDir: filepath.Join(dir, "data")}
	}
	if !write(0).apiSupported() {
		t.Fatal("check success must enable api service")
	}
	if write(1).apiSupported() {
		t.Fatal("check failure must disable api service")
	}
	if (&server{}).apiSupported() {
		t.Fatal("missing binary must disable api service")
	}
}

func TestGRPCWorkerMergesStatusGroupsAndSelects(t *testing.T) {
	fake := &fakeStarted{}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	events := make(chan string, 16)
	s.events[events] = struct{}{}
	s.state.State = "running"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.grpcWorker(ctx, "http://"+address, testSecret)

	eventually(t, "grpc active", func() bool {
		if !s.grpcActive() {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.state.UploadRate == 50
	})
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state.MemoryBytes != 2048 || state.ActiveConnections != 4 || state.DownloadRate != 70 ||
		state.UploadTotal != 150 || state.DownloadTotal != 270 || !state.MetricsAvailable {
		t.Fatalf("state=%+v", state)
	}

	eventually(t, "selectors", func() bool { _, ok := s.grpcSelectors(); return ok })
	selectors, _ := s.grpcSelectors()
	if len(selectors) != 2 || selectors[0].Name != "proxy" || selectors[0].Type != "Selector" || selectors[1].Type != "URLTest" {
		t.Fatalf("selectors=%+v", selectors)
	}
	var sawSelectors bool
drain:
	for {
		select {
		case message := <-events:
			sawSelectors = sawSelectors || strings.Contains(message, `"type":"selectors"`)
		default:
			break drain
		}
	}
	if !sawSelectors {
		t.Fatal("selectors event was not emitted")
	}

	recorder := httptest.NewRecorder()
	s.getSelectors(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/selectors", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"name":"proxy"`) {
		t.Fatalf("GET selectors: %d %s", recorder.Code, recorder.Body)
	}

	recorder = httptest.NewRecorder()
	s.setSelector(recorder, httptest.NewRequest(http.MethodPatch, "/api/v1/runtime/selector", strings.NewReader(`{"selector":"proxy","outbound":"a"}`)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("PATCH selector: %d %s", recorder.Code, recorder.Body)
	}
	fake.mu.Lock()
	selected := append([]string{}, fake.selected...)
	fake.mu.Unlock()
	if len(selected) != 1 || selected[0] != "proxy=a" {
		t.Fatalf("selected=%v", selected)
	}
	cached, _ := s.grpcSelectors()
	if cached[0].Now != "a" {
		t.Fatalf("cached now=%q", cached[0].Now)
	}
}

func TestGRPCWorkerReconnects(t *testing.T) {
	fake := &fakeStarted{}
	first, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.grpcWorker(ctx, "http://"+address, testSecret)
	eventually(t, "initial connection", s.grpcActive)

	first.Stop()
	eventually(t, "disconnect detected", func() bool { return !s.grpcActive() })
	if _, ok := s.grpcSelectors(); ok {
		t.Fatal("selectors must not be served while disconnected")
	}

	startFakeStarted(t, address, fake)
	eventually(t, "reconnection", s.grpcActive)
}

func TestGRPCWorkerRejectsWrongSecret(t *testing.T) {
	_, address := startFakeStarted(t, "127.0.0.1:0", &fakeStarted{})
	s := newGRPCTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.grpcWorker(ctx, "http://"+address, "wrong")
	time.Sleep(300 * time.Millisecond)
	if s.grpcActive() {
		t.Fatal("wrong secret must not activate grpc")
	}
}

func TestSetSelectorFallsBackToClashWhenGRPCFails(t *testing.T) {
	var clashPath string
	clash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clashPath = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer clash.Close()

	fake := &fakeStarted{selectErr: errors.New("boom")}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	s.clashAddr, s.clashSecret = strings.TrimPrefix(clash.URL, "http://"), "clash"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.grpcWorker(ctx, "http://"+address, testSecret)
	eventually(t, "grpc active", s.grpcActive)

	recorder := httptest.NewRecorder()
	s.setSelector(recorder, httptest.NewRequest(http.MethodPatch, "/api/v1/runtime/selector", strings.NewReader(`{"selector":"proxy","outbound":"a"}`)))
	if recorder.Code != http.StatusNoContent || clashPath != "PUT /proxies/proxy" {
		t.Fatalf("code=%d clash=%q", recorder.Code, clashPath)
	}
}

// With KIKO_TEST_CORE=/path/to/kiko-core (built with with_clash_api), verifies probing, injection, auth and subscriptions against a real daemon.
func TestRealSingBoxGRPC(t *testing.T) {
	binary := os.Getenv("KIKO_TEST_CORE")
	if binary == "" || runtime.GOOS == "windows" {
		t.Skip("KIKO_TEST_CORE not set")
	}
	s := newGRPCTestServer(t)
	s.singBox = binary
	if !s.apiSupported() {
		t.Fatal("real core must support the api service")
	}
	out, err := s.injectRuntime(map[string]any{"outbounds": []any{
		map[string]any{"type": "direct", "tag": "direct"},
		map[string]any{"type": "selector", "tag": "proxy", "outbounds": []any{"direct"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(s.dataDir, "config.json")
	if err = os.WriteFile(configPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", configPath)
	cmd.Dir = s.dataDir
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.grpcWorker(ctx, "http://127.0.0.1:"+strconv.Itoa(s.rpc.port), s.rpc.secret)
	eventually(t, "real grpc active", s.grpcActive)
	eventually(t, "real selectors", func() bool { _, ok := s.grpcSelectors(); return ok })
	selectors, _ := s.grpcSelectors()
	if len(selectors) != 1 || selectors[0].Name != "proxy" || selectors[0].Type != "Selector" || len(selectors[0].All) != 1 {
		t.Fatalf("selectors=%+v", selectors)
	}
	if err = s.grpcSelect(ctx, "proxy", "direct"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.MetricsAvailable || s.state.MemoryBytes == 0 {
		t.Fatalf("state=%+v", s.state)
	}
}

func TestGRPCTickStartsAndStopsWorker(t *testing.T) {
	_, address := startFakeStarted(t, "127.0.0.1:0", &fakeStarted{})
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	s := newGRPCTestServer(t)
	s.rpc.port, s.rpc.secret = port, testSecret

	s.grpcTick()
	if s.rpc.cancel != nil {
		t.Fatal("worker must not start while the core is stopped")
	}
	s.mu.Lock()
	s.state.State, s.state.StartedAt, s.state.PID = "running", time.Now(), 1
	s.helperPID = 1
	s.mu.Unlock()
	s.grpcTick()
	eventually(t, "grpc active", s.grpcActive)

	s.mu.Lock()
	s.state.State = "reloading"
	s.mu.Unlock()
	s.rpc.mu.Lock()
	workerDone := s.rpc.workerDone
	s.rpc.mu.Unlock()
	s.grpcTick()
	s.rpc.mu.Lock()
	sameWorker := s.rpc.workerDone == workerDone && s.rpc.cancel != nil
	s.rpc.mu.Unlock()
	if !sameWorker || !s.grpcActive() {
		t.Fatal("configuration reload must preserve the control worker")
	}

	s.mu.Lock()
	s.state.State = "stopped"
	s.mu.Unlock()
	s.grpcTick()
	if s.grpcActive() || s.rpc.cancel != nil {
		t.Fatal("worker must stop with the core")
	}
}

// stalledStarted sends one status frame per subscription and then goes silent
// without closing the stream.
type stalledStarted struct {
	fakeStarted
	mu            sync.Mutex
	subscriptions int
}

func (f *stalledStarted) SubscribeStatus(_ *daemon.SubscribeStatusRequest, stream grpc.ServerStreamingServer[daemon.Status]) error {
	f.mu.Lock()
	f.subscriptions++
	f.mu.Unlock()
	if err := stream.Send(&daemon.Status{Memory: 1024, TrafficAvailable: true}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

func (f *stalledStarted) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscriptions
}

// A status stream that stays open but stops sending frames is replaced by a
// new session instead of freezing the metrics forever.
func TestGRPCWorkerReconnectsStalledStatusStream(t *testing.T) {
	defer func(timeout, grace time.Duration) { grpcStatusTimeout, grpcCollectorGrace = timeout, grace }(grpcStatusTimeout, grpcCollectorGrace)
	grpcStatusTimeout, grpcCollectorGrace = 200*time.Millisecond, 100*time.Millisecond
	fake := &stalledStarted{}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.grpcWorker(ctx, "http://"+address, testSecret) }()
	eventually(t, "a second status subscription", func() bool { return fake.count() >= 2 })
	cancel()
	<-done
}
