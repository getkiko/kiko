package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// Selected cases ported from the former TypeScript console's route
// policy, device policy, TLS config and reference status tests, plus the
// queries the routes page uses. The recorded-call replay covers the rest.

func base() *model.Doc {
	doc := model.EmptyDoc("linux")
	doc.Outbounds = append(doc.Outbounds, &model.Outbound{Entity: model.Entity{ID: "proxy", Tag: "代理", Enabled: true}, Kind: "outbound", Type: "socks",
		Fields: model.Fields{"server": "127.0.0.1", "server_port": float64(1080)}})
	return model.FillDefaults(doc)
}

func TestEmptyDocFilled(t *testing.T) {
	doc := base()
	if got := model.PolicyOwners(doc); strings.Join(got, ",") != "pre,local,other" {
		t.Errorf("owners %v", got)
	}
	for _, owner := range model.PolicyOwners(doc) {
		if len(model.RouteGroupsOf(doc, owner)) != 1 || len(model.DNSGroupsOf(doc, owner)) != 1 {
			t.Errorf("%s: groups %d/%d", owner, len(model.RouteGroupsOf(doc, owner)), len(model.DNSGroupsOf(doc, owner)))
		}
	}
	if got := model.RouteFinalFor(doc, model.LocalDeviceID).ID(); got != doc.Outbounds[0].ID {
		t.Errorf("local final %q", got)
	}
	if got := model.DNSFinalFor(doc, model.LocalDeviceID).ID(); got != doc.DNS.Servers[1].ID {
		t.Errorf("local DNS %q", got)
	}
	if model.PolicyOwnerName(doc, model.LocalDeviceID) != "本机 / 网关" || model.PolicyOwnerName(doc, "gone") != "已删除设备" {
		t.Error("owner names")
	}
	if warnings := model.TabWarnings(doc, nil); warnings["route"] || warnings["subscriptions"] {
		t.Errorf("warnings on a new document: %v", warnings)
	}
	if model.HasBlockingReferenceWarnings(doc, nil) {
		t.Error("blocking warnings on a new document")
	}
}

func TestRuleSummary(t *testing.T) {
	cases := []struct {
		match map[string]any
		tree  *model.Condition
		logic string
		want  string
	}{
		{match: map[string]any{}, want: "全部连接"},
		{match: map[string]any{"domain_suffix": []any{"google.com", "youtube.com"}}, want: "域名后缀 google.com, youtube.com"},
		{match: map[string]any{"network": "tcp", "port": []any{float64(443)}}, want: "目标端口 443 · 传输协议 TCP"},
		{match: map[string]any{"domain": []any{"a"}, "port": []any{"80"}}, logic: "or", want: "目标端口 80 或 完整域名 a"},
		{match: map[string]any{"ip_is_private": true, "invert": true}, want: "排除（私有目标 是）"},
		{tree: &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
			{Kind: model.ConditionMatch, Match: map[string]any{"ip_version": float64(6)}},
			{Kind: model.ConditionMatch, Match: map[string]any{"clash_mode": "Global"}, Invert: true},
		}}, want: "IP 版本 IPv6 且 排除（Clash 模式 全局模式）"},
		{tree: &model.Condition{Kind: model.ConditionGroup, Mode: "or", Rules: []*model.Condition{
			model.ProcessSelectionLeaf(model.ProcessSelection{Source: "application", Type: "regex", Value: "/Applications/Example.app", Label: "Example"}),
		}}, want: "整个应用 Example"},
	}
	for _, c := range cases {
		if got := model.RuleSummary(c.match, c.tree, c.logic); got != c.want {
			t.Errorf("RuleSummary(%v) = %q, want %q", c.match, got, c.want)
		}
	}
}

