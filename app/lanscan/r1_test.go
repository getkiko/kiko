package lanscan

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestCleanText(t *testing.T) {
	cases := map[string]string{
		"  My \t TV\r\n ":        "My TV",
		"a\x00b\x07c":            "abc",
		"bad\xff\xfeutf8":        "badutf8",
		"zero​width":             "zerowidth",
		strings.Repeat("长", 200): strings.Repeat("长", 64),
	}
	for in, want := range cases {
		if got := cleanText(in); got != want {
			t.Errorf("cleanText(%q)=%q want %q", in, got, want)
		}
	}
	huge := "<root><device><friendlyName>" + strings.Repeat("x", 60000) + "\u200b</friendlyName><modelName>M\u200b</modelName></device></root>"
	if n, m := ParseUPnPDescription([]byte(huge)); len(n) != 64 || m != "M" {
		t.Errorf("upnp %d %q", len(n), m)
	}
}

func TestVirtualInterfaceNames(t *testing.T) {
	virtual := []string{"lo", "lo0", "docker0", "veth12ab", "br-1a2b3c", "virbr0", "vmnet8", "vboxnet0", "utun3", "tun0", "tap1", "tailscale0", "ztabcdef12", "awdl0", "llw0", "bridge0", "vEthernet (Default Switch)"}
	real := []string{"Local Area Connection", "Local Area Connection 2", "eth0", "en0", "wlan0", "Wi-Fi", "Tapo-Bridge", "Ethernet", "tunnel-lan", "ztest", "lobby"}
	for _, n := range virtual {
		if !isVirtualInterface(n) {
			t.Errorf("%q should be virtual", n)
		}
	}
	for _, n := range real {
		if isVirtualInterface(n) {
			t.Errorf("%q should be physical", n)
		}
	}
}

func ptrMsg(n int) []byte {
	m := new(dns.Msg)
	m.Response = true
	for i := 0; i < n; i++ {
		m.Answer = append(m.Answer, &dns.PTR{Hdr: dns.RR_Header{Name: "_device-info._tcp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: fmt.Sprintf("Dev%d._device-info._tcp.local.", i)})
	}
	b, _ := m.Pack()
	return b
}

func TestParseMDNSFollowUpCapped(t *testing.T) {
	_, follow, ok := ParseMDNS(ptrMsg(180), netip.MustParseAddr("192.168.1.9"), SourceMDNS)
	if !ok || len(follow) != maxFollowUp {
		t.Fatalf("ok=%v follow=%d", ok, len(follow))
	}
}

func TestFollowLimiterPerSourceAndDedup(t *testing.T) {
	f := newFollowLimiter()
	a, b := netip.MustParseAddr("192.168.1.9"), netip.MustParseAddr("192.168.1.10")
	if !f.allow(a, "X._d") || f.allow(a, "x._D") {
		t.Fatal("duplicate instance must be refused")
	}
	n := 1
	for i := 0; i < 50; i++ {
		if f.allow(a, fmt.Sprint("i", i)) {
			n++
		}
	}
	if n != maxFollowUp || !f.allow(b, "X._d") {
		t.Fatalf("n=%d", n)
	}
}

func TestMDNSAmplificationBounded(t *testing.T) {
	var txt atomic.Int32
	port := loopbackResponder(t, func(q []byte) []byte {
		var m dns.Msg
		if m.Unpack(q) != nil || len(m.Question) == 0 {
			return nil
		}
		if m.Question[0].Qtype == dns.TypeTXT {
			txt.Add(1)
			return nil
		}
		if m.Question[0].Name == "_device-info._tcp.local." {
			return ptrMsg(180)
		}
		return nil
	})
	err := MDNS{Port: port, Multicast: "-", Wait: 300 * time.Millisecond}.Identify(context.Background(), IdentifyRequest{
		Hosts: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Pacer: NewPacer(1000), Report: func(Hint) {},
	})
	if err != nil || txt.Load() > maxFollowUp {
		t.Fatalf("err=%v txt=%d", err, txt.Load())
	}
}

func TestUDPFollowUpUsesPacer(t *testing.T) {
	port := loopbackResponder(t, func(q []byte) []byte { return []byte("x") })
	p := NewPacer(10) // 100ms per packet
	start := time.Now()
	_ = udpExchange(context.Background(), nil, []netip.Addr{netip.MustParseAddr("127.0.0.1")}, port, p, 50*time.Millisecond,
		func(netip.Addr) [][]byte { return [][]byte{{1}} },
		func(_ netip.Addr, _ []byte, send func([]byte)) { send([]byte{2}); send([]byte{3}) })
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("follow-ups skipped the pacer: %v", time.Since(start))
	}
}

func TestServicesCapAndPublishCoalesced(t *testing.T) {
	var reports atomic.Int32
	var last []Device
	var mu sync.Mutex
	id := idFunc(func(ctx context.Context, req IdentifyRequest) error {
		ip := netip.MustParseAddr("192.168.1.20")
		for i := 0; i < 500; i++ {
			req.Report(Hint{IP: ip, Source: SourceMDNS, Name: "TV\x00", Services: []string{fmt.Sprintf("_svc%d._tcp", i), strings.Repeat("s", 500)}})
		}
		return nil
	})
	err := Scan(context.Background(), fakeEnv(nil, id), func(Progress) {}, func(l []Device) {
		reports.Add(1)
		mu.Lock()
		last = l
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if reports.Load() > 10 {
		t.Fatalf("publish not coalesced: %d reports", reports.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, d := range last {
		if d.IP == "192.168.1.20" {
			found = d.Name == "TV"
		}
	}
	if !found {
		t.Fatalf("final state missing or unclean: %+v", last)
	}
	if got := appendService(nil, strings.Repeat("s", 500)); len(got) != 0 {
		t.Fatal("long service kept")
	}
	var l []string
	for i := 0; i < 100; i++ {
		l = appendService(l, fmt.Sprint("_s", i))
	}
	if len(l) != maxServices {
		t.Fatalf("services %d", len(l))
	}
}

func TestManagerSharesPacer(t *testing.T) {
	var mu sync.Mutex
	var pacers []*Pacer
	release := make(chan struct{})
	m := NewManager(func() Env {
		e := fakeEnv(nil, idFunc(func(ctx context.Context, req IdentifyRequest) error {
			mu.Lock()
			pacers = append(pacers, req.Pacer)
			mu.Unlock()
			<-release
			return nil
		}))
		return e
	})
	id, err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_ = m.Cancel(id)
	if _, err := m.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	mu.Lock()
	defer mu.Unlock()
	if len(pacers) != 2 || pacers[0] != pacers[1] {
		t.Fatalf("scans must share one pacer: %v", pacers)
	}
	_ = net.IPv4zero
}
