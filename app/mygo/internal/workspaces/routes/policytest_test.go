package routes

import (
	"regexp"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Cases of the local policy test evaluation.

func baseTestDoc() *model.Doc {
	on := true
	doc := &model.Doc{Version: 1, Platform: model.PlatformLinux, IPv6Enabled: true}
	doc.Inbounds = []*model.Inbound{{Entity: model.Entity{ID: "mixed", Tag: "mixed-in", Enabled: true}, Kind: "inbound", Type: "mixed", Fields: model.Fields{}}}
	doc.Endpoints = []*model.Endpoint{}
	doc.Outbounds = []*model.Outbound{
		{Entity: model.Entity{ID: "direct", Tag: "直连", Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}},
		{Entity: model.Entity{ID: "proxy", Tag: "代理", Enabled: true}, Kind: "outbound", Type: "socks", Fields: model.Fields{}},
	}
	doc.Subscriptions = []*model.Subscription{}
	doc.Route = model.Route{
		Rules:          []*model.RouteRule{},
		Devices:        []*model.RouteDevice{{ID: "phone", Name: "手机", Icon: "phone", Addresses: []string{"192.168.1.20/32"}, Final: "proxy"}},
		LocalEnabled:   &on,
		LocalAddresses: []string{},
		LocalFinal:     model.Some("direct"),
		Groups: []*model.RouteGroup{
			{ID: "pre-group", Name: "前置", DeviceID: "pre"},
			{ID: "local-first", Name: "优先", DeviceID: "local"},
			{ID: "local-later", Name: "后续", DeviceID: "local"},
			{ID: "phone-group", Name: "手机", DeviceID: "phone"},
			{ID: "other-group", Name: "其他", DeviceID: "other"},
		},
		RuleSets: []*model.RuleSet{}, Final: model.Some("direct"), AutoDetectInterface: true,
	}
	doc.DNS = model.DNS{
		Servers: []*model.DNSServer{{ID: "dns1", Tag: "公共 DNS", Enabled: true, Type: "udp", Server: "1.1.1.1"}},
		Rules:   []*model.DNSRule{},
		Groups: []*model.RouteGroup{
			{ID: "dns-pre", Name: "前置", DeviceID: "pre"},
			{ID: "dns-local", Name: "本机", DeviceID: "local"},
			{ID: "dns-phone", Name: "手机", DeviceID: "phone"},
			{ID: "dns-other", Name: "其他", DeviceID: "other"},
		},
		Final: model.Some("dns1"), DevicePolicyVersion: 1,
		DeviceFinals: map[string]model.Ref{"local": model.Some("dns1"), "phone": model.Some("dns1"), "other": model.Some("dns1")},
	}
	return doc
}

func routeRule(id string, patch func(r *model.RouteRule)) *model.RouteRule {
	r := &model.RouteRule{ID: id, Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{}, DeviceID: "local", GroupID: "local-first"}
	if patch != nil {
		patch(r)
	}
	return r
}

func dnsRule(id string, patch func(r *model.DNSRule)) *model.DNSRule {
	r := &model.DNSRule{ID: id, Enabled: true, Action: model.ActionRoute, Server: model.Some("dns1"), Match: map[string]any{}, DeviceID: "local", GroupID: "dns-local"}
	if patch != nil {
		patch(r)
	}
	return r
}

func matchLeaf(match map[string]any) *model.Condition {
	return &model.Condition{Kind: model.ConditionMatch, Match: match}
}

func strs(v ...string) []any { return anyList(v) }

func TestTrafficMatchesIngressDomainAndIPInGroupOrder(t *testing.T) {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{
		routeRule("later", func(r *model.RouteRule) {
			r.GroupID = "local-later"
			r.Match = map[string]any{"inbound": strs("mixed"), "domain": strs("api.example.com")}
		}),
		routeRule("first", func(r *model.RouteRule) {
			r.Outbound = model.Some("proxy")
			r.ConditionTree = &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
				matchLeaf(map[string]any{"inbound": strs("mixed"), "domain_suffix": strs("example.com")}),
				matchLeaf(map[string]any{"ip_cidr": strs("203.0.113.0/24")}),
			}}
		}),
	}
	r := testRoutePolicy(doc, policyTestInput{deviceID: "local", inboundID: "mixed", domain: "api.example.com", ip: "203.0.113.25"})
	if r.decision != decisionMatched || r.rule.ID != "first" || r.outboundID != "proxy" {
		t.Fatalf("%+v", r)
	}
}

