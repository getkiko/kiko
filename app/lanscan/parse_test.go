package lanscan

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/miekg/dns"
)

func TestParseARPUnix(t *testing.T) {
	out := `? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]
? (192.168.1.20) at 0:1a:2b:3:4:5 on en0 ifscope [ethernet]
? (192.168.1.99) at (incomplete) on en0 ifscope [ethernet]
? (224.0.0.251) at 1:0:5e:0:0:fb on en0 ifscope permanent [ethernet]
? (192.168.1.255) at ff:ff:ff:ff:ff:ff on en0 ifscope [ethernet]
`
	got := ParseARPUnix([]byte(out))
	if len(got) != 2 || got[0].IP.String() != "192.168.1.1" || got[1].MAC.String() != "00:1a:2b:03:04:05" || got[1].Iface != "en0" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseProcNetARP(t *testing.T) {
	data := `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
192.168.1.7      0x1         0x0         00:00:00:00:00:00     *        eth0
`
	got := ParseProcNetARP([]byte(data))
	if len(got) != 1 || got[0].IP.String() != "192.168.1.1" || got[0].Iface != "eth0" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseARPWindows(t *testing.T) {
	out := "\r\nInterface: 192.168.1.5 --- 0x7\r\n  Internet Address      Physical Address      Type\r\n  192.168.1.1           aa-bb-cc-dd-ee-ff     dynamic   \r\n  224.0.0.22            01-00-5e-00-00-16     static    \r\n  192.168.1.255         ff-ff-ff-ff-ff-ff     static    \r\n"
	got := ParseARPWindows([]byte(out))
	if len(got) != 1 || got[0].MAC.String() != "aa:bb:cc:dd:ee:ff" || got[0].Iface != "192.168.1.5" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseGateways(t *testing.T) {
	unix := "Routing tables\n\nInternet:\nDestination        Gateway            Flags   Netif Expire\ndefault            192.168.1.1        UGScg     en0\n10.0.0/8           link#4             UCS       en1\n"
	if g := ParseRouteUnix([]byte(unix)); len(g) != 1 || g[0].String() != "192.168.1.1" {
		t.Errorf("unix %v", g)
	}
	proc := "Iface\tDestination\tGateway\tFlags\nwlan0\t00000000\t0101A8C0\t0003\nwlan0\t0001A8C0\t00000000\t0001\n"
	if g := ParseProcNetRoute([]byte(proc)); len(g) != 1 || g[0].String() != "192.168.1.1" {
		t.Errorf("proc %v", g)
	}
	win := "Network Destination        Netmask          Gateway       Interface  Metric\n          0.0.0.0          0.0.0.0      192.168.1.1    192.168.1.5     25\n"
	if g := ParseRoutePrintWindows([]byte(win)); len(g) != 1 || g[0].String() != "192.168.1.1" {
		t.Errorf("win %v", g)
	}
}

func TestHostNeighborsInjected(t *testing.T) {
	h := Host{
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			return []byte("? (10.0.0.2) at aa:bb:cc:00:00:01 on en0 ifscope [ethernet]\n"), nil
		},
		ReadFile: func(string) ([]byte, error) {
			return []byte("IP address HW type Flags HW address Mask Device\n10.0.0.2 0x1 0x2 aa:bb:cc:00:00:01 * eth0\n"), nil
		},
	}
	got, err := h.Neighbors(context.Background())
	if err != nil || len(got) != 1 || got[0].IP.String() != "10.0.0.2" {
		t.Fatalf("%v %v", got, err)
	}
}

func mdnsReply(t *testing.T, rrs ...dns.RR) []byte {
	t.Helper()
	m := new(dns.Msg)
	m.Response = true
	m.Answer = rrs
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseMDNSDeviceInfo(t *testing.T) {
	src := netip.MustParseAddr("192.168.1.20")
	pkt := mdnsReply(t,
		&dns.PTR{Hdr: dns.RR_Header{Name: "_device-info._tcp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "Bob\\226\\128\\153s MacBook._device-info._tcp.local."},
		&dns.TXT{Hdr: dns.RR_Header{Name: "Bob\\226\\128\\153s MacBook._device-info._tcp.local.", Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{"model=MacBookPro18,3", "osxvers=23"}},
		&dns.A{Hdr: dns.RR_Header{Name: "Bobs-MacBook-Pro.local.", Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("192.168.1.20")},
	)
	hint, follow, ok := ParseMDNS(pkt, src, SourceMDNS)
	if !ok || hint.Model != "MacBookPro18,3" || hint.Name != "Bob’s MacBook" || len(follow) != 0 {
		t.Fatalf("%+v follow=%v ok=%v", hint, follow, ok)
	}
	if len(hint.Services) != 1 || hint.Services[0] != "_device-info._tcp" {
		t.Fatalf("services %v", hint.Services)
	}
}

func TestParseMDNSFollowUpAndReverse(t *testing.T) {
	src := netip.MustParseAddr("192.168.1.30")
	pkt := mdnsReply(t,
		&dns.PTR{Hdr: dns.RR_Header{Name: "_device-info._tcp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "Den._device-info._tcp.local."},
		&dns.PTR{Hdr: dns.RR_Header{Name: "30.1.168.192.in-addr.arpa.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "den-tv.local."},
	)
	hint, follow, ok := ParseMDNS(pkt, src, SourceMDNS)
	if !ok || len(follow) != 1 || follow[0] != "Den._device-info._tcp.local." || hint.Name != "Den" {
		t.Fatalf("%+v %v", hint, follow)
	}
	// reverse-only reply carries the host name
	pkt = mdnsReply(t, &dns.PTR{Hdr: dns.RR_Header{Name: "30.1.168.192.in-addr.arpa.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "den-tv.local."})
	if hint, _, _ := ParseMDNS(pkt, src, SourceMDNS); hint.Name != "den-tv" {
		t.Fatalf("%+v", hint)
	}
	if _, _, ok := ParseMDNS([]byte{1, 2, 3}, src, SourceMDNS); ok {
		t.Fatal("garbage must not parse")
	}
	// A query (not a response) is ignored.
	q := new(dns.Msg)
	q.SetQuestion("x.local.", dns.TypeA)
	b, _ := q.Pack()
	if _, _, ok := ParseMDNS(b, src, SourceMDNS); ok {
		t.Fatal("query must be ignored")
	}
}

func TestParseMDNSServiceList(t *testing.T) {
	pkt := mdnsReply(t,
		&dns.PTR{Hdr: dns.RR_Header{Name: "_services._dns-sd._udp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "_ipp._tcp.local."},
		&dns.PTR{Hdr: dns.RR_Header{Name: "_services._dns-sd._udp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET}, Ptr: "_airplay._tcp.local."},
	)
	hint, _, _ := ParseMDNS(pkt, netip.MustParseAddr("10.0.0.9"), SourceMDNS)
	if len(hint.Services) != 2 || hint.Services[0] != "_ipp._tcp" {
		t.Fatalf("%v", hint.Services)
	}
}

func TestNBSTAT(t *testing.T) {
	q := BuildNBSTAT(0x1234)
	if len(q) != 50 || q[0] != 0x12 || q[12] != 0x20 {
		t.Fatalf("query %x", q)
	}
	resp := append([]byte{0x12, 0x34, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}, 0xC0, 0x0C, 0, 0x21, 0, 1, 0, 0, 0, 0, 0, 0)
	resp = append(resp, 2)
	pad := func(n string, suffix byte, flags uint16) []byte {
		b := []byte(n + "               ")[:15]
		return append(b, suffix, byte(flags>>8), byte(flags))
	}
	resp = append(resp, pad("WORKGROUP", 0, 0x8400)...) // group, skipped
	resp = append(resp, pad("DESKTOP-AB12CD", 0, 0x0400)...)
	if got := ParseNBSTAT(resp); got != "DESKTOP-AB12CD" {
		t.Fatalf("got %q", got)
	}
	if ParseNBSTAT(resp[:20]) != "" || ParseNBSTAT(nil) != "" {
		t.Fatal("truncated packets must yield no name")
	}
}

func TestSSDP(t *testing.T) {
	pkt := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\nSERVER: Linux UPnP/1.0\r\nLOCATION: http://192.168.1.1:5000/rootDesc.xml\r\n\r\n"
	r, ok := ParseSSDP([]byte(pkt))
	if !ok || r.Location != "http://192.168.1.1:5000/rootDesc.xml" || ssdpService(r.ST) != "_upnp-igd._tcp" || r.Server == "" {
		t.Fatalf("%+v", r)
	}
	if _, ok := ParseSSDP([]byte("NOTIFY * HTTP/1.1\r\n")); ok {
		t.Fatal("NOTIFY is not a response")
	}
	name, model := ParseUPnPDescription([]byte(`<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device><friendlyName>Living Room TV</friendlyName><modelName>UN55</modelName></device></root>`))
	if name != "Living Room TV" || model != "UN55" {
		t.Fatalf("%q %q", name, model)
	}
}
