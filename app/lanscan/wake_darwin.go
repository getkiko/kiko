//go:build darwin

package lanscan

import (
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// bindInterface pins a socket to the named interface (IP_BOUND_IF), so a
// probe leaves through the LAN card even while a TUN holds the default route.
func bindInterface(name string) func(network, address string, c syscall.RawConn) error {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifc.Index)
		}); err != nil {
			return err
		}
		return serr
	}
}

// listenICMP opens an unprivileged datagram ICMP socket bound to the subnet's
// local address and interface.
func listenICMP(s Subnet) (net.PacketConn, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, unix.IPPROTO_ICMP)
	if err != nil {
		return nil, err
	}
	if ifc, err := net.InterfaceByName(s.Iface); err == nil {
		_ = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_BOUND_IF, ifc.Index)
	}
	if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: s.Self.As4()}); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "icmp")
	c, err := net.FilePacketConn(f)
	_ = f.Close()
	return c, err
}
