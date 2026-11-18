package console

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func newConsole(t *testing.T) (*Console, *ui.Tester, *testkit.Server) {
	t.Helper()
	srv := testkit.NewServer(t)
	e, _ := testkit.NewEnv(srv)
	c := New(e)
	t.Cleanup(e.Server.Stop)
	tt := ui.NewTester(c.View, 1320, 860)
	testkit.Until(t, tt, func() bool { return c.Store.Loaded && e.Server.Connected })
	return c, tt, srv
}

func TestConsoleLoadsAndNavigates(t *testing.T) {
	c, tt, srv := newConsole(t)
	for _, label := range []string{"概览", "路由", "连接", "用量", "进程", "改写", "组件", "日志", "设置"} {
		if !tt.HasText(label) {
			t.Errorf("navigation lacks %q: %q", label, tt.Texts())
		}
	}
	// The first load creates a profile and saves it.
	testkit.Until(t, tt, func() bool { return srv.Store().Revision >= 2 })
	if len(c.Store.Profiles()) != 1 || c.Store.ActiveProfileID() == "" {
		t.Errorf("profiles %v, active %q", c.Store.Profiles(), c.Store.ActiveProfileID())
	}
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if c.Page() != env.PageSettings {
		t.Errorf("page %q after clicking 设置", c.Page())
	}
	tt.Key(ui.Cmd, ui.Key3)
	if c.Page() != env.PageConnections {
		t.Errorf("page %q after Cmd+3", c.Page())
	}
	tt.Key(ui.Cmd, ui.Key6)
	if c.Page() != env.PageRewrites {
		t.Errorf("page %q after Cmd+6", c.Page())
	}
	testkit.UntilText(t, tt, "未开启 HTTPS 解密")
}

func TestConsoleNoticeWhileDisconnected(t *testing.T) {
	c, tt, _ := newConsole(t)
	if tt.HasText("立即重试") {
		t.Fatal("notice shown while connected")
	}
	c.Env.Server.Stop()
	c.Env.Post(func() { c.Env.Server.Connected = false })
	testkit.UntilText(t, tt, "服务端连接已中断，实时数据已过期，正在自动重试。")
	if err := tt.Click("立即重试"); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, tt, func() bool { return c.Env.Server.Connected })
}

// The sidebar keeps its full width with its labels: it has no collapse
// button (dropped by f7dacd5).
func TestConsoleSidebarHasNoCollapseButton(t *testing.T) {
	_, tt, _ := newConsole(t)
	for _, label := range []string{"收起侧边栏", "展开侧边栏"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("sidebar shows %q: %q", label, tt.Texts())
		}
	}
	side, ok := tt.Find("侧边栏")
	if !ok || side.W != sidebarWidth {
		t.Errorf("sidebar %v, want %v DIPs wide", side, sidebarWidth)
	}
	for _, label := range []string{"工作区", "概览", "设置"} {
		if !tt.HasText(label) {
			t.Errorf("sidebar lacks %q: %q", label, tt.Texts())
		}
	}
}

// activator records SetActive.
type activator struct{ active bool }

func (a *activator) View(*ui.Context)      {}
func (a *activator) SetActive(active bool) { a.active = active }

// Navigating activates the new page at once and deactivates the old one,
// without waiting for a frame.
func TestNavigateActivatesAtOnce(t *testing.T) {
	c, _, _ := newConsole(t)
	from, to := &activator{}, &activator{}
	c.workspaces[env.PageLogs], c.workspaces[env.PageUsage] = from, to
	c.Navigate(env.PageLogs, nil)
	if !from.active {
		t.Fatal("logs not activated")
	}
	c.Navigate(env.PageUsage, nil)
	if from.active || !to.active {
		t.Errorf("after navigating: logs %v, usage %v", from.active, to.active)
	}
}

