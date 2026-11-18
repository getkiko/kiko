package lanscan

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// mdnsServices are the browse queries sent to the multicast group.
var mdnsServices = []string{
	"_device-info._tcp.local.", "_airplay._tcp.local.", "_companion-link._tcp.local.",
	"_raop._tcp.local.", "_services._dns-sd._udp.local.", "_ipp._tcp.local.",
	"_printer._tcp.local.", "_googlecast._tcp.local.", "_hap._tcp.local.", "_smb._tcp.local.",
	"_apple-mobdev2._tcp.local.", "_homekit._tcp.local.", "_workstation._tcp.local.",
}

// mdnsUnicastServices are browsed on each host directly, beside its reverse
// PTR: Apple devices announce their model in these TXT records.
var mdnsUnicastServices = []string{
	"_device-info._tcp.local.", "_companion-link._tcp.local.", "_airplay._tcp.local.",
	"_raop._tcp.local.", "_apple-mobdev2._tcp.local.", "_services._dns-sd._udp.local.",
}

// modelServices carry a model key in their TXT record; their instances are
// followed up with a TXT query when the reply left it out.
var modelServices = map[string]bool{
	"_device-info._tcp.local.": true, "_companion-link._tcp.local.": true,
	"_airplay._tcp.local.": true, "_raop._tcp.local.": true,
}

// anonymousServices have instance names that are not device names
// (_apple-mobdev2 uses "MAC@address", the sleep proxy a numeric label).
var anonymousServices = map[string]bool{
	"_apple-mobdev2._tcp.local.": true, "_sleep-proxy._udp.local.": true,
}

// txtModel reads the model identifier from one TXT string: model=
// (_device-info, _airplay), rpMd= (_companion-link) or am= (_raop).
func txtModel(s string) string {
	k, val, found := strings.Cut(s, "=")
	if !found {
		return ""
	}
	switch strings.ToLower(k) {
	case "model", "rpmd", "am":
		return strings.TrimSpace(val)
	}
	return ""
}

const mdnsUnicastResponse = 1 << 15 // QU bit

func reverseName(ip netip.Addr) string {
	b := ip.As4()
	return strconv.Itoa(int(b[3])) + "." + strconv.Itoa(int(b[2])) + "." + strconv.Itoa(int(b[1])) + "." + strconv.Itoa(int(b[0])) + ".in-addr.arpa."
}

func buildQuery(name string, qtype uint16, qu bool) []byte {
	m := new(dns.Msg)
	m.Id = 0
	m.Question = []dns.Question{{Name: name, Qtype: qtype, Qclass: dns.ClassINET}}
	if qu {
		m.Question[0].Qclass |= mdnsUnicastResponse
	}
	b, _ := m.Pack()
	return b
}

// ParseMDNS extracts the hint carried by an mDNS/LLMNR reply from src.
// followUp lists TXT instances announced without their TXT record.
func ParseMDNS(pkt []byte, src netip.Addr, source string) (hint Hint, followUp []string, ok bool) {
	return parseMDNS(pkt, src, source, false)
}

