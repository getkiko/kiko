package subscriptions

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

var inboundAddLabels = map[string]string{"tun": "TUN", "mixed": "Mixed", "burrow": "Burrow"}

func inboundAddLabel(t string) string {
	if l := inboundAddLabels[t]; l != "" {
		return l
	}
	return typeLabel(t)
}

// inboundSummary is an inbound card's address: "Virtual NIC" for TUN, the
// signal address of burrow, listen:port otherwise.
func inboundSummary(in *model.Inbound) string {
	switch in.Type {
	case "tun":
		return "虚拟网卡"
	case "burrow":
		return jsv.StringOr(in.Fields["signal_address"], "")
	}
	listen, port := inboundListen(in)
	host := listen
	if strings.Contains(listen, ":") && !strings.HasPrefix(listen, "[") {
		host = "[" + listen + "]"
	}
	if port != "" {
		return host + ":" + port
	}
	return host
}

// inboundListen is a listening inbound's listen address, 127.0.0.1 when
// unset, and its port, "" when unset.
func inboundListen(in *model.Inbound) (listen, port string) {
	listen = "127.0.0.1"
	if jsv.Truthy(in.Fields["listen"]) {
		listen = jsv.String(in.Fields["listen"])
	}
	return listen, jsv.StringOr(in.Fields["listen_port"], "")
}

// inboundConfirm is a pending "Restore defaults" (reset) or Delete of an inbound, by
// its editor or its card.
type inboundConfirm struct {
	reset bool
	id    string
	tag   string
}

// inboundsSection is the Inbounds section.
type inboundsSection struct {
	w      *Workspace
	editor inEditor
	// add is the type picker of "Add inbound", at the heading's button or the
	// grid's ＋ card.
	add kit.MenuAnchor
	// confirm is a card's "Restore defaults" (action "reset") or Delete.
	confirm cardConfirm
}

func (s *inboundsSection) init(w *Workspace) {
	s.w = w
	s.editor.w = w
}

// heading is the section's heading and its count; the grid's ＋ card
// opens the type picker.
func (s *inboundsSection) heading(c *ui.Context, doc *model.Doc) {
	kit.SectionHeading(c, "入站", fmt.Sprintf("%d 个", len(doc.Inbounds)), nil)
}

// grid is the cards of inbounds, reordered by dragging, then "+ Add inbound",
// opening the type picker.
func (s *inboundsSection) grid(c *ui.Context, doc *model.Doc, inbounds []*model.Inbound) {
	ids := make([]string, len(inbounds))
	for i, in := range inbounds {
		ids[i] = in.ID
	}
	reorderGrid(c, "inbounds", ids, s.w.reorderInbound, func(i int) { s.card(c, doc, inbounds[i]) }, func() {
		s.add.Card("card", kit.AddCard(c.Key("add-inbound"), "添加入站"))
	})
}

// overlays are the type picker of "Add inbound" and a card's confirmation, at
// their buttons when the list built them in this pass, and the editor.
func (s *inboundsSection) overlays(c *ui.Context, doc *model.Doc) {
	if anchor, _, ok := s.add.At(); ok {
		var items []pickerItem
		for _, t := range model.InboundTypes {
			items = append(items, pickerItem{id: t, label: inboundAddLabel(t), search: t + " " + typeLabel(t), icon: kit.SymServer})
		}
		if t := typePicker(c, anchor, &s.add, "搜索入站类型", 240, []pickerGroup{{label: "入站类型", items: items}}); t != "" {
			s.editor.openAdd(doc, t)
		}
	}
	if at, ok := s.confirm.anchor(); ok {
		p := inboundConfirm{reset: s.confirm.action == "reset", id: s.confirm.id, tag: s.confirm.title}
		inboundConfirmPopover(c, at, &s.confirm.state, p, func(done func(error)) {
			done(s.w.store.Update(func(d *model.Doc) {
				if p.reset {
					resetInboundFields(d, p.id)
				} else {
					removeInbound(d, p.id)
				}
			}))
		})
	}
	s.editor.view(c)
}

