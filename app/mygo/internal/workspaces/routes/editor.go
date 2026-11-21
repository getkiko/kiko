package routes

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// ruleEditor is the floating editor of a traffic or DNS rule
// (the rule editor sidebar): a draft of the rule and of its conditions.
type ruleEditor struct {
	dns   bool
	isNew bool
	open  bool
	// route or dnsRule is the draft; tree its conditions.
	route   *model.RouteRule
	dnsRule *model.DNSRule
	tree    *model.Condition
	// action is the stored rule's action, offered when legacy.
	action string

	menu bool
	del  kit.ConfirmState
	// ask is the question "Interrupt existing connections" of a changed outbound: askRule the
	// rule it saves, askChoice the answer.
	ask       kit.ConfirmState
	askRule   *model.RouteRule
	askChoice int
	// guard asks before the unsaved draft is dropped; base is the draft as
	// the editor first showed it, taken after its first build, which
	// normalizes the inputs.
	guard      kit.EditorGuard
	base       string
	based      bool
	validation string
	cond       conditionState
}

func (e *ruleEditor) id() string {
	if e.dns {
		return e.dnsRule.ID
	}
	return e.route.ID
}

func (e *ruleEditor) name() *string {
	if e.dns {
		return &e.dnsRule.Name
	}
	return &e.route.Name
}

func (e *ruleEditor) enabled() *bool {
	if e.dns {
		return &e.dnsRule.Enabled
	}
	return &e.route.Enabled
}

func (e *ruleEditor) groupID() *string {
	if e.dns {
		return &e.dnsRule.GroupID
	}
	return &e.route.GroupID
}

func (e *ruleEditor) actionValue() string {
	var a string
	if e.dns {
		a = e.dnsRule.Action
	} else {
		a = e.route.Action
	}
	if a == "" {
		return model.ActionRoute
	}
	return a
}

func (e *ruleEditor) setAction(a string) {
	if e.dns {
		e.dnsRule.Action = a
	} else {
		e.route.Action = a
	}
}

func (e *ruleEditor) owner() string {
	if e.dns {
		return e.dnsRule.PolicyOwner()
	}
	return e.route.PolicyOwner()
}

// editorTree is a stored rule's conditions as the editor shows them.
func editorTree(tree *model.Condition, match map[string]any, logic string) *model.Condition {
	if tree != nil {
		tree = model.CloneCondition(tree)
	} else {
		tree = model.LegacyCondition(match, logic)
	}
	return model.NormalizeTypedMatches(model.NormalizeProcessConditions(tree))
}

// openRule opens the editor of a rule of the current section.
func (w *Workspace) openRule(doc *model.Doc, id string) {
	if w.sec == sectionDNS {
		for _, r := range doc.DNS.Rules {
			if r.ID == id {
				draft := model.Clone(r)
				w.showEditor(&ruleEditor{dns: true, open: true, dnsRule: draft, action: r.Action,
					tree: editorTree(r.ConditionTree, r.Match, "")})
			}
		}
		return
	}
	for _, r := range doc.Route.Rules {
		if r.ID == id {
			draft := model.Clone(r)
			w.showEditor(&ruleEditor{open: true, route: draft, action: r.Action, tree: editorTree(r.ConditionTree, r.Match, r.Logic)})
		}
	}
}

// addRule opens the editor of a new traffic rule of the current device.
func (w *Workspace) addRule(doc *model.Doc) {
	w.err = ""
	w.showEditor(&ruleEditor{open: true, isNew: true, route: w.newRuleDraft(doc), tree: model.EmptyConditionGroup()})
}

// newRuleDraft is a new traffic rule of the current device: enabled, in
// its first group, to its default outbound.
func (w *Workspace) newRuleDraft(doc *model.Doc) *model.RouteRule {
	groups := model.RouteGroupsOf(doc, w.owner)
	rule := &model.RouteRule{ID: model.NewID("rr"), Enabled: true, Action: model.ActionRoute, Match: map[string]any{},
		ConditionTree: model.EmptyConditionGroup()}
	if w.owner != model.LocalDeviceID {
		rule.DeviceID = w.owner
	}
	if len(groups) > 0 {
		rule.GroupID = groups[0].ID
	}
	outbound, _ := ownFinal(doc, w.owner)
	if outbound == "" {
		if w.owner == model.LocalDeviceID {
			outbound = doc.Route.LocalFinal.ID()
		} else {
			outbound = doc.Route.Final.ID()
		}
	}
	if outbound == "" {
		outbound = doc.Route.Final.ID()
	}
	if outbound == "" {
		if refs := forms.DocRefOptions(doc); len(refs) > 0 {
			outbound = refs[0].ID
		}
	}
	if outbound != "" {
		rule.Outbound = model.Some(outbound)
	}
	return rule
}

