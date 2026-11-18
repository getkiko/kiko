package kit

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// inWindow fails unless r lies in the window, w×h, 4 DIPs from its edges
// (MyGo's window margin).
func inWindow(t *testing.T, what string, r ui.Rect, w, h float32) {
	t.Helper()
	const margin = 4 - 0.5
	if r.W <= 0 || r.H <= 0 || r.X < margin || r.Y < margin || r.X+r.W > w-margin || r.Y+r.H > h-margin {
		t.Fatalf("%s %+v leaves the %v×%v window", what, r, w, h)
	}
}

// within reports whether r lies inside box.
func within(r, box ui.Rect) bool {
	return r.Y >= box.Y-0.5 && r.Y+r.H <= box.Y+box.H+0.5 && r.X >= box.X-0.5 && r.X+r.W <= box.X+box.W+0.5
}

// TestActionMenuStaysInTheWindow opens a menu taller than the room on
// either side of its button, at the window's bottom, top and middle and
// in a narrow window: the panel lies whole in the window, opening on the
// larger side, and its list scrolls to the last item, which runs.
func TestActionMenuStaysInTheWindow(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		x, y          float32
		above         bool
	}{
		{"bottom", 640, 640, 300, 600, true},
		{"top", 640, 640, 300, 8, false},
		{"short", 640, 300, 300, 135, false},
		{"short-low", 640, 300, 300, 160, true},
		{"narrow", 200, 400, 150, 300, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, ran := false, ""
			var items []ActionItem
			for i := range 16 {
				label := fmt.Sprintf("操作 %d", i+1)
				items = append(items, ActionItem{ID: label, Label: label, Icon: SymCopy, Run: func() { ran = label }})
			}
			// The boxes are those of the last frame, read as each builds.
			var box, anchor ui.Rect
			tt := ui.NewTester(func(c *ui.Context) {
				Apply(c)
				c.Root().Background(T(c).Surface).Children(func() {
					button := IconButton(c, SymEllipsis, "更多", IconButtonOptions{}).Absolute().Left(tc.x).Top(tc.y)
					if button.Clicked() {
						open = !open
					}
					anchor = button.Bounds()
					if panel := ActionMenu(c, button, &open, []ActionGroup{{Label: "分组", Items: items}}, ActionMenuOptions{Label: "菜单"}); open {
						box = panel.Bounds()
					}
				})
			}, tc.width, tc.height)
			tt.Frame()
			if err := tt.Click("更多"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			tt.Frame()
			w, h := float32(tc.width), float32(tc.height)
			inWindow(t, "panel", box, w, h)
			if above := box.Y+box.H <= anchor.Y; above != tc.above {
				t.Fatalf("panel %+v above %v of button %+v, want %v", box, above, anchor, tc.above)
			}
			last, ok := tt.Find("操作 16")
			if !ok {
				t.Fatalf("no last item: %q", tt.Texts())
			}
			if within(last, box) {
				t.Fatalf("all 16 items fit in %+v: the menu is not tall enough to test", box)
			}
			for range 10 {
				tt.Scroll(box.X+box.W/2, box.Y+box.H/2, 0, 200)
				tt.Frame()
			}
			last, _ = tt.Find("操作 16")
			if !within(last, box) {
				t.Fatalf("last item %+v out of the scrolled panel %+v", last, box)
			}
			tt.ClickAt(last.X+last.W/2, last.Y+last.H/2)
			tt.Frame()
			if ran != "操作 16" || open {
				t.Fatalf("ran %q, open %v", ran, open)
			}
		})
	}
}

