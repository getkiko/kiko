package subscriptions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// groupPreviewFlags is how many member flags a group's card previews.
const groupPreviewFlags = 5

// groupView is what the page shows of a Selector or URLTest group, built
// with the page's rows: its members, its kind and filter, and while
// searching how many of its members match.
type groupView struct {
	o               *model.Outbound
	id, title, kind string
	// live is the group's tag in the configuration, which the running
	// core names it by.
	live    string
	filter  string
	invalid bool
	all     []groupTile
	// hits counts the members matching the search, 0 without one or when
	// the group matches by its name.
	hits int
	// flags are the first members' flags, which the card previews, and
	// more the members past them.
	flags string
	more  int
	// ids are the members' IDs, which the latency test tests.
	ids []string
	// current is the member in use as the configuration tells
	// (model.OfflineCurrent): a manual group's default, else its first
	// member, a fallback group's first enabled member. fastest is a
	// fastest or smart group's member with the shortest latency, as of
	// the store's LatencyGen fastestGen.
	current    string
	fastest    string
	fastestGen int
}

// groupTile is a member of a group as the card and the member sheet
// show it.
type groupTile struct {
	id, tag string
	// live is the member's tag in the configuration, which the running
	// core's choice names.
	live string
	// name is the tag without the subscription's prefix and, when flag
	// leads it, the flag; protocol the type's short name.
	name, flag, protocol, endpoint string
	// out is the member as an outbound, nil for an endpoint; testable
	// whether it has a server to test.
	out      *model.Outbound
	enabled  bool
	testable bool
	// appended is whether the group's filter added the member.
	appended bool
}

// groupViews are the views of groups, those matching query (lower case)
// while searching: by name or type, or by members, counting those. The
// members' names, regions and the groups' filters are worked out here,
// once per build of the page's rows.
func (s *outboundsSection) groupViews(doc *model.Doc, info *docInfo, groups []*model.Outbound, query string) []groupView {
	if len(groups) == 0 {
		return nil
	}
	members := nodeview.Members(doc)
	regions, matcher := info.regions, info.matcher
	labels := map[string]string{}
	for _, r := range regions.Regions {
		labels[r.ID] = model.RegionName(r)
	}
	configs := s.w.store.NetworkConfigs()
	targets := make(map[string]model.Target, len(doc.Outbounds)+len(doc.Endpoints))
	for _, e := range doc.Endpoints {
		targets[e.ID] = model.Target{Endpoint: e}
	}
	for _, x := range doc.Outbounds {
		targets[x.ID] = model.Target{Outbound: x}
	}
	var out []groupView
	for _, o := range groups {
		g := groupView{o: o, id: o.ID, title: o.Tag, live: info.outboundTag(o.ID, o.Tag), kind: model.GroupModeLabel(o.Type), fastestGen: -1,
			invalid: model.HasInvalidOutboundReferences(doc, o, configs)}
		if g.title == "" {
			g.title = typeLabel(o.Type)
		}
		g.filter = filterSummary(o.Filter, labels)
		explicit := map[string]bool{}
		for _, id := range o.Members {
			explicit[id] = true
		}
		g.ids = model.GroupMembers(doc, o)
		hits, flags := 0, 0
		var preview strings.Builder
		for _, id := range g.ids {
			t, ok := targets[id]
			if !ok {
				continue
			}
			tile := groupTile{id: id, tag: t.Tag(), live: info.outboundTag(id, t.Tag()), name: t.Tag(), appended: !explicit[id]}
			if x := t.Outbound; x != nil {
				if m, ok := members[x.Tag]; ok && m.ID == x.ID {
					tile.name = m.Name
				}
				tile.out, tile.enabled, tile.protocol = x, x.Enabled, nodeview.ProtocolTag(x.Type)
				tile.endpoint = nodeEndpoint(x)
				tile.testable = !model.IsGroup(x.Type) && jsv.Text(x.Fields["server"]) != ""
			} else {
				tile.enabled, tile.protocol, tile.endpoint = t.Endpoint.Enabled, nodeview.ProtocolTag(t.Endpoint.Type), targetEndpoint(t)
			}
			if r, ok := matcher.RegionOf(tile.tag); ok && r.Flag != "" {
				tile.flag, tile.name = r.Flag, nodeview.BesideFlag(r.Flag, tile.name)
				if flags < groupPreviewFlags {
					preview.WriteString(r.Flag)
					flags++
				}
			}
			g.all = append(g.all, tile)
			if query != "" && contains(query, tile.tag) {
				hits++
			}
		}
		g.flags, g.more = preview.String(), len(g.all)-flags
		switch {
		case query == "":
		case hits > 0:
			g.hits = hits
		case contains(query, o.Tag, o.Type, g.kind, g.strategyLabel()):
		default:
			continue
		}
		g.current = model.OfflineCurrent(doc, o, g.ids)
		out = append(out, g)
	}
	return out
}

