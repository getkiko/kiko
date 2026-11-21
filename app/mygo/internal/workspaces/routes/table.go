package routes

import (
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
)

// columns are the table's columns in default order, with columnIDs: 5 for
// traffic, 6 for DNS. Match Conditions takes the room the others leave; the #
// column shows no title.
func (w *Workspace) columns() []kit.Column {
	ids := columnIDs(w.sec)
	cols := []kit.Column{{Title: "序号", Width: 64}, {Title: "动作", Width: 44}, {Title: "规则名称", Width: 170},
		{Title: "匹配条件", MinWidth: 120}, {Title: "目标出口", Width: 150}}
	if w.sec == sectionDNS {
		cols[4].Title = "解析服务器"
		cols = append(cols, kit.Column{Title: "查询出口", Width: 160})
	}
	for i := range cols {
		cols[i].ID = ids[i]
	}
	return cols
}

// matchRoom is the width Match Conditions keeps, while it takes the room left,
// before the table scrolls sideways.
const matchRoom = 274

// minRowWidth is the width the columns need at their current widths, the
// table scrolling sideways below it.
func (w *Workspace) minRowWidth() float32 {
	var total float32
	widths := w.columnWidths()
	for _, col := range w.columns() {
		if width := widths.Width(col); width > 0 {
			total += width
		} else {
			total += matchRoom
		}
	}
	return total
}

// cells lays out a row's cells in the table's columns, in the order and at
// the widths the user gave them; cell is told each column's default index.
func (w *Workspace) cells(c *ui.Context, cell func(col int)) {
	w.cellBoxes(c, false, func(col, _ int, _ ui.Element) { cell(col) })
	w.orderView().Spacer(c)
}

// cellBoxes is cells with each cell's visual index and box too. The
// header's boxes (head) let the pointer through to the grip straddling
// the edge of the one before.
func (w *Workspace) cellBoxes(c *ui.Context, head bool, cell func(col, visual int, box ui.Element)) {
	cols := w.columns()
	widths := w.columnWidths()
	for v, i := range w.columnOrder().Indexes(columnIDs(w.sec)) {
		col := cols[i]
		box := ui.Row(c.Key(i)).MinWidth(0).PaddingX(kit.TableCellInset).Gap(7).FillHeight()
		if head {
			box.PassThrough()
		}
		if width := widths.Width(col); width > 0 {
			box.Width(width).Shrink(0)
		} else {
			box.Grow(1).Basis(0).MinWidth(col.MinWidth)
		}
		box.Children(func() { cell(i, v, box) })
	}
}

// actionLook is a rule action's label, symbol and tone.
func actionLook(c *ui.Context, action string, dns bool) (string, kit.Symbol, ui.Color) {
	k := kit.T(c)
	switch action {
	case model.ActionReject:
		if dns {
			return "拒绝查询", kit.SymBan, k.Danger
		}
		return "拒绝连接", kit.SymBan, k.Danger
	case model.ActionResolve:
		return "解析域名", kit.SymSearch, k.AccentText
	case model.ActionSniff:
		return "协议嗅探", kit.SymActivity, k.Label2
	case model.ActionHijackDNS:
		return "DNS 接管", kit.SymGlobe2, k.AccentText
	}
	if dns && action == model.ActionScript {
		return "脚本应答", kit.SymFileCode, k.AccentText
	}
	if dns {
		return "使用 DNS 服务器", kit.SymCircleCheck, k.Success
	}
	return "转发到出口", kit.SymCircleCheck, k.Success
}