// addDNSRule opens the editor of a new DNS rule of the current device.
func (w *Workspace) addDNSRule(doc *model.Doc) {
	w.err = ""
	groups := model.DNSGroupsOf(doc, w.owner)
	rule := &model.DNSRule{ID: model.NewID("dr"), Enabled: true, Action: model.ActionRoute, Match: map[string]any{},
		DeviceID: w.owner, ConditionTree: model.EmptyConditionGroup()}
	if len(groups) > 0 {
		rule.GroupID = groups[0].ID
	}
	server := model.DNSFinalFor(doc, w.owner).ID()
	if server == "" {
		for _, s := range doc.DNS.Servers {
			if s.Enabled && s.Type != "fakeip" {
				server = s.ID
				break
			}
		}
	}
	if server != "" {
		rule.Server = model.Some(server)
	}
	if detour := model.DNSFinalDetourFor(doc, w.owner); detour.Set() {
		rule.Detour = detour
	}
	w.showEditor(&ruleEditor{dns: true, open: true, isNew: true, dnsRule: rule, tree: model.EmptyConditionGroup()})
}

// showEditor opens e in the editor sidebar, in place of the rule or
// default editor shown: one sidebar shows at a time.
// A rule editor with unsaved changes asks before another replaces it; the
// answer decides, and the draft stays meanwhile.
func (w *Workspace) showEditor(e *ruleEditor) {
	if cur := w.editor; cur != nil && cur != e && cur.open {
		// Pressing the rule being edited keeps its editor and draft.
		if !cur.isNew && !e.isNew && cur.dns == e.dns && cur.id() == e.id() {
			return
		}
		w.leaveEditor(func() { w.editor, w.fallback = e, nil })
		return
	}
	w.editor, w.fallback = e, nil
}

// leaveEditor runs then once the editor sidebar is out of the way: at once
// when no rule editor is open or its draft is clean, else after the
// question "Discard changes?" over the editor's footer is answered with a
// discard. The sidebar and its draft stay while it waits, and a later way
// out asked meanwhile waits instead; then runs with the editor closed.
func (w *Workspace) leaveEditor(then func()) {
	if cur := w.editor; cur != nil && cur.open {
		cur.guard.Dirty = cur.dirty()
		cur.guard.Ask(&cur.open, func() { w.closeEditor(); then() })
		return
	}
	w.closeEditor()
	then()
}

// snapshot is the draft's saved content as stable text: the rule, its
// conditions, and a condition typed but not yet added.
func (e *ruleEditor) snapshot() string {
	var drafts []string
	for key, text := range e.cond.drafts {
		// The test box under a token field is scratch input, not the rule.
		if strings.TrimSpace(text) != "" && !strings.HasSuffix(key, ":test") {
			drafts = append(drafts, key+"="+text)
		}
	}
	slices.Sort(drafts)
	b, err := json.Marshal([]any{e.route, e.dnsRule, e.tree, drafts})
	if err != nil {
		return ""
	}
	return string(b)
}

// dirty is whether the draft differs from what the editor opened with.
func (e *ruleEditor) dirty() bool { return e.based && e.snapshot() != e.base }

// closeEditor closes the editor sidebar: the rule editor and the default
// outbound / fallback DNS editor.
func (w *Workspace) closeEditor() { w.editor, w.fallback = nil, nil }

// dnsProtocolLabel is a DNS server type's short name.
func dnsProtocolLabel(t string) string {
	labels := map[string]string{"local": "Local", "udp": "UDP", "tcp": "TCP", "tls": "DoT", "https": "DoH", "quic": "DoQ",
		"h3": "H3", "hosts": "Hosts", "fakeip": "FakeIP", "dhcp": "DHCP", "tailscale": "Tailscale"}
	if l, ok := labels[t]; ok {
		return l
	}
	return strings.ToUpper(t)
}

