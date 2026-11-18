// Package testkit runs the native console's views headless against a
// fake kiko-server, for the workspaces' tests.
package testkit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// Server is a fake kiko-server. It answers /info, /store (with
// revisions), /local-addresses, /runtime and /events by default; Route
// adds or replaces endpoints.
type Server struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	store    api.ServerStore
	runtime  api.Runtime
	streams  []chan string
	requests []string
	saves    int

	// Remote servers (NewTLSServer) take this Bearer token.
	token   string
	remote  api.RemoteStatus
	devices []api.RemoteDevice
	lock    api.LockState

	scripts scriptFake
	fetch   fetchFake
}

// NewServer starts a fake server, closed when the test ends.
func NewServer(t *testing.T) *Server { return newServer(t, "") }

// NewTLSServer starts a fake remote server: TLS with a self-signed
// certificate, every request but POST /pair needing token as a Bearer
// token (401 otherwise).
func NewTLSServer(t *testing.T, token string) *Server { return newServer(t, token) }

func newServer(t *testing.T, token string) *Server {
	t.Helper()
	s := &Server{
		token:  token,
		remote: api.RemoteStatus{Listen: ":9443", Fingerprint: strings.Repeat("ab", 32)},
		devices: []api.RemoteDevice{
			{ID: "dev-1", Name: "MacBook", CreatedAt: "2026-10-01T08:00:00Z", LastSeenAt: time.Now().UTC().Format(time.RFC3339), LastAddr: "192.168.1.20:52144"},
		},
		t:       t,
		routes:  map[string]http.HandlerFunc{},
		store:   api.ServerStore{Revision: 1, Doc: json.RawMessage("null"), Profiles: json.RawMessage("[]")},
		runtime: api.Runtime{State: "stopped"},
	}
	if token != "" {
		s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	} else {
		s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	}
	t.Cleanup(func() {
		s.mu.Lock()
		for _, ch := range s.streams {
			close(ch)
		}
		s.streams = nil
		s.mu.Unlock()
		s.Server.CloseClientConnections()
		s.Server.Close()
	})
	return s
}

// Route answers requests of method whose path starts with /api/v1+prefix,
// the longest matching prefix first.
func (s *Server) Route(method, prefix string, handler http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" /api/v1"+prefix] = handler
}

// Unroute removes a handler Route installed, restoring the default.
func (s *Server) Unroute(method, prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, method+" /api/v1"+prefix)
}

// JSON is a handler answering v as JSON.
func JSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Requests returns "METHOD path?query" of the requests so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Saves counts successful PUT /store requests.
func (s *Server) Saves() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

// SetStore replaces the stored state, as another client's save would.
func (s *Server) SetStore(store api.ServerStore) {
	s.mu.Lock()
	s.store = store
	s.mu.Unlock()
}

// Store returns the stored state.
func (s *Server) Store() api.ServerStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store
}

// SetRuntime sets what /runtime answers.
func (s *Server) SetRuntime(runtime api.Runtime) {
	s.mu.Lock()
	s.runtime = runtime
	s.mu.Unlock()
}