// tableView is the policy table of the current device and section, with
// its status bar.
func (w *Workspace) tableView(c *ui.Context, doc *model.Doc) {
	k := kit.T(c)
	t := buildTable(doc, w.sec, w.owner, w.search)
	w.syncChoices()
	if w.err != "" && w.editor == nil && w.fallback == nil && w.device == nil && w.group == nil && w.settings == nil && w.copy == nil {
		ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
			kit.Notice(c, kit.DangerTone, "", w.err, nil).Grow(1)
		})
	}
	label := "路由规则"
	if w.sec == sectionDNS {
		label = "DNS 分流规则"
	}
	minRow := w.minRowWidth() + 2*kit.TableInset + w.orderView().Reserved()
	// A row picked up with the keyboard puts down as it leaves the table,
	// or as the search or the editor stop the keys moving it. Dragging
	// rows works with the editor sidebar open: the drag closes it
	// (trackPress).
	if m := w.moving; m != nil && (w.search != "" || w.editor != nil || !slices.ContainsFunc(t.rows, func(r tableRow) bool { return r.key() == m.key })) {
		w.moving = nil
	}
	if w.reveal != "" {
		if i := slices.IndexFunc(t.rows, func(r tableRow) bool {
			if w.reveal == pinnedKey {
				return r.kind == rowPinned
			}
			return (r.kind == rowRule || r.kind == rowModuleRule) && r.rule.ID == w.reveal
		}); i >= 0 {
			w.list.ScrollTo(i, ui.Center)
		}
		w.reveal = ""
	}
	w.list.Key = func(i int) any { return t.rows[i].key() }
	w.list.Label = func(i int) string {
		r := t.rows[i]
		switch r.kind {
		case rowGroup:
			return r.group.Name
		case rowRule, rowModuleRule:
			return displayName(r.rule.Name)
		case rowModuleGroup:
			return model.ModuleRuleGroupPrefix + r.moduleName
		}
		return ""
	}
	w.list.Selected = nil
	w.list.Reorder = nil
	if w.search == "" {
		w.list.Reorder = func(rows []int, to int) {
			if len(rows) != 1 {
				return
			}
			if plan, ok := t.planReorder(rows[0], to); ok {
				w.leaveEditor(func() { w.reorder(plan) })
			}
		}
	}
	ui.Column(c).Grow(1).MinHeight(0).MinWidth(0).Label(label).Children(func() {
		head := ui.Row(c).Height(kit.RowHeight).Shrink(0).Clip().BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Background(k.Surface)
		head.Children(func() {
			// The cells fill the header's height: the grips at their right
			// edges span it, and their hairlines show between headers, as in
			// kit.Table.
			ui.Row(c).MinWidth(minRow).FillWidth().FillHeight().PaddingX(kit.TableInset).FontSize(kit.FontCaption).TextColor(k.Label3).Children(func() {
				ord := w.orderView()
				cols := w.columns()
				w.cellBoxes(c, true, func(col, visual int, box ui.Element) {
					defer kit.ColumnGrip(c, w.columnWidths(), cols[col], box, w.saveColumnWidths)
					label := cols[col].Title
					h := ui.ButtonBase(c).Label("移动" + label + "列").MinWidth(24).Cursor(ui.CursorGrab).Children(func() {
						if col == 0 {
							ui.Text(c, "").Label(label)
							return
						}
						ui.Text(c, label).SingleLine().FontWeight(500)
					})
					ord.Header(c, box, h, visual, label)
				})
				ord.Reset(c)
			})
		})
		list := ui.List(c.Key(fmt.Sprint("policy-", w.sec)), &w.list, len(t.rows)).Grow(1).MinHeight(0).RowWidth(minRow).Label(label)
		head.ScrollWith(list)
		list.Rows(func(r ui.ListRow) {
			w.row(r.Context, doc, t, r.Index)
		})
		footer := fmt.Sprintf("%d 条规则 · %d 个分组", t.all, len(t.groups))
		if w.search != "" {
			footer = fmt.Sprintf("%d / %d 条规则 · %d 个分组", t.shown, t.all, len(t.groups))
		}
		kit.StatusBar(c, footer, "", nil)
	})
}