func upper(s string) string { return strings.ToUpper(s) }

// editorRow is a compact row of the editor's identity group: the label
// on the left, the control on the right.
func editorRow(c *ui.Context, label string, control func()) {
	k := kit.T(c)
	ui.Row(c.Key(label)).MinHeight(48).PaddingY(8).Gap(12).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label2).Width(80).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Children(control)
	})
}

// editorView builds the open rule editor.
func (w *Workspace) editorView(c *ui.Context, doc *model.Doc) {
	e := w.editor
	if e == nil {
		return
	}
	if !e.open {
		// Closed since the last frame: no sidebar, and no key left for the
		// next element.
		w.closeEditor()
		return
	}
	k := kit.T(c)
	e.guard.Dirty = e.dirty()
	title := "编辑"
	if e.isNew {
		title = "创建"
	}
	if e.dns {
		title += " DNS 分流规则"
	} else {
		title += "路由规则"
	}
	var deleteButton ui.Element
	var more ui.Element
	// The sidebar keeps its key from rule to rule, staying in place; the
	// body is keyed by the rule. A press on another row keeps it open for
	// the row's click to switch it; its close button, Escape and Cancel close
	// it at once, dropping the draft, and a press elsewhere asks first.
	kit.EditorSidebarGuarded(c.Key(editorKey), &e.open, w.rowHeld, &e.guard, title, func() {
		if !e.isNew {
			more = kit.Button(c, "更多", kit.ButtonOptions{Small: true})
			if more.Clicked() {
				e.menu = !e.menu
			}
			kit.ActionMenu(c, more, &e.menu, []kit.ActionGroup{{Items: []kit.ActionItem{{ID: "copy", Label: "复制到设备…", Icon: kit.SymCopy, Run: func() {
				w.err = ""
				w.copy = newCopyDialog(w.doc(), copyRequest{sec: w.sec, owner: e.owner(), ruleID: e.id()})
			}}}}}, kit.ActionMenuOptions{Label: "规则操作", Compact: true})
		}
		ui.Text(c, "启用").FontSize(kit.FontTable).TextColor(k.Label2)
		kit.Switch(c, e.enabled(), "启用规则")
	}, func() {
		ui.Column(c.Key("editor-body:" + e.id())).Shrink(0).Gap(12).Children(func() {
			w.identityForm(c, doc, e)
			w.conditionEditor(c, doc, e)
		})
	}, func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
			ui.Column(c).Gap(3).MaxHeight(84).Children(func() {
				ui.Text(c, "匹配预览").FontSize(kit.FontCaption).TextColor(k.Label3)
				preview := "请添加匹配条件"
				if len(e.tree.Rules) > 0 {
					preview = model.ConditionSummary(e.tree)
				}
				ui.Text(c, preview).FontSize(kit.FontCaption).TextColor(k.Label2).Wrap().MaxLines(4)
			})
			msg := e.validation
			if msg == "" {
				msg = w.err
			}
			kit.FormError(c, msg)
			ui.Row(c).Gap(8).Children(func() {
				if !e.isNew {
					deleteButton = kit.Button(c, "删除", kit.ButtonOptions{Variant: kit.Danger})
					if deleteButton.Clicked() {
						e.del.Open = true
					}
					name := displayName(*e.name())
					kit.ConfirmPopover(c, deleteButton, &e.del, kit.ConfirmOptions{Title: "删除规则",
						Message: "删除「" + name + "」后，其余规则按当前顺序继续匹配。", Confirm: "删除规则", Danger: true,
						Run: func(done func(error)) {
							if w.deleteRule(e) {
								done(nil)
								w.closeEditor()
								return
							}
							done(errString(w.err))
						}})
				}
				ui.Spacer(c)
				// Cancel is a discard already: it closes at once, as the close
				// button and Escape do.
				if kit.Button(c, "取消", kit.ButtonOptions{}).Clicked() {
					e.guard.Discard(&e.open)
				}
				save := kit.Button(c, "保存规则", kit.ButtonOptions{Variant: kit.Primary})
				save.OnClick(func() { w.saveRule(e) })
				w.interruptPopover(c, save, e)
			})
		})
	})
	if !e.based {
		e.base, e.based = e.snapshot(), true
	}
	if !e.open && w.editor == e {
		w.closeEditor()
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// identityForm is the rule's name, group, action and target.
func (w *Workspace) identityForm(c *ui.Context, doc *model.Doc, e *ruleEditor) {
	k := kit.T(c)
	ui.Column(c).Shrink(0).Dividers(1, k.HairlineSoft).Margin(0, 0, 8).Children(func() {
		editorRow(c, "规则名称", func() {
			kit.Input(c, "name", e.name(), "为规则命名").Grow(1).Label("规则名称")
		})
		editorRow(c, "规则分组", func() {
			var groups []*model.RouteGroup
			if e.dns {
				groups = model.DNSGroupsOf(doc, e.owner())
			} else {
				groups = model.RouteGroupsOf(doc, e.owner())
			}
			var options []kit.Option
			for _, g := range groups {
				options = append(options, kit.Option{Value: g.ID, Label: g.Name})
			}
			options = withUnknown(options, *e.groupID())
			kit.Select(c, "group", e.groupID(), options, kit.SelectOptions{Label: "规则分组"}).Grow(1)
		})
		editorRow(c, "动作", func() {
			var options []kit.Option
			if e.dns {
				options = []kit.Option{{Value: model.ActionRoute, Label: "使用 DNS 服务器"}, {Value: model.ActionReject, Label: "拒绝查询"}, {Value: model.ActionScript, Label: "脚本应答"}}
			} else {
				// A sniff rule matters for its "Override destination": every
				// connection is sniffed by the built-in rule already.
				options = []kit.Option{{Value: model.ActionRoute, Label: "转发到出口"}, {Value: model.ActionReject, Label: "拒绝连接"},
					{Value: model.ActionResolve, Label: "解析域名"}, {Value: model.ActionSniff, Label: "协议嗅探"}}
				if !e.isNew && e.action == model.ActionHijackDNS {
					options = append(options, kit.Option{Value: model.ActionHijackDNS, Label: "DNS 接管（已有规则）"})
				}
			}
			value := e.actionValue()
			if kit.Select(c, "action", &value, options, kit.SelectOptions{Label: "规则动作"}).Grow(1).Changed() {
				if value == model.ActionHijackDNS && value != e.action {
					return
				}
				e.setAction(value)
			}
		})
		if e.actionValue() == model.ActionRoute && !e.dns {
			editorRow(c, "目标出口", func() { w.targetSelect(c, doc, e, "目标出口") })
		}
		if e.dns && e.actionValue() == model.ActionRoute {
			editorRow(c, "DNS 服务器", func() { dnsServersSelect(c, doc, e.dnsRule) })
			editorRow(c, "查询出口", func() { dnsDetoursEditor(c, doc, e.dnsRule) })
		}
		if e.dns && e.actionValue() == model.ActionScript {
			editorRow(c, "DNS 脚本", func() { dnsScriptSelect(c, doc, e.dnsRule) })
			editorRow(c, "失败时", func() {
				value := e.dnsRule.OnError
				onError := []kit.Option{{Value: "", Label: "继续下一条规则"}, {Value: model.ScriptOnErrorReject, Label: "拒绝查询"}}
				if kit.Select(c, "on-error", &value, onError, kit.SelectOptions{Label: "失败时"}).Grow(1).Changed() {
					e.dnsRule.OnError = value
				}
			})
		}
		if !e.dns && e.route.Action == model.ActionSniff {
			editorRow(c, "覆盖连接目标", func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					kit.Switch(c, &e.route.OverrideDestination, "覆盖连接目标")
					ui.Text(c, "用嗅探到的域名连接，由出口（代理服务器）解析域名").FontSize(kit.FontCaption).
						TextColor(kit.T(c).Label3).MinWidth(0).Shrink(1)
				})
			})
		}
	})
}

