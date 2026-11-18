package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
)

// Local raw TCP echo exercises real core traffic events, final close accounting,
// durable storage and replay. It needs no external network or elevated rights.
func TestUsageCoreEndToEnd(t *testing.T) {
	binary := os.Getenv("KIKO_USAGE_TEST_CORE")
	if binary == "" {
		t.Skip("set KIKO_USAGE_TEST_CORE to verify real-core usage accounting")
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
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
	apiPort, inboundPort := freePort(), freePort()
	config := map[string]any{
		"log":       map[string]any{"disabled": true},
		"inbounds":  []any{map[string]any{"type": "direct", "tag": "usage-in", "listen": "127.0.0.1", "listen_port": inboundPort, "override_address": "127.0.0.1", "override_port": echo.Addr().(*net.TCPAddr).Port}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "direct"},
		"services":  []any{map[string]any{"type": "api", "tag": "api", "listen": "127.0.0.1", "listen_port": apiPort, "secret": testSecret}},
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
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://127.0.0.1:" + strconv.Itoa(apiPort), Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.usage = newUsageStore(s.dataDir)
	run := time.Now()
	collect := func() (context.CancelFunc, <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.collectUsageConnections(ctx, daemon.NewStartedServiceClient(conn), run) }()
		return cancel, done
	}
	cancel, done := collect()
	defer cancel()
	eventually(t, "real core usage subscription", func() bool { s.usage.mu.Lock(); defer s.usage.mu.Unlock(); return s.usage.connected })
	payload := bytes.Repeat([]byte("usage"), 1024)
	transfer := func() {
		t.Helper()
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(inboundPort), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err = c.Write(payload); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, len(payload))
		if _, err = io.ReadFull(c, response); err != nil || !bytes.Equal(response, payload) {
			t.Fatal("core echo", err)
		}
		_ = c.Close()
	}
	for range 2 {
		transfer()
	}
	want := usageCounters{Upload: uint64(len(payload) * 2), Download: uint64(len(payload) * 2), Connections: 2}
	eventually(t, "exact traffic totals and close reconciliation", func() bool { return usageRead(t, s.usage, time.Now()).Totals == want })
	value := usageRead(t, s.usage, time.Now())
	if value.Facets["outbound"][0].Value != "direct" || value.Facets["inbound"][0].Value != "usage-in" || value.Facets["network"][0].Value != "TCP" {
		t.Fatalf("real core dimensions: %+v", value.Facets)
	}
	cancel()
	<-done
	// This connection starts and transfers while the collector is disconnected.
	// The next official reset must recover it alongside the already counted ones.
	transfer()
	want.add(usageCounters{Upload: uint64(len(payload)), Download: uint64(len(payload)), Connections: 1})
	s.usage = newUsageStore(s.dataDir)
	cancelAgain, doneAgain := collect()
	defer cancelAgain()
	eventually(t, "persistent store recovers traffic created during the gap", func() bool { return usageRead(t, s.usage, time.Now()).Totals == want })
	cancelAgain()
	<-doneAgain
	if got := usageRead(t, s.usage, time.Now()).Totals; got != want {
		t.Fatalf("replay changed persisted totals: %+v", got)
	}
}
