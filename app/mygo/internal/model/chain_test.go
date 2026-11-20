package model_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func chainDoc() *model.Doc {
	d := model.EmptyDoc("darwin")
	server := func(id, typ string, chain ...string) *model.Outbound {
		return &model.Outbound{Entity: model.Entity{ID: id, Tag: strings.ToUpper(id), Enabled: true}, Kind: "outbound", Type: typ,
			Fields: model.Fields{"server": "192.0.2.1", "server_port": 1080.0}, Chain: chain}
	}
	d.Outbounds = []*model.Outbound{
		server("direct", "direct"), server("a", "socks"), server("b", "vless"), server("c", "shadowsocks"),
		server("x", "socks"), server("h", "http"),
		{Entity: model.Entity{ID: "g", Tag: "G", Enabled: true}, Kind: "outbound", Type: "selector", Members: []string{"a", "b"}, Fields: model.Fields{}},
	}
	d.Endpoints = []*model.Endpoint{{Entity: model.Entity{ID: "wg", Tag: "WG", Enabled: true}, Kind: "endpoint", Type: "wireguard", Fields: model.Fields{}}}
	return d
}

func TestMigrateProxyChains(t *testing.T) {
	d := chainDoc()
	d.Outbounds[1].Fields["detour"] = "b"
	d.Outbounds[0].Fields["detour"] = "a" // direct dials too, through a chain
	d.Endpoints[0].Fields["detour"] = "c"
	d.Subscriptions = []*model.Subscription{{ID: "s", Tag: "S", Enabled: true, Detour: model.Some("a")}}
	got := model.MigrateProxyChains(d)
	if got == d {
		t.Fatal("a document with detours is copied")
	}
	if !slices.Equal(got.Outbounds[1].Chain, []string{"b"}) || got.Outbounds[1].Fields["detour"] != nil {
		t.Fatalf("outbound %v %v", got.Outbounds[1].Chain, got.Outbounds[1].Fields)
	}
	if !slices.Equal(got.Outbounds[0].Chain, []string{"a"}) || got.Outbounds[0].Fields["detour"] != nil {
		t.Fatalf("direct %v %v", got.Outbounds[0].Chain, got.Outbounds[0].Fields)
	}
	// A group takes no chain: a stray detour on one goes.
	g := chainDoc()
	g.Outbounds[6].Fields["detour"] = "a"
	if got := model.MigrateProxyChains(g); got.Outbounds[6].Chain != nil || got.Outbounds[6].Fields["detour"] != nil {
		t.Fatalf("group %v %v", got.Outbounds[6].Chain, got.Outbounds[6].Fields)
	}
	if !slices.Equal(got.Endpoints[0].Chain, []string{"c"}) || !slices.Equal(got.Subscriptions[0].Chain, []string{"a"}) || got.Subscriptions[0].Detour.Set() {
		t.Fatalf("endpoint %v subscription %v %v", got.Endpoints[0].Chain, got.Subscriptions[0].Chain, got.Subscriptions[0].Detour)
	}
	if d.Outbounds[1].Fields["detour"] != "b" {
		t.Fatal("the input changed")
	}
	if again := model.MigrateProxyChains(got); again != got {
		t.Fatal("a migrated document is returned as it is")
	}
	// A chain already set wins over a stray detour.
	d2 := chainDoc()
	d2.Outbounds[4].Chain = []string{"a", "b"}
	d2.Outbounds[4].Fields["detour"] = "c"
	if got := model.MigrateProxyChains(d2); !slices.Equal(got.Outbounds[4].Chain, []string{"a", "b"}) {
		t.Fatalf("chain %v", got.Outbounds[4].Chain)
	}
}

func TestChainError(t *testing.T) {
	d := chainDoc()
	cases := []struct {
		chain []string
		hop   int
		want  string // "" when the chain is fine
	}{
		{[]string{"a", "b", "c"}, 0, ""},
		{[]string{"g", "b"}, 0, ""},
		{[]string{"wg", "a"}, 0, ""},
		{[]string{"x"}, 0, "不能把本出站加入自己的代理链"},
		{[]string{"a", "a"}, 1, "在链中重复出现"},
		{[]string{"a", "g"}, 1, "第 2 跳起只能选择具体的服务器，「G」是策略组"},
		{[]string{"a", "wg"}, 1, "是端点"},
		{[]string{"a", "direct"}, 1, "不能转发连接"},
		{[]string{"direct"}, 0, "不能作为代理链的一跳"},
		{[]string{"gone"}, 0, "第 1 跳已删除"},
	}
	for _, c := range cases {
		issue, ok := model.ChainError(d, []string{"x"}, c.chain)
		if c.want == "" {
			if !ok {
				t.Errorf("%v: %+v", c.chain, issue)
			}
			continue
		}
		if ok || issue.Hop != c.hop || !strings.Contains(issue.Message, c.want) {
			t.Errorf("%v: %+v, want hop %d %q", c.chain, issue, c.hop, c.want)
		}
	}

	// The first hop is used as it is, so its own chain and group members
	// count; a later hop is a copy without its chain.
	d.Outbounds[1].Chain = []string{"x"} // a goes through x
	if issue, ok := model.ChainError(d, []string{"x"}, []string{"a", "b"}); ok || issue.Hop != -1 || issue.Message != "循环依赖：X → A → X，请调整代理链" {
		t.Fatalf("cycle through the first hop: %+v", issue)
	}
	if issue, ok := model.ChainError(d, []string{"x"}, []string{"g"}); ok || !strings.Contains(issue.Message, "X → G → A → X") {
		t.Fatalf("cycle through a group member: %+v", issue)
	}
	if issue, ok := model.ChainError(d, []string{"x"}, []string{"b", "a"}); !ok {
		t.Fatalf("a later hop's own chain does not count: %+v", issue)
	}

	if got := model.ChainWithoutUDP(d, []string{"a", "h"}); !slices.Equal(got, []string{"H"}) {
		t.Fatalf("without UDP %v", got)
	}
}
