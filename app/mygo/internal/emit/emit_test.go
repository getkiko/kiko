package emit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Selected cases ported from the former TypeScript emitter's tests
// (emit.test.ts, config-dependencies.test.ts); the recorded-call replay
// compares every emitted document of those tests and a corpus with every
// protocol.

func doc() *model.Doc {
	d := model.FillDefaults(model.EmptyDoc("linux"))
	d.Outbounds = append(d.Outbounds, &model.Outbound{Entity: model.Entity{ID: "proxy", Tag: "代理", Enabled: true}, Kind: "outbound", Type: "trojan",
		Fields: model.Fields{"server": "example.com", "server_port": float64(443), "password": "p<&>", "tls.enabled": true}})
	return d
}

func TestEmitEmptyDoc(t *testing.T) {
	r := emit.Emit(model.FillDefaults(model.EmptyDoc("linux")), emit.Options{})
	if len(r.Errors) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("errors %v warnings %v", r.Errors, r.Warnings)
	}
	text := string(r.JSON)
	for _, want := range []string{`"type": "mixed",` + "\n" + `      "tag": "mixed-in"`, `"listen_port": 7890`, `"final": "直连"`,
		`"clash_api": {`, `"default_domain_resolver": {`, `"server": "dns-cn"`, `"strategy": "ipv4_only"`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "{\n  \"log\": {") {
		t.Errorf("top-level order: %.40s", text)
	}
	rules := r.Config["route"].(map[string]any)["rules"].([]any)
	if len(rules) < 3 || rules[0].(map[string]any)["action"] != "sniff" || rules[1].(map[string]any)["action"] != "hijack-dns" {
		t.Errorf("system rules %v", rules)
	}
}

func TestEmitTagsAndHTML(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds, &model.Outbound{Entity: model.Entity{ID: "dup", Tag: "代理", Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}})
	r := emit.Emit(d, emit.Options{})
	if !strings.Contains(string(r.JSON), `"password": "p<&>"`) {
		t.Error("HTML characters escaped")
	}
	if !slicesContains(r.Warnings, "tag 重名，已自动重命名为「代理 2」") {
		t.Errorf("warnings %v", r.Warnings)
	}
}

func TestEmitRouteErrors(t *testing.T) {
	d := doc()
	group := model.RouteGroupsOf(d, model.LocalDeviceID)[0].ID
	d.Route.Rules = []*model.RouteRule{
		{ID: "a", Name: "无出口", GroupID: group, Enabled: true, Match: map[string]any{"domain": []any{"x.example"}}, Action: model.ActionRoute},
		{ID: "b", GroupID: group, Enabled: true, Match: map[string]any{"domain": []any{"y.example"}}, Action: model.ActionRoute, Outbound: model.Some("gone")},
	}
	r := emit.Emit(d, emit.Options{})
	want := []string{"路由规则没有选择目标出站，请在路由页为它选择一个出站", "路由规则的目标出站已删除，请修改规则"}
	for _, w := range want {
		if !slicesContains(r.Errors, w) {
			t.Errorf("errors %v lack %q", r.Errors, w)
		}
	}
}

func TestDependencyCycle(t *testing.T) {
	config := map[string]any{
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "https", "tag": "doh", "server": "dns.example", "detour": "proxy"}}},
		"outbounds": []any{map[string]any{"type": "trojan", "tag": "proxy", "server": "proxy.example"}},
		"route":     map[string]any{"default_domain_resolver": "doh"},
	}
	errs := emit.ConfigDependencyErrors(config)
	if len(errs) != 1 || !strings.HasPrefix(errs[0], "循环依赖：") || !strings.Contains(errs[0], "前置代理") {
		t.Fatalf("errors %v", errs)
	}
	config["outbounds"].([]any)[0].(map[string]any)["server"] = "1.2.3.4"
	if errs := emit.ConfigDependencyErrors(config); len(errs) != 0 {
		t.Errorf("IP server: %v", errs)
	}
}

func slicesContains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}

func hasTag(list any, tag string) bool {
	items, _ := list.([]any)
	for _, item := range items {
		if m, ok := item.(map[string]any); ok && m["tag"] == tag {
			return true
		}
		if item == tag {
			return true
		}
	}
	return false
}