// targetSelect is the searchable Target Outbound choice.
func (w *Workspace) targetSelect(c *ui.Context, doc *model.Doc, e *ruleEditor, label string) {
	value := e.route.Outbound.ID()
	if forms.OutboundSelect(c, "target", doc, &value, forms.OutboundSelectOptions{Label: label, Lead: []kit.Option{{Value: "", Label: "请选择"}}}).Changed() {
		e.route.Outbound = model.RefOf(value)
	}
}

// dnsServersSelect is the searchable choice of a DNS rule's servers,
// queried concurrently when there are several. A server keeps its detour
// while chosen; a new one starts with the first server's when it can
// take a detour.
func dnsServersSelect(c *ui.Context, doc *model.Doc, rule *model.DNSRule) {
	var options []kit.Option
	for _, s := range doc.DNS.Servers {
		if s.Enabled {
			options = append(options, kit.Option{Value: s.ID, Label: s.Tag, Detail: dnsProtocolLabel(s.Type)})
		}
	}
	targets := rule.Targets()
	var values []string
	for _, t := range targets {
		values = append(values, t.Server.ID())
		if t.Server.Valid() && !slices.ContainsFunc(options, func(o kit.Option) bool { return o.Value == t.Server.ID() }) {
			options = append(options, kit.Option{Value: t.Server.ID(), Label: t.Server.ID() + " · 需修正引用"})
		}
	}
	values = slices.DeleteFunc(values, func(v string) bool { return v == "" })
	if !kit.SearchMultiSelect(c, "servers", &values, options, kit.SearchSelectOptions{Label: "DNS 服务器", Placeholder: "搜索 DNS 服务器名称或协议"}).Changed() {
		return
	}
	inherit := model.Null()
	for _, t := range targets {
		if t.Detour.Valid() {
			inherit = t.Detour
			break
		}
	}
	var next []model.DNSTarget
	for _, v := range values {
		i := slices.IndexFunc(targets, func(t model.DNSTarget) bool { return t.Server.ID() == v })
		switch {
		case i >= 0:
			next = append(next, targets[i])
		case model.DNSTargetSupportsDetour(model.DNSServerByID(doc, v)):
			next = append(next, model.DNSTarget{Server: model.Some(v), Detour: inherit})
		default:
			next = append(next, model.DNSTarget{Server: model.Some(v), Detour: model.Null()})
		}
	}
	if len(next) == 0 {
		next = []model.DNSTarget{{Server: model.Null(), Detour: model.Null()}}
	}
	rule.SetTargets(next)
}

