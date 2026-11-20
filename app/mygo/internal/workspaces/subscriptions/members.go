package subscriptions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

type memberGroup struct {
	id, label  string
	candidates []model.Target
}

// memberCandidates are the targets a group may take, in a stable order:
// other outbounds (disabled nodes of disabled subscriptions only when
// chosen) and enabled or chosen endpoints.
func memberCandidates(doc *model.Doc, id string, selected map[string]bool, opening []string) []model.Target {
	active := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			active[s.ID] = true
		}
	}
	var targets []model.Target
	for _, o := range doc.Outbounds {
		if o.ID != id && (o.Enabled || selected[o.ID] || o.SubID == "" || active[o.SubID]) {
			targets = append(targets, model.Target{Outbound: o})
		}
	}
	for _, e := range doc.Endpoints {
		if e.Enabled || selected[e.ID] {
			targets = append(targets, model.Target{Endpoint: e})
		}
	}
	return model.StableGroupMemberOrder(targets, func(t model.Target) string { return t.ID() }, opening)
}

// memberGroups groups candidates by source: the manual outbounds, the
// policy groups, the endpoints, each subscription.
func memberGroups(doc *model.Doc, candidates []model.Target) []memberGroup {
	names := map[string]string{}
	for _, s := range doc.Subscriptions {
		names[s.ID] = s.Tag
	}
	var manual, groups, endpoints, other []model.Target
	bySub := map[string][]model.Target{}
	for _, t := range candidates {
		switch {
		case t.Endpoint != nil:
			endpoints = append(endpoints, t)
		case model.IsGroup(t.Outbound.Type):
			groups = append(groups, t)
		case t.Outbound.SubID == "":
			manual = append(manual, t)
		default:
			if _, ok := names[t.Outbound.SubID]; ok {
				bySub[t.Outbound.SubID] = append(bySub[t.Outbound.SubID], t)
			} else {
				other = append(other, t)
			}
		}
	}
	var out []memberGroup
	if len(manual) > 0 {
		out = append(out, memberGroup{"manual", "手动出站", manual})
	}
	if len(groups) > 0 {
		out = append(out, memberGroup{"groups", "策略组", groups})
	}
	if len(endpoints) > 0 {
		out = append(out, memberGroup{"endpoints", "Endpoint", endpoints})
	}
	for _, s := range doc.Subscriptions {
		if nodes := bySub[s.ID]; len(nodes) > 0 {
			label := s.Tag
			if label == "" {
				label = "未命名订阅"
			}
			out = append(out, memberGroup{"subscription:" + s.ID, label, nodes})
		}
	}
	if len(other) > 0 {
		out = append(out, memberGroup{"other", "其他", other})
	}
	return out
}

// changeMembers adds ids to members, or removes them.
func changeMembers(members, ids []string, add bool) []string {
	if add {
		out := slices.Clone(members)
		for _, id := range ids {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out
	}
	return slices.DeleteFunc(slices.Clone(members), func(m string) bool { return slices.Contains(ids, m) })
}

// testMenu gives row, that of candidate t, a context menu testing it
// while it is a node with a server.
func testMenu(w *Workspace, row ui.Element, t model.Target) {
	o := t.Outbound
	if o == nil || model.IsGroup(o.Type) || jsv.Text(o.Fields["server"]) == "" {
		return
	}
	id := o.ID
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("测速").Disabled(w.store.Testing(id)).Chosen() {
			w.measureLatency([]string{id})
		}
	})
}

// moveMember moves id to target's place among members.
func moveMember(members []string, id, target string) []string {
	from, to := slices.Index(members, id), slices.Index(members, target)
	if from < 0 || to < 0 || from == to {
		return members
	}
	out := slices.Delete(slices.Clone(members), from, from+1)
	return slices.Insert(out, to, id)
}

// memberDrag is a hand-picked member dragged within the "Added manually"
// list.
type memberDrag struct{ id string }

