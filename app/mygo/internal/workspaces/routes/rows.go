package routes

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// section is the page's tab: Traffic Rules or DNS Routing.
type section int

const (
	sectionTraffic section = iota
	sectionDNS
)

func (s section) policy() model.PolicySection {
	if s == sectionDNS {
		return model.SectionDNS
	}
	return model.SectionTraffic
}

// ruleView is what the table shows of a traffic or DNS rule.
type ruleView struct {
	ID, GroupID, Name string
	Enabled           bool
	Action            string
	Summary           string
	Target            string
	// QueryOutbound is a DNS rule's Query Outbound.
	QueryOutbound string
	// targetID and queryID are the outbounds Target and QueryOutbound
	// name, "" for words, for the table to follow their groups.
	targetID, queryID string
	Invalid           bool
	search            string
}

// displayName is a rule's name, "Unnamed rule" without one.
func displayName(name string) string {
	if name == "" {
		return "未命名规则"
	}
	return name
}

// conditionJSON is the condition a search matches, as JSON.stringify of
// the tree or the flat match.
func conditionJSON(tree *model.Condition, match map[string]any) string {
	var raw []byte
	if tree != nil {
		raw, _ = json.Marshal(tree)
	} else {
		raw, _ = json.Marshal(match)
	}
	return string(raw)
}

// trafficTarget is a traffic rule's Target Outbound column.
func trafficTarget(doc *model.Doc, rule *model.RouteRule) string {
	if rule.Action == model.ActionRoute {
		if tag := refTag(doc, rule.Outbound.ID()); tag != "" {
			return tag
		}
		return "请选择出口"
	}
	return map[string]string{
		model.ActionReject:    "拒绝",
		model.ActionResolve:   "解析域名",
		model.ActionSniff:     "嗅探 → 继续",
		model.ActionHijackDNS: "接管 DNS",
	}[rule.Action]
}

// refTag is the tag of an enabled outbound or endpoint by id, "" when it
// is not one (refOptions).
func refTag(doc *model.Doc, id string) string {
	t := model.FindOutboundTarget(doc, id)
	if !t.Valid() || !t.Enabled() {
		return ""
	}
	if t.Outbound != nil && t.Outbound.SubID != "" {
		for _, s := range doc.Subscriptions {
			if s.ID == t.Outbound.SubID && !s.Enabled {
				return ""
			}
		}
	}
	return t.Tag()
}

// outboundChain is the tag of the outbound or endpoint id, followed for a
// selector or urltest by the member it uses, " → " between them, member
// after member: the running core's choice (now, by group tag), else a
// selector's default or first member; a urltest without a choice ends
// the chain. byTag finds the outbounds and endpoints by tag (tagIndex).
// "" when id is not usable.
func outboundChain(doc *model.Doc, id string, now, byTag map[string]string) string {
	tag := refTag(doc, id)
	if tag == "" {
		return ""
	}
	parts := []string{tag}
	seen := map[string]bool{id: true}
	for {
		group := model.FindOutboundTarget(doc, id).Outbound
		if group == nil || !model.IsGroup(group.Type) {
			break
		}
		members := model.GroupMembers(doc, group)
		next := ""
		if chosen := now[tag]; chosen != "" && group.Type != model.ModeLoadBalance {
			next = byTag[chosen]
		} else if group.Type == model.ModeFallback {
			next = model.OfflineCurrent(doc, group, members)
		} else if group.Type == model.ModeManual {
			if def, _ := group.Fields["default_ref"].(string); slices.Contains(members, def) && refTag(doc, def) != "" {
				next = def
			} else {
				for _, m := range members {
					if refTag(doc, m) != "" {
						next = m
						break
					}
				}
			}
		}
		nextTag := refTag(doc, next)
		if next == "" || seen[next] || nextTag == "" {
			break
		}
		parts = append(parts, nextTag)
		seen[next] = true
		id, tag = next, nextTag
	}
	return strings.Join(parts, " → ")
}

// tagIndex maps the document's outbound and endpoint tags to their IDs.
func tagIndex(doc *model.Doc) map[string]string {
	byTag := map[string]string{}
	for _, o := range doc.Outbounds {
		byTag[strings.TrimSpace(o.Tag)] = o.ID
	}
	for _, e := range doc.Endpoints {
		byTag[strings.TrimSpace(e.Tag)] = e.ID
	}
	return byTag
}

