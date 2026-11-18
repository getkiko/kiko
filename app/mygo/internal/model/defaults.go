package model

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the document defaults: the empty document, new entities
// (MakeInbound and its siblings), filling in missing fields, and the
// runtime platform.

// Platforms the document targets.
const (
	PlatformLinux   = "linux"
	PlatformDarwin  = "darwin"
	PlatformWindows = "windows"
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
)

// DefaultLatencyTestURL is the HTTP 204 address of speed tests.
const DefaultLatencyTestURL = "https://www.gstatic.com/generate_204"

// ServerPlatform maps the server's GOOS to a document platform.
func ServerPlatform(goos string) string {
	switch goos {
	case "darwin":
		return PlatformDarwin
	case "windows":
		return PlatformWindows
	}
	return PlatformLinux
}

// ProjectRuntimeDoc returns doc as the host platform edits it: edits use
// the host's defaults while stored profiles keep their origin.
func ProjectRuntimeDoc(doc *Doc, platform string) *Doc {
	if platform == "" || platform == doc.Platform {
		return doc
	}
	view := *doc
	view.Platform = platform
	return &view
}

// MakeInbound returns a new inbound of typ with the schema's defaults; a
// TUN gets the platform's addresses and exclusion table, and listeners
// listen on IPv4 only while IPv6 is off. An empty tag becomes "<type>-in".
func MakeInbound(typ, tag, platform string, ipv6 bool) *Inbound {
	if platform == "" {
		platform = PlatformLinux
	}
	fields := schema.DefaultFields(schema.InboundSchema[typ])
	if typ == "tun" {
		address := TunDefaultAddress(platform, ipv6)
		fields["ipv6"] = ipv6
		fields["address"] = anyStrings(address)
		fields["route_exclude_address"] = anyStrings(TunDefaultExclude(address, ipv6))
	} else if fields["listen"] == "::" && !ipv6 {
		fields["listen"] = "0.0.0.0"
	}
	if tag == "" {
		tag = typ + "-in"
	}
	return &Inbound{Entity: Entity{ID: NewID("in"), Tag: tag, Enabled: true}, Kind: "inbound", Type: typ, Fields: fields}
}

// MakeOutbound returns a new outbound of typ with the schema's defaults.
// An empty tag becomes the type.
func MakeOutbound(typ, tag string) *Outbound {
	if tag == "" {
		tag = typ
	}
	o := &Outbound{Entity: Entity{ID: NewID("out"), Tag: tag, Enabled: true}, Kind: "outbound", Type: typ,
		Fields: schema.DefaultFields(schema.OutboundSchema[typ])}
	if IsGroup(typ) {
		o.Members = []string{}
	}
	return o
}

// MakeEndpoint returns a new endpoint of typ with the schema's defaults.
func MakeEndpoint(typ, tag string, ipv6 bool) *Endpoint {
	fields := schema.DefaultFields(schema.EndpointSchema[typ])
	if fields["listen"] == "::" && !ipv6 {
		fields["listen"] = "0.0.0.0"
	}
	if tag == "" {
		tag = typ
	}
	return &Endpoint{Entity: Entity{ID: NewID("ep"), Tag: tag, Enabled: true}, Kind: "endpoint", Type: typ, Fields: fields}
}

// MakeService returns a new service of typ with the schema's defaults.
func MakeService(typ, tag string) *Service {
	if tag == "" {
		tag = typ
	}
	return &Service{Entity: Entity{ID: NewID("svc"), Tag: tag, Enabled: true}, Kind: "service", Type: typ,
		Fields: schema.DefaultFields(schema.ServiceSchema[typ])}
}

