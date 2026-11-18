package kit

import "github.com/egoist/mygo/ui"

// ColumnWidths are the widths the user dragged a table's columns to with
// ColumnGrip, by column ID. A Resizable Table keeps them in its TableState;
// a hand-drawn table keeps one of its own. Save Widths across launches.
type ColumnWidths struct {
	Widths map[string]float32
	// resizing is the column whose edge is dragged, from origin by moved.
	resizing      string
	origin, moved float32
	// declared are the columns' own widths, as Width and the grips last
	// saw them: a width equal to its column's own is no change.
	declared map[string]float32
}

// Column widths a drag keeps to: at least minColumnWidth (or the default
// width of a column narrower than that), at most maxColumnWidth.
const (
	minColumnWidth = 60
	maxColumnWidth = 900
)

// Width is the width col, as declared, shows at: the one it was dragged
// to, else its own (0 for a column sharing the room left).
func (w *ColumnWidths) Width(col Column) float32 {
	w.declare(col)
	if d := w.Widths[col.id()]; d > 0 {
		return d
	}
	return col.Width
}

// Custom reports whether any of the columns with IDs ids was dragged to a
// width other than its own (as Width last saw it), dragged back or not.
func (w *ColumnWidths) Custom(ids []string) bool {
	for _, id := range ids {
		d := w.Widths[id]
		if d <= 0 {
			continue
		}
		if own, ok := w.declared[id]; !ok || abs32(d-own) >= 0.5 {
			return true
		}
	}
	return false
}

// Resizing reports whether a column's edge is being dragged.
func (w *ColumnWidths) Resizing() bool { return w.resizing != "" }

func (w *ColumnWidths) set(id string, width float32, col Column) {
	floor := max(col.MinWidth*0.5, minColumnWidth)
	if col.Width > 0 {
		floor = min(floor, col.Width)
	}
	if w.Widths == nil {
		w.Widths = map[string]float32{}
	}
	w.Widths[id] = min(max(width, floor), maxColumnWidth)
	w.declare(col)
}

// declare notes col's own width, so that Custom takes a width dragged back
// to it for the default.
func (w *ColumnWidths) declare(col Column) {
	if col.Width <= 0 {
		return
	}
	if w.declared == nil {
		w.declared = map[string]float32{}
	}
	w.declared[col.id()] = col.Width
}

// ColumnGrip is the draggable right edge of box, the header cell of col,
// built inside it last. col is the column as declared, before Width: its
// own Width is the narrowest a column below minColumnWidth drags to. The
// grip spans the header's height (fill it) and straddles the edge, so the
// next header cell lets the pointer through (PassThrough).
//
// Dragging sets the column's entry in w; a double click restores its
// default; with the grip focused, Left and Right narrow and widen it.
// onResize is told when a drag or a key changes the width, to save w.
func ColumnGrip(c *ui.Context, w *ColumnWidths, col Column, box ui.Element, onResize func()) {
	k := T(c)
	id := col.id()
	const grip = 12
	title := col.Title
	if title == "" {
		title = id
	}
	label := "调整" + title + "列宽"
	h := ui.Box(c.Key("grip")).Absolute().Top(0).Bottom(0).Right(-grip / 2).Width(grip).
		Cursor(ui.CursorResizeColumn).Focusable().Role(ui.RoleSplitter).Label(label)
	Hint(c, h, label+"，双击恢复默认")
	done := false
	dx, _, held := h.Dragged()
	switch {
	case held && w.resizing != id:
		w.resizing, w.origin, w.moved = id, box.Bounds().W, dx
	case held:
		w.moved += dx
	case w.resizing == id:
		w.resizing = ""
		done = w.moved != 0
	}
	if held && w.moved != 0 {
		w.set(id, w.origin+w.moved, col)
	}
	if h.DoubleClicked() {
		delete(w.Widths, id)
		w.resizing, done = "", true
	}
	for _, step := range []struct {
		key   ui.Key
		delta float32
	}{{ui.KeyLeft, -16}, {ui.KeyRight, 16}} {
		if h.Shortcut(0, step.key) {
			w.set(id, box.Bounds().W+step.delta, col)
			done = true
		}
	}
	// A hairline between headers shows where columns part; as in Finder
	// it stays as it is under the pointer and while dragged, the cursor
	// telling it apart. Keyboard focus shows it in the accent.
	line := k.Hairline
	if h.FocusVisible() {
		line = k.Accent
	}
	h.Children(func() {
		ui.Box(c).Absolute().Top(8).Bottom(8).Right(grip / 2).Width(1).Background(line)
	})
	if done && onResize != nil {
		onResize()
	}
}
