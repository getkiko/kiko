package emit_test

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A module rule's ref carries the number the route table shows: the user's
// own pre-device rules first, then the modules' rules in order.
func TestEmitModuleRuleOrdinals(t *testing.T) {
	d := moduleDoc(false, newModule("mod_a1", "Demo", demoContents()))
	d.Route.Groups = append(d.Route.Groups, &model.RouteGroup{ID: "pg", Name: "前置", DeviceID: model.PreDeviceID})
	d.Route.Rules = append(d.Route.Rules, &model.RouteRule{ID: "user_pre", GroupID: "pg", Name: "用户前置", Enabled: true,
		DeviceID: model.PreDeviceID, Match: map[string]any{"domain": []any{"u.com"}}, Action: model.ActionReject})
	user := len(model.RouteRuleOrdinals(d, model.PreDeviceID))
	if user != 1 {
		t.Fatalf("user pre rules numbered %d", user)
	}
	want := model.ModuleRuleOrdinals(d)
	if want["mod:mod_a1:1"] != 2 || want["mod:mod_a1:2"] != 3 || want["mod:mod_a1:4"] != 5 {
		t.Fatalf("ordinals %v", want)
	}
	r := emit.Emit(d, emit.Options{})
	got := map[string]int{}
	for _, ref := range r.RuleMap {
		if model.IsModuleRule(ref.ID) {
			got[ref.ID] = ref.Ordinal
		}
	}
	// Adjacent rules of one module with the same match and action share a
	// route rule, which carries the first one's number.
	if len(got) != 3 || got["mod:mod_a1:1"] != 2 || got["mod:mod_a1:3"] != 4 || got["mod:mod_a1:4"] != 5 {
		t.Errorf("ref ordinals %v", got)
	}
}