func TestValidateCondition(t *testing.T) {
	leaf := func(match map[string]any) *model.Condition {
		return &model.Condition{Kind: model.ConditionMatch, Match: match}
	}
	valid := []map[string]any{
		{"interface_address": map[string]any{"en0": []any{"192.168.1.1/32", "2001:db8::1"}}},
		{"network_interface_address": map[string]any{"wifi": []any{"10.0.0.0/8"}}},
		{"user_id": []any{float64(0), float64(2147483647)}},
	}
	for _, m := range valid {
		if problem := model.ValidateCondition(leaf(m), false); problem != "" {
			t.Errorf("%v: %s", m, problem)
		}
	}
	invalid := []map[string]any{
		{"interface_address": map[string]any{}}, {"interface_address": map[string]any{"": []any{"192.168.1.1"}}},
		{"network_interface_address": map[string]any{"ethernet": []any{"invalid"}}}, {"network_interface_address": map[string]any{"unknown": []any{"1.1.1.1"}}},
		{"default_interface_address": []any{"invalid"}}, {"user_id": []any{float64(-1)}}, {"user_id": []any{float64(2147483648)}},
		{"port": []any{"0"}}, {"port_range": []any{"9:1"}}, {"domain": []any{" "}},
	}
	for _, m := range invalid {
		if problem := model.ValidateCondition(leaf(m), false); problem == "" {
			t.Errorf("%v accepted", m)
		}
	}
	if model.ValidateCondition(model.EmptyConditionGroup(), true) != "" || model.ValidateCondition(model.EmptyConditionGroup(), false) == "" {
		t.Error("empty group")
	}
}

