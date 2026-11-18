package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Run with a freshly built KIKO_TEST_CORE. All traffic stays on loopback.
func TestSelectorSwitchCoreEndToEnd(t *testing.T) {
	binary := os.Getenv("KIKO_TEST_CORE")
	if binary == "" {
		t.Skip("set KIKO_TEST_CORE to verify real selector switching")
	}
	for _, protocol := range []string{"direct", "vless-mux", "vless-vision", "vless-reality"} {
		for _, transport := range []string{"grpc", "clash"} {
			t.Run(protocol+"/"+transport, func(t *testing.T) {
				testSelectorSwitchCore(t, binary, protocol, transport)
			})
		}
	}
}

func testSelectorSwitchCore(t *testing.T, binary, protocol, transport string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	certificateServer := httptest.NewUnstartedServer(http.NotFoundHandler())
	certificateServer.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	certificateServer.EnableHTTP2 = true
	certificateServer.StartTLS()
	t.Cleanup(certificateServer.Close)
	certificate := certificateServer.TLS.Certificates[0]
	vision := protocol == "vless-vision" || protocol == "vless-reality"
	if vision {
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}})
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	freePort := func() int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	apiPort, inboundPort, clashPort := freePort(), freePort(), freePort()
	config := map[string]any{
		"log":      map[string]any{"level": "debug"},
		"inbounds": []any{map[string]any{"type": "direct", "tag": "test-in", "listen": "127.0.0.1", "listen_port": inboundPort, "override_address": "127.0.0.1", "override_port": listener.Addr().(*net.TCPAddr).Port}},
		"outbounds": []any{
			map[string]any{"type": "selector", "tag": "proxy", "outbounds": []string{"nested", "b"}, "interrupt_exist_connections": true},
			map[string]any{"type": "selector", "tag": "nested", "outbounds": []string{"a", "b"}, "interrupt_exist_connections": true},
			map[string]any{"type": "direct", "tag": "a"},
			map[string]any{"type": "direct", "tag": "b"},
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route":        map[string]any{"final": "proxy", "rules": []any{map[string]any{"inbound": []string{"proxy-in"}, "outbound": "direct"}}},
		"services":     []any{map[string]any{"type": "api", "listen": "127.0.0.1", "listen_port": apiPort, "secret": testSecret}},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:" + strconv.Itoa(clashPort), "secret": testSecret}},
	}
	if strings.HasPrefix(protocol, "vless-") {
		proxyPort := freePort()
		realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		user := map[string]any{"uuid": "00000000-0000-4000-8000-000000000001"}
		inbound := map[string]any{"type": "vless", "tag": "proxy-in", "listen": "127.0.0.1", "listen_port": proxyPort, "users": []any{user}}
		if protocol == "vless-reality" {
			handshake := certificateServer.Listener.Addr().(*net.TCPAddr)
			inbound["tls"] = map[string]any{"enabled": true, "server_name": "example.com", "reality": map[string]any{
				"enabled": true, "private_key": base64.RawURLEncoding.EncodeToString(realityKey.Bytes()), "short_id": []string{"00"},
				"handshake": map[string]any{"server": "127.0.0.1", "server_port": handshake.Port},
			}}
			user["flow"] = "xtls-rprx-vision"
		} else if vision {
			key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			inbound["tls"] = map[string]any{"enabled": true, "certificate": []string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}))}, "key": []string{string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}}
			user["flow"] = "xtls-rprx-vision"
		} else {
			inbound["multiplex"] = map[string]any{"enabled": true}
		}
		config["inbounds"] = append(config["inbounds"].([]any), inbound)
		for i, tag := range []string{"a", "b"} {
			outbound := map[string]any{"type": "vless", "tag": tag, "server": "127.0.0.1", "server_port": proxyPort, "uuid": "00000000-0000-4000-8000-000000000001"}
			if protocol == "vless-reality" {
				outbound["flow"] = "xtls-rprx-vision"
				outbound["tls"] = map[string]any{"enabled": true, "server_name": "example.com", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{
					"enabled": true, "public_key": base64.RawURLEncoding.EncodeToString(realityKey.PublicKey().Bytes()), "short_id": "00",
				}}
			} else if vision {
				outbound["flow"] = "xtls-rprx-vision"
				outbound["tls"] = map[string]any{"enabled": true, "insecure": true}
			} else {
				outbound["multiplex"] = map[string]any{"enabled": true}
			}
			config["outbounds"].([]any)[i+2] = outbound
		}
	}
	content, _ := json.Marshal(config)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", configPath)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	rpcConn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://127.0.0.1:" + strconv.Itoa(apiPort), Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rpcConn.Close() })
	client := daemon.NewStartedServiceClient(rpcConn)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		if _, err := client.GetVersion(ctx, &emptypb.Empty{}); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("core API did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := newGRPCTestServer(t)
	if transport == "grpc" {
		s.rpc.client = client
	}
	s.clashAddr, s.clashSecret = "127.0.0.1:"+strconv.Itoa(clashPort), testSecret
	snapshot := func() []*daemon.ConnectionEvent {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stream, err := client.SubscribeConnections(ctx, &daemon.SubscribeConnectionsRequest{Interval: int64(20 * time.Millisecond)})
		if err != nil {
			t.Fatal(err)
		}
		message, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		return message.Events
	}
	open := func() net.Conn {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(inboundPort), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if vision {
			conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
		}
		if _, err = conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 4)
		if _, err = io.ReadFull(conn, response); err != nil || string(response) != "ping" {
			t.Fatal("echo failed", err)
		}
		// Exercise sustained forwarding, including Vision's direct TLS data path.
		payload := bytes.Repeat([]byte("selector-switch-stream"), 2048)
		response = make([]byte, len(payload))
		for range 8 {
			if _, err = conn.Write(payload); err != nil {
				t.Fatal("stream write failed", err)
			}
			if _, err = io.ReadFull(conn, response); err != nil || !bytes.Equal(response, payload) {
				t.Fatal("stream echo failed", err)
			}
		}
		return conn
	}
	for _, group := range []string{"nested", "proxy"} {
		conn := open()
		var id string
		for _, event := range snapshot() {
			c := event.Connection
			if c != nil && c.Inbound == "test-in" && c.Source == conn.LocalAddr().String() && c.ClosedAt == 0 {
				id = event.Id
			}
		}
		if id == "" {
			t.Fatal("missing live connection before switch")
		}
		w := httptest.NewRecorder()
		s.setSelector(w, httptest.NewRequest(http.MethodPatch, "/api/v1/runtime/selector", strings.NewReader(`{"selector":"`+group+`","outbound":"b"}`)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("selector request %d: %s", w.Code, w.Body.String())
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
			t.Fatalf("%s switch kept the old client connection alive: %v", group, err)
		}
		eventually(t, "old connection closed in the official monitor", func() bool {
			for _, event := range snapshot() {
				if event.Id == id && event.Connection != nil {
					return event.Connection.ClosedAt > 0
				}
			}
			return false
		})
	}
	open()
}
