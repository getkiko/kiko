package main

import (
	"strconv"
	"strings"
)

// linuxCapNetAdmin is CAP_NET_ADMIN's bit: creating a TUN interface and
// changing routes need it.
const linuxCapNetAdmin = 12

const linuxTUNPrivilegeMessage = "Linux TUN 需要 root 或 CAP_NET_ADMIN 权限：请用 sudo ./install-service.sh 安装服务，或以 root 运行 kiko-server"

// statusCapHas reports whether the capability set named field (CapAmb,
// CapEff…) of a /proc/<pid>/status holds bit.
func statusCapHas(status, field string, bit uint) bool {
	for line := range strings.SplitSeq(status, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != field {
			continue
		}
		set, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		return err == nil && set&(1<<bit) != 0
	}
	return false
}
