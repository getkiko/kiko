package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/miekg/dns"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type reloadStarted struct {
	daemon.UnimplementedStartedServiceServer
	marker        string
	readyAt       time.Time
	config        string
	legacy        bool
	partial       bool
	wrongRevision bool
}

func (f *reloadStarted) GetStartedAt(context.Context, *emptypb.Empty) (*daemon.StartedAt, error) {
	if time.Now().Before(f.readyAt) {
		return nil, status.Error(codes.Unavailable, "core is initializing")
	}
	data, _ := os.ReadFile(f.marker)
	_, legacyErr := os.Stat(f.marker + ".legacy")
	if f.legacy || legacyErr == nil {
		return &daemon.StartedAt{StartedAt: 1000}, nil
	}
	active, _ := os.ReadFile(f.marker + ".active")
	if len(active) == 0 {
		active, _ = os.ReadFile(f.config)
	}
	revision := configRevision(active)
	_, wrongErr := os.Stat(f.marker + ".wrong")
	if f.wrongRevision || wrongErr == nil {
		revision = "wrong"
	}
	_, partialErr := os.Stat(f.marker + ".partial")
	return &daemon.StartedAt{StartedAt: 1000 + int64(len(data)), InstanceId: 1000 + int64(len(data)), Ready: !f.partial && partialErr != nil, ReloadProtocol: 1, ConfigRevision: revision}, nil
}

func reloadFixture(t *testing.T) (*server, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal fixture")
	}
	dir := t.TempDir()
	marker, starts := filepath.Join(dir, "reloads"), filepath.Join(dir, "starts")
	runtimePath := filepath.Join(dir, "data", "runtime", "config.json")
	script := fmt.Sprintf("if [ \"$1\" = check ]; then if [ -f %q ]; then exit 1; fi; exit 0; fi\ncp %q %q\ntrap 'if [ -f %s.exit ]; then exit 1; fi; cp %q %q; printf h >> %s' HUP\ntrap 'exit 0' TERM INT\necho run >> %q\nwhile :; do sleep 0.02; done\n", marker+".failcheck", runtimePath, marker+".active", marker, runtimePath, marker+".active", marker, starts)
	s, err := newServer(writeScript(t, script), filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	api, address := startFakeStarted(t, "127.0.0.1:0", &reloadStarted{marker: marker, config: runtimePath})
	t.Cleanup(api.Stop)
	_, port, _ := net.SplitHostPort(address)
	s.rpc.port, _ = strconv.Atoi(port)
	s.rpc.secret = testSecret
	s.rpc.probeOnce.Do(func() { s.rpc.supported = true })
	s.rpc.dnsProbeOnce.Do(func() {})
	if res := postJSON(s.apply, "POST", `{"outbounds":[{"type":"direct","tag":"old"}],"route":{"final":"old"}}`); res.Code != 200 {
		t.Fatalf("initial apply: %d %s", res.Code, res.Body.String())
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.stop(true) })
	eventually(t, "fixture readiness", func() bool { _, err := os.Stat(starts); return err == nil })
	return s, marker, starts
}

// A running process can expose its API after initialization has finished.
func TestPrepareReloadWaitsForFirstAPIReadiness(t *testing.T) {
	s, marker, _ := reloadFixture(t)
	api, address := startFakeStarted(t, "127.0.0.1:0", &reloadStarted{marker: marker, readyAt: time.Now().Add(1300 * time.Millisecond)})
	t.Cleanup(api.Stop)
	_, port, _ := net.SplitHostPort(address)
	s.rpc.port, _ = strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	target, err := s.prepareReload(ctx, false)
	if err != nil {
		t.Fatalf("first API readiness: %v", err)
	}
	if target.generation != 1000 {
		t.Fatalf("generation=%d", target.generation)
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Fatalf("preflight signaled core: %q", data)
	}
}

func TestPrepareReloadReadinessHonorsCancellation(t *testing.T) {
	s, marker, _ := reloadFixture(t)
	api, address := startFakeStarted(t, "127.0.0.1:0", &reloadStarted{marker: marker, readyAt: time.Now().Add(time.Hour)})
	t.Cleanup(api.Stop)
	_, port, _ := net.SplitHostPort(address)
	s.rpc.port, _ = strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := s.prepareReload(ctx, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled preparation: %v", err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("readiness ignored request deadline")
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Fatalf("canceled preflight signaled core: %q", data)
	}
}

