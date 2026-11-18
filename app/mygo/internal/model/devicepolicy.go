package model

import (
	"errors"
	"slices"
	"sort"
	"strconv"
)

// This file holds the per-device route and DNS policies, their defaults,
// and copying or moving rules between devices. It also offers the queries
// the routes page reads.

// PolicySection selects the traffic or DNS policy.
type PolicySection string

// Policy sections.
const (
	SectionTraffic PolicySection = "traffic"
	SectionDNS     PolicySection = "dns"
)

// PolicyOwners lists the scopes in effect in policy order: pre, the active
// local scope (unless removed), the active scope of each LAN device, other.
func PolicyOwners(doc *Doc) []string {
	owners := []string{PreDeviceID}
	if LocalEnabled(doc) {
		owners = append(owners, LocalActiveScope(doc))
	}
	for _, d := range doc.Route.Devices {
		owners = append(owners, ActiveScope(d))
	}
	return append(owners, OtherDeviceID)
}

// AllPolicyScopes lists every scope in policy order, including the
// inactive profiles of the local scope and the devices.
func AllPolicyScopes(doc *Doc) []string {
	owners := []string{PreDeviceID}
	owners = append(owners, LocalScopes(doc)...)
	for _, d := range doc.Route.Devices {
		owners = append(owners, DeviceScopes(d)...)
	}
	return append(owners, OtherDeviceID)
}

// LocalEnabled reports whether the local scope exists.
func LocalEnabled(doc *Doc) bool { return doc.Route.LocalEnabled == nil || *doc.Route.LocalEnabled }

// PolicyOwnerName names a scope for display.
func PolicyOwnerName(doc *Doc, owner string) string {
	return ScopeName(doc, owner)
}

// AddressInNetwork reports whether an address lies in a network (an IP
// or CIDR).
func AddressInNetwork(address, network string) bool {
	ip, ok1 := ParseNetwork(address)
	cidr, ok2 := ParseNetwork(network)
	if !ok1 || !ok2 || len(ip.Bytes) != len(cidr.Bytes) {
		return false
	}
	for bit := 0; bit < cidr.Prefix; bit++ {
		mask := byte(128 >> (bit % 8))
		if ip.Bytes[bit>>3]&mask != cidr.Bytes[bit>>3]&mask {
			return false
		}
	}
	return true
}

// DeviceForSource is the scope in effect whose rules a connection from ip reaches,
// in the order the configuration matches them: the local server / gateway for the local
// source networks (localAddrs the host's detected addresses), then the
// first device listing a network of ip, else the other devices.
func DeviceForSource(doc *Doc, ip string, localAddrs []string) string {
	if ip == "" {
		return OtherDeviceID
	}
	in := func(networks []string) bool {
		return slices.ContainsFunc(networks, func(n string) bool { return AddressInNetwork(ip, n) })
	}
	if LocalEnabled(doc) && in(LocalSourceAddresses(doc, localAddrs)) {
		return LocalActiveScope(doc)
	}
	for _, d := range doc.Route.Devices {
		if in(d.Addresses) {
			return ActiveScope(d)
		}
	}
	return OtherDeviceID
}

// CanCopyPolicyDefault reports whether a default can be copied between
// scopes: pre has none.
func CanCopyPolicyDefault(source, target string) bool {
	return source != PreDeviceID && target != PreDeviceID
}

// ErrPreDeviceDefault is returned when setting a default of the pre scope.
var ErrPreDeviceDefault = errors.New("前置规则仅按规则匹配，剩余查询继续匹配后续设备")

// DNSFinalFor is a scope's default DNS server: null for pre, dns.final for
// other, the scope's own otherwise.
func DNSFinalFor(doc *Doc, owner string) Ref {
	switch owner {
	case PreDeviceID:
		return Null()
	case OtherDeviceID:
		return doc.DNS.Final.Or(Null())
	}
	return doc.DNS.DeviceFinals[owner].Or(Null())
}

// DNSFinalDetourFor is a scope's default DNS query detour.
func DNSFinalDetourFor(doc *Doc, owner string) Ref {
	switch owner {
	case PreDeviceID:
		return Null()
	case OtherDeviceID:
		return doc.DNS.FinalDetour.Or(Null())
	}
	return doc.DNS.DeviceFinalDetours[owner].Or(Null())
}

