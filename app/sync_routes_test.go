package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/syncbackend"
)

func syncCall(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func newRouteServer(t *testing.T, backend syncbackend.Backend, clock *testClock) (*server, http.Handler) {
	t.Helper()
	s, err := newServer("/nonexistent", filepath.Join(t.TempDir(), "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.sync.now = clock.Now
	if backend != nil {
		s.sync.newBackend = func(syncbackend.Config) (syncbackend.Backend, error) { return backend, nil }
	}
	return s, s.routes()
}

var kvConfigBody = map[string]any{"backend": map[string]any{"type": "kv", "kv": map[string]any{"url": "https://sync.example.test", "token": "real-token"}}}

func TestSyncRoutesConfigIsValidatedAndSecretsAreMasked(t *testing.T) {
	s, h := newRouteServer(t, &memBackend{}, newTestClock())
	_, status := syncCall(t, h, "GET", "/api/v1/sync/status", nil)
	if status["state"] != "unconfigured" || status["configured"] != false || status["vaultExists"] != nil {
		t.Fatalf("status=%v", status)
	}

	rec, body := syncCall(t, h, "PUT", "/api/v1/sync/config", map[string]any{"backend": map[string]any{"type": "kv", "kv": map[string]any{"url": "http://insecure.example", "token": "x"}}})
	if rec.Code != http.StatusBadRequest || body["code"] != "invalid-config" {
		t.Fatalf("insecure url: %d %v", rec.Code, body)
	}

	rec, body = syncCall(t, h, "PUT", "/api/v1/sync/config", kvConfigBody)
	if rec.Code != http.StatusOK || body["state"] != "needs-setup" || body["vaultExists"] != false {
		t.Fatalf("config: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "real-token") {
		t.Fatalf("secret leaked in response: %s", rec.Body)
	}
	kv := body["backend"].(map[string]any)["kv"].(map[string]any)
	if kv["token"] != syncbackend.Masked {
		t.Fatalf("token must be masked: %v", kv)
	}
	if _, raw := syncCall(t, h, "GET", "/api/v1/sync/status", nil); strings.Contains(toJSON(raw), "real-token") {
		t.Fatal("status leaked a secret")
	}

	masked := map[string]any{"backend": map[string]any{"type": "kv", "kv": map[string]any{"url": "https://sync2.example.test", "token": syncbackend.Masked}}, "autoSync": true}
	if rec, _ = syncCall(t, h, "PUT", "/api/v1/sync/config", masked); rec.Code != http.StatusOK {
		t.Fatalf("masked update: %d", rec.Code)
	}
	if got := s.sync.cfg.Backend.KV; got.Token != "real-token" || got.URL != "https://sync2.example.test" || !s.sync.cfg.AutoSync {
		t.Fatalf("masked secret must keep the stored value: %+v", got)
	}
	info, err := os.Stat(filepath.Join(s.dataDir, syncConfigFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", info, err)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestSyncRoutesUnlockLockResetLifecycle(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	_, h := newRouteServer(t, backend, clock)
	syncCall(t, h, "PUT", "/api/v1/sync/config", kvConfigBody)

	rec, body := syncCall(t, h, "POST", "/api/v1/sync/test", map[string]any{})
	if rec.Code != http.StatusOK || body["ok"] != true || body["vaultExists"] != false {
		t.Fatalf("test: %d %v", rec.Code, body)
	}
	if _, st := syncCall(t, h, "GET", "/api/v1/sync/status", nil); st["state"] != "needs-setup" {
		t.Fatalf("state after test=%v", st["state"])
	}

	rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword})
	if rec.Code != http.StatusNotFound || body["code"] != "vault-missing" {
		t.Fatalf("missing vault: %d %v", rec.Code, body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": "short", "confirm": "short", "create": true})
	if rec.Code != http.StatusBadRequest || body["code"] != "weak-password" {
		t.Fatalf("weak: %d %v", rec.Code, body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword, "confirm": "another password", "create": true})
	if rec.Code != http.StatusBadRequest || body["code"] != "confirm-mismatch" {
		t.Fatalf("mismatch: %d %v", rec.Code, body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": strings.Repeat("x", 300), "confirm": "x", "create": true})
	if rec.Code != http.StatusBadRequest || body["code"] != "password-too-long" {
		t.Fatalf("too long: %d %v", rec.Code, body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword, "confirm": testPassword, "create": true})
	if rec.Code != http.StatusOK || body["state"] != "unlocked" || body["vaultExists"] != true {
		t.Fatalf("create: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), testPassword) {
		t.Fatal("password must never be echoed")
	}

	rec, body = syncCall(t, h, "POST", "/api/v1/sync/now", nil)
	if rec.Code != http.StatusOK || body["result"] == nil || body["status"].(map[string]any)["state"] != "unlocked" {
		t.Fatalf("now: %d %v", rec.Code, body)
	}

	if _, body = syncCall(t, h, "POST", "/api/v1/sync/lock", nil); body["state"] != "locked" {
		t.Fatalf("lock: %v", body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/now", nil)
	if rec.Code != http.StatusConflict || body["code"] != "locked" {
		t.Fatalf("now while locked: %d %v", rec.Code, body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": "definitely wrong"})
	if rec.Code != http.StatusUnauthorized || body["code"] != "wrong-password" {
		t.Fatalf("wrong password: %d %v", rec.Code, body)
	}
	if rec, body = syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword}); rec.Code != http.StatusOK || body["state"] != "unlocked" {
		t.Fatalf("unlock: %d %v", rec.Code, body)
	}

	if rec, _ = syncCall(t, h, "POST", "/api/v1/sync/reset", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("reset without confirm: %d", rec.Code)
	}
	if !backend.exists {
		t.Fatal("unconfirmed reset must not delete anything")
	}
	if rec, body = syncCall(t, h, "POST", "/api/v1/sync/reset", map[string]any{"confirm": true}); rec.Code != http.StatusOK || body["state"] != "needs-setup" || backend.exists {
		t.Fatalf("reset: %d %v exists=%v", rec.Code, body, backend.exists)
	}
}

func TestSyncRoutesChangePasswordAutoAndWorker(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	s, h := newRouteServer(t, backend, clock)
	syncCall(t, h, "PUT", "/api/v1/sync/config", kvConfigBody)
	syncCall(t, h, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword, "confirm": testPassword, "create": true})

	rec, body := syncCall(t, h, "POST", "/api/v1/sync/change-password", map[string]any{"newPassword": "short", "confirm": "short"})
	if rec.Code != http.StatusBadRequest || body["code"] != "weak-password" {
		t.Fatalf("weak new password: %d %v", rec.Code, body)
	}
	rec, body = syncCall(t, h, "POST", "/api/v1/sync/change-password", map[string]any{"newPassword": "a brand new password", "confirm": "not the same"})
	if rec.Code != http.StatusBadRequest || body["code"] != "confirm-mismatch" {
		t.Fatalf("mismatch: %d %v", rec.Code, body)
	}
	if rec, body = syncCall(t, h, "POST", "/api/v1/sync/change-password", map[string]any{"newPassword": "a brand new password", "confirm": "a brand new password"}); rec.Code != http.StatusOK || body["state"] != "unlocked" {
		t.Fatalf("change password: %d %v", rec.Code, body)
	}

	if rec, body = syncCall(t, h, "POST", "/api/v1/sync/auto", map[string]any{"enabled": true}); rec.Code != http.StatusOK || body["autoSync"] != true {
		t.Fatalf("auto: %d %v", rec.Code, body)
	}
	var saved syncConfigFile
	if err := readJSON(filepath.Join(s.dataDir, syncConfigFileName), &saved); err != nil || !saved.AutoSync {
		t.Fatalf("auto sync must be persisted: %+v %v", saved, err)
	}

	rec, body = syncCall(t, h, "GET", "/api/v1/sync/worker", nil)
	commands, _ := body["commands"].([]any)
	if rec.Code != http.StatusOK || body["source"] == "" || len(commands) == 0 || !strings.Contains(toJSON(commands), "wrangler deploy") {
		t.Fatalf("worker: %d %v", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "real-token") {
		t.Fatal("worker template must not contain secrets")
	}
}

func TestSyncRoutesRejectNonJSONWrites(t *testing.T) {
	_, h := newRouteServer(t, &memBackend{}, newTestClock())
	req := httptest.NewRequest("POST", "/api/v1/sync/lock", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("code=%d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/api/v1/sync/lock", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write must be rejected, code=%d", rec.Code)
	}
}

func TestSyncSecondDeviceThroughRoutesSeesWrongPassword(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, ha := newRouteServer(t, backend, clock)
	_, hb := newRouteServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	for _, h := range []http.Handler{ha, hb} {
		syncCall(t, h, "PUT", "/api/v1/sync/config", kvConfigBody)
	}
	syncCall(t, ha, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword, "confirm": testPassword, "create": true})
	before, _ := backend.snapshot()
	rec, body := syncCall(t, hb, "POST", "/api/v1/sync/unlock", map[string]any{"password": "not the password"})
	if rec.Code != http.StatusUnauthorized || body["code"] != "wrong-password" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	if after, _ := backend.snapshot(); string(before) != string(after) {
		t.Fatal("remote changed by a failed unlock")
	}
	if rec, body = syncCall(t, hb, "POST", "/api/v1/sync/unlock", map[string]any{"password": testPassword}); rec.Code != http.StatusOK || body["state"] != "unlocked" {
		t.Fatalf("%d %v", rec.Code, body)
	}
}

// TestSyncRoutesSavingConfigProbesTheRemoteVault checks that saving the config
// also probes the remote, so the UI can tell "a new backup is needed" from
// "a backup exists and needs the password".
func TestSyncRoutesSavingConfigProbesTheRemoteVault(t *testing.T) {
	fresh := &memBackend{}
	_, h := newRouteServer(t, fresh, newTestClock())
	rec, body := syncCall(t, h, "PUT", "/api/v1/sync/config", kvConfigBody)
	if rec.Code != http.StatusOK || body["state"] != "needs-setup" || body["vaultExists"] != false {
		t.Fatalf("new remote: %d %v", rec.Code, body)
	}

	// Another device already created the vault: after saving the same backend
	// here, the UI should prompt for the password directly.
	shared := &memBackend{}
	first, hFirst := newRouteServer(t, shared, newTestClock())
	_ = first
	syncCall(t, hFirst, "PUT", "/api/v1/sync/config", kvConfigBody)
	if rec, _ = syncCall(t, hFirst, "POST", "/api/v1/sync/unlock", map[string]any{"password": "correct-horse-battery", "confirm": "correct-horse-battery", "create": true}); rec.Code != http.StatusOK {
		t.Fatalf("create vault: %d", rec.Code)
	}
	_, hSecond := newRouteServer(t, shared, newTestClock())
	rec, body = syncCall(t, hSecond, "PUT", "/api/v1/sync/config", kvConfigBody)
	if rec.Code != http.StatusOK || body["state"] != "locked" || body["vaultExists"] != true {
		t.Fatalf("existing remote: %d %v", rec.Code, body)
	}

	// When the connection fails, nothing is saved and a readable error is
	// returned to the UI.
	broken := &memBackend{getErr: syncbackend.ErrUnauthorized}
	_, hBroken := newRouteServer(t, broken, newTestClock())
	rec, body = syncCall(t, hBroken, "PUT", "/api/v1/sync/config", kvConfigBody)
	if rec.Code == http.StatusOK {
		t.Fatalf("an unreachable backend must not be saved: %v", body)
	}
	_, status := syncCall(t, hBroken, "GET", "/api/v1/sync/status", nil)
	if status["configured"] != false {
		t.Fatalf("config must stay unsaved: %v", status)
	}
}
