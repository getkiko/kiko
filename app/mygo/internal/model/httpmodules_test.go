package model

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func testModule() *HTTPModule {
	m := NewHTTPModule()
	m.ID = "mod_abc123"
	m.Name = "Demo"
	m.Source = ModuleSource{Kind: ModuleSourceInline}
	m.Enabled = true
	return m
}

func moduleScript(tag, source string) *HTTPScript {
	return &HTTPScript{ID: tag, Tag: tag, Type: ScriptHTTPResponse, Pattern: `^https://a\.com/`, Source: source}
}

func TestModuleIssue(t *testing.T) {
	good := testModule()
	good.Contents = ModuleContents{
		Hostnames: &MITMHostnames{Include: []string{"a.com", "*.b.com"}, Exclude: []string{"c.com"}},
		Rewrites:  &HTTPRewrites{URL: []*URLRewrite{{ID: "u1", Pattern: `^https://a\.com/`, Action: URLActionReject}}},
		Scripts:   []*HTTPScript{moduleScript("s1", "$done({});")},
		Rules:     []*ModuleRule{{Type: ModuleRuleDomain, Value: "ads.a.com", Action: ModuleActionReject}},
	}
	if issue := good.Issue(); issue != "" {
		t.Fatalf("good module: %s", issue)
	}
	cases := map[string]func(m *HTTPModule){
		"id":           func(m *HTTPModule) { m.ID = "x" },
		"id uppercase": func(m *HTTPModule) { m.ID = "mod_ABC" },
		"name":         func(m *HTTPModule) { m.Name = " " },
		"name ctrl":    func(m *HTTPModule) { m.Name = "a\x01" },
		"source":       func(m *HTTPModule) { m.Source.Kind = "ftp" },
		"url empty":    func(m *HTTPModule) { m.Source = ModuleSource{Kind: ModuleSourceURL} },
		"interval":     func(m *HTTPModule) { m.UpdateInterval = MaxModuleUpdateHours + 1 },
		"negative":     func(m *HTTPModule) { m.UpdateInterval = -1 },
		"raw":          func(m *HTTPModule) { m.Raw = strings.Repeat("a", MaxModuleRaw+1) },
		"arg name":     func(m *HTTPModule) { m.Arguments = []ModuleArgument{{Name: "a b"}} },
		"arg dup":      func(m *HTTPModule) { m.Arguments = []ModuleArgument{{Name: "a"}, {Name: "a"}} },
		"arg value":    func(m *HTTPModule) { m.Arguments = []ModuleArgument{{Name: "a", Value: "x\ny"}} },
		"host":         func(m *HTTPModule) { m.Contents.Hostnames.Include = []string{"a b"} },
		"url rule":     func(m *HTTPModule) { m.Contents.Rewrites.URL[0].Pattern = "(" },
		"max body":     func(m *HTTPModule) { m.Contents.Rewrites.MaxBodySize = 4096 },
		"script":       func(m *HTTPModule) { m.Contents.Scripts[0].Source = "" },
		"script tag":   func(m *HTTPModule) { m.Contents.Scripts[0].Tag = strings.Repeat("a", MaxModuleScriptTag+1) },
		"script dup":   func(m *HTTPModule) { m.Contents.Scripts = append(m.Contents.Scripts, moduleScript("s1", "1;")) },
		"rule type":    func(m *HTTPModule) { m.Contents.Rules[0].Type = "geoip" },
		"rule action":  func(m *HTTPModule) { m.Contents.Rules[0].Action = "proxy" },
		"rule domain":  func(m *HTTPModule) { m.Contents.Rules[0].Value = "A.com" },
		"pending": func(m *HTTPModule) {
			m.Pending = &ModulePending{Contents: ModuleContents{Rules: []*ModuleRule{{Type: "x"}}}}
		},
	}
	for name, mutate := range cases {
		m := *good
		c := good.Contents
		rw := *good.Contents.Rewrites
		rw.URL = []*URLRewrite{{ID: "u1", Pattern: `^https://a\.com/`, Action: URLActionReject}}
		hosts := *good.Contents.Hostnames
		c.Rewrites, c.Hostnames = &rw, &hosts
		c.Scripts = []*HTTPScript{moduleScript("s1", "$done({});")}
		c.Rules = []*ModuleRule{{Type: ModuleRuleDomain, Value: "ads.a.com", Action: ModuleActionReject}}
		m.Contents = c
		mutate(&m)
		if m.Issue() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestModuleRuleIssue(t *testing.T) {
	ok := []ModuleRule{
		{ModuleRuleDomain, "a.com", ModuleActionReject, false},
		{ModuleRuleDomainSuffix, "a.com", ModuleActionDirect, false},
		{ModuleRuleDomainKeyword, "track", ModuleActionReject, false},
		{ModuleRuleIPCIDR, "1.2.3.0/24", ModuleActionReject, true},
		{ModuleRuleIPCIDR6, "2001:db8::/32", ModuleActionDirect, false},
	}
	for _, r := range ok {
		if issue := r.Issue(); issue != "" {
			t.Errorf("%+v: %s", r, issue)
		}
	}
	bad := []ModuleRule{
		{ModuleRuleDomain, "", ModuleActionReject, false},
		{ModuleRuleDomain, "a..com", ModuleActionReject, false},
		{ModuleRuleDomain, "*.a.com", ModuleActionReject, false},
		{ModuleRuleDomainKeyword, "a,b", ModuleActionReject, false},
		{ModuleRuleDomainKeyword, "a b", ModuleActionReject, false},
		{ModuleRuleIPCIDR, "1.2.3.4", ModuleActionReject, false},
		{ModuleRuleIPCIDR, "2001:db8::/32", ModuleActionReject, false},
		{ModuleRuleIPCIDR6, "1.2.3.0/24", ModuleActionReject, false},
		{ModuleRuleIPCIDR, "::ffff:1.2.3.4/120", ModuleActionReject, false},
		{ModuleRuleDomain, "a.com", "", false},
	}
	for _, r := range bad {
		if r.Issue() == "" {
			t.Errorf("%+v accepted", r)
		}
	}
}

func TestModuleLimits(t *testing.T) {
	m := testModule()
	for i := 0; i < MaxModuleHosts+1; i++ {
		m.Contents.Hostnames = &MITMHostnames{Include: append(m.Contents.Hostnames.includes(), "h"+itoaTest(i)+".com")}
	}
	if m.Issue() == "" {
		t.Error("too many hostnames accepted")
	}
	m = testModule()
	for i := 0; i <= MaxModuleScripts; i++ {
		m.Contents.Scripts = append(m.Contents.Scripts, moduleScript("s"+itoaTest(i), "1;"))
	}
	if m.Issue() == "" {
		t.Error("too many scripts accepted")
	}
	m = testModule()
	big := strings.Repeat("a", MaxScriptSource-100)
	for i := 0; i < 5; i++ {
		m.Contents.Scripts = append(m.Contents.Scripts, moduleScript("s"+itoaTest(i), big+itoaTest(i)))
	}
	if m.Issue() == "" {
		t.Error("sources over the module total accepted")
	}
	tools := &HTTPTools{}
	for i := 0; i <= MaxModules; i++ {
		tools.Modules = append(tools.Modules, &HTTPModule{ID: "mod_" + itoaTest(i)})
	}
	if tools.ModulesIssue() != "模块过多" {
		t.Errorf("module count: %s", tools.ModulesIssue())
	}
	tools = &HTTPTools{Modules: []*HTTPModule{testModule(), testModule()}}
	if !strings.Contains(tools.ModulesIssue(), "重复") {
		t.Errorf("duplicate ids: %q", tools.ModulesIssue())
	}
}

func itoaTest(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestModuleChangeAndApproval(t *testing.T) {
	old := ModuleContents{
		Hostnames: &MITMHostnames{Include: []string{"a.com"}},
		Scripts:   []*HTTPScript{moduleScript("s1", "old();")},
	}
	next := ModuleContents{
		Hostnames: &MITMHostnames{Include: []string{"A.com", "b.com", "*.c.com"}, Exclude: []string{"x.com"}},
		Scripts:   []*HTTPScript{moduleScript("s1", "new();"), moduleScript("s2", "old();"), moduleScript("s3", "third();")},
	}
	diff := NewModuleChange(old, next)
	if strings.Join(diff.Hostnames, ",") != "b.com,*.c.com" {
		t.Errorf("hostnames = %v", diff.Hostnames)
	}
	if len(diff.Scripts) != 2 || diff.Scripts[0].Tag != "s1" || !diff.Scripts[0].Changed || diff.Scripts[1].Tag != "s3" || diff.Scripts[1].Changed {
		t.Errorf("scripts = %+v", diff.Scripts)
	}
	if diff.Scripts[0].SHA256 != HashModuleRaw("new();") {
		t.Error("sha256")
	}
	if !NewModuleChange(old, old).Empty() {
		t.Error("no change must be empty")
	}

	m := testModule()
	m.Contents = old
	if d := m.NeedsApproval(old); len(d.Hostnames) != 1 || len(d.Scripts) != 1 {
		t.Errorf("fresh module needs approval of everything: %+v", d)
	}
	m.ApproveContents(old)
	if !m.NeedsApproval(old).Empty() {
		t.Error("approved contents still need approval")
	}
	// Excludes never need approval; a changed script does.
	if d := m.NeedsApproval(ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}, Exclude: []string{"z.com"}}, Scripts: old.Scripts}); !d.Empty() {
		t.Errorf("exclude needs approval: %+v", d)
	}
	if d := m.NeedsApproval(next); len(d.Hostnames) != 2 || len(d.Scripts) != 2 {
		t.Errorf("update diff: %+v", d)
	}
	// Approved lists stay sorted and without duplicates.
	m.Approve(NewModuleChange(ModuleContents{}, next))
	m.Approve(NewModuleChange(ModuleContents{}, next))
	if len(m.Approved.Hostnames) != 3 || len(m.Approved.Scripts) != 3 {
		t.Errorf("approved = %+v", m.Approved)
	}
}

