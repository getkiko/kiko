package subscriptions

import (
	"fmt"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// rowKind is what a row of the page's list shows.
type rowKind uint8

const (
	// rowSectionHead is a section's heading, pinned at the list's top
	// while its section scrolls under it.
	rowSectionHead rowKind = iota
	// rowCardGrid is a grid of cards: the subscriptions, the inbounds or
	// the manual outbounds, reordered by dragging within it and ending in
	// an add card, which an empty section shows alone.
	rowCardGrid
	// rowSubHeading heads the strategy groups (its key "sub:groups") or
	// the manual outbounds.
	rowSubHeading
	// rowGroupGrid is the grid of the Selector and URLTest groups' cards,
	// reordered by dragging within it and ending in "+ Add policy group".
	rowGroupGrid
	// rowEndpoints is the Endpoints section's lists.
	rowEndpoints
)

// pageSection is one of the page's four sections, in their order.
type pageSection uint8

const (
	secSubs pageSection = iota
	secInbounds
	secOutbounds
	secEndpoints
	sectionCount
)

// sectionNames are the sections' titles.
var sectionNames = [sectionCount]string{"订阅", "入站", "出站", "端点"}

// pageRow is a row of the page's list: its kind, its section and its
// key in the list.
type pageRow struct {
	kind rowKind
	sec  pageSection
	key  string
	// top and bottom space it from the rows around it.
	top, bottom float32
}

// pageRows are the rows of the page, kept between frames until the
// document or the search changes, with the items they show.
type pageRows struct {
	built bool
	key   pageRowsKey
	rows  []pageRow
	// heads are the rows of the sections' headings, -1 for a section
	// left out while searching.
	heads [sectionCount]int
	// counts are the sections' items, all of them; nodes the
	// subscriptions' nodes.
	counts [sectionCount]int
	nodes  int
	// subs, inbounds, groups and manual (the outbounds outside the
	// groups) are the items matching the search; subHits counts a
	// subscription's nodes matching it, while searching.
	subs     []model.TopLevelItem
	inbounds []*model.Inbound
	groups   []groupView
	manual   []model.TopLevelItem
	subHits  map[string]int
	// info is what the page works out of the document's version.
	info *docInfo
}

// docInfo is what the page works out of a version of the document, once:
// the tags the configuration gives its entities and the region table.
type docInfo struct {
	version int
	// tags are the emitted tags by entity ID, which the running core and
	// its connections name the inbounds and outbounds by.
	tags    emit.Tags
	regions *model.RegionSettings
	matcher *model.RegionMatcher
}

// infoOf is the docInfo of the store's document version, kept with the
// rows between frames.
func (w *Workspace) infoOf(doc *model.Doc) *docInfo {
	version := w.store.Version()
	if w.rows.info == nil || w.rows.info.version != version {
		regions := model.Regions(doc)
		w.rows.info = &docInfo{version: version, tags: emit.EmittedTags(doc), regions: regions, matcher: model.NewRegionMatcher(regions)}
	}
	return w.rows.info
}

// outboundTag is the emitted tag of outbound or endpoint id, its document
// tag for one not emitted.
func (i *docInfo) outboundTag(id, tag string) string {
	if t, ok := i.tags.Outbounds[id]; ok {
		return t
	}
	return tag
}

// inboundTag is the emitted tag of inbound id, its document tag for one
// not emitted.
func (i *docInfo) inboundTag(id, tag string) string {
	if t, ok := i.tags.Inbounds[id]; ok {
		return t
	}
	return tag
}

// pageRowsKey is what pageRows are built from.
type pageRowsKey struct {
	version int
	query   string
}

// Spacing of the list's rows.
const (
	rowsPadX       = 20
	bodyTop        = 12
	sectionBottom  = 22
	groupBottom    = 16
	subHeadBottom  = 8
	lastRowsBottom = 24
)

// contains reports whether one of parts holds query, in lower case;
// every part does an empty query.
func contains(query string, parts ...string) bool {
	if query == "" {
		return true
	}
	for _, p := range parts {
		if strings.Contains(strings.ToLower(p), query) {
			return true
		}
	}
	return false
}

// rowsOf are the page's rows for query (lower case), built again only as
// the document or the query changes.
func (w *Workspace) rowsOf(doc *model.Doc, query string) *pageRows {
	key := pageRowsKey{version: w.store.Version(), query: query}
	if w.rows.built && w.rows.key == key {
		return &w.rows
	}
	r := pageRows{built: true, key: key, info: w.infoOf(doc)}
	for i := range r.heads {
		r.heads[i] = -1
	}

	subs := w.subs.items(doc)
	r.counts[secSubs] = len(subs)
	if query != "" {
		r.subHits = map[string]int{}
	}
	for _, item := range subs {
		r.nodes += len(item.Nodes)
		if hits, ok := subscriptionMatches(item, query); ok {
			r.subs = append(r.subs, item)
			if query != "" {
				r.subHits[item.Subscription.ID] = hits
			}
		}
	}

	r.counts[secInbounds] = len(doc.Inbounds)
	for _, in := range doc.Inbounds {
		if contains(query, in.Tag, in.Type, inboundSummary(in)) {
			r.inbounds = append(r.inbounds, in)
		}
	}

	all, groups, manual := w.outbounds.items(doc)
	r.counts[secOutbounds] = len(all)
	r.groups = w.outbounds.groupViews(doc, r.info, groups, query)
	for _, item := range manual {
		if contains(query, item.Outbound.Tag, item.Outbound.Type, outboundEndpoint(item.Outbound)) {
			r.manual = append(r.manual, item)
		}
	}

	r.counts[secEndpoints] = len(doc.Endpoints) + len(doc.Services)
	endpoints := query == ""
	for _, e := range doc.Endpoints {
		endpoints = endpoints || contains(query, e.Tag, e.Type)
	}
	for _, s := range doc.Services {
		endpoints = endpoints || contains(query, s.Tag, s.Type)
	}

	// While searching, a section without matches is left out.
	head := func(sec pageSection) {
		r.heads[sec] = len(r.rows)
		r.rows = append(r.rows, pageRow{kind: rowSectionHead, sec: sec, key: "head:" + sectionNames[sec]})
	}
	body := func(row pageRow) {
		if last := r.rows[len(r.rows)-1]; last.kind == rowSectionHead {
			row.top = bodyTop
		}
		r.rows = append(r.rows, row)
	}
	end := func() { r.rows[len(r.rows)-1].bottom = sectionBottom }
	if query == "" || len(r.subs) > 0 {
		head(secSubs)
		body(pageRow{kind: rowCardGrid, sec: secSubs, key: "grid:subs"})
		end()
	}
	if query == "" || len(r.inbounds) > 0 {
		head(secInbounds)
		body(pageRow{kind: rowCardGrid, sec: secInbounds, key: "grid:inbounds"})
		end()
	}
	if query == "" || len(r.groups) > 0 || len(r.manual) > 0 {
		head(secOutbounds)
		// The groups' grid, ending in "+ Add policy group", and the manual
		// outbounds' grid, ending in "+ Add outbound", each under its heading,
		// show without cards too, unless searching.
		groups := len(r.groups) > 0 || query == ""
		if groups {
			body(pageRow{kind: rowSubHeading, sec: secOutbounds, key: "sub:groups", bottom: subHeadBottom})
			body(pageRow{kind: rowGroupGrid, sec: secOutbounds, key: "grid:groups"})
		}
		if len(r.manual) > 0 || query == "" {
			top := float32(0)
			if groups {
				top = groupBottom
			}
			body(pageRow{kind: rowSubHeading, sec: secOutbounds, key: "sub:manual", top: top, bottom: subHeadBottom})
			body(pageRow{kind: rowCardGrid, sec: secOutbounds, key: "grid:manual"})
		}
		end()
	}
	if endpoints {
		head(secEndpoints)
		body(pageRow{kind: rowEndpoints, sec: secEndpoints, key: "endpoints"})
	}
	if n := len(r.rows); n > 0 {
		r.rows[n-1].bottom = lastRowsBottom
	}
	w.rows = r
	return &w.rows
}

// subscriptionMatches reports whether a subscription's name or one of its
// nodes holds query, with the count of the nodes that do.
func subscriptionMatches(item model.TopLevelItem, query string) (hits int, ok bool) {
	if query == "" {
		return 0, true
	}
	for _, o := range item.Nodes {
		if contains(query, o.Tag) {
			hits++
		}
	}
	return hits, hits > 0 || contains(query, item.Subscription.Tag)
}

// subtitle is the toolbar's statistics line.
func (r *pageRows) subtitle() string {
	return fmt.Sprintf("%d 个订阅 · %d 个节点", r.counts[secSubs], r.nodes)
}

// sectionAt is the section of row i, the first for none.
func (r *pageRows) sectionAt(i int) pageSection {
	if i < 0 || i >= len(r.rows) {
		return secSubs
	}
	return r.rows[i].sec
}