// A group whose only member is a group without members is skipped too,
// and nothing references either tag.
func TestEmitEmptyGroupsAreNotReferenced(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds,
		&model.Outbound{Entity: model.Entity{ID: "empty", Tag: "空组", Enabled: true}, Kind: "outbound", Type: "selector", Fields: model.Fields{}, Members: []string{"gone"}},
		&model.Outbound{Entity: model.Entity{ID: "outer", Tag: "外层", Enabled: true}, Kind: "outbound", Type: "urltest", Fields: model.Fields{}, Members: []string{"empty"}},
		&model.Outbound{Entity: model.Entity{ID: "mixed", Tag: "混合", Enabled: true}, Kind: "outbound", Type: "selector", Fields: model.Fields{}, Members: []string{"empty", "proxy"}},
	)
	group := model.RouteGroupsOf(d, model.LocalDeviceID)[0].ID
	d.Route.Rules = []*model.RouteRule{
		{ID: "r", GroupID: group, Enabled: true, Match: map[string]any{"domain": []any{"x.example"}}, Action: model.ActionRoute, Outbound: model.Some("outer")},
	}
	r := emit.Emit(d, emit.Options{})
	outbounds := r.Config["outbounds"]
	if hasTag(outbounds, "空组") || hasTag(outbounds, "外层") || !hasTag(outbounds, "混合") {
		t.Fatalf("outbounds %v", outbounds)
	}
	for _, o := range outbounds.([]any) {
		if m := o.(map[string]any); m["tag"] == "混合" && (hasTag(m["outbounds"], "空组") || !hasTag(m["outbounds"], "代理")) {
			t.Errorf("members %v", m["outbounds"])
		}
	}
	if strings.Contains(string(r.JSON), `"outbound": "外层"`) {
		t.Errorf("route references a skipped group:\n%s", r.JSON)
	}
	if !slicesContains(r.Errors, "路由规则引用的分组「外层」没有可用成员，请添加成员或修改规则") {
		t.Errorf("errors %v", r.Errors)
	}
}

// A rule naming only disabled inbounds must not widen to every inbound.
func TestEmitRuleWithDisabledInbound(t *testing.T) {
	d := doc()
	d.Inbounds = append(d.Inbounds, &model.Inbound{Entity: model.Entity{ID: "off", Tag: "off-in", Enabled: false}, Kind: "inbound", Type: "socks", Fields: model.Fields{"listen_port": 1080.0}})
	group := model.RouteGroupsOf(d, model.PreDeviceID)[0].ID
	d.Route.Rules = []*model.RouteRule{
		{ID: "r", DeviceID: model.PreDeviceID, GroupID: group, Enabled: true, Match: map[string]any{"inbound": []any{"off"}, "domain": []any{"x.example"}}, Action: model.ActionReject},
	}
	r := emit.Emit(d, emit.Options{})
	if !slicesContains(r.Errors, "路由规则引用的入站均已删除或停用，请修改规则") {
		t.Errorf("errors %v", r.Errors)
	}
	d.Route.Rules[0].Match = map[string]any{"preferred_by": []any{"gone"}, "domain": []any{"x.example"}}
	r = emit.Emit(d, emit.Options{})
	if !slicesContains(r.Errors, "路由规则的优先出站均已删除或停用，请修改规则") {
		t.Errorf("errors %v", r.Errors)
	}
}

// A disabled DNS rule of a deleted device is not emitted and blocks nothing.
func TestEmitDisabledDNSRuleOfDeletedDevice(t *testing.T) {
	d := doc()
	d.DNS.DevicePolicyVersion = 1
	d.DNS.Rules = append(d.DNS.Rules, &model.DNSRule{ID: "stale", Name: "旧规则", DeviceID: "gone", GroupID: "gone-group", Enabled: false,
		Match: map[string]any{"domain": []any{"x.example"}}, Action: model.ActionRoute})
	r := emit.Emit(d, emit.Options{})
	for _, e := range r.Errors {
		if strings.Contains(e, "旧规则") {
			t.Errorf("errors %v", r.Errors)
		}
	}
}

