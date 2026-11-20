package connections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// fakeConnections is the server's connection query over a fixed history:
// tabs, search, facets, App groups and pages, enough for the view.
type fakeConnections struct {
	mu      sync.Mutex
	rows    []api.ConnectionRow
	queries []string
	// closes are the query strings of POST /connections/close; failClose
	// refuses that many of them with an error first.
	closes    []string
	failClose int
}

func newFakeConnections(rows []api.ConnectionRow) *fakeConnections {
	return &fakeConnections{rows: rows}
}

// fixtureRows are three active connections and a closed one.
func fixtureRows(now time.Time) []api.ConnectionRow {
	type spec struct{ id, host, process, source, outbound, rule string }
	specs := []spec{
		{"c1", "mtalk.google.com", "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Helper", "172.18.0.1:61261", "美国板瓦", "rule_set=[Gfw Telegram openai]"},
		{"c2", "api.github.com", "/usr/local/bin/codex", "172.18.0.1:61763", "美国板瓦", "rule_set=[Gfw Telegram openai]"},
		{"c3", "ime.doubao.com", "/Applications/Doubao.app/Contents/MacOS/Doubaoime", "172.18.0.2:61804", "直连", "rule_set=[geosite-cn geoip-cn]"},
	}
	var rows []api.ConnectionRow
	for i, s := range specs {
		rows = append(rows, api.ConnectionRow{Connection: api.Connection{
			ID: s.id, Inbound: "tun-in", InboundType: "tun", IPVersion: 4, Network: "tcp", Source: s.source,
			Destination: fmt.Sprintf("142.250.141.%d:443", 180+i), Domain: s.host, Protocol: "tls",
			CreatedAt:   api.Int64(now.Add(-time.Duration(i) * time.Minute).UnixMilli()),
			UplinkTotal: api.Int64(2300 * (i + 1)), DownlinkTotal: api.Int64(5400 * (i + 1)),
			Rule: s.rule, Outbound: s.outbound, OutboundType: "selector", ChainList: []string{s.outbound, "自选"},
			ProcessInfo: &api.ProcessInfo{ProcessID: 100 + i, UserID: 501, UserName: "me", ProcessPath: s.process},
		}})
	}
	rows = append(rows, api.ConnectionRow{Connection: api.Connection{
		ID: "c4", Inbound: "tun-in", Network: "udp", Source: "172.18.0.1:5353", Destination: "8.8.8.8:53",
		Protocol: "dns", CreatedAt: api.Int64(now.Add(-time.Hour).UnixMilli()), ClosedAt: api.Int64(now.Add(-50 * time.Minute).UnixMilli()),
		Outbound: "直连", ChainList: []string{"直连"},
	}})
	return rows
}

// clear drops the records of scope, as DELETE /connections/history.
func (f *fakeConnections) clear(scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = slices.DeleteFunc(f.rows, func(r api.ConnectionRow) bool {
		closed := r.ClosedAt != 0
		return scope == "all" || scope == "closed" && closed || scope == "active" && !closed
	})
}

// queried lists the query strings of GET /connections so far.
func (f *fakeConnections) queried() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

// keeps reports whether row passes the search and filter.* of values.
func (f *fakeConnections) keeps(row api.ConnectionRow, values url.Values) bool {
	search := strings.ToLower(values.Get("search"))
	keep := search == "" || strings.Contains(strings.ToLower(row.Domain+" "+row.Destination+" "+row.Source), search)
	for key, v := range values {
		if name, ok := strings.CutPrefix(key, "filter."); ok && !slices.Contains(v, fakeFacet(row, name)) {
			keep = false
		}
	}
	return keep
}

