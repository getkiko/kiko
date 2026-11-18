package subscriptions

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// pickerItem is a choice of a type picker.
type pickerItem struct {
	id, label, search string
	icon              kit.Symbol
}

// pickerGroup is a titled group of a type picker.
type pickerGroup struct {
	label string
	items []pickerItem
}

// typePicker is the searchable popover of an add button
// (AnchoredActionPopover) at anchor, menu's At: a search field over
// grouped choices. It returns the id chosen this frame and closes, or "".
func typePicker(c *ui.Context, anchor ui.Element, menu *kit.MenuAnchor, placeholder string, width float32, groups []pickerGroup) string {
	k := kit.T(c)
	query := &menu.Query
	chosen := ""
	ui.PopoverBase(c, anchor, &menu.Open, func(panel ui.Element) {
		_, h := c.Size()
		kit.Popup(c, panel).Margin(6, 0, 0, 0).Width(width).MaxHeight(min(h*2/3+48, kit.PopoverRoom(c, anchor, 6))).
			Padding(6).Gap(6).Radius(16).Label(placeholder)
		panel.AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd)
		kit.Search(c, "picker-search", query, kit.SearchOptions{Placeholder: placeholder}).AutoFocus()
		q := strings.ToLower(strings.TrimSpace(*query))
		any := false
		ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Gap(1).Children(func() {
			for _, g := range groups {
				var items []pickerItem
				for _, it := range g.items {
					if q == "" || strings.Contains(strings.ToLower(g.label+" "+it.label+" "+it.search), q) {
						items = append(items, it)
					}
				}
				if len(items) == 0 {
					continue
				}
				any = true
				ui.Text(c.Key("group-"+g.label), g.label).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label3).
					Padding(6, 10, 2)
				for _, it := range items {
					if kit.MenuItem(c.Key(g.label+"/"+it.id), it.icon, it.label, false).Clicked() {
						chosen = it.id
					}
				}
			}
			if !any {
				ui.Text(c, "没有匹配项目").FontSize(kit.FontBody).TextColor(k.Label3).Padding(18, 12).TextAlign(ui.Center).FillWidth()
			}
		})
	})
	if chosen != "" {
		menu.Open = false
	}
	return chosen
}

// reorderGrid lays out the cards of ids in kit.HeroColumns columns,
// their cells stretched to the same height in each row, and lets a card
// be dragged onto another: move(id, target) then reorders them
// (SortableList). The drag carries the card's id, so a list that changed
// during the drag moves the card that was dragged. tail, when set,
// builds the grid's last cell after the cards, as an AddCard, outside
// the reordering.
func reorderGrid(c *ui.Context, key string, ids []string, move func(id, target string), card func(i int), tail func()) ui.Element {
	k := kit.T(c)
	return ui.Grid(c.Key(key)).Columns(kit.HeroColumns(c)).Gap(12).FillWidth().Shrink(0).Children(func() {
		for i, id := range ids {
			// A one-column grid lets the card fill the cell.
			cell := ui.Grid(c.Key(id)).Columns(1).MinWidth(0).Radius(kit.HeroRadius + 2).Drag(cardDrag{key, id})
			if cell.Dragging() {
				cell.Opacity(0.45)
			}
			if v, over := ui.DragOver[cardDrag](cell); over && v.grid == key && v.id != id {
				cell.Border(2, k.Accent.Alpha(0.55))
			}
			if v, ok := ui.Drop[cardDrag](cell); ok && v.grid == key && v.id != id && move != nil {
				move(v.id, id)
			}
			cell.Children(func() { card(i) })
		}
		if tail != nil {
			tail()
		}
	})
}

// cardDrag is a card dragged within a reorderGrid.
type cardDrag struct {
	grid string
	id   string
}
