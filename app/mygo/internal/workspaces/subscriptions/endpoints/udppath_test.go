package endpoints

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestUDPPathRulesRoundTrip(t *testing.T) {
	rules := udpPathRules([]any{
		map[string]any{"match": []any{"macbook", " ", "*-home"}, "path": "direct"},
		map[string]any{"match": "nas", "path": "relay"},
	})
	if len(rules) != 2 || !slices.Equal(rules[0].match, []string{"macbook", "*-home"}) || rules[1].match[0] != "nas" || rules[1].path != "relay" {
		t.Fatalf("rules %+v", rules)
	}
	again := udpPathRules(udpPathRulesValue(rules))
	if len(again) != 2 || !slices.Equal(again[0].match, rules[0].match) || again[1].path != "relay" {
		t.Fatalf("round trip %+v", again)
	}
}

func TestUDPPathPattern(t *testing.T) {
	if pattern, problem := udpPathPattern(" nas.tail1234.ts.net. "); pattern != "nas.tail1234.ts.net" || problem != "" {
		t.Fatalf("pattern %q %q", pattern, problem)
	}
	if _, problem := udpPathPattern("[a-"); problem == "" {
		t.Fatal("bad wildcard must be rejected")
	}
}

func TestUDPPathPeerSuggestions(t *testing.T) {
	status := &api.TailscaleStatus{UserGroups: []api.TailscaleUserGroup{{Peers: []api.TailscalePeer{
		{DNSName: "macbook.tail1234.ts.net.", HostName: "MacBook"},
		{DNSName: "nas.tail1234.ts.net."},
	}}}}
	got := udpPathPeerSuggestions(status, "tail", []string{"NAS"})
	if len(got) != 1 || got[0].ID != "macbook" || got[0].Detail != "macbook.tail1234.ts.net" {
		t.Fatalf("suggestions %+v", got)
	}
	if udpPathPeerSuggestions(nil, "", nil) != nil {
		t.Fatal("no status, no suggestions")
	}
}

func TestUDPPathIssues(t *testing.T) {
	doc := &model.Doc{Outbounds: []*model.Outbound{
		{Entity: model.Entity{ID: "vm", Tag: "vm"}, Type: "vmess", Fields: model.Fields{}},
		{Entity: model.Entity{ID: "vmx", Tag: "vmx"}, Type: "vmess", Fields: model.Fields{"packet_encoding": "xudp"}},
		{Entity: model.Entity{ID: "ss", Tag: "ss"}, Type: "shadowsocks", Fields: model.Fields{}},
		{Entity: model.Entity{ID: "auto", Tag: "auto"}, Type: "urltest", Members: []string{"vmx", "vm"}},
		{Entity: model.Entity{ID: "pick", Tag: "pick"}, Type: "selector", Members: []string{"auto", "ss"}},
		{Entity: model.Entity{ID: "safe", Tag: "safe"}, Type: "selector", Members: []string{"vmx", "ss"}},
	}}
	has := func(notes []string, part string) bool {
		return slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, part) })
	}
	if problem, notes := udpPathIssues(doc, model.Fields{}, nil); problem != "" || len(notes) != 0 {
		t.Fatalf("unset: %q %v", problem, notes)
	}
	if problem, _ := udpPathIssues(doc, model.Fields{"udp_path.default": "detour"}, nil); problem == "" {
		t.Fatal("detour without 前置代理 must be an error")
	}
	if problem, _ := udpPathIssues(doc, model.Fields{"udp_path.peers": []any{map[string]any{"match": []any{"nas"}, "path": "detour"}}}, nil); problem == "" {
		t.Fatal("a detour rule without 前置代理 must be an error")
	}
	if problem, notes := udpPathIssues(doc, model.Fields{"udp_path.default": "detour"}, []string{"vm"}); problem != "" || !has(notes, "「vm」按目的地绑定 UDP") {
		t.Fatalf("vmess: %q %v", problem, notes)
	}
	if problem, notes := udpPathIssues(doc, model.Fields{"udp_path.default": "detour"}, []string{"pick"}); problem != "" || len(notes) != 1 || !has(notes, "「vm」按目的地绑定 UDP") {
		t.Fatalf("group member: %q %v", problem, notes)
	}
	if problem, _ := udpPathIssues(doc, model.Fields{"udp_path.peers": []any{map[string]any{"match": []any{" "}, "path": "detour"}}}, nil); problem != "" {
		t.Fatal("a rule without a peer name is ignored")
	}
	for _, detour := range []string{"vmx", "ss", "safe"} {
		if problem, notes := udpPathIssues(doc, model.Fields{"udp_path.default": "detour"}, []string{detour}); problem != "" || len(notes) != 0 {
			t.Fatalf("%s: %q %v", detour, problem, notes)
		}
	}
	if _, notes := udpPathIssues(doc, model.Fields{"udp_path.default": "detour", "disable_udp_direct": true}, []string{"ss"}); !has(notes, "禁用 UDP 直连") {
		t.Fatalf("disable_udp_direct: %v", notes)
	}
	if _, notes := udpPathIssues(doc, model.Fields{"udp_path.default": "relay"}, nil); !has(notes, "不生效") {
		t.Fatalf("without detour path: %v", notes)
	}
}