// Emit sends an event to every open event stream.
func (s *Server) Emit(eventType string, data any) {
	raw, err := json.Marshal(map[string]any{"type": eventType, "data": data})
	if err != nil {
		s.t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.streams {
		ch <- string(raw)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	if s.token != "" && r.URL.Path != "/api/v1/pair" && r.Header.Get("Authorization") != "Bearer "+s.token {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"未授权：请重新配对此设备"}`)
		return
	}
	key := r.Method + " " + r.URL.Path
	var match string
	var handler http.HandlerFunc
	keys := make([]string, 0, len(s.routes))
	for k := range s.routes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if strings.HasPrefix(key, k) {
			match, handler = k, s.routes[k]
			break
		}
	}
	s.mu.Unlock()
	if handler != nil && match != "" {
		handler(w, r)
		return
	}
	switch key {
	case "GET /api/v1/info":
		client := &api.InfoClient{ID: "local", Name: "本机", Local: true}
		if s.token != "" {
			client = &api.InfoClient{ID: "dev-self", Name: "测试设备"}
		}
		JSON(api.ServerInfo{Version: "test", OS: "linux", Arch: "amd64", PID: 1, SingBox: api.SingBoxInfo{Version: "1.14.0"},
			ServerName: "test-host", Client: client,
			Capabilities: map[string]bool{"kikoRuntime": true, "remoteAccess": true, "writeLock": true, "fetchPublic": true, "fetchPublicRedirects": true}})(w, r)
	case "GET /api/v1/store":
		s.mu.Lock()
		store := s.store
		s.mu.Unlock()
		JSON(store)(w, r)
	case "PUT /api/v1/store":
		var next api.ServerStore
		if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if next.Revision != s.store.Revision {
			current := s.store
			s.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			JSON(current)(w, r)
			return
		}
		next.Revision++
		s.store = next
		s.saves++
		s.mu.Unlock()
		JSON(next)(w, r)
	case "GET /api/v1/local-addresses":
		JSON(map[string]any{"addresses": []string{"192.168.1.10/24"}})(w, r)
	case "GET /api/v1/runtime":
		s.mu.Lock()
		runtime := s.runtime
		s.mu.Unlock()
		JSON(runtime)(w, r)
	case "POST /api/v1/apply/live":
		JSON(map[string]any{"applied": true, "live": true})(w, r)
	case "GET /api/v1/events":
		s.events(w, r)
	default:
		if s.serveRemote(key, w, r) || s.serveScripts(key, w, r) || s.serveFetch(key, w, r) {
			return
		}
		http.Error(w, "not found: "+key, http.StatusNotFound)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	ch := make(chan string, 64)
	s.mu.Lock()
	s.streams = append(s.streams, ch)
	runtime := s.runtime
	s.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"type": "runtime", "data": runtime})
	fmt.Fprintf(w, "data: %s\n\n", raw)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// Shell is a fake desktop shell recording what it was asked.
type Shell struct {
	mu sync.Mutex
	// Restarts counts RestartServer calls.
	Restarts int
	Saved    map[string][]byte
	// Open is what OpenFile returns.
	Open     []byte
	OpenName string
	// OpenErr is the error OpenFile fails with.
	OpenErr error
	Opened  []string
}

func (s *Shell) Platform() string { return "linux" }

// RestartServer counts a restart of the server process.
func (s *Shell) RestartServer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Restarts++
}

// RestartCount reports the RestartServer calls.
func (s *Shell) RestartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Restarts
}
func (s *Shell) OpenLogsDirectory() error { return nil }
func (s *Shell) HelperStatus() (env.HelperStatus, error) {
	return env.HelperStatus{}, fmt.Errorf("仅 macOS 提供特权助手")
}
func (s *Shell) InstallHelper() (bool, error) {
	return false, fmt.Errorf("仅 macOS 提供特权助手")
}
func (s *Shell) AppIcon(string) []byte { return nil }
func (s *Shell) SaveFile(name string, data []byte, _ ...env.FileFilter) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Saved == nil {
		s.Saved = map[string][]byte{}
	}
	s.Saved[name] = data
	return true, nil
}

// SavedFile returns what SaveFile wrote under name.
func (s *Shell) SavedFile(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.Saved[name]
	return data, ok
}

// SavedNames returns the names SaveFile wrote, in no order.
func (s *Shell) SavedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.Saved))
	for name := range s.Saved {
		names = append(names, name)
	}
	return names
}

func (s *Shell) OpenFile(...env.FileFilter) ([]byte, string, bool, error) {
	if s.OpenErr != nil {
		return nil, "", false, s.OpenErr
	}
	return s.Open, s.OpenName, s.Open != nil, nil
}
func (s *Shell) OpenPath(path string) error {
	s.mu.Lock()
	s.Opened = append(s.Opened, path)
	s.mu.Unlock()
	return nil
}
func (s *Shell) OpenURL(url string) error        { return s.OpenPath(url) }
func (s *Shell) RevealPath(path string) error    { return s.OpenPath(path) }
func (s *Shell) OpenInConsole(path string) error { return s.OpenPath(path) }

// NewEnv returns an environment talking to srv with a fake shell.
func NewEnv(srv *Server) (*env.Env, *Shell) {
	shell := &Shell{}
	return env.New(api.New(srv.URL), shell), shell
}

// Until runs frames of tt until cond holds, failing the test after ten
// seconds: the view's requests run on goroutines against a real HTTP
// server, so their results arrive in real time.
func Until(t *testing.T, tt *ui.Tester, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tt.Frame()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met; texts: %q", tt.Texts())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// UntilText waits until tt shows text.
func UntilText(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	Until(t, tt, func() bool { return tt.HasText(text) })
}

// Body reads a request's JSON body into v.
func Body(r *http.Request, v any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