// dnsDetoursEditor is the Query Outbound of each server of a DNS rule: one choice,
// or one per server named beside it.
func dnsDetoursEditor(c *ui.Context, doc *model.Doc, rule *model.DNSRule) {
	targets := rule.Targets()
	if len(targets) <= 1 {
		var target model.DNSTarget
		if len(targets) == 1 {
			target = targets[0]
		}
		value := target.Detour.ID()
		if detourSelect(c, "detour", doc, target.Server.ID(), &value, "DNS 查询出口") {
			rule.Detour = model.RefOf(value)
		}
		return
	}
	k := kit.T(c)
	ui.Column(c).Gap(8).FillWidth().Children(func() {
		for i, t := range targets {
			name := t.Server.ID()
			if s := model.DNSServerByID(doc, name); s != nil && s.Tag != "" {
				name = s.Tag
			}
			ui.Column(c.Key(t.Server.ID())).Gap(4).FillWidth().Children(func() {
				ui.Text(c, name).FontSize(kit.FontCaption).TextColor(k.Label2).SingleLine().Ellipsis("…")
				value := t.Detour.ID()
				if detourSelect(c, "detour", doc, t.Server.ID(), &value, name+" 查询出口") {
					targets[i].Detour = model.RefOf(value)
					rule.SetTargets(targets)
				}
			})
		}
	})
}

