package schema_test

import (
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// Ported from the former TypeScript console's schema tests, plus the form
// helpers.

func byKey(fields []schema.Field, key string) (schema.Field, int) {
	i := slices.IndexFunc(fields, func(f schema.Field) bool { return f.Key == key })
	if i < 0 {
		return schema.Field{}, -1
	}
	return fields[i], i
}

func TestTunStackAndDNS(t *testing.T) {
	fields := schema.InboundSchema["tun"]
	stack, _ := byKey(fields, "stack")
	mode, modeIndex := byKey(fields, "dns_mode")
	address, addressIndex := byKey(fields, "dns_address")
	if stack.Def != nil || stack.Options[0] != "" || mode.Def != nil || mode.Options[0] != "" {
		t.Errorf("stack %+v, dns_mode %+v", stack, mode)
	}
	if addressIndex != modeIndex+1 || address.Adv {
		t.Errorf("dns_address at %d (dns_mode %d), adv %v", addressIndex, modeIndex, address.Adv)
	}
}

func TestOutboundDomainResolver(t *testing.T) {
	for _, typ := range []string{"direct", "hysteria", "snell", "tor", "socks", "http", "shadowsocks", "shadowsocksr", "vless", "vmess",
		"trojan", "hysteria2", "tuic", "anytls", "shadowtls", "wireguard", "ssh", "naive"} {
		fields := schema.OutboundSchema[typ]
		resolver, i := byKey(fields, "domain_resolver.server")
		if i < 0 || resolver.Type != schema.RefDNS || resolver.Label != "服务器域名解析" || resolver.Group != "basic" || resolver.Adv || resolver.ShowIf != nil {
			t.Errorf("%s: resolver %+v", typ, resolver)
		}
		for _, key := range []string{"domain_resolver.strategy", "domain_resolver.disable_cache", "domain_resolver.rewrite_ttl",
			"domain_resolver.client_subnet", "domain_resolver.disable_optimistic_cache", "domain_resolver.timeout", "domain_strategy"} {
			if _, i := byKey(fields, key); i >= 0 {
				t.Errorf("%s.%s present", typ, key)
			}
		}
	}
}

func TestDNSDialerSchema(t *testing.T) {
	for _, key := range []string{"detour", "bind_interface", "connect_timeout", "tcp_fast_open", "udp_fragment", "network_strategy",
		"fallback_delay", "domain_resolver.server", "domain_resolver.strategy", "domain_resolver.disable_cache",
		"domain_resolver.rewrite_ttl", "domain_resolver.client_subnet"} {
		f, i := byKey(schema.DNSDialerSchema, key)
		want := "dialer"
		if key == "detour" {
			want = "basic"
		}
		if i < 0 || f.Group != want {
			t.Errorf("%s: group %q", key, f.Group)
		}
	}
	for _, key := range []string{"domain_resolver.server", "domain_resolver.strategy", "domain_resolver.disable_cache"} {
		if f, _ := byKey(schema.DNSDialerSchema, key); f.Hint == "" {
			t.Errorf("%s has no hint", key)
		}
	}
	if _, i := byKey(schema.DNSDialerSchema, "udp_timeout"); i >= 0 {
		t.Error("udp_timeout present")
	}
}

func TestFieldVisibleChain(t *testing.T) {
	fields := schema.OutboundSchema["vless"]
	key, _ := byKey(fields, "tls.reality.public_key")
	values := map[string]any{"tls.enabled": true, "tls.reality.enabled": true}
	if !schema.FieldVisible(key, fields, values) {
		t.Error("REALITY key hidden with TLS and REALITY on")
	}
	values["tls.enabled"] = false
	if schema.FieldVisible(key, fields, values) {
		t.Error("REALITY key shown with TLS off")
	}
}

func TestFormValidation(t *testing.T) {
	fields := schema.OutboundSchema["shadowsocks"]
	errs := schema.FormValidationErrors(fields, map[string]any{"server": "", "server_port": float64(70000), "method": "2022-blake3-aes-128-gcm", "password": "c2hvcnQ="}, "linux")
	messages := map[string]string{}
	for _, e := range errs {
		messages[e.Field.Key] = e.Message
	}
	if messages["server"] != "必填" || messages["server_port"] != "最大值为 65535" || messages["password"] != "Base64 解码后必须是 16 字节，当前为 5 字节" {
		t.Errorf("errors %v", messages)
	}
	if got := schema.FieldValidationError(fields[0], fields, map[string]any{"server": "a"}, "linux", false); got != "" {
		t.Errorf("valid server: %q", got)
	}
}

func TestItemSpecs(t *testing.T) {
	cases := []struct {
		kind  schema.ItemKind
		value string
		ok    bool
	}{
		{schema.ItemCIDR, "192.168.0.0/16", true}, {schema.ItemCIDR, "fd00::/8", true}, {schema.ItemCIDR, "1.2.3.4/33", false},
		{schema.ItemIP, "::1", true}, {schema.ItemIP, "1.2.3", false}, {schema.ItemUIDRange, "10:2", false},
		{schema.ItemUIDRange, "0:999", true}, {schema.ItemPackage, "com.example.app", true}, {schema.ItemPackage, "example", false},
		{schema.ItemRuleSet, "geoip-cn", true}, {schema.ItemRuleSet, "geo ip", false}, {schema.ItemUID, "1000", true},
	}
	for _, c := range cases {
		if got := schema.ItemSpecs[c.kind].Validate(c.value) == ""; got != c.ok {
			t.Errorf("%s %q: valid %v", c.kind, c.value, got)
		}
	}
}

func TestServices(t *testing.T) {
	if schema.ServiceAvailable("usbip-server", "darwin") || !schema.ServiceAvailable("usbip-client", "darwin") {
		t.Error("availability")
	}
	if got := schema.ServiceUnavailableReason("usbip-client", "android"); got != "Android 不支持 USB/IP Client" {
		t.Errorf("reason %q", got)
	}
	if got := schema.UsbDevicesError("usbip-server", map[string]any{"provider": "default"}); got != "default 模式至少需要一个要导出的设备" {
		t.Errorf("devices error %q", got)
	}
	if got := schema.EndpointAddress("openvpn-server", map[string]any{"listen": "::", "listen_port": float64(1194)}); got != "[::]:1194" {
		t.Errorf("address %q", got)
	}
	if !schema.HasServer("vless") || schema.HasServer("selector") {
		t.Error("HasServer")
	}
}

func TestRequiredFields(t *testing.T) {
	for typ, keys := range map[string][]string{"vless": {"server", "server_port", "uuid"}, "burrow": {"signal_address", "room"}} {
		for _, key := range keys {
			if f, _ := byKey(schema.OutboundSchema[typ], key); !f.Required {
				t.Errorf("%s.%s not required", typ, key)
			}
		}
	}
	if f, _ := byKey(schema.ServiceSchema["usbip-server"], "listen_port"); !f.Required {
		t.Error("usbip-server listen_port not required")
	}
}

func TestIPChecks(t *testing.T) {
	for _, s := range []string{"::", "::1", "2001:db8::1", "1:2:3:4:5:6:7:8", "::ffff:1.2.3.4", "1:2:3:4:5:6:1.2.3.4", "fe80::"} {
		if !schema.IsIPv6(s) {
			t.Errorf("IsIPv6(%q) = false", s)
		}
	}
	for _, s := range []string{"abc:def", "1:2", ":::", "1:2:3:4:5:6:7:8:9", "1.2.3.4::", ":1::2", "1::2::3"} {
		if schema.IsIPv6(s) || schema.IsIPAddress(s) {
			t.Errorf("%q accepted as IPv6", s)
		}
	}
	for _, s := range []string{"01.2.3.4", "1.2.3.004", "256.1.1.1"} {
		if schema.IsIPv4(s) || schema.IsIPAddress(s) {
			t.Errorf("%q accepted as IPv4", s)
		}
	}
	if !schema.IsIPv4("0.0.0.0") || !schema.IsCIDR("10.0.0.0/8") || schema.IsCIDR("abc:def/64") || schema.IsCIDR("010.0.0.0/8") {
		t.Error("IPv4 / CIDR")
	}
	if spec := schema.ItemSpecs[schema.ItemIP]; spec.Validate("abc:def") == "" {
		t.Error("IP item accepts abc:def")
	}
}

func TestNumberAndDurationValidation(t *testing.T) {
	port := schema.Field{Key: "server_port", Type: schema.Number}
	timeout := schema.Field{Key: "connect_timeout", Type: schema.Duration}
	table := []schema.Field{port, timeout}
	check := func(f schema.Field, v any) string {
		return schema.FieldValidationError(f, table, map[string]any{f.Key: v}, "linux", true)
	}
	if got := check(port, 1.5); got != "请输入整数" {
		t.Errorf("1.5: %q", got)
	}
	if got := check(port, "443"); got != "" {
		t.Errorf("443: %q", got)
	}
	for _, v := range []string{"30s", "1h30m", "500ms", "1.5h", "7d", "0", "2µs"} {
		if got := check(timeout, v); got != "" {
			t.Errorf("%s: %q", v, got)
		}
	}
	for _, v := range []any{"30", "5 s", "abc", "s", 30.0} {
		if got := check(timeout, v); got == "" {
			t.Errorf("%v accepted", v)
		}
	}
}

// Visibility follows the slice as it is now, also for slices callers
// build and change: they are not cached.
func TestFieldVisibleBuiltSlice(t *testing.T) {
	fields := []schema.Field{
		{Key: "a", Type: schema.Bool},
		{Key: "b", Type: schema.Text, ShowIf: &schema.ShowIf{Key: "a", Truthy: true}},
	}
	values := map[string]any{"a": false, "c": true}
	if schema.FieldVisible(fields[1], fields, values) {
		t.Fatal("b visible while a is off")
	}
	fields[0].Key = "c"
	fields[1].ShowIf = &schema.ShowIf{Key: "c", Truthy: true}
	if !schema.FieldVisible(fields[1], fields, values) {
		t.Error("stale index")
	}
	// The same through an enabled switch along the dotted path.
	tls := []schema.Field{{Key: "tls.enabled", Type: schema.Bool}, {Key: "tls.server_name", Type: schema.Text}}
	if schema.FieldVisible(tls[1], tls, map[string]any{}) {
		t.Fatal("server_name visible while TLS is off")
	}
	tls[0].Key = "other"
	if !schema.FieldVisible(tls[1], tls, map[string]any{}) {
		t.Error("stale parent")
	}
}
