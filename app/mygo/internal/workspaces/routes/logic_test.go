package routes

import (
	"reflect"
	"regexp"
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// tableDoc has two groups of the local scope (a: first, last; b:
// other-first, other-last), an empty group, and the default row.
func tableDoc() *model.Doc {
	doc := &model.Doc{Version: 1}
	doc.Route.Groups = []*model.RouteGroup{{ID: "group-a", Name: "A"}, {ID: "group-b", Name: "B"}, {ID: "empty", Name: "E"},
		{ID: "phone-group", Name: "P", DeviceID: "phone"}}
	rule := func(id, group string) *model.RouteRule {
		return &model.RouteRule{ID: id, GroupID: group, Name: id, Enabled: true, Action: model.ActionReject, Match: map[string]any{}}
	}
	doc.Route.Rules = []*model.RouteRule{rule("first", "group-a"), rule("last", "group-a"), rule("other-first", "group-b"), rule("other-last", "group-b")}
	return doc
}

func rowIndex(t policyTable, kind rowKind, id string) int {
	return slices.IndexFunc(t.rows, func(r tableRow) bool {
		switch r.kind {
		case rowRule:
			return kind == rowRule && r.rule.ID == id
		case rowGroup, rowEmptyGroup:
			return kind == r.kind && r.group.ID == id
		}
		return kind == r.kind
	})
}

func ruleOrder(doc *model.Doc) []string {
	var out []string
	for _, r := range model.RouteRulesOf(doc, model.LocalDeviceID) {
		out = append(out, r.GroupID+"/"+r.ID)
	}
	return out
}

// TestPlanReorder checks drag reordering: rows own their drop slots,
// headings and empty groups take rules, and groups move among groups.
func TestPlanReorder(t *testing.T) {
	cases := []struct {
		name     string
		from     func(policyTable) int
		to       func(policyTable) int
		want     []string
		groupsTo []string
	}{
		{"the last of two rules goes before the first",
			func(t policyTable) int { return rowIndex(t, rowRule, "last") }, func(t policyTable) int { return rowIndex(t, rowRule, "first") },
			[]string{"group-a/last", "group-a/first", "group-b/other-first", "group-b/other-last"}, nil},
		{"a rule dropped on a row of another group goes before it",
			func(t policyTable) int { return rowIndex(t, rowRule, "first") }, func(t policyTable) int { return rowIndex(t, rowRule, "other-last") },
			[]string{"group-a/last", "group-b/other-first", "group-b/first", "group-b/other-last"}, nil},
		{"a rule dropped on a heading ends the group above",
			func(t policyTable) int { return rowIndex(t, rowRule, "other-first") }, func(t policyTable) int { return rowIndex(t, rowGroup, "group-b") },
			[]string{"group-a/first", "group-a/last", "group-a/other-first", "group-b/other-last"}, nil},
		{"a rule dropped before the first heading starts the first group",
			func(t policyTable) int { return rowIndex(t, rowRule, "other-last") }, func(policyTable) int { return 0 },
			[]string{"group-a/other-last", "group-a/first", "group-a/last", "group-b/other-first"}, nil},
		{"a rule dropped on an empty group joins it",
			func(t policyTable) int { return rowIndex(t, rowRule, "first") }, func(t policyTable) int { return rowIndex(t, rowEmptyGroup, "empty") },
			[]string{"group-a/last", "group-b/other-first", "group-b/other-last", "empty/first"}, nil},
		{"a rule dropped past the groups ends the last group",
			func(t policyTable) int { return rowIndex(t, rowRule, "first") }, func(t policyTable) int { return len(t.rows) },
			[]string{"group-a/last", "group-b/other-first", "group-b/other-last", "empty/first"}, nil},
		{"groups move before groups",
			func(t policyTable) int { return rowIndex(t, rowGroup, "group-b") }, func(t policyTable) int { return 0 },
			[]string{"group-b/other-first", "group-b/other-last", "group-a/first", "group-a/last"}, []string{"group-b", "group-a", "empty", "phone-group"}},
		{"a group dropped past the groups goes last of its device",
			func(t policyTable) int { return rowIndex(t, rowGroup, "group-a") }, func(t policyTable) int { return len(t.rows) },
			[]string{"group-b/other-first", "group-b/other-last", "group-a/first", "group-a/last"}, []string{"group-b", "empty", "group-a", "phone-group"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := tableDoc()
			table := buildTable(doc, sectionTraffic, model.LocalDeviceID, "")
			plan, ok := table.planReorder(tc.from(table), tc.to(table))
			if !ok {
				t.Fatal("no plan")
			}
			applyReorder(doc, sectionTraffic, model.LocalDeviceID, plan)
			if got := ruleOrder(doc); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rules %v, want %v", got, tc.want)
			}
			if tc.groupsTo != nil {
				var groups []string
				for _, g := range doc.Route.Groups {
					groups = append(groups, g.ID)
				}
				if !reflect.DeepEqual(groups, tc.groupsTo) {
					t.Errorf("groups %v, want %v", groups, tc.groupsTo)
				}
			}
		})
	}
	// Rules of other devices stay put; their groups take no local rules.
	doc := tableDoc()
	before := ruleOrder(doc)
	applyReorder(doc, sectionTraffic, model.LocalDeviceID, reorderPlan{rule: "first", toGroup: "phone-group"})
	if !reflect.DeepEqual(ruleOrder(doc), before) {
		t.Error("a rule moved into another device's group")
	}
}