// The navigation's entries start where the group titles' text does, below
// the macOS window controls.
func TestConsoleSidebarLayout(t *testing.T) {
	_, tt, _ := newConsole(t)
	bar := ui.TitleBar{Height: 48, Left: 70}
	tt.SetTitleBar(bar)
	tt.SetVibrancy(true)
	tt.Frame()
	find := func(label string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("no %q in %q", label, tt.Texts())
		}
		return r
	}
	// inkStart is the first column of r, from the left, that draws
	// something other than r's background at its left edge, in the middle
	// half of its height (clear of rounded corners).
	inkStart := func(r ui.Rect) float32 {
		img := tt.Image()
		y0, y1 := int(r.Y+r.H/4), int(r.Y+r.H*3/4)
		bg := img.RGBAAt(int(r.X)+3, int(r.Y+r.H/2))
		for x := int(r.X) + 3; x < int(r.X+r.W); x++ {
			for y := y0; y < y1; y++ {
				p := img.RGBAAt(x, y)
				if d := int(p.R) - int(bg.R) + int(p.G) - int(bg.G) + int(p.B) - int(bg.B); d > 60 || d < -60 {
					return float32(x)
				}
			}
		}
		return -1
	}
	heading := inkStart(find("工作区"))
	for _, label := range []string{"概览", "组件", "设置"} {
		if x := inkStart(find(label)); x < heading-2 || x > heading+2 {
			t.Errorf("%s's icon starts at x=%v, the group title's text at x=%v", label, x, heading)
		}
	}
	// The header row keeps the navigation clear of the title bar's window
	// controls.
	if top := find("工作区"); top.Y < bar.Height {
		t.Errorf("navigation starts at y=%v, under the %v DIP title bar", top.Y, bar.Height)
	}
	savePreview(t, tt, "console-sidebar.png")
	tt.SetDark(true)
	tt.SetVibrancy(false)
	tt.Frame()
	savePreview(t, tt, "console-sidebar-dark.png")
	// Without a hidden title bar (Linux, Windows), the navigation's icons
	// still start where the group titles' text does.
	tt.SetDark(false)
	tt.SetTitleBar(ui.TitleBar{})
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	heading = inkStart(find("工作区"))
	if x := inkStart(find("设置")); x < heading-2 || x > heading+2 {
		t.Errorf("设置's icon starts at x=%v, the group title's text at x=%v", x, heading)
	}
	savePreview(t, tt, "console-settings.png")
}

// savePreview writes the frame where CONSOLE_PREVIEW_DIR says, for
// reviewing the look.
func savePreview(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("CONSOLE_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// Workspaces with unapplied changes carry a blue dot while the core runs.
func TestConsoleMarksUnappliedWorkspaces(t *testing.T) {
	c, tt, srv := newConsole(t)
	if _, ok := tt.Find("有未应用的更改"); ok {
		t.Fatal("dot shown before the applied configuration is known")
	}
	// Unapplied changes show only while the core runs (e09da14).
	running := api.Runtime{State: "running", DesiredRunning: true, StartedAt: "2026-11-06T00:00:00Z"}
	srv.SetRuntime(running)
	srv.Emit("runtime", running)
	testkit.Until(t, tt, func() bool { return c.Env.Server.Runtime.Running() })
	srv.Route("GET", "/config/applied", testkit.JSON(map[string]any{}))
	c.Env.Post(c.Store.RefreshApplied)
	testkit.Until(t, tt, func() bool { _, ok := tt.Find("有未应用的更改"); return ok })
	savePreview(t, tt, "console-unapplied.png")
	// Stopping the core clears the dots: it takes the document as it is
	// when it starts.
	stopped := api.Runtime{State: "stopped"}
	srv.SetRuntime(stopped)
	srv.Emit("runtime", stopped)
	testkit.Until(t, tt, func() bool { _, ok := tt.Find("有未应用的更改"); return !ok })
}

// Two consoles in one process, one per server window, keep their own
// stores and pages.
func TestTwoConsolesStayApart(t *testing.T) {
	a, ta, srvA := newConsole(t)
	b, tb, srvB := newConsole(t)
	testkit.Until(t, ta, func() bool { return srvA.Store().Revision >= 2 })
	testkit.Until(t, tb, func() bool { return srvB.Store().Revision >= 2 })
	if err := ta.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if a.Page() != env.PageSettings || b.Page() != env.PageOverview {
		t.Fatalf("pages %q / %q", a.Page(), b.Page())
	}
	before := srvB.Saves()
	if err := a.Store.Update(func(d *model.Doc) { d.LatencyTestURL = "https://a.example/204" }); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, ta, func() bool { return strings.Contains(string(srvA.Store().Doc), "a.example") })
	tb.Frame()
	if srvB.Saves() != before || strings.Contains(string(srvB.Store().Doc), "a.example") || b.Store.Doc().LatencyTestURL == "https://a.example/204" {
		t.Errorf("a's change reached b")
	}
}