func copyRefs(m map[string]Ref) map[string]Ref {
	out := make(map[string]Ref, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// SetDNSFinal returns doc with a scope's default DNS server set.
func SetDNSFinal(doc *Doc, owner string, value Ref) (*Doc, error) {
	if owner == PreDeviceID {
		return nil, ErrPreDeviceDefault
	}
	next := *doc
	if owner == OtherDeviceID {
		next.DNS.Final = value
	} else {
		next.DNS.DeviceFinals = copyRefs(doc.DNS.DeviceFinals)
		next.DNS.DeviceFinals[owner] = value
	}
	return &next, nil
}

// SetDNSFinalDetour returns doc with a scope's default DNS detour set.
func SetDNSFinalDetour(doc *Doc, owner string, value Ref) (*Doc, error) {
	if owner == PreDeviceID {
		return nil, ErrPreDeviceDefault
	}
	next := *doc
	if owner == OtherDeviceID {
		next.DNS.FinalDetour = value
	} else {
		next.DNS.DeviceFinalDetours = copyRefs(doc.DNS.DeviceFinalDetours)
		next.DNS.DeviceFinalDetours[owner] = value
	}
	return &next, nil
}

// RouteFinalFor is a scope's default outbound: none for pre, route.final
// for other, the local default (inheriting route.final while unset) and
// each device's own.
func RouteFinalFor(doc *Doc, owner string) Ref {
	switch owner {
	case PreDeviceID:
		return Null()
	case OtherDeviceID:
		return doc.Route.Final.Or(Null())
	case LocalDeviceID:
		return doc.Route.LocalFinal.Or(doc.Route.Final).Or(Null())
	}
	if d, _, ok := ScopeDevice(doc, owner); ok {
		return RefOf(ProfileFinal(d, owner))
	}
	return Null()
}

// SetRouteFinal returns doc with a scope's default outbound set: other is
// route.final, local route.localFinal, a device its own default. Pre has
// none.
func SetRouteFinal(doc *Doc, owner string, value Ref) (*Doc, error) {
	if owner == PreDeviceID {
		return nil, ErrPreDeviceDefault
	}
	next := *doc
	switch owner {
	case OtherDeviceID:
		next.Route.Final = value
	case LocalDeviceID:
		next.Route.LocalFinal = value
	default:
		setScopeFinal(&next, owner, value.ID())
	}
	return &next, nil
}

// MigrateDeviceDNS fans the former global DNS policy out to every scope
// once; later reads keep the ids.
func MigrateDeviceDNS(doc *Doc) DNS {
	owners := AllPolicyScopes(doc)
	legacy := doc.DNS.DevicePolicyVersion != 1
	localEnabled := LocalEnabled(doc)
	rules := []*DNSRule{}
	if legacy {
		for _, owner := range owners {
			if owner == PreDeviceID {
				continue
			}
			for _, rule := range doc.DNS.Rules {
				copied := Clone(rule)
				copied.ID = stable("dr", owner, rule.ID)
				copied.DeviceID = owner
				copied.GroupID = stable("dg", owner, "default")
				rules = append(rules, copied)
			}
		}
	} else {
		for _, rule := range doc.DNS.Rules {
			if localEnabled || !IsLocalScope(DeviceOwner(rule.DeviceID)) {
				rules = append(rules, rule)
			}
		}
	}
	groups := []*RouteGroup{}
	if !legacy {
		for _, g := range doc.DNS.Groups {
			if localEnabled || !IsLocalScope(DeviceOwner(g.DeviceID)) {
				groups = append(groups, g)
			}
		}
	}
	finals := copyRefs(doc.DNS.DeviceFinals)
	detours := copyRefs(doc.DNS.DeviceFinalDetours)
	delete(finals, PreDeviceID)
	delete(detours, PreDeviceID)
	if !localEnabled {
		for _, m := range []map[string]Ref{finals, detours} {
			for owner := range m {
				if IsLocalScope(owner) {
					delete(m, owner)
				}
			}
		}
	}
	for _, owner := range owners {
		if !slices.ContainsFunc(groups, func(g *RouteGroup) bool { return DeviceOwner(g.DeviceID) == owner }) {
			groups = append(groups, &RouteGroup{ID: stable("dg", owner, "default"), Name: "默认分组", DeviceID: owner})
		}
		if owner != PreDeviceID && owner != OtherDeviceID {
			if _, ok := finals[owner]; !ok {
				finals[owner] = doc.DNS.Final.Or(Null())
				if _, ok := detours[owner]; !ok && doc.DNS.FinalDetour.Valid() {
					detours[owner] = doc.DNS.FinalDetour
				}
			}
		}
	}
	for i, rule := range rules {
		owner := DeviceOwner(rule.DeviceID)
		if slices.ContainsFunc(groups, func(g *RouteGroup) bool { return g.ID == rule.GroupID && DeviceOwner(g.DeviceID) == owner }) {
			continue
		}
		copied := *rule
		copied.GroupID = ""
		for _, g := range groups {
			if DeviceOwner(g.DeviceID) == owner {
				copied.GroupID = g.ID
				break
			}
		}
		rules[i] = &copied
	}
	next := doc.DNS
	next.DevicePolicyVersion = 1
	next.Rules, next.Groups, next.DeviceFinals = rules, groups, finals
	if len(detours) > 0 || doc.DNS.DeviceFinalDetours != nil {
		next.DeviceFinalDetours = detours
	}
	return next
}

// stable is the stable id of a migrated rule or group, as
// `${kind}:${encodeURIComponent(owner)}:${encodeURIComponent(id)}`.
func stable(kind, owner, id string) string {
	return kind + ":" + encodeURIComponent(owner) + ":" + encodeURIComponent(id)
}

// encodeURIComponent escapes as JavaScript's encodeURIComponent.
func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var out []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || indexByte(keep, c) {
			out = append(out, c)
		} else {
			out = append(out, '%', "0123456789ABCDEF"[c>>4], "0123456789ABCDEF"[c&15])
		}
	}
	return string(out)
}