// closeMatching closes the active rows the query keeps, as POST
// /connections/close.
func (f *fakeConnections) closeMatching(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes = append(f.closes, r.URL.RawQuery)
	w.Header().Set("Content-Type", "application/json")
	if f.failClose > 0 {
		f.failClose--
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"核心未响应"}`))
		return
	}
	var result api.CloseResult
	for i, row := range f.rows {
		if row.ClosedAt == 0 && f.keeps(row, r.URL.Query()) {
			f.rows[i].ClosedAt = api.Int64(time.Now().UnixMilli())
			result.Matched++
			result.Closed++
		}
	}
	_ = json.NewEncoder(w).Encode(result)
}

// closeRequests lists the query strings of the close requests so far.
func (f *fakeConnections) closeRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.closes)
}

func fakeApp(r api.ConnectionRow) string {
	path := ""
	if r.ProcessInfo != nil {
		path = r.ProcessInfo.ProcessPath
	}
	if i := strings.Index(path, ".app"); i >= 0 {
		return path[strings.LastIndex(path[:i], "/")+1 : i]
	}
	return firstNonEmpty(baseName(path), "未知进程")
}

func fakeFacet(r api.ConnectionRow, f string) string {
	switch f {
	case "source":
		return firstNonEmpty(sourceHost(r.Source), "—")
	case "process":
		return firstNonEmpty(processName(&r.Connection), "未知进程")
	case "inbound":
		return firstNonEmpty(r.Inbound, "—")
	case "outbound":
		return firstNonEmpty(r.Outbound, "—")
	case "pid":
		return facetOf(&r.Connection, facetPID)
	case "id":
		return r.ID
	}
	return upperOr(r.Network, "—")
}

func (f *fakeConnections) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/connections/"); ok {
		for _, row := range f.rows {
			if row.ID == id {
				_ = json.NewEncoder(w).Encode(row)
				return
			}
		}
		http.Error(w, `{"error":"missing"}`, http.StatusNotFound)
		return
	}
	values := r.URL.Query()
	f.queries = append(f.queries, r.URL.RawQuery)
	page := api.ConnectionPage{Facets: map[string][]api.ConnectionFacet{}}
	var pool []api.ConnectionRow
	state := values.Get("state")
	for _, row := range f.rows {
		closed := row.ClosedAt != 0
		page.Counts.All++
		if closed {
			page.Counts.Closed++
		} else {
			page.Counts.Active++
		}
		if state == "" || state == "all" || state == "closed" && closed || state == "active" && !closed {
			pool = append(pool, row)
		}
	}
	for _, name := range []string{"source", "process", "pid", "inbound", "outbound", "network"} {
		for _, row := range pool {
			value := fakeFacet(row, name)
			if value == "" {
				continue
			}
			i := slices.IndexFunc(page.Facets[name], func(o api.ConnectionFacet) bool { return o.Value == value })
			if i < 0 {
				page.Facets[name] = append(page.Facets[name], api.ConnectionFacet{Value: value, ProcessInfo: row.ProcessInfo})
				i = len(page.Facets[name]) - 1
			}
			page.Facets[name][i].Count++
		}
	}
	var matched []api.ConnectionRow
	for _, row := range pool {
		if f.keeps(row, values) {
			matched = append(matched, row)
			if row.ClosedAt == 0 {
				page.MatchedActive++
			}
		}
	}
	page.Matched = len(matched)
	page.Totals = page.Counts
	expand := strings.Split(values.Get("expand"), ",")
	if values.Get("group") == "app" || values.Get("group") == "parent" {
		for _, row := range matched {
			label := fakeApp(row)
			key := "app:" + label
			i := slices.IndexFunc(page.Groups, func(g api.ConnectionGroup) bool { return g.Key == key })
			if i < 0 {
				page.Groups = append(page.Groups, api.ConnectionGroup{Key: key, Label: label, ProcessCount: 1, Rules: 1, Chains: 1,
					Process: api.ConnectionProcess{Name: label}, Expanded: slices.Contains(expand, key) || slices.Contains(expand, "*")})
				i = len(page.Groups) - 1
			}
			g := &page.Groups[i]
			g.Count++
			g.UplinkTotal += row.UplinkTotal
			g.DownlinkTotal += row.DownlinkTotal
			if g.Expanded {
				child := row
				g.Children = append(g.Children, api.ConnectionItem{Row: &child})
			}
		}
		page.Total = len(page.Groups)
	} else {
		page.Rows = matched
		page.Total = len(matched)
	}
	page.Limit, _ = strconv.Atoi(values.Get("limit"))
	_ = json.NewEncoder(w).Encode(page)
}
