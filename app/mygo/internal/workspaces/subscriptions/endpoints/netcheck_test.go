package endpoints

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func diagnosticsBody(now time.Time) string {
	ms := now.UnixMilli()
	return fmt.Sprintf(`{"netcheck":{"checkedAt":"%d","udp":true,"ipv4":true,"ipv6":false,"osHasIPv6":false,
		"globalV4":"203.0.113.7:41641","mappingVariesByDestIP":"true","upnp":"false","pmp":"true","pcp":"false",
		"preferredDERP":2,"stunPath":"detour","selfEndpoints":["203.0.113.7:41641","192.168.1.5:41641"],
		"regions":[{"id":2,"code":"tok","name":"Tokyo","latencyMs":31.6},{"id":1,"code":"nyc","name":"New York City","latencyMs":182},
		{"id":3,"code":"fra","name":"Frankfurt"}]},
		"peers":[{"stableID":"n1","lastHandshake":"%d",
		"endpoints":[{"address":"198.51.100.2:41641","path":"detour"},{"address":"192.168.1.9:41641","path":"direct"}],
		"changes":[{"at":"%d","what":"handlePongConnLocked-bestAddr-update","to":"198.51.100.2:41641@85.2ms+1280"},
		{"at":"%d","what":"updateFromNode-new-Endpoints","to":"[198.51.100.2:41641 192.168.1.9:41641]"}]}]}`,
		ms-3000, ms-12_000, ms-60_000, ms-120_000)
}

// TestNetworkDiagnostics: "Network diagnostics" shows this node's network check, the
// DERP regions and how each device is reached; a device's sheet shows its
// candidates and path changes; "Check again" asks for a new check.
func TestNetworkDiagnostics(t *testing.T) {
	f := newFixture(t, testDoc())
	var refreshes atomic.Int32
	f.srv.Route("GET", "/tailscale/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("refresh") == "1" {
			refreshes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(diagnosticsBody(time.Now())))
	})
	event := tailscaleEvent()
	peer := event["endpoints"].([]map[string]any)[0]["userGroups"].([]map[string]any)[0]["peers"].([]map[string]any)[0]
	peer["curAddr"], peer["relay"], peer["udpPath"], peer["rxBytes"], peer["txBytes"] = "198.51.100.2:41641", "tok", "detour", "2048", "1024"
	f.srv.Emit("tailscale", event)
	testkit.Until(t, f.tt, func() bool { return f.s.rt.tailscale["Tailscale"] != nil })
	f.openTailscale()
	testkit.UntilText(t, f.tt, "Alice")
	f.click("网络诊断")
	for _, text := range []string{"本机网络", "203.0.113.7:41641", "随目的地变化", "NAT-PMP", "本机没有 IPv6", "经代理",
		"Tokyo", "32 ms", "182 ms", "—", "设备连接", "nas", "12 秒前"} {
		testkit.UntilText(t, f.tt, text)
	}
	if _, ok := f.tt.Find("首选 DERP"); !ok {
		t.Error("no preferred DERP mark")
	}
	f.tt.Advance(300 * time.Millisecond)
	f.snapshot("tailscale-diagnostics")
	f.click("重新检测")
	testkit.Until(t, f.tt, func() bool { return refreshes.Load() == 1 })

	f.clickBelow("设备连接")
	for _, text := range []string{"候选地址", "192.168.1.9:41641", "直连", "路径变化", "切换路径", "198.51.100.2:41641 85 ms", "更新候选地址", "2.0 KB / 1.0 KB"} {
		testkit.UntilText(t, f.tt, text)
	}
	f.tt.Advance(300 * time.Millisecond)
	f.snapshot("tailscale-peer-connection")
}

func TestDescribeChange(t *testing.T) {
	for _, test := range []struct {
		change       api.TailscalePathChange
		title, extra string
	}{
		{api.TailscalePathChange{What: "handlePongConnLocked-bestAddr-update", To: "1.2.3.4:5@12.4ms+1280"}, "切换路径", "1.2.3.4:5 12 ms"},
		{api.TailscalePathChange{What: "deleteEndpointLocked-bestAddr-pong-timeout", From: "1.2.3.4:5@1ms+0"}, "当前路径失效", "1.2.3.4:5"},
		{api.TailscalePathChange{What: "handleCallMeMaybe-new-endpoints", To: "[1.2.3.4:5]"}, "对端请求打洞", "1.2.3.4:5"},
		{api.TailscalePathChange{What: "stopAndReset-resetLocked"}, "重置连接", ""},
		{api.TailscalePathChange{What: "other", From: "a", To: "b"}, "other", "a b"},
	} {
		title, extra := describeChange(test.change)
		if title != test.title || extra != test.extra {
			t.Errorf("%s: %q %q", test.change.What, title, extra)
		}
	}
	now := time.Now()
	for ms, want := range map[int64]string{0: "", now.Add(-2 * time.Second).UnixMilli(): "刚刚", now.Add(-90 * time.Second).UnixMilli(): "1 分钟前"} {
		if got := relativeTime(ms, now); got != want {
			t.Errorf("relativeTime %d: %q, want %q", ms, got, want)
		}
	}
}
