package subscriptions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

// subsSection is the Subscriptions section: subscription cards, each
// opening its nodes in a side sheet, with the subscription editor.
type subsSection struct {
	w      *Workspace
	editor subEditor
	nodes  nodesSheet
	// refreshing are the subscriptions being fetched again
	// (refreshSubscription).
	refreshing map[string]bool
	// remove is the confirmation of a card's "Delete subscription".
	remove cardConfirm
}

func (s *subsSection) init(w *Workspace) {
	s.w = w
	s.editor.w = w
	s.nodes.w = w
}

// items are the subscriptions in the outbounds list's order.
func (s *subsSection) items(doc *model.Doc) []model.TopLevelItem {
	var out []model.TopLevelItem
	for _, item := range model.TopLevelOutboundItems(doc) {
		if item.Subscription != nil {
			out = append(out, item)
		}
	}
	return out
}

// heading is the section's heading and its count; the grid's ＋ card
// adds.
func (s *subsSection) heading(c *ui.Context, doc *model.Doc, count int) {
	kit.SectionHeading(c, "订阅", fmt.Sprintf("%d 个", count), nil)
}

// grid is the cards of items, reordered by dragging, then "+ Add subscription",
// opening the editor; hits counts a
// subscription's nodes matching the search, nil without one.
func (s *subsSection) grid(c *ui.Context, doc *model.Doc, items []model.TopLevelItem, hits map[string]int) {
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.Key()
	}
	reorderGrid(c, "subs", keys, s.w.reorderTopLevel, func(i int) {
		s.subCard(c, doc, items[i], hits[items[i].Subscription.ID])
	}, func() {
		if kit.AddCard(c.Key("add-subscription"), "添加订阅").Clicked() {
			s.editor.openAdd(doc)
		}
	})
}

// overlays are the section's editor, the nodes sheet and the
// confirmation of "Delete subscription" at its card's more button, when the list built
// that card in this pass.
func (s *subsSection) overlays(c *ui.Context, doc *model.Doc) {
	if at, ok := s.remove.anchor(); ok && findSubscription(doc, s.remove.id) != nil {
		id, title := s.remove.id, s.remove.title
		kit.ConfirmPopover(c, at, &s.remove.state, kit.ConfirmOptions{
			Title: "删除「" + title + "」？", Message: fmt.Sprintf("该订阅及其 %d 个节点都会被删除。", len(subscriptionNodes(doc, id))),
			Confirm: "删除", Danger: true,
			Run: func(done func(error)) {
				done(s.w.store.Update(func(d *model.Doc) { removeSubscription(d, id) }))
			},
		})
	}
	s.editor.view(c)
	s.nodes.view(c)
}

// nodesSheet is the side sheet of a subscription's nodes: its traffic, a
// search, "Sort by latency" and a
// speed test of its nodes, a rail of their regions down the left, which
// scrolls the list to a region's section and follows its scrolling, and
// the nodes by region, each with its protocol, address and latency, as
// the Overview chooser shows them.
type nodesSheet struct {
	w           *Workspace
	sheet       kit.SideSheetState
	id          string
	query       string
	sortLatency bool

	list ui.ListState
	// railTop is the rail item lit while the list is at its top, -1 for
	// "All"; railShown the item lit in the last frame; railScroll the
	// rail's scrolling.
	railTop, railShown int
	railScroll         ui.ScrollState

	rows nodeRows
}

// nodeRows are the rows of the nodes sheet, kept between frames until the
// document, the subscription, the search or the order changes.
type nodeRows struct {
	built bool
	key   nodeRowsKey
	rows  []nodeRow
	// sections are the regions of the nodes shown, heads the rows of
	// their headers, sectionIDs the ids of their nodes, which a header
	// tests; headers whether there are several to head.
	sections   []nodeview.Section
	heads      []int
	sectionIDs [][]string
	headers    bool
	// total is the subscription's nodes, shown those matching the search.
	total, shown int
}

