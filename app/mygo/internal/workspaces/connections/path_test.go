package connections

import (
	"reflect"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// The path runs in the order traffic takes it: the groups, the outbound
// they chose, then the detours it dials through, a detour group followed
// by its chosen member.
func TestConnectionPath(t *testing.T) {
	conn := &api.Connection{Outbound: "马来西亚服务器", ChainList: []string{"马来西亚服务器", "自选"}}
	detours := map[string]string{"马来西亚服务器": "香港组", "香港 01": "上海中转", "无关": "x"}
	selected := map[string]string{"香港组": "香港 01", "自选": "马来西亚服务器"}
	want := []hop{{"自选", hopGroup}, {"马来西亚服务器", hopOutbound}, {"香港组", hopDetour}, {"香港 01", hopDetour}, {"上海中转", hopDetour}}
	if got := connectionPath(conn, detours, selected); !reflect.DeepEqual(got, want) {
		t.Fatalf("path %v", got)
	}
	if got := pathText(want); got != "自选 → 马来西亚服务器 → 香港组 → 香港 01 → 上海中转" {
		t.Fatalf("text %q", got)
	}
	if got := pathDetail(want[:3]); got != "自选（策略组） → 马来西亚服务器 → 香港组（前置代理）" {
		t.Fatalf("detail %q", got)
	}

	// Without detours it is the core's chain, reversed.
	if got := chainName(conn); got != "自选 → 马来西亚服务器" {
		t.Fatalf("chain %q", got)
	}
	// A loop of detours stops where it returns.
	loop := map[string]string{"a": "b", "b": "a"}
	if got := pathText(connectionPath(&api.Connection{Outbound: "a", ChainList: []string{"a"}}, loop, nil)); got != "a → b" {
		t.Fatalf("loop %q", got)
	}
	// No chain falls back to the outbound; refused connections have none.
	if got := chainName(&api.Connection{Outbound: "直连"}); got != "直连" {
		t.Fatalf("outbound only %q", got)
	}
	if got := chainName(&api.Connection{}); got != "—" {
		t.Fatalf("empty %q", got)
	}
	if got := chainName(&api.Connection{RejectCount: 1, Outbound: "x"}); got != "拒绝" {
		t.Fatalf("rejected %q", got)
	}
}
