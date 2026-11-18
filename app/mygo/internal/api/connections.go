package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Int64 is a 64-bit integer that protobuf JSON writes as a string; it reads
// a string or a number and writes a string.
type Int64 int64

// UnmarshalJSON reads "123", 123 or null.
func (v *Int64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*v = 0
		return nil
	}
	text := strings.Trim(string(data), `"`)
	if text == "" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(text, 64)
		if ferr != nil {
			return err
		}
		n = int64(f)
	}
	*v = Int64(n)
	return nil
}

// MarshalJSON writes the integer as a string, as protobuf JSON does.
func (v Int64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(strconv.FormatInt(int64(v), 10))), nil
}

// ProcessInfo is the process behind a connection.
type ProcessInfo struct {
	ProcessID    int      `json:"processId"`
	UserID       int      `json:"userId"`
	UserName     string   `json:"userName"`
	ProcessPath  string   `json:"processPath"`
	PackageNames []string `json:"packageNames"`
}

// Connection is a connection of the core, as GET /connections answers it
// (daemon.Connection in protobuf JSON). Rule is the matched rule, named
// "<number> <name>" for a route table rule, which RuleID, RuleOwner and
// RuleSection then identify.
type Connection struct {
	ID            string `json:"id"`
	Inbound       string `json:"inbound"`
	InboundType   string `json:"inboundType"`
	IPVersion     int    `json:"ipVersion"`
	Network       string `json:"network"`
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Domain        string `json:"domain"`
	Protocol      string `json:"protocol"`
	User          string `json:"user"`
	FromOutbound  string `json:"fromOutbound"`
	CreatedAt     Int64  `json:"createdAt"`
	ClosedAt      Int64  `json:"closedAt"`
	UplinkTotal   Int64  `json:"uplinkTotal"`
	DownlinkTotal Int64  `json:"downlinkTotal"`
	Rule          string `json:"rule"`
	RuleID        string `json:"ruleId,omitempty"`
	RuleOwner     string `json:"ruleOwner,omitempty"`
	RuleSection   string `json:"ruleSection,omitempty"`
	// CoreRule is the rule as the core reported it.
	CoreRule     string       `json:"coreRule,omitempty"`
	Outbound     string       `json:"outbound"`
	OutboundType string       `json:"outboundType"`
	ChainList    []string     `json:"chainList"`
	ProcessInfo  *ProcessInfo `json:"processInfo,omitempty"`
	// RejectCount is how many times a reject rule refused the connection,
	// merged within a second; 0 for a carried connection.
	RejectCount Int64 `json:"rejectCount,omitempty"`
	// Mitm is the HTTPS decryption state: MitmPending, MitmDecrypted,
	// MitmFailed, MitmSkipped or MitmPlain; "" for a connection left alone.
	Mitm string `json:"mitm,omitempty"`
	// HTTPRequests counts the HTTP requests decrypted on the connection.
	HTTPRequests Int64 `json:"httpRequests,omitempty"`
	// MitmError is why decryption failed or was skipped.
	MitmError string `json:"mitmError,omitempty"`
}

// HTTPS decryption states of Connection.Mitm.
const (
	MitmPending   = "pending"
	MitmDecrypted = "decrypted"
	MitmFailed    = "failed"
	MitmSkipped   = "skipped"
	// MitmPlain marks plaintext HTTP handled by the rewrite engine; it is
	// not intercepted HTTPS.
	MitmPlain = "plain"
)

// Rejected reports whether a reject rule refused the connection.
func (c Connection) Rejected() bool { return c.RejectCount > 0 }

// Intercepted reports whether HTTPS decryption took the connection on,
// decrypted or failed.
func (c Connection) Intercepted() bool { return c.Mitm == MitmDecrypted || c.Mitm == MitmFailed }

// ConnectionRow is a connection of a query with its rates, in bytes per
// second; Run is the core run that reported it and CloseReason why the
// server closed it, "core-restart" when that run ended.
type ConnectionRow struct {
	Connection
	UpRate   float64 `json:"upRate"`
	DownRate float64 `json:"downRate"`
	// PeakUpRate and PeakDownRate are the highest rates it had.
	PeakUpRate   float64 `json:"peakUpRate"`
	PeakDownRate float64 `json:"peakDownRate"`
	Run          string  `json:"run,omitempty"`
	CloseReason  string  `json:"closeReason,omitempty"`
}

// ConnectionItem is a child of a group: a connection or a group.
type ConnectionItem struct {
	Row   *ConnectionRow   `json:"row,omitempty"`
	Group *ConnectionGroup `json:"group,omitempty"`
}

