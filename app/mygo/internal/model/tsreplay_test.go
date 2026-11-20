package model_test

import (
	"encoding/json"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/model/tsgolden"
)

// TestTypeScriptParity replays the calls recorded from the former
// TypeScript console's tests and corpus (see package tsgolden) against the
// port.
func TestTypeScriptParity(t *testing.T) {
	calls := tsgolden.Load(t, "testdata/ts-calls.jsonl.gz")
	ran := tsgolden.Replay(t, calls, modelAdapters, tsgolden.Options{
		Recorded:  func(fn string, v any) any { return tsgolden.CoreWording(v) },
		Normalize: normalizeOrder,
		Fixed:     modelFixes,
	})
	tsgolden.Summary(t, ran, modelAdapters)
}

var modelFixes = []tsgolden.Fix{{
	Bug: "store-pure.ts keeps a single detour field; Go moves it into the proxy chain (MigrateProxyChains), " +
		"which the emitter turns back into the same detour",
	Fn:    "store-pure.fillDefaults",
	Match: func(c tsgolden.Call) bool { return recordedDetours(c.R) },
	Want: func(c tsgolden.Call, _ any) json.RawMessage {
		var want map[string]any
		if json.Unmarshal(c.R, &want) != nil {
			return c.R
		}
		for _, section := range []string{"outbounds", "endpoints"} {
			takes := model.OutboundTakesChain
			if section == "endpoints" {
				takes = model.EndpointTakesChain
			}
			items, _ := want[section].([]any)
			for _, item := range items {
				e, _ := item.(map[string]any)
				fields, _ := e["fields"].(map[string]any)
				detour, has := fields["detour"]
				if !has {
					continue
				}
				delete(fields, "detour")
				typ, _ := e["type"].(string)
				if id, _ := detour.(string); id != "" && e["chain"] == nil && takes(typ) {
					e["chain"] = []any{id}
				}
			}
		}
		subscriptions, _ := want["subscriptions"].([]any)
		for _, item := range subscriptions {
			s, _ := item.(map[string]any)
			if detour, has := s["detour"]; has {
				delete(s, "detour")
				if id, _ := detour.(string); id != "" && s["chain"] == nil {
					s["chain"] = []any{id}
				}
			}
		}
		raw, _ := json.Marshal(want)
		return raw
	},
}, {
	Bug: "store-pure.ts keeps the Tailscale endpoint's tag; Go keeps one Tailscale endpoint with the fixed tag " +
		"Tailscale (2d21d9c), so completing a document renames it, references going by ID",
	Fn: "store-pure.fillDefaults",
	Match: func(c tsgolden.Call) bool {
		var recorded model.Doc
		_ = json.Unmarshal(c.R, &recorded)
		for _, e := range recorded.Endpoints {
			if e.Type == "tailscale" && e.Tag != model.TailscaleTag {
				return true
			}
		}
		return false
	},
	Want: func(c tsgolden.Call, got any) json.RawMessage {
		doc, _ := got.(*model.Doc)
		if doc == nil {
			return c.R
		}
		kept := model.TailscaleEndpoint(doc)
		var want map[string]any
		if kept == nil || json.Unmarshal(c.R, &want) != nil {
			return c.R
		}
		endpoints, _ := want["endpoints"].([]any)
		for _, item := range endpoints {
			if e, ok := item.(map[string]any); ok && e["id"] == kept.ID {
				e["tag"] = model.TailscaleTag
			}
		}
		raw, _ := json.Marshal(want)
		return raw
	},
}, {
	Bug: "rulesets.ts labels every imported rule set 用户导入; Go labels a custom one (inline, no link) 自建 (F14)",
	Fn:  "rulesets.ruleSetOptions",
	Match: func(c tsgolden.Call) bool {
		for _, r := range arg[[]*model.RuleSet](c, 0) {
			if r != nil && model.IsImportedRuleSet(r) && model.IsCustomRuleSet(r) {
				return true
			}
		}
		return false
	},
	Want: func(c tsgolden.Call, _ any) json.RawMessage {
		custom := map[string]bool{}
		for _, r := range arg[[]*model.RuleSet](c, 0) {
			if r != nil && model.IsCustomRuleSet(r) {
				custom[r.Tag] = true
			}
		}
		var want []map[string]any
		if json.Unmarshal(c.R, &want) != nil {
			return c.R
		}
		for _, o := range want {
			if value, _ := o["value"].(string); custom[value] && o["source"] == "imported" {
				o["label"] = value + " · 自建"
			}
		}
		raw, _ := json.Marshal(want)
		return raw
	},
}}

