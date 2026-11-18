package lanscan

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func ifaces() []InterfaceInfo {
	up := net.FlagUp | net.FlagBroadcast
	mac := net.HardwareAddr{0x3c, 0x22, 0xfb, 1, 2, 3}
	return []InterfaceInfo{
		{Name: "en0", Flags: up, MAC: mac, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.5/24"), netip.MustParsePrefix("fe80::1/64")}},
		{Name: "en1", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("10.20.30.40/16")}},  // narrowed to /24
		{Name: "en2", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("8.8.8.8/24")}},      // public
		{Name: "utun3", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("172.16.0.2/24")}}, // virtual
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback, Addrs: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}},
		{Name: "en3", Flags: net.FlagBroadcast, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.9.5/24")}}, // down
		{Name: "en4", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("169.254.3.4/16")}},                // link-local
		{Name: "en5", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.77/24")}},               // duplicate subnet
		{Name: "en6", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("172.20.0.9/28")}},                 // small subnet kept
		{Name: "en7", Flags: up, Addrs: []netip.Prefix{netip.MustParsePrefix("100.64.1.2/24")}},                 // CGNAT / tailnet
	}
}

func TestSubnetsPrivateOnly(t *testing.T) {
	subs := Subnets(ifaces())
	var got []string
	for _, s := range subs {
		got = append(got, s.Prefix.String())
	}
	want := []string{"192.168.1.0/24", "10.20.30.0/24", "172.20.0.0/28"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	hosts := subs[0].Hosts()
	if len(hosts) != 253 { // 256 minus network, broadcast and self
		t.Fatalf("hosts %d", len(hosts))
	}
	for _, h := range hosts {
		if h == subs[0].Self || h.String() == "192.168.1.0" || h.String() == "192.168.1.255" {
			t.Fatalf("unexpected %v", h)
		}
	}
	if len(subs[2].Hosts()) != 13 {
		t.Fatalf("/28 hosts %d", len(subs[2].Hosts()))
	}
}

func fakeEnv(neigh []Neighbor, ids ...Identifier) Env {
	return Env{
		Interfaces: func() ([]InterfaceInfo, error) { return ifaces()[:1], nil },
		Host: Host{
			Run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]\n? (192.168.1.20) at 3c:22:fb:00:00:20 on en0\n? (192.168.1.31) at 02:11:22:33:44:55 on en0\n? (10.9.9.9) at aa:aa:aa:aa:aa:aa on en0\n"), nil
			},
			ReadFile: func(string) ([]byte, error) { return nil, errors.New("none") },
		},
		Wake:        func(context.Context, []Subnet, []netip.Addr, *Pacer) error { return nil },
		Identifiers: ids,
		Hostname:    func() string { return "my-macbook" },
	}
}

type idFunc func(ctx context.Context, req IdentifyRequest) error

func (idFunc) Name() string                                              { return "fake" }
func (f idFunc) Identify(ctx context.Context, req IdentifyRequest) error { return f(ctx, req) }

func TestScanBuildsDevices(t *testing.T) {
	env := fakeEnv(nil, idFunc(func(_ context.Context, req IdentifyRequest) error {
		req.Report(Hint{IP: netip.MustParseAddr("192.168.1.20"), Source: SourceMDNS, Name: "Bob's iPhone", Model: "iPhone14,2"})
		req.Report(Hint{IP: netip.MustParseAddr("192.168.1.20"), Source: SourceDNS, Name: "dns-name"})
		req.Report(Hint{IP: netip.MustParseAddr("192.168.1.99"), Source: SourceMDNS, Name: "no-arp"})  // in subnet, no ARP entry
		req.Report(Hint{IP: netip.MustParseAddr("172.31.5.5"), Source: SourceMDNS, Name: "elsewhere"}) // outside scanned subnets
		req.Report(Hint{IP: netip.MustParseAddr("192.168.1.31"), Source: SourceNetBIOS, Name: "DESKTOP-1", NetBIOS: true})
		return nil
	}))
	var last []Device
	var prog []Progress
	err := Scan(context.Background(), env, func(p Progress) { prog = append(prog, p) }, func(l []Device) { last = l })
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Device{}
	for _, d := range last {
		by[d.IP] = d
	}
	if len(by) != 5 { // self, router, phone, windows, no-arp host; 10.9.9.9 and 172.31.5.5 excluded
		t.Fatalf("devices %+v", last)
	}
	if d := by["192.168.1.20"]; d.Name != "Bob's iPhone" || d.Image != ImageIPhone || d.Type != TypePhone || d.Vendor != "Apple" || d.MACRandom {
		t.Errorf("phone %+v", d)
	}
	if d := by["192.168.1.1"]; d.Gateway || d.Type != TypeUnknown {
		t.Errorf("without a route the router is not known: %+v", d)
	}
	if d := by["192.168.1.99"]; d.Name != "no-arp" || d.MAC != "" || d.MACRandom {
		t.Errorf("no-arp host %+v", d)
	}
	if d := by["192.168.1.31"]; !d.MACRandom || d.Type != TypeComputer || d.Image != ImagePC || d.Vendor != "" {
		t.Errorf("windows %+v", d)
	}
	if d := by["192.168.1.5"]; !d.Self || d.Name != "my-macbook" || d.Image != ImageMac {
		t.Errorf("self %+v", d)
	}
	if last[0].IP != "192.168.1.1" || last[len(last)-1].IP != "192.168.1.99" {
		t.Errorf("not sorted by address: %v", last)
	}
	if p := prog[len(prog)-1]; p.Phase != "done" || p.Step != p.Steps {
		t.Errorf("final progress %+v", p)
	}
}