// parseMDNS reads one reply. A Bonjour sleep proxy (HomePod, Apple TV)
// answers for the sleeping hosts it stands in for, so a service instance only
// describes src when its SRV target is a name of src: an A record of src or
// the reverse PTR of src in the same packet. A TXT-only instance
// (_device-info) belongs to src when another tied instance shares its label.
// strict (multicast replies) drops instances that cannot be tied; otherwise
// (a direct unicast exchange with src) only instances whose SRV points at
// another host are dropped.
func parseMDNS(pkt []byte, src netip.Addr, source string, strict bool) (hint Hint, followUp []string, ok bool) {
	var m dns.Msg
	if m.Unpack(pkt) != nil || !m.Response {
		return Hint{}, nil, false
	}
	hint = Hint{IP: src, Source: source}
	rrs := append(append(append([]dns.RR{}, m.Answer...), m.Ns...), m.Extra...)
	lower := strings.ToLower
	srcNames := map[string]bool{} // names that resolve to src
	hasA := map[string]bool{}     // names with an A record in the packet
	srv := map[string]string{}    // instance → target
	haveTXT := map[string]bool{}
	var host string
	rev := lower(reverseName(src))
	for _, rr := range rrs {
		switch v := rr.(type) {
		case *dns.A:
			hasA[lower(v.Hdr.Name)] = true
			if a, okA := netip.AddrFromSlice(v.A); okA && a.Unmap() == src {
				srcNames[lower(v.Hdr.Name)] = true
				if host == "" {
					host = hostLabel(v.Hdr.Name)
				}
			}
		case *dns.PTR:
			if lower(v.Hdr.Name) == rev {
				srcNames[lower(v.Ptr)] = true
				host = hostLabel(v.Ptr)
			}
		case *dns.SRV:
			srv[lower(v.Hdr.Name)] = lower(v.Target)
		case *dns.TXT:
			haveTXT[lower(v.Hdr.Name)] = true
		}
	}
	label := func(inst string) string { l, _, _ := strings.Cut(inst, "._"); return lower(l) }
	tiedLabels := map[string]bool{}
	for inst, target := range srv {
		if srcNames[target] {
			tiedLabels[label(inst)] = true
		}
	}
	service := func(inst string) string { _, svc, _ := strings.Cut(inst, "._"); return svc }
	txtOnly := map[string]int{} // service → TXT-only instances in the packet
	for name := range haveTXT {
		if _, has := srv[name]; !has {
			txtOnly[service(name)]++
		}
	}
	instanceOK := func(inst string) bool {
		inst = lower(inst)
		if target, has := srv[inst]; has {
			// unicast: only a target shown to be another host is dropped
			return srcNames[target] || !strict && !hasA[target]
		}
		// untied TXT-only: unicast accepts it only when it is the single
		// instance of its service in the packet
		return tiedLabels[label(inst)] || !strict && txtOnly[service(inst)] <= 1
	}
	// host comes only from names of src (A record or reverse PTR above);
	// an SRV target that cannot be tied to src is never taken as its name.
	for _, rr := range rrs {
		if v, isTXT := rr.(*dns.TXT); isTXT && instanceOK(v.Hdr.Name) {
			for _, s := range v.Txt {
				if m := txtModel(s); m != "" && hint.Model == "" {
					hint.Model = m
				}
			}
		}
	}
	var instance string
	for _, rr := range rrs {
		ptr, isPTR := rr.(*dns.PTR)
		if !isPTR {
			continue
		}
		name := lower(ptr.Hdr.Name)
		switch {
		case strings.HasSuffix(name, ".in-addr.arpa."):
		case name == "_services._dns-sd._udp.local.":
			// a multicast enumeration names service types, not instances: it
			// counts only alongside an instance tied to src, and never as
			// the _apple-mobdev2 phone signal
			svc := serviceKey(ptr.Ptr)
			if !strict || len(tiedLabels) > 0 && svc != "_apple-mobdev2._tcp" {
				hint.Services = appendService(hint.Services, svc)
			}
		case strings.HasPrefix(name, "_") && strings.HasSuffix(name, ".local."):
			if !instanceOK(ptr.Ptr) {
				continue
			}
			hint.Services = appendService(hint.Services, serviceKey(name))
			lbl, _, found := strings.Cut(ptr.Ptr, "._")
			if found && !anonymousServices[name] {
				// a space may be escaped as \032 or "\ "
				lbl = unescapeLabel(lbl)
				if name == "_workstation._tcp.local." { // "host [aa:bb:cc:dd:ee:ff]"
					if i := strings.LastIndex(lbl, " ["); i > 0 {
						lbl = lbl[:i]
					}
				}
				if instance == "" || name == "_device-info._tcp.local." || name == "_companion-link._tcp.local." {
					instance = lbl
				}
			}
			if modelServices[name] && !haveTXT[lower(ptr.Ptr)] && len(followUp) < maxFollowUp {
				followUp = appendUnique(followUp, ptr.Ptr)
			}
		}
	}
	hint.Name = host
	if instance != "" && !strings.ContainsAny(instance, "\x00") {
		hint.Name = instance
	}
	return hint, followUp, true
}

func serviceKey(n string) string {
	n = strings.TrimSuffix(strings.ToLower(n), ".local.")
	return strings.TrimSuffix(n, ".")
}

