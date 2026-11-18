package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/sagernet/sing-box/common/dnsmonitor"
	dnsmonitorapi "github.com/sagernet/sing-box/common/dnsmonitor/api"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Set KIKO_DNS_TEST_CORE to a freshly built binary to verify the complete
// hijack -> resolver -> monitor RPC -> app HTTP path using local sockets only.
func TestDNSMonitorCoreEndToEnd(t *testing.T) {
	binary := os.Getenv("KIKO_DNS_TEST_CORE")
	if binary == "" {
		t.Skip("set KIKO_DNS_TEST_CORE to run the real-core integration")
	}
	for _, protocol := range []string{"udp_direct", "tls_proxy"} {
		t.Run(protocol, func(t *testing.T) { testDNSMonitorCoreEndToEnd(t, binary, protocol) })
	}
}

func testDNSMonitorCoreEndToEnd(t *testing.T, binary, protocol string) {
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			buffer := make([]byte, 4096)
			n, addr, err := upstream.ReadFrom(buffer)
			if err != nil {
				return
			}
			var q dns.Msg
			if q.Unpack(buffer[:n]) != nil {
				continue
			}
			response := new(dns.Msg)
			response.SetReply(&q)
			rr, _ := dns.NewRR("e2e.example. 120 IN A 192.0.2.23")
			response.Answer = []dns.RR{rr}
			raw, _ := response.Pack()
			_, _ = upstream.WriteTo(raw, addr)
		}
	}()
	freePort := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		return listener.Addr().(*net.TCPAddr).Port
	}
	apiPort, dnsPort := freePort(), freePort()
	upstreamPort := upstream.LocalAddr().(*net.UDPAddr).Port
	config := map[string]any{"log": map[string]any{"disabled": true}, "experimental": map[string]any{"dns_monitor": map[string]any{"enabled": true}}, "dns": map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "local-test", "server": "127.0.0.1", "server_port": upstreamPort}}, "final": "local-test"}, "inbounds": []any{map[string]any{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": dnsPort}}, "route": map[string]any{"rules": []any{map[string]any{"inbound": []string{"dns-in"}, "action": "hijack-dns"}}}, "services": []any{map[string]any{"type": "api", "tag": "api", "listen": "127.0.0.1", "listen_port": apiPort, "secret": "e2e-secret"}}}
	wantChain := []string{"direct"}
	if protocol == "tls_proxy" {
		certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
		certificate := certificateServer.TLS.Certificates[0]
		certificateServer.Close()
		listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer conn.Close()
					wire := &dns.Conn{Conn: conn}
					for {
						q, err := wire.ReadMsg()
						if err != nil {
							return
						}
						response := new(dns.Msg)
						response.SetReply(q)
						rr, _ := dns.NewRR(q.Question[0].Name + " 120 IN A 192.0.2.23")
						response.Answer = []dns.RR{rr}
						if wire.WriteMsg(response) != nil {
							return
						}
					}
				}(conn)
			}
		}()
		proxyPort := freePort()
		config["dns"] = map[string]any{"servers": []any{map[string]any{"type": "tls", "tag": "local-test", "server": "127.0.0.1", "server_port": listener.Addr().(*net.TCPAddr).Port, "detour": "proxy", "tls": map[string]any{"enabled": true, "insecure": true}}}, "final": "local-test"}
		config["outbounds"] = []any{
			map[string]any{"type": "selector", "tag": "proxy", "outbounds": []string{"socks-node"}},
			map[string]any{"type": "socks", "tag": "socks-node", "server": "127.0.0.1", "server_port": proxyPort, "version": "5"},
			map[string]any{"type": "direct", "tag": "direct"},
		}
		config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"type": "socks", "tag": "proxy-in", "listen": "127.0.0.1", "listen_port": proxyPort})
		config["route"].(map[string]any)["final"] = "direct"
		wantChain = []string{"proxy", "socks-node"}
	}
	content, _ := json.Marshal(config)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", configPath)
	logFile, err := os.CreateTemp(t.TempDir(), "core-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		if t.Failed() {
			data, _ := os.ReadFile(logFile.Name())
			t.Log(string(data))
		}
	}()
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://127.0.0.1:" + strconv.Itoa(apiPort), Secret: "e2e-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := dnsmonitorapi.NewDNSMonitorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		cap, err := client.GetCapabilities(ctx, &emptypb.Empty{})
		if err == nil && cap.Enabled {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("core monitor did not start", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := newGRPCTestServer(t)
	s.singBox = binary
	s.rpc.dns = client
	if !s.dnsMonitorSupported() {
		t.Fatal("config preflight failed")
	}
	request := new(dns.Msg)
	request.SetQuestion("e2e.example.", dns.TypeA)
	for range 2 {
		response, _, err := (&dns.Client{Timeout: time.Second}).Exchange(request, "127.0.0.1:"+strconv.Itoa(dnsPort))
		if err != nil || response == nil || len(response.Answer) != 1 {
			t.Fatalf("DNS exchange: %+v %v", response, err)
		}
	}
	w := httptest.NewRecorder()
	s.dnsQueries(w, httptest.NewRequest("GET", "/api/v1/dns/queries", nil))
	var view dnsmonitor.View
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("history HTTP %d: %s", w.Code, w.Body.String())
	}
	view.Queries = slices.DeleteFunc(view.Queries, func(q dnsmonitor.Query) bool { return q.Role != "query" })
	if len(view.Queries) != 2 {
		t.Fatalf("client query count %d: %s", len(view.Queries), w.Body.String())
	}
	cache, network := false, false
	for _, q := range view.Queries {
		request := httptest.NewRequest("GET", "/api/v1/dns/queries/"+q.ID+"?instance="+view.Instance, nil)
		request.SetPathValue("id", q.ID)
		w = httptest.NewRecorder()
		s.dnsQuery(w, request)
		var detail dnsmonitor.Query
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
			t.Fatal("detail bridge", w.Body.String())
		}
		if detail.Cache == "hit" {
			cache = true
		}
		if len(detail.Attempts) == 1 && detail.Attempts[0].Connection != nil && slices.Equal(detail.Attempts[0].Connection.Outbound, wantChain) {
			network = true
		}
		if detail.Source.Inbound != "dns-in" || detail.Response.Answer[0].Type != "A" || !slices.Equal(detail.ResolvedAddresses, []string{"192.0.2.23"}) {
			t.Fatalf("details %+v", detail)
		}
	}
	if !cache || !network {
		t.Fatalf("cache %v actual network %v", cache, network)
	}
	httpServer := httptest.NewServer(http.HandlerFunc(s.streamDNS))
	defer httpServer.Close()
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	streamReq, _ := http.NewRequestWithContext(streamCtx, "GET", httpServer.URL, nil)
	response, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	if !scanner.Scan() {
		t.Fatal("missing NDJSON snapshot")
	}
	var snapshot dnsmonitor.View
	if json.Unmarshal(scanner.Bytes(), &snapshot) != nil || !snapshot.Reset || len(snapshot.Queries) < 2 {
		t.Fatalf("stream %s", scanner.Text())
	}
	stop()
	t.Logf("real core: DNS success/cache, route %v, resolved addresses, RPC, HTTP list/detail/NDJSON; instance %s", wantChain, view.Instance)
}
