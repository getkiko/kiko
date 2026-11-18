package connections

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// connPageSize is how many top-level rows a page shows.
const connPageSize = 50

// facet is a filter dimension of the connection monitor.
type facet string

const (
	facetSource   facet = "source"
	facetProcess  facet = "process"
	facetInbound  facet = "inbound"
	facetOutbound facet = "outbound"
	facetNetwork  facet = "network"
	facetProtocol facet = "protocol"
	facetRule     facet = "rule"
	facetApp      facet = "app"
	facetPID      facet = "pid"
)

var facets = []facet{facetSource, facetProcess, facetApp, facetPID, facetInbound, facetRule, facetOutbound, facetNetwork, facetProtocol}

var facetLabels = map[facet]string{
	facetSource: "源地址", facetProcess: "进程", facetApp: "App", facetPID: "PID", facetInbound: "入站", facetRule: "规则", facetOutbound: "出站",
	facetNetwork: "网络", facetProtocol: "应用协议",
}

// iconFacets are the facets whose options show their process's icon.
var iconFacets = map[facet]bool{facetProcess: true, facetApp: true, facetPID: true}

// Tabs of the connection monitor.
const (
	tabAll = iota
	tabActive
	tabClosed
	tabRejected
)

var tabNames = []string{"all", "active", "closed", "rejected"}

// connMonitor is the Network Connections monitor. The server
// filters, sorts, groups and pages the history; the monitor polls the
// page it shows.
type connMonitor struct {
	// header draws the page's toolbar row (Workspace.header).
	header func(c *ui.Context, subtitle string, filters, actions func())

	env   *env.Env
	icons *iconCache
	// store is the document "Add to Rule" reads its devices from.
	store *store.Store
	// exits bring the connections' exits down to servers for their
	// speed tests.
	exits nodeview.ExitsCache

	active bool
	// ticker stops the poll; cancel the request in flight.
	ticker, cancel context.CancelFunc
	// requested is the query of the request in flight or of data, "" to
	// ask again.
	requested string
	data      *api.ConnectionPage
	dataKey   string
	loaded    bool
	version   int
	paused    bool

	// fetched is the selected connection as last read by its ID.
	fetched      *Row
	detailID     string
	detailCancel context.CancelFunc

	err, streamErr, exportErr string

	tab    int
	search string
	// connID limits the list to one connection (env.ConnectionsView).
	connID string
	filter kit.FacetChoice[facet]
	// pid keeps one process's connections, as Processes opens the monitor; 0
	// keeps all.
	pid     int
	sidebar *bool
	// filterWidth is the width the user gave the filter pane.
	filterWidth kit.PaneWidth
	// facets keeps each filter group's unfolding and search.
	facets kit.Facets

	columns     []Column
	columnOrder []Column
	settings    bool
	columnList  ui.ListState

	table    kit.TableState
	sortCol  Column
	page     int
	lastPage int

	selectedID    string
	selectedGroup *node
	groupDetails  bool

	mode groupMode
	// expansions are the open groups by tab and mode: true open, false
	// closed, "*" every group not listed.
	expansions map[string]map[string]bool
	limits     map[string]map[string]int

	confirm kit.ConfirmState
	// listConfirm asks to close the connections the filtered list holds.
	listConfirm kit.ConfirmState
	busy        bool
	copied      bool

	// clearConfirm asks which records to clear, clearScope.
	clearConfirm kit.ConfirmState
	clearScope   int

	derived *connDerived
	// freeze holds the list while a row's context menu shows.
	freeze kit.MenuFreeze[*connDerived]
	// held is the last page that arrived while a row's menu showed, the
	// answer of heldKey.
	held       *api.ConnectionPage
	heldKey    string
	derivedKey int

	// names name the source addresses, shared by the three monitors.
	names *sourceNamer
}

// pollEvery is how often the shown page is read again.
const pollEvery = time.Second

func newConnMonitor(e *env.Env, icons *iconCache) *connMonitor {
	m := &connMonitor{
		env: e, icons: icons, filter: kit.FacetChoice[facet]{},
		columns: slices.Clone(defaultColumns), mode: groupApp,
		expansions: map[string]map[string]bool{}, limits: map[string]map[string]int{},
		derivedKey: -1,
	}
	m.columnOrder = append(slices.Clone(defaultColumns), restColumns(defaultColumns)...)
	m.table.Sort.Column, m.table.Sort.Descending = string(colCreated), true
	m.sortCol = colCreated
	m.loadPrefs()
	return m
}