// normalizeOrder hides the order of entries derived from a match's keys:
// the TypeScript kept insertion order, a decoded Go map has none (the
// port uses a stable order).
func normalizeOrder(fn string, v any) any {
	switch fn {
	case "route-policy.legacyCondition", "route-policy.setConditionMode":
		return tsgolden.SortLeaves(v)
	case "route-policy.ruleSummary", "route-policy.conditionSummary", "network-config.networkSummary":
		if s, ok := v.(string); ok {
			return tsgolden.SortSummary(s)
		}
	}
	return v
}

func arg[T any](c tsgolden.Call, i int) T {
	var v T
	c.Arg(i, &v)
	return v
}

func argOr[T any](c tsgolden.Call, i int, fallback T) T {
	v := fallback
	c.Arg(i, &v)
	return v
}

// ref reads an id argument that may be null.
func ref(c tsgolden.Call, i int) model.Ref {
	var r model.Ref
	c.Arg(i, &r)
	return r
}

// docArg reads a document argument; a document without ipv6Enabled
// infers it, as fillDefaults does.
func docArg(c tsgolden.Call, i int) *model.Doc {
	return arg[*model.Doc](c, i)
}

type network struct {
	Bytes  []int `json:"bytes"`
	Prefix int   `json:"prefix"`
}

type valueOrError[T any] struct {
	Value *T     `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

type migratedDoc struct {
	Doc            *model.Doc             `json:"doc"`
	NetworkConfigs []*model.NetworkConfig `json:"networkConfigs"`
}

type migratedStore struct {
	Doc            *model.Doc             `json:"doc"`
	Profiles       []*model.Profile       `json:"profiles"`
	NetworkConfigs []*model.NetworkConfig `json:"networkConfigs"`
}

type expanded struct {
	Doc    *model.Doc `json:"doc"`
	Errors []string   `json:"errors"`
}

type networkUse struct {
	ProfileID string `json:"profileId"`
	EntityID  string `json:"entityId"`
	Label     string `json:"label"`
}

type processTag struct {
	Source   string `json:"source"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	Label    string `json:"label,omitempty"`
	Field    string `json:"field"`
	Path     []int  `json:"path"`
	Index    int    `json:"index"`
	Excluded bool   `json:"excluded"`
}

type builtinRuleSet struct {
	Tag      string `json:"tag"`
	Path     string `json:"path"`
	FileName string `json:"fileName"`
	Desc     string `json:"desc"`
}

