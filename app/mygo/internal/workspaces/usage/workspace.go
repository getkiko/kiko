// Package usage is the console's usage workspace:
// recorded traffic grouped by one or two dimensions over a time range,
// with the connection monitor's filter pane, a compact striped list, trend
// dot charts, a cross-detail inspector, CSV export and history clearing.
package usage

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
	"github.com/getkiko/kiko/app/mygo/internal/srcname"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// pageSize is how many groups a page shows.
const pageSize = 50

// refreshEvery is how often the range's end follows the clock.
const refreshEvery = 5 * time.Second

// Workspace is the usage page. Its fields are UI-thread state.
type Workspace struct {
	env   *env.Env
	store *store.Store
	// icons is the console's process icon store.
	icons *procicon.Store
	// exits bring the groups' exits down to servers for their speed
	// tests.
	exits nodeview.ExitsCache
	// now is the clock, replaced by tests.
	now func() time.Time

	// names name the source filter's addresses.
	names        *srcname.Names
	namesVersion int
	namesLocal   []string

	active bool
	ticker context.CancelFunc

	group, secondary string
	rng, start, end  string
	// startMS and endMS are the custom range (Unix ms, invalidTime while
	// the field does not parse); start and end are their text in
	// rangeZone, written again as the display time zone changes.
	startMS, endMS int64
	rangeZone      string
	search         string
	filters        kit.FacetChoice[string]
	sortKey        string
	ascending      bool
	page           int
	clock          time.Time

	data *api.UsageResponse
	// freeze holds the list while a row's context menu shows; held is the
	// last answer that arrived meanwhile.
	freeze        kit.MenuFreeze[struct{}]
	held          *heldAnswer
	dataGroup     string
	dataSecondary string
	err           string
	loading       bool

	sidebar     bool
	sidebarInit bool
	// filterWidth is the width the user gave the filter pane.
	filterWidth kit.PaneWidth
	// facets keeps each filter group's unfolding and search.
	facets      kit.Facets
	selected    *api.UsageGroup
	detailGroup string
	detail      *api.UsageResponse
	detailErr   string

	confirm      kit.ConfirmState
	working      bool
	exportErr    string
	exportCancel context.CancelFunc

	table     kit.TableState
	scrollTop bool

	// requested is the query of the request in flight or answered;
	// cancel aborts it.
	requested       string
	cancel          context.CancelFunc
	detailRequested string
	detailCancel    context.CancelFunc
}

// prefFilterWidth keeps the width of the filter pane.
const prefFilterWidth = "kiko-usage-filter-width"

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	now := time.Now()
	w := &Workspace{
		env: e, store: s, icons: procicon.For(e), now: time.Now,
		group: "source", rng: "today",
		startMS: now.Add(-24 * time.Hour).UnixMilli(), endMS: now.UnixMilli(), rangeZone: "\x00",
		filters: kit.FacetChoice[string]{}, sortKey: "total", clock: now,
		detailGroup: "rootDomain", loading: true,
	}
	w.table.Sort = ui.SortOrder{Column: "total", Descending: true}
	e.Prefs.Load(prefFilterWidth, &w.filterWidth.Width)
	return w
}

// SetActive starts following the clock while the page shows, and stops
// requests when it hides.
func (w *Workspace) SetActive(active bool) {
	if active == w.active {
		return
	}
	w.active = active
	if !active {
		if w.ticker != nil {
			w.ticker()
			w.ticker = nil
		}
		w.stopRequests()
		return
	}
	w.refresh()
	ctx, cancel := context.WithCancel(context.Background())
	w.ticker = cancel
	go func() {
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.env.Post(func() {
					if w.active {
						w.refresh()
					}
				})
			}
		}
	}()
}

func (w *Workspace) stopRequests() {
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	if w.detailCancel != nil {
		w.detailCancel()
		w.detailCancel = nil
	}
	w.requested, w.detailRequested = "", ""
}

// refresh moves the range's end to now and queries again.
func (w *Workspace) refresh() {
	w.clock = w.now()
	w.requested, w.detailRequested = "", ""
}

