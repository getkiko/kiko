package emit

import (
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// buildDNSServer emits one server instance: the server with one query
// detour.
func (e *emitter) buildDNSServer(instance *dnsInstance) *object {
	s := model.DNSServerForType(instance.server, instance.server.Type)
	if problem := model.FakeIPRangeError(s); problem != "" {
		e.errors = append(e.errors, "DNS「"+s.Tag+"」："+problem)
	}
	o := obj("type", s.Type, "tag", instance.tag)
	if s.Type == "tailscale" {
		endpointID := jsv.Text(s.Fields["endpoint"])
		endpointTag, ok := e.resolve(endpointID)
		if ok && endpointTag != "" {
			o.set("endpoint", endpointTag)
		}
		if s.Fields["accept_default_resolvers"] == true {
			o.set("accept_default_resolvers", true)
		}
		if s.Fields["accept_search_domain"] == true {
			o.set("accept_search_domain", true)
		}
		if endpointID != "" && !ok {
			e.warnings = append(e.warnings, "DNS「"+instance.tag+"」引用的 Tailscale 端点已删除或停用")
		}
		return prune(o).(*object)
	}
	if s.Type != "dhcp" {
		if s.Server != "" {
			o.set("server", s.Server)
		}
		if s.ServerPort != nil && *s.ServerPort != 0 {
			o.set("server_port", float64(*s.ServerPort))
		}
		if s.Path != "" {
			o.set("path", s.Path)
		}
	}
	dialerValues := model.Fields{}
	for k, v := range s.Fields {
		dialerValues[k] = v
	}
	set := func(key string, value any, ok bool) {
		if ok {
			dialerValues[key] = value
		} else {
			delete(dialerValues, key)
		}
	}
	set("server", s.Server, s.Server != "")
	set("detour", instance.detour, instance.detour != "")
	set("domain_resolver.server", s.DomainResolver.ID(), s.DomainResolver.Valid())
	set("domain_resolver.detour", s.DomainResolverDetour.ID(), s.DomainResolverDetour.Valid())
	set("domain_resolver.strategy", s.DomainStrategy, s.DomainStrategy != "")
	for _, key := range []string{"disable_cache", "rewrite_ttl", "client_subnet"} {
		v, ok := s.Fields["domain_resolver_"+key]
		set("domain_resolver."+key, v, ok)
	}
	if instance.detour != "" {
		detourTag, _ := e.resolve(instance.detour)
		var detourOutbound *object
		for _, out := range e.outbounds {
			if out.(*object).get("tag") == detourTag {
				detourOutbound = out.(*object)
				break
			}
		}
		if detourOutbound != nil && detourOutbound.get("type") == "direct" &&
			!slices.ContainsFunc(detourOutbound.keys, func(k string) bool { return k != "type" && k != "tag" }) {
			delete(dialerValues, "detour")
		} else if !slices.ContainsFunc(append(slices.Clone(e.outbounds), e.endpoints...), func(out any) bool {
			return out.(*object).get("tag") == detourTag && detourTag != ""
		}) {
			e.dnsError("DNS「" + s.Tag + "」的查询出口未生成有效配置，请检查出口及分组成员")
		}
	}
	protocol := model.Fields{}
	for k, v := range s.Fields {
		protocol[k] = v
	}
	for _, f := range schema.DNSDialerSchema {
		delete(protocol, f.Key)
	}
	for _, k := range []string{"domain_resolver_disable_cache", "domain_resolver_rewrite_ttl", "domain_resolver_client_subnet", "domain_resolver.detour", "udp_timeout"} {
		delete(protocol, k)
	}
	o.assign(fromMap(protocol))
	if model.IsDialerDNS(s.Type) {
		applyFields(o, dialerValues, schema.DNSDialerSchema, e.resolve, e.platform, e.noteSkip("DNS「"+instance.tag+"」"), nil, e.resolveDNS)
	}
	return prune(o).(*object)
}

var listDNSMatchKeys = []string{"domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr", "query_type", "network", "network_type", "protocol"}

func (e *emitter) convertDNSMatch(raw map[string]any) *object {
	m := obj()
	for _, k := range sortedKeys(raw) {
		v := raw[k]
		if isEmpty(v) {
			continue
		}
		switch {
		case k == "rule_set":
			var sets []string
			for _, t := range splitList(v) {
				if e.rsTags.has(t) {
					sets = append(sets, t)
				} else {
					e.errors = append(e.errors, "DNS 规则引用的规则集「"+t+"」缺失或尚未拉取")
				}
			}
			if len(sets) == 0 {
				e.errors = append(e.errors, "DNS 规则引用的规则集缺失或尚未拉取")
				continue
			}
			if len(sets) == 1 {
				m.set(k, sets[0])
			} else {
				m.set(k, anyStrings(sets))
			}
		case k == "preferred_by":
			var tags []any
			for _, id := range splitList(v) {
				if tag, ok := e.resolveDNS(id, ""); ok && tag != "" {
					tags = append(tags, tag)
				}
			}
			if len(tags) > 0 {
				m.set(k, tags)
			}
		case slices.Contains(listDNSMatchKeys, k):
			if list := splitList(v); len(list) > 0 {
				m.set(k, anyStrings(list))
			}
		case k == "ip_version":
			if version := jsv.StringToNumber(firstString(v)); version == 4 || version == 6 {
				m.set(k, version)
			}
		case k == "clash_mode":
			if mode := firstString(v); mode != "" {
				m.set(k, mode)
			}
		case k == "script":
			if tags := e.scriptConditionTags(v, "DNS 规则"); len(tags) > 0 {
				m.set(k, anyStrings(tags))
			}
		default:
			m.set(k, v)
		}
	}
	if hasConditionKey(raw) && m.len() == 0 {
		e.errors = append(e.errors, "匹配条件缺少有效内容或规则集引用，请修正后应用")
	}
	return m
}

// resolveDNSTargets returns the server instance tags of a DNS rule, one
// per server and detour, "" for a target that cannot be used. A rule with
// several servers queries them concurrently, which FakeIP cannot join.
func (e *emitter) resolveDNSTargets(r *model.DNSRule) []string {
	targets := r.Targets()
	var tags []string
	for _, t := range targets {
		if s := e.dnsByID[t.Server.ID()]; len(targets) > 1 && s != nil && s.Type == "fakeip" {
			e.dnsError("DNS 分流规则「" + ruleName(r.Name, r.ID) + "」：FakeIP 不能与其他服务器同时查询")
			tags = append(tags, "")
			continue
		}
		tag, _ := e.resolveDNS(t.Server.ID(), t.Detour.ID())
		tags = append(tags, tag)
	}
	return tags
}

// compactTags drops repeated tags, keeping the first of each.
func compactTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// resolveDNSFinal is the server of a scope's default DNS rule; without one
// it matches the core's implicit default.
func (e *emitter) resolveDNSFinal(owner string) string {
	doc := e.doc
	id := model.DNSFinalFor(doc, owner).ID()
	detour := model.DNSFinalDetourFor(doc, owner).ID()
	if id != "" {
		var server *model.DNSServer
		for _, s := range e.enabledDNS {
			if s.ID == id {
				server = s
				break
			}
		}
		if server == nil || server.Type == "fakeip" {
			name := owner
			if d, _, ok := model.ScopeDevice(doc, owner); ok && owner != d.ID {
				name = model.ScopeName(doc, owner)
			}
			e.errors = append(e.errors, "设备「"+name+"」的兜底 DNS 需要选择已启用的普通服务器")
		}
		tag, _ := e.resolveDNS(id, detour)
		return tag
	}
	if len(e.enabledDNS) > 0 {
		if e.enabledDNS[0].Type == "fakeip" {
			e.errors = append(e.errors, "核心默认 DNS 需要首个服务器为普通服务器")
		}
		tag, _ := e.resolveDNS(e.enabledDNS[0].ID, detour)
		return tag
	}
	if detour != "" {
		e.dnsError("请先添加 DNS 服务器，再为兜底 DNS 指定查询出口")
	}
	if e.coreDNSTag == "" {
		e.coreDNSTag = "kiko-default-dns"
		for n := 2; e.dnsUsed.has(e.coreDNSTag); n++ {
			e.coreDNSTag = "kiko-default-dns-" + strconv.Itoa(n)
		}
		e.dnsServers = append(e.dnsServers, obj("type", "local", "tag", e.coreDNSTag))
	}
	return e.coreDNSTag
}

func (e *emitter) emitDNS(route *object) *object {
	doc := e.doc
	scopedDNS := doc.DNS.DevicePolicyVersion == 1
	localEnabled := model.LocalEnabled(doc)
	type dnsRule struct {
		rule  *model.DNSRule
		final bool
	}
	var ordered []dnsRule
	add := func(rules []*model.DNSRule) {
		for _, r := range rules {
			ordered = append(ordered, dnsRule{rule: r})
		}
	}
	if scopedDNS {
		add(model.OrderedDeviceRules(doc.DNS.Rules, doc.DNS.Groups, model.PreDeviceID))
		var owners []string
		if localEnabled {
			owners = append(owners, model.LocalActiveScope(doc))
		}
		for _, d := range doc.Route.Devices {
			owners = append(owners, model.ActiveScope(d))
		}
		for _, owner := range owners {
			add(model.OrderedDeviceRules(doc.DNS.Rules, doc.DNS.Groups, owner))
			ordered = append(ordered, dnsRule{rule: &model.DNSRule{ID: "dns-final:" + owner, DeviceID: owner, Enabled: true, CatchAll: true, Match: map[string]any{}, Action: model.ActionRoute}, final: true})
		}
		add(model.OrderedDeviceRules(doc.DNS.Rules, doc.DNS.Groups, model.OtherDeviceID))
		for _, r := range doc.DNS.Rules {
			// Disabled rules are not emitted, so they block nothing.
			if !r.Enabled {
				continue
			}
			owner := r.PolicyOwner()
			if _, _, ok := model.ScopeDevice(doc, owner); !model.IsFixedDevice(owner) && !ok {
				e.errors = append(e.errors, "DNS 规则「"+ruleName(r.Name, r.ID)+"」的来源设备已删除")
			}
			if !slices.ContainsFunc(doc.DNS.Groups, func(g *model.RouteGroup) bool { return g.ID == r.GroupID && model.DeviceOwner(g.DeviceID) == owner }) {
				e.errors = append(e.errors, "DNS 规则「"+ruleName(r.Name, r.ID)+"」的分组需要修正")
			}
		}
	} else {
		add(doc.DNS.Rules)
	}
	var dnsRules []any
	dnsRules = append(dnsRules, e.moduleDNSScriptRules()...)
	for _, item := range ordered {
		r := item.rule
		if !r.Enabled {
			continue
		}
		owner := r.PolicyOwner()
		if r.ConditionTree != nil {
			if problem := model.ValidateCondition(r.ConditionTree, r.CatchAll && owner != model.PreDeviceID); problem != "" {
				e.errors = append(e.errors, "DNS 分流规则「"+ruleName(r.Name, r.ID)+"」："+problem)
				continue
			}
		}
		if r.ConditionTree == nil {
			if problem := model.ScriptInvertProblem(model.LegacyCondition(r.Match, "")); problem != "" {
				e.errors = append(e.errors, "DNS 分流规则「"+ruleName(r.Name, r.ID)+"」："+problem)
				continue
			}
		}
		var m *object
		if r.ConditionTree != nil {
			m = compileCondition(r.ConditionTree, e.convertDNSMatch)
		} else {
			m = e.convertDNSMatch(r.Match)
		}
		if scopedDNS && owner == model.PreDeviceID && m.len() == 0 {
			e.errors = append(e.errors, "前置 DNS 规则「"+ruleName(r.Name, r.ID)+"」请至少添加一个匹配条件")
			continue
		}
		if m.len() == 0 && !r.CatchAll {
			continue
		}
		if scopedDNS && owner != model.PreDeviceID && owner != model.OtherDeviceID {
			m = e.scoped(owner, m)
		}
		if r.Action == model.ActionScript {
			tag := strings.TrimSpace(r.Script)
			if issue := e.scriptRefIssue(tag, model.ScriptDNS); issue != "" {
				e.dnsError("DNS 规则「" + ruleName(r.Name, r.ID) + "」" + issue)
				continue
			}
			m.set("action", "script")
			m.set("dns_script", tag)
			if r.OnError == model.ScriptOnErrorReject {
				m.set("on_error", "reject")
			}
		} else if r.Action == model.ActionReject {
			m.set("action", "reject")
		} else {
			var tags []string
			if item.final {
				tags = []string{e.resolveDNSFinal(owner)}
			} else {
				tags = e.resolveDNSTargets(r)
			}
			if len(tags) == 0 || slices.Contains(tags, "") {
				e.dnsError("DNS 分流规则「" + ruleName(r.Name, r.ID) + "」需要选择可用的 DNS 服务器和查询出口")
				continue
			}
			if tags = compactTags(tags); len(tags) == 1 {
				m.set("server", tags[0])
			} else {
				m.set("servers", anyStrings(tags))
			}
		}
		if scopedDNS {
			if ref, ok := e.dnsRuleRef(r, item.final); ok {
				e.ruleRefs[m] = ref
			}
		}
		dnsRules = append(dnsRules, m)
	}
	if len(e.ruleSets) > 0 {
		route.set("rule_set", e.ruleSets)
	}
	dns := obj("servers", nil)
	if len(dnsRules) > 0 {
		dns.set("rules", dnsRules)
	}
	var dnsFinal string
	if scopedDNS {
		if doc.DNS.Final.Valid() || doc.DNS.FinalDetour.Valid() {
			dnsFinal = e.resolveDNSFinal(model.OtherDeviceID)
		}
	} else {
		dnsFinal, _ = e.resolveDNS(doc.DNS.Final.ID(), doc.DNS.FinalDetour.ID())
	}
	if dnsFinal != "" {
		dns.set("final", dnsFinal)
	}
	if doc.DNS.Strategy != "" {
		dns.set("strategy", doc.DNS.Strategy)
	}
	if doc.DNS.DisableCache {
		dns.set("disable_cache", true)
	}
	if doc.DNS.DisableExpire {
		dns.set("disable_expire", true)
	}
	if doc.DNS.CacheCapacity != 0 {
		dns.set("cache_capacity", float64(doc.DNS.CacheCapacity))
	}
	if subnet := strings.TrimSpace(doc.DNS.ClientSubnet); subnet != "" {
		dns.set("client_subnet", subnet)
	}
	if doc.DNS.ReverseMapping {
		dns.set("reverse_mapping", true)
	}
	resolverObject := func(server string) *object {
		o := obj("server", server)
		if doc.Route.DefaultDomainStrategy != "" {
			o.set("strategy", doc.Route.DefaultDomainStrategy)
		}
		return o
	}
	if ddr, ok := e.resolveDNS(doc.Route.DefaultDomainResolver.ID(), doc.Route.DefaultDomainResolverDetour.ID()); ok && ddr != "" {
		route.set("default_domain_resolver", resolverObject(ddr))
	}
	if core := model.CoreDNSServer(doc); dnsFinal == "" && core != nil {
		e.resolveDNS(core.ID, doc.DNS.FinalDetour.ID())
	}
	for _, s := range e.enabledDNS {
		used := false
		for _, key := range e.instanceKeys {
			used = used || e.instances[key].server.ID == s.ID
		}
		if !used {
			e.resolveDNS(s.ID, "")
		}
	}
	// Bootstrap references may add instances while compiling; each pair is
	// compiled once, so cycles stay finite.
	compiled := map[string]*object{}
	for i := 0; i < len(e.instanceKeys); i++ {
		key := e.instanceKeys[i]
		compiled[key] = e.buildDNSServer(e.instances[key])
	}
	var head []any
	for _, s := range e.enabledDNS {
		for _, key := range e.instanceKeys {
			if e.instances[key].server.ID == s.ID {
				head = append(head, compiled[key])
			}
		}
	}
	e.dnsServers = append(head, e.dnsServers...)
	if e.dnsServers == nil {
		e.dnsServers = []any{}
	}
	if dnsFinal == "" && len(e.enabledDNS) > 0 {
		coreDefault, _ := e.resolveDNS(e.enabledDNS[0].ID, doc.DNS.FinalDetour.ID())
		var first any
		if len(e.dnsServers) > 0 {
			first = e.dnsServers[0].(*object).get("tag")
		}
		if coreDefault != "" && first != any(coreDefault) {
			dns.set("final", coreDefault)
		}
	}
	if !doc.Route.DefaultDomainResolver.Valid() && len(e.enabledDNS) == 1 && len(e.dnsServers) > 1 {
		resolver := dnsFinal
		if resolver == "" {
			resolver, _ = e.resolveDNS(e.enabledDNS[0].ID, doc.DNS.FinalDetour.ID())
		}
		if resolver != "" {
			route.set("default_domain_resolver", resolverObject(resolver))
		}
	}
	dns.set("servers", e.dnsServers)
	return dns
}

// dnsRuleRef is the DNS table rule an emitted DNS rule comes from: a rule
// the table numbers, or an owner's fallback DNS (final).
func (e *emitter) dnsRuleRef(r *model.DNSRule, final bool) (api.RuleRef, bool) {
	owner := r.PolicyOwner()
	if final {
		return api.RuleRef{Owner: owner, Section: "dns", Final: true, Name: "兜底 DNS"}, true
	}
	for i, rule := range model.DNSRulesOf(e.source, owner) {
		if rule.ID == r.ID {
			name := r.Name
			if name == "" {
				name = "未命名规则"
			}
			return api.RuleRef{ID: r.ID, Owner: owner, Section: "dns", Ordinal: i + 1, Name: name}, true
		}
	}
	return api.RuleRef{}, false
}

// dnsRuleMap lists the refs of dns.rules by index, after every insertion,
// and, with device DNS policies, dns.final as the other devices' fallback DNS at
// index -1.
func (e *emitter) dnsRuleMap(dns *object) []api.RuleRef {
	if e.doc.DNS.DevicePolicyVersion != 1 {
		return nil
	}
	out := []api.RuleRef{{Index: -1, Owner: model.OtherDeviceID, Section: "dns", Final: true, Name: "兜底 DNS"}}
	rules, _ := dns.get("rules").([]any)
	for i, raw := range rules {
		o, ok := raw.(*object)
		if !ok {
			continue
		}
		if ref, ok := e.ruleRefs[o]; ok {
			ref.Index = i
			out = append(out, ref)
		}
	}
	return out
}
