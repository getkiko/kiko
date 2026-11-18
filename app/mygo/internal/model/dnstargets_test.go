package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A stored rule with one server reads as a list of one and writes back
// unchanged; several servers keep the first in server / detour.
func TestDNSRuleTargets(t *testing.T) {
	var rule model.DNSRule
	if err := json.Unmarshal([]byte(`{"id":"r","enabled":true,"match":{},"server":"a","detour":"out"}`), &rule); err != nil {
		t.Fatal(err)
	}
	targets := rule.Targets()
	if len(targets) != 1 || targets[0].Server.ID() != "a" || targets[0].Detour.ID() != "out" {
		t.Fatalf("targets %+v", targets)
	}
	raw, _ := json.Marshal(&rule)
	if strings.Contains(string(raw), "moreServers") {
		t.Fatalf("single server wrote %s", raw)
	}
	rule.SetTargets(append(targets, model.DNSTarget{Server: model.Some("b"), Detour: model.Null()}))
	raw, _ = json.Marshal(&rule)
	var back model.DNSRule
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Targets()
	if len(got) != 2 || back.Server.ID() != "a" || got[1].Server.ID() != "b" || !got[1].Detour.IsNull() {
		t.Fatalf("round trip %s → %+v", raw, got)
	}
	back.SetTargets(got[1:])
	if back.Server.ID() != "b" || back.MoreServers != nil {
		t.Fatalf("set one: %+v", back)
	}
	if (&model.DNSRule{}).Targets() != nil {
		t.Error("a rule without servers has targets")
	}
}
