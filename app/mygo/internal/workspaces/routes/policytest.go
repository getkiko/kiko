package routes

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// This file holds a local evaluation of the
// traffic and DNS policy for a sample connection or query, telling which
// rule matches, the default taken, or the conditions it cannot evaluate
// without the core (then both outcomes). The workspace's "Test rules" dialog
// asks the running core (POST /policy/test); this evaluation answers
// without one.

// Decisions of a policy test.
const (
	decisionMatched = "matched"
	decisionDefault = "default"
	decisionUnknown = "unknown"
)

// policyTestInput is a sample: the device it comes from, its inbound, the
// domain and the destination IP (for DNS, an assumed response IP). facts
// are what else the sample tells by condition field (sampleFacts), which
// decides the conditions on them that are otherwise unknown.
type policyTestInput struct {
	deviceID, inboundID, domain, ip string
	facts                           map[string]string
}

// routeTestResult is a traffic test's outcome.
type routeTestResult struct {
	decision          string
	rule              *model.RouteRule
	outboundID        string
	hasOutbound       bool
	usedCoreDefault   bool
	unknownConditions []string
	appliedRuleNames  []string
	ifMatched         *routeTestResult
	otherwise         *routeTestResult
}

// dnsTestResult is a DNS test's outcome.
type dnsTestResult struct {
	decision          string
	rule              *model.DNSRule
	dnsServerID       string
	dnsDetourID       string
	usedCoreDefault   bool
	unknownConditions []string
	appliedRuleNames  []string
	invalidReference  string
	ifMatched         *dnsTestResult
	otherwise         *dnsTestResult
}

type matchState int

const (
	stateMatch matchState = iota
	stateMiss
	stateUnknown
)

type matchResult struct {
	state   matchState
	unknown []string
}

// matchGroups are the condition fields the core matches as one: a leaf
// meets a group when any of its fields matches, as sing-box's rule items
// do for the destination address, source address and the two ports.
var matchGroups = [][]string{
	{"domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr", "ip_is_private"},
	{"source_ip_cidr", "source_ip_is_private"},
	{"source_port", "source_port_range"},
	{"port", "port_range"},
}

func invertResult(r matchResult) matchResult {
	switch r.state {
	case stateMatch:
		r.state = stateMiss
	case stateMiss:
		r.state = stateMatch
	}
	return r
}

func combine(mode string, values []matchResult) matchResult {
	terminal, all := stateMiss, stateMatch
	if mode != "and" {
		terminal, all = stateMatch, stateMiss
	}
	if slices.ContainsFunc(values, func(v matchResult) bool { return v.state == terminal }) {
		return matchResult{state: terminal}
	}
	if !slices.ContainsFunc(values, func(v matchResult) bool { return v.state != all }) {
		return matchResult{state: all}
	}
	var unknown []string
	for _, v := range values {
		for _, u := range v.unknown {
			if !slices.Contains(unknown, u) {
				unknown = append(unknown, u)
			}
		}
	}
	return matchResult{state: stateUnknown, unknown: unknown}
}

var reCandidates = regexp.MustCompile(`[\n,]`)

func candidates(value any) []string {
	var raw []string
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		for _, item := range v {
			raw = append(raw, fmt.Sprint(item))
		}
	case []string:
		raw = v
	default:
		raw = reCandidates.Split(fmt.Sprint(v), -1)
	}
	var out []string
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var reSuffixPrefix = regexp.MustCompile(`^(?:\*\.|\.)`)

