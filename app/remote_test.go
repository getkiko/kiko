package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestValidateLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9099", "localhost:9099", "[::1]:9099", "127.0.0.2:0"} {
		if err := validateLoopbackListen(address); err != nil {
			t.Fatalf("%s rejected: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:9099", ":9099", "192.168.1.2:9099", "[::]:9099", "example.com:9099", "127.0.0.1"} {
		if err := validateLoopbackListen(address); err == nil {
			t.Fatalf("%s accepted", address)
		}
	}
}

func TestNonLoopbackListenRefusesToStart(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The legacy --sing-box alias is still accepted.
	cmd := exec.Command(binary, "--sing-box", binary, "--listen", "0.0.0.0:0", "--data-dir", t.TempDir())
	cmd.Env = append(os.Environ(), "KIKO_LIFETIME_TEST=1")
	hideProcessWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("server started on a non-loopback address:\n%s", output)
	}
	if !strings.Contains(string(output), "--remote-listen") {
		t.Fatalf("missing remote access hint:\n%s", output)
	}
}

type remoteHarness struct {
	t      *testing.T
	s      *server
	local  http.Handler
	base   string
	client *http.Client
	leaf   []byte
}

func newRemoteHarness(t *testing.T) *remoteHarness {
	t.Helper()
	s, err := newServer("/unused/kiko-core", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	h := &remoteHarness{t: t, s: s, local: s.routes()}
	s.remote.setHandler(h.local)
	status := s.remote.configure(remoteConfig{Enabled: true, Listen: "127.0.0.1:0"})
	if !status.Running || status.Error != "" {
		t.Fatalf("remote listener: %+v", status)
	}
	t.Cleanup(s.remote.close)
	h.base = "https://" + s.remote.active.addr.String()
	h.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			h.leaf = state.PeerCertificates[0].Raw
			return nil
		},
	}}}
	return h
}

func (h *remoteHarness) localJSON(method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	h.local.ServeHTTP(recorder, request)
	return recorder
}

func (h *remoteHarness) do(ctx context.Context, method, path, token, body string) *http.Response {
	h.t.Helper()
	request, _ := http.NewRequestWithContext(ctx, method, h.base+path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	return response
}

func (h *remoteHarness) pairingCode() string {
	h.t.Helper()
	recorder := h.localJSON(http.MethodPost, "/api/v1/remote/pairing", "{}")
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("pairing: %d %s", recorder.Code, recorder.Body.String())
	}
	var out struct {
		Code, Fingerprint, URI string
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out)
	return out.Code
}

func (h *remoteHarness) pair(name string) (string, string) {
	h.t.Helper()
	response := h.do(context.Background(), http.MethodPost, "/api/v1/pair", "", `{"code":"`+strings.ToLower(h.pairingCode())+`","deviceName":"`+name+`"}`)
	defer response.Body.Close()
	var out struct {
		Token, DeviceID, ServerName string
	}
	_ = json.NewDecoder(response.Body).Decode(&out)
	if response.StatusCode != http.StatusOK || out.Token == "" || out.DeviceID == "" || out.ServerName == "" {
		h.t.Fatalf("pair: %d %+v", response.StatusCode, out)
	}
	return out.Token, out.DeviceID
}

