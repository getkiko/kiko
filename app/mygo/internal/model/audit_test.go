package model_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A stored document with one mistyped value keeps everything else; the
// former TypeScript console read stored documents without checking value
// types.
func TestDecodeDocSkipsMistypedValues(t *testing.T) {
	raw := `{"version":1,"platform":"linux","ipv6Enabled":false,
		"inbounds":[],"outbounds":[{"kind":"outbound","id":"o1","tag":7,"enabled":true,"type":"direct","fields":{}}],
		"subscriptions":[{"id":"s1","tag":"订阅","url":"https://x","updatedAt":1.5}],
		"route":{"rules":[{"id":"r1","enabled":true,"match":{"domain":["a.com"]},"action":"route","outbound":5,"name":3}],
			"ruleSets":[],"final":"o1","autoDetectInterface":true},
		"dns":{"servers":[],"rules":[{"id":"d1","enabled":true,"match":{},"server":"x","name":1}],"cacheCapacity":1.5},
		"experimental":{"clashApi":true,"externalController":"127.0.0.1:9090","cacheFile":true}}`
	doc := model.DecodeDoc([]byte(raw))
	if doc == nil {
		t.Fatal("document with a fractional cache capacity was discarded")
	}
	if len(doc.Outbounds) != 1 || doc.Outbounds[0].ID != "o1" || doc.Route.Final.ID() != "o1" {
		t.Fatalf("outbounds or final lost: %+v %v", doc.Outbounds, doc.Route.Final)
	}
	if len(doc.Subscriptions) != 1 || !doc.Subscriptions[0].Enabled {
		t.Fatalf("subscription lost: %+v", doc.Subscriptions)
	}
	var rule *model.RouteRule
	for _, r := range doc.Route.Rules {
		if r.ID == "r1" {
			rule = r
		}
	}
	if rule == nil || !rule.Enabled || rule.Outbound.Set() {
		t.Fatalf("route rule: %+v", rule)
	}
	if len(doc.DNS.Rules) == 0 {
		t.Fatal("DNS rules lost")
	}
	profiles := model.DecodeProfiles([]byte(`[{"id":"p1","name":"P","savedAt":1.5,"doc":` + raw + `}]`))
	if len(profiles) != 1 || profiles[0].Doc == nil {
		t.Fatalf("profile with a mistyped value was dropped: %+v", profiles)
	}
	if model.DecodeDoc([]byte(`{"version":"1"}`)) != nil || model.DecodeDoc([]byte(`{"version":1`)) != nil {
		t.Fatal("a non-version-1 or malformed document must still be rejected")
	}
}

func TestRenameRuleSetReferences(t *testing.T) {
	doc := base()
	doc.Route.Rules = append(doc.Route.Rules,
		&model.RouteRule{ID: "a", Enabled: true, Action: model.ActionRoute, Match: map[string]any{"rule_set": []any{"old", "keep"}}},
		&model.RouteRule{ID: "b", Enabled: true, Action: model.ActionRoute, Match: map[string]any{},
			ConditionTree: &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
				{Kind: model.ConditionMatch, Match: map[string]any{"rule_set": "old"}}}}})
	doc.DNS.Rules = append(doc.DNS.Rules, &model.DNSRule{ID: "d", Enabled: true, Match: map[string]any{"rule_set": []any{"old"}}})
	tun := model.MakeInbound("tun", "tun-in", "linux", false)
	tun.Fields["route_address_set"] = []any{"old"}
	tun.Fields["route_exclude_address_set"] = []any{"other"}
	doc.Inbounds = append(doc.Inbounds, tun)
	before, _ := json.Marshal(doc)

	next := model.RenameRuleSetReferences(doc, "old", "new")

	after, _ := json.Marshal(doc)
	if string(before) != string(after) {
		t.Fatal("RenameRuleSetReferences modified its input")
	}
	if got := model.UsedRuleSetTags(next); !reflect.DeepEqual(got, []string{"new", "keep", "other"}) {
		t.Fatalf("used tags after rename = %v", got)
	}
	in := next.Inbounds[len(next.Inbounds)-1]
	if !reflect.DeepEqual(in.Fields["route_address_set"], []any{"new"}) || !reflect.DeepEqual(in.Fields["route_exclude_address_set"], []any{"other"}) {
		t.Fatalf("TUN rule set fields: %v %v", in.Fields["route_address_set"], in.Fields["route_exclude_address_set"])
	}
}