// filterSummary is what a group's card tells of its filter, its parts
// joined by " · ": the regions by name ("Hong Kong, Japan", or "Hong
// Kong, Japan and 3 in all"), the keywords ("contains IEPL, dedicated
// line"), the excluded ones ("excludes 03"), "Custom match" for a pattern; "" without a filter. labels are the region
// table's names by ID; other IDs are country codes.
func filterSummary(f *model.GroupFilter, labels map[string]string) string {
	if !f.Active() {
		return ""
	}
	// Patterns of literal alternatives read as the keywords the editor
	// turns them into.
	copied := *f
	copied.Keywords = slices.Clone(f.Keywords)
	copied.ExcludeKeywords = slices.Clone(f.ExcludeKeywords)
	model.MigrateFilterKeywords(&copied)
	f = &copied
	var parts []string
	if len(f.Regions) > 0 {
		names := make([]string, 0, len(f.Regions))
		for _, id := range f.Regions {
			if label := labels[id]; label != "" {
				names = append(names, label)
			} else {
				names = append(names, model.RegionName(model.Region{ID: id}))
			}
		}
		parts = append(parts, summaryList(names))
	}
	keywords, exclude := trimmedKeywords(f.Keywords), trimmedKeywords(f.ExcludeKeywords)
	if len(keywords) > 0 {
		parts = append(parts, "含 "+summaryList(keywords))
	}
	if strings.TrimSpace(f.Pattern) != "" || strings.TrimSpace(f.Exclude) != "" {
		parts = append(parts, "自定义匹配")
	}
	if len(exclude) > 0 {
		parts = append(parts, "不含 "+summaryList(exclude))
	}
	return strings.Join(parts, " · ")
}

// summaryList joins names with the Chinese enumeration comma, the first
// two and the count past two.
func summaryList(names []string) string {
	if len(names) > 2 {
		return fmt.Sprintf("%s 等 %d 个", strings.Join(names[:2], "、"), len(names))
	}
	return strings.Join(names, "、")
}

