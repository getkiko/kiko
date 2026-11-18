package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func holder(name, op string) gateHolder {
	return gateHolder{Client: clientIdentity{ID: name, Name: name}, Op: op}
}

func TestWriteGateFIFO(t *testing.T) {
	g := newWriteGate(nil, nil)
	release, err := g.acquire(context.Background(), holder("first", "保存配置"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	names := []string{"a", "b", "c", "d"}
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done, err := g.acquire(context.Background(), holder(name, "应用配置"))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			done()
		}()
		waitFor(t, "queue", func() bool { return g.state().Queued == i+1 })
	}
	release()
	release() // A second release is a no-op.
	wg.Wait()
	if strings.Join(order, ",") != strings.Join(names, ",") {
		t.Fatalf("order = %v", order)
	}
	if state := g.state(); state.Held || state.Queued != 0 {
		t.Fatalf("gate after drain: %+v", state)
	}
}

func TestWriteGateCancelledWaiterLeavesQueue(t *testing.T) {
	g := newWriteGate(nil, nil)
	release, _ := g.acquire(context.Background(), holder("first", "保存配置"))
	ctx, cancel := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, holder("gone", "应用配置"))
		failed <- err
	}()
	waitFor(t, "queued waiter", func() bool { return g.state().Queued == 1 })
	cancel()
	if err := <-failed; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if g.state().Queued != 0 {
		t.Fatal("cancelled waiter kept its place")
	}
	release()
	if g.state().Held {
		t.Fatal("gate handed to a cancelled waiter")
	}
}

func TestWriteGateAnnouncesLongHolds(t *testing.T) {
	events := make(chan lockState, 8)
	g := newWriteGate(func(state lockState) { events <- state }, nil)
	g.notify = 20 * time.Millisecond
	release, _ := g.acquire(context.Background(), holder("quick", "保存配置"))
	release()
	select {
	case state := <-events:
		t.Fatalf("short hold announced: %+v", state)
	case <-time.After(60 * time.Millisecond):
	}
	release, _ = g.acquire(context.Background(), holder("slow", "应用配置"))
	state := <-events
	if !state.Held || state.Holder.Name != "slow" || state.Op != "应用配置" || state.Since == nil {
		t.Fatalf("hold event: %+v", state)
	}
	release()
	if state = <-events; state.Held || state.Holder != nil {
		t.Fatalf("release event: %+v", state)
	}
}

func newGateServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	s, err := newServer("/unused/kiko-core", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return s, s.routes()
}

func TestWriteGateTimeoutReturns423(t *testing.T) {
	s, handler := newGateServer(t)
	s.writeGate().timeout = 50 * time.Millisecond
	release, _ := s.writeGate().acquire(context.Background(), holder("MacBook", "应用配置"))
	defer release()

	request := httptest.NewRequest(http.MethodPut, "/api/v1/store", strings.NewReader(`{"revision":0,"doc":{}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var body struct {
		Error  string         `json:"error"`
		Holder clientIdentity `json:"holder"`
		Op     string         `json:"op"`
		Since  string         `json:"since"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if recorder.Code != http.StatusLocked || recorder.Header().Get("Retry-After") != "2" {
		t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
	}
	if body.Error != "「MacBook」正在应用配置，请稍后重试" || body.Holder.ID != "MacBook" || body.Holder.Name != "MacBook" || body.Op != "应用配置" {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	if _, err := time.Parse(time.RFC3339, body.Since); err != nil {
		t.Fatal(err)
	}

	// Reads never wait for the gate; a gated read would get 423 here.
	for _, path := range []string{"/api/v1/runtime", "/api/v1/store", "/api/v1/lock"} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, recorder.Code)
		}
	}
	var state lockState
	_ = json.Unmarshal(recorder.Body.Bytes(), &state)
	if !state.Held || state.Holder.Name != "MacBook" || state.Op != "应用配置" {
		t.Fatalf("lock state = %s", recorder.Body.String())
	}
}

func TestWriteRequestsRunInOrderAndAreAudited(t *testing.T) {
	s, handler := newGateServer(t)
	release, _ := s.writeGate().acquire(context.Background(), holder("other", "应用配置"))
	results := make(chan int, 2)
	put := func(revision string) {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/store", strings.NewReader(`{"revision":`+revision+`,"doc":{}}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		results <- recorder.Code
	}
	go put("0")
	waitFor(t, "first write queued", func() bool { return s.writeGate().state().Queued == 1 })
	go put("1")
	waitFor(t, "second write queued", func() bool { return s.writeGate().state().Queued == 2 })
	release()
	// Revision 0 must land first, or the second write would conflict.
	if a, b := <-results, <-results; a != http.StatusOK || b != http.StatusOK {
		t.Fatalf("statuses = %d, %d", a, b)
	}
	if s.store.Revision != 2 {
		t.Fatalf("revision = %d", s.store.Revision)
	}
	b, _ := os.ReadFile(filepath.Join(s.dataDir, "logs", "kiko.log"))
	if !strings.Contains(string(b), "[审计] 本机（local）保存配置 PUT /api/v1/store → 200") {
		t.Fatalf("audit log:\n%s", b)
	}
}

