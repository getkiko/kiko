package settings

import (
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// setGlobalIPv6 turns IPv6 on or off across the document: the DNS
// strategies, the wildcard listen addresses and
// every TUN inbound's addresses and exclusions.
func setGlobalIPv6(d *model.Doc, enabled bool) {
	ipv4Strategy := func(strategy string) string {
		if strategy == "prefer_ipv6" || strategy == "ipv6_only" {
			return "ipv4_only"
		}
		return strategy
	}
	d.IPv6Enabled = enabled
	if enabled {
		d.DNS.Strategy = "prefer_ipv6"
	} else {
		d.DNS.Strategy = "prefer_ipv4"
		for _, server := range d.DNS.Servers {
			server.DomainStrategy = ipv4Strategy(server.DomainStrategy)
		}
		d.Route.DefaultDomainStrategy = ipv4Strategy(d.Route.DefaultDomainStrategy)
	}
	platform := d.Platform
	if platform == "" {
		platform = model.PlatformLinux
	}
	wildcardFrom, wildcardTo := "::", "0.0.0.0"
	if enabled {
		wildcardFrom, wildcardTo = "0.0.0.0", "::"
	}
	for _, in := range d.Inbounds {
		if in.Fields == nil {
			in.Fields = model.Fields{}
		}
		if in.Type != "tun" {
			if listen, _ := in.Fields["listen"].(string); listen == wildcardFrom && in.Fields["listen"] != nil {
				in.Fields["listen"] = wildcardTo
			}
			continue
		}
		var ipv4 []string
		if list, ok := in.Fields["address"].([]any); ok {
			for _, a := range list {
				if s, ok := a.(string); ok && !strings.Contains(s, ":") {
					ipv4 = append(ipv4, s)
				}
			}
		}
		in.Fields["ipv6"] = enabled
		var address []string
		if len(ipv4) > 0 {
			address = ipv4
			for _, a := range model.TunDefaultAddress(platform, enabled) {
				if strings.Contains(a, ":") {
					address = append(address, a)
				}
			}
		} else {
			address = model.TunDefaultAddress(platform, enabled)
		}
		in.Fields["address"] = toAny(address)
		in.Fields["route_exclude_address"] = toAny(model.TunDefaultExclude(address, enabled))
	}
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
