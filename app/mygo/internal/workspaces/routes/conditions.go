package routes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// choice is an option of a condition field.
type choice struct{ value, label string }

// fieldSpec describes a condition field of the editor (ConditionEditor's
// FieldSpec).
type fieldSpec struct {
	key, label, section string
	control             string // choices, select, boolean, address-map
	options             []choice
	hint, placeholder   string
	json                bool
}

func upperChoices(items ...string) []choice {
	out := make([]choice, len(items))
	for i, v := range items {
		out[i] = choice{v, strings.ToUpper(v)}
	}
	return out
}

var networkTypes = []choice{{"wifi", "Wi-Fi"}, {"cellular", "蜂窝网络"}, {"ethernet", "有线网络"}, {"other", "其他网络"}}

func protocolChoices() []choice {
	out := upperChoices("tls", "http", "quic", "dns", "stun", "bittorrent", "dtls", "ssh", "rdp", "ntp")
	for i := range out {
		if out[i].value == "bittorrent" {
			out[i].label = "BitTorrent"
		}
	}
	return out
}

var trafficFields = []fieldSpec{
	{key: "source_port", label: "来源端口", section: "来源"},
	{key: "ip_cidr", label: "目标 IP 地址", section: "目标", placeholder: "1.1.1.1\n10.0.0.0/8", hint: "填写 IPv4、IPv6 或 CIDR 网段，每行一个。"},
	{key: "port", label: "目标端口", section: "目标", placeholder: "80, 443", hint: "填写 1–65535 的端口号，多个端口用逗号分隔。"},
	{key: "domain_suffix", label: "域名", section: "目标", placeholder: "example.com", hint: "每行一个域名；后缀匹配同时包含域名及其子域名。"},
	{key: "domain", label: "完整域名", section: "高级匹配", placeholder: "www.example.com"},
	{key: "domain_keyword", label: "域名关键词", section: "高级匹配", placeholder: "example"},
	{key: "domain_regex", label: "域名正则", section: "高级匹配", placeholder: `^.*\.example\.com$`, hint: "每行一个正则表达式。"},
	{key: "network", label: "传输协议", section: "协议与接入", control: "choices", options: upperChoices("tcp", "udp", "icmp")},
	{key: "protocol", label: "应用协议", section: "协议与接入", control: "choices", options: protocolChoices()},
	{key: "inbound", label: "接入入口", section: "协议与接入", hint: "从已配置的入站和端点中选择，可多选。"},
	{key: "rule_set", label: "规则集", section: "规则集与进程", hint: "直接选择已配置的规则集，匹配内容随规则集更新。"},
	{key: "process", label: "进程", section: "规则集与进程"},
	{key: "package_name", label: "Android 应用", section: "规则集与进程", placeholder: "com.android.chrome", hint: "从本机应用中选择，或每行填写一个应用包名。"},
	{key: "ip_version", label: "IP 版本", section: "协议与接入", control: "select", options: []choice{{"4", "IPv4"}, {"6", "IPv6"}}},
	{key: "clash_mode", label: "运行模式", section: "高级匹配", control: "select", options: []choice{{"Rule", "规则模式"}, {"Global", "全局模式"}, {"Direct", "直连模式"}}},
}

var advancedFields = []fieldSpec{
	{key: "auth_user", label: "认证用户", section: "高级匹配", placeholder: "proxy-user", hint: "填写入站认证用户名，每行一个。"},
	{key: "client", label: "客户端类型", section: "高级匹配", control: "choices", options: []choice{
		{"chromium", "Chromium"}, {"safari", "Safari"}, {"firefox", "Firefox"}, {"quic-go", "quic-go"}, {"unknown", "未知客户端"}}},
	{key: "user", label: "系统用户", section: "高级匹配", placeholder: "ubuntu", hint: "每行一个系统用户名。"},
	{key: "user_id", label: "用户 ID", section: "高级匹配", placeholder: "1000, 1001", hint: "填写数字用户 ID，多个值用逗号分隔。"},
	{key: "network_type", label: "网络接口类型", section: "协议与接入", control: "choices", options: networkTypes},
	{key: "network_is_expensive", label: "按流量计费网络", section: "高级匹配", control: "boolean"},
	{key: "network_is_constrained", label: "低数据模式", section: "高级匹配", control: "boolean"},
	{key: "wifi", label: "Wi-Fi", section: "高级匹配"},
	{key: "interface_address", label: "接口地址", section: "高级匹配", control: "address-map", hint: "按接口名称填写地址或网段，例如 en0。"},
	{key: "network_interface_address", label: "接口类型地址", section: "高级匹配", control: "address-map", options: networkTypes, hint: "按网络接口类型填写地址或网段。"},
	{key: "default_interface_address", label: "默认接口地址", section: "高级匹配", placeholder: "192.168.1.0/24", hint: "每行一个 IP 地址或 CIDR 网段。"},
	{key: "preferred_by", label: "首选出站", section: "高级匹配", hint: "选择用于识别首选连接的出站或端点。"},
	{key: "script", label: "脚本条件", section: "高级匹配"},
}

