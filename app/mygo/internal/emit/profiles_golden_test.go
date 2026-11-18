package emit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// profileFixture is a document in the format before device profiles: two
// devices with route rules in several groups, DNS rules, and finals.
func profileFixture() *model.Doc {
	d := doc()
	d.Outbounds = append(d.Outbounds, &model.Outbound{Entity: model.Entity{ID: "alt", Tag: "备用", Enabled: true}, Kind: "outbound", Type: "socks",
		Fields: model.Fields{"server": "192.0.2.9", "server_port": float64(1080)}})
	d.Route.Devices = []*model.RouteDevice{
		{ID: "phone", Name: "手机", Icon: "phone", Addresses: []string{"192.168.1.20/32"}, Final: "proxy"},
		{ID: "tv", Name: "电视", Icon: "computer", Addresses: []string{"192.168.1.30", "10.0.0.0/24"}, Final: "alt"},
	}
	d = model.FillDefaults(d)
	g := func(owner string) string { return model.RouteGroupsOf(d, owner)[0].ID }
	extra := &model.RouteGroup{ID: "rg-phone-2", DeviceID: "phone", Name: "流媒体"}
	d.Route.Groups = append(d.Route.Groups, extra)
	d.Route.Rules = []*model.RouteRule{
		{ID: "r-pre", DeviceID: model.PreDeviceID, GroupID: g(model.PreDeviceID), Name: "前置", Enabled: true, Match: map[string]any{"domain": []any{"pre.example"}}, Action: model.ActionReject},
		{ID: "r-local", GroupID: g(model.LocalDeviceID), Name: "本机", Enabled: true, Match: map[string]any{"domain_suffix": []any{"local.example"}}, Action: model.ActionRoute, Outbound: model.RefOf("proxy")},
		{ID: "r-p1", DeviceID: "phone", GroupID: g("phone"), Name: "手机一", Enabled: true, Match: map[string]any{"domain_suffix": []any{"a.example"}}, Action: model.ActionRoute, Outbound: model.RefOf("alt")},
		{ID: "r-p2", DeviceID: "phone", GroupID: "rg-phone-2", Name: "手机二", Enabled: true, Match: map[string]any{"domain_suffix": []any{"b.example"}}, Action: model.ActionRoute, Outbound: model.RefOf("proxy")},
		{ID: "r-tv", DeviceID: "tv", GroupID: g("tv"), Name: "电视", Enabled: true, Match: map[string]any{"domain": []any{"tv.example"}}, Action: model.ActionRoute, Outbound: model.RefOf("proxy")},
		{ID: "r-other", DeviceID: model.OtherDeviceID, GroupID: g(model.OtherDeviceID), Name: "其他", Enabled: true, Match: map[string]any{"domain": []any{"other.example"}}, Action: model.ActionReject},
	}
	servers := d.DNS.Servers
	d.DNS.Rules = []*model.DNSRule{
		{ID: "d-phone", DeviceID: "phone", GroupID: model.DNSGroupsOf(d, "phone")[0].ID, Name: "手机DNS", Enabled: true, Match: map[string]any{"domain_suffix": []any{"a.example"}}, Server: model.RefOf(servers[0].ID)},
		{ID: "d-tv", DeviceID: "tv", GroupID: model.DNSGroupsOf(d, "tv")[0].ID, Name: "电视DNS", Enabled: true, Match: map[string]any{"domain": []any{"tv.example"}}, Server: model.RefOf(servers[1].ID)},
	}
	d.DNS.DeviceFinals["phone"] = model.RefOf(servers[0].ID)
	d.DNS.DeviceFinals["tv"] = model.RefOf(servers[1].ID)
	return d
}

type goldenOutput struct {
	JSON     string
	RuleMap  any
	Warnings []string
	Errors   []string
}

func goldenOf(r emit.Result) []byte {
	b, err := json.MarshalIndent(goldenOutput{string(r.JSON), r.RuleMap, r.Warnings, r.Errors}, "", " ")
	if err != nil {
		panic(err)
	}
	return b
}

// A document without profiles round-trips unchanged through JSON and the
// migrations, and emits exactly what it did before profiles existed.
func TestLegacyDocEmitsUnchanged(t *testing.T) {
	d := profileFixture()
	before, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid(before) && containsProfiles(before) {
		t.Fatalf("legacy document encodes profiles: %s", before)
	}
	var round model.Doc
	if err := json.Unmarshal(before, &round); err != nil {
		t.Fatal(err)
	}
	round.Route = model.MigrateRoutePolicy(round.Route)
	round.DNS = model.MigrateDeviceDNS(&round)
	after, _ := json.Marshal(&round)
	if string(before) != string(after) {
		t.Errorf("round trip changed the document:\n%s\n%s", before, after)
	}
	got := goldenOf(emit.Emit(&round, emit.Options{}))
	path := filepath.Join("testdata", "profiles_legacy.golden.json")
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("golden %s generated", path)
	}
	if string(want) != string(got) {
		t.Errorf("emit output changed:\n%s", got)
	}
}

func containsProfiles(b []byte) bool {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	var walk func(v any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if k == "profiles" || k == "activeProfile" || walk(e) {
					return true
				}
			}
		case []any:
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	return walk(m)
}
