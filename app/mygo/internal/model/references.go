package model

import (
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the reference checks and tab warnings: which entities
// hold references that need fixing.

func hasEnabledSubscription(doc *Doc, subID string) bool {
	for _, s := range doc.Subscriptions {
		if s.ID == subID {
			return s.Enabled
		}
	}
	return false
}

// IsOutboundUsable reports whether id names an enabled endpoint or an
// enabled outbound outside disabled subscriptions.
func IsOutboundUsable(doc *Doc, id string) bool {
	if id == "" {
		return false
	}
	for _, e := range doc.Endpoints {
		if e.ID == id {
			return e.Enabled
		}
	}
	for _, o := range doc.Outbounds {
		if o.ID == id {
			return o.Enabled && (o.SubID == "" || hasEnabledSubscription(doc, o.SubID))
		}
	}
	return false
}

// IsInboundUsable reports whether id names an enabled inbound.
func IsInboundUsable(doc *Doc, id string) bool {
	if id == "" {
		return false
	}
	for _, in := range doc.Inbounds {
		if in.ID == id && in.Enabled {
			return true
		}
	}
	return false
}

// IsDNSServerUsable reports whether id names an enabled DNS server.
func IsDNSServerUsable(doc *Doc, id string) bool {
	if id == "" {
		return false
	}
	for _, s := range doc.DNS.Servers {
		if s.ID == id && s.Enabled {
			return true
		}
	}
	return false
}

// HasInvalidDNSTarget reports a DNS reference to a missing or disabled
// server, or a detour the server cannot use or that is broken.
func HasInvalidDNSTarget(doc *Doc, id, detour string) bool {
	var server *DNSServer
	for _, s := range doc.DNS.Servers {
		if s.ID == id && s.Enabled {
			server = s
			break
		}
	}
	return server == nil || detour != "" && (!DNSTargetSupportsDetour(server) || DNSDetourError(doc, detour) != "")
}

// hasInvalidSchemaReferences checks an entity's ref, refDns and refIn
// fields.
func hasInvalidSchemaReferences(doc *Doc, table []schema.Field, fields Fields) bool {
	for _, field := range table {
		value := fields[field.Key]
		if !jsv.Truthy(value) {
			continue
		}
		id := jsv.String(value)
		switch field.Type {
		case schema.Ref:
			if !IsOutboundUsable(doc, id) {
				return true
			}
		case schema.RefDNS:
			if HasInvalidDNSTarget(doc, id, refString(fields[DNSResolverDetourKey(field.Key)])) {
				return true
			}
		case schema.RefIn:
			if !IsInboundUsable(doc, id) {
				return true
			}
		}
	}
	return false
}

// refString reads an optional id field ("" when unset or null).
func refString(v any) string {
	if !jsv.Truthy(v) {
		return ""
	}
	return jsv.String(v)
}

// HasInvalidOutboundReferences reports broken references of an outbound:
// shared configs, group members and default, and reference fields.
func HasInvalidOutboundReferences(doc *Doc, outbound *Outbound, library []*NetworkConfig) bool {
	table := schema.OutboundSchema[outbound.Type]
	if len(NetworkReferenceErrors(table, outbound.NetworkConfigRefs, library, outbound.Tag)) > 0 {
		return true
	}
	for _, m := range outbound.Members {
		if !IsOutboundUsable(doc, m) {
			return true
		}
	}
	if IsGroup(outbound.Type) {
		if def := outbound.Fields["default_ref"]; jsv.Truthy(def) {
			id := jsv.String(def)
			member := false
			for _, m := range GroupMembers(doc, outbound) {
				member = member || m == id
			}
			if !member || !IsOutboundUsable(doc, id) {
				return true
			}
		}
	}
	return hasInvalidSchemaReferences(doc, table, outbound.Fields)
}

// HasInvalidEndpointReferences reports broken references of an endpoint.
func HasInvalidEndpointReferences(doc *Doc, endpoint *Endpoint, library []*NetworkConfig) bool {
	table := schema.EndpointSchema[endpoint.Type]
	return IsExtraTailscale(doc, endpoint) ||
		len(NetworkReferenceErrors(table, endpoint.NetworkConfigRefs, library, endpoint.Tag)) > 0 ||
		hasInvalidSchemaReferences(doc, table, endpoint.Fields)
}

// HasInvalidServiceReferences reports broken references of a service.
func HasInvalidServiceReferences(doc *Doc, service *Service, library []*NetworkConfig) bool {
	table := schema.ServiceSchema[service.Type]
	return len(NetworkReferenceErrors(table, service.NetworkConfigRefs, library, service.Tag)) > 0 ||
		hasInvalidSchemaReferences(doc, table, service.Fields)
}

// HasInvalidInboundReferences reports broken references of an inbound,
// including its route address rule sets.
func HasInvalidInboundReferences(doc *Doc, inbound *Inbound, library []*NetworkConfig) bool {
	table := schema.InboundSchema[inbound.Type]
	if len(NetworkReferenceErrors(table, inbound.NetworkConfigRefs, library, inbound.Tag)) > 0 {
		return true
	}
	return hasInvalidSchemaReferences(doc, table, inbound.Fields) ||
		hasInvalidRuleSetTag(doc, inbound.Fields["route_address_set"]) ||
		hasInvalidRuleSetTag(doc, inbound.Fields["route_exclude_address_set"])
}

func hasCondition(value any) bool {
	if list, ok := jsv.Array(value); ok {
		for _, v := range list {
			if hasCondition(v) {
				return true
			}
		}
		return false
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return value != nil && value != false
}

func matchHasCondition(match map[string]any) bool {
	for _, v := range match {
		if hasCondition(v) {
			return true
		}
	}
	return false
}

// HasInvalidRouteRuleReference reports a route rule whose target is not
// usable. A rule without conditions is skipped by the emitter and is not
// invalid.
func HasInvalidRouteRuleReference(doc *Doc, rule *RouteRule) bool {
	if !rule.Enabled || rule.Action != ActionRoute {
		return false
	}
	if rule.Outbound.Valid() {
		return !IsOutboundUsable(doc, rule.Outbound.ID())
	}
	return rule.ConditionTree != nil || rule.CatchAll || matchHasCondition(rule.Match)
}

// HasInvalidDNSServerReferences reports broken references of a DNS server.
func HasInvalidDNSServerReferences(doc *Doc, server *DNSServer, library []*NetworkConfig) bool {
	if !IsDialerDNS(server.Type) {
		return false
	}
	if len(NetworkReferenceErrors(DNSNetworkSchema(server.Type), server.NetworkConfigRefs, library, server.Tag)) > 0 {
		return true
	}
	return server.Detour.Valid() && !IsOutboundUsable(doc, server.Detour.ID()) ||
		server.DomainResolver.Valid() && HasInvalidDNSTarget(doc, server.DomainResolver.ID(), server.DomainResolverDetour.ID())
}

// HasInvalidDNSRuleReference reports a DNS rule whose server or detour is
// not usable.
func HasInvalidDNSRuleReference(doc *Doc, rule *DNSRule) bool {
	if !rule.Enabled || rule.Action == ActionReject {
		return false
	}
	if rule.Action == ActionScript {
		return !ScriptUsable(doc, rule.Script, ScriptDNS)
	}
	if targets := rule.Targets(); len(targets) > 0 {
		for _, t := range targets {
			if !t.Server.Valid() || HasInvalidDNSTarget(doc, t.Server.ID(), t.Detour.ID()) {
				return true
			}
			if s := DNSServerByID(doc, t.Server.ID()); len(targets) > 1 && s != nil && s.Type == "fakeip" {
				return true
			}
		}
		return false
	}
	return rule.ConditionTree != nil || rule.CatchAll || matchHasCondition(rule.Match)
}

// HasInvalidSubscriptionReferences reports a subscription whose detour is
// not usable.
func HasInvalidSubscriptionReferences(doc *Doc, subscription *Subscription) bool {
	return subscription.Detour.Valid() && !IsOutboundUsable(doc, subscription.Detour.ID())
}

func hasInvalidRuleSetTag(doc *Doc, value any) bool {
	known := map[string]bool{}
	for _, r := range doc.Route.RuleSets {
		known[r.Tag] = true
	}
	for _, tag := range RuleSetValues(value) {
		for _, r := range doc.Route.RuleSets {
			if r.Tag == tag && !r.Enabled {
				return true
			}
		}
		if !IsBuiltinRuleSet(tag) && !known[tag] {
			return true
		}
	}
	return false
}

func hasInvalidRouteMatchReferences(doc *Doc, match map[string]any) bool {
	if hasInvalidRuleSetTag(doc, match["rule_set"]) || hasInvalidScriptTags(doc, match["script"]) {
		return true
	}
	for _, id := range RuleSetValues(match["inbound"]) {
		if IsInboundUsable(doc, id) {
			continue
		}
		endpoint := false
		for _, e := range doc.Endpoints {
			endpoint = endpoint || e.ID == id && e.Enabled
		}
		if !endpoint {
			return true
		}
	}
	for _, id := range RuleSetValues(match["preferred_by"]) {
		if !IsOutboundUsable(doc, id) {
			return true
		}
	}
	return false
}

func hasInvalidHTTPClient(doc *Doc, id string) bool {
	if id == "" {
		return false
	}
	for _, c := range doc.HTTPClients {
		if c.ID == id {
			return !c.Enabled || c.Detour.Valid() && !IsOutboundUsable(doc, c.Detour.ID())
		}
	}
	return true
}

// HasInvalidRuleSetReferences reports a rule set whose HTTP client is not
// usable.
func HasInvalidRuleSetReferences(doc *Doc, ruleSet *RuleSet) bool {
	return hasInvalidHTTPClient(doc, ruleSet.HTTPClient.ID())
}

// HasInvalidRouteGlobalReferences reports broken route settings: local
// addresses, devices, defaults, the default resolver and HTTP client.
func HasInvalidRouteGlobalReferences(doc *Doc) bool {
	for _, a := range doc.Route.LocalAddresses {
		if !ValidNetwork(a) {
			return true
		}
	}
	for _, d := range doc.Route.Devices {
		if ValidateDevice(d, doc.Route.Devices) != "" {
			return true
		}
		for _, final := range DeviceFinalOutbounds(d) {
			if !IsOutboundUsable(doc, final) {
				return true
			}
		}
	}
	if ValidateLocalProfiles(doc) != "" {
		return true
	}
	r := doc.Route
	return localFinalsInvalid(doc) ||
		r.Final.Valid() && !IsOutboundUsable(doc, r.Final.ID()) ||
		r.DefaultDomainResolver.Valid() && HasInvalidDNSTarget(doc, r.DefaultDomainResolver.ID(), r.DefaultDomainResolverDetour.ID()) ||
		r.DefaultHTTPClient.Valid() && hasInvalidHTTPClient(doc, r.DefaultHTTPClient.ID())
}

// HasInvalidDNSGlobalReferences reports broken DNS defaults.
func HasInvalidDNSGlobalReferences(doc *Doc) bool {
	invalid := func(id, detour string) bool {
		target := id
		if target == "" {
			if s := CoreDNSServer(doc); s != nil {
				target = s.ID
			}
		}
		if target == "" {
			return detour != ""
		}
		if HasInvalidDNSTarget(doc, target, detour) {
			return true
		}
		for _, s := range doc.DNS.Servers {
			if s.ID == target {
				return s.Type == "fakeip"
			}
		}
		return false
	}
	for _, owner := range sortedKeys(doc.DNS.DeviceFinals) {
		if invalid(doc.DNS.DeviceFinals[owner].ID(), doc.DNS.DeviceFinalDetours[owner].ID()) {
			return true
		}
	}
	for _, owner := range sortedKeys(doc.DNS.DeviceFinalDetours) {
		if detour := doc.DNS.DeviceFinalDetours[owner].ID(); detour != "" && invalid(doc.DNS.DeviceFinals[owner].ID(), detour) {
			return true
		}
	}
	return FakeIPServerError(doc.DNS.Servers, doc.DNS.Final) != "" || invalid(doc.DNS.Final.ID(), doc.DNS.FinalDetour.ID())
}

// HasInvalidRuleReferences reports a route rule that needs fixing: its
// target, scope, conditions or the references in its conditions.
func HasInvalidRuleReferences(doc *Doc, rule *RouteRule) bool {
	if HasInvalidRouteRuleReference(doc, rule) {
		return true
	}
	if rule.DeviceID == PreDeviceID && ValidateCondition(RuleCondition(rule.Match, rule.ConditionTree, rule.Logic), false) != "" {
		return true
	}
	if rule.DeviceID != "" && !IsFixedDevice(rule.DeviceID) && !hasDevice(doc, rule.DeviceID) {
		return true
	}
	if rule.ConditionTree != nil {
		if ValidateCondition(rule.ConditionTree, rule.CatchAll) != "" {
			return true
		}
		for _, m := range ConditionMatches(rule.ConditionTree) {
			if hasInvalidRouteMatchReferences(doc, m) {
				return true
			}
		}
		return false
	}
	return hasInvalidRouteMatchReferences(doc, rule.Match)
}

// hasDevice reports a scope that belongs to a device, in effect or not.
func hasDevice(doc *Doc, scope string) bool {
	_, _, ok := ScopeDevice(doc, scope)
	return ok
}

func hasInvalidDNSMatchReferences(doc *Doc, match map[string]any) bool {
	if hasInvalidRuleSetTag(doc, match["rule_set"]) || hasInvalidScriptTags(doc, match["script"]) {
		return true
	}
	for _, id := range RuleSetValues(match["preferred_by"]) {
		if !IsDNSServerUsable(doc, id) {
			return true
		}
	}
	return false
}

// HasInvalidDNSRuleReferences reports a DNS rule that needs fixing.
func HasInvalidDNSRuleReferences(doc *Doc, rule *DNSRule) bool {
	if HasInvalidDNSRuleReference(doc, rule) {
		return true
	}
	if rule.DeviceID == PreDeviceID && ValidateCondition(RuleCondition(rule.Match, rule.ConditionTree, ""), false) != "" {
		return true
	}
	if rule.DeviceID != "" && !IsFixedDevice(rule.DeviceID) && !hasDevice(doc, rule.DeviceID) {
		return true
	}
	if rule.ConditionTree != nil {
		if ValidateCondition(rule.ConditionTree, rule.CatchAll) != "" {
			return true
		}
		for _, m := range ConditionMatches(rule.ConditionTree) {
			if hasInvalidDNSMatchReferences(doc, m) {
				return true
			}
		}
		return false
	}
	return hasInvalidDNSMatchReferences(doc, rule.Match)
}

// HasBlockingReferenceWarnings reports whether anything in the document
// holds a broken reference.
func HasBlockingReferenceWarnings(doc *Doc, library []*NetworkConfig) bool {
	if TailscaleLimitError(doc) != "" {
		return true
	}
	for _, s := range doc.Subscriptions {
		if HasInvalidSubscriptionReferences(doc, s) {
			return true
		}
	}
	for _, o := range doc.Outbounds {
		if HasInvalidOutboundReferences(doc, o, library) {
			return true
		}
	}
	for _, e := range doc.Endpoints {
		if HasInvalidEndpointReferences(doc, e, library) {
			return true
		}
	}
	for _, s := range doc.Services {
		if HasInvalidServiceReferences(doc, s, library) {
			return true
		}
	}
	for _, in := range doc.Inbounds {
		if HasInvalidInboundReferences(doc, in, library) {
			return true
		}
	}
	for _, r := range doc.Route.Rules {
		if HasInvalidRuleReferences(doc, r) {
			return true
		}
	}
	for _, r := range doc.Route.RuleSets {
		if HasInvalidRuleSetReferences(doc, r) {
			return true
		}
	}
	if HasInvalidRouteGlobalReferences(doc) {
		return true
	}
	for _, s := range doc.DNS.Servers {
		if HasInvalidDNSServerReferences(doc, s, library) {
			return true
		}
	}
	for _, r := range doc.DNS.Rules {
		if HasInvalidDNSRuleReferences(doc, r) {
			return true
		}
	}
	return HasInvalidDNSGlobalReferences(doc)
}

// TabWarnings reports which console pages hold references that need
// fixing, by page: "subscriptions", "route" and "settings" (the DNS
// servers, under Settings › DNS).
func TabWarnings(doc *Doc, library []*NetworkConfig) map[string]bool {
	some := func(n int, bad func(int) bool) bool {
		for i := 0; i < n; i++ {
			if bad(i) {
				return true
			}
		}
		return false
	}
	subscriptions := some(len(doc.Subscriptions), func(i int) bool { return HasInvalidSubscriptionReferences(doc, doc.Subscriptions[i]) })
	outbounds := some(len(doc.Outbounds), func(i int) bool { return HasInvalidOutboundReferences(doc, doc.Outbounds[i], library) })
	inbounds := some(len(doc.Inbounds), func(i int) bool { return HasInvalidInboundReferences(doc, doc.Inbounds[i], library) })
	endpoints := some(len(doc.Endpoints), func(i int) bool { return HasInvalidEndpointReferences(doc, doc.Endpoints[i], library) }) ||
		some(len(doc.Services), func(i int) bool { return HasInvalidServiceReferences(doc, doc.Services[i], library) })
	route := some(len(doc.Route.Rules), func(i int) bool { return HasInvalidRuleReferences(doc, doc.Route.Rules[i]) }) ||
		some(len(doc.Route.RuleSets), func(i int) bool { return HasInvalidRuleSetReferences(doc, doc.Route.RuleSets[i]) }) ||
		HasInvalidRouteGlobalReferences(doc) ||
		some(len(doc.DNS.Rules), func(i int) bool { return HasInvalidDNSRuleReferences(doc, doc.DNS.Rules[i]) }) ||
		HasInvalidDNSGlobalReferences(doc)
	dns := some(len(doc.DNS.Servers), func(i int) bool { return HasInvalidDNSServerReferences(doc, doc.DNS.Servers[i], library) }) ||
		doc.Route.DefaultDomainResolver.Valid() && !IsDNSServerUsable(doc, doc.Route.DefaultDomainResolver.ID())
	components := subscriptions || outbounds || inbounds || endpoints
	return map[string]bool{"subscriptions": components, "route": route, "settings": dns}
}

// ScriptUsable reports whether tag names an enabled, valid user script of the
// type (rule for a `script` condition, dns for a script action).
func ScriptUsable(doc *Doc, tag, typ string) bool {
	if doc == nil || doc.HTTPTools == nil {
		return false
	}
	for _, s := range doc.HTTPTools.Scripts {
		if s != nil && s.EffectiveTag() == tag && s.Type == typ {
			return !s.Disabled && s.Issue() == ""
		}
	}
	return false
}

// hasInvalidScriptTags reports a `script` condition naming a script that is
// missing, disabled, invalid or not a rule script.
func hasInvalidScriptTags(doc *Doc, value any) bool {
	for _, tag := range Values(value) {
		if !ScriptUsable(doc, tag, ScriptRule) {
			return true
		}
	}
	return false
}
