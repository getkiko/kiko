package emit_test

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// ruleSetDoc is doc with a traffic rule using tags.
func ruleSetDoc(sets []*model.RuleSet, tags ...string) *model.Doc {
	d := doc()
	d.Route.RuleSets = append(d.Route.RuleSets, sets...)
	group := model.RouteGroupsOf(d, model.PreDeviceID)[0].ID
	d.Route.Rules = []*model.RouteRule{{ID: "r", DeviceID: model.PreDeviceID, GroupID: group, Enabled: true,
		Match: map[string]any{"rule_set": anyList(tags)}, Action: model.ActionReject}}
	return d
}

func anyList(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// emittedRuleSets are the route's rule set definitions by tag.
func emittedRuleSets(t *testing.T, d *model.Doc) map[string]map[string]any {
	t.Helper()
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) > 0 {
		t.Fatalf("errors %v", r.Errors)
	}
	out := map[string]map[string]any{}
	list, _ := r.Config["route"].(map[string]any)["rule_set"].([]any)
	for _, item := range list {
		m := item.(map[string]any)
		out[m["tag"].(string)] = m
	}
	return out
}

func asJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

var testOverrides = &model.RuleSetOverrides{
	Add:    []model.RuleSetEntry{{Field: "domain_suffix", Value: "a.example"}, {Field: "ip_cidr", Value: "10.0.0.0/8"}, {Field: "domain_suffix", Value: "b.example"}},
	Remove: []model.RuleSetEntry{{Field: "ip_cidr", Value: "1.1.1.0/24"}, {Field: "domain", Value: "x.example"}, {Field: "domain", Value: "y.example"}},
}

const wantOverrides = `{"add":[{"domain_suffix":["a.example","b.example"]},{"ip_cidr":["10.0.0.0/8"]}],` +
	`"remove":{"domain":["x.example","y.example"],"ip_cidr":["1.1.1.0/24"]}}`

func TestEmitRuleSetOverrides(t *testing.T) {
	sets := []*model.RuleSet{
		{ID: "remote", Tag: "remote-set", Enabled: true, Mode: "remote", URL: "https://example.com/a.srs", Overrides: testOverrides},
		{ID: "inline", Tag: "inline-set", Enabled: true, Mode: "inline", URL: "https://example.com/a.json",
			Rules: []any{map[string]any{"domain": []any{"x.example"}}}, Overrides: testOverrides},
		{ID: model.BuiltinRuleSetID("Gfw"), Tag: "Gfw", Enabled: true, Mode: "inline", URL: model.BuiltinRuleSetURL("Gfw", ""),
			Rules: []any{map[string]any{"domain": []any{"x.example"}}}, Overrides: testOverrides},
		{ID: model.BuiltinRuleSetID("geoip-cn"), Tag: "geoip-cn", Enabled: true, Mode: "remote", URL: model.BuiltinRuleSetURL("geoip-cn", "srs"), Overrides: testOverrides},
		{ID: "plain", Tag: "plain-set", Enabled: true, Mode: "remote", URL: "https://example.com/b.srs", Overrides: &model.RuleSetOverrides{}},
	}
	got := emittedRuleSets(t, ruleSetDoc(sets, "remote-set", "inline-set", "Gfw", "geoip-cn", "plain-set", "geosite-cn"))
	for _, tag := range []string{"remote-set", "inline-set", "Gfw", "geoip-cn"} {
		if s := asJSON(got[tag]["overrides"]); s != wantOverrides {
			t.Errorf("%s overrides %s", tag, s)
		}
	}
	if got["inline-set"]["type"] != "inline" || got["Gfw"]["type"] != "inline" || got["remote-set"]["type"] != "remote" {
		t.Errorf("types %v", got)
	}
	// Empty overrides, and a catalog set without a stored entry, have none.
	for _, tag := range []string{"plain-set", "geosite-cn"} {
		if _, ok := got[tag]["overrides"]; ok || got[tag] == nil {
			t.Errorf("%s: %v", tag, got[tag])
		}
	}
}

