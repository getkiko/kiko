package lanscan

import (
	"net"
	"net/netip"
	"regexp"
	"strings"
)

// InterfaceInfo is one network interface as the scanner needs it.
type InterfaceInfo struct {
	Name  string
	Flags net.Flags
	MAC   net.HardwareAddr
	Addrs []netip.Prefix
}

// Subnet is a private IPv4 subnet to scan.
type Subnet struct {
	Prefix netip.Prefix // masked; at most /24-sized
	Self   netip.Addr
	Iface  string
	MAC    net.HardwareAddr
}

// SystemInterfaces lists the interfaces of this machine.
func SystemInterfaces() ([]InterfaceInfo, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var res []InterfaceInfo
	for _, ifc := range list {
		info := InterfaceInfo{Name: ifc.Name, Flags: ifc.Flags, MAC: ifc.HardwareAddr}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				info.Addrs = append(info.Addrs, p)
			}
		}
		res = append(res, info)
	}
	return res, nil
}

// virtualPrefixes are interfaces that never carry a physical LAN.
var virtualName = regexp.MustCompile(`^(lo\d*|docker.*|veth.*|br-.+|virbr\d+|vmnet\d+|vboxnet\d+|utun\d+|tun\d+|tap\d+|tailscale.*|zt[0-9a-z]{6,}|awdl\d+|llw\d+|bridge\d+)$`)

func isVirtualInterface(name string) bool { return virtualName.MatchString(strings.ToLower(name)) }

// Subnets keeps up, broadcast-capable interfaces with RFC1918 IPv4 addresses.
// A subnet larger than /24 is narrowed to the /24 holding the local address;
// subnets with fewer than two other hosts are dropped. Duplicates are merged.
func Subnets(ifs []InterfaceInfo) []Subnet {
	var res []Subnet
	seen := map[netip.Prefix]bool{}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		lower := strings.ToLower(ifc.Name)
		if isVirtualInterface(lower) {
			continue
		}
		for _, a := range ifc.Addrs {
			self := a.Addr().Unmap()
			if !self.Is4() || !self.IsPrivate() || self.IsLinkLocalUnicast() {
				continue
			}
			bits := a.Bits()
			if bits > 24 {
				// keep as is
			} else {
				bits = 24
			}
			if bits > 30 {
				continue
			}
			prefix := netip.PrefixFrom(self, bits).Masked()
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			res = append(res, Subnet{Prefix: prefix, Self: self, Iface: ifc.Name, MAC: ifc.MAC})
		}
	}
	return res
}

// Hosts lists the scannable addresses of s: no network, broadcast or self.
func (s Subnet) Hosts() []netip.Addr {
	var res []netip.Addr
	first := s.Prefix.Addr()
	size := 1 << (32 - s.Prefix.Bits())
	addr := first
	for i := 0; i < size; i++ {
		if i != 0 && i != size-1 && addr != s.Self {
			res = append(res, addr)
		}
		addr = addr.Next()
	}
	return res
}

// Contains reports whether ip is inside one of the subnets.
func Contains(subnets []Subnet, ip netip.Addr) bool {
	for _, s := range subnets {
		if s.Prefix.Contains(ip) {
			return true
		}
	}
	return false
}