func installed(contents ModuleContents) *HTTPModule {
	m := testModule()
	m.Raw = "v1"
	m.Hash = HashModuleRaw("v1")
	m.Contents = contents
	m.ApproveContents(contents)
	return m
}

func TestAcceptUpdate(t *testing.T) {
	base := ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}}, Scripts: []*HTTPScript{moduleScript("s1", "v1();")}}
	m := installed(base)

	// Unchanged text only refreshes the check time.
	if m.AcceptUpdate(ModuleUpdate{Name: "Demo", Raw: "v1", Contents: base}, 100) || m.LastCheckedAt != 100 || m.Updated || m.UpdatedAt != 0 {
		t.Errorf("unchanged: %+v", m)
	}

	// The same text with other arguments (or a changed script download)
	// converts to other content and counts as an update.
	other := ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}}, Scripts: []*HTTPScript{moduleScript("s1", "v1();")}}
	other.Scripts[0].Argument = "x=2"
	// A script's argument is part of what the user confirmed: unconfirmed,
	// the change waits.
	if !m.AcceptUpdate(ModuleUpdate{Name: "Demo", Raw: "v1", Contents: other}, 120) || m.Pending == nil || m.Contents.Scripts[0].Argument != "" {
		t.Errorf("unconfirmed argument change: %+v", m)
	}
	m.DiscardPending()
	m.Approve(NewModuleChange(ModuleContents{}, other))
	if m.AcceptUpdate(ModuleUpdate{Name: "Demo", Raw: "v1", Contents: other, Arguments: []ModuleArgument{{Name: "x", Value: "2"}}}, 150) || !m.Updated || m.Contents.Scripts[0].Argument != "x=2" || len(m.Arguments) != 1 {
		t.Errorf("argument change: %+v", m)
	}
	m.Updated = false
	m.Contents, m.Arguments = base, nil

	// A change without new sensitive items takes effect at once.
	smaller := ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}}, Rules: []*ModuleRule{{Type: ModuleRuleDomain, Value: "x.com", Action: ModuleActionReject}}}
	if m.AcceptUpdate(ModuleUpdate{Name: "Demo2", Version: "2", Raw: "v2", Contents: smaller}, 200) {
		t.Fatal("pending without new items")
	}
	if !m.Updated || m.Name != "Demo2" || m.Version != "2" || m.UpdatedAt != 200 || m.Hash != HashModuleRaw("v2") || len(m.Contents.Scripts) != 0 || len(m.Contents.Rules) != 1 || m.Pending != nil {
		t.Errorf("direct update: %+v", m)
	}

	// New hostnames and scripts wait for confirmation; the old content stays.
	m.Updated = false
	richer := ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com", "b.com"}}, Scripts: []*HTTPScript{moduleScript("s9", "evil();")}}
	if !m.AcceptUpdate(ModuleUpdate{Name: "Demo3", Raw: "v3", Contents: richer}, 300) {
		t.Fatal("update with new items must be pending")
	}
	if m.Pending == nil || m.Hash != HashModuleRaw("v2") || m.Updated || len(m.Contents.Rules) != 1 || m.Name != "Demo2" {
		t.Errorf("pending state: %+v", m)
	}
	if strings.Join(m.Pending.Added.Hostnames, ",") != "b.com" || len(m.Pending.Added.Scripts) != 1 {
		t.Errorf("preview: %+v", m.Pending.Added)
	}
	if !m.AcceptUpdate(ModuleUpdate{Name: "Demo3", Raw: "v3", Contents: richer}, 400) || m.Pending.Hash != HashModuleRaw("v3") {
		t.Error("same pending text must stay pending")
	}
	if !m.ApprovePending(500) {
		t.Fatal("approve pending")
	}
	if m.Pending != nil || !m.Updated || m.Name != "Demo3" || m.Hash != HashModuleRaw("v3") || len(m.Contents.Scripts) != 1 || !m.NeedsApproval(m.Contents).Empty() {
		t.Errorf("after approval: %+v", m)
	}
	if m.ApprovePending(600) {
		t.Error("nothing pending")
	}

	// A newer text replaces the pending one; discard drops it.
	m.AcceptUpdate(ModuleUpdate{Name: "D", Raw: "v4", Contents: ModuleContents{Hostnames: &MITMHostnames{Include: []string{"z.com"}}}}, 700)
	m.AcceptUpdate(ModuleUpdate{Name: "D", Raw: "v5", Contents: ModuleContents{Hostnames: &MITMHostnames{Include: []string{"y.com"}}}}, 800)
	if m.Pending == nil || m.Pending.Hash != HashModuleRaw("v5") {
		t.Errorf("newer pending: %+v", m.Pending)
	}
	m.DiscardPending()
	if m.Pending != nil || m.Hash != HashModuleRaw("v3") {
		t.Errorf("discard: %+v", m)
	}
}

