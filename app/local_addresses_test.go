package main

import (
	"net"
	"reflect"
	"testing"
)

func TestLocalHostPrefixesPreserveDeviceBoundaries(t *testing.T) {
	var addresses []net.Addr
	for _, text := range []string{"192.168.1.1/24", "192.168.1.1/32", "2001:db8::1/64", "127.0.0.1/8", "::ffff:192.168.1.2/128", "0.0.0.0/0", "ff02::1/128"} {
		ip, network, err := net.ParseCIDR(text)
		if err != nil {
			t.Fatal(err)
		}
		network.IP = ip
		addresses = append(addresses, network)
	}
	got := localHostPrefixes(addresses)
	want := []string{"127.0.0.1/32", "192.168.1.1/32", "192.168.1.2/32", "2001:db8::1/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host scopes = %v, want %v", got, want)
	}
}