// dnsTarget is a DNS rule's Resolver column.
func dnsTarget(doc *model.Doc, rule *model.DNSRule) string {
	if rule.Action == model.ActionReject {
		return "拒绝查询"
	}
	if rule.Action == model.ActionScript {
		if rule.Script == "" {
			return "请选择脚本"
		}
		return "脚本 " + rule.Script
	}
	names := dnsServerNames(doc, rule)
	switch len(names) {
	case 0:
		return "请选择 DNS"
	case 1, 2:
		return strings.Join(names, "、")
	}
	return names[0] + " 等 " + strconv.Itoa(len(names)) + " 个"
}

// dnsServerNames are the names of a DNS rule's servers, in order.
func dnsServerNames(doc *model.Doc, rule *model.DNSRule) []string {
	var names []string
	for _, t := range rule.Targets() {
		name := "请选择 DNS"
		if s := model.DNSServerByID(doc, t.Server.ID()); s != nil && s.Tag != "" {
			name = s.Tag
		}
		names = append(names, name)
	}
	return names
}

// dnsQueryOutbound is a DNS rule's Query Outbound column: the outbound shared by
// its servers, or each distinct one.
func dnsQueryOutbound(doc *model.Doc, rule *model.DNSRule) string {
	if rule.Action == model.ActionReject || rule.Action == model.ActionScript {
		return "—"
	}
	var labels []string
	for _, t := range rule.Targets() {
		if label := dnsTargetDetour(doc, t); !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		return dnsTargetDetour(doc, model.DNSTarget{})
	}
	return strings.Join(labels, "、")
}

// dnsTargetDetour is one server's Query Outbound.
func dnsTargetDetour(doc *model.Doc, t model.DNSTarget) string {
	if t.Detour.Valid() {
		if tag := refTag(doc, t.Detour.ID()); tag != "" {
			return tag
		}
		return "需修正引用"
	}
	if model.DNSTargetSupportsDetour(model.DNSServerByID(doc, t.Server.ID())) {
		return "直连"
	}
	return "协议默认"
}

// dnsQueryID is the outbound every server of a DNS rule queries through,
// "" when they differ or none is set.
func dnsQueryID(doc *model.Doc, rule *model.DNSRule) string {
	id := ""
	for i, t := range rule.Targets() {
		if i > 0 && t.Detour.ID() != id {
			return ""
		}
		id = t.Detour.ID()
	}
	if id == "" || refTag(doc, id) == "" {
		return ""
	}
	return id
}

// trafficViews are the owner's traffic rules in the document's order.
func trafficViews(doc *model.Doc, owner string) []ruleView {
	var out []ruleView
	for _, r := range doc.Route.Rules {
		if r.PolicyOwner() != owner {
			continue
		}
		v := ruleView{ID: r.ID, GroupID: r.GroupID, Name: r.Name, Enabled: r.Enabled, Action: r.Action,
			Summary: model.RuleSummary(r.Match, r.ConditionTree, r.Logic), Target: trafficTarget(doc, r),
			Invalid: model.HasInvalidRuleReferences(doc, r)}
		if r.Action == model.ActionRoute && refTag(doc, r.Outbound.ID()) != "" {
			v.targetID = r.Outbound.ID()
		}
		v.search = strings.ToLower(r.Name + " " + conditionJSON(r.ConditionTree, r.Match))
		out = append(out, v)
	}
	return out
}

// dnsViews are the owner's DNS rules ordered by group.
func dnsViews(doc *model.Doc, owner string) []ruleView {
	var out []ruleView
	for _, r := range model.DNSRulesOf(doc, owner) {
		v := ruleView{ID: r.ID, GroupID: r.GroupID, Name: r.Name, Enabled: r.Enabled, Action: r.Action,
			Summary: model.RuleSummary(r.Match, r.ConditionTree, ""), Target: dnsTarget(doc, r),
			QueryOutbound: dnsQueryOutbound(doc, r), Invalid: model.HasInvalidDNSRuleReferences(doc, r)}
		if r.Action != model.ActionReject && r.Action != model.ActionScript {
			v.queryID = dnsQueryID(doc, r)
		}
		if v.Action == "" {
			v.Action = model.ActionRoute
		}
		v.search = strings.ToLower(r.Name + " " + conditionJSON(r.ConditionTree, r.Match) + " " + strings.Join(dnsServerNames(doc, r), " ") + " " + v.QueryOutbound)
		out = append(out, v)
	}
	return out
}

