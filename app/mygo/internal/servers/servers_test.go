package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// memSecrets is a credential store in memory; unsupported, a system
// without one.
type memSecrets struct {
	mu          sync.Mutex
	values      map[string][]byte
	unsupported bool
}

func (m *memSecrets) Set(key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return fmt.Errorf("no keyring: %w", errors.ErrUnsupported)
	}
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	m.values[key] = value
	return nil
}

func (m *memSecrets) Get(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return nil, fmt.Errorf("no keyring: %w", errors.ErrUnsupported)
	}
	v, ok := m.values[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return v, nil
}

func (m *memSecrets) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unsupported {
		return fmt.Errorf("no keyring: %w", errors.ErrUnsupported)
	}
	delete(m.values, key)
	return nil
}

// pairServer is a remote server accepting the code ABCD-EFGH once per
// token it hands out, and revocations.
type pairServer struct {
	*httptest.Server
	mu      sync.Mutex
	tokens  int
	revoked []string
}

func newPairServer(t *testing.T) *pairServer {
	p := &pairServer{}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pair":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Dashes and case are ignored, as the server does.
			if strings.ToUpper(strings.ReplaceAll(body["code"], "-", "")) != "ABCDEFGH" || body["deviceName"] != "Mac" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"配对码无效或已过期"}`)
				return
			}
			p.tokens++
			fmt.Fprintf(w, `{"token":"tok%d","deviceId":"dev%d","serverName":"nas"}`, p.tokens, p.tokens)
		case r.Method == http.MethodDelete:
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			p.revoked = append(p.revoked, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pairServer) uri() string {
	u, _ := url.Parse(p.URL)
	fp := api.FormatFingerprint(api.Fingerprint(p.Certificate().Raw))
	return "kiko-pair://" + u.Host + "?fp=" + fp + "&code=ABCDEFGH&name=" + url.QueryEscape("家里 NAS")
}

func TestParsePairURI(t *testing.T) {
	fp := api.FormatFingerprint(api.Fingerprint([]byte("x")))
	p, err := ParsePairURI("kiko-pair://nas.lan:9443?fp=" + fp + "&code=ABCD-EFGH&name=%E5%AE%B6")
	if err != nil || p.Host != "nas.lan:9443" || p.Code != "ABCD-EFGH" || p.Name != "家" || p.URL() != "https://nas.lan:9443" {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := ParsePairURI("kiko-pair://[fd00::1]?fp=" + fp + "&code=X"); err != nil || p.Host != "[fd00::1]:9443" {
		t.Errorf("default port: %+v %v", p, err)
	}
	// Servers from before the rename to Kiko print sbcfg-pair:// strings.
	if p, err := ParsePairURI("sbcfg-pair://nas.lan:9443?fp=" + fp + "&code=X"); err != nil || p.Host != "nas.lan:9443" {
		t.Errorf("legacy scheme: %+v %v", p, err)
	}
	if !IsPairURI(" SBCFG-PAIR://nas") || !IsPairURI("kiko-pair://nas") || IsPairURI("nas:9443") {
		t.Error("IsPairURI")
	}
	for _, bad := range []string{"https://nas:9443?fp=" + fp + "&code=X", "kiko-pair://nas:9443?code=X", "kiko-pair://nas:9443?fp=" + fp} {
		if _, err := ParsePairURI(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for in, want := range map[string]string{"nas": "nas:9443", "nas:1234": "nas:1234", "https://10.0.0.2:9443/": "10.0.0.2:9443", "fd00::1": "[fd00::1]:9443"} {
		if got, err := HostPort(in); err != nil || got != want {
			t.Errorf("HostPort(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := HostPort("http://nas:9099"); err == nil {
		t.Error("accepted http")
	}
}

func TestPairRenameRemove(t *testing.T) {
	srv := newPairServer(t)
	dir := t.TempDir()
	secrets := &memSecrets{}
	d, err := Open(dir, Server{URL: "http://127.0.0.1:9099"}, secrets, "Mac")
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	d.OnChange(func() { changes++ })
	s, err := d.Add(context.Background(), srv.uri())
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "nas" || s.URL != srv.URL || s.DeviceID != "dev1" || s.Color == "" || !s.Remote() {
		t.Errorf("added %+v", s)
	}
	if token, err := d.Token(s.ID); err != nil || token != "tok1" {
		t.Errorf("token %q %v", token, err)
	}
	if string(secrets.values["server:"+s.ID]) != "tok1" {
		t.Errorf("token not in the credential store: %v", secrets.values)
	}
	// Pairing again at the same address keeps the entry.
	again, err := d.Add(context.Background(), srv.uri())
	if err != nil || again.ID != s.ID || len(d.List()) != 2 {
		t.Fatalf("re-pair %+v %v; list %+v", again, err, d.List())
	}
	if err := d.Rename(s.ID, "书房"); err != nil {
		t.Fatal(err)
	}
	// The list survives a restart; tokens stay out of servers.json.
	d2, err := Open(dir, Server{}, secrets, "Mac")
	if err != nil {
		t.Fatal(err)
	}
	list := d2.List()
	if len(list) != 2 || list[0].ID != LocalID || list[0].Name != "本机" || list[1].Name != "书房" {
		t.Fatalf("reopened %+v", list)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "servers.json"))
	if !json.Valid(raw) || bytes.Contains(raw, []byte("tok")) {
		t.Errorf("servers.json holds a token: %s", raw)
	}
	client, err := d2.Client(s.ID)
	if err != nil || client.Header().Get("Authorization") != "Bearer tok2" || client.TLSConfig() == nil {
		t.Fatalf("client %v", err)
	}
	if err := d2.Remove(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if len(d2.List()) != 1 || len(srv.revoked) != 1 || srv.revoked[0] != "/api/v1/remote/devices/dev2" {
		t.Errorf("after remove: %+v, revoked %v", d2.List(), srv.revoked)
	}
	if _, err := d2.Token(s.ID); !errors.Is(err, ErrNoToken) {
		t.Errorf("token kept: %v", err)
	}
	if changes < 3 {
		t.Errorf("changes %d", changes)
	}
}

func TestAddManualTrustsOnFirstUse(t *testing.T) {
	srv := newPairServer(t)
	d, _ := Open(t.TempDir(), Server{}, &memSecrets{}, "Mac")
	u, _ := url.Parse(srv.URL)
	want := api.Fingerprint(srv.Certificate().Raw)
	if _, err := d.AddManual(context.Background(), u.Host, "ABCD-EFGH", func([]byte) bool { return false }); err == nil {
		t.Fatal("paired without confirmation")
	}
	var shown []byte
	s, err := d.AddManual(context.Background(), u.Host, "ABCD-EFGH", func(fp []byte) bool { shown = fp; return true })
	if err != nil || s.Fingerprint != api.FormatFingerprint(want) || api.ShortFingerprint(shown) != api.ShortFingerprint(want) {
		t.Fatalf("manual %+v %v", s, err)
	}
	// Pairing again with a code: the pinned certificate needs no
	// confirmation.
	if _, err := d.Repair(context.Background(), s.ID, "ABCD-EFGH", nil); err != nil {
		t.Fatal(err)
	}
	if token, _ := d.Token(s.ID); token != "tok2" {
		t.Errorf("token after repair %q", token)
	}
}

func TestTokensFallBackToFile(t *testing.T) {
	srv := newPairServer(t)
	dir := t.TempDir()
	d, _ := Open(dir, Server{}, &memSecrets{unsupported: true}, "Mac")
	s, err := d.Add(context.Background(), srv.uri())
	if err != nil {
		t.Fatal(err)
	}
	if !d.InsecureTokens() {
		t.Error("fallback not reported")
	}
	info, err := os.Stat(filepath.Join(dir, "server-tokens.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v %v", info, err)
	}
	d2, _ := Open(dir, Server{}, &memSecrets{unsupported: true}, "Mac")
	if token, err := d2.Token(s.ID); err != nil || token != "tok1" {
		t.Errorf("token %q %v", token, err)
	}
}

func TestRestoreWindows(t *testing.T) {
	srv := newPairServer(t)
	dir := t.TempDir()
	d, _ := Open(dir, Server{}, &memSecrets{}, "Mac")
	s, _ := d.Add(context.Background(), srv.uri())
	_ = d.SetRestore(true)
	d.SetOpen([]string{LocalID, s.ID, "gone"})
	d2, _ := Open(dir, Server{}, &memSecrets{}, "Mac")
	on, open := d2.Restore()
	if !on || len(open) != 1 || open[0] != s.ID {
		t.Errorf("restore %v %v", on, open)
	}
}

// Tokens stored under the credential store service used before the rename
// to Kiko move to the current service on first read.
func TestMigratingSecretsMovesLegacyTokens(t *testing.T) {
	current, legacy := &memSecrets{}, &memSecrets{}
	if err := legacy.Set("srv_1", []byte("token-1")); err != nil {
		t.Fatal(err)
	}
	store := MigratingSecrets(current, legacy)
	value, err := store.Get("srv_1")
	if err != nil || string(value) != "token-1" {
		t.Fatalf("Get = %q, %v", value, err)
	}
	if moved, err := current.Get("srv_1"); err != nil || string(moved) != "token-1" {
		t.Fatalf("current = %q, %v", moved, err)
	}
	if _, err := legacy.Get("srv_1"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("legacy copy remains: %v", err)
	}
	if _, err := store.Get("missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	// The current value wins over a legacy one.
	_ = legacy.Set("srv_2", []byte("old"))
	_ = current.Set("srv_2", []byte("new"))
	if value, _ := store.Get("srv_2"); string(value) != "new" {
		t.Fatalf("srv_2 = %q", value)
	}
	if err := store.Delete("srv_2"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*memSecrets{current, legacy} {
		if _, err := s.Get("srv_2"); !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("srv_2 remains after Delete: %v", err)
		}
	}
}

// Without a credential store the unsupported error passes through, so
// the directory falls back to its token file.
func TestMigratingSecretsUnsupported(t *testing.T) {
	store := MigratingSecrets(&memSecrets{unsupported: true}, &memSecrets{unsupported: true})
	if _, err := store.Get("srv_1"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Get: %v", err)
	}
	if err := store.Set("srv_1", []byte("x")); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Set: %v", err)
	}
}
