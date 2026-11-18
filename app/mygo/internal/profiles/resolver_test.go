package profiles

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestImportDomainResolverForms(t *testing.T) {
	doc := mustImport(t, `{
		"dns": {"servers": [
			{"type": "local", "tag": "local"},
			{"type": "https", "tag": "doh", "server": "dns.google",
				"domain_resolver": {"server": "local", "strategy": "ipv4_only", "disable_cache": true}}
		]},
		"endpoints": [{"type": "wireguard", "tag": "wg", "address": ["10.0.0.2/32"], "private_key": "k",
			"domain_resolver": "local",
			"peers": [{"address": "wg.example", "port": 51820, "public_key": "p", "allowed_ips": ["0.0.0.0/0"]}]}],
		"outbounds": [
			{"type": "direct", "tag": "direct"},
			{"type": "trojan", "tag": "t", "server": "t.example", "server_port": 443, "password": "p", "domain_resolver": "doh"}
		]}`, "linux")
	local, doh := doc.DNS.Servers[0], doc.DNS.Servers[1]
	if got := doc.Outbounds[1].Fields["domain_resolver.server"]; got != doh.ID {
		t.Errorf("outbound resolver %v, want %s", got, doh.ID)
	}
	if got := doc.Endpoints[0].Fields["domain_resolver.server"]; got != local.ID {
		t.Errorf("endpoint resolver %v, want %s", got, local.ID)
	}
	if doh.DomainResolver.ID() != local.ID || doh.DomainStrategy != "ipv4_only" || doh.Fields["domain_resolver_disable_cache"] != true {
		t.Errorf("DNS resolver %s", asJSON(t, doh))
	}
	if _, err := ImportSingBoxConfig([]byte(`{"outbounds": [{"type": "direct", "tag": "direct"}],
		"endpoints": [{"type": "wireguard", "tag": "wg", "domain_resolver": "missing"}]}`), "linux"); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Errorf("missing endpoint resolver: %v", err)
	}
}

// A document saved before ipv6Enabled existed infers it from its
// inbounds, as fillDefaults does.
func TestParseImportInfersIPv6(t *testing.T) {
	doc := model.EmptyDoc("linux")
	in := model.MakeInbound("mixed", "mixed-in", "linux", true)
	in.Fields["listen"] = "::"
	doc.Inbounds = []*model.Inbound{in}
	raw := asJSON(t, &model.Profile{ID: "p", Name: "old", Doc: doc})
	raw = strings.Replace(raw, `"ipv6Enabled":false,`, "", 1)
	if strings.Contains(raw, "ipv6Enabled") {
		t.Fatalf("fixture still has ipv6Enabled: %s", raw)
	}
	profiles, _, err := ParseImport([]byte(`[`+raw+`]`), "linux")
	if err != nil || len(profiles) != 1 {
		t.Fatalf("%v %v", profiles, err)
	}
	if !profiles[0].Doc.IPv6Enabled {
		t.Error("ipv6Enabled was not inferred from the :: listener")
	}
	filled := model.FillDefaults(model.Clone(profiles[0].Doc))
	if filled.Inbounds[0].Fields["listen"] != "::" {
		t.Errorf("listener moved to %v", filled.Inbounds[0].Fields["listen"])
	}
}

func TestNormalizeConfigURLRejectsOpaque(t *testing.T) {
	if _, err := NormalizeConfigURL("https:example.com/config.json"); err == nil {
		t.Error("an opaque URL was accepted")
	}
}

func TestImportSingBoxInfersIPv6(t *testing.T) {
	for _, config := range []string{
		`{"inbounds": [{"type": "mixed", "tag": "m", "listen": "::", "listen_port": 7890}], "outbounds": [{"type": "direct", "tag": "d"}]}`,
		`{"inbounds": [{"type": "tun", "tag": "t", "address": ["172.19.0.1/30", "fdfe:dcba:9876::1/126"]}], "outbounds": [{"type": "direct", "tag": "d"}]}`,
		`{"dns": {"strategy": "prefer_ipv6", "servers": [{"type": "local", "tag": "l"}]}, "outbounds": [{"type": "direct", "tag": "d"}]}`,
	} {
		doc := mustImport(t, config, "linux")
		if !doc.IPv6Enabled {
			t.Errorf("%s: ipv6Enabled off", config)
		}
		filled := model.FillDefaults(doc)
		if len(filled.Inbounds) > 0 && filled.Inbounds[0].Fields["listen"] == "0.0.0.0" {
			t.Errorf("%s: listener moved to IPv4", config)
		}
		if strings.Contains(config, "prefer_ipv6") && filled.DNS.Strategy != "prefer_ipv6" {
			t.Errorf("strategy %q", filled.DNS.Strategy)
		}
	}
	if doc := mustImport(t, `{"inbounds": [{"type": "mixed", "tag": "m", "listen": "127.0.0.1"}], "outbounds": [{"type": "direct", "tag": "d"}]}`, "linux"); doc.IPv6Enabled {
		t.Error("an IPv4 configuration enabled IPv6")
	}
}

func TestParseImportKeepsMistypedProfiles(t *testing.T) {
	doc := asJSON(t, model.EmptyDoc("linux"))
	profiles, _, err := ParseImport([]byte(`[{"id": "p", "name": 7, "savedAt": 1.5, "doc": `+doc+`}]`), "linux")
	if err != nil || len(profiles) != 1 || profiles[0].Doc == nil || profiles[0].ID != "p" {
		t.Errorf("profiles %v %v", profiles, err)
	}
}

func TestRedactMasksTransportHosts(t *testing.T) {
	out, err := RedactConfigForCopy(`{"outbounds": [
		{"type": "vmess", "transport": {"type": "ws", "headers": {"Host": "cdn.secret.example"}}},
		{"type": "vmess", "transport": {"type": "http", "host": ["h2.secret.example"]}},
		{"type": "vless", "transport": {"type": "httpupgrade", "host": "hu.secret.example"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secret.example") {
		t.Errorf("hosts kept:\n%s", out)
	}
}