// row builds row i of the table.
func (w *Workspace) row(c *ui.Context, doc *model.Doc, t policyTable, i int) {
	k := kit.T(c)
	r := t.rows[i]
	dns := w.sec == sectionDNS
	switch r.kind {
	case rowGroup:
		b := ui.ButtonBase(c).Height(kit.RowHeight).PaddingX(kit.TableInset + kit.TableCellInset).Gap(8).FillWidth().Justify(ui.Start).
			Background(k.Surface).TextColor(k.Label2).FontSize(kit.FontTable).FontWeight(600).
			Label(r.group.Name).Cursor(ui.CursorGrab).Transition(kit.HoverMotion)
		if b.Hovered() {
			b.Background(k.Fill.Alpha(0.5))
		}
		if w.moving != nil && w.moving.key == "group:"+r.group.ID {
			b.Background(k.AccentWeak).Border(1.5, k.Accent)
		}
		b.Children(func() {
			kit.Clipped(c, r.group.Name, kit.FontTable, 600)
			kit.Badge(c, fmt.Sprint(r.count), kit.Neutral)
		})
		if b.Pressed() {
			w.pressed = ""
			w.trackPress(b, "group:"+r.group.ID)
		}
		w.keyboardMove(b, t, i, "group:"+r.group.ID)
		if b.Clicked() {
			w.err = ""
			w.group = &groupDialog{open: true, draft: *r.group}
		}
	case rowEmptyGroup:
		ui.Row(c).Height(42).FillWidth().Center().Background(k.Surface).Children(func() {
			ui.Text(c, "暂无规则，可从顶部添加规则或拖入已有规则").FontSize(kit.FontCaption).TextColor(k.Label3)
		})
	case rowEmpty:
		w.emptyRow(c, doc, t)
	case rowPinned:
		w.pinnedRow(c, doc, r)
	case rowRule:
		r.index = i
		w.ruleRow(c, doc, t, r, dns)
	case rowModuleGroup:
		w.moduleGroupRow(c, r)
	case rowModuleRule:
		w.moduleRuleRow(c, r)
	}
}

// openModule shows a module in the Rewrites workspace's module tab.
func (w *Workspace) openModule(id string) { w.env.Navigate(env.PageRewrites, "module:"+id) }

// moduleGroupRow heads a module's read-only rules: its name, the rule count
// and the way to the module.
func (w *Workspace) moduleGroupRow(c *ui.Context, r tableRow) {
	k := kit.T(c)
	ui.Row(c).Height(kit.RowHeight).PaddingX(kit.TableInset + kit.TableCellInset).Gap(8).FillWidth().Background(k.Surface).
		TextColor(k.Label2).FontSize(kit.FontTable).FontWeight(600).Label(model.ModuleRuleGroupPrefix + r.moduleName).Children(func() {
		kit.Icon(c, kit.SymLayers, 14).TextColor(k.Label3)
		kit.Clipped(c, model.ModuleRuleGroupPrefix+r.moduleName, kit.FontTable, 600).Grow(1)
		kit.Badge(c, fmt.Sprint(r.count), kit.Neutral)
		kit.IconButton(c, kit.SymArrowRight, "前往模块", kit.IconButtonOptions{Size: 24, IconSize: 13}).OnClick(func() { w.openModule(r.moduleID) })
	})
}

// moduleRuleRow is a routing rule a module brings: read-only, not draggable,
// without a switch or an editor. Its menu copies the condition, goes to
// the module or tests its exit.
func (w *Workspace) moduleRuleRow(c *ui.Context, r tableRow) {
	k := kit.T(c)
	rule := r.rule
	// A button for its menu; a click does nothing.
	row := ui.ButtonBase(c).Height(kit.RowHeight).PaddingX(kit.TableInset).FillWidth().FontSize(kit.FontTable).
		TextColor(k.Label).Justify(ui.Start).Label(rule.Name + " " + rule.Summary)
	highlighted := false
	if w.highlight == rule.ID {
		if left := time.Until(w.highlightUntil); left > 0 {
			highlighted = true
			c.After(left)
		} else {
			w.highlight = ""
		}
	}
	switch {
	case highlighted:
		row.Background(k.AccentWeak).Border(1.5, k.Accent.Alpha(0.6))
	case r.stripe:
		row.Background(k.Stripe)
	}
	row.PopoverMenu(func(m *ui.Menu) {
		if m.Item("复制").Chosen() {
			c.WriteClipboard(rule.Summary)
		}
		if m.Item("前往模块").Chosen() {
			w.openModule(r.moduleID)
		}
		w.exitTests(m, rule.targetID, true)
	})
	row.Children(func() {
		w.cells(c, func(col int) {
			switch col {
			case 0:
				_, _, tone := actionLook(c, rule.Action, false)
				w.hitLight(c, rule.ID, tone)
				ui.Text(c, fmt.Sprint(r.ordinal)).TextColor(k.Label3).FontFeatures("tnum")
			case 1:
				label, icon, tone := actionLook(c, rule.Action, false)
				kit.Hint(c, kit.Icon(c, icon, 16).TextColor(tone).Label(label), label)
			case 2:
				kit.Icon(c, kit.SymLayers, 14).TextColor(k.Label3)
				kit.Clipped(c, rule.Name, kit.FontTable, 400)
				kit.Hint(c, kit.Icon(c, kit.SymLock, 12).TextColor(k.Label3).Label("只读"), "只读")
			case 3:
				kit.Clipped(c, rule.Summary, kit.FontTable, 400)
			case 4:
				icon := kit.SymRouter
				if rule.Action == model.ActionReject {
					icon = kit.SymBan
				}
				kit.Icon(c, icon, 14).TextColor(k.Label3)
				kit.Clipped(c, rule.Target, kit.FontTable, 400)
			}
		})
	})
}

