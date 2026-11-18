package routes

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// moduleFixtureDoc is fixtureDoc with a user Pre-rules rule and two modules: one
// with reject and direct rules, a disabled one, and one more enabled.
func moduleFixtureDoc() *model.Doc {
	doc := fixtureDoc()
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r-pre", GroupID: "rg-pre", DeviceID: model.PreDeviceID, Name: "公司内网直连",
		Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{"domain_suffix": []any{"corp.test"}}})
	mod := func(id, name string, enabled bool, rules ...*model.ModuleRule) *model.HTTPModule {
		m := model.NewHTTPModule()
		m.ID, m.Name, m.Enabled = id, name, enabled
		m.Contents = model.ModuleContents{Rules: rules}
		m.ApproveContents(m.Contents)
		return m
	}
	doc.HTTPTools = &model.HTTPTools{Modules: []*model.HTTPModule{
		mod("mod_ads", "去广告", true,
			&model.ModuleRule{Type: model.ModuleRuleDomainSuffix, Value: "ads.example.com", Action: model.ModuleActionReject},
			&model.ModuleRule{Type: model.ModuleRuleIPCIDR, Value: "10.1.0.0/16", Action: model.ModuleActionDirect}),
		mod("mod_off", "已停用", false, &model.ModuleRule{Type: model.ModuleRuleDomain, Value: "off.example.com", Action: model.ModuleActionReject}),
		mod("mod_trk", "追踪拦截", true, &model.ModuleRule{Type: model.ModuleRuleDomainKeyword, Value: "tracker", Action: model.ModuleActionReject}),
	}}
	return doc
}

func TestModuleRowsInPreTable(t *testing.T) {
	doc := moduleFixtureDoc()
	tbl := buildTable(doc, sectionTraffic, model.PreDeviceID, "")
	var kinds []string
	ordinals := map[string]int{}
	for _, r := range tbl.rows {
		switch r.kind {
		case rowModuleGroup:
			kinds = append(kinds, fmt.Sprint("G:", r.moduleName, ":", r.count))
		case rowModuleRule:
			kinds = append(kinds, "R:"+r.rule.ID)
			ordinals[r.rule.ID] = r.ordinal
		}
	}
	want := []string{"G:去广告:2", "R:mod:mod_ads:1", "R:mod:mod_ads:2", "G:追踪拦截:1", "R:mod:mod_trk:1"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("module rows %v, want %v", kinds, want)
	}
	// The user's rule is 1; the modules' rules go on from it, the same
	// numbers the emitter's rule map carries.
	if ordinals["mod:mod_ads:1"] != 2 || ordinals["mod:mod_ads:2"] != 3 || ordinals["mod:mod_trk:1"] != 4 {
		t.Errorf("ordinals %v", ordinals)
	}
	for id, n := range model.ModuleRuleOrdinals(doc) {
		if ordinals[id] != n {
			t.Errorf("%s: row %d, model %d", id, ordinals[id], n)
		}
	}
	if tbl.all != 4 || tbl.shown != 4 {
		t.Errorf("counts %d / %d", tbl.shown, tbl.all)
	}
	// The user's rows come first, modules last; the table has no pinned row.
	if tbl.rows[0].kind != rowGroup || tbl.rows[len(tbl.rows)-1].kind != rowModuleRule {
		t.Errorf("order: first %d, last %d", tbl.rows[0].kind, tbl.rows[len(tbl.rows)-1].kind)
	}
	// Other devices and the DNS tab show none.
	for _, other := range []struct {
		sec   section
		owner string
	}{{sectionTraffic, model.LocalDeviceID}, {sectionTraffic, model.OtherDeviceID}, {sectionDNS, model.PreDeviceID}} {
		for _, r := range buildTable(doc, other.sec, other.owner, "").rows {
			if r.kind == rowModuleGroup || r.kind == rowModuleRule {
				t.Errorf("module row in %v", other)
			}
		}
	}
	// A search matches the module's name, rule name and condition.
	tbl = buildTable(doc, sectionTraffic, model.PreDeviceID, "tracker")
	var shown []string
	for _, r := range tbl.rows {
		if r.kind == rowModuleRule || r.kind == rowModuleGroup || r.kind == rowEmpty {
			shown = append(shown, r.key())
		}
	}
	if !slices.Equal(shown, []string{"module:mod_trk", "rule:mod:mod_trk:1"}) || tbl.shown != 1 {
		t.Errorf("search shows %v (%d)", shown, tbl.shown)
	}
	if tbl = buildTable(doc, sectionTraffic, model.PreDeviceID, "没有"); tbl.rows[0].kind != rowEmpty {
		t.Errorf("empty search %v", tbl.rows)
	}
}