func TestActiveContentsDropsUnapproved(t *testing.T) {
	m := testModule()
	m.Contents = ModuleContents{
		Hostnames: &MITMHostnames{Include: []string{"a.com", "b.com"}, Exclude: []string{"x.com"}},
		Scripts:   []*HTTPScript{moduleScript("s1", "1;"), moduleScript("s2", "2;")},
		Rules:     []*ModuleRule{{Type: ModuleRuleDomain, Value: "r.com", Action: ModuleActionReject}},
	}
	m.Approved = ModuleApproved{Hostnames: []string{"a.com"}, Scripts: []string{ScriptApprovalKey(moduleScript("s2", "2;"))}}
	active, hosts, scripts, _ := m.ActiveContents()
	if strings.Join(active.Hostnames.Include, ",") != "a.com" || strings.Join(active.Hostnames.Exclude, ",") != "x.com" {
		t.Errorf("hosts = %+v", active.Hostnames)
	}
	if len(active.Scripts) != 1 || active.Scripts[0].Tag != "s2" || len(active.Rules) != 1 {
		t.Errorf("active = %+v", active)
	}
	if strings.Join(hosts, ",") != "b.com" || strings.Join(scripts, ",") != "s1" {
		t.Errorf("skipped %v %v", hosts, scripts)
	}
	if len(m.Contents.Hostnames.Include) != 2 || len(m.Contents.Scripts) != 2 {
		t.Error("ActiveContents modified the module")
	}
}