// ruleRow is a rule: a click opens its editor, a right click copies it to
// another device, and it drags to another place or device.
func (w *Workspace) ruleRow(c *ui.Context, doc *model.Doc, t policyTable, r tableRow, dns bool) {
	k := kit.T(c)
	rule := r.rule
	selected := w.editor != nil && w.editor.id() == rule.ID
	name := displayName(rule.Name)
	b := ui.ButtonBase(c).Height(kit.RowHeight).PaddingX(kit.TableInset).FillWidth().FontSize(kit.FontTable).
		TextColor(k.Label).Justify(ui.Start).Label("编辑 " + name).Transition(kit.HoverMotion)
	muted := k.Label3
	highlighted := false
	if w.highlight == rule.ID {
		if left := time.Until(w.highlightUntil); left > 0 {
			highlighted = true
			c.After(left)
		} else {
			w.highlight = ""
		}
	}
	switch {
	case w.moving != nil && w.moving.key == "rule:"+rule.ID:
		b.Background(k.AccentWeak).Border(1.5, k.Accent)
	case highlighted:
		b.Background(k.AccentWeak).Border(1.5, k.Accent.Alpha(0.6))
	case selected:
		b.Background(k.AccentWeak)
	case b.Hovered():
		b.Background(k.RowHover)
	case r.stripe:
		b.Background(k.Stripe)
	}
	if !rule.Enabled && !selected {
		b.TextColor(k.Label4)
		muted = k.Label4
	}
	b.PopoverMenu(func(m *ui.Menu) {
		if !dns && m.Item("在连接中查看").Chosen() {
			w.env.Navigate(env.PageConnections, env.ConnectionsView{Filter: map[string][]string{"rule": {ruleLabel(r.ordinal, name)}}})
		}
		if m.Item("复制到设备…").Chosen() {
			w.err = ""
			w.copy = newCopyDialog(w.doc(), copyRequest{sec: w.sec, owner: w.owner, ruleID: rule.ID})
		}
		exit := rule.targetID
		if dns {
			exit = rule.queryID
		}
		w.exitTests(m, exit, true)
	})
	b.Children(func() {
		w.cells(c, func(col int) {
			switch col {
			case 0:
				if !dns {
					_, _, tone := actionLook(c, rule.Action, dns)
					if !rule.Enabled && !selected {
						tone = k.Label4
					}
					w.hitLight(c, rule.ID, tone)
				}
				ui.Text(c, fmt.Sprint(r.ordinal)).TextColor(muted).FontFeatures("tnum")
			case 1:
				label, icon, tone := actionLook(c, rule.Action, dns)
				if !rule.Enabled && !selected {
					tone = k.Label4
				}
				kit.Hint(c, kit.Icon(c, icon, 16).TextColor(tone).Label(label), label)
			case 2:
				kit.Icon(c, kit.SymRoute, 14).TextColor(muted)
				kit.Clipped(c, name, kit.FontTable, 400)
				if rule.Invalid {
					kit.Hint(c, kit.Icon(c, kit.SymTriangleAlert, 13).TextColor(k.Warning).Label("请修正配置"), "请修正配置")
				}
			case 3:
				kit.Clipped(c, rule.Summary, kit.FontTable, 400)
			case 4:
				icon := kit.SymRouter
				if dns {
					icon = kit.SymGlobe2
				}
				kit.Icon(c, icon, 14).TextColor(muted)
				kit.Clipped(c, w.chained(doc, rule.Target, rule.targetID), kit.FontTable, 400)
			case 5:
				kit.Icon(c, kit.SymRouter, 14).TextColor(muted)
				out := w.chained(doc, rule.QueryOutbound, rule.queryID)
				if out == "" {
					out = "—"
				}
				kit.Clipped(c, out, kit.FontTable, 400)
			}
		})
	})
	if b.Pressed() {
		w.pressed, w.rowHeld = rule.ID, true
		w.trackPress(b, "rule:"+rule.ID)
	}
	w.keyboardMove(b, t, r.index, "rule:"+rule.ID)
	if b.Clicked() {
		w.err = ""
		w.fallback = nil
		w.openRule(doc, rule.ID)
	}
}

