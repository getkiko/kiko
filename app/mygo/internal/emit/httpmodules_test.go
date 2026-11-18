package emit_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/modules"
)

func modScript(tag, source string) *model.HTTPScript {
	return &model.HTTPScript{ID: tag, Tag: tag, Type: model.ScriptHTTPResponse, Pattern: `^https://a\.com/`, Source: source}
}

func newModule(id, name string, contents model.ModuleContents) *model.HTTPModule {
	m := model.NewHTTPModule()
	m.ID, m.Name, m.Enabled = id, name, true
	m.Source = model.ModuleSource{Kind: model.ModuleSourceInline}
	m.Contents = contents
	m.ApproveContents(contents)
	return m
}

func moduleDoc(mitm bool, modules ...*model.HTTPModule) *model.Doc {
	d := bareDoc()
	d.HTTPTools = &model.HTTPTools{Modules: modules}
	if mitm {
		d.HTTPTools.MITM = &model.MITMSettings{Enabled: true}
	}
	return d
}

func demoContents() model.ModuleContents {
	return model.ModuleContents{
		Hostnames: &model.MITMHostnames{Include: []string{"m.example.com"}, Exclude: []string{"x.example.com"}},
		Rewrites: &model.HTTPRewrites{
			URL:      []*model.URLRewrite{{ID: "u1", Pattern: `^https://m\.example\.com/ad`, Action: model.URLActionReject}},
			Headers:  []*model.HeaderRewrite{{ID: "h1", Pattern: `^https://m\.example\.com/`, Scope: "request", Ops: []*model.HeaderEdit{{Action: "delete", Name: "X-A"}}}},
			MapLocal: []*model.MapLocal{{ID: "l1", Pattern: `^https://m\.example\.com/l`, Status: 200, Body: "{}"}},
		},
		Scripts: []*model.HTTPScript{modScript("s1", "$done({});")},
		Rules: []*model.ModuleRule{
			{Type: model.ModuleRuleDomainSuffix, Value: "ads.example.com", Action: model.ModuleActionReject},
			{Type: model.ModuleRuleDomainSuffix, Value: "track.example.com", Action: model.ModuleActionReject},
			{Type: model.ModuleRuleIPCIDR, Value: "10.1.0.0/16", Action: model.ModuleActionDirect},
			{Type: model.ModuleRuleDomain, Value: "d.example.com", Action: model.ModuleActionReject},
		},
	}
}

func routeRules(r emit.Result) []map[string]any {
	route, _ := r.Config["route"].(map[string]any)
	var out []map[string]any
	for _, raw := range route["rules"].([]any) {
		out = append(out, raw.(map[string]any))
	}
	return out
}

func httpRewrite(r emit.Result) map[string]any {
	mitm, _ := r.Config["mitm"].(map[string]any)
	rw, _ := mitm["http_rewrite"].(map[string]any)
	return rw
}

func includeHosts(r emit.Result) []any {
	mitm, _ := r.Config["mitm"].(map[string]any)
	hosts, _ := mitm["hostnames"].(map[string]any)
	list, _ := hosts["include"].([]any)
	return list
}

func scriptTags(r emit.Result) []string {
	list, _ := r.Config["scripts"].([]any)
	var out []string
	for _, s := range list {
		out = append(out, s.(map[string]any)["tag"].(string))
	}
	return out
}

func warningsContaining(r emit.Result, sub string) []string {
	var out []string
	for _, w := range r.Warnings {
		if strings.Contains(w, sub) {
			out = append(out, w)
		}
	}
	return out
}

