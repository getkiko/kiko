package emit_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// ruleScriptDoc has a rule script and a dns script, a route rule using the
// first as a condition and a DNS rule using the second as its action.
func ruleScriptDoc(t *testing.T) *model.Doc {
	d, _ := parallelDoc(t)
	d.DNS.Rules = nil
	d.HTTPTools = &model.HTTPTools{Scripts: []*model.HTTPScript{
		{ID: "geo", Tag: "geo", Type: model.ScriptRule, Source: "$done(true);", Timeout: 50, Argument: "a"},
		{ID: "ans", Tag: "ans", Type: model.ScriptDNS, Source: "$done({addresses:['192.0.2.7']});"},
		{ID: "gen", Tag: "gen", Type: model.ScriptGeneric, Source: "$done();"},
	}}
	d.Route.Rules = append(d.Route.Rules, &model.RouteRule{ID: "r-script", Name: "脚本", Enabled: true, DeviceID: model.PreDeviceID,
		GroupID: model.RouteGroupsOf(d, model.PreDeviceID)[0].ID,
		Match:   map[string]any{"domain_suffix": []any{"example.com"}, "script": []any{"geo"}}, Action: model.ActionReject})
	rule := &model.DNSRule{ID: "d-script", Name: "脚本应答", DeviceID: model.PreDeviceID, GroupID: model.DNSGroupsOf(d, model.PreDeviceID)[0].ID,
		Enabled: true, Match: map[string]any{"domain_suffix": []any{"example.com"}, "script": []any{"geo"}},
		Action: model.ActionScript, Script: "ans", OnError: model.ScriptOnErrorReject}
	d.DNS.Rules = append(d.DNS.Rules, rule)
	return d
}

func TestEmitRuleScriptReferences(t *testing.T) {
	d := ruleScriptDoc(t)
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	var route map[string]any
	for _, rule := range routeRules(r) {
		if rule["script"] != nil {
			route = rule
		}
	}
	if route == nil || !reflect.DeepEqual(route["script"], []any{"geo"}) || route["domain_suffix"] == nil {
		t.Fatalf("route rule = %v", route)
	}
	dns := emittedDNSRule(t, r, "dns_script")
	if dns["action"] != "script" || dns["dns_script"] != "ans" || dns["on_error"] != "reject" || !reflect.DeepEqual(dns["script"], []any{"geo"}) || dns["server"] != nil {
		t.Fatalf("dns rule = %v", dns)
	}
	// Timeouts of rule scripts are milliseconds.
	for _, raw := range r.Config["scripts"].([]any) {
		item := raw.(map[string]any)
		if item["tag"] == "geo" && (item["timeout"] != "50ms" || item["pattern"] != nil || item["type"] != "rule") {
			t.Errorf("geo = %v", item)
		}
	}
	// A DNS script action without on_error leaves the default.
	d.DNS.Rules[0].OnError = ""
	if rule := emittedDNSRule(t, emit.Emit(d, emit.Options{}), "dns_script"); rule["on_error"] != nil {
		t.Errorf("on_error = %v", rule["on_error"])
	}
}

func TestEmitRuleScriptReferenceErrors(t *testing.T) {
	cases := map[string]struct {
		change func(d *model.Doc)
		want   string
	}{
		"condition missing":    {func(d *model.Doc) { d.Route.Rules[len(d.Route.Rules)-1].Match["script"] = []any{"nope"} }, "不存在"},
		"condition wrong type": {func(d *model.Doc) { d.Route.Rules[len(d.Route.Rules)-1].Match["script"] = []any{"gen"} }, "不是规则脚本"},
		"condition dns type":   {func(d *model.Doc) { d.Route.Rules[len(d.Route.Rules)-1].Match["script"] = []any{"ans"} }, "不是规则脚本"},
		"condition disabled":   {func(d *model.Doc) { d.HTTPTools.Scripts[0].Disabled = true }, "不存在、已停用或无效"},
		"action missing":       {func(d *model.Doc) { d.DNS.Rules[0].Script = "nope" }, "不存在"},
		"action wrong type":    {func(d *model.Doc) { d.DNS.Rules[0].Script = "geo" }, "不是DNS 脚本"},
		"dns condition":        {func(d *model.Doc) { d.DNS.Rules[0].Match["script"] = []any{"ans"} }, "不是规则脚本"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := ruleScriptDoc(t)
			c.change(d)
			r := emit.Emit(d, emit.Options{})
			if !strings.Contains(strings.Join(r.Errors, "\n"), c.want) {
				t.Fatalf("errors = %v, want %q", r.Errors, c.want)
			}
		})
	}
}

// TestEmitRuleScriptCorpus hands the whole emitted configuration to the
// core-side test (app TestRuleScriptEmitAcceptedByCore) in $RULE_SCRIPT_CORPUS.
func TestEmitRuleScriptCorpus(t *testing.T) {
	out := os.Getenv("RULE_SCRIPT_CORPUS")
	if out == "" {
		t.Skip("RULE_SCRIPT_CORPUS not set")
	}
	d := ruleScriptDoc(t)
	d.HTTPTools.Modules = []*model.HTTPModule{newModule("mod_a1", "Demo", model.ModuleContents{
		Scripts: []*model.HTTPScript{
			{ID: "pick", Tag: "pick", Type: model.ScriptRule, Source: "$done(true);"},
			{ID: "dnsjs", Tag: "dnsjs", Type: model.ScriptDNS, Source: "$done({});", Timeout: 200},
		},
		Rules: []*model.ModuleRule{{Type: model.ModuleRuleScript, Value: "pick", Action: model.ModuleActionReject}},
	})}
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	data, err := json.Marshal(r.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A script condition cannot be excluded: an error or timeout counts as no match and the exclusion
// would turn it into one. The emitter refuses it and nothing reaches the core.
func TestEmitScriptConditionRefusesInvert(t *testing.T) {
	cases := map[string]func(d *model.Doc){
		"route legacy invert": func(d *model.Doc) { d.Route.Rules[len(d.Route.Rules)-1].Match["invert"] = true },
		"dns legacy invert":   func(d *model.Doc) { d.DNS.Rules[0].Match["invert"] = true },
		"route not group": func(d *model.Doc) {
			r := d.Route.Rules[len(d.Route.Rules)-1]
			r.ConditionTree = &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
				{Kind: model.ConditionGroup, Mode: "and", Invert: true, Rules: []*model.Condition{
					{Kind: model.ConditionMatch, Match: map[string]any{"script": []any{"geo"}}},
				}},
			}}
		},
		"dns leaf invert": func(d *model.Doc) {
			d.DNS.Rules[0].ConditionTree = &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
				{Kind: model.ConditionMatch, Invert: true, Match: map[string]any{"script": []any{"geo"}}},
			}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			d := ruleScriptDoc(t)
			change(d)
			r := emit.Emit(d, emit.Options{})
			if !strings.Contains(strings.Join(r.Errors, "\n"), "脚本条件不能与「排除」同用") {
				t.Fatalf("errors = %v", r.Errors)
			}
		})
	}
	// Excluding other conditions stays valid.
	d := ruleScriptDoc(t)
	d.Route.Rules[len(d.Route.Rules)-1].ConditionTree = &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
		{Kind: model.ConditionMatch, Invert: true, Match: map[string]any{"domain_suffix": []any{"example.org"}}},
		{Kind: model.ConditionMatch, Match: map[string]any{"script": []any{"geo"}}},
	}}
	if r := emit.Emit(d, emit.Options{}); len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
}
