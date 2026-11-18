package routes

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// deviceIcon is a device's symbol by its icon name.
func deviceIcon(icon string) kit.Symbol {
	switch icon {
	case "phone":
		return kit.SymSmartphone
	case "computer":
		return kit.SymLaptop
	case "server":
		return kit.SymServer
	}
	return kit.SymRouter
}

// sidebarView is the solid device sidebar: Pre-rules, Local / Gateway, the LAN
// devices and Other Devices, each a target for rules dragged onto it.
func (w *Workspace) sidebarView(c *ui.Context, doc *model.Doc, narrow bool, width float32) {
	k := kit.T(c)
	ui.Column(c).Width(width).Shrink(0).Background(k.Surface2).BorderWidth(0, 1, 0, 0).
		BorderColor(k.HairlineSoft).Label("设备").Children(func() {
		ui.Row(c).Height(kit.ToolbarHeight).Padding(0, 10, 0, 16).Gap(2).Shrink(0).DragWindow().Children(func() {
			ui.Text(c, "设备").FontSize(kit.FontCaption).FontWeight(700).TextColor(k.Label3).Grow(1)
			if w.canScan() {
				kit.IconButton(c, kit.SymSearch, "扫描局域网", kit.IconButtonOptions{Size: 26, IconSize: 15}).OnClick(func() { w.startScan() })
			}
			kit.IconButton(c, kit.SymPlus, "添加设备", kit.IconButtonOptions{Size: 26, IconSize: 15}).OnClick(func() { w.addDevice(doc) })
			if model.IsLocalScope(w.owner) || findDevice(doc, w.owner) != nil {
				kit.IconButton(c, kit.SymSettings, "设备设置", kit.IconButtonOptions{Size: 26, IconSize: 15}).OnClick(func() { w.editDevice(doc, w.owner) })
			}
			if narrow {
				kit.IconButton(c, kit.SymX, "关闭设备分栏", kit.IconButtonOptions{Size: 26, IconSize: 14}).OnClick(func() { w.sidebar = false })
			}
		})
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(0, 10, 16).Gap(2).Children(func() {
			w.deviceItem(c, doc, model.PreDeviceID, "前置规则", kit.SymRoute, nil, narrow)
			if model.LocalEnabled(doc) {
				local := model.LocalDevice(doc)
				w.deviceItem(c, doc, model.ActiveScope(local), local.Name, kit.SymRouter, local, narrow)
			}
			for _, d := range doc.Route.Devices {
				w.deviceItem(c, doc, model.ActiveScope(d), d.Name, deviceIcon(d.Icon), d, narrow)
			}
			w.deviceItem(c, doc, model.OtherDeviceID, "其他设备", kit.SymGlobe2, nil, narrow)
			if !model.LocalEnabled(doc) {
				ui.Column(c).Margin(12, 0, 0).Padding(8, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
					kit.Button(c, "添加本机 / 网关", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).OnClick(func() {
						if w.save(func(doc *model.Doc) error { *doc = *model.AddLocalPolicyDevice(doc); return nil }) {
							w.owner, w.search, w.notice = model.LocalDeviceID, "", ""
						}
					})
				})
			}
		})
	})
}

// profilesShown reports whether the sidebar lists the profiles of dev, a
// device of several profiles: while the device is selected, and while a
// switch to one of them asks for confirmation, which shows
// beside that profile's row.
func (w *Workspace) profilesShown(dev *model.RouteDevice, selected bool) bool {
	_, asking := w.pendingProfileSwitch(dev.ID)
	return selected || asking
}