// emptyRow is the table's empty state.
func (w *Workspace) emptyRow(c *ui.Context, doc *model.Doc, t policyTable) {
	ui.Column(c).FillWidth().MinHeight(220).Children(func() {
		switch {
		case w.search != "":
			description := "尝试其他规则名称、域名或匹配条件。"
			icon := kit.SymRoute
			if w.sec == sectionDNS {
				description, icon = "尝试其他规则名称、域名或解析器。", kit.SymGlobe2
			}
			kit.EmptyState(c, icon, "没有匹配的规则", description, func() {
				kit.Button(c, "清除搜索", kit.ButtonOptions{}).OnClick(func() { w.search = "" })
			})
		case w.sec == sectionDNS:
			description := "为当前设备的域名、查询类型或规则集指定解析器与查询出口。"
			if w.owner == model.PreDeviceID {
				description = "优先为匹配的查询指定解析器，其余查询继续后续设备规则。"
			}
			kit.EmptyState(c, kit.SymGlobe2, "添加 DNS 分流规则", description, func() {
				kit.Button(c, "添加规则", kit.ButtonOptions{}).OnClick(func() { w.addDNSRule(doc) })
			})
		default:
			kit.EmptyState(c, kit.SymRoute, "添加路由分组", "将同一用途的规则放在分组中，按顺序匹配连接。", func() {
				kit.Button(c, "添加分组", kit.ButtonOptions{}).OnClick(func() { w.addGroup() })
			})
		}
	})
}