func TestScanGatewayFromRoute(t *testing.T) {
	env := fakeEnv(nil)
	env.Host.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-an" {
			return []byte("? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0\n"), nil
		}
		return []byte("default 192.168.1.1 UGScg en0\n"), nil
	}
	env.Host.ReadFile = func(path string) ([]byte, error) {
		if path == "/proc/net/route" {
			return []byte("Iface\tDestination\tGateway\tFlags\nen0\t00000000\t0101A8C0\t0003\n"), nil
		}
		return nil, errors.New("none")
	}
	var last []Device
	if err := Scan(context.Background(), env, func(Progress) {}, func(l []Device) { last = l }); err != nil {
		t.Fatal(err)
	}
	for _, d := range last {
		if d.IP == "192.168.1.1" && (d.Type != TypeRouter || !d.Gateway || d.Image != ImageRouter) {
			t.Errorf("gateway %+v", d)
		}
	}
}

func TestManagerBusyPollCancel(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var stopped atomic.Bool
	env := func() Env {
		return fakeEnv(nil, idFunc(func(ctx context.Context, req IdentifyRequest) error {
			close(started)
			select {
			case <-ctx.Done():
				stopped.Store(true)
			case <-release:
			}
			return ctx.Err()
		}))
	}
	m := NewManager(env)
	id, err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second start: %v", err)
	}
	<-started
	snap, err := m.Poll(id, 0)
	if err != nil || snap.State != StateRunning || len(snap.Devices) == 0 {
		t.Fatalf("poll %+v %v", snap, err)
	}
	again, _ := m.Poll(id, snap.Cursor)
	if len(again.Devices) != 0 || again.Cursor != snap.Cursor {
		t.Fatalf("incremental poll must be empty: %+v", again)
	}
	start := time.Now()
	if err := m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if snap, _ := m.Poll(id, 0); snap.State != StateCanceled || len(snap.Devices) == 0 {
		t.Fatalf("after cancel %+v", snap)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !stopped.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !stopped.Load() || time.Since(start) > time.Second {
		t.Fatal("identifier did not observe cancellation promptly")
	}
	if _, err := m.Poll("nope", 0); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown: %v", err)
	}
	// A new scan may start after cancel.
	env2 := NewManager(func() Env { return fakeEnv(nil) })
	id2, _ := env2.Start()
	for i := 0; i < 200; i++ {
		if s, _ := env2.Poll(id2, 0); s.State == StateDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan did not finish")
}

