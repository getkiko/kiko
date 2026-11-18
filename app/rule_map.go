package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The core reports a connection's matched route rule as "#<index> <rule>",
// index being the rule's position in route.rules. The desktop sends with
// each apply a map from those indexes to its route table, so connections
// and usage name rules as the route table numbers and names them.

// ruleRef ties route.rules[Index] of the applied configuration to the
// route table, as the desktop's emitter describes it.
type ruleRef struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Section string `json:"section,omitempty"`
	Ordinal int    `json:"ordinal,omitempty"`
	Name    string `json:"name,omitempty"`
	// Final marks a device's default outbound rule.
	Final bool `json:"final,omitempty"`
}

// label is how connections and usage show the rule.
func (r ruleRef) label() string {
	if r.Final {
		return "默认出口"
	}
	return strconv.Itoa(r.Ordinal) + " · " + r.Name
}

// linked reports whether the rule opens a route table rule.
func (r ruleRef) linked() bool { return !r.Final && r.ID != "" }

// connRule is a matched rule as clients see it: its label and, for a route
// table rule, what opens it.
type connRule struct {
	Label   string
	ID      string
	Owner   string
	Section string
	// Final marks a device's default outbound, Owner naming the device.
	Final bool
	// set marks a resolved rule; a zero connRule resolves from the raw rule.
	set bool
}

// ruleLink is the route table rule a label opens, for usage.
type ruleLink struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Section string `json:"section"`
}

// routeFinalOwner is the owner of the connections route.final carries, as
// rule hits key them ("final:route").
const routeFinalOwner = "route"

// ruleMapState is the rule map of the applied configuration.
type ruleMapState struct {
	mu      sync.RWMutex
	byIndex map[int]ruleRef
	// dns names the DNS rules of the applied configuration by their
	// index in dns.rules, dns.final at -1, for GET /dns/rule-map.
	dns dnsRuleMap
}

// dnsRuleMap is the DNS rule map of the configuration applied at
// AppliedAt: queries that began before it went by an earlier one.
type dnsRuleMap struct {
	AppliedAt time.Time `json:"appliedAt"`
	Rules     []ruleRef `json:"rules"`
}

// splitConnRuleIndex splits "#<index> <rule>" into the index and the rule;
// a rule without the prefix has index -1.
func splitConnRuleIndex(rule string) (int, string) {
	rest, ok := strings.CutPrefix(rule, "#")
	if !ok {
		return -1, rule
	}
	number, description, ok := strings.Cut(rest, " ")
	if !ok {
		return -1, rule
	}
	index, err := strconv.Atoi(number)
	if err != nil || index < 0 {
		return -1, rule
	}
	return index, description
}

// stripConnRuleIndex is the rule without the core's index prefix.
func stripConnRuleIndex(rule string) string {
	_, description := splitConnRuleIndex(rule)
	return description
}

func (s *server) ruleMapPath() string {
	return filepath.Join(s.dataDir, "runtime", "rule-map.json")
}

func (s *server) dnsRuleMapPath() string {
	return filepath.Join(s.dataDir, "runtime", "dns-rule-map.json")
}

// loadRuleMap reads the rule maps of the configuration last applied.
func (s *server) loadRuleMap() {
	var refs []ruleRef
	_ = readJSON(s.ruleMapPath(), &refs)
	s.ruleMap.set(refs)
	var dns dnsRuleMap
	_ = readJSON(s.dnsRuleMapPath(), &dns)
	s.ruleMap.setDNS(dns)
}

// storeDNSRuleMap keeps the DNS rule map of the configuration just
// applied; an apply without one leaves DNS rules unnamed. A configuration
// the core already ran (same) keeps the time it was applied, so that the
// queries since stay named.
func (s *server) storeDNSRuleMap(refs []ruleRef, same bool) {
	if refs == nil {
		refs = []ruleRef{}
	}
	dns := dnsRuleMap{AppliedAt: time.Now(), Rules: refs}
	if same {
		s.ruleMap.mu.RLock()
		if at := s.ruleMap.dns.AppliedAt; !at.IsZero() {
			dns.AppliedAt = at
		}
		s.ruleMap.mu.RUnlock()
	}
	s.ruleMap.setDNS(dns)
	if err := atomicJSON(s.dnsRuleMapPath(), dns); err != nil && s.logs != nil {
		s.logs.add("[kiko] 保存 DNS 规则映射失败: " + err.Error())
	}
}

// dnsRuleMap answers GET /dns/rule-map: the DNS rule map of the applied
// configuration.
func (s *server) dnsRuleMap(w http.ResponseWriter, r *http.Request) {
	s.ruleMap.mu.RLock()
	dns := s.ruleMap.dns
	s.ruleMap.mu.RUnlock()
	if dns.Rules == nil {
		dns.Rules = []ruleRef{}
	}
	writeJSON(w, http.StatusOK, dns)
}

func (m *ruleMapState) setDNS(dns dnsRuleMap) {
	m.mu.Lock()
	m.dns = dns
	m.mu.Unlock()
}

// storeRuleMap keeps the rule map of the configuration just applied; an
// apply without one leaves rules unnamed.
func (s *server) storeRuleMap(refs []ruleRef) {
	s.ruleMap.set(refs)
	if s.logs != nil {
		s.logs.add(fmt.Sprintf("[kiko] 规则映射：%d 条", len(refs)))
	}
	if refs == nil {
		refs = []ruleRef{}
	}
	if err := atomicJSON(s.ruleMapPath(), refs); err != nil && s.logs != nil {
		s.logs.add("[kiko] 保存规则映射失败: " + err.Error())
	}
}

func (m *ruleMapState) set(refs []ruleRef) {
	byIndex := make(map[int]ruleRef, len(refs))
	for _, ref := range refs {
		byIndex[ref.Index] = ref
	}
	m.mu.Lock()
	m.byIndex = byIndex
	m.mu.Unlock()
}

// resolve names a rule the core reported.
func (m *ruleMapState) resolve(rule string) connRule {
	if rule == "" {
		// No rule matched: route.final carried it, the default outbound of
		// every device without one of its own. It keeps no label.
		return connRule{Final: true, Owner: routeFinalOwner, set: true}
	}
	index, description := splitConnRuleIndex(rule)
	resolved := connRule{Label: description, set: true}
	if index < 0 {
		return resolved
	}
	m.mu.RLock()
	ref, ok := m.byIndex[index]
	m.mu.RUnlock()
	if !ok {
		return resolved
	}
	resolved.Label = ref.label()
	if ref.Final {
		resolved.Final, resolved.Owner = true, ref.Owner
	}
	if ref.linked() {
		resolved.ID, resolved.Owner, resolved.Section = ref.ID, ref.Owner, ref.Section
	}
	return resolved
}

// label is the label of a rule the core reported.
func (m *ruleMapState) label(rule string) string { return m.resolve(rule).Label }

// links are the route table rules of the labels, for labels only one rule
// has.
func (m *ruleMapState) links() map[string]ruleLink {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]ruleLink{}
	ambiguous := map[string]bool{}
	for _, ref := range m.byIndex {
		if !ref.linked() {
			continue
		}
		label := ref.label()
		if _, seen := out[label]; seen || ambiguous[label] {
			delete(out, label)
			ambiguous[label] = true
			continue
		}
		out[label] = ruleLink{ID: ref.ID, Owner: ref.Owner, Section: ref.Section}
	}
	return out
}