func TestDestinationAddressFieldsUseOr(t *testing.T) {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{routeRule("address-group", func(r *model.RouteRule) {
		r.Outbound = model.Some("proxy")
		r.Match = map[string]any{"domain_suffix": strs("example.com"), "ip_cidr": strs("10.0.0.0/8")}
	})}
	if r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com", ip: "203.0.113.7"}); r.decision != decisionMatched || r.rule.ID != "address-group" {
		t.Fatalf("matched %+v", r)
	}
	if r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "other.test", ip: "203.0.113.7"}); r.decision != decisionDefault {
		t.Fatalf("missed %+v", r)
	}
}

func TestRuleSetsStayIndeterminate(t *testing.T) {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{routeRule("set", func(r *model.RouteRule) {
		r.Match = map[string]any{"domain_suffix": strs("example.com"), "rule_set": strs("remote-set")}
	})}
	r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "other.test"})
	if r.decision != decisionUnknown || len(r.unknownConditions) != 1 || r.unknownConditions[0] != "规则集" {
		t.Fatalf("%+v", r)
	}
}

func TestPreDeviceRulesRunFirst(t *testing.T) {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{
		routeRule("local", func(r *model.RouteRule) { r.DeviceID, r.GroupID = "phone", "phone-group" }),
		routeRule("pre", func(r *model.RouteRule) {
			r.DeviceID, r.GroupID, r.Outbound = "pre", "pre-group", model.Some("proxy")
			r.Match = map[string]any{"domain_keyword": strs("example")}
		}),
	}
	r := testRoutePolicy(doc, policyTestInput{deviceID: "phone", domain: "api.example.com"})
	if r.rule.ID != "pre" || r.outboundID != "proxy" {
		t.Fatalf("%+v", r)
	}
}

func TestTailscalePreferredRouteShowsBothOutcomes(t *testing.T) {
	doc := baseTestDoc()
	doc.Endpoints = []*model.Endpoint{{Entity: model.Entity{ID: "tail", Tag: "工作网络", Enabled: true}, Kind: "endpoint", Type: "tailscale", Fields: model.Fields{}}}
	doc.Route.Rules = []*model.RouteRule{
		routeRule("tail", func(r *model.RouteRule) {
			r.Name, r.DeviceID, r.GroupID, r.Outbound = "Tailscale出站", "pre", "pre-group", model.Some("tail")
			r.Match = map[string]any{"preferred_by": strs("tail")}
		}),
		routeRule("web", func(r *model.RouteRule) {
			r.Name, r.Outbound = "网页代理", model.Some("proxy")
			r.Match = map[string]any{"domain_suffix": strs("example.com")}
		}),
	}
	r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionUnknown || r.rule.ID != "tail" {
		t.Fatalf("%+v", r)
	}
	if !regexp.MustCompile(`工作网络.*域名 api\.example\.com.*节点和子网路由`).MatchString(r.unknownConditions[0]) {
		t.Errorf("condition %q", r.unknownConditions[0])
	}
	if r.ifMatched.decision != decisionMatched || r.ifMatched.outboundID != "tail" {
		t.Errorf("if matched %+v", r.ifMatched)
	}
	if r.otherwise.decision != decisionMatched || r.otherwise.rule.ID != "web" || r.otherwise.outboundID != "proxy" {
		t.Errorf("otherwise %+v", r.otherwise)
	}
	address := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "other.test", ip: "100.97.68.71"})
	if !regexp.MustCompile(`工作网络.*IP 100\.97\.68\.71.*节点和子网路由`).MatchString(address.unknownConditions[0]) {
		t.Errorf("address condition %q", address.unknownConditions[0])
	}
	if address.ifMatched.outboundID != "tail" || address.otherwise.outboundID != "direct" {
		t.Errorf("address outcomes %+v / %+v", address.ifMatched, address.otherwise)
	}
	doc.Route.Rules = doc.Route.Rules[:1]
	if empty := testRoutePolicy(doc, policyTestInput{deviceID: "local"}); empty.decision != decisionDefault || empty.outboundID != "direct" {
		t.Errorf("empty %+v", empty)
	}
}

func TestTrafficStopsAtUnevaluableCondition(t *testing.T) {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{
		routeRule("needs-port", func(r *model.RouteRule) { r.Match = map[string]any{"port": []any{float64(443)}} }),
		routeRule("domain", func(r *model.RouteRule) {
			r.GroupID, r.Outbound = "local-later", model.Some("proxy")
			r.Match = map[string]any{"domain_suffix": strs("example.com")}
		}),
	}
	r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionUnknown || r.rule.ID != "needs-port" || len(r.unknownConditions) != 1 || r.unknownConditions[0] != "目标端口" {
		t.Fatalf("%+v", r)
	}
}

