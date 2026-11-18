package lanscan

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

// subnetOf returns the scanned subnet holding ip.
func subnetOf(subnets []Subnet, ip netip.Addr) (Subnet, bool) {
	for _, s := range subnets {
		if s.Prefix.Contains(ip) {
			return s, true
		}
	}
	return Subnet{}, false
}

// listenUDP opens a UDP socket for traffic to subnet s: bound to its local
// address and, where the platform allows, to its interface, so TUN routes
// never carry it. Binding the interface is best effort; the local address
// binding is kept when it fails.
func listenUDP(ctx context.Context, s Subnet) (net.PacketConn, error) {
	local := netip.AddrPortFrom(s.Self, 0).String()
	lc := net.ListenConfig{Control: bindInterface(s.Iface)}
	if c, err := lc.ListenPacket(ctx, "udp4", local); err == nil {
		return c, nil
	}
	return net.ListenPacket("udp4", local)
}

// listenUDPFor opens the socket for traffic to ip: bound to its subnet when
// it has one, otherwise unbound.
func listenUDPFor(ctx context.Context, subnets []Subnet, ip netip.Addr) (net.PacketConn, error) {
	if s, ok := subnetOf(subnets, ip); ok {
		if c, err := listenUDP(ctx, s); err == nil {
			return c, nil
		}
	}
	return net.ListenPacket("udp4", ":0")
}

// dialerFor returns a dialer for network ("tcp…" or "udp…") bound to the
// subnet of ip, or a plain one. The local address type matches network.
func dialerFor(subnets []Subnet, ip netip.Addr, network string, timeout time.Duration) *net.Dialer {
	s, ok := subnetOf(subnets, ip)
	if !ok {
		return &net.Dialer{Timeout: timeout}
	}
	var local net.Addr = &net.TCPAddr{IP: s.Self.AsSlice()}
	if strings.HasPrefix(network, "udp") {
		local = &net.UDPAddr{IP: s.Self.AsSlice()}
	}
	return &net.Dialer{Timeout: timeout, Control: bindInterface(s.Iface), LocalAddr: local}
}

// multicastExchange sends pkts to group out of every subnet's interface,
// each from a socket bound to that subnet (address, interface and multicast
// interface), then hands the replies to handle until wait after the last
// send. handle may run on several goroutines at once.
func multicastExchange(ctx context.Context, subnets []Subnet, group string, pkts [][]byte, pacer *Pacer, wait time.Duration,
	handle func(src netip.Addr, pkt []byte)) error {
	dst, err := net.ResolveUDPAddr("udp4", group)
	if err != nil {
		return err
	}
	var (
		mu      sync.Mutex
		conns   []net.PacketConn
		readers sync.WaitGroup
	)
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.SetDeadline(time.Now())
		}
	})
	defer stop()
	finish := func() {
		mu.Lock()
		for _, c := range conns {
			_ = c.SetReadDeadline(time.Now())
		}
		mu.Unlock()
		readers.Wait()
		for _, c := range conns {
			_ = c.Close()
		}
	}
	defer finish()
	for _, sub := range subnets {
		conn, err := listenUDP(ctx, sub)
		if err != nil {
			continue
		}
		mu.Lock()
		conns = append(conns, conn)
		if ctx.Err() != nil {
			_ = conn.SetDeadline(time.Now())
		}
		mu.Unlock()
		if ifc, err := net.InterfaceByName(sub.Iface); err == nil {
			_ = ipv4.NewPacketConn(conn).SetMulticastInterface(ifc)
		}
		readers.Add(1)
		go func() {
			defer readers.Done()
			buf := make([]byte, 9000)
			for {
				n, from, err := conn.ReadFrom(buf)
				if err != nil {
					return
				}
				udp, ok := from.(*net.UDPAddr)
				if !ok {
					continue
				}
				src, ok := netip.AddrFromSlice(udp.IP)
				if !ok || !Contains(subnets, src.Unmap()) {
					continue
				}
				handle(src.Unmap(), append([]byte(nil), buf[:n]...))
			}
		}()
		for _, pkt := range pkts {
			if err := pacer.Wait(ctx); err != nil {
				return err
			}
			_, _ = conn.WriteTo(pkt, dst)
		}
	}
	return sleep(ctx, wait)
}