// card is an inbound's hero card: its state, name and type over its
// port (a TUN's IPv4 address, burrow's signal address), its listen
// address or a TUN's routing switches and its active connections; it
// opens the editor, its menu restores the defaults or deletes after a
// confirmation at the more button.
func (s *inboundsSection) card(c *ui.Context, doc *model.Doc, in *model.Inbound) {
	id := in.ID
	invalid := model.HasInvalidInboundReferences(doc, in, s.w.store.NetworkConfigs())
	title := in.Tag
	if title == "" {
		title = typeLabel(in.Type)
	}
	main, note, mono := inboundFacts(in)
	more := kit.HeroCard(c, id, kit.HeroCardOptions{
		Label: "编辑 " + title,
		Muted: !in.Enabled,
		Header: func() {
			heroHeader(c, func() { kit.HeroDot(c, in.Enabled) }, title, invalid, strings.ToUpper(in.Type))
		},
		Main: func() {
			if mono {
				kit.HeroMono(c, main)
			} else {
				kit.HeroValue(c, main)
			}
		},
		Footer: func() {
			t := ui.Text(c, note).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			if in.Type != "tun" && in.Type != "burrow" {
				t.Font(kit.MonoFont)
			}
			ui.Spacer(c)
			conns := "—"
			if n, ok := s.w.conns.count(s.w.infoOf(doc).inboundTag(id, in.Tag)); ok {
				conns = fmt.Sprintf("%d 个连接", n)
			}
			ui.Text(c, conns).FontFeatures("tnum").SingleLine().Shrink(0)
		},
		Enabled: in.Enabled,
		OnEnabledChange: func(enabled bool) {
			s.w.update(func(d *model.Doc) {
				for _, x := range d.Inbounds {
					if x.ID == id {
						x.Enabled = enabled
					}
				}
			})
		},
		Menu: []kit.EntityMenuItem{
			{Label: "恢复默认", Icon: kit.SymRotateCcw, Run: func(ui.Element) { s.confirm.start(id, "reset") }},
			{Label: "删除", Icon: kit.SymTrash2, Danger: true, Run: func(ui.Element) { s.confirm.start(id, "delete") }},
		},
		KeepMore: s.confirm.showing(id),
		OnOpen:   func() { s.editor.openEdit(s.w.store.Doc(), id) },
	})
	s.confirm.track(id, in.Tag, more)
}

// inboundFacts are what an inbound's card shows: its main value, the
// port as ":7890", a TUN's IPv4 address or burrow's signal address
// (mono, as addresses); and the bottom line's note, the listen address,
// a TUN's prefix and routing switches ("/30 · Auto route · Strict route",
// "Manual route" without auto_route) or "Hole-punch signaling".
func inboundFacts(in *model.Inbound) (main, note string, mono bool) {
	switch in.Type {
	case "tun":
		ipv4, _ := tunAddresses(in)
		addr, prefix, _ := strings.Cut(ipv4, "/")
		var parts []string
		if prefix != "" {
			parts = append(parts, "/"+prefix)
		}
		if jsv.Truthy(in.Fields["auto_route"]) {
			parts = append(parts, "自动路由")
		} else {
			parts = append(parts, "手动路由")
		}
		if jsv.Truthy(in.Fields["strict_route"]) {
			parts = append(parts, "严格路由")
		}
		if addr == "" {
			addr = "虚拟网卡"
		}
		return addr, strings.Join(parts, " · "), true
	case "burrow":
		return jsv.StringOr(in.Fields["signal_address"], "—"), "打洞信令", true
	}
	listen, port := inboundListen(in)
	if port != "" {
		return ":" + port, listen, false
	}
	return listen, "未设置端口", true
}

func inboundConfirmPopover(c *ui.Context, anchor ui.Element, state *kit.ConfirmState, p inboundConfirm, run func(func(error))) {
	o := kit.ConfirmOptions{Title: "删除入站？", Message: "「" + p.tag + "」将从入站列表中删除。", Confirm: "删除", Danger: true, Run: run}
	if p.reset {
		o.Title, o.Message, o.Confirm = "恢复默认值？", "「"+p.tag+"」当前填写的参数将恢复默认值。", "恢复"
	}
	kit.ConfirmPopover(c, anchor, state, o)
}

// resetInboundFields returns an inbound's fields to its type's defaults,
// keeping its tag and state.
func resetInboundFields(doc *model.Doc, id string) {
	for _, in := range doc.Inbounds {
		if in.ID == id {
			in.Fields = model.MakeInbound(in.Type, in.Tag, doc.Platform, doc.IPv6Enabled).Fields
		}
	}
}

// inEditor is the dialog adding or editing an inbound, editing a copy.
type inEditor struct {
	w       *Workspace
	open    bool
	isNew   bool
	draft   *model.Inbound
	tried   bool
	form    forms.Result
	confirm kit.ConfirmState
	pending inboundConfirm
	// generation keys the form, so each opening starts fresh.
	generation int
}

func (e *inEditor) start(in *model.Inbound, isNew bool) {
	e.close()
	e.open, e.isNew, e.draft, e.tried = true, isNew, in, false
	e.form = forms.Result{Valid: true}
	e.confirm = kit.ConfirmState{}
	e.generation++
	e.w.store.BeginEdit()
}