func TestTrafficDeviceDefault(t *testing.T) {
	r := testRoutePolicy(baseTestDoc(), policyTestInput{deviceID: "phone", domain: "unmatched.test"})
	if r.decision != decisionDefault || r.outboundID != "proxy" {
		t.Fatalf("%+v", r)
	}
}

func TestDNSUsesAssumedResponseIP(t *testing.T) {
	doc := baseTestDoc()
	doc.DNS.Rules = []*model.DNSRule{dnsRule("domain", func(r *model.DNSRule) {
		r.Detour = model.Some("proxy")
		r.ConditionTree = &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
			matchLeaf(map[string]any{"inbound": strs("mixed"), "domain_suffix": strs("example.com")}),
			matchLeaf(map[string]any{"ip_cidr": strs("203.0.113.0/24")}),
		}}
	})}
	r := testDNSPolicy(doc, policyTestInput{deviceID: "local", inboundID: "mixed", domain: "api.example.com", ip: "203.0.113.7"})
	if r.decision != decisionMatched || r.rule.ID != "domain" || r.dnsServerID != "dns1" || r.dnsDetourID != "proxy" {
		t.Fatalf("%+v", r)
	}
	pending := testDNSPolicy(doc, policyTestInput{deviceID: "local", inboundID: "mixed", domain: "api.example.com"})
	if pending.decision != decisionUnknown || len(pending.unknownConditions) != 1 || pending.unknownConditions[0] != "DNS 响应 IP" {
		t.Fatalf("pending %+v", pending)
	}
	if missed := testDNSPolicy(doc, policyTestInput{deviceID: "local", inboundID: "mixed", domain: "api.example.com", ip: "198.51.100.7"}); missed.decision != decisionDefault {
		t.Fatalf("missed %+v", missed)
	}
}

func TestDNSAddressFieldsShareOr(t *testing.T) {
	doc := baseTestDoc()
	doc.DNS.Rules = []*model.DNSRule{dnsRule("address-group", func(r *model.DNSRule) {
		r.Match = map[string]any{"domain_suffix": strs("example.com"), "ip_cidr": strs("203.0.113.0/24")}
	})}
	if r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com", ip: "198.51.100.7"}); r.decision != decisionMatched {
		t.Fatalf("domain %+v", r)
	}
	if r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "other.test", ip: "203.0.113.7"}); r.decision != decisionMatched {
		t.Fatalf("response %+v", r)
	}
}

func TestTailscaleDNSPreference(t *testing.T) {
	doc := baseTestDoc()
	doc.DNS.Servers = append(doc.DNS.Servers, &model.DNSServer{ID: "tail-dns", Tag: "工作网络 DNS", Enabled: true, Type: "tailscale", Fields: model.Fields{"endpoint": "tail"}})
	doc.DNS.Rules = []*model.DNSRule{dnsRule("tail-dns-rule", func(r *model.DNSRule) {
		r.Name, r.DeviceID, r.GroupID, r.Server = "Tailscale 内网域名", "pre", "dns-pre", model.Some("tail-dns")
		r.Match = map[string]any{"preferred_by": strs("tail-dns")}
	})}
	r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "host.example.com"})
	if r.decision != decisionUnknown || !regexp.MustCompile(`工作网络 DNS.*域名 host\.example\.com.*内部域名和搜索域`).MatchString(r.unknownConditions[0]) {
		t.Fatalf("%+v", r)
	}
	if r.ifMatched.dnsServerID != "tail-dns" || r.otherwise.decision != decisionDefault || r.otherwise.dnsServerID != "dns1" {
		t.Errorf("outcomes %+v / %+v", r.ifMatched, r.otherwise)
	}
	if got := testDNSPolicy(doc, policyTestInput{deviceID: "local", ip: "100.97.68.71"}); got.decision != decisionDefault {
		t.Errorf("without a domain %+v", got)
	}
}

func TestDNSReportsDisabledServer(t *testing.T) {
	doc := baseTestDoc()
	doc.DNS.Servers[0].Enabled = false
	doc.DNS.Rules = []*model.DNSRule{dnsRule("domain", func(r *model.DNSRule) { r.Match = map[string]any{"domain_suffix": strs("example.com")} })}
	r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionMatched || !regexp.MustCompile(`已删除或停用`).MatchString(r.invalidReference) {
		t.Fatalf("%+v", r)
	}
}

func TestDNSReportsUnavailableDetour(t *testing.T) {
	doc := baseTestDoc()
	doc.Outbounds[1].Enabled = false
	doc.DNS.Rules = []*model.DNSRule{dnsRule("domain", func(r *model.DNSRule) {
		r.Match = map[string]any{"domain_suffix": strs("example.com")}
		r.Detour = model.Some("proxy")
	})}
	r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionMatched || !regexp.MustCompile(`已删除或停用`).MatchString(r.invalidReference) {
		t.Fatalf("%+v", r)
	}
}

