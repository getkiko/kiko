package endpoints

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/websocket"
)

// sshFrame is a message of GET /api/v1/tailscale/ssh, the WebSocket
// that bridges a Tailscale SSH session: start, input and resize go to the
// server; authBanner, ready, output, exit and error come back.
type sshFrame struct {
	Type         string   `json:"type"`
	EndpointTag  string   `json:"endpointTag,omitempty"`
	PeerAddress  string   `json:"peerAddress,omitempty"`
	Username     string   `json:"username,omitempty"`
	Columns      int      `json:"columns,omitempty"`
	Rows         int      `json:"rows,omitempty"`
	TerminalType string   `json:"terminalType,omitempty"`
	HostKeys     []string `json:"hostKeys,omitempty"`
	Data         string   `json:"data,omitempty"`
	Message      string   `json:"message,omitempty"`
	ExitCode     *int     `json:"exitCode,omitempty"`
	Signal       string   `json:"signal,omitempty"`
	ErrorMessage string   `json:"errorMessage,omitempty"`
}

// sshStart is what the session starts with, but its size.
type sshStart struct {
	EndpointTag, PeerAddress, Username, TerminalType string
	HostKeys                                         []string
}

// sshEvents are told what the session reports beside its output. They
// run on the connection's goroutine.
type sshEvents struct {
	Banner func(message string)
	Ready  func()
	Ended  func(status string)
}

// sshConn is a terminal's Conn over the SSH WebSocket: reads give the
// session's output (and the notes of its end), writes send what is
// typed, Resize sends the size; the first size starts the session.
type sshConn struct {
	ws     *websocket.Conn
	start  sshStart
	events sshEvents

	out *io.PipeReader
	pw  *io.PipeWriter

	mu      sync.Mutex
	started bool
	closed  bool
}

// sshURL is the WebSocket URL of an API origin such as
// http://127.0.0.1:9099.
func sshURL(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/tailscale/ssh"
	return u.String(), nil
}

// dialSSH connects to the server's SSH bridge. The loopback server
// accepts the console's own origins only; a remote one takes the device's
// Bearer token in header, over the pinned tlsConfig (nil for none).
func dialSSH(origin string, tlsConfig *tls.Config, header http.Header, start sshStart, events sshEvents) (*sshConn, error) {
	target, err := sshURL(origin)
	if err != nil {
		return nil, err
	}
	config, err := websocket.NewConfig(target, "http://kiko.localhost")
	if err != nil {
		return nil, err
	}
	config.TlsConfig = tlsConfig
	for key, values := range header {
		config.Header[key] = values
	}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		return nil, err
	}
	ws.MaxPayloadBytes = 1 << 20
	pr, pw := io.Pipe()
	conn := &sshConn{ws: ws, start: start, events: events, out: pr, pw: pw}
	go conn.receive()
	return conn, nil
}

func (s *sshConn) send(frame sshFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	return websocket.JSON.Send(s.ws, frame)
}

// receive reads the server's messages until the socket ends.
func (s *sshConn) receive() {
	defer s.pw.Close()
	for {
		var message string
		if err := websocket.Message.Receive(s.ws, &message); err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed && s.events.Ended != nil {
				s.events.Ended("已断开")
			}
			return
		}
		for _, line := range strings.Split(message, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var frame sshFrame
			if json.Unmarshal([]byte(line), &frame) != nil {
				continue
			}
			s.handle(frame)
		}
	}
}

func (s *sshConn) handle(frame sshFrame) {
	switch frame.Type {
	case "authBanner":
		if s.events.Banner != nil {
			s.events.Banner(frame.Message)
		}
	case "ready":
		if s.events.Ready != nil {
			s.events.Ready()
		}
	case "output":
		if data, err := base64.StdEncoding.DecodeString(frame.Data); err == nil {
			_, _ = s.pw.Write(data)
		}
	case "exit":
		text := "\r\n[会话已结束"
		if frame.ExitCode != nil {
			text += fmt.Sprintf("，退出码 %d", *frame.ExitCode)
		}
		if frame.ErrorMessage != "" {
			text += "：" + frame.ErrorMessage
		}
		_, _ = s.pw.Write([]byte(text + "]\r\n"))
		if s.events.Ended != nil {
			s.events.Ended("已断开")
		}
	case "error":
		message := frame.Message
		if message == "" {
			message = "未知错误"
		}
		_, _ = s.pw.Write([]byte("\r\n[错误] " + message + "\r\n"))
		if s.events.Ended != nil {
			s.events.Ended("连接失败")
		}
	}
}

// Read returns the session's output.
func (s *sshConn) Read(p []byte) (int, error) { return s.out.Read(p) }

// Write sends typed bytes.
func (s *sshConn) Write(p []byte) (int, error) {
	if err := s.send(sshFrame{Type: "input", Data: base64.StdEncoding.EncodeToString(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize sends the terminal's size; the first starts the session.
func (s *sshConn) Resize(cols, rows int) error {
	s.mu.Lock()
	started := s.started
	s.started = true
	s.mu.Unlock()
	if !started {
		terminalType := strings.TrimSpace(s.start.TerminalType)
		if terminalType == "" {
			terminalType = "xterm-256color"
		}
		hostKeys := s.start.HostKeys
		if hostKeys == nil {
			hostKeys = []string{}
		}
		return s.send(sshFrame{Type: "start", EndpointTag: s.start.EndpointTag, PeerAddress: s.start.PeerAddress,
			Username: s.start.Username, Columns: cols, Rows: rows, TerminalType: terminalType, HostKeys: hostKeys})
	}
	return s.send(sshFrame{Type: "resize", Columns: cols, Rows: rows})
}

// Close ends the session.
func (s *sshConn) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.out.Close()
	return s.ws.Close()
}
