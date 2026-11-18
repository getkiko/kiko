package main

import (
	"net"
	"net/http"
	"net/netip"
	"sort"
)

// Local source scopes contain host addresses, preserving adjacent LAN devices.
func localHostPrefixes(addresses []net.Addr) []string {
	seen := map[string]bool{}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			continue
		}
		host := prefix.Addr().Unmap()
		if !host.IsValid() || host.IsUnspecified() || host.IsMulticast() {
			continue
		}
		seen[netip.PrefixFrom(host, host.BitLen()).String()] = true
	}
	result := make([]string, 0, len(seen))
	for prefix := range seen {
		result = append(result, prefix)
	}
	sort.Strings(result)
	return result
}

func localAddresses(w http.ResponseWriter, r *http.Request) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"addresses": localHostPrefixes(addresses)})
}
