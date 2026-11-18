package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// errNonPublicAddress is what a publicOnly download answers when its target
// (or a redirect's) resolves to an address outside the public internet.
var errNonPublicAddress = errors.New("不允许访问内网地址")

// cgnat is the shared address space (100.64.0.0/10) that Tailscale and
// carrier-grade NAT use.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Further ranges that never hold a public host: benchmarking (198.18.0.0/15),
// IETF protocol assignments (192.0.0.0/24), the reserved class E (240.0.0.0/4),
// site-local IPv6 (fec0::/10), the deprecated IPv4-compatible IPv6 (::/96) and
// the NAT64 well-known prefix (64:ff9b::/96), whose address stands for the
// IPv4 address it embeds.
var (
	benchmarking = netip.MustParsePrefix("198.18.0.0/15")
	ietfProtocol = netip.MustParsePrefix("192.0.0.0/24")
	reservedE    = netip.MustParsePrefix("240.0.0.0/4")
	siteLocal6   = netip.MustParsePrefix("fec0::/10")
	compatible6  = netip.MustParsePrefix("::/96")
	nat64        = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local   = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour    = netip.MustParsePrefix("2002::/16")
	teredo       = netip.MustParsePrefix("2001::/32")
)

// nonPublicAddress reports whether ip is a loopback, private, link-local,
// unspecified or multicast address, or one of the reserved ranges above (or
// the shared 100.64.0.0/10 space, the broadcast address or 0.0.0.0/8).
func nonPublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && nat64.Contains(ip) {
		b := ip.As16()
		return nonPublicAddress(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	if ip.Is6() {
		b := ip.As16()
		switch {
		case nat64Local.Contains(ip):
			// The local-use NAT64 prefix reaches the local network.
			return true
		case sixToFour.Contains(ip):
			return nonPublicAddress(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		case teredo.Contains(ip):
			// The server address and the obfuscated client address.
			return nonPublicAddress(netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]})) ||
				nonPublicAddress(netip.AddrFrom4([4]byte{b[12] ^ 0xff, b[13] ^ 0xff, b[14] ^ 0xff, b[15] ^ 0xff}))
		case (b[8] == 0 || b[8] == 2) && b[9] == 0 && b[10] == 0x5e && b[11] == 0xfe:
			// ISATAP: the interface identifier carries an IPv4 address.
			return nonPublicAddress(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
	}
	switch {
	case !ip.IsValid(), ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast(), ip.IsUnspecified():
		return true
	case ip.Is4():
		return ip.As4()[0] == 0 || cgnat.Contains(ip) || benchmarking.Contains(ip) || ietfProtocol.Contains(ip) || reservedE.Contains(ip)
	}
	return siteLocal6.Contains(ip) || compatible6.Contains(ip)
}

// publicAllowHook lets tests pass a local server as if it were public; nil in
// the program.
var publicAllowHook func(address string) bool

// publicOnlyControl checks the address a connection is about to be made to:
// the address is the resolved one, so a name that changes its answer between
// the check and the connection (DNS rebinding) is caught as well.
func publicOnlyControl(network, address string, _ syscall.RawConn) error {
	if publicAllowHook != nil && publicAllowHook(address) {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errNonPublicAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || nonPublicAddress(ip) {
		return errNonPublicAddress
	}
	return nil
}

// publicOnlyTransport is a transport that connects to public addresses only.
// It ignores proxy settings: a proxy would resolve and reach the target
// itself, past the check.
func publicOnlyTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: publicOnlyControl}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
	}
}

// redirectTransport sends a download's first request over first and every
// redirect that follows over rest. A download has one client per request, so
// the count is not shared.
type redirectTransport struct {
	first, rest http.RoundTripper
	used        bool
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.used {
		t.used = true
		return t.first.RoundTrip(req)
	}
	return t.rest.RoundTrip(req)
}