func TestEmitModuleMerge(t *testing.T) {
	d := moduleDoc(true, newModule("mod_a1", "Demo", demoContents()))
	d.HTTPTools.MITM.Hostnames = &model.MITMHostnames{Include: []string{"user.example.com"}}
	d.HTTPTools.Rewrites = &model.HTTPRewrites{URL: []*model.URLRewrite{{ID: "user_u", Pattern: "x", Action: model.URLActionReject}}}
	d.HTTPTools.Scripts = []*model.HTTPScript{modScript("user", "1;")}
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	if got := includeHosts(r); !reflect.DeepEqual(got, []any{"user.example.com", "m.example.com"}) {
		t.Errorf("include = %v", got)
	}
	mitm := r.Config["mitm"].(map[string]any)
	if ex := mitm["hostnames"].(map[string]any)["exclude"]; !reflect.DeepEqual(ex, []any{"x.example.com"}) {
		t.Errorf("exclude = %v", ex)
	}
	rw := httpRewrite(r)
	urls := rw["url"].([]any)
	if len(urls) != 2 || urls[0].(map[string]any)["id"] != "user_u" || urls[1].(map[string]any)["id"] != "mod:mod_a1:1" {
		t.Errorf("url rules = %v", urls)
	}
	if h := rw["headers"].([]any); h[0].(map[string]any)["id"] != "mod:mod_a1:2" {
		t.Errorf("header id = %v", h)
	}
	if l := rw["map_local"].([]any); l[0].(map[string]any)["id"] != "mod:mod_a1:3" {
		t.Errorf("map local id = %v", l)
	}
	if tags := scriptTags(r); !reflect.DeepEqual(tags, []string{"user", "mod_a1-s1"}) {
		t.Errorf("tags = %v", tags)
	}
	scripts := r.Config["scripts"].([]any)
	if scripts[1].(map[string]any)["source_path"] != emit.ScriptSourcePath("$done({});") {
		t.Errorf("source path: %v", scripts[1])
	}
	if exp := r.Config["experimental"].(map[string]any); exp["cache_file"] == nil {
		t.Error("cache file must be on for module scripts")
	}
}

func TestEmitModuleDisabledAndAbsent(t *testing.T) {
	m := newModule("mod_a1", "Demo", demoContents())
	m.Enabled = false
	d := moduleDoc(true, m)
	off := emit.Emit(d, emit.Options{})
	plain := bareDoc()
	plain.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	base := emit.Emit(plain, emit.Options{})
	if !bytes.Equal(off.JSON, base.JSON) {
		t.Error("a disabled module changes the emitted config")
	}
	// Switching it on changes the config; switching back restores it.
	m.Enabled = true
	on := emit.Emit(d, emit.Options{})
	if bytes.Equal(on.JSON, base.JSON) {
		t.Error("enabling a module does not change the config")
	}
	m.Enabled = false
	if again := emit.Emit(d, emit.Options{}); !bytes.Equal(again.JSON, base.JSON) {
		t.Error("disabling does not restore the config")
	}
}

func TestEmitModuleMITMOff(t *testing.T) {
	d := moduleDoc(false, newModule("mod_a1", "Demo", demoContents()))
	r := emit.Emit(d, emit.Options{})
	if _, ok := r.Config["mitm"]; ok {
		t.Error("mitm emitted")
	}
	if _, ok := r.Config["scripts"]; ok {
		t.Errorf("module scripts emitted without decryption: %v", r.Config["scripts"])
	}
	// The routing rules do not depend on decryption.
	var ids int
	for _, rule := range routeRules(r) {
		if rule["domain_suffix"] != nil || rule["ip_cidr"] != nil && rule["action"] == nil {
			ids++
		}
	}
	if ids == 0 {
		t.Errorf("module routing rules missing: %v", routeRules(r))
	}
	// A user script keeps running without decryption.
	d.HTTPTools.Scripts = []*model.HTTPScript{modScript("user", "1;")}
	r = emit.Emit(d, emit.Options{})
	if tags := scriptTags(r); !reflect.DeepEqual(tags, []string{"user"}) {
		t.Errorf("tags = %v", tags)
	}
}

func TestEmitModulePendingNotEmitted(t *testing.T) {
	m := newModule("mod_a1", "Demo", model.ModuleContents{Hostnames: &model.MITMHostnames{Include: []string{"old.example.com"}}})
	m.Pending = &model.ModulePending{Contents: demoContents()}
	r := emit.Emit(moduleDoc(true, m), emit.Options{})
	if got := includeHosts(r); !reflect.DeepEqual(got, []any{"old.example.com"}) {
		t.Errorf("include = %v", got)
	}
	if httpRewrite(r) != nil || r.Config["scripts"] != nil {
		t.Error("pending content emitted")
	}
	if len(r.Warnings) != 0 {
		t.Errorf("warnings: %v", r.Warnings)
	}
}

