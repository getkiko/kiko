package routes

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// dialogs builds the open dialogs.
func (w *Workspace) dialogs(c *ui.Context, doc *model.Doc) {
	if w.device != nil {
		w.deviceDialogView(c, doc)
	}
	if w.scan != nil {
		w.scanDialogView(c, doc)
	}
	if w.group != nil {
		w.groupDialogView(c, doc)
	}
	if w.settings != nil {
		w.settingsDialogView(c, doc)
	}
	if w.copy != nil {
		w.copyDialogView(c, doc)
	}
	if w.test != nil {
		w.testDialogView(c, doc)
	}
	if w.profile != nil {
		w.profileDialogView(c, doc)
	}
	w.profileDeleteView(c, doc)
	w.imageRemoveView(c, doc)
}

// dialogRow is a row of a dialog's form: label left, control right.
func dialogRow(c *ui.Context, label string, control func()) {
	kit.FormRow(c, label, "", control)
}

// help is a dialog's explanatory text.
func help(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(kit.FontCaption).TextColor(kit.T(c).Label3).Wrap()
}

// ---- devices ----

// lanPicker lists the LAN devices the server sees, to fill a device's
// addresses (GET /lan-devices with the DHCP lease files).
type lanPicker struct {
	open     bool
	loading  bool
	devices  []api.LANDevice
	messages []string
	query    string
}

// deviceDialog adds or edits a device, or the local scope's extra
// addresses and default.
type deviceDialog struct {
	open       bool
	isNew      bool
	draft      model.RouteDevice
	addresses  string
	validation string
	del        kit.ConfirmState
	lan        lanPicker
}

// newDeviceFinal is the default exit of a new device: the document's, else
// the first usable one.
func newDeviceFinal(doc *model.Doc) string {
	final := doc.Route.Final.ID()
	if !model.IsOutboundUsable(doc, final) {
		final = ""
		if refs := forms.DocRefOptions(doc); len(refs) > 0 {
			final = refs[0].ID
		}
	}
	return final
}

// addDevice opens the dialog adding a device.
func (w *Workspace) addDevice(doc *model.Doc) {
	final := newDeviceFinal(doc)
	w.err = ""
	w.device = &deviceDialog{open: true, isNew: true, draft: model.RouteDevice{ID: model.NewID("device"), Icon: "phone", Addresses: []string{}, Final: final}}
}

// editDevice opens a device's settings, or the local scope's.
func (w *Workspace) editDevice(doc *model.Doc, id string) {
	w.err = ""
	if model.IsLocalScope(id) {
		// The default exit is the local profile's in effect.
		draft := model.RouteDevice{ID: model.LocalDeviceID, Name: model.LocalDeviceName, Icon: "router",
			Addresses: slices.Clone(doc.Route.LocalAddresses), Final: model.RouteFinalFor(doc, model.LocalActiveScope(doc)).ID()}
		w.device = &deviceDialog{open: true, draft: draft, addresses: strings.Join(draft.Addresses, "\n")}
		return
	}
	if d := findDevice(doc, id); d != nil {
		draft := *model.Clone(d)
		draft.Final = model.ProfileFinal(d, model.ActiveScope(d))
		w.device = &deviceDialog{open: true, draft: draft, addresses: strings.Join(draft.Addresses, "\n")}
	}
}

