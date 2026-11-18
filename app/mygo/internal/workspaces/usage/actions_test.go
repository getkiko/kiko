package usage

import (
	"maps"
	"slices"
	"testing"
)

func TestConnectionsViewUsesFilterGroups(t *testing.T) {
	v, ok := connectionsView(map[string]string{"rule": "3 · 国内直连", "protocol": "BITTORRENT", "host": "example.com"})
	want := map[string][]string{"rule": {"3 · 国内直连"}, "protocol": {"BitTorrent"}}
	if !ok || !maps.EqualFunc(v.Filter, want, slices.Equal[[]string]) || v.Search != "example.com" {
		t.Fatalf("view = %+v, %v; want filter %v and search example.com", v, ok, want)
	}
	v, _ = connectionsView(map[string]string{"rule": "—", "protocol": "—"})
	if want := map[string][]string{"rule": {"默认出口"}, "protocol": {"未识别"}}; !maps.EqualFunc(v.Filter, want, slices.Equal[[]string]) || v.Search != "" {
		t.Fatalf("view = %+v; want filter %v", v, want)
	}
}
