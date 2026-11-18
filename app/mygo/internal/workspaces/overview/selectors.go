package overview

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
)

// selectorState is the running core's selector groups and the chooser of
// one group's outbound.
type selectorState struct {
	// groups change through setGroups, or with gen bumped, so that the
	// views' cache follows.
	groups []api.Selector
	gen    int
	err    string
	// state is the runtime state the groups were loaded for; cancel ends
	// the load in flight.
	state  string
	cancel context.CancelFunc
	// applied is the store's AppliedGen the groups were loaded for.
	applied int

	open      bool
	group     string
	query     string
	switching bool
	reveal    bool
	// scroll is the chooser's list, which the region rail scrolls;
	// railTop the rail item lit while it is at the top, -1 for All.
	scroll  ui.ScrollState
	railTop int
	// railShown is the rail item lit in the last frame; railScroll the
	// rail's scrolling.
	railShown  int
	railScroll ui.ScrollState

	// sheet is the side sheet of all groups; expanded the groups it
	// shows the members of.
	sheet    kit.SideSheetState
	expanded map[string]bool

	derived selectorDerived
}

// selectorDerived is what the views derive from the groups and the
// document, kept between frames until either changes.
type selectorDerived struct {
	built    bool
	version  int
	platform string
	gen      int
	first    *api.Selector
	count    int
	// latency is the store's LatencyGen the configuration's groups chose
	// their fastest members by, while the core is off; 0 while it runs.
	latency int

	// groups are the groups shown, ordered; live whether they are the
	// running core's.
	groups []api.Selector
	live   bool
	// ids are the outbounds' ids by tag, members what the chooser shows
	// of them; matcher finds the regions of names.
	ids     map[string]string
	members map[string]nodeview.Member
	regions *model.RegionSettings
	matcher *model.RegionMatcher
	// chains bring the groups' members down to servers.
	chains nodeview.Groups

	// The chooser's matching members and their sections, for chooserKey
	// (its group and query); chosen the ids of the servers the members
	// stand for now, which its speed test tests, and sectionIDs those of
	// all servers under each section's members, which its header tests.
	chooserKey   string
	chooserBuilt bool
	options      []string
	sections     []nodeview.Section
	chosen       []string
	sectionIDs   [][]string
}

// setGroups replaces the groups.
func (s *selectorState) setGroups(groups []api.Selector) {
	s.groups = groups
	s.gen++
	if _, ok := s.chosenGroup(); s.open && !ok && !s.switching {
		// The group being chosen from is gone.
		s.open = false
	}
	for name := range s.expanded {
		if !slices.ContainsFunc(groups, func(g api.Selector) bool { return g.Name == name }) {
			delete(s.expanded, name)
		}
	}
}

// selectorView is the groups shown and what the views derive from them,
// rebuilt when the document's version, the host platform or the groups
// change. Its slices and maps are shared: read only.
func (w *Workspace) selectorView() *selectorDerived {
	s := &w.selector
	d := &s.derived
	var first *api.Selector
	if len(s.groups) > 0 {
		first = &s.groups[0]
	}
	version, platform, latency := w.store.Version(), w.store.Platform(), 0
	if len(s.groups) == 0 {
		latency = w.store.LatencyGen()
	}
	if d.built && d.version == version && d.platform == platform && d.gen == s.gen && d.first == first && d.count == len(s.groups) &&
		d.latency == latency {
		return d
	}
	doc := w.store.Doc()
	*d = selectorDerived{built: true, version: version, platform: platform, gen: s.gen, first: first, count: len(s.groups),
		latency: latency}
	// While the core runs the groups and their choices are its own;
	// otherwise the configuration's, with the default choices.
	d.live = len(s.groups) > 0
	groups := s.groups
	if !d.live {
		groups = nodeview.DocGroups(doc, w.store.Latency)
	}
	d.groups = orderedSelectors(groups, doc)
	d.ids = map[string]string{}
	for _, o := range doc.Outbounds {
		if _, taken := d.ids[o.Tag]; !taken {
			d.ids[o.Tag] = o.ID
		}
	}
	d.members = nodeview.Members(doc)
	d.regions = model.Regions(doc)
	d.matcher = model.NewRegionMatcher(d.regions)
	d.chains = nodeview.NewGroups(d.groups, d.members)
	return d
}