// openAdd starts a new inbound with a random free listen port.
func (e *inEditor) openAdd(doc *model.Doc, typ string) {
	in := model.MakeInbound(typ, "", platformOf(doc), doc.IPv6Enabled)
	if slices.ContainsFunc(schema.InboundSchema[typ], func(f schema.Field) bool { return f.Key == "listen_port" }) {
		used := map[float64]bool{}
		for _, x := range doc.Inbounds {
			used[jsv.Number(x.Fields["listen_port"])] = true
		}
		port := 0
		for range 200 {
			candidate := rand.IntN(50000) + 10000
			if !used[float64(candidate)] {
				port = candidate
				break
			}
		}
		if port == 0 {
			port = 20000
		}
		in.Fields["listen_port"] = float64(port)
	}
	e.start(in, true)
}

func (e *inEditor) openEdit(doc *model.Doc, id string) {
	for _, in := range doc.Inbounds {
		if in.ID == id {
			e.start(model.Clone(in), false)
			return
		}
	}
}

func (e *inEditor) close() {
	if !e.open {
		return
	}
	e.open, e.draft = false, nil
	e.w.store.EndEdit()
}

func platformOf(doc *model.Doc) string {
	if doc.Platform == "" {
		return "linux"
	}
	return doc.Platform
}

// tunAddresses splits a TUN inbound's addresses into its IPv4 and IPv6
// ranges.
func tunAddresses(in *model.Inbound) (ipv4, ipv6 string) {
	values, _ := jsv.Array(in.Fields["address"])
	for _, v := range values {
		a := jsv.String(v)
		if strings.Contains(a, ":") {
			if ipv6 == "" {
				ipv6 = a
			}
		} else if ipv4 == "" {
			ipv4 = a
		}
	}
	return ipv4, ipv6
}

// setTunAddresses sets a TUN inbound's ranges and recomputes its route
// exclusions.
func setTunAddresses(in *model.Inbound, ipv4, ipv6 string, ipv6Enabled bool) {
	var address []string
	if v := strings.TrimSpace(ipv4); v != "" {
		address = append(address, v)
	}
	if v := strings.TrimSpace(ipv6); ipv6Enabled && v != "" {
		address = append(address, v)
	}
	values := make([]any, len(address))
	for i, a := range address {
		values[i] = a
	}
	in.Fields["address"] = values
	exclude := model.TunDefaultExclude(address, ipv6Enabled)
	ex := make([]any, len(exclude))
	for i, a := range exclude {
		ex[i] = a
	}
	in.Fields["route_exclude_address"] = ex
}

// portOwner is the other inbound listening on in's port, or nil. An
// inbound without a port, as TUN, owns none.
func portOwner(doc *model.Doc, in *model.Inbound) *model.Inbound {
	port, ok := in.Fields["listen_port"]
	if !ok || port == nil {
		return nil
	}
	n := jsv.Number(port)
	for _, x := range doc.Inbounds {
		if x.ID != in.ID && jsv.NumberOrNaN(x.Fields["listen_port"]) == n {
			return x
		}
	}
	return nil
}

func (e *inEditor) save() {
	in := e.draft
	doc := e.w.store.Doc()
	e.tried = true
	if !e.form.Valid {
		e.w.env.Toast("请修正 JSON 格式")
		return
	}
	if msg := model.TagValidationError(doc, in.Tag, in.ID); msg != "" {
		e.w.env.Toast(msg)
		return
	}
	if owner := portOwner(doc, in); owner != nil {
		e.w.env.Toast("监听端口已被入站「" + owner.Tag + "」使用")
		return
	}
	if !doc.IPv6Enabled && in.Fields["listen"] == "::" {
		e.w.env.Toast("请先在概览中启用 IPv6，再选择 :: 监听地址")
		return
	}
	if in.Type == "tun" {
		ipv4, ipv6 := tunAddresses(in)
		if ipv4 == "" || !schema.IsCIDR(ipv4) || doc.IPv6Enabled && (ipv6 == "" || !schema.IsCIDR(ipv6)) {
			msg := "请填写有效的 TUN IPv4 网段"
			if doc.IPv6Enabled {
				msg += "和 IPv6 网段"
			}
			e.w.env.Toast(msg)
			return
		}
	}
	table := schema.Fields(schema.KindInbound, in.Type)
	effective := model.EffectiveNetworkFields(table, in.Fields, in.NetworkConfigRefs, e.w.store.NetworkConfigs(), doc.Platform)
	if errs := schema.FormValidationErrors(table, effective, platformOf(doc)); len(errs) > 0 {
		e.w.env.Toast(errs[0].Field.Label + "：" + errs[0].Message)
		return
	}
	saved := model.Clone(in)
	if !e.w.update(func(d *model.Doc) { putInbound(d, saved) }) {
		return
	}
	e.close()
}