// dnsFields are the DNS rule's fields; others show only while present.
func dnsFields() []fieldSpec {
	var out []fieldSpec
	for _, f := range trafficFields {
		if slices.Contains([]string{"domain_suffix", "domain", "domain_keyword", "domain_regex", "rule_set", "network", "protocol", "ip_version", "clash_mode"}, f.key) {
			if f.key == "network" {
				f.options = upperChoices("tcp", "udp")
			}
			out = append(out, f)
		}
	}
	out = append(out,
		fieldSpec{key: "query_type", label: "查询类型", section: "目标", control: "choices",
			options: upperChoices("A", "AAAA", "HTTPS", "SVCB", "CNAME", "MX", "TXT", "PTR", "SRV", "NS", "SOA", "CAA"), hint: "可多选常用查询类型；兼容已有的其他类型。"},
		fieldSpec{key: "preferred_by", label: "首选 DNS", section: "高级匹配"},
	)
	for _, f := range advancedFields {
		if f.key == "network_type" || f.key == "script" {
			out = append(out, f)
		}
	}
	return out
}

// conditionState is what the condition editor keeps between frames.
type conditionState struct {
	collapsed map[string]bool
	advanced  map[string]bool
	legacy    map[string]bool
	drafts    map[string]string
	clears    map[string]int
	maps      map[string][]addressRow
	queries   map[string]string
	errors    map[string]string
	// regex is the domain token whose regex test shows, keyed by row.
	regex     map[string]string
	regexOpen bool
	regexText string
	process   *processPicker
}

func (s *conditionState) init() {
	if s.collapsed == nil {
		s.collapsed, s.advanced, s.legacy = map[string]bool{}, map[string]bool{}, map[string]bool{}
		s.drafts, s.clears, s.maps = map[string]string{}, map[string]int{}, map[string][]addressRow{}
		s.queries, s.errors, s.regex = map[string]string{}, map[string]string{}, map[string]string{}
	}
}

// pathKey names a group of the tree by its path.
func pathKey(path []int) string {
	if len(path) == 0 {
		return "root"
	}
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, "-")
}

// update edits the group at path of a copy of the editor's tree.
func (e *ruleEditor) update(path []int, action func(g *model.Condition)) {
	next := model.CloneCondition(e.tree)
	group := next
	for _, index := range path {
		if index < 0 || index >= len(group.Rules) || group.Rules[index].Kind != model.ConditionGroup {
			return
		}
		group = group.Rules[index]
	}
	action(group)
	e.tree = next
}

// replaceRules is an update setting a group's rules to those of next.
func (e *ruleEditor) replaceRules(path []int, next *model.Condition) {
	e.update(path, func(g *model.Condition) { g.Rules = next.Rules })
}

// conditionEditor is the Match Conditions section of the rule editor.
func (w *Workspace) conditionEditor(c *ui.Context, doc *model.Doc, e *ruleEditor) {
	k := kit.T(c)
	e.cond.init()
	ui.Column(c).Gap(10).Shrink(0).Margin(4, 0, 0).Children(func() {
		ui.Row(c).Gap(10).Shrink(0).Children(func() {
			ui.Text(c, "匹配条件").FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).Grow(1)
			mode := 0
			if e.tree.Mode == "or" {
				mode = 1
			}
			if kit.Segmented(c, "mode", &mode, kit.Segment{Label: "全部满足"}, kit.Segment{Label: "任一满足"}).Label("条件组合方式").Changed() {
				next := model.SetConditionMode(e.tree, []string{"and", "or"}[mode])
				tree := model.CloneCondition(e.tree)
				tree.Mode, tree.Rules = next.Mode, next.Rules
				e.tree = tree
			}
			invert := e.tree.Invert
			if kit.Checkbox(c, &invert, "排除").Changed() {
				tree := model.CloneCondition(e.tree)
				tree.Invert = invert
				e.tree = tree
			}
		})
		w.groupForm(c, doc, e, e.tree, nil, true)
	})
}

