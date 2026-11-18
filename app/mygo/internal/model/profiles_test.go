package model_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func profileDoc(t *testing.T) *model.Doc {
	t.Helper()
	doc := base()
	doc.Outbounds = append(doc.Outbounds, &model.Outbound{Entity: model.Entity{ID: "alt", Tag: "备用", Enabled: true}, Kind: "outbound", Type: "socks",
		Fields: model.Fields{"server": "127.0.0.2", "server_port": float64(1081)}})
	doc.Route.Devices = []*model.RouteDevice{
		{ID: "phone", Name: "手机", Icon: "phone", Addresses: []string{"192.168.1.20/32"}, Final: "proxy"},
		{ID: "tv", Name: "电视", Icon: "computer", Addresses: []string{"192.168.1.30/32"}, Final: "alt"},
	}
	doc = model.FillDefaults(doc)
	group := model.RouteGroupsOf(doc, "phone")[0].ID
	doc.Route.Rules = append(doc.Route.Rules,
		&model.RouteRule{ID: "r1", DeviceID: "phone", GroupID: group, Name: "一", Enabled: true, Match: map[string]any{"domain": []any{"a.example"}}, Action: model.ActionRoute, Outbound: model.RefOf("alt")})
	dnsGroup := model.DNSGroupsOf(doc, "phone")[0].ID
	doc.DNS.Rules = append(doc.DNS.Rules,
		&model.DNSRule{ID: "d1", DeviceID: "phone", GroupID: dnsGroup, Name: "DNS一", Enabled: true, Match: map[string]any{"domain": []any{"a.example"}}, Server: model.RefOf(doc.DNS.Servers[0].ID)})
	doc.DNS.DeviceFinals["phone"] = model.RefOf(doc.DNS.Servers[0].ID)
	return doc
}

