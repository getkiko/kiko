// Package subscriptions is the console's Components workspace: a toolbar
// with the page's search, its section jumps and the speed test over one
// virtualized list of four sections — Subscriptions, Inbounds, Outbounds
// and Endpoints — each under a heading
// pinned while its section scrolls, with its add actions on the right,
// its entities as cards in responsive columns; the Selector and URLTest
// groups' cards open their members in a side sheet. What opens over the page
// (editors, sheets, menus and confirmations) is built after the list, so
// a row scrolling out takes none away. The Endpoints section is the
// package endpoints; the DNS servers live in Settings › DNS
// (settings/dnsservers).
package subscriptions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/subscriptions/endpoints"
)

// section is a section of the page built by another package: its
// heading, its content and, after the list, its overlays.
type section interface {
	Heading(c *ui.Context)
	Content(c *ui.Context)
	Overlays(c *ui.Context)
}

// Workspace is the subscriptions page.
type Workspace struct {
	env   *env.Env
	store *store.Store

	subs      subsSection
	inbounds  inboundsSection
	outbounds outboundsSection
	endpoints section
	// tailscale is the Endpoints section's Tailscale panel, which the Overview
	// card shows.
	tailscale *endpoints.TailscalePanel
	// conns are the inbounds' active connections, polled while the page
	// shows.
	conns connCounts

	// query is the search; shownQuery the one the list last showed, to
	// scroll back to its top as the search changes; focusSearch moves the
	// focus to the search field (Cmd+F).
	query, shownQuery string
	focusSearch       bool
	list              ui.ListState
	rows              pageRows
	// jumped is the section a jump chose, lit while the list rests at its
	// end short of it; -1 for none.
	jumped int
	// chainCache is the groups' chains (chains) as last worked out.
	chainCache chainCache
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	w := &Workspace{env: e, store: s, jumped: -1}
	w.conns.env = e
	w.subs.init(w)
	w.inbounds.init(w)
	w.outbounds.init(w)
	ep := endpoints.New(e, s)
	w.endpoints, w.tailscale = ep, ep.Tailscale()
	return w
}

// Tailscale is the Tailscale endpoint's live state and control sheet,
// sharing the Endpoints section's runtime state, for the Overview card.
func (w *Workspace) Tailscale() *endpoints.TailscalePanel { return w.tailscale }

// SetActive starts or stops the live state of the page: the inbounds'
// connection counts and the endpoints' runtime status.
func (w *Workspace) SetActive(active bool) {
	if active {
		w.conns.start()
	} else {
		w.conns.stop()
	}
	for _, s := range []section{w.endpoints} {
		if a, ok := s.(env.Activator); ok {
			a.SetActive(active)
		}
	}
}

// overlayOpen reports whether an editor or the nodes sheet is open over
// the page, which keeps Cmd+F from the page's search.
func (w *Workspace) overlayOpen() bool {
	return w.subs.editor.open || w.inbounds.editor.open || w.outbounds.editor.open || w.subs.nodes.sheet.Shown() ||
		w.outbounds.sheet.state.Shown()
}

// View builds the workspace: the toolbar, the sections' rows in one
// list, or a note that nothing matches the search, then the overlays.
func (w *Workspace) View(c *ui.Context) {
	doc := w.store.Doc()
	query := strings.ToLower(strings.TrimSpace(w.query))
	if query != w.shownQuery {
		w.shownQuery, w.jumped = query, -1
		w.list.ScrollTo(0, ui.Start)
	}
	rows := w.rowsOf(doc, query)
	if c.Shortcut(ui.Cmd, ui.KeyF) && !w.overlayOpen() {
		w.focusSearch = true
	}
	kit.Workspace(c, func() {
		w.toolbar(c, rows)
		if len(rows.rows) == 0 {
			kit.EmptyState(c, kit.SymSearch, "没有匹配的组件", "", nil)
		} else {
			w.list.Key = func(i int) any { return rows.rows[i].key }
			w.list.Header = func(i int) bool { return rows.rows[i].kind == rowSectionHead }
			ui.List(c.Key("subscriptions-content"), &w.list, len(rows.rows), func(i int) { w.row(c, doc, rows, i) }).
				Grow(1).MinHeight(0).Label("组件")
		}
		w.subs.overlays(c, doc)
		w.inbounds.overlays(c, doc)
		w.outbounds.overlays(c, doc)
		w.endpoints.Overlays(c)
	})
}

// toolbar is the page's title and statistics, the search, the section
// jumps and "Test all" with its progress.
func (w *Workspace) toolbar(c *ui.Context, rows *pageRows) {
	k := kit.T(c)
	progress := w.store.LatencyProgress
	kit.Toolbar(c, "组件", rows.subtitle(), func() {
		search := kit.Search(c, "components-search", &w.query, kit.SearchOptions{Placeholder: "搜索组件与节点", Toolbar: true, Width: 240}).
			Label("搜索组件与节点")
		if w.focusSearch {
			w.focusSearch = false
			search.Focus()
		}
		if width, _ := c.Size(); width >= 640 && len(rows.rows) > 0 {
			w.jumps(c, rows)
		}
		if progress.Running {
			ui.Text(c, fmt.Sprintf("%d/%d", progress.Completed, progress.Total)).FontSize(kit.FontTable).
				TextColor(k.Label3).FontFeatures("tnum").SingleLine()
		}
		kit.ToolbarGroup(c, func() {
			kit.IconButton(c, kit.SymGauge, "全部测速", kit.IconButtonOptions{IconSize: 17, Busy: progress.Running, BusyHint: "测速中"}).
				OnClick(w.store.MeasureLatency)
		})
	})
}