// specsFor lists the fields a group of the tree shows.
func specsFor(doc *model.Doc, dns bool, root *model.Condition, leaves []*model.Condition) []fieldSpec {
	existing := map[string]bool{}
	for _, m := range model.ConditionMatches(root) {
		for key := range m {
			existing[key] = true
		}
	}
	var base []fieldSpec
	if dns {
		base = dnsFields()
		for _, f := range append(slices.Clone(trafficFields), advancedFields...) {
			var keys []string
			switch f.key {
			case "process":
				keys = model.ProcessFields
			case "wifi":
				keys = []string{"wifi_ssid", "wifi_bssid"}
			case "ip_cidr":
				keys = []string{"ip_cidr", "ip_is_private"}
			case "port", "source_port":
				keys = []string{f.key, f.key + "_range"}
			}
			if slices.ContainsFunc(keys, func(k string) bool { return existing[k] }) {
				base = append(base, f)
			}
		}
	} else {
		for _, f := range append(slices.Clone(trafficFields), advancedFields...) {
			if f.key != "package_name" || doc.Platform == model.PlatformAndroid {
				base = append(base, f)
			}
		}
	}
	represented := map[string]bool{"port_range": true, "source_port_range": true, "ip_is_private": true, "wifi_ssid": true, "wifi_bssid": true}
	for _, f := range base {
		represented[f.key] = true
	}
	for _, f := range model.ProcessFields {
		represented[f] = true
	}
	var advancedKeys []string
	seen := map[string]bool{}
	for _, leaf := range leaves {
		for _, key := range sortedMatchKeys(leaf.Match) {
			if !seen[key] && !represented[key] && key != "package_apps" {
				seen[key] = true
				advancedKeys = append(advancedKeys, key)
			}
		}
	}
	var specs []fieldSpec
	for _, f := range base {
		if f.key == "domain_suffix" {
			f.key = "domain_input"
			specs = append(specs, f)
			continue
		}
		if !slices.Contains(model.DomainFields, f.key) {
			specs = append(specs, f)
		}
	}
	// Historical domain-owned rule-set references stay in advanced fields.
	for _, field := range model.DomainFields {
		if slices.ContainsFunc(leaves, func(l *model.Condition) bool { _, ok := l.Match["rule_set"]; return l.UIField == field && ok }) {
			specs = append(specs, fieldSpec{key: field, label: model.ConditionLabels[field], section: "高级匹配"})
		}
	}
	for _, key := range advancedKeys {
		if strings.HasPrefix(key, "source_ip") || key == "source_mac_address" || key == "source_hostname" {
			continue
		}
		label := model.ConditionLabels[key]
		if label == "" {
			label = key
		}
		isJSON := slices.ContainsFunc(leaves, func(l *model.Condition) bool { _, ok := l.Match[key].(map[string]any); return ok })
		specs = append(specs, fieldSpec{key: key, label: label, section: "高级匹配", json: isJSON})
	}
	return specs
}

