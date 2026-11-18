package store_test

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Only the rules of the scopes in effect have connections: the exit of a
// rule of an inactive device profile is not recorded as applied.
func TestNoteAppliedSkipsInactiveProfiles(t *testing.T) {
	h := newHarness(t, nil)
	doc := &model.Doc{}
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Addresses: []string{"192.168.1.2"}}}
	next, err := model.AddProfile(doc, "phone", "工作", "")
	if err != nil {
		t.Fatal(err)
	}
	var work string
	for _, p := range model.DeviceProfiles(next.Route.Devices[0]) {
		if p.Name == "工作" {
			work = p.Scope
		}
	}
	rule := func(id, scope string) *model.RouteRule {
		return &model.RouteRule{ID: id, DeviceID: scope, Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{}}
	}
	next.Route.Rules = append(next.Route.Rules, rule("r-active", "phone"), rule("r-inactive", work))
	h.s.NoteApplied(next)
	if _, ok := h.s.AppliedExit("r-active"); !ok {
		t.Error("the rule in effect has no applied exit")
	}
	if got, ok := h.s.AppliedExit("r-inactive"); ok {
		t.Errorf("a rule of an inactive profile has the applied exit %q", got)
	}
}
