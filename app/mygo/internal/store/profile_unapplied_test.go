package store

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func emitted(t *testing.T, doc *model.Doc) map[string]any {
	t.Helper()
	result := emit.Emit(doc, emit.Options{})
	if len(result.Errors) > 0 {
		t.Fatalf("emit: %v", result.Errors)
	}
	var config map[string]any
	if err := json.Unmarshal(result.JSON, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

// Switching a device's profile is a pending change of the routing rules (and,
// with other DNS rules, of the DNS routing rules), which the live apply takes.
func TestProfileSwitchIsAPendingRouteChange(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	exit := doc.Outbounds[0].ID
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Addresses: []string{"192.168.1.20/32"}, Final: exit}}
	doc = model.FillDefaults(doc)
	group := model.RouteGroupsOf(doc, "phone")[0].ID
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r-ph", DeviceID: "phone", GroupID: group, Name: "规则", Enabled: true,
		Action: model.ActionRoute, Outbound: model.Some(exit), Match: map[string]any{"domain_suffix": []any{"apple.com"}}})
	doc.DNS.Rules = append(doc.DNS.Rules, &model.DNSRule{ID: "d-ph", DeviceID: "phone", GroupID: model.DNSGroupsOf(doc, "phone")[0].ID, Name: "解析", Enabled: true,
		Action: model.ActionRoute, Server: model.Some(doc.DNS.Servers[0].ID), Match: map[string]any{"domain_suffix": []any{"apple.com"}}})
	doc.DNS.DeviceFinals["phone"] = model.RefOf(doc.DNS.Servers[1].ID)
	next, err := model.AddProfile(doc, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	d := next.Route.Devices[0]
	var work model.DeviceProfile
	for _, p := range model.DeviceProfiles(d) {
		if p.Name == "工作" {
			work = p
		}
	}
	applied := emitted(t, next)
	switched, err := model.SetActiveProfile(next, "phone", work.ID)
	if err != nil {
		t.Fatal(err)
	}
	u := classifyUnapplied(emitted(t, switched), applied)
	if !slices.Contains(u.Parts, "路由规则") || !u.Pages[env.PageRoute] {
		t.Fatalf("a profile switch is not a route change: %v", u.Parts)
	}
	// Switching back is no change.
	back, err := model.SetActiveProfile(switched, "phone", model.MainProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if u := classifyUnapplied(emitted(t, back), applied); u.Count() != 0 {
		t.Fatalf("switching back leaves changes: %v", u.Parts)
	}
}