func TestRestartHoldsGateUntilDone(t *testing.T) {
	s, handler := newGateServer(t)
	// The restart's start step needs configMu; hold it so the restart stays busy.
	s.configMu.Lock()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/restart", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		s.configMu.Unlock()
		t.Fatalf("restart: %d %s", recorder.Code, recorder.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	state := s.writeGate().state()
	s.configMu.Unlock()
	if !state.Held || state.Op != "重启核心" {
		t.Fatalf("gate released before the restart finished: %+v", state)
	}
	waitFor(t, "restart release", func() bool { return !s.writeGate().state().Held })
}

func TestStopHoldsGateUntilDone(t *testing.T) {
	s, _ := newGateServer(t)
	released := make(chan struct{})
	s.stopAsync(true, func() { close(released) })
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("stop never released")
	}
}

func TestEveryRouteIsClassified(t *testing.T) {
	table := (&server{}).routeTable()
	want := map[string]routeClass{
		"POST /api/v1/shutdown":       routeExempt,
		"PUT /api/v1/store":           routeWrite,
		"POST /api/v1/config/apply":   routeWrite,
		"POST /api/v1/runtime/start":  routeWrite,
		"POST /api/v1/taildrop/send":  routeOwn,
		"POST /api/v1/sync/now":       routeOwn,
		"POST /api/v1/sync/reset":     routeOwn,
		"POST /api/v1/sync/unlock":    routeOwn,
		"POST /api/v1/fetch":          routeRead,
		"POST /api/v1/latency/test":   routeRead,
		"POST /api/v1/policy/test":    routeRead,
		"POST /api/v1/tailscale/ping": routeRead,
		"POST /api/v1/sync/test":      routeRead,
		"PUT /api/v1/remote":          routeWrite,
		"GET /api/v1/events":          routeRead,
		"GET /api/v1/lock":            routeRead,
	}
	seen := map[string]bool{}
	for _, spec := range table.specs {
		if seen[spec.Pattern] {
			t.Fatalf("duplicate route %s", spec.Pattern)
		}
		seen[spec.Pattern] = true
		if spec.Class == routeUnclassified {
			t.Fatalf("%s is unclassified", spec.Pattern)
		}
		if spec.Method != http.MethodGet && spec.Class != routeRead && spec.Op == "" {
			t.Fatalf("%s has no operation label", spec.Pattern)
		}
		if spec.Method == http.MethodDelete && spec.Class != routeWrite {
			t.Fatalf("%s should be a write", spec.Pattern)
		}
		if class, ok := want[spec.Pattern]; ok && class != spec.Class {
			t.Fatalf("%s: class %s, want %s", spec.Pattern, spec.Class, class)
		}
	}
	for pattern := range want {
		if !seen[pattern] {
			t.Fatalf("route %s missing", pattern)
		}
	}
	for _, bad := range []func(m *routeMux){
		func(m *routeMux) { m.handle("POST /x", routeUnclassified, "", nil) },
		func(m *routeMux) { m.handle("POST /x", routeWrite, "", nil) },
		func(m *routeMux) { m.handle("/x", routeRead, "", nil) },
		func(m *routeMux) { m.handle("GET /x", routeWrite, "写", nil) },
		func(m *routeMux) { m.get("POST /x", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("bad registration accepted")
				}
			}()
			bad(newRouteMux(nil))
		}()
	}
}

func TestRemoteHolderNamedInLock(t *testing.T) {
	h := newRemoteHarness(t)
	token, id := h.pair("iPad")
	// apply takes configMu after the gate; holding it keeps the remote write busy.
	h.s.configMu.Lock()
	done := make(chan int, 1)
	go func() {
		response := h.do(context.Background(), http.MethodPost, "/api/v1/config/apply", token, `{}`)
		response.Body.Close()
		done <- response.StatusCode
	}()
	waitFor(t, "remote holder", func() bool {
		state := h.s.writeGate().state()
		return state.Held && state.Holder.ID == id && state.Holder.Name == "iPad" && state.Op == "应用配置"
	})
	h.s.writeGate().timeout = 50 * time.Millisecond
	recorder := h.localJSON(http.MethodPut, "/api/v1/store", `{"revision":0,"doc":{}}`)
	h.s.configMu.Unlock()
	if recorder.Code != http.StatusLocked || !strings.Contains(recorder.Body.String(), "「iPad」正在应用配置") {
		t.Fatalf("local write while remote holds: %d %s", recorder.Code, recorder.Body.String())
	}
	<-done
	waitFor(t, "release", func() bool { return !h.s.writeGate().state().Held })
}
