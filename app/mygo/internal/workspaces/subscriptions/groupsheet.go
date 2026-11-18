package subscriptions

import (
	"context"
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

// groupSheet is the side sheet of a strategy group's members, opened by
// its card's "Switch" or the "Members" item of its menu: under a line of
// the group's kind, member count and filter, the members by region, as
// the Overview chooser lists them, each with its protocol and latency, the
// member in use on a neutral fill with a check. Choosing a Selector's
// member switches the running core's group (API.Select) while it runs
// the group, else makes it the group's default. A URLTest's members only
// show, the fastest checked. The header tests the members and opens the
// group's editor.
type groupSheet struct {
	s     *outboundsSection
	state kit.SideSheetState
	id    string
	list  ui.ListState
	// switching is set while the running core switches the group.
	switching bool
	// refocused tells that the card was asked last frame to take the
	// focus back as the sheet closed; a card that was not built by then
	// leaves it.
	refocused bool
	rows      groupSheetRows
}

// groupSheetRows are the sheet's rows, kept between frames until the
// document or the group changes, with the group's view they show.
type groupSheetRows struct {
	built    bool
	version  int
	id       string
	view     groupView
	rows     []groupSheetRow
	sections []nodeview.Section
	// tiles are each section's members, as indexes of view.all.
	tiles   [][]int
	headers bool
}

// groupSheetRow is a row of the sheet: a region's header, or the member
// view.all[tile].
type groupSheetRow struct {
	head bool
	sec  int
	tile int
}

// show opens the sheet of group id.
func (g *groupSheet) show(id string) {
	g.state.Open, g.state.Refocus = true, false
	g.id, g.refocused = id, false
	g.list.ScrollTo(0, ui.Start)
}

// rowsOf are the rows of group o's members, by region.
func (g *groupSheet) rowsOf(doc *model.Doc, o *model.Outbound) *groupSheetRows {
	version := g.s.w.store.Version()
	if g.rows.built && g.rows.version == version && g.rows.id == o.ID {
		return &g.rows
	}
	r := groupSheetRows{built: true, version: version, id: o.ID}
	info := g.s.w.infoOf(doc)
	// The page's rows hold the group's view while it shows the group, all
	// of its members included.
	found := false
	if rows := &g.s.w.rows; rows.built && rows.key.version == version {
		for _, v := range rows.groups {
			if v.id == o.ID {
				r.view, found = v, true
				break
			}
		}
	}
	if !found {
		if views := g.s.groupViews(doc, info, []*model.Outbound{o}, ""); len(views) == 1 {
			r.view = views[0]
		}
	}
	byTag := map[string]int{}
	var tags []string
	for i, t := range r.view.all {
		if _, taken := byTag[t.tag]; taken {
			continue
		}
		byTag[t.tag] = i
		tags = append(tags, t.tag)
	}
	r.sections = nodeview.Sections(info.regions, info.matcher, tags)
	r.headers = len(r.sections) > 1
	for i, sec := range r.sections {
		if r.headers {
			r.rows = append(r.rows, groupSheetRow{head: true, sec: i})
		}
		tiles := make([]int, 0, len(sec.Names))
		for _, tag := range sec.Names {
			r.rows = append(r.rows, groupSheetRow{sec: i, tile: byTag[tag]})
			tiles = append(tiles, byTag[tag])
		}
		r.tiles = append(r.tiles, tiles)
	}
	g.rows = r
	return &g.rows
}

func (g *groupSheet) view(c *ui.Context) {
	// The card takes the focus back in the next frame; past it, a card
	// scrolled out leaves it.
	if g.state.Refocus {
		if g.refocused {
			g.state.Refocus, g.refocused = false, false
		} else {
			g.refocused = true
			c.AnimationFrame()
		}
	}
	if !g.state.Shown() {
		return
	}
	doc := g.s.w.store.Doc()
	var o *model.Outbound
	for _, x := range doc.Outbounds {
		if x.ID == g.id && model.IsGroup(x.Type) {
			o = x
		}
	}
	if o == nil {
		g.state = kit.SideSheetState{}
		return
	}
	k := kit.T(c)
	id := o.ID
	rows := g.rowsOf(doc, o)
	view := &rows.view
	title := view.title
	// "Test this group" tests the servers under the members, member groups
	// unfolded.
	servers := g.s.w.groupServers(view)
	batch := groupBatch(id)
	running := g.s.w.store.BatchTesting(batch)
	choose := o.Type == "selector"
	current := g.s.inUse(view)
	kit.SideSheet(c, &g.state, title, func() {
		kit.IconButton(c, kit.SymGauge, "测速本组", kit.IconButtonOptions{Size: 32, IconSize: 16, Busy: running, BusyHint: "测速中"}).
			OnClick(func() { g.s.w.measureBatch(batch, servers) })
		kit.IconButton(c, kit.SymPencil, "编辑分组", kit.IconButtonOptions{Size: 32, IconSize: 16}).OnClick(func() {
			g.state.Open = false
			g.s.editor.openEdit(g.s.w.store.Doc(), id)
		})
	}, func() {
		summary := fmt.Sprintf("%s · %d 个成员", view.kind, len(view.all))
		if view.filter != "" {
			summary += " · " + view.filter
		}
		ui.Row(c).Gap(8).Padding(0, 18, 10).AlignItems(ui.Center).MinWidth(0).Shrink(0).Children(func() {
			ui.Text(c, summary).FontSize(kit.FontCaption).TextColor(k.Label2).FontFeatures("tnum").SingleLine().
				Ellipsis("…").Shrink(1).MinWidth(0)
		})
		if len(rows.rows) == 0 {
			ui.Text(c, "还没有成员").FontSize(kit.FontBody).TextColor(k.Label3).TextAlign(ui.Center).Padding(32, 16)
			return
		}
		g.list.Key = func(i int) any {
			if r := rows.rows[i]; !r.head {
				return rows.view.all[r.tile].id
			}
			return "region:" + rows.sections[rows.rows[i].sec].ID
		}
		g.list.Header = func(i int) bool { return rows.rows[i].head }
		ui.List(c.Key("group-members"), &g.list, len(rows.rows), func(i int) { g.row(c, rows, current, choose, i) }).
			Grow(1).MinHeight(0).Padding(0, 8, 8).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Label("成员")
	})
	if g.state.Refocus {
		// The card takes the focus back in the next frame.
		c.AnimationFrame()
	}
}

// row builds row i of the list: a region's header, its name and count,
// testing the servers under the region's members (nodeview.SectionHeader),
// or a member: its flag (a group its initial), name and protocol, then
// at the right whether the filter added it, a smart group's score, its
// latency tag (nodeview.MemberTest, with the core's state of it; a member
// group's, that of the server its choice comes down to, ViaTest), which a
// click tests again, and a check on the member in use. A Selector's member is
// a button choosing it; the context menu tests a member (a member group's
// server, or all its servers) and copies its share link or address.
func (g *groupSheet) row(c *ui.Context, rows *groupSheetRows, current string, choose bool, i int) {
	k := kit.T(c)
	r := rows.rows[i]
	w := g.s.w
	chains, chainsLive := w.chains()
	if r.head {
		sec := rows.sections[r.sec]
		names := make([]string, 0, len(rows.tiles[r.sec]))
		for _, j := range rows.tiles[r.sec] {
			names = append(names, chainName(&rows.view.all[j], chainsLive))
		}
		ids, key := chains.Servers(names...), groupRegionBatch(rows.view.id, sec.ID)
		nodeview.SectionHeader(c, "head-"+sec.ID, sec.Label, len(sec.Names), nodeHeaderH, k.Elevated,
			w.store.BatchTesting(key), func() { w.measureBatch(key, ids) })
		return
	}
	view := &rows.view
	t := &view.all[r.tile]
	member, live := g.s.liveMember(view, t)
	selected := t.id == current
	if view.o.Type == model.ModeLoadBalance {
		selected = live && member.Active
	}
	// A member group shows and tests the server its choice comes down to
	// (server, named via); its menu tests all its servers as well.
	isGroup := t.out != nil && model.IsGroup(t.out.Type)
	server, via := t.id, ""
	var servers []string
	if isGroup {
		final, fm, ok := chains.Final(chainName(t, chainsLive))
		server = fm.ID
		if ok {
			via = fm.Name
			if via == "" {
				via = final
			}
		}
		servers = chains.Servers(chainName(t, chainsLive))
	}
	testing := server != "" && w.store.Testing(server)
	var line ui.Element
	if choose {
		line = ui.ButtonBase(c).Label("选择 " + t.tag).Disabled(g.switching).Justify(ui.Start)
		if g.switching {
			line.Opacity(0.5)
		}
	} else {
		line = ui.Row(c)
	}
	line.Height(nodeRowH).PaddingX(10).Gap(8).Radius(10).AlignItems(ui.Center).Shrink(0).Transition(kit.HoverMotion)
	switch {
	case selected:
		line.Background(k.Fill)
	case line.Hovered() && !g.switching:
		line.Background(k.RowHover)
	}
	id, out, endpoint := t.id, t.out, t.endpoint
	line.ContextMenu(func(m *ui.Menu) {
		if t.testable && m.Item("测速").Disabled(testing).Chosen() {
			g.s.w.measureLatency([]string{id})
		}
		if isGroup {
			if m.Item("测速").Disabled(server == "" || testing).Chosen() {
				w.measureLatency([]string{server})
			}
			key := groupBatch(t.id)
			if m.Item("测速全部成员").Disabled(len(servers) == 0 || w.store.BatchTesting(key)).Chosen() {
				w.measureBatch(key, servers)
			}
		}
		if out != nil {
			if link, ok := subscription.FormatOutboundLink(out); ok && m.Item("复制分享链接").Chosen() {
				c.WriteClipboard(link)
				g.s.w.env.Toast("分享链接已复制")
			}
		}
		if endpoint != "" && m.Item("复制地址").Chosen() {
			c.WriteClipboard(endpoint)
			g.s.w.env.Toast("地址已复制")
		}
	})
	// A disabled member dims; availability shows in the tag alone.
	color := k.Label
	if !t.enabled {
		color = k.Label3
	}
	line.Children(func() {
		ui.Box(c).Size(24, 24).Center().Shrink(0).Children(func() {
			switch {
			case out != nil && model.IsGroup(out.Type):
				ui.Box(c).Size(22, 22).Radius(6).Background(k.Fill2).Center().Children(func() {
					ui.Text(c, nodeview.GroupInitial(t.tag)).FontSize(11).FontWeight(700).TextColor(k.Label2).SingleLine()
				})
			case t.flag != "":
				ui.Text(c, t.flag).FontSize(17).SingleLine()
			default:
				kit.Icon(c, kit.SymServer, 16).TextColor(k.Label3)
			}
		})
		weight := 500
		if selected {
			weight = 600
		}
		ui.Text(c, t.name).FontSize(14).FontWeight(weight).TextColor(color).SingleLine().Ellipsis("…").
			Shrink(1).MinWidth(0)
		kit.CapsuleTag(c, t.protocol, k.Label2, ui.Color{}, k.Hairline)
		ui.Box(c).Grow(1).MinWidth(0)
		if t.appended {
			ui.Text(c, "筛选加入").FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
		}
		if live && view.o.Type == model.ModeSmart {
			memberScore(c, member)
		}
		// Availability shows in the latency tag alone: the app's test,
		// else the core's state of the member.
		state := ""
		if live {
			state = member.State
		}
		switch {
		case t.testable:
			if nodeview.MemberTest(c, g.s.w.store.Latency[id], "测速 "+t.tag, testing, state).Clicked() {
				g.s.w.measureLatency([]string{id})
			}
		case isGroup && server != "":
			if nodeview.ViaTest(c, w.store.Latency[server], "测速 "+t.tag, testing, state, via).Clicked() {
				w.measureLatency([]string{server})
			}
		default:
			nodeview.MemberBadge(c, api.LatencyResult{}, false, state)
		}
		ui.Box(c).Size(16, 16).Shrink(0).Children(func() {
			if selected {
				kit.Icon(c, kit.SymCheck, 16).TextColor(k.Accent)
			}
		})
	})
	if choose && line.Clicked() {
		g.choose(view, t)
	}
}

// choose makes member t group view's choice: in the running core while
// it runs the group, keeping the core's selectors as switched once it
// has, else as the group's default in the configuration.
func (g *groupSheet) choose(view *groupView, t *groupTile) {
	w := g.s.w
	name, member := view.live, t.live
	if _, live := g.s.liveChoice(name); live {
		if g.switching {
			return
		}
		g.switching = true
		w.env.Run(func(ctx context.Context) error {
			return w.env.API.Select(ctx, name, member)
		}, func(err error) {
			g.switching = false
			if err != nil {
				w.env.ToastError(err)
				return
			}
			selectors := slices.Clone(w.env.Server.Selectors)
			for i := range selectors {
				if selectors[i].Name == name {
					selectors[i].Now = member
				}
			}
			w.env.Server.Selectors = selectors
		})
		return
	}
	id, ref := view.id, t.id
	if view.current == ref {
		return
	}
	w.update(func(d *model.Doc) {
		for _, x := range d.Outbounds {
			if x.ID == id {
				if x.Fields == nil {
					x.Fields = model.Fields{}
				}
				x.Fields["default_ref"] = ref
			}
		}
	})
}

// memberScore is a smart group's member's score in the running core
// beside its latency: the rate of successful connections, its hint the
// time to first byte and the score.
func memberScore(c *ui.Context, m api.SelectorMember) {
	if m.SuccessRate < 0 {
		return
	}
	k := kit.T(c)
	text := ui.Text(c, fmt.Sprintf("成功率 %.0f%%", m.SuccessRate*100)).FontSize(kit.FontCaption).TextColor(k.Label2).
		FontFeatures("tnum").SingleLine().Shrink(0)
	hint := fmt.Sprintf("首字节 %d ms", m.LatencyMS)
	if m.Score > 0 {
		hint += fmt.Sprintf(" · 得分 %.0f", m.Score)
	}
	kit.Hint(c, text, hint)
}