func TestAddsTailscale(t *testing.T) {
	empty := guideDoc("linux")
	ts := tsEndpoint("ts", "ts")
	if addsTailscale(empty, nil, ts) {
		t.Fatal("the first Tailscale endpoint is allowed")
	}
	if !addsTailscale(empty, []*model.Endpoint{tsEndpoint("p", "p")}, ts) {
		t.Fatal("a pending and a new Tailscale endpoint are two")
	}
	doc := tailscaleGuideDoc()
	if addsTailscale(doc, nil, doc.Endpoints[0]) {
		t.Fatal("editing the existing endpoint is allowed")
	}
	if !addsTailscale(doc, nil, tsEndpoint("new", "new")) {
		t.Fatal("a second Tailscale endpoint is refused")
	}
	disabled := tailscaleGuideDoc()
	disabled.Endpoints[0].Enabled = false
	plan := planUsbIP(disabled, "client", pathTailscale)
	if plan.Endpoint != nil || plan.Service.Fields["detour"] != "ts" {
		t.Fatalf("a disabled Tailscale endpoint is reused: %+v", plan.Service.Fields)
	}
}

func TestPruneUDPPathRules(t *testing.T) {
	fields := model.Fields{udpPathPeersKey: []any{
		map[string]any{"match": []any{}, "path": "detour"},
		map[string]any{"match": []any{"nas"}, "path": "relay"},
	}}
	pruneUDPPathRules(fields)
	if rules := udpPathRules(fields[udpPathPeersKey]); len(rules) != 1 || rules[0].match[0] != "nas" {
		t.Fatalf("pruned %+v", rules)
	}
	fields = model.Fields{udpPathPeersKey: []any{map[string]any{"match": []any{" "}, "path": "direct"}}}
	pruneUDPPathRules(fields)
	if _, ok := fields[udpPathPeersKey]; ok {
		t.Fatal("no named rule leaves no udp_path.peers")
	}
}

func TestUDPPathFieldIDs(t *testing.T) {
	var u udpPathField
	u.sync(3)
	first := slices.Clone(u.ids)
	u.ids = slices.Delete(u.ids, 1, 2)
	u.sync(2)
	if !slices.Equal(u.ids, []int{first[0], first[2]}) {
		t.Fatalf("ids after removing the middle row %v from %v", u.ids, first)
	}
	u.sync(3)
	if u.ids[2] == first[1] || u.ids[2] == first[2] {
		t.Fatalf("a new row gets a new id: %v", u.ids)
	}
}
