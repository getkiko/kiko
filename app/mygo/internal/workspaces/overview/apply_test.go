package overview

import (
	"net/http"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

var someChanges = store.Unapplied{Parts: []string{"路由规则", "出站"}, Pages: map[string]bool{env.PageRoute: true, env.PageSubscriptions: true}}

// withUnapplied makes the server's applied configuration empty, so the
// whole document is unapplied, and waits for the sidebar's apply entry.
func (f *fixture) withUnapplied(t *testing.T) {
	t.Helper()
	f.sidebar = true
	f.srv.Route("GET", "/config/applied", testkit.JSON(map[string]any{}))
	f.env.Post(f.store.RefreshApplied)
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find("未应用的更改"); return ok })
}

// A stopped core takes the document as it is when it starts (e09da14): no
// apply entry shows, and the power button applies before it starts.
func TestApplyStartsAStoppedCore(t *testing.T) {
	f := newFixture(t)
	f.sidebar = true
	testkit.UntilText(t, f.tt, "已停止")
	f.srv.Route("GET", "/config/applied", testkit.JSON(map[string]any{}))
	before := f.store.AppliedGen()
	f.env.Post(f.store.RefreshApplied)
	testkit.Until(t, f.tt, func() bool { return f.store.AppliedGen() > before })
	f.tt.Frame()
	if _, ok := f.tt.Find("未应用的更改"); ok {
		t.Fatalf("stopped core shows the apply entry: %q", f.tt.Texts())
	}
	f.click(t, "启动")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/runtime/start") })
	if !f.requested("POST /api/v1/config/apply") {
		t.Errorf("start did not apply first: %v", f.srv.Requests())
	}
}

// The sidebar's apply entry counts the changes and applies them live; the
// overview page itself shows no apply card.
func TestSidebarAppliesLive(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	f.withUnapplied(t)
	for _, text := range []string{"应用", "重启并应用"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q: %q", text, f.tt.Texts())
		}
	}
	saveApplyPreview(t, f, "sidebar-apply-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	saveApplyPreview(t, f, "sidebar-apply-dark.png")
	f.tt.SetDark(false)
	f.click(t, "应用")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/apply/live") })
	testkit.UntilText(t, f.tt, "已应用")
	if f.requested("POST /api/v1/config/apply") {
		t.Error("live apply restarted the core")
	}
}

// Folded, the entry is one Apply button carrying the count.
func TestFoldedSidebarApplies(t *testing.T) {
	f := newFixture(t)
	f.folded = true
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	f.w.apply.changes = func() store.Unapplied { return someChanges }
	f.sidebar = true
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find("应用 2 处更改"); return ok })
	saveApplyPreview(t, f, "sidebar-apply-folded.png")
	f.click(t, "应用 2 处更改")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/apply/live") })
}

func TestApplyAsksToRestart(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	f.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"入站或 TUN 有改动，需要重启核心","restartRequired":true}`))
	})
	f.withUnapplied(t)
	f.click(t, "应用")
	testkit.UntilText(t, f.tt, "入站或 TUN 有改动，需要重启核心")
	saveApplyPreview(t, f, "sidebar-apply-restart.png")
	if f.requested("POST /api/v1/config/apply") {
		t.Fatal("restart before confirming")
	}
	// The popover's confirm button sits at its lower right.
	panel, ok := f.tt.Find("需要重启核心")
	if !ok {
		t.Fatalf("no popover: %q", f.tt.Texts())
	}
	f.tt.ClickAt(panel.X+panel.W-50, panel.Y+panel.H-27)
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/config/apply") })
}

func saveApplyPreview(t *testing.T, f *fixture, name string) { savePreview(t, f.tt, name) }
