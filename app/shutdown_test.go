package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDesktopShutdownAuthorization(t *testing.T) {
	for _, test := range []struct {
		name, configured, token, remote, origin string
		want                                    int
	}{
		{"browser", "", "secret", "127.0.0.1:1234", "", 404},
		{"missing token", "secret", "", "127.0.0.1:1234", "", 403},
		{"wrong token", "secret", "other", "127.0.0.1:1234", "", 403},
		{"remote peer", "secret", "secret", "192.0.2.1:1234", "", 403},
		{"cross origin", "secret", "secret", "127.0.0.1:1234", "http://elsewhere", 403},
		{"desktop", "secret", "secret", "127.0.0.1:1234", "", 202},
		{"IPv6 desktop", "secret", "secret", "[::1]:1234", "", 202},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &server{shutdownToken: test.configured, shutdownCancel: cancel}
			r := httptest.NewRequest("POST", "/api/v1/shutdown", strings.NewReader("{}"))
			r.RemoteAddr = test.remote
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", test.origin)
			r.Header.Set("X-Kiko-Shutdown-Token", test.token)
			w := httptest.NewRecorder()
			s.routes().ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if test.want == 202 {
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("shutdown was not requested")
				}
			} else if ctx.Err() != nil {
				t.Fatal("unauthorized shutdown")
			}
		})
	}
}

func TestShutdownIsIdempotentAndBlocksNewStarts(t *testing.T) {
	s, err := newServer("", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.td.tasks = map[string]*taildropTask{"active": {cancel: cancel}}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); s.shutdown() }()
	}
	wg.Wait()
	if ctx.Err() == nil {
		t.Fatal("Taildrop task remains active")
	}
	if err := s.start(); err == nil {
		t.Fatal("start accepted during shutdown")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/runtime/start", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestShutdownClosesEventStreams(t *testing.T) {
	s, err := newServer("", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s.routes())
	defer h.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(h.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	s.shutdown()
	if _, err = io.ReadAll(response.Body); err != nil {
		t.Fatal("event stream remains open:", err)
	}
}
