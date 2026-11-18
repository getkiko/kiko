package routes

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// hover moves the pointer over the element label names and lets the
// hover motion settle.
func (f *fixture) hover(label string) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q; texts %q", label, f.tt.Texts())
	}
	f.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	f.tt.Advance(time.Second)
	f.tt.Frame()
}

// hoverShot writes the band of the frame around label, 200 DIPs high,
// with the pointer on it, where ROUTES_HOVER_DIR says.
func (f *fixture) hoverShot(dir, name, label string, dark bool) {
	f.t.Helper()
	if dir == "" {
		return
	}
	f.tt.SetDark(dark)
	defer f.tt.SetDark(false)
	f.hover(label)
	r, _ := f.tt.Find(label)
	img := f.tt.Image()
	scale := float32(img.Bounds().Dx()) / hoverWidth
	top := max(int((r.Y-100)*scale), 0)
	band := img.SubImage(image.Rect(0, top, img.Bounds().Dx(), min(top+int(200*scale), img.Bounds().Dy())))
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, band); err != nil {
		f.t.Fatal(err)
	}
}

// hoverWidth is the window's width in DIPs as hoverShot writes it.
var hoverWidth float32 = 1280

// TestRuleSetHoverScreenshots writes the rule set pages with the pointer
// on each kind of button when ROUTES_HOVER_DIR is set.
func TestRuleSetHoverScreenshots(t *testing.T) {
	dir := os.Getenv("ROUTES_HOVER_DIR")
	if dir == "" {
		t.Skip("ROUTES_HOVER_DIR is not set")
	}
	prefix := os.Getenv("ROUTES_HOVER_PREFIX")
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	hoverWidth = 1280
	defer func() { hoverWidth = 1280 }()
	testkit.UntilText(t, tt, "my-remote")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.statusLoaded })
	for _, dark := range []bool{false, true} {
		mode := "light"
		if dark {
			mode = "dark"
		}
		f.hoverShot(dir, prefix+"list-row-"+mode+".png", "my-remote", dark)
		f.hoverShot(dir, prefix+"list-update-"+mode+".png", "更新规则集 my-remote", dark)
		f.hoverShot(dir, prefix+"list-trash-"+mode+".png", "删除规则集 my-remote", dark)
		f.hoverShot(dir, prefix+"list-switch-"+mode+".png", "启用规则集 my-remote", dark)
		f.hoverShot(dir, prefix+"list-updateall-"+mode+".png", "全部更新", dark)
	}
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "b.example")
	for _, dark := range []bool{false, true} {
		mode := "light"
		if dark {
			mode = "dark"
		}
		f.hoverShot(dir, prefix+"detail-back-"+mode+".png", "返回规则集列表", dark)
		f.hoverShot(dir, prefix+"detail-update-"+mode+".png", "更新规则集 my-remote", dark)
		f.hoverShot(dir, prefix+"detail-add-"+mode+".png", "添加规则", dark)
		f.hoverShot(dir, prefix+"detail-remove-"+mode+".png", "移除 b.example", dark)
		f.hoverShot(dir, prefix+"detail-delete-"+mode+".png", "删除规则集", dark)
	}
	// A chosen row, with the pointer on its button.
	f.click("a.example")
	f.hoverShot(dir, prefix+"detail-chosen-remove-light.png", "移除 a.example", false)
	tt.SetSize(640, 1100)
	hoverWidth = 640
	tt.Frame()
	f.hoverShot(dir, prefix+"detail-remove-narrow.png", "移除 b.example", false)
	f.click("返回规则集列表")
	testkit.UntilText(t, tt, "my-remote")
	f.hoverShot(dir, prefix+"list-update-narrow.png", "更新规则集 my-remote", false)
}

// frameAt is the frame with the pointer at x, y, the hover motion settled.
func (f *fixture) frameAt(x, y float32) *image.RGBA {
	f.tt.Move(x, y)
	f.tt.Advance(time.Second)
	f.tt.Frame()
	return f.tt.Image()
}

// center is the middle of the element label names.
func (f *fixture) center(label string) (float32, float32) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q", label)
	}
	return r.X + r.W/2, r.Y + r.H/2
}

// differs reports whether the frames differ within r, in DIPs at scale.
func differs(a, b *image.RGBA, r ui.Rect, scale float32) bool {
	box := image.Rect(int(r.X*scale), int(r.Y*scale), int((r.X+r.W)*scale), int((r.Y+r.H)*scale)).Intersect(a.Bounds())
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				return true
			}
		}
	}
	return false
}

// accentish reports a pixel of the accent's blue.
func accentish(p color.RGBA) bool {
	return p.B > 200 && p.R < 80 && p.G > 90 && p.G < 160
}

