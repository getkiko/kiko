package overview

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
)

// sheetMotion moves the rows of the group sheet as a group above them
// expands, and fades their hover colors.
var sheetMotion = ui.ElementTransition{Duration: 220 * time.Millisecond, Position: true, Colors: true}

// membersMotion unfolds a group's members below its row, and folds them
// away again.
var membersMotion = ui.ElementTransition{Duration: 220 * time.Millisecond, Position: true,
	Enter: &ui.Motion{Collapse: true}, Exit: &ui.Motion{Collapse: true}}

// groupSheet is the side sheet the policy group card's arrow opens
// (kit.SideSheet): a row per group, its choice under its name, and an
// arrow that unfolds its members two to a row, the current one checked;
// live, a member switches the group to it.
func (w *Workspace) groupSheet(c *ui.Context) {
	s := &w.selector
	if !s.sheet.Shown() {
		return
	}
	view := w.selectorView()
	groups, live, ids := view.groups, view.live, view.ids
	k := kit.T(c)
	kit.SideSheet(c, &s.sheet, "策略组", nil, func() {
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(0, 14, 18).Gap(12).Children(func() {
			if len(groups) == 0 {
				ui.Text(c, "没有策略组").FontSize(kit.FontBody).TextColor(k.Label3).TextAlign(ui.Center).Padding(32, 16)
				return
			}
			if s.err != "" {
				ui.Text(c, s.err).FontSize(kit.FontTable).TextColor(k.Danger).Selectable()
			}
			ui.Column(c).Gap(6).Children(func() {
				for _, group := range groups {
					w.sheetGroup(c, group, live, ids)
				}
			})
		})
	})
}

// sheetGroup is a group of the sheet: its row, and its members while
// expanded. The row shows the latency of the server the group's choice
// comes down to, and a gauge testing all the group's servers, its groups
// unfolded, shown while the row is hovered or they are tested; its
// context menu tests either (groupMenu).
func (w *Workspace) sheetGroup(c *ui.Context, group api.Selector, live bool, ids map[string]string) {
	s := &w.selector
	k := kit.T(c)
	chains := w.selectorView().chains
	open := s.expanded[group.Name]
	now := group.Now
	if now == "" {
		now = "未选择"
	}
	ui.Column(c.Key("sheet-" + group.Name)).Radius(14).Background(k.Surface2).Clip().Transition(sheetMotion).Children(func() {
		row := ui.ButtonBase(c.Key("head")).MinHeight(56).Padding(8, 12).Gap(10).Label(group.Name).
			Transition(kit.HoverMotion)
		if row.Pressed() {
			row.Background(k.Fill2)
		} else if row.Hovered() {
			row.Background(k.RowHover)
		}
		w.groupMenu(row, chains, group.Name, true)
		hovered := row.Hovered()
		row.Children(func() {
			ui.Box(c).Size(36, 36).Radius(10).Background(k.Fill).Center().Shrink(0).Children(func() {
				ui.Text(c, nodeview.GroupInitial(group.Name)).FontSize(15).FontWeight(600).TextColor(k.Label2)
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).MinWidth(0).Children(func() {
					ui.Text(c, group.Name).FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).SingleLine().
						Ellipsis("…").Shrink(1).MinWidth(0)
					ui.Text(c, groupKind(group.Type)).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
				})
				ui.Text(c, now).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().Ellipsis("…")
			})
			if id := chains.FinalID(group.Name); id != "" {
				nodeview.LatencyBadge(c, w.store.Latency[id], w.store.Testing(id))
			}
			ui.Text(c, fmt.Sprint(len(group.All))).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").Shrink(0)
			// The gauge keeps its place, so that the row does not shift
			// as the pointer comes.
			all := chains.Servers(group.Name)
			// Busy (the spinner) while the batch it started, or the
			// group's from elsewhere (groupBatch), is under way; the gauge
			// shows while the row is hovered.
			key := groupBatch(group.Name)
			busy := w.store.BatchTesting(key)
			ui.Box(c).Size(kit.SmallControl, kit.SmallControl).Center().Shrink(0).Children(func() {
				if !hovered && !busy || len(all) == 0 {
					return
				}
				if kit.IconButton(c, kit.SymGauge, "测速本组", kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 14,
					Busy: busy, BusyHint: "测速中"}).Clicked() {
					w.measureBatch(key, all)
				}
			})
			angle := float32(0)
			if open {
				angle = 90
			}
			icon := kit.Icon(c, kit.SymChevronRight, 16).TextColor(k.Label3)
			icon.Rotate(icon.Animate("turn", angle, 200*time.Millisecond))
		})
		if row.Clicked() {
			if s.expanded == nil {
				s.expanded = map[string]bool{}
			}
			s.expanded[group.Name] = !open
		}
		if open {
			ui.Grid(c.Key("members")).Columns(2).Gap(6).Padding(2, 10, 10).Transition(membersMotion).Children(func() {
				for _, name := range group.All {
					w.sheetMember(c, group, name, live)
				}
			})
		}
	})
}

