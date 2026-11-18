package kit

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

// ColumnOrder is the order a table's columns are shown in, set by dragging
// their headers. Keep one for each table; save Order across launches. The
// zero value is the default order.
//
// Order lists column IDs. Tables pass their columns' IDs in default order
// to the methods, so an Order saved by an older version still applies:
// unknown IDs are dropped and columns it lacks follow in default order.
type ColumnOrder struct {
	Order []string
	// Confirm is the state of the restore confirmation (View.Reset).
	Confirm ConfirmState

	// drag is the column whose header is dragged, at visual index dragAt;
	// its centre starts at start and has moved by moved and movedY since.
	drag          string
	dragAt        int
	start         float32
	moved, movedY float32
	active        bool
	escaped       bool
	// boundary is the visual index the dragged column would drop before
	// (the column count: after the last), -1 for no change; slot is its
	// new visual index.
	boundary, slot int
	// centers are the headers' centres as of the last frame.
	centers []float32
}

// reorderSlop is how far a header is dragged before it counts as a drag,
// and reorderCancel how far off the header row cancels it.
const (
	reorderSlop   = 4
	reorderCancel = 48
)

// Indexes returns the visual order of the columns whose default order has
// the IDs ids: Indexes()[i] is the index in ids of the i-th column shown.
func (o *ColumnOrder) Indexes(ids []string) []int {
	out := make([]int, 0, len(ids))
	seen := make([]bool, len(ids))
	for _, id := range o.Order {
		if i := slices.Index(ids, id); i >= 0 && !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	for i := range ids {
		if !seen[i] {
			out = append(out, i)
		}
	}
	return out
}

// Arrange returns columns in the order shown.
func (o *ColumnOrder) Arrange(columns []Column) []Column {
	ids := make([]string, len(columns))
	for i, col := range columns {
		ids[i] = col.id()
	}
	order := o.Indexes(ids)
	out := make([]Column, len(columns))
	for i, at := range order {
		out[i] = columns[at]
	}
	return out
}

// Custom reports whether the order differs from the default.
func (o *ColumnOrder) Custom(ids []string) bool {
	for i, at := range o.Indexes(ids) {
		if i != at {
			return true
		}
	}
	return false
}

// Move puts column id at visual index to (held within the columns),
// reporting whether the order changed.
func (o *ColumnOrder) Move(ids []string, id string, to int) bool {
	order := o.Indexes(ids)
	from := slices.IndexFunc(order, func(at int) bool { return ids[at] == id })
	if from < 0 || len(order) == 0 {
		return false
	}
	to = min(max(to, 0), len(order)-1)
	if to == from {
		return false
	}
	moving := order[from]
	order = slices.Delete(order, from, from+1)
	order = slices.Insert(order, to, moving)
	next := make([]string, len(order))
	for i, at := range order {
		next[i] = ids[at]
	}
	o.Order = next
	return true
}

// Reset restores the default order.
func (o *ColumnOrder) Reset() { o.Order = nil }

// OrderView is a ColumnOrder bound to a table's columns for a frame.
type OrderView struct {
	o        *ColumnOrder
	ids      []string
	order    []int
	onChange func()
	// widths, when bound (WithWidths), are restored by Reset too.
	widths   *ColumnWidths
	onResize func()
}

// WithWidths binds the table's column widths too: Reset's button then
// shows while the order or the widths differ from the default, and
// restores both, telling onResize to save the widths.
func (v OrderView) WithWidths(w *ColumnWidths, onResize func()) OrderView {
	v.widths, v.onResize = w, onResize
	return v
}

// Custom reports whether the order, or the bound widths, differ from the
// default.
func (v OrderView) Custom() bool {
	return v.o != nil && (v.o.Custom(v.ids) || v.widths != nil && v.widths.Custom(v.ids))
}

// View binds o to the columns with IDs ids (in default order) for the
// frame. onChange is told when the order changes, to save it.
func (o *ColumnOrder) View(ids []string, onChange func()) OrderView {
	return OrderView{o: o, ids: ids, order: o.Indexes(ids), onChange: onChange}
}

// Order is the visual order: the index in the default order of each column
// shown.
func (v OrderView) Order() []int { return v.order }

func (v OrderView) changed() {
	if v.onChange != nil {
		v.onChange()
	}
}

// Header makes handle, the focusable element in the header box of the
// column shown at visual index visual, drag the column to a new place, and
// Alt+Left / Alt+Right move it with the keyboard. It shows the drop place
// as an accent line and the dragged column lifted, and reports whether a
// drag is going on or just ended, so a click on the handle is not one.
func (v OrderView) Header(c *ui.Context, box, handle ui.Element, visual int, label string) (swallow bool) {
	o, k := v.o, T(c)
	id := v.ids[v.order[visual]]
	n := len(v.order)
	if len(o.centers) != n {
		o.centers = make([]float32, n)
	}
	if b := box.Bounds(); b.W > 0 {
		o.centers[visual] = b.X + b.W/2
	}
	Hint(c, handle, label+"，拖动或按 Alt+←/→ 调整列顺序")

	dx, dy, held := handle.Dragged()
	switch {
	case held && o.drag != id:
		o.drag, o.dragAt, o.start = id, visual, o.centers[visual]
		o.moved, o.movedY, o.active, o.escaped = dx, dy, false, false
		o.boundary, o.slot = -1, visual
	case held:
		o.moved += dx
		o.movedY += dy
	case o.drag == id:
		// Let go: drop it where the line was.
		moved := o.active
		if o.active && !o.escaped && o.slot != visual && o.boundary >= 0 {
			if o.Move(v.ids, id, o.slot) {
				v.changed()
			}
		}
		o.drag, o.active, o.boundary = "", false, -1
		swallow = moved
	}
	if o.drag == id {
		if handle.Shortcut(0, ui.KeyEscape) {
			o.escaped = true
		}
		o.active = o.active || abs32(o.moved) > reorderSlop
		swallow = swallow || o.active
		off := abs32(o.movedY) > reorderCancel || o.escaped
		o.boundary, o.slot = -1, visual
		if o.active && !off {
			pos := o.start + o.moved
			slot := 0
			for i, centre := range o.centers {
				if i != visual && centre < pos {
					slot++
				}
			}
			if slot != visual {
				o.slot = slot
				if slot < n-1 {
					// Before the column now in that place.
					o.boundary = slot
					if slot >= visual {
						o.boundary = slot + 1
					}
				} else {
					o.boundary = n
				}
			}
		}
		if o.active {
			box.Opacity(0.55).Background(k.Fill)
		}
	}

	for _, step := range []struct {
		key   ui.Key
		delta int
	}{{ui.KeyLeft, -1}, {ui.KeyRight, 1}} {
		if handle.Shortcut(ui.Alt, step.key) && o.Move(v.ids, id, visual+step.delta) {
			v.changed()
		}
	}

	// The line shows where the column would go: at the left edge of the
	// header it drops before, at the right edge of the last one after it.
	if o.drag != "" && o.active && o.boundary >= 0 {
		if o.boundary == visual {
			ui.Box(c.Key("drop")).Absolute().Top(4).Bottom(4).Left(0).Width(2).Radius(1).Background(k.Accent)
		}
		if o.boundary == n && visual == n-1 {
			ui.Box(c.Key("drop")).Absolute().Top(4).Bottom(4).Right(0).Width(2).Radius(1).Background(k.Accent)
		}
	}
	return swallow
}

// resetSlot is the width reserved at the end of every row, header and
// cells alike, for the restore button, while the columns are custom.
const resetSlot = 28

func (v OrderView) showReset() bool {
	return v.Custom() || v.o != nil && v.o.Confirm.Open
}

// Reserved is the width the rows reserve at their end for Reset's button:
// 0 while the columns are the default. Add it to the rows' minimum width.
func (v OrderView) Reserved() float32 {
	if v.showReset() {
		return resetSlot
	}
	return 0
}

// Spacer ends a row's cells with the room Reset's button takes in the
// header, keeping the columns aligned.
func (v OrderView) Spacer(c *ui.Context) {
	if v.showReset() {
		ui.Box(c.Key("order-slot")).Width(resetSlot).Shrink(0).FillHeight()
	}
}

// Reset ends the header's cells with the small button restoring the default
// columns, in the room Spacer keeps in the rows, so it covers no title, and
// its confirmation. It shows only while the order, or the bound widths
// (WithWidths), differ, and restores both.
func (v OrderView) Reset(c *ui.Context) {
	if !v.showReset() {
		return
	}
	o := v.o
	var btn ui.Element
	// The slot lets the pointer through, and its button keeps to its end,
	// clear of the last column's grip straddling the slot's start.
	ui.Row(c.Key("order-slot")).Width(resetSlot).Shrink(0).FillHeight().Justify(ui.End).PassThrough().Children(func() {
		btn = IconButton(c.Key("order-reset"), SymRotateCcw, "恢复默认列", IconButtonOptions{Size: 22, IconSize: 13})
	})
	if btn.Clicked() {
		o.Confirm.Open = true
	}
	title := "恢复默认列顺序？"
	if v.widths != nil {
		title = "恢复默认列顺序和宽度？"
	}
	ConfirmPopover(c, btn, &o.Confirm, ConfirmOptions{
		Title: title, Confirm: "恢复",
		Run: func(done func(error)) {
			if o.Custom(v.ids) {
				o.Reset()
				v.changed()
			}
			if w := v.widths; w != nil && w.Custom(v.ids) {
				for _, id := range v.ids {
					delete(w.Widths, id)
				}
				if v.onResize != nil {
					v.onResize()
				}
			}
			done(nil)
		},
	})
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// Dropping reports whether a header is being dragged to a new place, with
// the drop line showing.
func (o *ColumnOrder) Dropping() bool { return o.drag != "" && o.active && o.boundary >= 0 }