// nodeRowsKey is what nodeRows are built from: latency is the store's
// LatencyGen while sorting by latency, else 0.
type nodeRowsKey struct {
	version     int
	id, query   string
	sortLatency bool
	latency     int
}

// nodeRow is a row of the nodes sheet: a region's header, or a node with
// what it shows of it.
type nodeRow struct {
	head bool
	sec  int
	node *model.Outbound
	// name is the node's tag without the subscription's prefix and,
	// when its flag leads the row, the flag; protocol its type's tag.
	name, flag, protocol, endpoint string
}

// Nodes sheet metrics, those of the Overview chooser.
const (
	nodeHeaderH = 38
	nodeRowH    = 40
	nodeRailW   = 60
)

// show opens the sheet of subscription id, searching its nodes for
// query, the page's search.
func (n *nodesSheet) show(id, query string) {
	n.sheet.Open, n.sheet.Refocus = true, false
	n.id, n.query, n.sortLatency = id, strings.TrimSpace(query), false
	n.list.ScrollTo(0, ui.Start)
	n.railTop, n.railShown = -1, -1
	n.railScroll.X, n.railScroll.Y = 0, 0
}

// nodeName is what the sheet shows of a node's tag: without the
// subscription's prefix, and without leading flags when flag shows.
func nodeName(sub *model.Subscription, o *model.Outbound, flag string) string {
	name := strings.TrimSpace(strings.TrimPrefix(o.Tag, sub.NamePrefix))
	if name == "" {
		name = o.Tag
	}
	return nodeview.BesideFlag(flag, name)
}

// rowsOf are the rows of sub's nodes matching query (lower case),
// grouped by region, each region sorted by latency when sortLatency.
func (n *nodesSheet) rowsOf(doc *model.Doc, sub *model.Subscription, query string) *nodeRows {
	key := nodeRowsKey{version: n.w.store.Version(), id: sub.ID, query: query, sortLatency: n.sortLatency}
	if n.sortLatency {
		key.latency = n.w.store.LatencyGen()
	}
	if n.rows.built && n.rows.key == key {
		return &n.rows
	}
	nodes := subscriptionNodes(doc, sub.ID)
	byTag := map[string]*model.Outbound{}
	var names []string
	for _, o := range nodes {
		if _, taken := byTag[o.Tag]; taken {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(o.Tag+" "+o.Type+" "+nodeEndpoint(o)), query) {
			continue
		}
		byTag[o.Tag] = o
		names = append(names, o.Tag)
	}
	regions := model.Regions(doc)
	sections := nodeview.Sections(regions, model.NewRegionMatcher(regions), names)
	r := nodeRows{built: true, key: key, sections: sections, headers: len(sections) > 1, total: len(nodes), shown: len(names)}
	latency := n.w.store.Latency
	for i, sec := range sections {
		tags := sec.Names
		if n.sortLatency {
			tags = slices.Clone(tags)
			slices.SortStableFunc(tags, func(a, b string) int {
				av, bv := latencyValue(latency, byTag[a].ID), latencyValue(latency, byTag[b].ID)
				switch {
				case av < bv:
					return -1
				case av > bv:
					return 1
				}
				return 0
			})
		}
		if r.headers {
			r.heads = append(r.heads, len(r.rows))
			r.rows = append(r.rows, nodeRow{head: true, sec: i})
		}
		ids := make([]string, len(tags))
		for j, tag := range tags {
			ids[j] = byTag[tag].ID
		}
		r.sectionIDs = append(r.sectionIDs, ids)
		for _, tag := range tags {
			o := byTag[tag]
			r.rows = append(r.rows, nodeRow{sec: i, node: o, name: nodeName(sub, o, sec.Flag), flag: sec.Flag,
				protocol: nodeview.ProtocolTag(o.Type), endpoint: nodeEndpoint(o)})
		}
	}
	n.rows = r
	return &n.rows
}