// sortedMatchKeys orders a match's keys as the condition labels do.
func sortedMatchKeys(match map[string]any) []string {
	var keys []string
	for _, k := range model.ConditionLabelKeys {
		if _, ok := match[k]; ok {
			keys = append(keys, k)
		}
	}
	var rest []string
	for k := range match {
		if _, ok := model.ConditionLabels[k]; !ok {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	return append(keys, rest...)
}

// groupForm builds a group of the tree: its nested groups, its fields by
// section (Advanced Matching folded), legacy structures and condition groups.
func (w *Workspace) groupForm(c *ui.Context, doc *model.Doc, e *ruleEditor, group *model.Condition, path []int, root bool) {
	k := kit.T(c)
	key := pathKey(path)
	s := &e.cond
	type indexed struct {
		node  *model.Condition
		index int
	}
	var groups []indexed
	var leaves []*model.Condition
	for i, node := range group.Rules {
		if node.Kind == model.ConditionGroup && node.UIField == "" {
			groups = append(groups, indexed{node, i})
		}
		if node.Kind == model.ConditionMatch {
			leaves = append(leaves, node)
		}
	}
	specs := specsFor(doc, e.dns, e.tree, leaves)
	var sharedLegacy, simple []*model.Condition
	for _, leaf := range leaves {
		keys := sortedMatchKeys(leaf.Match)
		onlyPorts := func(names ...string) bool {
			return !slices.ContainsFunc(keys, func(k string) bool { return !slices.Contains(names, k) })
		}
		if len(keys) > 1 && !onlyPorts("port", "port_range") && !onlyPorts("source_port", "source_port_range") {
			sharedLegacy = append(sharedLegacy, leaf)
		}
		if len(keys) == 1 {
			simple = append(simple, leaf)
		}
	}
	compositeRoot := root && len(groups) > 0 && len(leaves) == 0
	box := ui.Column(c.Key("group:" + key)).Gap(2).Shrink(0)
	if !root {
		box.Padding(10, 12).Radius(12).Border(1, k.HairlineSoft)
		if group.Invert {
			box.Border(1, k.Danger.Alpha(0.4))
		}
	}
	box.Children(func() {
		if !root {
			ui.Row(c).MinHeight(30).Gap(6).Shrink(0).Children(func() {
				head := ui.ButtonBase(c).Gap(5).Grow(1).Justify(ui.Start).Label("条件组 " + strconv.Itoa(path[len(path)-1]+1))
				head.Children(func() {
					sym := kit.SymChevronDown
					if s.collapsed[key] {
						sym = kit.SymChevronRight
					}
					kit.Icon(c, sym, 12).TextColor(k.Label3)
					ui.Text(c, "条件组 "+strconv.Itoa(path[len(path)-1]+1)).FontSize(kit.FontBody).FontWeight(600)
					if s.collapsed[key] {
						summary := fmt.Sprintf("%s · %d 项", strings.ToUpper(group.Mode), len(group.Rules))
						if group.Invert {
							summary += " · 已排除"
						}
						ui.Text(c, summary).FontSize(kit.FontCaption).TextColor(k.Label3)
					}
				})
				if head.Clicked() {
					s.collapsed[key] = !s.collapsed[key]
				}
				kit.IconButton(c, kit.SymX, "移除条件组", kit.IconButtonOptions{Size: 24, IconSize: 13}).OnClick(func() {
					parent, at := path[:len(path)-1], path[len(path)-1]
					e.update(parent, func(g *model.Condition) { g.Rules = slices.Delete(g.Rules, at, at+1) })
				})
			})
			if s.collapsed[key] {
				return
			}
			ui.Row(c).Gap(14).PaddingY(4).Shrink(0).Children(func() {
				mode := 0
				if group.Mode == "or" {
					mode = 1
				}
				if kit.Segmented(c, "logic", &mode, kit.Segment{Label: "全部满足"}, kit.Segment{Label: "任一满足"}).Label("条件组合方式").Changed() {
					e.update(path, func(g *model.Condition) {
						next := model.SetConditionMode(g, []string{"and", "or"}[mode])
						g.Mode, g.Rules = next.Mode, next.Rules
					})
				}
				invert := group.Invert
				if kit.Checkbox(c, &invert, "排除此组").Changed() {
					e.update(path, func(g *model.Condition) { g.Invert = invert })
				}
			})
		}
		for position, item := range groups {
			if position > 0 {
				junction := "或者 OR"
				if group.Mode == "and" {
					junction = "并且 AND"
				}
				ui.Text(c, junction).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label3).Padding(4, 2)
			}
			w.groupForm(c, doc, e, item.node, append(slices.Clone(path), item.index), false)
		}
		showFields := !root || len(groups) == 0 || len(leaves) > 0 ||
			slices.ContainsFunc(group.Rules, func(n *model.Condition) bool { return n.Kind == model.ConditionGroup && n.UIField != "" })
		_ = compositeRoot
		if showFields {
			var sections []string
			for _, spec := range specs {
				if !slices.Contains(sections, spec.section) {
					sections = append(sections, spec.section)
				}
			}
			for _, section := range sections {
				fields := func() {
					for _, spec := range specs {
						if spec.section == section {
							w.fieldForm(c, doc, e, group, path, leaves, spec)
						}
					}
				}
				if section == "高级匹配" {
					open, chosen := s.advanced[key]
					if !chosen {
						// A group that already holds an advanced condition opens it.
						open = slices.ContainsFunc(specs, func(spec fieldSpec) bool {
							return spec.section == "高级匹配" && slices.ContainsFunc(leaves, func(l *model.Condition) bool { return fieldActive(l, spec.key) })
						})
					}
					ui.Column(c.Key("advanced")).Shrink(0).Margin(6, 0, 0).Padding(8, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
						kit.Disclosure(c, "高级匹配", &open, func() {
							ui.Column(c).Gap(2).Margin(0, 0, 0, -16).Children(fields)
						})
					})
					s.advanced[key] = open
				} else {
					fields()
				}
			}
		}
		if len(sharedLegacy) > 0 {
			open := s.legacy[key]
			kit.Disclosure(c, "既有核心组合结构", &open, func() {
				ui.Text(c, "以上字段沿用原有组合关系。").FontSize(kit.FontCaption).TextColor(k.Label3)
				for _, leaf := range sharedLegacy {
					raw, _ := json.MarshalIndent(leaf.Match, "", "  ")
					kit.CodeBlock(c, string(raw))
				}
			})
			s.legacy[key] = open
		}
		for _, leaf := range simple {
			legacySource := slices.ContainsFunc(sortedMatchKeys(leaf.Match), func(k string) bool {
				return strings.HasPrefix(k, "source_ip") || k == "source_mac_address" || k == "source_hostname"
			})
			if legacySource {
				ui.Column(c).Gap(4).Shrink(0).Children(func() {
					ui.Text(c, "旧版来源条件 · 保留").FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label2)
					raw, _ := json.MarshalIndent(leaf.Match, "", "  ")
					kit.CodeBlock(c, string(raw))
				})
			}
		}
		kit.Button(c, "条件组", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).AlignSelf(ui.Start).OnClick(func() {
			e.update(path, func(g *model.Condition) {
				// Wrap the existing fields to expose mixed Boolean expressions
				// without changing their meaning.
				if root && len(g.Rules) > 0 && !slices.ContainsFunc(g.Rules, func(n *model.Condition) bool { return n.Kind == model.ConditionGroup && n.UIField == "" }) {
					previous := model.CloneCondition(g)
					previous.Invert = false
					g.Rules = []*model.Condition{previous, model.EmptyConditionGroup()}
					g.Mode = "and"
					s.collapsed["0"] = true
				} else {
					g.Rules = append(g.Rules, model.EmptyConditionGroup())
				}
			})
		})
	})
}