// saveDevice validates and saves the device dialog.
func (w *Workspace) saveDevice(doc *model.Doc, d *deviceDialog) {
	device := d.draft
	device.Name = strings.TrimSpace(device.Name)
	device.Addresses = []string{}
	for _, a := range strings.Split(d.addresses, "\n") {
		if a = strings.TrimSpace(a); a != "" {
			device.Addresses = append(device.Addresses, a)
		}
	}
	local := device.ID == model.LocalDeviceID
	problem := ""
	if local {
		if slices.ContainsFunc(device.Addresses, func(a string) bool { return !model.ValidNetwork(a) }) {
			problem = "请填写有效的补充 IP / CIDR"
		}
	} else {
		others := slices.Clone(doc.Route.Devices)
		if d.isNew {
			others = append(others, &device)
		} else {
			for i, o := range others {
				if o.ID == device.ID {
					others[i] = &device
				}
			}
		}
		problem = model.ValidateDevice(&device, others)
	}
	ownExit := !local || model.LocalActiveScope(doc) != model.LocalDeviceID
	if problem == "" && (ownExit || device.Final != "") && !model.IsOutboundUsable(doc, device.Final) {
		problem = "请选择有效的默认出口"
	}
	if problem != "" {
		d.validation = problem
		return
	}
	isNew := d.isNew
	if w.save(func(doc *model.Doc) error {
		switch {
		case local:
			doc.Route.LocalAddresses = device.Addresses
			next, err := model.SetRouteFinal(doc, model.LocalActiveScope(doc), model.RefOf(device.Final))
			if err != nil {
				return err
			}
			*doc = *next
		case isNew:
			appendDevice(doc, device)
		default:
			for i, o := range doc.Route.Devices {
				if o.ID == device.ID {
					// The dialog's default exit belongs to the profile in
					// effect, which may not be the original one.
					saved := device
					saved.Profiles, saved.ActiveProfile, saved.Final = o.Profiles, o.ActiveProfile, o.Final
					saved.MAC, saved.PendingAddresses, saved.PendingFrom = o.MAC, o.PendingAddresses, o.PendingFrom
					doc.Route.Devices[i] = &saved
				}
			}
			if next, err := model.SetRouteFinal(doc, activeScope(doc, device.ID), model.RefOf(device.Final)); err == nil {
				doc.Route.Devices = next.Route.Devices
			}
		}
		return nil
	}) {
		if isNew {
			w.owner = device.ID
		}
		w.device = nil
	}
}

// deviceDialogView is the "Add device" / "Device settings" dialog.
func (w *Workspace) deviceDialogView(c *ui.Context, doc *model.Doc) {
	d := w.device
	k := kit.T(c)
	local := d.draft.ID == model.LocalDeviceID
	title := "设备设置"
	if d.isNew {
		title = "添加设备"
	}
	var deleteButton ui.Element
	kit.Dialog(c, &d.open, title, 0, func() {
		if !local {
			dialogRow(c, "名称", func() {
				kit.Input(c, "name", &d.draft.Name, "iPhone").Label("设备名称")
			})
			dialogRow(c, "设备图标", func() {
				kit.Select(c, "icon", &d.draft.Icon, []kit.Option{{Value: "phone", Label: "手机"}, {Value: "computer", Label: "电脑"},
					{Value: "server", Label: "服务器"}, {Value: "router", Label: "路由器"}}, kit.SelectOptions{Label: "设备图标"})
			})
			w.imageRow(c, d)
			if d.draft.MAC != "" {
				dialogRow(c, "MAC", func() {
					text := d.draft.MAC
					if model.IsRandomMAC(text) {
						text += "（随机）"
					}
					ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").Selectable()
				})
			}
		} else {
			dialogRow(c, "自动识别的来源 IP", func() {
				plain := *doc
				plain.Route.LocalAddresses = nil
				automatic := model.LocalSourceAddresses(&plain, w.store.LocalAddresses)
				ui.Text(c, strings.Join(automatic, ", ")).FontSize(kit.FontTable).TextColor(k.Label2).Selectable().Wrap()
				kit.Button(c, "刷新本机地址", kit.ButtonOptions{Variant: kit.TextButton, Small: true}).AlignSelf(ui.Start).
					OnClick(func() { w.store.RefreshLocalAddresses() })
			})
		}
		label := "来源 IP / CIDR"
		if local {
			label = "补充 IP / CIDR"
		}
		dialogRow(c, label, func() {
			kit.TextArea(c, "addresses", &d.addresses, "192.168.1.20\n2001:db8::20", false).MinHeight(72).Label("设备来源 IP")
			if !local {
				w.lanDevices(c, doc, d)
			}
		})
		dialogRow(c, "默认出口", func() {
			first := "请选择"
			if local && model.LocalActiveScope(doc) == model.LocalDeviceID {
				first = "核心默认"
			}
			options := withUnknown(append([]kit.Option{{Value: "", Label: first}}, outputOptions(doc)...), d.draft.Final)
			kit.Select(c, "final", &d.draft.Final, options, kit.SelectOptions{Label: "设备默认出口"})
		})
		if local {
			help(c, "自动地址包含本机网卡、回环与 TUN 地址，应用配置时刷新。补充地址随配置保存；本机范围优先于设备网段。")
		} else {
			help(c, "来源范围作为所有内部规则的必备条件，本机地址优先归入本机。独立默认出口承接此设备的其余连接。")
		}
		if !d.isNew {
			deleteButton = kit.Button(c, "删除设备及规则", kit.ButtonOptions{Variant: kit.Danger}).AlignSelf(ui.Start)
			if deleteButton.Clicked() {
				d.del.Open = true
			}
			kit.ConfirmPopover(c, deleteButton, &d.del, kit.ConfirmOptions{Title: "删除设备",
				Message: "删除「" + d.draft.Name + "」及其流量、DNS 规则和分组，后续连接与查询归入其他设备。",
				Confirm: "删除设备及规则", Danger: true, Run: func(done func(error)) {
					id := d.draft.ID
					if w.save(func(doc *model.Doc) error {
						next, err := model.RemovePolicyDevice(doc, id)
						return replace(doc, next, err)
					}) {
						done(nil)
						w.owner = model.OtherDeviceID
						if model.LocalEnabled(w.doc()) {
							w.owner = model.LocalActiveScope(w.doc())
						}
						w.device = nil
						return
					}
					done(errString(w.err))
				}})
		}
		msg := d.validation
		if msg == "" {
			msg = w.err
		}
		kit.FormError(c, msg)
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.open = false })
		kit.Button(c, "保存设备", kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() { w.saveDevice(doc, d) })
	})
	if !d.open && w.device == d {
		w.device = nil
	}
}