// saveRule validates the draft and saves it, closing the editor.
func (w *Workspace) saveRule(e *ruleEditor) {
	doc := w.doc()
	if problem := model.ValidateCondition(e.tree, false); problem != "" {
		e.validation = problem
		return
	}
	tree := model.CloneCondition(e.tree)
	var invalid bool
	if e.dns {
		next := model.Clone(e.dnsRule)
		next.Match, next.CatchAll, next.ConditionTree = map[string]any{}, false, tree
		if targets := next.Targets(); next.Action != model.ActionReject && next.Action != model.ActionScript && len(targets) > 1 && slices.ContainsFunc(targets, func(t model.DNSTarget) bool {
			s := model.DNSServerByID(doc, t.Server.ID())
			return s != nil && s.Type == "fakeip"
		}) {
			e.validation = "FakeIP 不能与其他服务器同时查询"
			return
		}
		invalid = model.HasInvalidDNSRuleReferences(doc, next)
		if invalid {
			e.validation = "请检查目标、规则集及条件中的资源引用"
			return
		}
		e.validation = ""
		if w.save(func(doc *model.Doc) error {
			doc.DNS.Rules = putRule(doc.DNS.Rules, next, e.isNew)
			return nil
		}) {
			w.closeEditor()
		}
		return
	}
	next := model.Clone(e.route)
	next.Match, next.Logic, next.CatchAll, next.ConditionTree = map[string]any{}, "", false, tree
	if model.HasInvalidRuleReferences(doc, next) {
		e.validation = "请检查目标、规则集及条件中的资源引用"
		return
	}
	e.validation = ""
	if w.asksToInterrupt(e, next) {
		e.askRule, e.askChoice, e.ask.Open = next, keepConnections, true
		return
	}
	if w.commitRoute(e, next, false) {
		w.closeEditor()
	}
}

// commitRoute saves the traffic rule next. When the outbound changed
// under a running core, interrupt records the answer of the question: the
// next live apply closes the rule's connections, or leaves them.
func (w *Workspace) commitRoute(e *ruleEditor, next *model.RouteRule, interrupt bool) bool {
	asked := w.asksToInterrupt(e, next)
	var from string
	marked := false
	for _, r := range w.doc().Route.Rules {
		if r.ID == next.ID && !e.isNew {
			from = w.exitBase(r)
			_, marked = w.store.InterruptFrom(r.ID)
		}
	}
	if !w.save(func(doc *model.Doc) error {
		doc.Route.Rules = putRule(doc.Route.Rules, next, e.isNew)
		return nil
	}) {
		return false
	}
	switch {
	case asked:
		w.store.SetInterrupt(next.ID, from, next.Outbound.ID(), interrupt)
	case marked && next.Outbound.ID() == from:
		// Sent back to the outbound its connections use: nothing to close.
		w.store.SetInterrupt(next.ID, from, from, false)
	}
	return true
}

// putRule appends a new rule or replaces the stored one.
func putRule[T model.PolicyRule](rules []T, rule T, isNew bool) []T {
	if isNew {
		return append(rules, rule)
	}
	out := make([]T, len(rules))
	for i, r := range rules {
		if r.PolicyID() == rule.PolicyID() {
			out[i] = rule
		} else {
			out[i] = r
		}
	}
	return out
}

// deleteRule removes the edited rule.
func (w *Workspace) deleteRule(e *ruleEditor) bool {
	id := e.id()
	return w.save(func(doc *model.Doc) error {
		if e.dns {
			var rules []*model.DNSRule
			for _, r := range doc.DNS.Rules {
				if r.ID != id {
					rules = append(rules, r)
				}
			}
			doc.DNS.Rules = append([]*model.DNSRule{}, rules...)
			return nil
		}
		var rules []*model.RouteRule
		for _, r := range doc.Route.Rules {
			if r.ID != id {
				rules = append(rules, r)
			}
		}
		doc.Route.Rules = append([]*model.RouteRule{}, rules...)
		return nil
	})
}

// dnsScriptSelect is the choice of the dns script a script action runs.
func dnsScriptSelect(c *ui.Context, doc *model.Doc, rule *model.DNSRule) {
	options := []kit.Option{{Value: "", Label: "请选择"}}
	if doc.HTTPTools != nil {
		for _, sc := range doc.HTTPTools.Scripts {
			if sc != nil && sc.Type == model.ScriptDNS && model.ScriptUsable(doc, sc.EffectiveTag(), model.ScriptDNS) {
				name := sc.Name
				if name == "" {
					name = sc.EffectiveTag()
				}
				options = append(options, kit.Option{Value: sc.EffectiveTag(), Label: name})
			}
		}
	}
	options = withUnknown(options, rule.Script)
	kit.Select(c, "dns-script", &rule.Script, options, kit.SelectOptions{Label: "DNS 脚本"}).Grow(1)
}
