package routes

import (
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// TestF6WidthScreenshots writes the acceptance frames of the tables'
// column widths when ROUTES_PREVIEW_DIR is set: Rule Name widened (Match Conditions
// taking less of the room), the edge focused in the accent, the reset
// button; the DNS table; a narrow window scrolled sideways.
func TestF6WidthScreenshots(t *testing.T) {
	if os.Getenv("ROUTES_PREVIEW_DIR") == "" {
		t.Skip("ROUTES_PREVIEW_DIR is not set")
	}
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.dragGrip("规则名称", 80)
	f.dragGrip("目标出口", 40)
	// The keyboard widens Rule Name a step more and leaves its edge focused.
	f.click("移动规则名称列")
	f.tt.Key(0, ui.KeyTab)
	f.tt.Key(0, ui.KeyRight)
	f.tt.Frame()
	if !f.tt.Focused("调整规则名称列宽") {
		t.Errorf("tab does not reach the edge of 规则名称")
	}
	g := f.grip("规则名称")
	f.tt.Move(g.X+g.W/2, g.Y+g.H/2)
	f.tt.Frame()
	f.snapshot("17-widths-light.png", false)
	f.snapshot("17-widths-dark.png", true)

	f.tt.Move(600, 500)
	f.click("DNS 分流")
	testkit.UntilText(t, f.tt, "查询出口")
	f.dragGrip("规则名称", 60)
	f.dragGrip("查询出口", 50)
	f.tt.Move(600, 500)
	f.tt.Frame()
	f.snapshot("17-dns-widths-light.png", false)

	f.click("流量规则")
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.tt.SetSize(860, 720)
	f.tt.Frame()
	f.tt.Frame()
	f.snapshot("17-narrow-light.png", false)
	r, _ := f.tt.Find("此设备的其余连接")
	f.tt.Scroll(r.X, r.Y, 160, 0)
	f.tt.Frame()
	f.snapshot("17-narrow-scrolled-light.png", false)
}