func TestApplyReloadsConfigurationInSameProcess(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	before := s.snapshot()
	began := time.Now()
	res := postJSON(s.apply, "POST", `{"outbounds":[{"type":"direct","tag":"new"}],"route":{"final":"new"}}`)
	if res.Code != 200 {
		t.Fatalf("apply: %d %s", res.Code, res.Body.String())
	}
	after := s.snapshot()
	if after.PID != before.PID || !after.StartedAt.Equal(before.StartedAt) {
		t.Fatalf("config apply restarted core: before=%+v after=%+v", before, after)
	}
	if after.State != "running" || !after.DesiredRunning {
		t.Fatalf("runtime=%+v", after)
	}
	if data, _ := os.ReadFile(marker); string(data) != "h" {
		t.Fatalf("reload signals=%q", data)
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "run") != 1 {
		t.Fatalf("process starts=%q", data)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("config apply kept a fixed health wait: %v", elapsed)
	}
}

func TestApplyReloadRepeatedAndStopped(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	before := s.snapshot()
	for i := range 3 {
		res := postJSON(s.apply, "POST", fmt.Sprintf(`{"outbounds":[{"type":"direct","tag":"new-%d"}],"route":{"final":"new-%d"}}`, i, i))
		if res.Code != 200 {
			t.Fatalf("apply: %d %s", res.Code, res.Body.String())
		}
		var response struct {
			Runtime runtimeState `json:"runtime"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Runtime.PID != before.PID || !response.Runtime.StartedAt.Equal(before.StartedAt) {
			t.Fatalf("process identity changed: %+v", response.Runtime)
		}
	}
	if data, _ := os.ReadFile(marker); string(data) != "hhh" {
		t.Fatalf("reload signals=%q", data)
	}
	s.stop(true)
	res := postJSON(s.apply, "POST", `{"outbounds":[{"type":"direct","tag":"saved"}],"route":{"final":"saved"}}`)
	if res.Code != 200 || s.snapshot().PID != 0 || s.snapshot().DesiredRunning {
		t.Fatalf("stopped apply: %d %+v", res.Code, s.snapshot())
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "run") != 1 {
		t.Fatalf("process starts=%q", data)
	}
}

func TestApplyUnchangedConfigKeepsActiveInstance(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	before := s.snapshot()
	for range 3 {
		res := postJSON(s.apply, "POST", `{"outbounds":[{"type":"direct","tag":"old"}],"route":{"final":"old"}}`)
		if res.Code != 200 {
			t.Fatalf("apply unchanged: %d %s", res.Code, res.Body)
		}
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Fatalf("unchanged config reloaded services: %q", data)
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "run") != 1 {
		t.Fatal("unchanged config restarted core")
	}
	if after := s.snapshot(); after.PID != before.PID || !after.StartedAt.Equal(before.StartedAt) {
		t.Fatalf("process changed: %+v", after)
	}
}

func TestApplyReloadPreflightPreservesRunningConfig(t *testing.T) {
	for _, scenario := range []string{"invalid-reference", "invalid-config", "null", "api-unavailable", "environment-change", "concurrent-apply", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			s, marker, _ := reloadFixture(t)
			before := s.snapshot()
			lastApplied := s.persisted.LastApplied
			paths := []string{filepath.Join(s.dataDir, "config.json"), filepath.Join(s.dataDir, "runtime", "config.json")}
			old := make([][]byte, len(paths))
			for i, path := range paths {
				old[i], _ = os.ReadFile(path)
			}
			body := `{"outbounds":[{"type":"direct","tag":"new"}],"route":{"final":"new"}}`
			switch scenario {
			case "invalid-reference":
				body = `{"route":{"final":"missing"}}`
			case "invalid-config":
				s.singBox = writeScript(t, "echo invalid >&2\nexit 1\n")
			case "null":
				body = `null`
			case "api-unavailable":
				s.rpc.secret = "wrong-secret"
			case "environment-change":
				body = `{"_kiko_runtime":{"disableTailscaleUDPDirect":true}}`
			case "concurrent-apply":
				s.configMu.Lock()
				defer s.configMu.Unlock()
			}
			req := httptest.NewRequest("POST", "/", strings.NewReader(body))
			if scenario == "canceled" {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			if scenario == "concurrent-apply" {
				// apply waits for configMu until the caller gives up.
				ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
				defer cancel()
				req = req.WithContext(ctx)
			}
			res := httptest.NewRecorder()
			s.apply(res, req)
			if res.Code < 400 {
				t.Fatalf("expected error: %d %s", res.Code, res.Body.String())
			}
			after := s.snapshot()
			if after.PID != before.PID || !after.StartedAt.Equal(before.StartedAt) || after.State != "running" {
				t.Fatalf("runtime changed: %+v", after)
			}
			if !s.persisted.LastApplied.Equal(lastApplied) {
				t.Fatal("failed apply updated LastApplied")
			}
			for i, path := range paths {
				data, _ := os.ReadFile(path)
				if !bytes.Equal(data, old[i]) {
					t.Fatalf("changed %s", path)
				}
			}
			if data, _ := os.ReadFile(marker); len(data) != 0 {
				t.Fatalf("unexpected reload signal=%q", data)
			}
		})
	}
}

func TestReloadExplicitStopWins(t *testing.T) {
	s, marker, _ := reloadFixture(t)
	target, err := s.prepareReload(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	s.stop(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := target.execute(ctx); err == nil {
		t.Fatal("reload must respect explicit stop")
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Fatalf("unexpected reload=%q", data)
	}
	if state := s.snapshot(); state.PID != 0 || state.DesiredRunning || state.State != "stopped" {
		t.Fatalf("runtime=%+v", state)
	}
}

func TestApplyReloadFailureRestoresFiles(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	paths := []string{filepath.Join(s.dataDir, "config.json"), filepath.Join(s.dataDir, "runtime", "config.json")}
	previous := make([][]byte, len(paths))
	for i, path := range paths {
		previous[i], _ = os.ReadFile(path)
	}
	lastApplied := s.persisted.LastApplied
	if err := os.WriteFile(marker+".exit", []byte("exit"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := postJSON(s.apply, "POST", `{"outbounds":[{"type":"direct","tag":"new"}],"route":{"final":"new"}}`)
	if res.Code != 500 {
		t.Fatalf("reload: %d %s", res.Code, res.Body.String())
	}
	for i, path := range paths {
		data, _ := os.ReadFile(path)
		if !bytes.Equal(data, previous[i]) {
			t.Fatalf("failed to restore %s", path)
		}
	}
	if !s.persisted.LastApplied.Equal(lastApplied) {
		t.Fatal("failed reload changed LastApplied")
	}
	eventually(t, "recovery process start", func() bool { data, _ := os.ReadFile(starts); return strings.Count(string(data), "run") == 2 })
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "run") != 2 {
		t.Fatalf("expected exactly one recovery start: %q response=%s logs=%s", data, res.Body, strings.Join(s.logs.snapshot(), "\n"))
	}
	if state := s.snapshot(); state.State != "running" || !state.DesiredRunning {
		t.Fatalf("recovery: %+v", state)
	}
}

// Uses an isolated core, local proxy and local HTTP target. No system TUN/DNS.
func TestReloadCoreEndToEnd(t *testing.T) {
	for _, stateful := range []bool{false, true} {
		t.Run(fmt.Sprintf("stateful=%v", stateful), func(t *testing.T) { reloadCoreEndToEnd(t, stateful) })
	}
}

func reloadCoreEndToEnd(t *testing.T, stateful bool) {
	binary := os.Getenv("KIKO_TEST_CORE")
	if binary == "" || runtime.GOOS == "windows" {
		t.Skip("set KIKO_TEST_CORE for Unix core reload validation")
	}
	s, err := newServer(binary, t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.stopGRPC()
		s.stop(true)
		if t.Failed() {
			t.Log(strings.Join(s.logs.snapshot(), "\n"))
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	control := httptest.NewServer(http.NotFoundHandler())
	defer control.Close()
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, addr, err := upstream.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dns.Msg
			if query.Unpack(buffer[:n]) != nil {
				continue
			}
			response := new(dns.Msg)
			response.SetReply(&query)
			answer, _ := dns.NewRR(query.Question[0].Name + " 0 IN A 192.0.2.23")
			response.Answer = []dns.RR{answer}
			wire, _ := response.Pack()
			_, _ = upstream.WriteTo(wire, addr)
		}
	}()
	spare, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dnsPort := spare.LocalAddr().(*net.UDPAddr).Port
	spare.Close()
	config := func(reject bool) string {
		rules := `[{"inbound":["dns-in"],"action":"hijack-dns"}]`
		if reject {
			rules = `[{"inbound":["dns-in"],"action":"hijack-dns"},{"ip_cidr":["127.0.0.1/32"],"action":"reject"}]`
		}
		state := ""
		if stateful {
			state = fmt.Sprintf(`"experimental":{"cache_file":{"enabled":true,"store_dns":true}},"endpoints":[{"type":"tailscale","tag":"tailscale","state_directory":"tailscale","control_url":%q}],`, control.URL)
		}
		dnsRules := "[]"
		if reject {
			dnsRules = `[{"domain":["reload.example"],"action":"predefined","rcode":"REFUSED"}]`
		}

		return fmt.Sprintf(`{%s"log":{"level":"trace","disabled":false},"dns":{"disable_cache":true,"servers":[{"type":"udp","tag":"test-dns","server":"127.0.0.1","server_port":%d}],"final":"test-dns","rules":%s},"inbounds":[{"type":"mixed","listen":"127.0.0.1","listen_port":%d},{"type":"direct","tag":"dns-in","listen":"127.0.0.1","listen_port":%d}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct","rules":%s}}`, state, upstream.LocalAddr().(*net.UDPAddr).Port, dnsRules, port, dnsPort, rules)
	}
	res := postJSON(s.apply, "POST", config(false))
	if res.Code != 200 {
		t.Fatalf("initial config: %d %s", res.Code, res.Body.String())
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "core API readiness", func() bool {
		_, err := s.prepareReload(context.Background(), false)
		if err != nil {
			return false
		}
		return true
	})
	s.grpcTick()
	eventually(t, "monitor connected", s.grpcActive)
	before := s.snapshot()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("reload-ok")) }))
	defer target.Close()
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	allowed := func() bool {
		response, err := client.Get(target.URL)
		if err != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == 200
	}
	if !allowed() {
		t.Fatal("initial direct route failed")
	}
	count := 4
	if configured, err := strconv.Atoi(os.Getenv("KIKO_TEST_RELOAD_COUNT")); err == nil && configured > 0 {
		count = configured
	}
	elapsedSamples := make([]time.Duration, 0, count)
	var baselineGoroutines int32
	var lastGoroutines int32
	var baselineMemory, lastMemory uint64
	for i := 0; i < count; i++ {
		reject := i%2 == 0
		s.rpc.mu.Lock()
		remote := daemon.RemoteClientOptions{ServerURL: "http://127.0.0.1:" + strconv.Itoa(s.rpc.port), Secret: s.rpc.secret}
		s.rpc.mu.Unlock()
		conn, err := daemon.NewRemoteClient(remote)
		if err != nil {
			t.Fatal(err)
		}
		subscriptionCtx, subscriptionCancel := context.WithTimeout(context.Background(), 5*time.Second)
		stream, err := daemon.NewStartedServiceClient(conn).SubscribeStatus(subscriptionCtx, &daemon.SubscribeStatusRequest{Interval: int64(time.Second)})
		if err != nil {
			subscriptionCancel()
			conn.Close()
			t.Fatal(err)
		}
		frame, err := stream.Recv()
		if err != nil {
			subscriptionCancel()
			conn.Close()
			t.Fatal(err)
		}
		lastGoroutines, lastMemory = frame.Goroutines, frame.Memory
		if i == 0 {
			baselineGoroutines, baselineMemory = lastGoroutines, lastMemory
		}
		streamEnded := make(chan error, 1)
		go func() {
			for {
				_, err := stream.Recv()
				if err != nil {
					streamEnded <- err
					return
				}
			}
		}()
		began := time.Now()
		res := postJSON(s.apply, "POST", config(reject))
		elapsed := time.Since(began)
		elapsedSamples = append(elapsedSamples, elapsed)
		if res.Code != 200 {
			t.Fatalf("reload: %d %s", res.Code, res.Body.String())
		}
		select {
		case <-streamEnded:
		case <-time.After(time.Second):
			subscriptionCancel()
			conn.Close()
			t.Fatal("old API subscription survived teardown")
		}
		subscriptionCancel()
		conn.Close()
		after := s.snapshot()
		if after.PID != before.PID || !after.StartedAt.Equal(before.StartedAt) {
			t.Fatalf("OS process changed: before=%+v after=%+v", before, after)
		}
		if allowed() == reject {
			t.Fatalf("route change not effective, reject=%v", reject)
		}
		query := new(dns.Msg)
		query.SetQuestion("reload.example.", dns.TypeA)
		reply, _, dnsErr := (&dns.Client{Timeout: time.Second}).Exchange(query, fmt.Sprintf("127.0.0.1:%d", dnsPort))
		if dnsErr != nil {
			t.Fatalf("DNS after reload: %v", dnsErr)
		}
		if reject && reply.Rcode != dns.RcodeRefused || !reject && (reply.Rcode != dns.RcodeSuccess || len(reply.Answer) != 1) {
			t.Fatalf("DNS rule not effective reject=%v reply=%v", reject, reply)
		}
		// Exercise an active h2c subscription on every subsequent teardown.
		eventually(t, "monitor reconnect after reload", s.grpcActive)
		t.Logf("reject=%v PID=%d startedAt=%s reload=%v route verified", reject, after.PID, after.StartedAt.Format(time.RFC3339Nano), elapsed)
	}
	sort.Slice(elapsedSamples, func(i, j int) bool { return elapsedSamples[i] < elapsedSamples[j] })
	p95, maxElapsed := elapsedSamples[(len(elapsedSamples)-1)*95/100], elapsedSamples[len(elapsedSamples)-1]
	t.Logf("reload count=%d p95=%s max=%s goroutines=%d→%d memory=%d→%d", count, p95, maxElapsed, baselineGoroutines, lastGoroutines, baselineMemory, lastMemory)
	if count >= 100 && lastGoroutines > baselineGoroutines+25 {
		t.Fatal("goroutines grew across reloads")
	}
	if p95 > 2*time.Second || maxElapsed > 5*time.Second {
		t.Fatalf("reload exceeded latency target")
	}
	began := time.Now()
	s.stop(true)
	t.Logf("stop=%v", time.Since(began))
}