// deviceItem is a row of the sidebar: its icon and name, for a LAN device
// its IP changed marker, and for an owner of profiles its More button. scope
// is the scope it shows (the profile in effect); dev is the owner of the
// profiles (a LAN device, or model.LocalDevice), nil for Pre-rules and Other Devices.
// A device of several profiles shows the one in effect after
// its name; selected, it lists them below it instead (profileItem). A rule
// of another scope dropped on it moves there.
func (w *Workspace) deviceItem(c *ui.Context, doc *model.Doc, scope, name string, icon kit.Symbol, dev *model.RouteDevice, narrow bool) {
	k := kit.T(c)
	selected := w.owner == scope || (dev != nil && slices.Contains(model.DeviceScopes(dev), w.owner))
	id := scope
	if dev != nil {
		id = dev.ID
	}
	multi := dev != nil && len(model.DeviceProfiles(dev)) > 1
	listed := multi && w.profilesShown(dev, selected)
	accepts := scope != w.owner && w.pressed != "" && w.search == ""
	box := ui.Column(c.Key("device:" + id)).Shrink(0)
	over := false
	if accepts {
		if v, ok := ui.DragOver[any](box); ok && w.draggedRule(doc, v) != "" {
			over = true
			box.Background(k.AccentWeak).Radius(7)
		}
		if v, ok := ui.Drop[any](box); ok {
			if rule := w.draggedRule(doc, v); rule != "" {
				w.leaveEditor(func() { w.moveToDevice(doc, rule, scope) })
			}
			w.pressed = ""
		}
	}
	menu := func(m *ui.Menu) { w.deviceMenu(c, m, doc, scope, dev, "device:"+id) }
	box.Children(func() {
		row := ui.Row(c).FillWidth().Radius(7).Transition(kit.HoverMotion)
		switch {
		case selected:
			row.Background(k.AccentWeak)
		case row.Hovered():
			row.Background(k.Fill)
		}
		row.Children(func() {
			item := ui.ButtonBase(c).MinHeight(36).PaddingX(8).Gap(8).FillWidth().Grow(1).MinWidth(0).Justify(ui.Start).
				FontSize(kit.FontTable).TextColor(k.Label).Label(name).Transition(kit.HoverMotion)
			weight := 400
			if selected {
				weight = 600
				item.TextColor(k.AccentText).FontWeight(600)
			}
			// "Copy to…" copies this device's rules and groups of the section to
			// another device, through the copy dialog.
			item.PopoverMenu(menu)
			item.Children(func() {
				w.deviceAvatar(c, dev, icon)
				if multi && !listed {
					kit.Clipped(c, name, kit.FontTable, weight).Shrink(1)
					kit.Clipped(c, model.ActiveProfileOf(dev).Name, kit.FontCaption, 400).TextColor(k.Label3).Grow(1)
				} else {
					kit.Clipped(c, name, kit.FontTable, weight).Grow(1)
				}
			})
			// In the narrow overlay a device of several profiles keeps the
			// panel open: its profiles show in place, and a confirmed switch
			// closes it.
			if item.Clicked() {
				w.selectDevice(scope, narrow && !multi)
			}
			if item.DoubleClicked() && (model.IsLocalScope(id) || findDevice(doc, id) != nil) {
				w.editDevice(doc, id)
			}
			if dev != nil && len(dev.PendingAddresses) > 0 {
				kit.IconButton(c.Key("pending:"+id), kit.SymTriangleAlert, pendingText(dev), kit.IconButtonOptions{Size: 22, IconSize: 14}).
					TextColor(k.Warning).PopoverMenuButton(func(m *ui.Menu) { w.pendingMenu(m, dev) })
			}
			if dev != nil {
				more := kit.IconButton(c.Key("more:"+id), kit.SymEllipsis, "更多："+name, kit.IconButtonOptions{Size: 22, IconSize: 14})
				more.PopoverMenuButton(menu)
				w.noteDeleteAnchor("device:"+id, more)
			}
		})
		if over {
			ui.Text(c, "移动到 "+name).FontSize(kit.FontCaption).TextColor(k.AccentText).Padding(0, 8, 4, sidebarTextInset)
		}
	})
	if listed {
		for _, p := range model.DeviceProfiles(dev) {
			w.profileItem(c, doc, dev, p)
		}
	}
}

// sidebarTextInset is where the names of the sidebar rows start: the row's
// padding, the avatar square and the gap after it.
const sidebarTextInset = 8 + avatarSize + 8

// menuProfile is the profile the device menu manages: the one shown when
// the device is, else the one in effect.
func (w *Workspace) menuProfile(dev *model.RouteDevice) model.DeviceProfile {
	for _, p := range model.DeviceProfiles(dev) {
		if p.Scope == w.owner {
			return p
		}
	}
	return model.ActiveProfileOf(dev)
}