// memberIDs are the outbound ids of the servers the members of group
// matching query stand for now, a group member's choice's
// (nodeview.Groups.Chosen): those the chooser's speed test tests.
func (d *selectorDerived) memberIDs(group api.Selector, query string) []string {
	d.chooser(group, query)
	return d.chosen
}

// chooser is the members of group matching query, and their sections.
func (d *selectorDerived) chooser(group api.Selector, query string) ([]string, []nodeview.Section) {
	key := group.Name + "\x00" + query
	if d.chooserBuilt && d.chooserKey == key {
		return d.options, d.sections
	}
	var options []string
	for _, name := range group.All {
		if query == "" || strings.Contains(strings.ToLower(name), query) {
			options = append(options, name)
		}
	}
	d.chooserKey, d.chooserBuilt = key, true
	d.options, d.sections = options, nodeview.Sections(d.regions, d.matcher, options)
	d.chosen = d.chains.Chosen(options)
	d.sectionIDs = make([][]string, len(d.sections))
	for i, sec := range d.sections {
		d.sectionIDs[i] = d.chains.Servers(sec.Names...)
	}
	return d.options, d.sections
}

// invalidate drops loads in flight and loads again on the next frame.
func (s *selectorState) invalidate() {
	s.stop()
	s.state = "\x00"
}

func (s *selectorState) stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// follow loads the groups when the core starts running or a new
// configuration is applied, retrying every half second up to ten times
// until it reports some, and clears them when it stops.
func (s *selectorState) follow(w *Workspace, c *ui.Context) {
	state := w.env.Server.Runtime.State
	applied := w.store.AppliedGen()
	if !w.active || (state == s.state && applied == s.applied) {
		return
	}
	// A new applied configuration (a live apply, a restart or another
	// device's) changes the core's groups: they load again, the ones
	// shown staying until the new arrive.
	s.state, s.applied = state, applied
	s.stop()
	if !w.env.Server.HasRuntime || state != "running" {
		s.setGroups(nil)
		s.open = false
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		for attempt := 0; attempt <= 10 && ctx.Err() == nil; attempt++ {
			request, done := context.WithTimeout(ctx, 5*time.Second)
			groups, err := w.env.API.Selectors(request)
			done()
			w.env.Post(func() {
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					s.err = err.Error()
				} else {
					s.setGroups(groups)
					s.err = ""
				}
			})
			if err == nil && len(groups) > 0 {
				return
			}
			if sleep(ctx, 500*time.Millisecond) != nil {
				return
			}
		}
	}()
}

// ordered sorts the groups: selectors before URL tests, each in the
// configuration's outbound order.
func orderedSelectors(groups []api.Selector, doc *model.Doc) []api.Selector {
	order := map[string]int{}
	for i, o := range model.OrderedOutbounds(doc) {
		if _, ok := order[o.Tag]; !ok {
			order[o.Tag] = i
		}
	}
	rank := func(t string) int {
		if strings.EqualFold(t, "selector") {
			return 0
		}
		return 1
	}
	position := func(name string) int {
		if i, ok := order[name]; ok {
			return i
		}
		return math.MaxInt
	}
	sorted := slices.Clone(groups)
	slices.SortStableFunc(sorted, func(a, b api.Selector) int {
		if d := rank(a.Type) - rank(b.Type); d != 0 {
			return d
		}
		pa, pb := position(a.Name), position(b.Name)
		switch {
		case pa < pb:
			return -1
		case pa > pb:
			return 1
		}
		return 0
	})
	return sorted
}

