package model

import (
	"strconv"
	"strings"
)

// This file holds the TUN address helpers the editors and the defaults
// share.

// PrivateCIDR are the private and reserved networks direct rules use.
var PrivateCIDR = []string{
	"10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12",
	"127.0.0.0/8", "224.0.0.0/4", "169.254.0.0/16", "100.64.0.0/10",
}

// tunExcludeV4 is what a TUN bypasses by default: private, link-local,
// multicast and reserved networks. Public direct routing is up to rules.
var tunExcludeV4 = []string{
	"10.0.0.0/8", "172.16.0.0/12", "100.64.0.0/10", "192.168.0.0/16", "169.254.0.0/16",
	"192.0.0.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
}

// tunExcludeV6 is excluded in addition while IPv6 is on.
var tunExcludeV6 = []string{"fc00::/7", "fe80::/10", "::1/128", "ff00::/8", "2001:db8::/32"}

// TUN interface addresses.
const (
	TunAddrV4        = "172.18.0.1/30"
	TunAddrV6Android = "fd00::1/126"
	TunAddrV6        = "fdfe:dcba:9876::1/126"
)

func ipv4ToInt(s string) uint32 {
	var n uint32
	for _, part := range strings.Split(s, ".") {
		v, _ := strconv.Atoi(part)
		n = n<<8 + uint32(v)
	}
	return n
}

func intToIPv4(n uint32) string {
	return strconv.Itoa(int(n>>24)) + "." + strconv.Itoa(int(n>>16&255)) + "." + strconv.Itoa(int(n>>8&255)) + "." + strconv.Itoa(int(n&255))
}

func maskBits(bits int) uint32 {
	if bits <= 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

// RemoveIPRange returns the smallest CIDR list covering outer without
// inner (IPv4 only); outer itself when inner is not inside it.
func RemoveIPRange(outer, inner string) []string {
	oAddr, oBitsText, _ := strings.Cut(outer, "/")
	iAddr, iBitsText, _ := strings.Cut(inner, "/")
	oBits, err1 := strconv.Atoi(oBitsText)
	iBits, err2 := strconv.Atoi(iBitsText)
	if !strings.Contains(oAddr, ".") || !strings.Contains(iAddr, ".") || err1 != nil || err2 != nil || iBits <= oBits {
		return []string{outer}
	}
	oNet := ipv4ToInt(oAddr) & maskBits(oBits)
	iNet := ipv4ToInt(iAddr) & maskBits(iBits)
	// The former TypeScript's containment mask shifted by 32 - bits modulo
	// 32, so a /0 outer network never contains anything; kept for parity.
	if iNet&(^uint32(0)<<((32-oBits)&31)) != oNet {
		return []string{outer}
	}
	var out []string
	net := oNet
	for bits := oBits + 1; bits <= iBits; bits++ {
		half := uint32(1) << (32 - bits)
		innerInUpper := iNet&half != 0
		if innerInUpper {
			out = append(out, intToIPv4(net)+"/"+strconv.Itoa(bits))
			net += half
		} else {
			out = append(out, intToIPv4(net+half)+"/"+strconv.Itoa(bits))
		}
	}
	return out
}

// TunExcludeV4 is the TUN IPv4 exclusion table; the interface's own
// addresses are routed locally by the system.
func TunExcludeV4(_ []string) []string { return append([]string{}, tunExcludeV4...) }

// TunDefaultExclude is the full exclusion table written into a new TUN
// form (and by "Recompute excluded ranges"), the same as the emitter's.
func TunDefaultExclude(addresses []string, ipv6 bool) []string {
	out := TunExcludeV4(addresses)
	if ipv6 {
		out = append(out, tunExcludeV6...)
	}
	return unique(out)
}

// TunDefaultAddress is a TUN's interface addresses for a platform.
func TunDefaultAddress(platform string, ipv6 bool) []string {
	out := []string{TunAddrV4}
	if ipv6 {
		if platform == PlatformAndroid {
			out = append(out, TunAddrV6Android)
		} else {
			out = append(out, TunAddrV6)
		}
	}
	return out
}
