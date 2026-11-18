package routes

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// interruptProfileFixture is the fixture with a rule on the phone, and its profiles
// Work (blank) and Copy (a copy of the first).
func interruptProfileFixture(t *testing.T) (f *fixture, work, copied model.DeviceProfile) {
	t.Helper()
	doc := fixtureDoc()
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r-phone", GroupID: "rg-phone", DeviceID: "phone", Name: "手机规则",
		Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{}})
	next, err := model.AddProfile(doc, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	next, err = model.AddProfile(next, "phone", "副本", "phone")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range model.DeviceProfiles(next.Route.Devices[0]) {
		switch p.Name {
		case "工作":
			work = p
		case "副本":
			copied = p
		}
	}
	f = newFixtureDoc(t, next, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.running(true)
	return f, work, copied
}

func TestProfileSwitchAsksOnlyForOtherExits(t *testing.T) {
	f, work, copied := interruptProfileFixture(t)
	doc := f.w.doc()
	if !f.w.asksToInterruptProfile(doc, "phone", work.ID) {
		t.Error("profiles with other exits do not ask")
	}
	if f.w.asksToInterruptProfile(doc, "phone", copied.ID) {
		t.Error("an identical copy asks")
	}
	if f.w.asksToInterruptProfile(doc, "phone", model.MainProfileID) {
		t.Error("the profile in effect asks")
	}
	if f.w.asksToInterruptProfile(doc, "phone", "gone") || f.w.asksToInterruptProfile(doc, "tv", work.ID) {
		t.Error("a missing profile or device asks")
	}
	f.running(false)
	if f.w.asksToInterruptProfile(doc, "phone", work.ID) {
		t.Error("asked with the core stopped")
	}
}

// The answer is recorded with the switch; switching back to where the
// connections are clears it.
func TestProfileSwitchRecordsAndRevertsTheAnswer(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	if !f.w.applyProfileSwitch("phone", work.ID, true, true) {
		t.Fatal(f.w.err)
	}
	f.waitSaved(func(doc *model.Doc) bool { return model.ActiveScope(doc.Route.Devices[0]) == work.Scope })
	if from, ok := f.store.ProfileInterrupt("phone"); !ok || from != "phone" {
		t.Fatalf("mark %q %v", from, ok)
	}
	// B → A needs no question: the connections are on A.
	if f.w.asksToInterruptProfile(f.w.doc(), "phone", model.MainProfileID) {
		t.Fatal("switching back asks")
	}
	if !f.w.applyProfileSwitch("phone", model.MainProfileID, false, false) {
		t.Fatal(f.w.err)
	}
	f.waitSaved(func(doc *model.Doc) bool { return model.ActiveScope(doc.Route.Devices[0]) == "phone" })
	if len(f.store.InterruptDevices()) != 0 {
		t.Fatalf("marks %v", f.store.InterruptDevices())
	}
}

// Keeping the connections records nothing.
func TestProfileSwitchKeepRecordsNothing(t *testing.T) {
	f, work, _ := interruptProfileFixture(t)
	if !f.w.applyProfileSwitch("phone", work.ID, true, false) {
		t.Fatal(f.w.err)
	}
	if len(f.store.InterruptDevices()) != 0 {
		t.Fatalf("marks %v", f.store.InterruptDevices())
	}
}

// The base of the question is the scope last applied: after A is applied,
// A → B (keep) → C asks about A against C.
func TestProfileSwitchComparesWithTheAppliedScope(t *testing.T) {
	f, work, copied := interruptProfileFixture(t)
	f.store.NoteApplied(f.w.doc())
	if !f.w.applyProfileSwitch("phone", work.ID, true, false) {
		t.Fatal(f.w.err)
	}
	// The copy has the exits of A, where the connections still are.
	if f.w.asksToInterruptProfile(f.w.doc(), "phone", copied.ID) {
		t.Error("a switch to a profile with the applied scope's exits asks")
	}
	if f.w.asksToInterruptProfile(f.w.doc(), "phone", model.MainProfileID) {
		t.Error("back to the applied profile asks")
	}
}

// The exits of a profile that is not in effect have no connections:
// editing them asks nothing, nor does a profile just switched to and not
// yet applied.
func TestInactiveProfileRuleEditNeverAsks(t *testing.T) {
	f, work, copied := interruptProfileFixture(t)
	e := &ruleEditor{}
	// Copy's rule is a copy with its own id; it is not in effect.
	var copyRule *model.RouteRule
	for _, r := range f.w.doc().Route.Rules {
		if r.DeviceID == copied.Scope {
			copyRule = r
		}
	}
	if copyRule == nil {
		t.Fatal("no copied rule")
	}
	edited := *copyRule
	edited.Outbound = model.RefOf("proxy")
	if f.w.asksToInterrupt(e, &edited) {
		t.Error("an inactive profile's rule asks")
	}
	// The rule in effect still asks.
	live := *findRule(f.w.doc(), "r-phone")
	live.Outbound = model.RefOf("proxy")
	if !f.w.asksToInterrupt(e, &live) {
		t.Error("the rule in effect does not ask")
	}
	// After a switch not yet applied, the old profile's rule is inactive and
	// the new one has no connections yet.
	f.store.NoteApplied(f.w.doc())
	if !f.w.applyProfileSwitch("phone", copied.ID, false, false) {
		t.Fatal(f.w.err)
	}
	if f.w.asksToInterrupt(e, &live) {
		t.Error("the profile switched away from asks")
	}
	if f.w.asksToInterrupt(e, &edited) {
		t.Error("the profile switched to, not applied, asks")
	}
	_ = work
}

// The confirmation shows beside the anchor, says the connections go only
// when they do, and performs the switch.
func TestProfileSwitchPopover(t *testing.T) {
	f, work, copied := interruptProfileFixture(t)
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		f.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		anchor := kit.Button(c, "锚点", kit.ButtonOptions{})
		if profile, open := f.w.pendingProfileSwitch("phone"); open {
			f.w.profileSwitchPopover(c, anchor, "phone", profile)
		}
	}, 600, 400)
	f.tt.Frame()
	f.w.openProfileSwitch("phone", copied.ID)
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "切换到「副本」？")
	if f.tt.HasText("已有连接") {
		t.Fatal("an identical copy says the connections go")
	}
	f.click("取消")
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("切换到「副本」？") })
	f.w.openProfileSwitch("phone", work.ID)
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "「iPhone」的已有连接会中断。")
	if model.ActiveScope(f.w.doc().Route.Devices[0]) != "phone" {
		t.Fatal("switched before the answer")
	}
	f.click("切换")
	f.waitSaved(func(doc *model.Doc) bool { return model.ActiveScope(doc.Route.Devices[0]) == work.Scope })
	if from, ok := f.store.ProfileInterrupt("phone"); !ok || from != "phone" {
		t.Fatalf("mark %q %v", from, ok)
	}
}