func device(doc *model.Doc, id string) *model.RouteDevice {
	for _, d := range doc.Route.Devices {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func profileByName(t *testing.T, d *model.RouteDevice, name string) model.DeviceProfile {
	t.Helper()
	for _, p := range model.DeviceProfiles(d) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no profile %q in %+v", name, d.Profiles)
	return model.DeviceProfile{}
}

func TestLegacyDeviceHasImplicitProfile(t *testing.T) {
	doc := profileDoc(t)
	d := device(doc, "phone")
	profiles := model.DeviceProfiles(d)
	if len(profiles) != 1 || profiles[0].Scope != "phone" || profiles[0].Final != "proxy" || model.ActiveScope(d) != "phone" {
		t.Fatalf("profiles %+v", profiles)
	}
	if b, _ := json.Marshal(d); strings.Contains(string(b), "rofile") {
		t.Errorf("legacy device encodes profiles: %s", b)
	}
	if !slices.Equal(model.AllPolicyScopes(doc), model.PolicyOwners(doc)) {
		t.Errorf("scopes %v owners %v", model.AllPolicyScopes(doc), model.PolicyOwners(doc))
	}
	if model.PolicyOwnerName(doc, "phone") != "手机" {
		t.Errorf("name %q", model.PolicyOwnerName(doc, "phone"))
	}
}

func TestAddBlankProfile(t *testing.T) {
	doc := profileDoc(t)
	next, err := model.AddProfile(doc, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Route.Devices[0].Profiles) != 0 {
		t.Error("the input document changed")
	}
	d := device(next, "phone")
	if len(d.Profiles) != 2 || d.Profiles[0].ID != model.MainProfileID || d.Profiles[0].Scope != "phone" {
		t.Fatalf("profiles %+v", d.Profiles)
	}
	work := profileByName(t, d, "工作")
	if work.Scope != "phone@"+work.ID || work.Final != "proxy" {
		t.Errorf("profile %+v", work)
	}
	if model.ActiveScope(d) != "phone" {
		t.Error("the new profile is active")
	}
	if len(model.RouteGroupsOf(next, work.Scope)) != 1 || len(model.DNSGroupsOf(next, work.Scope)) != 1 || len(model.RouteRulesOf(next, work.Scope)) != 0 {
		t.Error("blank profile lacks its default groups or has rules")
	}
	if _, ok := next.DNS.DeviceFinals[work.Scope]; !ok {
		t.Error("no DNS default")
	}
	if got := model.PolicyOwnerName(next, work.Scope); got != "手机 · 工作" {
		t.Errorf("name %q", got)
	}
	if got := model.PolicyOwnerName(next, "phone"); got != "手机 · 默认" {
		t.Errorf("name %q", got)
	}
	// Both profiles are scopes; only the active one is an owner in effect.
	if !slices.Contains(model.AllPolicyScopes(next), work.Scope) || slices.Contains(model.PolicyOwners(next), work.Scope) {
		t.Errorf("scopes %v owners %v", model.AllPolicyScopes(next), model.PolicyOwners(next))
	}
	if model.HasInvalidRouteGlobalReferences(next) {
		t.Error("invalid references after adding a profile")
	}
}

func TestAddProfileCopiesRulesWithNewIDs(t *testing.T) {
	doc := profileDoc(t)
	doc, _ = model.SetRouteFinal(doc, "phone", model.RefOf("alt"))
	next, err := model.AddProfile(doc, "phone", "副本", "phone")
	if err != nil {
		t.Fatal(err)
	}
	d := device(next, "phone")
	p := profileByName(t, d, "副本")
	if p.Final != "alt" {
		t.Errorf("final %q", p.Final)
	}
	rules, dnsRules := model.RouteRulesOf(next, p.Scope), model.DNSRulesOf(next, p.Scope)
	if len(rules) != 1 || len(dnsRules) != 1 || rules[0].ID == "r1" || dnsRules[0].ID == "d1" || rules[0].Outbound.ID() != "alt" || rules[0].Name != "一" {
		t.Fatalf("rules %+v dns %+v", rules, dnsRules)
	}
	groups := model.RouteGroupsOf(next, p.Scope)
	if len(groups) != 1 || groups[0].ID == model.RouteGroupsOf(next, "phone")[0].ID || rules[0].GroupID != groups[0].ID {
		t.Errorf("groups %+v", groups)
	}
	if dg := model.DNSGroupsOf(next, p.Scope); len(dg) != 1 || dnsRules[0].GroupID != dg[0].ID {
		t.Errorf("dns groups %+v", dg)
	}
	if next.DNS.DeviceFinals[p.Scope].ID() != doc.DNS.DeviceFinals["phone"].ID() {
		t.Error("DNS default not copied")
	}
	if len(model.RouteRulesOf(next, "phone")) != 1 {
		t.Error("source lost its rules")
	}
	// Copying from another device's scope works too.
	other, err := model.AddProfile(next, "tv", "来自手机", p.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if got := model.RouteRulesOf(other, model.ProfileScope("tv", profileByName(t, device(other, "tv"), "来自手机").ID)); len(got) != 1 {
		t.Errorf("rules %+v", got)
	}
}

func TestProfileNamesAreUnique(t *testing.T) {
	doc := profileDoc(t)
	if _, err := model.AddProfile(doc, "phone", "默认", ""); err != model.ErrProfileNameTaken {
		t.Errorf("duplicate of the implicit profile: %v", err)
	}
	if _, err := model.AddProfile(doc, "phone", "  ", ""); err != model.ErrProfileName {
		t.Errorf("blank: %v", err)
	}
	if _, err := model.AddProfile(doc, "gone", "x", ""); err != model.ErrProfileDevice {
		t.Errorf("device: %v", err)
	}
	if _, err := model.AddProfile(doc, "phone", "x", "nowhere"); err != model.ErrPolicyScope {
		t.Errorf("source: %v", err)
	}
	next, _ := model.AddProfile(doc, "phone", "工作", "")
	if _, err := model.AddProfile(next, "phone", "工作", ""); err != model.ErrProfileNameTaken {
		t.Errorf("duplicate: %v", err)
	}
	// Another device may reuse a name.
	if _, err := model.AddProfile(next, "tv", "工作", ""); err != nil {
		t.Errorf("other device: %v", err)
	}
	work := profileByName(t, device(next, "phone"), "工作")
	if _, err := model.RenameProfile(next, "phone", work.ID, "默认"); err != model.ErrProfileNameTaken {
		t.Errorf("rename to a taken name: %v", err)
	}
	renamed, err := model.RenameProfile(next, "phone", work.ID, " 家里 ")
	if err != nil {
		t.Fatal(err)
	}
	if profileByName(t, device(renamed, "phone"), "家里").ID != work.ID {
		t.Error("renamed profile changed id")
	}
	if got := model.UniqueProfileName(device(renamed, "phone"), "家里"); got != "家里 2" {
		t.Errorf("unique %q", got)
	}
	// Renaming the implicit profile writes the profiles out.
	imp, err := model.RenameProfile(doc, "tv", model.MainProfileID, "客厅")
	if err != nil || device(imp, "tv").Profiles[0].Name != "客厅" || model.ActiveScope(device(imp, "tv")) != "tv" {
		t.Errorf("implicit rename %v %+v", err, device(imp, "tv").Profiles)
	}
	if _, err := model.RenameProfile(next, "phone", "nope", "x"); err != model.ErrProfileGone {
		t.Errorf("gone: %v", err)
	}
}

func TestDeleteProfile(t *testing.T) {
	doc := profileDoc(t)
	if _, err := model.DeleteProfile(doc, "phone", model.MainProfileID); err != model.ErrProfileLast {
		t.Errorf("last profile: %v", err)
	}
	next, _ := model.AddProfile(doc, "phone", "工作", "phone")
	work := profileByName(t, device(next, "phone"), "工作")
	if _, err := model.DeleteProfile(next, "phone", "nope"); err != model.ErrProfileGone {
		t.Errorf("gone: %v", err)
	}
	// An inactive profile goes with its rules; the active one stays.
	after, err := model.DeleteProfile(next, "phone", work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(device(after, "phone").Profiles) != 1 || model.ActiveScope(device(after, "phone")) != "phone" {
		t.Fatalf("profiles %+v", device(after, "phone"))
	}
	assertNoScope(t, after, work.Scope)
	if len(model.RouteRulesOf(after, "phone")) != 1 {
		t.Error("the original profile lost rules")
	}
	// Deleting the active profile activates the first remaining one and
	// removes its rules; the original scope disappears.
	active, _ := model.SetActiveProfile(next, "phone", work.ID)
	if model.ActiveScope(device(active, "phone")) != work.Scope {
		t.Fatal("not active")
	}
	gone, err := model.DeleteProfile(active, "phone", work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if model.ActiveScope(device(gone, "phone")) != "phone" {
		t.Errorf("active %q", model.ActiveScope(device(gone, "phone")))
	}
	assertNoScope(t, gone, work.Scope)
	orig, err := model.DeleteProfile(active, "phone", model.MainProfileID)
	if err != nil {
		t.Fatal(err)
	}
	d := device(orig, "phone")
	if len(d.Profiles) != 1 || d.ActiveProfile != work.ID || model.ActiveScope(d) != work.Scope {
		t.Fatalf("device %+v", d)
	}
	assertNoScope(t, orig, "phone")
	if len(model.RouteRulesOf(orig, work.Scope)) != 1 {
		t.Error("remaining profile lost its rules")
	}
	if d.Final != "proxy" || model.HasInvalidRouteGlobalReferences(orig) {
		t.Errorf("device final %q", d.Final)
	}
	// Deleting the original profile of an inactive state activates the first.
	back, _ := model.SetActiveProfile(next, "phone", model.MainProfileID)
	dropped, _ := model.DeleteProfile(back, "phone", model.MainProfileID)
	if model.ActiveScope(device(dropped, "phone")) != work.Scope {
		t.Errorf("active %q", model.ActiveScope(device(dropped, "phone")))
	}
}

func assertNoScope(t *testing.T, doc *model.Doc, scope string) {
	t.Helper()
	for _, r := range doc.Route.Rules {
		if r.PolicyOwner() == scope {
			t.Errorf("route rule %s remains in %s", r.ID, scope)
		}
	}
	for _, r := range doc.DNS.Rules {
		if r.PolicyOwner() == scope {
			t.Errorf("DNS rule %s remains in %s", r.ID, scope)
		}
	}
	if len(model.RouteGroupsOf(doc, scope)) != 0 || len(model.DNSGroupsOf(doc, scope)) != 0 {
		t.Errorf("groups remain in %s", scope)
	}
	if _, ok := doc.DNS.DeviceFinals[scope]; ok {
		t.Errorf("DNS final remains in %s", scope)
	}
	if _, ok := doc.DNS.DeviceFinalDetours[scope]; ok {
		t.Errorf("DNS detour remains in %s", scope)
	}
	if slices.Contains(model.AllPolicyScopes(doc), scope) {
		t.Errorf("scope %s is listed", scope)
	}
}

func TestSetActiveProfile(t *testing.T) {
	doc := profileDoc(t)
	same, err := model.SetActiveProfile(doc, "phone", model.MainProfileID)
	if err != nil || len(device(same, "phone").Profiles) != 0 {
		t.Errorf("implicit: %v %+v", err, device(same, "phone"))
	}
	next, _ := model.AddProfile(doc, "phone", "工作", "")
	work := profileByName(t, device(next, "phone"), "工作")
	active, err := model.SetActiveProfile(next, "phone", work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := model.DeviceForSource(active, "192.168.1.20", nil); got != work.Scope {
		t.Errorf("source scope %q", got)
	}
	if got := model.DeviceForSource(next, "192.168.1.20", nil); got != "phone" {
		t.Errorf("source scope %q", got)
	}
	if !slices.Contains(model.PolicyOwners(active), work.Scope) || slices.Contains(model.PolicyOwners(active), "phone") {
		t.Errorf("owners %v", model.PolicyOwners(active))
	}
	if got := model.RouteFinalFor(active, work.Scope).ID(); got != "proxy" {
		t.Errorf("final %q", got)
	}
	if _, err := model.SetActiveProfile(next, "phone", "nope"); err != model.ErrProfileGone {
		t.Errorf("gone: %v", err)
	}
	// An unknown active id falls back to the first profile.
	broken := *device(active, "phone")
	broken.ActiveProfile = "stale"
	if model.ActiveScope(&broken) != "phone" {
		t.Error("stale active profile")
	}
}

func TestSetRouteFinalOfProfileScope(t *testing.T) {
	doc := profileDoc(t)
	next, _ := model.AddProfile(doc, "phone", "工作", "")
	work := profileByName(t, device(next, "phone"), "工作")
	set, err := model.SetRouteFinal(next, work.Scope, model.RefOf("alt"))
	if err != nil {
		t.Fatal(err)
	}
	if model.RouteFinalFor(set, work.Scope).ID() != "alt" || model.RouteFinalFor(set, "phone").ID() != "proxy" || device(set, "phone").Final != "proxy" {
		t.Errorf("finals %q %q", model.RouteFinalFor(set, work.Scope).ID(), model.RouteFinalFor(set, "phone").ID())
	}
	if model.RouteFinalFor(next, work.Scope).ID() != "proxy" {
		t.Error("input changed")
	}
	if model.OutboundUseCount(set, "alt") != model.OutboundUseCount(next, "alt")+1 {
		t.Error("an inactive profile's default outbound is not counted")
	}
	dns, _ := model.SetDNSFinal(set, work.Scope, model.RefOf(set.DNS.Servers[1].ID))
	if model.DNSFinalFor(dns, work.Scope).ID() != set.DNS.Servers[1].ID {
		t.Error("DNS final")
	}
}

// Rules of inactive profiles are referenced like those of active ones.
func TestInactiveProfileReferences(t *testing.T) {
	doc := profileDoc(t)
	next, _ := model.AddProfile(doc, "phone", "工作", "phone")
	work := profileByName(t, device(next, "phone"), "工作")
	rule := model.RouteRulesOf(next, work.Scope)[0]
	// The active profile's copy and the inactive one both count.
	if before, after := model.OutboundUseCount(doc, "alt"), model.OutboundUseCount(next, "alt"); after != before+1 {
		t.Errorf("use count %d -> %d", before, after)
	}
	active, _ := model.SetActiveProfile(next, "phone", work.ID)
	if model.OutboundUseCount(active, "alt") != model.OutboundUseCount(next, "alt") {
		t.Error("switching the profile changes the use count")
	}
	// Removing the outbound leaves the inactive rule dangling and flagged.
	gone := *next
	gone.Outbounds = slices.DeleteFunc(slices.Clone(next.Outbounds), func(o *model.Outbound) bool { return o.ID == "alt" })
	if !model.HasInvalidRuleReferences(&gone, rule) {
		t.Error("inactive profile rule with a deleted outbound is not flagged")
	}
	if model.HasInvalidRuleReferences(next, rule) {
		t.Error("valid inactive rule flagged")
	}
	// A rule in a scope of no device is flagged; an inactive profile's is not.
	orphan := *rule
	orphan.DeviceID = "phone@nope"
	if !model.HasInvalidRuleReferences(next, &orphan) {
		t.Error("orphan scope")
	}
	// The default outbound of an inactive profile is validated.
	bad, _ := model.SetRouteFinal(next, work.Scope, model.RefOf("missing"))
	if !model.HasInvalidRouteGlobalReferences(bad) {
		t.Error("inactive profile final not validated")
	}
	bad.Route.Devices[0].Profiles = slices.Clone(bad.Route.Devices[0].Profiles)
	// Blank or duplicate profile names are device errors.
	broken := *device(next, "phone")
	broken.Profiles = slices.Clone(broken.Profiles)
	broken.Profiles[1].Name = broken.Profiles[0].Name
	if model.ValidateDevice(&broken, []*model.RouteDevice{&broken}) == "" {
		t.Error("duplicate profile names accepted")
	}
	broken.Profiles[1].Name = "x"
	broken.Profiles[1].Final = ""
	if model.ValidateDevice(&broken, []*model.RouteDevice{&broken}) == "" {
		t.Error("blank profile final accepted")
	}
}

func TestRemovePolicyDeviceRemovesEveryProfile(t *testing.T) {
	doc := profileDoc(t)
	next, _ := model.AddProfile(doc, "phone", "工作", "phone")
	work := profileByName(t, device(next, "phone"), "工作")
	next, _ = model.SetActiveProfile(next, "phone", work.ID)
	next, _ = model.AddProfile(next, "tv", "其他", "")
	after, err := model.RemovePolicyDevice(next, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if device(after, "phone") != nil || device(after, "tv") == nil {
		t.Fatalf("devices %+v", after.Route.Devices)
	}
	assertNoScope(t, after, "phone")
	assertNoScope(t, after, work.Scope)
	tv := profileByName(t, device(after, "tv"), "其他")
	if len(model.RouteGroupsOf(after, tv.Scope)) != 1 {
		t.Error("another device lost its profile")
	}
}

func TestCopyAndMoveBetweenProfileScopes(t *testing.T) {
	doc := profileDoc(t)
	next, _ := model.AddProfile(doc, "phone", "工作", "")
	work := profileByName(t, device(next, "phone"), "工作")
	copied, err := model.CopyDevicePolicy(next, model.CopyPolicyOptions{Section: model.SectionTraffic, Source: "phone", Target: work.Scope, IncludeDefault: true})
	if err != nil || len(model.RouteRulesOf(copied, work.Scope)) != 1 {
		t.Fatalf("copy: %v", err)
	}
	moved, err := model.MoveDevicePolicy(copied, model.MovePolicyOptions{Section: model.SectionDNS, Source: "phone", Target: work.Scope, RuleID: "d1"})
	if err != nil || len(model.DNSRulesOf(moved, work.Scope)) != 1 || len(model.DNSRulesOf(moved, "phone")) != 0 {
		t.Fatalf("move: %v", err)
	}
	if _, err := model.CopyDevicePolicy(next, model.CopyPolicyOptions{Section: model.SectionTraffic, Source: "phone", Target: "phone@nope"}); err != model.ErrPolicyScope {
		t.Errorf("unknown target: %v", err)
	}
	// An inactive scope can be the source of a copy to the active one.
	back, err := model.CopyDevicePolicy(copied, model.CopyPolicyOptions{Section: model.SectionTraffic, Source: work.Scope, Target: "tv"})
	if err != nil || len(model.RouteRulesOf(back, "tv")) != 1 {
		t.Fatalf("copy back: %v", err)
	}
}

func TestMigrateGuaranteesGroupsForEveryProfile(t *testing.T) {
	doc := profileDoc(t)
	next, _ := model.AddProfile(doc, "phone", "工作", "")
	work := profileByName(t, device(next, "phone"), "工作")
	broken := *next
	broken.Route.Groups = slices.DeleteFunc(slices.Clone(next.Route.Groups), func(g *model.RouteGroup) bool { return g.DeviceID == work.Scope })
	broken.DNS.Groups = slices.DeleteFunc(slices.Clone(next.DNS.Groups), func(g *model.RouteGroup) bool { return g.DeviceID == work.Scope })
	delete(broken.DNS.DeviceFinals, work.Scope)
	route := model.MigrateRoutePolicy(broken.Route)
	broken.Route = route
	dns := model.MigrateDeviceDNS(&broken)
	broken.DNS = dns
	if len(model.RouteGroupsOf(&broken, work.Scope)) != 1 || len(model.DNSGroupsOf(&broken, work.Scope)) != 1 {
		t.Error("an inactive profile has no default group after migration")
	}
	if _, ok := broken.DNS.DeviceFinals[work.Scope]; !ok {
		t.Error("an inactive profile has no DNS default after migration")
	}
}

// After the original profile is deleted its default outbound is nobody's:
// it is neither counted nor validated.
func TestDeletedOriginalFinalIsIgnored(t *testing.T) {
	doc := profileDoc(t)
	next, _ := model.AddProfile(doc, "phone", "工作", "phone")
	work := profileByName(t, device(next, "phone"), "工作")
	if got := model.DeviceFinalOutbounds(device(doc, "phone")); !slices.Equal(got, []string{"proxy"}) {
		t.Fatalf("no profiles: %v", got)
	}
	active, _ := model.SetActiveProfile(next, "phone", work.ID)
	after, err := model.DeleteProfile(active, "phone", model.MainProfileID)
	if err != nil {
		t.Fatal(err)
	}
	d := device(after, "phone")
	if slices.Contains(model.DeviceScopes(d), "phone") {
		t.Fatalf("scopes %v", model.DeviceScopes(d))
	}
	// The orphan holds an outbound that is gone; it must not matter.
	d.Final = "missing"
	if got := model.DeviceFinalOutbounds(d); slices.Contains(got, "missing") || len(got) != 1 {
		t.Fatalf("finals %v", got)
	}
	if model.HasInvalidRouteGlobalReferences(after) {
		t.Error("the orphan final is validated")
	}
	d.Final = "zzz"
	without := model.OutboundUseCount(after, "proxy")
	d.Final = "proxy"
	if model.OutboundUseCount(after, "proxy") != without {
		t.Error("the orphan final is counted")
	}
}
