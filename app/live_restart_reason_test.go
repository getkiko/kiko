package main

import "testing"

func TestLiveRestartReason(t *testing.T) {
	for in, want := range map[string]string{
		"inbounds or TUN changed, restart required":                "入站或 TUN 有改动，需要重启核心",
		"overrides of rule set geoip-cn changed, restart required": "规则集「geoip-cn」的覆盖设置有改动，需要重启核心",
		"something new changed, restart required":                  "something new changed, restart required",
		"": "",
	} {
		if got := liveRestartReason(in); got != want {
			t.Errorf("liveRestartReason(%q) = %q, want %q", in, got, want)
		}
	}
}