func TestValidateConditionScriptNotInverted(t *testing.T) {
	script := &model.Condition{Kind: model.ConditionMatch, Match: map[string]any{"script": []any{"s"}}}
	domain := &model.Condition{Kind: model.ConditionMatch, Match: map[string]any{"domain": []any{"a.test"}}}
	group := func(invert bool, rules ...*model.Condition) *model.Condition {
		return &model.Condition{Kind: model.ConditionGroup, Mode: "and", Invert: invert, Rules: rules}
	}
	if problem := model.ValidateCondition(group(false, domain, script), false); problem != "" {
		t.Errorf("plain: %s", problem)
	}
	if problem := model.ValidateCondition(group(true, domain), false); problem != "" {
		t.Errorf("inverted domain: %s", problem)
	}
	excluded := *script
	excluded.Invert = true
	nested := group(true, group(false, script))
	for name, tree := range map[string]*model.Condition{
		"group":  group(true, domain, script),
		"leaf":   group(false, &excluded),
		"nested": group(false, nested),
	} {
		if problem := model.ValidateCondition(tree, false); problem == "" {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseNetwork(t *testing.T) {
	cases := map[string]string{
		"192.168.1.0/24": "[192 168 1 0]/24", "::ffff:10.0.0.1/128": "[10 0 0 1]/32", "fd00::/8": "[253 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0]/8",
		"1.2.3.4": "[1 2 3 4]/32", "2001:db8::1.2.3.4": "[32 1 13 184 0 0 0 0 0 0 0 0 1 2 3 4]/128",
	}
	for input, want := range cases {
		n, ok := model.ParseNetwork(input)
		if !ok || n.String() != want {
			t.Errorf("ParseNetwork(%q) = %v %v, want %s", input, n, ok, want)
		}
	}
	for _, input := range []string{"", "1.2.3", "1.2.3.4/33", "1.2.3.4/", "fe80::1%eth0", "::1::2", "1:2:3:4:5:6:7:8:9", "256.1.1.1"} {
		if _, ok := model.ParseNetwork(input); ok {
			t.Errorf("ParseNetwork(%q) accepted", input)
		}
	}
	if !model.NetworksOverlap("10.0.0.0/8", "10.1.2.3") || model.NetworksOverlap("10.0.0.0/8", "11.0.0.0/8") {
		t.Error("overlap")
	}
}

func TestDevicePolicyCopyMoveRemove(t *testing.T) {
	doc := base()
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Icon: "phone", Addresses: []string{"192.168.1.9/32"}, Final: "proxy"}}
	doc = model.FillDefaults(doc)
	local := model.RouteGroupsOf(doc, model.LocalDeviceID)[0]
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r1", GroupID: local.ID, Enabled: true, Match: map[string]any{"domain": []any{"a.example"}}, Action: model.ActionRoute, Outbound: model.Some("proxy")})
	copied, err := model.CopyDevicePolicy(doc, model.CopyPolicyOptions{Section: model.SectionTraffic, Source: model.LocalDeviceID, Target: "phone", IncludeDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	rules := model.RouteRulesOf(copied, "phone")
	if len(rules) != 1 || rules[0].ID == "r1" || rules[0].Outbound.ID() != "proxy" {
		t.Fatalf("copied rules %+v", rules)
	}
	if got := model.RouteFinalFor(copied, "phone").ID(); got != doc.Route.LocalFinal.ID() {
		t.Errorf("copied default %q", got)
	}
	moved, err := model.MoveDevicePolicy(doc, model.MovePolicyOptions{Section: model.SectionTraffic, Source: model.LocalDeviceID, Target: model.OtherDeviceID, RuleID: "r1"})
	if err != nil || len(model.RouteRulesOf(moved, model.OtherDeviceID)) != 1 || len(model.RouteRulesOf(moved, model.LocalDeviceID)) != 0 {
		t.Fatalf("moved %v", err)
	}
	if _, err := model.MoveDevicePolicy(doc, model.MovePolicyOptions{Section: model.SectionTraffic, Source: "phone", Target: model.OtherDeviceID, RuleID: "r1"}); err != model.ErrPolicyRuleGone {
		t.Errorf("moving a rule of another scope: %v", err)
	}
	removed, err := model.RemovePolicyDevice(doc, model.LocalDeviceID)
	if err != nil || model.LocalEnabled(removed) || len(model.RouteRulesOf(removed, model.LocalDeviceID)) != 0 {
		t.Fatalf("removed local: %v", err)
	}
	restored := model.AddLocalPolicyDevice(removed)
	if !model.LocalEnabled(restored) || len(model.RouteGroupsOf(restored, model.LocalDeviceID)) != 1 || len(model.DNSGroupsOf(restored, model.LocalDeviceID)) != 1 {
		t.Error("local not restored")
	}
	if _, err := model.RemovePolicyDevice(doc, model.PreDeviceID); err != model.ErrFixedScope {
		t.Errorf("removing pre: %v", err)
	}
	if _, err := model.SetDNSFinal(doc, model.PreDeviceID, model.Null()); err != model.ErrPreDeviceDefault {
		t.Errorf("pre DNS default: %v", err)
	}
}

func TestMovePolicyRule(t *testing.T) {
	rules := []*model.RouteRule{{ID: "a", GroupID: "g1"}, {ID: "b", GroupID: "g1"}, {ID: "c", GroupID: "g2"}}
	ids := func(list []*model.RouteRule) string {
		var out []string
		for _, r := range list {
			out = append(out, r.ID+":"+r.GroupID)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		id, group, target, position, want string
	}{
		{"a", "g1", "b", model.PositionAfter, "b:g1,a:g1,c:g2"},
		{"c", "g1", "a", model.PositionBefore, "c:g1,a:g1,b:g1"},
		{"a", "g2", "", "", "b:g1,c:g2,a:g2"},
		{"a", "g1", "a", "", "a:g1,b:g1,c:g2"},
	}
	for _, c := range cases {
		if got := ids(model.MovePolicyRule(rules, c.id, c.group, c.target, c.position)); got != c.want {
			t.Errorf("move %s → %s %s %s: %s, want %s", c.id, c.group, c.position, c.target, got, c.want)
		}
	}
	if ids(rules) != "a:g1,b:g1,c:g2" {
		t.Error("input modified")
	}
}

func TestNetworkConfigs(t *testing.T) {
	client := &model.NetworkConfig{ID: "client", Name: "客户端", Kind: model.NetworkConfigTLSClient, UpdatedAt: 1,
		Fields: model.Fields{"tls.min_version": "1.3", "tls.alpn": []any{"h2"}, "tls.fragment": true, "tls.kernel_tx": true}}
	linux := model.EffectiveNetworkFields(schema.OutboundSchema["trojan"], model.Fields{"tls.server_name": "node.example.com"}, model.NetworkConfigRefs{"tls": "client"}, []*model.NetworkConfig{client}, "linux")
	if linux["tls.server_name"] != "node.example.com" || linux["tls.kernel_tx"] != true {
		t.Errorf("linux %v", linux)
	}
	mac := model.EffectiveNetworkFields(schema.DNSTLSSchema, model.Fields{"tls": map[string]any{"server_name": "dns.example.com"}}, model.NetworkConfigRefs{"tls": "client"}, []*model.NetworkConfig{client}, "darwin")
	if _, ok := mac["tls.kernel_tx"]; ok || mac["tls.min_version"] != "1.3" {
		t.Errorf("darwin %v", mac)
	}
	if errs := model.NetworkReferenceErrors(schema.InboundSchema["trojan"], model.NetworkConfigRefs{"tls": "client"}, []*model.NetworkConfig{client}, "in"); len(errs) != 1 || !strings.Contains(errs[0], "类型不匹配") {
		t.Errorf("kind mismatch %v", errs)
	}
	with := func(fields model.Fields) *model.NetworkConfig {
		c := *client
		c.Fields = fields
		return &c
	}
	if p := model.ValidateNetworkConfig(with(model.Fields{"tls.alpn": []any{"my-protocol"}})); p != "" {
		t.Errorf("custom ALPN: %s", p)
	}
	for _, fields := range []model.Fields{{"tls.key_path": "server.key"}, {"tls.server_name": "per-node.example"},
		{"tls.min_version": "1.3", "tls.max_version": "1.2"}, {"tls.reality.enabled": true}, {"tls.utls.enabled": true}} {
		if model.ValidateNetworkConfig(with(fields)) == "" {
			t.Errorf("%v accepted", fields)
		}
	}
	summary := model.NetworkSummary(with(model.Fields{"tls.client_key": "PRIVATE-CONTENT", "tls.certificate": "CERT-CONTENT", "tls.alpn": []any{"h2"}}))
	if strings.Contains(summary, "PRIVATE") || strings.Contains(summary, "CERT-CONTENT") || !strings.Contains(summary, "h2") {
		t.Errorf("summary %q", summary)
	}
	if got := model.RebaseNetworkConfigs([]*model.NetworkConfig{{ID: "a"}}, []*model.NetworkConfig{{ID: "a"}, {ID: "new"}}, []*model.NetworkConfig{{ID: "b"}}); len(got) != 2 || got[1].ID != "new" {
		t.Errorf("rebase %v", got)
	}
}

func TestMigrateNetworkStoreShares(t *testing.T) {
	doc := base()
	doc.Outbounds[1].Fields["bind_interface"] = "eth0"
	other := model.Clone(doc)
	profiles := []*model.Profile{{ID: "home", Name: "家庭", Doc: doc}, {ID: "office", Name: "公司", Doc: other}}
	migrated, profiles, library := model.MigrateNetworkStore(doc, profiles, nil, "home")
	if len(library) != 1 || migrated.Outbounds[1].NetworkConfigRefs["default"] != library[0].ID {
		t.Fatalf("library %+v", library)
	}
	if _, ok := migrated.Outbounds[1].Fields["bind_interface"]; ok {
		t.Error("inline field kept")
	}
	if uses := model.NetworkUsage(profiles, library[0].ID); len(uses) != 2 {
		t.Errorf("uses %v", uses)
	}
	if _, ok := doc.Outbounds[1].NetworkConfigRefs["default"]; ok {
		t.Error("input modified")
	}
}

func TestDecodeDocInfersIPv6(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Inbounds[0].Fields["listen"] = "::"
	raw, _ := json.Marshal(doc)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	delete(m, "ipv6Enabled")
	raw, _ = json.Marshal(m)
	if decoded := model.DecodeDoc(raw); decoded == nil || !decoded.IPv6Enabled {
		t.Errorf("ipv6 not inferred")
	}
	if model.DecodeDoc([]byte(`{"version":2}`)) != nil {
		t.Error("version 2 accepted")
	}
}

// Documents saved before the core renamed holepunch to burrow load with
// burrow inbounds and outbounds, their fields kept.
func TestDecodeDocMigratesHolepunchType(t *testing.T) {
	doc := model.EmptyDoc("linux")
	in := model.MakeInbound("burrow", "burrow-in", "linux", false)
	in.Fields["signal_address"] = "signal.example.com:9960/path"
	out := model.MakeOutbound("burrow", "burrow-out")
	out.Fields["signal_address"] = "signal.example.com:9960/path"
	doc.Inbounds = append(doc.Inbounds, in)
	doc.Outbounds = append(doc.Outbounds, out)
	raw, _ := json.Marshal(doc)
	legacy := strings.ReplaceAll(string(raw), `"type":"burrow"`, `"type":"holepunch"`)
	if !strings.Contains(legacy, `"type":"holepunch"`) {
		t.Fatal("fixture has no holepunch entries")
	}
	decoded := model.DecodeDoc([]byte(legacy))
	if decoded == nil {
		t.Fatal("not decoded")
	}
	var inbound *model.Inbound
	for _, i := range decoded.Inbounds {
		if i.Tag == "burrow-in" {
			inbound = i
		}
	}
	var outbound *model.Outbound
	for _, o := range decoded.Outbounds {
		if o.Tag == "burrow-out" {
			outbound = o
		}
	}
	if inbound == nil || inbound.Type != "burrow" || inbound.Fields["signal_address"] != "signal.example.com:9960/path" {
		t.Errorf("inbound %+v", inbound)
	}
	if outbound == nil || outbound.Type != "burrow" || outbound.Fields["signal_address"] != "signal.example.com:9960/path" {
		t.Errorf("outbound %+v", outbound)
	}
}