func TestEmitModuleUnapproved(t *testing.T) {
	c := demoContents()
	c.Hostnames.Include = []string{"ok.example.com", "new.example.com"}
	c.Scripts = []*model.HTTPScript{modScript("s1", "ok();"), modScript("s2", "new();")}
	m := newModule("mod_a1", "Demo", c)
	m.Approved = model.ModuleApproved{Hostnames: []string{"ok.example.com"}, Scripts: []string{model.ScriptApprovalKey(modScript("s1", "ok();"))},
		Direct: []string{"ip-cidr,10.1.0.0/16"}}
	r := emit.Emit(moduleDoc(true, m), emit.Options{})
	if got := includeHosts(r); !reflect.DeepEqual(got, []any{"ok.example.com"}) {
		t.Errorf("include = %v", got)
	}
	if tags := scriptTags(r); !reflect.DeepEqual(tags, []string{"mod_a1-s1"}) {
		t.Errorf("tags = %v", tags)
	}
	if w := warningsContaining(r, "尚未确认"); len(w) != 2 {
		t.Errorf("warnings = %v", r.Warnings)
	}
	// Rewrites and rules of the module still apply.
	if httpRewrite(r) == nil {
		t.Error("rewrites dropped")
	}
}

func TestEmitModuleScriptConflictsAndLimit(t *testing.T) {
	d := moduleDoc(true, newModule("mod_a1", "Demo", model.ModuleContents{Scripts: []*model.HTTPScript{modScript("s1", "1;")}}))
	d.HTTPTools.Scripts = []*model.HTTPScript{modScript("mod_a1-s1", "user();")}
	r := emit.Emit(d, emit.Options{})
	if tags := scriptTags(r); !reflect.DeepEqual(tags, []string{"mod_a1-s1"}) {
		t.Errorf("tags = %v", tags)
	}
	if w := warningsContaining(r, "重复"); len(w) != 1 {
		t.Errorf("warnings = %v", r.Warnings)
	}

	d = moduleDoc(true, newModule("mod_a1", "Demo", model.ModuleContents{Scripts: []*model.HTTPScript{modScript("s1", "1;")}}))
	for i := 0; i < model.MaxScripts; i++ {
		d.HTTPTools.Scripts = append(d.HTTPTools.Scripts, modScript("u"+strconv.Itoa(i), "1;"))
	}
	r = emit.Emit(d, emit.Options{})
	if len(scriptTags(r)) != model.MaxScripts {
		t.Errorf("scripts = %d", len(scriptTags(r)))
	}
	if w := warningsContaining(r, "256"); len(w) != 1 || !strings.Contains(w[0], "Demo") {
		t.Errorf("warnings = %v", r.Warnings)
	}
}

func TestEmitModuleMapLocalTotal(t *testing.T) {
	big := strings.Repeat("a", model.MaxMapLocalBody)
	user := &model.HTTPRewrites{MapLocal: []*model.MapLocal{{ID: "u1", Pattern: "a", Status: 200, Body: big}, {ID: "u2", Pattern: "b", Status: 200, Body: big}}}
	m := newModule("mod_a1", "Demo", model.ModuleContents{Rewrites: &model.HTTPRewrites{MapLocal: []*model.MapLocal{{ID: "l", Pattern: "c", Status: 200, Body: "x"}}}})
	d := moduleDoc(true, m)
	d.HTTPTools.Rewrites = user
	r := emit.Emit(d, emit.Options{})
	if l := httpRewrite(r)["map_local"].([]any); len(l) != 2 {
		t.Errorf("map local = %d", len(l))
	}
	if w := warningsContaining(r, "4 MiB"); len(w) != 1 {
		t.Errorf("warnings = %v", r.Warnings)
	}
}

