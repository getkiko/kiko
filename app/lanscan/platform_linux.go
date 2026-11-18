//go:build linux

package lanscan

import (
	"context"
	"net/netip"
	"os"
)

func defaultHost() Host { return Host{Run: ExecRunner, ReadFile: os.ReadFile} }

func (h Host) Neighbors(ctx context.Context) ([]Neighbor, error) {
	data, err := h.ReadFile("/proc/net/arp")
	if err != nil {
		out, runErr := h.Run(ctx, "arp", "-an")
		if runErr != nil {
			return nil, err
		}
		return ParseARPUnix(out), nil
	}
	return ParseProcNetARP(data), nil
}

func (h Host) Gateways(ctx context.Context) ([]netip.Addr, error) {
	data, err := h.ReadFile("/proc/net/route")
	if err != nil {
		return nil, err
	}
	return ParseProcNetRoute(data), nil
}
