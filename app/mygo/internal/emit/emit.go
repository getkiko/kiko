// Package emit turns the document into a sing-box configuration. It is
// the only place ids become tags: the
// document references entities by id, and the configuration by tag.
package emit

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// Options are the host's inputs to emitting.
type Options struct {
	NetworkConfigs []*model.NetworkConfig
	// LocalAddresses are the host's detected interface addresses.
	LocalAddresses []string
	// LocalRuleSetCatalog, when set, lists bundled rule set files (the
	// Android app's): user rule sets are emitted as local files.
	LocalRuleSetCatalog model.LocalRuleSetCatalog
	// LocalBasePath is the directory custom local rule set files live in.
	LocalBasePath string
}

// Result is the emitted configuration with its diagnostics.
type Result struct {
	// JSON is the configuration, indented as JSON.stringify(config, null, 2).
	JSON []byte
	// Config is the configuration decoded from JSON.
	Config map[string]any
	// Warnings are adjustments the emitter made (skipped or renamed parts).
	Warnings []string
	// Errors block applying the configuration.
	Errors []string
	// DependencyErrors are the dependency cycles among Errors.
	DependencyErrors []string
	// RuleMap names the route rules that come from the route table, by
	// their index in route.rules; generated rules have no entry.
	RuleMap []api.RuleRef
	// DNSRuleMap names the DNS rules that come from the DNS table by their
	// index in dns.rules (Section "dns"), dns.final at index -1.
	DNSRuleMap []api.RuleRef
}

// orderedSet keeps the first insertion order of strings.
type orderedSet struct {
	order []string
	seen  map[string]bool
}

func (s *orderedSet) add(v string) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if !s.seen[v] {
		s.seen[v] = true
		s.order = append(s.order, v)
	}
}

func (s *orderedSet) has(v string) bool { return s.seen[v] }

type dnsInstance struct {
	server *model.DNSServer
	detour string
	tag    string
}

// emitter holds the state of one Emit call.
type emitter struct {
	// scriptTypes maps the tag of each emitted script to its type.
	scriptTypes map[string]string
	doc         *model.Doc
	// source is the document before expansion, numbered as the route
	// table numbers it.
	source   *model.Doc
	options  Options
	platform string
	warnings []string
	errors   []string

	skippedLabels []string
	skipped       map[string]*orderedSet

	enabledOut       []*model.Outbound
	enabledEndpoints []*model.Endpoint
	outboundByID     map[string]*model.Outbound
	tagOf            map[string]string

	enabledDNS   []*model.DNSServer
	dnsByID      map[string]*model.DNSServer
	dnsTagOf     map[string]string
	dnsUsed      orderedSet
	instanceKeys []string
	instances    map[string]*dnsInstance
	dnsErrors    map[string]bool
	detourErrors map[string]string
	inTagOf      map[string]string
	// emptyGroups are enabled groups left without a usable member; they are
	// not emitted, so nothing may reference their tags.
	// The value is the group's claimed tag, for messages.
	emptyGroups map[string]string
	// chain holds the copies proxy chains emit (chain.go).
	chain       chainState
	outbounds   []any
	endpoints   []any
	rsTags      orderedSet
	ruleSets    []any
	localSource string
	hasLocalSet bool
	localAddrs  []string
	dnsServers  []any
	coreDNSTag  string
	// ruleRefs are the route (and DNS) table rules of emitted route (and
	// DNS) rules.
	ruleRefs map[*object]api.RuleRef
	ordinals map[string]map[string]int
}

func (e *emitter) noteSkip(where string) func(label string) {
	return func(label string) {
		set, ok := e.skipped[label]
		if !ok {
			set = &orderedSet{}
			e.skipped[label] = set
			e.skippedLabels = append(e.skippedLabels, label)
		}
		set.add(where)
	}
}

func (e *emitter) resolve(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	tag, ok := e.tagOf[id]
	return tag, ok
}

func (e *emitter) resolveIn(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	tag, ok := e.inTagOf[id]
	return tag, ok
}

func (e *emitter) dnsError(message string) {
	if !e.dnsErrors[message] {
		e.dnsErrors[message] = true
		e.errors = append(e.errors, message)
	}
}

// resolveDNS returns the tag of a DNS server used with a query detour,
// creating a shadow copy of the server for each detour it is used with.
func (e *emitter) resolveDNS(id, detour string) (string, bool) {
	if id == "" {
		return "", false
	}
	server, ok := e.dnsByID[id]
	if !ok {
		e.dnsError("DNS 服务器「" + id + "」已删除或停用，请重新选择")
		return "", false
	}
	if detour != "" && !model.DNSTargetSupportsDetour(server) {
		e.dnsError("DNS「" + server.Tag + "」使用 " + server.Type + " 解析方式，请将查询出口设为协议默认")
		return "", false
	}
	if detour != "" {
		problem, ok := e.detourErrors[detour]
		if !ok {
			problem = model.DNSDetourError(e.doc, detour)
			e.detourErrors[detour] = problem
		}
		if problem != "" {
			e.dnsError("DNS「" + server.Tag + "」的查询出口：" + problem)
			return "", false
		}
	}
	key := id + "\x00" + detour
	if existing, ok := e.instances[key]; ok {
		return existing.tag, true
	}
	tag := e.dnsTagOf[id]
	if detour != "" {
		detourTag, _ := e.resolve(detour)
		base := "shadow " + tag + " · " + detourTag
		tag = base
		for n := 2; e.dnsUsed.has(tag); n++ {
			tag = base + " " + strconv.Itoa(n)
		}
		e.dnsUsed.add(tag)
	}
	e.instanceKeys = append(e.instanceKeys, key)
	e.instances[key] = &dnsInstance{server: server, detour: detour, tag: tag}
	return tag, true
}

