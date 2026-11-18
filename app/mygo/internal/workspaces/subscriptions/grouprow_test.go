package subscriptions

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// groupsDoc is sampleDoc with the Selector holding both manual nodes,
// the second its default, and a URLTest holding the first and adding the
// VLESS node by its filter.
func groupsDoc() *model.Doc {
	doc := sampleDoc()
	for _, o := range doc.Outbounds {
		if o.ID == "out_sel" {
			o.Members = []string{"out_ss", "out_vless"}
			o.Fields["default_ref"] = "out_vless"
		}
	}
	auto := outboundOf("out_auto", "urltest", "香港自动", model.Fields{})
	auto.Members = []string{"out_ss"}
	auto.Filter = &model.GroupFilter{Pattern: "VLESS"}
	doc.Outbounds = append(doc.Outbounds, auto)
	return doc
}

// groupOf is the page's view of group id, nil when it is not listed.
func groupOf(f *fixture, id string) *groupView {
	for i := range f.w.rows.groups {
		if f.w.rows.groups[i].id == id {
			return &f.w.rows.groups[i]
		}
	}
	return nil
}

// routeLatency answers speed tests with ms by outbound, 48 for the
// others; it returns the outbounds tested so far.
func routeLatency(f *fixture, ms map[string]float64) func() []string {
	var mu sync.Mutex
	var tested []string
	f.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		var results []api.LatencyResult
		mu.Lock()
		for _, target := range body.Targets {
			v, ok := ms[target.ID]
			if !ok {
				v = 48
			}
			tested = append(tested, target.ID)
			results = append(results, api.LatencyResult{ID: target.ID, MedianConnectMs: &v, SuccessSamples: 3})
		}
		mu.Unlock()
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tested...)
	}
}

// runCore makes the core run with the selector groups of groups.
func runCore(f *fixture, groups ...api.Selector) {
	f.env.Server.HasRuntime = true
	f.env.Server.Runtime = api.Runtime{State: "running"}
	f.env.Server.Selectors = groups
	f.tt.Frame()
}

// TestGroupCards: the groups show as cards under "Policy groups" with their kind,
// filter and member count, the filter's member marked as added, a
// Selector's default in use and a URLTest's member untested, then the
// manual outbounds.
func TestGroupCards(t *testing.T) {
	f := newFixture(t, groupsDoc())
	for _, text := range []string{"策略组", "手动 · 2", "最快 · 自定义匹配 · 2", "手动出站", "未测速", "切换", "测速", "添加策略组"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q in %q", text, f.tt.Texts())
		}
	}
	auto := groupOf(f, "out_auto")
	if len(auto.all) != 2 || auto.all[0].appended || !auto.all[1].appended {
		t.Errorf("URLTest members %+v", auto.all)
	}
	if got := f.w.outbounds.inUse(groupOf(f, "out_sel")); got != "out_vless" {
		t.Errorf("Selector in use %q", got)
	}
	if got := f.w.outbounds.inUse(auto); got != "" {
		t.Errorf("untested URLTest in use %q", got)
	}
	if _, ok := f.tt.Find("编辑 节点选择"); !ok {
		t.Error("card does not open the editor")
	}
	f.save("groups-light.png")
}

// TestGroupInUseLive: while the core runs a group, its card shows the
// core's choice over the configuration's default.
func TestGroupInUseLive(t *testing.T) {
	f := newFixture(t, groupsDoc())
	runCore(f, api.Selector{Name: "节点选择", Type: "selector", Now: "香港 SS", All: []string{"香港 SS", "日本 VLESS"}})
	if got := f.w.outbounds.inUse(groupOf(f, "out_sel")); got != "out_ss" {
		t.Errorf("live choice %q", got)
	}
	// A group the core does not run keeps the configuration's.
	runCore(f)
	if got := f.w.outbounds.inUse(groupOf(f, "out_sel")); got != "out_vless" {
		t.Errorf("without the core's group %q", got)
	}
}

// openSheet opens group title's member sheet from its card's "Switch" and
// lets it slide in.
func openSheet(t *testing.T, f *fixture, title string) {
	t.Helper()
	f.click("切换 " + title)
	f.tt.Advance(time.Second)
	f.tt.Frame()
	if !f.w.outbounds.sheet.state.Open {
		t.Fatalf("sheet closed: %q", f.tt.Texts())
	}
}