// manualRowH is a row of the "Added manually" list.
const manualRowH = 28

// manualSection is "Added manually": "+ Add node" opening the node picker, then
// the members chosen by hand, in their order, each removable; fallback
// groups number them and take a drag to reorder them, their order being
// the fallback's. Every row is built, so that a row dragged stays while
// it scrolls away; each is light: the targets and their region codes come
// from the index, and the remove button is built only on the row hovered.
func (e *outEditor) manualSection(c *ui.Context, doc *model.Doc, o *model.Outbound) {
	k := kit.T(c)
	g := &e.group
	idx := g.index.of(e.w.store.Version(), doc, o.ID)
	ordered := o.Type == model.ModeFallback
	ui.Column(c.Key("manual")).Gap(8).Shrink(0).Children(func() {
		var add ui.Element
		ui.Row(c).Gap(6).Shrink(0).Children(func() {
			sourceCaption(c, "手动添加").Grow(1)
			add = linkButton(c, "add-node", "＋ 添加节点")
		})
		if add.Clicked() {
			g.nodeMenu, g.nodeQuery = true, ""
			g.nodeToggled, g.nodeOpen = map[string]bool{}, nil
			g.nodeList = ui.ListState{}
		}
		e.nodePicker(c, add, doc, o)
		shown := idx.shown(o.Members)
		if len(shown) == 0 {
			return
		}
		ui.Column(c.Key("manual-list")).Shrink(0).Children(func() {
			for i, t := range shown {
				e.manualRow(c, idx, o, t, i, ordered, k)
			}
		})
	})
}

// manualRow is a member of the "Added manually" list: its place (fallback), its
// region code and name, and × on hover.
func (e *outEditor) manualRow(c *ui.Context, idx *nodeIndex, o *model.Outbound, t model.Target, i int, ordered bool, k *kit.Tokens) {
	id := t.ID()
	row := ui.Row(c.Key("m-" + id)).Height(manualRowH).Gap(6).PaddingX(6).Radius(7).Shrink(0).Transition(kit.HoverMotion)
	if ordered {
		row.Drag(memberDrag{id})
		if row.Dragging() {
			row.Opacity(0.45)
		}
	}
	hovered := row.Hovered()
	switch {
	case ordered && dragOverOther(row, id):
		row.Background(k.AccentWeak)
	case hovered && !row.Dragging():
		row.Background(k.Fill)
	default:
		row.Background(k.Fill.Alpha(0))
	}
	if ordered {
		if v, ok := ui.Drop[memberDrag](row); ok && v.id != id {
			o.Members = moveMember(o.Members, v.id, id)
		}
	}
	testMenu(e.w, row, t)
	row.Children(func() {
		if ordered {
			memberOrder(c, i+1, k.Label3)
		}
		if code := idx.codes[id]; code != "" {
			regionBadge(c, code, false)
		}
		ui.Text(c, t.Tag()).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		if hovered && kit.IconButton(c, kit.SymX, "移除 "+t.Tag(), kit.IconButtonOptions{Size: 20, IconSize: 11}).Clicked() {
			o.Members = changeMembers(o.Members, []string{id}, false)
		}
	})
}

// dragOverOther reports whether another member is dragged over row.
func dragOverOther(row ui.Element, id string) bool {
	v, over := ui.DragOver[memberDrag](row)
	return over && v.id != id
}

// memberOrder is a fallback member's place, its priority, before its
// name.
func memberOrder(c *ui.Context, n int, color ui.Color) {
	ui.Text(c, fmt.Sprint(n)).FontSize(kit.FontTable).FontWeight(600).FontFeatures("tnum").TextColor(color).
		Width(18).Shrink(0).SingleLine()
}

// pickerBigList is the candidates beyond which the node picker opens
// only the sources holding members.
const pickerBigList = 200

// pickerRowH is a candidate row of the node picker.
const pickerRowH = 30