func (n *nodesSheet) view(c *ui.Context) {
	if !n.sheet.Shown() {
		return
	}
	doc := n.w.store.Doc()
	sub := findSubscription(doc, n.id)
	if sub == nil {
		n.sheet = kit.SideSheetState{}
		return
	}
	k := kit.T(c)
	id := sub.ID
	query := strings.ToLower(strings.TrimSpace(n.query))
	rows := n.rowsOf(doc, sub, query)
	var nodeIDs []string
	for _, o := range subscriptionNodes(doc, id) {
		nodeIDs = append(nodeIDs, o.ID)
	}
	// Busy while the batch of the subscription's test is under way.
	batch := subBatch(id)
	testing := n.w.store.BatchTesting(batch)
	kit.SideSheet(c, &n.sheet, sub.Tag, func() {
		n.w.subs.refreshButton(c, sub, 32, 15)
		kit.IconButton(c, kit.SymPencil, "编辑订阅", kit.IconButtonOptions{Size: 32, IconSize: 16}).OnClick(func() {
			n.sheet.Open = false
			n.w.subs.editor.openEdit(n.w.store.Doc(), id)
		})
	}, func() {
		usage := usageOf(sub, c.Now())
		count := fmt.Sprintf("%d 个节点", rows.total)
		if query != "" {
			count = fmt.Sprintf("%d / %d 个节点", rows.shown, rows.total)
		}
		for _, part := range []string{usage.traffic, usage.expiry} {
			if part != "" {
				count += " · " + part
			}
		}
		ui.Row(c).Gap(10).Padding(0, 18, 4).AlignItems(ui.Center).MinWidth(0).Shrink(0).Children(func() {
			if usage.bar {
				usageBar(c, usage)
			}
			text := ui.Text(c, count).FontSize(kit.FontCaption).TextColor(k.Label2).FontFeatures("tnum").SingleLine().
				Ellipsis("…").MinWidth(0)
			if !usage.bar {
				text.Grow(1)
			}
		})
		ui.Row(c).Gap(6).Padding(8, 12, 8, 14).AlignItems(ui.Center).Shrink(0).Children(func() {
			kit.Search(c, "node-search", &n.query, kit.SearchOptions{Placeholder: "搜索节点", Width: kit.EditorWidth}).
				Label("搜索节点")
			kit.IconButton(c, kit.SymArrowDownUp, "按延迟排序", kit.IconButtonOptions{Active: n.sortLatency, Size: 32}).
				OnClick(func() { n.sortLatency = !n.sortLatency })
			kit.IconButton(c, kit.SymGauge, "测速 "+sub.Tag, kit.IconButtonOptions{Size: 32, Busy: testing, BusyHint: "测速中"}).
				OnClick(func() { n.w.measureBatch(batch, nodeIDs) })
		})
		if rows.shown == 0 {
			text := "还没有节点"
			if rows.total > 0 {
				text = "没有匹配的节点"
			}
			ui.Text(c, text).FontSize(kit.FontBody).TextColor(k.Label3).TextAlign(ui.Center).Padding(32, 16)
			return
		}
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).
			Children(func() {
				if rows.headers {
					n.rail(c, rows)
				}
				n.list.Key = func(i int) any {
					if r := rows.rows[i]; !r.head {
						return r.node.ID
					}
					return "region:" + rows.sections[rows.rows[i].sec].ID
				}
				n.list.Header = func(i int) bool { return rows.rows[i].head }
				ui.List(c.Key("nodes-list"), &n.list, len(rows.rows), func(i int) { n.row(c, rows, i) }).
					Grow(1).MinWidth(0).Padding(0, 8, 8).Label("节点")
			})
	})
	if n.sheet.Refocus {
		// The card's node count takes the focus back in the next frame.
		c.AnimationFrame()
	}
}

