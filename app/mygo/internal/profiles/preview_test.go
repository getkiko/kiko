package profiles

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestRedactHidesUpstreamsAndCredentials(t *testing.T) {
	config := `{
	"outbounds": [
		{"type": "vless", "tag": "proxy-us", "server": "proxy.example.com", "server_port": 443,
		 "uuid": "11111111-1111-1111-1111-111111111111", "tls": {"server_name": "proxy.example.com",
		 "reality": {"public_key": "public-material", "private_key": "private-material", "short_id": "short-secret"}}},
		{"type": "vmess", "tag": "fallback", "server": "10.0.0.2", "server_port": 8443,
		 "users": [{"name": "alice", "uuid": "22222222-2222-2222-2222-222222222222", "password": "vmess-password"}]}
	],
	"inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		{"type": "tun", "address": ["172.19.0.1/30"], "route_exclude_address": ["100.64.0.0/10"]}],
	"route": {
		"final": "fallback",
		"rules": [{"action": "route", "source_ip_cidr": ["192.168.31.42/32"], "domain_suffix": ["example.com"], "outbound": "fallback"}],
		"rule_set": [{"type": "inline", "tag": "kiko-local-source", "rules": [{"source_ip_cidr": ["192.168.31.42/32"]}]}]
	},
	"dns": {"servers": [{"type": "https", "tag": "dns-cn", "server": "223.5.5.5", "server_port": 443,
		"path": "/dns-query", "tls": {"server_name": "dns.example.com"}, "headers": {"Authorization": "Bearer secret-token"}}],
		"rules": [{"rule_set": ["kiko-local-source"], "server": "dns-cn"}], "final": "dns-cn"},
	"endpoints": [{"type": "wireguard", "tag": "wg", "private_key": "wg-private", "peers": [{"address": "198.51.100.9",
		"port": 51820, "public_key": "wg-public", "pre_shared_key": "wg-psk", "allowed_ips": ["10.0.0.0/8"]}]}],
	"experimental": {"clash_api": {"external_controller": "127.0.0.1:9090", "secret": "api-secret"}}
}`
	shared, err := RedactConfigForCopy(config)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(shared), &r); err != nil {
		t.Fatal(err)
	}
	get := func(path ...any) any {
		var v any = r
		for _, p := range path {
			switch k := p.(type) {
			case string:
				v = v.(map[string]any)[k]
			case int:
				v = v.([]any)[k]
			}
		}
		return v
	}
	checks := []struct {
		path []any
		want any
	}{
		{[]any{"outbounds", 0, "type"}, "vless"},
		{[]any{"route", "rules", 0, "action"}, "route"},
		{[]any{"outbounds", 0, "server_port"}, 0.0},
		{[]any{"outbounds", 1, "server_port"}, 0.0},
		{[]any{"dns", "servers", 0, "server_port"}, 0.0},
		{[]any{"endpoints", 0, "peers", 0, "port"}, 0.0},
		{[]any{"outbounds", 0, "tag"}, "proxy-us"},
		{[]any{"route", "final"}, "fallback"},
		{[]any{"dns", "servers", 0, "tag"}, "dns-cn"},
		{[]any{"dns", "rules", 0, "server"}, "dns-cn"},
		{[]any{"route", "rules", 0, "source_ip_cidr"}, []any{"192.168.31.42/32"}},
		{[]any{"route", "rules", 0, "domain_suffix"}, []any{"example.com"}},
		{[]any{"route", "rule_set", 0, "rules", 0, "source_ip_cidr"}, []any{"192.168.31.42/32"}},
		{[]any{"inbounds", 1, "address"}, []any{"172.19.0.1/30"}},
		{[]any{"inbounds", 1, "route_exclude_address"}, []any{"100.64.0.0/10"}},
		{[]any{"inbounds", 0, "listen"}, "127.0.0.1"},
		{[]any{"inbounds", 0, "listen_port"}, 2080.0},
		{[]any{"experimental", "clash_api", "external_controller"}, "127.0.0.1:9090"},
		{[]any{"endpoints", 0, "peers", 0, "allowed_ips"}, []any{"10.0.0.0/8"}},
	}
	for _, c := range checks {
		if got := get(c.path...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v = %#v, want %#v", c.path, got, c.want)
		}
	}
	if !regexp.MustCompile(`^<已屏蔽 \d+>$`).MatchString(get("endpoints", 0, "peers", 0, "address").(string)) {
		t.Errorf("peer address not masked: %v", get("endpoints", 0, "peers", 0, "address"))
	}
	for _, secret := range []string{"proxy.example.com", "10.0.0.2", "dns.example.com", "443", "8443",
		"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222",
		"public-material", "private-material", "short-secret", "vmess-password", "alice", "Bearer secret-token",
		"api-secret", "198.51.100.9", "51820", "wg-private", "wg-public", "wg-psk"} {
		if strings.Contains(shared, secret) {
			t.Errorf("leaked %s", secret)
		}
	}
	// Keys keep their order.
	if !strings.HasPrefix(shared, "{\n  \"outbounds\": [\n    {\n      \"type\": \"vless\",") || !strings.HasSuffix(shared, "}\n") {
		t.Errorf("order or format changed:\n%s", shared[:80])
	}
}