// fieldKey is a condition field's name: with a value, a button toggling
// between Match (a blue check) and Exclude (a red minus).
func fieldKey(c *ui.Context, label string, active, excluded, mixed bool, toggle func()) {
	k := kit.T(c)
	if !active || toggle == nil {
		ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label2)
		return
	}
	icon, tone, action := kit.SymCircleCheck, k.AccentText, "排除"
	title := "匹配"
	switch {
	case excluded:
		icon, tone, action, title = kit.SymMinus, k.Danger, "匹配", "排除"
	case mixed:
		icon, title = kit.SymChevronsUpDown, "包含匹配和排除条件"
	}
	b := ui.ButtonBase(c).Gap(5).Radius(6).PaddingX(4).Height(24).Label("将" + label + "改为" + action).Transition(kit.HoverMotion)
	kit.Hint(c, b, title+" · 点击改为"+action)
	if b.Hovered() {
		b.Background(k.Fill)
	}
	b.Children(func() {
		kit.Icon(c, icon, 12).TextColor(tone)
		ui.Text(c, label).FontSize(kit.FontBody).TextColor(k.Label)
	})
	if b.Clicked() {
		toggle()
	}
}

// fieldHead is a field's name with its clear button.
func fieldHead(c *ui.Context, label string, active, excluded, mixed bool, toggle, clear func()) {
	ui.Row(c).MinHeight(28).Gap(8).Shrink(0).Children(func() {
		fieldKey(c, label, active, excluded, mixed, toggle)
		ui.Spacer(c)
		if active && clear != nil {
			kit.IconButton(c, kit.SymX, "清除"+label+"条件", kit.IconButtonOptions{Size: 22, IconSize: 12}).OnClick(clear)
		}
	})
}

