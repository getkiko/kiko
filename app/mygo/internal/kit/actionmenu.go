package kit

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// ActionItem is an action of an ActionMenu. SearchText is what the search
// matches, the label when empty; Detail shows muted after the label; Name
// is the accessible name, the label when empty. Hint shows by the item as
// the pointer rests on it (Hint). A Disabled item shows in the tertiary
// text color and does nothing; an Unavailable one looks the same but
// stays focusable, shows its hint and runs, as to lead to the setting that
// makes it available.
type ActionItem struct {
	ID, Label, Detail, SearchText string
	Name, Hint                    string
	Icon                          Symbol
	Disabled, Unavailable, Danger bool
	Run                           func()
}

// ActionGroup is a titled group of an ActionMenu.
type ActionGroup struct {
	Label  string
	Items  []ActionItem
	Danger bool
}

// ActionMenuOptions shape an ActionMenu.
type ActionMenuOptions struct {
	// Width is the panel's width, 220 DIPs by default.
	Width float32
	// Searchable shows a search field at the top, focused as the menu
	// opens, with Placeholder ("Search..." in Chinese by default).
	Searchable  bool
	Placeholder string
	// Compact rows are 30 DIPs instead of 36.
	Compact bool
	// Label names the menu, "Actions" in Chinese by default.
	Label string
}

type actionMenuState struct{ query string }

// ActionMenu is the grouped action popover beside anchor
// (AnchoredActionPopover), open while *open: a search field when
// Searchable, then each group's title and items. Choosing an item closes
// the menu and runs it.
func ActionMenu(c *ui.Context, anchor ui.Element, open *bool, groups []ActionGroup, o ActionMenuOptions) ui.Element {
	k := T(c)
	width := o.Width
	if width == 0 {
		width = 220
	}
	label := o.Label
	if label == "" {
		label = "操作"
	}
	return ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		w, h := c.Size()
		Popup(c, panel).Width(min(width, max(w-16, 160))).Radius(16).Padding(6).Label(label)
		AttachPopover(c, panel, anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd, 6, max(min(h*0.7, 560), 200))
		state := ui.Local(panel, "menu", func() actionMenuState { return actionMenuState{} })
		if o.Searchable {
			placeholder := o.Placeholder
			if placeholder == "" {
				placeholder = "搜索…"
			}
			ui.Row(c).Height(32).PaddingX(10).Gap(6).Radius(10).Background(k.Fill).TextColor(k.Label3).
				Margin(0, 0, 4).Shrink(0).Children(func() {
				Icon(c, SymSearch, 15)
				ui.TextInputBase(c, &state.query).Grow(1).MinWidth(0).FontSize(FontBody).TextColor(k.Label).
					Placeholder(placeholder).SingleLine().Label(placeholder).AutoFocus()
			})
		}
		query := strings.ToLower(strings.TrimSpace(state.query))
		shown := 0
		ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Gap(4).Children(func() {
			for gi, group := range groups {
				var items []ActionItem
				for _, item := range group.Items {
					text := item.SearchText
					if text == "" {
						text = item.Label
					}
					if query == "" || strings.Contains(strings.ToLower(text), query) {
						items = append(items, item)
					}
				}
				if len(items) == 0 {
					continue
				}
				shown += len(items)
				ui.Column(c.Key(gi)).Shrink(0).Children(func() {
					if group.Label != "" {
						title := ui.Text(c, group.Label).FontSize(FontCaption).FontWeight(600).TextColor(k.Label3).Padding(6, 10, 3)
						if group.Danger {
							title.TextColor(k.Danger)
						}
					}
					for _, item := range items {
						height := float32(36)
						if o.Compact {
							height = 30
						}
						name := item.Name
						if name == "" {
							name = item.Label
						}
						muted := item.Disabled || item.Unavailable
						row := ui.ButtonBase(c.Key(item.ID + item.Label)).MinHeight(height).PaddingX(10).Gap(9).Radius(10).
							FillWidth().Justify(ui.Start).FontSize(FontBody).TextColor(k.Label).Label(name).Disabled(item.Disabled).
							Transition(HoverMotion)
						switch {
						case muted:
							row.TextColor(k.Label3)
						case item.Danger:
							row.TextColor(k.Danger)
						}
						switch {
						case item.Disabled:
						case row.Pressed():
							row.Background(k.Fill2)
						case row.Hovered():
							row.Background(k.Fill)
						}
						if item.Hint != "" {
							// Assistive technology reads the hint too.
							row.Description(item.Hint)
						}
						Hint(c, row, item.Hint)
						row.Children(func() {
							if item.Icon != "" {
								tone := k.Label3
								switch {
								case muted:
									tone = k.Label4
								case item.Danger:
									tone = k.Danger
								}
								Icon(c, item.Icon, 15).TextColor(tone)
							}
							ui.Text(c, item.Label).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							if item.Detail != "" {
								ui.Text(c, item.Detail).FontSize(FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
							}
						})
						if row.Clicked() && !item.Disabled {
							*open = false
							state.query = ""
							if item.Run != nil {
								item.Run()
							}
						}
					}
				})
			}
			if o.Searchable && shown == 0 {
				ui.Text(c, "没有匹配项目").FontSize(FontBody).TextColor(k.Label3).Padding(10)
			}
		})
	})
}
