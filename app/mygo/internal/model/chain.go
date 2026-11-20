package model

import (
	"fmt"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds proxy chains: the outbounds an outbound or endpoint
// dials through, in the order traffic takes them. The emitter gives every
// hop after the first a copy of its own whose detour is the hop before
// it, so each server is defined once however many chains pass through it.

// MigrateProxyChains moves the former single detour of outbounds,
// endpoints and subscriptions into their Chain. A document without one
// is returned as it is; otherwise input is left unchanged.
func MigrateProxyChains(input *Doc) *Doc {
	if !hasLegacyDetours(input) {
		return input
	}
	doc := Clone(input)
	for _, o := range doc.Outbounds {
		o.Chain = migrateDetourField(o.Chain, o.Fields, OutboundTakesChain(o.Type))
	}
	for _, e := range doc.Endpoints {
		e.Chain = migrateDetourField(e.Chain, e.Fields, EndpointTakesChain(e.Type))
	}
	for _, s := range doc.Subscriptions {
		if s.Detour.Valid() && len(s.Chain) == 0 {
			s.Chain = []string{s.Detour.ID()}
		}
		s.Detour = Ref{}
	}
	return doc
}

func hasLegacyDetours(doc *Doc) bool {
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
		if s.Detour.Set() {
			return true
		}
	}
	return false
}

// migrateDetourField takes the detour out of fields, returning the chain
// it starts when chain has none yet and the type takes one; a type
// without a detour never emitted it.
func migrateDetourField(chain []string, fields Fields, takes bool) []string {
	value, ok := fields["detour"]
	if !ok {
		return chain
	}
	delete(fields, "detour")
	if id := jsv.String(value); takes && jsv.Truthy(value) && id != "" && len(chain) == 0 {
		return []string{id}
	}
	return chain
}

// OutboundTakesChain reports whether outbounds of typ dial through a
// proxy chain: servers whose dialer takes a detour.
func OutboundTakesChain(typ string) bool {
	return !IsGroup(typ) && hasDetourField(schema.OutboundSchema[typ])
}

// EndpointTakesChain reports whether endpoints of typ dial through a proxy
// chain.
func EndpointTakesChain(typ string) bool { return hasDetourField(schema.EndpointSchema[typ]) }

func hasDetourField(table []schema.Field) bool {
	return slices.ContainsFunc(table, func(f schema.Field) bool { return f.Key == "detour" })
}

// ChainOf is the proxy chain of the outbound or endpoint id, nil for
// none.
func ChainOf(doc *Doc, id string) []string {
	t := FindOutboundTarget(doc, id)
	switch {
	case t.Outbound != nil:
		return t.Outbound.Chain
	case t.Endpoint != nil:
		return t.Endpoint.Chain
	}
	return nil
}

// noChainHop are the outbound types that dial nothing a chain could pass
// through.
var noChainHop = []string{"direct", "block", "dns"}

// ChainMiddleHop reports whether t can stand after the first hop of a
// chain: a server whose dialer takes a detour, which its copy points at
// the hop before it. Groups and endpoints can only be the first hop.
func ChainMiddleHop(t Target) bool {
	o := t.Outbound
	return o != nil && !slices.Contains(noChainHop, o.Type) && OutboundTakesChain(o.Type)
}

// ChainFirstHop reports whether t can be the first hop of a chain: any
// outbound, group or endpoint except those that dial nothing.
func ChainFirstHop(t Target) bool {
	if t.Endpoint != nil {
		return true
	}
	return t.Outbound != nil && !slices.Contains(noChainHop, t.Outbound.Type)
}

// ChainIssue is what is wrong with a chain: Hop is the index of the hop
// at fault, -1 for the chain as a whole.
type ChainIssue struct {
	Hop     int
	Message string
}

// ChainError checks chain for an outbound or endpoint among owners (the
// one edited, or every node of a subscription): each hop exists, is
// enabled and may stand where it is, none repeats or is an owner, and
// the chain does not lead back to an owner. ok is false with the first
// issue found.
func ChainError(doc *Doc, owners []string, chain []string) (issue ChainIssue, ok bool) {
	isOwner := func(id string) bool { return slices.Contains(owners, id) }
	seen := map[string]bool{}
	for i, id := range chain {
		t := FindOutboundTarget(doc, id)
		name := chainHopName(t, id)
		switch {
		case isOwner(id):
			return ChainIssue{i, "不能把本出站加入自己的代理链"}, false
		case seen[id]:
			return ChainIssue{i, "「" + name + "」在链中重复出现"}, false
		case !t.Valid():
			return ChainIssue{i, fmt.Sprintf("第 %d 跳已删除，请移除或重新选择", i+1)}, false
		case !IsOutboundUsable(doc, id):
			return ChainIssue{i, "「" + name + "」已停用，请启用它或重新选择"}, false
		case i == 0 && !ChainFirstHop(t):
			return ChainIssue{i, "「" + name + "」不能作为代理链的一跳"}, false
		case i > 0 && !ChainMiddleHop(t):
			return ChainIssue{i, "第 2 跳起只能选择具体的服务器，「" + name + "」" + chainHopKind(t)}, false
		}
		seen[id] = true
	}
	if path := chainCycle(doc, owners, chain); path != nil {
		return ChainIssue{-1, "循环依赖：" + strings.Join(path, " → ") + "，请调整代理链"}, false
	}
	return ChainIssue{}, true
}

