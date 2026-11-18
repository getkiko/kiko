package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// NewID returns a new id such as "out_1a2b3c4d".
func NewID(prefix string) string {
	if prefix == "" {
		prefix = "x"
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// RandomUUID returns a version 4 UUID.
func RandomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// UniqueTag returns base, or base with a number after it, not in taken.
func UniqueTag(base string, taken []string) string {
	b := strings.TrimSpace(base)
	if b == "" {
		b = "unnamed"
	}
	if !slices.Contains(taken, b) {
		return b
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s %d", b, n)
		if !slices.Contains(taken, candidate) {
			return candidate
		}
	}
}

// Target is something routes, groups and detours may name: an outbound or
// an endpoint. Exactly one of its fields is set.
type Target struct {
	Outbound *Outbound
	Endpoint *Endpoint
}

func (t Target) entity() *Entity {
	if t.Outbound != nil {
		return &t.Outbound.Entity
	}
	if t.Endpoint != nil {
		return &t.Endpoint.Entity
	}
	return nil
}

// Valid reports whether the target names something.
func (t Target) Valid() bool { return t.entity() != nil }

// ID is the target's id.
func (t Target) ID() string {
	if e := t.entity(); e != nil {
		return e.ID
	}
	return ""
}

// Tag is the target's tag.
func (t Target) Tag() string {
	if e := t.entity(); e != nil {
		return e.Tag
	}
	return ""
}

// Type is the target's protocol type.
func (t Target) Type() string {
	switch {
	case t.Outbound != nil:
		return t.Outbound.Type
	case t.Endpoint != nil:
		return t.Endpoint.Type
	}
	return ""
}

// Enabled reports whether the target is enabled.
func (t Target) Enabled() bool {
	if e := t.entity(); e != nil {
		return e.Enabled
	}
	return false
}

// TopLevelItem is an entry of the outbounds list: an outbound, or a
// subscription with its nodes, which sort as one.
type TopLevelItem struct {
	Outbound     *Outbound
	Subscription *Subscription
	Nodes        []*Outbound
}

// Key is the item's key in Doc.OutboundOrder.
func (i TopLevelItem) Key() string {
	if i.Subscription != nil {
		return "sub:" + i.Subscription.ID
	}
	return i.Outbound.ID
}

// TopLevelOutboundItems returns the outbounds list's items in their order.
func TopLevelOutboundItems(doc *Doc) []TopLevelItem {
	subscriptionIDs := map[string]bool{}
	for _, s := range doc.Subscriptions {
		subscriptionIDs[s.ID] = true
	}
	byKey := map[string]TopLevelItem{}
	var keys []string
	for _, o := range doc.Outbounds {
		if o.SubID == "" || !subscriptionIDs[o.SubID] {
			byKey[o.ID] = TopLevelItem{Outbound: o}
			keys = append(keys, o.ID)
		}
	}
	for _, s := range doc.Subscriptions {
		item := TopLevelItem{Subscription: s}
		for _, o := range doc.Outbounds {
			if o.SubID == s.ID {
				item.Nodes = append(item.Nodes, o)
			}
		}
		byKey["sub:"+s.ID] = item
		keys = append(keys, "sub:"+s.ID)
	}
	order := doc.OutboundOrder
	if order == nil {
		order = keys
	}
	var result []TopLevelItem
	seen := map[string]bool{}
	for _, key := range order {
		if item, ok := byKey[key]; ok && !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	for _, key := range keys {
		if !seen[key] {
			result = append(result, byKey[key])
		}
	}
	return result
}

// OrderedOutbounds returns the outbounds in the order the list shows them
// and the configuration emits them.
func OrderedOutbounds(doc *Doc) []*Outbound {
	var out []*Outbound
	for _, item := range TopLevelOutboundItems(doc) {
		if item.Subscription != nil {
			out = append(out, item.Nodes...)
		} else {
			out = append(out, item.Outbound)
		}
	}
	return out
}

// AllOutboundTargets returns every target: outbounds, then endpoints.
func AllOutboundTargets(doc *Doc) []Target {
	var out []Target
	for _, o := range OrderedOutbounds(doc) {
		out = append(out, Target{Outbound: o})
	}
	for _, e := range doc.Endpoints {
		out = append(out, Target{Endpoint: e})
	}
	return out
}

// FindOutboundTarget returns the outbound or endpoint with id.
func FindOutboundTarget(doc *Doc, id string) Target {
	if id == "" {
		return Target{}
	}
	for _, o := range doc.Outbounds {
		if o.ID == id {
			return Target{Outbound: o}
		}
	}
	for _, e := range doc.Endpoints {
		if e.ID == id {
			return Target{Endpoint: e}
		}
	}
	return Target{}
}

// TagByID returns the tag of the target or DNS server with id, "" if none.
func TagByID(doc *Doc, id string) string {
	if t := FindOutboundTarget(doc, id); t.Valid() {
		return t.Tag()
	}
	for _, s := range doc.DNS.Servers {
		if s.ID == id {
			return s.Tag
		}
	}
	return ""
}

// DNSServerByID returns the DNS server with id, or nil.
func DNSServerByID(doc *Doc, id string) *DNSServer {
	for _, s := range doc.DNS.Servers {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// PruneOutboundMembers drops the removed outbound or endpoint ids from
// the lists that hold them as members: selector and URLTest members, a
// group's default (cleared so the group falls back to its first member)
// and OutboundOrder. Single-target references (detours, rule outbounds,
// device defaults) stay as they are, for the pages to flag. Changed
// outbounds are replaced by copies, so entities shared with another
// document are not modified.
func PruneOutboundMembers(doc *Doc, ids ...string) {
	if len(ids) == 0 {
		return
	}
	removed := map[string]bool{}
	for _, id := range ids {
		removed[id] = true
	}
	outbounds := make([]*Outbound, len(doc.Outbounds))
	for i, o := range doc.Outbounds {
		outbounds[i] = o
		dropMembers := slices.ContainsFunc(o.Members, func(m string) bool { return removed[m] })
		def, hasDefault := o.Fields["default_ref"].(string)
		dropDefault := hasDefault && removed[def]
		if !dropMembers && !dropDefault {
			continue
		}
		c := *o
		if dropMembers {
			c.Members = slices.DeleteFunc(slices.Clone(o.Members), func(m string) bool { return removed[m] })
		}
		if dropDefault {
			c.Fields = cloneFields(o.Fields)
			delete(c.Fields, "default_ref")
		}
		outbounds[i] = &c
	}
	if doc.Outbounds != nil {
		doc.Outbounds = outbounds
	}
	if slices.ContainsFunc(doc.OutboundOrder, func(k string) bool { return removed[k] }) {
		doc.OutboundOrder = slices.DeleteFunc(slices.Clone(doc.OutboundOrder), func(k string) bool { return removed[k] })
	}
}

// RemoveOutbound deletes an outbound and prunes it from group members,
// group defaults and OutboundOrder.
func RemoveOutbound(doc *Doc, id string) {
	doc.Outbounds = slices.DeleteFunc(slices.Clone(doc.Outbounds), func(o *Outbound) bool { return o.ID == id })
	PruneOutboundMembers(doc, id)
}

// RemoveEndpoint deletes an endpoint and prunes it from group members,
// group defaults and OutboundOrder.
func RemoveEndpoint(doc *Doc, id string) {
	doc.Endpoints = slices.DeleteFunc(slices.Clone(doc.Endpoints), func(e *Endpoint) bool { return e.ID == id })
	PruneOutboundMembers(doc, id)
}

// RemoveSubscription deletes a subscription with its nodes and prunes
// the nodes and the subscription's "sub:<id>" entry from group members,
// group defaults and OutboundOrder.
func RemoveSubscription(doc *Doc, id string) {
	pruned := []string{"sub:" + id}
	for _, o := range doc.Outbounds {
		if o.SubID == id {
			pruned = append(pruned, o.ID)
		}
	}
	doc.Subscriptions = slices.DeleteFunc(slices.Clone(doc.Subscriptions), func(s *Subscription) bool { return s.ID == id })
	doc.Outbounds = slices.DeleteFunc(slices.Clone(doc.Outbounds), func(o *Outbound) bool { return o.SubID == id })
	PruneOutboundMembers(doc, pruned...)
}
