package main

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/daemon"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// This file is the query of GET /api/v1/connections: the tabs, facets,
// search, sorting and the process grouping tree of the connection
// monitor, moved here from the client so that a page arrives whole.

// connProcessInfo is the process behind a connection, as the client reads it.
type connProcessInfo struct {
	ProcessID    int      `json:"processId"`
	UserID       int      `json:"userId"`
	UserName     string   `json:"userName"`
	ProcessPath  string   `json:"processPath"`
	PackageNames []string `json:"packageNames"`
}

// connInfo is a connection of kiko-core with the fields the query reads,
// written as daemon.Connection in protobuf JSON.
type connInfo struct {
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
	CreatedAt     int64  `json:"createdAt,string"`
	ClosedAt      int64  `json:"closedAt,string"`
	UplinkTotal   int64  `json:"uplinkTotal,string"`
	DownlinkTotal int64  `json:"downlinkTotal,string"`
	Rule          string `json:"rule"`
	RuleID        string `json:"ruleId,omitempty"`
	RuleOwner     string `json:"ruleOwner,omitempty"`
	RuleSection   string `json:"ruleSection,omitempty"`
	// CoreRule is the rule as the core reported it, "#<index> <rule>" from a
	// core that numbers rules, for diagnosing an unnamed rule.
	CoreRule     string           `json:"coreRule,omitempty"`
	Outbound     string           `json:"outbound"`
	OutboundType string           `json:"outboundType"`
	ChainList    []string         `json:"chainList"`
	ProcessInfo  *connProcessInfo `json:"processInfo,omitempty"`
	// RejectCount is how many times a reject rule refused the connection
	// (merged within a second); 0 for a carried connection.
	RejectCount int64 `json:"rejectCount,omitempty,string"`
	// Mitm is the HTTPS decryption state the core reported: "pending",
	// "decrypted", "failed" or "skipped"; "" for a connection it left alone.
	Mitm string `json:"mitm,omitempty"`
	// HTTPRequests counts the HTTP requests decrypted on the connection.
	HTTPRequests int64 `json:"httpRequests,omitempty,string"`
	// MitmError is why decryption failed or was skipped.
	MitmError string `json:"mitmError,omitempty"`
}

func connInfoOf(c *daemon.Connection) connInfo {
	info := connInfo{
		ID: c.GetId(), Inbound: c.GetInbound(), InboundType: c.GetInboundType(), IPVersion: int(c.GetIpVersion()),
		Network: c.GetNetwork(), Source: c.GetSource(), Destination: c.GetDestination(), Domain: c.GetDomain(),
		Protocol: c.GetProtocol(), User: c.GetUser(), FromOutbound: c.GetFromOutbound(), CreatedAt: c.GetCreatedAt(),
		ClosedAt: c.GetClosedAt(), UplinkTotal: c.GetUplinkTotal(), DownlinkTotal: c.GetDownlinkTotal(), Rule: c.GetRule(),
		CoreRule: c.GetRule(), Outbound: c.GetOutbound(), OutboundType: c.GetOutboundType(), ChainList: chainOrder(c.GetChainList()),
		RejectCount: c.GetRejectCount(), Mitm: c.GetMitm(), HTTPRequests: c.GetHttpRequests(), MitmError: c.GetMitmError(),
	}
	if p := c.GetProcessInfo(); p != nil {
		info.ProcessInfo = &connProcessInfo{ProcessID: int(p.GetProcessId()), UserID: int(p.GetUserId()), UserName: p.GetUserName(),
			ProcessPath: p.GetProcessPath(), PackageNames: p.GetPackageNames()}
	}
	return info
}

// connRow is a connection with its rates and totals.
type connRow struct {
	Conn             connInfo
	UpRate, DownRate float64
	// PeakUp and PeakDown are the highest rates the connection had.
	PeakUp, PeakDown float64
	// RuleKey is the matched rule's hit key (ruleHitKey), "" outside the
	// route table.
	RuleKey            string
	UpTotal, DownTotal int64
	// ClosedAt is in Unix milliseconds, 0 while active.
	ClosedAt int64
	// Run is the core run that reported the connection; CloseReason why
	// the history closed it, as closeReasonCoreRestart.
	Run, CloseReason string
}

// Closed reports whether the connection has closed.
func (r *connRow) Closed() bool { return r.ClosedAt != 0 }

// Rejected reports whether a reject rule refused the connection.
func (r *connRow) Rejected() bool { return r.Conn.RejectCount > 0 }

var connPathSeparators = func(r rune) bool { return r == '/' || r == '\\' }

func connBaseName(path string) string {
	parts := strings.FieldsFunc(path, connPathSeparators)
	if len(parts) == 0 || strings.HasSuffix(path, "/") || strings.HasSuffix(path, "\\") {
		return ""
	}
	return parts[len(parts)-1]
}