func normalizedDomain(value string) string {
	if host := model.NormalizeDomainName(value); host != "" {
		return host
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

// matchesDomain matches a domain field's pattern; nil for an invalid
// regex.
func matchesDomain(field, pattern, domain string) *bool {
	host := normalizedDomain(domain)
	result := func(b bool) *bool { return &b }
	switch field {
	case "domain":
		return result(normalizedDomain(pattern) == host)
	case "domain_suffix":
		suffix := normalizedDomain(reSuffixPrefix.ReplaceAllString(pattern, ""))
		return result(host == suffix || strings.HasSuffix(host, "."+suffix))
	case "domain_keyword":
		return result(strings.Contains(host, strings.ToLower(pattern)))
	}
	compiled, _, problem := model.CompileDomainRegex(pattern)
	if problem != "" {
		return nil
	}
	return result(compiled.MatchString(host))
}

func evaluateField(field string, value any, doc *model.Doc, in policyTestInput, dns bool) matchResult {
	values := candidates(value)
	if len(values) == 0 {
		return matchResult{state: stateMiss}
	}
	switch {
	case field == "script":
		// Scripts run in the core only: the condition may or may not hold.
		return matchResult{state: stateUnknown, unknown: []string{"脚本 " + strings.Join(values, "、")}}
	case field == "inbound":
		if in.inboundID == "" {
			return matchResult{state: stateUnknown, unknown: []string{"进站"}}
		}
		names := []string{in.inboundID}
		for _, ib := range doc.Inbounds {
			if ib.ID == in.inboundID {
				names = append(names, ib.Tag)
			}
		}
		for _, ep := range doc.Endpoints {
			if ep.ID == in.inboundID {
				names = append(names, ep.Tag)
			}
		}
		if slices.ContainsFunc(values, func(v string) bool { return slices.Contains(names, v) }) {
			return matchResult{state: stateMatch}
		}
		return matchResult{state: stateMiss}
	case field == "preferred_by":
		if dns && in.domain == "" || !dns && in.domain == "" && in.ip == "" {
			return matchResult{state: stateMiss}
		}
		names := make([]string, len(values))
		for i, id := range values {
			names[i] = id
			if dns {
				if s := model.DNSServerByID(doc, id); s != nil && s.Tag != "" {
					names[i] = s.Tag
				}
			} else if t := model.FindOutboundTarget(doc, id); t.Valid() && t.Tag() != "" {
				names[i] = t.Tag()
			}
		}
		var target string
		if dns {
			target = "域名 " + in.domain
		} else {
			var parts []string
			if in.domain != "" {
				parts = append(parts, "域名 "+in.domain)
			}
			if in.ip != "" {
				parts = append(parts, "IP "+in.ip)
			}
			target = strings.Join(parts, "、")
		}
		basis := "内部域名、节点和子网路由"
		if dns {
			basis = "内部域名和搜索域"
		}
		return matchResult{state: stateUnknown, unknown: []string{fmt.Sprintf("「%s」当前是否优先处理%s（依据%s）", strings.Join(names, "、"), target, basis)}}
	case slices.Contains(model.DomainFields, field):
		if strings.TrimSpace(in.domain) == "" {
			return matchResult{state: stateUnknown, unknown: []string{"域名"}}
		}
		invalid := false
		for _, pattern := range values {
			m := matchesDomain(field, pattern, in.domain)
			if m != nil && *m {
				return matchResult{state: stateMatch}
			}
			invalid = invalid || m == nil
		}
		if invalid {
			return matchResult{state: stateUnknown, unknown: []string{"域名正则"}}
		}
		return matchResult{state: stateMiss}
	case field == "ip_cidr":
		if strings.TrimSpace(in.ip) == "" {
			label := "目标 IP"
			if dns {
				label = "DNS 响应 IP"
			}
			return matchResult{state: stateUnknown, unknown: []string{label}}
		}
		invalid := false
		for _, network := range values {
			if _, ok := model.ParseNetwork(network); !ok {
				invalid = true
				continue
			}
			if model.AddressInNetwork(in.ip, network) {
				return matchResult{state: stateMatch}
			}
		}
		if invalid {
			label := "目标 IP 条件"
			if dns {
				label = "DNS 响应 IP 条件"
			}
			return matchResult{state: stateUnknown, unknown: []string{label}}
		}
		return matchResult{state: stateMiss}
	}
	if r, ok := evaluateFact(field, values, in); ok {
		return r
	}
	label := model.ConditionLabels[field]
	if label == "" {
		label = field
	}
	return matchResult{state: stateUnknown, unknown: []string{label}}
}

func evaluateLeaf(node *model.Condition, doc *model.Doc, in policyTestInput, dns bool) matchResult {
	// A rule set can contribute any match group, including destination
	// addresses.
	if _, ok := node.Match["rule_set"]; ok {
		return matchResult{state: stateUnknown, unknown: []string{"规则集"}}
	}
	var fields []matchResult
	grouped := make([][]matchResult, len(matchGroups))
	for _, key := range sortedMatchKeys(node.Match) {
		r := evaluateField(key, node.Match[key], doc, in, dns)
		if i := slices.IndexFunc(matchGroups, func(g []string) bool { return slices.Contains(g, key) }); i >= 0 {
			grouped[i] = append(grouped[i], r)
		} else {
			fields = append(fields, r)
		}
	}
	for _, g := range grouped {
		if len(g) > 0 {
			fields = append(fields, combine("or", g))
		}
	}
	matched := combine("and", fields)
	if node.Invert {
		return invertResult(matched)
	}
	return matched
}

func evaluateCondition(node *model.Condition, doc *model.Doc, in policyTestInput, dns bool) matchResult {
	if node.Kind == model.ConditionMatch {
		return evaluateLeaf(node, doc, in, dns)
	}
	children := make([]matchResult, len(node.Rules))
	for i, r := range node.Rules {
		children[i] = evaluateCondition(r, doc, in, dns)
	}
	value := combine(node.Mode, children)
	if node.Invert {
		return invertResult(value)
	}
	return value
}

func evaluateRule(tree *model.Condition, catchAll bool, doc *model.Doc, in policyTestInput, dns bool) matchResult {
	if len(tree.Rules) == 0 {
		if catchAll {
			return matchResult{state: stateMatch}
		}
		return matchResult{state: stateMiss}
	}
	return evaluateCondition(tree, doc, in, dns)
}

// policyRule is what the evaluation needs of a traffic or DNS rule.
type policyRule struct {
	id, name, action string
	enabled          bool
	tree             *model.Condition
	catchAll         bool
	// script is the dns script of a script action; the core alone knows
	// what it answers.
	script string
}

func (r policyRule) label() string {
	if r.name != "" {
		return r.name
	}
	return r.id
}

// ruleEvaluation is the first rule deciding a sample: matched, unknown
// (with the rules after it), or none (the default).
type ruleEvaluation struct {
	decision  string
	index     int
	unknown   []string
	applied   []string
	remaining []policyRule
}

func evaluateRules(rules []policyRule, doc *model.Doc, in policyTestInput, dns bool, prior []string) ruleEvaluation {
	applied := slices.Clone(prior)
	for i, r := range rules {
		if !r.enabled {
			continue
		}
		m := evaluateRule(r.tree, r.catchAll, doc, in, dns)
		if dns && r.action == model.ActionScript && m.state != stateMiss {
			unknown := append(slices.Clone(m.unknown), "脚本 "+r.script)
			return ruleEvaluation{decision: decisionUnknown, index: i, unknown: unknown, applied: applied, remaining: rules[i+1:]}
		}
		switch m.state {
		case stateMiss:
			continue
		case stateUnknown:
			return ruleEvaluation{decision: decisionUnknown, index: i, unknown: m.unknown, applied: applied, remaining: rules[i+1:]}
		}
		if !dns && (r.action == model.ActionSniff || r.action == model.ActionResolve) {
			applied = append(applied, r.label())
			continue
		}
		return ruleEvaluation{decision: decisionMatched, index: i, applied: applied}
	}
	return ruleEvaluation{decision: decisionDefault, applied: applied}
}

// orderedPolicy is Pre-rules' rules then the device's, in group order.
func orderedRoutePolicy(doc *model.Doc, device string) ([]policyRule, []*model.RouteRule) {
	rules := model.OrderedDeviceRules(doc.Route.Rules, doc.Route.Groups, model.PreDeviceID)
	// The modules' rules follow the user's Pre-rules, as the core applies them.
	rules = append(rules, moduleRoutes(doc)...)
	if device != model.PreDeviceID {
		rules = append(rules, model.OrderedDeviceRules(doc.Route.Rules, doc.Route.Groups, device)...)
	}
	out := make([]policyRule, len(rules))
	for i, r := range rules {
		out[i] = policyRule{r.ID, r.Name, r.Action, r.Enabled, model.RuleCondition(r.Match, r.ConditionTree, r.Logic), r.CatchAll, ""}
	}
	return out, rules
}

func orderedDNSPolicy(doc *model.Doc, device string) ([]policyRule, []*model.DNSRule) {
	rules := model.OrderedDeviceRules(doc.DNS.Rules, doc.DNS.Groups, model.PreDeviceID)
	if device != model.PreDeviceID {
		rules = append(rules, model.OrderedDeviceRules(doc.DNS.Rules, doc.DNS.Groups, device)...)
	}
	// The dns scripts of the enabled modules run first, on every query, as the emitter puts them.
	rules = append(moduleDNSScriptRules(doc), rules...)
	out := make([]policyRule, len(rules))
	for i, r := range rules {
		out[i] = policyRule{r.ID, r.Name, r.Action, r.Enabled, model.RuleCondition(r.Match, r.ConditionTree, ""), r.CatchAll, r.Script}
	}
	return out, rules
}

// moduleDNSScriptRules are the catch-all script rules of the dns scripts of the enabled modules.
// Like the emitter, it leaves out disabled and invalid scripts and tags the core already has.
func moduleDNSScriptRules(doc *model.Doc) []*model.DNSRule {
	var out []*model.DNSRule
	seen := map[string]bool{}
	count := 0
	usable := func(s *model.HTTPScript) bool {
		return s != nil && !s.Disabled && s.Issue() == ""
	}
	if doc.HTTPTools != nil {
		for _, s := range doc.HTTPTools.Scripts {
			if usable(s) && !seen[s.EffectiveTag()] && count < model.MaxScripts {
				seen[s.EffectiveTag()] = true
				count++
			}
		}
	}
	for _, a := range model.ActiveModules(doc.HTTPTools) {
		for _, sc := range model.ModuleScripts(a) {
			if !usable(sc) || seen[sc.Tag] || count >= model.MaxScripts {
				continue
			}
			seen[sc.Tag] = true
			count++
			if sc.Type != model.ScriptDNS {
				continue
			}
			out = append(out, &model.DNSRule{ID: "mod:" + sc.Tag, Name: a.Module.Name, Enabled: true, CatchAll: true, Action: model.ActionScript, Script: sc.Tag})
		}
	}
	return out
}

// defaultRouteOutbound is a device's default outbound; local and other
// fall back to the core's first enabled outbound.
func defaultRouteOutbound(doc *model.Doc, device string) (string, bool) {
	var selected string
	switch device {
	case model.LocalDeviceID:
		selected = doc.Route.LocalFinal.Or(doc.Route.Final).ID()
	case model.OtherDeviceID:
		selected = doc.Route.Final.ID()
	default:
		selected, _ = ownFinal(doc, device)
	}
	if selected != "" {
		return selected, false
	}
	if device != model.LocalDeviceID && device != model.OtherDeviceID {
		return "", false
	}
	enabledSubs := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			enabledSubs[s.ID] = true
		}
	}
	for _, t := range model.AllOutboundTargets(doc) {
		if t.Enabled() && (t.Outbound == nil || t.Outbound.SubID == "" || enabledSubs[t.Outbound.SubID]) {
			return t.ID(), true
		}
	}
	return "", true
}