func TestModuleRulesCountInPolicyAndTopology(t *testing.T) {
	doc := moduleFixtureDoc()
	// A module's reject rule takes the sample ahead of the device's rules,
	// and after the user's own Pre-rules; the result names the module.
	r := testRoutePolicy(doc, policyTestInput{deviceID: model.LocalDeviceID, domain: "x.ads.example.com"})
	if r.decision != decisionMatched || r.rule.ID != "mod:mod_ads:1" || r.rule.Name != "模块 · 去广告" || r.rule.Action != model.ActionReject {
		t.Fatalf("test result %+v", r)
	}
	// The direct rule leaves by the direct outbound.
	r = testRoutePolicy(doc, policyTestInput{deviceID: model.LocalDeviceID, domain: "x.test", ip: "10.1.2.3"})
	if r.decision != decisionMatched || r.rule.ID != "mod:mod_ads:2" || r.outboundID != "direct" {
		t.Fatalf("direct %+v", r)
	}
	// A disabled module's rule does not match.
	if r = testRoutePolicy(doc, policyTestInput{deviceID: model.LocalDeviceID, domain: "off.example.com", ip: "8.8.8.8"}); r.decision != decisionDefault {
		t.Fatalf("disabled module matched: %+v", r)
	}
	// The user's Pre-rules are tried before the modules'.
	rules, _ := orderedRoutePolicy(doc, model.LocalDeviceID)
	var ids []string
	for _, p := range rules {
		ids = append(ids, p.id)
	}
	if i, j := slices.Index(ids, "r-pre"), slices.Index(ids, "mod:mod_ads:1"); i < 0 || j != i+1 || slices.Index(ids, "r-apple") < slices.Index(ids, "mod:mod_trk:1") {
		t.Errorf("order %v", ids)
	}

	// The topology draws them in bands after the user's own, numbered on.
	g := buildTopology(doc, model.PreDeviceID, func(string) string { return "" })
	var numbers []string
	for _, rule := range g.rules() {
		numbers = append(numbers, rule.number+":"+rule.id)
	}
	want := []string{"1:r-pre", "2:mod:mod_ads:1", "3:mod:mod_ads:2", "4:mod:mod_trk:1", ":final"}
	if !slices.Equal(numbers, want) {
		t.Errorf("前置规则 topology %v, want %v", numbers, want)
	}
	if band := g.bands[len(g.bands)-2]; band.title != "模块 · 去广告" {
		t.Errorf("band %q", band.title)
	}
	g = buildTopology(doc, model.LocalDeviceID, func(string) string { return "" })
	if g.bands[len(g.bands)-1].title == "" {
		t.Fatal("no bands")
	}
	var titles []string
	for _, b := range g.bands {
		titles = append(titles, b.title)
	}
	if !slices.Contains(titles, "前置规则 · 模块 · 去广告") || !slices.Contains(titles, "前置规则 · 模块 · 追踪拦截") {
		t.Errorf("device topology bands %v", titles)
	}
	for _, rule := range g.rules() {
		if rule.id == "mod:mod_ads:1" && (rule.number != "P2" || !rule.pre || rule.exit == "") {
			t.Errorf("module rule node %+v", rule)
		}
	}
	// The sample's path passes the modules' rules too.
	p := explore(doc, policyTestInput{deviceID: model.LocalDeviceID, domain: "x.ads.example.com", facts: map[string]string{}})
	if p.state["mod:mod_ads:1"] != pathDecided || p.state["r-apple"] == pathDecided || p.state["final"] != pathSkipped {
		t.Errorf("path %v", p.state)
	}
	if via := testVia(g, p, "reject"); !strings.Contains(via, "P2 · 模块 · 去广告") {
		t.Errorf("via %q", via)
	}
}