// The local device's implicit default is the first outbound emitted, not
// an empty group that is skipped.
func TestEmitLocalFinalSkipsEmptyGroup(t *testing.T) {
	d := doc()
	d.Outbounds = append([]*model.Outbound{{Entity: model.Entity{ID: "empty", Tag: "空组", Enabled: true}, Kind: "outbound", Type: "selector", Fields: model.Fields{}}}, d.Outbounds...)
	if d.OutboundOrder != nil {
		d.OutboundOrder = append([]string{"empty"}, d.OutboundOrder...)
	}
	d.Route.LocalFinal = model.Some("")
	r := emit.Emit(d, emit.Options{})
	for _, e := range r.Errors {
		if strings.Contains(e, "空组") {
			t.Errorf("errors %v", r.Errors)
		}
	}
	if strings.Contains(string(r.JSON), `"outbound": "空组"`) {
		t.Errorf("references the empty group:\n%s", r.JSON)
	}
}

// Detours and HTTP clients that point at a disabled or deleted target are
// errors: dropping them would send the traffic through another route.
func TestEmitUnavailableDetoursAreErrors(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds,
		&model.Outbound{Entity: model.Entity{ID: "off", Tag: "停用", Enabled: false}, Kind: "outbound", Type: "socks", Fields: model.Fields{"server": "192.0.2.1", "server_port": 1080.0}},
		&model.Outbound{Entity: model.Entity{ID: "chain", Tag: "链式", Enabled: true}, Kind: "outbound", Type: "socks", Fields: model.Fields{"server": "192.0.2.2", "server_port": 1080.0, "detour": "off"}},
		&model.Outbound{Entity: model.Entity{ID: "chain2", Tag: "链式2", Enabled: true}, Kind: "outbound", Type: "socks", Fields: model.Fields{"server": "192.0.2.3", "server_port": 1080.0, "detour": "gone"}},
	)
	d.HTTPClients = []*model.HTTPClient{
		{ID: "hc", Tag: "客户端", Enabled: true, Detour: model.Some("off")},
		{ID: "hc-off", Tag: "停用客户端", Enabled: false},
	}
	d.Route.DefaultHTTPClient = model.Some("hc-off")
	r := emit.Emit(d, emit.Options{})
	for _, want := range []string{
		"出站「链式」的前置代理「停用」不可用，请启用它或重新选择",
		"出站「链式2」的前置代理已删除，请重新选择",
		"HTTP 客户端「客户端」的前置代理「停用」不可用，请启用它或重新选择",
		"路由默认的 HTTP 客户端「停用客户端」已停用，请启用它或重新选择",
	} {
		if !slicesContains(r.Errors, want) {
			t.Errorf("errors %v lack %q", r.Errors, want)
		}
	}
	for _, w := range r.Warnings {
		if strings.Contains(w, "已清除") {
			t.Errorf("warning %q", w)
		}
	}
}

func TestEmitRuleSetMissingHTTPClient(t *testing.T) {
	d := doc()
	d.Route.RuleSets = append(d.Route.RuleSets, &model.RuleSet{ID: "rs", Tag: "my-set", Enabled: true, Mode: "remote", URL: "https://example.com/a.srs", HTTPClient: model.Some("gone")})
	group := model.RouteGroupsOf(d, model.PreDeviceID)[0].ID
	d.Route.Rules = []*model.RouteRule{{ID: "r", DeviceID: model.PreDeviceID, GroupID: group, Enabled: true, Match: map[string]any{"rule_set": []any{"my-set"}}, Action: model.ActionReject}}
	r := emit.Emit(d, emit.Options{})
	if !slicesContains(r.Errors, "规则集「my-set」的 HTTP 客户端已删除，请重新选择") {
		t.Errorf("errors %v", r.Errors)
	}
}