// TestConfirmPopoverStaysInTheWindow: a confirmation with a long body by
// a button low in a short window shows above it, whole in the window, its
// body scrolling to the confirm button.
func TestConfirmPopoverStaysInTheWindow(t *testing.T) {
	var s ConfirmState
	confirmed := false
	var box, anchor ui.Rect
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Children(func() {
			button := Button(c, "清空", ButtonOptions{}).Absolute().Left(40).Top(200)
			if button.Clicked() {
				s.Open = true
			}
			anchor = button.Bounds()
			panel := ConfirmPopover(c, button, &s, ConfirmOptions{Title: "清空？", Confirm: "确认清空", Danger: true,
				Body: func() {
					for i := range 12 {
						ui.Text(c, fmt.Sprintf("将清除第 %d 项", i+1)).FontSize(FontTable)
					}
				},
				Run: func(done func(error)) { confirmed = true; done(nil) }})
			if s.Open {
				box = panel.Bounds()
			}
		})
	}, 400, 260)
	tt.Frame()
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	inWindow(t, "panel", box, 400, 260)
	if box.Y+box.H > anchor.Y {
		t.Fatalf("panel %+v not above the button %+v", box, anchor)
	}
	for range 10 {
		tt.Scroll(box.X+box.W/2, box.Y+box.H/2, 0, 200)
		tt.Frame()
	}
	r, ok := tt.Find("确认清空")
	if !ok || !within(r, box) {
		t.Fatalf("confirm button %+v (%v) out of the scrolled panel %+v", r, ok, box)
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if !confirmed {
		t.Fatal("not confirmed")
	}
}

// TestActionMenuUnavailableItem: an unavailable item shows its hint on a
// long hover and runs; a disabled one does nothing.
func TestActionMenuUnavailableItem(t *testing.T) {
	open, ran := true, ""
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20).Children(func() {
			button := IconButton(c, SymEllipsis, "更多", IconButtonOptions{})
			ActionMenu(c, button, &open, []ActionGroup{{Items: []ActionItem{
				{ID: "share", Label: "分享", Unavailable: true, Hint: "先配置地址", Run: func() { ran = "share" }},
				{ID: "export", Label: "导出", Disabled: true, Run: func() { ran = "export" }},
			}}}, ActionMenuOptions{})
		})
	}, 400, 300)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ran != "" || !open {
		t.Fatalf("disabled item ran %q, open %v", ran, open)
	}
	r, ok := tt.Find("分享")
	if !ok {
		t.Fatalf("no item: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(2 * time.Second)
	if !tt.HasText("先配置地址") {
		t.Fatalf("no hint: %q", tt.Texts())
	}
	if err := tt.Click("分享"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ran != "share" || open {
		t.Fatalf("unavailable item ran %q, open %v", ran, open)
	}
}

// TestSelectStaysInTheWindow opens a select of 30 options low in a short
// window: the popup lies whole in the window, above the trigger, and the
// arrows bring the last option into it, where Enter chooses it.
func TestSelectStaysInTheWindow(t *testing.T) {
	value := ""
	var options []Option
	for i := range 30 {
		v := fmt.Sprintf("o%d", i+1)
		options = append(options, Option{Value: v, Label: "选项 " + v})
	}
	var anchor ui.Rect
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Children(func() {
			sel := Select(c, "pick", &value, options, SelectOptions{Label: "选择", Width: 160})
			sel.Absolute().Left(40).Top(220)
			anchor = sel.Bounds()
		})
	}, 400, 280)
	tt.Frame()
	if err := tt.Click("选择：请选择"); err != nil {
		if err := tt.Click("选择"); err != nil {
			t.Fatalf("%v: %q", err, tt.Texts())
		}
	}
	tt.Frame()
	tt.Frame()
	first, ok := tt.Find("选项 o1")
	if !ok {
		t.Fatalf("no popup: %q", tt.Texts())
	}
	if first.Y >= anchor.Y {
		t.Fatalf("first option %+v not above the trigger %+v", first, anchor)
	}
	inWindow(t, "first option", first, 400, 280)
	for range 30 {
		tt.Key(0, ui.KeyDown)
	}
	tt.Advance(time.Second)
	last, ok := tt.Find("选项 o30")
	if !ok {
		t.Fatalf("no last option: %q", tt.Texts())
	}
	inWindow(t, "last option", last, 400, 280)
	if last.Y+last.H > anchor.Y {
		t.Fatalf("last option %+v not above the trigger %+v", last, anchor)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if value != "o30" {
		t.Fatalf("chose %q", value)
	}
}
