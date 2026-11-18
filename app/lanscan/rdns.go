package lanscan

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ReverseDNS looks hosts up at the LAN gateway, which knows the DHCP host
// names, over a socket bound to the LAN interface (the system resolver may be
// a TUN DNS hijack). Without a LAN gateway it uses the system resolver.
type ReverseDNS struct {
	Lookup  func(ctx context.Context, addr string) ([]string, error) // default: gateway, then net.DefaultResolver
	Workers int                                                      // default 8
	Timeout time.Duration                                            // per lookup, default 1.5s
	// GatewayTimeout bounds the gateway attempt inside Timeout, default 650ms.
	GatewayTimeout time.Duration
}

func (ReverseDNS) Name() string { return SourceDNS }

func (r ReverseDNS) Identify(ctx context.Context, req IdentifyRequest) error {
	lookup, workers, timeout := r.Lookup, r.Workers, r.Timeout
	if lookup == nil {
		lookup = net.DefaultResolver.LookupAddr
		if len(req.Gateways) > 0 {
			gw := req.Gateways[0]
			udp, tcp := dialerFor(req.Subnets, gw, "udp4", 0), dialerFor(req.Subnets, gw, "tcp4", 0)
			res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := udp
				if strings.HasPrefix(network, "tcp") {
					d, network = tcp, "tcp4"
				} else {
					network = "udp4"
				}
				return d.DialContext(ctx, network, netip.AddrPortFrom(gw, 53).String())
			}}
			gwTimeout := r.GatewayTimeout
			if gwTimeout <= 0 {
				gwTimeout = 650 * time.Millisecond
			}
			lookup = func(ctx context.Context, addr string) ([]string, error) {
				// The gateway gets its own share so a silent port 53 leaves the
				// system resolver the rest of the budget.
				gctx, cancel := context.WithTimeout(ctx, gwTimeout)
				names, err := res.LookupAddr(gctx, addr)
				cancel()
				var de *net.DNSError
				if err == nil || errors.As(err, &de) && de.IsNotFound {
					return names, err
				}
				return net.DefaultResolver.LookupAddr(ctx, addr) // the gateway serves no DNS
			}
		}
	}
	if workers <= 0 {
		workers = 8
	}
	if timeout == 0 {
		timeout = 1500 * time.Millisecond
	}
	var wg sync.WaitGroup
	jobs := make(chan netip.Addr)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				c, cancel := context.WithTimeout(ctx, timeout)
				names, err := lookup(c, ip.String())
				cancel()
				if err != nil || len(names) == 0 {
					continue
				}
				if name := shortHost(names[0]); name != "" && name != ip.String() {
					req.Report(Hint{IP: ip, Source: SourceDNS, Name: name})
				}
			}
		}()
	}
	for _, ip := range req.Hosts {
		if req.Pacer.Wait(ctx) != nil {
			break
		}
		select {
		case jobs <- ip:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

// shortHost keeps the first label of a reverse-DNS name.
func shortHost(n string) string {
	n = strings.TrimSuffix(strings.TrimSpace(n), ".")
	if i := strings.IndexByte(n, '.'); i > 0 {
		n = n[:i]
	}
	return n
}
