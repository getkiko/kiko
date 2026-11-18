package subscriptions

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// nodeIndex is what the group editor knows of the document's nodes,
// derived once per document version: each outbound's region, the regions
// found in table order, and the node counts by region of the nodes a
// filter chooses from (info nodes and the group itself left out).
type nodeIndex struct {
	version int
	self    string
	built   bool

	regions  map[string]model.Region // by ID
	order    []model.Region          // table regions, then flag countries found
	regionOf map[string]string       // outbound ID to region ID, "" unrecognized
	counts   map[string]int          // region ID to filter nodes, info nodes left out
	withInfo map[string]int          // region ID to filter nodes, info nodes counted
	targets  map[string]model.Target // outbounds and endpoints by ID
	codes    map[string]string       // node ID to its region's badge code

	// refs are the document's reference options (forms.DocRefOptions),
	// derived on first use.
	refs     []forms.RefOption
	refsMade bool
}

// of is the index of doc at version, for group self, building it anew
// when either changed.
func (x *nodeIndex) of(version int, doc *model.Doc, self string) *nodeIndex {
	if x.built && x.version == version && x.self == self {
		return x
	}
	*x = nodeIndex{version: version, self: self, built: true,
		regions: map[string]model.Region{}, regionOf: map[string]string{}, counts: map[string]int{},
		withInfo: map[string]int{}, targets: map[string]model.Target{}, codes: map[string]string{}}
	m := model.NewRegionMatcher(model.Regions(doc))
	// The first of an ID wins, as model.FindOutboundTarget finds it.
	for _, e := range slices.Backward(doc.Endpoints) {
		x.targets[e.ID] = model.Target{Endpoint: e}
	}
	for _, o := range slices.Backward(doc.Outbounds) {
		x.targets[o.ID] = model.Target{Outbound: o}
	}
	var tags []string
	for _, o := range doc.Outbounds {
		if model.IsGroup(o.Type) {
			continue
		}
		tags = append(tags, o.Tag)
		if r, ok := m.RegionOf(o.Tag); ok {
			x.regionOf[o.ID] = r.ID
		}
	}
	for _, o := range model.FilterNodes(doc) {
		id, ok := x.regionOf[o.ID]
		if o.ID == self || !ok {
			continue
		}
		x.withInfo[id]++
		if !m.IsInfo(o.Tag) {
			x.counts[id]++
		}
	}
	x.order = m.RegionsFound(tags)
	for _, r := range x.order {
		x.regions[r.ID] = r
	}
	for id, region := range x.regionOf {
		x.codes[id] = model.RegionCode(x.region(region))
	}
	return x
}

// shown are the targets of ids that exist, in order, by
// the index's table.
func (x *nodeIndex) shown(ids []string) []model.Target {
	out := make([]model.Target, 0, len(ids))
	for _, id := range ids {
		if t, ok := x.targets[id]; ok && id != "" {
			out = append(out, t)
		}
	}
	return out
}

// regionCounts are the node counts by region a filter keeping or skipping
// the info nodes shows.
func (x *nodeIndex) regionCounts(keepInfo bool) map[string]int {
	if keepInfo {
		return x.withInfo
	}
	return x.counts
}

// refOptions are the document's reference options, derived once.
func (x *nodeIndex) refOptions(doc *model.Doc) []forms.RefOption {
	if !x.refsMade {
		x.refs, x.refsMade = forms.DocRefOptions(doc), true
	}
	return x.refs
}

// region is the region of id: the table's or a flag's, else one named by
// its ID alone, as a filter may keep.
func (x *nodeIndex) region(id string) model.Region {
	if r, ok := x.regions[id]; ok {
		return r
	}
	return model.Region{ID: id}
}