func restColumns(columns []Column) []Column {
	var rest []Column
	for _, col := range allColumns {
		if !slices.Contains(columns, col) {
			rest = append(rest, col)
		}
	}
	return rest
}

// setActive starts or stops polling the page; a hidden monitor drops
// its page, so that it shows again only once the server answers.
func (m *connMonitor) setActive(active bool) {
	if active == m.active {
		return
	}
	m.active = active
	// The last page stays shown until the server's answer replaces it, so
	// coming back shows the page at once rather than loading again.
	m.stopRequests()
	if !active {
		if m.ticker != nil {
			m.ticker()
			m.ticker = nil
		}
		m.settings, m.confirm.Open, m.clearConfirm.Open, m.listConfirm.Open = false, false, false, false
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.ticker = cancel
	go func() {
		t := time.NewTicker(pollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.env.Post(func() {
					if m.active && !m.paused {
						m.refresh()
					}
				})
			}
		}
	}()
}

func (m *connMonitor) stopRequests() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.detailCancel != nil {
		m.detailCancel()
		m.detailCancel = nil
	}
	m.requested, m.detailID = "", ""
}

// refresh asks for the page and the selected connection again.
func (m *connMonitor) refresh() { m.requested, m.detailID = "", "" }

// query is the page's query from the current state.
func (m *connMonitor) query() api.ConnectionQuery {
	q := api.ConnectionQuery{State: tabNames[m.tab], Search: trim(m.search), Filter: map[string][]string{},
		Sort: string(m.sortCol), Order: "asc", Group: string(m.mode), Page: m.page, Limit: connPageSize}
	if m.table.Sort.Descending {
		q.Order = "desc"
	}
	for f, values := range m.filter {
		q.Filter[string(f)] = slices.Clone(values)
	}
	if m.connID != "" {
		q.Filter["id"] = []string{m.connID}
	}
	if m.mode != groupNone {
		expanded := m.expanded()
		for key, open := range expanded {
			switch {
			case key == "*" && open:
				q.Expand = append(q.Expand, "*")
			case key == "*":
			case open:
				q.Expand = append(q.Expand, key)
			case expanded["*"]:
				q.Collapse = append(q.Collapse, key)
			}
		}
		for key, n := range m.childLimits() {
			if q.More == nil {
				q.More = map[string]int{}
			}
			q.More[key] = n
		}
	}
	return q
}

// sync starts the requests the state calls for. UI thread.
func (m *connMonitor) sync() {
	if !m.active {
		return
	}
	q := m.query()
	if key := q.Params(); key != m.requested {
		m.requested = key
		m.fetch(q, key)
	}
	if m.selectedID != "" && m.selectedID != m.detailID {
		m.detailID = m.selectedID
		m.fetchDetail(m.selectedID)
	}
}

