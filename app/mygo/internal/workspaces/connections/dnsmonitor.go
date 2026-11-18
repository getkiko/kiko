package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// dnsPageSize is how many queries a page shows.
const dnsPageSize = 50

// dnsFacet is a filter dimension of the DNS monitor.
type dnsFacet string

var dnsFacets = []struct {
	key   dnsFacet
	label string
}{
	{"source", "源 IP"}, {"status", "查询状态"}, {"resolver", "解析器"}, {"protocol", "传输协议"},
	{"outbound", "出站"}, {"process", "进程"}, {"inbound", "入站"},
}

func dnsFacetValue(q *api.DNSQuery, f dnsFacet) string {
	switch f {
	case "source":
		return dnsSourceIP(q)
	case "status":
		return q.Status
	case "resolver":
		return firstNonEmpty(dnsResolver(q), "本地策略")
	case "protocol":
		return dnsProtocol(q)
	case "outbound":
		return dnsOutbound(q)
	case "process":
		return firstNonEmpty(q.Source.Process, "核心内部")
	}
	return firstNonEmpty(q.Source.Inbound, "核心内部")
}

// DNS record scopes.
const (
	dnsModeAll = iota
	dnsModeErrors
	dnsModeCache
)

// dnsMonitor is the DNS Queries monitor.
type dnsMonitor struct {
	// header draws the page's toolbar row (Workspace.header).
	header func(c *ui.Context, subtitle string, filters, actions func())

	env   *env.Env
	icons *iconCache
	// names name the source addresses, nil naming none.
	names *sourceNamer
	// nodes is the document whose servers "Test Exit" tests, nil without
	// one; exits bring the queries' exits down to them.
	nodes *store.Store
	exits nodeview.ExitsCache

	active  bool
	cancel  context.CancelFunc
	gen     int
	version int

	live      *dnsState
	frozen    *dnsState
	paused    bool
	cap       *api.DNSCapabilities
	err       string
	connected bool

	// cursor is where a (re)started stream resumes, read by the stream.
	cursorMu       sync.Mutex
	cursorInstance string
	cursorSeq      string

	search   string
	filter   kit.FacetChoice[dnsFacet]
	mode     int
	detailed bool
	page     int
	lastPage int
	table    kit.TableState
	sortCol  string
	sidebar  *bool
	// filterWidth is the width the user gave the filter pane.
	filterWidth kit.PaneWidth
	// facets keeps each filter group's unfolding and search.
	facets kit.Facets

	selected    string
	inspector   bool
	detail      *api.DNSQuery
	detailError string
	detailFor   string
	detailToken int
	disclosures map[string]*bool

	// rules is the DNS rule map the inspector names the matched rule by.
	rules dnsRuleMap

	confirm kit.ConfirmState
	copied  bool
	loading bool

	derived *dnsDerived
	// freeze holds the list while a row's context menu shows.
	freeze     kit.MenuFreeze[*dnsDerived]
	derivedKey string
	// groups groups the list by query name.
	groups domainGroups[*api.DNSQuery]
}

func newDNSMonitor(e *env.Env, icons *iconCache) *dnsMonitor {
	m := &dnsMonitor{env: e, icons: icons, live: newDNSState(), filter: kit.FacetChoice[dnsFacet]{}, inspector: true,
		sortCol: "started_at", disclosures: map[string]*bool{}}
	m.table.Sort.Column, m.table.Sort.Descending = "started_at", true
	m.loadPrefs()
	return m
}

func (m *dnsMonitor) displayed() *dnsState {
	if m.frozen != nil {
		return m.frozen
	}
	return m.live
}