// rail is the column of the regions beside the list, "All" first: the
// item of the section at the list's top is lit, and scrolls into the
// rail's view as it changes; an item scrolls the list to its section.
func (n *nodesSheet) rail(c *ui.Context, rows *nodeRows) {
	k := kit.T(c)
	current := -1
	if first, last := n.list.Visible(); last >= first && first < len(rows.rows) {
		current = rows.rows[first].sec
		if first == 0 {
			// At the top, the rail shows what was chosen there: "All", or
			// the first section.
			current = n.railTop
		}
	} else {
		current = n.railTop
	}
	ui.Scroll(c.Key("nodes-rail")).TrackScroll(&n.railScroll).Width(nodeRailW).Gap(4).Padding(8, 8).Shrink(0).
		BorderWidth(0, 1, 0, 0).BorderColor(k.HairlineSoft).Label("地区").Children(func() {
		follow := current != n.railShown
		n.railShown = current
		if follow && current < 0 {
			follow = false
			n.railScroll.ScrollTo(0, 0, ui.Smooth)
		}
		item := func(key, glyph, label string, on bool, icon kit.Symbol) ui.Element {
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
		if item("all", "", "全部", current < 0, kit.SymLayoutGrid).Clicked() {
			n.railTop = -1
			n.list.ScrollTo(0, ui.Start)
		}
		ui.Box(c).Height(1).Margin(2, 6).Background(k.HairlineSoft).Shrink(0)
		for i, sec := range rows.sections {
			glyph, icon := sec.Flag, kit.Symbol("")
			if glyph == "" {
				glyph = nodeview.GroupInitial(sec.Label)
			}
			if sec.ID == "" {
				icon = kit.SymCircleDashed
			}
			if item("rail-"+sec.ID, glyph, "跳到 "+sec.Label, current == i, icon).Clicked() {
				n.railTop = -1
				if i == 0 {
					n.railTop = 0
				}
				n.list.ScrollTo(rows.heads[i], ui.Start)
			}
		}
	})
}

// row builds row i of the list: a region's header, its name and count,
// testing the region's nodes (nodeview.SectionHeader), or a node: its
// flag, name, protocol, address and latency, which a click tests again;
// its context menu tests it and copies its share link or address.
func (n *nodesSheet) row(c *ui.Context, rows *nodeRows, i int) {
	k := kit.T(c)
	r := rows.rows[i]
	if r.head {
		sec := rows.sections[r.sec]
		ids, key := rows.sectionIDs[r.sec], subRegionBatch(n.id, sec.ID)
		nodeview.SectionHeader(c, "head-"+sec.ID, sec.Label, len(sec.Names), nodeHeaderH, k.Elevated,
			n.w.store.BatchTesting(key), func() { n.w.measureBatch(key, ids) })
		return
	}
	o := r.node
	testing := n.w.store.Testing(o.ID)
	line := ui.Row(c).Height(nodeRowH).PaddingX(10).Gap(8).Radius(10).AlignItems(ui.Center).Transition(kit.HoverMotion)
	if line.Hovered() {
		line.Background(k.RowHover)
	}
	link, linked := subscription.FormatOutboundLink(o)
	line.ContextMenu(func(m *ui.Menu) {
		if m.Item("测速").Disabled(testing).Chosen() {
			n.w.measureLatency([]string{o.ID})
		}
		if linked && m.Item("复制分享链接").Chosen() {
			c.WriteClipboard(link)
			n.w.env.Toast("分享链接已复制")
		}
		if r.endpoint != "" && m.Item("复制地址").Chosen() {
			c.WriteClipboard(r.endpoint)
			n.w.env.Toast("地址已复制")
		}
	})
	name := k.Label
	if !o.Enabled {
		name = k.Label3
	}
	line.Children(func() {
		ui.Box(c).Size(24, 24).Center().Shrink(0).Children(func() {
			if r.flag != "" {
				ui.Text(c, r.flag).FontSize(17).SingleLine()
				return
			}
			kit.Icon(c, kit.SymServer, 16).TextColor(k.Label3)
		})
		ui.Text(c, r.name).FontSize(14).FontWeight(500).TextColor(name).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		kit.CapsuleTag(c, r.protocol, k.Label2, ui.Color{}, k.Hairline)
		ui.Text(c, r.endpoint).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		if nodeview.LatencyTest(c, n.w.store.Latency[o.ID], "测速 "+o.Tag, testing).Clicked() {
			n.w.measureLatency([]string{o.ID})
		}
	})
}