// fetch reads the page of q, replacing the request in flight; answers of
// an older query are dropped.
func (m *connMonitor) fetch(q api.ConnectionQuery, key string) {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	if key == m.dataKey && m.data != nil {
		q.ETag = m.data.ETag
	}
	env.Go(m.env, func(context.Context) (api.ConnectionPage, error) {
		// A short pause lets typing settle before the query runs.
		select {
		case <-ctx.Done():
			return api.ConnectionPage{}, ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
		return m.env.API.QueryConnections(ctx, q)
	}, func(page api.ConnectionPage, err error) {
		if ctx.Err() != nil || m.requested != key {
			return
		}
		if err != nil {
			m.confirm.Open, m.clearConfirm.Open, m.listConfirm.Open = false, false, false
			m.streamErr = err.Error()
			return
		}
		m.streamErr = ""
		if page.NotModified {
			return
		}
		// While a row's menu shows, the page waits for it to close.
		if m.freeze.Frozen() {
			m.held, m.heldKey = &page, key
			return
		}
		m.applyPage(&page, key)
	})
}

// applyHeld shows the page held while a row's menu showed, once it no
// longer shows and is still the page asked for.
func (m *connMonitor) applyHeld() {
	page, key := m.held, m.heldKey
	if page == nil || m.freeze.Frozen() {
		return
	}
	m.held, m.heldKey = nil, ""
	if key == m.requested {
		m.applyPage(page, key)
	}
}

// applyPage shows page, the answer of the query key.
func (m *connMonitor) applyPage(page *api.ConnectionPage, key string) {
	m.data, m.dataKey, m.loaded = page, key, true
	m.version++
	if page.Page != m.page {
		m.page = page.Page
	}
	// A selection that left the page closes its inspector.
	if m.selectedID != "" {
		if _, ok := m.derive().index["connection:"+m.selectedID]; !ok {
			m.selectedID, m.fetched = "", nil
			if m.selectedGroup == nil {
				m.table.Clear()
			}
		}
	}
}

// fetchDetail reads the selected connection by its ID.
func (m *connMonitor) fetchDetail(id string) {
	if m.detailCancel != nil {
		m.detailCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.detailCancel = cancel
	env.Go(m.env, func(context.Context) (api.ConnectionRow, error) {
		return m.env.API.Connection(ctx, id)
	}, func(row api.ConnectionRow, err error) {
		if ctx.Err() != nil || err != nil || m.selectedID != id {
			return
		}
		m.fetched = rowOf(row)
	})
}

// facetOption is a filter choice with its count.
type facetOption struct {
	value string
	count int
}

// withSelected keeps a group's selected values among its options: one the
// page does not list, as a navigation from another workspace may choose,
// shows first with no matches.
func withSelected(options []facetOption, selected []string) []facetOption {
	var missing []facetOption
	for _, value := range selected {
		if value != "" && !slices.ContainsFunc(options, func(o facetOption) bool { return o.value == value }) {
			missing = append(missing, facetOption{value, 0})
		}
	}
	if len(missing) == 0 {
		return options
	}
	return append(missing, options...)
}

// connDerived is what the view shows of the page.
type connDerived struct {
	// counts are the connections of each tab passing the filters and
	// search; totals those of the history.
	counts, totals api.ConnectionCounts
	// pool is the connections of the tab, total the rows or groups of all
	// pages, matched the connections passing the filters.
	pool, total, matched int
	// matchedActive are the matched connections still open: what "Close
	// listed connections" closes.
	matchedActive int
	facets        map[facet][]facetOption
	// facetIcons are the processes of the options with icons, by
	// facetKey.
	facetIcons    map[string]*api.Connection
	iconProcesses map[int]Process
	tree          []*node
	index         map[string]*node
}

func (m *connMonitor) derive() *connDerived {
	if m.derived != nil && m.version == m.derivedKey {
		return m.derived
	}
	d := &connDerived{facets: map[facet][]facetOption{}, facetIcons: map[string]*api.Connection{}, iconProcesses: map[int]Process{}}
	if page := m.data; page != nil {
		d.counts, d.totals, d.total, d.matched, d.matchedActive = page.Counts, page.Totals, page.Total, page.Matched, page.MatchedActive
		// A server without totals counts the tabs over the history alone.
		if d.totals.All == 0 && d.counts.All > 0 {
			d.totals = d.counts
		}
		d.pool = [...]int{tabAll: d.totals.All, tabActive: d.totals.Active, tabClosed: d.totals.Closed, tabRejected: d.totals.Rejected}[m.tab]
		for _, f := range facets {
			for _, option := range page.Facets[string(f)] {
				d.facets[f] = append(d.facets[f], facetOption{option.Value, option.Count})
				if iconFacets[f] {
					d.facetIcons[facetKey(f, option.Value)] = &api.Connection{ProcessInfo: option.ProcessInfo}
				}
			}
		}
		for _, p := range page.Processes {
			d.iconProcesses[p.PID] = p
		}
		d.tree = pageNodes(page)
	}
	d.index = indexNodes(d.tree)
	m.derived, m.derivedKey = d, m.version
	return d
}

func (m *connMonitor) scope() string { return fmt.Sprint(m.tab, ":", m.mode) }

// isExpanded reports whether the group key is open.
func (m *connMonitor) isExpanded(key string) bool {
	expanded := m.expanded()
	if open, ok := expanded[key]; ok {
		return open
	}
	return expanded["*"]
}

func (m *connMonitor) expanded() map[string]bool {
	if m.expansions[m.scope()] == nil {
		m.expansions[m.scope()] = map[string]bool{}
	}
	return m.expansions[m.scope()]
}

func (m *connMonitor) childLimits() map[string]int {
	if m.limits[m.scope()] == nil {
		m.limits[m.scope()] = map[string]int{}
	}
	return m.limits[m.scope()]
}

func (m *connMonitor) setTab(tab int) {
	m.tab, m.page = tab, 0
	m.selectedID, m.selectedGroup, m.groupDetails = "", nil, false
	m.table.Clear()
}

// facetKey keys an option of facet f.
func facetKey(f facet, value string) string { return string(f) + "\x00" + value }

// toggleFilter chooses value in f's group, alongside the group's other
// choices, or unchooses it.
func (m *connMonitor) toggleFilter(f facet, value string) {
	m.filter.Toggle(f, value)
	m.page = 0
}

// onlyFilter keeps value alone chosen in f's group, as "Only this…" does, or
// unchooses the group when it already is.
func (m *connMonitor) onlyFilter(f facet, value string) {
	m.filter.Only(f, value)
	m.page = 0
}

// open shows v's tab (All when it names none) with only v's filters,
// search and process, from the first page (env.ConnectionsView).
func (m *connMonitor) open(v env.ConnectionsView) {
	tab := tabAll
	if i := slices.Index(tabNames, v.State); i >= 0 {
		tab = i
	}
	m.setTab(tab)
	m.filter = kit.FacetChoice[facet]{}
	m.connID = v.ConnectionID
	if m.connID != "" {
		m.search = ""
		return
	}
	for key, values := range v.Filter {
		f := facet(key)
		if !slices.Contains(facets, f) {
			continue
		}
		for _, value := range values {
			if value != "" {
				m.filter.Add(f, value)
			}
		}
	}
	m.search = v.Search
	if m.sidebar != nil && len(m.filter) > 0 {
		*m.sidebar = true
	}
}

// togglePause stops or resumes the poll; a paused monitor keeps its page.
func (m *connMonitor) togglePause() {
	m.paused = !m.paused
	if !m.paused {
		m.refresh()
	}
}

func (m *connMonitor) setMode(mode groupMode) {
	m.mode, m.page = mode, 0
	m.env.Prefs.Save(prefGroupMode, mode)
	m.selectedGroup, m.groupDetails = nil, false
	if mode != groupNone && !slices.Contains(m.columns, colHost) {
		var next []Column
		for _, col := range m.columnOrder {
			if col == colHost || slices.Contains(m.columns, col) {
				next = append(next, col)
			}
		}
		m.columns = next
		m.saveColumns()
	}
}

// setColumns shows next, keeping the sort on a shown column.
func (m *connMonitor) setColumns(next []Column) {
	if len(next) == 0 || (m.mode != groupNone && !slices.Contains(next, colHost)) {
		return
	}
	if !slices.Contains(next, m.sortCol) {
		replacement := next[0]
		if slices.Contains(next, colCreated) {
			replacement = colCreated
		}
		m.sortCol = replacement
		m.table.Sort.Column, m.table.Sort.Descending = string(replacement), replacement.descendingByDefault()
	}
	m.columns = next
	m.saveColumns()
}

func (m *connMonitor) terminate(id string) {
	m.busy = true
	m.env.Run(func(ctx context.Context) error { return m.env.API.CloseConnection(ctx, id) }, func(err error) {
		m.busy = false
		m.err = ""
		if err != nil {
			m.err = err.Error()
		}
		m.refresh()
	})
}

// clearScopes are the server's scopes of the "Clear records" choices, by tab.
var clearScopes = [...]string{tabAll: api.ClearAllConnections, tabActive: api.ClearActiveConnections, tabClosed: api.ClearClosedConnections}

// clearHistory removes the records of the chosen scope from the server's
// history, then reads the page again, also while paused.
func (m *connMonitor) clearHistory(done func(error)) {
	scope := m.clearScope
	m.busy = true
	m.env.Run(func(ctx context.Context) error { return m.env.API.ClearConnectionHistory(ctx, clearScopes[scope]) }, func(err error) {
		m.busy = false
		if err == nil {
			m.refresh()
		}
		done(err)
	})
}

func (m *connMonitor) closeAll(done func(error)) {
	m.busy = true
	m.env.Run(func(ctx context.Context) error { return m.env.API.CloseAllConnections(ctx) }, func(err error) {
		m.busy = false
		done(err)
	})
}

// closeList closes the active connections of the filtered list, the ones
// its search and filters keep on every page, then reads the page again.
func (m *connMonitor) closeList(done func(error)) {
	q := m.query()
	m.busy = true
	m.env.Run(func(ctx context.Context) error {
		_, err := m.env.API.CloseConnections(ctx, q)
		return err
	}, func(err error) {
		m.busy = false
		if err == nil {
			m.refresh()
		}
		done(err)
	})
}
