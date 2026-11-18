package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// tlsServer is a remote server: TLS, a Bearer token on every request.
func tlsServer(t *testing.T, token string, handler http.HandlerFunc) (*httptest.Server, []byte, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"未授权：请重新配对此设备"}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, Fingerprint(srv.Certificate().Raw), &seen
}

func TestPinnedClientSendsTokenEverywhere(t *testing.T) {
	srv, fp, seen := tlsServer(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/info":
			fmt.Fprint(w, `{"version":"1","serverName":"nas","client":{"id":"d1","name":"Mac","local":false}}`)
		case "/api/v1/export":
			fmt.Fprint(w, "raw")
		case "/api/v1/items":
			fmt.Fprint(w, "{\"n\":1}\n{\"n\":2}\n")
		case "/api/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"lock\",\"data\":{\"held\":true,\"holder\":{\"id\":\"x\",\"name\":\"NAS\"},\"op\":\"保存配置\"}}\n\n")
		}
	})
	c := NewWith(srv.URL, Options{Token: "tok", Fingerprint: fp})
	ctx := context.Background()
	info, err := c.Info(ctx)
	if err != nil || info.ServerName != "nas" || info.Client == nil || info.Client.Local {
		t.Fatalf("info %+v, %v", info, err)
	}
	if raw, err := c.Raw(ctx, http.MethodGet, "/export"); err != nil || string(raw) != "raw" {
		t.Fatalf("raw %q, %v", raw, err)
	}
	var items int
	if err := c.NDJSON(ctx, http.MethodGet, "/items", nil, false, func(json.RawMessage) error { items++; return nil }); err != nil || items != 2 {
		t.Fatalf("ndjson %d, %v", items, err)
	}
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got := make(chan Event, 1)
	go c.Events(ctx2, nil, func(e Event) {
		select {
		case got <- e:
		default:
		}
	})
	select {
	case e := <-got:
		lock, ok := e.Lock()
		if !ok || !lock.Held || lock.Holder.Name != "NAS" {
			t.Errorf("lock event %+v", lock)
		}
	case <-ctx2.Done():
		t.Fatal("no event")
	}
	cancel()
	for _, s := range *seen {
		if s[len(s)-len("Bearer tok"):] != "Bearer tok" {
			t.Errorf("request without token: %q", s)
		}
	}
	if h := c.Header().Get("Authorization"); h != "Bearer tok" {
		t.Errorf("header %q", h)
	}
	if c.TLSConfig() == nil {
		t.Error("no TLS config for the SSH socket")
	}
}

func TestPinMismatch(t *testing.T) {
	srv, fp, _ := tlsServer(t, "tok", func(w http.ResponseWriter, r *http.Request) {})
	wrong := append([]byte(nil), fp...)
	wrong[0] ^= 0xff
	c := NewWith(srv.URL, Options{Token: "tok", Fingerprint: wrong})
	_, err := c.Info(context.Background())
	var fpErr *FingerprintError
	if !errors.As(err, &fpErr) || !Fatal(err) {
		t.Fatalf("err %v (%T)", err, err)
	}
	if ShortFingerprint(fpErr.Got) != ShortFingerprint(fp) {
		t.Errorf("got fingerprint %x", fpErr.Got)
	}
	// The event stream gives up instead of retrying.
	done := make(chan error, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		c.Follow(ctx, nil, func(err error) { done <- err }, func(Event) {})
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("Follow kept retrying a pin mismatch")
	}
	if err := <-done; !errors.As(err, &fpErr) {
		t.Errorf("failure %v", err)
	}
}

func TestUnauthorizedAndLocked(t *testing.T) {
	srv, fp, _ := tlsServer(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusLocked)
		fmt.Fprint(w, `{"error":"「NAS」正在应用配置，请稍后重试","holder":{"id":"x","name":"NAS"},"op":"应用配置","since":"2026-10-29T10:00:00Z"}`)
	})
	_, err := NewWith(srv.URL, Options{Token: "bad", Fingerprint: fp}).Info(context.Background())
	if !errors.Is(err, ErrUnauthorized) || !IsStatus(err, 401) || !Fatal(err) {
		t.Fatalf("401: %v", err)
	}
	err = NewWith(srv.URL, Options{Token: "tok", Fingerprint: fp}).Send(context.Background(), http.MethodPut, "/store", struct{}{}, nil)
	locked, ok := IsLocked(err)
	if !ok || locked.Lock == nil || locked.Lock.Holder.Name != "NAS" || locked.Lock.Op != "应用配置" {
		t.Fatalf("423: %v %+v", err, locked)
	}
	if err.Error() != "「NAS」正在应用配置，请稍后重试" || Fatal(err) {
		t.Errorf("message %q", err)
	}
}

func TestFetchFingerprintAndPair(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/api/v1/pair" || r.Header.Get("Authorization") != "" || body["code"] != "ABCD-EFGH" {
			http.Error(w, `{"error":"配对码无效或已过期"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"token":"t1","deviceId":"d1","serverName":"nas"}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	fp, err := FetchFingerprint(context.Background(), u.Host)
	if err != nil || ShortFingerprint(fp) != ShortFingerprint(Fingerprint(srv.Certificate().Raw)) {
		t.Fatalf("fingerprint %x, %v", fp, err)
	}
	result, err := Pair(context.Background(), srv.URL, fp, "ABCD-EFGH", "Mac")
	if err != nil || result.Token != "t1" || result.ServerName != "nas" {
		t.Fatalf("pair %+v, %v", result, err)
	}
	if _, err := Pair(context.Background(), srv.URL, fp, "WRONG", "Mac"); err == nil || err.Error() != "配对码无效或已过期" {
		t.Errorf("bad code: %v", err)
	}
}

func TestFingerprintText(t *testing.T) {
	fp := Fingerprint([]byte("x"))
	text := FormatFingerprint(fp)
	back, err := ParseFingerprint(GroupFingerprint(fp))
	if err != nil || FormatFingerprint(back) != text || len(text) != 64 {
		t.Fatalf("round trip %q %v", text, err)
	}
	if _, err := ParseFingerprint("abc"); err == nil {
		t.Error("short fingerprint accepted")
	}
}