// lanDevices offers the devices the server sees on the LAN: choosing one
// adds its addresses, and names the device after its host name.
func (w *Workspace) lanDevices(c *ui.Context, doc *model.Doc, d *deviceDialog) {
	k := kit.T(c)
	p := &d.lan
	trigger := kit.Button(c, "从局域网设备选择", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymRouter}).AlignSelf(ui.Start)
	if trigger.Clicked() {
		p.open = !p.open
		if p.open {
			w.loadLAN(doc, p)
		}
	}
	ui.PopoverBase(c, trigger, &p.open, func(panel ui.Element) {
		kit.Popup(c, panel).Margin(4, 0, 0, 0).Width(320).Padding(8).Gap(6).MaxHeight(min(400, kit.PopoverRoom(c, trigger, 4))).Label("局域网设备")
		ui.Row(c).Gap(6).Children(func() {
			ui.Text(c, "局域网设备").FontSize(kit.FontBody).FontWeight(600).Grow(1)
			kit.IconButton(c, kit.SymRefreshCw, "刷新设备", kit.IconButtonOptions{Size: 26, IconSize: 14, Disabled: p.loading}).OnClick(func() { w.loadLAN(doc, p) })
		})
		kit.Search(c, "lan-search", &p.query, kit.SearchOptions{Placeholder: "搜索名称、IP、MAC"})
		for _, m := range p.messages {
			ui.Text(c, m).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
		if p.loading {
			ui.Text(c, "正在读取设备…").FontSize(kit.FontCaption).TextColor(k.Label3)
		}
		query := strings.ToLower(strings.TrimSpace(p.query))
		ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Gap(1).Children(func() {
			shown := 0
			for i, device := range p.devices {
				name := device.Hostname
				if name == "" {
					name = device.MAC
				}
				text := strings.ToLower(name + " " + device.MAC + " " + strings.Join(device.Addresses, " "))
				if query != "" && !strings.Contains(text, query) || len(device.Addresses) == 0 {
					continue
				}
				shown++
				row := ui.ButtonBase(c.Key(i)).MinHeight(44).PaddingX(8).Gap(8).Radius(8).FillWidth().Justify(ui.Start).Label("选择 " + name).Transition(kit.HoverMotion)
				if row.Hovered() {
					row.Background(k.Fill)
				}
				row.Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
						ui.Text(c, name).FontSize(kit.FontBody).FontWeight(500).SingleLine().Ellipsis("…")
						ui.Text(c, strings.Join(device.Addresses, "、")).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…")
						var sources []string
						for _, s := range device.Sources {
							if s == "neighbor" {
								sources = append(sources, "邻居记录")
							} else {
								sources = append(sources, "DHCP 租约")
							}
						}
						detail := strings.Join(sources, " · ")
						if device.MAC != "" {
							detail = device.MAC + " · " + detail
						}
						ui.Text(c, detail).FontSize(10).TextColor(k.Label3).SingleLine().Ellipsis("…")
					})
					kit.Icon(c, kit.SymPlus, 14).TextColor(k.Label3)
				})
				if row.Clicked() {
					lines := strings.Split(strings.TrimSpace(d.addresses), "\n")
					if strings.TrimSpace(d.addresses) == "" {
						lines = nil
					}
					for _, a := range device.Addresses {
						if !slices.Contains(lines, a) {
							lines = append(lines, a)
						}
					}
					d.addresses = strings.Join(lines, "\n")
					if strings.TrimSpace(d.draft.Name) == "" && device.Hostname != "" {
						d.draft.Name = device.Hostname
					}
					p.open = false
				}
			}
			if !p.loading && shown == 0 {
				text := "当前设备列表为空，可手动填写来源 IP。"
				if query != "" {
					text = "当前搜索结果为空，可手动填写来源 IP。"
				}
				ui.Text(c, text).FontSize(kit.FontCaption).TextColor(k.Label3).Padding(6)
			}
		})
	})
}