// EmptyDoc is the document of a first start: a direct outbound, a mixed
// inbound on 0.0.0.0:7890, the default DNS servers and policy groups.
func EmptyDoc(platform string) *Doc {
	if platform == "" {
		platform = PlatformLinux
	}
	direct := MakeOutbound("direct", "直连")
	mixed := MakeInbound("mixed", "mixed-in", PlatformLinux, false)
	mixed.Fields["listen"] = "0.0.0.0"
	mixed.Fields["listen_port"] = float64(7890)
	local, cn := DefaultDNSServers(NewID("dns"), NewID("dns"))
	findProcess := true
	return &Doc{
		Version:       1,
		Platform:      platform,
		Log:           Log{Level: "info", Timestamp: true},
		Inbounds:      []*Inbound{mixed},
		Outbounds:     []*Outbound{direct},
		Endpoints:     []*Endpoint{},
		Services:      []*Service{},
		Subscriptions: []*Subscription{},
		Route: Route{
			Groups: []*RouteGroup{
				{ID: NewID("rg"), Name: "默认分组", DeviceID: PreDeviceID},
				{ID: NewID("rg"), Name: "默认分组"},
				{ID: NewID("rg"), Name: "默认分组", DeviceID: OtherDeviceID},
			},
			Rules:                 []*RouteRule{},
			RuleSets:              []*RuleSet{},
			Final:                 Some(direct.ID),
			LocalFinal:            Some(direct.ID),
			AutoDetectInterface:   true,
			FindProcess:           &findProcess,
			Devices:               []*RouteDevice{},
			DefaultDomainResolver: Some(cn.ID),
			DefaultDomainStrategy: "ipv4_only",
		},
		DNS: DNS{
			DevicePolicyVersion: 1,
			RoutingVersion:      1,
			Groups: []*RouteGroup{
				{ID: NewID("dg"), Name: "默认分组", DeviceID: PreDeviceID},
				{ID: NewID("dg"), Name: "默认分组", DeviceID: LocalDeviceID},
				{ID: NewID("dg"), Name: "默认分组", DeviceID: OtherDeviceID},
			},
			DeviceFinals:  map[string]Ref{LocalDeviceID: Some(cn.ID)},
			Servers:       []*DNSServer{local, cn},
			Rules:         []*DNSRule{},
			Final:         Some(cn.ID),
			Strategy:      "prefer_ipv4",
			CacheCapacity: 2048,
		},
		Experimental:   Experimental{ClashAPI: true, ExternalController: "127.0.0.1:9090", DefaultMode: "Rule", CacheFile: true},
		LatencyTestURL: DefaultLatencyTestURL,
	}
}

// FillDefaults completes a stored document with the current schema's
// defaults and every migration: WireGuard outbounds to endpoints, DNS
// detours onto references, route and DNS device policies, list fields
// stored as text, TUN exclusion tables, the Burrow type (formerly
// holepunch) and its signal addresses, SOCKS
// authentication, IPv4-only strategies while IPv6 is off, uncached
// built-in rule sets, the Clash mode spelling and the speed test URL.
// It does not modify doc.
func FillDefaults(doc *Doc) *Doc { return fillDefaults(doc, false) }

// DecodeDoc reads a stored document and completes it with FillDefaults.
// It returns nil when raw is not a version 1 document. A document without
// ipv6Enabled infers it from its inbounds.
func DecodeDoc(raw []byte) *Doc {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var doc Doc
	if UnmarshalLenient(raw, &doc) != nil || doc.Version != 1 {
		return nil
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	_, hasIPv6 := keys["ipv6Enabled"]
	return fillDefaults(&doc, !hasIPv6)
}

// DecodeProfiles reads stored profiles, dropping entries without an id or
// a document, and completes their documents.
func DecodeProfiles(raw []byte) []*Profile {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var profiles []*Profile
	for _, item := range list {
		var p struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			SavedAt int64           `json:"savedAt"`
			Doc     json.RawMessage `json:"doc"`
		}
		if UnmarshalLenient(item, &p) != nil || p.ID == "" || len(p.Doc) == 0 || string(bytes.TrimSpace(p.Doc)) == "null" {
			continue
		}
		doc := decodeAnyDoc(p.Doc)
		if doc == nil {
			continue
		}
		profiles = append(profiles, &Profile{ID: p.ID, Name: p.Name, SavedAt: p.SavedAt, Doc: doc})
	}
	return profiles
}