// ConnectionGroup is a group of connections by App or process with its
// summary; an expanded group lists its first children and how many
// remain.
type ConnectionGroup struct {
	Key           string            `json:"key"`
	Label         string            `json:"label"`
	Process       ConnectionProcess `json:"process"`
	Count         int               `json:"count"`
	ProcessCount  int               `json:"processCount"`
	CreatedAt     Int64             `json:"createdAt"`
	UpRate        float64           `json:"upRate"`
	DownRate      float64           `json:"downRate"`
	UplinkTotal   Int64             `json:"uplinkTotal"`
	DownlinkTotal Int64             `json:"downlinkTotal"`
	Rules         int               `json:"rules"`
	Chains        int               `json:"chains"`
	Users         []string          `json:"users"`
	Expanded      bool              `json:"expanded"`
	Children      []ConnectionItem  `json:"children"`
	Remaining     int               `json:"remaining"`
}

// ConnectionFacet is a filter choice with its count; a process carries
// the process of its connections, for its icon.
type ConnectionFacet struct {
	Value       string       `json:"value"`
	Count       int          `json:"count"`
	ProcessInfo *ProcessInfo `json:"processInfo,omitempty"`
}

// ConnectionCounts are the connections of each state.
type ConnectionCounts struct {
	All    int `json:"all"`
	Active int `json:"active"`
	// Closed are the closed connections an outbound carried, Rejected
	// those a reject rule refused.
	Closed   int `json:"closed"`
	Rejected int `json:"rejected"`
}

// ConnectionPage is the answer of GET /connections: a page of rows, or
// of groups when grouped, with the counts and facets of the history.
type ConnectionPage struct {
	Revision Int64 `json:"revision"`
	// Counts are the connections of each state passing the filters and
	// search, every limit but the state; Totals those of the history.
	Counts ConnectionCounts             `json:"counts"`
	Totals ConnectionCounts             `json:"totals"`
	Facets map[string][]ConnectionFacet `json:"facets"`
	Rows   []ConnectionRow              `json:"rows"`
	Groups []ConnectionGroup            `json:"groups"`
	// Total is the rows or groups of all pages, Matched the connections
	// passing the state, filters and search.
	Total   int `json:"total"`
	Matched int `json:"matched"`
	// MatchedActive are the matched connections still open, the ones
	// CloseConnections would close.
	MatchedActive int                 `json:"matchedActive"`
	Page          int                 `json:"page"`
	Limit         int                 `json:"limit"`
	Processes     []ConnectionProcess `json:"processes"`
	// ETag identifies the answer; NotModified reports that the server
	// answered 304 to the query's ETag, leaving the rest empty.
	ETag        string `json:"-"`
	NotModified bool   `json:"-"`
}

// ConnectionQuery is a query of GET /connections.
type ConnectionQuery struct {
	// State is "all", "active" or "closed".
	State  string
	Search string
	// Filter maps facets (source, process, inbound, outbound, network,
	// protocol…) to the values to keep: a connection passes a facet with
	// any of its values, and every facet.
	Filter map[string][]string
	Sort   string
	// Rule keeps the connections of one route table rule by its ID.
	Rule string
	// Order is "asc" or "desc"; empty is the column's default.
	Order string
	// Group is "none", "app", "parent", "rootDomain" or "domain".
	Group string
	// Expand lists the open groups, "*" all of them but Collapse; More
	// maps a group to how many children it shows.
	Expand, Collapse []string
	More             map[string]int
	Page, Limit      int
	// ETag is the answer the client holds, for a 304.
	ETag string
}