// trimmedKeywords are the non-empty keywords, trimmed.
func trimmedKeywords(keywords []string) []string {
	var out []string
	for _, k := range keywords {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// strategyLabel names a load balance group's strategy, "" for other
// groups.
func (g *groupView) strategyLabel() string {
	if g.o.Type != model.ModeLoadBalance {
		return ""
	}
	return model.LoadBalanceStrategyLabel(jsv.Text(g.o.Fields["strategy"]))
}

// currentOf is the member a group uses as the configuration tells: a
// manual group's default, a fallback group's first enabled member, a
// fastest or smart group's fastest member tested, "" for none.
func (s *outboundsSection) currentOf(g *groupView) string {
	if g.o.Type != model.ModeFastest && g.o.Type != model.ModeSmart {
		return g.current
	}
	if gen := s.w.store.LatencyGen(); g.fastestGen != gen {
		g.fastestGen, g.fastest = gen, nodeview.FastestTested(g.ids, s.w.store.Latency)
	}
	return g.fastest
}

// chainCache is the groups' chains of a document version and the
// running core's groups (their first element and count, nil for none).
type chainCache struct {
	built   bool
	version int
	first   *api.Selector
	count   int
	// latency is the store's LatencyGen the configuration's groups chose
	// their fastest members by, while the core is off.
	latency int
	chains  nodeview.Groups
}

// chains bring the strategy groups' members down to servers
// (nodeview.Groups): the running core's groups, by the tags it names the
// outbounds by, while it runs some; else the configuration's with their
// default choices (nodeview.DocGroups). live tells which: the names are
// the emitted tags (groupTile.live) while live, else the document's
// (groupTile.tag); chainName picks.
func (w *Workspace) chains() (nodeview.Groups, bool) {
	doc := w.store.Doc()
	var live []api.Selector
	if server := w.env.Server; server != nil && server.HasRuntime && server.Runtime.Running() {
		live = server.Selectors
	}
	var first *api.Selector
	if len(live) > 0 {
		first = &live[0]
	}
	version, latency := w.store.Version(), 0
	if len(live) == 0 {
		latency = w.store.LatencyGen()
	}
	cache := &w.chainCache
	if cache.built && cache.version == version && cache.first == first && cache.count == len(live) && cache.latency == latency {
		return cache.chains, len(live) > 0
	}
	members := nodeview.Members(doc)
	groups := live
	if len(live) > 0 {
		// The core names the outbounds by their emitted tags.
		info := w.infoOf(doc)
		byTag := members
		members = make(map[string]nodeview.Member, len(byTag))
		for _, o := range doc.Outbounds {
			tag := info.outboundTag(o.ID, o.Tag)
			if _, taken := members[tag]; taken {
				continue
			}
			m := byTag[o.Tag]
			if m.ID != o.ID {
				m = nodeview.Member{ID: o.ID, Name: o.Tag, Protocol: nodeview.ProtocolTag(o.Type), Group: model.IsGroup(o.Type)}
			}
			members[tag] = m
		}
	} else {
		groups = nodeview.DocGroups(doc, w.store.Latency)
	}
	*cache = chainCache{built: true, version: version, first: first, count: len(live), latency: latency,
		chains: nodeview.NewGroups(groups, members)}
	return cache.chains, len(live) > 0
}

// finalServer is the id of the server member t stands for now: its own
// (a node), or the one a member group's choice comes down to
// (nodeview.Groups.Final), "" for none.
func (w *Workspace) finalServer(t *groupTile) string {
	if t.out == nil || !model.IsGroup(t.out.Type) {
		return t.id
	}
	chains, live := w.chains()
	return chains.FinalID(chainName(t, live))
}

// chainName is the name of member t in the chains (Workspace.chains),
// live or not.
func chainName(t *groupTile, live bool) string {
	if live {
		return t.live
	}
	return t.tag
}

// groupServers are the ids of all servers under group g, its member
// groups unfolded (nodeview.Groups.Servers), which its latency test tests.
func (w *Workspace) groupServers(g *groupView) []string {
	chains, live := w.chains()
	name := g.o.Tag
	if live {
		name = g.live
	}
	if _, ok := chains.Group(name); !ok {
		// A group the chains lack (disabled, or not in the core): its
		// own members, groups among them unfolded.
		names := make([]string, len(g.all))
		for i := range g.all {
			names[i] = chainName(&g.all[i], live)
		}
		return chains.Servers(names...)
	}
	return chains.Servers(name)
}

// liveGroup is the running core's group name, and whether the core
// runs that group.
func (s *outboundsSection) liveGroup(name string) (api.Selector, bool) {
	server := s.w.env.Server
	if server == nil || !server.HasRuntime || !server.Runtime.Running() {
		return api.Selector{}, false
	}
	for _, g := range server.Selectors {
		if g.Name == name {
			return g, true
		}
	}
	return api.Selector{}, false
}

// liveChoice is the member tag the running core's group name has
// chosen, and whether the core runs that group.
func (s *outboundsSection) liveChoice(name string) (string, bool) {
	g, ok := s.liveGroup(name)
	return g.Now, ok
}

// liveMember is the running core's state of member tile t of group g,
// false while the core does not report it.
func (s *outboundsSection) liveMember(g *groupView, t *groupTile) (api.SelectorMember, bool) {
	live, ok := s.liveGroup(g.live)
	if !ok {
		return api.SelectorMember{}, false
	}
	return live.Member(t.live)
}

// available counts the members of a load balance group in rotation in
// the running core and all its members there, and whether the core
// reports them.
func (s *outboundsSection) available(g *groupView) (int, int, bool) {
	live, ok := s.liveGroup(g.live)
	if !ok || len(live.Members) == 0 {
		return 0, 0, false
	}
	n := 0
	for _, m := range live.Members {
		if m.Active {
			n++
		}
	}
	return n, len(live.Members), true
}

// inUse is the ID of the member group g uses: the running core's choice
// while it runs the group, else currentOf; "" for none and for a load
// balance group, which gives each connection a member.
func (s *outboundsSection) inUse(g *groupView) string {
	if g.o.Type == model.ModeLoadBalance {
		return ""
	}
	if now, ok := s.liveChoice(g.live); ok {
		for i := range g.all {
			if g.all[i].live == now {
				return g.all[i].id
			}
		}
	}
	return s.currentOf(g)
}

// tileOf is group g's tile of member id, nil for none.
func (g *groupView) tileOf(id string) *groupTile {
	if id == "" {
		return nil
	}
	for i := range g.all {
		if g.all[i].id == id {
			return &g.all[i]
		}
	}
	return nil
}

// groupGrid is the strategy groups' cards, reordered by dragging as the
// outbounds list's items, then "+ Add policy group", opening the group editor in
// the manual mode.
func (s *outboundsSection) groupGrid(c *ui.Context, groups []groupView, searching bool) {
	keys := make([]string, len(groups))
	for i := range groups {
		keys[i] = groups[i].id
	}
	reorderGrid(c, "outbounds-groups", keys, s.w.reorderTopLevel, func(i int) { s.groupCard(c, &groups[i], searching) }, func() {
		if kit.AddCardHeight(c.Key("add-group"), "添加策略组", cardHeight).Clicked() {
			s.editor.openAdd(model.ModeManual)
		}
	})
}

// groupCard is a strategy group's hero card, opening its editor: its
// initial and name over "Manual · N" or "Auto · Region Hong Kong · N",
// the member in use with its flag and latency, the trend of that member's latency
// faintly along the bottom, and the members' flags ("N matches" while the
// search matches members) with "Switch" opening the member sheet (a
// Selector) or the latency test testing the members (a URLTest). A member group in
// use shows the latency of the server its choice comes down to. Its menu,
// the more button's and the context menu, shows the members, tests them
// (member groups unfolded into their servers), copies its share link and
// deletes it after a confirmation at the more button.
func (s *outboundsSection) groupCard(c *ui.Context, g *groupView, searching bool) {
	k := kit.T(c)
	id, title := g.id, g.title
	servers := s.w.groupServers(g)
	// Busy while the batch of the group's latency test is under way (groupBatch).
	batch := groupBatch(id)
	running := s.w.store.BatchTesting(batch)
	current := g.tileOf(s.inUse(g))
	// server is the outbound whose latency the card shows: the member in
	// use, or the server a member group in use comes down to.
	server := ""
	if current != nil {
		server = s.w.finalServer(current)
	}
	refocus := s.sheet.state.Refocus && s.sheet.id == id
	measure := func() { s.w.measureBatch(batch, servers) }
	opts := s.cardOptions(c, g.o, title)
	opts.Focus = refocus
	summary := fmt.Sprintf("%s · %d", g.kind, len(g.all))
	if searching && g.hits > 0 {
		summary = fmt.Sprintf("命中 %d", g.hits)
	}
	muted := !g.o.Enabled
	opts.Header = func() { cardTitle(c, func() { kit.HeroInitial(c, nodeview.GroupInitial(title)) }, title, g.invalid) }
	opts.HeaderEnd = func() {
		ui.Text(c, summary).FontSize(kit.FontCaption).FontWeight(400).TextColor(k.Label3).
			FontFeatures("tnum").SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	}
	// The detail is what the group goes through now, the member in use,
	// which a click on opens the members to switch, and its latency, which
	// a click tests the group again; a balancer, how many members are up.
	opts.Main = func() {
		if g.o.Type == model.ModeLoadBalance && len(g.all) > 0 {
			if n, total, ok := s.available(g); ok {
				cardDetail(c, muted, fmt.Sprintf("可用 %d / %d", n, total))
			} else {
				cardDetail(c, muted, fmt.Sprintf("%d 个成员", len(g.all)))
			}
		} else {
			s.groupCurrent(c, g, current, muted)
		}
		ui.Spacer(c)
		s.groupLatency(c, title, server, running, len(servers) > 0, measure)
	}
	opts.Menu = append(opts.Menu, kit.EntityMenuItem{Label: "成员", Icon: kit.SymList, Run: func(ui.Element) { s.sheet.show(id) }})
	opts.Menu = append(opts.Menu, kit.EntityMenuItem{Label: "测速", Icon: kit.SymGauge, Disabled: running || len(servers) == 0,
		Run: func(ui.Element) { measure() }})
	s.heroCard(c, g.o, title, opts)
	if refocus {
		s.sheet.state.Refocus = false
	}
}

// heroLink is a link on a hero card's bottom line, as "Switch": text in the
// accent color, underlined while hovered, label naming the button; it
// takes the click from the card around it. Busy, the work it started
// under way, it takes no clicks and shows the turning spinner before its
// text in Label3. Read Clicked on it.
func heroLink(c *ui.Context, text, label string, busy bool) ui.Element {
	k := kit.T(c)
	if busy {
		var cover ui.Element
		ui.Row(c).Height(19).PaddingX(3).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
			kit.SpinnerIn(c, 12, k.Label2)
			ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine()
			cover = kit.BusyCover(c, label)
		})
		return cover
	}
	b := ui.ButtonBase(c).Height(19).PaddingX(3).Radius(5).AlignItems(ui.Center).Shrink(0).Label(label)
	if b.Pressed() {
		b.Opacity(0.6)
	}
	b.Children(func() {
		t := ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Accent).SingleLine()
		if b.Hovered() {
			t.Underline()
		}
	})
	return b
}