func TestModuleRulesAreReadOnlyRows(t *testing.T) {
	f := newFixtureDoc(t, moduleFixtureDoc(), nil)
	trk := "模块 · 追踪拦截 " + model.RuleSummary(map[string]any{"domain_keyword": []any{"tracker"}}, nil, "")
	var pages []string
	f.env.SetNavigator(func(page string, arg any) { pages = append(pages, fmt.Sprint(page, ":", arg)) })
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.w.selectDevice(model.PreDeviceID, false)
	testkit.UntilText(t, f.tt, "公司内网直连")
	for _, text := range []string{"模块 · 去广告", "模块 · 追踪拦截", "ads.example.com", "10.1.0.0/16", "拒绝", "直连", "4 条规则 · 1 个分组"} {
		if !f.tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, f.tt.Texts())
		}
	}
	if f.tt.HasText("已停用") || f.tt.HasText("off.example.com") {
		t.Fatalf("disabled module shows: %q", f.tt.Texts())
	}
	// The row's menu (a popover) offers Copy and "Go to module" only, and Copy
	// takes the condition.
	count := func(text string) int {
		n := 0
		for _, s := range f.tt.Texts() {
			if s == text {
				n++
			}
		}
		return n
	}
	before := count("前往模块")
	if f.tt.HasText("复制") {
		t.Fatal("menu open before the click")
	}
	if err := f.tt.RightClick(trk); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if !f.tt.HasText("复制") || count("前往模块") <= before || count("编辑") > 0 || count("删除") > 0 || count("复制到设备…") > 0 {
		t.Fatalf("menu: %q", f.tt.Texts())
	}
	f.click("复制")
	if want := model.RuleSummary(map[string]any{"domain_keyword": []any{"tracker"}}, nil, ""); f.tt.Clipboard() != want {
		t.Fatalf("clipboard %q, want %q", f.tt.Clipboard(), want)
	}
	f.tt.Frame()
	// A click opens no editor; the group heading goes to the module.
	f.tt.Frame()
	if err := f.tt.Click(trk); err != nil {
		t.Fatalf("click module rule: %v", err)
	}
	f.tt.Frame()
	if f.w.editor != nil || f.w.fallback != nil {
		t.Fatal("a module rule opened the editor")
	}
	f.click("前往模块")
	if len(pages) != 1 || !strings.HasPrefix(pages[0], "rewrites:module:mod_") {
		t.Fatalf("navigation %v", pages)
	}
	f.click("编辑 公司内网直连")
	testkit.UntilText(t, f.tt, "编辑路由规则")
	f.w.closeEditor()
	f.tt.Frame()
	f.snapshot("routes-modules-light.png", false)
	f.snapshot("routes-modules-dark.png", true)
}

func TestOpenRevealsModuleRule(t *testing.T) {
	f := newFixtureDoc(t, moduleFixtureDoc(), nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.w.Open(env.RouteRule{ID: "mod:mod_trk:1", Owner: model.PreDeviceID, Section: "route"})
	f.tt.Frame()
	if f.w.owner != model.PreDeviceID || f.w.highlight != "mod:mod_trk:1" || f.tt.HasText("该规则已删除") {
		t.Fatalf("owner %q highlight %q", f.w.owner, f.w.highlight)
	}
}

// moduleReorderDoc is moduleFixtureDoc with a second user Pre-rules rule, so the
// last user rule sits right above the modules' rows.
func moduleReorderDoc() *model.Doc {
	doc := moduleFixtureDoc()
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r-pre2", GroupID: "rg-pre", DeviceID: model.PreDeviceID, Name: "内网二",
		Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{"domain_suffix": []any{"lan.test"}}})
	return doc
}

func preRuleOrder(doc *model.Doc) []string {
	var out []string
	for _, r := range model.RouteRulesOf(doc, model.PreDeviceID) {
		out = append(out, r.GroupID+"/"+r.ID)
	}
	return out
}