func TestEmitModuleRoutes(t *testing.T) {
	d := moduleDoc(false, newModule("mod_a1", "Demo", demoContents()))
	pre := &model.RouteRule{ID: "user_pre", Name: "用户前置", Enabled: true, DeviceID: model.PreDeviceID, Match: map[string]any{"domain": []any{"u.com"}}, Action: model.ActionReject}
	d.Route.Rules = append(d.Route.Rules, pre)
	r := emit.Emit(d, emit.Options{})
	rules := routeRules(r)
	first := -1
	for i, rule := range rules {
		if rule["domain"] != nil && reflect.DeepEqual(rule["domain"], []any{"u.com"}) {
			first = i
		}
	}
	if first < 0 || first+4 >= len(rules) {
		t.Fatalf("rules = %v", rules)
	}
	// Consecutive reject rules of one module share a route rule; the direct
	// rule and the later reject rule stay apart.
	if rule := rules[first+1]; !reflect.DeepEqual(rule["domain_suffix"], []any{"ads.example.com", "track.example.com"}) || rule["action"] != "reject" {
		t.Errorf("grouped rule = %v", rule)
	}
	if rule := rules[first+2]; !reflect.DeepEqual(rule["ip_cidr"], []any{"10.1.0.0/16"}) || rule["outbound"] == nil || rule["action"] != nil {
		t.Errorf("direct rule = %v", rule)
	}
	if rule := rules[first+3]; !reflect.DeepEqual(rule["domain"], []any{"d.example.com"}) || rule["action"] != "reject" {
		t.Errorf("last rule = %v", rule)
	}
	// The rule map names the module and keeps the owner.
	byIndex := map[int]string{}
	for _, ref := range r.RuleMap {
		byIndex[ref.Index] = ref.ID + "|" + ref.Owner + "|" + ref.Name + "|" + ref.Section
	}
	if got := byIndex[first+1]; got != "mod:mod_a1:1|pre|模块 · Demo|route" {
		t.Errorf("ref of grouped rule = %q", got)
	}
	if got := byIndex[first+2]; got != "mod:mod_a1:3|pre|模块 · Demo|route" {
		t.Errorf("ref of direct rule = %q", got)
	}
	if byIndex[first] == "" || !strings.HasPrefix(byIndex[first], "user_pre|pre|") {
		t.Errorf("user rule ref = %q", byIndex[first])
	}
}

func TestEmitModuleRoutesWithoutDirectOutbound(t *testing.T) {
	d := moduleDoc(false, newModule("mod_a1", "Demo", demoContents()))
	d.Outbounds = nil
	r := emit.Emit(d, emit.Options{})
	if w := warningsContaining(r, "直连出站"); len(w) != 1 {
		t.Errorf("warnings = %v", r.Warnings)
	}
	for _, rule := range routeRules(r) {
		if reflect.DeepEqual(rule["ip_cidr"], []any{"10.1.0.0/16"}) {
			t.Errorf("direct rule emitted: %v", rule)
		}
	}
}

func TestEmitModuleDeterministic(t *testing.T) {
	d := moduleDoc(true, newModule("mod_a1", "Demo", demoContents()), newModule("mod_b2", "Other", model.ModuleContents{Scripts: []*model.HTTPScript{modScript("s1", "2;")}}))
	a := emit.Emit(d, emit.Options{})
	b := emit.Emit(d, emit.Options{})
	if !bytes.Equal(a.JSON, b.JSON) || !reflect.DeepEqual(a.RuleMap, b.RuleMap) {
		t.Error("two emits differ")
	}
	if tags := scriptTags(a); !reflect.DeepEqual(tags, []string{"mod_a1-s1", "mod_b2-s1"}) {
		t.Errorf("module order: %v", tags)
	}
}

func TestEmitModuleInvalidItemsSkipped(t *testing.T) {
	c := model.ModuleContents{Rewrites: &model.HTTPRewrites{URL: []*model.URLRewrite{
		{ID: "bad", Pattern: "(", Action: model.URLActionReject},
		{ID: "ok", Pattern: "a", Action: model.URLActionReject},
	}}}
	r := emit.Emit(moduleDoc(true, newModule("mod_a1", "Demo", c)), emit.Options{})
	urls := httpRewrite(r)["url"].([]any)
	if len(urls) != 1 || urls[0].(map[string]any)["id"] != "mod:mod_a1:2" {
		t.Errorf("urls = %v", urls)
	}
	if w := warningsContaining(r, "模块「Demo」第 1 条"); len(w) != 1 {
		t.Errorf("warnings = %v", r.Warnings)
	}
}

// fixtureModules converts the fixtures of the modules package into enabled,
// confirmed modules.
func fixtureModules(t testing.TB) []*model.HTTPModule {
	files, err := filepath.Glob("../modules/testdata/*.sgmodule")
	if err != nil || len(files) < 5 {
		t.Fatalf("fixtures: %v %v", files, err)
	}
	var out []*model.HTTPModule
	for i, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		res, err := modules.ParseSurge(string(data), modules.Options{})
		if err != nil {
			t.Fatal(err)
		}
		fetched := map[string][]byte{}
		for _, f := range res.Fetches {
			fetched[f.URL] = []byte("$done({});")
			if f.Kind == modules.FetchData {
				fetched[f.URL] = []byte("{}")
			}
		}
		if _, err := res.Resolve(fetched); err != nil {
			t.Fatal(err)
		}
		m := newModule("mod_fx"+strconv.Itoa(i), res.Name, res.Contents)
		m.Raw = string(data)
		out = append(out, m)
	}
	// A hand-written module for the syntax the public fixtures lack.
	res, err := modules.ParseSurge(syntheticModule, modules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Resolve(nil); err != nil {
		t.Fatal(err)
	}
	out = append(out, newModule("mod_fxsyn", res.Name, res.Contents))
	return out
}