// pickerSource is a source of the node picker: its candidates, their
// search text in lower case, and how many are members.
type pickerSource struct {
	id, label string
	targets   []model.Target
	search    []string
	chosen    int
}

// pickerCache keeps the node picker's sources with what they were
// derived from: the document's version and the members.
type pickerCache struct {
	key      string
	sources  []pickerSource
	selected map[string]bool
	total    int
}

// pickerLine is a line of the node picker's list: a source's heading
// (row < 0) or a candidate of it.
type pickerLine struct{ source, row int }

// nodePicker is the popover of "+ Add node" at anchor: a search over the
// candidates grouped by source, each source folding, in one list building
// only the lines in view; a row toggles its node in the members, the
// popover staying open.
func (e *outEditor) nodePicker(c *ui.Context, anchor ui.Element, doc *model.Doc, o *model.Outbound) {
	g := &e.group
	if !g.nodeMenu {
		return
	}
	k := kit.T(c)
	pc := e.pickerSources(doc, o)
	if g.nodeOpen == nil {
		// The sources holding members as the picker opened start open, or
		// all of them in a short list; choosing does not fold them.
		g.nodeOpen = map[string]bool{}
		for _, ps := range pc.sources {
			g.nodeOpen[ps.id] = pc.total <= pickerBigList || ps.chosen > 0
		}
	}
	q := strings.ToLower(strings.TrimSpace(g.nodeQuery))
	var lines []pickerLine
	for si, ps := range pc.sources {
		head := len(lines)
		lines = append(lines, pickerLine{si, -1})
		open := q != "" || g.nodeToggled[ps.id] != g.nodeOpen[ps.id]
		any := false
		for ri := range ps.targets {
			if q != "" && !strings.Contains(ps.search[ri], q) {
				continue
			}
			any = true
			if !open {
				break
			}
			lines = append(lines, pickerLine{si, ri})
		}
		if !any {
			lines = lines[:head]
		}
	}
	ui.PopoverBase(c, anchor, &g.nodeMenu, func(panel ui.Element) {
		kit.Popup(c, panel).Margin(6, 0, 0, 0).Width(360).Height(min(520, kit.PopoverRoom(c, anchor, 6))).
			Radius(16).Clip().Label("添加节点")
		panel.AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd)
		ui.Column(c).Padding(10, 10, 6).Shrink(0).Children(func() {
			kit.Input(c, "node-search", &g.nodeQuery, "搜索节点或订阅").AutoFocus()
		})
		if len(lines) == 0 {
			ui.Text(c, "没有匹配的节点").FontSize(kit.FontBody).TextColor(k.Label3).Padding(18, 12).
				TextAlign(ui.Center).FillWidth()
			return
		}
		g.nodeList.Key = func(i int) any {
			l := lines[i]
			ps := &pc.sources[l.source]
			if l.row < 0 {
				return "s:" + ps.id
			}
			return "c:" + ps.id + "/" + ps.targets[l.row].ID()
		}
		g.nodeList.Header = func(i int) bool { return lines[i].row < 0 }
		ui.List(c.Key("node-list"), &g.nodeList, len(lines), func(i int) {
			l := lines[i]
			ps := &pc.sources[l.source]
			if l.row < 0 {
				e.pickerHead(c, ps, q != "" || g.nodeToggled[ps.id] != g.nodeOpen[ps.id], q == "")
				return
			}
			t := ps.targets[l.row]
			id := t.ID()
			on := pc.selected[id]
			rowLabel := "加入 " + t.Tag()
			if on {
				rowLabel = "移除 " + t.Tag()
			}
			row := ui.ButtonBase(c.Key("cand-" + id)).Height(pickerRowH).PaddingX(8).Gap(8).Radius(7).FillWidth().
				Justify(ui.Start).Label(rowLabel).Selected(on).Transition(kit.HoverMotion)
			// Selected tells assistive technologies; the colors after it
			// replace its accent look: neutral fill, bold, blue check.
			switch {
			case on && row.Hovered():
				row.Background(k.Fill2)
			case on, row.Hovered():
				row.Background(k.Fill)
			default:
				row.Background(k.Fill.Alpha(0))
			}
			row.TextColor(k.Label)
			row.Children(func() {
				mark := kit.Icon(c, kit.SymCheck, 13).TextColor(k.Accent).Shrink(0)
				if !on {
					mark.Invisible()
				}
				name := ui.Text(c, t.Tag()).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				if on {
					name.FontWeight(600)
				}
				ui.Text(c, targetEndpoint(t)).FontSize(kit.FontCaption).TextColor(k.Label3).
					SingleLine().Ellipsis("…").MinWidth(0).Grow(1).Shrink(8)
			})
			if row.Clicked() {
				o.Members = changeMembers(o.Members, []string{id}, !on)
			}
		}).Grow(1).MinHeight(0).Padding(0, 6, 6).Label("候选节点")
	})
}

