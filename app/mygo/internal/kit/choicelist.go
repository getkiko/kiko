package kit

import (
	"cmp"

	"github.com/egoist/mygo/ui"
)

// Choice is an option of a ChoiceList: a label, an optional detail on its
// right (a count), and whether it cannot be chosen. Name, when set, is
// what assistive technology reads instead of the label.
type Choice struct {
	Label, Detail, Name string
	Disabled            bool
}

// ChoiceList is a compact radio group named label: one row per choice
// with a round mark, the label and its detail. The chosen row takes the
// weak accent fill; a click sets *selected.
func ChoiceList(c *ui.Context, label string, selected *int, choices ...Choice) ui.Element {
	k := T(c)
	return ui.RadioGroup(c, func() {
		for i, choice := range choices {
			on := *selected == i
			r := ui.RadioBase(c, selected, i).MinHeight(SmallControl + 2).PaddingX(8).Gap(8).Radius(8).
				FontSize(FontTable).Label(cmp.Or(choice.Name, choice.Label)).Disabled(choice.Disabled).Transition(HoverMotion)
			switch {
			case on:
				r.Background(k.AccentWeak).TextColor(k.AccentText)
			case r.Hovered() && !choice.Disabled:
				r.Background(k.Fill).TextColor(k.Label)
			case choice.Disabled:
				r.TextColor(k.Label3)
			default:
				r.TextColor(k.Label)
			}
			r.Children(func() {
				mark := ui.Box(c).Size(14, 14).Radius(7).Center().Border(1, k.Hairline).Shrink(0)
				if on {
					mark.Border(0, k.Accent).Background(k.Accent).Children(func() {
						ui.Box(c).Size(6, 6).Radius(3).Background(k.OnAccent)
					})
				}
				ui.Text(c, choice.Label).SingleLine().Grow(1).MinWidth(0)
				if choice.Detail != "" {
					ui.Text(c, choice.Detail).FontSize(FontCaption).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
				}
			})
		}
	}).Gap(2).Label(label)
}
