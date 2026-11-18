//go:build !darwin

package lanscan

import (
	"errors"
	"net"
	"syscall"
)

// bindInterface is a no-op here: the socket is bound to the subnet's local
// address instead (binding a device needs privilege on Linux).
func bindInterface(string) func(network, address string, c syscall.RawConn) error { return nil }

// listenICMP is unavailable here; probes fall back to UDP.
func listenICMP(Subnet) (net.PacketConn, error) { return nil, errors.New("icmp unsupported") }
