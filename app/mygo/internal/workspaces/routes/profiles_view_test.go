package routes

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// profileDoc is the fixture whose phone has two profiles: Default (scope
// phone) with the rule "Phone rule" and the DNS rule "Phone DNS", and Work
// with "Work rule" and "Work DNS".
func profileDoc(t *testing.T) (*model.Doc, string) {
	t.Helper()
	doc := fixtureDoc()
	leaf := doc.Route.Rules[0].ConditionTree
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r-phone", GroupID: "rg-phone", DeviceID: "phone", Name: "手机规则",
		Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{}, ConditionTree: leaf})
	doc.DNS.Rules = append(doc.DNS.Rules, &model.DNSRule{ID: "d-phone", GroupID: "dg-phone", DeviceID: "phone", Name: "手机解析", Enabled: true,
		Action: model.ActionRoute, Server: doc.DNS.Rules[0].Server, Match: map[string]any{}, ConditionTree: leaf})
	next, err := model.AddProfile(doc, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	var scope string
	for _, p := range model.DeviceProfiles(next.Route.Devices[0]) {
		if p.Name == "工作" {
			scope = p.Scope
		}
	}
	var group, dnsGroup string
	for _, g := range next.Route.Groups {
		if g.DeviceID == scope {
			group = g.ID
		}
	}
	for _, g := range next.DNS.Groups {
		if g.DeviceID == scope {
			dnsGroup = g.ID
		}
	}
	next.Route.Rules = append(next.Route.Rules, &model.RouteRule{ID: "r-work", GroupID: group, DeviceID: scope, Name: "工作规则",
		Enabled: true, Action: model.ActionRoute, Outbound: model.Some("proxy"), Match: map[string]any{}, ConditionTree: leaf})
	next.DNS.Rules = append(next.DNS.Rules, &model.DNSRule{ID: "d-work", GroupID: dnsGroup, DeviceID: scope, Name: "工作解析", Enabled: true,
		Action: model.ActionRoute, Server: doc.DNS.Rules[0].Server, Match: map[string]any{}, ConditionTree: leaf})
	return next, scope
}