// selectorGroups is the policy group card, always shown: the count, a link to
// the outbounds, and a compact tile per group with its choice, two
// columns (three on wide windows, one on narrow ones).
func (w *Workspace) selectorGroups(c *ui.Context) {
	view := w.selectorView()
	groups, live := view.groups, view.live
	if len(groups) == 0 {
		return
	}
	k := kit.T(c)
	columns := 2
	switch width := w.metrics.width; {
	case width >= 880:
		columns = 3
	case width > 0 && width < 420:
		columns = 1
	}
	dashboardCard(c, "策略组", 1).Margin(12, 0, 0).Gap(12).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).MinHeight(26).AlignItems(ui.Center).Children(func() {
			kit.Icon(c, kit.SymLayers, 16).TextColor(k.Label2)
			ui.Text(c, "策略组").FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).SingleLine()
			ui.Row(c).Height(20).PaddingX(7).Radius(kit.RadiusCapsule).Background(k.Fill).Shrink(0).Children(func() {
				ui.Text(c, fmt.Sprint(len(groups))).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label2).FontFeatures("tnum")
			})
			ui.Spacer(c)
			if kit.IconButton(c, kit.SymChevronRight, "全部策略组", kit.IconButtonOptions{Size: kit.SmallControl}).Clicked() {
				w.selector.sheet.Open = true
			}
		})
		ui.Grid(c).Columns(columns).Gap(8).Children(func() {
			for _, group := range groups {
				w.selectorCard(c, group, live)
			}
		})
	})
}

// selectorCard is a group's compact tile: an avatar with its initial,
// the name and the current choice, with the latency of the server it
// comes down to; live, it opens the chooser. Its context menu tests that
// server ("Test") or all the group's servers ("Test group").
func (w *Workspace) selectorCard(c *ui.Context, group api.Selector, live bool) {
	k := kit.T(c)
	view := w.selectorView()
	card := ui.ButtonBase(c.Key(group.Name)).MinHeight(60).MinWidth(0).Padding(10, 12).Gap(10).Radius(14).
		Background(k.Surface2).Label(group.Name).Transition(kit.HoverMotion)
	if card.Pressed() && live {
		card.Background(k.Fill2)
	} else if card.Hovered() && live {
		card.Background(k.RowHover)
	}
	now := group.Now
	if now == "" {
		now = "未选择"
	}
	card.Children(func() {
		ui.Box(c).Size(36, 36).Radius(10).Background(k.Fill).Center().Shrink(0).Children(func() {
			ui.Text(c, nodeview.GroupInitial(group.Name)).FontSize(15).FontWeight(600).TextColor(k.Label2)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, group.Name).FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).SingleLine()
			ui.Text(c, now).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine()
		})
		if id := w.choiceID(group.Now); id != "" {
			nodeview.LatencyBadge(c, w.store.Latency[id], w.store.Testing(id))
		}
	})
	w.groupMenu(card, view.chains, group.Name, true)
	if card.Clicked() && live {
		s := &w.selector
		s.open, s.group, s.query, s.err, s.reveal = true, group.Name, "", "", true
		s.scroll.Y, s.railTop = 0, -1
	}
}

// choiceID is the id of the server a group's choice now stands for: the
// choice itself, or the one a group chosen has in turn
// (nodeview.Groups.Final), "" when none is known. The groups are the
// running core's, else the configuration's with their default choices.
func (w *Workspace) choiceID(now string) string {
	return w.selectorView().chains.FinalID(now)
}

// measure tests the outbounds ids, keeping the other results.
func (w *Workspace) measure(ids []string) {
	if !w.store.MeasureLatencyOf(ids) {
		w.env.Toast("没有可测速的节点")
	}
}

// measureBatch tests ids as the batch entry key (Store.MeasureBatch),
// busy while Store.BatchTesting(key).
func (w *Workspace) measureBatch(key string, ids []string) {
	if !w.store.MeasureBatch(key, ids) {
		w.env.Toast("没有可测速的节点")
	}
}

