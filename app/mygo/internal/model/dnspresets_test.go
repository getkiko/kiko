package model_test

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestDNSPresetAddress(t *testing.T) {
	p := model.DNSPresets[0]
	if p.Address() != "tls://dns.alidns.com" {
		t.Fatal(p.Address())
	}
	v6 := model.DNSPreset{Vendor: "x", Tag: "t", Type: "udp", Server: "2400:3200::1"}
	if v6.Address() != "udp://[2400:3200::1]" {
		t.Fatal(v6.Address())
	}
	s := model.DNSPresetServer(p, map[string]bool{"dns-ali-tls": true, "dns-ali-tls-2": true}, false)
	if s.Tag != "dns-ali-tls-3" || s.Enabled || s.Fields["tls"].(map[string]any)["server_name"] != "dns.alidns.com" {
		t.Fatalf("%+v", s)
	}
}

func TestAddDNSPresets(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	next := model.AddDNSPresets(doc)
	// EmptyDoc's dns-cn is AliDNS's DoH on 223.5.5.5.
	if len(next.DNS.Servers) != 2+len(model.DNSPresets)-1 || next.DNS.PresetsVersion != model.DNSPresetsVersion {
		t.Fatalf("%d servers, version %d", len(next.DNS.Servers), next.DNS.PresetsVersion)
	}
	if len(doc.DNS.Servers) != 2 {
		t.Fatal("doc modified")
	}
	if !next.DNS.Servers[0].Enabled || !next.DNS.Servers[1].Enabled {
		t.Fatal("default servers disabled")
	}
	for _, s := range next.DNS.Servers[2:] {
		if s.Enabled || s.Tag == "dns-ali-https" {
			t.Fatalf("preset %+v", s)
		}
	}
	if again := model.AddDNSPresets(next); again != next {
		t.Fatal("added twice")
	}
	// A preset deleted after the migration stays deleted.
	next.DNS.Servers = next.DNS.Servers[:2]
	if again := model.AddDNSPresets(next); len(again.DNS.Servers) != 2 {
		t.Fatal("deleted presets came back")
	}
}
