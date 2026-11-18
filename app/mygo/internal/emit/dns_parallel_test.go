package emit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func dnsServerTagged(d *model.Doc, tag string) *model.DNSServer {
	for _, s := range d.DNS.Servers {
		if s.Tag == tag {
			return s
		}
	}
	return nil
}

func parallelDoc(t *testing.T) (*model.Doc, *model.DNSRule) {
	d := doc()
	cn := dnsServerTagged(d, "dns-cn")
	if cn == nil {
		t.Fatal("no dns-cn")
	}
	d.DNS.Servers = append(d.DNS.Servers, &model.DNSServer{ID: "b", Tag: "dns-b", Enabled: true, Type: "udp", Server: "192.0.2.53"})
	rule := &model.DNSRule{ID: "par", Name: "并发", DeviceID: model.LocalDeviceID, GroupID: model.DNSGroupsOf(d, model.LocalDeviceID)[0].ID,
		Enabled: true, Match: map[string]any{"domain_suffix": []any{"example.com"}}, Action: model.ActionRoute}
	rule.SetTargets([]model.DNSTarget{
		{Server: model.Some(cn.ID), Detour: model.Some("proxy")},
		{Server: model.Some("b"), Detour: model.Null()},
		{Server: model.Some("b"), Detour: model.Some("proxy")},
	})
	d.DNS.Rules = append(d.DNS.Rules, rule)
	return d, rule
}

func emittedDNSRule(t *testing.T, r emit.Result, domain string) map[string]any {
	dns := r.Config["dns"].(map[string]any)
	rules, _ := dns["rules"].([]any)
	for _, raw := range rules {
		rule := raw.(map[string]any)
		if strings.Contains(strings.ReplaceAll(stringify(rule), " ", ""), domain) {
			return rule
		}
	}
	t.Fatalf("no rule for %s in %v", domain, rules)
	return nil
}

func stringify(v any) string {
	switch v := v.(type) {
	case map[string]any:
		var parts []string
		for k, x := range v {
			parts = append(parts, k+":"+stringify(x))
		}
		return strings.Join(parts, ",")
	case []any:
		var parts []string
		for _, x := range v {
			parts = append(parts, stringify(x))
		}
		return strings.Join(parts, ",")
	case string:
		return v
	}
	return ""
}

// Every server of a rule becomes its own instance with its detour, and the
// rule lists them in order.
func TestEmitDNSRuleWithSeveralServers(t *testing.T) {
	d, _ := parallelDoc(t)
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 {
		t.Fatalf("errors %v", r.Errors)
	}
	rule := emittedDNSRule(t, r, "example.com")
	if _, ok := rule["server"]; ok {
		t.Fatalf("single server key in %v", rule)
	}
	servers, _ := rule["servers"].([]any)
	want := []any{"shadow dns-cn · 代理", "dns-b", "shadow dns-b · 代理"}
	if !slices.Equal(servers, want) {
		t.Fatalf("servers %v, want %v", servers, want)
	}
	tags := map[string]bool{}
	for _, s := range r.Config["dns"].(map[string]any)["servers"].([]any) {
		tags[s.(map[string]any)["tag"].(string)] = true
	}
	for _, tag := range want {
		if !tags[tag.(string)] {
			t.Errorf("no instance %q in %v", tag, tags)
		}
	}
}

// One server keeps the single server key; a repeated server and detour
// collapses to one.
func TestEmitDNSRuleSingleServerUnchanged(t *testing.T) {
	d, rule := parallelDoc(t)
	rule.SetTargets([]model.DNSTarget{{Server: model.Some("b"), Detour: model.Null()}, {Server: model.Some("b"), Detour: model.Null()}})
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 {
		t.Fatalf("errors %v", r.Errors)
	}
	emitted := emittedDNSRule(t, r, "example.com")
	if emitted["server"] != "dns-b" || emitted["servers"] != nil {
		t.Fatalf("rule %v", emitted)
	}
}

func TestEmitDNSRuleRejectsFakeIPAmongServers(t *testing.T) {
	d, rule := parallelDoc(t)
	d.DNS.Servers = append(d.DNS.Servers, &model.DNSServer{ID: "fk", Tag: "fake", Enabled: true, Type: "fakeip",
		Fields: model.Fields{"inet4_range": "198.18.0.0/15"}})
	rule.SetTargets([]model.DNSTarget{{Server: model.Some("b"), Detour: model.Null()}, {Server: model.Some("fk"), Detour: model.Null()}})
	r := emit.Emit(d, emit.Options{})
	if !slicesContains(r.Errors, "DNS 分流规则「并发」：FakeIP 不能与其他服务器同时查询") {
		t.Fatalf("errors %v", r.Errors)
	}
	if !model.HasInvalidDNSRuleReferences(d, rule) {
		t.Error("rule not reported invalid")
	}
}

// A deleted extra server blocks the rule like a deleted first server.
func TestEmitDNSRuleExtraServerDeleted(t *testing.T) {
	d, rule := parallelDoc(t)
	rule.SetTargets([]model.DNSTarget{{Server: model.Some("b"), Detour: model.Null()}, {Server: model.Some("gone"), Detour: model.Null()}})
	r := emit.Emit(d, emit.Options{})
	if !slicesContains(r.Errors, "DNS 服务器「gone」已删除或停用，请重新选择") {
		t.Fatalf("errors %v", r.Errors)
	}
	if !model.HasInvalidDNSRuleReferences(d, rule) {
		t.Error("rule not reported invalid")
	}
}