// rowKind is what a row of the policy table shows.
type rowKind int

const (
	rowGroup rowKind = iota
	rowRule
	// rowEmptyGroup stands in a group without rules.
	rowEmptyGroup
	// rowEmpty is the table's empty state.
	rowEmpty
	// rowPinned is the default outbound or fallback DNS.
	rowPinned
	// rowModuleGroup heads the read-only rules of a module in Pre-rules.
	rowModuleGroup
	// rowModuleRule is a routing rule a module brings, read-only.
	rowModuleRule
)

// tableRow is a row of the policy table.
type tableRow struct {
	kind  rowKind
	group *model.RouteGroup
	rule  ruleView
	// ordinal is a rule's 1-based position among the owner's rules.
	ordinal int
	// count is a group's rule count.
	count int
	// stripe tints every other rule row.
	stripe bool
	// index is the row's position in the table.
	index int
	// moduleID and moduleName say which module a rowModuleGroup or
	// rowModuleRule belongs to.
	moduleID, moduleName string
}

// key names the row: its kind and its group or rule.
func (r tableRow) key() string {
	switch r.kind {
	case rowGroup:
		return "group:" + r.group.ID
	case rowRule:
		return "rule:" + r.rule.ID
	case rowEmptyGroup:
		return "empty:" + r.group.ID
	case rowModuleGroup:
		return "module:" + r.moduleID
	case rowModuleRule:
		return "rule:" + r.rule.ID
	case rowEmpty:
		return "empty"
	}
	return "pinned"
}

// policyTable is the policy table of an owner and a section.
type policyTable struct {
	rows   []tableRow
	groups []*model.RouteGroup
	// all is the owner's rule count, shown the number matching the search.
	all, shown int
}

// buildTable lays out the owner's groups and rules for the table: each
// group heads its rules (or an empty row), the empty state follows when
// nothing shows, and the default row ends it except for the pre device.
func buildTable(doc *model.Doc, sec section, owner, search string) policyTable {
	query := strings.ToLower(search)
	var groups []*model.RouteGroup
	var all []ruleView
	traffic := sec == sectionTraffic
	if traffic {
		groups = model.RouteGroupsOf(doc, owner)
		all = trafficViews(doc, owner)
	} else {
		groups = model.DNSGroupsOf(doc, owner)
		all = dnsViews(doc, owner)
	}
	// Traffic rules without a group show in the first group.
	inGroup := func(r ruleView, g *model.RouteGroup) bool {
		return r.GroupID == g.ID || traffic && r.GroupID == "" && len(groups) > 0 && g.ID == groups[0].ID
	}
	ordinal := map[string]int{}
	if traffic {
		// Shared with the emitter, so connections name rules by these numbers.
		ordinal = model.RouteRuleOrdinals(doc, owner)
	} else {
		for i, r := range all {
			ordinal[r.ID] = i + 1
		}
	}
	var matched []ruleView
	for _, r := range all {
		if strings.Contains(r.search, query) {
			matched = append(matched, r)
		}
	}
	t := policyTable{groups: groups, all: len(all), shown: len(matched)}
	stripe := false
	for _, g := range groups {
		var items []ruleView
		for _, r := range matched {
			if inGroup(r, g) {
				items = append(items, r)
			}
		}
		if search != "" && len(items) == 0 {
			continue
		}
		t.rows = append(t.rows, tableRow{kind: rowGroup, group: g, count: len(items)})
		for _, r := range items {
			t.rows = append(t.rows, tableRow{kind: rowRule, group: g, rule: r, ordinal: ordinal[r.ID], stripe: stripe})
			stripe = !stripe
		}
		if len(items) == 0 {
			t.rows = append(t.rows, tableRow{kind: rowEmptyGroup, group: g})
		}
	}
	modules := moduleRows(doc, sec, owner, query, stripe)
	empty := traffic && (len(groups) == 0 && search == "" || search != "" && len(matched) == 0 && len(modules) == 0) ||
		!traffic && (search != "" && len(matched) == 0 || len(all) == 0 && search == "")
	if empty {
		t.rows = append(t.rows, tableRow{kind: rowEmpty})
	}
	t.rows = append(t.rows, modules...)
	for _, r := range modules {
		if r.kind == rowModuleRule {
			t.shown++
		}
	}
	if traffic && owner == model.PreDeviceID {
		t.all += len(model.ModuleRouteRules(doc))
	}
	if owner != model.PreDeviceID {
		t.rows = append(t.rows, tableRow{kind: rowPinned, ordinal: len(ordinal) + 1})
	}
	return t
}

