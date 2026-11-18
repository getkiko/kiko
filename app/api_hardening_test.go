package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hardenedAPI(t *testing.T) (http.Handler, string) {
	t.Helper()
	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/fetch", s.fetch)
	mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pong")) })
	mux.HandleFunc("POST /api/v1/ping", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pong")) })
	return localHostGuard(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9099}, s.serveAPI(mux)), "127.0.0.1:9099"
}

func TestFetchGetNeverServesUpstreamContentType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	}))
	defer upstream.Close()
	handler, host := hardenedAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fetch?url="+upstream.URL, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	header := rec.Header()
	if rec.Code != 200 || header.Get("Content-Type") != "application/octet-stream" ||
		header.Get("X-Content-Type-Options") != "nosniff" ||
		header.Get("Content-Security-Policy") != "sandbox" ||
		!strings.HasPrefix(header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("code=%d headers=%v", rec.Code, header)
	}
}

func TestLocalHostGuard(t *testing.T) {
	handler, _ := hardenedAPI(t)
	cases := map[string]int{
		"127.0.0.1:9099": 200, "localhost:9099": 200, "LOCALHOST:9099": 200, "[::1]:9099": 200,
		"evil.example:9099": 403, "127.0.0.1.evil.example:9099": 403, "127.0.0.1:1234": 403,
		"127.0.0.1": 403, "": 403,
	}
	for host, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: %d, want %d", host, rec.Code, want)
		}
	}
	open := localHostGuard(&net.TCPAddr{IP: net.IPv4zero, Port: 9099}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "any.example"
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("non-loopback listener must not be guarded: %d", rec.Code)
	}
}

func TestCrossSiteRequestsRejected(t *testing.T) {
	handler, host := hardenedAPI(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for site, want := range map[string]int{"cross-site": 403, "same-site": 403, "same-origin": 200, "none": 200, "": 200} {
			req := httptest.NewRequest(method, "/api/v1/ping", strings.NewReader("{}"))
			req.Host = host
			req.Header.Set("Content-Type", "application/json")
			if site != "" {
				req.Header.Set("Sec-Fetch-Site", site)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Errorf("%s Sec-Fetch-Site %q: %d, want %d", method, site, rec.Code, want)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fetch?url=http://127.0.0.1:1/", nil)
	req.Host = host
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("cross-site fetch: %d", rec.Code)
	}
}