func TestModuleIDHelpers(t *testing.T) {
	id := ModuleItemID("mod_ab12", 7)
	if id != "mod:mod_ab12:7" || !IsModuleRule(id) {
		t.Errorf("id = %s", id)
	}
	if m, n, ok := ModuleOf(id); !ok || m != "mod_ab12" || n != 7 {
		t.Errorf("ModuleOf = %s %d %v", m, n, ok)
	}
	for _, bad := range []string{"", "mod:", "mod:x:1", "mod:mod_ab:0", "mod:mod_ab:x", "rule_1", "mod:mod_AB:1", "mod:mod_ab"} {
		if IsModuleRule(bad) {
			t.Errorf("%q is not a module id", bad)
		}
	}
	if m, ok := ModuleOfRewriteID(id); !ok || m != "mod_ab12" {
		t.Error("rewrite id")
	}
	tag := ModuleScriptTag("mod_ab12", "my-script")
	if tag != "mod_ab12-my-script" {
		t.Errorf("tag = %s", tag)
	}
	if m, ok := ModuleOfScriptTag(tag); !ok || m != "mod_ab12" {
		t.Errorf("tag module = %s %v", m, ok)
	}
	if _, ok := ModuleOfScriptTag("my-script"); ok {
		t.Error("user tag taken for a module tag")
	}
	if ScriptTagIssue(tag) != "" || len(tag) > MaxScriptTag {
		t.Error("module tag is not a valid script tag")
	}
}