// pinnedRow is the device's default outbound (traffic) or fallback DNS
// with its query outbound (DNS), numbered after the rules; a click opens
// it in the editor sidebar.
func (w *Workspace) pinnedRow(c *ui.Context, doc *model.Doc, r tableRow) {
	k := kit.T(c)
	dns := w.sec == sectionDNS
	name, summary := "默认出口", "此设备的其余连接"
	if w.owner == model.OtherDeviceID {
		summary = "其他设备的其余连接"
	}
	if dns {
		name, summary = "兜底 DNS", "此设备的其余 DNS 查询"
	}
	selected := w.fallback != nil
	b := ui.ButtonBase(c).Height(kit.RowHeight).PaddingX(kit.TableInset).FillWidth().FontSize(kit.FontTable).
		TextColor(k.Label).Justify(ui.Start).Label("编辑 " + name).Transition(kit.HoverMotion)
	highlighted := false
	if w.highlight == pinnedKey {
		if left := time.Until(w.highlightUntil); left > 0 {
			highlighted = true
			c.After(left)
		} else {
			w.highlight = ""
		}
	}
	switch {
	case highlighted:
		b.Background(k.AccentWeak).Border(1.5, k.Accent.Alpha(0.6))
	case selected:
		b.Background(k.AccentWeak)
	case b.Hovered():
		b.Background(k.RowHover)
	}
	view := !dns && w.owner != model.PreDeviceID
	exit := w.fallbackExit(doc, dns)
	if view || w.hasExitTests(exit) {
		b.PopoverMenu(func(m *ui.Menu) {
			if view && m.Item("在连接中查看").Chosen() {
				w.env.Navigate(env.PageConnections, env.ConnectionsView{Filter: map[string][]string{"rule": {finalRuleLabel}}})
			}
			w.exitTests(m, exit, view)
		})
	}
	b.Children(func() {
		w.cells(c, func(col int) {
			switch col {
			case 0:
				if !dns {
					_, _, tone := actionLook(c, model.ActionRoute, dns)
					w.hitLight(c, finalHitKey(doc, w.owner), tone)
				}
				ui.Text(c, fmt.Sprint(r.ordinal)).TextColor(k.Label3).FontFeatures("tnum")
			case 1:
				label, icon, tone := actionLook(c, model.ActionRoute, dns)
				kit.Hint(c, kit.Icon(c, icon, 16).TextColor(tone).Label(label), label)
			case 2:
				kit.Icon(c, kit.SymCornerDownRight, 14).TextColor(k.Label3)
				kit.Clipped(c, name, kit.FontTable, 400)
			case 3:
				kit.Clipped(c, summary, kit.FontTable, 400)
			case 4:
				icon := kit.SymRouter
				if dns {
					icon = kit.SymGlobe2
				}
				kit.Icon(c, icon, 14).TextColor(k.Label3)
				kit.Clipped(c, w.fallbackTarget(doc, dns), kit.FontTable, 400)
			case 5:
				kit.Icon(c, kit.SymRouter, 14).TextColor(k.Label3)
				kit.Clipped(c, w.fallbackDetour(doc), kit.FontTable, 400)
			}
		})
	})
	if b.Pressed() {
		w.rowHeld = true
	}
	if b.Clicked() {
		w.err = ""
		w.leaveEditor(func() { w.editor, w.fallback = nil, &fallbackEditor{open: true} })
	}
}

// syncChoices takes the running core's group choices for the frame's
// outbound chains, forgetting the last frame's chains.
func (w *Workspace) syncChoices() {
	w.choices, w.chains, w.byTag = nil, map[string]string{}, nil
	if server := w.env.Server; server.Runtime.Running() {
		w.choices = map[string]string{}
		for _, s := range server.Selectors {
			w.choices[s.Name] = s.Now
		}
	}
}

// exitKey is the batch key of testing all the servers under the group
// name from a row's menu.
func exitKey(name string) string { return "routes/group:" + name }

// exitTests adds to a row's menu the speed tests of its exit, outbound
// id (nodeview.ExitTests): "Test exit" and, for a group, "Test all members",
// after a separator when separate.
func (w *Workspace) exitTests(m *ui.Menu, id string, separate bool) {
	if w.store == nil || id == "" {
		return
	}
	x := w.exits.Of(w.store, nodeview.LiveGroups(w.env))
	name := x.Name(id)
	nodeview.ExitTests(m, w.store, x, name, exitKey(name), separate, w.env.Toast)
}

// hasExitTests tells that exitTests adds an item for outbound id.
func (w *Workspace) hasExitTests(id string) bool {
	if w.store == nil || id == "" {
		return false
	}
	x := w.exits.Of(w.store, nodeview.LiveGroups(w.env))
	final, all := nodeview.ExitTargets(w.store, x, x.Name(id), true)
	return final != "" || len(all) > 0
}

// fallbackExit is the outbound the device's other connections leave
// through (traffic), or the fallback DNS's query outbound (DNS); "" for
// none set.
func (w *Workspace) fallbackExit(doc *model.Doc, dns bool) string {
	if dns {
		if detour := model.DNSFinalDetourFor(doc, w.owner); detour.Valid() {
			return detour.ID()
		}
		return ""
	}
	if final, ok := ownFinal(doc, w.owner); ok {
		return final
	}
	return model.RouteFinalFor(doc, w.owner).ID()
}

