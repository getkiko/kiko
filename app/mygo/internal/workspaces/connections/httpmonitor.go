package connections

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/rewrites"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/scripts"
)

// Preference keys of the HTTP Requests monitor.
const (
	prefHTTPDetailed     = "kiko-http-detailed"
	prefHTTPSidebar      = "kiko-http-sidebar"
	prefHTTPFilterWidth  = "kiko-http-filter-width"
	prefHTTPColumnWidths = "kiko-http-column-widths"
)

// deriveEvery is the shortest time between two rebuilds of the list while
// the stream changes it.
const deriveEvery = 120 * time.Millisecond

// httpMonitor is the HTTP Requests monitor. The stream feeds a store of the
// newest httpLimit records; the list is the store filtered and sorted,
// rebuilt at most once per deriveEvery however many events arrive.
//
// Pausing freezes the list on the rows it shows; the store keeps taking
// events, and resuming shows them all.
type httpMonitor struct {
	// header draws the page's toolbar row (Workspace.header).
	header func(c *ui.Context, subtitle string, filters, actions func())

	env   *env.Env
	icons *iconCache
	// doc is the document "Skip decryption for this host" edits, nil without one.
	doc docEditor
	// names name the source addresses, nil naming none.
	names *sourceNamer
	// rw is the rewrite rule editor dialog, made on first use.
	rw *rewrites.Editors
	// sc is the script editor dialog, made on first use.
	sc *scripts.Editors
	// nodes is the document whose servers "Test Exit" tests, nil without
	// one; exits bring the exchanges' exits down to them.
	nodes *store.Store
	exits nodeview.ExitsCache

	active  bool
	cancel  context.CancelFunc
	gen     int
	version int

	store     *httpStore
	cap       *api.HTTPCapabilities
	err       string
	connected bool

	// mu guards the frames the stream goroutine queues for the UI
	// thread, the generation it may queue for and the cursor a restarted
	// stream resumes from.
	mu             sync.Mutex
	streamGen      int
	pending        []api.HTTPFrame
	scheduled      bool
	cursorInstance string
	cursorSeq      api.Uint64

	// paused freezes the list on snap, the rows at that moment;
	// pausedAt is the store's count of added records then.
	paused   bool
	snap     []*httpRow
	pausedAt int

	search   string
	filter   kit.FacetChoice[httpFacet]
	mode     int
	conn     string
	detailed bool
	table    kit.TableState
	sortCol  string
	sidebar  *bool
	// filterWidth is the width the user gave the filter pane.
	filterWidth kit.PaneWidth
	facets      kit.Facets

	selected  string
	inspector bool
	// fetched is the selected record read from the core, fetching the ID
	// being read, for a record the store dropped.
	fetched  *httpRow
	fetching string
	// refocus gives the list the keyboard focus again in the next frame,
	// after the inspector closed.
	refocus bool
	insp    httpInspectorState

	confirm kit.ConfirmState
	// removeRow confirms the deletion of the selected record.
	removeRow kit.ConfirmState
	// rp is the replay dialog, har the export popover.
	rp      replayState
	har     harState
	loading bool
	// notice is a short result for the user, such as a failed page.
	notice string

	derived *httpDerived
	// freeze holds the list while a row's context menu shows.
	freeze     kit.MenuFreeze[*httpDerived]
	derivedKey string
	derivedAt  time.Time
	// groups groups the list by domain.
	groups domainGroups[*httpRow]
}

func newHTTPMonitor(e *env.Env, icons *iconCache) *httpMonitor {
	m := &httpMonitor{env: e, icons: icons, store: newHTTPStore(), filter: kit.FacetChoice[httpFacet]{}, inspector: true, sortCol: "time"}
	m.table.Sort.Column, m.table.Sort.Descending = "time", true
	e.Prefs.Load(prefHTTPDetailed, &m.detailed)
	e.Prefs.Load(prefHTTPFilterWidth, &m.filterWidth.Width)
	e.Prefs.Load(prefHTTPColumnWidths, &m.table.Widths)
	var sidebar bool
	if e.Prefs.Load(prefHTTPSidebar, &sidebar) {
		m.sidebar = &sidebar
	}
	m.loadGrouping()
	m.insp.init()
	m.har.opts = defaultHARoptions()
	return m
}