// sectionIcons are the symbols of the sections' jumps.
var sectionIcons = [sectionCount]kit.Symbol{kit.SymRss, kit.SymRadio, kit.SymLayers3, kit.SymCable}

// jumps is the segmented control of the sections shown, each its symbol
// and count: the section at the list's top is chosen, and choosing one
// scrolls the list to its heading.
func (w *Workspace) jumps(c *ui.Context, rows *pageRows) {
	current := secSubs
	if first, last := w.list.Visible(); last >= first {
		current = rows.sectionAt(first)
	}
	// A section too short to reach the list's top stays lit after its
	// jump while the list rests at its end.
	if w.jumped >= 0 {
		if j := pageSection(w.jumped); w.list.AtEnd() && current < j && rows.heads[j] >= 0 {
			current = j
		} else {
			w.jumped = -1
		}
	}
	var shown []pageSection
	var segments []kit.Segment
	chosen := 0
	for sec := secSubs; sec < sectionCount; sec++ {
		if rows.heads[sec] < 0 {
			continue
		}
		if sec == current {
			chosen = len(shown)
		}
		shown = append(shown, sec)
		segments = append(segments, kit.Segment{Icon: sectionIcons[sec], Count: fmt.Sprint(rows.counts[sec]),
			Name: "跳到 " + sectionNames[sec]})
	}
	if kit.Segmented(c, "section-jumps", &chosen, segments...).Label("跳到分区").Changed() {
		sec := shown[chosen]
		w.jumped = int(sec)
		w.list.ScrollTo(rows.heads[sec], ui.Start)
	}
}

// row builds row i of the page's list.
func (w *Workspace) row(c *ui.Context, doc *model.Doc, rows *pageRows, i int) {
	k := kit.T(c)
	r := rows.rows[i]
	if r.kind == rowSectionHead {
		ui.Column(c).FillWidth().PaddingX(rowsPadX).Background(k.Surface).BorderWidth(0, 0, 0.5, 0).
			BorderColor(k.HairlineSoft).Children(func() {
			switch r.sec {
			case secSubs:
				w.subs.heading(c, doc, rows.counts[secSubs])
			case secInbounds:
				w.inbounds.heading(c, doc)
			case secOutbounds:
				w.outbounds.heading(c, doc, rows.counts[secOutbounds])
			case secEndpoints:
				w.endpoints.Heading(c)
			}
		})
		return
	}
	ui.Column(c).FillWidth().Padding(r.top, rowsPadX, r.bottom).Children(func() {
		switch r.kind {
		case rowCardGrid:
			switch r.sec {
			case secSubs:
				w.subs.grid(c, doc, rows.subs, rows.subHits)
			case secInbounds:
				w.inbounds.grid(c, doc, rows.inbounds)
			case secOutbounds:
				w.outbounds.grid(c, doc, rows.manual)
			}
		case rowSubHeading:
			if r.key == "sub:groups" {
				subHeading(c, "策略组", fmt.Sprintf("%d 个", len(rows.groups)))
			} else {
				subHeading(c, "手动出站", fmt.Sprintf("%d 个", len(rows.manual)))
			}
		case rowGroupGrid:
			w.outbounds.groupGrid(c, rows.groups, rows.key.query != "")
		case rowEndpoints:
			w.endpoints.Content(c)
		}
	})
}

// update changes the document, showing a refusal as a toast; it reports
// whether the change was saved.
func (w *Workspace) update(fn func(doc *model.Doc)) bool {
	if err := w.store.Update(fn); err != nil {
		w.env.ToastError(err)
		return false
	}
	return true
}

// reorderTopLevel moves the outbounds list's item key to target's place,
// saving only when both are still listed and the order changes.
func (w *Workspace) reorderTopLevel(key, target string) {
	keys := topLevelKeys(w.store.Doc())
	if key == target || !slices.Contains(keys, key) || !slices.Contains(keys, target) {
		return
	}
	w.update(func(d *model.Doc) { reorderTopLevelTo(d, key, target) })
}

// reorderInbound moves inbound id to target's place, saving only when
// both still exist and the order changes.
func (w *Workspace) reorderInbound(id, target string) {
	doc := w.store.Doc()
	has := func(id string) bool {
		return slices.ContainsFunc(doc.Inbounds, func(in *model.Inbound) bool { return in.ID == id })
	}
	if id == target || !has(id) || !has(target) {
		return
	}
	w.update(func(d *model.Doc) { reorderInbound(d, id, target) })
}