// regionPicker is the popover of "+ Choose regions…" at anchor: a search
// over the regions that have nodes and those chosen, the most nodes first, then the rest by
// name (matches by name while searching), each row toggling the region
// in f; its footer counts the choice and clears it.
func (e *outEditor) regionPicker(c *ui.Context, anchor ui.Element, idx *nodeIndex, f *model.GroupFilter) {
	g := &e.group
	k := kit.T(c)
	ui.PopoverBase(c, anchor, &g.regionMenu, func(panel ui.Element) {
		kit.Popup(c, panel).Margin(6, 0, 0, 0).Width(340).MaxHeight(min(500, kit.PopoverRoom(c, anchor, 6))).
			Radius(16).Clip().Label("选择地区")
		panel.AttachTo(anchor, ui.AnchorBottomStart, ui.AnchorTopStart)
		counts := idx.regionCounts(f.KeepInfo)
		chosen := map[string]bool{}
		for _, id := range f.Regions {
			chosen[id] = true
		}
		var candidates []model.Region
		for _, r := range idx.order {
			if counts[r.ID] > 0 || chosen[r.ID] {
				candidates = append(candidates, r)
			}
		}
		for _, id := range f.Regions {
			if _, ok := idx.regions[id]; !ok {
				candidates = append(candidates, idx.region(id))
			}
		}
		q := strings.ToLower(strings.TrimSpace(g.regionQuery))
		type section struct {
			title   string
			regions []model.Region
		}
		var sections []section
		if q != "" {
			var matched []model.Region
			for _, r := range candidates {
				if strings.Contains(strings.ToLower(model.RegionName(r)+"\x00"+model.RegionEnglish(r)+"\x00"+model.RegionCode(r)), q) {
					matched = append(matched, r)
				}
			}
			model.SortRegionsByName(matched)
			sections = append(sections, section{"", matched})
		} else {
			top := slices.Clone(candidates)
			model.SortRegionsByName(top)
			slices.SortStableFunc(top, func(a, b model.Region) int { return cmp.Compare(counts[b.ID], counts[a.ID]) })
			n := min(5, len(top))
			for n > 0 && counts[top[n-1].ID] == 0 {
				n--
			}
			rest := slices.Clone(top[n:])
			model.SortRegionsByName(rest)
			sections = append(sections, section{"节点最多", top[:n]}, section{"其他 · 按名称", rest})
		}
		ui.Column(c).Padding(10, 10, 6).Shrink(0).Children(func() {
			kit.Input(c, "region-search", &g.regionQuery, "搜索地区：中文、英文或代码").AutoFocus()
		})
		ui.Scroll(c.Key("region-list")).Grow(1).Shrink(1).MinHeight(0).Padding(0, 6, 6).Children(func() {
			any := false
			for _, s := range sections {
				if len(s.regions) == 0 {
					continue
				}
				any = true
				if s.title != "" {
					ui.Text(c.Key("cap-"+s.title), s.title).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label2).
						Padding(8, 10, 4)
				}
				for _, r := range s.regions {
					if regionRow(c, r, counts[r.ID], chosen[r.ID]) {
						id := r.ID
						if chosen[id] {
							f.Regions = slices.DeleteFunc(slices.Clone(f.Regions), func(x string) bool { return x == id })
						} else {
							f.Regions = append(slices.Clone(f.Regions), id)
						}
					}
				}
			}
			if !any {
				ui.Text(c, "没有匹配的地区").FontSize(kit.FontBody).TextColor(k.Label3).Padding(18, 12).
					TextAlign(ui.Center).FillWidth()
			}
		})
		nodes := 0
		for _, id := range f.Regions {
			nodes += counts[id]
		}
		ui.Row(c).Padding(8, 14).Gap(8).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
			ui.Text(c, fmt.Sprintf("已选 %d 个地区 · %d 个节点", len(f.Regions), nodes)).FontSize(kit.FontTable).
				TextColor(k.Label2).FontFeatures("tnum").SingleLine().Grow(1)
			if len(f.Regions) > 0 && linkButton(c, "clear-regions", "清除").Clicked() {
				f.Regions = nil
			}
		})
	})
}

// regionRow is a region of the picker: its check box, code, Chinese and
// English names and node count. It reports a click.
func regionRow(c *ui.Context, r model.Region, count int, on bool) bool {
	k := kit.T(c)
	name := model.RegionName(r)
	row := ui.ButtonBase(c.Key("r-" + r.ID)).Height(30).PaddingX(10).Gap(8).Radius(7).FillWidth().Justify(ui.Start).
		Label(name).Selected(on).Transition(kit.HoverMotion)
	// Selected tells assistive technologies; the colors after it replace
	// its accent look.
	if row.Hovered() {
		row.Background(k.Fill)
	} else {
		row.Background(k.Fill.Alpha(0))
	}
	row.TextColor(k.Label).Children(func() {
		box := ui.Box(c).Size(15, 15).Radius(4).Center().Shrink(0)
		if on {
			box.Background(k.Accent).TextColor(k.OnAccent)
		} else {
			box.Border(1.5, k.Hairline)
		}
		box.Children(func() {
			if on {
				kit.Icon(c, kit.SymCheck, 11)
			}
		})
		regionBadge(c, model.RegionCode(r), true)
		ui.Row(c).Gap(6).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, name).FontSize(kit.FontBody).SingleLine().Shrink(0)
			if en := model.RegionEnglish(r); en != "" {
				ui.Text(c, en).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…").MinWidth(0)
			}
		})
		ui.Text(c, fmt.Sprint(count)).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").SingleLine().Shrink(0)
	})
	return row.Clicked()
}