func TestModuleHostsAndRouteRules(t *testing.T) {
	a := testModule()
	a.Contents = ModuleContents{
		Hostnames: &MITMHostnames{Include: []string{"A.com", "b.com"}, Exclude: []string{"x.com"}},
		Rules: []*ModuleRule{
			{Type: ModuleRuleDomainSuffix, Value: "ads.com", Action: ModuleActionReject},
			{Type: ModuleRuleIPCIDR, Value: "1.2.3.0/24", Action: ModuleActionDirect},
			{Type: "bogus", Value: "x", Action: ModuleActionReject},
			{Type: ModuleRuleDomain, Value: "d.com", Action: ModuleActionReject},
		},
	}
	a.Approved.Hostnames = []string{"a.com"}
	a.Approved.Direct = []string{"ip-cidr,1.2.3.0/24"}
	b := testModule()
	b.ID = "mod_def456"
	b.Name = "Off"
	b.Enabled = false
	b.Contents = ModuleContents{Rules: []*ModuleRule{{Type: ModuleRuleDomain, Value: "off.com", Action: ModuleActionReject}}}
	doc := &Doc{HTTPTools: &HTTPTools{Modules: []*HTTPModule{a, b}}}

	hosts := ModuleHosts(doc)
	if len(hosts) != 3 || hosts[0].Host != "a.com" || !hosts[0].Approved || hosts[1].Host != "b.com" || hosts[1].Approved || !hosts[2].Exclude || hosts[0].ModuleName != "Demo" {
		t.Errorf("hosts = %+v", hosts)
	}
	rules := ModuleRouteRules(doc)
	if len(rules) != 3 {
		t.Fatalf("rules = %d", len(rules))
	}
	if r := rules[0]; r.ID != "mod:mod_abc123:1" || r.Action != ActionReject || r.Name != "模块 · Demo" || r.PolicyOwner() != PreDeviceID || r.Match["domain_suffix"] == nil {
		t.Errorf("rule 1: %+v", r)
	}
	if r := rules[1]; r.Action != ActionRoute || r.Match["ip_cidr"] == nil || r.Outbound.ID() != "" {
		t.Errorf("rule 2: %+v", r)
	}
	// The numbering counts the refused rule, so ids stay stable.
	if rules[2].ID != "mod:mod_abc123:4" {
		t.Errorf("rule id = %s", rules[2].ID)
	}
	if ModuleRouteRules(nil) != nil || ModuleHosts(&Doc{}) != nil {
		t.Error("nil doc")
	}
}

func TestModuleRewritesAndScripts(t *testing.T) {
	m := testModule()
	m.Contents = ModuleContents{
		Rewrites: &HTTPRewrites{
			URL:      []*URLRewrite{{ID: "u1", Pattern: "a", Action: URLActionReject}},
			Headers:  []*HeaderRewrite{{ID: "h1", Pattern: "a", Scope: "request", Ops: []*HeaderEdit{{Action: "delete", Name: "X"}}}},
			MapLocal: []*MapLocal{{ID: "m1", Pattern: "a", Status: 200}},
		},
		Scripts: []*HTTPScript{moduleScript("s1", "1;")},
	}
	m.ApproveContents(m.Contents)
	active := ActiveModules(&HTTPTools{Modules: []*HTTPModule{m}})
	if len(active) != 1 {
		t.Fatal("active")
	}
	rw := ModuleRewrites(active[0])
	if rw.URL[0].ID != "mod:mod_abc123:1" || rw.Headers[0].ID != "mod:mod_abc123:2" || rw.MapLocal[0].ID != "mod:mod_abc123:3" {
		t.Errorf("ids: %s %s %s", rw.URL[0].ID, rw.Headers[0].ID, rw.MapLocal[0].ID)
	}
	if m.Contents.Rewrites.URL[0].ID != "u1" {
		t.Error("module rewrites modified")
	}
	scripts := ModuleScripts(active[0])
	if scripts[0].Tag != "mod_abc123-s1" || scripts[0].ID != scripts[0].Tag || scripts[0].Issue() != "" {
		t.Errorf("scripts = %+v", scripts[0])
	}
	if ModuleRewrites(ActiveModule{Module: m}) != nil {
		t.Error("empty contents")
	}
}

func TestModuleJSONRoundTrip(t *testing.T) {
	m := testModule()
	m.UpdateInterval = 0
	m.Contents = ModuleContents{Scripts: []*HTTPScript{moduleScript("s1", "1;")}, Rules: []*ModuleRule{{Type: ModuleRuleDomain, Value: "a.com", Action: ModuleActionReject, NoResolve: true}}}
	m.Pending = &ModulePending{Raw: "x", Contents: ModuleContents{}}
	m.Unsupported = []UnsupportedItem{{Section: "General", Line: 3, Reason: "r"}}
	raw, err := json.Marshal(&Doc{Version: 1, Platform: "linux", HTTPTools: &HTTPTools{Modules: []*HTTPModule{m}}})
	if err != nil {
		t.Fatal(err)
	}
	doc := DecodeDoc(raw)
	if doc == nil || doc.HTTPTools == nil || len(doc.HTTPTools.Modules) != 1 {
		t.Fatalf("decode: %s", raw)
	}
	got := doc.HTTPTools.Modules[0]
	if got.UpdateInterval != 0 || got.Contents.Rules[0].NoResolve != true || got.Pending == nil || got.Unsupported[0].Line != 3 || got.Contents.Scripts[0].Source != "1;" {
		t.Errorf("round trip: %+v", got)
	}
	if !doc.HTTPTools.EnabledModules() || doc.HTTPTools.FindModule("mod_abc123") != got || doc.HTTPTools.FindModule("x") != nil {
		t.Error("helpers")
	}
	if c := got.Contents.Counts(); c.Scripts != 1 || c.Rules != 1 {
		t.Errorf("counts %+v", c)
	}
}

