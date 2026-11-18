//go:build !linux && !windows

package lanscan

import (
	"context"
	"net/netip"
	"os"
)

func defaultHost() Host { return Host{Run: ExecRunner, ReadFile: os.ReadFile} }

func (h Host) Neighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := h.Run(ctx, "/usr/sbin/arp", "-an")
	if err != nil {
		return nil, err
	}
	return ParseARPUnix(out), nil
}

func (h Host) Gateways(ctx context.Context) ([]netip.Addr, error) {
	out, err := h.Run(ctx, "/usr/sbin/netstat", "-rn", "-f", "inet")
	if err != nil {
		return nil, err
	}
	return ParseRouteUnix(out), nil
}