// groupBatch is the batch key of testing all the servers under group
// name, the same from its tile, its sheet row and its members' menus;
// chooserBatch that of the chooser's speed test of group name, and
// regionBatch that of the header of region id in it.
func groupBatch(name string) string      { return "overview/group:" + name }
func chooserBatch(name string) string    { return "overview/chooser:" + name }
func regionBatch(name, id string) string { return "overview/region:" + name + "\x00" + id }

// groupMenu gives e, the element of name (a group, or a group's member
// as a server or a group), its context menu: "Test", testing the server
// name stands for now (nodeview.Groups.Final), and for a group all the
// servers under it, its groups unfolded: "Test group" when e is the group
// itself (own), else "Test all members", a batch (groupBatch). "Test" is
// disabled while its server is tested, the batch while it is under way.
func (w *Workspace) groupMenu(e ui.Element, chains nodeview.Groups, name string, own bool) {
	group := chains.IsGroup(name)
	e.ContextMenu(func(m *ui.Menu) {
		now := chains.FinalID(name)
		if m.Item("测速").Disabled(now == "" || w.store.Testing(now)).Chosen() {
			w.measure([]string{now})
		}
		if !group {
			return
		}
		all := chains.Servers(name)
		label := "测速全部成员"
		if own {
			label = "测速本组"
		}
		key := groupBatch(name)
		if m.Item(label).Disabled(len(all) == 0 || w.store.BatchTesting(key)).Chosen() {
			w.measureBatch(key, all)
		}
	})
}

// chosenGroup is the group the chooser shows.
func (s *selectorState) chosenGroup() (api.Selector, bool) {
	for _, g := range s.groups {
		if g.Name == s.group {
			return g, true
		}
	}
	return api.Selector{}, false
}

// choose switches group to outbound in the running core; the chooser of
// the group closes once it has.
func (w *Workspace) choose(group, outbound string) {
	s := &w.selector
	if s.switching {
		return
	}
	s.switching, s.err = true, ""
	w.env.Run(func(ctx context.Context) error {
		return w.env.API.Select(ctx, group, outbound)
	}, func(err error) {
		s.switching = false
		if err != nil {
			s.err = err.Error()
			return
		}
		for i := range s.groups {
			if s.groups[i].Name == group {
				s.groups[i].Now = outbound
			}
		}
		s.gen++
		if s.group == group {
			s.open = false
		}
	})
}

// Chooser list metrics: rows and section headers have fixed heights, so
// the region rail can scroll the list to a section.
const (
	chooserHeaderH = 38
	chooserRowH    = 40
	chooserPadY    = 8
)

// Chooser dialog size: its width, its search bar's height, its region
// rail's width, and its list's height at most.
const (
	chooserWidth     = 600
	chooserSearchH   = 54
	chooserRailW     = 60
	chooserMaxHeight = 460
)

// syncChooser closes the chooser when its group is gone.
func (s *selectorState) syncChooser() {
	if _, ok := s.chosenGroup(); s.open && !ok {
		s.open = false
	}
}

