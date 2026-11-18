package lanscan

import (
	"context"
	"net"
	"net/netip"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// WakeProbe sends one packet to every target so the OS resolves it with ARP:
// an ICMP echo where an unprivileged ICMP socket exists (macOS), otherwise an
// empty datagram to the discard port. Each subnet gets its own sockets bound
// to its local address and, where the platform allows, to its interface, so
// TUN routes never carry the probes. No raw socket or privilege is needed.
func WakeProbe(ctx context.Context, subnets []Subnet, targets []netip.Addr, p *Pacer) error {
	type probe struct {
		icmp   net.PacketConn
		udp    net.PacketConn
		opened bool
	}
	probes := make([]probe, len(subnets))
	defer func() {
		for _, pr := range probes {
			if pr.icmp != nil {
				_ = pr.icmp.Close()
			}
			if pr.udp != nil {
				_ = pr.udp.Close()
			}
		}
	}()
	var fallback net.PacketConn // targets outside every subnet
	defer func() {
		if fallback != nil {
			_ = fallback.Close()
		}
	}()
	open := func(i int) *probe {
		pr := &probes[i]
		if !pr.opened {
			pr.opened = true
			if c, err := listenICMP(subnets[i]); err == nil {
				pr.icmp = c
			}
			if c, err := listenUDP(ctx, subnets[i]); err == nil {
				pr.udp = c
			}
		}
		return pr
	}
	seq := 0
	for _, ip := range targets {
		if err := p.Wait(ctx); err != nil {
			return err
		}
		var icmpConn, udpConn net.PacketConn
		for i, s := range subnets {
			if s.Prefix.Contains(ip) {
				pr := open(i)
				icmpConn, udpConn = pr.icmp, pr.udp
				break
			}
		}
		if udpConn == nil && icmpConn == nil {
			if fallback == nil {
				c, err := net.ListenPacket("udp4", ":0")
				if err != nil {
					return err
				}
				fallback = c
			}
			udpConn = fallback
		}
		seq++
		dst := &net.UDPAddr{IP: ip.AsSlice()}
		if icmpConn != nil {
			if _, err := icmpConn.WriteTo(echoRequest(seq), dst); err == nil {
				continue
			}
		}
		if udpConn != nil {
			_, _ = udpConn.WriteTo([]byte{0}, &net.UDPAddr{IP: ip.AsSlice(), Port: 9})
		}
	}
	return nil
}

func echoRequest(seq int) []byte {
	m := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: 0x5062, Seq: seq & 0xffff, Data: []byte("kiko")}}
	b, _ := m.Marshal(nil)
	return b
}
