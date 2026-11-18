package profiles

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func outboundID(doc *model.Doc, tag string) string {
	for _, o := range doc.Outbounds {
		if o.Tag == tag {
			return o.ID
		}
	}
	return ""
}

func dnsID(doc *model.Doc, tag string) string {
	for _, s := range doc.DNS.Servers {
		if s.Tag == tag {
			return s.ID
		}
	}
	return ""
}

// emitted emits doc and returns the configuration as plain JSON values.
func emitted(t *testing.T, doc *model.Doc, options emit.Options) map[string]any {
	t.Helper()
	result := emit.Emit(doc, options)
	if len(result.Errors) > 0 {
		t.Fatalf("emit errors: %v", result.Errors)
	}
	var config map[string]any
	if err := json.Unmarshal(result.JSON, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func plain(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// at reads a path of object keys and list indexes, nil where it is
// missing.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func rulesOf(config map[string]any, section string) []any {
	s, _ := config[section].(map[string]any)
	rules, _ := s["rules"].([]any)
	return rules
}

func TestImportRemapsOutboundReferences(t *testing.T) {
	doc := mustImport(t, `{
		"log": {"level": "info", "timestamp": true},
		"inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890}],
		"outbounds": [{"type": "direct", "tag": "direct"}, {"type": "selector", "tag": "main", "outbounds": ["direct"], "default": "direct"}],
		"dns": {"servers": [{"type": "local", "tag": "local"}, {"type": "https", "tag": "remote", "server": "1.1.1.1", "path": "/dns-query", "tls": {"server_name": "cloudflare-dns.com"}}], "final": "remote"},
		"route": {"rules": [], "final": "main"}}`, "linux")
	if doc.Version != 1 || outboundID(doc, "direct") == "" || len(doc.Inbounds) != 1 || doc.Inbounds[0].Tag != "mixed-in" {
		t.Fatalf("doc %s", asJSON(t, doc))
	}
	if doc.Route.Final.ID() != outboundID(doc, "main") || doc.DNS.Final.ID() != dnsID(doc, "remote") {
		t.Errorf("finals %v %v", doc.Route.Final, doc.DNS.Final)
	}
	main := doc.Outbounds[1]
	if !reflect.DeepEqual(main.Members, []string{outboundID(doc, "direct")}) || main.Fields["default_ref"] != outboundID(doc, "direct") {
		t.Errorf("selector %s", asJSON(t, main))
	}
	config := emitted(t, doc, emit.Options{})
	if _, ok := config["outbounds"].([]any); !ok {
		t.Error("no outbounds emitted")
	}
	if at(config, "route", "final") != "main" {
		t.Errorf("route %v", config["route"])
	}
}

func TestImportLogicalRulesAsConditionTrees(t *testing.T) {
	doc := mustImport(t, `{"inbounds": [], "outbounds": [{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}],
		"route": {"rules": [{"type": "logical", "mode": "and", "rules": [{"domain_suffix": ["example.com"]}], "action": "route", "outbound": "direct"}], "final": "block"}}`, "linux")
	rule := doc.Route.Rules[0]
	if rule.ConditionTree == nil || len(rule.ConditionTree.Rules) != 1 {
		t.Fatalf("rule %s", asJSON(t, rule))
	}
	if rule.Outbound.ID() != outboundID(doc, "direct") || doc.Route.Final.ID() != outboundID(doc, "block") {
		t.Errorf("rule %s final %v", asJSON(t, rule), doc.Route.Final)
	}
}

func TestImportMapsDNSAndHTTPClientReferences(t *testing.T) {
	doc := mustImport(t, `{
		"inbounds": [{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": 7890}],
		"outbounds": [{"type": "direct", "tag": "direct"}, {"type": "http", "tag": "proxy", "server": "proxy.example", "server_port": 443, "domain_resolver": {"server": "bootstrap"}}],
		"http_clients": [{"tag": "fetcher", "detour": "proxy"}],
		"dns": {"servers": [{"type": "local", "tag": "bootstrap"}, {"type": "https", "tag": "remote", "server": "dns.example", "domain_resolver": "bootstrap", "detour": "proxy"}],
			"rules": [{"domain_suffix": ["example.com"], "server": "remote"}], "final": "remote", "client_subnet": "192.0.2.1", "reverse_mapping": true},
		"route": {"rules": [{"rule_set": ["custom"], "outbound": "proxy"}], "final": "proxy", "default_http_client": "fetcher",
			"default_domain_resolver": {"server": "bootstrap", "strategy": "ipv4_only"},
			"rule_set": [{"type": "remote", "tag": "custom", "url": "https://example.com/rules.srs", "update_interval": "12h", "http_client": "fetcher"}]}}`, "linux")
	proxy := doc.Outbounds[1]
	bootstrap, remote := dnsID(doc, "bootstrap"), doc.DNS.Servers[1]
	if proxy.Fields["domain_resolver.server"] != bootstrap {
		t.Errorf("proxy resolver %v", proxy.Fields["domain_resolver.server"])
	}
	if remote.DomainResolver.ID() != bootstrap || remote.Detour.ID() != proxy.ID || doc.DNS.Rules[0].Server.ID() != remote.ID {
		t.Errorf("remote %s rules %s", asJSON(t, remote), asJSON(t, doc.DNS.Rules))
	}
	client := doc.HTTPClients[0].ID
	if doc.Route.DefaultHTTPClient.ID() != client || doc.Route.RuleSets[0].HTTPClient.ID() != client {
		t.Errorf("http clients %s", asJSON(t, doc.Route))
	}
	if doc.Route.DefaultDomainResolver.ID() != bootstrap || doc.Route.DefaultDomainStrategy != "ipv4_only" ||
		doc.DNS.ClientSubnet != "192.0.2.1" || !doc.DNS.ReverseMapping {
		t.Errorf("route %s dns %s", asJSON(t, doc.Route), asJSON(t, doc.DNS))
	}
	config := emitted(t, doc, emit.Options{})
	if at(config, "route", "default_http_client") != "fetcher" {
		t.Errorf("route %v", config["route"])
	}
}

func TestImportRejectsLocalRuleSets(t *testing.T) {
	_, err := ImportSingBoxConfig([]byte(`{"outbounds": [{"type": "direct", "tag": "direct"}],
		"route": {"rules": [{"rule_set": ["local-set"], "outbound": "direct"}], "rule_set": [{"type": "local", "tag": "local-set", "path": "rules.srs", "format": "binary"}]}}`), "")
	if err == nil || !regexp.MustCompile(`本地规则集.*暂不支持 URL 导入`).MatchString(err.Error()) {
		t.Errorf("err %v", err)
	}
}

func TestImportMapsInboundDetoursAndRejectsMissingReferences(t *testing.T) {
	doc := mustImport(t, `{"inbounds": [{"type": "mixed", "tag": "entry", "listen": "127.0.0.1", "listen_port": 7890},
		{"type": "socks", "tag": "chain", "listen": "127.0.0.1", "listen_port": 7891, "detour": "entry"}],
		"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "direct"}}`, "")
	if doc.Inbounds[1].Fields["detour"] != doc.Inbounds[0].ID {
		t.Errorf("detour %v", doc.Inbounds[1].Fields["detour"])
	}
	_, err := ImportSingBoxConfig([]byte(`{"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "missing"}}`), "")
	if err == nil || !regexp.MustCompile(`路由兜底出口.*不存在`).MatchString(err.Error()) {
		t.Errorf("err %v", err)
	}
}

func TestImportKeepsGlobalRuleOrder(t *testing.T) {
	raw := `{"outbounds": [{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}],
		"dns": {"servers": [{"type": "local", "tag": "local"}, {"type": "hosts", "tag": "hosts", "predefined": {"example.org": ["192.0.2.1"]}}],
			"rules": [{"domain_suffix": ["example.org"], "server": "hosts"}], "final": "local"},
		"route": {"rules": [{"domain_suffix": ["example.org"], "outbound": "block"}], "final": "direct"}}`
	original := plain(t, raw).(map[string]any)
	doc := mustImport(t, raw, "")
	if doc.Route.LocalEnabled == nil || *doc.Route.LocalEnabled || doc.Route.Rules[0].DeviceID != "other" {
		t.Errorf("route %s", asJSON(t, doc.Route))
	}
	config := emitted(t, doc, emit.Options{LocalAddresses: []string{"192.168.1.5"}})
	if rules := rulesOf(config, "route"); len(rules) < 3 || !reflect.DeepEqual(rules[2], rulesOf(original, "route")[0]) {
		t.Errorf("route rules %v", rules)
	}
	if rules := rulesOf(config, "dns"); !reflect.DeepEqual(rules, rulesOf(original, "dns")) {
		t.Errorf("dns rules %v", rules)
	}
	if !reflect.DeepEqual(at(config, "dns", "servers", 1), at(original, "dns", "servers", 1)) {
		t.Errorf("dns servers %v", at(config, "dns", "servers"))
	}
}

func TestImportKeepsUnconditionalRules(t *testing.T) {
	raw := `{"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"rules": [{"action": "reject"}], "final": "direct"},
		"dns": {"servers": [{"type": "local", "tag": "local"}], "rules": [{"action": "reject"}], "final": "local"}}`
	original := plain(t, raw).(map[string]any)
	config := emitted(t, mustImport(t, raw, ""), emit.Options{})
	if rules := rulesOf(config, "route"); len(rules) < 3 || !reflect.DeepEqual(rules[2], rulesOf(original, "route")[0]) {
		t.Errorf("route rules %v", rules)
	}
	if rules := rulesOf(config, "dns"); !reflect.DeepEqual(rules, rulesOf(original, "dns")) {
		t.Errorf("dns rules %v", rules)
	}
}

func TestImportKeepsNestedInversionAndPreferredBy(t *testing.T) {
	raw := `{"outbounds": [{"type": "direct", "tag": "direct"}], "endpoints": [{"type": "tailscale", "tag": "tailnet", "auth_key": "key"}],
		"dns": {"servers": [{"type": "local", "tag": "local"}, {"type": "https", "tag": "remote", "server": "1.1.1.1"}],
			"rules": [{"preferred_by": ["local"], "server": "remote"}], "final": "local"},
		"route": {"rules": [{"type": "logical", "mode": "or", "invert": true, "rules": [
			{"preferred_by": ["tailnet"]}, {"type": "logical", "mode": "and", "invert": true, "rules": [{"domain_suffix": ["example.org"]}]}
		], "outbound": "direct"}], "final": "direct"}}`
	original := plain(t, raw).(map[string]any)
	config := emitted(t, mustImport(t, raw, ""), emit.Options{})
	if rules := rulesOf(config, "route"); len(rules) < 3 || !reflect.DeepEqual(rules[2], rulesOf(original, "route")[0]) {
		t.Errorf("route rules %v", rules)
	}
	if rules := rulesOf(config, "dns"); !reflect.DeepEqual(rules, rulesOf(original, "dns")) {
		t.Errorf("dns rules %v", rules)
	}
}

func TestImportRejectsUnrepresentableActions(t *testing.T) {
	_, err := ImportSingBoxConfig([]byte(`{"outbounds": [{"type": "direct", "tag": "direct"}],
		"route": {"rules": [{"domain_suffix": ["example.org"], "action": "route-options"}], "final": "direct"}}`), "")
	if err == nil || !regexp.MustCompile(`路由规则 1.*route-options`).MatchString(err.Error()) {
		t.Errorf("route: %v", err)
	}
	_, err = ImportSingBoxConfig([]byte(`{"outbounds": [{"type": "direct", "tag": "direct"}],
		"dns": {"servers": [{"type": "local", "tag": "local"}], "rules": [{"domain_suffix": ["example.org"], "action": "predefined"}]}}`), "")
	if err == nil || !regexp.MustCompile(`DNS 规则 1.*predefined`).MatchString(err.Error()) {
		t.Errorf("dns: %v", err)
	}
}

func TestImportKeepsDoHHeadersAndUSBIPDetour(t *testing.T) {
	doc := mustImport(t, `{"outbounds": [{"type": "direct", "tag": "direct"}],
		"services": [{"type": "usbip-client", "tag": "usb", "server": "192.0.2.1", "server_port": 3240, "detour": "direct"}],
		"dns": {"servers": [{"type": "https", "tag": "doh", "server": "1.1.1.1", "headers": {"Authorization": "Bearer token"}}], "final": "doh"},
		"route": {"final": "direct"}}`, "darwin")
	if doc.Services[0].Fields["detour"] != doc.Outbounds[0].ID {
		t.Errorf("service %s", asJSON(t, doc.Services[0]))
	}
	config := emitted(t, doc, emit.Options{})
	if headers := at(config, "dns", "servers", 0, "headers"); !reflect.DeepEqual(headers, map[string]any{"Authorization": "Bearer token"}) {
		t.Errorf("dns server headers %v", headers)
	}
	if at(config, "services", 0, "detour") != "direct" {
		t.Errorf("services %v", config["services"])
	}
}

func TestImportRejectsInvalidInput(t *testing.T) {
	for raw, want := range map[string]string{
		`not json`:           "不是有效 JSON",
		`[]`:                 "必须是 JSON 对象",
		`{}`:                 "缺少有效的 outbounds",
		`{"outbounds": []}`:  "没有有效的出站",
		`{"outbounds": [1]}`: "包含无效项目",
	} {
		if _, err := ImportSingBoxConfig([]byte(raw), ""); err == nil || !regexp.MustCompile(want).MatchString(err.Error()) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
}

func TestImportRejectsSecondTailscale(t *testing.T) {
	_, err := ImportSingBoxConfig([]byte(`{"outbounds": [{"type": "direct", "tag": "direct"}],
		"endpoints": [{"type": "tailscale", "tag": "a"}, {"type": "tailscale", "tag": "b"}], "route": {"final": "direct"}}`), "")
	if err == nil || !strings.Contains(err.Error(), "多个 Tailscale 端点") {
		t.Errorf("err %v", err)
	}
}

func TestImportKeepsTailscaleUDPPath(t *testing.T) {
	doc := mustImport(t, `{"outbounds": [{"type": "direct", "tag": "direct"}],
		"endpoints": [{"type": "tailscale", "tag": "ts", "detour": "direct", "udp_path": {"default": "detour", "peers": [{"match": ["nas"], "path": "relay"}]}}],
		"route": {"final": "direct"}}`, "")
	fields := doc.Endpoints[0].Fields
	peers, _ := fields["udp_path.peers"].([]any)
	if fields["udp_path.default"] != "detour" || len(peers) != 1 {
		t.Fatalf("fields %v", fields)
	}
}