func TestDNSFallback(t *testing.T) {
	r := testDNSPolicy(baseTestDoc(), policyTestInput{deviceID: "local", ip: "198.51.100.8"})
	if r.decision != decisionDefault || r.dnsServerID != "dns1" {
		t.Fatalf("%+v", r)
	}
}

func TestScriptConditionIsMaybe(t *testing.T) {
	doc := baseTestDoc()
	doc.Route.Rules = []*model.RouteRule{routeRule("script", func(r *model.RouteRule) {
		r.Outbound = model.Some("proxy")
		r.Match = map[string]any{"domain_suffix": strs("example.com"), "script": strs("geo")}
	})}
	r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionUnknown || len(r.unknownConditions) != 1 || r.unknownConditions[0] != "脚本 geo" {
		t.Fatalf("%+v", r)
	}
	if r.ifMatched == nil || r.ifMatched.outboundID != "proxy" || r.otherwise == nil || r.otherwise.decision != decisionDefault {
		t.Fatalf("branches %+v", r)
	}
	// Another domain misses the other condition, so the script is moot.
	if r := testRoutePolicy(doc, policyTestInput{deviceID: "local", domain: "other.test"}); r.decision != decisionDefault {
		t.Fatalf("missed %+v", r)
	}
}

func TestDNSScriptActionIsMaybe(t *testing.T) {
	doc := baseTestDoc()
	doc.DNS.Rules = []*model.DNSRule{dnsRule("script", func(r *model.DNSRule) {
		r.Action, r.Script, r.Server = model.ActionScript, "answer", model.Ref{}
		r.Match = map[string]any{"domain_suffix": strs("example.com")}
	})}
	r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionUnknown || len(r.unknownConditions) != 1 || r.unknownConditions[0] != "脚本 answer" {
		t.Fatalf("%+v", r)
	}
	if r.ifMatched == nil || r.ifMatched.invalidReference != "" || r.otherwise == nil || r.otherwise.decision != decisionDefault {
		t.Fatalf("branches %+v", r)
	}
}

// A module's dns script runs on every query before the user's DNS rules, so the result is maybe.
func TestModuleDNSScriptIsMaybe(t *testing.T) {
	doc := baseTestDoc()
	m := model.NewHTTPModule()
	m.ID, m.Name, m.Enabled = "mod_dns", "模块DNS", true
	m.Contents = model.ModuleContents{Scripts: []*model.HTTPScript{{Tag: "resolve", Type: model.ScriptDNS, Source: "$done({})"}}}
	m.ApproveContents(m.Contents)
	doc.HTTPTools = &model.HTTPTools{Modules: []*model.HTTPModule{m}}
	r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"})
	if r.decision != decisionUnknown || len(r.unknownConditions) != 1 || r.unknownConditions[0] != "脚本 "+model.ModuleScriptTag("mod_dns", "resolve") {
		t.Fatalf("%+v", r)
	}
	if r.ifMatched == nil || r.otherwise == nil || r.otherwise.decision != decisionDefault {
		t.Fatalf("branches %+v", r)
	}
	m.Enabled = false
	if r := testDNSPolicy(doc, policyTestInput{deviceID: "local", domain: "api.example.com"}); r.decision != decisionDefault {
		t.Fatalf("disabled module %+v", r)
	}
}

// Disabled and invalid module dns scripts are not emitted, so they are not in the policy either.
func TestModuleDNSScriptsFollowTheEmitter(t *testing.T) {
	doc := baseTestDoc()
	m := model.NewHTTPModule()
	m.ID, m.Name, m.Enabled = "mod_dns", "模块DNS", true
	m.Contents = model.ModuleContents{Scripts: []*model.HTTPScript{
		{Tag: "off", Type: model.ScriptDNS, Source: "$done({})", Disabled: true},
		{Tag: "bad", Type: model.ScriptDNS, Pattern: "x"},
		{Tag: "ok", Type: model.ScriptDNS, Source: "$done({})"},
		{Tag: "ok", Type: model.ScriptDNS, Source: "$done({})"},
		{Tag: "rule", Type: model.ScriptRule, Source: "$done(true)"},
	}}
	m.ApproveContents(m.Contents)
	doc.HTTPTools = &model.HTTPTools{Modules: []*model.HTTPModule{m}}
	rules := moduleDNSScriptRules(doc)
	if len(rules) != 1 || rules[0].Script != model.ModuleScriptTag("mod_dns", "ok") {
		t.Fatalf("rules %+v", rules)
	}
}
