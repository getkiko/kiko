package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// fullTextHover is the text the pointer rests on, as FullText measured
// it. The pointer rests on one element at a time, and views build on the
// UI thread only.
var fullTextHover struct {
	id     uint64
	text   string
	size   float32
	weight int
	width  float32
	since  time.Time
}

// FullText shows s whole in a hint by e, single-line text showing s, when
// e is too narrow for it and ends it with an ellipsis: after the pointer
// has rested on e for about 1.5 seconds, as Hint. size and weight are e's
// font, which measures s.
func FullText(c *ui.Context, e ui.Element, s string, size float32, weight int) ui.Element {
	h := &fullTextHover
	if s == "" || !e.Hovered() {
		if h.id == e.ID() {
			// The pointer left: its hint waits anew as it comes back.
			h.since = time.Time{}
		}
		return e
	}
	if h.id != e.ID() || h.text != s || h.size != size || h.weight != weight {
		w, _ := ui.ShapeText(s, ui.Font{Size: size, Weight: weight}, 0).Size()
		h.id, h.text, h.size, h.weight, h.width, h.since = e.ID(), s, size, weight, w, time.Time{}
		// e becomes a tooltip anchor in this frame and counts as one from
		// the next: build it once more, so a pointer that has come to rest
		// still starts the tip's wait.
		c.After(time.Millisecond)
	}
	if h.width <= e.Bounds().W+0.5 {
		return e
	}
	hint(c, e, s, &h.since)
	return e
}

// Clipped is single-line text in size and weight that ends with an
// ellipsis where it does not fit and then shows whole in a hint
// (FullText).
func Clipped(c *ui.Context, s string, size float32, weight int) ui.Element {
	e := ui.Text(c, s).SingleLine().Ellipsis("…").MinWidth(0).FontSize(size).FontWeight(weight)
	return FullText(c, e, s, size, weight)
}