func TestPacerRate(t *testing.T) {
	p := NewPacer(200)
	start := time.Now()
	for i := 0; i < 41; i++ {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 190*time.Millisecond {
		t.Fatalf("40 intervals at 200/s took only %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slow := NewPacer(1)
	_ = slow.Wait(ctx)
	if err := slow.Wait(ctx); err == nil {
		t.Fatal("canceled context must abort Wait")
	}
}

func TestReverseDNSConcurrencyCap(t *testing.T) {
	var cur, max atomic.Int32
	r := ReverseDNS{Workers: 3, Lookup: func(ctx context.Context, addr string) ([]string, error) {
		n := cur.Add(1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		cur.Add(-1)
		return []string{"host-" + strings.ReplaceAll(addr, ".", "-") + ".lan."}, nil
	}}
	var hosts []netip.Addr
	for i := 1; i <= 30; i++ {
		hosts = append(hosts, netip.AddrFrom4([4]byte{192, 168, 1, byte(i)}))
	}
	var mu sync.Mutex
	got := 0
	err := r.Identify(context.Background(), IdentifyRequest{Hosts: hosts, Pacer: NewPacer(100000), Report: func(h Hint) {
		mu.Lock()
		got++
		mu.Unlock()
		if h.Name != "host-"+strings.ReplaceAll(h.IP.String(), ".", "-") {
			t.Errorf("name %q", h.Name)
		}
	}})
	if err != nil || got != 30 || max.Load() > 3 {
		t.Fatalf("err=%v got=%d max=%d", err, got, max.Load())
	}
}

func TestShortHost(t *testing.T) {
	if shortHost("nas.fritz.box.") != "nas" || shortHost("plain") != "plain" {
		t.Fatal("shortHost")
	}
}

// loopbackResponder answers every datagram with reply(pkt) from a fresh UDP socket.
func loopbackResponder(t *testing.T, reply func([]byte) []byte) int {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback UDP:", err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := reply(append([]byte(nil), buf[:n]...)); r != nil {
				_, _ = conn.WriteTo(r, from)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func TestNetBIOSIdentifyOverUDP(t *testing.T) {
	port := loopbackResponder(t, func(q []byte) []byte {
		resp := append([]byte{q[0], q[1], 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}, 0xC0, 0x0C, 0, 0x21, 0, 1, 0, 0, 0, 0, 0, 0, 1)
		return append(resp, []byte("NAS-BOX        \x00\x04\x00")...)
	})
	var got Hint
	err := NetBIOS{Port: port, Wait: 200 * time.Millisecond}.Identify(context.Background(), IdentifyRequest{
		Hosts: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Pacer: NewPacer(1000), Report: func(h Hint) { got = h },
	})
	if err != nil || got.Name != "NAS-BOX" || !got.NetBIOS {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestMDNSUnicastFollowUp(t *testing.T) {
	var asked atomic.Int32
	port := loopbackResponder(t, func(q []byte) []byte {
		var m dns.Msg
		if m.Unpack(q) != nil || len(m.Question) == 0 {
			return nil
		}
		qs := m.Question[0]
		resp := new(dns.Msg)
		resp.Response = true
		switch {
		case qs.Qtype == dns.TypePTR && qs.Name == "_device-info._tcp.local.":
			resp.Answer = []dns.RR{&dns.PTR{Hdr: dns.RR_Header{Name: qs.Name, Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "Studio._device-info._tcp.local."}}
		case qs.Qtype == dns.TypeTXT:
			asked.Add(1)
			resp.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: qs.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{"model=Macmini9,1"}}}
		default:
			return nil
		}
		b, _ := resp.Pack()
		return b
	})
	var mu sync.Mutex
	var hints []Hint
	err := MDNS{Port: port, Multicast: "-", Wait: 300 * time.Millisecond}.Identify(context.Background(), IdentifyRequest{
		Hosts: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Pacer: NewPacer(1000),
		Report: func(h Hint) { mu.Lock(); hints = append(hints, h); mu.Unlock() },
	})
	mu.Lock()
	defer mu.Unlock()
	model := ""
	for _, h := range hints {
		if h.Model != "" {
			model = h.Model
		}
	}
	if err != nil || asked.Load() != 1 || model != "Macmini9,1" {
		t.Fatalf("err=%v asked=%d hints=%+v", err, asked.Load(), hints)
	}
}

func TestIdentifyCancelStopsQuickly(t *testing.T) {
	port := loopbackResponder(t, func([]byte) []byte { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	err := NetBIOS{Port: port, Wait: 10 * time.Second}.Identify(ctx, IdentifyRequest{
		Hosts: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Pacer: NewPacer(1000), Report: func(Hint) {},
	})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestScanGatewayWithoutARPEntry(t *testing.T) {
	env := fakeEnv(nil)
	env.Host.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	env.Host.ReadFile = func(path string) ([]byte, error) {
		if path == "/proc/net/route" {
			return []byte("Iface\tDestination\tGateway\tFlags\nen0\t00000000\t0101A8C0\t0003\n"), nil
		}
		return nil, errors.New("none")
	}
	env.Host = Host{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "-an" {
			return nil, nil
		}
		return []byte("default 192.168.1.1 UGScg en0\n"), nil
	}, ReadFile: env.Host.ReadFile}
	var last []Device
	if err := Scan(context.Background(), env, func(Progress) {}, func(l []Device) { last = l }); err != nil {
		t.Fatal(err)
	}
	for _, d := range last {
		if d.IP == "192.168.1.1" {
			if d.Type != TypeRouter || d.MAC != "" {
				t.Fatalf("%+v", d)
			}
			return
		}
	}
	t.Fatal("gateway missing")
}