func TestModuleRuleScopeLimits(t *testing.T) {
	for _, tc := range []struct {
		rule ModuleRule
		want string
	}{
		{ModuleRule{Type: ModuleRuleIPCIDR, Value: "0.0.0.0/0", Action: ModuleActionDirect}, ModuleRuleTooBroad},
		{ModuleRule{Type: ModuleRuleIPCIDR, Value: "10.0.0.0/7", Action: ModuleActionReject}, ModuleRuleTooBroad},
		{ModuleRule{Type: ModuleRuleIPCIDR, Value: "10.0.0.0/8", Action: ModuleActionReject}, ""},
		{ModuleRule{Type: ModuleRuleIPCIDR, Value: "1.2.3.4/32", Action: ModuleActionDirect}, ""},
		{ModuleRule{Type: ModuleRuleIPCIDR6, Value: "::/0", Action: ModuleActionDirect}, ModuleRuleTooBroad},
		{ModuleRule{Type: ModuleRuleIPCIDR6, Value: "2000::/31", Action: ModuleActionDirect}, ModuleRuleTooBroad},
		{ModuleRule{Type: ModuleRuleIPCIDR6, Value: "2001:db8::/32", Action: ModuleActionDirect}, ""},
		{ModuleRule{Type: ModuleRuleDomainSuffix, Value: "com", Action: ModuleActionReject}, ModuleRuleTooBroad},
		{ModuleRule{Type: ModuleRuleDomainSuffix, Value: "example.com", Action: ModuleActionReject}, ""},
		{ModuleRule{Type: ModuleRuleDomainKeyword, Value: "ad", Action: ModuleActionReject}, ModuleRuleTooBroad},
		{ModuleRule{Type: ModuleRuleDomainKeyword, Value: "ads", Action: ModuleActionReject}, ""},
		{ModuleRule{Type: ModuleRuleDomainKeyword, Value: "", Action: ModuleActionReject}, "关键字无效"},
		{ModuleRule{Type: ModuleRuleDomain, Value: "localhost", Action: ModuleActionReject}, ""},
	} {
		if got := tc.rule.Issue(); got != tc.want {
			t.Errorf("%+v: issue %q, want %q", tc.rule, got, tc.want)
		}
	}
	// A module whose contents carry such a rule is invalid.
	m := testModule()
	m.Contents = ModuleContents{Rules: []*ModuleRule{{Type: ModuleRuleDomainSuffix, Value: "com", Action: ModuleActionDirect}}}
	if !strings.Contains(m.Issue(), ModuleRuleTooBroad) {
		t.Errorf("module issue = %q", m.Issue())
	}
}

func TestScriptApprovalKeyIncludesTriggers(t *testing.T) {
	base := moduleScript("s1", "run();")
	key := ScriptApprovalKey(base)
	same := *base
	same.Tag = "renamed"
	if ScriptApprovalKey(&same) != key {
		t.Error("the tag is not a trigger")
	}
	for name, mut := range map[string]func(*HTTPScript){
		"pattern":  func(s *HTTPScript) { s.Pattern = `^https://.*` },
		"type":     func(s *HTTPScript) { s.Type = ScriptHTTPRequest },
		"cron":     func(s *HTTPScript) { s.Cron = "0 * * * *" },
		"body":     func(s *HTTPScript) { s.RequiresBody = true },
		"binary":   func(s *HTTPScript) { s.BinaryBody = true },
		"argument": func(s *HTTPScript) { s.Argument = "x" },
		"max":      func(s *HTTPScript) { s.MaxSize = 2048 },
		"timeout":  func(s *HTTPScript) { s.Timeout = 9 },
		"source":   func(s *HTTPScript) { s.Source = "other();" },
	} {
		c := *base
		mut(&c)
		if ScriptApprovalKey(&c) == key {
			t.Errorf("%s change keeps the key", name)
		}
	}
	// A confirmed script whose pattern widens needs a new confirmation.
	m := testModule()
	m.Contents = ModuleContents{Scripts: []*HTTPScript{base}}
	m.ApproveContents(m.Contents)
	wide := *base
	wide.Pattern = `^https://`
	diff := m.NeedsApproval(ModuleContents{Scripts: []*HTTPScript{&wide}})
	if len(diff.Scripts) != 1 || !diff.Scripts[0].Changed || diff.Scripts[0].Pattern != `^https://` || diff.Scripts[0].Type != ScriptHTTPResponse {
		t.Fatalf("diff = %+v", diff)
	}
	if !m.NeedsApproval(m.Contents).Empty() {
		t.Error("the confirmed script asks again")
	}
	m.Contents = ModuleContents{Scripts: []*HTTPScript{&wide}}
	active, _, skipped, _ := m.ActiveContents()
	if len(active.Scripts) != 0 || len(skipped) != 1 {
		t.Errorf("widened script active: %+v skipped %v", active.Scripts, skipped)
	}
}