// Params is the query string of q, "?…", stable for equal queries.
func (q ConnectionQuery) Params() string {
	values := url.Values{}
	set := func(key, value string) {
		if value != "" {
			values.Set(key, value)
		}
	}
	set("state", q.State)
	set("search", q.Search)
	set("rule", q.Rule)
	for facet, list := range q.Filter {
		for _, value := range sortedStrings(list) {
			if value != "" {
				values.Add("filter."+facet, value)
			}
		}
	}
	set("sort", q.Sort)
	set("order", q.Order)
	set("group", q.Group)
	set("expand", strings.Join(sortedStrings(q.Expand), ","))
	set("collapse", strings.Join(sortedStrings(q.Collapse), ","))
	for key, n := range q.More {
		values.Add("more", key+"="+strconv.Itoa(n))
	}
	if more := values["more"]; len(more) > 1 {
		slices.Sort(more)
	}
	if q.Page > 0 {
		values.Set("page", strconv.Itoa(q.Page))
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	return "?" + values.Encode()
}

// filtered reports whether a facet of q has a value to keep.
func (q ConnectionQuery) filtered() bool {
	for _, values := range q.Filter {
		if len(values) > 0 {
			return true
		}
	}
	return false
}

func sortedStrings(list []string) []string {
	list = slices.Clone(list)
	slices.Sort(list)
	return list
}

// QueryConnections reads a page of the server's connection history.
func (c *Client) QueryConnections(ctx context.Context, q ConnectionQuery) (ConnectionPage, error) {
	var page ConnectionPage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL("/connections"+q.Params()), nil)
	if err != nil {
		return page, err
	}
	if q.ETag != "" {
		req.Header.Set("If-None-Match", q.ETag)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return ConnectionPage{ETag: q.ETag, NotModified: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return page, errorFrom(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return page, err
	}
	page.ETag = resp.Header.Get("ETag")
	return page, nil
}

// Connection reads one connection of the history, for the inspector.
func (c *Client) Connection(ctx context.Context, id string) (ConnectionRow, error) {
	var v ConnectionRow
	return v, c.Get(ctx, "/connections/"+pathEscape(id), &v)
}

// CloseConnection closes one connection.
func (c *Client) CloseConnection(ctx context.Context, id string) error {
	return c.Send(ctx, http.MethodDelete, "/connections/"+pathEscape(id), struct{}{}, nil)
}

// CloseAllConnections closes every active connection.
func (c *Client) CloseAllConnections(ctx context.Context) error {
	return c.Send(ctx, http.MethodDelete, "/connections", struct{}{}, nil)
}

// CloseResult tells how many active connections a close matched and how
// many it closed.
type CloseResult struct {
	Matched int `json:"matched"`
	Closed  int `json:"closed"`
}

// CloseConnections closes the active connections the query lists on the
// Active tab: its search, filters and Rule (State, sort and paging do not
// count). A query without any of them closes every connection, as
// CloseAllConnections does. It is the one close by condition, shared by
// the list's "Close listed connections" and the rules' "Interrupt connections".
func (c *Client) CloseConnections(ctx context.Context, q ConnectionQuery) (CloseResult, error) {
	keep := ConnectionQuery{Search: q.Search, Filter: q.Filter, Rule: q.Rule}
	var result CloseResult
	params := keep.Params()
	if q.Search == "" && !q.filtered() && q.Rule == "" {
		// Nothing limits the close: it is "Close all", which the server wants said.
		params = "?all=1"
	}
	err := c.Send(ctx, http.MethodPost, "/connections/close"+params, struct{}{}, &result)
	return result, err
}

// CloseConnectionIDs closes those of the named connections that are still
// active. No ids close nothing.
func (c *Client) CloseConnectionIDs(ctx context.Context, ids []string) (CloseResult, error) {
	var result CloseResult
	if len(ids) == 0 {
		return result, nil
	}
	err := c.Send(ctx, http.MethodPost, "/connections/close", map[string][]string{"ids": ids}, &result)
	return result, err
}

// Scopes of ClearConnectionHistory.
const (
	ClearAllConnections    = "all"
	ClearActiveConnections = "active"
	ClearClosedConnections = "closed"
)

// ClearConnectionHistory removes the server's records of scope (all,
// active or closed connections) without closing connections; queries
// then answer the remaining history.
func (c *Client) ClearConnectionHistory(ctx context.Context, scope string) error {
	return c.Send(ctx, http.MethodDelete, "/connections/history", map[string]string{"scope": scope}, nil)
}

// ConnectionSettings are the server's connection history settings.
type ConnectionSettings struct {
	// ClosedLimit is how many closed connections the history keeps, the
	// most recently closed; 0 keeps them all.
	ClosedLimit int `json:"closedLimit"`
}

// ConnectionSettings reads the connection history settings.
func (c *Client) ConnectionSettings(ctx context.Context) (ConnectionSettings, error) {
	var v ConnectionSettings
	err := c.Get(ctx, "/connections/settings", &v)
	return v, err
}

// SetConnectionSettings saves the connection history settings; the server
// trims its history to them at once.
func (c *Client) SetConnectionSettings(ctx context.Context, settings ConnectionSettings) (ConnectionSettings, error) {
	var v ConnectionSettings
	err := c.Send(ctx, http.MethodPatch, "/connections/settings", settings, &v)
	return v, err
}

// ExportConnections downloads the server's whole connection history as
// "csv" or "json".
func (c *Client) ExportConnections(ctx context.Context, format string) ([]byte, error) {
	return c.Raw(ctx, http.MethodGet, "/connections/export"+Query("format", format))
}

// ConnectionProcess is a process as the connection monitor groups by it:
// identity, ancestry and application.
type ConnectionProcess struct {
	PID       int    `json:"pid"`
	PPID      int    `json:"ppid"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	AppName   string `json:"appName,omitempty"`
	AppPath   string `json:"appPath,omitempty"`
	StartedAt int64  `json:"startedAt"`
}

func pathEscape(s string) string {
	return strings.ReplaceAll(esc(s), "+", "%20")
}