// moduleRuleView is what the table shows of a module's routing rule.
func moduleRuleView(doc *model.Doc, r *model.RouteRule, name string) ruleView {
	target := "直连"
	if r.Action == model.ActionReject {
		target = "拒绝"
	}
	summary := model.RuleSummary(r.Match, nil, "")
	v := ruleView{ID: r.ID, Name: r.Name, Enabled: true, Action: r.Action, Summary: summary, Target: target,
		search: strings.ToLower(name + " " + r.Name + " " + conditionJSON(nil, r.Match))}
	if tag := refTag(doc, r.Outbound.ID()); r.Action == model.ActionRoute && tag != "" {
		v.Target, v.targetID = tag, r.Outbound.ID()
	}
	return v
}

// moduleRows are the read-only groups of the enabled modules' routing rules
// that follow the user's own Pre-rules: a heading per module, then its rules,
// numbered on from the user's rules. They show in Pre-rules' traffic tab,
// those matching the search only.
func moduleRows(doc *model.Doc, sec section, owner, query string, stripe bool) []tableRow {
	if sec != sectionTraffic || owner != model.PreDeviceID {
		return nil
	}
	rules := model.ModuleRouteRules(doc)
	if len(rules) == 0 {
		return nil
	}
	ordinal := model.ModuleRuleOrdinals(doc)
	var rows []tableRow
	var head int
	currentModule := ""
	for _, r := range rules {
		id, _, _ := model.ModuleOf(r.ID)
		m := doc.HTTPTools.FindModule(id)
		if m == nil {
			continue
		}
		v := moduleRuleView(doc, r, m.Name)
		if !strings.Contains(v.search, query) {
			continue
		}
		if id != currentModule {
			currentModule = id
			head = len(rows)
			rows = append(rows, tableRow{kind: rowModuleGroup, moduleID: id, moduleName: m.Name})
		}
		rows[head].count++
		rows = append(rows, tableRow{kind: rowModuleRule, rule: v, ordinal: ordinal[r.ID], stripe: stripe, moduleID: id, moduleName: m.Name})
		stripe = !stripe
	}
	return rows
}

// reorderPlan is what dropping a row before row `to` does: a group moves
// before another (or to the end), a rule moves into a group, before or
// after a rule there, or last.
type reorderPlan struct {
	group string
	// beforeGroup is the group the moved group goes before, "" for the
	// owner's end.
	beforeGroup string

	rule     string
	toGroup  string
	target   string
	position string
}

// planReorder maps a list reorder of row from before row to.
func (t policyTable) planReorder(from, to int) (reorderPlan, bool) {
	if from < 0 || from >= len(t.rows) {
		return reorderPlan{}, false
	}
	src := t.rows[from]
	switch src.kind {
	case rowGroup:
		plan := reorderPlan{group: src.group.ID}
		for i := max(to, 0); i < len(t.rows); i++ {
			if t.rows[i].kind == rowGroup {
				plan.beforeGroup = t.rows[i].group.ID
				break
			}
		}
		return plan, plan.beforeGroup != plan.group
	case rowRule:
		plan := reorderPlan{rule: src.rule.ID}
		var dst tableRow
		if to >= 0 && to < len(t.rows) {
			dst = t.rows[to]
		} else {
			dst = tableRow{kind: rowPinned}
		}
		switch dst.kind {
		case rowRule:
			plan.toGroup, plan.target, plan.position = dst.group.ID, dst.rule.ID, model.PositionBefore
		case rowEmptyGroup:
			plan.toGroup = dst.group.ID
		default:
			// Before a group's heading, or past the groups: the end of the
			// group above.
			var above *model.RouteGroup
			last := ""
			for i := 0; i < min(to, len(t.rows)); i++ {
				switch r := t.rows[i]; r.kind {
				case rowGroup:
					above, last = r.group, ""
				case rowRule:
					if r.rule.ID != src.rule.ID {
						last = r.rule.ID
					}
				}
			}
			if above == nil {
				// Before the first heading: before the first group's first
				// rule other than the one moving.
				first := slices.IndexFunc(t.rows, func(r tableRow) bool { return r.kind == rowGroup })
				if first < 0 {
					return plan, false
				}
				plan.toGroup = t.rows[first].group.ID
				for _, r := range t.rows[first+1:] {
					if r.kind != rowRule {
						break
					}
					if r.rule.ID != src.rule.ID {
						plan.target, plan.position = r.rule.ID, model.PositionBefore
						break
					}
				}
				return plan, true
			}
			plan.toGroup = above.ID
			if last != "" {
				plan.target, plan.position = last, model.PositionAfter
			}
		}
		return plan, plan.toGroup != ""
	}
	return reorderPlan{}, false
}