func (e *inEditor) view(c *ui.Context) {
	if !e.open || e.draft == nil {
		return
	}
	k := kit.T(c)
	in := e.draft
	doc := e.w.store.Doc()
	verb := "编辑"
	if e.isNew {
		verb = "添加"
	}
	name := in.Tag
	if name == "" {
		name = typeLabel(in.Type)
	}
	open := true
	kit.Dialog(c, &open, verb+" "+name, 600, func() {
		if in.Type == "tun" {
			e.tunFields(c, doc)
		}
		var inOptions []forms.RefOption
		for _, x := range doc.Inbounds {
			if x.Enabled {
				inOptions = append(inOptions, forms.RefOption{ID: x.ID, Tag: x.Tag, Type: x.Type})
			}
		}
		table := schema.Fields(schema.KindInbound, in.Type)
		if in.Type == "tun" {
			table = slices.DeleteFunc(slices.Clone(table), func(f schema.Field) bool { return f.Key == "address" })
		}
		e.form = forms.Form(c, []any{"inbound-form", in.Type, in.ID, e.generation}, table, in.Fields, forms.Context{
			Doc: doc, NetworkConfigs: e.w.store.NetworkConfigs(), NetworkRefs: in.NetworkConfigRefs,
			OnNetworkRefs: func(r model.NetworkConfigRefs) { in.NetworkConfigRefs = r },
			ExcludeRef:    in.ID, InOptions: inOptions, ShowErrors: e.tried,
			Leading: func() {
				ui.Column(c).Gap(5).Shrink(0).Children(func() {
					ui.Text(c, "名称").FontSize(kit.FontBody).TextColor(k.Label2)
					kit.Input(c, "name", &in.Tag, "").Label("名称")
				})
			},
		})
	}, func() {
		if !e.isNew {
			reset := kit.Button(c, "恢复默认", kit.ButtonOptions{Small: true})
			if reset.Clicked() {
				e.pending, e.confirm = inboundConfirm{reset: true, id: in.ID, tag: in.Tag}, kit.ConfirmState{Open: true}
			}
			if e.pending.reset {
				inboundConfirmPopover(c, reset, &e.confirm, e.pending, func(done func(error)) {
					in.Fields = model.MakeInbound(in.Type, in.Tag, platformOf(doc), doc.IPv6Enabled).Fields
					done(nil)
				})
			}
			ui.Spacer(c)
			del := kit.Button(c, "删除", kit.ButtonOptions{Variant: kit.Danger, Small: true})
			if del.Clicked() {
				e.pending, e.confirm = inboundConfirm{id: in.ID, tag: in.Tag}, kit.ConfirmState{Open: true}
			}
			if !e.pending.reset {
				inboundConfirmPopover(c, del, &e.confirm, e.pending, func(done func(error)) {
					id := in.ID
					if err := e.w.store.Update(func(d *model.Doc) { removeInbound(d, id) }); err != nil {
						done(err)
						return
					}
					done(nil)
					e.close()
				})
			}
		} else {
			ui.Spacer(c)
		}
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(e.close)
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary}).OnClick(e.save)
	})
	if !open {
		e.close()
	}
}

// tunFields edit a TUN inbound's IPv4 and IPv6 ranges, side by side.
func (e *inEditor) tunFields(c *ui.Context, doc *model.Doc) {
	in := e.draft
	ipv4, ipv6 := tunAddresses(in)
	defaults := model.TunDefaultAddress(platformOf(doc), doc.IPv6Enabled)
	placeholder6 := ""
	for _, a := range defaults {
		if strings.Contains(a, ":") {
			placeholder6 = a
		}
	}
	placeholder4 := ""
	if len(defaults) > 0 {
		placeholder4 = defaults[0]
	}
	columns := 1
	if doc.IPv6Enabled {
		columns = 2
	}
	ui.Grid(c).Columns(columns).Gap(12).FillWidth().Shrink(0).Children(func() {
		ui.Column(c).Gap(5).MinWidth(0).Children(func() {
			fieldLabel(c, "IPv4 网段")
			v := ipv4
			if kit.Input(c, "tun-ipv4", &v, placeholder4).Label("IPv4 网段").Changed() {
				setTunAddresses(in, v, ipv6, doc.IPv6Enabled)
			}
			if v != "" && !schema.IsCIDR(v) {
				kit.FormError(c, "请输入 IPv4 CIDR，例如 172.18.0.1/30")
			}
		})
		if doc.IPv6Enabled {
			ui.Column(c).Gap(5).MinWidth(0).Children(func() {
				fieldLabel(c, "IPv6 网段")
				v := ipv6
				if kit.Input(c, "tun-ipv6", &v, placeholder6).Label("IPv6 网段").Changed() {
					setTunAddresses(in, ipv4, v, doc.IPv6Enabled)
				}
				if v != "" && !schema.IsCIDR(v) {
					kit.FormError(c, "请输入 IPv6 CIDR，例如 fdfe:dcba:9876::1/126")
				}
			})
		}
	})
}