// fallbackTarget is the name of the default outbound or fallback DNS
// server the device uses, as its select in the editor shows it.
func (w *Workspace) fallbackTarget(doc *model.Doc, dns bool) string {
	var value string
	var options []kit.Option
	if dns {
		value, options = model.DNSFinalFor(doc, w.owner).ID(), dnsServerOptions(doc)
	} else {
		value = model.RouteFinalFor(doc, w.owner).ID()
		if final, ok := ownFinal(doc, w.owner); ok {
			value = final
		} else {
			options = append(options, kit.Option{Value: "", Label: "核心默认"})
		}
		options = append(options, outputOptions(doc)...)
		if chain := w.chain(doc, value); value != "" && chain != "" {
			return chain
		}
	}
	return optionLabel(withUnknown(options, value), value)
}

// chained is an outbound column's words: the chain through the groups of
// outbound id (outboundChain), else label.
func (w *Workspace) chained(doc *model.Doc, label, id string) string {
	if id != "" {
		if chain := w.chain(doc, id); chain != "" {
			return chain
		}
	}
	return label
}

// chain is outboundChain of id, worked out once a frame.
func (w *Workspace) chain(doc *model.Doc, id string) string {
	if id == "" {
		return ""
	}
	if chain, ok := w.chains[id]; ok {
		return chain
	}
	if w.byTag == nil {
		w.byTag = tagIndex(doc)
	}
	chain := outboundChain(doc, id, w.choices, w.byTag)
	if w.chains != nil {
		w.chains[id] = chain
	}
	return chain
}

// fallbackDetour is the fallback DNS's query outbound, worded as a DNS
// rule's Query Outbound column (dnsQueryOutbound): Direct for a server that
// dials, "Protocol default" for the others.
func (w *Workspace) fallbackDetour(doc *model.Doc) string {
	if detour := model.DNSFinalDetourFor(doc, w.owner); detour.Valid() {
		if tag := refTag(doc, detour.ID()); tag != "" {
			return tag
		}
		return "需修正引用"
	}
	server := model.DNSFinalFor(doc, w.owner).ID()
	if server == "" {
		if core := model.CoreDNSServer(doc); core != nil {
			server = core.ID
		}
	}
	if model.DNSTargetSupportsDetour(model.DNSServerByID(doc, server)) {
		return "直连"
	}
	return "协议默认"
}

// optionLabel is the label of value among options, else value itself.
func optionLabel(options []kit.Option, value string) string {
	for _, o := range options {
		if o.Value == value {
			return o.Label
		}
	}
	if value == "" {
		return "—"
	}
	return value
}

// fallbackEditor is the editor sidebar of the device's default outbound
// or fallback DNS: its choices save at once.
type fallbackEditor struct{ open bool }

// fallbackEditorView builds the open fallback editor.
func (w *Workspace) fallbackEditorView(c *ui.Context, doc *model.Doc) {
	f := w.fallback
	if f == nil {
		return
	}
	dns := w.sec == sectionDNS
	title := "默认出口"
	if dns {
		title = "兜底 DNS"
	}
	if !f.open {
		w.fallback = nil
		return
	}
	// The same sidebar as the rule editor's: a press on a row keeps it
	// for the row's click; its close button, Escape and Done close it.
	kit.EditorSidebarKeeping(c.Key(editorKey), &f.open, w.rowHeld, title, nil, func() {
		ui.Column(c.Key("editor-body:fallback")).Shrink(0).Dividers(1, kit.T(c).HairlineSoft).Children(func() {
			if dns {
				editorRow(c, "DNS 服务器", func() { w.fallbackDNSSelect(c, doc) })
				editorRow(c, "查询出口", func() { w.fallbackDetourSelect(c, doc) })
			} else {
				editorRow(c, "出口", func() { w.defaultOutboundSelect(c, doc) })
			}
		})
		kit.FormError(c, w.err)
	}, func() {
		ui.Spacer(c)
		kit.Button(c, "完成", kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() { f.open = false })
	})
	if !f.open && w.fallback == f {
		w.fallback = nil
	}
}

// withUnknown adds a value missing from options, marked for fixing
// (PolicySelect).
func withUnknown(options []kit.Option, value string) []kit.Option {
	if value == "" {
		return options
	}
	for _, o := range options {
		if o.Value == value {
			return options
		}
	}
	return append([]kit.Option{{Value: value, Label: value + " · 需修正引用"}}, options...)
}

