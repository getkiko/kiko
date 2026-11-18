package kit

import "github.com/egoist/mygo/ui"

// Bounds of a filter pane the user resizes.
const (
	FilterPaneMinW = 180
	FilterPaneMaxW = 420
)

// PaneWidth is the width the user gave a side pane by dragging its edge;
// a Width of 0 keeps the pane's default width.
type PaneWidth struct {
	Width float32

	held          bool
	origin, moved float32
}

// Shown is the width to lay the pane out at: Width kept between lo and
// hi, or def when the user set none.
func (p *PaneWidth) Shown(def, lo, hi float32) float32 {
	if p.Width <= 0 {
		return def
	}
	return min(max(p.Width, lo), hi)
}

// ResizeHandle is the handle on the right edge of a pane shown DIPs wide
// that starts its row: build it last in that row, which then draws it
// over its neighbours. Dragging it sets p.Width between lo and hi, the
// arrows move it once it has the focus, and a double click returns to the
// default width. It reports a finished change (a drag let go, a key, a
// reset), the time to save p.Width.
func ResizeHandle(c *ui.Context, p *PaneWidth, shown, lo, hi float32, label string) bool {
	k := T(c)
	const grip = 7
	h := ui.Box(c).Absolute().Top(0).Bottom(0).Left(shown - grip/2.0).Width(grip).
		Cursor(ui.CursorResizeColumn).Focusable().Role(ui.RoleSplitter).Label(label)
	Hint(c, h, label+"，双击恢复默认")
	done := false
	dx, _, held := h.Dragged()
	switch {
	case held && !p.held:
		p.held, p.origin, p.moved = true, shown, dx
	case held:
		p.moved += dx
	case p.held:
		p.held = false
		done = p.moved != 0
	}
	if held && p.moved != 0 {
		p.Width = min(max(p.origin+p.moved, lo), hi)
	}
	if h.DoubleClicked() {
		p.held, p.Width, done = false, 0, true
	}
	for _, step := range []struct {
		key   ui.Key
		delta float32
	}{{ui.KeyLeft, -16}, {ui.KeyRight, 16}} {
		if h.Shortcut(0, step.key) {
			p.Width, done = min(max(shown+step.delta, lo), hi), true
		}
	}
	line := ui.Color{}
	if held || h.Hovered() || h.FocusVisible() {
		line = k.Accent
	}
	h.Children(func() {
		ui.Box(c).Absolute().Top(0).Bottom(0).Left((grip - 2) / 2).Width(2).Background(line).Transition(HoverMotion)
	})
	return done
}

// FilterPaneSize is the width to lay a resizable filter pane out at, p's
// or the default, and the bounds a drag keeps it in: at most a third of
// the window, so the list keeps its room.
func FilterPaneSize(c *ui.Context, variant FilterVariant, p *PaneWidth) (shown, lo, hi float32) {
	def := FilterPaneWidth(c, variant)
	w, _ := c.Size()
	lo = min(FilterPaneMinW, def)
	hi = max(lo, min(FilterPaneMaxW, w/3))
	return p.Shown(def, lo, hi), lo, hi
}

// FilterResizeLabel names the handle of a resizable filter pane.
const FilterResizeLabel = "调整筛选栏宽度"