// sheetMember is a member tile of an expanded group: its name and
// latency tag (nodeview.MemberBadge; a group's member, that of the
// server its choice comes down to, nodeview.ViaBadge), accented when it
// is the group's choice. Its context menu tests the server it stands for
// now, and a group member's servers (groupMenu).
func (w *Workspace) sheetMember(c *ui.Context, group api.Selector, name string, live bool) {
	chains := w.selectorView().chains
	// The menu is the holder's: a disabled tile takes no secondary click.
	holder := ui.Column(c.Key(name)).MinWidth(0)
	w.groupMenu(holder, chains, name, false)
	holder.Children(func() { w.sheetTile(c, group, name, live, chains) })
}

// sheetTile is the tile of sheetMember: a button choosing the member
// while the running core's manual group can switch to it, else a plain
// row, out of the keyboard's way, never faded.
func (w *Workspace) sheetTile(c *ui.Context, group api.Selector, name string, live bool, chains nodeview.Groups) {
	s := &w.selector
	k := kit.T(c)
	selected := name == group.Now
	choosable := live && manualGroup(group)
	enabled := choosable && !s.switching
	var tile ui.Element
	if choosable {
		tile = ui.ButtonBase(c)
	} else {
		tile = ui.Row(c).AlignItems(ui.Center)
	}
	tile.MinHeight(34).PaddingX(10).Gap(6).Radius(9).MinWidth(0).FontSize(kit.FontTable).Label(name).
		Background(k.Surface).Transition(kit.HoverMotion)
	// The choice reads in the label's color on a neutral fill, with an
	// accent check.
	tile.TextColor(k.Label)
	switch {
	case selected:
		tile.Background(k.Fill).FontWeight(600)
	case choosable && tile.Hovered() && !s.switching:
		tile.Background(k.RowHover)
	}
	tile.Children(func() {
		ui.Text(c, name).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		// Availability shows in the latency tag alone: the app's test,
		// else the core's state of the member.
		member, _ := group.Member(name)
		final, fm, ok := chains.Final(name)
		via := ""
		if ok && chains.IsGroup(name) {
			via = fm.Name
			if via == "" {
				via = final
			}
		}
		nodeview.ViaBadge(c, w.store.Latency[fm.ID], ok && w.store.Testing(fm.ID), member.State, via)
		if selected {
			kit.Icon(c, kit.SymCheck, 14).TextColor(k.Accent).Shrink(0)
		}
	})
	if enabled && !selected && tile.Clicked() {
		w.choose(group.Name, name)
	}
}

// groupKind names a group's mode, by its type in the configuration or
// its Clash type name.
func groupKind(t string) string {
	if label := model.GroupModeLabel(strings.ToLower(t)); label != "" {
		return label
	}
	return model.GroupModeLabel(model.ModeManual)
}

// manualGroup tells whether the user chooses group's member.
func manualGroup(group api.Selector) bool {
	return strings.EqualFold(group.Type, model.ModeManual)
}