func TestRedactMasksOnlyCredentialFieldsAndServerPorts(t *testing.T) {
	out, err := RedactConfigForCopy(`{"outbounds": [{"type": "shadowsocks", "server": "host.example",
		"server_ports": [443, "8443"], "password": "secret", "pin": 123456, "private_key": "private", "custom_plugin_value": "keep-me"}],
		"route": {"port_range": ["1000:2000"], "hosts": {"private.example.com": "192.0.2.1"}},
		"enabled": true, "retries": 3, "type": "custom-type", "method": "custom-method"}`)
	if err != nil {
		t.Fatal(err)
	}
	var shared map[string]any
	_ = json.Unmarshal([]byte(out), &shared)
	o := shared["outbounds"].([]any)[0].(map[string]any)
	want := map[string]any{"server_ports": []any{0.0, 0.0}, "server": "<已屏蔽 1>", "password": "<已屏蔽 2>",
		"private_key": "<已屏蔽 3>", "custom_plugin_value": "keep-me", "pin": 0.0}
	for k, v := range want {
		if !reflect.DeepEqual(o[k], v) {
			t.Errorf("%s = %#v, want %#v", k, o[k], v)
		}
	}
	route := shared["route"].(map[string]any)
	if !reflect.DeepEqual(route["port_range"], []any{"1000:2000"}) || !reflect.DeepEqual(route["hosts"], map[string]any{"private.example.com": "192.0.2.1"}) {
		t.Errorf("route changed: %v", route)
	}
	if shared["enabled"] != true || shared["retries"] != 3.0 || shared["type"] != "custom-type" || shared["method"] != "custom-method" {
		t.Errorf("top level changed: %v", shared)
	}
	if _, err := RedactConfigForCopy("invalid JSON"); err == nil {
		t.Error("invalid JSON redacted")
	}
}

func TestRedactNeverReusesExistingText(t *testing.T) {
	redacted, err := RedactConfigForCopy(`{"outbounds": [{"type": "vless", "password": "<已屏蔽 1>", "server": "proxy.example.com"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, "<已屏蔽 1>") || strings.Contains(redacted, "proxy.example.com") {
		t.Errorf("redacted %s", redacted)
	}
}

func TestFindConfigMatches(t *testing.T) {
	cases := []struct {
		text, query string
		want        []int
	}{
		{"Apple.com apple.com APPLE.COM", "apple.com", []int{0, 10, 20}},
		{"a.b axb a.b", "a.b", []int{0, 8}},
		{"abc", "", nil},
		{"abc", "missing", nil},
	}
	for _, c := range cases {
		if got := FindConfigMatches(c.text, c.query); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FindConfigMatches(%q, %q) = %v, want %v", c.text, c.query, got, c.want)
		}
	}
}
