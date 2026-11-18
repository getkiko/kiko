package routes

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// TestSidebarProfileScreenshots writes the item-16 acceptance frames when
// ROUTES_PREVIEW_DIR is set: the selected iPhone listing its profiles, a
// sidebar of one-profile devices to compare with the one before F4, and the
// narrow overlay.
func TestSidebarProfileScreenshots(t *testing.T) {
	previewDir(t)
	f := sidebarProfilesFixture(t)
	f.w.showActiveProfile("phone")
	f.tt.Frame()
	f.snapshot("16-sidebar-light.png", false)
	f.snapshot("16-sidebar-dark.png", true)

	g := newFixture(t, nil)
	testkit.UntilText(t, g.tt, "Apple 服务")
	g.click("本机 / 网关")
	g.tt.Frame()
	g.snapshot("16-single-light.png", false)

	h := sidebarProfilesFixture(t)
	h.w.showActiveProfile("phone")
	h.tt.SetSize(480, 820)
	h.tt.Frame()
	h.click("设备分栏")
	testkit.UntilText(t, h.tt, "游戏")
	h.snapshot("16-narrow-light.png", false)
	h.snapshot("16-narrow-dark.png", true)
}