func indexByte(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}

// OrderedDeviceRules returns a scope's rules ordered by group, then by
// their order in the list.
func OrderedDeviceRules[T PolicyRule](rules []T, groups []*RouteGroup, owner string) []T {
	order := map[string]int{}
	for i, g := range groups {
		order[g.ID] = i
	}
	rank := func(group string) int {
		if i, ok := order[group]; ok {
			return i
		}
		return int(^uint(0) >> 1)
	}
	out := []T{}
	for _, r := range rules {
		if r.PolicyOwner() == owner {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].PolicyGroup()) < rank(out[j].PolicyGroup()) })
	return out
}

// GroupsOf returns the groups of a scope, in order.
func GroupsOf(groups []*RouteGroup, owner string) []*RouteGroup {
	var out []*RouteGroup
	for _, g := range groups {
		if DeviceOwner(g.DeviceID) == owner {
			out = append(out, g)
		}
	}
	return out
}

// RouteGroupsOf returns the route groups of a scope.
func RouteGroupsOf(doc *Doc, owner string) []*RouteGroup { return GroupsOf(doc.Route.Groups, owner) }

// DNSGroupsOf returns the DNS groups of a scope.
func DNSGroupsOf(doc *Doc, owner string) []*RouteGroup { return GroupsOf(doc.DNS.Groups, owner) }

// RouteRulesOf returns the route rules of a scope in group order.
func RouteRulesOf(doc *Doc, owner string) []*RouteRule {
	return OrderedDeviceRules(doc.Route.Rules, doc.Route.Groups, owner)
}

// RouteRuleOrdinals numbers the scope's route rules as the route table
// does: 1-based, group by group, a rule without a group counting in the
// first group. Rules outside every group of the scope have no number.
func RouteRuleOrdinals(doc *Doc, owner string) map[string]int {
	groups := RouteGroupsOf(doc, owner)
	ordinal := map[string]int{}
	n := 0
	for i, g := range groups {
		for _, r := range doc.Route.Rules {
			if r.PolicyOwner() != owner {
				continue
			}
			if r.GroupID == g.ID || r.GroupID == "" && i == 0 {
				n++
				ordinal[r.ID] = n
			}
		}
	}
	return ordinal
}

// DNSRulesOf returns the DNS rules of a scope in group order.
func DNSRulesOf(doc *Doc, owner string) []*DNSRule {
	return OrderedDeviceRules(doc.DNS.Rules, doc.DNS.Groups, owner)
}

// RulesInGroup returns the rules of one group, in order.
func RulesInGroup[T PolicyRule](rules []T, groupID string) []T {
	var out []T
	for _, r := range rules {
		if r.PolicyGroup() == groupID {
			out = append(out, r)
		}
	}
	return out
}

func uniqueGroupName(name string, groups []*RouteGroup) string {
	taken := func(n string) bool {
		return slices.ContainsFunc(groups, func(g *RouteGroup) bool { return g.Name == n })
	}
	if !taken(name) {
		return name
	}
	candidate := name + " 副本"
	for n := 2; taken(candidate); n++ {
		candidate = name + " 副本 " + strconv.Itoa(n)
	}
	return candidate
}