// pickerHead is a source's heading in the node picker: its fold chevron,
// name and members of its candidates; a click folds it unless a search
// holds every source open (foldable false).
func (e *outEditor) pickerHead(c *ui.Context, ps *pickerSource, open, foldable bool) {
	k := kit.T(c)
	g := &e.group
	label := "展开 " + ps.label
	if open {
		label = "收起 " + ps.label
	}
	head := ui.ButtonBase(c.Key("src-" + ps.id)).Height(28).PaddingX(8).Gap(6).Radius(7).FillWidth().
		Justify(ui.Start).Label(label).Transition(kit.HoverMotion)
	if head.Hovered() {
		head.Background(k.Elevated.Mix(k.Fill2, 0.5))
	} else {
		head.Background(k.Elevated)
	}
	head.Children(func() {
		chev := kit.Icon(c, kit.SymChevronDown, 12).TextColor(k.Label3)
		if !open {
			chev.Rotate(-90)
		}
		ui.Text(c, ps.label).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label2).
			SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		ui.Text(c, fmt.Sprintf("%d/%d", ps.chosen, len(ps.targets))).FontSize(kit.FontCaption).TextColor(k.Label3).
			FontFeatures("tnum")
	})
	if head.Clicked() && foldable {
		g.nodeToggled[ps.id] = !g.nodeToggled[ps.id]
	}
}

// pickerSources are the node picker's sources, derived again only when
// the document or the members changed; a new query only filters them.
func (e *outEditor) pickerSources(doc *model.Doc, o *model.Outbound) *pickerCache {
	g := &e.group
	key := fmt.Sprintf("%d\x01%s\x01%s", e.w.store.Version(), o.ID, strings.Join(o.Members, "\x00"))
	if g.picker.key == key {
		return &g.picker
	}
	selected := map[string]bool{}
	for _, m := range o.Members {
		selected[m] = true
	}
	pc := pickerCache{key: key, selected: selected}
	candidates := memberCandidates(doc, o.ID, selected, g.opening)
	pc.total = len(candidates)
	for _, mg := range memberGroups(doc, candidates) {
		ps := pickerSource{id: mg.id, label: mg.label, targets: mg.candidates, search: make([]string, len(mg.candidates))}
		label := strings.ToLower(mg.label)
		for i, t := range mg.candidates {
			ps.search[i] = strings.ToLower(t.Tag()+" "+targetEndpoint(t)) + " " + label
			if selected[t.ID()] {
				ps.chosen++
			}
		}
		pc.sources = append(pc.sources, ps)
	}
	g.picker = pc
	return &g.picker
}

// finalRow is a row of Final members: a member, or a node the filter takes
// that its exclusions drop (excluded names why).
type finalRow struct {
	t        model.Target
	lower    string // the name in lower case, for the search
	manual   bool
	keyword  string // the keyword matched, "Custom" for the pattern
	excluded string
	order    int // place among the members, 1 first; 0 when excluded
}

