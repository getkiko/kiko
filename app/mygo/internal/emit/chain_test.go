package emit_test

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func socks(id, tag, server string, chain ...string) *model.Outbound {
	return &model.Outbound{Entity: model.Entity{ID: id, Tag: tag, Enabled: true}, Kind: "outbound", Type: "socks",
		Fields: model.Fields{"server": server, "server_port": 1080.0}, Chain: chain}
}

// outboundsByTag are the emitted outbounds by tag.
func outboundsByTag(t *testing.T, r emit.Result) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, item := range r.Config["outbounds"].([]any) {
		o := item.(map[string]any)
		out[o["tag"].(string)] = o
	}
	return out
}

// A chain of three hops: the owner's detour is a copy of the last hop,
// whose detour is a copy of the second, whose detour is the first; the
// hops' own outbounds keep their own detours, and copies of a shared
// prefix are emitted once.
func TestEmitProxyChain(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds,
		socks("a", "A", "192.0.2.1"),
		socks("b", "B", "192.0.2.2"),
		socks("c", "C", "192.0.2.3", "a"),
		socks("x", "X", "192.0.2.9", "a", "b", "c"),
		socks("y", "Y", "192.0.2.8", "a", "b"),
		socks("one", "One", "192.0.2.7", "b"),
	)
	r := emit.Emit(d, emit.Options{})
	if len(r.Errors) != 0 || len(r.DependencyErrors) != 0 {
		t.Fatalf("errors %v %v", r.Errors, r.DependencyErrors)
	}
	out := outboundsByTag(t, r)
	if out["X"]["detour"] != "C ⛓1" || out["C ⛓1"]["detour"] != "B ⛓1" || out["B ⛓1"]["detour"] != "A" {
		t.Fatalf("chain of X: %v → %v → %v", out["X"]["detour"], out["C ⛓1"]["detour"], out["B ⛓1"]["detour"])
	}
	if out["Y"]["detour"] != "B ⛓1" {
		t.Fatalf("Y shares the copy of B after A: %v", out["Y"]["detour"])
	}
	if out["One"]["detour"] != "B" || out["B"]["detour"] != nil || out["C"]["detour"] != "A" {
		t.Fatalf("one hop and the hops themselves: %v %v %v", out["One"]["detour"], out["B"]["detour"], out["C"]["detour"])
	}
	if out["C ⛓1"]["server"] != "192.0.2.3" || out["C ⛓1"]["type"] != "socks" {
		t.Fatalf("copy %v", out["C ⛓1"])
	}
	copies := 0
	for tag := range out {
		if strings.Contains(tag, "⛓") {
			copies++
		}
	}
	if copies != 2 {
		t.Fatalf("%d copies, want 2", copies)
	}
	if hop, ok := emit.ChainCopyHop("C ⛓12"); !ok || hop != "C" {
		t.Fatalf("ChainCopyHop %q %v", hop, ok)
	}
	if _, ok := emit.ChainCopyHop("香港 01"); ok {
		t.Fatal("a real tag is not a copy")
	}
}

// A copy's tag never takes a real outbound's: one already named "B ⛓1"
// keeps it and the copy takes the next.
func TestEmitProxyChainCopyTags(t *testing.T) {
	d := doc()
	d.Outbounds = append(d.Outbounds,
		socks("a", "A", "192.0.2.1"), socks("b", "B", "192.0.2.2"),
		socks("taken", "B ⛓1", "192.0.2.5"),
		socks("x", "X", "192.0.2.9", "a", "b"),
	)
	r := emit.Emit(d, emit.Options{})
	out := outboundsByTag(t, r)
	if out["X"]["detour"] != "B ⛓2" || out["B ⛓1"]["server"] != "192.0.2.5" {
		t.Fatalf("X → %v, B ⛓1 %v", out["X"]["detour"], out["B ⛓1"])
	}
	if tags := emit.EmittedTags(d).Outbounds; tags["taken"] != "B ⛓1" {
		t.Fatalf("emitted tags %v", tags)
	}
}

// Hops that cannot stand where they are, and a chain leading back to its
// owner, are errors.
func TestEmitProxyChainErrors(t *testing.T) {
	d := doc()
	off := socks("off", "停用", "192.0.2.4")
	off.Enabled = false
	d.Outbounds = append(d.Outbounds,
		socks("a", "A", "192.0.2.1"), off,
		&model.Outbound{Entity: model.Entity{ID: "g", Tag: "组", Enabled: true}, Kind: "outbound", Type: "selector", Members: []string{"a"}, Fields: model.Fields{}},
		socks("x1", "X1", "192.0.2.9", "a", "off"),
		socks("x2", "X2", "192.0.2.9", "a", "g"),
	)
	r := emit.Emit(d, emit.Options{})
	for _, want := range []string{
		"出站「X1」的代理链第 2 跳「停用」不可用，请启用它或重新选择",
		"出站「X2」的代理链第 2 跳「组」不是可以转发连接的服务器，请重新选择",
	} {
		if !slicesContains(r.Errors, want) {
			t.Errorf("errors %v lack %q", r.Errors, want)
		}
	}

	loop := doc()
	loop.Outbounds = append(loop.Outbounds, socks("a", "A", "192.0.2.1", "x"), socks("b", "B", "192.0.2.2"), socks("x", "X", "192.0.2.9", "a", "b"))
	if r := emit.Emit(loop, emit.Options{}); len(r.DependencyErrors) == 0 {
		t.Fatalf("no cycle error: %v", r.Errors)
	}
}

// A former detour field is a one-hop chain, emitted as the same detour.
func TestEmitLegacyDetour(t *testing.T) {
	d := doc()
	legacy := socks("x", "X", "192.0.2.9")
	legacy.Fields["detour"] = "a"
	d.Outbounds = append(d.Outbounds, socks("a", "A", "192.0.2.1"), legacy)
	r := emit.Emit(d, emit.Options{})
	if out := outboundsByTag(t, r); out["X"]["detour"] != "A" || len(r.Errors) != 0 {
		t.Fatalf("X %v errors %v", out["X"], r.Errors)
	}
}