func TestBuildTable(t *testing.T) {
	doc := tableDoc()
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "orphan", Enabled: true, Action: model.ActionReject, Match: map[string]any{}})
	table := buildTable(doc, sectionTraffic, model.LocalDeviceID, "")
	var kinds []rowKind
	for _, r := range table.rows {
		kinds = append(kinds, r.kind)
	}
	want := []rowKind{rowGroup, rowRule, rowRule, rowRule, rowGroup, rowRule, rowRule, rowGroup, rowEmptyGroup, rowPinned}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("rows %v, want %v", kinds, want)
	}
	// Traffic rules without a group show in the first one, numbered in
	// table order.
	if table.rows[3].rule.ID != "orphan" || table.rows[3].ordinal != 3 || table.rows[5].ordinal != 4 {
		t.Errorf("orphan row %+v", table.rows[3])
	}
	searched := buildTable(doc, sectionTraffic, model.LocalDeviceID, "other")
	if searched.shown != 2 || searched.all != 5 || len(searched.rows) != 4 {
		t.Errorf("search rows %d shown %d all %d", len(searched.rows), searched.shown, searched.all)
	}
	none := buildTable(doc, sectionTraffic, model.PreDeviceID, "")
	if len(none.rows) != 1 || none.rows[0].kind != rowEmpty {
		t.Errorf("pre without groups: %+v", none.rows)
	}
	dns := buildTable(doc, sectionDNS, model.LocalDeviceID, "")
	if len(dns.rows) != 2 || dns.rows[0].kind != rowEmpty || dns.rows[1].kind != rowPinned {
		t.Errorf("empty DNS: %+v", dns.rows)
	}
}

func TestTokenInputs(t *testing.T) {
	doc := &model.Doc{}
	doc.Route.RuleSets = []*model.RuleSet{{ID: "rs", Tag: "my-set", Enabled: true, Mode: "inline", Rules: []any{map[string]any{"domain": []any{"a"}}}}}
	values, problem := tokenAdditions(doc, model.TypedPort, "80, 30 - 500", "")
	if problem != "" || !reflect.DeepEqual(values, []model.MatchValue{{Field: "port", Value: "80"}, {Field: "port_range", Value: "30:500"}}) {
		t.Errorf("ports %v %q", values, problem)
	}
	if _, problem := tokenAdditions(doc, model.TypedSourcePort, "70000", ""); problem != errPortFormat {
		t.Errorf("invalid port %q", problem)
	}
	values, _ = tokenAdditions(doc, model.TypedSourcePort, "443", "")
	if values[0].Field != "source_port" {
		t.Errorf("source port %v", values)
	}
	values, _ = tokenAdditions(doc, model.TypedWiFi, "AA-BB-CC-DD-EE-FF", "")
	if !reflect.DeepEqual(values, []model.MatchValue{{Field: "wifi_bssid", Value: "aa:bb:cc:dd:ee:ff"}}) {
		t.Errorf("wifi %v", values)
	}
	values, _ = tokenAdditions(doc, model.TypedIP, "set:private_ip", "")
	if values[0].Field != "ip_is_private" {
		t.Errorf("private %v", values)
	}
	values, _ = tokenAdditions(doc, model.TypedIP, "10.1.2.3/8", "")
	if values[0].Value != "10.0.0.0/8" {
		t.Errorf("network %v", values)
	}
	values, _ = tokenAdditions(doc, model.TypedIP, "my-set", "")
	if !reflect.DeepEqual(values, []model.MatchValue{{Field: "rule_set", Value: "my-set"}}) {
		t.Errorf("rule set %v", values)
	}
	if _, problem := tokenAdditions(doc, model.TypedIP, "nonsense", ""); problem == "" {
		t.Error("nonsense accepted")
	}
	suggestions := tokenSuggestions(doc, model.TypedIP, "set")
	if len(suggestions) < 2 || suggestions[0].ID != "private" || !slices.ContainsFunc(suggestions, func(s kit.TokenSuggestion) bool { return s.ID == "set:my-set" }) {
		t.Errorf("set suggestions %v", suggestions)
	}
	if s := tokenSuggestions(doc, model.TypedPort, "443"); len(s) != 1 || s[0].Detail != "HTTPS" {
		t.Errorf("port suggestion %v", s)
	}
	if s := domainSuggestions("Example.COM"); len(s) != 3 || s[0].ID != "domain" || s[0].Label != "example.com" || s[2].ID != "domain_keyword" {
		t.Errorf("domain suggestions %v", s)
	}
	if s := domainSuggestions(`^.*\.example\.com$`); s[0].ID != "domain_regex" {
		t.Errorf("regex suggestions %v", s)
	}
	if message, ok := regexResult(`^api\.`, "api.example.com"); ok == nil || !*ok || message != "匹配" {
		t.Errorf("regex test %q %v", message, ok)
	}
	if message, ok := regexResult(`(`, "api.example.com"); ok != nil || !regexp.MustCompile(`^正则表达式有误`).MatchString(message) {
		t.Errorf("bad regex %q", message)
	}
}

