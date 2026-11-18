package testkit

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// FetchPage is what the fake's POST /fetch answers for one URL.
type FetchPage struct {
	// Status is the upstream status, 200 when 0.
	Status int
	Body   string
	// Delay holds the answer back; a canceled request leaves at once.
	Delay time.Duration
	// Fail makes the server itself fail (502 with this message), as when
	// it cannot reach the address.
	Fail string
}

// fetchFake is the state of the fake's POST /fetch.
type fetchFake struct {
	mu       sync.Mutex
	pages    map[string]FetchPage
	calls    []string
	inflight int
	peak     int
	hook     func(url string) (FetchPage, bool)
}

// SetFetch sets the answer for url; an unknown URL answers 404.
func (s *Server) SetFetch(url string, page FetchPage) {
	f := &s.fetch
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pages == nil {
		f.pages = map[string]FetchPage{}
	}
	f.pages[url] = page
}

// SetFetchBody is SetFetch for a 200 answer with body.
func (s *Server) SetFetchBody(url, body string) { s.SetFetch(url, FetchPage{Body: body}) }

// SetFetchHook answers the URLs it accepts (ok) in place of the table; it
// runs per request, so a test can change an answer while a refresh runs.
func (s *Server) SetFetchHook(fn func(url string) (FetchPage, bool)) {
	f := &s.fetch
	f.mu.Lock()
	f.hook = fn
	f.mu.Unlock()
}

// FetchCalls lists the URLs fetched, in arrival order.
func (s *Server) FetchCalls() []string {
	f := &s.fetch
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// FetchPeak is the most fetches that ran at once.
func (s *Server) FetchPeak() int {
	f := &s.fetch
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

// ResetFetchCalls forgets the calls and the peak.
func (s *Server) ResetFetchCalls() {
	f := &s.fetch
	f.mu.Lock()
	f.calls, f.peak = nil, 0
	f.mu.Unlock()
}

// The fake's POST /fetch: the page of the URL in the request, after its
// delay, or its failure.
func (s *Server) serveFetch(key string, w http.ResponseWriter, r *http.Request) bool {
	if key != "POST /api/v1/fetch" {
		return false
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	f := &s.fetch
	f.mu.Lock()
	f.calls = append(f.calls, req.URL)
	f.inflight++
	f.peak = max(f.peak, f.inflight)
	page, ok := f.pages[req.URL]
	if f.hook != nil {
		if p, hooked := f.hook(req.URL); hooked {
			page, ok = p, true
		}
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	if page.Delay > 0 {
		select {
		case <-time.After(page.Delay):
		case <-r.Context().Done():
			return true
		}
	}
	if page.Fail != "" {
		w.WriteHeader(http.StatusBadGateway)
		JSON(map[string]string{"error": page.Fail})(w, r)
		return true
	}
	status := page.Status
	switch {
	case !ok:
		status = http.StatusNotFound
	case status == 0:
		status = http.StatusOK
	}
	JSON(api.FetchResult{Status: status, Body: page.Body})(w, r)
	return true
}