func TestDirectRulesNeedApproval(t *testing.T) {
	direct := &ModuleRule{Type: ModuleRuleDomainSuffix, Value: "cdn.example.com", Action: ModuleActionDirect}
	reject := &ModuleRule{Type: ModuleRuleDomainSuffix, Value: "ads.example.com", Action: ModuleActionReject}
	m := testModule()
	m.Enabled = true
	m.Contents = ModuleContents{Rules: []*ModuleRule{reject, direct, direct}}
	diff := m.NeedsApproval(m.Contents)
	if len(diff.Direct) != 1 || diff.Direct[0] != "domain-suffix,cdn.example.com" || diff.Empty() {
		t.Fatalf("diff = %+v", diff)
	}
	// Unconfirmed DIRECT rules stay out of the routing rules and the active
	// contents; the numbering still counts them.
	doc := &Doc{HTTPTools: &HTTPTools{Modules: []*HTTPModule{m}}}
	rules := ModuleRouteRules(doc)
	if len(rules) != 1 || rules[0].ID != ModuleItemID(m.ID, 1) {
		t.Fatalf("route rules = %+v", rules)
	}
	active, _, _, skipped := m.ActiveContents()
	if len(active.Rules) != 1 || active.Rules[0] != reject || len(skipped) != 1 {
		t.Errorf("active = %+v skipped %v", active.Rules, skipped)
	}
	m.ApproveContents(m.Contents)
	if !m.NeedsApproval(m.Contents).Empty() || len(ModuleRouteRules(doc)) != 3 {
		t.Errorf("approved: %+v", m.NeedsApproval(m.Contents))
	}
	// An update that adds a DIRECT rule waits; one that adds a REJECT rule
	// does not.
	old := m.Contents
	more := ModuleContents{Rules: append(slices.Clone(old.Rules), &ModuleRule{Type: ModuleRuleIPCIDR, Value: "8.8.0.0/16", Action: ModuleActionDirect})}
	m.Raw, m.Hash = "v1", HashModuleRaw("v1")
	if !m.AcceptUpdate(ModuleUpdate{Raw: "v2", Contents: more}, 5) || m.Pending == nil || len(m.Pending.Added.Direct) != 1 || m.Pending.Added.Direct[0] != "ip-cidr,8.8.0.0/16" {
		t.Fatalf("pending = %+v", m.Pending)
	}
	if len(m.Contents.Rules) != 3 {
		t.Error("old contents replaced")
	}
	m.DiscardPending()
	more = ModuleContents{Rules: append(slices.Clone(old.Rules), &ModuleRule{Type: ModuleRuleDomain, Value: "x.example.com", Action: ModuleActionReject})}
	if m.AcceptUpdate(ModuleUpdate{Raw: "v3", Contents: more}, 6) || len(m.Contents.Rules) != 4 {
		t.Error("a reject rule needs no confirmation")
	}
}

func TestAcceptReportsAppliedForArgumentChangeWithSameText(t *testing.T) {
	m := installed(ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}}})
	m.ApproveContents(m.Contents)
	same := ModuleUpdate{Raw: "v1", Contents: m.Contents, Arguments: m.Arguments}
	if r := m.Accept(same, 9); r.Applied || r.Pending {
		t.Errorf("identical update: %+v", r)
	}
	other := ModuleUpdate{Raw: "v1", Arguments: []ModuleArgument{{Name: "A", Value: "b"}},
		Contents: ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}, Exclude: []string{"x.com"}}}}
	if r := m.Accept(other, 10); !r.Applied || r.Pending || m.Hash != HashModuleRaw("v1") || !m.Updated {
		t.Errorf("changed content with the same text: %+v", r)
	}
}