// loadLAN reads the LAN devices, when the server can discover them.
func (w *Workspace) loadLAN(doc *model.Doc, p *lanPicker) {
	p.loading, p.messages = true, nil
	files := slices.Clone(doc.Route.DHCPLeaseFiles)
	type result struct {
		supported bool
		devices   []api.LANDevice
		warnings  []string
	}
	env.Go(w.env, func(ctx context.Context) (result, error) {
		info, err := w.env.API.Info(ctx)
		if err != nil {
			return result{}, err
		}
		if !info.Capabilities["lanDevices"] {
			return result{}, nil
		}
		devices, warnings, err := w.env.API.LANDevices(ctx, files)
		return result{supported: true, devices: devices, warnings: warnings}, err
	}, func(r result, err error) {
		p.loading = false
		switch {
		case err != nil:
			p.messages = []string{"读取设备失败：" + err.Error()}
		case !r.supported:
			p.messages = []string{"当前 server 支持手动输入设备标识；自动发现适用于 Linux server。"}
		default:
			p.devices, p.messages = r.devices, r.warnings
		}
	})
}

// ---- groups ----

// groupDialog creates or renames a group, or deletes it.
type groupDialog struct {
	open       bool
	isNew      bool
	draft      model.RouteGroup
	validation string
}

// groupDialogView is the "Create group" / "Group settings" dialog.
func (w *Workspace) groupDialogView(c *ui.Context, doc *model.Doc) {
	g := w.group
	sec := w.sec
	var groups []*model.RouteGroup
	if sec == sectionDNS {
		groups = model.DNSGroupsOf(doc, w.owner)
	} else {
		groups = model.RouteGroupsOf(doc, w.owner)
	}
	title := "分组设置"
	if g.isNew {
		title = "创建分组"
	}
	policyGroups := func(doc *model.Doc) *[]*model.RouteGroup {
		if sec == sectionDNS {
			return &doc.DNS.Groups
		}
		return &doc.Route.Groups
	}
	kit.Dialog(c, &g.open, title, 0, func() {
		dialogRow(c, "分组名称", func() {
			kit.Input(c, "name", &g.draft.Name, "").Label("分组名称").AutoFocus()
		})
		if !g.isNew && len(groups) > 1 {
			var replacement *model.RouteGroup
			for _, other := range groups {
				if other.ID != g.draft.ID {
					replacement = other
					break
				}
			}
			kit.Button(c, "删除分组，规则移至「"+replacement.Name+"」", kit.ButtonOptions{Variant: kit.Danger}).AlignSelf(ui.Start).OnClick(func() {
				id, to := g.draft.ID, replacement.ID
				if w.save(func(doc *model.Doc) error {
					list := policyGroups(doc)
					*list = slices.DeleteFunc(slices.Clone(*list), func(x *model.RouteGroup) bool { return x.ID == id })
					if sec == sectionDNS {
						for _, r := range doc.DNS.Rules {
							if r.GroupID == id {
								r.GroupID = to
							}
						}
					} else {
						for _, r := range doc.Route.Rules {
							if r.GroupID == id {
								r.GroupID = to
							}
						}
					}
					return nil
				}) {
					w.group = nil
				}
			})
		}
		msg := g.validation
		if msg == "" {
			msg = w.err
		}
		kit.FormError(c, msg)
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { g.open = false })
		kit.Button(c, "保存分组", kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() {
			name := strings.TrimSpace(g.draft.Name)
			if name == "" {
				g.validation = "请填写分组名称"
				return
			}
			draft := g.draft
			draft.Name = name
			isNew := g.isNew
			if w.save(func(doc *model.Doc) error {
				list := policyGroups(doc)
				if isNew {
					*list = append(*list, &draft)
					return nil
				}
				for i, x := range *list {
					if x.ID == draft.ID {
						saved := draft
						(*list)[i] = &saved
					}
				}
				return nil
			}) {
				w.group = nil
			}
		})
	})
	if w.group == g && !g.open {
		w.group = nil
	}
}

