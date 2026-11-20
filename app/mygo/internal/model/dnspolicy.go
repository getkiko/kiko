package model

import (
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds DNS query detours.

// DNSResolverDetourKey is the field holding the query detour that belongs
// to a DNS reference field: x.server → x.detour, x → x.detour.
func DNSResolverDetourKey(key string) string {
	if strings.HasSuffix(key, ".server") {
		return strings.TrimSuffix(key, ".server") + ".detour"
	}
	return key + ".detour"
}

// CoreDNSServer is the core's implicit default: the first enabled server.
func CoreDNSServer(doc *Doc) *DNSServer {
	for _, s := range doc.DNS.Servers {
		if s.Enabled {
			return s
		}
	}
	return nil
}

// DNSTargetSupportsDetour reports whether queries to a server can use a
// detour: dialing types except local, which may hand lookups to platform
// APIs that bypass its dialer.
func DNSTargetSupportsDetour(server *DNSServer) bool {
	return server != nil && server.Type != "local" && IsDialerDNS(server.Type)
}

// DNSDetourError checks the whole proxy chain of a DNS query detour, ""
// when every hop is usable.
func DNSDetourError(doc *Doc, id string) string {
	type target struct {
		outbound *Outbound
		endpoint *Endpoint
	}
	targets := map[string]target{}
	for _, o := range doc.Outbounds {
		targets[o.ID] = target{outbound: o}
	}
	for _, e := range doc.Endpoints {
		targets[e.ID] = target{endpoint: e}
	}
	subscriptions := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			subscriptions[s.ID] = true
		}
	}
	available := func(t target, ok bool) bool {
		switch {
		case !ok:
			return false
		case t.endpoint != nil:
			return t.endpoint.Enabled
		}
		return t.outbound.Enabled && (t.outbound.SubID == "" || subscriptions[t.outbound.SubID])
	}
	pending := []string{id}
	visited := map[string]bool{}
	platform := doc.Platform
	if platform == "" {
		platform = PlatformLinux
	}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if visited[current] {
			continue
		}
		visited[current] = true
		t, ok := targets[current]
		if !available(t, ok) {
			tag := current
			if ok {
				if name := targetTag(t.outbound, t.endpoint); name != "" {
					tag = name
				}
			}
			return "出口「" + tag + "」已删除或停用，请重新选择"
		}
		if t.outbound != nil && IsGroup(t.outbound.Type) {
			var members []string
			for _, m := range GroupMembers(doc, t.outbound) {
				if mt, ok := targets[m]; available(mt, ok) {
					members = append(members, m)
				}
			}
			if len(members) == 0 {
				return "分组「" + t.outbound.Tag + "」需要至少一个可用成员"
			}
			pending = append(pending, members...)
			continue
		}
		var fields Fields
		var table []schema.Field
		if t.endpoint != nil {
			fields, table = t.endpoint.Fields, schema.EndpointSchema[t.endpoint.Type]
		} else {
			fields, table = t.outbound.Fields, schema.OutboundSchema[t.outbound.Type]
		}
		for _, field := range table {
			if field.Type != schema.Ref || field.Key == "default_ref" ||
				!schema.FieldAvailable(field, platform) || !schema.FieldVisible(field, table, fields) {
				continue
			}
			if dependency := fields[field.Key]; jsv.Truthy(dependency) {
				pending = append(pending, jsv.String(dependency))
			}
		}
		// Every hop of the proxy chain carries the query too.
		if t.outbound != nil {
			pending = append(pending, t.outbound.Chain...)
		} else {
			pending = append(pending, t.endpoint.Chain...)
		}
	}
	return ""
}

func targetTag(o *Outbound, e *Endpoint) string {
	if o != nil {
		return o.Tag
	}
	if e != nil {
		return e.Tag
	}
	return ""
}