func routeResult(doc *model.Doc, in policyTestInput, rules []*model.RouteRule, offset int, ev ruleEvaluation, branches bool) *routeTestResult {
	switch ev.decision {
	case decisionUnknown:
		rule := rules[offset+ev.index]
		out := &routeTestResult{decision: decisionUnknown, rule: rule, unknownConditions: ev.unknown, appliedRuleNames: ev.applied}
		if branches {
			next := offset + ev.index + 1
			remaining := ev.remaining
			if rule.Action == model.ActionSniff || rule.Action == model.ActionResolve {
				name := rule.Name
				if name == "" {
					name = rule.ID
				}
				out.ifMatched = routeResult(doc, in, rules, next, evaluateRules(remaining, doc, in, false, append(slices.Clone(ev.applied), name)), false)
			} else {
				out.ifMatched = routeResult(doc, in, rules, offset, ruleEvaluation{decision: decisionMatched, index: ev.index, applied: ev.applied}, false)
			}
			out.otherwise = routeResult(doc, in, rules, next, evaluateRules(remaining, doc, in, false, ev.applied), false)
		}
		return out
	case decisionMatched:
		rule := rules[offset+ev.index]
		out := &routeTestResult{decision: decisionMatched, rule: rule, appliedRuleNames: ev.applied}
		if rule.Action == model.ActionRoute {
			out.outboundID, out.hasOutbound = rule.Outbound.ID(), true
		}
		return out
	}
	id, core := defaultRouteOutbound(doc, in.deviceID)
	return &routeTestResult{decision: decisionDefault, outboundID: id, hasOutbound: true, usedCoreDefault: core, appliedRuleNames: ev.applied}
}