// syntheticModule covers body rewrites (regex and jq), local mappings with
// text, base64 and tiny-gif data, and every URL rewrite action.
const syntheticModule = `#!name=Synthetic
[Body Rewrite]
http-response-replace-regex ^https://syn\.example\.com/ "ad":true "ad":false
http-request-replace-regex ^https://syn\.example\.com/ (\d+)px $1em
http-response-jq ^https://syn\.example\.com/j 'del(.data.ads, .list[].promo) | .data.on = true | .n = [1,2]'
[Map Local]
^https://syn\.example\.com/t data="{"a":1}" status-code=200 header="Content-Type:application/json|X-A:b"
^https://syn\.example\.com/b data-type=base64 data="aGVsbG8=" status-code=201
^https://syn\.example\.com/g data-type=tiny-gif
[URL Rewrite]
^https://syn\.example\.com/a - reject
^https://syn\.example\.com/b - reject-200
^https://syn\.example\.com/c - reject-img
^https://syn\.example\.com/d - reject-dict
^https://syn\.example\.com/e - reject-array
^https://syn\.example\.com/f - reject-tinygif
^https://syn\.example\.com/(g) https://other.example.com/$1 302
^https://syn\.example\.com/(h) https://other.example.com/$1 307
^https://syn\.example\.com/(i) https://other.example.com/$1 header
[Header Rewrite]
http-response ^https://syn\.example\.com/ header-replace-regex Location ^http:// https://
http-request ^https://syn\.example\.com/ header-add X-Syn "a b"
[MITM]
hostname = syn.example.com
`

