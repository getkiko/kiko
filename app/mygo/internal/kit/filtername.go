package kit

import "github.com/egoist/mygo/ui"

// FilterOptionNamed is a filter option whose label (an IP) is followed by a
// name in the secondary colour, as the source filters show a device. The
// label keeps its width; the name shrinks to one line with an ellipsis and
// shows whole in a hint. detail is the right-hand text (count or total); the
// button's accessible label is "label name". Read Clicked on the element it
// returns.
func FilterOptionNamed(c *ui.Context, variant FilterVariant, selected bool, label, name, detail string) ui.Element {
	return FilterOptionIconNamed(c, variant, selected, nil, label, name, detail)
}

// FilterOptionIconNamed is FilterOptionNamed with an icon before the label,
// as the connection PID options show the process icon and name; icon may
// be nil.
func FilterOptionIconNamed(c *ui.Context, variant FilterVariant, selected bool, icon func(), label, name, detail string) ui.Element {
	k := T(c)
	full := label
	if name != "" {
		full = label + " " + name
	}
	gap := float32(6)
	if icon != nil {
		gap = 7
	}
	o := ui.ButtonBase(c).MinHeight(26).PaddingX(7).Gap(gap).Radius(7).FontSize(FontTable).Label(full).
		Transition(HoverMotion)
	if variant == FilterDefault {
		o.MinHeight(28)
	}
	switch {
	case selected && variant == FilterConnection:
		o.Background(k.AccentWeak).TextColor(k.AccentText).FontWeight(600)
	case o.Hovered():
		o.Background(k.Fill)
	}
	if !selected || variant != FilterConnection {
		o.TextColor(k.Label)
	}
	o.Children(func() {
		if variant == FilterDefault {
			mark := ui.Box(c).Size(14, 14).Radius(3.5).Center().Border(1, k.Hairline).Shrink(0)
			if selected {
				mark.Background(k.Accent).Border(0, k.Accent).TextColor(k.OnAccent).Children(func() { Icon(c, SymCheck, 10) })
			}
		}
		if icon != nil {
			icon()
		}
		weight := filterWeight(variant, selected)
		if name == "" {
			Clipped(c, label, FontTable, weight).Grow(1)
		} else {
			ui.Text(c, label).SingleLine().FontSize(FontTable).FontWeight(weight).Shrink(0)
			Clipped(c, name, FontCaption, 400).TextColor(k.Label3).Grow(1)
		}
		if detail != "" {
			ui.Text(c, detail).FontSize(10).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
		}
	})
	return o
}