func chainHopName(t Target, id string) string {
	if t.Valid() {
		return t.Tag()
	}
	return id
}

func chainHopKind(t Target) string {
	switch {
	case t.Endpoint != nil:
		return "是端点"
	case t.Outbound != nil && IsGroup(t.Outbound.Type):
		return "是策略组"
	}
	return "不能转发连接"
}

// chainCycle finds a path by which chain leads back to an owner, the
// names along it starting at the owner, nil for none. The first hop is
// used as it is, so its own chain, group members and references count;
// later hops are copies that keep their references but not their chain.
func chainCycle(doc *Doc, owners []string, chain []string) []string {
	if len(chain) == 0 || len(owners) == 0 {
		return nil
	}
	ownerName := chainHopName(FindOutboundTarget(doc, owners[0]), owners[0])
	parent := map[string]string{}
	var found string
	var visit func(id, from string, full bool) bool
	visited := map[string]bool{}
	visit = func(id, from string, full bool) bool {
		if slices.Contains(owners, id) {
			parent[id+"\x00end"] = from
			found = id
			return true
		}
		key := id
		if !full {
			key = id + "\x00copy"
		}
		if visited[key] {
			return false
		}
		visited[key] = true
		parent[key] = from
		t := FindOutboundTarget(doc, id)
		if !t.Valid() {
			return false
		}
		var deps []string
		var copies []string
		if t.Outbound != nil && IsGroup(t.Outbound.Type) {
			deps = append(deps, GroupMembers(doc, t.Outbound)...)
		}
		deps = append(deps, chainRefs(doc, t)...)
		if full {
			own := ChainOf(doc, id)
			if len(own) > 0 {
				deps = append(deps, own[0])
				copies = append(copies, own[1:]...)
			}
		}
		for _, dep := range deps {
			if visit(dep, key, true) {
				return true
			}
		}
		for _, dep := range copies {
			if visit(dep, key, false) {
				return true
			}
		}
		return false
	}
	start := "\x00start"
	if !visit(chain[0], start, true) {
		for _, hop := range chain[1:] {
			if visit(hop, start, false) {
				break
			}
		}
	}
	if found == "" {
		return nil
	}
	var names []string
	for key := parent[found+"\x00end"]; key != start && key != ""; key = parent[key] {
		id, _, _ := strings.Cut(key, "\x00")
		names = append(names, chainHopName(FindOutboundTarget(doc, id), id))
	}
	slices.Reverse(names)
	return append(append([]string{ownerName}, names...), ownerName)
}

// chainRefs are the outbounds a target's own fields refer to, apart from
// the detour a chain replaces: the reality handshake's detour and the
// like, which a copy keeps.
func chainRefs(doc *Doc, t Target) []string {
	var fields Fields
	var table []schema.Field
	switch {
	case t.Endpoint != nil:
		fields, table = t.Endpoint.Fields, schema.EndpointSchema[t.Endpoint.Type]
	case t.Outbound != nil && !IsGroup(t.Outbound.Type):
		fields, table = t.Outbound.Fields, schema.OutboundSchema[t.Outbound.Type]
	default:
		return nil
	}
	platform := doc.Platform
	if platform == "" {
		platform = PlatformLinux
	}
	var refs []string
	for _, field := range table {
		if field.Type != schema.Ref || field.Key == "detour" || field.Key == "default_ref" ||
			!schema.FieldAvailable(field, platform) || !schema.FieldVisible(field, table, fields) {
			continue
		}
		if dependency := fields[field.Key]; jsv.Truthy(dependency) {
			refs = append(refs, jsv.String(dependency))
		}
	}
	return refs
}

// noUDPHop are outbound types that carry no UDP, so neither does a chain
// through them.
var noUDPHop = []string{"http", "ssh", "naive", "shadowtls", "tor"}

// ChainWithoutUDP returns the hops of chain that carry no UDP, by tag.
func ChainWithoutUDP(doc *Doc, chain []string) []string {
	var out []string
	for _, id := range chain {
		if t := FindOutboundTarget(doc, id); t.Outbound != nil && slices.Contains(noUDPHop, t.Outbound.Type) {
			out = append(out, t.Outbound.Tag)
		}
	}
	return out
}
