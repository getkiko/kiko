//go:build !windows

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
)

// fakeProxyCore is a core whose outbound proxies listen on a made-up
// address, recording what was asked for.
type fakeProxyCore struct {
	fakeEndpoints
	mu       sync.Mutex
	requests []string
}

func (f *fakeProxyCore) StartOutboundProxy(request *daemon.OutboundProxyRequest, stream grpc.ServerStreamingServer[daemon.OutboundProxyEvent]) error {
	f.mu.Lock()
	f.requests = append(f.requests, fmt.Sprintf("%s:%d", request.Outbound, request.Port))
	f.mu.Unlock()
	if request.Outbound == "gone" {
		if err := stream.Send(&daemon.OutboundProxyEvent{Error: "outbound not found: gone"}); err != nil {
			return err
		}
	} else if err := stream.Send(&daemon.OutboundProxyEvent{Address: "127.0.0.1:23456"}); err != nil {
		return err
	}
	<-stream.Context().Done()
	return nil
}

func (f *fakeProxyCore) proxies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// fakeBrowser is a kiko-browser that records its arguments and the
// commands of its input, and shows READY.
const fakeBrowser = `#!/bin/sh
dir=$(dirname "$0")
printf '%s\n' "$@" > "$dir/args"
echo READY
while IFS= read -r line; do printf '%s\n' "$line" >> "$dir/commands"; done
echo closed > "$dir/closed"
`

// The built-in browser of an outbound runs on the core's proxy for it,
// with a profile of its own; opened again it loads the page in its
// window; it closes as the server exits. An outbound the running core
// lacks is named.
func TestBrowserOpensThroughOutbound(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the built-in browser is macOS only")
	}
	fake := &fakeProxyCore{}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	dir := t.TempDir()
	s.singBox = filepath.Join(dir, "kiko-core")
	s.dataDir = dir
	if err := os.WriteFile(filepath.Join(dir, "kiko-browser"), []byte(fakeBrowser), 0o755); err != nil {
		t.Fatal(err)
	}
	go s.grpcWorker(t.Context(), "http://"+address, testSecret)
	eventually(t, "grpc active", s.grpcActive)

	if _, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "gone"}); err == nil || !strings.Contains(err.Error(), "不在正在运行的配置中") {
		t.Fatalf("missing outbound: %v", err)
	}
	result, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "香港 01"})
	if err != nil || result.URL != browserHome || result.Browser != "内置浏览器" {
		t.Fatalf("open: %+v %v", result, err)
	}
	if _, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "x", Home: "ftp://a"}); err == nil || !strings.Contains(err.Error(), "默认页面无效") {
		t.Fatalf("open: %+v %v", result, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	want := strings.Join([]string{"http://127.0.0.1:23456", browserHome, browserProfileUUID("香港 01"), "香港 01", "no-webrtc"}, "\n") + "\n"
	if string(args) != want {
		t.Fatalf("args %q, want %q", args, want)
	}
	if _, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "香港 01", URL: "example.com/a"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the page sent to the window", func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "commands"))
		var command map[string]string
		line, _ := bufio.NewReader(strings.NewReader(string(data))).ReadString('\n')
		return json.Unmarshal([]byte(line), &command) == nil && command["url"] == "https://example.com/a"
	})
	// Opened again without a page, the window only comes forward.
	if _, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "香港 01", Home: "https://elsewhere.test/"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the window brought forward", func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "commands"))
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		return len(lines) == 2 && lines[1] == `{"focus":"1"}`
	})
	if got := fake.proxies(); len(got) != 2 || got[1] != "香港 01:0" {
		t.Fatalf("proxies %v", got)
	}
	// A browser whose proxy failed for good starts anew as it is opened.
	s.browsers.mu.Lock()
	broken := s.browsers.sessions[browserBuiltIn+"\x00香港 01"]
	s.browsers.mu.Unlock()
	broken.proxyDown.Store(true)
	if _, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "香港 01"}); err != nil {
		t.Fatal(err)
	}
	if broken.running() {
		t.Error("the broken browser still runs")
	}
	if got := fake.proxies(); len(got) != 3 {
		t.Fatalf("proxies after restart %v", got)
	}
	s.closeBrowsers()
	eventually(t, "the browser closed", func() bool {
		_, err := os.Stat(filepath.Join(dir, "closed"))
		return err == nil
	})
	if _, err := s.openBrowser(t.Context(), browserOpenRequest{Outbound: "香港 01"}); err == nil {
		t.Fatal("opened while closing")
	}
}

func TestBrowserTarget(t *testing.T) {
	for raw, want := range map[string]string{"": browserHome, " example.com ": "https://example.com", "http://a.test/x?y=1": "http://a.test/x?y=1"} {
		if got, err := browserTarget(raw); err != nil || got != want {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://"} {
		if _, err := browserTarget(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	if args := strings.Join(chromeArgs("p", "127.0.0.1:1", "https://a.test/", false), " "); !strings.Contains(args, "disable_non_proxied_udp") {
		t.Errorf("WebRTC not limited: %s", args)
	}
	if args := strings.Join(chromeArgs("p", "127.0.0.1:1", "https://a.test/", true), " "); strings.Contains(args, "webrtc") {
		t.Errorf("WebRTC limited although allowed: %s", args)
	}
	if a, b := browserProfileUUID("a"), browserProfileUUID("b"); a == b || len(a) != 36 {
		t.Errorf("profiles %q %q", a, b)
	}
}