// Emit builds the sing-box configuration of doc.
func Emit(doc *model.Doc, options Options) Result {
	expanded, expandErrors := model.ExpandNetworkDoc(model.MigrateProxyChains(model.MigrateDNSDetours(doc)), options.NetworkConfigs)
	expanded, moduleIssues := sanitizeModules(expanded)
	e := &emitter{
		doc: expanded, options: options, errors: append([]string{}, expandErrors...),
		skipped: map[string]*orderedSet{}, outboundByID: map[string]*model.Outbound{}, tagOf: map[string]string{},
		dnsByID: map[string]*model.DNSServer{}, dnsTagOf: map[string]string{}, instances: map[string]*dnsInstance{},
		dnsErrors: map[string]bool{}, detourErrors: map[string]string{}, inTagOf: map[string]string{},
		emptyGroups: map[string]string{}, source: doc, ruleRefs: map[*object]api.RuleRef{},
		ordinals: map[string]map[string]int{},
	}
	e.warnings = append(e.warnings, moduleIssues...)
	return e.run()
}

func (e *emitter) run() Result {
	doc := e.doc
	e.platform = doc.Platform
	if e.platform == "" {
		e.platform = model.PlatformLinux
	}
	platform := e.platform

	e.enabledOut = enabledOutbounds(doc)
	for _, o := range e.enabledOut {
		e.outboundByID[o.ID] = o
	}
	e.expandGroupFilters()
	enabledIn := enabledInbounds(doc)
	isEmptyDirect := func(id string) bool {
		o := e.outboundByID[id]
		if id == "" || o == nil || o.Type != "direct" {
			return false
		}
		for _, v := range o.Fields {
			if !isEmpty(v) {
				return false
			}
		}
		return true
	}

	e.enabledEndpoints = enabledEndpoints(doc)
	var renamed []string
	e.tagOf, renamed = outboundTags(e.enabledOut, e.enabledEndpoints)
	for _, t := range renamed {
		e.warnings = append(e.warnings, "tag 重名，已自动重命名为「"+t+"」")
	}
	e.dropEmptyGroups()

	for _, s := range doc.DNS.Servers {
		if s.Enabled {
			e.enabledDNS = append(e.enabledDNS, s)
		}
	}
	if problem := model.FakeIPServerError(e.enabledDNS, doc.DNS.Final); problem != "" {
		e.errors = append(e.errors, problem)
	}
	for _, s := range e.enabledDNS {
		tag := strings.TrimSpace(s.Tag)
		if tag == "" {
			tag = s.Type
		}
		if e.dnsUsed.has(tag) {
			suffix := 2
			for e.dnsUsed.has(tag + " " + strconv.Itoa(suffix)) {
				suffix++
			}
			tag = tag + " " + strconv.Itoa(suffix)
		}
		e.dnsUsed.add(tag)
		e.dnsTagOf[s.ID] = tag
	}
	// new Map keeps the last of duplicate ids.
	for _, s := range e.enabledDNS {
		e.dnsByID[s.ID] = s
	}

	// The scripts are built first: route and DNS rules name rule and dns
	// scripts by tag and need to know which of them exist.
	var scripts []any
	var scriptNotes []string
	if doc.HTTPTools != nil {
		merged, moduleWarnings := withModuleScripts(doc.HTTPTools)
		var scriptWarnings []string
		scripts, scriptWarnings = scriptsConfig(merged)
		scriptNotes = append(moduleWarnings, scriptWarnings...)
	}
	e.scriptTypes = scriptTypesOf(scripts)

	e.emitOutbounds()
	e.emitEndpoints()
	services := e.emitServices()
	inbounds := e.emitInbounds(enabledIn)
	e.emitRuleSets()
	route, routeRules := e.emitRoute(inbounds)
	e.emitTailscaleDirect(route, routeRules, services, isEmptyDirect)

	finalTag, finalOK := e.resolve(doc.Route.Final.ID())
	if doc.Route.Final.Valid() && !finalOK {
		target := model.FindOutboundTarget(doc, doc.Route.Final.ID())
		if tag, empty := e.emptyGroups[doc.Route.Final.ID()]; empty {
			e.errors = append(e.errors, "默认出口「"+tag+"」是没有可用成员的分组，请添加成员或重新选择")
		} else if target.Valid() {
			e.errors = append(e.errors, "默认出口「"+orUnnamed(target.Tag())+"」已关闭，请启用它或重新选择")
		} else {
			e.errors = append(e.errors, "默认出口已删除，请重新选择")
		}
	}
	if !doc.Route.AutoDetectInterface && strings.TrimSpace(doc.Route.DefaultInterface) != "" {
		route.set("default_interface", strings.TrimSpace(doc.Route.DefaultInterface))
	}
	var leaseFiles []string
	for _, p := range doc.Route.DHCPLeaseFiles {
		if p = strings.TrimSpace(p); p != "" {
			leaseFiles = append(leaseFiles, p)
		}
	}
	if slices.ContainsFunc(leaseFiles, func(p string) bool { return !strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") }) {
		e.errors = append(e.errors, "DHCP 租约文件需要绝对路径")
	}
	if len(leaseFiles) > 0 {
		route.set("dhcp_lease_files", anyStrings(uniqueStrings(leaseFiles)))
	}
	if len(e.ruleSets) > 0 {
		route.set("rule_set", e.ruleSets)
	}
	var clients []*model.HTTPClient
	for _, c := range doc.HTTPClients {
		if c.Enabled {
			clients = append(clients, c)
		}
	}
	var httpClients []any
	for _, c := range clients {
		client := obj("tag", c.Tag)
		if tag, ok := e.resolve(c.Detour.ID()); ok && tag != "" {
			client.set("detour", tag)
		} else if c.Detour.Valid() {
			e.errors = append(e.errors, e.detourError("HTTP 客户端「"+orUnnamed(c.Tag)+"」", c.Detour.ID()))
		}
		httpClients = append(httpClients, client)
	}
	if doc.Route.DefaultHTTPClient.Valid() {
		if tag, ok := e.httpClientTag(doc.Route.DefaultHTTPClient.ID(), "路由默认"); ok && tag != "" {
			route.set("default_http_client", tag)
		}
	}
	if finalOK && finalTag != "" {
		route.set("final", finalTag)
	}

	dns := e.emitDNS(route)

	config := obj(
		"log", obj("level", doc.Log.Level, "timestamp", doc.Log.Timestamp),
		"dns", dns,
		"inbounds", inbounds,
		"outbounds", e.outbounds,
	)
	if len(e.endpoints) > 0 {
		config.set("endpoints", e.endpoints)
	}
	config.set("route", route)
	if len(services) > 0 {
		config.set("services", services)
	}
	if len(httpClients) > 0 {
		config.set("http_clients", httpClients)
	}
	mitm, mitmWarnings := mitmConfig(doc)
	if mitm != nil {
		config.set("mitm", mitm)
	}
	e.warnings = append(e.warnings, mitmWarnings...)
	e.warnings = append(e.warnings, scriptNotes...)
	if len(scripts) > 0 {
		config.set("scripts", scripts)
	}
	// The script store lives in the cache file: enabling any script turns
	// it on.
	cacheFile := doc.Experimental.CacheFile || len(scripts) > 0
	if doc.Experimental.ClashAPI || cacheFile {
		exp := obj()
		if doc.Experimental.ClashAPI {
			controller := doc.Experimental.ExternalController
			if controller == "" {
				controller = "127.0.0.1:9090"
			}
			mode := doc.Experimental.DefaultMode
			if mode == "" {
				mode = "Rule"
			}
			exp.set("clash_api", obj("external_controller", controller, "default_mode", mode))
		}
		if cacheFile {
			exp.set("cache_file", obj("enabled", true, "store_fakeip", false))
		}
		config.set("experimental", exp)
	}

	var compact bytes.Buffer
	encode(&compact, config)
	var decoded map[string]any
	_ = json.Unmarshal(compact.Bytes(), &decoded)
	dependencyErrors := ConfigDependencyErrors(decoded)
	e.errors = append(e.errors, dependencyErrors...)

	platLabel := schema.PlatformLabel[platform]
	for _, label := range e.skippedLabels {
		list := e.skipped[label].order
		at := strings.Join(list, "、")
		if len(list) > 3 {
			at = strings.Join(list[:3], "、") + " 等 " + strconv.Itoa(len(list)) + " 处"
		}
		e.warnings = append(e.warnings, platLabel+" 不支持「"+label+"」，已从输出中省略（"+at+"）")
	}

	var indented bytes.Buffer
	_ = json.Indent(&indented, compact.Bytes(), "", "  ")
	return Result{JSON: indented.Bytes(), Config: decoded, Warnings: orEmpty(e.warnings), Errors: orEmpty(e.errors), DependencyErrors: orEmpty(dependencyErrors),
		RuleMap: e.ruleMap(route), DNSRuleMap: e.dnsRuleMap(dns)}
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func orUnnamed(tag string) string {
	if tag == "" {
		return "未命名"
	}
	return tag
}

// expandGroupFilters gives groups with a filter the members it selects
// after their explicit ones (model.GroupMembers), on copies of the groups;
// groups without one stay as they are.
func (e *emitter) expandGroupFilters() {
	for i, o := range e.enabledOut {
		if !model.IsGroup(o.Type) || !o.Filter.Active() {
			continue
		}
		tag := orUnnamed(o.Tag)
		if problem := o.Filter.Error(); problem != "" {
			e.warnings = append(e.warnings, "分组「"+tag+"」的"+problem+"，已忽略筛选")
		}
		c := *o
		c.Members = model.GroupMembers(e.doc, o)
		e.enabledOut[i], e.outboundByID[o.ID] = &c, &c
	}
}

// dropEmptyGroups unresolves groups that have no usable member, directly
// or through groups that are themselves empty: they are skipped when
// emitting, and a reference to their tag would make sing-box refuse the
// configuration. Their tags stay claimed, so other tags do not change.
func (e *emitter) dropEmptyGroups() {
	for changed := true; changed; {
		changed = false
		for _, o := range e.enabledOut {
			if _, empty := e.emptyGroups[o.ID]; empty || !model.IsGroup(o.Type) {
				continue
			}
			if !slices.ContainsFunc(o.Members, func(id string) bool { t, ok := e.resolve(id); return ok && t != "" }) {
				e.emptyGroups[o.ID] = e.tagOf[o.ID]
				delete(e.tagOf, o.ID)
				changed = true
			}
		}
	}
}

// detourError is the error of a detour that does not resolve: emitting
// without it would send the traffic direct instead of through the chain
// the user chose.
func (e *emitter) detourError(owner, id string) string {
	target := model.FindOutboundTarget(e.doc, id)
	if tag, empty := e.emptyGroups[id]; empty {
		return owner + "的前置代理「" + tag + "」是没有可用成员的分组，请添加成员或重新选择"
	}
	if target.Valid() {
		return owner + "的前置代理「" + orUnnamed(target.Tag()) + "」不可用，请启用它或重新选择"
	}
	return owner + "的前置代理已删除，请重新选择"
}

// httpClientTag is the tag of the enabled HTTP client id names; a client
// that is missing or disabled is an error, so requests do not leave
// through another route.
func (e *emitter) httpClientTag(id, owner string) (string, bool) {
	for _, c := range e.doc.HTTPClients {
		if c.ID == id {
			if c.Enabled {
				return c.Tag, true
			}
			e.errors = append(e.errors, owner+"的 HTTP 客户端「"+orUnnamed(c.Tag)+"」已停用，请启用它或重新选择")
			return "", false
		}
	}
	e.errors = append(e.errors, owner+"的 HTTP 客户端已删除，请重新选择")
	return "", false
}

func (e *emitter) emitOutbounds() {
	for _, ob := range e.enabledOut {
		tag, ok := e.tagOf[ob.ID]
		if !ok {
			tag = e.emptyGroups[ob.ID]
		}
		o := e.outboundObject(ob, tag, "出站「"+tag+"」")
		if ob.Type == "wireguard" {
			e.warnings = append(e.warnings, "WireGuard 出站「"+tag+"」已在 sing-box 1.13 移除，请改用 WireGuard endpoint")
		}
		if model.IsGroup(ob.Type) {
			members := []any{}
			for _, mid := range ob.Members {
				t, ok := e.resolve(mid)
				if !ok || t == "" {
					e.warnings = append(e.warnings, "分组「"+tag+"」引用了已删除或未启用的出站，已跳过")
					continue
				}
				members = append(members, t)
			}
			if len(members) == 0 {
				e.warnings = append(e.warnings, "分组「"+tag+"」没有任何成员，已跳过该分组")
				continue
			}
			o.set("outbounds", members)
			if ob.Type == "selector" {
				d, ok := e.resolve(idOf(ob.Fields["default_ref"]))
				o.del("default_ref")
				if ok && d != "" && slices.Contains(members, any(d)) {
					o.set("default", d)
				}
			}
		} else if !model.OutboundTakesChain(ob.Type) {
			// A type without a detour has no chain to dial through.
		} else if detour, ok := e.chainDetour("出站「"+tag+"」", ob.Chain); ok && detour != "" {
			o.set("detour", detour)
		}
		e.outbounds = append(e.outbounds, prune(o))
	}
	e.flushChainCopies()
	if e.outbounds == nil {
		e.outbounds = []any{}
	}
}

func (e *emitter) emitEndpoints() {
	for _, ep := range e.enabledEndpoints {
		tag := e.tagOf[ep.ID]
		o := obj("type", ep.Type, "tag", tag)
		applyFields(o, ep.Fields, schema.EndpointSchema[ep.Type], e.resolve, e.platform, e.noteSkip("Endpoint「"+tag+"」"), nil, e.resolveDNS)
		o.del("detour")
		if !model.EndpointTakesChain(ep.Type) {
			// No detour, no chain.
		} else if detour, ok := e.chainDetour("Endpoint「"+tag+"」", ep.Chain); ok && detour != "" {
			o.set("detour", detour)
		}
		postProcessEndpoint(ep.Type, o, ep.Fields, &e.warnings)
		e.endpoints = append(e.endpoints, prune(o))
	}
	e.flushChainCopies()
}

func usbDevices(list []schema.UsbDeviceMatch) []any {
	out := make([]any, len(list))
	for i, d := range list {
		o := obj()
		if d.BusID != "" {
			o.set("bus_id", d.BusID)
		}
		if d.VendorID != nil {
			o.set("vendor_id", *d.VendorID)
		}
		if d.ProductID != nil {
			o.set("product_id", *d.ProductID)
		}
		if d.Serial != "" {
			o.set("serial", d.Serial)
		}
		out[i] = o
	}
	return out
}

func (e *emitter) emitServices() []any {
	doc, platform := e.doc, e.platform
	var tailscale []*model.Endpoint
	for _, ep := range e.enabledEndpoints {
		if ep.Type == "tailscale" {
			tailscale = append(tailscale, ep)
		}
	}
	var services []any
	tags := orderedSet{}
	for _, sv := range doc.Services {
		if !sv.Enabled {
			continue
		}
		label := schema.TypeLabel[sv.Type]
		if label == "" {
			label = sv.Type
		}
		if !schema.ServiceAvailable(sv.Type, platform) {
			e.warnings = append(e.warnings, schema.PlatformLabel[platform]+" 不可用「"+label+"」，已从输出中省略："+schema.ServiceUnavailableReason(sv.Type, platform))
			continue
		}
		base := strings.TrimSpace(sv.Tag)
		if base == "" {
			base = sv.Type
		}
		tag := base
		for n := 2; tags.has(tag); n++ {
			tag = base + " " + strconv.Itoa(n)
		}
		tags.add(tag)
		o := obj("type", sv.Type, "tag", tag)
		applyFields(o, sv.Fields, schema.ServiceSchema[sv.Type], e.resolve, platform, e.noteSkip("服务「"+tag+"」"), nil, e.resolveDNS)
		o.del("devices")
		devices := schema.NormalizeUsbDevices(sv.Fields["devices"])
		if sv.Type == "usbip-server" {
			dynamic := sv.Fields["provider"] == "dynamic"
			if dynamic {
				o.set("provider", "dynamic")
			} else {
				o.del("provider")
				if len(devices) == 0 {
					e.warnings = append(e.warnings, label+"「"+tag+"」没有要导出的设备，已从输出中省略")
					continue
				}
				o.set("devices", usbDevices(devices))
			}
			listen := jsv.Text(sv.Fields["listen"])
			if dynamic {
				e.warnings = append(e.warnings, label+"「"+tag+"」使用 dynamic 模式，设备需由 API 客户端注入：Kiko 桌面版会自动提供 api 服务，单独导出配置时请自行添加 type 为 api 的服务")
			}
			if len(tailscale) > 0 && listen != "" && !model.IsWildcardHost(listen) && !model.IsLoopbackHost(listen) {
				e.warnings = append(e.warnings, label+"「"+tag+"」监听在具体 IP "+listen+" 上，Tailscale 端点的连接会被改写到 127.0.0.1，将无法到达；请改为 127.0.0.1 或 0.0.0.0")
			} else if len(tailscale) == 0 && listen != "" && model.IsLoopbackHost(listen) {
				e.warnings = append(e.warnings, label+"「"+tag+"」只监听 127.0.0.1，且没有启用的 Tailscale 端点，其他设备无法访问")
			}
		} else {
			server := jsv.Text(sv.Fields["server"])
			if server == "" {
				e.warnings = append(e.warnings, label+"「"+tag+"」缺少服务器地址，已从输出中省略")
				continue
			}
			if len(devices) > 0 {
				o.set("devices", usbDevices(devices))
			}
			detourID := refValue(sv.Fields["detour"])
			target := model.FindOutboundTarget(doc, detourID)
			_, resolved := e.resolve(detourID)
			if detourID != "" && !resolved {
				e.errors = append(e.errors, e.detourError(label+"「"+tag+"」", detourID))
			}
			detourIsTailscale := target.Valid() && target.Type() == "tailscale" && resolved
			if model.IsTailnetHost(server) && !detourIsTailscale {
				e.warnings = append(e.warnings, label+"「"+tag+"」的服务器地址 "+server+" 是 Tailscale 地址，但没有把前置代理设为 Tailscale 端点；只用 Kiko 时连接会超时（本机装了系统级 Tailscale 可忽略）")
			}
		}
		services = append(services, prune(o))
	}
	return services
}

var removedInboundKeys = []string{"sniff", "sniff_override_destination", "sniff_timeout", "domain_strategy", "udp_disable_domain_unmapping"}

func (e *emitter) emitInbounds(enabledIn []*model.Inbound) []any {
	var renamed []string
	e.inTagOf, renamed = inboundTags(enabledIn)
	for _, tag := range renamed {
		e.warnings = append(e.warnings, "入站 tag 重名，已自动重命名为「"+tag+"」")
	}
	inbounds := []any{}
	for _, ib := range enabledIn {
		tag := e.inTagOf[ib.ID]
		o := obj("type", ib.Type, "tag", tag)
		table := schema.InboundSchema[ib.Type]
		applyFields(o, ib.Fields, table, e.resolve, e.platform, e.noteSkip("入站「"+tag+"」"), e.resolveIn, e.resolveDNS)
		var detourField *schema.Field
		for i := range table {
			if table[i].Key == "detour" {
				detourField = &table[i]
				break
			}
		}
		if ref := ib.Fields["detour"]; jsv.Truthy(ref) && detourField != nil {
			var ok bool
			if detourField.Type == schema.Ref {
				_, ok = e.resolve(idOf(ref))
			} else {
				_, ok = e.resolveIn(idOf(ref))
			}
			if !ok {
				o.del("detour")
				kind := "入站"
				if detourField.Type == schema.Ref {
					kind = "出站"
				}
				e.warnings = append(e.warnings, "入站「"+tag+"」的 detour 指向了已删除或未启用的"+kind+"，已清除")
			}
		}
		postProcessInbound(ib.Type, o, ib.Fields, e.platform)
		removed := slices.Clone(removedInboundKeys)
		if ib.Type == "tun" {
			removed = append(removed, "gso", "endpoint_independent_nat")
		}
		for _, k := range removed {
			if !o.has(k) {
				continue
			}
			o.del(k)
			hint := "请改用路由规则的 sniff / resolve 动作"
			if k == "gso" || k == "endpoint_independent_nat" {
				hint = "该选项已无替代，可直接去掉"
			}
			e.warnings = append(e.warnings, "入站「"+tag+"」的 "+k+" 已被新版 sing-box 移除（保留会导致启动失败），已自动删除；"+hint)
		}
		inbounds = append(inbounds, prune(o))
	}
	if len(inbounds) == 0 {
		e.warnings = append(e.warnings, "没有启用任何入站，核心将无法接收流量")
	}
	return inbounds
}

func (e *emitter) remoteRuleSet(rs *model.RuleSet) *object {
	interval := rs.UpdateInterval
	if interval == "" {
		interval = "24h0m0s"
	}
	definition := obj("type", "remote", "tag", rs.Tag, "url", rs.URL, "update_interval", interval)
	if rs.HTTPClient.Valid() {
		if tag, ok := e.httpClientTag(rs.HTTPClient.ID(), "规则集「"+rs.Tag+"」"); ok && tag != "" {
			definition.set("http_client", tag)
		}
	}
	return definition
}

func (e *emitter) emitRuleSets() {
	doc := e.doc
	imported := map[string]*model.RuleSet{}
	for _, rs := range doc.Route.RuleSets {
		if model.IsImportedRuleSet(rs) {
			imported[rs.Tag] = rs
		}
	}
	useLocal := e.options.LocalRuleSetCatalog != nil
	push := func(tag string, v *object) {
		e.ruleSets = append(e.ruleSets, withOverrides(doc, tag, v))
		e.rsTags.add(tag)
	}
	for _, tag := range model.UsedRuleSetTags(doc) {
		var configured *model.RuleSet
		for _, rs := range doc.Route.RuleSets {
			if rs.Tag == tag {
				configured = rs
				break
			}
		}
		if configured != nil && !configured.Enabled {
			e.warnings = append(e.warnings, "规则引用了已停用的规则集「"+tag+"」，已跳过该引用")
			continue
		}
		custom := imported[tag]
		if custom != nil && custom.Mode == "remote" {
			push(tag, e.remoteRuleSet(custom))
			continue
		}
		if model.IsBuiltinRuleSet(tag) {
			if useLocal {
				if local, ok := e.options.LocalRuleSetCatalog[tag]; ok {
					push(tag, obj("type", "local", "tag", tag, "format", local.Format, "path", local.Path))
				} else {
					e.errors = append(e.errors, "Android 内置规则集「"+tag+"」不可用，请在设置的规则集页面检查安装文件")
				}
			} else if configured != nil && len(configured.Rules) > 0 {
				push(tag, obj("type", "inline", "tag", tag, "rules", configured.Rules))
			} else if u := model.BuiltinRuleSetURL(tag, "srs"); u != "" {
				push(tag, obj("type", "remote", "tag", tag, "url", u, "update_interval", "24h0m0s"))
			} else {
				e.warnings = append(e.warnings, "内置规则集「"+tag+"」尚未导入，已跳过")
			}
			continue
		}
		rs := imported[tag]
		if useLocal {
			if local, ok := e.options.LocalRuleSetCatalog[tag]; ok {
				push(tag, obj("type", "local", "tag", tag, "format", local.Format, "path", local.Path))
				continue
			}
		}
		if rs == nil {
			e.warnings = append(e.warnings, "规则引用了尚未导入或已停用的规则集「"+tag+"」，已跳过该引用")
			continue
		}
		if rs.Mode == "remote" {
			push(tag, e.remoteRuleSet(rs))
			continue
		}
		if len(rs.Rules) == 0 {
			// The core refuses an inline rule set without rules.
			if strings.TrimSpace(rs.URL) != "" {
				e.warnings = append(e.warnings, "规则集「"+tag+"」尚未更新内容，暂只匹配添加的规则")
			}
			e.ruleSets = append(e.ruleSets, emptyInlineRuleSet(rs))
			e.rsTags.add(tag)
			continue
		}
		if useLocal {
			path := model.CustomLocalRuleSetPath(rs)
			if e.options.LocalBasePath != "" {
				path = strings.TrimSuffix(e.options.LocalBasePath, "/") + "/" + path
			}
			push(tag, obj("type", "local", "tag", rs.Tag, "format", "source", "path", path))
		} else {
			var rules any
			if rs.Rules != nil {
				rules = rs.Rules
			}
			push(tag, obj("type", "inline", "tag", rs.Tag, "rules", rules))
		}
	}
}

// neverMatch is a domain regex no domain matches.
const neverMatch = `[^\s\S]`

// emptyInlineRuleSet is the definition of an inline rule set without
// rules, which the core refuses: its added entries as its rules (they are
// all it has, as nothing is left to remove from), or a rule that never
// matches.
func emptyInlineRuleSet(rs *model.RuleSet) *object {
	var rules []any
	for _, g := range rs.Overrides.AddGroups() {
		rules = append(rules, obj(g.Field, anyStrings(g.Values)))
	}
	if len(rules) == 0 {
		rules = []any{obj("domain_regex", []any{neverMatch})}
	}
	return obj("type", "inline", "tag", rs.Tag, "rules", rules)
}

// withOverrides adds the overrides of the rule set tag names to its
// definition v: the added entries as one headless rule per field, the
// removed values by field. The core applies them to the source each time
// it loads it.
func withOverrides(doc *model.Doc, tag string, v *object) *object {
	var rs *model.RuleSet
	for _, r := range doc.Route.RuleSets {
		if r.Tag == tag {
			rs = r
			break
		}
	}
	if rs == nil || rs.Overrides.Empty() {
		return v
	}
	overrides := obj()
	var add []any
	for _, g := range rs.Overrides.AddGroups() {
		add = append(add, obj(g.Field, anyStrings(g.Values)))
	}
	if len(add) > 0 {
		overrides.set("add", add)
	}
	remove := obj()
	for _, g := range rs.Overrides.RemoveGroups() {
		remove.set(g.Field, anyStrings(g.Values))
	}
	if len(remove.keys) > 0 {
		overrides.set("remove", remove)
	}
	if len(overrides.keys) > 0 {
		v.set("overrides", overrides)
	}
	return v
}

func (e *emitter) ensureLocalSourceSet() {
	if e.hasLocalSet {
		return
	}
	addresses := model.LocalSourceAddresses(e.doc, e.localAddrs)
	e.ruleSets = append(e.ruleSets, obj("type", "inline", "tag", e.localSource, "rules", []any{obj("source_ip_cidr", anyStrings(addresses))}))
	e.hasLocalSet = true
}

// scoped wraps a scope's rule in its source predicate.
func (e *emitter) scoped(owner string, m *object) *object {
	if model.IsLocalScope(owner) {
		e.ensureLocalSourceSet()
	}
	scope := fromMap(model.RouteSourceScope(e.doc, owner, e.localSource))
	if m.len() > 0 {
		return obj("type", "logical", "mode", "and", "rules", []any{scope, m})
	}
	return scope
}

// deviceFinalPrefix starts the id of an owner's default outbound rule.
const deviceFinalPrefix = "device-final:"

func ruleName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

var listMatchKeys = []string{"ip_cidr", "domain", "domain_suffix", "domain_keyword", "domain_regex",
	"source_ip_cidr", "source_port_range", "port_range", "process_name",
	"process_path", "process_path_regex", "package_name", "user", "auth_user",
	"client", "wifi_ssid", "wifi_bssid", "default_interface_address",
	"network", "network_type", "protocol"}

func hasConditionKey(raw map[string]any) bool {
	for k := range raw {
		if k != "package_apps" {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (e *emitter) convertRouteMatch(raw map[string]any) *object {
	doc := e.doc
	m := obj()
	for _, k := range sortedKeys(raw) {
		v := raw[k]
		if k == "package_apps" {
			continue
		}
		if k == "source_mac_address" || k == "source_hostname" {
			values := model.DeviceValues(v)
			normalized := values
			if k == "source_mac_address" {
				normalized = make([]string, len(values))
				for i, value := range values {
					normalized[i] = model.NormalizeMAC(value)
				}
			}
			if len(values) == 0 || slices.Contains(normalized, "") {
				if k == "source_mac_address" {
					e.errors = append(e.errors, "源设备 MAC 地址格式无效")
				} else {
					e.errors = append(e.errors, "源设备主机名需要填写")
				}
			} else {
				m.set(k, anyStrings(uniqueStrings(normalized)))
			}
			continue
		}
		if isEmpty(v) {
			continue
		}
		switch {
		case k == "rule_set":
			var sets []string
			for _, t := range splitList(v) {
				if !e.rsTags.has(t) {
					e.errors = append(e.errors, "路由规则引用的规则集「"+t+"」缺失或尚未拉取")
					continue
				}
				sets = append(sets, t)
			}
			if len(sets) > 0 {
				m.set(k, anyStrings(sets))
			}
		case k == "port" || k == "source_port":
			ports, invalid := validPorts(v)
			if len(invalid) > 0 {
				which := "来源"
				if k == "port" {
					which = "目标"
				}
				e.warnings = append(e.warnings, "路由规则包含无效"+which+"端口，已跳过："+strings.Join(invalid, "、"))
			}
			if len(ports) > 0 {
				m.set(k, ports)
			}
		case k == "inbound":
			var tags []any
			for _, id := range splitList(v) {
				tag := id
				if slices.ContainsFunc(doc.Inbounds, func(in *model.Inbound) bool { return in.ID == id }) {
					tag, _ = e.resolveIn(id)
				} else if slices.ContainsFunc(doc.Endpoints, func(ep *model.Endpoint) bool { return ep.ID == id }) {
					tag, _ = e.resolve(id)
				}
				if tag != "" {
					tags = append(tags, tag)
				}
			}
			// Dropping every inbound would widen the rule to all of them.
			if len(tags) == 0 {
				e.errors = append(e.errors, "路由规则引用的入站均已删除或停用，请修改规则")
				continue
			}
			m.set(k, tags)
		case k == "preferred_by":
			var tags []any
			for _, id := range splitList(v) {
				if tag, ok := e.resolve(id); ok && tag != "" {
					tags = append(tags, tag)
				}
			}
			if len(tags) == 0 {
				e.errors = append(e.errors, "路由规则的优先出站均已删除或停用，请修改规则")
				continue
			}
			m.set(k, tags)
		case k == "user_id":
			var ids []any
			for _, item := range splitList(v) {
				id := jsv.StringToNumber(item)
				if jsv.IsInteger(id) && id >= 0 && id <= 2147483647 {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				m.set(k, ids)
			}
		case k == "interface_address" || k == "network_interface_address":
			value := v
			if s, ok := v.(string); ok {
				if json.Unmarshal([]byte(s), &value) != nil {
					which := "网络类型接口地址"
					if k == "interface_address" {
						which = "接口地址"
					}
					e.errors = append(e.errors, "路由规则的"+which+"不是有效 JSON 对象")
					continue
				}
			}
			if object, ok := value.(map[string]any); ok {
				m.set(k, object)
			}
		case slices.Contains(listMatchKeys, k):
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
			if tags := e.scriptConditionTags(v, "路由规则"); len(tags) > 0 {
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

func (e *emitter) emitRoute(inbounds []any) (*object, []any) {
	doc := e.doc
	routeRules := []any{}
	for _, r := range model.SystemRouteRules() {
		if r["protocol"] != nil {
			routeRules = append(routeRules, obj("protocol", r["protocol"], "action", r["action"]))
		} else {
			routeRules = append(routeRules, obj("action", r["action"]))
		}
	}
	localEnabled := model.LocalEnabled(doc)
	var candidates []*model.RouteRule
	for _, r := range doc.Route.Rules {
		if r.Enabled && !model.IsSystemRouteRule(r) && (localEnabled || !model.IsLocalScope(r.PolicyOwner())) {
			candidates = append(candidates, r)
		}
	}
	order := map[string]int{}
	for i, g := range doc.Route.Groups {
		order[g.ID] = i
	}
	rank := func(group string) int {
		if i, ok := order[group]; ok {
			return i
		}
		return int(^uint(0) >> 1)
	}
	sortedRules := slices.Clone(candidates)
	sort.SliceStable(sortedRules, func(i, j int) bool { return rank(sortedRules[i].GroupID) < rank(sortedRules[j].GroupID) })
	devices := doc.Route.Devices
	if localEnabled && slices.ContainsFunc(doc.Route.LocalAddresses, func(a string) bool { return !model.ValidNetwork(a) }) {
		e.errors = append(e.errors, "本机补充来源 IP / CIDR 无效")
	}
	for _, d := range devices {
		if problem := model.ValidateDevice(d, devices); problem != "" {
			e.errors = append(e.errors, "设备「"+d.Name+"」："+problem)
		}
	}
	if problem := model.ValidateLocalProfiles(doc); problem != "" {
		e.errors = append(e.errors, "设备「"+model.LocalDeviceName+"」："+problem)
	}
	for _, r := range sortedRules {
		if _, _, ok := model.ScopeDevice(doc, r.PolicyOwner()); !model.IsFixedDevice(r.PolicyOwner()) && !ok {
			e.errors = append(e.errors, "路由规则「"+ruleName(r.Name, r.ID)+"」的来源设备已删除")
		}
	}
	localFinal := doc.Route.Final.ID()
	if doc.Route.LocalFinal.Set() {
		localFinal = doc.Route.LocalFinal.ID()
		// The core's default: the first outbound emitted, so not a
		// skipped empty group.
		for _, o := range e.enabledOut {
			if _, ok := e.tagOf[o.ID]; ok && localFinal == "" {
				localFinal = o.ID
			}
		}
		if localFinal == "" && len(e.enabledEndpoints) > 0 {
			localFinal = e.enabledEndpoints[0].ID
		}
	}
	e.localAddrs = append([]string{}, e.options.LocalAddresses...)
	for _, in := range inbounds {
		o := in.(*object)
		if o.get("type") != "tun" {
			continue
		}
		for _, a := range splitList(o.get("address")) {
			e.localAddrs = append(e.localAddrs, strings.Split(a, "/")[0])
		}
	}
	reserved := orderedSet{}
	for _, rs := range doc.Route.RuleSets {
		reserved.add(rs.Tag)
	}
	for _, tag := range model.UsedRuleSetTags(doc) {
		reserved.add(tag)
	}
	e.localSource = "kiko-local-source"
	for suffix := 2; reserved.has(e.localSource); suffix++ {
		e.localSource = "kiko-local-source-" + strconv.Itoa(suffix)
	}
	byOwner := func(owner string) []*model.RouteRule {
		var out []*model.RouteRule
		for _, r := range sortedRules {
			if r.PolicyOwner() == owner {
				out = append(out, r)
			}
		}
		return out
	}
	finalRule := func(owner, outbound string) *model.RouteRule {
		return &model.RouteRule{ID: deviceFinalPrefix + owner, Enabled: true, CatchAll: true, DeviceID: owner, Match: map[string]any{}, Action: model.ActionRoute, Outbound: model.RefOf(outbound)}
	}
	ordered := byOwner(model.PreDeviceID)
	ordered = append(ordered, e.moduleRouteRules()...)
	if localScope := model.LocalActiveScope(doc); localScope == model.LocalDeviceID {
		ordered = append(ordered, byOwner(model.LocalDeviceID)...)
		if localEnabled && localFinal != "" {
			ordered = append(ordered, finalRule(model.LocalDeviceID, localFinal))
		}
	} else if localEnabled {
		ordered = append(ordered, byOwner(localScope)...)
		ordered = append(ordered, finalRule(localScope, model.ProfileFinal(model.LocalDevice(doc), localScope)))
	}
	for _, d := range devices {
		scope := model.ActiveScope(d)
		for _, r := range sortedRules {
			if r.DeviceID == scope {
				ordered = append(ordered, r)
			}
		}
		ordered = append(ordered, finalRule(scope, model.ProfileFinal(d, scope)))
	}
	ordered = append(ordered, byOwner(model.OtherDeviceID)...)

	for _, r := range ordered {
		owner := r.PolicyOwner()
		if r.ConditionTree != nil {
			if problem := model.ValidateCondition(r.ConditionTree, r.CatchAll && owner != model.PreDeviceID); problem != "" {
				e.errors = append(e.errors, "路由规则「"+ruleName(r.Name, r.ID)+"」："+problem)
				continue
			}
		}
		if r.ConditionTree == nil {
			if problem := model.ScriptInvertProblem(model.LegacyCondition(r.Match, r.Logic)); problem != "" {
				e.errors = append(e.errors, "路由规则「"+ruleName(r.Name, r.ID)+"」："+problem)
				continue
			}
		}
		var m *object
		if r.ConditionTree != nil {
			m = compileCondition(r.ConditionTree, e.convertRouteMatch)
		} else {
			m = e.convertRouteMatch(r.Match)
		}
		if owner == model.PreDeviceID && m.len() == 0 {
			e.errors = append(e.errors, "前置规则「"+ruleName(r.Name, r.ID)+"」请至少添加一个匹配条件")
			continue
		}
		if m.len() == 0 && !r.CatchAll && (r.Action == model.ActionRoute || r.Action == model.ActionReject) {
			continue
		}
		if r.ConditionTree == nil && r.Logic == "or" {
			invert := m.get("invert")
			rules := []any{}
			for _, k := range m.keys {
				if k != "invert" {
					rules = append(rules, obj(k, m.get(k)))
				}
			}
			next := obj("type", "logical", "mode", "or", "rules", rules)
			if jsv.Truthy(invert) {
				next.set("invert", true)
			}
			m = next
		}
		action := obj()
		if r.Action == model.ActionRoute {
			t, ok := e.resolve(r.Outbound.ID())
			if !ok && !r.Outbound.Valid() {
				e.errors = append(e.errors, "路由规则没有选择目标出站，请在路由页为它选择一个出站")
				continue
			}
			if !ok {
				target := model.FindOutboundTarget(doc, r.Outbound.ID())
				if tag, empty := e.emptyGroups[r.Outbound.ID()]; empty {
					e.errors = append(e.errors, "路由规则引用的分组「"+tag+"」没有可用成员，请添加成员或修改规则")
				} else if target.Valid() {
					e.errors = append(e.errors, "路由规则引用的目标出站「"+orUnnamed(target.Tag())+"」已关闭，请启用它或修改规则")
				} else {
					e.errors = append(e.errors, "路由规则的目标出站已删除，请修改规则")
				}
				continue
			}
			action.set("outbound", t)
		} else {
			action.set("action", r.Action)
			if r.Action == model.ActionSniff && r.OverrideDestination {
				action.set("override_destination", true)
			}
		}
		if owner != model.PreDeviceID && owner != model.OtherDeviceID {
			m = e.scoped(owner, m)
		}
		emitted := spread(m, action)
		if ref, ok := e.ruleRef(r); ok {
			e.ruleRefs[emitted] = ref
		}
		routeRules = append(routeRules, emitted)
	}
	route := obj("rules", routeRules, "auto_detect_interface", doc.Route.AutoDetectInterface, "find_process", true)
	return route, routeRules
}

// ruleRef is the route table rule an emitted rule comes from: a rule the
// table numbers, or an owner's default outbound.
func (e *emitter) ruleRef(r *model.RouteRule) (api.RuleRef, bool) {
	owner := r.PolicyOwner()
	if model.IsModuleRule(r.ID) {
		return e.moduleRuleRef(r), true
	}
	if strings.HasPrefix(r.ID, deviceFinalPrefix) {
		return api.RuleRef{Owner: owner, Section: "route", Final: true, Name: "默认出口"}, true
	}
	ordinals, ok := e.ordinals[owner]
	if !ok {
		ordinals = model.RouteRuleOrdinals(e.source, owner)
		e.ordinals[owner] = ordinals
	}
	ordinal, ok := ordinals[r.ID]
	if !ok {
		return api.RuleRef{}, false
	}
	name := r.Name
	if name == "" {
		name = "未命名规则"
	}
	return api.RuleRef{ID: r.ID, Owner: owner, Section: "route", Ordinal: ordinal, Name: name}, true
}

// ruleMap lists the refs of route.rules by index, after every insertion.
func (e *emitter) ruleMap(route *object) []api.RuleRef {
	rules, _ := route.get("rules").([]any)
	var out []api.RuleRef
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

// emitTailscaleDirect adds a direct rule for connections a Tailscale
// endpoint rewrites to 127.0.0.1, so a USB/IP server receives them when
// the default outbound is not direct. The rule is not written into the
// document.
func (e *emitter) emitTailscaleDirect(route *object, rules []any, services []any, isEmptyDirect func(string) bool) {
	routeRules := &rules
	var tailnetTags []any
	for _, ep := range e.enabledEndpoints {
		if ep.Type == "tailscale" {
			if tag, ok := e.tagOf[ep.ID]; ok && tag != "" {
				tailnetTags = append(tailnetTags, tag)
			}
		}
	}
	hasServer := slices.ContainsFunc(services, func(s any) bool { return s.(*object).get("type") == "usbip-server" })
	defer func() { route.set("rules", *routeRules) }()
	if len(tailnetTags) == 0 || !hasServer {
		return
	}
	var final *model.Outbound
	if e.doc.Route.Final.Valid() {
		final = e.outboundByID[e.doc.Route.Final.ID()]
	} else if len(e.enabledOut) > 0 {
		final = e.enabledOut[0]
	}
	if final != nil && final.Type == "direct" {
		return
	}
	var direct *model.Outbound
	for _, o := range e.enabledOut {
		if isEmptyDirect(o.ID) {
			direct = o
			break
		}
	}
	if direct == nil {
		for _, o := range e.enabledOut {
			if o.Type == "direct" {
				direct = o
				break
			}
		}
	}
	directTag := ""
	if direct != nil {
		directTag = e.tagOf[direct.ID]
	}
	if directTag == "" {
		e.warnings = append(e.warnings, "默认出口不是直连，且没有直连出站，来自 Tailscale 的 USB/IP 连接会被默认出口拦截；请添加一个直连出站")
		return
	}
	covered := slices.ContainsFunc(*routeRules, func(rule any) bool {
		o := rule.(*object)
		inbound, ok := o.get("inbound").([]any)
		if o.get("outbound") != directTag || !ok {
			return false
		}
		for _, t := range tailnetTags {
			if !slices.Contains(inbound, t) {
				return false
			}
		}
		return true
	})
	if covered {
		return
	}
	rule := obj("inbound", tailnetTags, "ip_cidr", []any{"127.0.0.0/8", "::1/128"}, "outbound", directTag)
	*routeRules = slices.Insert(*routeRules, len(model.SystemRouteRules()), any(rule))
	var names []string
	for _, t := range tailnetTags {
		names = append(names, "「"+t.(string)+"」")
	}
	e.warnings = append(e.warnings, "已自动添加 Tailscale→USB/IP 直连规则：来自"+strings.Join(names, "、")+"、目标为本机的连接走「"+directTag+"」")
}
