package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTailscaleUDPDirectDisabled(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want bool
	}{
		{"enabled tailscale endpoint", `{"endpoints":[{"type":"tailscale","enabled":true,"fields":{"disable_udp_direct":true}}]}`, true},
		{"disabled endpoint", `{"endpoints":[{"type":"tailscale","enabled":false,"fields":{"disable_udp_direct":true}}]}`, false},
		{"other endpoint", `{"endpoints":[{"type":"wireguard","enabled":true,"fields":{"disable_udp_direct":true}}]}`, false},
		{"malformed document", `{`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tailscaleUDPDirectDisabled(json.RawMessage(test.doc)); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestTailscaleUDPEnvironment(t *testing.T) {
	got := tailscaleUDPEnvironment([]string{"PATH=/bin", "TS_DEBUG_NEVER_DIRECT_UDP=true", "TS_DEBUG_ALWAYS_USE_DERP=false", "OTHER=value"}, true)
	want := []string{"PATH=/bin", "OTHER=value", "TS_DEBUG_ALWAYS_USE_DERP=true"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	got = tailscaleUDPEnvironment(got, false)
	want = []string{"PATH=/bin", "OTHER=value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
