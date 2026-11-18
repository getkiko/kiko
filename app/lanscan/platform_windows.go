//go:build windows

package lanscan

import (
	"context"
	"net/netip"
	"os"
)

func defaultHost() Host { return Host{Run: ExecRunner, ReadFile: os.ReadFile} }

func (h Host) Neighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := h.Run(ctx, "arp", "-a")
	if err != nil {
		return nil, err
	}
	return ParseARPWindows(out), nil
}

func (h Host) Gateways(ctx context.Context) ([]netip.Addr, error) {
	out, err := h.Run(ctx, "route", "print", "-4")
	if err != nil {
		return nil, err
	}
	return ParseRoutePrintWindows(out), nil
}