func TestSameSystemDNSConfig(t *testing.T) {
	previous := []byte(`{"inbounds":[{"type":"tun","address":["172.19.0.1/30"]}]}`)
	for _, test := range []struct {
		next string
		same bool
	}{
		{`{"inbounds":[{"type":"tun","tag":"renamed","address":["172.19.0.1/30"]}],"route":{"rules":[]}}`, true},
		{`{"inbounds":[{"type":"tun","address":["172.20.0.1/30"]}]}`, false},
		{`{"inbounds":[{"type":"tun","dns_mode":"disabled","address":["172.19.0.1/30"]}]}`, false},
		{`{"inbounds":[]}`, false},
	} {
		if got := sameSystemDNSConfig(previous, []byte(test.next)); got != test.same {
			t.Fatalf("next=%s same=%v", test.next, got)
		}
	}
}

func TestReloadReadinessAndIdentity(t *testing.T) {
	for _, scenario := range []string{"legacy", "partial", "wrong"} {
		t.Run(scenario, func(t *testing.T) {
			s, marker, _ := reloadFixture(t)
			if err := os.WriteFile(marker+"."+scenario, []byte("test"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if scenario == "wrong" {
				target, err := s.prepareReload(ctx, false)
				if err != nil {
					t.Fatal(err)
				}
				defer target.finish()
				target.expectedRevision = "candidate"
				if err := target.execute(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("wrong revision accepted: %v", err)
				}
			} else {
				_, err := s.prepareReload(ctx, false)
				if err == nil {
					t.Fatal("incomplete/legacy readiness accepted")
				}
			}
		})
	}
}

func TestStopCancelsActiveReloadAndRecovery(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	target, err := s.prepareReload(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.finish()
	// Keep the next instance incomplete while the process remains alive.
	if err := os.WriteFile(marker+".partial", []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	target.expectedRevision = "candidate"
	done := make(chan error, 1)
	go func() { done <- target.execute(context.Background()) }()
	eventually(t, "reload signal", func() bool { data, _ := os.ReadFile(marker); return len(data) > 0 })
	began := time.Now()
	s.stop(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stopped reload succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel reload")
	}
	if err := target.recover(); err == nil {
		t.Fatal("Stop allowed recovery")
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "run") != 1 {
		t.Fatalf("unexpected recovery: %q", data)
	}
	if time.Since(began) > time.Second {
		t.Fatal("Stop cancellation took too long")
	}
}

func TestCoreColdStartThenImmediateReload(t *testing.T) {
	binary := os.Getenv("KIKO_TEST_CORE")
	if binary == "" || runtime.GOOS == "windows" {
		t.Skip("set KIKO_TEST_CORE")
	}
	for i := 0; i < 20; i++ {
		s, err := newServer(binary, t.TempDir(), "test")
		if err != nil {
			t.Fatal(err)
		}
		initial := `{"log":{"disabled":true},"outbounds":[{"type":"direct","tag":"old"}],"route":{"final":"old"},"experimental":{"cache_file":{"enabled":true}}}`
		if res := postJSON(s.apply, "POST", initial); res.Code != 200 {
			t.Fatalf("initial: %s", res.Body)
		}
		if err := s.start(); err != nil {
			t.Fatal(err)
		}
		before := s.snapshot()
		res := postJSON(s.apply, "POST", strings.ReplaceAll(initial, "old", "new"))
		after := s.snapshot()
		s.stop(true)
		if res.Code != 200 || after.PID != before.PID {
			t.Fatalf("cold-start %d: %d %s before=%+v after=%+v", i, res.Code, res.Body, before, after)
		}
	}
}

func TestRealCoreReloadStartupFailureRestoresRunningInstance(t *testing.T) {
	binary := os.Getenv("KIKO_TEST_CORE")
	if binary == "" || runtime.GOOS == "windows" {
		t.Skip("set KIKO_TEST_CORE")
	}
	s, err := newServer(binary, t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop(true)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	oldPort := spare.Addr().(*net.TCPAddr).Port
	spare.Close()
	config := func(port int) string {
		return fmt.Sprintf(`{"log":{"level":"trace"},"inbounds":[{"type":"mixed","listen":"127.0.0.1","listen_port":%d}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"},"experimental":{"cache_file":{"enabled":true}}}`, port)
	}
	if res := postJSON(s.apply, "POST", config(oldPort)); res.Code != 200 {
		t.Fatalf("initial: %s", res.Body)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	before := s.snapshot()
	previous, _ := os.ReadFile(filepath.Join(s.dataDir, "runtime", "config.json"))
	res := postJSON(s.apply, "POST", config(busy.Addr().(*net.TCPAddr).Port))
	if res.Code != 500 || !strings.Contains(res.Body.String(), "已恢复原配置") {
		t.Fatalf("rollback: %d %s logs=%s", res.Code, res.Body, strings.Join(s.logs.snapshot(), "\n"))
	}
	after := s.snapshot()
	if after.PID != before.PID || after.State != "running" || !after.DesiredRunning {
		t.Fatalf("rollback state: %+v", after)
	}
	actual, _ := os.ReadFile(filepath.Join(s.dataDir, "runtime", "config.json"))
	if !bytes.Equal(previous, actual) {
		t.Fatal("runtime config not restored")
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", oldPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	// A valid next transaction must work after a rejected candidate.
	if res := postJSON(s.apply, "POST", strings.Replace(config(oldPort), `"level":"trace"`, `"level":"debug"`, 1)); res.Code != 200 {
		t.Fatalf("subsequent reload: %d %s", res.Code, res.Body)
	}
}

func TestReloadTimeoutUsesSingleRecoveryWithoutSecondSignal(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	target, err := s.prepareReload(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.finish()
	target.expectedRevision = "unconfirmed-candidate"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := target.execute(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	s.configMu.Lock()
	err = target.recover()
	s.configMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "single recovery start", func() bool { data, _ := os.ReadFile(starts); return strings.Count(string(data), "run") == 2 })
	if data, _ := os.ReadFile(marker); string(data) != "h" {
		t.Fatalf("overlapping reload signals: %q", data)
	}
	if state := s.snapshot(); state.State != "running" || !state.DesiredRunning {
		t.Fatalf("recovery: %+v", state)
	}
}

func TestFailedReloadRecoveryStopsAutomaticRetries(t *testing.T) {
	s, marker, starts := reloadFixture(t)
	s.mu.Lock()
	s.autoStart = true
	s.mu.Unlock()
	target, err := s.prepareReload(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.finish()
	target.expectedRevision = "candidate"
	if err := os.WriteFile(marker+".exit", []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := target.execute(ctx); err == nil {
		t.Fatal("expected failed reload")
	}
	if err := os.WriteFile(marker+".failcheck", []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	s.configMu.Lock()
	err = target.recover()
	s.configMu.Unlock()
	if err == nil {
		t.Fatal("failed recovery succeeded")
	}
	s.mu.Lock()
	retryPending := s.restartTimer != nil
	s.mu.Unlock()
	if retryPending {
		t.Fatal("recovery scheduled another automatic start")
	}
	if state := s.snapshot(); state.State != "failed" || state.DesiredRunning || state.PID != 0 {
		t.Fatalf("failed recovery state: %+v", state)
	}
	if data, _ := os.ReadFile(starts); strings.Count(string(data), "run") != 1 {
		t.Fatalf("unexpected recovery starts: %q", data)
	}
}