// setActive follows the stream while the page shows and stops it while
// it does not; the records stay, and the stream resumes from its cursor.
func (m *httpMonitor) setActive(active bool) {
	if active == m.active {
		return
	}
	m.active = active
	m.gen++
	m.mu.Lock()
	m.streamGen, m.pending, m.scheduled = m.gen, nil, false
	m.mu.Unlock()
	if !active {
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.connected, m.confirm.Open = false, false
		m.rp.open, m.har.open = false, false
		if m.rw != nil {
			m.rw.Close()
		}
		if m.sc != nil {
			m.sc.Close()
		}
		m.cancelExport()
		m.insp.stop()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.stream(ctx, m.gen)
}

// post runs fn on the UI thread unless the stream it came from stopped.
func (m *httpMonitor) post(gen int, fn func()) {
	m.env.Post(func() {
		if m.gen == gen {
			fn()
		}
	})
}

// stream reads the capabilities, then follows the live stream from the
// last cursor, retrying while the core does not capture.
func (m *httpMonitor) stream(ctx context.Context, gen int) {
	for ctx.Err() == nil {
		capabilities, err := m.env.API.HTTPCapabilities(ctx)
		if err == nil {
			m.post(gen, func() { m.cap = &capabilities })
			if !capabilities.Supported || !capabilities.Enabled {
				m.post(gen, func() { m.connected = false })
				if !sleep(ctx, 3*time.Second) {
					return
				}
				continue
			}
			m.mu.Lock()
			instance, seq := m.cursorInstance, m.cursorSeq
			m.mu.Unlock()
			err = m.env.API.StreamHTTP(ctx, seq, instance, func(f api.HTTPFrame) { m.enqueue(gen, f) })
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			message := err.Error()
			m.post(gen, func() { m.err = message })
		}
		m.post(gen, func() { m.connected = false })
		if !sleep(ctx, 1500*time.Millisecond) {
			return
		}
	}
}

// enqueue queues a frame for the UI thread, which takes all that are
// waiting in one go. Stream goroutine.
func (m *httpMonitor) enqueue(gen int, f api.HTTPFrame) {
	m.mu.Lock()
	if gen != m.streamGen {
		m.mu.Unlock()
		return
	}
	m.pending = append(m.pending, f)
	schedule := !m.scheduled
	m.scheduled = true
	m.mu.Unlock()
	if schedule {
		m.post(gen, m.drain)
	}
}

// drain applies the queued frames, then drops the records beyond the
// limit. UI thread.
func (m *httpMonitor) drain() {
	m.mu.Lock()
	frames := m.pending
	m.pending, m.scheduled = nil, false
	// The cursor moves on with the frames applied, so that a restarted
	// stream resumes after what the list holds. A reset's middle frames
	// carry no cursor (Seq 0).
	for _, f := range frames {
		if f.Seq != 0 {
			m.cursorInstance, m.cursorSeq = f.Instance, f.Seq
		}
	}
	m.mu.Unlock()
	m.applyFrames(frames)
}

func (m *httpMonitor) applyFrames(frames []api.HTTPFrame) {
	if len(frames) == 0 {
		return
	}
	instance, epoch := m.store.instance, m.store.epoch
	for _, f := range frames {
		m.store.apply(f)
	}
	// A reset drops the records read for the connection: read them again.
	if m.conn != "" && epoch != m.store.epoch {
		m.loadConnection(m.conn)
	}
	m.store.trim(httpLimit, m.keepRow)
	if instance != m.store.instance && instance != "" {
		m.selected = ""
		m.insp.stop()
	}
	m.connected, m.err = true, ""
	m.version++
}

// keepRow names the rows eviction spares: the selected one and those of
// the connection the list is limited to.
func (m *httpMonitor) keepRow(r *httpRow) bool {
	return r.ID == m.selected || m.conn != "" && r.ConnectionID == m.conn
}

// httpDerived is what the list shows, rebuilt as its inputs change.
type httpDerived struct {
	version int
	// total is the rows before filters. all, errors and live count the
	// segments over the rows passing every limit but the segment.
	total, all, errors, live int
	rows                     []*httpRow
	// facets lists each group's options over the rows passing every limit
	// but its own; available are the groups with options.
	facets    map[httpFacet][]facetOption
	available []httpFacet
}

func (m *httpMonitor) source() []*httpRow {
	if m.paused {
		return m.snap
	}
	return m.store.rows
}

func (m *httpMonitor) filterState() httpFilter {
	return httpFilter{facets: m.filter, search: lower(trim(m.search)), conn: m.conn, mode: m.mode}
}

func (m *httpMonitor) filtered() bool {
	return m.search != "" || m.mode != httpModeAll || len(m.filter) > 0 || m.conn != ""
}

// derive filters and sorts the rows. While only the records change it
// rebuilds at most each deriveEvery, so a busy stream costs one pass per
// interval.
func (m *httpMonitor) derive() *httpDerived {
	key := fmt.Sprint(m.mode, m.filter, m.search, m.sortCol, m.table.Sort.Descending, m.conn, m.paused, m.store.epoch, len(m.snap))
	// A paused list shows its snapshot: the stream does not rebuild it.
	if d := m.derived; d != nil && key == m.derivedKey && (m.paused || d.version == m.version || time.Since(m.derivedAt) < deriveEvery) {
		return d
	}
	d := &httpDerived{version: m.version, facets: map[httpFacet][]facetOption{}}
	rows := m.source()
	d.total = len(rows)
	filter := m.filterState()
	conds := filter.conditions()
	counts := map[httpFacet]map[string]int{}
	d.rows = make([]*httpRow, 0, len(rows))
	// Newest first is the store's order reversed.
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		miss := conds.Miss(r)
		if miss == kit.FacetNone {
			continue
		}
		if miss == kit.FacetAll {
			d.rows = append(d.rows, r)
		}
		if conds.Counts(miss, httpModeGroup) {
			d.all++
			if r.problem() {
				d.errors++
			}
			if r.live() {
				d.live++
			}
		}
		for _, f := range httpFacets {
			if !conds.Counts(miss, string(f.key)) {
				continue
			}
			if v := httpFacetValue(r, f.key); v != "" {
				if counts[f.key] == nil {
					counts[f.key] = map[string]int{}
				}
				counts[f.key][v]++
			}
		}
	}
	for _, f := range httpFacets {
		if options := httpOptions(f.key, counts[f.key]); len(options) > 0 {
			d.facets[f.key] = options
			d.available = append(d.available, f.key)
		}
	}
	if m.sortCol != "time" || !m.table.Sort.Descending {
		m.sortRows(d.rows)
	}
	m.derived, m.derivedKey, m.derivedAt = d, key, time.Now()
	return d
}