func TestEmitFixtureModules(t *testing.T) {
	d := moduleDoc(true, fixtureModules(t)...)
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	for _, w := range r.Warnings {
		t.Logf("warning: %s", w)
	}
	if httpRewrite(r) == nil || len(scriptTags(r)) == 0 || len(includeHosts(r)) == 0 {
		t.Fatalf("fixtures emitted nothing: %v", r.Config["mitm"])
	}
	// Every module script and rewrite rule the model accepted reaches the
	// core config (invalid ones would have been skipped with a warning).
	for _, w := range r.Warnings {
		if strings.Contains(w, "无效") {
			t.Errorf("module item refused: %s", w)
		}
	}
	// Hand the emitted sections to the core-side test.
	if out := os.Getenv("MODULE_CORPUS"); out != "" {
		mitm := r.Config["mitm"].(map[string]any)
		data, err := json.Marshal(map[string]any{"http_rewrite": mitm["http_rewrite"], "scripts": r.Config["scripts"], "mitm_hostnames": mitm["hostnames"]})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmitModuleUnconfirmedDirectRuleIsSkipped(t *testing.T) {
	m := newModule("mod_a1", "Demo", demoContents())
	m.Approved.Direct = nil
	r := emit.Emit(moduleDoc(false, m), emit.Options{})
	for _, rule := range routeRules(r) {
		if reflect.DeepEqual(rule["ip_cidr"], []any{"10.1.0.0/16"}) {
			t.Errorf("unconfirmed direct rule emitted: %v", rule)
		}
	}
	if w := warningsContaining(r, "直连规则尚未确认"); len(w) != 1 {
		t.Errorf("warnings = %v", r.Warnings)
	}
	// The reject rules of the module still apply.
	found := false
	for _, rule := range routeRules(r) {
		if rule["action"] == "reject" {
			found = true
		}
	}
	if !found {
		t.Error("reject rules dropped")
	}
	m.ApproveContents(m.Contents)
	r = emit.Emit(moduleDoc(false, m), emit.Options{})
	if w := warningsContaining(r, "直连规则尚未确认"); len(w) != 0 {
		t.Errorf("warnings after approval = %v", r.Warnings)
	}
}

func TestEmitModuleDuplicateIDsAreSkipped(t *testing.T) {
	a := newModule("mod_a1", "First", model.ModuleContents{Rewrites: &model.HTTPRewrites{URL: []*model.URLRewrite{{ID: "u", Pattern: "a", Action: model.URLActionReject}}}})
	b := newModule("mod_a1", "Second", model.ModuleContents{Rewrites: &model.HTTPRewrites{URL: []*model.URLRewrite{{ID: "u", Pattern: "b", Action: model.URLActionReject}}}})
	r := emit.Emit(moduleDoc(true, a, b), emit.Options{})
	urls := httpRewrite(r)["url"].([]any)
	if len(urls) != 1 || urls[0].(map[string]any)["pattern"] != "a" {
		t.Errorf("urls = %v", urls)
	}
	if w := warningsContaining(r, "「Second」的标识重复"); len(w) != 1 {
		t.Errorf("warnings = %v", r.Warnings)
	}
}

func TestEmitModuleCountAndTotalLimits(t *testing.T) {
	var mods []*model.HTTPModule
	for i := 0; i < model.MaxModules+2; i++ {
		mods = append(mods, newModule("mod_c"+strconv.Itoa(i), "M"+strconv.Itoa(i), model.ModuleContents{}))
	}
	r := emit.Emit(moduleDoc(true, mods...), emit.Options{})
	if w := warningsContaining(r, "模块超过"); len(w) != 2 {
		t.Errorf("count warnings = %v", r.Warnings)
	}

	var big []*model.HTTPModule
	for i := 0; i < 7; i++ {
		var rules []*model.ModuleRule
		for j := 0; j < 8000; j++ {
			rules = append(rules, &model.ModuleRule{Type: model.ModuleRuleDomain, Value: "h" + strconv.Itoa(j) + ".m" + strconv.Itoa(i) + ".example.com", Action: model.ModuleActionReject})
		}
		big = append(big, newModule("mod_r"+strconv.Itoa(i), "R"+strconv.Itoa(i), model.ModuleContents{Rules: rules}))
	}
	r = emit.Emit(moduleDoc(false, big...), emit.Options{})
	if w := warningsContaining(r, "总量过大"); len(w) != 1 || !strings.Contains(w[0], "R6") {
		t.Errorf("total warnings = %v", w)
	}
}

func TestEmitModuleRuleAndDNSScripts(t *testing.T) {
	c := model.ModuleContents{
		Scripts: []*model.HTTPScript{
			{ID: "pick", Tag: "pick", Type: model.ScriptRule, Source: "$done(true);"},
			{ID: "dnsjs", Tag: "dnsjs", Type: model.ScriptDNS, Source: "$done({});", Timeout: 200},
			{ID: "off", Tag: "off", Type: model.ScriptRule, Source: "$done(false);", Disabled: true},
		},
		Rules: []*model.ModuleRule{
			{Type: model.ModuleRuleScript, Value: "pick", Action: model.ModuleActionReject},
			{Type: model.ModuleRuleScript, Value: "off", Action: model.ModuleActionReject},
			{Type: model.ModuleRuleDomain, Value: "a.example.com", Action: model.ModuleActionReject},
		},
	}
	// Decryption is off: rule and dns scripts still reach the core.
	r := emit.Emit(moduleDoc(false, newModule("mod_a1", "Demo", c)), emit.Options{})
	if tags := scriptTags(r); !reflect.DeepEqual(tags, []string{"mod_a1-pick", "mod_a1-dnsjs"}) {
		t.Fatalf("tags = %v\nerrors %v", tags, r.Errors)
	}
	if len(r.Errors) != 0 {
		t.Errorf("errors: %v", r.Errors)
	}
	var found, plain bool
	for _, rule := range routeRules(r) {
		if got, ok := rule["script"].([]any); ok {
			found = reflect.DeepEqual(got, []any{"mod_a1-pick"}) && rule["action"] == "reject"
		}
		if rule["domain"] != nil {
			plain = true
		}
	}
	if !found || !plain {
		t.Errorf("route rules: %v", routeRules(r))
	}
	dns, _ := r.Config["dns"].(map[string]any)
	rules, _ := dns["rules"].([]any)
	if len(rules) == 0 {
		t.Fatalf("dns rules: %v", dns)
	}
	first, _ := rules[0].(map[string]any)
	if first["action"] != "script" || first["dns_script"] != "mod_a1-dnsjs" {
		t.Errorf("first dns rule = %v", first)
	}
	scripts, _ := r.Config["scripts"].([]any)
	for _, raw := range scripts {
		item := raw.(map[string]any)
		if item["tag"] == "mod_a1-dnsjs" && item["timeout"] != "200ms" {
			t.Errorf("timeout = %v", item["timeout"])
		}
	}
}
