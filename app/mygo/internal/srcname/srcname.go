// Package srcname names the source addresses the connection and usage
// pages show in their filters, lists and inspectors: an IP is followed by
// the name of the Routing device that owns it, or the local server for the host's own
// addresses. It only adds display text; filter values, sorting, search and
// copies stay the IP.
package srcname

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Local is the name shown for the host's own addresses.
const Local = "本机"

// Names resolves addresses to device names.
type Names struct {
	local   map[netip.Addr]bool
	hosts   map[netip.Addr]string
	subnets []subnet
}

type subnet struct {
	prefix netip.Prefix
	name   string
}

// New builds the names of devices (the document's route devices) and the
// host's own addresses, plain IPs or CIDRs as the local address list has
// them. Either may be empty.
func New(devices []*model.RouteDevice, local []string) *Names {
	n := &Names{local: map[netip.Addr]bool{}, hosts: map[netip.Addr]string{}}
	for _, a := range local {
		if p, ok := parse(a); ok && p.IsSingleIP() {
			n.local[p.Addr()] = true
		}
	}
	for _, d := range devices {
		if d == nil {
			continue
		}
		name := strings.TrimSpace(d.Name)
		if name == "" {
			continue
		}
		for _, a := range d.Addresses {
			p, ok := parse(a)
			if !ok {
				continue
			}
			if p.IsSingleIP() {
				if _, taken := n.hosts[p.Addr()]; !taken {
					n.hosts[p.Addr()] = name
				}
			} else {
				n.subnets = append(n.subnets, subnet{p, name})
			}
		}
	}
	return n
}

// FromDoc is New for the devices of doc. The host's addresses are local
// (the interfaces' addresses as the daemon detects them) and those the
// document gives the host, with or without a running core: the host
// address of every TUN inbound (its configured addresses, or the
// platform's defaults the core assigns without them; a disabled TUN
// counts too, for the history it left) and the supplementary local-server
// addresses, whether or not the local scope is on: the scope switch
// decides routing, not whose address it is. Ranges among the
// supplementary addresses are left out, for they may cover other
// devices; the loopback addresses are always the host's (Name).
func FromDoc(doc *model.Doc, local []string) *Names {
	if doc == nil {
		return New(nil, local)
	}
	local = append(slices.Clone(local), model.LocalSourceAddresses(doc, nil)...)
	for _, in := range doc.Inbounds {
		if in == nil || in.Type != "tun" || in.Enabled {
			continue
		}
		for _, a := range model.Values(in.Fields["address"]) {
			host, _, _ := strings.Cut(a, "/")
			local = append(local, host)
		}
	}
	return New(doc.Route.Devices, local)
}

// Name is the name for ip (a bare address; case, brackets-free IPv6 and
// IPv4-mapped forms are normalised), "" when it has none. The host's own
// addresses, the loopback ones among them, win over devices; an exact
// device address wins over a range, and a narrower range over a wider
// one.
func (n *Names) Name(ip string) string {
	if n == nil {
		return ""
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	addr = addr.Unmap().WithZone("")
	if n.local[addr] || addr.IsLoopback() {
		return Local
	}
	if name, ok := n.hosts[addr]; ok {
		return name
	}
	best, name := -1, ""
	for _, s := range n.subnets {
		if s.prefix.Contains(addr) && s.prefix.Bits() > best {
			best, name = s.prefix.Bits(), s.name
		}
	}
	return name
}

// Text is the plain text of an option, "IP name", for matching a search.
func (n *Names) Text(ip string) string {
	if name := n.Name(ip); name != "" {
		return ip + " " + name
	}
	return ip
}

func parse(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		return netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p)).Masked(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		a = a.Unmap().WithZone("")
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

// unmapBits is the prefix length of p after its address loses an
// IPv4-mapped form.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		return max(p.Bits()-96, 0)
	}
	return p.Bits()
}