// MigrateDNSDetours moves the former per-server query detours onto the
// references that use each server, preserving every query path. It runs
// once (dns.routingVersion) and does not modify input.
func MigrateDNSDetours(input *Doc) *Doc {
	if input.DNS.RoutingVersion == 1 {
		return input
	}
	doc := Clone(input)
	old := map[string]string{}
	for _, s := range doc.DNS.Servers {
		if !IsDialerDNS(s.Type) {
			continue
		}
		if s.Detour.Set() {
			old[s.ID] = s.Detour.ID()
		} else if d, ok := s.Fields["detour"].(string); ok {
			old[s.ID] = d
		}
	}
	detour := func(id string) string {
		if id == "" {
			return ""
		}
		return old[id]
	}
	first := ""
	if s := CoreDNSServer(doc); s != nil {
		first = s.ID
	}
	or := func(id string) string {
		if id != "" {
			return id
		}
		return first
	}
	for i, rule := range doc.DNS.Rules {
		if rule.Action != ActionReject && !rule.Detour.Set() && detour(rule.Server.ID()) != "" {
			copied := *rule
			copied.Detour = RefOf(detour(rule.Server.ID()))
			doc.DNS.Rules[i] = &copied
		}
		if rule := doc.DNS.Rules[i]; rule.Action != ActionReject && len(rule.MoreServers) > 0 {
			more := append([]DNSTarget(nil), rule.MoreServers...)
			for j, t := range more {
				if !t.Detour.Set() && detour(t.Server.ID()) != "" {
					more[j].Detour = RefOf(detour(t.Server.ID()))
				}
			}
			copied := *rule
			copied.MoreServers = more
			doc.DNS.Rules[i] = &copied
		}
	}
	if !doc.DNS.FinalDetour.Set() {
		if d := detour(or(doc.DNS.Final.ID())); d != "" {
			doc.DNS.FinalDetour = RefOf(d)
		}
	}
	for _, owner := range sortedKeys(doc.DNS.DeviceFinals) {
		id := doc.DNS.DeviceFinals[owner]
		if _, ok := doc.DNS.DeviceFinalDetours[owner]; ok && doc.DNS.DeviceFinalDetours[owner].Set() {
			continue
		}
		if d := detour(or(id.ID())); d != "" {
			detours := copyRefs(doc.DNS.DeviceFinalDetours)
			detours[owner] = RefOf(d)
			doc.DNS.DeviceFinalDetours = detours
		}
	}
	if !doc.Route.DefaultDomainResolverDetour.Set() {
		if d := detour(doc.Route.DefaultDomainResolver.ID()); d != "" {
			doc.Route.DefaultDomainResolverDetour = RefOf(d)
		}
	}
	patch := func(fields Fields, table []schema.Field) {
		for _, field := range table {
			if field.Type != schema.RefDNS {
				continue
			}
			key := DNSResolverDetourKey(field.Key)
			value := detour(jsv.StringOr(fields[field.Key], ""))
			if _, ok := fields[key]; !ok && value != "" {
				fields[key] = value
			}
		}
	}
	for _, o := range doc.Outbounds {
		o.Fields = ensureFields(o.Fields)
		patch(o.Fields, schema.OutboundSchema[o.Type])
	}
	for _, e := range doc.Endpoints {
		e.Fields = ensureFields(e.Fields)
		patch(e.Fields, schema.EndpointSchema[e.Type])
	}
	for _, in := range doc.Inbounds {
		in.Fields = ensureFields(in.Fields)
		patch(in.Fields, schema.InboundSchema[in.Type])
	}
	for _, s := range doc.Services {
		s.Fields = ensureFields(s.Fields)
		patch(s.Fields, schema.ServiceSchema[s.Type])
	}
	for _, s := range doc.DNS.Servers {
		if !s.DomainResolverDetour.Set() {
			if d := detour(s.DomainResolver.ID()); d != "" {
				s.DomainResolverDetour = RefOf(d)
			}
		}
		s.Detour = Ref{}
		delete(s.Fields, "detour")
	}
	doc.DNS.RoutingVersion = 1
	return doc
}

func ensureFields(f Fields) Fields {
	if f == nil {
		return Fields{}
	}
	return f
}