// deviceMenu is the menu of a device row: copying its rules to another
// scope, for a LAN device its picture, and for an owner of profiles (a LAN
// device or Local / Gateway) managing them.
func (w *Workspace) deviceMenu(c *ui.Context, m *ui.Menu, doc *model.Doc, scope string, dev *model.RouteDevice, from string) {
	// A LAN device's menu copies from the profile it manages below.
	if dev != nil {
		scope = w.menuProfile(dev).Scope
	}
	m.Submenu("复制到…", func(sub *ui.Menu) {
		for _, target := range model.AllPolicyScopes(doc) {
			if target == scope {
				continue
			}
			if sub.Item(model.ScopeName(doc, target)).Chosen() {
				w.err = ""
				w.copy = newCopyDialog(doc, copyRequest{sec: w.sec, owner: target})
				w.copy.selected = scope
			}
		}
	})
	if dev != nil && dev.ID != model.LocalDeviceID {
		m.Separator()
		if dev.Image == "" {
			if m.Item("选择图片…").Chosen() {
				w.chooseImage(dev.ID)
			}
		} else {
			if m.Item("更换图片…").Chosen() {
				w.chooseImage(dev.ID)
			}
			if m.Item("移除图片").Chosen() {
				w.requestRemoveImage(from, dev.ID)
			}
		}
		if len(dev.PendingAddresses) > 0 {
			m.Separator()
			w.pendingMenu(m, dev)
		}
	}
	if dev != nil {
		m.Separator()
		w.profileMenu(c, m, dev, w.menuProfile(dev), from)
	}
}

// profileItem is a profile of a device of several profiles, below it: a
// check on the one in effect and its name, which lines up with the
// device's. A click shows the profile; a double click, Cmd+Enter or the
// menu's "Switch to this profile" asks to put it in effect. A rule dropped on it moves
// into the profile.
func (w *Workspace) profileItem(c *ui.Context, doc *model.Doc, dev *model.RouteDevice, p model.DeviceProfile) {
	k := kit.T(c)
	active := model.ActiveProfileOf(dev).ID == p.ID
	viewing := w.owner == p.Scope
	key := "profile:" + dev.ID + "/" + p.ID
	box := ui.Column(c.Key(key)).Shrink(0)
	over := false
	if p.Scope != w.owner && w.pressed != "" && w.search == "" {
		if v, ok := ui.DragOver[any](box); ok && w.draggedRule(doc, v) != "" {
			over = true
			box.Background(k.AccentWeak).Radius(7)
		}
		if v, ok := ui.Drop[any](box); ok {
			if rule := w.draggedRule(doc, v); rule != "" {
				w.leaveEditor(func() { w.moveToDevice(doc, rule, p.Scope) })
			}
			w.pressed = ""
		}
	}
	box.Children(func() {
		item := ui.ButtonBase(c).MinHeight(30).PaddingX(8).Gap(8).Radius(7).FillWidth().Justify(ui.Start).
			FontSize(kit.FontTable).TextColor(k.Label2).Label(p.Name).Role(ui.RoleRadio).Selected(active).Transition(kit.HoverMotion)
		// Selected (above) reports the profile in effect to assistive
		// technologies; the colors here replace its accent look: a neutral
		// fill marks the profile shown, the check and the weight the one in
		// effect.
		bg, fg, weight := ui.Color{}, k.Label2, 400
		if viewing || item.Hovered() {
			bg = k.Fill
		}
		if active || viewing {
			fg, weight = k.Label, 600
		}
		item.Background(bg).TextColor(fg).FontWeight(weight)
		item.PopoverMenu(func(m *ui.Menu) { w.profileRowMenu(c, m, doc, dev, p) })
		item.Children(func() {
			ui.Box(c).Size(avatarSize, 14).Shrink(0).Center().Children(func() {
				if active {
					kit.Icon(c, kit.SymCheck, 12).TextColor(k.AccentText)
				}
			})
			kit.Clipped(c, p.Name, kit.FontTable, weight).Grow(1)
		})
		// A click shows the profile without putting it in effect; showing it
		// again does nothing, so the second click of a double click keeps
		// the confirmation the double click opens, or the switch waiting on
		// a dirty draft (whose question a press outside the editor turns to
		// closing it). The narrow overlay stays open.
		if item.Clicked() {
			switch {
			case w.switchWaits(dev.ID, p.ID):
				// The press of the click asked to close the editor instead:
				// the switch waits again.
				w.requestProfileSwitch(dev.ID, p.ID)
			case w.owner != p.Scope:
				w.selectDevice(p.Scope, false)
			}
		}
		if (item.DoubleClicked() || item.Shortcut(ui.Cmd, ui.KeyEnter)) && !active {
			w.requestProfileSwitch(dev.ID, p.ID)
		}
		if target, open := w.pendingProfileSwitch(dev.ID); open && target == p.ID {
			w.profileSwitchPopover(c, item, dev.ID, p.ID)
		}
		if over {
			ui.Text(c, "移动到 "+model.ScopeName(doc, p.Scope)).FontSize(kit.FontCaption).TextColor(k.AccentText).Padding(0, 8, 4, sidebarTextInset)
		}
	})
}

