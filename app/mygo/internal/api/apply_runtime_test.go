package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runtimeServer answers /info with caps and records the runtime options key
// of each apply; fail makes applies fail with a 422.
func runtimeServer(t *testing.T, caps map[string]bool, fail bool) (*Client, *[]string) {
	t.Helper()
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/info":
			if caps == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(ServerInfo{Version: "x", Capabilities: caps})
		case "/api/v1/config/apply", "/api/v1/apply/live":
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, key := range []string{runtimeOptionsKey, legacyRuntimeOptionsKey} {
				if _, ok := body[key]; ok {
					keys = append(keys, key)
				}
			}
			if fail {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":"unknown outbound type"}`))
				return
			}
			_, _ = w.Write([]byte(`{"applied":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), &keys
}

// A current server gets _kiko_runtime; a server from before the rename,
// without the kikoRuntime capability, gets _sbcfg_runtime, which its core
// accepts. When /info cannot be read the current key is sent.
func TestApplyRuntimeKeyFollowsServer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		caps map[string]bool
		want string
	}{
		{"current", map[string]bool{CapabilityKikoRuntime: true}, runtimeOptionsKey},
		{"outdated", map[string]bool{"remoteAccess": true}, legacyRuntimeOptionsKey},
		{"no info", nil, runtimeOptionsKey},
	} {
		c, keys := runtimeServer(t, tc.caps, false)
		if _, err := c.Apply(ctx, []byte(`{"outbounds":[]}`), ApplyOptions{}); err != nil {
			t.Fatalf("%s: apply: %v", tc.name, err)
		}
		if _, err := c.ApplyLive(ctx, []byte(`{"outbounds":[]}`), ApplyOptions{}); err != nil {
			t.Fatalf("%s: live apply: %v", tc.name, err)
		}
		if len(*keys) != 2 || (*keys)[0] != tc.want || (*keys)[1] != tc.want {
			t.Fatalf("%s: keys %v, want %s", tc.name, *keys, tc.want)
		}
	}
}

// A failed apply on an outdated server says to upgrade it and keeps the
// server's error.
func TestApplyOnOutdatedServerSuggestsUpgrade(t *testing.T) {
	c, _ := runtimeServer(t, map[string]bool{}, true)
	_, err := c.Apply(context.Background(), []byte(`{}`), ApplyOptions{})
	var outdated *OutdatedServerError
	var apiErr *Error
	if !errors.As(err, &outdated) || !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("error %T %v", err, err)
	}
	if !strings.HasPrefix(err.Error(), "服务端版本过旧，请升级") {
		t.Fatalf("message %q", err.Error())
	}
	current, _ := runtimeServer(t, map[string]bool{CapabilityKikoRuntime: true}, true)
	if _, err := current.Apply(context.Background(), []byte(`{}`), ApplyOptions{}); errors.As(err, &outdated) || err == nil {
		t.Fatalf("current server error %v", err)
	}
	if !(ServerInfo{}).Outdated() || (ServerInfo{Capabilities: map[string]bool{CapabilityKikoRuntime: true}}).Outdated() {
		t.Fatal("Outdated")
	}
}

// Applies reuse the server info read on connecting; reconfiguring the client
// reads it again.
func TestApplyReusesCachedInfo(t *testing.T) {
	infos := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/info" {
			infos++
			_ = json.NewEncoder(w).Encode(ServerInfo{Capabilities: map[string]bool{CapabilityKikoRuntime: true}})
			return
		}
		_, _ = w.Write([]byte(`{"applied":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if _, err := c.Info(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := c.Apply(ctx, []byte(`{}`), ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if infos != 1 {
		t.Fatalf("info read %d times", infos)
	}
	c.Reconfigure(Options{})
	if _, err := c.Apply(ctx, []byte(`{}`), ApplyOptions{}); err != nil || infos != 2 {
		t.Fatalf("after reconfigure: %v, %d reads", err, infos)
	}
}

// An info response to a request sent before the cache was dropped (the event
// stream ended meanwhile) is not stored.
func TestInfoCacheDropsStaleResponse(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		_ = json.NewEncoder(w).Encode(ServerInfo{Version: "old"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	done := make(chan error, 1)
	go func() {
		_, err := c.Info(context.Background())
		done <- err
	}()
	<-arrived
	c.info.drop()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if info := c.info.load(); info != nil {
		t.Fatalf("stale info stored: %+v", info)
	}
	gen := c.info.generation()
	c.info.store(gen, ServerInfo{Version: "new"})
	if info := c.info.load(); info == nil || info.Version != "new" {
		t.Fatalf("current info not stored: %+v", info)
	}
}
