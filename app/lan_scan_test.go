package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkiko/kiko/app/lanscan"
)

func TestLANScanRoutesLocalOnly(t *testing.T) {
	m := newRouteMux(nil)
	registerLANScan(m)
	remote := context.WithValue(context.Background(), clientContextKey{}, clientIdentity{ID: "x", Name: "phone", Remote: true})
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/lan-scan"}, {"GET", "/api/v1/lan-scan?id=1"}, {"POST", "/api/v1/lan-scan/cancel?id=1"},
	} {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil).WithContext(remote))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s from a paired device: %d", c.method, c.path, w.Code)
		}
	}
}

func TestLANScanLifecycle(t *testing.T) {
	lanScannerOnce.Do(func() {}) // keep the real manager out; install one with an empty network
	lanScanner = lanscan.NewManager(func() lanscan.Env {
		return lanscan.Env{
			Interfaces: func() ([]lanscan.InterfaceInfo, error) { return nil, nil },
			Host: lanscan.Host{
				Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
				ReadFile: func(string) ([]byte, error) { return nil, nil },
			},
		}
	})
	m := newRouteMux(nil)
	registerLANScan(m)
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	w := do("POST", "/api/v1/lan-scan")
	var started struct{ ID string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &started) != nil || started.ID == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/v1/lan-scan?id="+started.ID+"&since=0"); w.Code != 200 {
		t.Fatalf("poll: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/api/v1/lan-scan?id=missing"); w.Code != http.StatusNotFound {
		t.Fatalf("poll unknown: %d", w.Code)
	}
	if w := do("POST", "/api/v1/lan-scan/cancel?id="+started.ID); w.Code != 200 {
		t.Fatalf("cancel: %d", w.Code)
	}
}

// A start while a scan runs answers 409 with that scan's id.
func TestLANScanBusyReportsRunningID(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	lanScannerOnce.Do(func() {})
	lanScanner = lanscan.NewManager(func() lanscan.Env {
		return lanscan.Env{
			Interfaces: func() ([]lanscan.InterfaceInfo, error) { <-gate; return nil, nil },
			Host: lanscan.Host{
				Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
				ReadFile: func(string) ([]byte, error) { return nil, nil },
			},
		}
	})
	m := newRouteMux(nil)
	registerLANScan(m)
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	var first, second struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	w := do("POST", "/api/v1/lan-scan")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.ID == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	w = do("POST", "/api/v1/lan-scan")
	if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &second) != nil || second.ID != first.ID || second.Error == "" {
		t.Fatalf("busy: %d %s (want id %s)", w.Code, w.Body, first.ID)
	}
	if w := do("POST", "/api/v1/lan-scan/cancel?id="+first.ID); w.Code != 200 {
		t.Fatalf("cancel: %d", w.Code)
	}
	if id := lanScanManager().RunningID(); id != "" {
		t.Errorf("running after cancel: %q", id)
	}
}
