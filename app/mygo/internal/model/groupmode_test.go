package model

import (
	"reflect"
	"testing"
)

func TestSwitchGroupModeKeepsMembersAndSharedFields(t *testing.T) {
	o := MakeOutbound(ModeFastest, GroupModeDefaultName(ModeFastest))
	o.Members = []string{"a", "b"}
	o.Filter = &GroupFilter{Regions: []string{"hk"}}
	o.Fields["url"] = "https://example.com/204"
	o.Fields["tolerance"] = float64(80)

	SwitchGroupMode(o, ModeLoadBalance)
	if o.Type != ModeLoadBalance || o.Tag != GroupModeDefaultName(ModeLoadBalance) {
		t.Fatalf("type %q tag %q", o.Type, o.Tag)
	}
	if !reflect.DeepEqual(o.Members, []string{"a", "b"}) || o.Filter == nil || o.Filter.Regions[0] != "hk" {
		t.Fatalf("members %v filter %v", o.Members, o.Filter)
	}
	if o.Fields["url"] != "https://example.com/204" || o.Fields["strategy"] != "round_robin" || o.Fields["timeout"] != "5s" {
		t.Fatalf("fields %v", o.Fields)
	}
	// Switching back restores the old mode's own field.
	SwitchGroupMode(o, ModeFastest)
	if o.Fields["tolerance"] != float64(80) {
		t.Fatalf("tolerance lost: %v", o.Fields)
	}
	SwitchGroupMode(o, ModeManual)
	PruneGroupFields(o)
	for _, key := range []string{"url", "tolerance", "strategy", "timeout"} {
		if _, ok := o.Fields[key]; ok {
			t.Fatalf("%s kept after pruning: %v", key, o.Fields)
		}
	}
}

func TestSwitchGroupModeKeepsCustomName(t *testing.T) {
	o := MakeOutbound(ModeManual, "我的组")
	SwitchGroupMode(o, ModeSmart)
	if o.Tag != "我的组" {
		t.Fatalf("tag %q", o.Tag)
	}
}

func TestOfflineCurrent(t *testing.T) {
	doc := EmptyDoc("linux")
	a, b := MakeOutbound("direct", "a"), MakeOutbound("direct", "b")
	a.Enabled = false
	doc.Outbounds = append(doc.Outbounds, a, b)
	members := []string{a.ID, b.ID}
	manual := MakeOutbound(ModeManual, "")
	if got := OfflineCurrent(doc, manual, members); got != a.ID {
		t.Fatalf("manual %q, want the first member", got)
	}
	manual.Fields["default_ref"] = b.ID
	if got := OfflineCurrent(doc, manual, members); got != b.ID {
		t.Fatalf("manual %q, want its default", got)
	}
	if got := OfflineCurrent(doc, MakeOutbound(ModeFallback, ""), members); got != b.ID {
		t.Fatalf("fallback %q, want the first enabled member", got)
	}
	for _, mode := range []string{ModeFastest, ModeLoadBalance, ModeSmart} {
		if got := OfflineCurrent(doc, MakeOutbound(mode, ""), members); got != "" {
			t.Fatalf("%s %q, want none", mode, got)
		}
	}
	if !HasCurrentMember(ModeSmart) || HasCurrentMember(ModeLoadBalance) || HasHealthCheck(ModeManual) || !HasHealthCheck(ModeFallback) {
		t.Fatal("mode traits")
	}
}