// selectorDialog chooses a group's outbound as a command palette over the
// window, as wide as its content: the search across the top, a rail of
// the regions found in the members' names down the left (All first),
// which scrolls the list beside it to a region's section, and the
// members edge to edge, each with its protocol, latency and subscription
// as tags and a check on the current choice. It opens on the section of
// the choice: at its start when the choice shows there, else with the
// choice centered.
func (w *Workspace) selectorDialog(c *ui.Context) {
	s := &w.selector
	group, _ := s.chosenGroup()
	wasOpen := s.open
	ui.DialogBase(c, &s.open, func(backdrop, panel ui.Element) {
		k := kit.T(c)
		view := w.selectorView()
		width, h := c.Size()
		backdrop.Background(k.Scrim).Padding(16)
		panel.Width(min(chooserWidth, max(width-32, 280))).Radius(kit.RadiusDialog).Background(k.Elevated).
			Border(0.5, k.GlassEdge).Shadow(0, 24, 60, 0, k.Shadow.Alpha(0.3)).Clip().Label("选择 " + group.Name)
		var input, test ui.Element
		// Busy while the test it started is under way, a member's own
		// test or another entry's leaving it alone.
		batch := chooserBatch(group.Name)
		testing := w.store.BatchTesting(batch)
		ui.Row(c).Height(chooserSearchH).PaddingX(18).Gap(10).AlignItems(ui.Center).Shrink(0).
			BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
			kit.Icon(c, kit.SymSearch, 17).TextColor(k.Label3)
			input = ui.TextInputBase(c.Key("selector-search"), &s.query).Grow(1).MinWidth(0).FontSize(15).
				TextColor(k.Label).Placeholder("在「" + group.Name + "」中搜索").SingleLine().Label("搜索节点")
			test = kit.IconButton(c, kit.SymGauge, "测速", kit.IconButtonOptions{Size: kit.SmallControl + 4, IconSize: 16,
				Busy: testing, Hint: "测速当前列表", BusyHint: "测速中"})
		})
		if s.reveal {
			input.AutoFocus()
		}
		query := strings.ToLower(strings.TrimSpace(s.query))
		options, sections := view.chooser(group, query)
		if test.Clicked() {
			w.measureBatch(batch, view.memberIDs(group, query))
		}
		if input.Submitted() && len(options) > 0 {
			w.choose(group.Name, options[0])
		}
		// A single region, Unrecognized included, shows neither the rail nor headers.
		headers := len(sections) > 1
		offsets := make([]float32, len(sections))
		y := float32(chooserPadY)
		for i, sec := range sections {
			offsets[i] = y
			if headers {
				y += chooserHeaderH
			}
			y += float32(len(sec.Names)) * chooserRowH
		}
		content := y + chooserPadY
		if len(options) == 0 {
			content = 96
		}
		if headers {
			content = max(content, float32(len(sections)+1)*44+2*chooserPadY+9)
		}
		height := min(content, chooserMaxHeight, max(h-32-chooserSearchH, 160))
		if s.reveal {
			s.scroll.Y, s.railTop = revealChoice(sections, offsets, headers, group.Now, height)
		}
		current := -1
		for i, off := range offsets {
			if s.scroll.Y+1 >= off-chooserPadY {
				current = i
			}
		}
		// At the top, the rail shows what was chosen there: All, or the
		// first section (Unrecognized starts the list at the top as well).
		if s.scroll.Y < 1 {
			current = s.railTop
		}
		members := view.members
		ui.Row(c).Height(height).AlignItems(ui.Stretch).Shrink(0).Children(func() {
			if headers {
				ui.Scroll(c.Key("selector-rail")).TrackScroll(&s.railScroll).Width(chooserRailW).Gap(4).Padding(chooserPadY, 8).Shrink(0).
					BorderWidth(0, 1, 0, 0).BorderColor(k.HairlineSoft).Label("地区").Children(func() {
					// The lit item follows the list's scrolling: it scrolls
					// into the rail's view as it changes; All lit brings the
					// rail to its top, padding and all.
					follow := current != s.railShown
					s.railShown = current
					if follow && current < 0 {
						follow = false
						s.railScroll.ScrollTo(0, 0, ui.Smooth)
					}
					rail := func(key, glyph, label string, on bool, icon kit.Symbol) ui.Element {
						b := ui.ButtonBase(c.Key(key)).Size(44, 40).Radius(10).Center().Label(label).Shrink(0).
							Transition(kit.HoverMotion)
						if on && follow {
							b.ScrollIntoView()
						}
						switch {
						case on:
							b.Background(k.Fill2).TextColor(k.Label)
						case b.Hovered():
							b.Background(k.Fill)
						default:
							b.TextColor(k.Label2)
						}
						kit.Hint(c, b, label)
						return b.Children(func() {
							if icon != "" {
								kit.Icon(c, icon, 18)
								return
							}
							ui.Text(c, glyph).FontSize(19).SingleLine()
						})
					}
					if rail("all", "", "全部", current < 0, kit.SymLayoutGrid).Clicked() {
						s.railTop = -1
						s.railScroll.ScrollTo(0, 0, ui.Smooth)
						s.glideTo(0, height)
					}
					ui.Box(c).Height(1).Margin(2, 6).Background(k.HairlineSoft).Shrink(0)
					for i, sec := range sections {
						glyph, icon := sec.Flag, kit.Symbol("")
						if glyph == "" {
							glyph = nodeview.GroupInitial(sec.Label)
						}
						if sec.ID == "" {
							icon = kit.SymCircleDashed
						}
						if rail("rail-"+sec.ID, glyph, "跳到 "+sec.Label, current == i, icon).Clicked() {
							s.railTop = -1
							if i == 0 {
								s.railTop = 0
							}
							s.glideTo(offsets[i]-chooserPadY, height)
						}
					}
				})
			}
			ui.Scroll(c.Key("selector-list")).TrackScroll(&s.scroll).Grow(1).MinWidth(0).Padding(chooserPadY, 8).
				Label("节点").Children(func() {
				if len(options) == 0 {
					ui.Text(c, "没有匹配的节点").FontSize(kit.FontBody).TextColor(k.Label3).TextAlign(ui.Center).Padding(24, 16)
					return
				}
				for i, sec := range sections {
					if headers {
						// A header tests the servers under its members, their
						// groups unfolded.
						ids, key := view.sectionIDs[i], regionBatch(group.Name, sec.ID)
						nodeview.SectionHeader(c, "head-"+sec.ID, sec.Label, len(sec.Names), chooserHeaderH, ui.Color{},
							w.store.BatchTesting(key), func() { w.measureBatch(key, ids) })
					}
					for _, name := range sec.Names {
						w.chooserRow(c, group, name, sec.Flag, members[name])
					}
				}
			})
		})
		if s.err != "" {
			ui.Text(c, s.err).FontSize(kit.FontTable).TextColor(k.Danger).Selectable().Padding(10, 18).Shrink(0).
				BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft)
		}
		s.reveal = false
	})
	if wasOpen && !s.open && s.switching {
		// A switch in flight keeps the chooser open.
		s.open = true
	}
}