// finalGroup is a region's rows of Final members, or Other (other set).
type finalGroup struct {
	id     string
	region model.Region
	other  bool
	rows   []finalRow
	count  int // members, the excluded left out
}

// finalList is Final members grouped, and the members' count.
type finalList struct {
	groups []finalGroup
	total  int
	ids    []string // the members in order, model.GroupMembers that exist
}

// finalCache keeps the last finalList with what it was derived from.
type finalCache struct {
	key  string
	list finalList
}

// finalOther is the group key of Other: nodes of no known region, policy
// groups and endpoints.
const finalOther = "\x00other"

// finalMembers are the group's final members by region, derived again
// only when the document, the members, the filter or the mode changed.
func (e *outEditor) finalMembers(doc *model.Doc, o *model.Outbound) finalList {
	g := &e.group
	version := e.w.store.Version()
	idx := g.index.of(version, doc, o.ID)
	key := fmt.Sprintf("%d\x01%s\x01%s\x01%q", version, o.ID, strings.Join(o.Members, "\x00"), filterKey(o.Filter))
	if g.final.key == key {
		return g.final.list
	}
	var list finalList
	byID := map[string]int{}
	group := func(id string) *finalGroup {
		if i, ok := byID[id]; ok {
			return &list.groups[i]
		}
		fg := finalGroup{id: id, other: id == finalOther}
		if !fg.other {
			fg.region = idx.region(id)
		}
		byID[id] = len(list.groups)
		list.groups = append(list.groups, fg)
		return &list.groups[len(list.groups)-1]
	}
	// Groups in the table's order, then flag countries, Other last.
	for _, r := range idx.order {
		group(r.ID)
	}
	regionKey := func(t model.Target) string {
		if t.Outbound == nil || model.IsGroup(t.Outbound.Type) {
			return finalOther
		}
		if id, ok := idx.regionOf[t.ID()]; ok && id != "" {
			return id
		}
		return finalOther
	}
	manual := map[string]bool{}
	order := 0
	for _, t := range idx.shown(o.Members) {
		if manual[t.ID()] {
			continue
		}
		manual[t.ID()] = true
		order++
		fg := group(regionKey(t))
		fg.rows = append(fg.rows, finalRow{t: t, lower: strings.ToLower(t.Tag()), manual: true, order: order})
		list.ids = append(list.ids, t.ID())
		fg.count++
	}
	var excluded []finalRow
	var excludedKeys []string
	if o.Filter.Error() == "" {
		for _, hit := range model.FilterHits(doc, o.Filter, o.ID) {
			if manual[hit.ID] {
				continue
			}
			t, ok := idx.targets[hit.ID]
			if !ok {
				continue
			}
			id := hit.Region.ID
			if id == "" {
				id = finalOther
			}
			row := finalRow{t: t, lower: strings.ToLower(t.Tag()), keyword: hit.Keyword, excluded: hit.Excluded}
			if hit.Excluded != "" {
				excluded = append(excluded, row)
				excludedKeys = append(excludedKeys, id)
				continue
			}
			order++
			row.order = order
			list.ids = append(list.ids, t.ID())
			fg := group(id)
			fg.rows = append(fg.rows, row)
			fg.count++
		}
	}
	// The excluded end their groups.
	for i, row := range excluded {
		fg := group(excludedKeys[i])
		fg.rows = append(fg.rows, row)
	}
	list.total = order
	// Other last; empty groups (table regions without rows) dropped.
	var out []finalGroup
	var other *finalGroup
	for i := range list.groups {
		fg := list.groups[i]
		switch {
		case len(fg.rows) == 0:
		case fg.other:
			other = &fg
		default:
			out = append(out, fg)
		}
	}
	if other != nil {
		out = append(out, *other)
	}
	list.groups = out
	g.final = finalCache{key: key, list: list}
	return list
}