// CopyPolicyOptions selects what CopyDevicePolicy copies: every rule of
// Source, or the one RuleID, to Target; IncludeDefault also copies the
// default outbound or DNS.
type CopyPolicyOptions struct {
	Section        PolicySection
	Source, Target string
	RuleID         string
	IncludeDefault bool
}

// Errors of copying and moving policies.
var (
	ErrPolicyScope    = errors.New("请选择有效的来源和目标设备")
	ErrPolicyRuleGone = errors.New("来源规则已更新，请重新选择")
	ErrNoDefaultOut   = errors.New("请先配置可用的默认出口")
	ErrFixedScope     = errors.New("前置规则与其他设备为固定作用域")
)

func policyGroups(doc *Doc, section PolicySection) []*RouteGroup {
	if section == SectionDNS {
		return doc.DNS.Groups
	}
	return doc.Route.Groups
}

func groupPrefix(section PolicySection) string {
	if section == SectionDNS {
		return "dg"
	}
	return "rg"
}

func rulePrefix(section PolicySection) string {
	if section == SectionDNS {
		return "dr"
	}
	return "rr"
}

// policyRuleRef is a rule of either section.
type policyRuleRef struct {
	route *RouteRule
	dns   *DNSRule
}

func (r policyRuleRef) id() string {
	if r.route != nil {
		return r.route.ID
	}
	return r.dns.ID
}

func (r policyRuleRef) group() string {
	if r.route != nil {
		return r.route.GroupID
	}
	return r.dns.GroupID
}

func sectionRules(doc *Doc, section PolicySection, owner string, groups []*RouteGroup) []policyRuleRef {
	var out []policyRuleRef
	if section == SectionDNS {
		for _, r := range OrderedDeviceRules(doc.DNS.Rules, groups, owner) {
			out = append(out, policyRuleRef{dns: r})
		}
	} else {
		for _, r := range OrderedDeviceRules(doc.Route.Rules, groups, owner) {
			out = append(out, policyRuleRef{route: r})
		}
	}
	return out
}

// CopyDevicePolicy copies rules (and optionally the default) from one
// scope to another in one transaction. References stay shared; the rule
// and group objects are new.
func CopyDevicePolicy(doc *Doc, options CopyPolicyOptions) (*Doc, error) {
	section, source, target, ruleID := options.Section, options.Source, options.Target, options.RuleID
	owners := AllPolicyScopes(doc)
	if source == target || !slices.Contains(owners, source) || !slices.Contains(owners, target) {
		return nil, ErrPolicyScope
	}
	sourceGroups := GroupsOf(policyGroups(doc, section), source)
	candidates := sectionRules(doc, section, source, sourceGroups)
	selected := candidates
	if ruleID != "" {
		selected = nil
		for _, r := range candidates {
			if r.id() == ruleID {
				selected = append(selected, r)
			}
		}
		if len(selected) == 0 {
			return nil, ErrPolicyRuleGone
		}
	}
	groups := slices.Clone(policyGroups(doc, section))
	mapping := map[string]string{}
	for _, group := range sourceGroups {
		if ruleID != "" && !slices.ContainsFunc(selected, func(r policyRuleRef) bool { return r.group() == group.ID }) {
			continue
		}
		var existing *RouteGroup
		if ruleID != "" {
			for _, g := range groups {
				if DeviceOwner(g.DeviceID) == target && g.Name == group.Name {
					existing = g
					break
				}
			}
		}
		if existing != nil {
			mapping[group.ID] = existing.ID
			continue
		}
		id := NewID(groupPrefix(section))
		mapping[group.ID] = id
		groups = append(groups, &RouteGroup{ID: id, DeviceID: target, Name: uniqueGroupName(group.Name, GroupsOf(groups, target))})
	}
	if slices.ContainsFunc(selected, func(r policyRuleRef) bool { _, ok := mapping[r.group()]; return !ok }) {
		var fallback *RouteGroup
		for _, g := range groups {
			if DeviceOwner(g.DeviceID) == target {
				fallback = g
				break
			}
		}
		if fallback == nil {
			fallback = &RouteGroup{ID: NewID("pg"), Name: "默认分组", DeviceID: target}
			groups = append(groups, fallback)
		}
		for _, r := range selected {
			if _, ok := mapping[r.group()]; !ok {
				mapping[r.group()] = fallback.ID
			}
		}
	}
	next := *doc
	if section == SectionDNS {
		rules := slices.Clone(doc.DNS.Rules)
		for _, r := range selected {
			c := Clone(r.dns)
			c.ID, c.DeviceID, c.GroupID = NewID(rulePrefix(section)), target, mapping[r.group()]
			rules = append(rules, c)
		}
		next.DNS.Groups, next.DNS.Rules = groups, rules
	} else {
		rules := slices.Clone(doc.Route.Rules)
		for _, r := range selected {
			c := Clone(r.route)
			c.ID, c.DeviceID, c.GroupID = NewID(rulePrefix(section)), target, mapping[r.group()]
			rules = append(rules, c)
		}
		next.Route.Groups, next.Route.Rules = groups, rules
	}
	result := &next
	if options.IncludeDefault && ruleID == "" && CanCopyPolicyDefault(source, target) {
		var err error
		if section == SectionDNS {
			if result, err = SetDNSFinal(result, target, DNSFinalFor(doc, source)); err != nil {
				return nil, err
			}
			if result, err = SetDNSFinalDetour(result, target, DNSFinalDetourFor(doc, source)); err != nil {
				return nil, err
			}
		} else {
			var value string
			switch source {
			case LocalDeviceID:
				value = doc.Route.LocalFinal.Or(doc.Route.Final).ID()
			case OtherDeviceID:
				value = doc.Route.Final.ID()
			default:
				if d, _, ok := ScopeDevice(doc, source); ok {
					value = ProfileFinal(d, source)
				}
			}
			core := firstUsableTarget(doc)
			if target != LocalDeviceID && target != OtherDeviceID && value == "" && core == "" {
				return nil, ErrNoDefaultOut
			}
			copied := *result
			switch target {
			case LocalDeviceID:
				copied.Route.LocalFinal = defaultRef(source, doc, value)
			case OtherDeviceID:
				copied.Route.Final = defaultRef(source, doc, value)
			default:
				final := value
				if final == "" {
					final = core
				}
				setScopeFinal(&copied, target, final)
			}
			result = &copied
		}
	}
	return result, nil
}