func (m *dnsMonitor) setActive(active bool) {
	if active == m.active {
		return
	}
	m.active = active
	m.gen++
	if !active {
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.connected, m.confirm.Open = false, false
		// A pending query's detail stops refreshing; the inspector loads
		// it again when the page shows.
		m.detailToken++
		m.detailFor = ""
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.stream(ctx, m.gen)
}

// post runs fn on the UI thread unless the stream it came from stopped.
func (m *dnsMonitor) post(gen int, fn func()) {
	m.env.Post(func() {
		if m.gen == gen {
			fn()
		}
	})
}

// stream reads the capabilities, then follows the live stream from the
// last cursor, retrying while the core does not monitor DNS.
func (m *dnsMonitor) stream(ctx context.Context, gen int) {
	for ctx.Err() == nil {
		capabilities, err := m.env.API.DNSCapabilities(ctx)
		if err == nil {
			m.post(gen, func() { m.cap = &capabilities })
			if capabilities.Enabled && capabilities.SchemaVersion != 1 {
				err = errors.New("当前 DNS 数据版本需要更新客户端")
			} else if !capabilities.Enabled {
				m.post(gen, func() { m.connected = false })
				if !sleep(ctx, 3*time.Second) {
					return
				}
				continue
			}
		}
		if err == nil {
			m.cursorMu.Lock()
			instance, seq := m.cursorInstance, firstNonEmpty(m.cursorSeq, "0")
			m.cursorMu.Unlock()
			err = m.env.API.DNSStream(ctx, instance, seq, func(view api.DNSView) {
				m.post(gen, func() {
					m.applyLive(view)
					m.connected, m.err = true, ""
				})
			})
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

// applyLive merges a view into the live history. UI thread.
func (m *dnsMonitor) applyLive(view api.DNSView) {
	instance := m.live.instance
	if m.live.apply(view, dnsLimit) {
		m.version++
	}
	m.cursorMu.Lock()
	m.cursorInstance, m.cursorSeq = m.live.instance, m.live.seq.String()
	m.cursorMu.Unlock()
	if instance != m.live.instance {
		m.rules.reset()
	}
	if instance != m.live.instance && m.frozen == nil {
		m.selected, m.detail = "", nil
	}
}

func (m *dnsMonitor) togglePause() {
	m.paused = !m.paused
	if m.paused {
		m.frozen = m.live.clone()
	} else {
		m.frozen = nil
	}
	m.version++
}

func (m *dnsMonitor) clear(done func(error)) {
	env.Go(m.env, m.env.API.ClearDNSHistory, func(view api.DNSView, err error) {
		if err != nil {
			m.err = err.Error()
			done(err)
			return
		}
		m.live = newDNSState()
		m.applyLive(view)
		if m.paused {
			m.frozen = m.live.clone()
		}
		m.version++
		m.selected, m.detail, m.err, m.page = "", nil, "", 0
		m.table.Clear()
		done(nil)
	})
}

func (m *dnsMonitor) loadHistory() {
	m.loading = true
	state := m.live
	before := state.oldestSeq()
	env.Go(m.env, func(ctx context.Context) (api.DNSView, error) { return m.env.API.DNSQueries(ctx, before) }, func(view api.DNSView, err error) {
		m.loading = false
		// The history was cleared meanwhile: the page belongs to what
		// was cleared.
		if m.live != state {
			return
		}
		if err != nil {
			m.err = err.Error()
			return
		}
		m.live.mergeHistory(view)
		if !m.paused {
			m.version++
		}
	})
}

// export saves the listed queries, or one query, as JSON.
func (m *dnsMonitor) export(rows []*api.DNSQuery, query *api.DNSQuery) {
	var value any
	name := fmt.Sprintf("dns-%d.json", time.Now().UnixMilli())
	if query != nil {
		value = query
		name = "dns-" + query.ID + ".json"
	} else {
		state := m.displayed()
		value = map[string]any{
			"instance": state.instance, "seq": state.seq, "exported_at": time.Now().UTC().Format(time.RFC3339Nano),
			"scope": "当前列表摘要，完整 RR 通过查询详情获取", "queries": rows,
		}
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		m.err = err.Error()
		return
	}
	saveFile(m.env, name, raw, env.FileFilter{Name: "JSON", Extensions: []string{"json"}}, func(err error) {
		if err != nil {
			m.err = err.Error()
		}
	})
}

// loadDetail reads the selected query's detail, again each second while
// it is pending.
func (m *dnsMonitor) loadDetail(id, instance string) {
	m.detailToken++
	token := m.detailToken
	m.detail, m.detailError = nil, ""
	var refresh func()
	refresh = func() {
		env.Go(m.env, func(ctx context.Context) (api.DNSQuery, error) { return m.env.API.DNSQuery(ctx, id, instance) }, func(q api.DNSQuery, err error) {
			if token != m.detailToken {
				return
			}
			if err != nil {
				m.detailError = err.Error()
				return
			}
			m.detail, m.detailError = &q, ""
			if q.Status == "pending" {
				time.AfterFunc(time.Second, func() {
					m.env.Post(func() {
						if token == m.detailToken {
							refresh()
						}
					})
				})
			}
		})
	}
	refresh()
}

// dnsDerived is what the view shows, recomputed as its inputs change.
type dnsDerived struct {
	queries, rows []*api.DNSQuery
	// all, errors and cached count the segments over the queries passing
	// every limit but the segment.
	all, errors, cached int
	// facets lists each group's options over the queries passing every
	// limit but its own; available are the groups with options.
	facets    map[dnsFacet][]facetOption
	available []dnsFacet
}

// dnsModeGroup is the group of the list's scope among the conditions.
const dnsModeGroup = "\x00mode"

func (m *dnsMonitor) derive() *dnsDerived {
	key := fmt.Sprint(m.version, m.mode, m.filter, m.search, m.sortCol, m.table.Sort.Descending)
	if m.derived != nil && key == m.derivedKey {
		return m.derived
	}
	d := &dnsDerived{facets: map[dnsFacet][]facetOption{}}
	for _, q := range m.displayed().queries {
		q := q
		d.queries = append(d.queries, &q)
	}
	slices.SortFunc(d.queries, func(a, b *api.DNSQuery) int { return cmpUint(b.Seq, a.Seq) })
	conds := &kit.FacetFilter[*api.DNSQuery]{}
	switch m.mode {
	case dnsModeErrors:
		conds.Add(dnsModeGroup, dnsFailed)
	case dnsModeCache:
		conds.Add(dnsModeGroup, dnsCached)
	}
	for _, f := range dnsFacets {
		if chosen := m.filter.Match(f.key); chosen != nil {
			conds.Add(string(f.key), func(q *api.DNSQuery) bool { return chosen(dnsFacetValue(q, f.key)) })
		}
	}
	if search := lower(m.search); search != "" {
		conds.Add("", func(q *api.DNSQuery) bool {
			fields := append([]string{dnsDomain(q), q.Source.Process, q.Source.ProcessPath, q.Source.Address, q.Resolver, strings.Join(q.Candidates, " "), dnsOutbound(q)}, q.ResolvedAddresses...)
			return slices.ContainsFunc(fields, func(v string) bool { return contains(lower(v), search) })
		})
	}
	options := make(map[dnsFacet][]facetOption, len(dnsFacets))
	index := make(map[dnsFacet]map[string]int, len(dnsFacets))
	for _, f := range dnsFacets {
		index[f.key] = map[string]int{}
	}
	for _, q := range d.queries {
		miss := conds.Miss(q)
		if miss == kit.FacetNone {
			continue
		}
		if miss == kit.FacetAll {
			d.rows = append(d.rows, q)
		}
		if conds.Counts(miss, dnsModeGroup) {
			d.all++
			if dnsFailed(q) {
				d.errors++
			}
			if dnsCached(q) {
				d.cached++
			}
		}
		for _, f := range dnsFacets {
			if !conds.Counts(miss, string(f.key)) {
				continue
			}
			value := dnsFacetValue(q, f.key)
			if i, ok := index[f.key][value]; ok {
				options[f.key][i].count++
			} else {
				index[f.key][value] = len(options[f.key])
				options[f.key] = append(options[f.key], facetOption{value, 1})
			}
		}
	}
	for _, f := range dnsFacets {
		list := options[f.key]
		slices.SortStableFunc(list, func(a, b facetOption) int {
			if a.count != b.count {
				return b.count - a.count
			}
			return collateCompare(a.value, b.value)
		})
		d.facets[f.key] = list
		if len(list) > 0 {
			d.available = append(d.available, f.key)
		}
	}
	sortValue := func(q *api.DNSQuery) string {
		switch m.sortCol {
		case "domain":
			return dnsDomain(q)
		case "outbound":
			return dnsOutbound(q)
		case "status":
			return q.Status
		case "resolver":
			return dnsResolver(q)
		}
		return q.StartedAt
	}
	slices.SortStableFunc(d.rows, func(a, b *api.DNSQuery) int {
		left, right := sortValue(a), sortValue(b)
		var order int
		if m.sortCol == "started_at" {
			order = cmpString(left, right)
		} else {
			order = collateCompare(left, right)
		}
		if m.table.Sort.Descending {
			order = -order
		}
		if order != 0 {
			return order
		}
		return cmpUint(b.Seq, a.Seq)
	})
	m.derived, m.derivedKey = d, key
	return d
}

func cmpUint(a, b api.Uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