func connFirst(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// connProcessName is the connection's process name, or its package.
func connProcessName(c *connInfo) string {
	if c.ProcessInfo == nil {
		return ""
	}
	if name := connBaseName(c.ProcessInfo.ProcessPath); name != "" {
		return name
	}
	if len(c.ProcessInfo.PackageNames) > 0 {
		return c.ProcessInfo.PackageNames[0]
	}
	return ""
}

// connHostName is the connection's domain or destination.
func connHostName(c *connInfo) string {
	return connFirst(c.Domain, c.Destination, "—")
}

// chainOrder puts the core's chain, which lists the outbound that dials
// first, in the order a connection takes: from the group the rule chose
// to the outbound that dials.
func chainOrder(chain []string) []string {
	ordered := slices.Clone(chain)
	slices.Reverse(ordered)
	return ordered
}

// connChainName is the outbound chain, "a → b".
func connChainName(c *connInfo) string {
	if len(c.ChainList) > 0 {
		if s := strings.Join(c.ChainList, " → "); s != "" {
			return s
		}
	}
	return connFirst(c.Outbound, "—")
}

// connColumn is a column of the connection table.
type connColumn string

// Columns of the connection table, as the client names them.
const (
	colHost          connColumn = "host"
	colRule          connColumn = "rule"
	colUploadRate    connColumn = "uploadRate"
	colDownloadRate  connColumn = "downloadRate"
	colUploadTotal   connColumn = "uploadTotal"
	colDownloadTotal connColumn = "downloadTotal"
	colSource        connColumn = "source"
	colDestination   connColumn = "destination"
	colCreated       connColumn = "created"
	colNetwork       connColumn = "network"
	colInbound       connColumn = "inbound"
	colOutbound      connColumn = "outbound"
	colUser          connColumn = "user"
)

var connColumns = []connColumn{colHost, colRule, colUploadRate, colDownloadRate, colUploadTotal, colDownloadTotal,
	colSource, colDestination, colCreated, colNetwork, colInbound, colOutbound, colUser}

func (col connColumn) isTotal() bool {
	return col == colUploadRate || col == colDownloadRate || col == colUploadTotal || col == colDownloadTotal
}

// descendingByDefault: times and amounts sort largest first.
func (col connColumn) descendingByDefault() bool { return col == colCreated || col.isTotal() }

var connCollator = collate.New(language.Chinese)

func collateCompare(a, b string) int { return connCollator.CompareString(a, b) }

var cryptexPrefix = regexp.MustCompile(`^/System/Volumes/Preboot/Cryptexes/(?:Incoming/)?OS/System/`)

// canonicalProcessPath folds the Cryptex OS paths of macOS into one.
func canonicalProcessPath(path string) string {
	if loc := cryptexPrefix.FindStringIndex(path); loc != nil {
		return "/System/" + path[loc[1]:]
	}
	return path
}

// columnText is the plain text of a column, for search and sorting.
func columnText(row *connRow, key connColumn) string {
	c := &row.Conn
	switch key {
	case colHost:
		return connHostName(c) + " " + connProcessName(c)
	case colRule:
		return formatConnectionRule(c.Rule) + " " + connChainName(c)
	case colUploadRate:
		return strconv.FormatFloat(row.UpRate, 'f', -1, 64)
	case colDownloadRate:
		return strconv.FormatFloat(row.DownRate, 'f', -1, 64)
	case colUploadTotal:
		return strconv.FormatInt(row.UpTotal, 10)
	case colDownloadTotal:
		return strconv.FormatInt(row.DownTotal, 10)
	case colSource:
		return c.Source
	case colDestination:
		return c.Destination
	case colCreated:
		return strconv.FormatInt(c.CreatedAt, 10)
	case colNetwork:
		return c.Network + " " + c.Protocol
	case colInbound:
		return c.Inbound + " " + c.InboundType
	case colOutbound:
		return c.Outbound + " " + c.OutboundType
	case colUser:
		if c.User != "" {
			return c.User
		}
		if c.ProcessInfo != nil {
			return c.ProcessInfo.UserName
		}
	}
	return ""
}

func cmpNumber[T int64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareConnections orders two rows by a column, ascending.
func compareConnections(a, b *connRow, key connColumn) int {
	switch key {
	case colCreated:
		return cmpNumber(a.Conn.CreatedAt, b.Conn.CreatedAt)
	case colUploadRate:
		return cmpNumber(a.UpRate, b.UpRate)
	case colDownloadRate:
		return cmpNumber(a.DownRate, b.DownRate)
	case colUploadTotal:
		return cmpNumber(a.UpTotal, b.UpTotal)
	case colDownloadTotal:
		return cmpNumber(a.DownTotal, b.DownTotal)
	}
	return collateCompare(columnText(a, key), columnText(b, key))
}

var connHostPort = regexp.MustCompile(`^([^:]+):[0-9]+$`)

// sourceHost is the address without its port; bare IPv6 stays whole.
func sourceHost(source string) string {
	value := strings.TrimSpace(source)
	if strings.HasPrefix(value, "[") && strings.Contains(value, "]") {
		return value[1:strings.Index(value, "]")]
	}
	if m := connHostPort.FindStringSubmatch(value); m != nil {
		return m[1]
	}
	return value
}

// timestampMs reads seconds or milliseconds as milliseconds; 0 for none.
func timestampMs(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value < 100_000_000_000 {
		return value * 1000
	}
	return value
}

var connRuleLabels = map[string]string{
	"rule_set": "RuleSet", "domain": "Domain", "domain_suffix": "DomainSuffix",
	"domain_keyword": "DomainKeyword", "domain_regex": "DomainRegex",
	"ip_cidr": "IP CIDR", "source_ip_cidr": "Source IP CIDR",
	"port": "Port", "port_range": "PortRange", "source_port": "SourcePort",
	"process_name": "Process", "process_path": "ProcessPath", "package_name": "Package",
	"network": "Network", "protocol": "Protocol", "inbound": "Inbound",
	"ip_version": "IPVersion", "user": "User", "user_id": "UserID",
}

var (
	connRuleField   = regexp.MustCompile(`\b([a-z][a-z_0-9]*)=(\[[^\[\]]*\]|[^\s()&|]+)`)
	connVerbatimKey = regexp.MustCompile(`path|regex|ssid`)
)

// formatConnectionRule prettifies the field notation of a rule, keeping
// logical operators and unknown fields, as the client shows it.
func formatConnectionRule(rule string) string {
	return connRuleField.ReplaceAllStringFunc(rule, func(match string) string {
		m := connRuleField.FindStringSubmatch(match)
		key, raw := m[1], m[2]
		label, ok := connRuleLabels[key]
		if !ok {
			parts := strings.Split(key, "_")
			for i, part := range parts {
				if part != "" {
					parts[i] = strings.ToUpper(part[:1]) + part[1:]
				}
			}
			label = strings.Join(parts, "")
		}
		value := raw
		if strings.HasPrefix(raw, "[") && !connVerbatimKey.MatchString(key) {
			value = strings.Join(strings.Fields(raw[1:len(raw)-1]), ", ")
		}
		return label + ": " + value
	})
}

// connFacet is a filter dimension of the connection monitor.
type connFacet string

const (
	facetSource   connFacet = "source"
	facetProcess  connFacet = "process"
	facetInbound  connFacet = "inbound"
	facetOutbound connFacet = "outbound"
	facetNetwork  connFacet = "network"
	facetProtocol connFacet = "protocol"
	facetRule     connFacet = "rule"
	facetApp      connFacet = "app"
	facetPID      connFacet = "pid"
	// facetID keeps one connection; it has no options in the filter pane.
	facetID connFacet = "id"
)

var connFacets = []connFacet{facetSource, facetProcess, facetApp, facetPID, facetInbound, facetRule, facetOutbound, facetNetwork, facetProtocol}

// connAppName is the App a connection's process runs in, as "Safari" for
// /Applications/Safari.app/… or a Windows App by its description; "" for
// other processes.
func connAppName(c *connInfo) string {
	if c.ProcessInfo == nil {
		return ""
	}
	if bundle := appBundle(c.ProcessInfo.ProcessPath); bundle != "" {
		return strings.TrimSuffix(connBaseName(bundle), ".app")
	}
	_, name := platformAppIdentity(c.ProcessInfo.ProcessPath)
	return name
}

func facetValue(row *connRow, f connFacet) string {
	c := &row.Conn
	switch f {
	case facetSource:
		return connFirst(sourceHost(c.Source), "—")
	case facetProcess:
		return connFirst(connProcessName(c), "未知进程")
	case facetApp:
		// Only the connections of an App list it.
		return connAppName(c)
	case facetPID:
		if c.ProcessInfo == nil || c.ProcessInfo.ProcessID <= 0 {
			return ""
		}
		return strconv.Itoa(c.ProcessInfo.ProcessID)
	case facetInbound:
		return connFirst(c.Inbound, "—")
	case facetRule:
		// route.final carries the connections no rule matched.
		return connFirst(c.Rule, "默认出口")
	case facetOutbound:
		if c.RejectCount > 0 {
			return "拒绝"
		}
		return connFirst(c.Outbound, "—")
	case facetProtocol:
		return connFirst(protocolLabel(c.Protocol), "未识别")
	case facetID:
		return c.ID
	}
	return connFirst(strings.ToUpper(c.Network), "—")
}

// protocolLabel names the application protocol the core sniffed (http,
// tls, quic, stun, dns, bittorrent…), "" when it sniffed none.
func protocolLabel(protocol string) string {
	switch p := strings.ToLower(strings.TrimSpace(protocol)); p {
	case "":
		return ""
	case "bittorrent":
		return "BitTorrent"
	default:
		return strings.ToUpper(p)
	}
}

// connSearchMatch reports whether row matches query, lowercased and
// trimmed; "" matches every row.
func connSearchMatch(row *connRow, query string) bool {
	if query == "" {
		return true
	}
	c := &row.Conn
	fields := []string{c.Domain, c.Destination, c.Source, c.Rule, c.Outbound, c.Inbound, c.User, c.Protocol}
	if c.ProcessInfo != nil {
		fields = append(fields, c.ProcessInfo.ProcessPath)
	}
	fields = append(fields, c.ChainList...)
	return slices.ContainsFunc(fields, func(v string) bool { return strings.Contains(strings.ToLower(v), query) })
}

// filterConnectionRows keeps the rows matching every facet filter and the
// search.
func filterConnectionRows(pool []*connRow, filter map[connFacet][]string, search string) []*connRow {
	return filterQueryRows(pool, connQuery{Filter: filter, Search: search})
}

// connCondition is one limit of a query: the tab, a facet filter, the
// search or a hidden process, App or rule limit.
type connCondition struct {
	// facet is the filter group the condition belongs to, "" for none.
	facet connFacet
	pass  func(row *connRow) bool
}

// connTab is the tab of a row: "active", "closed" or "rejected".
func connTab(row *connRow) string {
	switch {
	case row.Rejected():
		return "rejected"
	case row.Closed():
		return "closed"
	}
	return "active"
}

// conditions are the limits of q, the tab first when tab is set.
func (q connQuery) conditions(tab bool) []connCondition {
	var conds []connCondition
	if tab {
		state := q.State
		conds = append(conds, connCondition{pass: func(row *connRow) bool { return state == "all" || connTab(row) == state }})
	}
	for _, f := range append(slices.Clone(connFacets), facetID) {
		values := q.Filter[f]
		if len(values) == 0 {
			continue
		}
		// A group passes the rows of any of its chosen values.
		if len(values) == 1 {
			value := values[0]
			conds = append(conds, connCondition{facet: f, pass: func(row *connRow) bool { return facetValue(row, f) == value }})
			continue
		}
		set := make(map[string]bool, len(values))
		for _, v := range values {
			set[v] = true
		}
		conds = append(conds, connCondition{facet: f, pass: func(row *connRow) bool { return set[facetValue(row, f)] }})
	}
	if query := strings.ToLower(strings.TrimSpace(q.Search)); query != "" {
		conds = append(conds, connCondition{pass: func(row *connRow) bool { return connSearchMatch(row, query) }})
	}
	if pid := q.PID; pid > 0 {
		conds = append(conds, connCondition{pass: func(row *connRow) bool {
			return row.Conn.ProcessInfo != nil && row.Conn.ProcessInfo.ProcessID == pid
		}})
	}
	if rule := q.Rule; rule != "" {
		conds = append(conds, connCondition{pass: func(row *connRow) bool { return row.RuleKey == rule }})
	}
	if app := q.App; app != "" {
		conds = append(conds, connCondition{pass: func(row *connRow) bool {
			return row.Conn.ProcessInfo != nil && pathInApp(row.Conn.ProcessInfo.ProcessPath, app)
		}})
	}
	return conds
}

// connFacetOption is a filter choice with its count; a process, App or
// PID carries the process of its first connection with a path, for its
// icon.
type connFacetOption struct {
	Value       string           `json:"value"`
	Count       int              `json:"count"`
	ProcessInfo *connProcessInfo `json:"processInfo,omitempty"`
}

// connFacetTally counts the values of each facet as rows are added.
type connFacetTally struct {
	options map[connFacet][]connFacetOption
	index   map[connFacet]map[string]int
}

func newConnFacetTally() *connFacetTally {
	t := &connFacetTally{options: make(map[connFacet][]connFacetOption, len(connFacets)), index: make(map[connFacet]map[string]int, len(connFacets))}
	for _, f := range connFacets {
		t.options[f] = []connFacetOption{}
		t.index[f] = map[string]int{}
	}
	return t
}

// add counts row toward the options of facet f.
func (t *connFacetTally) add(f connFacet, row *connRow) {
	value := facetValue(row, f)
	if value == "" {
		return
	}
	t.addValue(f, value, row)
}

func (t *connFacetTally) addValue(f connFacet, value string, row *connRow) {
	i, ok := t.index[f][value]
	if !ok {
		i = len(t.options[f])
		t.index[f][value] = i
		t.options[f] = append(t.options[f], connFacetOption{Value: value})
	}
	if row == nil {
		return
	}
	options := t.options[f]
	options[i].Count++
	if (f == facetProcess || f == facetApp || f == facetPID) && row.Conn.ProcessInfo != nil {
		if old := options[i].ProcessInfo; old == nil || old.ProcessPath == "" {
			options[i].ProcessInfo = row.Conn.ProcessInfo
		}
	}
}

// keepProcess gives the options values of f, processes, Apps or PIDs
// that lack one, the process of the first row of all with each value, for
// their icons, in one pass over all.
func (t *connFacetTally) keepProcess(f connFacet, values []string, all []*connRow) {
	if f != facetProcess && f != facetApp && f != facetPID {
		return
	}
	lacking := map[string]*connFacetOption{}
	for _, value := range values {
		i, ok := t.index[f][value]
		if !ok {
			continue
		}
		if option := &t.options[f][i]; option.ProcessInfo == nil || option.ProcessInfo.ProcessPath == "" {
			lacking[value] = option
		}
	}
	for _, row := range all {
		if len(lacking) == 0 {
			return
		}
		p := row.Conn.ProcessInfo
		if p == nil || p.ProcessPath == "" {
			continue
		}
		value := facetValue(row, f)
		if option := lacking[value]; option != nil {
			option.ProcessInfo = p
			delete(lacking, value)
		}
	}
}

// result is the options of each facet, the most frequent first.
func (t *connFacetTally) result() map[connFacet][]connFacetOption {
	for _, options := range t.options {
		slices.SortStableFunc(options, func(a, b connFacetOption) int { return b.Count - a.Count })
	}
	return t.options
}

// connectionFacets counts the values of each facet over pool, the most
// frequent first.
func connectionFacets(pool []*connRow) map[connFacet][]connFacetOption {
	tally := newConnFacetTally()
	for _, row := range pool {
		for _, f := range connFacets {
			tally.add(f, row)
		}
	}
	return tally.result()
}

// facetConnectionRows answers q's limits over all in one pass: the rows
// passing them all; the tab counts over the rows passing every limit but
// the tab; and each facet's options over the rows passing every limit but
// its own filter, a chosen value listed even when no row has it.
func facetConnectionRows(all []*connRow, q connQuery) (matched []*connRow, counts, totals connCounts, facets map[connFacet][]connFacetOption) {
	conds := q.conditions(true)
	const tabIndex = 0
	own := make(map[connFacet]int, len(conds))
	for i, c := range conds {
		if c.facet != "" {
			own[c.facet] = i
		}
	}
	tally := newConnFacetTally()
	for _, row := range all {
		totals.add(row)
		miss := facetMiss(len(conds), func(i int) bool { return conds[i].pass(row) })
		if miss == facetNone {
			continue
		}
		if miss == facetAll {
			matched = append(matched, row)
		}
		if facetCounts(miss, tabIndex) {
			counts.add(row)
		}
		for _, f := range connFacets {
			i, ok := own[f]
			if !ok {
				i = -1
			}
			if facetCounts(miss, i) {
				tally.add(f, row)
			}
		}
	}
	for _, f := range connFacets {
		for _, value := range q.Filter[f] {
			if value != "" {
				tally.addValue(f, value, nil)
			}
		}
		// The options hold their pointers once every value is added.
		tally.keepProcess(f, q.Filter[f], all)
	}
	return matched, counts, totals, tally.result()
}

// connProcess is a process of the monitor, as connections group by it.
type connProcess struct {
	PID       int    `json:"pid"`
	PPID      int    `json:"ppid"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	AppName   string `json:"appName,omitempty"`
	AppPath   string `json:"appPath,omitempty"`
	StartedAt int64  `json:"startedAt"`
}

// connGroupMode is how the connection list groups its rows.
type connGroupMode string

const (
	groupNone   connGroupMode = "none"
	groupApp    connGroupMode = "app"
	groupParent connGroupMode = "parent"
	// groupRootDomain groups by registrable domain (eTLD+1), groupDomain
	// by the full domain; connections without one share connIPGroupKey.
	groupRootDomain connGroupMode = "rootDomain"
	groupDomain     connGroupMode = "domain"
)

// connIPGroupKey and connIPGroupLabel are the domain group of the
// connections without a domain.
const (
	connIPGroupKey   = "ip"
	connIPGroupLabel = "IP 地址"
)

// connDomainGroup is the key and label of a connection's domain group.
func connDomainGroup(mode connGroupMode, c *connInfo) (key, label string) {
	if c.Domain == "" {
		return connIPGroupKey, connIPGroupLabel
	}
	host, root := normalizeUsageDomain(c.Domain)
	if host == "" || net.ParseIP(host) != nil {
		return connIPGroupKey, connIPGroupLabel
	}
	if mode == groupRootDomain {
		return "root:" + root, root
	}
	return "host:" + host, host
}

// processMeta is the process identity and the last known ancestry of a
// connection, kept across process snapshots.
type processMeta struct {
	Process   connProcess
	Ancestors []connProcess
	Identity  string
}

func processKey(p connProcess) string { return fmt.Sprintf("%d:%d:%s", p.PID, p.StartedAt, p.Path) }

func processDisplayName(p connProcess) string {
	return connFirst(p.AppName, p.Name, connBaseName(p.Path), "未知进程")
}

// appBundle returns the path up to the first ".app" bundle of path.
func appBundle(path string) string {
	for i := 0; ; {
		j := strings.Index(path[i:], ".app")
		if j < 0 {
			return ""
		}
		end := i + j + len(".app")
		if end == len(path) || path[end] == '/' || path[end] == '\\' {
			return path[:end]
		}
		i = i + j + 1
	}
}

// captureConnectionProcesses retains the process identity and the last
// known ancestry of each connection.
func captureConnectionProcesses(rows []*connRow, processes map[int]connProcess, previous map[string]processMeta) map[string]processMeta {
	result := make(map[string]processMeta, len(rows))
	for _, row := range rows {
		c := &row.Conn
		old, hasOld := previous[c.ID]
		info := c.ProcessInfo
		var candidate *connProcess
		if info != nil && info.ProcessID != 0 {
			if p, ok := processes[info.ProcessID]; ok {
				candidate = &p
			}
		}
		created := timestampMs(c.CreatedAt)
		valid := candidate != nil && (info.ProcessPath == "" || candidate.Path == info.ProcessPath) &&
			(created == 0 || candidate.StartedAt == 0 || candidate.StartedAt <= created)
		if hasOld && ((row.ClosedAt != 0 && old.Process.StartedAt > 0) || !valid ||
			(old.Process.StartedAt > 0 && processKey(old.Process) != processKey(*candidate))) {
			result[c.ID] = old
			continue
		}
		path := ""
		pid := 0
		if info != nil {
			path, pid = info.ProcessPath, info.ProcessID
		}
		var process connProcess
		if valid {
			process = *candidate
		} else {
			appPath := appBundle(path)
			process = connProcess{PID: pid, Path: path, Name: connFirst(connProcessName(c), "未知进程"), AppPath: appPath,
				AppName: strings.TrimSuffix(connBaseName(appPath), ".app")}
		}
		upward := []connProcess{process}
		seen := map[int]int{process.PID: 0}
		var previousChain []connProcess
		if hasOld {
			previousChain = append(slices.Clone(old.Ancestors), old.Process)
		}
		current := process
		for {
			cachedIndex := slices.IndexFunc(previousChain, func(p connProcess) bool { return processKey(p) == processKey(current) })
			var cachedParent *connProcess
			if cachedIndex > 0 {
				cachedParent = &previousChain[cachedIndex-1]
			}
			// Keep the captured lineage when an existing process is adopted by init.
			parentPID := current.PPID
			if parentPID <= 1 {
				parentPID = 0
				if cachedParent != nil {
					parentPID = cachedParent.PID
				}
			}
			if parentPID == 0 || parentPID == current.PID {
				break
			}
			var parent *connProcess
			if current.PPID <= 1 {
				parent = cachedParent
			} else if p, ok := processes[parentPID]; ok {
				parent = &p
			} else if hasOld {
				if i := slices.IndexFunc(old.Ancestors, func(p connProcess) bool { return p.PID == parentPID }); i >= 0 {
					parent = &old.Ancestors[i]
				}
			}
			if parent == nil || (parent.StartedAt != 0 && current.StartedAt != 0 && parent.StartedAt > current.StartedAt) {
				break
			}
			if repeated, ok := seen[parent.PID]; ok {
				// Canonical root: cut the incoming edge to the lowest PID in the cycle.
				root := upward[repeated]
				for _, p := range upward[repeated:] {
					if p.PID < root.PID {
						root = p
					}
				}
				i := slices.IndexFunc(upward, func(p connProcess) bool { return p.PID == root.PID })
				upward = upward[:i+1]
				break
			}
			seen[parent.PID] = len(upward)
			upward = append(upward, *parent)
			current = *parent
		}
		identity := processKey(process)
		if process.StartedAt <= 0 {
			name := path
			if name == "" && info != nil && len(info.PackageNames) > 0 {
				name = info.PackageNames[0]
			}
			identity = fmt.Sprintf("%s:%d:%s", sourceHost(c.Source), process.PID, connFirst(name, "unknown"))
		}
		ancestors := slices.Clone(upward[1:])
		slices.Reverse(ancestors)
		result[c.ID] = processMeta{Process: process, Identity: identity, Ancestors: ancestors}
	}
	return result
}

// groupTotals are a group's summed rates and amounts.
type groupTotals struct {
	UpRate, DownRate   float64
	UpTotal, DownTotal int64
}

// connNode is a line of the connection list: a connection or a group.
type connNode struct {
	Key string
	// Row is set for a connection.
	Row *connRow

	Label        string
	Process      connProcess
	Children     []*connNode
	Rows         []*connRow
	Totals       groupTotals
	Texts        map[connColumn][]string
	CreatedAt    int64
	ProcessCount int
}

func (n *connNode) group() bool { return n.Row == nil }

// ended reports a connection that has closed or was refused, or a group
// whose connections all have.
func (n *connNode) ended() bool {
	if !n.group() {
		return n.Row.Closed() || n.Row.Rejected()
	}
	for _, row := range n.Rows {
		if !row.Closed() && !row.Rejected() {
			return false
		}
	}
	return len(n.Rows) > 0
}

func leafNode(row *connRow) *connNode { return &connNode{Key: "connection:" + row.Conn.ID, Row: row} }

// sortValue is what a node sorts by: a string, an integer or a float.
type sortValue struct {
	kind int // 0 string, 1 integer, 2 float
	s    string
	i    int64
	f    float64
}

func (a sortValue) compare(b sortValue) int {
	switch {
	case a.kind == 0 && b.kind == 0:
		return collateCompare(a.s, b.s)
	case a.kind == 1 && b.kind == 1:
		return cmpNumber(a.i, b.i)
	}
	return cmpNumber(a.number(), b.number())
}

func (a sortValue) number() float64 {
	if a.kind == 1 {
		return float64(a.i)
	}
	return a.f
}

func nodeSortValue(n *connNode, key connColumn) sortValue {
	if !n.group() {
		switch key {
		case colCreated:
			return sortValue{kind: 1, i: timestampMs(n.Row.Conn.CreatedAt)}
		case colUploadRate:
			return sortValue{kind: 2, f: n.Row.UpRate}
		case colDownloadRate:
			return sortValue{kind: 2, f: n.Row.DownRate}
		case colUploadTotal:
			return sortValue{kind: 1, i: n.Row.UpTotal}
		case colDownloadTotal:
			return sortValue{kind: 1, i: n.Row.DownTotal}
		}
		return sortValue{s: columnText(n.Row, key)}
	}
	switch key {
	case colUploadRate:
		return sortValue{kind: 2, f: n.Totals.UpRate}
	case colDownloadRate:
		return sortValue{kind: 2, f: n.Totals.DownRate}
	case colUploadTotal:
		return sortValue{kind: 1, i: n.Totals.UpTotal}
	case colDownloadTotal:
		return sortValue{kind: 1, i: n.Totals.DownTotal}
	case colCreated:
		return sortValue{kind: 1, i: n.CreatedAt}
	case colHost:
		return sortValue{s: n.Label}
	}
	return sortValue{s: strings.Join(n.Texts[key], ", ")}
}

// buildConnectionTree groups rows by App, by process ancestry or by
// domain, sorted by a column; groupNone lists the rows themselves.
func buildConnectionTree(rows []*connRow, metadata map[string]processMeta, mode connGroupMode, sort connColumn, descending bool) []*connNode {
	return buildOrderedConnectionTree(rows, metadata, mode, sort, descending, false)
}

// buildOrderedConnectionTree is buildConnectionTree that, with endedLast,
// puts the connections that have ended (closed or refused) after those
// still open, each part in the order sort gives; a group goes last when
// all its connections have ended. The All tab lists the open ones first.
func buildOrderedConnectionTree(rows []*connRow, metadata map[string]processMeta, mode connGroupMode, sort connColumn, descending, endedLast bool) []*connNode {
	var roots []*connNode
	index := map[string]*connNode{}
	create := func(key string, p connProcess, label string) *connNode {
		return &connNode{Key: key, Process: p, Label: label}
	}
	main := func(v connProcess) bool {
		return v.AppPath != "" && strings.HasPrefix(v.Path, v.AppPath+"/Contents/MacOS/")
	}
	if mode != groupNone {
		for _, row := range rows {
			meta := metadata[row.Conn.ID]
			if mode == groupRootDomain || mode == groupDomain {
				key, label := connDomainGroup(mode, &row.Conn)
				group := index[key]
				if group == nil {
					group = create(key, connProcess{}, label)
					index[key] = group
					roots = append(roots, group)
				}
				group.Children = append(group.Children, leafNode(row))
				continue
			}
			if mode == groupApp {
				p := meta.Process
				var key string
				switch {
				case p.AppPath != "":
					key = "app:" + canonicalProcessPath(p.AppPath)
				case p.Path != "":
					key = "app:path:" + canonicalProcessPath(p.Path)
				default:
					key = "app:process:" + meta.Identity
				}
				group := index[key]
				if group == nil {
					group = create(key, p, processDisplayName(p))
					index[key] = group
					roots = append(roots, group)
				}
				g := group.Process
				if (main(p) && !main(g)) || (main(p) == main(g) && (p.StartedAt < g.StartedAt || (p.StartedAt == g.StartedAt && p.PID < g.PID))) {
					group.Process = p
				}
				group.Children = append(group.Children, leafNode(row))
				continue
			}
			chain := append(slices.Clone(meta.Ancestors), meta.Process)
			var parent *connNode
			for _, p := range chain {
				key := "parent:" + processKey(p)
				if p.StartedAt == 0 {
					key = "parent:" + sourceHost(row.Conn.Source) + ":" + processKey(p)
				}
				var group *connNode
				if parent != nil {
					for _, child := range parent.Children {
						if child.group() && child.Key == key {
							group = child
							break
						}
					}
				} else {
					group = index[key]
				}
				if group == nil {
					group = create(key, p, connFirst(p.Name, processDisplayName(p)))
					if parent != nil {
						parent.Children = append(parent.Children, group)
					} else {
						index[key] = group
						roots = append(roots, group)
					}
				}
				parent = group
			}
			parent.Children = append(parent.Children, leafNode(row))
		}
	}
	compare := func(a, b *connNode) int {
		if endedLast {
			if ea, eb := a.ended(), b.ended(); ea != eb {
				if ea {
					return 1
				}
				return -1
			}
		}
		order := nodeSortValue(a, sort).compare(nodeSortValue(b, sort))
		if descending {
			order = -order
		}
		if order != 0 {
			return order
		}
		return collateCompare(a.Key, b.Key)
	}
	var finish func(group *connNode)
	finish = func(group *connNode) {
		group.Rows = nil
		for _, child := range group.Children {
			if child.group() {
				finish(child)
				group.Rows = append(group.Rows, child.Rows...)
			} else {
				group.Rows = append(group.Rows, child.Row)
			}
		}
		processIDs := map[string]bool{}
		texts := map[connColumn]map[string]bool{}
		for _, row := range group.Rows {
			group.Totals.UpRate += row.UpRate
			group.Totals.DownRate += row.DownRate
			group.Totals.UpTotal += row.UpTotal
			group.Totals.DownTotal += row.DownTotal
			if stamp := timestampMs(row.Conn.CreatedAt); stamp != 0 && (group.CreatedAt == 0 || stamp < group.CreatedAt) {
				group.CreatedAt = stamp
			}
			processIDs[metadata[row.Conn.ID].Identity] = true
			for _, key := range connColumns {
				if key.isTotal() || key == colHost || key == colCreated {
					continue
				}
				if texts[key] == nil {
					texts[key] = map[string]bool{}
				}
				if value := columnText(row, key); value != "" {
					texts[key][value] = true
				}
			}
		}
		group.Texts = map[connColumn][]string{}
		for key, values := range texts {
			list := make([]string, 0, len(values))
			for v := range values {
				list = append(list, v)
			}
			slices.SortFunc(list, collateCompare)
			group.Texts[key] = list
		}
		group.ProcessCount = len(processIDs)
		slices.SortStableFunc(group.Children, compare)
	}
	for _, group := range roots {
		finish(group)
	}
	if mode == groupNone {
		roots = make([]*connNode, len(rows))
		for i, row := range rows {
			roots[i] = leafNode(row)
		}
	}
	slices.SortStableFunc(roots, compare)
	return roots
}

// connRuleCounts are how many distinct rules and chains rows use.
func connRuleCounts(rows []*connRow) (rules, chains int) {
	ruleSet, chainSet := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if row.Conn.Rule != "" {
			ruleSet[row.Conn.Rule] = true
		}
		if chain := connChainName(&row.Conn); chain != "—" {
			chainSet[chain] = true
		}
	}
	return len(ruleSet), len(chainSet)
}

// connChildLimit is how many children an expanded group shows until the
// client asks for more.
const connChildLimit = 50

// connExpansion is which groups a query opens: the listed keys, or all
// of them but the collapsed ones.
type connExpansion struct {
	all       bool
	keys      map[string]bool
	collapsed map[string]bool
	// limits are the children shown of a group, connChildLimit by default.
	limits map[string]int
}

func (e connExpansion) open(key string) bool {
	if e.collapsed[key] {
		return false
	}
	return e.all || e.keys[key]
}

func (e connExpansion) limit(key string) int {
	if limit := e.limits[key]; limit > 0 {
		return limit
	}
	return connChildLimit
}

// connRowJSON is a connection of a query result.
type connRowJSON struct {
	connInfo
	UpRate       float64 `json:"upRate"`
	DownRate     float64 `json:"downRate"`
	PeakUpRate   float64 `json:"peakUpRate"`
	PeakDownRate float64 `json:"peakDownRate"`
	Run          string  `json:"run,omitempty"`
	CloseReason  string  `json:"closeReason,omitempty"`
}

func connRowOutput(row *connRow) connRowJSON {
	info := row.Conn
	info.UplinkTotal, info.DownlinkTotal, info.ClosedAt = row.UpTotal, row.DownTotal, row.ClosedAt
	return connRowJSON{connInfo: info, UpRate: row.UpRate, DownRate: row.DownRate, PeakUpRate: row.PeakUp, PeakDownRate: row.PeakDown, Run: row.Run, CloseReason: row.CloseReason}
}

// connItemJSON is a child of a group: a connection or a group.
type connItemJSON struct {
	Row   *connRowJSON   `json:"row,omitempty"`
	Group *connGroupJSON `json:"group,omitempty"`
}

// connGroupJSON is a group of a query result with its summary; an
// expanded group lists its first children and how many remain.
type connGroupJSON struct {
	Key          string         `json:"key"`
	Label        string         `json:"label"`
	Process      connProcess    `json:"process"`
	Count        int            `json:"count"`
	ProcessCount int            `json:"processCount"`
	CreatedAt    int64          `json:"createdAt,string"`
	UpRate       float64        `json:"upRate"`
	DownRate     float64        `json:"downRate"`
	UpTotal      int64          `json:"uplinkTotal,string"`
	DownTotal    int64          `json:"downlinkTotal,string"`
	Rules        int            `json:"rules"`
	Chains       int            `json:"chains"`
	Users        []string       `json:"users"`
	Expanded     bool           `json:"expanded,omitempty"`
	Children     []connItemJSON `json:"children,omitempty"`
	Remaining    int            `json:"remaining,omitempty"`
}

func connGroupOutput(n *connNode, expansion connExpansion) *connGroupJSON {
	rules, chains := connRuleCounts(n.Rows)
	group := &connGroupJSON{Key: n.Key, Label: n.Label, Process: n.Process, Count: len(n.Rows), ProcessCount: n.ProcessCount,
		CreatedAt: n.CreatedAt, UpRate: n.Totals.UpRate, DownRate: n.Totals.DownRate, UpTotal: n.Totals.UpTotal, DownTotal: n.Totals.DownTotal,
		Rules: rules, Chains: chains, Users: n.Texts[colUser]}
	if group.Users == nil {
		group.Users = []string{}
	}
	if !expansion.open(n.Key) {
		return group
	}
	group.Expanded = true
	limit := expansion.limit(n.Key)
	group.Children = []connItemJSON{}
	for _, child := range n.Children[:min(limit, len(n.Children))] {
		if child.group() {
			group.Children = append(group.Children, connItemJSON{Group: connGroupOutput(child, expansion)})
		} else {
			row := connRowOutput(child.Row)
			group.Children = append(group.Children, connItemJSON{Row: &row})
		}
	}
	group.Remaining = max(0, len(n.Children)-limit)
	return group
}

// connQuery is a query of GET /api/v1/connections.
type connQuery struct {
	State  string
	Search string
	// Filter is the chosen values of each facet: a row passes a facet
	// with any of its values (or), and every facet (and).
	Filter map[connFacet][]string
	// PID keeps the connections of one process (pid=, hidden from the
	// filter pane); 0 keeps all.
	PID int
	// App keeps the connections of every process inside an App bundle
	// (app=, its path; hidden from the filter pane); "" keeps all.
	App string
	// Rule keeps the connections of one route table rule, by its hit key
	// (ruleHitKey: the rule's ID, "final:<owner>" for a default outbound;
	// rule=, hidden from the filter pane); "" keeps all.
	Rule       string
	Sort       connColumn
	Descending bool
	Group      connGroupMode
	Expansion  connExpansion
	Page       int
	Limit      int
}

// connCounts are the rows of each tab.
type connCounts struct {
	All    int `json:"all"`
	Active int `json:"active"`
	// Closed are the closed connections an outbound carried, Rejected
	// those a reject rule refused.
	Closed   int `json:"closed"`
	Rejected int `json:"rejected"`
}

// add counts row in its tab and in All.
func (c *connCounts) add(row *connRow) {
	c.All++
	switch connTab(row) {
	case "rejected":
		c.Rejected++
	case "closed":
		c.Closed++
	default:
		c.Active++
	}
}

// connPageJSON is the body of GET /api/v1/connections.
type connPageJSON struct {
	Revision uint64 `json:"revision,string"`
	// Counts are the rows of each tab that pass the filters and search,
	// Totals those of the whole history.
	Counts connCounts                      `json:"counts"`
	Totals connCounts                      `json:"totals"`
	Facets map[connFacet][]connFacetOption `json:"facets"`
	// Rows is the page without grouping, Groups the page of groups.
	Rows   []connRowJSON   `json:"rows"`
	Groups []connGroupJSON `json:"groups"`
	// Total is the rows or groups of every page, Matched the connections
	// that pass the tab, filters and search.
	Total   int `json:"total"`
	Matched int `json:"matched"`
	// MatchedActive are the matched connections still open, the ones
	// closing the list would close.
	MatchedActive int `json:"matchedActive"`
	Page          int `json:"page"`
	Limit         int `json:"limit"`
	// Processes are the known processes of the page's connections, for
	// their icons.
	Processes []connProcess `json:"processes"`
}

// filterQueryRows keeps the rows of pool that pass the filters, the search
// and the process, App and rule limits of q: the connections a list shows,
// and the ones closing it closes.
func filterQueryRows(pool []*connRow, q connQuery) []*connRow {
	conds := q.conditions(false)
	var rows []*connRow
	for _, row := range pool {
		if !slices.ContainsFunc(conds, func(c connCondition) bool { return !c.pass(row) }) {
			rows = append(rows, row)
		}
	}
	return rows
}

// queryConnectionPage answers q over all, the history by creation, with
// the process metadata of the rows and the process snapshot.
func queryConnectionPage(all []*connRow, metadata map[string]processMeta, processes map[int]connProcess, q connQuery) connPageJSON {
	out := connPageJSON{Facets: map[connFacet][]connFacetOption{}, Rows: []connRowJSON{}, Groups: []connGroupJSON{}, Processes: []connProcess{}, Limit: q.Limit}
	filtered, counts, totals, facets := facetConnectionRows(all, q)
	out.Counts, out.Totals, out.Facets = counts, totals, facets
	out.Matched = len(filtered)
	for _, row := range filtered {
		if !row.Closed() && !row.Rejected() {
			out.MatchedActive++
		}
	}
	tree := buildOrderedConnectionTree(filtered, metadata, q.Group, q.Sort, q.Descending, q.State == "all")
	out.Total = len(tree)
	pages := max(1, (out.Total+q.Limit-1)/q.Limit)
	out.Page = min(max(q.Page, 0), pages-1)
	roots := tree[out.Page*q.Limit : min((out.Page+1)*q.Limit, len(tree))]
	pids := map[int]bool{}
	for _, n := range roots {
		if n.group() {
			out.Groups = append(out.Groups, *connGroupOutput(n, q.Expansion))
			for _, row := range n.Rows {
				if row.Conn.ProcessInfo != nil {
					pids[row.Conn.ProcessInfo.ProcessID] = true
				}
			}
		} else {
			out.Rows = append(out.Rows, connRowOutput(n.Row))
			if n.Row.Conn.ProcessInfo != nil {
				pids[n.Row.Conn.ProcessInfo.ProcessID] = true
			}
		}
	}
	for _, option := range out.Facets[facetProcess] {
		if option.ProcessInfo != nil {
			pids[option.ProcessInfo.ProcessID] = true
		}
	}
	// The processes of the snapshot, else as the metadata last knew them.
	known := map[int]connProcess{}
	for _, row := range filtered {
		meta, ok := metadata[row.Conn.ID]
		if !ok {
			continue
		}
		for _, p := range append(slices.Clone(meta.Ancestors), meta.Process) {
			if _, seen := known[p.PID]; !seen && pids[p.PID] {
				known[p.PID] = p
			}
		}
	}
	for pid := range pids {
		if p, ok := processes[pid]; ok && pid != 0 {
			known[pid] = p
		}
	}
	for pid, p := range known {
		if pid != 0 {
			out.Processes = append(out.Processes, p)
		}
	}
	slices.SortFunc(out.Processes, func(a, b connProcess) int { return a.PID - b.PID })
	return out
}

// pathInApp reports whether path is the App bundle app or inside it.
func pathInApp(path, app string) bool {
	app = strings.TrimSuffix(app, "/")
	return app != "" && (path == app || strings.HasPrefix(path, app+"/"))
}