// outputOptions are the outbounds and endpoints a rule may route to
// (refOptions).
func outputOptions(doc *model.Doc) []kit.Option {
	var out []kit.Option
	for _, r := range forms.DocRefOptions(doc) {
		out = append(out, kit.Option{Value: r.ID, Label: r.Tag})
	}
	return out
}

// defaultOutboundSelect chooses the device's default outbound, saving the
// choice at once.
func (w *Workspace) defaultOutboundSelect(c *ui.Context, doc *model.Doc) {
	value := model.RouteFinalFor(doc, w.owner).ID()
	var lead []kit.Option
	if final, ok := ownFinal(doc, w.owner); ok {
		value = final
	} else {
		lead = append(lead, kit.Option{Value: "", Label: "核心默认"})
	}
	if forms.OutboundSelect(c, "final", doc, &value, forms.OutboundSelectOptions{Label: "默认出口", Lead: lead}).Grow(1).Changed() {
		owner := w.owner
		w.save(func(doc *model.Doc) error {
			next, err := model.SetRouteFinal(doc, owner, model.RefOf(value))
			return replace(doc, next, err)
		})
	}
}

// dnsServerOptions are the servers a fallback may use: enabled, not
// FakeIP.
func dnsServerOptions(doc *model.Doc) []kit.Option {
	options := []kit.Option{{Value: "", Label: "核心默认"}}
	for _, s := range doc.DNS.Servers {
		if s.Enabled && s.Type != "fakeip" {
			options = append(options, kit.Option{Value: s.ID, Label: s.Tag})
		}
	}
	return options
}

// fallbackDNSSelect chooses the device's fallback DNS, saving the choice
// at once.
func (w *Workspace) fallbackDNSSelect(c *ui.Context, doc *model.Doc) {
	value := model.DNSFinalFor(doc, w.owner).ID()
	options := withUnknown(dnsServerOptions(doc), value)
	if kit.Select(c, "dns-final", &value, options, kit.SelectOptions{Label: "兜底 DNS"}).Grow(1).Changed() {
		owner := w.owner
		w.save(func(doc *model.Doc) error {
			next, err := model.SetDNSFinal(doc, owner, model.RefOf(value))
			if err != nil {
				return err
			}
			target := value
			if target == "" {
				if core := model.CoreDNSServer(next); core != nil {
					target = core.ID
				}
			}
			if !model.DNSTargetSupportsDetour(model.DNSServerByID(next, target)) {
				next, err = model.SetDNSFinalDetour(next, owner, model.Null())
			}
			return replace(doc, next, err)
		})
	}
}

// fallbackDetourSelect chooses the fallback DNS's query outbound, saving
// the choice at once.
func (w *Workspace) fallbackDetourSelect(c *ui.Context, doc *model.Doc) {
	server := model.DNSFinalFor(doc, w.owner).ID()
	if server == "" {
		if core := model.CoreDNSServer(doc); core != nil {
			server = core.ID
		}
	}
	value := model.DNSFinalDetourFor(doc, w.owner).ID()
	if detourSelect(c, "dns-final-detour", doc, server, &value, "兜底 DNS 查询出口") {
		owner := w.owner
		w.save(func(doc *model.Doc) error {
			next, err := model.SetDNSFinalDetour(doc, owner, model.RefOf(value))
			return replace(doc, next, err)
		})
	}
}

// detourSelect chooses the outbound DNS queries to server leave through
// (DnsDetourSelect): Direct for servers that dial, "Protocol default" for
// the others.
// It is a searchable popover, and reports a change of *value.
func detourSelect(c *ui.Context, key any, doc *model.Doc, server string, value *string, label string) bool {
	supports := model.DNSTargetSupportsDetour(model.DNSServerByID(doc, server))
	first := "协议默认"
	if supports {
		first = "直连（服务器拨号）"
	}
	return forms.OutboundSelect(c, key, doc, value, forms.OutboundSelectOptions{
		Label: label, Lead: []kit.Option{{Value: "", Label: first}}, NoTargets: !supports, Disabled: !supports && *value == "",
	}).Changed()
}