func TestEmitRuleSetOverridesOneSide(t *testing.T) {
	sets := []*model.RuleSet{
		{ID: "a", Tag: "add-only", Enabled: true, Mode: "remote", URL: "https://example.com/a.srs",
			Overrides: &model.RuleSetOverrides{Add: []model.RuleSetEntry{{Field: "process_name", Value: "curl"}}}},
		{ID: "b", Tag: "remove-only", Enabled: true, Mode: "remote", URL: "https://example.com/b.srs",
			Overrides: &model.RuleSetOverrides{Remove: []model.RuleSetEntry{{Field: "package_name", Value: "com.example"}}}},
	}
	got := emittedRuleSets(t, ruleSetDoc(sets, "add-only", "remove-only"))
	if s := asJSON(got["add-only"]["overrides"]); s != `{"add":[{"process_name":["curl"]}]}` {
		t.Errorf("add only %s", s)
	}
	if s := asJSON(got["remove-only"]["overrides"]); s != `{"remove":{"package_name":["com.example"]}}` {
		t.Errorf("remove only %s", s)
	}
}

// A rule set the user builds is inline, and traffic and DNS rules can use
// it like any other.
func TestEmitCustomRuleSet(t *testing.T) {
	rules := []any{map[string]any{"domain_suffix": []any{"corp.example"}}, map[string]any{"ip_cidr": []any{"10.1.0.0/16"}}}
	custom := &model.RuleSet{ID: "mine", Tag: "我的规则集", Enabled: true, Mode: "inline", Rules: rules,
		Overrides: &model.RuleSetOverrides{Add: []model.RuleSetEntry{{Field: "domain", Value: "extra.example"}}}}
	if !model.IsCustomRuleSet(custom) || model.RuleSetKind(custom) != "自建" {
		t.Fatalf("custom: %v %s", model.IsCustomRuleSet(custom), model.RuleSetKind(custom))
	}
	d := ruleSetDoc([]*model.RuleSet{custom}, "我的规则集")
	d.DNS.Rules = append(d.DNS.Rules, &model.DNSRule{ID: "d-mine", DeviceID: model.PreDeviceID, Name: "自建 DNS", Enabled: true,
		Match: map[string]any{"rule_set": []any{"我的规则集"}}, Action: model.ActionRoute, Server: model.RefOf(d.DNS.Servers[0].ID)})
	if groups := model.DNSGroupsOf(d, model.PreDeviceID); len(groups) > 0 {
		d.DNS.Rules[len(d.DNS.Rules)-1].GroupID = groups[0].ID
	}
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) > 0 {
		t.Fatalf("errors %v", r.Errors)
	}
	set := emittedRuleSets(t, d)["我的规则集"]
	if set["type"] != "inline" || set["tag"] != "我的规则集" || asJSON(set["rules"]) != asJSON(rules) ||
		asJSON(set["overrides"]) != `{"add":[{"domain":["extra.example"]}]}` {
		t.Errorf("custom set %v", set)
	}
	used := func(rules []any) bool {
		for _, item := range rules {
			if containsTag(item) {
				return true
			}
		}
		return false
	}
	if !used(r.Config["route"].(map[string]any)["rules"].([]any)) {
		t.Errorf("route rules do not use the set: %s", asJSON(r.Config["route"]))
	}
	if !used(r.Config["dns"].(map[string]any)["rules"].([]any)) {
		t.Errorf("dns rules do not use the set: %s", asJSON(r.Config["dns"]))
	}
}

// containsTag finds the custom set's tag in a (possibly logical) rule.
func containsTag(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for _, item := range x {
			if containsTag(item) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if containsTag(item) {
				return true
			}
		}
	case string:
		return x == "我的规则集"
	}
	return false
}

