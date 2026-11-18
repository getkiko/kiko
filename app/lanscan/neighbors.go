package lanscan

import (
	"bufio"
	"context"
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

// Host reads the neighbor table and default gateways of this machine; the
// per-platform methods live in platform_*.go. Run and ReadFile are injectable.
type Host struct {
	Run      Runner
	ReadFile func(string) ([]byte, error)
}

// Neighbor is one resolved entry of the system neighbor (ARP) table.
type Neighbor struct {
	IP    netip.Addr
	MAC   net.HardwareAddr
	Iface string
}

// Runner executes a command and returns its stdout; injectable for tests.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs real commands.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ParseARPUnix parses `arp -an` (macOS/BSD and Linux net-tools):
//
//	? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]
func ParseARPUnix(out []byte) []Neighbor {
	var res []Neighbor
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		open, closeIdx := strings.IndexByte(sc.Text(), '('), strings.IndexByte(sc.Text(), ')')
		if open < 0 || closeIdx < open || len(f) < 4 {
			continue
		}
		ip, err := netip.ParseAddr(sc.Text()[open+1 : closeIdx])
		if err != nil || !ip.Is4() {
			continue
		}
		var mac net.HardwareAddr
		iface := ""
		for i, w := range f {
			if w == "at" && i+1 < len(f) {
				mac = parseMAC(f[i+1])
			}
			if w == "on" && i+1 < len(f) {
				iface = f[i+1]
			}
		}
		if validNeighborMAC(mac) {
			res = append(res, Neighbor{IP: ip, MAC: mac, Iface: iface})
		}
	}
	return res
}

// ParseProcNetARP parses /proc/net/arp.
//
//	IP address  HW type  Flags  HW address  Mask  Device
func ParseProcNetARP(data []byte) []Neighbor {
	var res []Neighbor
	for i, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 6 {
			continue
		}
		ip, err := netip.ParseAddr(f[0])
		mac := parseMAC(f[3])
		if err != nil || !ip.Is4() || f[2] == "0x0" || !validNeighborMAC(mac) {
			continue
		}
		res = append(res, Neighbor{IP: ip, MAC: mac, Iface: f[5]})
	}
	return res
}

// ParseARPWindows parses `arp -a`:
//
//	192.168.1.1           aa-bb-cc-dd-ee-ff     dynamic
func ParseARPWindows(out []byte) []Neighbor {
	var res []Neighbor
	iface := ""
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "Interface:" {
			iface = f[1]
			continue
		}
		if len(f) < 3 || f[2] != "dynamic" && f[2] != "static" {
			continue
		}
		ip, err := netip.ParseAddr(f[0])
		mac := parseMAC(f[1])
		if err != nil || !ip.Is4() || !validNeighborMAC(mac) {
			continue
		}
		res = append(res, Neighbor{IP: ip, MAC: mac, Iface: iface})
	}
	return res
}

func parseMAC(s string) net.HardwareAddr {
	s = strings.ReplaceAll(s, "-", ":")
	// macOS prints single-digit bytes without padding: 0:1a:2b:3:4:5.
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return nil
	}
	for i, p := range parts {
		if len(p) == 1 {
			parts[i] = "0" + p
		}
	}
	mac, err := net.ParseMAC(strings.Join(parts, ":"))
	if err != nil {
		return nil
	}
	return mac
}

func validNeighborMAC(mac net.HardwareAddr) bool {
	if len(mac) != 6 || mac[0]&1 != 0 {
		return false
	}
	for _, b := range mac {
		if b != 0 && b != 0xff {
			return true
		}
	}
	return false
}

// ParseRouteUnix extracts default gateways from `netstat -rn -f inet`.
func ParseRouteUnix(out []byte) []netip.Addr {
	var res []netip.Addr
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "default" {
			if ip, err := netip.ParseAddr(f[1]); err == nil && ip.Is4() {
				res = append(res, ip)
			}
		}
	}
	return res
}

// ParseProcNetRoute extracts default gateways from /proc/net/route.
func ParseProcNetRoute(data []byte) []netip.Addr {
	var res []netip.Addr
	for i, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 3 || f[1] != "00000000" || len(f[2]) != 8 {
			continue
		}
		var b [4]byte
		ok := true
		for j := 0; j < 4; j++ { // little-endian hex
			var v byte
			for _, c := range f[2][j*2 : j*2+2] {
				v <<= 4
				switch {
				case c >= '0' && c <= '9':
					v |= byte(c - '0')
				case c >= 'A' && c <= 'F':
					v |= byte(c-'A') + 10
				case c >= 'a' && c <= 'f':
					v |= byte(c-'a') + 10
				default:
					ok = false
				}
			}
			b[3-j] = v
		}
		if ok && b != [4]byte{} {
			res = append(res, netip.AddrFrom4(b))
		}
	}
	return res
}

// ParseRoutePrintWindows extracts default gateways from `route print -4`.
func ParseRoutePrintWindows(out []byte) []netip.Addr {
	var res []netip.Addr
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "0.0.0.0" && f[1] == "0.0.0.0" {
			if ip, err := netip.ParseAddr(f[2]); err == nil && ip.Is4() {
				res = append(res, ip)
			}
		}
	}
	return res
}
