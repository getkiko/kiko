package kit

import "github.com/egoist/mygo/ui"

// EntityMenuItem is an action of an entity card's "More" menu. Run gets the
// card's more button, the anchor of a confirmation started from the menu.
// Disabled shows it in Label3, taking no clicks, as while its work is
// under way.
type EntityMenuItem struct {
	Label    string
	Icon     Symbol
	Danger   bool
	Disabled bool
	Run      func(anchor ui.Element)
}

// EntityCardOptions describe an entity card, the cards of the subscriptions
// workspace: inbounds, outbounds, DNS servers, endpoints and USB/IP services.
type EntityCardOptions struct {
	Title string
	// Eyebrow is a small uppercase line above the title.
	Eyebrow string
	Icon    Symbol
	// Badge builds chips after the title, as "Default" or "Needs fixing".
	Badge func()
	// Summary builds the line under the title, as a type chip and an
	// address (SummaryText for plain text).
	Summary     func()
	SummaryText string
	// Action builds controls left of the more button.
	Action func()
	// Footer builds the card's 42 DIP footer row.
	Footer func()
	// Menu lists the more menu's actions; with OnEnabledChange the menu
	// starts with "Enable" or "Disable".
	Menu            []EntityMenuItem
	Enabled         bool
	OnEnabledChange func(enabled bool)
	// OnOpen runs when the card's body is clicked: the editor opens.
	OnOpen func()
	// OpenLabel names the body for assistive technology, "Edit <title>"
	// by default.
	OpenLabel string
	// Muted grays a disabled entity; Warning tints a card that needs
	// fixing.
	Muted, Warning bool
	// Focus moves the keyboard focus to the card's body (FocusMore to its
	// more button) this frame, as an editor closes.
	Focus, FocusMore bool
}

// EntityCard is the card of an entity: an icon tile, the title with its
// badges, a summary, the more menu (enable/disable and Menu) and a footer. It
// returns the more button, which stays mounted, for anchoring
// confirmations started from the menu; the zero Element without a menu.
func EntityCard(c *ui.Context, key any, o EntityCardOptions) ui.Element {
	k := T(c)
	var more ui.Element
	card := ui.Column(c.Key(key)).Radius(18).Background(k.Surface).Border(1, k.HairlineSoft).Clip().
		AlignSelf(ui.Start).MinWidth(0).Transition(HoverMotion)
	menuOpen := ui.Local(card, "menu", func() bool { return false })
	switch {
	case o.Warning:
		card.Background(k.Surface.Mix(k.DangerWeak, 0.24)).Border(1, k.HairlineSoft.Mix(k.Danger, 0.42))
		if card.Hovered() {
			card.Border(1, k.Danger)
		}
	case o.Muted:
		card.Background(k.Surface2)
	default:
		if card.Hovered() {
			card.Border(1, k.Hairline)
		}
	}
	card.Children(func() {
		ui.Row(c).Padding(14).Gap(12).AlignItems(ui.Start).Children(func() {
			label := o.OpenLabel
			if label == "" {
				label = "编辑 " + o.Title
			}
			body := ui.ButtonBase(c).Grow(1).MinWidth(0).Gap(12).AlignItems(ui.Start).Justify(ui.Start).Label(label)
			if body.Pressed() {
				body.Opacity(0.7)
			}
			if o.Muted {
				body.Opacity(0.62)
			}
			if o.Focus {
				body.Focus()
			}
			body.Children(func() {
				if o.Icon != "" {
					tile := ui.Box(c).Size(40, 40).Radius(12).Center().Background(k.Fill.Alpha(0.55)).TextColor(k.Label3).Shrink(0)
					tile.Children(func() { Icon(c, o.Icon, 20) })
				}
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					if o.Eyebrow != "" {
						ui.Text(c, o.Eyebrow).FontSize(FontCaption).FontWeight(600).TextColor(k.Label3).SingleLine().Ellipsis("…")
					}
					ui.Row(c).Gap(8).MinWidth(0).Margin(2, 0, 0).Children(func() {
						ui.Text(c, o.Title).FontSize(16).FontWeight(600).TextColor(k.Label).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
						if o.Badge != nil {
							o.Badge()
						}
					})
					if o.Summary != nil || o.SummaryText != "" {
						ui.Row(c).Gap(8).MinWidth(0).Margin(6, 0, 0).MinHeight(20).FontSize(FontBody).TextColor(k.Label3).Children(func() {
							if o.Summary != nil {
								o.Summary()
							} else {
								ui.Text(c, o.SummaryText).SingleLine().Ellipsis("…").MinWidth(0)
							}
						})
					}
				})
			})
			if body.Clicked() && o.OnOpen != nil {
				o.OnOpen()
			}
			if o.Action == nil && o.OnEnabledChange == nil && len(o.Menu) == 0 {
				return
			}
			ui.Row(c).Gap(4).Shrink(0).Children(func() {
				if o.Action != nil {
					o.Action()
				}
				if o.OnEnabledChange == nil && len(o.Menu) == 0 {
					return
				}
				more = IconButton(c, SymEllipsis, "更多操作", IconButtonOptions{Size: 36, IconSize: 19})
				if o.FocusMore {
					more.Focus()
				}
				if more.Clicked() {
					*menuOpen = !*menuOpen
				}
				entityMenu(c, more, menuOpen, o.Enabled, o.OnEnabledChange, o.Menu)
			})
		})
		if o.Footer != nil {
			ui.Row(c).MinHeight(42).PaddingX(12).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).
				Background(k.Surface2.Alpha(0.35)).Children(o.Footer)
		}
	})
	return more
}