// TestWriteRuleSetOverridesConfig writes a configuration with overrides on
// remote, inline and custom rule sets to EMIT_CHECK_OUT, for sing-box
// check against the core.
func TestWriteRuleSetOverridesConfig(t *testing.T) {
	out := os.Getenv("EMIT_CHECK_OUT")
	if out == "" {
		t.Skip("EMIT_CHECK_OUT is not set")
	}
	var all []model.RuleSetEntry
	for _, f := range model.RuleSetFields {
		value := "x.example"
		switch f.Field {
		case "ip_cidr", "source_ip_cidr":
			value = "10.9.0.0/16"
		case "domain_regex":
			value = `^ads\.`
		case "process_path":
			value = "/usr/bin/curl"
		}
		all = append(all, model.RuleSetEntry{Field: f.Field, Value: value})
	}
	overrides := &model.RuleSetOverrides{Add: all, Remove: all}
	sets := []*model.RuleSet{
		{ID: "remote", Tag: "remote-set", Enabled: true, Mode: "remote", URL: "https://example.com/a.srs", Overrides: overrides},
		{ID: "inline", Tag: "inline-set", Enabled: true, Mode: "inline", URL: "https://example.com/a.json",
			Rules: []any{map[string]any{"domain": []any{"x.example", "y.example"}}}, Overrides: overrides},
		{ID: "mine", Tag: "custom-set", Enabled: true, Mode: "inline",
			Rules: []any{map[string]any{"ip_cidr": []any{"10.9.0.0/16", "10.10.0.0/16"}}}, Overrides: overrides},
	}
	sets = append(sets, &model.RuleSet{ID: "empty", Tag: "empty-set", Enabled: true, Mode: "inline"},
		&model.RuleSet{ID: "added", Tag: "added-set", Enabled: true, Mode: "inline", Overrides: &model.RuleSetOverrides{Add: all}})
	d := ruleSetDoc(sets, "remote-set", "inline-set", "custom-set", "empty-set", "added-set")
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) > 0 {
		t.Fatalf("errors %v", r.Errors)
	}
	if err := os.WriteFile(out, r.JSON, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The core refuses an inline rule set without rules: an empty one has its
// added entries as its rules, or a rule that never matches, on Android
// too.
func TestEmitEmptyInlineRuleSets(t *testing.T) {
	sets := []*model.RuleSet{
		{ID: "empty", Tag: "empty-set", Enabled: true, Mode: "inline"},
		{ID: "added", Tag: "added-set", Enabled: true, Mode: "inline", Overrides: &model.RuleSetOverrides{
			Add:    []model.RuleSetEntry{{Field: "domain", Value: "a.example"}, {Field: "ip_cidr", Value: "10.0.0.0/8"}},
			Remove: []model.RuleSetEntry{{Field: "domain", Value: "x.example"}}}},
		{ID: "pending", Tag: "pending-set", Enabled: true, Mode: "inline", URL: "https://example.com/p.json"},
	}
	d := ruleSetDoc(sets, "empty-set", "added-set", "pending-set")
	for _, options := range []emit.Options{{}, {LocalRuleSetCatalog: model.LocalRuleSetCatalog{}}} {
		r := emit.Emit(d, options)
		if len(r.Errors) > 0 {
			t.Fatalf("errors %v", r.Errors)
		}
		got := map[string]map[string]any{}
		for _, item := range r.Config["route"].(map[string]any)["rule_set"].([]any) {
			m := item.(map[string]any)
			got[m["tag"].(string)] = m
		}
		never := `[{"domain_regex":["[^\\s\\S]"]}]`
		if s := asJSON(got["empty-set"]["rules"]); got["empty-set"]["type"] != "inline" || s != never {
			t.Errorf("empty %v", got["empty-set"])
		}
		if s := asJSON(got["added-set"]["rules"]); s != `[{"domain":["a.example"]},{"ip_cidr":["10.0.0.0/8"]}]` || got["added-set"]["overrides"] != nil {
			t.Errorf("added %v", got["added-set"])
		}
		if s := asJSON(got["pending-set"]["rules"]); s != never || !slicesContains(r.Warnings, "规则集「pending-set」尚未更新内容，暂只匹配添加的规则") {
			t.Errorf("pending %v warnings %v", got["pending-set"], r.Warnings)
		}
	}
	if _, err := regexp.Compile(`[^\s\S]`); err != nil {
		t.Error(err)
	}
}
