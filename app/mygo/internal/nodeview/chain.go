package nodeview

import (
	"slices"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// chainLimit is how many groups a choice passes through at most on its
// way to a server.
const chainLimit = 8

// Groups is how the members of strategy groups come down to servers:
// the groups by name, with their choices and members (the running
// core's, else the configuration's, DocGroups), and the outbounds by the
// names the groups use (Members). The Overview chooser and sheet and the Components
// page share it. Its maps are read only.
type Groups struct {
	byName  map[string]api.Selector
	members map[string]Member
}

// NewGroups is the Groups of groups and members, the first group of a
// name counting.
func NewGroups(groups []api.Selector, members map[string]Member) Groups {
	byName := make(map[string]api.Selector, len(groups))
	for _, g := range groups {
		if _, taken := byName[g.Name]; !taken {
			byName[g.Name] = g
		}
	}
	return Groups{byName: byName, members: members}
}

// Group is the group of name, false when there is none.
func (g Groups) Group(name string) (api.Selector, bool) {
	s, ok := g.byName[name]
	return s, ok
}

// IsGroup tells whether name is a group: one of the groups, or an
// outbound of a group's type.
func (g Groups) IsGroup(name string) bool {
	if _, ok := g.byName[name]; ok {
		return true
	}
	return g.members[name].Group
}

// Choice is the member group name has chosen, "" when name is no group
// or has chosen none.
func (g Groups) Choice(name string) string { return g.byName[name].Now }

// Member is what the views show of the outbound name, false when there
// is none.
func (g Groups) Member(name string) (Member, bool) {
	m, ok := g.members[name]
	return m, ok
}

// Final is the server name stands for now: name itself when it is a
// server, else the server its group's choice comes down to, through the
// choices of the groups chosen in turn, at most chainLimit of them. ok is
// false when the chain ends nowhere: a group without a choice, a name
// that is no outbound (an endpoint), a loop or too long a chain.
func (g Groups) Final(name string) (server string, m Member, ok bool) {
	seen := map[string]bool{}
	for range chainLimit + 1 {
		if !g.IsGroup(name) {
			m, ok := g.members[name]
			if !ok || m.ID == "" {
				return "", Member{}, false
			}
			return name, m, true
		}
		if seen[name] {
			break
		}
		seen[name] = true
		next := g.Choice(name)
		if next == "" {
			break
		}
		name = next
	}
	return "", Member{}, false
}

// FinalID is the outbound id of the server name stands for now (Final),
// "" for none.
func (g Groups) FinalID(name string) string {
	_, m, _ := g.Final(name)
	return m.ID
}

// Chosen are the outbound ids of the servers names stand for now
// (Final), each once, in order: a server's own, a group's choice's.
func (g Groups) Chosen(names []string) []string {
	var ids []string
	for _, name := range names {
		if id := g.FinalID(name); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// Servers are the outbound ids of the servers under names, each once, in
// order: a server's own, and for a group those of its members, groups
// among them unfolded in turn, each group once (a loop ends there). A
// group the groups do not list has no servers.
func (g Groups) Servers(names ...string) []string {
	var ids []string
	added := map[string]bool{}
	unfolded := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if g.IsGroup(name) {
			if unfolded[name] {
				return
			}
			unfolded[name] = true
			for _, member := range g.byName[name].All {
				walk(member)
			}
			return
		}
		if id := g.members[name].ID; id != "" && !added[id] {
			added[id] = true
			ids = append(ids, id)
		}
	}
	for _, name := range names {
		walk(name)
	}
	return ids
}

// DocGroups are the configuration's enabled strategy groups with
// members, as the Components page's cards tell them while the core is off:
// their members' tags, and as the choice the member in use
// (model.OfflineCurrent: a manual group's default, else its first
// member; a fallback group's first enabled member), a fastest or smart
// group's fastest member in latency (FastestTested), none for a load
// balance group.
func DocGroups(doc *model.Doc, latency map[string]api.LatencyResult) []api.Selector {
	var groups []api.Selector
	for _, o := range model.OrderedOutbounds(doc) {
		if !o.Enabled || !model.IsGroup(o.Type) {
			continue
		}
		ids := model.GroupMembers(doc, o)
		var members []string
		for _, id := range ids {
			if tag := model.TagByID(doc, id); tag != "" {
				members = append(members, tag)
			}
		}
		if len(members) == 0 {
			continue
		}
		var current string
		if o.Type == model.ModeFastest || o.Type == model.ModeSmart {
			current = FastestTested(ids, latency)
		} else {
			current = model.OfflineCurrent(doc, o, ids)
		}
		now := ""
		if tag := model.TagByID(doc, current); current != "" && slices.Contains(members, tag) {
			now = tag
		}
		groups = append(groups, api.Selector{Name: o.Tag, Type: o.Type, Now: now, All: members})
	}
	return groups
}

// FastestTested is the one of ids with the shortest latency measured
// (a median connect time, without an error), "" when none was.
func FastestTested(ids []string, latency map[string]api.LatencyResult) string {
	fastest, best := "", 0.0
	for _, id := range ids {
		r, ok := latency[id]
		if !ok || r.Error != "" || r.MedianConnectMs == nil {
			continue
		}
		if v := *r.MedianConnectMs; fastest == "" || v < best {
			fastest, best = id, v
		}
	}
	return fastest
}