// filterKey is what of a filter the final members depend on.
func filterKey(f *model.GroupFilter) string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("%q|%q|%q|%q|%q|%v", f.Regions, f.Keywords, f.ExcludeKeywords, f.Pattern, f.Exclude, f.KeepInfo)
}

// finalLine is a line of the Final members list: a group's heading (row < 0)
// or a row of it.
type finalLine struct {
	group, row int
}

// finalColumn is Final members: the count and a search, then the members by
// region, each group folding, in one list building only the lines in
// view, the groups' headings sticking at its top.
func (e *outEditor) finalColumn(c *ui.Context, doc *model.Doc, o *model.Outbound, list finalList) {
	k := kit.T(c)
	g := &e.group
	idx := g.index.of(e.w.store.Version(), doc, o.ID)
	ordered := o.Type == model.ModeFallback
	ui.Row(c.Key("final-head")).Padding(14, 16, 10).Gap(8).Shrink(0).Children(func() {
		ui.Text(c, "最终成员").FontSize(kit.FontSection).FontWeight(600).SingleLine()
		ui.Text(c, fmt.Sprint(list.total)).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").SingleLine()
		ui.Spacer(c)
		compactSearch(c, "final-search", &g.query, 160)
	})
	q := strings.ToLower(strings.TrimSpace(g.query))
	// The lines: a group's heading, then, unless folded, its rows. A
	// search keeps the rows it matches, unfolding every group with one and
	// counting them; cleared, the groups fold as the user left them.
	var lines []finalLine
	hits := map[int]int{}
	for gi, fg := range list.groups {
		head := len(lines)
		lines = append(lines, finalLine{gi, -1})
		open := q != "" || !g.folded[fg.id]
		n := 0
		for ri, row := range fg.rows {
			if q != "" && !strings.Contains(row.lower, q) {
				continue
			}
			if row.excluded == "" {
				n++
			}
			if open {
				lines = append(lines, finalLine{gi, ri})
			}
		}
		hits[gi] = n
		if q != "" && len(lines) == head+1 {
			lines = lines[:head]
		}
		if q == "" && len(fg.rows) == 0 {
			lines = lines[:head]
		}
	}
	area := ui.Column(c.Key("final-area")).Grow(1).MinHeight(0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft)
	area.Children(func() {
		if len(lines) == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "没有成员").FontSize(kit.FontBody).TextColor(k.Label3)
			})
			return
		}
		g.list.Key = func(i int) any {
			l := lines[i]
			if l.row < 0 {
				return "g:" + list.groups[l.group].id
			}
			return "m:" + list.groups[l.group].rows[l.row].t.ID()
		}
		g.list.Header = func(i int) bool { return lines[i].row < 0 }
		ui.List(c.Key("final-list"), &g.list, len(lines), func(i int) {
			l := lines[i]
			fg := list.groups[l.group]
			if l.row < 0 {
				count := fg.count
				if q != "" {
					count = hits[l.group]
				}
				e.finalGroupHead(c, fg, q != "" || !g.folded[fg.id], q == "", count)
				return
			}
			e.finalMemberRow(c, idx, fg.rows[l.row], ordered)
		}).Grow(1).MinHeight(0).Label("最终成员")
	})
}