type ruleSetOption struct {
	Value  string `json:"value"`
	Label  string `json:"label"`
	Source string `json:"source"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func routeRulesArg(c tsgolden.Call, i int) []*model.RouteRule { return arg[[]*model.RouteRule](c, i) }

// isDNSRules reports whether rule list argument i holds DNS rules.
func isDNSRules(c tsgolden.Call, i int) bool {
	var raw []map[string]json.RawMessage
	c.Arg(i, &raw)
	for _, r := range raw {
		if _, ok := r["server"]; ok {
			return true
		}
		if _, ok := r["outbound"]; ok {
			return false
		}
	}
	return false
}

var modelAdapters = map[string]tsgolden.Adapter{
	// route-policy.ts
	"route-policy.isSystemRouteRule": func(c tsgolden.Call) (any, error) {
		return model.IsSystemRouteRule(arg[*model.RouteRule](c, 0)), nil
	},
	"route-policy.migrateRoutePolicy": func(c tsgolden.Call) (any, error) {
		return model.MigrateRoutePolicy(arg[model.Route](c, 0)), nil
	},
	"route-policy.legacyCondition": func(c tsgolden.Call) (any, error) {
		return model.LegacyCondition(arg[map[string]any](c, 0), arg[string](c, 1)), nil
	},
	"route-policy.conditionSummary": func(c tsgolden.Call) (any, error) {
		return model.ConditionSummary(arg[*model.Condition](c, 0)), nil
	},
	"route-policy.ruleSummary": func(c tsgolden.Call) (any, error) {
		r := arg[*model.RouteRule](c, 0)
		return model.RuleSummary(r.Match, r.ConditionTree, r.Logic), nil
	},
	"route-policy.parseNetwork": func(c tsgolden.Call) (any, error) {
		n, ok := model.ParseNetwork(arg[string](c, 0))
		if !ok {
			return (*network)(nil), nil
		}
		out := &network{Prefix: n.Prefix}
		for _, b := range n.Bytes {
			out.Bytes = append(out.Bytes, int(b))
		}
		return out, nil
	},
	"route-policy.networksOverlap": func(c tsgolden.Call) (any, error) {
		return model.NetworksOverlap(arg[string](c, 0), arg[string](c, 1)), nil
	},
	"route-policy.validateDevice": func(c tsgolden.Call) (any, error) {
		return model.ValidateDevice(arg[*model.RouteDevice](c, 0), arg[[]*model.RouteDevice](c, 1)), nil
	},
	"route-policy.validateCondition": func(c tsgolden.Call) (any, error) {
		return model.ValidateCondition(arg[*model.Condition](c, 0), arg[bool](c, 1)), nil
	},
	"route-policy.movePolicyRule": func(c tsgolden.Call) (any, error) {
		id, group, target, position := arg[string](c, 1), arg[string](c, 2), arg[string](c, 3), arg[string](c, 4)
		if isDNSRules(c, 0) {
			return model.MovePolicyRule(arg[[]*model.DNSRule](c, 0), id, group, target, position), nil
		}
		return model.MovePolicyRule(routeRulesArg(c, 0), id, group, target, position), nil
	},
	"route-policy.setConditionField": func(c tsgolden.Call) (any, error) {
		return model.SetConditionField(arg[*model.Condition](c, 0), arg[string](c, 1), arg[string](c, 2), arg[string](c, 3), arg[any](c, 4), arg[bool](c, 5)), nil
	},
	"route-policy.setPortConditionField": func(c tsgolden.Call) (any, error) {
		return model.SetPortConditionField(arg[*model.Condition](c, 0), arg[string](c, 1), arg[string](c, 2), arg[any](c, 3), arg[bool](c, 4)), nil
	},
	"route-policy.setConditionMode": func(c tsgolden.Call) (any, error) {
		return model.SetConditionMode(arg[*model.Condition](c, 0), arg[string](c, 1)), nil
	},
	"route-policy.localSourceAddresses": func(c tsgolden.Call) (any, error) {
		return model.LocalSourceAddresses(docArg(c, 0), arg[[]string](c, 1)), nil
	},
	"route-policy.routeSourceScope": func(c tsgolden.Call) (any, error) {
		return model.RouteSourceScope(docArg(c, 0), arg[string](c, 1), arg[string](c, 2)), nil
	},

	// device-policy.ts
	"device-policy.setDnsFinal": func(c tsgolden.Call) (any, error) {
		return model.SetDNSFinal(docArg(c, 0), arg[string](c, 1), ref(c, 2))
	},
	"device-policy.setDnsFinalDetour": func(c tsgolden.Call) (any, error) {
		return model.SetDNSFinalDetour(docArg(c, 0), arg[string](c, 1), ref(c, 2))
	},
	"device-policy.migrateDeviceDNS": func(c tsgolden.Call) (any, error) {
		return model.MigrateDeviceDNS(docArg(c, 0)), nil
	},
	"device-policy.orderedDeviceRules": func(c tsgolden.Call) (any, error) {
		groups, owner := arg[[]*model.RouteGroup](c, 1), arg[string](c, 2)
		if isDNSRules(c, 0) {
			return model.OrderedDeviceRules(arg[[]*model.DNSRule](c, 0), groups, owner), nil
		}
		return model.OrderedDeviceRules(routeRulesArg(c, 0), groups, owner), nil
	},
	"device-policy.copyDevicePolicy": func(c tsgolden.Call) (any, error) {
		var o struct {
			Section, Source, Target, RuleID string
			IncludeDefault                  bool
		}
		c.Arg(1, &o)
		return model.CopyDevicePolicy(docArg(c, 0), model.CopyPolicyOptions{Section: model.PolicySection(o.Section), Source: o.Source, Target: o.Target, RuleID: o.RuleID, IncludeDefault: o.IncludeDefault})
	},
	"device-policy.moveDevicePolicy": func(c tsgolden.Call) (any, error) {
		var o struct{ Section, Source, Target, RuleID string }
		c.Arg(1, &o)
		return model.MoveDevicePolicy(docArg(c, 0), model.MovePolicyOptions{Section: model.PolicySection(o.Section), Source: o.Source, Target: o.Target, RuleID: o.RuleID})
	},
	"device-policy.removePolicyDevice": func(c tsgolden.Call) (any, error) {
		return model.RemovePolicyDevice(docArg(c, 0), arg[string](c, 1))
	},
	"device-policy.addLocalPolicyDevice": func(c tsgolden.Call) (any, error) {
		return model.AddLocalPolicyDevice(docArg(c, 0)), nil
	},

	// dns-policy.ts
	"dns-policy.dnsDetourError": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.DNSDetourError(docArg(c, 0), arg[string](c, 1))), nil
	},
	"dns-policy.migrateDnsDetours": func(c tsgolden.Call) (any, error) {
		return model.MigrateDNSDetours(docArg(c, 0)), nil
	},

	// network-config.ts
	"network-config.networkSummary": func(c tsgolden.Call) (any, error) {
		return model.NetworkSummary(arg[*model.NetworkConfig](c, 0)), nil
	},
	"network-config.validateNetworkConfig": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.ValidateNetworkConfig(arg[*model.NetworkConfig](c, 0))), nil
	},
	"network-config.migrateNetworkDoc": func(c tsgolden.Call) (any, error) {
		doc, library := model.MigrateNetworkDoc(docArg(c, 0), arg[[]*model.NetworkConfig](c, 1), argOr(c, 2, model.DefaultNetworkProfileName))
		return migratedDoc{doc, library}, nil
	},
	"network-config.migrateNetworkStore": func(c tsgolden.Call) (any, error) {
		doc, profiles, library := model.MigrateNetworkStore(docArg(c, 0), arg[[]*model.Profile](c, 1), arg[[]*model.NetworkConfig](c, 2), arg[string](c, 3))
		return migratedStore{doc, profiles, library}, nil
	},
	"network-config.networkUsage": func(c tsgolden.Call) (any, error) {
		out := []networkUse{}
		for _, u := range model.NetworkUses(arg[[]*model.Profile](c, 0), arg[string](c, 1)) {
			out = append(out, networkUse{u.ProfileID, u.EntityID, u.Label})
		}
		return out, nil
	},
	"network-config.importNetworkBundle": func(c tsgolden.Call) (any, error) {
		profiles, library, err := model.ImportNetworkBundle(arg[[]*model.Profile](c, 0), arg[[]*model.NetworkConfig](c, 1), arg[[]*model.NetworkConfig](c, 2))
		if err != nil {
			return nil, err
		}
		return migratedStore{Profiles: profiles, NetworkConfigs: library}, nil
	},
	"network-config.expandNetworkDoc": func(c tsgolden.Call) (any, error) {
		doc, errs := model.ExpandNetworkDoc(docArg(c, 0), arg[[]*model.NetworkConfig](c, 1))
		return expanded{doc, errs}, nil
	},
	"network-config.rebaseNetworkConfigs": func(c tsgolden.Call) (any, error) {
		return model.RebaseNetworkConfigs(arg[[]*model.NetworkConfig](c, 0), arg[[]*model.NetworkConfig](c, 1), arg[[]*model.NetworkConfig](c, 2)), nil
	},

	// reference-status.ts
	"reference-status.isOutboundUsable": func(c tsgolden.Call) (any, error) {
		return model.IsOutboundUsable(docArg(c, 0), ref(c, 1).ID()), nil
	},
	"reference-status.isInboundUsable": func(c tsgolden.Call) (any, error) {
		return model.IsInboundUsable(docArg(c, 0), ref(c, 1).ID()), nil
	},
	"reference-status.isDnsServerUsable": func(c tsgolden.Call) (any, error) {
		return model.IsDNSServerUsable(docArg(c, 0), ref(c, 1).ID()), nil
	},
	"reference-status.hasInvalidDnsTarget": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidDNSTarget(docArg(c, 0), ref(c, 1).ID(), ref(c, 2).ID()), nil
	},
	"reference-status.hasInvalidOutboundReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidOutboundReferences(docArg(c, 0), arg[*model.Outbound](c, 1), arg[[]*model.NetworkConfig](c, 2)), nil
	},
	"reference-status.hasInvalidEndpointReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidEndpointReferences(docArg(c, 0), arg[*model.Endpoint](c, 1), arg[[]*model.NetworkConfig](c, 2)), nil
	},
	"reference-status.hasInvalidServiceReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidServiceReferences(docArg(c, 0), arg[*model.Service](c, 1), arg[[]*model.NetworkConfig](c, 2)), nil
	},
	"reference-status.hasInvalidInboundReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidInboundReferences(docArg(c, 0), arg[*model.Inbound](c, 1), arg[[]*model.NetworkConfig](c, 2)), nil
	},
	"reference-status.hasInvalidRouteRuleReference": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidRouteRuleReference(docArg(c, 0), arg[*model.RouteRule](c, 1)), nil
	},
	"reference-status.hasInvalidDnsServerReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidDNSServerReferences(docArg(c, 0), arg[*model.DNSServer](c, 1), arg[[]*model.NetworkConfig](c, 2)), nil
	},
	"reference-status.hasInvalidDnsRuleReference": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidDNSRuleReference(docArg(c, 0), arg[*model.DNSRule](c, 1)), nil
	},
	"reference-status.hasInvalidSubscriptionReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidSubscriptionReferences(docArg(c, 0), arg[*model.Subscription](c, 1)), nil
	},
	"reference-status.hasInvalidRuleSetReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidRuleSetReferences(docArg(c, 0), arg[*model.RuleSet](c, 1)), nil
	},
	"reference-status.hasInvalidRouteGlobalReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidRouteGlobalReferences(docArg(c, 0)), nil
	},
	"reference-status.hasInvalidDnsGlobalReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidDNSGlobalReferences(docArg(c, 0)), nil
	},
	"reference-status.hasInvalidRuleReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidRuleReferences(docArg(c, 0), arg[*model.RouteRule](c, 1)), nil
	},
	"reference-status.hasInvalidDnsRuleReferences": func(c tsgolden.Call) (any, error) {
		return model.HasInvalidDNSRuleReferences(docArg(c, 0), arg[*model.DNSRule](c, 1)), nil
	},
	"reference-status.hasBlockingReferenceWarnings": func(c tsgolden.Call) (any, error) {
		return model.HasBlockingReferenceWarnings(docArg(c, 0), arg[[]*model.NetworkConfig](c, 1)), nil
	},

	// dns-server.ts, dns-address.ts, default-dns.ts
	"dns-server.dnsServerForType": func(c tsgolden.Call) (any, error) {
		return model.DNSServerForType(arg[*model.DNSServer](c, 0), arg[string](c, 1)), nil
	},
	"dns-server.withFakeIpDefaults": func(c tsgolden.Call) (any, error) {
		return model.WithFakeIPDefaults(arg[*model.DNSServer](c, 0)), nil
	},
	"dns-server.fakeIpRangeError": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.FakeIPRangeError(arg[*model.DNSServer](c, 0))), nil
	},
	"dns-server.fakeIpServerError": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.FakeIPServerError(arg[[]*model.DNSServer](c, 0), ref(c, 1))), nil
	},
	"dns-address.parseDnsAddress": func(c tsgolden.Call) (any, error) {
		v, problem := model.ParseDNSAddress(arg[string](c, 0), arg[string](c, 1))
		if problem != "" {
			return valueOrError[model.ParsedDNSAddress]{Error: problem}, nil
		}
		return valueOrError[model.ParsedDNSAddress]{Value: &v}, nil
	},
	"dns-address.formatDnsAddress": func(c tsgolden.Call) (any, error) {
		return model.FormatDNSAddress(arg[*model.DNSServer](c, 0)), nil
	},
	"dns-address.dnsAddressPatch": func(c tsgolden.Call) (any, error) {
		s := model.DNSAddressPatch(arg[*model.DNSServer](c, 0), arg[model.ParsedDNSAddress](c, 1))
		return struct {
			Type              string                  `json:"type"`
			Server            string                  `json:"server"`
			ServerPort        *int                    `json:"serverPort,omitempty"`
			Path              string                  `json:"path,omitempty"`
			Fields            model.Fields            `json:"fields"`
			NetworkConfigRefs model.NetworkConfigRefs `json:"networkConfigRefs,omitempty"`
		}{s.Type, s.Server, s.ServerPort, s.Path, s.Fields, s.NetworkConfigRefs}, nil
	},
	"default-dns.defaultDnsServers": func(c tsgolden.Call) (any, error) {
		local, ali := model.DefaultDNSServers(arg[string](c, 0), arg[string](c, 1))
		return struct {
			Local *model.DNSServer `json:"local"`
			Ali   *model.DNSServer `json:"ali"`
		}{local, ali}, nil
	},

	// endpoints.ts
	"endpoints.wireguardOutboundToEndpoint": func(c tsgolden.Call) (any, error) {
		return model.WireguardOutboundToEndpoint(arg[*model.Outbound](c, 0)), nil
	},
	"endpoints.migrateWireGuardOutbounds": func(c tsgolden.Call) (any, error) {
		return model.MigrateWireGuardOutbounds(docArg(c, 0)), nil
	},

	// process-conditions.ts, process-path.ts
	"process-conditions.processSelectionLeaf": func(c tsgolden.Call) (any, error) {
		return model.ProcessSelectionLeaf(arg[model.ProcessSelection](c, 0)), nil
	},
	"process-conditions.normalizeProcessConditions": func(c tsgolden.Call) (any, error) {
		return model.NormalizeProcessConditions(arg[*model.Condition](c, 0)), nil
	},
	"process-conditions.processTags": func(c tsgolden.Call) (any, error) {
		return model.ProcessTags(arg[*model.Condition](c, 0)), nil
	},
	"process-conditions.changeProcessTag": func(c tsgolden.Call) (any, error) {
		var replacement *model.ProcessSelection
		if c.Has(2) {
			replacement = new(model.ProcessSelection)
			c.Arg(2, replacement)
		}
		return model.ChangeProcessTag(arg[*model.Condition](c, 0), arg[model.ProcessTag](c, 1), replacement), nil
	},
	"process-conditions.addProcessSelection": func(c tsgolden.Call) (any, error) {
		return model.AddProcessSelection(arg[*model.Condition](c, 0), arg[model.ProcessSelection](c, 1)), nil
	},
	"process-path.processPathRegex": func(c tsgolden.Call) (any, error) {
		return model.ProcessPathRegex(arg[string](c, 0), arg[bool](c, 1)), nil
	},

	// typed-match.ts, ip-version.ts
	"typed-match.matchExclusion": func(c tsgolden.Call) (any, error) {
		excluded, mixed := model.MatchExclusion(arg[*model.Condition](c, 0), arg[string](c, 1))
		return struct {
			Excluded bool `json:"excluded"`
			Mixed    bool `json:"mixed"`
		}{excluded, mixed}, nil
	},
	"typed-match.setMatchExclusion": func(c tsgolden.Call) (any, error) {
		return model.SetMatchExclusion(arg[*model.Condition](c, 0), arg[string](c, 1), arg[bool](c, 2)), nil
	},
	"typed-match.normalizeTypedMatches": func(c tsgolden.Call) (any, error) {
		return model.NormalizeTypedMatches(arg[*model.Condition](c, 0)), nil
	},
	"typed-match.matchTokens": func(c tsgolden.Call) (any, error) {
		return model.MatchTokens(arg[*model.Condition](c, 0), arg[string](c, 1)), nil
	},
	"typed-match.removeMatchToken": func(c tsgolden.Call) (any, error) {
		return model.RemoveMatchToken(arg[*model.Condition](c, 0), arg[model.MatchToken](c, 1)), nil
	},
	"typed-match.addMatchValues": func(c tsgolden.Call) (any, error) {
		return model.AddMatchValues(arg[*model.Condition](c, 0), arg[string](c, 1), arg[[]model.MatchValue](c, 2)), nil
	},
	"ip-version.ipVersionSelection": func(c tsgolden.Call) (any, error) {
		versions, excluded := model.IPVersionSelection(arg[*model.Condition](c, 0))
		return struct {
			Values   []int `json:"values"`
			Excluded bool  `json:"excluded"`
		}{versions, excluded}, nil
	},
	"ip-version.setIpVersions": func(c tsgolden.Call) (any, error) {
		group := arg[*model.Condition](c, 0)
		_, excluded := model.IPVersionSelection(group)
		c.Arg(2, &excluded)
		return model.SetIPVersions(group, arg[[]int](c, 1), excluded), nil
	},

	// match-input.ts
	"match-input.parsePortInput": func(c tsgolden.Call) (any, error) {
		entries, ok := model.ParsePortInput(arg[string](c, 0))
		if !ok {
			return []model.PortEntry(nil), nil
		}
		return entries, nil
	},
	"match-input.canonicalNetwork": func(c tsgolden.Call) (any, error) {
		v, ok := model.CanonicalNetwork(arg[string](c, 0), argOr(c, 1, -1))
		if !ok {
			return (*string)(nil), nil
		}
		return &v, nil
	},
	"match-input.addressSuggestions": func(c tsgolden.Call) (any, error) {
		return model.AddressSuggestions(arg[string](c, 0)), nil
	},

	// proxy-address.ts
	"proxy-address.proxyAddressPatch": func(c tsgolden.Call) (any, error) {
		fields, ok := model.ProxyAddressPatch(arg[*model.Outbound](c, 0), arg[string](c, 1))
		if !ok {
			return (*struct {
				Fields model.Fields `json:"fields"`
			})(nil), nil
		}
		return &struct {
			Fields model.Fields `json:"fields"`
		}{fields}, nil
	},

	// rulesets.ts
	"rulesets.builtinRuleSetForEntry": func(c tsgolden.Call) (any, error) {
		b, ok := model.BuiltinRuleSetForEntry(arg[*model.RuleSet](c, 0))
		if !ok {
			return (*builtinRuleSet)(nil), nil
		}
		return &builtinRuleSet{b.Tag, b.Path, b.FileName, b.Desc}, nil
	},
	"rulesets.defaultBuiltinRuleSet": func(c tsgolden.Call) (any, error) {
		b := arg[builtinRuleSet](c, 0)
		return model.DefaultBuiltinRuleSet(model.BuiltinRuleSet{Tag: b.Tag, Path: b.Path, FileName: b.FileName, Desc: b.Desc}), nil
	},
	"rulesets.builtinRuleSetUrl": func(c tsgolden.Call) (any, error) {
		return optional(model.BuiltinRuleSetURL(arg[string](c, 0), arg[string](c, 1))), nil
	},
	"rulesets.customLocalRuleSetPath": func(c tsgolden.Call) (any, error) {
		return model.CustomLocalRuleSetPath(arg[*model.RuleSet](c, 0)), nil
	},
	"rulesets.materializeBuiltinRuleSets": func(c tsgolden.Call) (any, error) {
		return model.MaterializeBuiltinRuleSets(arg[[]*model.RuleSet](c, 0), arg[[]string](c, 1)), nil
	},
	"rulesets.androidLocalRuleSetPayload": func(c tsgolden.Call) (any, error) {
		return struct {
			Version int                      `json:"version"`
			Files   []model.LocalRuleSetFile `json:"files"`
		}{1, model.LocalRuleSetPayload(docArg(c, 0))}, nil
	},
	"rulesets.ruleSetOptions": func(c tsgolden.Call) (any, error) {
		var out []ruleSetOption
		for _, o := range model.RuleSetOptions(arg[[]*model.RuleSet](c, 0)) {
			out = append(out, ruleSetOption{o.Value, o.Label, o.Source})
		}
		return out, nil
	},
	"rulesets.ruleSetValues": func(c tsgolden.Call) (any, error) {
		return model.RuleSetValues(arg[any](c, 0)), nil
	},
	"rulesets.usedRuleSetTags": func(c tsgolden.Call) (any, error) {
		return model.UsedRuleSetTags(docArg(c, 0)), nil
	},
	"rulesets.validateRuleSetUrl": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.ValidateRuleSetURL(arg[string](c, 0), arg[string](c, 1))), nil
	},
	"rulesets.updateRuleSetSource": func(c tsgolden.Call) (any, error) {
		var patch json.RawMessage
		c.Arg(1, &patch)
		return model.UpdateRuleSetSource(arg[*model.RuleSet](c, 0), func(r *model.RuleSet) {
			if err := json.Unmarshal(patch, r); err != nil {
				panic(err)
			}
		}), nil
	},
	"rulesets.validateRuleSetUpdateInterval": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.ValidateRuleSetUpdateInterval(arg[string](c, 0))), nil
	},

	// lan-devices.ts
	"lan-devices.deviceValues": func(c tsgolden.Call) (any, error) {
		return model.DeviceValues(arg[any](c, 0)), nil
	},
	"lan-devices.normalizeMAC": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.NormalizeMAC(arg[string](c, 0))), nil
	},
	"lan-devices.deviceKey": func(c tsgolden.Call) (any, error) {
		return model.DeviceKey(arg[string](c, 0), arg[string](c, 1)), nil
	},
	"lan-devices.deviceName": func(c tsgolden.Call) (any, error) {
		return model.DeviceName(arg[string](c, 0), arg[string](c, 1), arg[map[string]model.DeviceLabel](c, 2), arg[[]model.DiscoveredDevice](c, 3)), nil
	},
	"lan-devices.convertDeviceMode": func(c tsgolden.Call) (any, error) {
		values, missing := model.ConvertDeviceMode(arg[[]string](c, 0), arg[string](c, 1), arg[string](c, 2), arg[[]model.DiscoveredDevice](c, 3))
		return struct {
			Values  []string `json:"values"`
			Missing []string `json:"missing"`
		}{values, missing}, nil
	},
	"lan-devices.snapshotDeviceLabels": func(c tsgolden.Call) (any, error) {
		return model.SnapshotDeviceLabels(arg[string](c, 0), arg[[]string](c, 1), arg[map[string]model.DeviceLabel](c, 2), arg[[]model.DiscoveredDevice](c, 3)), nil
	},

	// tls-config.ts
	"tls-config.flattenTlsFields": func(c tsgolden.Call) (any, error) {
		return model.FlattenTLSFields(arg[model.Fields](c, 0)), nil
	},
	"tls-config.nestTlsFields": func(c tsgolden.Call) (any, error) {
		return model.NestTLSFields(arg[model.Fields](c, 0)), nil
	},

	// usbip-guide.ts, group-members.ts, tags.ts, runtime-platform.ts
	"usbip-guide.isWildcardHost":   func(c tsgolden.Call) (any, error) { return model.IsWildcardHost(arg[string](c, 0)), nil },
	"usbip-guide.isLoopbackHost":   func(c tsgolden.Call) (any, error) { return model.IsLoopbackHost(arg[string](c, 0)), nil },
	"usbip-guide.isTailnetHost":    func(c tsgolden.Call) (any, error) { return model.IsTailnetHost(arg[string](c, 0)), nil },
	"usbip-guide.isPrivateLanHost": func(c tsgolden.Call) (any, error) { return model.IsPrivateLanHost(arg[string](c, 0)), nil },
	"group-members.stableGroupMemberOrder": func(c tsgolden.Call) (any, error) {
		return model.StableGroupMemberOrder(arg[[]*model.Outbound](c, 0), func(o *model.Outbound) string { return o.ID }, arg[[]string](c, 1)), nil
	},
	"group-members.matchesMemberSource": func(c tsgolden.Call) (any, error) {
		return model.MatchesMemberSource(arg[*model.Outbound](c, 0), arg[string](c, 1)), nil
	},
	"tags.tagValidationError": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.TagValidationError(docArg(c, 0), arg[string](c, 1), arg[string](c, 2))), nil
	},
	"tags.serviceTagValidationError": func(c tsgolden.Call) (any, error) {
		return nilIfEmpty(model.ServiceTagValidationError(docArg(c, 0), arg[string](c, 1), arg[string](c, 2))), nil
	},
	"runtime-platform.serverPlatform": func(c tsgolden.Call) (any, error) {
		return model.ServerPlatform(arg[string](c, 0)), nil
	},
	"runtime-platform.projectRuntimeDoc": func(c tsgolden.Call) (any, error) {
		return model.ProjectRuntimeDoc(docArg(c, 0), arg[string](c, 1)), nil
	},

	// store.tsx
	"store-pure.fillDefaults": func(c tsgolden.Call) (any, error) {
		raw := c.Args[0]
		if doc := model.DecodeDoc(raw); doc != nil {
			return doc, nil
		}
		return model.FillDefaults(docArg(c, 0)), nil
	},
	"store-pure.emptyDoc": func(c tsgolden.Call) (any, error) {
		return model.EmptyDoc(arg[string](c, 0)), nil
	},
	"store-pure.makeInbound": func(c tsgolden.Call) (any, error) {
		return model.MakeInbound(arg[string](c, 0), arg[string](c, 1), arg[string](c, 2), arg[bool](c, 3)), nil
	},
	"store-pure.makeOutbound": func(c tsgolden.Call) (any, error) {
		return model.MakeOutbound(arg[string](c, 0), arg[string](c, 1)), nil
	},
	"store-pure.makeEndpoint": func(c tsgolden.Call) (any, error) {
		return model.MakeEndpoint(arg[string](c, 0), arg[string](c, 1), arg[bool](c, 2)), nil
	},
	"store-pure.makeService": func(c tsgolden.Call) (any, error) {
		return model.MakeService(arg[string](c, 0), arg[string](c, 1)), nil
	},
}

// recordedDetours reports a recorded document with a detour of the kind
// MigrateProxyChains moves into a chain.
func recordedDetours(raw json.RawMessage) bool {
	var doc struct {
		Outbounds     []struct{ Fields map[string]any } `json:"outbounds"`
		Endpoints     []struct{ Fields map[string]any } `json:"endpoints"`
		Subscriptions []map[string]any                  `json:"subscriptions"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for _, o := range doc.Outbounds {
		if _, ok := o.Fields["detour"]; ok {
			return true
		}
	}
	for _, e := range doc.Endpoints {
		if _, ok := e.Fields["detour"]; ok {
			return true
		}
	}
	for _, s := range doc.Subscriptions {
		if _, ok := s["detour"]; ok {
			return true
		}
	}
	return false
}