// testRoutePolicy evaluates the traffic policy for a sample connection.
func testRoutePolicy(doc *model.Doc, in policyTestInput) *routeTestResult {
	rules, stored := orderedRoutePolicy(doc, in.deviceID)
	return routeResult(doc, in, stored, 0, evaluateRules(rules, doc, in, false, nil), true)
}

// dnsReferenceError tells why a DNS target cannot be used, "" when it can.
func dnsReferenceError(doc *model.Doc, id, detour string, usedCoreDefault bool) string {
	if id == "" {
		switch {
		case detour != "":
			return "查询出口需要可用的 DNS 服务器"
		case usedCoreDefault:
			return ""
		}
		return "DNS 服务器尚未选择"
	}
	server := model.DNSServerByID(doc, id)
	if server == nil || !server.Enabled {
		tag := id
		if server != nil && server.Tag != "" {
			tag = server.Tag
		}
		return "DNS 服务器「" + tag + "」已删除或停用"
	}
	if detour != "" && !model.DNSTargetSupportsDetour(server) {
		return "DNS「" + server.Tag + "」的查询出口与服务器类型不兼容"
	}
	if detour != "" {
		return model.DNSDetourError(doc, detour)
	}
	return ""
}

func dnsResult(doc *model.Doc, in policyTestInput, rules []*model.DNSRule, offset int, ev ruleEvaluation, branches bool) *dnsTestResult {
	switch ev.decision {
	case decisionUnknown:
		rule := rules[offset+ev.index]
		out := &dnsTestResult{decision: decisionUnknown, rule: rule, unknownConditions: ev.unknown, appliedRuleNames: ev.applied}
		if branches {
			out.ifMatched = dnsResult(doc, in, rules, offset, ruleEvaluation{decision: decisionMatched, index: ev.index, applied: ev.applied}, false)
			out.otherwise = dnsResult(doc, in, rules, offset+ev.index+1, evaluateRules(ev.remaining, doc, in, true, ev.applied), false)
		}
		return out
	case decisionMatched:
		rule := rules[offset+ev.index]
		out := &dnsTestResult{decision: decisionMatched, rule: rule, appliedRuleNames: ev.applied}
		if rule.Action != model.ActionReject && rule.Action != model.ActionScript {
			out.dnsServerID, out.dnsDetourID = rule.Server.ID(), rule.Detour.ID()
			targets := rule.Targets()
			if len(targets) == 0 {
				targets = []model.DNSTarget{{}}
			}
			for _, t := range targets {
				if out.invalidReference = dnsReferenceError(doc, t.Server.ID(), t.Detour.ID(), false); out.invalidReference != "" {
					break
				}
			}
		}
		return out
	}
	server := model.DNSFinalFor(doc, in.deviceID).ID()
	core := server == ""
	if core {
		if s := model.CoreDNSServer(doc); s != nil {
			server = s.ID
		}
	}
	detour := model.DNSFinalDetourFor(doc, in.deviceID).ID()
	return &dnsTestResult{decision: decisionDefault, dnsServerID: server, dnsDetourID: detour, usedCoreDefault: core,
		invalidReference: dnsReferenceError(doc, server, detour, core), appliedRuleNames: ev.applied}
}

// testDNSPolicy evaluates the DNS policy for a sample query.
func testDNSPolicy(doc *model.Doc, in policyTestInput) *dnsTestResult {
	rules, stored := orderedDNSPolicy(doc, in.deviceID)
	return dnsResult(doc, in, stored, 0, evaluateRules(rules, doc, in, true, nil), true)
}
