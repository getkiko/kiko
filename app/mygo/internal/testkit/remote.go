package testkit

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// The fake's remote access: GET/PUT /remote, POST /remote/pairing (code
// ABCD-EFGH, five minutes), GET /remote/devices, DELETE
// /remote/devices/{id} and GET /lock.
func (s *Server) serveRemote(key string, w http.ResponseWriter, r *http.Request) bool {
	switch {
	case key == "GET /api/v1/remote":
		JSON(s.Remote())(w, r)
	case key == "PUT /api/v1/remote":
		var settings api.RemoteSettings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		s.mu.Lock()
		s.remote.Enabled, s.remote.Listen = settings.Enabled, settings.Listen
		s.remote.Running = settings.Enabled
		s.remote.Addresses = nil
		if settings.Enabled {
			s.remote.Addresses = []string{"192.168.1.10" + settings.Listen}
		}
		status := s.remote
		s.mu.Unlock()
		JSON(status)(w, r)
	case key == "POST /api/v1/remote/pairing":
		status := s.Remote()
		if !status.Enabled {
			w.WriteHeader(http.StatusConflict)
			JSON(map[string]string{"error": "远程访问未开启"})(w, r)
			return true
		}
		JSON(api.Pairing{Code: "ABCD-EFGH", ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
			Fingerprint: status.Fingerprint,
			URI:         "kiko-pair://192.168.1.10:9443?fp=" + status.Fingerprint + "&code=ABCDEFGH&name=test-host"})(w, r)
	case key == "GET /api/v1/remote/devices":
		s.mu.Lock()
		devices := slices.Clone(s.devices)
		s.mu.Unlock()
		JSON(map[string]any{"devices": devices})(w, r)
	case strings.HasPrefix(key, "DELETE /api/v1/remote/devices/"):
		id := strings.TrimPrefix(key, "DELETE /api/v1/remote/devices/")
		s.mu.Lock()
		s.devices = slices.DeleteFunc(s.devices, func(d api.RemoteDevice) bool { return d.ID == id })
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case key == "GET /api/v1/lock":
		s.mu.Lock()
		lock := s.lock
		s.mu.Unlock()
		JSON(lock)(w, r)
	default:
		return false
	}
	return true
}

// Remote is the remote access state.
func (s *Server) Remote() api.RemoteStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remote
}

// Devices are the paired devices.
func (s *Server) Devices() []api.RemoteDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.devices)
}

// SetLock holds or frees the write gate, broadcasting the lock event as
// the server does.
func (s *Server) SetLock(lock api.LockState) {
	s.mu.Lock()
	s.lock = lock
	s.mu.Unlock()
	s.Emit("lock", lock)
}

// SetToken changes the token a remote fake takes, as revoking this device
// does.
func (s *Server) SetToken(token string) {
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
}

// Fingerprint is a remote fake's certificate fingerprint.
func (s *Server) Fingerprint() []byte { return api.Fingerprint(s.Certificate().Raw) }

// NewRemoteEnv returns an environment for a remote fake (NewTLSServer)
// with token and pin: the console of a paired server named "NAS". A
// Directory shell lets its settings manage servers.
func NewRemoteEnv(srv *Server, token string, pin []byte) (*env.Env, *DirectoryShell) {
	shell := &DirectoryShell{Shell: &Shell{}, Directory: NewDirectory()}
	e := env.New(api.NewWith(srv.URL, api.Options{Token: token, Fingerprint: pin}), shell)
	e.Target = env.Target{ID: "nas", Name: "NAS", URL: srv.URL, Remote: true, Color: "#5856D6"}
	shell.Directory.add(serverEntry("nas", "NAS", srv.URL, api.FormatFingerprint(pin)))
	return e, shell
}