// glideTo scrolls the chooser's list to y, a list as tall as height. A
// place more than two lists away is reached at speed: the list jumps to
// one list short of it, then glides the rest, quick at first and easing
// out, as a flick would arrive.
func (s *selectorState) glideTo(y, height float32) {
	from := s.scroll.Y
	if d := y - from; d > 2*height || d < -2*height {
		if d > 0 {
			s.scroll.Y = y - height
		} else {
			s.scroll.Y = y + height
		}
		s.scroll.ScrollTo(0, y, ui.Animation{Duration: 280 * time.Millisecond, Ease: ui.EaseOut})
		return
	}
	s.scroll.ScrollTo(0, y, ui.Smooth)
}

// revealChoice is where the list opens for the choice now: its section's
// start when the choice shows from there, else the choice centered; and
// the rail item lit at the top (-1 for All).
func revealChoice(sections []nodeview.Section, offsets []float32, headers bool, now string, height float32) (float32, int) {
	for i, sec := range sections {
		j := slices.Index(sec.Names, now)
		if j < 0 {
			continue
		}
		start := offsets[i] - chooserPadY
		top := offsets[i] + float32(j)*chooserRowH
		if headers {
			top += chooserHeaderH
		}
		if top+chooserRowH-start <= height-chooserPadY {
			if start < 1 {
				return 0, i
			}
			return start, -1
		}
		return max(top-(height-chooserRowH)/2, 0), -1
	}
	return 0, -1
}

// chooserRow is a member of the chooser: its flag (or kind), its name,
// its protocol and latency as tags, its subscription, and a check on the
// current choice. A group member shows its choice after its name and
// the latency of the server its choice comes down to, which its hover
// text names. Its context menu tests the server it stands for now, and a
// group member's servers (groupMenu).
func (w *Workspace) chooserRow(c *ui.Context, group api.Selector, name, flag string, m nodeview.Member) {
	chains := w.selectorView().chains
	selected := name == group.Now
	manual := manualGroup(group)
	// The menu is the holder's: a disabled row (switching, or a group the
	// core chooses for) takes no secondary click.
	holder := ui.Column(c.Key(name)).Shrink(0)
	w.groupMenu(holder, chains, name, false)
	holder.Children(func() { w.chooserButton(c, group, name, flag, m, selected, manual, chains) })
}

