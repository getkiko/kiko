package kit

import "github.com/egoist/mygo/ui"

// FilterOptionDetail is FilterOption with a text in place of the count,
// as the usage monitor's options show their total traffic ("1.2 GB").
// Read Clicked on the element it returns.
func FilterOptionDetail(c *ui.Context, variant FilterVariant, selected bool, label, detail string) ui.Element {
	return FilterOptionIconDetail(c, variant, selected, nil, label, detail)
}

// FilterOptionIconDetail is FilterOptionDetail with an icon before the
// label, as Usage's process options show the process icon; icon may be nil.
func FilterOptionIconDetail(c *ui.Context, variant FilterVariant, selected bool, icon func(), label, detail string) ui.Element {
	k := T(c)
	o := ui.ButtonBase(c).MinHeight(26).PaddingX(7).Gap(7).Radius(7).FontSize(FontTable).Label(label).
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
		Clipped(c, label, FontTable, filterWeight(variant, selected)).Grow(1)
		if detail != "" {
			ui.Text(c, detail).FontSize(10).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
		}
	})
	return o
}