// TestRuleDroppedOnModuleRowsStaysInItsGroup drops a user rule on a module's
// heading and on a module's rule: it goes last of its own group; the
// modules' rows stay as they were.
func TestRuleDroppedOnModuleRowsStaysInItsGroup(t *testing.T) {
	for _, target := range []struct {
		name string
		kind rowKind
		pick func(tableRow) bool
	}{
		{"heading", rowModuleGroup, func(r tableRow) bool { return r.kind == rowModuleGroup }},
		{"rule", rowModuleRule, func(r tableRow) bool { return r.kind == rowModuleRule }},
	} {
		doc := moduleReorderDoc()
		before := moduleRowKeys(buildTable(doc, sectionTraffic, model.PreDeviceID, ""))
		table := buildTable(doc, sectionTraffic, model.PreDeviceID, "")
		to := slices.IndexFunc(table.rows, target.pick)
		from := slices.IndexFunc(table.rows, func(r tableRow) bool { return r.kind == rowRule && r.rule.ID == "r-pre" })
		if to < 0 || from < 0 {
			t.Fatalf("%s: rows %d / %d", target.name, from, to)
		}
		plan, ok := table.planReorder(from, to)
		if !ok {
			t.Fatalf("%s: no plan", target.name)
		}
		applyReorder(doc, sectionTraffic, model.PreDeviceID, plan)
		if got, want := preRuleOrder(doc), []string{"rg-pre/r-pre2", "rg-pre/r-pre"}; !slices.Equal(got, want) {
			t.Errorf("%s: rules %v, want %v", target.name, got, want)
		}
		if after := moduleRowKeys(buildTable(doc, sectionTraffic, model.PreDeviceID, "")); !slices.Equal(after, before) {
			t.Errorf("%s: module rows %v, was %v", target.name, after, before)
		}
		// A module row cannot be picked up.
		if _, ok := table.planReorder(to, from); ok {
			t.Errorf("%s: a module row moved", target.name)
		}
	}
}

// moduleRowKeys lists the keys of the module rows of t.
func moduleRowKeys(t policyTable) []string {
	var out []string
	for _, r := range t.rows {
		if r.kind == rowModuleGroup || r.kind == rowModuleRule {
			out = append(out, r.key())
		}
	}
	return out
}

// TestKeyboardDownStopsAtModuleRows moves the last user rule down with the
// keyboard: it stays above the modules' rows.
func TestKeyboardDownStopsAtModuleRows(t *testing.T) {
	f := newFixtureDoc(t, moduleReorderDoc(), nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.w.selectDevice(model.PreDeviceID, false)
	testkit.UntilText(t, f.tt, "公司内网直连")
	before := moduleRowKeys(buildTable(f.w.doc(), sectionTraffic, model.PreDeviceID, ""))
	f.click("编辑 公司内网直连")
	testkit.UntilText(t, f.tt, "编辑路由规则")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	f.tt.Key(0, ui.KeySpace)
	if f.w.moving == nil {
		t.Fatal("space does not pick the rule up")
	}
	// Down from the first user rule: below the second, above the modules.
	f.tt.Key(0, ui.KeyDown)
	f.waitSaved(func(doc *model.Doc) bool {
		return slices.Equal(preRuleOrder(doc), []string{"rg-pre/r-pre2", "rg-pre/r-pre"})
	})
	// Down again from the last user rule: nothing moves past the modules.
	version := f.store.Version()
	f.tt.Key(0, ui.KeyDown)
	f.tt.Frame()
	if v := f.store.Version(); v != version {
		t.Fatalf("a move past the modules saved: version %d → %d", version, v)
	}
	doc := f.saved()
	if got := preRuleOrder(doc); !slices.Equal(got, []string{"rg-pre/r-pre2", "rg-pre/r-pre"}) {
		t.Errorf("rules %v", got)
	}
	if after := moduleRowKeys(buildTable(doc, sectionTraffic, model.PreDeviceID, "")); !slices.Equal(after, before) {
		t.Errorf("module rows %v, was %v", after, before)
	}
}