func decodeBody(t *testing.T, response *http.Response, v any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAuthentication(t *testing.T) {
	h := newRemoteHarness(t)

	var info struct {
		Client struct {
			ID, Name string
			Local    bool
		}
		ServerName   string
		Capabilities map[string]bool
	}
	local := h.localJSON(http.MethodGet, "/api/v1/info", "")
	if err := json.Unmarshal(local.Body.Bytes(), &info); err != nil || local.Code != 200 || !info.Client.Local || info.Client.Name != "本机" || info.ServerName == "" || !info.Capabilities["remoteAccess"] {
		t.Fatalf("loopback info: %d %s", local.Code, local.Body.String())
	}

	for _, token := range []string{"", "wrong"} {
		response := h.do(context.Background(), http.MethodGet, "/api/v1/info", token, "")
		var body map[string]string
		decodeBody(t, response, &body)
		if response.StatusCode != http.StatusUnauthorized || body["error"] != unauthorizedMessage {
			t.Fatalf("token %q: %d %v", token, response.StatusCode, body)
		}
	}

	token, id := h.pair("测试 Mac")
	response := h.do(context.Background(), http.MethodGet, "/api/v1/info", token, "")
	decodeBody(t, response, &info)
	if response.StatusCode != 200 || info.Client.Local || info.Client.ID != id || info.Client.Name != "测试 Mac" {
		t.Fatalf("remote info: %d %+v", response.StatusCode, info)
	}
	sum := sha256.Sum256(h.leaf)
	if hex.EncodeToString(sum[:]) != h.s.remote.status().Fingerprint {
		t.Fatal("served certificate does not match the reported fingerprint")
	}
	// Bearer requests skip the Origin check but keep the Content-Type check.
	request, _ := http.NewRequest(http.MethodPut, h.base+"/api/v1/store", strings.NewReader(`{"revision":0,"doc":{}}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Origin", "https://elsewhere.example")
	request.Header.Set("Content-Type", "application/json")
	if response, err := h.client.Do(request); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated cross-origin write: %v %v", err, response)
	} else {
		response.Body.Close()
	}
	request, _ = http.NewRequest(http.MethodPut, h.base+"/api/v1/store", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	if response, err := h.client.Do(request); err != nil || response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type: %v %v", err, response)
	} else {
		response.Body.Close()
	}
	// The shutdown endpoint stays local even with a valid device token.
	h.s.shutdownToken, h.s.shutdownCancel = "secret", func() {}
	request, _ = http.NewRequest(http.MethodPost, h.base+"/api/v1/shutdown", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Kiko-Shutdown-Token", "secret")
	if response, err := h.client.Do(request); err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("remote shutdown: %v %v", err, response)
	} else {
		response.Body.Close()
	}

	var devices struct {
		Devices []map[string]any `json:"devices"`
	}
	decodeBody(t, h.do(context.Background(), http.MethodGet, "/api/v1/remote/devices", token, ""), &devices)
	if len(devices.Devices) != 1 || devices.Devices[0]["id"] != id || devices.Devices[0]["tokenHash"] != nil {
		t.Fatalf("devices: %+v", devices)
	}
	saved, _ := os.ReadFile(h.s.remote.path("remote-devices.json"))
	if bytes.Contains(saved, []byte(token)) || !bytes.Contains(saved, []byte("tokenHash")) {
		t.Fatalf("device file stores the raw token: %s", saved)
	}
}

func TestRevokedDeviceLosesAccessAndStreams(t *testing.T) {
	h := newRemoteHarness(t)
	token, id := h.pair("phone")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := h.do(ctx, http.MethodGet, "/api/v1/events", token, "")
	defer events.Body.Close()
	if events.StatusCode != http.StatusOK {
		t.Fatalf("events: %d", events.StatusCode)
	}
	reader := bufio.NewReader(events.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first event %q: %v", line, err)
	}
	if recorder := h.localJSON(http.MethodDelete, "/api/v1/remote/devices/"+id, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", recorder.Code, recorder.Body.String())
	}
	closed := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); closed <- err }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("event stream stayed open after revocation")
	}
	response := h.do(context.Background(), http.MethodGet, "/api/v1/runtime", token, "")
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d", response.StatusCode)
	}
	if recorder := h.localJSON(http.MethodDelete, "/api/v1/remote/devices/"+id, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", recorder.Code)
	}
}

func TestPairingEndpointResponses(t *testing.T) {
	h := newRemoteHarness(t)
	recorder := h.localJSON(http.MethodPost, "/api/v1/remote/pairing", "{}")
	var out struct {
		Code, ExpiresAt, Fingerprint, URI string
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &out)
	if len(out.Code) != 9 || out.Code[4] != '-' || len(out.Fingerprint) != 64 {
		t.Fatalf("pairing response: %s", recorder.Body.String())
	}
	if _, err := time.Parse(time.RFC3339, out.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(out.URI)
	if err != nil || uri.Scheme != "kiko-pair" || uri.Port() == "" || uri.Query().Get("fp") != out.Fingerprint || uri.Query().Get("code") != strings.ReplaceAll(out.Code, "-", "") || uri.Query().Get("name") != serverName() {
		t.Fatalf("pairing uri %q: %v", out.URI, err)
	}
	// The pairing route exists only on the remote listener.
	if recorder := h.localJSON(http.MethodPost, "/api/v1/pair", `{"code":"`+out.Code+`"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("loopback pair: %d", recorder.Code)
	}
	response := h.do(context.Background(), http.MethodPost, "/api/v1/pair", "", `{"code":"0000-0000","deviceName":"x"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong code: %d", response.StatusCode)
	}
	h.s.remote.configure(remoteConfig{Enabled: false, Listen: "127.0.0.1:0"})
	if recorder := h.localJSON(http.MethodPost, "/api/v1/remote/pairing", "{}"); recorder.Code != http.StatusConflict {
		t.Fatalf("pairing while disabled: %d", recorder.Code)
	}
}

func TestPairingCodeRules(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := newPairingStore()
	p.now = func() time.Time { return now }

	code, expires := p.issue()
	if !expires.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("expires = %v", expires)
	}
	if err := p.redeem("a", strings.ToLower(code)); err != nil {
		t.Fatalf("valid code: %v", err)
	}
	if err := p.redeem("a", code); err != errPairInvalid {
		t.Fatalf("reused code: %v", err)
	}

	code, _ = p.issue()
	now = now.Add(5 * time.Minute)
	if err := p.redeem("b", code); err != errPairInvalid {
		t.Fatalf("expired code: %v", err)
	}

	code, _ = p.issue()
	for i := 0; i < pairMaxFailures; i++ {
		if err := p.redeem("c", "ZZZZ-ZZZZ"); err != errPairInvalid {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := p.redeem("c", code); err != errPairInvalid {
		t.Fatalf("code survived %d failures: %v", pairMaxFailures, err)
	}

	p.issue()
	for i := 0; i < pairRateLimit; i++ {
		_ = p.redeem("d", "ZZZZ-ZZZZ")
		p.failures = 0
	}
	if err := p.redeem("d", "ZZZZ-ZZZZ"); err != errPairRateLimited {
		t.Fatalf("rate limit: %v", err)
	}
	now = now.Add(pairRateWindow)
	if err := p.redeem("d", "ZZZZ-ZZZZ"); err == errPairRateLimited {
		t.Fatal("rate limit outlived its window")
	}
	if normalizePairCode("abcd-efgh o i l") != "ABCDEFGH011" {
		t.Fatal("Crockford normalization")
	}
}

func TestRemoteCertificateIsStable(t *testing.T) {
	h := newRemoteHarness(t)
	first := h.s.remote.status().Fingerprint
	if len(first) != 64 {
		t.Fatalf("fingerprint = %q", first)
	}
	for _, name := range []string{"remote-cert.pem", "remote-key.pem"} {
		info, err := os.Stat(h.s.remote.path(name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	h.s.remote.configure(remoteConfig{Enabled: false})
	h.s.remote.configure(remoteConfig{Enabled: true, Listen: "127.0.0.1:0"})
	if got := h.s.remote.status().Fingerprint; got != first {
		t.Fatalf("fingerprint changed on restart: %s", got)
	}
	reloaded := newRemoteManager(h.s)
	if got := reloaded.status(); got.Fingerprint != first || !got.Enabled {
		t.Fatalf("reloaded manager: %+v", got)
	}
}

func TestPutRemoteTogglesListener(t *testing.T) {
	s, err := newServer("/unused/kiko-core", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.remote.close()
	handler := s.routes()
	put := func(body string) (int, remoteStatus) {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/remote", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var status remoteStatus
		_ = json.Unmarshal(recorder.Body.Bytes(), &status)
		return recorder.Code, status
	}
	if code, status := put(`{"enabled":true,"listen":"127.0.0.1:0"}`); code != 200 || !status.Running || status.Fingerprint == "" {
		t.Fatalf("enable: %d %+v", code, status)
	}
	if code, status := put(`{"enabled":false,"listen":"127.0.0.1:0"}`); code != 200 || status.Running || status.Enabled {
		t.Fatalf("disable: %d %+v", code, status)
	}
	if code, _ := put(`{"enabled":true,"listen":"nope"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid listen: %d", code)
	}
	var saved remoteConfig
	if err := readJSON(s.remote.path("remote.json"), &saved); err != nil || saved.Enabled {
		t.Fatalf("remote.json: %+v %v", saved, err)
	}
}

func TestPairCommand(t *testing.T) {
	h := newRemoteHarness(t)
	local := httptest.NewServer(h.local)
	defer local.Close()
	address := strings.TrimPrefix(local.URL, "http://")
	h.s.writeLocalAPIAddress(address)
	var stdout, stderr bytes.Buffer
	if code := runPairCommand([]string{"--data-dir", h.s.dataDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	fp := h.s.remote.status().Fingerprint
	if !strings.Contains(stdout.String(), "配对字符串：kiko-pair://") || !strings.Contains(stdout.String(), "证书指纹："+fp[:8]+"\n") {
		t.Fatalf("output:\n%s", stdout.String())
	}
	h.s.remote.configure(remoteConfig{Enabled: false})
	stdout.Reset()
	if code := runPairCommand([]string{"--listen", address}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "--remote-listen") {
		t.Fatalf("disabled remote: exit %d: %s", code, stderr.String())
	}
}

// The default data directory moves from sbcfg-server (before the rename to
// Kiko) to kiko-server once; `pair` reads a legacy directory in place.
func TestResolveDataDirMigratesLegacy(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "sbcfg-server")
	if err := os.MkdirAll(filepath.Join(legacy, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "editor.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dir, err := resolveDataDir(base, false); err != nil || dir != legacy {
		t.Fatalf("without migrate: %q %v", dir, err)
	}
	current := filepath.Join(base, "kiko-server")
	if dir, err := resolveDataDir(base, true); err != nil || dir != current {
		t.Fatalf("migrate: %q %v", dir, err)
	}
	if data, err := os.ReadFile(filepath.Join(current, "editor.json")); err != nil || string(data) != "{}" {
		t.Fatalf("migrated editor.json %q %v", data, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory remains: %v", err)
	}
	// An existing kiko-server wins over a legacy directory.
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if dir, err := resolveDataDir(base, true); err != nil || dir != current {
		t.Fatalf("both present: %q %v", dir, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy directory touched: %v", err)
	}
	empty := t.TempDir()
	if dir, err := resolveDataDir(empty, true); err != nil || dir != filepath.Join(empty, "kiko-server") {
		t.Fatalf("fresh: %q %v", dir, err)
	}
}

// A legacy data directory that cannot move is used where it is.
func TestResolveDataDirKeepsLegacyOnRenameFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only directory")
	}
	base := t.TempDir()
	legacy := filepath.Join(base, "sbcfg-server")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	if dir, err := resolveDataDir(base, true); err != nil || dir != legacy {
		t.Fatalf("dir %q, %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(base, "kiko-server")); !os.IsNotExist(err) {
		t.Fatalf("kiko-server created: %v", err)
	}
}