// groupCurrent is the member group g uses now, "→ 🇭🇰 Hong Kong 01", or
// why there is none, a button opening the members to switch.
func (s *outboundsSection) groupCurrent(c *ui.Context, g *groupView, current *groupTile, muted bool) {
	k := kit.T(c)
	id := g.id
	b := ui.ButtonBase(c).Height(22).PaddingX(4).MarginStart(-4).Gap(5).Radius(6).AlignItems(ui.Center).
		Shrink(1).MinWidth(0).Label("切换 " + g.title).Transition(kit.HoverMotion)
	if b.Hovered() {
		b.Background(k.Fill)
	}
	if b.Pressed() {
		b.Opacity(0.6)
	}
	b.Children(func() {
		if current == nil {
			text := "未测速"
			switch {
			case len(g.all) == 0:
				text = "没有成员"
			case g.o.Type == model.ModeManual || g.o.Type == model.ModeFallback:
				text = "未选择"
			}
			cardDetail(c, true, text)
			return
		}
		ui.Text(c, "→").FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().Shrink(0)
		if current.flag != "" {
			ui.Text(c, current.flag).FontSize(kit.FontTable).SingleLine().Shrink(0)
		}
		cardDetail(c, muted, current.name)
	})
	if b.Clicked() {
		s.sheet.show(id)
	}
}

// groupLatency is the latency of server, the group's member in use, a
// button testing the group again; "Test" without one, a spinner while a
// test runs.
func (s *outboundsSection) groupLatency(c *ui.Context, title, server string, running, testable bool, measure func()) {
	k := kit.T(c)
	if running || s.w.store.Testing(server) {
		ui.Row(c).Height(19).AlignItems(ui.Center).Shrink(0).Children(func() {
			nodeview.TestingMark(c)
			kit.BusyCover(c, "测速 "+title)
		})
		return
	}
	result := s.w.store.Latency[server]
	text, fg, bg, ok := nodeview.LatencyTag(k, result)
	if !ok {
		if testable && heroLink(c, "测速", "测速 "+title, false).Clicked() {
			measure()
		}
		return
	}
	b := ui.ButtonBase(c).Height(19).Radius(6).AlignItems(ui.Center).Shrink(0).Label("测速 " + title).
		Transition(kit.HoverMotion)
	if b.Pressed() {
		b.Opacity(0.6)
	}
	b.Children(func() { kit.HeroLatency(c, text, fg, bg) })
	kit.Hint(c, b, nodeview.LatencyHint(result))
	if b.Clicked() && testable {
		measure()
	}
}
