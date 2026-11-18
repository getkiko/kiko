package main

import (
	"reflect"
	"testing"
)

func TestMitmProtectPorts(t *testing.T) {
	s := &server{listenAddr: "127.0.0.1:18080"}
	s.mitm.protectedPortsProbeOnce.Do(func() {})
	s.mitm.protectedPortsSupported = true

	section := map[string]any{"http_rewrite": map[string]any{"url": []any{}}}
	s.mitmProtectPorts(section)
	got := section["http_rewrite"].(map[string]any)["protected_ports"]
	if !reflect.DeepEqual(got, []int{18080}) {
		t.Fatalf("protected_ports = %v", got)
	}

	// Without rewrite rules there is nothing to protect against.
	section = map[string]any{"enabled": true}
	s.mitmProtectPorts(section)
	if _, ok := section["http_rewrite"]; ok {
		t.Fatal("http_rewrite appeared")
	}

	// A core that does not know the key must not get it.
	old := &server{listenAddr: "127.0.0.1:18080"}
	old.mitm.protectedPortsProbeOnce.Do(func() {})
	section = map[string]any{"http_rewrite": map[string]any{"url": []any{}}}
	old.mitmProtectPorts(section)
	if _, ok := section["http_rewrite"].(map[string]any)["protected_ports"]; ok {
		t.Fatal("key sent to an old core")
	}

	// An unparseable listen address adds nothing.
	odd := &server{listenAddr: "garbage"}
	odd.mitm.protectedPortsProbeOnce.Do(func() {})
	odd.mitm.protectedPortsSupported = true
	section = map[string]any{"http_rewrite": map[string]any{}}
	odd.mitmProtectPorts(section)
	if _, ok := section["http_rewrite"].(map[string]any)["protected_ports"]; ok {
		t.Fatal("port from nothing")
	}
}

func TestMitmProtectPortsIncludesSharePage(t *testing.T) {
	s := &server{listenAddr: "127.0.0.1:18080"}
	s.mitm.share.cur = &mitmShareSession{port: 45123}
	got := s.appListenPorts()
	if !reflect.DeepEqual(got, []int{18080, 45123}) {
		t.Fatalf("ports = %v", got)
	}
	s.mitm.share.cur = nil
	if got = s.appListenPorts(); !reflect.DeepEqual(got, []int{18080}) {
		t.Fatalf("ports after stop = %v", got)
	}
}