// tableOrder is the order the table shows the owner's groups and rules
// in, which a reorder must change to be saved.
func tableOrder(doc *model.Doc, sec section, owner string) []string {
	var out []string
	for _, r := range buildTable(doc, sec, owner, "").rows {
		key := r.key()
		if r.kind == rowRule {
			key += "@" + r.group.ID
		}
		out = append(out, key)
	}
	return out
}

// reorder carries out a plan on the current device's section, saving
// only a change of the order shown.
func (w *Workspace) reorder(plan reorderPlan) {
	sec, owner := w.sec, w.owner
	next := model.Clone(w.doc())
	applyReorder(next, sec, owner, plan)
	if slices.Equal(tableOrder(w.doc(), sec, owner), tableOrder(next, sec, owner)) {
		return
	}
	w.save(func(doc *model.Doc) error { *doc = *next; return nil })
}

// applyReorder carries out a plan on the section of doc.
func applyReorder(doc *model.Doc, sec section, owner string, plan reorderPlan) {
	if plan.group != "" {
		groups := &doc.Route.Groups
		if sec == sectionDNS {
			groups = &doc.DNS.Groups
		}
		*groups = moveGroup(*groups, owner, plan.group, plan.beforeGroup)
		return
	}
	ownsGroup := func(groups []*model.RouteGroup) bool {
		for _, g := range groups {
			if g.ID == plan.toGroup && model.DeviceOwner(g.DeviceID) == owner {
				return true
			}
		}
		return false
	}
	if sec == sectionDNS {
		if !ownsGroup(doc.DNS.Groups) {
			return
		}
		for _, r := range doc.DNS.Rules {
			if r.ID == plan.rule && r.PolicyOwner() == owner {
				doc.DNS.Rules = model.MovePolicyRule(doc.DNS.Rules, plan.rule, plan.toGroup, plan.target, plan.position)
				return
			}
		}
		return
	}
	if !ownsGroup(doc.Route.Groups) {
		return
	}
	for _, r := range doc.Route.Rules {
		if r.ID == plan.rule && r.PolicyOwner() == owner {
			doc.Route.Rules = model.MovePolicyRule(doc.Route.Rules, plan.rule, plan.toGroup, plan.target, plan.position)
			return
		}
	}
}

// moveGroup moves group id of owner before group before ("" after the
// owner's last group), within the document's list of every owner's groups.
func moveGroup(groups []*model.RouteGroup, owner, id, before string) []*model.RouteGroup {
	from := -1
	for i, g := range groups {
		if g.ID == id && model.DeviceOwner(g.DeviceID) == owner {
			from = i
		}
	}
	if from < 0 {
		return groups
	}
	moved := groups[from]
	next := make([]*model.RouteGroup, 0, len(groups))
	next = append(next, groups[:from]...)
	next = append(next, groups[from+1:]...)
	at := -1
	for i, g := range next {
		if before != "" && g.ID == before && model.DeviceOwner(g.DeviceID) == owner {
			at = i
		}
	}
	if at < 0 {
		// After the owner's last group.
		at = len(next)
		for i := len(next) - 1; i >= 0; i-- {
			if model.DeviceOwner(next[i].DeviceID) == owner {
				at = i + 1
				break
			}
		}
	}
	out := make([]*model.RouteGroup, 0, len(groups))
	out = append(out, next[:at]...)
	out = append(out, moved)
	return append(out, next[at:]...)
}
