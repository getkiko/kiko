package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func saveNetworkLibrary(t *testing.T, s *server, configs []networkConfig) {
	t.Helper()
	s.mu.Lock()
	next := s.store
	s.mu.Unlock()
	next.NetworkConfigs, _ = json.Marshal(configs)
	r, _ := syncCall(t, s.routes(), "PUT", "/api/v1/store", next)
	if r.Code != 200 {
		t.Fatalf("save network library: %d %s", r.Code, r.Body.String())
	}
}
func sharedConfig(timeout string, at int64) networkConfig {
	return networkConfig{ID: "shared", Name: "共享网络", Fields: json.RawMessage(`{"connect_timeout":"` + timeout + `","tcp_fast_open":false}`), UpdatedAt: at}
}
func library(t *testing.T, s *server) []networkConfig {
	t.Helper()
	s.mu.Lock()
	raw := append([]byte(nil), s.store.NetworkConfigs...)
	s.mu.Unlock()
	var list []networkConfig
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	return list
}
func TestNetworkConfigStoreAndEncryptedSync(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "home", testProfile{ID: "home", Name: "家庭", Doc: map[string]any{"version": 1, "outbounds": []any{map[string]any{"id": "proxy", "networkConfigRefs": map[string]string{"default": "shared"}}}}})
	saveNetworkLibrary(t, a, []networkConfig{sharedConfig("5s", clock.Now().UnixMilli())})
	reloaded, err := newServer("/nonexistent", a.dataDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(library(t, reloaded)) != 1 {
		t.Fatal("library lost on restart")
	}
	createVault(t, a)
	b := newSyncTestServer(t, backend, clock)
	unlockVault(t, b)
	if got := library(t, b); len(got) != 1 || !strings.Contains(string(got[0].Fields), "5s") {
		t.Fatalf("library missing after encrypted sync: %v", got)
	}
	if !strings.Contains(storeProfileDoc(b, "home"), "shared") {
		t.Fatal("profile reference lost")
	}
	clock.Add(time.Minute)
	saveNetworkLibrary(t, b, []networkConfig{sharedConfig("9s", clock.Now().UnixMilli())})
	mustSync(t, b)
	mustSync(t, a)
	if !strings.Contains(string(library(t, a)[0].Fields), "9s") {
		t.Fatal("global edit failed to sync")
	}
	clock.Add(time.Minute)
	saveNetworkLibrary(t, b, []networkConfig{})
	mustSync(t, b)
	mustSync(t, a)
	if len(library(t, a)) != 0 {
		t.Fatal("deleted library entry resurrected")
	}
}
func TestNetworkConfigConcurrentEditsPreserveConflictCopy(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p", testProfile{ID: "p", Name: "P", Doc: doc(1)})
	saveNetworkLibrary(t, a, []networkConfig{sharedConfig("5s", clock.Now().UnixMilli())})
	createVault(t, a)
	b := newSyncTestServer(t, backend, clock)
	unlockVault(t, b)
	clock.Add(time.Minute)
	saveNetworkLibrary(t, a, []networkConfig{sharedConfig("7s", clock.Now().UnixMilli())})
	clock.Add(time.Minute)
	saveNetworkLibrary(t, b, []networkConfig{sharedConfig("8s", clock.Now().UnixMilli())})
	mustSync(t, a)
	mustSync(t, b)
	mustSync(t, a)
	got := library(t, a)
	if len(got) != 2 {
		t.Fatalf("conflict copies: %v", got)
	}
	if got[0].ID != "shared" || !strings.Contains(string(got[0].Fields), "8s") {
		t.Fatalf("winner/reference ID: %v", got)
	}
	if got[1].ID == "shared" || !strings.Contains(got[1].Name, "冲突副本") {
		t.Fatalf("conflict lost: %v", got)
	}
	a.mu.Lock()
	profiles := parseStoreProfiles(a.store.Profiles)
	a.mu.Unlock()
	if len(profiles) != 1 {
		t.Fatal("network conflict leaked into profiles")
	}
}
func TestNetworkConfigSyncSchemaCompatibility(t *testing.T) {
	for _, v := range []int{1, 2} {
		raw, _ := json.Marshal(map[string]any{"format": syncPayloadFormat, "schema": v, "profiles": []any{}})
		if _, e := parseSyncPayload(raw); e != nil {
			t.Fatalf("schema %d: %v", v, e)
		}
	}
	raw := []byte(`{"format":"kiko-sync","schema":3}`)
	if _, e := parseSyncPayload(raw); e == nil {
		t.Fatal("future schema accepted")
	}
}

func TestTLSConfigEncryptedSyncPreservesRoleAndReferences(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p", testProfile{ID: "p", Name: "P", Doc: map[string]any{"version": 1, "outbounds": []any{map[string]any{"id": "proxy", "networkConfigRefs": map[string]string{"tls": "tls-client"}}}}})
	configs := []networkConfig{
		{ID: "tls-client", Name: "客户端", ConfigKind: "tls-client", Fields: json.RawMessage(`{"tls.min_version":"1.3","tls.fragment":true}`), UpdatedAt: clock.Now().UnixMilli()},
		{ID: "tls-server", Name: "服务端", ConfigKind: "tls-server", Fields: json.RawMessage(`{"tls.key_path":"server.key"}`), UpdatedAt: clock.Now().UnixMilli()},
		sharedConfig("5s", clock.Now().UnixMilli()),
	}
	saveNetworkLibrary(t, a, configs)
	createVault(t, a)
	b := newSyncTestServer(t, backend, clock)
	unlockVault(t, b)
	got := library(t, b)
	if len(got) != 3 {
		t.Fatalf("missing entries: %+v", got)
	}
	kinds := map[string]string{}
	for _, c := range got {
		kinds[c.ID] = c.ConfigKind
	}
	if kinds["tls-client"] != "tls-client" || kinds["tls-server"] != "tls-server" {
		t.Fatalf("TLS role lost: %+v", got)
	}
	if !strings.Contains(storeProfileDoc(b, "p"), `"tls":"tls-client"`) {
		t.Fatal("TLS reference lost")
	}
	clock.Add(time.Minute)
	configs[0].ConfigKind = "tls-server"
	configs[0].UpdatedAt = clock.Now().UnixMilli()
	saveNetworkLibrary(t, b, configs)
	mustSync(t, b)
	mustSync(t, a)
	for _, c := range library(t, a) {
		if c.ID == "tls-client" && c.ConfigKind != "tls-server" {
			t.Fatal("kind-only update lost")
		}
	}
}