// chooserButton is the line of chooserRow: a manual group's member a
// button choosing it, another group's a plain row, out of the keyboard's
// way, as the core chooses for that group.
func (w *Workspace) chooserButton(c *ui.Context, group api.Selector, name, flag string, m nodeview.Member,
	selected, manual bool, chains nodeview.Groups) {
	s := &w.selector
	k := kit.T(c)
	var row ui.Element
	if manual {
		row = ui.ButtonBase(c).Disabled(s.switching)
	} else {
		row = ui.Row(c)
	}
	row.Height(chooserRowH).PaddingX(10).Gap(8).Radius(10).AlignItems(ui.Center).Label(name).Shrink(0).
		Transition(kit.HoverMotion)
	// The choice reads in the label's color on a neutral fill, with an
	// accent check.
	switch {
	case selected:
		row.Background(k.Fill)
	case manual && row.Hovered() && !s.switching:
		row.Background(k.RowHover)
	}
	if s.switching {
		row.Opacity(0.5)
	}
	member, _ := group.Member(name)
	row.Children(func() {
		ui.Box(c).Size(24, 24).Center().Shrink(0).Children(func() {
			switch {
			case m.Group:
				// A group shows its initial, as its policy group tile does.
				ui.Box(c).Size(22, 22).Radius(6).Background(k.Fill2).Center().Children(func() {
					ui.Text(c, nodeview.GroupInitial(name)).FontSize(11).FontWeight(700).TextColor(k.Label2).SingleLine()
				})
			case flag != "":
				ui.Text(c, flag).FontSize(17).SingleLine()
			default:
				kit.Icon(c, kit.SymServer, 16).TextColor(k.Label3)
			}
		})
		weight := 500
		if selected {
			weight = 600
		}
		shown := m.Name
		if shown == "" {
			shown = name
		}
		// The flag leads the row already.
		shown = nodeview.BesideFlag(flag, shown)
		ui.Text(c, shown).FontSize(14).FontWeight(weight).TextColor(k.Label).SingleLine().Ellipsis("…").
			Shrink(1).MinWidth(0)
		now := ""
		if m.Group {
			now = chains.Choice(name)
		}
		if now != "" {
			// A group shows the outbound it has chosen right after its
			// name, before its tags.
			ui.Row(c).Gap(4).Shrink(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
				kit.Icon(c, kit.SymArrowRight, 12).TextColor(k.Label3).Shrink(0)
				ui.Text(c, now).FontSize(kit.FontTable).TextColor(k.Label2).SingleLine().Ellipsis("…").MinWidth(0)
			})
		}
		if m.Protocol != "" {
			kit.CapsuleTag(c, m.Protocol, k.Label2, ui.Color{}, k.Hairline)
		}
		// Availability shows in the latency tag alone: the app's test,
		// else the core's state of the member. A group shows the test of
		// the server its choice comes down to, naming it.
		var result api.LatencyResult
		testing, via := false, ""
		if final, fm, ok := chains.Final(name); ok {
			result, testing = w.store.Latency[fm.ID], w.store.Testing(fm.ID)
			if m.Group {
				via = fm.Name
				if via == "" {
					via = final
				}
			}
		}
		nodeview.ViaBadge(c, result, testing, member.State, via)
		source := m.Source
		if m.Group {
			source = ""
		}
		ui.Text(c, source).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().Ellipsis("…").
			Grow(1).MinWidth(0)
		ui.Box(c).Size(16, 16).Shrink(0).Children(func() {
			if selected {
				kit.Icon(c, kit.SymCheck, 16).TextColor(k.Accent)
			}
		})
	})
	if manual && row.Clicked() {
		w.choose(group.Name, name)
	}
}