// query is the list's query from the current state.
func (w *Workspace) query() Query {
	from, to := TimeRange(w.rng, "", "", w.clock)
	if w.rng == "custom" {
		from, to = w.startMS, w.endMS
	}
	order := "desc"
	if w.ascending {
		order = "asc"
	}
	return Query{From: from, To: to, Group: w.group, Secondary: w.secondary, Filters: w.filters,
		Search: w.search, Sort: w.sortKey, Order: order, Page: w.page, Limit: pageSize, HasPage: true}
}

// selectedFilters are the filters narrowed to the selected group's values.
func (w *Workspace) selectedFilters() kit.FacetChoice[string] {
	next := w.filters.Clone()
	if w.selected != nil {
		next[w.group] = []string{w.selected.Label(0)}
		if w.secondary != "" {
			next[w.secondary] = []string{w.selected.Label(1)}
		}
	}
	return next
}

// sync starts the requests the state calls for. UI thread.
func (w *Workspace) sync() {
	if !w.active {
		return
	}
	q := w.query()
	if !ValidRange(q.From, q.To) {
		w.loading = false
		if w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
		w.requested = ""
		return
	}
	if key := q.Params(); key != w.requested {
		w.requested = key
		w.fetch(q, key)
	}
	if w.selected == nil {
		return
	}
	dq := q
	dq.Filters, dq.Group, dq.Secondary = w.selectedFilters(), w.detailGroup, ""
	dq.Page, dq.Sort, dq.Order = 0, "total", "desc"
	if key := dq.Params(); key != w.detailRequested {
		w.detailRequested = key
		w.fetchDetail(dq, key)
	}
}

