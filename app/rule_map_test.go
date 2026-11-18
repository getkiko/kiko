package main

import "testing"

func TestRuleMapResolvesCoreRuleIndex(t *testing.T) {
	var m ruleMapState
	m.set([]ruleRef{
		{Index: 2, ID: "rr-1", Owner: "local", Section: "route", Ordinal: 3, Name: "Gfw"},
		{Index: 5, Owner: "local", Section: "route", Final: true},
	})
	got := m.resolve("#2 rule_set=[Gfw] => route(proxy)")
	if got.Label != "3 · Gfw" || got.ID != "rr-1" || got.Owner != "local" || got.Section != "route" {
		t.Fatalf("resolve rule = %+v", got)
	}
	if got := m.resolve("#5 source_ip_cidr=[10.0.0.1/32] => route(direct)"); got.Label != "默认出口" || got.ID != "" {
		t.Fatalf("resolve final = %+v", got)
	}
	if got := m.resolve("#1 protocol=dns => hijack-dns"); got.Label != "protocol=dns => hijack-dns" || got.ID != "" {
		t.Fatalf("resolve generated rule = %+v", got)
	}
	if got := m.resolve("network=tcp => sniff"); got.Label != "network=tcp => sniff" {
		t.Fatalf("resolve plain rule = %+v", got)
	}
	if got := m.resolve(""); got.Label != "" {
		t.Fatalf("resolve empty rule = %+v", got)
	}
	links := m.links()
	if len(links) != 1 || links["3 · Gfw"].ID != "rr-1" {
		t.Fatalf("links = %+v", links)
	}
}

func TestStripConnRuleIndexKeepsUnprefixedRules(t *testing.T) {
	for in, want := range map[string]string{
		"#12 domain=[a.com] => route(x)": "domain=[a.com] => route(x)",
		"#x domain=[a.com]":              "#x domain=[a.com]",
		"#3":                             "#3",
		"domain=[a.com]":                 "domain=[a.com]",
	} {
		if got := stripConnRuleIndex(in); got != want {
			t.Errorf("stripConnRuleIndex(%q) = %q, want %q", in, got, want)
		}
	}
}