// stale reports whether the records changed since the list was built.
func (m *httpMonitor) stale() bool {
	return !m.paused && m.derived != nil && m.derived.version != m.version
}

// sortRows orders newest-first rows by the sort column; ties keep the
// newest first.
func (m *httpMonitor) sortRows(rows []*httpRow) {
	value := func(r *httpRow) string {
		switch m.sortCol {
		case "method":
			return r.Method
		case "host":
			return r.Host
		case "path":
			return r.Path
		case "type":
			return r.ctype
		case "process":
			return r.process
		}
		return ""
	}
	number := func(r *httpRow) int64 {
		switch m.sortCol {
		case "status":
			return int64(r.Status)
		case "size":
			return r.ResponseBytes
		case "duration":
			return int64(r.total)
		}
		return r.start
	}
	text := slices.Contains([]string{"method", "host", "path", "type", "process"}, m.sortCol)
	slices.SortStableFunc(rows, func(a, b *httpRow) int {
		var order int
		if text {
			order = collateCompare(value(a), value(b))
		} else {
			order = int(sign(number(a) - number(b)))
		}
		if m.table.Sort.Descending {
			order = -order
		}
		return order
	})
}

func sign(n int64) int64 {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func (m *httpMonitor) togglePause() {
	m.paused = !m.paused
	if m.paused {
		m.snap, m.pausedAt = slices.Clone(m.store.rows), m.store.added
	} else {
		m.snap = nil
	}
	m.version++
}

// sincePause is how many records arrived while the list was paused.
func (m *httpMonitor) sincePause() int { return m.store.added - m.pausedAt }

// clear drops the finished records of the core and of the console.
func (m *httpMonitor) clear(done func(error)) {
	env.Go(m.env, func(ctx context.Context) (api.HTTPStatus, error) { return m.env.API.ClearHTTP(ctx) }, func(_ api.HTTPStatus, err error) {
		if err != nil {
			done(err)
			return
		}
		m.store.dropFinished()
		if m.paused {
			m.snap = slices.Clone(m.store.rows)
		}
		if _, ok := m.store.byID[m.selected]; !ok {
			m.selected = ""
			m.insp.stop()
			m.table.Clear()
		}
		m.err, m.notice = "", ""
		m.version++
		done(nil)
	})
}

// deleteRecord removes one finished record from the core, from disk and
// from the list.
func (m *httpMonitor) deleteRecord(id string, done func(error)) {
	instance := m.store.instance
	env.Go(m.env, func(ctx context.Context) (api.HTTPStatus, error) {
		return m.env.API.DeleteHTTPRecord(ctx, id, instance)
	}, func(_ api.HTTPStatus, err error) {
		if err != nil {
			if !api.IsStatus(err, 404) {
				done(err)
				return
			}
		}
		m.store.forget(id)
		if m.paused {
			m.snap = slices.Clone(m.store.rows)
		}
		if m.selected == id {
			m.selected, m.fetched = "", nil
			m.insp.stop()
			m.table.Clear()
		}
		m.err, m.notice = "", ""
		m.version++
		done(nil)
	})
}

// canLoadEarlier reports whether older records exist than the list holds,
// with room for them.
func (m *httpMonitor) canLoadEarlier() bool {
	s := m.store
	return !s.exhausted && s.instance != "" && s.retained > len(s.rows) && len(s.rows) < httpLimit
}

// loadEarlier reads the page of records older than the oldest kept. The
// client keeps at most httpLimit records: the page fills what room is
// left.
func (m *httpMonitor) loadEarlier() {
	if m.loading || m.store.oldest() == nil {
		return
	}
	m.loading = true
	epoch, before := m.store.epoch, m.store.oldest().ID
	limit := min(httpPageLimit, httpLimit-len(m.store.rows))
	if limit <= 0 {
		m.loading = false
		return
	}
	env.Go(m.env, func(ctx context.Context) (api.HTTPList, error) {
		return m.env.API.ListHTTPRecords(ctx, api.HTTPQuery{Limit: limit, Before: before})
	}, func(list api.HTTPList, err error) {
		m.loading = false
		if err != nil {
			m.err = err.Error()
			return
		}
		m.store.mergeList(list, epoch, limit, httpLimit-len(m.store.rows))
		m.version++
	})
}

// loadConnection reads the records of one connection from the core, so
// that a list limited to it holds records older than those streamed.
func (m *httpMonitor) loadConnection(id string) {
	epoch := m.store.epoch
	env.Go(m.env, func(ctx context.Context) (api.HTTPList, error) {
		return m.env.API.ListHTTPRecords(ctx, api.HTTPQuery{ConnectionID: id, Limit: httpPageLimit})
	}, func(list api.HTTPList, err error) {
		if err != nil || m.conn != id {
			return
		}
		// A connection's records may sit anywhere in the history: the
		// store keeps them beyond its limit while the list is limited
		// to them (keepRow).
		m.store.mergeConnection(list, epoch)
		m.version++
	})
}

// The methods below are what the inspector builds on.

// record is the record with id as the monitor holds it, nil when evicted
// or unknown.
func (m *httpMonitor) record(id string) *api.HTTPRecord {
	if r := m.store.byID[id]; r != nil {
		return &r.HTTPRecord
	}
	return nil
}

// selectedRow is the selected row, nil without a selection or after
// the record left the store.
func (m *httpMonitor) selectedRow() *httpRow {
	if m.selected == "" {
		return nil
	}
	return m.store.byID[m.selected]
}

// fetchRecord reads a record from the core by ID, such as one the store
// evicted; done runs on the UI thread.
func (m *httpMonitor) fetchRecord(id string, done func(api.HTTPRecord, error)) {
	instance := m.store.instance
	env.Go(m.env, func(ctx context.Context) (api.HTTPRecord, error) {
		return m.env.API.GetHTTPRecord(ctx, id, instance)
	}, done)
}

// fetchBody reads a range of a record's body from the core (length 0 for
// the rest); done runs on the UI thread.
func (m *httpMonitor) fetchBody(id, side string, offset, length int64, decoded bool, done func(api.HTTPBody, error)) {
	env.Go(m.env, func(ctx context.Context) (api.HTTPBody, error) {
		return m.env.API.HTTPBody(ctx, id, side, offset, length, decoded)
	}, done)
}

// showConnection limits the list to one connection's records ("" lifts
// the limit).
func (m *httpMonitor) showConnection(id string) {
	m.conn = id
	m.mode = httpModeAll
	m.version++
	if id != "" {
		// The list holds the connection's records only: no search or
		// filter hides them.
		m.search, m.filter = "", kit.FacetChoice[httpFacet]{}
		m.loadConnection(id)
	}
}

// inspectedRow is the record the inspector shows: the selected row, or
// the record read from the core when the store no longer holds it (after
// a reset or a clear). Nil while that read is under way.
func (m *httpMonitor) inspectedRow() *httpRow {
	if r := m.selectedRow(); r != nil {
		return r
	}
	if m.selected == "" {
		return nil
	}
	if m.fetched != nil && m.fetched.ID == m.selected {
		return m.fetched
	}
	if id := m.selected; m.fetching != id {
		m.fetching = id
		m.fetchRecord(id, func(rec api.HTTPRecord, err error) {
			if m.fetching == id {
				m.fetching = ""
			}
			if m.selected != id {
				return
			}
			if err != nil {
				m.err, m.selected = "读取记录失败："+err.Error(), ""
				return
			}
			m.fetched = newHTTPRow(rec)
			m.version++
		})
	}
	return nil
}

// selectRow selects a record and shows its inspector.
func (m *httpMonitor) selectRow(id string) {
	if id != m.selected {
		m.insp.reset()
	}
	m.selected, m.inspector = id, true
}

func (m *httpMonitor) closeInspector() { m.inspector, m.refocus = false, true }

func (m *httpMonitor) resetFilters() {
	m.filter, m.search, m.mode, m.conn = kit.FacetChoice[httpFacet]{}, "", httpModeAll, ""
}