func (w *Workspace) fetch(q Query, key string) {
	if w.cancel != nil {
		w.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.loading = true
	env.Go(w.env, func(context.Context) (api.UsageResponse, error) {
		// A short pause lets typing settle before the query runs.
		select {
		case <-ctx.Done():
			return api.UsageResponse{}, ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
		return w.env.API.Usage(ctx, key)
	}, func(result api.UsageResponse, err error) {
		if ctx.Err() != nil || w.requested != key {
			return
		}
		w.loading = false
		if err != nil {
			w.err = err.Error()
			return
		}
		// While a row's menu shows, the answer waits for it to close.
		if w.freeze.Frozen() {
			w.held = &heldAnswer{result, q, key}
			return
		}
		w.apply(result, q)
	})
}

// heldAnswer is an answer that arrived while a row's menu showed.
type heldAnswer struct {
	result api.UsageResponse
	q      Query
	key    string
}

// applyHeld shows the answer held while a row's menu showed, once it no
// longer shows.
func (w *Workspace) applyHeld() {
	if h := w.held; h != nil && !w.freeze.Frozen() {
		w.held = nil
		// An answer to a query since replaced is dropped.
		if h.key == w.requested {
			w.apply(h.result, h.q)
		}
	}
}

// apply shows result, the answer of q.
func (w *Workspace) apply(result api.UsageResponse, q Query) {
	w.data, w.dataGroup, w.dataSecondary = &result, q.Group, q.Secondary
	if w.selected != nil {
		var found *api.UsageGroup
		for i := range result.Rows {
			if result.Rows[i].Key == w.selected.Key {
				found = &result.Rows[i]
			}
		}
		w.selected = found
	}
	w.err = ""
	if q.Page > 0 && q.Page*pageSize >= result.Groups {
		w.page = 0
	}
}

func (w *Workspace) fetchDetail(q Query, key string) {
	if w.detailCancel != nil {
		w.detailCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.detailCancel = cancel
	w.detailErr = ""
	env.Go(w.env, func(context.Context) (api.UsageResponse, error) {
		return w.env.API.Usage(ctx, key)
	}, func(result api.UsageResponse, err error) {
		if ctx.Err() != nil || w.detailRequested != key {
			return
		}
		if err != nil {
			w.detailErr = err.Error()
			return
		}
		w.detail = &result
	})
}

// ---- state changes, as UsageMonitor's handlers ----

// Open shows usage as another page asks (env.UsageView): only its
// filters, grouped by its dimension, from the first page.
func (w *Workspace) Open(arg any) {
	v, ok := arg.(env.UsageView)
	if !ok {
		return
	}
	known := func(value string) bool {
		for _, d := range Dimensions {
			if d.Value == value {
				return true
			}
		}
		return false
	}
	filters := kit.FacetChoice[string]{}
	for dimension, value := range v.Filters {
		if known(dimension) {
			filters.Add(dimension, value)
		}
	}
	w.filters, w.search = filters, ""
	if known(v.Group) {
		w.changeGroup(v.Group)
	} else {
		w.resetPage()
	}
	if len(filters) > 0 {
		w.sidebar, w.sidebarInit = true, true
	}
}

func (w *Workspace) resetPage() {
	w.page = 0
	w.scrollTop = true
	w.selected = nil
}

func (w *Workspace) changeGroup(value string) {
	w.group = value
	if w.secondary == value {
		w.secondary = ""
	}
	w.resetPage()
}

func (w *Workspace) clearFilters() {
	w.filters = kit.FacetChoice[string]{}
	w.search = ""
	w.resetPage()
}

// toggleFilter chooses value among dimension's chosen values, or
// unchooses it.
func (w *Workspace) toggleFilter(dimension, value string) {
	next := w.filters.Clone()
	next.Toggle(dimension, value)
	if next.Has(dimension, value) {
		// The filter pane shows the filters set.
		w.sidebar = true
	}
	w.filters = next
	w.resetPage()
}

// onlyFilter keeps value alone chosen in dimension, as "Filter by this" does, or
// unchooses the dimension when it already is.
func (w *Workspace) onlyFilter(dimension, value string) {
	next := w.filters.Clone()
	next.Only(dimension, value)
	if next.Has(dimension, value) {
		// The filter pane shows the filters set.
		w.sidebar = true
	}
	w.filters = next
	w.resetPage()
}

func (w *Workspace) changeSort(key string) {
	if w.sortKey == key {
		w.ascending = !w.ascending
	} else {
		w.ascending = key == "label"
	}
	w.sortKey = key
	w.resetPage()
}

func (w *Workspace) drill(row api.UsageGroup) {
	filters := w.selectedFilters()
	w.group, w.secondary, w.selected, w.page = w.detailGroup, "", nil, 0
	if label := row.Label(0); label != "" {
		filters[w.detailGroup] = []string{label}
	}
	w.filters, w.sidebar = filters, true
}

func (w *Workspace) changingGroup() bool {
	if w.data == nil {
		return false
	}
	return w.group != w.dataGroup || w.secondary != w.dataSecondary
}

func (w *Workspace) openRow(row api.UsageGroup) {
	if w.changingGroup() {
		return
	}
	w.selected = &row
	w.detail = nil
	w.detailGroup = PreferredDetail(w.group, w.secondary)
	w.detailRequested = ""
}

func (w *Workspace) exportCSV(all bool) {
	if w.working {
		return
	}
	w.working, w.exportErr = true, ""
	ctx, cancel := context.WithCancel(context.Background())
	w.exportCancel = cancel
	params := ExportParams(w.query(), all)
	scope := "summary"
	if all {
		scope = "details"
	}
	name := "usage-" + scope + "-" + timefmt.Format(time.Now(), "2006-01-02") + ".csv"
	w.env.Run(func(context.Context) error {
		data, err := w.env.API.UsageExport(ctx, params)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err = w.env.Shell.SaveFile(name, data, env.FileFilter{Name: "CSV", Extensions: []string{"csv"}})
		return err
	}, func(err error) {
		if err != nil && ctx.Err() == nil {
			w.exportErr = "导出失败：" + err.Error()
		}
		cancel()
		w.exportCancel = nil
		w.working = false
	})
}

func (w *Workspace) clearHistory(done func(error)) {
	w.working = true
	w.env.Run(func(ctx context.Context) error { return w.env.API.ClearUsage(ctx) }, func(err error) {
		w.working = false
		if err == nil {
			w.selected = nil
			w.err = ""
			w.refresh()
		}
		done(err)
	})
}

// availableFilters are the dimensions with options or a filter set.
func (w *Workspace) availableFilters() []Dimension {
	var out []Dimension
	for _, d := range Dimensions {
		if (w.data != nil && len(w.data.Facets[d.Value]) > 0) || len(w.filters[d.Value]) > 0 {
			out = append(out, d)
		}
	}
	return out
}

func (w *Workspace) filtered() bool { return w.search != "" || len(w.filters) > 0 }
