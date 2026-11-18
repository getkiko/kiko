package lanscan

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// udpExchange sends build(ip) to every host on port, paced, then collects
// replies for wait after the last send. Hosts of a scanned subnet share one
// socket bound to that subnet's address and interface; other hosts share an
// unbound one. handle receives each reply with its source address and may
// send follow-ups with send. Replies are matched by source IP only.
func udpExchange(ctx context.Context, subnets []Subnet, hosts []netip.Addr, port int, pacer *Pacer, wait time.Duration,
	build func(netip.Addr) [][]byte, handle func(src netip.Addr, pkt []byte, send func([]byte))) error {
	conns := map[netip.Prefix]net.PacketConn{} // zero prefix: hosts outside every subnet
	var (
		mu      sync.Mutex // guards conns against the cancel callback
		readers sync.WaitGroup
	)
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.SetDeadline(time.Now())
		}
	})
	defer stop()
	read := func(conn net.PacketConn) {
		defer readers.Done()
		buf := make([]byte, 4096)
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
			if !ok {
				continue
			}
			src = src.Unmap()
			pkt := append([]byte(nil), buf[:n]...)
			handle(src, pkt, func(b []byte) {
				if pacer.Wait(ctx) != nil {
					return
				}
				_, _ = conn.WriteTo(b, &net.UDPAddr{IP: src.AsSlice(), Port: udp.Port})
			})
		}
	}
	connFor := func(ip netip.Addr) (net.PacketConn, error) {
		key := netip.Prefix{}
		if s, ok := subnetOf(subnets, ip); ok {
			key = s.Prefix
		}
		mu.Lock()
		defer mu.Unlock()
		if c := conns[key]; c != nil {
			return c, nil
		}
		c, err := listenUDPFor(ctx, subnets, ip)
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			_ = c.SetDeadline(time.Now())
		}
		conns[key] = c
		readers.Add(1)
		go read(c)
		return c, nil
	}
	finish := func() {
		mu.Lock()
		for _, c := range conns {
			_ = c.SetReadDeadline(time.Now())
		}
		mu.Unlock()
		readers.Wait()
	}
	for _, h := range hosts {
		conn, err := connFor(h)
		if err != nil {
			finish()
			return err
		}
		for _, pkt := range build(h) {
			if err := pacer.Wait(ctx); err != nil {
				finish()
				return err
			}
			_, _ = conn.WriteTo(pkt, &net.UDPAddr{IP: h.AsSlice(), Port: port})
		}
	}
	if err := sleep(ctx, wait); err != nil {
		finish()
		return err
	}
	finish()
	return ctx.Err()
}
