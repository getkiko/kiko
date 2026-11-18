package model

import (
	"slices"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// A policy group's type is its mode: how it chooses among its members.
const (
	ModeManual      = "selector"
	ModeFastest     = "urltest"
	ModeFallback    = "fallback"
	ModeLoadBalance = "loadbalance"
	ModeSmart       = "smart"
)

// GroupModes are the policy groups' modes in the order the editor
// offers them.
var GroupModes = []string{ModeManual, ModeFastest, ModeFallback, ModeLoadBalance, ModeSmart}

// LegacyGroupModes are the modes every sing-box core runs; the others
// need a core reporting the groupModes capability.
var LegacyGroupModes = []string{ModeManual, ModeFastest}

var groupModeLabels = map[string]string{
	ModeManual: "手动", ModeFastest: "最快", ModeFallback: "故障转移", ModeLoadBalance: "负载均衡", ModeSmart: "智能",
}

var groupModeNames = map[string]string{
	ModeManual: "节点选择", ModeFastest: "自动选择", ModeFallback: "故障转移", ModeLoadBalance: "负载均衡", ModeSmart: "智能选择",
}

// GroupModeLabel is the short name of a group's mode, "" for other types.
func GroupModeLabel(typ string) string { return groupModeLabels[typ] }

// GroupModeDefaultName is the name a new group of the mode gets.
func GroupModeDefaultName(typ string) string { return groupModeNames[typ] }

// HasHealthCheck reports whether groups of the mode test their members
// periodically.
func HasHealthCheck(typ string) bool { return IsGroup(typ) && typ != ModeManual }

// HasCurrentMember reports whether groups of the mode use one member at
// a time.
func HasCurrentMember(typ string) bool { return IsGroup(typ) && typ != ModeLoadBalance }

// LoadBalanceStrategyLabel names a load balance strategy.
func LoadBalanceStrategyLabel(strategy string) string {
	switch strategy {
	case "consistent_hashing":
		return "按目标"
	case "sticky_sessions":
		return "按来源"
	default:
		return "轮询"
	}
}

// SwitchGroupMode changes group o to mode typ, keeping its members, its
// filter and the fields both modes have; fields of the new mode it has
// none of get their defaults. Fields only the old mode has stay, so that
// switching back restores them, until PruneGroupFields drops them. A name
// still the old mode's default name becomes the new one's.
func SwitchGroupMode(o *Outbound, typ string) {
	if o == nil || !IsGroup(o.Type) || !IsGroup(typ) || o.Type == typ {
		return
	}
	if o.Tag == "" || o.Tag == GroupModeDefaultName(o.Type) {
		o.Tag = GroupModeDefaultName(typ)
	}
	if o.Fields == nil {
		o.Fields = Fields{}
	}
	for key, value := range schema.DefaultFields(schema.OutboundSchema[typ]) {
		if _, ok := o.Fields[key]; !ok {
			o.Fields[key] = value
		}
	}
	o.Type = typ
}

// PruneGroupFields drops the fields of group o its mode does not have,
// as left by SwitchGroupMode.
func PruneGroupFields(o *Outbound) {
	if o == nil || !IsGroup(o.Type) {
		return
	}
	table := schema.OutboundSchema[o.Type]
	for key := range o.Fields {
		if !slices.ContainsFunc(table, func(f schema.Field) bool { return f.Key == key }) {
			delete(o.Fields, key)
		}
	}
}

// OfflineCurrent is the member a group uses as its configuration tells,
// for when no core runs it: a manual group's default member, else its
// first; a fallback group's first enabled member; "" for the modes that
// choose by tests or per connection. members are the group's members
// (GroupMembers).
func OfflineCurrent(doc *Doc, o *Outbound, members []string) string {
	switch o.Type {
	case ModeManual:
		if len(members) == 0 {
			return ""
		}
		if def, _ := o.Fields["default_ref"].(string); def != "" && slices.Contains(members, def) {
			return def
		}
		return members[0]
	case ModeFallback:
		for _, id := range members {
			if t := FindOutboundTarget(doc, id); t.Valid() && t.entity().Enabled {
				return id
			}
		}
	}
	return ""
}
