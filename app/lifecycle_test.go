package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func lifecycleFixture(t *testing.T, script string) *server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal fixture")
	}
	s, err := newServer(writeScript(t, script), filepath.Join(t.TempDir(), "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(filepath.Join(s.dataDir, "runtime", "config.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.stop(true) })
	return s
}

func TestStopCancelsInFlightStartupCheck(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "checking")
	s := lifecycleFixture(t, "if [ \"$1\" = check ]; then touch "+marker+"; exec sleep 2; fi\nexec sleep 30\n")
	started := make(chan error, 1)
	go func() { started <- s.start() }()
	eventually(t, "configuration check started", func() bool { _, err := os.Stat(marker); return err == nil })
	began := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/stop", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("stop HTTP %d: %s", rec.Code, rec.Body)
	}
	if elapsed := time.Since(began); elapsed > 500*time.Millisecond {
		t.Fatalf("stop blocked on startup check: %v", elapsed)
	}
	select {
	case err := <-started:
		if err == nil {
			t.Fatal("canceled startup succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("startup check was not canceled")
	}
	if state := s.snapshot(); state.PID != 0 || state.DesiredRunning || state.State != "stopped" {
		t.Fatalf("%+v", state)
	}
}

func TestRestartRespondsBeforeSlowExitAndStopCancelsRestart(t *testing.T) {
	s := lifecycleFixture(t, "if [ \"$1\" = check ]; then exit 0; fi\ntrap 'sleep 1; exit 0' TERM INT\ntouch ready\nwhile :; do sleep 0.02; done\n")
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "core readiness", func() bool { _, err := os.Stat(filepath.Join(s.dataDir, "ready")); return err == nil })
	began := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/restart", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	s.routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("restart HTTP %d: %s", rec.Code, rec.Body)
	}
	if elapsed := time.Since(began); elapsed > 500*time.Millisecond {
		t.Fatalf("restart blocked on exit: %v", elapsed)
	}
	s.stopAsync(true)
	eventually(t, "explicit stop wins", func() bool {
		state := s.snapshot()
		return state.State == "stopped" && !state.DesiredRunning && state.PID == 0
	})
	time.Sleep(100 * time.Millisecond)
	if state := s.snapshot(); state.State != "stopped" || state.PID != 0 {
		t.Fatalf("restart raced explicit stop: %+v", state)
	}
}

func TestStopBoundsUnresponsiveCore(t *testing.T) {
	s := lifecycleFixture(t, "if [ \"$1\" = check ]; then exit 0; fi\ntrap '' TERM INT\ntouch ready\nwhile :; do sleep 0.02; done\n")
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "core readiness", func() bool { _, err := os.Stat(filepath.Join(s.dataDir, "ready")); return err == nil })
	done := make(chan struct{})
	began := time.Now()
	go func() { s.stop(true); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop exceeded five-second bound")
	}
	if state := s.snapshot(); state.State != "stopped" || state.PID != 0 {
		t.Fatalf("stop left process: %+v", state)
	}
	t.Logf("unresponsive core stopped in %v", time.Since(began))
}

func TestStartWaitsForPreviousCoreDNSCleanup(t *testing.T) {
	s := lifecycleFixture(t, "if [ \"$1\" = check ]; then exit 0; fi\nexec sleep 30\n")
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	cleaning, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	s.mu.Lock()
	s.restoreSystemDNS = func() error { close(cleaning); <-release; return nil }
	cmd := s.cmd
	s.mu.Unlock()
	defer unblock()
	if err := forceProcess(cmd); err != nil {
		t.Fatal(err)
	}
	<-cleaning
	started := make(chan error, 1)
	go func() { started <- s.start() }()
	select {
	case err := <-started:
		t.Fatalf("start finished while old DNS restore was pending: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// A later explicit stop must cancel the start waiting for cleanup.
	s.stopAsync(true)
	unblock()
	select {
	case err := <-started:
		if err == nil {
			t.Fatal("stop failed to cancel start waiting for DNS cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("start did not finish after cleanup")
	}
}

func TestStopJoinsDNSCleanupAfterUnexpectedExit(t *testing.T) {
	s := lifecycleFixture(t, "if [ \"$1\" = check ]; then exit 0; fi\nexec sleep 30\n")
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	cleaning, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	s.mu.Lock()
	s.restoreSystemDNS = func() error { close(cleaning); <-release; return nil }
	cmd := s.cmd
	s.mu.Unlock()
	defer unblock()
	if err := forceProcess(cmd); err != nil {
		t.Fatal(err)
	}
	<-cleaning
	done := make(chan struct{})
	go func() { s.stop(true); close(done) }()
	select {
	case <-done:
		t.Fatal("stop returned before old DNS cleanup completed")
	case <-time.After(150 * time.Millisecond):
	}
	unblock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not finish after cleanup")
	}
}

func TestFailedDNSRestorePreservesOriginalForRetry(t *testing.T) {
	s := newGRPCTestServer(t)
	calls := 0
	s.restoreSystemDNS = func() error {
		calls++
		if calls == 1 {
			return errors.New("temporary DNS failure")
		}
		return nil
	}
	s.restoreSystemDNSConfig()
	if s.restoreSystemDNS == nil {
		t.Fatal("failed restoration discarded original DNS snapshot")
	}
	s.restoreSystemDNSConfig()
	if calls != 2 || s.restoreSystemDNS != nil {
		t.Fatalf("restore retry: calls=%d pending=%v", calls, s.restoreSystemDNS != nil)
	}
}

func TestStoppedReloadCannotRefreshSystemDNS(t *testing.T) {
	s := lifecycleFixture(t, "if [ \"$1\" = check ]; then exit 0; fi\nexec sleep 30\n")
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	reload := &coreReload{s: s, cmd: s.cmd, startedAt: s.state.StartedAt}
	s.mu.Unlock()
	s.stop(true)
	called := false
	s.restoreSystemDNS = func() error { called = true; return nil }
	s.refreshSystemDNSConfig(reload, "stale-runtime-config.json")
	if called {
		t.Fatal("stale reload changed DNS after explicit stop")
	}
}

func TestStopAsyncRetriesDNSRestoreInBackground(t *testing.T) {
	s := newGRPCTestServer(t)
	cleaning, release := make(chan struct{}), make(chan struct{})
	s.restoreSystemDNS = func() error { close(cleaning); <-release; return nil }
	defer func() { close(release); s.stop(true) }()
	returned := make(chan struct{})
	go func() { s.stopAsync(true); close(returned) }()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stop response blocked on a pending DNS restoration")
	}
	<-cleaning
}