func hostLabel(n string) string {
	n = strings.TrimSuffix(n, ".")
	n = strings.TrimSuffix(n, ".local")
	return unescapeLabel(n)
}

// unescapeLabel decodes DNS presentation escapes (\032, \.) in a label.
func unescapeLabel(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
			b.WriteByte(byte((int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0')) & 0xff))
			i += 3
			continue
		}
		i++
		b.WriteByte(s[i])
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// MDNS queries hosts directly on UDP 5353 (reverse PTR, device-info,
// service list) and the multicast group on every scanned interface.
type MDNS struct {
	Port      int           // unicast port, default 5353
	Multicast string        // group address, default 224.0.0.251:5353; "-" disables
	Wait      time.Duration // default 1.5s
}

func (MDNS) Name() string { return SourceMDNS }

func (d MDNS) Identify(ctx context.Context, req IdentifyRequest) error {
	port, wait := d.Port, d.Wait
	if port == 0 {
		port = 5353
	}
	if wait == 0 {
		wait = 1500 * time.Millisecond
	}
	group := d.Multicast
	if group == "" {
		group = "224.0.0.251:5353"
	}
	errc := make(chan error, 1)
	go func() {
		if group == "-" {
			errc <- nil
			return
		}
		errc <- multicastQuery(ctx, group, req, wait+time.Second, mdnsServices, SourceMDNS)
	}()
	fl := newFollowLimiter()
	err := udpExchange(ctx, req.Subnets, req.Hosts, port, req.Pacer, wait,
		func(ip netip.Addr) [][]byte {
			pkts := [][]byte{buildQuery(reverseName(ip), dns.TypePTR, true)}
			for _, svc := range mdnsUnicastServices {
				pkts = append(pkts, buildQuery(svc, dns.TypePTR, true))
			}
			return pkts
		},
		func(src netip.Addr, pkt []byte, send func([]byte)) {
			hint, follow, ok := ParseMDNS(pkt, src, SourceMDNS)
			if !ok {
				return
			}
			for _, inst := range follow {
				if fl.allow(src, inst) {
					send(buildQuery(inst, dns.TypeTXT, true))
				}
			}
			report(req, hint)
		})
	if mErr := <-errc; err == nil && ctx.Err() == nil {
		_ = mErr // multicast is best effort
	}
	return err
}

func report(req IdentifyRequest, h Hint) {
	if h.Name != "" || h.Model != "" || len(h.Services) > 0 {
		req.Report(h)
	}
}

// multicastQuery sends PTR queries (QU set) to group out of every scanned
// interface and feeds the unicast replies to the shared parser.
func multicastQuery(ctx context.Context, group string, req IdentifyRequest, wait time.Duration, services []string, source string) error {
	pkts := make([][]byte, 0, len(services))
	for _, svc := range services {
		pkts = append(pkts, buildQuery(svc, dns.TypePTR, true))
	}
	return multicastExchange(ctx, req.Subnets, group, pkts, req.Pacer, wait, func(src netip.Addr, pkt []byte) {
		if hint, _, ok := parseMDNS(pkt, src, source, true); ok {
			report(req, hint)
		}
	})
}

// LLMNR sends reverse PTR queries to hosts on UDP 5355 (answered by Windows).
type LLMNR struct {
	Port int
	Wait time.Duration
}

func (LLMNR) Name() string { return SourceLLMNR }

func (l LLMNR) Identify(ctx context.Context, req IdentifyRequest) error {
	port, wait := l.Port, l.Wait
	if port == 0 {
		port = 5355
	}
	if wait == 0 {
		wait = time.Second
	}
	return udpExchange(ctx, req.Subnets, req.Hosts, port, req.Pacer, wait,
		func(ip netip.Addr) [][]byte { return [][]byte{buildQuery(reverseName(ip), dns.TypePTR, false)} },
		func(src netip.Addr, pkt []byte, _ func([]byte)) {
			if hint, _, ok := ParseMDNS(pkt, src, SourceLLMNR); ok && hint.Name != "" {
				req.Report(Hint{IP: src, Source: SourceLLMNR, Name: hint.Name})
			}
		})
}