// TestLANDevices covers package model's LAN device helpers,
// which the device editor's LAN picker relies on.
func TestLANDevices(t *testing.T) {
	devices := []model.DiscoveredDevice{
		{MAC: "02:11:22:33:44:55", Hostname: "TV", Addresses: []string{"192.168.1.20", "fd00::20"}, Sources: []string{"neighbor", "dhcp"}},
		{MAC: "02:11:22:33:44:66", Hostname: "TV", Addresses: []string{"192.168.1.21"}, Sources: []string{"dhcp"}},
		{MAC: "02:11:22:33:44:77", Addresses: []string{"192.168.1.22"}, Sources: []string{"neighbor"}},
		{Hostname: "Phone", Addresses: []string{"fd00::23"}, Sources: []string{"dhcp"}},
	}
	for _, v := range []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff", "aabb.ccdd.eeff"} {
		if got := model.NormalizeMAC(v); got != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("NormalizeMAC(%q) = %q", v, got)
		}
	}
	if got := model.NormalizeMAC("02:11:22:33:44:55:66:77"); got != "02:11:22:33:44:55:66:77" {
		t.Errorf("EUI-64 %q", got)
	}
	for _, v := range []string{"", "aa:bb:cc", "aa:bb:cc:dd:ee:gg", "aa-bb:cc-dd-ee-ff", "aabb.ccdd", "aa:bb:cc:dd:ee:ff:12"} {
		if got := model.NormalizeMAC(v); got != "" {
			t.Errorf("NormalizeMAC(%q) = %q", v, got)
		}
	}
	if got := model.DeviceValues(" TV,TV\nPhone\n "); !reflect.DeepEqual(got, []string{"TV", "Phone"}) {
		t.Errorf("DeviceValues %v", got)
	}
	mac := "02:11:22:33:44:55"
	key := model.DeviceKey(model.DeviceModeMAC, mac)
	labels := map[string]model.DeviceLabel{key: {Alias: "客厅电视", Hostname: "OldTV"}}
	if got := model.DeviceName(model.DeviceModeMAC, mac, labels, devices); got != "客厅电视" {
		t.Errorf("alias %q", got)
	}
	labels = map[string]model.DeviceLabel{key: {Hostname: "OldTV"}}
	if got := model.DeviceName(model.DeviceModeMAC, mac, labels, devices); got != "TV" {
		t.Errorf("current hostname %q", got)
	}
	if got := model.DeviceName(model.DeviceModeMAC, mac, labels, nil); got != "OldTV" {
		t.Errorf("snapshot %q", got)
	}
	if got := model.DeviceName(model.DeviceModeMAC, mac, nil, nil); got != mac {
		t.Errorf("raw %q", got)
	}
	next, missing := model.ConvertDeviceMode([]string{devices[0].MAC, devices[1].MAC}, model.DeviceModeMAC, model.DeviceModeHostname, devices)
	if !reflect.DeepEqual(next, []string{"TV"}) || len(missing) != 0 {
		t.Errorf("to hostname %v %v", next, missing)
	}
	next, _ = model.ConvertDeviceMode([]string{"TV"}, model.DeviceModeHostname, model.DeviceModeMAC, devices)
	if !reflect.DeepEqual(next, []string{devices[0].MAC, devices[1].MAC}) {
		t.Errorf("to MAC %v", next)
	}
	next, missing = model.ConvertDeviceMode([]string{"tv"}, model.DeviceModeHostname, model.DeviceModeMAC, devices)
	if len(next) != 0 || !reflect.DeepEqual(missing, []string{"tv"}) {
		t.Errorf("case-sensitive %v %v", next, missing)
	}
	next, missing = model.ConvertDeviceMode([]string{devices[2].MAC, "02:11:22:33:44:88"}, model.DeviceModeMAC, model.DeviceModeHostname, devices)
	if len(next) != 0 || !reflect.DeepEqual(missing, []string{devices[2].MAC, "02:11:22:33:44:88"}) {
		t.Errorf("missing identities %v %v", next, missing)
	}
	labels = map[string]model.DeviceLabel{key: {Alias: "客厅电视", Hostname: "OldTV"}}
	updated := model.SnapshotDeviceLabels(model.DeviceModeMAC, []string{devices[0].MAC, devices[2].MAC}, labels, devices)
	if updated[key] != (model.DeviceLabel{Alias: "客厅电视", Hostname: "TV"}) || labels[key].Hostname != "OldTV" {
		t.Errorf("snapshot labels %v, original %v", updated[key], labels[key])
	}
}
