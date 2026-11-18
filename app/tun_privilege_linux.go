//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// tunPrivilegeError is why kiko-core, started by this server, cannot bring
// up TUN: it runs with CAP_NET_ADMIN when the server is root, passes the
// capability on as an ambient one (systemd AmbientCapabilities), or the
// kiko-core file carries it (setcap).
func tunPrivilegeError(singBox string) string {
	if os.Geteuid() == 0 {
		return ""
	}
	if status, err := os.ReadFile("/proc/self/status"); err == nil && statusCapHas(string(status), "CapAmb", linuxCapNetAdmin) {
		return ""
	}
	if _, err := unix.Getxattr(singBox, "security.capability", nil); err == nil {
		return ""
	}
	return linuxTUNPrivilegeMessage
}