// defaultRef keeps the source's default as stored: an absent local
// default that inherits an absent final stays absent.
func defaultRef(source string, doc *Doc, value string) Ref {
	if value != "" {
		return RefOf(value)
	}
	switch source {
	case LocalDeviceID:
		return doc.Route.LocalFinal.Or(doc.Route.Final)
	case OtherDeviceID:
		return doc.Route.Final
	}
	return Ref{}
}

// firstUsableTarget is the core's implicit default outbound: the first
// enabled target outside disabled subscriptions.
func firstUsableTarget(doc *Doc) string {
	for _, t := range AllOutboundTargets(doc) {
		if !t.Enabled() {
			continue
		}
		if t.Outbound != nil && t.Outbound.SubID != "" {
			ok := slices.ContainsFunc(doc.Subscriptions, func(s *Subscription) bool { return s.ID == t.Outbound.SubID && s.Enabled })
			if !ok {
				continue
			}
		}
		return t.ID()
	}
	return ""
}

// MovePolicyOptions selects the rule MoveDevicePolicy moves.
type MovePolicyOptions struct {
	Section        PolicySection
	Source, Target string
	RuleID         string
}

// MoveDevicePolicy moves one rule to another scope, keeping its identity,
// conditions and references; it lands in the target's group of the same
// name, created when missing.
func MoveDevicePolicy(doc *Doc, options MovePolicyOptions) (*Doc, error) {
	section, source, target, ruleID := options.Section, options.Source, options.Target, options.RuleID
	owners := AllPolicyScopes(doc)
	if !slices.Contains(owners, source) || !slices.Contains(owners, target) {
		return nil, ErrPolicyScope
	}
	var groupID string
	found := false
	if section == SectionDNS {
		for _, r := range doc.DNS.Rules {
			if r.ID == ruleID && r.PolicyOwner() == source {
				groupID, found = r.GroupID, true
				break
			}
		}
	} else {
		for _, r := range doc.Route.Rules {
			if r.ID == ruleID && r.PolicyOwner() == source {
				groupID, found = r.GroupID, true
				break
			}
		}
	}
	if !found {
		return nil, ErrPolicyRuleGone
	}
	if source == target {
		return doc, nil
	}
	groups := slices.Clone(policyGroups(doc, section))
	name := "默认分组"
	for _, g := range groups {
		if g.ID == groupID && DeviceOwner(g.DeviceID) == source {
			name = g.Name
			break
		}
	}
	var group *RouteGroup
	for _, g := range groups {
		if DeviceOwner(g.DeviceID) == target && g.Name == name {
			group = g
			break
		}
	}
	if group == nil {
		group = &RouteGroup{ID: NewID(groupPrefix(section)), Name: name, DeviceID: target}
		groups = append(groups, group)
	}
	next := *doc
	if section == SectionDNS {
		var rules []*DNSRule
		var moved *DNSRule
		for _, r := range doc.DNS.Rules {
			if moved == nil && r.ID == ruleID && r.PolicyOwner() == source {
				c := *r
				moved = &c
				continue
			}
			rules = append(rules, r)
		}
		moved.DeviceID, moved.GroupID = target, group.ID
		next.DNS.Groups, next.DNS.Rules = groups, append(rules, moved)
	} else {
		var rules []*RouteRule
		var moved *RouteRule
		for _, r := range doc.Route.Rules {
			if moved == nil && r.ID == ruleID && r.PolicyOwner() == source {
				c := *r
				moved = &c
				continue
			}
			rules = append(rules, r)
		}
		moved.DeviceID, moved.GroupID = target, group.ID
		next.Route.Groups, next.Route.Rules = groups, append(rules, moved)
	}
	return &next, nil
}

