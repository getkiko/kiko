package main

import (
	"net"
	"reflect"
	"testing"
)

func TestMitmRankShareAddresses(t *testing.T) {
	entries := []mitmShareIfaceAddr{
		{"wlan0", net.ParseIP("169.254.3.4")},
		{"docker0", net.ParseIP("172.17.0.1")},
		{"br-1a2b", net.ParseIP("192.168.200.1")},
		{"eth0", net.ParseIP("172.20.0.5")},
		{"eth0", net.ParseIP("10.0.0.7")},
		{"eth0", net.ParseIP("fd00::5")},
		{"eth0", net.ParseIP("fe80::1")},
		{"wlan0", net.ParseIP("192.168.1.9")},
		{"veth12", net.ParseIP("10.9.9.9")},
		{"virbr0", net.ParseIP("192.168.122.1")},
		{"cni0", net.ParseIP("10.244.0.1")},
		{"flannel.1", net.ParseIP("10.244.1.0")},
		{"lo", net.ParseIP("192.168.9.9")},
		{"eth1", net.ParseIP("192.168.1.9")},
		{"eth1", net.ParseIP("8.8.8.8")},
	}
	want := []string{"192.168.1.9", "10.0.0.7", "172.20.0.5", "fd00::5", "169.254.3.4"}
	if got := mitmRankShareAddresses(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := mitmRankShareAddresses(nil); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}
}
