package endpoints

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"golang.org/x/net/websocket"
)

// newHTTPServer serves handler for the test and returns its origin.
func newHTTPServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSSHURL(t *testing.T) {
	for origin, want := range map[string]string{
		"http://127.0.0.1:9099":   "ws://127.0.0.1:9099/api/v1/tailscale/ssh",
		"https://example.com/":    "wss://example.com/api/v1/tailscale/ssh",
		"http://kiko.localhost:1": "ws://kiko.localhost:1/api/v1/tailscale/ssh",
	} {
		if got, err := sshURL(origin); err != nil || got != want {
			t.Errorf("sshURL(%q) = %q, %v", origin, got, err)
		}
	}
}

// TestSSHConn runs a session against a fake bridge: the first size
// starts it, typing sends input, output reaches the terminal, the end is
// written to it.
func TestSSHConn(t *testing.T) {
	var mu sync.Mutex
	var frames []sshFrame
	var seenOrigin string
	origin := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tailscale/ssh" {
			http.NotFound(w, r)
			return
		}
		websocket.Server{Handshake: func(c *websocket.Config, r *http.Request) (err error) {
			c.Origin, err = websocket.Origin(c, r)
			return err
		}, Handler: func(ws *websocket.Conn) {
			mu.Lock()
			seenOrigin = ws.Config().Origin.String()
			mu.Unlock()
			for {
				var frame sshFrame
				if websocket.JSON.Receive(ws, &frame) != nil {
					return
				}
				mu.Lock()
				frames = append(frames, frame)
				mu.Unlock()
				switch frame.Type {
				case "start":
					_ = websocket.JSON.Send(ws, sshFrame{Type: "authBanner", Message: "visit https://login.example/x"})
					_ = websocket.JSON.Send(ws, sshFrame{Type: "ready"})
				case "input":
					data, _ := base64.StdEncoding.DecodeString(frame.Data)
					_ = websocket.JSON.Send(ws, sshFrame{Type: "output", Data: base64.StdEncoding.EncodeToString([]byte("echo:" + string(data)))})
					code := 0
					_ = websocket.JSON.Send(ws, sshFrame{Type: "exit", ExitCode: &code})
				}
			}
		}}.ServeHTTP(w, r)
	})
	var banner, ended string
	var ready bool
	events := sshEvents{
		Banner: func(m string) { mu.Lock(); banner = m; mu.Unlock() },
		Ready:  func() { mu.Lock(); ready = true; mu.Unlock() },
		Ended:  func(s string) { mu.Lock(); ended = s; mu.Unlock() },
	}
	conn, err := dialSSH(origin, nil, nil, sshStart{EndpointTag: "ts", PeerAddress: "100.64.0.2", Username: "root", HostKeys: []string{"key"}}, events)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Resize(80, 24); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if err := conn.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	buf := make([]byte, 256)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "会话已结束，退出码 0") && time.Now().Before(deadline) {
		n, err := conn.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	if !strings.Contains(out.String(), "echo:ls\r") || !strings.Contains(out.String(), "[会话已结束，退出码 0]") {
		t.Fatalf("output %q", out.String())
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		done := ended != ""
		mu.Unlock()
		if done {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !ready || !strings.Contains(banner, "login.example") || ended != "已断开" {
		t.Fatalf("events ready=%v banner=%q ended=%q", ready, banner, ended)
	}
	if seenOrigin != "http://kiko.localhost" {
		t.Fatalf("origin %q", seenOrigin)
	}
	raw, _ := json.Marshal(frames)
	if len(frames) < 3 || frames[0].Type != "start" || frames[0].Columns != 80 || frames[0].Rows != 24 ||
		frames[0].TerminalType != "xterm-256color" || frames[0].Username != "root" || frames[1].Type != "input" || frames[2].Type != "resize" {
		t.Fatalf("frames %s", raw)
	}
}

// A remote server's bridge is reached over the pinned TLS with the
// device's token.
func TestSSHConnRemote(t *testing.T) {
	auth := make(chan string, 1)
	srv := httptest.NewTLSServer(websocket.Server{Handshake: func(c *websocket.Config, r *http.Request) error {
		auth <- r.Header.Get("Authorization")
		return nil
	}, Handler: func(ws *websocket.Conn) { _ = websocket.JSON.Send(ws, sshFrame{Type: "ready"}) }})
	defer srv.Close()
	client := api.NewWith(srv.URL, api.Options{Token: "tok", Fingerprint: api.Fingerprint(srv.Certificate().Raw)})
	ready := make(chan struct{}, 1)
	conn, err := dialSSH(client.Origin(), client.TLSConfig(), client.Header(), sshStart{}, sshEvents{Ready: func() { ready <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := <-auth; got != "Bearer tok" {
		t.Errorf("authorization %q", got)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("no ready frame")
	}
	wrong := api.NewWith(srv.URL, api.Options{Fingerprint: make([]byte, 32)})
	if _, err := dialSSH(wrong.Origin(), wrong.TLSConfig(), wrong.Header(), sshStart{}, sshEvents{}); err == nil {
		t.Error("dialed a server with another certificate")
	}
}
