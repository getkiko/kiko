package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReadSSE(t *testing.T) {
	input := "data: {\"type\":\"runtime\"}\n\n: comment\ndata: line one\ndata: line two\n\n\ndata: tail\n\n"
	var got []string
	if err := readSSE(strings.NewReader(input), func(b []byte) { got = append(got, string(b)) }, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"type":"runtime"}`, "line one\nline two", "tail"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEventsReconnects(t *testing.T) {
	var connections int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"log\",\"data\":\"hello %d\"}\n\n", connections)
		// The stream ends: the client reconnects.
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := make(chan Event, 4)
	go New(srv.URL).Events(ctx, nil, func(e Event) { events <- e })
	for i := 1; i <= 2; i++ {
		select {
		case e := <-events:
			var line string
			if err := json.Unmarshal(e.Data, &line); err != nil || line != fmt.Sprintf("hello %d", i) {
				t.Fatalf("event %d: %q %v", i, e.Data, err)
			}
		case <-ctx.Done():
			t.Fatal("no reconnection")
		}
	}
}

func TestNDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/ok":
			fmt.Fprint(w, "{\"n\":1}\n\n{\"n\":2}\n{\"n\":3}")
		case "/api/v1/item-error":
			fmt.Fprint(w, "{\"n\":1}\n{\"error\":\"boom\"}\n{\"n\":3}\n")
		case "/api/v1/fail":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"bad request body"}`)
		}
	}))
	defer srv.Close()
	client := New(srv.URL)
	count := func(path string, allow bool) (int, error) {
		n := 0
		err := client.NDJSON(context.Background(), http.MethodGet, path, nil, allow, func(json.RawMessage) error { n++; return nil })
		return n, err
	}
	if n, err := count("/ok", false); n != 3 || err != nil {
		t.Errorf("ok: %d items, %v", n, err)
	}
	if n, err := count("/item-error", false); n != 1 || err == nil || err.Error() != "boom" {
		t.Errorf("item error: %d items, %v", n, err)
	}
	if n, err := count("/item-error", true); n != 3 || err != nil {
		t.Errorf("allowed item errors: %d items, %v", n, err)
	}
	_, err := count("/fail", false)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Message != "bad request body" {
		t.Errorf("failure: %v", err)
	}
}

func TestErrorsAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "配置正在应用", http.StatusConflict)
	}))
	defer srv.Close()
	err := New(srv.URL).Get(context.Background(), "/info", nil)
	if !IsStatus(err, http.StatusConflict) || err.Error() != "配置正在应用" {
		t.Errorf("error %v", err)
	}
	if q := Query("a", "1", "b", "", "c", "x y"); q != "?a=1&c=x+y" {
		t.Errorf("query %q", q)
	}
}

// A stream that connected and then broke starts the reconnection backoff
// over, however long the failures before it made it.
func TestEventsBackoffResetsAfterConnecting(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 10*time.Millisecond, 10*time.Second
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests <= 5 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		// Connected, then the connection breaks mid-stream.
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		payload := "data: {\"type\":\"runtime\",\"data\":{}}\n\n"
		fmt.Fprintf(buf, "%x\r\n%s\r\n", len(payload), payload)
		buf.Flush()
		conn.Close()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ups := make(chan time.Time, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(srv.URL).Events(ctx, func(up bool) {
			if up {
				select {
				case ups <- time.Now():
				default:
				}
			}
		}, func(Event) {})
	}()
	// Stop the follower before the deferred restore of the timings it reads.
	defer func() { cancel(); <-done }()
	var first time.Time
	select {
	case first = <-ups:
	case <-time.After(5 * time.Second):
		t.Fatal("never connected")
	}
	for i := 0; i < 4; i++ {
		select {
		case at := <-ups:
			if at.Sub(first) > 2*time.Second {
				t.Fatalf("reconnection %d after %v", i+1, at.Sub(first))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("reconnection %d waited the grown backoff", i+1)
		}
	}
}

// A stream with heartbeats that stays open but stops sending is dropped
// after eventsIdle and followed again.
func TestEventsReconnectsSilentStream(t *testing.T) {
	defer func(idle, min time.Duration) { eventsIdle, eventsBackoff = idle, min }(eventsIdle, eventsBackoff)
	eventsIdle, eventsBackoff = 100*time.Millisecond, 10*time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n\ndata: {\"type\":\"runtime\",\"data\":{}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ups := make(chan struct{}, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(srv.URL).Events(ctx, func(up bool) {
			if up {
				select {
				case ups <- struct{}{}:
				default:
				}
			}
		}, func(Event) {})
	}()
	// Stop the follower before the deferred restore of the timings it reads.
	defer func() { cancel(); <-done }()
	for i := 0; i < 2; i++ {
		select {
		case <-ups:
		case <-time.After(5 * time.Second):
			t.Fatalf("connection %d never came", i+1)
		}
	}
}

// A server without heartbeats (an older version) is never cut off for
// being quiet.
func TestEventsKeepsQuietStreamWithoutHeartbeat(t *testing.T) {
	defer func(idle time.Duration) { eventsIdle = idle }(eventsIdle)
	eventsIdle = 50 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"runtime\",\"data\":{}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	ups := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(srv.URL).Events(ctx, func(up bool) {
			if up {
				mu.Lock()
				ups++
				mu.Unlock()
			}
		}, func(Event) {})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if ups != 1 {
		t.Fatalf("connected %d times, want 1", ups)
	}
}