// TestGroupSheetSetsDefault: without the core, choosing a Selector's
// member in its sheet makes it the default.
func TestGroupSheetSetsDefault(t *testing.T) {
	f := newFixture(t, groupsDoc())
	openSheet(t, f, "节点选择")
	if !f.tt.HasText("手动 · 2 个成员") || f.tt.HasText("点按成员") {
		t.Errorf("summary: %q", f.tt.Texts())
	}
	f.click("选择 香港 SS")
	f.saved(func(d *model.Doc) bool {
		for _, o := range d.Outbounds {
			if o.ID == "out_sel" {
				return o.Fields["default_ref"] == "out_ss"
			}
		}
		return false
	})
	f.tt.Frame()
	if got := f.w.outbounds.inUse(groupOf(f, "out_sel")); got != "out_ss" {
		t.Errorf("in use after choosing %q", got)
	}
	f.save("group-sheet.png")
	// "Edit group" opens the editor in place of the sheet.
	f.click("编辑分组")
	if !f.w.outbounds.editor.open || f.w.outbounds.sheet.state.Open {
		t.Errorf("editor %v, sheet %v", f.w.outbounds.editor.open, f.w.outbounds.sheet.state.Open)
	}
}

// TestGroupSheetSwitchesLive: while the core runs the group, choosing a
// member switches it there, leaving the configuration's default.
func TestGroupSheetSwitchesLive(t *testing.T) {
	f := newFixture(t, groupsDoc())
	var mu sync.Mutex
	var switched map[string]string
	f.srv.Route("PATCH", "/runtime/selector", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = testkit.Body(r, &body)
		mu.Lock()
		switched = body
		mu.Unlock()
		testkit.JSON(map[string]any{})(w, r)
	})
	runCore(f, api.Selector{Name: "节点选择", Type: "selector", Now: "日本 VLESS", All: []string{"香港 SS", "日本 VLESS"}})
	openSheet(t, f, "节点选择")
	if !f.tt.HasText("手动 · 2 个成员") {
		t.Errorf("summary missing: %q", f.tt.Texts())
	}
	f.click("选择 香港 SS")
	testkit.Until(t, f.tt, func() bool { return f.env.Server.Selectors[0].Now == "香港 SS" })
	mu.Lock()
	if switched["selector"] != "节点选择" || switched["outbound"] != "香港 SS" {
		t.Errorf("switched %v", switched)
	}
	mu.Unlock()
	if got := f.w.outbounds.inUse(groupOf(f, "out_sel")); got != "out_ss" {
		t.Errorf("in use after switching %q", got)
	}
	for _, o := range f.store.Doc().Outbounds {
		if o.ID == "out_sel" && o.Fields["default_ref"] != "out_vless" {
			t.Errorf("default changed to %v", o.Fields["default_ref"])
		}
	}
}

// TestGroupLatencyTestsMembers: a URLTest's latency test tests its members only,
// keeping the other results; the fastest is then in use and shows its
// latency, and a second test draws its trend.
func TestGroupLatencyTestsMembers(t *testing.T) {
	f := newFixture(t, groupsDoc())
	tested := routeLatency(f, map[string]float64{"out_ss": 48, "out_vless": 90})
	f.store.Latency["out_node"] = api.LatencyResult{ID: "out_node", Error: "timeout"}
	f.click("测速 香港自动")
	testkit.Until(t, f.tt, func() bool {
		return f.store.Latency["out_ss"].ID != "" && f.store.Latency["out_vless"].ID != "" && !f.store.TestingAny([]string{"out_ss", "out_vless"})
	})
	f.tt.Frame()
	for _, id := range tested() {
		if id != "out_ss" && id != "out_vless" {
			t.Errorf("tested %q, outside the group", id)
		}
	}
	if _, ok := f.store.Latency["out_ss"]; !ok {
		t.Error("member out_ss not tested")
	}
	if _, ok := f.store.Latency["out_vless"]; !ok {
		t.Error("added member out_vless not tested")
	}
	if f.store.Latency["out_node"].Error != "timeout" {
		t.Error("other result replaced")
	}
	if got := f.w.outbounds.inUse(groupOf(f, "out_auto")); got != "out_ss" {
		t.Errorf("fastest %q", got)
	}
	if !f.tt.HasText("48 ms") {
		t.Errorf("card latency missing: %q", f.tt.Texts())
	}
	f.click("测速 香港自动")
	testkit.Until(t, f.tt, func() bool { return !f.store.Testing("out_ss") && len(f.store.LatencyHistory("out_ss")) == 2 })
	f.tt.Frame()
	f.save("groups-trend.png")
}

