package lanscan

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Fingerprint ports, most telling first so a scan cut short by its time
// limit still has the Apple lockdownd answer.
const portLockdown = 62078 // iOS / iPadOS lockdownd

var defaultFingerprintPorts = []int{
	portLockdown,
	7000, 5000, 3689, 548, // AirPlay, AirTunes, DAAP, AFP
	22, 445, 139, 3389, // SSH, SMB, NetBIOS session, RDP
	80, 443, 53, // web UI, DNS (routers, NAS)
	9100, 631, // printers
}

// Ports connects to a few TCP ports on every host (connect only, nothing is
// sent) and, where lockdownd listens, reads the device model from it.
type Ports struct {
	Ports       []int         // default defaultFingerprintPorts
	Timeout     time.Duration // per connect, default 1.5s
	LockTimeout time.Duration // connect to lockdownd, default 3s
	Parallel    int           // concurrent connects, default 64
	NoLockdown  bool          // skip the lockdownd query
	LockdownCap int           // concurrent lockdownd queries, default 8
}

func (Ports) Name() string { return SourcePorts }

func (p Ports) Identify(ctx context.Context, req IdentifyRequest) error {
	ports, timeout, parallel, lockCap := p.Ports, p.Timeout, p.Parallel, p.LockdownCap
	if len(ports) == 0 {
		ports = defaultFingerprintPorts
	}
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	if parallel <= 0 {
		parallel = 64
	}
	if lockCap <= 0 {
		lockCap = 8
	}
	lockTimeout := p.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = 3 * time.Second
	}
	type dialKey struct {
		prefix netip.Prefix
		lock   bool
	}
	dialers := map[dialKey]*net.Dialer{}
	dialer := func(ip netip.Addr, port int) *net.Dialer {
		key := dialKey{lock: port == portLockdown}
		if s, ok := subnetOf(req.Subnets, ip); ok {
			key.prefix = s.Prefix
		}
		if d := dialers[key]; d != nil {
			return d
		}
		t := timeout
		if key.lock {
			t = lockTimeout
		}
		d := dialerFor(req.Subnets, ip, "tcp4", t)
		dialers[key] = d
		return d
	}
	sem := make(chan struct{}, parallel)
	lockSem := make(chan struct{}, lockCap)
	var wg sync.WaitGroup
	for _, port := range ports {
		for _, ip := range req.Hosts {
			if err := req.Pacer.Wait(ctx); err != nil {
				wg.Wait()
				return err
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			}
			d := dialer(ip, port)
			wg.Add(1)
			go func(ip netip.Addr, port int) {
				defer wg.Done()
				conn, err := d.DialContext(ctx, "tcp4", netip.AddrPortFrom(ip, uint16(port)).String())
				<-sem
				if err != nil {
					return
				}
				if port != portLockdown || p.NoLockdown {
					_ = conn.Close()
					req.Report(Hint{IP: ip, Source: SourcePorts, Ports: []int{port}})
					return
				}
				lockSem <- struct{}{}
				info, ok := QueryLockdown(ctx, conn, lockTimeout)
				<-lockSem
				_ = conn.Close()
				h := Hint{IP: ip, Source: SourcePorts, Ports: []int{port}}
				if ok {
					h.Source, h.Name, h.Model, h.OS = SourceLockdown, info.DeviceName, info.ProductType, info.OS()
				}
				req.Report(h)
			}(ip, port)
		}
	}
	wg.Wait()
	return ctx.Err()
}