// On a rule set row's button only that button hovers, with kit's hover
// fill: the row's name, icon and switch keep their look. Elsewhere on the
// row its name fades, the switch still as it is.
func TestRuleSetRowHover(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.statusLoaded })
	// The controls stand beside the row's button, not in it: assistive
	// technology reads a button whole, and would not reach them.
	open, _ := tt.Find("打开规则集 my-remote")
	for _, control := range []string{"更新规则集 my-remote", "启用规则集 my-remote", "删除规则集 my-remote"} {
		r, ok := tt.Find(control)
		if !ok || r.X < open.X+open.W {
			t.Errorf("%s at %v is inside the row's button %v", control, r, open)
		}
	}
	away := f.frameAt(2, 2)
	scale := float32(away.Bounds().Dx()) / 1280
	name, _ := tt.Find("my-remote")
	sw, _ := tt.Find("启用规则集 my-remote")
	for _, button := range []string{"更新规则集 my-remote", "删除规则集 my-remote"} {
		r, _ := tt.Find(button)
		on := f.frameAt(f.center(button))
		if differs(away, on, name, scale) {
			t.Errorf("on %s the row's name changes", button)
		}
		if differs(away, on, sw, scale) {
			t.Errorf("on %s the switch changes", button)
		}
		if !differs(away, on, r, scale) {
			t.Errorf("%s shows no hover", button)
		}
		// kit's hover fill, at the button's edge, away from its symbol.
		edge := on.RGBAAt(int((r.X+3)*scale), int((r.Y+r.H/2)*scale))
		if accentish(edge) || edge == away.RGBAAt(int((r.X+3)*scale), int((r.Y+r.H/2)*scale)) {
			t.Errorf("%s hover fill %v", button, edge)
		}
	}
	// On the switch: nothing else changes.
	on := f.frameAt(f.center("启用规则集 my-remote"))
	if differs(away, on, name, scale) {
		t.Error("on the switch the row's name changes")
	}
	// On the row's name: it fades as a navigation row does, and the
	// switch keeps its look.
	on = f.frameAt(name.X+name.W/2, name.Y+name.H/2)
	if !differs(away, on, name, scale) {
		t.Error("the row shows no hover")
	}
	if differs(away, on, sw, scale) {
		t.Error("the row's hover changes the switch")
	}
}

// The chevron opens the rule set as its name does, and hovering it fades
// the name with it; the space just before the controls is the name's.
func TestRuleSetRowChevron(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	away := f.frameAt(2, 2)
	scale := float32(away.Bounds().Dx()) / 1280
	name, _ := tt.Find("my-remote")
	trash, _ := tt.Find("删除规则集 my-remote")
	update, _ := tt.Find("更新规则集 my-remote")
	x, y := trash.X+trash.W+12+7, trash.Y+trash.H/2
	if !differs(away, f.frameAt(x, y), name, scale) {
		t.Error("hovering the chevron leaves the name as it is")
	}
	tt.ClickAt(x, y)
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.detail != nil && f.w.ruleSets.detail.id == "rs-remote" })
	f.click("返回规则集列表")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.detail == nil })
	tt.ClickAt(update.X-6, update.Y+update.H/2)
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.detail != nil && f.w.ruleSets.detail.id == "rs-remote" })
}

// The content table's chosen row is the neutral fill: on its button, no
// accent shows on the row.
func TestRuleSetContentChosenRowHover(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "b.example")
	f.click("a.example")
	img := f.frameAt(f.center("移除 a.example"))
	scale := float32(img.Bounds().Dx()) / 1280
	value, _ := tt.Find("a.example")
	button, _ := tt.Find("移除 a.example")
	for x := float32(300); x < button.X+button.W; x += 2 {
		if p := img.RGBAAt(int(x*scale), int((value.Y+value.H/2)*scale)); accentish(p) {
			t.Fatalf("the chosen row shows the accent %v at %v", p, x)
		}
	}
	if f.w.ruleSets.detail.table.Selected != 0 {
		t.Fatalf("chosen %d", f.w.ruleSets.detail.table.Selected)
	}
}

// The content's search shows the best matches first: the value equal to
// the query, then those starting with it, then the domains it is a
// suffix of, then the others containing it.
func TestRuleSetContentSearchRanks(t *testing.T) {
	f, _ := ruleSetFixture(t, func(doc *model.Doc) {
		doc.Route.RuleSets = append(doc.Route.RuleSets, &model.RuleSet{ID: "rs-g", Tag: "g", Enabled: true, Mode: "inline",
			Rules: []any{map[string]any{"domain_suffix": []any{"agoogle.com", "www.google.com", "google.com.hk", "Google.com", "example.org"}}}})
	})
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("打开规则集 g")
	testkit.UntilText(t, tt, "www.google.com")
	f.click("搜索规则")
	tt.Type("google.com")
	tt.Advance(searchDelay)
	testkit.Until(t, tt, func() bool { return tt.HasText("4 / 5 条") })
	order := []string{"Google.com", "google.com.hk", "www.google.com", "agoogle.com"}
	last := float32(-1)
	for _, value := range order {
		r, ok := tt.Find(value)
		if !ok || r.Y <= last {
			t.Fatalf("%q at %v (after %v); texts %q", value, r, last, tt.Texts())
		}
		last = r.Y
	}
}
