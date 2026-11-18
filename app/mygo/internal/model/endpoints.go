package model

import (
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the WireGuard migration: sing-box 1.13 removed the
// WireGuard outbound, so stored ones become WireGuard endpoints.

var wireguardOutboundKeys = map[string]bool{
	"server": true, "server_port": true, "private_key": true, "peer_public_key": true,
	"pre_shared_key": true, "local_address": true, "mtu": true, "reserved": true,
}

// WireguardOutboundToEndpoint converts a WireGuard outbound to an endpoint
// with the same id and tag, so references to it keep working.
func WireguardOutboundToEndpoint(outbound *Outbound) *Endpoint {
	f := outbound.Fields
	peer := map[string]any{"allowed_ips": []any{"0.0.0.0/0", "::/0"}}
	if server := jsv.Text(f["server"]); server != "" {
		peer["address"] = server
	}
	if port := jsv.NumberOrNaN(f["server_port"]); jsv.IsInteger(port) && port > 0 {
		peer["port"] = port
	}
	if key := jsv.Text(f["peer_public_key"]); key != "" {
		peer["public_key"] = key
	}
	if key := jsv.Text(f["pre_shared_key"]); key != "" {
		peer["pre_shared_key"] = key
	}
	var reserved []any
	for _, item := range SplitList(f["reserved"]) {
		if n := jsv.StringToNumber(item); jsv.IsFinite(n) {
			reserved = append(reserved, n)
		}
	}
	if len(reserved) > 0 {
		peer["reserved"] = reserved
	}
	fields := Fields{
		"address":     anyStrings(SplitList(f["local_address"])),
		"private_key": strings.TrimSpace(jsv.StringOr(f["private_key"], "")),
		"peers":       []any{peer},
	}
	if mtu, ok := f["mtu"]; ok && mtu != "" {
		fields["mtu"] = mtu
	}
	for key, value := range f {
		if !wireguardOutboundKeys[key] {
			fields[key] = value
		}
	}
	entity := Entity{ID: outbound.ID, Tag: outbound.Tag, Enabled: outbound.Enabled}
	if len(outbound.NetworkConfigRefs) > 0 {
		// The endpoint's dialer takes the same shared network config.
		entity.NetworkConfigRefs = NetworkConfigRefs{}
		for slot, id := range outbound.NetworkConfigRefs {
			entity.NetworkConfigRefs[slot] = id
		}
	}
	return &Endpoint{Entity: entity, Kind: "endpoint", Type: "wireguard", Fields: fields}
}

// MigrateWireGuardOutbounds moves WireGuard outbounds that do not come
// from a subscription to endpoints; subscriptions refresh their own nodes.
func MigrateWireGuardOutbounds(doc *Doc) *Doc {
	ids := map[string]bool{}
	var legacy []*Outbound
	for _, o := range doc.Outbounds {
		if o.Type == "wireguard" && o.SubID == "" {
			legacy = append(legacy, o)
			ids[o.ID] = true
		}
	}
	if len(legacy) == 0 {
		return doc
	}
	next := *doc
	next.Outbounds = []*Outbound{}
	for _, o := range doc.Outbounds {
		if !ids[o.ID] {
			next.Outbounds = append(next.Outbounds, o)
		}
	}
	next.Endpoints = append([]*Endpoint{}, doc.Endpoints...)
	for _, o := range legacy {
		next.Endpoints = append(next.Endpoints, WireguardOutboundToEndpoint(o))
	}
	if doc.OutboundOrder != nil {
		next.OutboundOrder = []string{}
		for _, key := range doc.OutboundOrder {
			if !ids[key] {
				next.OutboundOrder = append(next.OutboundOrder, key)
			}
		}
	}
	return &next
}