// profileRowMenu is the context menu of a profile row: switching to it
// when it is not in effect, copying its rules and managing it. Its delete
// confirmation shows at the device's More button.
func (w *Workspace) profileRowMenu(c *ui.Context, m *ui.Menu, doc *model.Doc, dev *model.RouteDevice, p model.DeviceProfile) {
	if model.ActiveProfileOf(dev).ID != p.ID {
		if m.Item("切换到此配置").Chosen() {
			w.requestProfileSwitch(dev.ID, p.ID)
		}
		m.Separator()
	}
	m.Submenu("复制到…", func(sub *ui.Menu) {
		for _, target := range model.AllPolicyScopes(doc) {
			if target == p.Scope {
				continue
			}
			if sub.Item(model.ScopeName(doc, target)).Chosen() {
				w.err = ""
				w.copy = newCopyDialog(doc, copyRequest{sec: w.sec, owner: target})
				w.copy.selected = p.Scope
			}
		}
	})
	m.Separator()
	w.profileMenu(c, m, dev, p, "device:"+dev.ID)
}

// draggedRule returns the rule a drag of the policy list carries: the one
// rule row it drags, which must be the rule row pressed last; "" for any
// other drag (a group, the default row, rows of another list).
func (w *Workspace) draggedRule(doc *model.Doc, value any) string {
	rows, ok := listDragRows(value, &w.list)
	if !ok || len(rows) != 1 || w.pressed == "" {
		return ""
	}
	t := buildTable(doc, w.sec, w.owner, w.search)
	if rows[0] < 0 || rows[0] >= len(t.rows) {
		return ""
	}
	if r := t.rows[rows[0]]; r.kind == rowRule && r.rule.ID == w.pressed {
		return r.rule.ID
	}
	return ""
}

// listDragRows reads the rows of a drag of list's rows (ListState.Reorder),
// whose value package ui keeps unexported: a pointer to a struct with the
// list's state s and the rows dragged.
func listDragRows(value any, list *ui.ListState) ([]int, bool) {
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	v = v.Elem()
	s, rows := v.FieldByName("s"), v.FieldByName("rows")
	if !s.IsValid() || s.Kind() != reflect.Pointer || s.Pointer() != reflect.ValueOf(list).Pointer() ||
		!rows.IsValid() || rows.Kind() != reflect.Slice || rows.Type().Elem().Kind() != reflect.Int {
		return nil, false
	}
	out := make([]int, rows.Len())
	for i := range out {
		out[i] = int(rows.Index(i).Int())
	}
	return out, true
}

// sidebarOverlay shows the sidebar over the window's left edge in narrow
// windows.
func (w *Workspace) sidebarOverlay(c *ui.Context, doc *model.Doc) {
	k := kit.T(c)
	var panel ui.Element
	ui.Overlay(c, func() {
		panel = ui.Row(c).Absolute().Top(0).Bottom(0).Left(0).AlignItems(ui.Stretch).
			Shadow(0, 12, 40, 0, k.Shadow.Alpha(0.25)).Children(func() { w.sidebarView(c, doc, true, kit.FilterWidth) })
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			w.sidebar = false
		}
	})
	if panel.PressedOutside() {
		w.sidebar = false
	}
}

// moveToDevice moves a rule of the current device to another, keeping its
// id and conditions (moveDevicePolicy).
func (w *Workspace) moveToDevice(doc *model.Doc, ruleID, target string) {
	if target == w.owner || ruleID == "" {
		return
	}
	name := ""
	if w.sec == sectionDNS {
		for _, r := range doc.DNS.Rules {
			if r.ID == ruleID {
				name = r.Name
			}
		}
	} else {
		for _, r := range doc.Route.Rules {
			if r.ID == ruleID {
				name = r.Name
			}
		}
	}
	w.notice = ""
	options := model.MovePolicyOptions{Section: w.sec.policy(), Source: w.owner, Target: target, RuleID: ruleID}
	if w.save(func(doc *model.Doc) error {
		next, err := model.MoveDevicePolicy(doc, options)
		return replace(doc, next, err)
	}) {
		w.notice = fmt.Sprintf("已将“%s”移动到 %s", displayName(name), model.PolicyOwnerName(w.doc(), target))
	}
	w.pressed = ""
}