func profileFixture(t *testing.T) (*fixture, string) {
	doc, scope := profileDoc(t)
	f := newFixtureDoc(t, doc, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	return f, scope
}

func (f *fixture) profileID(name string) string {
	f.t.Helper()
	p, ok := profileByName(f.w.doc(), "phone", name)
	if !ok {
		f.t.Fatalf("no profile %q", name)
	}
	return p.ID
}

// The profiles of a device show below it while it is selected; otherwise
// its row shows the one in effect beside its name.
func TestProfileListFollowsSelection(t *testing.T) {
	f, _ := profileFixture(t)
	tt := f.tt
	if tt.HasText("工作") || !tt.HasText("默认") {
		t.Fatalf("unselected device: %q", tt.Texts())
	}
	f.click("iPhone")
	testkit.UntilText(t, tt, "工作")
	if !tt.HasText("默认") {
		t.Fatalf("selected device: %q", tt.Texts())
	}
	f.click("本机 / 网关")
	testkit.Until(t, tt, func() bool { return !tt.HasText("工作") })
}

// A click on a profile shows it, the topology and DNS too, without
// putting it in effect; the profile in effect and the device row go back.
func TestProfilePreviewChangesViews(t *testing.T) {
	f, scope := profileFixture(t)
	tt := f.tt
	f.click("iPhone")
	testkit.UntilText(t, tt, "手机规则")
	if f.w.owner != "phone" || tt.HasText("工作规则") {
		t.Fatalf("owner %q: %q", f.w.owner, tt.Texts())
	}
	saves := f.srv.Saves()
	f.click("工作")
	testkit.UntilText(t, tt, "工作规则")
	if f.w.owner != scope || tt.HasText("手机规则") {
		t.Fatalf("owner %q: %q", f.w.owner, tt.Texts())
	}
	// DNS and the topology follow the profile shown.
	f.click("DNS 分流")
	testkit.UntilText(t, tt, "工作解析")
	if tt.HasText("手机解析") {
		t.Fatalf("DNS: %q", tt.Texts())
	}
	f.click("流量规则")
	f.w.topology = true
	testkit.UntilText(t, tt, "iPhone · 工作")
	f.w.topology = false
	tt.Frame()
	if f.srv.Saves() != saves || profileActive(f) != model.MainProfileID || savedActive(f) != model.MainProfileID {
		t.Fatalf("a preview saved: saves %d → %d, active %q", saves, f.srv.Saves(), profileActive(f))
	}
	if f.w.profileAsk().state.Open {
		t.Fatal("a click asked to switch")
	}
	// Back to the profile in effect, through its row and the device row.
	f.click("默认")
	testkit.UntilText(t, tt, "手机规则")
	if f.w.owner != "phone" {
		t.Fatalf("owner %q", f.w.owner)
	}
	f.click("工作")
	testkit.Until(t, tt, func() bool { return f.w.owner == scope })
	f.click("iPhone")
	testkit.Until(t, tt, func() bool { return f.w.owner == "phone" })
	if f.srv.Saves() != saves {
		t.Fatal("going back saved")
	}
}

func TestProfileSelectDeviceUsesActiveScope(t *testing.T) {
	f, scope := profileFixture(t)
	f.w.update(func(doc *model.Doc) (*model.Doc, error) {
		return model.SetActiveProfile(doc, "phone", f.profileID("工作"))
	})
	f.click("本机 / 网关")
	f.click("iPhone")
	testkit.UntilText(t, f.tt, "工作规则")
	if f.w.owner != scope {
		t.Fatalf("owner %q", f.w.owner)
	}
}

func TestProfilePreviewAsksAboutDirtyDraft(t *testing.T) {
	f, scope := profileFixture(t)
	tt := f.tt
	f.click("iPhone")
	openGuarded(t, f, "r-phone", true)
	f.click("工作")
	tt.Frame()
	if !asks(f) || f.w.owner == scope {
		t.Fatalf("a preview with a dirty draft: asks %v owner %q", asks(f), f.w.owner)
	}
	f.click("放弃更改")
	testkit.Until(t, tt, func() bool { return f.w.owner == scope && f.w.editor == nil })
}

func TestProfileNewRenameDuplicateDelete(t *testing.T) {
	f, _ := profileFixture(t)
	tt := f.tt
	// The menus are the sidebar's; their entries call these.
	f.w.startNewProfile("phone")
	testkit.UntilText(t, tt, "新建配置")
	if f.w.profile == nil || f.w.profile.name != "新配置" {
		t.Fatalf("default name %+v", f.w.profile)
	}
	f.w.profile.name = "工作"
	f.click("保存")
	tt.Frame()
	if !tt.HasText("配置名称已存在") {
		t.Fatalf("duplicate name: %q", tt.Texts())
	}
	f.w.profile.name = "游戏"
	f.click("保存")
	testkit.Until(t, tt, func() bool { return f.w.profile == nil })
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].Profiles) == 3 })
	// The new profile shows; the one in effect stays.
	game := f.profileID("游戏")
	if d := findDevice(f.w.doc(), "phone"); model.ActiveProfileOf(d).ID != model.MainProfileID || f.w.owner != model.ProfileScope("phone", game) {
		t.Fatalf("active %q owner %q", model.ActiveProfileOf(d).ID, f.w.owner)
	}
	if !tt.HasText("游戏") || !tt.HasText("工作") {
		t.Fatalf("the profiles are not listed: %q", tt.Texts())
	}

	// Rename it from its own row.
	f.w.startRenameProfile("phone", game)
	tt.Frame()
	testkit.UntilText(t, tt, "重命名配置")
	f.w.profile.name = "娱乐"
	f.click("保存")
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Profiles[2].Name == "娱乐" })

	// Duplicating copies the rules and shows the copy, still not in effect.
	f.w.duplicateProfile("phone", f.profileID("工作"))
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].Profiles) == 4 })
	testkit.UntilText(t, tt, "工作规则")
	copyScope := model.ProfileScope("phone", f.profileID("工作 副本"))
	if f.w.owner != copyScope || len(model.RouteRulesOf(f.w.doc(), copyScope)) != 1 {
		t.Fatalf("owner %q rules %d", f.w.owner, len(model.RouteRulesOf(f.w.doc(), copyScope)))
	}
	if profileActive(f) != model.MainProfileID || savedActive(f) != model.MainProfileID {
		t.Fatalf("active %q after duplicate", profileActive(f))
	}

	// Delete asks beside the device's more button; cancelling keeps it.
	f.w.requestDeleteProfile("device:phone", "phone", f.profileID("工作 副本"))
	testkit.UntilText(t, tt, "删除配置「工作 副本」？")
	f.click("取消")
	testkit.Until(t, tt, func() bool { return !f.w.profileDelete.state.Open })
	if len(f.w.doc().Route.Devices[0].Profiles) != 4 {
		t.Fatal("cancel deleted")
	}
	f.w.requestDeleteProfile("device:phone", "phone", f.profileID("工作 副本"))
	testkit.UntilText(t, tt, "删除配置「工作 副本」？")
	f.click("删除")
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].Profiles) == 3 })
	// The deleted profile's rules go with it, and the view falls back to the
	// profile in effect.
	if d := findDevice(f.w.doc(), "phone"); f.w.owner != model.ActiveScope(d) {
		t.Fatalf("owner %q after delete", f.w.owner)
	}
	if len(model.RouteRulesOf(f.saved(), copyScope)) != 0 {
		t.Fatal("rules of the deleted profile remain")
	}
}

func TestProfileDeleteLastIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	if ok := f.w.update(func(doc *model.Doc) (*model.Doc, error) {
		return model.DeleteProfile(doc, "phone", model.MainProfileID)
	}); ok || f.w.err != model.ErrProfileLast.Error() {
		t.Fatalf("delete last: ok %v err %q", ok, f.w.err)
	}
}

func TestProfileCopyAndMoveTargets(t *testing.T) {
	f, scope := profileFixture(t)
	tt := f.tt
	doc := f.w.doc()
	scopes := model.AllPolicyScopes(doc)
	if !slices.Contains(scopes, scope) || model.ScopeName(doc, scope) != "iPhone · 工作" {
		t.Fatalf("scopes %v", scopes)
	}
	// The copy dialog lists both profiles.
	d := newCopyDialog(doc, copyRequest{sec: sectionTraffic, owner: model.LocalDeviceID})
	if c := d.choices(doc); !slices.Contains(c, "phone") || !slices.Contains(c, scope) {
		t.Fatalf("copy choices %v", c)
	}
	// "Copy to…" of the local row opens the dialog like this.
	f.w.copy = newCopyDialog(doc, copyRequest{sec: sectionTraffic, owner: scope})
	f.w.copy.selected = model.LocalDeviceID
	testkit.UntilText(t, tt, "复制流量规则")
	if !tt.HasText("从「本机 / 网关」复制到「iPhone · 工作」：2 个分组 · 3 条规则，按原顺序追加。") {
		t.Fatalf("summary: %q", tt.Texts())
	}
	f.click("复制")
	f.waitSaved(func(doc *model.Doc) bool { return len(model.RouteRulesOf(doc, scope)) == 4 })
	// A rule moves to a profile scope; to the device row it moves to the
	// profile in effect.
	f.w.moveToDevice(f.w.doc(), "r-apple", scope)
	f.waitSaved(func(doc *model.Doc) bool { r := findRule(doc, "r-apple"); return r != nil && r.DeviceID == scope })
	testkit.UntilText(t, tt, "已将“Apple 服务”移动到 iPhone · 工作")
}

func TestProfileDeviceDialogKeepsProfiles(t *testing.T) {
	f, _ := profileFixture(t)
	tt := f.tt
	active := f.profileID("工作")
	f.w.update(func(doc *model.Doc) (*model.Doc, error) { return model.SetActiveProfile(doc, "phone", active) })
	f.w.editDevice(f.w.doc(), "phone")
	tt.Frame()
	f.w.device.draft.Name = "我的手机"
	f.w.saveDevice(f.w.doc(), f.w.device)
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Name == "我的手机" })
	d := f.saved().Route.Devices[0]
	if len(d.Profiles) != 2 || d.ActiveProfile != active {
		t.Fatalf("profiles %+v active %q", d.Profiles, d.ActiveProfile)
	}
}

// A link to a rule of an inactive profile shows that profile's scope; a
// removed one falls back to the device's profile in effect.
func TestProfileOpenRuleOfInactiveProfile(t *testing.T) {
	f, scope := profileFixture(t)
	f.w.Open(env.RouteRule{ID: "r-work", Owner: scope})
	testkit.UntilText(t, f.tt, "工作规则")
	if f.w.owner != scope {
		t.Fatalf("owner %q", f.w.owner)
	}
	f.w.owner = "phone@gone"
	f.tt.Frame()
	if f.w.owner != "phone" {
		t.Fatalf("owner %q", f.w.owner)
	}
}

func TestProfileSidebarNarrowAndWide(t *testing.T) {
	f, scope := profileFixture(t)
	f.w.showDevice("phone", false)
	for _, width := range []int{1280, 1000, 700, 480} {
		f.tt = ui.NewTester(f.view, width, 820)
		f.w.sidebar = true
		f.tt.Frame()
		f.tt.Frame()
		if !f.tt.HasText("工作") {
			t.Fatalf("width %v: %q", width, f.tt.Texts())
		}
	}
	// In the overlay, a profile shows and the panel stays for a double
	// click.
	f.click("工作")
	testkit.Until(t, f.tt, func() bool { return f.w.owner == scope })
	f.tt.Frame()
	if !f.w.sidebar || profileActive(f) != model.MainProfileID {
		t.Fatalf("overlay %v active %q", f.w.sidebar, profileActive(f))
	}
}