func TestPendingFingerprintFollowsContent(t *testing.T) {
	c := ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com"}}, Scripts: []*HTTPScript{moduleScript("s", "1;")}}
	a := &ModulePending{Hash: "h", Contents: c}
	b := &ModulePending{Hash: "h", Contents: c}
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("equal pending updates differ")
	}
	b.Contents = ModuleContents{Hostnames: &MITMHostnames{Include: []string{"a.com", "b.com"}}, Scripts: c.Scripts}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("another host keeps the fingerprint")
	}
}

func TestModuleRuleOrdinalsKeepFirstOfDuplicateID(t *testing.T) {
	first, dup, other := testModule(), testModule(), testModule()
	other.ID = "mod_other"
	first.Contents.Rules = []*ModuleRule{{Type: ModuleRuleDomain, Value: "a.com", Action: ModuleActionReject}}
	dup.Contents.Rules = []*ModuleRule{{Type: ModuleRuleDomain, Value: "dup1.com", Action: ModuleActionReject}, {Type: ModuleRuleDomain, Value: "dup2.com", Action: ModuleActionReject}}
	other.Contents.Rules = []*ModuleRule{{Type: ModuleRuleDomain, Value: "b.com", Action: ModuleActionReject}}
	doc := &Doc{HTTPTools: &HTTPTools{Modules: []*HTTPModule{first, dup, other}}}
	rules := ModuleRouteRules(doc)
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2 (the duplicate id is skipped)", len(rules))
	}
	ordinals := ModuleRuleOrdinals(doc)
	if len(ordinals) != 2 || ordinals[ModuleItemID(first.ID, 1)] != 1 || ordinals[ModuleItemID(other.ID, 1)] != 2 {
		t.Errorf("ordinals = %v", ordinals)
	}
	if got := ActiveModules(doc.HTTPTools); len(got) != 2 || got[0].Module != first {
		t.Errorf("active modules = %d", len(got))
	}
}

func TestModuleHostsKeepFirstOfDuplicateID(t *testing.T) {
	first, dup, other := testModule(), testModule(), testModule()
	other.ID = "mod_other"
	first.Contents.Hostnames = &MITMHostnames{Include: []string{"a.com"}}
	dup.Contents.Hostnames = &MITMHostnames{Include: []string{"dup.com"}, Exclude: []string{"dupx.com"}}
	other.Contents.Hostnames = &MITMHostnames{Include: []string{"b.com"}}
	doc := &Doc{HTTPTools: &HTTPTools{Modules: []*HTTPModule{first, dup, other}}}
	hosts := ModuleHosts(doc)
	if len(hosts) != 2 || hosts[0].Host != "a.com" || hosts[1].Host != "b.com" {
		t.Fatalf("hosts = %+v", hosts)
	}
	keys := map[string]bool{}
	for _, h := range hosts {
		key := h.ModuleID + ":" + h.Host
		if keys[key] {
			t.Errorf("repeated row key %s", key)
		}
		keys[key] = true
	}
}

func TestModuleScriptRules(t *testing.T) {
	good := &ModuleRule{Type: ModuleRuleScript, Value: "pick", Action: ModuleActionReject}
	if issue := good.Issue(); issue != "" {
		t.Fatalf("issue %q", issue)
	}
	if issue := (&ModuleRule{Type: ModuleRuleScript, Value: "a b", Action: ModuleActionReject}).Issue(); issue == "" {
		t.Error("bad tag accepted")
	}
	script := &HTTPScript{ID: "pick", Tag: "pick", Type: ScriptRule, Source: "$done(true);"}
	m := NewHTTPModule()
	m.ID, m.Name, m.Enabled = "mod_a", "Demo", true
	m.Contents = ModuleContents{Scripts: []*HTTPScript{script}, Rules: []*ModuleRule{good, {Type: ModuleRuleScript, Value: "none", Action: ModuleActionReject}}}
	doc := &Doc{HTTPTools: &HTTPTools{Modules: []*HTTPModule{m}}}
	// The script is not confirmed yet: the rule waits for it.
	if rules := ModuleRouteRules(doc); len(rules) != 0 {
		t.Fatalf("unconfirmed script rule emitted: %v", rules)
	}
	m.ApproveContents(m.Contents)
	rules := ModuleRouteRules(doc)
	if len(rules) != 1 || rules[0].ID != "mod:mod_a:1" || rules[0].Action != ActionReject {
		t.Fatalf("rules = %+v", rules)
	}
	if got := rules[0].Match["script"]; !reflect.DeepEqual(got, []any{"mod_a-pick"}) {
		t.Errorf("match = %v", got)
	}
	// The timeout of a rule script is part of what the user confirms.
	a, b := *script, *script
	b.Timeout = 500
	if ScriptApprovalKey(&a) == ScriptApprovalKey(&b) {
		t.Error("timeout not in the approval key")
	}
}