// decodeAnyDoc completes a profile's document whatever its version, as
// fillProfile does.
func decodeAnyDoc(raw []byte) *Doc {
	var doc Doc
	if UnmarshalLenient(raw, &doc) != nil {
		return nil
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	_, hasIPv6 := keys["ipv6Enabled"]
	return fillDefaults(&doc, !hasIPv6)
}

func ipv4Strategy(strategy string) string {
	if strategy == "prefer_ipv6" || strategy == "ipv6_only" {
		return "ipv4_only"
	}
	return strategy
}

// patchFields completes an entity's fields; it returns nil when nothing
// changed.
func patchFields(typ string, cur Fields, table []schema.Field) Fields {
	def := schema.DefaultFields(table)
	changed := false
	next := cloneFields(cur)
	for k, v := range def {
		if _, ok := cur[k]; !ok {
			next[k] = v
			changed = true
		}
	}
	for _, f := range table {
		if f.Type != schema.List {
			continue
		}
		if s, ok := next[f.Key].(string); ok {
			next[f.Key] = anyStrings(splitText(s))
			changed = true
		}
	}
	switch typ {
	case "tun":
		if _, ok := next["route_exclude_address"].([]any); !ok {
			var addresses []string
			if list, ok := next["address"].([]any); ok {
				for _, a := range list {
					addresses = append(addresses, jsv.String(a))
				}
			}
			next["route_exclude_address"] = anyStrings(TunDefaultExclude(addresses, jsv.Truthy(next["ipv6"])))
			changed = true
		}
		if _, ok := next["exclude_private"]; ok {
			delete(next, "exclude_private")
			changed = true
		}
	case "burrow":
		_, hasAddress := cur["signal_address"]
		if !hasAddress || strings.TrimSpace(jsv.StringOr(next["signal_address"], "")) == "" {
			if address := schema.FormatBurrowSignalAddress(next["server"], next["server_port"], next["signal_path"]); address != "" {
				next["signal_address"] = address
			}
		}
		if _, ok := cur["signal_server_ip"]; !ok {
			legacyIP := strings.TrimSpace(jsv.StringOr(next["hosts_ip"], ""))
			mappedIP := ""
			if parsed, err := schema.ParseBurrowSignalAddress(jsv.StringOr(next["signal_address"], "")); err == "" && parsed.IsDomain {
				if hosts, ok := next["hosts"].(map[string]any); ok {
					mappedIP = strings.TrimSpace(jsv.StringOr(hosts[parsed.Hostname], ""))
				}
			}
			if legacyIP != "" {
				next["signal_server_ip"] = legacyIP
			} else if mappedIP != "" {
				next["signal_server_ip"] = mappedIP
			}
		}
		for _, key := range []string{"server", "server_port", "signal_path", "hosts", "hosts_domain", "hosts_ip"} {
			if _, ok := next[key]; ok {
				delete(next, key)
				changed = true
			}
		}
		if !jsv.StrictEqual(next["signal_address"], cur["signal_address"]) || !jsv.StrictEqual(next["signal_server_ip"], cur["signal_server_ip"]) {
			changed = true
		}
	case "socks":
		if _, ok := cur["auth_enabled"]; !ok && strings.TrimSpace(jsv.StringOr(cur["username"], "")) != "" {
			next["auth_enabled"] = true
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return next
}

// BurrowType is the Burrow protocol's type.
const BurrowType = "burrow"

// legacyBurrowType is the Burrow protocol's type before the core renamed
// it; FillDefaults moves inbounds and outbounds to BurrowType.
const legacyBurrowType = "holepunch"

func fillDefaults(input *Doc, inferIPv6 bool) *Doc {
	doc := MigrateDNSDetours(MigrateWireGuardOutbounds(input))
	if doc == input {
		doc = Clone(input)
	}
	inbounds := make([]*Inbound, len(doc.Inbounds))
	for i, in := range doc.Inbounds {
		if in.Type == legacyBurrowType {
			c := *in
			c.Type = BurrowType
			in = &c
		}
		inbounds[i] = in
		if fields := patchFields(in.Type, in.Fields, schema.InboundSchema[in.Type]); fields != nil {
			c := *in
			c.Fields = fields
			inbounds[i] = &c
		}
	}
	ipv6Enabled := doc.IPv6Enabled
	if inferIPv6 {
		ipv6Enabled = false
		for _, in := range inbounds {
			if in.Type == "tun" && in.Fields["ipv6"] == true || in.Type != "tun" && in.Fields["listen"] == "::" {
				ipv6Enabled = true
			}
		}
	}
	next := *doc
	next.IPv6Enabled = ipv6Enabled
	if next.Platform == "" {
		next.Platform = PlatformLinux
	}
	if doc.OutboundOrder == nil {
		order := []string{}
		for _, o := range doc.Outbounds {
			if o.SubID == "" {
				order = append(order, o.ID)
			}
		}
		for _, s := range doc.Subscriptions {
			order = append(order, "sub:"+s.ID)
		}
		next.OutboundOrder = order
	}
	next.Subscriptions = make([]*Subscription, len(doc.Subscriptions))
	for i, s := range doc.Subscriptions {
		c := *s
		next.Subscriptions[i] = &c
	}
	if next.Subscriptions == nil {
		next.Subscriptions = []*Subscription{}
	}
	next.Inbounds = inbounds
	next.Outbounds = make([]*Outbound, len(doc.Outbounds))
	for i, o := range doc.Outbounds {
		if o.Type == legacyBurrowType {
			c := *o
			c.Type = BurrowType
			o = &c
		}
		next.Outbounds[i] = o
		if fields := patchFields(o.Type, o.Fields, schema.OutboundSchema[o.Type]); fields != nil {
			c := *o
			c.Fields = fields
			next.Outbounds[i] = &c
		}
	}
	next.Endpoints = make([]*Endpoint, len(doc.Endpoints))
	for i, e := range doc.Endpoints {
		next.Endpoints[i] = e
		if fields := patchFields(e.Type, e.Fields, schema.EndpointSchema[e.Type]); fields != nil {
			c := *e
			c.Fields = fields
			next.Endpoints[i] = &c
		}
	}
	nameTailscale(&next)
	next.Services = make([]*Service, len(doc.Services))
	for i, s := range doc.Services {
		next.Services[i] = s
		if fields := patchFields(s.Type, s.Fields, schema.ServiceSchema[s.Type]); fields != nil {
			c := *s
			c.Fields = fields
			next.Services[i] = &c
		}
	}
	servers := make([]*DNSServer, len(doc.DNS.Servers))
	for i, s := range doc.DNS.Servers {
		servers[i] = s
		if IsTLSDNS(s.Type) {
			if _, ok := s.Fields["tls"].(map[string]any); !ok {
				c := *s
				c.Fields = cloneFields(s.Fields)
				c.Fields["tls"] = map[string]any{}
				servers[i] = &c
			}
		}
		if !ipv6Enabled {
			c := *servers[i]
			c.DomainStrategy = ipv4Strategy(c.DomainStrategy)
			servers[i] = &c
		}
	}
	migratedRoute := MigrateRoutePolicy(doc.Route)
	withRoute := *doc
	withRoute.Route = migratedRoute
	dns := MigrateDeviceDNS(&withRoute)
	if !ipv6Enabled {
		dns.Strategy = ipv4Strategy(doc.DNS.Strategy)
	}
	dns.Servers = servers
	next.DNS = dns
	route := migratedRoute
	if !ipv6Enabled {
		route.DefaultDomainStrategy = ipv4Strategy(doc.Route.DefaultDomainStrategy)
	}
	route.RuleSets = []*RuleSet{}
	for _, r := range doc.Route.RuleSets {
		if !IsBuiltinRuleSet(r.Tag) || r.Mode == "remote" || len(r.Rules) > 0 {
			route.RuleSets = append(route.RuleSets, r)
		}
	}
	next.Route = route
	if doc.Experimental.DefaultMode == "rule" {
		next.Experimental.DefaultMode = "Rule"
	}
	if next.LatencyTestURL == "" {
		next.LatencyTestURL = DefaultLatencyTestURL
	}
	return SanitizeDeviceExtras(&next)
}
