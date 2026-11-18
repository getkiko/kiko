package overview

import (
	"net/http"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// withUnapplied makes the server's applied configuration empty, so the
// whole document is unapplied, and waits for the card.
func (f *fixture) withUnapplied(t *testing.T) {
	t.Helper()
	f.srv.Route("GET", "/config/applied", testkit.JSON(map[string]any{}))
	f.env.Post(f.store.RefreshApplied)
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find("未应用的更改"); return ok })
	// The card has slid in.
	f.tt.Advance(applyIn + 50*time.Millisecond)
}

// A stopped core takes the document as it is when it starts (e09da14): no
// apply card shows, and the power button applies before it starts.
func TestApplyCardStartsAStoppedCore(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "已停止")
	f.srv.Route("GET", "/config/applied", testkit.JSON(map[string]any{}))
	before := f.store.AppliedGen()
	f.env.Post(f.store.RefreshApplied)
	testkit.Until(t, f.tt, func() bool { return f.store.AppliedGen() > before })
	f.tt.Advance(applyIn + 50*time.Millisecond)
	if _, ok := f.tt.Find("未应用的更改"); ok {
		t.Fatalf("stopped core shows the apply card: %q", f.tt.Texts())
	}
	if f.tt.HasText("不停机应用") || f.tt.HasText("重启并应用") {
		t.Fatalf("stopped core offers a live apply: %q", f.tt.Texts())
	}
	saveApplyPreview(t, f, "overview-apply-stopped.png")
	f.click(t, "启动")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/runtime/start") })
	if !f.requested("POST /api/v1/config/apply") {
		t.Errorf("start did not apply first: %v", f.srv.Requests())
	}
}

func TestApplyCardAppliesLive(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	f.withUnapplied(t)
	for _, text := range []string{"不停机应用", "重启并应用", "路由", "组件"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q: %q", text, f.tt.Texts())
		}
	}
	saveApplyPreview(t, f, "overview-apply-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	saveApplyPreview(t, f, "overview-apply-dark.png")
	f.tt.SetDark(false)
	var navigated string
	f.env.SetNavigator(func(page string, _ any) { navigated = page })
	f.click(t, "前往路由")
	if navigated != env.PageRoute {
		t.Errorf("navigated to %q", navigated)
	}
	f.click(t, "不停机应用")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/apply/live") })
	testkit.UntilText(t, f.tt, "已不停机应用")
	if f.requested("POST /api/v1/config/apply") {
		t.Error("live apply restarted the core")
	}
}

func TestApplyCardAsksToRestart(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	f.srv.Route("POST", "/apply/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"入站或 TUN 有改动，需要重启核心","restartRequired":true}`))
	})
	f.withUnapplied(t)
	f.click(t, "不停机应用")
	testkit.UntilText(t, f.tt, "入站或 TUN 有改动，需要重启核心")
	saveApplyPreview(t, f, "overview-apply-restart.png")
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
