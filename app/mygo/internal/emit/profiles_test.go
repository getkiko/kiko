package emit_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func withWorkProfile(t *testing.T) (*model.Doc, model.DeviceProfile) {
	t.Helper()
	d := profileFixture()
	d, err := model.AddProfile(d, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	var work model.DeviceProfile
	for _, p := range model.DeviceProfiles(d.Route.Devices[0]) {
		if p.Name == "工作" {
			work = p
		}
	}
	// A rule and a DNS rule only the work profile has.
	d.Route.Rules = append(d.Route.Rules, &model.RouteRule{ID: "r-work", DeviceID: work.Scope, GroupID: model.RouteGroupsOf(d, work.Scope)[0].ID, Name: "工作规则",
		Enabled: true, Match: map[string]any{"domain": []any{"work.example"}}, Action: model.ActionRoute, Outbound: model.RefOf("alt")})
	d.DNS.Rules = append(d.DNS.Rules, &model.DNSRule{ID: "d-work", DeviceID: work.Scope, GroupID: model.DNSGroupsOf(d, work.Scope)[0].ID, Name: "工作DNS",
		Enabled: true, Match: map[string]any{"domain": []any{"work.example"}}, Server: model.RefOf(d.DNS.Servers[1].ID)})
	d, err = model.SetRouteFinal(d, work.Scope, model.RefOf("alt"))
	if err != nil {
		t.Fatal(err)
	}
	d, _ = model.SetDNSFinal(d, work.Scope, model.RefOf(d.DNS.Servers[1].ID))
	return d, work
}

// Only the active profile of a device is emitted, and switching changes the
// configuration.
func TestEmitActiveProfileOnly(t *testing.T) {
	d, work := withWorkProfile(t)
	first := emit.Emit(d, emit.Options{})
	if len(first.Errors) != 0 {
		t.Fatalf("errors %v", first.Errors)
	}
	// The inactive profile does not change the output of the document
	// before it existed.
	base := emit.Emit(profileFixture(), emit.Options{})
	if string(base.JSON) != string(first.JSON) {
		t.Errorf("an inactive profile changed the configuration:\n%s", first.JSON)
	}
	if strings.Contains(string(first.JSON), "work.example") {
		t.Error("inactive profile rule emitted")
	}
	switched, err := model.SetActiveProfile(d, "phone", work.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := emit.Emit(switched, emit.Options{})
	if len(second.Errors) != 0 {
		t.Fatalf("errors %v", second.Errors)
	}
	text := string(second.JSON)
	if !strings.Contains(text, "work.example") || strings.Contains(text, "a.example") || strings.Contains(text, "b.example") {
		t.Errorf("switch did not replace the phone rules:\n%s", text)
	}
	if text == string(first.JSON) {
		t.Error("output unchanged by the switch")
	}
	// The rule map names the active scope's rules, and its default.
	var scopes []string
	for _, ref := range second.RuleMap {
		scopes = append(scopes, ref.Owner)
	}
	if !slices.Contains(scopes, work.Scope) || slices.Contains(scopes, "phone") {
		t.Errorf("rule map owners %v", scopes)
	}
	var final bool
	for _, ref := range second.RuleMap {
		if ref.Owner == work.Scope && ref.Final {
			final = true
		}
	}
	if !final {
		t.Error("no default outbound entry for the active profile")
	}
	// The phone's default outbound is the profile's.
	rules := second.Config["route"].(map[string]any)["rules"].([]any)
	var defaults []string
	for _, raw := range rules {
		r := raw.(map[string]any)
		if cidr, ok := r["source_ip_cidr"].([]any); ok && len(cidr) == 1 && cidr[0] == "192.168.1.20/32" {
			defaults = append(defaults, r["outbound"].(string))
		}
	}
	if !slices.Equal(defaults, []string{"备用"}) {
		t.Errorf("phone defaults %v", defaults)
	}
	// Switching back restores the first output.
	back, _ := model.SetActiveProfile(switched, "phone", model.MainProfileID)
	if got := emit.Emit(back, emit.Options{}); string(got.JSON) != string(first.JSON) {
		t.Error("switching back differs")
	}
}

// Rules of inactive profiles raise no "source device deleted" error, while
// rules of a scope without a device still do.
func TestEmitInactiveProfileRulesRaiseNoErrors(t *testing.T) {
	d, work := withWorkProfile(t)
	if r := emit.Emit(d, emit.Options{}); len(r.Errors) != 0 || len(r.Warnings) != 0 {
		t.Errorf("errors %v warnings %v", r.Errors, r.Warnings)
	}
	d.Route.Rules = append(d.Route.Rules, &model.RouteRule{ID: "r-orphan", DeviceID: "phone@nope", Name: "孤儿", Enabled: true,
		Match: map[string]any{"domain": []any{"o.example"}}, Action: model.ActionReject})
	d.DNS.Rules = append(d.DNS.Rules, &model.DNSRule{ID: "d-orphan", DeviceID: "phone@nope", Name: "孤儿DNS", Enabled: true,
		Match: map[string]any{"domain": []any{"o.example"}}, Server: model.RefOf(d.DNS.Servers[0].ID)})
	r := emit.Emit(d, emit.Options{})
	for _, want := range []string{"路由规则「孤儿」的来源设备已删除", "DNS 规则「孤儿DNS」的来源设备已删除"} {
		if !slicesContains(r.Errors, want) {
			t.Errorf("errors %v lack %q", r.Errors, want)
		}
	}
	for _, e := range r.Errors {
		if strings.Contains(e, "工作") {
			t.Errorf("error about the inactive profile: %s", e)
		}
	}
	_ = work
}

// The DNS policy follows the active profile too: its rules and default
// server.
func TestEmitDNSProfiles(t *testing.T) {
	d, work := withWorkProfile(t)
	before := emit.Emit(d, emit.Options{})
	if strings.Contains(string(before.JSON), "work.example") {
		t.Error("inactive DNS rule emitted")
	}
	switched, _ := model.SetActiveProfile(d, "phone", work.ID)
	after := emit.Emit(switched, emit.Options{})
	rules := after.Config["dns"].(map[string]any)["rules"].([]any)
	var sawWork, sawDefault bool
	for _, raw := range rules {
		b, _ := json.Marshal(raw)
		text := string(b)
		if strings.Contains(text, "work.example") && strings.Contains(text, "192.168.1.20/32") && strings.Contains(text, `"server":"dns-cn"`) {
			sawWork = true
		}
		if strings.Contains(text, "a.example") {
			t.Errorf("rule of the inactive profile: %s", text)
		}
		if !strings.Contains(text, "rules") && strings.Contains(text, `"source_ip_cidr":["192.168.1.20/32"]`) && strings.Contains(text, `"server":"dns-cn"`) {
			sawDefault = true
		}
	}
	if !sawWork || !sawDefault {
		t.Errorf("work rule %v, work default %v in %s", sawWork, sawDefault, after.JSON)
	}
	if len(after.Errors) != 0 {
		t.Errorf("errors %v", after.Errors)
	}
}

func TestEmitMITMDevicesIgnoreProfiles(t *testing.T) {
	d, work := withWorkProfile(t)
	a := emit.Emit(d, emit.Options{})
	switched, _ := model.SetActiveProfile(d, "phone", work.ID)
	b := emit.Emit(switched, emit.Options{})
	// Source addresses of the device appear unchanged in both outputs.
	if !strings.Contains(string(a.JSON), "192.168.1.20/32") || !strings.Contains(string(b.JSON), "192.168.1.20/32") {
		t.Error("device address missing")
	}
}