// Moving a rule removes only that rule, even when another scope holds a
// rule with the same id.
func TestMoveDevicePolicyKeepsRulesSharingTheID(t *testing.T) {
	doc := base()
	doc.Route.Rules = []*model.RouteRule{
		{ID: "dup", DeviceID: model.PreDeviceID, Enabled: true, Action: model.ActionRoute, Match: map[string]any{"domain": []any{"a"}}},
		{ID: "dup", DeviceID: model.OtherDeviceID, Enabled: true, Action: model.ActionRoute, Match: map[string]any{"domain": []any{"b"}}},
	}
	next, err := model.MoveDevicePolicy(doc, model.MovePolicyOptions{Section: model.SectionTraffic, Source: model.PreDeviceID, Target: model.LocalDeviceID, RuleID: "dup"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Route.Rules) != 2 {
		t.Fatalf("rules after move = %d, want 2", len(next.Route.Rules))
	}
	owners := map[string]bool{}
	for _, r := range next.Route.Rules {
		owners[r.PolicyOwner()] = true
	}
	if !owners[model.LocalDeviceID] || !owners[model.OtherDeviceID] {
		t.Fatalf("owners after move = %v", owners)
	}
}

// Removing a process value keeps the other fields of its leaf.
func TestChangeProcessTagKeepsSiblingFields(t *testing.T) {
	group := &model.Condition{Kind: model.ConditionGroup, UIField: "process", Mode: "and", Rules: []*model.Condition{
		{Kind: model.ConditionMatch, Match: map[string]any{"process_name": []any{"curl"}, "process_path": []any{"/usr/bin/curl"}}},
	}}
	var tag model.ProcessTag
	for _, t := range model.ProcessTags(group) {
		if t.Field == "process_name" {
			tag = t
		}
	}
	next := model.ChangeProcessTag(group, tag, nil)
	if len(next.Rules) != 1 || !reflect.DeepEqual(next.Rules[0].Match, map[string]any{"process_path": []any{"/usr/bin/curl"}}) {
		t.Fatalf("after removing the name: %+v", next.Rules)
	}

	group.Rules[0].Match["process_name"] = []any{"curl", "wget"}
	for _, t := range model.ProcessTags(group) {
		if t.Field == "process_name" && t.Value == "curl" {
			tag = t
		}
	}
	next = model.ChangeProcessTag(group, tag, &model.ProcessSelection{Source: model.ProcessSourceCustom, Type: model.ProcessTypeName, Value: "git"})
	kept := next.Rules[0].Rules[0]
	if !reflect.DeepEqual(kept.Match, map[string]any{"process_name": []any{"wget"}, "process_path": []any{"/usr/bin/curl"}}) {
		t.Fatalf("after replacing a name: %+v", kept.Match)
	}
}

func TestWireguardOutboundToEndpointKeepsNetworkConfig(t *testing.T) {
	outbound := &model.Outbound{Entity: model.Entity{ID: "wg", Tag: "wg", Enabled: true, NetworkConfigRefs: model.NetworkConfigRefs{"default": "net_1"}},
		Kind: "outbound", Type: "wireguard", Fields: model.Fields{"server": "1.2.3.4"}}
	endpoint := model.WireguardOutboundToEndpoint(outbound)
	if endpoint.NetworkConfigRefs["default"] != "net_1" {
		t.Fatalf("network config refs = %v", endpoint.NetworkConfigRefs)
	}
}

func TestRemoveOutboundsPrunesMemberships(t *testing.T) {
	doc := base()
	doc.Subscriptions = []*model.Subscription{{ID: "s", Tag: "订阅", Enabled: true}}
	node := &model.Outbound{Entity: model.Entity{ID: "n1", Tag: "n1", Enabled: true}, Kind: "outbound", Type: "socks", SubID: "s", Fields: model.Fields{}}
	ep := &model.Endpoint{Entity: model.Entity{ID: "ep", Tag: "ep", Enabled: true}, Kind: "endpoint", Type: "wireguard", Fields: model.Fields{}}
	group := &model.Outbound{Entity: model.Entity{ID: "g", Tag: "g", Enabled: true}, Kind: "outbound", Type: "selector",
		Members: []string{"proxy", "n1", "ep"}, Fields: model.Fields{"default_ref": "proxy"}}
	doc.Outbounds = append(doc.Outbounds, node, group)
	doc.Endpoints = append(doc.Endpoints, ep)
	doc.OutboundOrder = append(doc.OutboundOrder, "sub:s", "g")
	doc.Route.Final = model.Some("proxy")
	live := doc.Outbounds

	model.RemoveOutbound(doc, "proxy")
	if g := doc.Outbounds[len(doc.Outbounds)-1]; !reflect.DeepEqual(g.Members, []string{"n1", "ep"}) || g.Fields["default_ref"] != nil {
		t.Fatalf("group after removing proxy: %v %v", g.Members, g.Fields)
	}
	if slices.Contains(doc.OutboundOrder, "proxy") || doc.Route.Final.ID() != "proxy" {
		t.Fatalf("order %v, final %v (single-target refs stay)", doc.OutboundOrder, doc.Route.Final)
	}
	if !reflect.DeepEqual(live[len(live)-1].Members, []string{"proxy", "n1", "ep"}) {
		t.Fatal("pruning modified a shared outbound")
	}
	model.RemoveSubscription(doc, "s")
	model.RemoveEndpoint(doc, "ep")
	g := doc.Outbounds[len(doc.Outbounds)-1]
	if len(g.Members) != 0 || slices.Contains(doc.OutboundOrder, "sub:s") || len(doc.Subscriptions) != 0 || len(doc.Endpoints) != 0 {
		t.Fatalf("after removing subscription and endpoint: members %v order %v", g.Members, doc.OutboundOrder)
	}
}
