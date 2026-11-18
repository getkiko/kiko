package dnsservers

import (
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// dnsTypes are the resolver types the editor offers, in its order.
var dnsTypes = []string{"local", "udp", "tcp", "tls", "https", "quic", "h3", "hosts", "fakeip", "dhcp", "tailscale"}

// strategies are the query strategies with their labels.
var strategies = []struct{ value, label string }{
	{"", "默认"},
	{"prefer_ipv4", "IPv4 优先"},
	{"prefer_ipv6", "IPv6 优先"},
	{"ipv4_only", "仅 IPv4"},
	{"ipv6_only", "仅 IPv6"},
}

// strategyOptions are the strategies a document offers: the IPv6 ones only
// with IPv6 on.
func strategyOptions(ipv6 bool) []kit.Option {
	var out []kit.Option
	for _, s := range strategies {
		if ipv6 || !strings.Contains(s.value, "ipv6") {
			out = append(out, kit.Option{Value: s.value, Label: s.label})
		}
	}
	return out
}
