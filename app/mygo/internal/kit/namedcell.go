package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// namedGap is the space between a value and its name, as in
// FilterOptionNamed.
const namedGap = 6

// NamedCell is a table cell of value (an address) followed by name in the
// secondary colour, as the source columns name the device of an address,
// with secondary on a second line in two-line tables. The cell clips what
// it draws. value in tabular digits keeps its width while it fits; name
// shrinks first, to one line with an ellipsis, and only a cell too narrow
// for the whole address leaves the name out and ends the address with an
// ellipsis. Then, either clipped, the cell shows "value · name" whole in a
// hint. An address without a name is drawn the same way, alone, so a column
// of addresses lines up; its hint is the address.
func NamedCell(c *ui.Context, value, name, secondary string) ui.Element {
	full := value
	if name != "" {
		full = value + " · " + name
	}
	return ui.Column(c).MinWidth(0).Grow(1).Gap(1).Clip().Children(func() {
		line := ui.Row(c).FillWidth().MinWidth(0).Gap(namedGap)
		m := ui.Local(line, "named-cell", func() namedWidth { return namedWidth{} })
		if m.value != value || m.name != name {
			vw, _ := ui.ShapeText(value, ui.Font{Size: FontTable, Weight: 400, Features: "tnum"}, 0).Size()
			width := vw
			if name != "" {
				nw, _ := ui.ShapeText(name, ui.Font{Size: FontCaption, Weight: 400}, 0).Size()
				width += namedGap + nw
			}
			m.value, m.name, m.valueWidth, m.width = value, name, vw, width
		}
		// The line's width of the last frame; none yet before the first.
		room := line.Bounds().W
		whole := room <= 0 || m.valueWidth <= room+0.5
		line.Children(func() {
			address := ui.Text(c, value).SingleLine().FontSize(FontTable).FontFeatures("tnum")
			if !whole {
				address.Ellipsis("…").MinWidth(0)
				return
			}
			address.Shrink(0)
			if name != "" {
				ui.Text(c, name).SingleLine().Ellipsis("…").MinWidth(0).FontSize(FontCaption).TextColor(Muted(c))
			}
		})
		namedHint(c, line, room > 0 && m.width > room+0.5, full)
		if secondary != "" {
			Clipped(c, secondary, FontCaption, 400).TextColor(Muted(c))
		}
	})
}

// namedHint shows full in a hint by line, the pointer resting on it, when
// clipped: as FullText, which shares the hover state, for the pointer
// rests on one element at a time.
func namedHint(c *ui.Context, line ui.Element, clipped bool, full string) {
	h := &fullTextHover
	if !line.Hovered() {
		if h.id == line.ID() {
			h.since = time.Time{}
		}
		return
	}
	if h.id != line.ID() || h.text != full {
		h.id, h.text, h.size, h.weight, h.width, h.since = line.ID(), full, 0, 0, 0, time.Time{}
		// The line becomes a tooltip anchor in this frame and counts as one
		// from the next: build it once more, so a pointer that has come to
		// rest still starts the tip's wait.
		c.After(time.Millisecond)
	}
	if clipped {
		hint(c, line, full, &h.since)
	}
}

// namedWidth is the measured width of a value and its name on one line,
// and of the value alone.
type namedWidth struct {
	value, name       string
	valueWidth, width float32
}

// NamedProperty is Property whose value (an address) is followed by name
// in the secondary colour, as the inspectors name the device of a source
// address: on one line where both fit, else the name below the value,
// both wrapping. Only the value is selectable, so a copy is the address
// alone. Without a name it is Property.
func NamedProperty(c *ui.Context, label, value, name string) ui.Element {
	if name == "" {
		return Property(c, label, value)
	}
	k := T(c)
	return PropertyWith(c, label, func() {
		// Clipped, so an address longer than a line without a break never
		// draws past the inspector.
		box := ui.Column(c).Grow(1).MinWidth(0).Gap(1).Clip()
		m := ui.Local(box, "named-property", func() namedWidth { return namedWidth{} })
		if m.value != value || m.name != name {
			font := ui.Font{Size: FontTable, Weight: 400}
			vw, _ := ui.ShapeText(value, font, 0).Size()
			nw, _ := ui.ShapeText(name, font, 0).Size()
			m.value, m.name, m.valueWidth, m.width = value, name, vw, vw+namedGap+nw
		}
		inline := m.width <= box.Bounds().W+0.5
		box.Children(func() {
			if inline {
				ui.Row(c).Gap(namedGap).Children(func() {
					ui.Text(c, value).FontSize(FontTable).Selectable().SingleLine().Shrink(0)
					ui.Text(c, name).FontSize(FontTable).TextColor(k.Label2).SingleLine().Shrink(0)
				})
				return
			}
			ui.Text(c, value).FontSize(FontTable).Selectable().Wrap().MinWidth(0)
			ui.Text(c, name).FontSize(FontTable).TextColor(k.Label2).Wrap().MinWidth(0)
		})
	})
}