// RemovePolicyDevice removes a device (every scope of its profiles) or the
// local scope with its rules, groups and defaults from both policies.
// Removing local turns the host scope off.
func RemovePolicyDevice(doc *Doc, id string) (*Doc, error) {
	if id == PreDeviceID || id == OtherDeviceID {
		return nil, ErrFixedScope
	}
	next := *doc
	owners := []string{id}
	if id == LocalDeviceID {
		owners = append(owners, LocalScopes(doc)...)
		off := false
		next.Route.LocalEnabled = &off
		next.Route.LocalAddresses = []string{}
		next.Route.LocalFinal = Null()
		next.Route.LocalProfiles, next.Route.LocalActiveProfile = nil, ""
	}
	if doc.Route.Devices != nil {
		next.Route.Devices = []*RouteDevice{}
		for _, d := range doc.Route.Devices {
			if d.ID != id {
				next.Route.Devices = append(next.Route.Devices, d)
			} else {
				owners = append(owners, DeviceScopes(d)...)
			}
		}
	}
	dropOwners(&next, owners)
	return &next, nil
}

// dropOwners removes the rules, groups and DNS defaults of the scopes.
func dropOwners(next *Doc, owners []string) {
	gone := func(owner string) bool { return slices.Contains(owners, owner) }
	routeRules := []*RouteRule{}
	for _, r := range next.Route.Rules {
		if !gone(r.PolicyOwner()) {
			routeRules = append(routeRules, r)
		}
	}
	next.Route.Rules = routeRules
	next.Route.Groups = withoutOwners(next.Route.Groups, owners)
	dnsRules := []*DNSRule{}
	for _, r := range next.DNS.Rules {
		if !gone(r.PolicyOwner()) {
			dnsRules = append(dnsRules, r)
		}
	}
	next.DNS.Rules = dnsRules
	next.DNS.Groups = withoutOwners(next.DNS.Groups, owners)
	finals := copyRefs(next.DNS.DeviceFinals)
	detours := copyRefs(next.DNS.DeviceFinalDetours)
	for _, owner := range owners {
		delete(finals, owner)
		delete(detours, owner)
	}
	next.DNS.DeviceFinals, next.DNS.DeviceFinalDetours = finals, detours
}

func withoutOwners(groups []*RouteGroup, owners []string) []*RouteGroup {
	if groups == nil {
		return nil
	}
	out := []*RouteGroup{}
	for _, g := range groups {
		if !slices.Contains(owners, DeviceOwner(g.DeviceID)) {
			out = append(out, g)
		}
	}
	return out
}

// AddLocalPolicyDevice restores the host scope with fresh groups and the
// current fallback defaults.
func AddLocalPolicyDevice(doc *Doc) *Doc {
	if LocalEnabled(doc) {
		return doc
	}
	next := *doc
	route := doc.Route
	on := true
	route.LocalEnabled = &on
	route.LocalAddresses = []string{}
	route.LocalFinal = doc.Route.Final.Or(Null())
	route.LocalProfiles, route.LocalActiveProfile = nil, ""
	next.Route = MigrateRoutePolicy(route)
	next.DNS = MigrateDeviceDNS(&next)
	return &next
}