// finalGroupHead is a group's 34 DIP heading: the region's code and
// name, the members' count and the fold chevron; a click folds it.
func (e *outEditor) finalGroupHead(c *ui.Context, fg finalGroup, open, foldable bool, count int) {
	k := kit.T(c)
	name := "其他"
	if !fg.other {
		name = model.RegionName(fg.region)
	}
	label := "收起 " + name
	if !open {
		label = "展开 " + name
	}
	head := ui.ButtonBase(c.Key("g-"+fg.id)).Height(34).PaddingX(12).Gap(8).FillWidth().Justify(ui.Start).
		BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Label(label).Transition(kit.HoverMotion)
	// The list's own row look gives way to the heading's solid fill.
	if head.Hovered() {
		head.Background(k.Surface2.Mix(k.Fill2, 0.5))
	} else {
		head.Background(k.Surface2)
	}
	head.TextColor(k.Label).Children(func() {
		if !fg.other {
			regionBadge(c, model.RegionCode(fg.region), false)
		}
		ui.Text(c, name).FontSize(kit.FontBody).FontWeight(600).SingleLine().Shrink(0)
		ui.Text(c, fmt.Sprint(count)).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").SingleLine()
		ui.Spacer(c)
		chev := kit.Icon(c, kit.SymChevronDown, 13).TextColor(k.Label3)
		if !open {
			chev.Rotate(-90)
		}
	})
	// While a search unfolds the groups, their folds wait for it to clear.
	if head.Clicked() && foldable {
		e.group.folded[fg.id] = open
	}
}

// finalMemberRow is a 30 DIP member row: its place (fallback), name,
// source tag ("Manual", or the keyword it matched) and latency; an excluded
// node struck through with why instead.
func (e *outEditor) finalMemberRow(c *ui.Context, idx *nodeIndex, row finalRow, ordered bool) {
	k := kit.T(c)
	id := row.t.ID()
	line := ui.Row(c.Key("f-"+id)).Height(30).Padding(0, 12, 0, 40).Gap(8).FillWidth().Shrink(0).
		BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft.Alpha(0.6))
	if row.excluded == "" {
		testMenu(e.w, line, row.t)
	}
	line.Children(func() {
		if ordered && row.excluded == "" {
			memberOrder(c, row.order, k.Label3)
		}
		name := ui.Text(c, row.t.Tag()).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		if row.excluded != "" {
			name.TextColor(k.Label3).Strikethrough()
			sourceTag(c, row.excluded, k.Warning, k.WarningWeak)
			ui.Box(c).Width(52).Shrink(0)
			return
		}
		switch {
		case row.manual:
			sourceTag(c, "手动", k.AccentText, k.AccentWeak)
		case row.keyword != "":
			sourceTag(c, row.keyword, k.Label2, k.Fill)
		}
		ui.Row(c).Width(52).Shrink(0).Justify(ui.End).Children(func() {
			switch r, tested := e.w.store.Latency[id]; {
			case e.w.store.Testing(id):
				nodeview.TestingMark(c)
			case tested:
				nodeview.LatencyLabel(c, r)
			}
		})
	})
}

// sourceTag is a member's small source tag, 10 DIPs on a 5 DIP radius.
func sourceTag(c *ui.Context, text string, fg, bg ui.Color) {
	ui.Row(c).Height(16).PaddingX(5).Radius(5).Background(bg).Shrink(0).MaxWidth(140).Children(func() {
		ui.Text(c, text).FontSize(10).TextColor(fg).SingleLine().Ellipsis("…").MinWidth(0)
	})
}

// compactSearch is a 28 DIP search field of width DIPs editing *query;
// Escape clears it.
func compactSearch(c *ui.Context, key string, query *string, width float32) {
	k := kit.T(c)
	var input ui.Element
	box := ui.Row(c.Key(key)).Height(28).Width(width).PaddingX(9).Gap(6).Radius(kit.RadiusInput).Background(k.Fill).
		TextColor(k.Label3).Cursor(ui.CursorText).Shrink(1).MinWidth(100)
	box.Children(func() {
		kit.Icon(c, kit.SymSearch, 13)
		input = ui.TextInputBase(c, query).Grow(1).MinWidth(0).FontSize(kit.FontTable).SingleLine().
			Placeholder("搜索").TextColor(k.Label).Label("搜索最终成员")
	})
	if input.Focused() {
		kit.FocusRing(box, 1.5, k.Accent.Alpha(0.6))
	}
	if *query != "" && input.Shortcut(0, ui.KeyEscape) {
		*query = ""
	}
}
