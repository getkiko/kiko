package nodeview

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// Exits are the groups the Routing, Connections and Usage pages bring an exit down
// to its server by: while the core runs its groups and choices, the
// outbounds under the names it gives them (their emitted tags, as the
// connections' chains name them); otherwise the configuration's groups
// (DocGroups) and tags.
type Exits struct {
	Groups
	// names are the outbounds' names in Groups by id.
	names map[string]string
}

// Name is the name of outbound id in the groups, "" for none.
func (x Exits) Name(id string) string { return x.names[id] }

// LiveGroups are the running core's groups, nil while it is stopped.
func LiveGroups(e *env.Env) []api.Selector {
	if e == nil || e.Server == nil || !e.Server.HasRuntime || !e.Server.Runtime.Running() {
		return nil
	}
	return e.Server.Selectors
}

// ExitsCache keeps the Exits of a document version, the core's groups
// and, while it is stopped, the latency results that pick the choices of
// fastest groups.
type ExitsCache struct {
	built            bool
	version, latency int
	first            *api.Selector
	count            int
	exits            Exits
}

// Of are the Exits of s's document with the running core's groups live
// (LiveGroups), built again once one of them changes.
func (c *ExitsCache) Of(s *store.Store, live []api.Selector) Exits {
	var first *api.Selector
	if len(live) > 0 {
		first = &live[0]
	}
	version, latency := s.Version(), 0
	if len(live) == 0 {
		latency = s.LatencyGen()
	}
	if c.built && c.version == version && c.first == first && c.count == len(live) && c.latency == latency {
		return c.exits
	}
	doc := s.Doc()
	byTag := Members(doc)
	members := byTag
	names := make(map[string]string, len(doc.Outbounds))
	groups := live
	if len(live) > 0 {
		// The core names the outbounds by their emitted tags.
		tags := emit.EmittedTags(doc)
		members = make(map[string]Member, len(byTag))
		for _, o := range doc.Outbounds {
			tag := o.Tag
			if t, ok := tags.Outbounds[o.ID]; ok {
				tag = t
			}
			if _, taken := members[tag]; taken {
				continue
			}
			m := byTag[o.Tag]
			if m.ID != o.ID {
				m = Member{ID: o.ID, Name: o.Tag, Protocol: ProtocolTag(o.Type), Group: model.IsGroup(o.Type)}
			}
			members[tag] = m
			names[o.ID] = tag
		}
	} else {
		groups = DocGroups(doc, s.Latency)
		for _, o := range doc.Outbounds {
			if m, ok := byTag[o.Tag]; ok && m.ID == o.ID {
				names[o.ID] = o.Tag
			}
		}
	}
	*c = ExitsCache{built: true, version: version, first: first, count: len(live), latency: latency,
		exits: Exits{Groups: NewGroups(groups, members), names: names}}
	return c.exits
}

// LastHop is the last outbound of a chain "a → b → c" or of its names,
// the one a connection left through; "" for none or "—".
func LastHop(chain ...string) string {
	for i := len(chain) - 1; i >= 0; i-- {
		hops := strings.Split(chain[i], "→")
		if hop := strings.TrimSpace(hops[len(hops)-1]); hop != "" && hop != "—" {
			return hop
		}
	}
	return ""
}

// ExitTargets are what ExitTests tests of the exit name: final, the
// server it comes down to now when that has a server to test, else "";
// and when group is asked for and name is a group, all, the servers under
// it with one to test.
func ExitTargets(s *store.Store, x Exits, name string, group bool) (final string, all []string) {
	if s == nil || name == "" {
		return "", nil
	}
	if id := x.FinalID(name); s.Measurable(id) {
		final = id
	}
	if group && x.IsGroup(name) {
		all = s.MeasurableOf(x.Servers(name))
	}
	return final, all
}

// ExitTests adds to m the speed tests of the exit name (in x): "Test outbound",
// testing the server name comes down to now (Groups.Final) when it has
// one to test, disabled while that is tested; and when name is a group
// (and batch is not "") "Test all outbound members", testing all the servers under
// it (Groups.Servers) as the batch entry batch (Store.MeasureBatch),
// disabled while that is under way. A separator goes before them when
// separate. It adds nothing when there is nothing to test; toast tells
// that a test found nothing after all. It reports whether it added an
// item.
func ExitTests(m *ui.Menu, s *store.Store, x Exits, name, batch string, separate bool, toast func(string)) bool {
	final, all := ExitTargets(s, x, name, batch != "")
	if final == "" && len(all) == 0 {
		return false
	}
	none := func() {
		if toast != nil {
			toast("没有可测速的节点")
		}
	}
	if separate {
		m.Separator()
	}
	if final != "" && m.Item("测速出口").Disabled(s.Testing(final)).Chosen() {
		if !s.MeasureLatencyOf([]string{final}) {
			none()
		}
	}
	if len(all) > 0 && m.Item("测速出口全部成员").Disabled(s.BatchTesting(batch)).Chosen() {
		if !s.MeasureBatch(batch, all) {
			none()
		}
	}
	return true
}
