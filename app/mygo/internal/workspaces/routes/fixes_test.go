package routes

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// TestFirstRuleBeforeFirstHeadingStays drops the first group's first rule
// before the first heading: it stays first, not last of its group.
func TestFirstRuleBeforeFirstHeadingStays(t *testing.T) {
	doc := tableDoc()
	before := ruleOrder(doc)
	table := buildTable(doc, sectionTraffic, model.LocalDeviceID, "")
	plan, ok := table.planReorder(rowIndex(table, rowRule, "first"), rowIndex(table, rowGroup, "group-a"))
	if !ok {
		t.Fatal("no plan")
	}
	applyReorder(doc, sectionTraffic, model.LocalDeviceID, plan)
	if got := ruleOrder(doc); !reflect.DeepEqual(got, before) {
		t.Errorf("rules %v, want %v", got, before)
	}
}

// TestNoOpReordersDoNotSave drops rows where they already are, and moves
// the first rule up: nothing saves.
func TestNoOpReordersDoNotSave(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	version := f.store.Version()
	// Rows: System Services, Ads & Tracking, App Routing, Apple Services,
	// Dev & Collaboration, Default Outbound.
	f.w.list.Reorder([]int{0}, 2) // System Services before App Routing
	f.w.list.Reorder([]int{2}, 6) // App Routing, the last group, to the end
	f.w.list.Reorder([]int{4}, 6) // Dev & Collaboration, the last rule, past the groups
	tt.Frame()
	// Ads & Tracking is the first rule: Up keeps it.
	plan, ok := buildTable(f.w.doc(), sectionTraffic, model.LocalDeviceID, "").planReorder(1, 0)
	if ok {
		f.w.reorder(plan)
	}
	if v := f.store.Version(); v != version {
		t.Fatalf("no-op reorders saved: version %d → %d", version, v)
	}
}

// TestEscapeRestoresOrderOnly picks a rule up, moves it, changes another
// rule meanwhile and puts it back with Escape: the order returns and the
// other change stays.
func TestEscapeRestoresOrderOnly(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "开发与协作")
	f.click("编辑 开发与协作")
	testkit.UntilText(t, tt, "编辑路由规则")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return f.w.editor == nil })
	tt.Key(0, ui.KeySpace)
	if f.w.moving == nil {
		t.Fatal("space does not pick the rule up")
	}
	tt.Key(0, ui.KeyUp)
	f.waitSaved(func(doc *model.Doc) bool {
		rules := model.RouteRulesOf(doc, model.LocalDeviceID)
		return rules[1].ID == "r-dev"
	})
	f.w.save(func(doc *model.Doc) error { findRule(doc, "r-ads").Name = "广告"; return nil })
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	f.waitSaved(func(doc *model.Doc) bool {
		rules := model.RouteRulesOf(doc, model.LocalDeviceID)
		return rules[1].ID == "r-apple" && rules[2].ID == "r-dev"
	})
	if r := findRule(f.saved(), "r-ads"); r.Name != "广告" {
		t.Fatalf("escape lost a change made while moving: %q", r.Name)
	}
	// Escape without a move saves nothing.
	tt.Key(0, ui.KeySpace)
	version := f.store.Version()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if f.store.Version() != version {
		t.Fatal("escape without a move saved")
	}
}

// TestDragOfDefaultRowMovesNoRule clicks a rule, then drags the default
// row onto a device: no rule moves, the press being stale.
func TestDragOfDefaultRowMovesNoRule(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return f.w.editor == nil })
	if f.w.pressed != "r-apple" {
		t.Fatalf("pressed %q", f.w.pressed)
	}
	from, _ := tt.Find("此设备的其余连接")
	to, _ := tt.Find("iPhone")
	tt.Press(from.X+from.W/2, from.Y+from.H/2)
	for i := 1; i <= 8; i++ {
		x := from.X + (to.X-from.X)*float32(i)/8
		y := from.Y + (to.Y-from.Y)*float32(i)/8
		tt.Move(x, y+from.H/2)
	}
	tt.Move(to.X+to.W/2, to.Y+to.H/2)
	tt.Release(to.X+to.W/2, to.Y+to.H/2)
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	testkit.Until(t, tt, func() bool { return !f.store.Saving() })
	if r := findRule(f.w.doc(), "r-apple"); r.DeviceID != "" {
		t.Fatalf("the default row's drag moved Apple 服务 to %q", r.DeviceID)
	}
}

// TestRuleSetRenameUpdatesTUNAddressSets renames a rule set a TUN inbound
// routes by: its route_address_set follows.
func TestRuleSetRenameUpdatesTUNAddressSets(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	ok := f.w.save(func(doc *model.Doc) error {
		doc.Route.RuleSets = append(doc.Route.RuleSets, &model.RuleSet{ID: "rs-a", Tag: "geo-a", Enabled: true, Mode: "remote",
			URL: "https://example.com/a.srs", UpdateInterval: "24h0m0s"})
		var tun *model.Inbound
		for _, in := range doc.Inbounds {
			if in.Type == "tun" {
				tun = in
			}
		}
		if tun == nil {
			tun = model.MakeInbound("tun", "tun-in", model.PlatformLinux, false)
			tun.ID = "tun-in"
			doc.Inbounds = append(doc.Inbounds, tun)
		}
		tun.Fields["route_address_set"] = []any{"geo-a"}
		return nil
	})
	if !ok {
		t.Fatalf("setup: %s", f.w.err)
	}
	f.w.ruleSets = f.w.newRuleSetPage()
	var item *model.RuleSet
	for _, r := range f.w.doc().Route.RuleSets {
		if r.ID == "rs-a" {
			item = r
		}
	}
	f.w.openRuleSet(item)
	e := f.w.ruleSets.detail
	e.value.Tag = "geo-b"
	f.w.saveRuleSet(f.w.doc(), e, "", "")
	if e.validation != "" {
		t.Fatalf("save: %s", e.validation)
	}
	f.waitSaved(func(doc *model.Doc) bool {
		for _, in := range doc.Inbounds {
			if in.Type == "tun" {
				return reflect.DeepEqual(model.RuleSetValues(in.Fields["route_address_set"]), []string{"geo-b"})
			}
		}
		return false
	})
}
