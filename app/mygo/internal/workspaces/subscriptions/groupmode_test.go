package subscriptions

import (
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// modesDoc is sampleDoc with a group of each automatic mode holding the
// two manual nodes.
func modesDoc() *model.Doc {
	doc := sampleDoc()
	for _, g := range []struct{ id, typ, tag string }{
		{"out_fb", model.ModeFallback, "故障转移"},
		{"out_lb", model.ModeLoadBalance, "负载均衡"},
		{"out_smart", model.ModeSmart, "智能选择"},
	} {
		o := model.MakeOutbound(g.typ, g.tag)
		o.ID = g.id
		o.Members = []string{"out_ss", "out_vless"}
		doc.Outbounds = append(doc.Outbounds, o)
	}
	return doc
}

func liveModes() []api.Selector {
	return []api.Selector{
		{Name: "故障转移", Type: "Fallback", Mode: "fallback", Now: "日本 VLESS", All: []string{"香港 SS", "日本 VLESS"},
			Members: []api.SelectorMember{{Name: "香港 SS", State: "down", SuccessRate: -1}, {Name: "日本 VLESS", State: "up", Active: true, SuccessRate: -1}}},
		{Name: "负载均衡", Type: "LoadBalance", Mode: "loadbalance", Strategy: "round_robin", Now: "日本 VLESS", All: []string{"香港 SS", "日本 VLESS"},
			Members: []api.SelectorMember{{Name: "香港 SS", State: "down", SuccessRate: -1}, {Name: "日本 VLESS", State: "up", Active: true, SuccessRate: -1}}},
		{Name: "智能选择", Type: "Smart", Mode: "smart", Now: "香港 SS", All: []string{"香港 SS", "日本 VLESS"},
			Members: []api.SelectorMember{{Name: "香港 SS", State: "up", Active: true, SuccessRate: 0.98, LatencyMS: 120, Score: 130}, {Name: "日本 VLESS", State: "suspect", SuccessRate: 0.5}}},
	}
}

// TestGroupModeCards: each mode's card tells its mode, a load balance
// group its strategy; offline a fallback group uses its first member and
// a load balance group counts its members.
func TestGroupModeCards(t *testing.T) {
	f := newFixture(t, modesDoc())
	for _, text := range []string{"故障转移 · 2", "负载均衡 · 轮询 · 2", "智能 · 2", "2 个成员", "测速 故障转移", "测速 负载均衡", "测速 智能选择"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q in %q", text, f.tt.Texts())
		}
	}
	if got := f.w.outbounds.inUse(groupOf(f, "out_fb")); got != "out_ss" {
		t.Errorf("offline fallback in use %q", got)
	}
	if got := f.w.outbounds.inUse(groupOf(f, "out_lb")); got != "" {
		t.Errorf("load balance in use %q", got)
	}
	f.save("group-modes-light.png")
}

// TestGroupModeCardsLive: with the core's states, the fallback group
// shows its live member, the load balance group the members in rotation.
func TestGroupModeCardsLive(t *testing.T) {
	f := newFixture(t, modesDoc())
	runCore(f, liveModes()...)
	if got := f.w.outbounds.inUse(groupOf(f, "out_fb")); got != "out_vless" {
		t.Errorf("live fallback in use %q", got)
	}
	if !f.tt.HasText("1 / 2") {
		t.Errorf("load balance availability missing: %q", f.tt.Texts())
	}
	f.tt.SetDark(true)
	for range 3 {
		f.tt.Advance(time.Second)
		f.tt.Frame()
	}
	f.save("group-modes-dark.png")
}

// TestGroupModeSheet: an automatic group's members only show, with their
// state and, for a smart group, the success rate.
func TestGroupModeSheet(t *testing.T) {
	f := newFixture(t, modesDoc())
	runCore(f, liveModes()...)
	f.w.outbounds.sheet.show("out_smart")
	f.tt.Advance(time.Second)
	f.tt.Frame()
	if !f.tt.HasText("智能 · 2 个成员") || !f.tt.HasText("成功率 98%") {
		t.Errorf("smart sheet: %q", f.tt.Texts())
	}
	if _, ok := f.tt.Find("选择 香港 SS"); ok {
		t.Error("a smart group's member is a choice")
	}
	for range 3 {
		f.tt.Advance(time.Second)
		f.tt.Frame()
	}
	f.save("group-sheet-smart.png")
}

// TestGroupEditorSwitchesMode: the editor's "Selection mode" switches the
// draft's mode keeping its members, and saving drops the old mode's
// fields.
func TestGroupEditorSwitchesMode(t *testing.T) {
	f := newFixture(t, groupsDoc())
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_auto")
	f.tt.Frame()
	if !f.tt.HasText("选择方式") || !f.tt.HasText("故障转移") {
		t.Fatalf("mode control missing: %q", f.tt.Texts())
	}
	f.click("故障转移")
	draft := f.w.outbounds.editor.draft
	if draft.Type != model.ModeFallback || len(draft.Members) != 1 || draft.Filter == nil {
		t.Fatalf("draft %+v", draft)
	}
	f.tt.Advance(time.Second)
	f.tt.Frame()
	f.save("group-editor-fallback.png")
	f.click("保存")
	f.saved(func(d *model.Doc) bool {
		for _, o := range d.Outbounds {
			if o.ID == "out_auto" {
				_, tolerance := o.Fields["tolerance"]
				return o.Type == model.ModeFallback && !tolerance && o.Fields["timeout"] == "5s"
			}
		}
		return false
	})
}

// TestGroupEditorFailedSaveKeepsOtherModeFields: a save that fails
// keeps the fields of the mode switched from, so that switching back
// restores them.
func TestGroupEditorFailedSaveKeepsOtherModeFields(t *testing.T) {
	f := newFixture(t, groupsDoc())
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_auto")
	f.tt.Frame()
	draft := f.w.outbounds.editor.draft
	draft.Fields["tolerance"] = 80
	f.click("故障转移")
	draft.Members, draft.Filter = nil, nil
	f.w.outbounds.editor.save()
	if !f.w.outbounds.editor.open {
		t.Fatal("a group without members was saved")
	}
	if draft.Fields["tolerance"] != 80 {
		t.Fatalf("failed save dropped tolerance: %v", draft.Fields["tolerance"])
	}
}

// TestGroupEditorLegacyServer: a server without the groupModes
// capability is offered the manual and fastest modes only.
func TestGroupEditorLegacyServer(t *testing.T) {
	f := newFixture(t, groupsDoc())
	f.env.Server.Info = &api.ServerInfo{Capabilities: map[string]bool{}}
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_auto")
	f.tt.Frame()
	if f.tt.HasText("故障转移") || !f.tt.HasText("最快") {
		t.Errorf("modes: %q", f.tt.Texts())
	}
}

// TestAddGroupOpensManualEditor: "+ Add policy group" opens the editor of a new
// manual group.
func TestAddGroupOpensManualEditor(t *testing.T) {
	f := newFixture(t, groupsDoc())
	f.click("添加策略组")
	testkit.Until(t, f.tt, func() bool { return f.w.outbounds.editor.open })
	if d := f.w.outbounds.editor.draft; d.Type != model.ModeManual || d.Tag != "节点选择" {
		t.Errorf("draft %s %q", d.Type, d.Tag)
	}
}