// TestSearchFiltersGroups: a search lists the groups matching by name or
// by members, those counting the members as "N matches", and leaves out the
// others.
func TestSearchFiltersGroups(t *testing.T) {
	f := newFixture(t, groupsDoc())
	f.w.query = "vless"
	f.tt.Frame()
	sel, auto := groupOf(f, "out_sel"), groupOf(f, "out_auto")
	if sel == nil || auto == nil || sel.hits != 1 || auto.hits != 1 {
		t.Fatalf("searched groups %+v %+v", sel, auto)
	}
	if !f.tt.HasText("命中 1") {
		t.Errorf("no hit count: %q", f.tt.Texts())
	}
	f.w.query = "香港自动"
	f.tt.Frame()
	if groupOf(f, "out_sel") != nil || groupOf(f, "out_auto") == nil || groupOf(f, "out_auto").hits != 0 {
		t.Error("search by name")
	}
	f.w.query = "新加坡"
	f.tt.Frame()
	if groupOf(f, "out_sel") != nil || groupOf(f, "out_auto") != nil {
		t.Error("groups without matches listed")
	}
	if !f.tt.HasText("命中 1") {
		t.Errorf("no subscription hit count: %q", f.tt.Texts())
	}
	f.w.query = ""
	f.tt.Frame()
	if groupOf(f, "out_sel") == nil || groupOf(f, "out_auto") == nil {
		t.Error("groups not listed again")
	}
}

// TestGroupInUseLiveByEmittedTag: the core names groups and members by
// their emitted tags, trimmed and renamed apart, which the card matches.
func TestGroupInUseLiveByEmittedTag(t *testing.T) {
	doc := groupsDoc()
	for _, o := range doc.Outbounds {
		if o.ID == "out_sel" {
			o.Tag = " 节点选择 "
		}
		if o.ID == "out_ss" {
			o.Tag = "香港 SS "
		}
	}
	f := newFixture(t, doc)
	g := groupOf(f, "out_sel")
	if g == nil || g.live != "节点选择" {
		t.Fatalf("group %+v", g)
	}
	runCore(f, api.Selector{Name: "节点选择", Type: "selector", Now: "香港 SS", All: []string{"香港 SS", "日本 VLESS"}})
	if got := f.w.outbounds.inUse(groupOf(f, "out_sel")); got != "out_ss" {
		t.Errorf("live choice %q", got)
	}
}

// TestInboundFacts: a TUN's note names manual routing whenever
// auto_route is off; listening inbounds default to 127.0.0.1.
func TestInboundFacts(t *testing.T) {
	tun := func(auto, strict bool) *model.Inbound {
		return &model.Inbound{Type: "tun", Fields: model.Fields{"address": []any{"172.19.0.1/30"}, "auto_route": auto, "strict_route": strict}}
	}
	cases := []struct {
		in         *model.Inbound
		main, note string
	}{
		{tun(true, true), "172.19.0.1", "/30 · 自动路由 · 严格路由"},
		{tun(false, true), "172.19.0.1", "/30 · 手动路由 · 严格路由"},
		{tun(false, false), "172.19.0.1", "/30 · 手动路由"},
		{&model.Inbound{Type: "mixed", Fields: model.Fields{"listen_port": 7890.0}}, ":7890", "127.0.0.1"},
		{&model.Inbound{Type: "mixed", Fields: model.Fields{"listen": "::"}}, "::", "未设置端口"},
	}
	for _, tc := range cases {
		if main, note, _ := inboundFacts(tc.in); main != tc.main || note != tc.note {
			t.Errorf("%s %v: %q, %q; want %q, %q", tc.in.Type, tc.in.Fields, main, note, tc.main, tc.note)
		}
	}
	if got := inboundSummary(&model.Inbound{Type: "mixed", Fields: model.Fields{"listen": "::", "listen_port": 1.0}}); got != "[::]:1" {
		t.Errorf("summary %q", got)
	}
}