func TestEmittedTagsMatchEmit(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds,
		&model.Outbound{Entity: model.Entity{ID: "dup", Tag: " 代理 ", Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}},
		&model.Outbound{Entity: model.Entity{ID: "blank", Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}},
		&model.Outbound{Entity: model.Entity{ID: "off", Tag: "代理", Enabled: false}, Kind: "outbound", Type: "direct", Fields: model.Fields{}})
	d.Inbounds = append(d.Inbounds,
		&model.Inbound{Entity: model.Entity{ID: "in-blank", Enabled: true}, Kind: "inbound", Type: "mixed", Fields: model.Fields{"listen_port": float64(7891)}},
		&model.Inbound{Entity: model.Entity{ID: "in-blank-2", Tag: "  ", Enabled: true}, Kind: "inbound", Type: "mixed", Fields: model.Fields{"listen_port": float64(7892)}})
	tags := emit.EmittedTags(d)
	if tags.Outbounds["dup"] != "代理 2" || tags.Outbounds["proxy"] != "代理" {
		t.Errorf("outbound tags %v", tags.Outbounds)
	}
	if _, ok := tags.Outbounds["off"]; ok {
		t.Errorf("disabled outbound tagged: %v", tags.Outbounds)
	}
	r := emit.Emit(d, emit.Options{})
	emitted := map[string]bool{}
	for _, key := range []string{"outbounds", "inbounds"} {
		list, _ := r.Config[key].([]any)
		for _, item := range list {
			emitted[item.(map[string]any)["tag"].(string)] = true
		}
	}
	for id, tag := range tags.Inbounds {
		if !emitted[tag] {
			t.Errorf("inbound %s: tag %q not emitted (%v)", id, tag, emitted)
		}
	}
	for _, id := range []string{"proxy", "dup", "blank"} {
		if !emitted[tags.Outbounds[id]] {
			t.Errorf("outbound %s: tag %q not emitted (%v)", id, tags.Outbounds[id], emitted)
		}
	}
	if tags.Inbounds["in-blank"] != "mixed-in 2" || tags.Inbounds["in-blank-2"] != "mixed-in 3" {
		t.Errorf("inbound tags %v", tags.Inbounds)
	}
}

func TestEmitTailscaleUDPPath(t *testing.T) {
	d := doc()
	d.Endpoints = append(d.Endpoints, &model.Endpoint{Entity: model.Entity{ID: "ts", Tag: "ts", Enabled: true}, Kind: "endpoint", Type: "tailscale",
		Fields: model.Fields{"detour": "proxy", "udp_path.default": "detour", "udp_path.peers": []any{
			map[string]any{"match": []any{"macbook", " *-home "}, "path": "direct"},
			map[string]any{"match": "nas.tail1234.ts.net", "path": "relay"},
			map[string]any{"match": []any{}, "path": "relay"},
			map[string]any{"match": []any{"phone"}},
		}}})
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 || slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, "UDP 路径") }) {
		t.Fatalf("errors %v warnings %v", r.Errors, r.Warnings)
	}
	var endpoint map[string]any
	for _, e := range r.Config["endpoints"].([]any) {
		if m := e.(map[string]any); m["tag"] == "ts" {
			endpoint = m
		}
	}
	if endpoint == nil || endpoint["detour"] != "代理" {
		t.Fatalf("endpoint %v", endpoint)
	}
	udpPath := endpoint["udp_path"].(map[string]any)
	if udpPath["default"] != "detour" {
		t.Errorf("default %v", udpPath["default"])
	}
	peers := udpPath["peers"].([]any)
	want := []struct {
		match []string
		path  string
	}{{[]string{"macbook", "*-home"}, "direct"}, {[]string{"nas.tail1234.ts.net"}, "relay"}, {[]string{"phone"}, "direct"}}
	if len(peers) != len(want) {
		t.Fatalf("peers %v", peers)
	}
	for i, w := range want {
		peer := peers[i].(map[string]any)
		match := peer["match"].([]any)
		if peer["path"] != w.path || len(match) != len(w.match) {
			t.Errorf("peer %d: %v", i, peer)
			continue
		}
		for j, m := range w.match {
			if match[j] != m {
				t.Errorf("peer %d match %d: %v", i, j, match[j])
			}
		}
	}
}

func TestEmitTailscaleUDPPathNeedsDetour(t *testing.T) {
	d := doc()
	d.Endpoints = append(d.Endpoints, &model.Endpoint{Entity: model.Entity{ID: "ts", Tag: "ts", Enabled: true}, Kind: "endpoint", Type: "tailscale",
		Fields: model.Fields{"udp_path.peers": []any{map[string]any{"match": []any{"nas"}, "path": "detour"}}}})
	r := emit.Emit(d, emit.Options{})
	if !slicesContains(r.Warnings, "Tailscale 的 UDP 路径「经代理」需要前置代理，核心将拒绝启动") {
		t.Errorf("warnings %v", r.Warnings)
	}
	d.Endpoints[0].Fields = model.Fields{"udp_path.peers": []any{map[string]any{"match": []any{" "}, "path": "detour"}}}
	r = emit.Emit(d, emit.Options{})
	for _, e := range r.Config["endpoints"].([]any) {
		if _, ok := e.(map[string]any)["udp_path"]; ok {
			t.Errorf("rules without a name leave no udp_path: %v", e)
		}
	}
}
