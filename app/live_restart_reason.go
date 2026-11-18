package main

import "strings"

// liveRestartReasons translates the reasons the core gives when a change
// cannot be applied live. Reasons not listed are shown as the core wrote
// them, so a newer core still reads sensibly.
var liveRestartReasons = map[string]string{
	"inbounds or TUN changed, restart required":                              "入站或 TUN 有改动，需要重启核心",
	"endpoints changed, restart required":                                    "端点有改动，需要重启核心",
	"services changed, restart required":                                     "服务有改动，需要重启核心",
	"HTTP clients changed, restart required":                                 "HTTP 客户端有改动，需要重启核心",
	"experimental options (cache, API and others) changed, restart required": "实验选项（缓存、API 等）有改动，需要重启核心",
	"log options changed, restart required":                                  "日志选项有改动，需要重启核心",
	"NTP options changed, restart required":                                  "NTP 选项有改动，需要重启核心",
	"certificate options changed, restart required":                          "证书选项有改动，需要重启核心",
	"certificate providers changed, restart required":                        "证书提供方有改动，需要重启核心",
	"network namespaces changed, restart required":                           "网络命名空间有改动，需要重启核心",
	"rule sets changed, restart required":                                    "规则集有改动，需要重启核心",
	"DHCP lease files changed, restart required":                             "DHCP 租约文件有改动，需要重启核心",
	"global route options changed, restart required":                         "全局路由选项有改动，需要重启核心",
	"global DNS options changed, restart required":                           "全局 DNS 选项有改动，需要重启核心",
	"FakeIP servers changed, restart required":                               "FakeIP 服务器有改动，需要重启核心",
	"default DNS server type changed, restart required":                      "默认 DNS 服务器类型有改动，需要重启核心",
	"new rules need process matching, restart required":                      "新规则需要进程匹配，需要重启核心",
	"new rules need neighbor device resolution, restart required":            "新规则需要识别局域网设备，需要重启核心",
}

// liveRestartReason is the message shown for a restart the core asks for.
func liveRestartReason(reason string) string {
	if message, ok := liveRestartReasons[reason]; ok {
		return message
	}
	if tag, ok := strings.CutPrefix(reason, "overrides of rule set "); ok {
		if tag, ok = strings.CutSuffix(tag, " changed, restart required"); ok {
			return "规则集「" + tag + "」的覆盖设置有改动，需要重启核心"
		}
	}
	return reason
}