// anyList converts strings to the JSON array a match stores.
func anyList(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

var reUserIDs = regexp.MustCompile(`[，,\s]+`)

// fieldForm builds one field of a group.
func (w *Workspace) fieldForm(c *ui.Context, doc *model.Doc, e *ruleEditor, group *model.Condition, path []int, leaves []*model.Condition, spec fieldSpec) {
	k := kit.T(c)
	s := &e.cond
	cc := c.Key("field:" + spec.key)
	set := func(next *model.Condition) { e.replaceRules(path, next) }
	switch spec.key {
	case "domain_input":
		ui.Column(cc).Gap(4).Shrink(0).PaddingY(6).Children(func() { w.domainField(c, e, group, path) })
		return
	case "ip_version":
		ui.Column(cc).Gap(4).Shrink(0).PaddingY(6).Children(func() { ipVersionField(c, group, set) })
		return
	case "process":
		ui.Column(cc).Gap(4).Shrink(0).PaddingY(6).Children(func() { w.processField(c, e, group, path) })
		return
	case "ip_cidr", "port", "source_port", "wifi":
		kind := spec.key
		if kind == "ip_cidr" {
			kind = model.TypedIP
		}
		ui.Column(cc).Gap(4).Shrink(0).PaddingY(6).Children(func() { w.tokenField(c, doc, e, kind, spec.label, group, path) })
		return
	}
	var leaf *model.Condition
	for _, node := range leaves {
		if _, ok := node.Match[spec.key]; node.UIField == spec.key || node.UIField == "" && ok {
			leaf = node
			break
		}
	}
	field := spec.key
	if leaf != nil && leaf.UIField != "" {
		field = "rule_set"
	}
	var fieldValue any
	invert := false
	if leaf != nil {
		fieldValue = leaf.Match[field]
		invert = leaf.Invert
	}
	rowKey := pathKey(path) + ":" + spec.key
	setField := func(field string, value any, invert bool) {
		e.update(path, func(g *model.Condition) {
			g.Rules = model.SetConditionField(g, spec.key, "", field, value, invert).Rules
		})
	}
	put := func(next any) {
		active := next != nil
		switch v := next.(type) {
		case string:
			active = v != ""
		case []any:
			active = slices.ContainsFunc(v, func(item any) bool { return strings.TrimSpace(fmt.Sprint(item)) != "" })
		}
		if active {
			setField(field, next, invert)
		} else {
			setField("", nil, invert)
		}
	}
	clear := func() {
		delete(s.drafts, rowKey)
		delete(s.maps, rowKey)
		s.clears[rowKey]++
		setField("", nil, false)
	}
	current := model.Values(fieldValue)
	var options []choice
	options = append(options, spec.options...)
	for _, v := range current {
		if !slices.ContainsFunc(spec.options, func(o choice) bool { return o.value == v }) {
			options = append(options, choice{v, v + "（已有值）"})
		}
	}
	ui.Column(cc).Gap(4).Shrink(0).PaddingY(6).Children(func() {
		toggle := func() { setField(field, fieldValue, !invert) }
		if spec.key == "script" {
			toggle = nil // a script condition cannot be excluded
		}
		fieldHead(c, spec.label, leaf != nil, invert, false, toggle, clear)
		switch {
		case spec.key == "rule_set" || field == "rule_set":
			next, changed := ruleSetSelect(c, "value", doc, fieldValue)
			if changed {
				f := ""
				if len(next) > 0 {
					f = "rule_set"
				}
				var value any
				if len(next) > 0 {
					value = anyList(next)
				}
				setField(f, value, invert)
			}
		case spec.control == "choices":
			ui.Row(c).Wrap().GapY(4).GapX(14).Label(spec.label).Children(func() {
				for _, o := range options {
					on := slices.Contains(current, o.value)
					if kit.Checkbox(c.Key(o.value), &on, o.label).Changed() {
						next := slices.DeleteFunc(slices.Clone(current), func(v string) bool { return v == o.value })
						if on {
							next = append(slices.Clone(current), o.value)
						}
						put(anyList(next))
					}
				}
			})
		case spec.control == "select":
			value := ""
			if len(current) > 0 {
				value = current[0]
			}
			opts := []kit.Option{{Value: "", Label: "不限"}}
			for _, o := range options {
				opts = append(opts, kit.Option{Value: o.value, Label: o.label})
			}
			if kit.Select(c, "value", &value, opts, kit.SelectOptions{Label: spec.label}).Changed() {
				if value == "" {
					put(nil)
				} else if spec.key == "ip_version" {
					n, _ := strconv.Atoi(value)
					put(float64(n))
				} else {
					put(value)
				}
			}
		case spec.control == "boolean":
			on := fieldValue == true
			ui.Row(c).Children(func() {
				if kit.Switch(c, &on, "匹配"+spec.label).Changed() {
					if on {
						put(true)
					} else {
						put(nil)
					}
				}
			})
		case spec.control == "address-map":
			w.addressMap(c, e, rowKey, fieldValue, spec, put)
		case spec.key == "inbound" || spec.key == "preferred_by" || spec.key == "script":
			var opts []kit.Option
			switch {
			case spec.key == "script":
				if doc.HTTPTools != nil {
					for _, sc := range doc.HTTPTools.Scripts {
						if sc != nil && sc.Type == model.ScriptRule && model.ScriptUsable(doc, sc.EffectiveTag(), model.ScriptRule) {
							name := sc.Name
							if name == "" {
								name = sc.EffectiveTag()
							}
							opts = append(opts, kit.Option{Value: sc.EffectiveTag(), Label: name})
						}
					}
				}
			case spec.key == "inbound":
				for _, in := range doc.Inbounds {
					opts = append(opts, kit.Option{Value: in.ID, Label: in.Tag})
				}
				for _, ep := range doc.Endpoints {
					opts = append(opts, kit.Option{Value: ep.ID, Label: ep.Tag})
				}
			case e.dns:
				for _, sv := range doc.DNS.Servers {
					if sv.Type == "local" || sv.Type == "hosts" || sv.Type == "tailscale" {
						opts = append(opts, kit.Option{Value: sv.ID, Label: sv.Tag})
					}
				}
			default:
				for _, o := range doc.Outbounds {
					opts = append(opts, kit.Option{Value: o.ID, Label: o.Tag})
				}
				for _, ep := range doc.Endpoints {
					opts = append(opts, kit.Option{Value: ep.ID, Label: ep.Tag})
				}
			}
			if next, changed := multiSelect(c, "value", current, opts, spec.label, "选择"+spec.label); changed {
				put(anyList(next))
			}
		case spec.key == "user_id":
			draft, ok := s.drafts[rowKey]
			if !ok {
				draft = strings.Join(current, ", ")
			}
			if kit.Input(c, "value", &draft, spec.placeholder).Label(spec.label).Changed() {
				s.drafts[rowKey] = draft
				var ids []string
				for _, part := range reUserIDs.Split(draft, -1) {
					if part != "" {
						ids = append(ids, part)
					}
				}
				put(anyList(ids))
			}
		default:
			text := ""
			if fieldValue != nil {
				if m, ok := fieldValue.(map[string]any); ok {
					raw, _ := json.Marshal(m)
					text = string(raw)
				} else {
					text = strings.Join(current, "\n")
				}
			}
			draft, ok := s.drafts[rowKey]
			if !ok {
				draft = text
			}
			placeholder := spec.placeholder
			if placeholder == "" {
				placeholder = "每行一个值"
			}
			area := kit.TextArea(c, fmt.Sprint("value", s.clears[rowKey]), &draft, placeholder, spec.json || strings.Contains(spec.key, "regex"))
			area.MinHeight(36).Label(spec.label)
			if area.Changed() {
				s.drafts[rowKey] = draft
				if spec.json {
					put(draft)
				} else {
					put(anyList(strings.Split(draft, "\n")))
				}
			}
		}
		if spec.hint != "" && spec.section == "高级匹配" {
			ui.Text(c, spec.hint).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
		if field == "rule_set" && spec.key != "rule_set" && spec.key != "ip_cidr" {
			ui.Text(c, "保留已有规则集引用，内容随规则集更新。").FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
	})
}

// addressRow is a row of an interface address map.
type addressRow struct {
	name      string
	addresses string
}

// addressMap edits a map of interface (or interface type) to addresses.
func (w *Workspace) addressMap(c *ui.Context, e *ruleEditor, rowKey string, value any, spec fieldSpec, put func(any)) {
	s := &e.cond
	rows, ok := s.maps[rowKey]
	if !ok {
		m, _ := value.(map[string]any)
		if text, isText := value.(string); isText {
			_ = json.Unmarshal([]byte(text), &m)
		}
		for _, name := range sortedKeys(m) {
			rows = append(rows, addressRow{name, strings.Join(model.Values(m[name]), "\n")})
		}
		s.maps[rowKey] = rows
	}
	change := func(next []addressRow) {
		s.maps[rowKey] = next
		if len(next) == 0 {
			put(nil)
			return
		}
		m := map[string]any{}
		for _, r := range next {
			m[r.name] = anyList(strings.Split(r.addresses, "\n"))
		}
		put(m)
	}
	ui.Column(c).Gap(6).Children(func() {
		for i := range rows {
			row := rows[i]
			ui.Row(c.Key(i)).Gap(6).AlignItems(ui.Start).Children(func() {
				name := row.name
				if spec.options != nil {
					opts := []kit.Option{{Value: "", Label: "选择接口类型"}}
					for _, o := range spec.options {
						opts = append(opts, kit.Option{Value: o.value, Label: o.label})
					}
					if kit.Select(c, "name", &name, opts, kit.SelectOptions{Width: 130, Label: fmt.Sprintf("%s类型 %d", spec.label, i+1)}).Changed() {
						next := slices.Clone(rows)
						next[i].name = name
						change(next)
					}
				} else {
					if kit.Input(c, "name", &name, "接口名称，如 en0").Width(130).Label(fmt.Sprintf("%s名称 %d", spec.label, i+1)).Changed() {
						next := slices.Clone(rows)
						next[i].name = name
						change(next)
					}
				}
				addresses := row.addresses
				area := kit.TextArea(c, "addresses", &addresses, "192.168.1.0/24，一行一个地址或网段", false)
				area.MinHeight(52).Grow(1).Label(fmt.Sprintf("%s地址 %d", spec.label, i+1))
				if area.Changed() {
					next := slices.Clone(rows)
					next[i].addresses = addresses
					change(next)
				}
				kit.IconButton(c, kit.SymX, fmt.Sprintf("移除%s %d", spec.label, i+1), kit.IconButtonOptions{Size: 26, IconSize: 12}).OnClick(func() {
					change(slices.Delete(slices.Clone(rows), i, i+1))
				})
			})
		}
		kit.Button(c, "添加接口", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).Label("添加" + spec.label).
			AlignSelf(ui.Start).OnClick(func() { s.maps[rowKey] = append(slices.Clone(rows), addressRow{}) })
	})
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ipVersionField is the IP Version check boxes; none selected is no limit.
func ipVersionField(c *ui.Context, group *model.Condition, set func(*model.Condition)) {
	versions, excluded := model.IPVersionSelection(group)
	fieldHead(c, "IP 版本", len(versions) > 0, excluded, false,
		func() { set(model.SetIPVersions(group, versions, !excluded)) },
		func() { set(model.SetIPVersions(group, nil, false)) })
	ui.Row(c).Gap(18).Label("IP 版本").Children(func() {
		for _, v := range []int{4, 6} {
			on := slices.Contains(versions, v)
			if kit.Checkbox(c.Key(v), &on, fmt.Sprintf("IPv%d", v)).Changed() {
				next := slices.DeleteFunc(slices.Clone(versions), func(x int) bool { return x == v })
				if on {
					next = append(slices.Clone(versions), v)
				}
				set(model.SetIPVersions(group, next, excluded))
			}
		}
	})
}

// ruleSetSelect chooses rule sets by tag; tags no longer offered stay,
// marked "Currently unavailable".
func ruleSetSelect(c *ui.Context, key any, doc *model.Doc, value any) ([]string, bool) {
	selected := model.RuleSetValues(value)
	var options []kit.Option
	known := map[string]bool{}
	for _, o := range model.RuleSetOptions(doc.Route.RuleSets) {
		options = append(options, kit.Option{Value: o.Value, Label: o.Label})
		known[o.Value] = true
	}
	for _, tag := range selected {
		if !known[tag] {
			options = append(options, kit.Option{Value: tag, Label: tag + " · 当前不可用"})
		}
	}
	return multiSelect(c, key, selected, options, "规则集", "选择规则集")
}

// multiSelect chooses several options as the other TAG fields do: chips
// of the chosen ones, wrapping, and a click or typing lists the others,
// filtered by what is typed. It copies the values and pastes them back by
// value or label.
func multiSelect(c *ui.Context, key any, selected []string, options []kit.Option, label, placeholder string) ([]string, bool) {
	labels := map[string]string{}
	for _, o := range options {
		labels[o.Value] = o.Label
	}
	var next []string
	changed := false
	set := func(values []string) { next, changed = values, true }
	ui.Column(c.Key(key)).Shrink(0).Children(func() {
		box := ui.Column(c).Shrink(0)
		query := ui.Local(box, "multi-query", func() string { return "" })
		draft := strings.ToLower(strings.TrimSpace(*query))
		tokens := make([]kit.Token, 0, len(selected))
		for _, v := range selected {
			text := labels[v]
			if text == "" {
				text = v
			}
			// "name · detail" shows the detail muted.
			name, detail, _ := strings.Cut(text, " · ")
			tokens = append(tokens, kit.Token{ID: v, Label: name, Detail: detail, Title: text, Text: v})
		}
		var suggestions []kit.TokenSuggestion
		for _, o := range options {
			if slices.Contains(selected, o.Value) || draft != "" && !strings.Contains(strings.ToLower(o.Label+" "+o.Value), draft) {
				continue
			}
			suggestions = append(suggestions, kit.TokenSuggestion{ID: o.Value, Label: o.Label})
		}
		// find is the option a typed or pasted value names, by value or
		// label.
		find := func(v string) string {
			v = strings.TrimSpace(v)
			for _, o := range options {
				if strings.EqualFold(o.Value, v) || strings.EqualFold(o.Label, v) {
					return o.Value
				}
			}
			return ""
		}
		add := func(values []string) {
			out := slices.Clone(selected)
			for _, v := range values {
				if v != "" && !slices.Contains(out, v) {
					out = append(out, v)
				}
			}
			if len(out) != len(selected) {
				set(out)
			}
		}
		box.Children(func() {
			kit.TokenInput(c, "tokens", query, kit.TokenInputOptions{
				Label: label, Placeholder: placeholder, Tokens: tokens, Suggestions: suggestions, Browse: true,
				Commit: func(suggestion string) bool {
					if suggestion == "" {
						if len(suggestions) == 0 {
							return false
						}
						suggestion = find(*query)
						if suggestion == "" {
							suggestion = suggestions[0].ID
						}
					}
					add([]string{suggestion})
					return true
				},
				// Leaving the field adds only an option named exactly.
				Blur: func() bool {
					if v := find(*query); v != "" {
						add([]string{v})
					}
					return true
				},
				Remove: func(id string) {
					set(slices.DeleteFunc(slices.Clone(selected), func(v string) bool { return v == id }))
				},
				RemoveAll: func(ids []string) {
					set(slices.DeleteFunc(slices.Clone(selected), func(v string) bool { return slices.Contains(ids, v) }))
				},
				Split: kit.SplitTokenLines,
				Paste: func(values []string) {
					var found []string
					for _, v := range values {
						found = append(found, find(v))
					}
					add(found)
				},
			})
		})
	})
	return next, changed
}

// fieldActive reports whether the leaf holds a value for the field key.
func fieldActive(leaf *model.Condition, key string) bool {
	if leaf == nil {
		return false
	}
	_, ok := leaf.Match[key]
	return ok
}
