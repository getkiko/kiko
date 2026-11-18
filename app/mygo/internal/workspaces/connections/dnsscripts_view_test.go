package connections

import (
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func scriptRuns() []api.DNSScriptRun {
	return []api.DNSScriptRun{
		{Tag: "geo", Outcome: "answer", Detail: "203.0.113.9", Cached: true, DurationMS: 0.4},
		{Tag: "split", Outcome: "server", Detail: "cloudflare", DurationMS: 1.2},
		{Tag: "skip", Outcome: "pass", DurationMS: 0.1},
		{Tag: "block", Outcome: "reject", DurationMS: 0.2},
		{Tag: "slow", Outcome: "error", Detail: "script timed out after 100ms while evaluating a very long detail text that has to wrap across several lines in the inspector without being cut off", DurationMS: 100},
		{Tag: "test", Outcome: "maybe", DurationMS: 0},
	}
}

func dnsScriptShots(t *testing.T, h *harness, name string) {
	dir := os.Getenv("DNSQ_PREVIEW_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		suffix := "-light.png"
		if dark {
			suffix = "-dark.png"
		}
		h.tt.SetDark(dark)
		h.tt.Frame()
		h.tt.Advance(600 * time.Millisecond)
		h.tt.Frame()
		f, err := os.Create(filepath.Join(dir, name+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, h.tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	h.tt.SetDark(false)
	h.tt.Advance(600 * time.Millisecond)
	h.tt.Frame()
}

func scriptsHarness(t *testing.T, width int) *harness {
	h := newSizedHarness(t, width, 900, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", newFakeConnections(nil).ServeHTTP)
		srv.Route("GET", "/dns/queries/", func(w http.ResponseWriter, r *http.Request) {
			q := fixtureDNS().Queries[1]
			q.Scripts = scriptRuns()
			testkit.JSON(q)(w, r)
		})
	})
	h.click(t, "DNS 查询")
	testkit.UntilText(t, h.tt, "example.org.")
	h.click(t, "chatgpt.com.")
	testkit.UntilText(t, h.tt, "上游尝试")
	return h
}

func TestDNSInspectorScripts(t *testing.T) {
	h := scriptsHarness(t, 1440)
	tt := h.tt
	testkit.UntilText(t, tt, "脚本")
	for _, text := range []string{"geo", "返回应答", "转发到服务器", "继续下一条", "拒绝", "出错", "可能", "缓存", "耗时"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, tt.Texts())
		}
	}
	dnsScriptShots(t, h, "74-dns-query-scripts")
}

func TestDNSInspectorScriptsNarrow(t *testing.T) {
	h := scriptsHarness(t, 640)
	testkit.UntilText(t, h.tt, "脚本")
	dnsScriptShots(t, h, "75-dns-query-scripts-narrow")
}

func TestDNSInspectorWithoutScripts(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", newFakeConnections(nil).ServeHTTP)
		srv.Route("GET", "/dns/queries/", testkit.JSON(fixtureDNS().Queries[1]))
	})
	h.click(t, "DNS 查询")
	testkit.UntilText(t, h.tt, "example.org.")
	h.click(t, "chatgpt.com.")
	testkit.UntilText(t, h.tt, "上游尝试")
	if h.tt.HasText("转发到服务器") || h.tt.HasText("返回应答") {
		t.Fatal("script section without scripts")
	}
}