// ---- route settings ----

// settingsDialog edits the route section's settings.
type settingsDialog struct {
	open          bool
	route         model.Route
	leaseFiles    string
	advancedShown bool
}

func newSettingsDialog(doc *model.Doc) *settingsDialog {
	route := model.Clone(doc.Route)
	return &settingsDialog{open: true, route: route, leaseFiles: strings.Join(route.DHCPLeaseFiles, "\n")}
}

// settingsDialogView is the "Routing settings" dialog.
func (w *Workspace) settingsDialogView(c *ui.Context, doc *model.Doc) {
	s := w.settings
	kit.Dialog(c, &s.open, "路由设置", 0, func() {
		dialogRow(c, "自动检测网络接口", func() {
			ui.Row(c).Children(func() { kit.Switch(c, &s.route.AutoDetectInterface, "自动检测网络接口") })
		})
		if !s.route.AutoDetectInterface {
			dialogRow(c, "默认网络接口", func() {
				kit.Input(c, "interface", &s.route.DefaultInterface, "en0").Label("默认网络接口")
			})
		}
		dialogRow(c, "默认域名解析器", func() {
			value := s.route.DefaultDomainResolver.ID()
			options := withUnknown(dnsServerOptions(doc), value)
			if kit.Select(c, "resolver", &value, options, kit.SelectOptions{Label: "默认域名解析器"}).Changed() {
				s.route.DefaultDomainResolver = model.RefOf(value)
				if !model.DNSTargetSupportsDetour(model.DNSServerByID(doc, value)) {
					s.route.DefaultDomainResolverDetour = model.Null()
				}
			}
		})
		if s.route.DefaultDomainResolver.Valid() {
			dialogRow(c, "解析器查询出口", func() {
				value := s.route.DefaultDomainResolverDetour.ID()
				if detourSelect(c, "resolver-detour", doc, s.route.DefaultDomainResolver.ID(), &value, "默认域名解析器查询出口") {
					s.route.DefaultDomainResolverDetour = model.RefOf(value)
				}
			})
		}
		dialogRow(c, "查找连接进程", func() {
			on := true
			kit.Checkbox(c, &on, "始终开启").Disabled(true).Opacity(0.6)
		})
		kit.Disclosure(c, "高级选项", &s.advancedShown, func() {
			dialogRow(c, "DHCP 租约文件", func() {
				kit.TextArea(c, "lease", &s.leaseFiles, "", false).MinHeight(60).Label("DHCP 租约文件")
			})
		})
		kit.FormError(c, w.err)
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { s.open = false })
		kit.Button(c, "保存设置", kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() {
			r := s.route
			var files []string
			for _, f := range strings.Split(s.leaseFiles, "\n") {
				if f != "" {
					files = append(files, f)
				}
			}
			if w.save(func(doc *model.Doc) error {
				on := true
				doc.Route.AutoDetectInterface = r.AutoDetectInterface
				doc.Route.FindProcess = &on
				doc.Route.DefaultInterface = r.DefaultInterface
				doc.Route.DefaultDomainResolver = r.DefaultDomainResolver
				doc.Route.DefaultDomainResolverDetour = r.DefaultDomainResolverDetour
				doc.Route.DHCPLeaseFiles = files
				return nil
			}) {
				w.settings = nil
			}
		})
	})
	if w.settings == s && !s.open {
		w.settings = nil
	}
}

// OpenRuleSets shows the list of Settings › Rule Sets, built by RuleSetsPage.
func (w *Workspace) OpenRuleSets() { w.ruleSets = w.newRuleSetPage() }

// CloseRuleSets drops the Rule Sets pages' state, as the Settings workspace stops
// showing them, releasing the store's edit an unsaved rule set held.
func (w *Workspace) CloseRuleSets() {
	if w.ruleSets == nil {
		return
	}
	w.ruleSets = nil
	w.syncEditing()
}

// activeScope is the scope in effect of the device.
func activeScope(doc *model.Doc, deviceID string) string {
	if d := findDevice(doc, deviceID); d != nil {
		return model.ActiveScope(d)
	}
	return deviceID
}
