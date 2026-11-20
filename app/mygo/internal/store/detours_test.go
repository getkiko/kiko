package store

import (
	"reflect"
	"testing"
)

func TestAppliedDetours(t *testing.T) {
	config := map[string]any{
		"outbounds": []any{
			map[string]any{"tag": "马来西亚服务器", "type": "vless", "detour": "香港 01"},
			map[string]any{"tag": "香港 01", "type": "shadowsocks"},
		},
		"endpoints": []any{map[string]any{"tag": "Tailscale", "detour": "美国板瓦"}},
	}
	want := map[string]string{"马来西亚服务器": "香港 01", "Tailscale": "美国板瓦"}
	if got := appliedDetours(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("detours %v", got)
	}
}