// entityMenu is the more menu of a card, open while *open, below its more
// button: "Enable" or "Disable" with onEnabledChange, then items, each closing
// the menu and running with the button as its anchor.
func entityMenu(c *ui.Context, more ui.Element, open *bool, enabled bool, onEnabledChange func(bool), items []EntityMenuItem) {
	ui.PopoverBase(c, more, open, func(panel ui.Element) {
		Popup(c, panel).Width(190).Padding(5).Radius(14).Clip().Label("更多操作")
		AttachPopover(c, panel, more, ui.AnchorBottomEnd, ui.AnchorTopEnd, 4, 0)
		PopoverBody(c, 1, func() { entityMenuItems(c, more, open, enabled, onEnabledChange, items) })
	})
}

// entityMenuItems are the rows of entityMenu.
func entityMenuItems(c *ui.Context, more ui.Element, open *bool, enabled bool, onEnabledChange func(bool), items []EntityMenuItem) {
	if onEnabledChange != nil {
		icon, text := SymPower, "启用"
		if enabled {
			icon, text = SymPowerOff, "停用"
		}
		if MenuItem(c, icon, text, false).Clicked() {
			*open = false
			onEnabledChange(!enabled)
		}
	}
	for _, item := range items {
		if item.Disabled {
			disabledMenuItem(c, item.Icon, item.Label)
			continue
		}
		if MenuItem(c, item.Icon, item.Label, item.Danger).Clicked() {
			*open = false
			if item.Run != nil {
				item.Run(more)
			}
		}
	}
}

// entityContextMenu fills m, the context menu of a card, with what its
// more menu lists (entityMenu): "Enable" or "Disable" with onEnabledChange, then
// items, each running with more, the card's more button, as its anchor.
func entityContextMenu(m *ui.Menu, more ui.Element, enabled bool, onEnabledChange func(bool), items []EntityMenuItem) {
	if onEnabledChange != nil {
		text := "启用"
		if enabled {
			text = "停用"
		}
		if m.Item(text).Chosen() {
			onEnabledChange(!enabled)
		}
		if len(items) > 0 {
			m.Separator()
		}
	}
	for _, item := range items {
		if m.Item(item.Label).Disabled(item.Disabled).Chosen() && item.Run != nil {
			item.Run(more)
		}
	}
}

// disabledMenuItem is MenuItem taking no clicks, its icon and label in
// Label3 at full opacity.
func disabledMenuItem(c *ui.Context, icon Symbol, label string) ui.Element {
	k := T(c)
	item := ui.Row(c).MinHeight(32).PaddingX(10).Gap(8).Radius(9).FillWidth().Justify(ui.Start).
		FontSize(FontBody).TextColor(k.Label3).Label(label)
	item.Children(func() {
		ui.Box(c).Size(20, 20).Center().Shrink(0).Children(func() {
			if icon != "" {
				Icon(c, icon, 15)
			}
		})
		ui.Text(c, label).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
	})
	return item
}

// MenuItem is a row of a popover menu: an icon and a label, 32 DIPs high,
// in the danger color when danger. Read Clicked on the element.
func MenuItem(c *ui.Context, icon Symbol, label string, danger bool) ui.Element {
	k := T(c)
	item := ui.ButtonBase(c).MinHeight(32).PaddingX(10).Gap(8).Radius(9).FillWidth().Justify(ui.Start).
		FontSize(FontBody).TextColor(k.Label).Label(label).Transition(HoverMotion)
	if danger {
		item.TextColor(k.Danger)
	}
	if item.Pressed() {
		item.Background(k.Fill2)
	} else if item.Hovered() {
		item.Background(k.Fill)
	}
	item.Children(func() {
		ui.Box(c).Size(20, 20).Center().Shrink(0).Children(func() {
			if icon != "" {
				Icon(c, icon, 15)
			}
		})
		ui.Text(c, label).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
	})
	return item
}

// TypeChip is the small protocol chip of an entity card's summary, as
// "Hysteria2" or "HTTPS", in the warning tint.
func TypeChip(c *ui.Context, text string) ui.Element {
	k := T(c)
	return ui.Row(c).Height(18).PaddingX(6).Radius(4).Background(k.WarningWeak).TextColor(k.Warning).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(FontCaption).FontWeight(500).SingleLine()
	})
}

// entityGridColumns is how many entity cards fit a row at the window's
// width: one on narrow windows, two from 640 DIPs and three from 1280.
func entityGridColumns(c *ui.Context) int {
	w, _ := c.Size()
	switch {
	case w >= 1280:
		return 3
	case w >= 640:
		return 2
	}
	return 1
}

// EntityGrid lays out n entity cards, built by card(i), in rows of
// entityGridColumns with 12 DIP gaps.
func EntityGrid(c *ui.Context, n int, card func(i int)) ui.Element {
	return ui.Grid(c).Columns(entityGridColumns(c)).Gap(12).FillWidth().Shrink(0).Children(func() {
		for i := 0; i < n; i++ {
			card(i)
		}
	})
}
