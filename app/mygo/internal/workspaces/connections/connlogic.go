package connections

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// This file holds the columns and formatting of the connection list; the
// server filters, sorts and groups it.

// Row is a connection with its rates and totals.
type Row struct {
	Conn             api.Connection
	UpRate, DownRate float64
	// PeakUp and PeakDown are the highest rates the connection had.
	PeakUp, PeakDown   float64
	UpTotal, DownTotal int64
	// ClosedAt is in Unix milliseconds, 0 while active.
	ClosedAt int64
	// CloseReason is why the server closed it, "core-restart" when the
	// core run that reported it ended.
	CloseReason string
}

// Closed reports whether the connection has closed.
func (r *Row) Closed() bool { return r.ClosedAt != 0 }

var pathSeparators = func(r rune) bool { return r == '/' || r == '\\' }

func baseName(path string) string {
	parts := strings.FieldsFunc(path, pathSeparators)
	if len(parts) == 0 || strings.HasSuffix(path, "/") || strings.HasSuffix(path, "\\") {
		return ""
	}
	return parts[len(parts)-1]
}

// appBundle is the path up to the first ".app" bundle of path, "" outside
// one.
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

// bundleName names an App bundle as the App filter lists it: "Safari"
// for /Applications/Safari.app.
func bundleName(bundle string) string { return strings.TrimSuffix(baseName(bundle), ".app") }

// appName is the App the connection's process runs in, "" outside one.
func appName(c *api.Connection) string {
	if c.ProcessInfo == nil {
		return ""
	}
	return bundleName(appBundle(c.ProcessInfo.ProcessPath))
}

// processName is the connection's process name, or its package.
func processName(c *api.Connection) string {
	if c.ProcessInfo == nil {
		return ""
	}
	if name := baseName(c.ProcessInfo.ProcessPath); name != "" {
		return name
	}
	if len(c.ProcessInfo.PackageNames) > 0 {
		return c.ProcessInfo.PackageNames[0]
	}
	return ""
}

// hostName is the connection's domain or destination.
func hostName(c *api.Connection) string {
	return firstNonEmpty(c.Domain, c.Destination, "—")
}

// rejectedLabel is a refused connection's chain: "Rejected", with the count of
// refusals merged into it.
func rejectedLabel(c *api.Connection) string {
	if c.RejectCount > 1 {
		return fmt.Sprintf("拒绝 × %d", c.RejectCount)
	}
	return "拒绝"
}

// chainName is the outbound chain in the order traffic takes it, the
// groups first and the outbound they chose last, "group → outbound";
// "Rejected" for a refused connection. The core lists it the other way.
func chainName(c *api.Connection) string {
	if c.Rejected() {
		return "拒绝"
	}
	return pathText(connectionPath(c, nil, nil))
}

// Roles of the hops of a connection's path.
const (
	hopGroup    = "group"
	hopOutbound = "outbound"
	hopDetour   = "detour"
)

// hop is an outbound a connection passes through: a group choosing, the
// outbound chosen, or a detour that outbound dials through.
type hop struct {
	Tag  string
	Role string
}

// maxDetourHops bounds a detour chain, against a loop the core refuses.
const maxDetourHops = 8

// connectionPath is the outbounds c passes through in the order traffic
// takes them: the groups and the outbound the core reported, then the
// detours that outbound dials through (detours, by tag), a detour that is
// a group followed by its chosen member (selected, by group); nil for a
// refused connection.
func connectionPath(c *api.Connection, detours, selected map[string]string) []hop {
	if c.Rejected() {
		return nil
	}
	var path []hop
	for i := len(c.ChainList) - 1; i >= 0; i-- {
		if tag := c.ChainList[i]; tag != "" {
			path = append(path, hop{Tag: tag, Role: hopGroup})
		}
	}
	if len(path) == 0 && c.Outbound != "" {
		path = append(path, hop{Tag: c.Outbound})
	}
	if len(path) == 0 {
		return nil
	}
	path[len(path)-1].Role = hopOutbound
	seen := map[string]bool{}
	for _, h := range path {
		seen[h.Tag] = true
	}
	last := path[len(path)-1].Tag
	for range maxDetourHops {
		next := detours[last]
		if next == "" || seen[next] {
			break
		}
		// A hop of a chain of several is a copy of it; it shows as the hop.
		name, _ := emit.ChainCopyHop(next)
		path = append(path, hop{Tag: name, Role: hopDetour})
		seen[next] = true
		last = next
		if member := selected[next]; member != "" && !seen[member] {
			path = append(path, hop{Tag: member, Role: hopDetour})
			seen[member] = true
			last = member
		}
	}
	return path
}

// pathText is a path as "a → b → c"; "—" for none.
func pathText(path []hop) string {
	if len(path) == 0 {
		return "—"
	}
	tags := make([]string, len(path))
	for i, h := range path {
		tags[i] = h.Tag
	}
	return strings.Join(tags, " → ")
}

// pathDetail is a path with each hop's role, for the inspector:
// "自选（策略组） → 马来西亚服务器 → 香港 01（前置代理）".
func pathDetail(path []hop) string {
	if len(path) == 0 {
		return "—"
	}
	parts := make([]string, len(path))
	for i, h := range path {
		switch h.Role {
		case hopGroup:
			parts[i] = h.Tag + "（策略组）"
		case hopDetour:
			parts[i] = h.Tag + "（前置代理）"
		default:
			parts[i] = h.Tag
		}
	}
	return strings.Join(parts, " → ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var protocolLabels = map[string]string{
	"http": "HTTP", "quic": "QUIC", "dns": "DNS", "stun": "STUN", "dtls": "DTLS",
	"ssh": "SSH", "rdp": "RDP", "ntp": "NTP", "bittorrent": "BitTorrent",
}

var portSuffix = regexp.MustCompile(`:(\d+)$`)

func destinationPort(destination string) int {
	m := portSuffix.FindStringSubmatch(destination)
	if m == nil {
		return -1
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port > 65535 {
		return -1
	}
	return port
}

// connectionProtocolLabel is the badge of a connection: the sniffed
// application protocol, else its transport.
func connectionProtocolLabel(c *api.Connection) string {
	// Decryption takes on HTTPS only, whatever the port.
	if c.Intercepted() {
		return "HTTPS"
	}
	protocol := strings.ToLower(strings.TrimSpace(c.Protocol))
	if protocol == "tls" {
		switch destinationPort(c.Destination) {
		case 443:
			return "HTTPS"
		case 853:
			return "DoT"
		}
		return "TLS"
	}
	if protocol != "" {
		if label, ok := protocolLabels[protocol]; ok {
			return label
		}
		return strings.ToUpper(protocol)
	}
	return firstNonEmpty(strings.ToUpper(c.Network), "—")
}

// Column is a column of the connection table.
type Column string

// Columns of the connection table.
const (
	colHost          Column = "host"
	colRule          Column = "rule"
	colUploadRate    Column = "uploadRate"
	colDownloadRate  Column = "downloadRate"
	colUploadTotal   Column = "uploadTotal"
	colDownloadTotal Column = "downloadTotal"
	colSource        Column = "source"
	colDestination   Column = "destination"
	colCreated       Column = "created"
	colNetwork       Column = "network"
	colInbound       Column = "inbound"
	colOutbound      Column = "outbound"
	colUser          Column = "user"
)

// allColumns lists the columns in their default order.
var allColumns = []Column{colHost, colRule, colUploadRate, colDownloadRate, colUploadTotal, colDownloadTotal,
	colSource, colDestination, colCreated, colNetwork, colInbound, colOutbound, colUser}

var columnLabels = map[Column]string{
	colHost: "主机 / 进程", colRule: "规则 / 链路", colUploadRate: "上传速度", colDownloadRate: "下载速度",
	colUploadTotal: "上传量", colDownloadTotal: "下载量", colSource: "源地址", colDestination: "目标地址",
	colCreated: "建立时间", colNetwork: "网络", colInbound: "入站", colOutbound: "出站", colUser: "用户",
}

var defaultColumns = []Column{colHost, colRule, colUploadRate, colDownloadRate, colUploadTotal, colDownloadTotal, colSource, colCreated}

// columnWidths are the default widths; Source Address fits an address with its
// port and a short device name.
var columnWidths = map[Column]float32{
	colHost: 300, colRule: 167, colUploadRate: 95, colDownloadRate: 95, colUploadTotal: 95, colDownloadTotal: 95,
	colSource: 230, colDestination: 180, colCreated: 120, colNetwork: 90, colInbound: 130, colOutbound: 130, colUser: 90,
}

func (col Column) isTotal() bool {
	return col == colUploadRate || col == colDownloadRate || col == colUploadTotal || col == colDownloadTotal
}

// descendingByDefault: times and amounts sort largest first.
func (col Column) descendingByDefault() bool { return col == colCreated || col.isTotal() }

var collator = collate.New(language.Chinese)

func collateCompare(a, b string) int { return collator.CompareString(a, b) }

func formatNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// columnText is the plain text of a column, for search, CSV and sorting.
func columnText(row *Row, key Column) string {
	c := &row.Conn
	switch key {
	case colHost:
		return hostName(c) + " " + processName(c)
	case colRule:
		return ruleText(c) + " " + chainName(c)
	case colUploadRate:
		return formatNumber(row.UpRate)
	case colDownloadRate:
		return formatNumber(row.DownRate)
	case colUploadTotal:
		return strconv.FormatInt(row.UpTotal, 10)
	case colDownloadTotal:
		return strconv.FormatInt(row.DownTotal, 10)
	case colSource:
		return c.Source
	case colDestination:
		return c.Destination
	case colCreated:
		return strconv.FormatInt(int64(c.CreatedAt), 10)
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

var hostPort = regexp.MustCompile(`^([^:]+):[0-9]+$`)

// sourceHost is the address without its port; bare IPv6 stays whole.
func sourceHost(source string) string {
	value := strings.TrimSpace(source)
	if strings.HasPrefix(value, "[") && strings.Contains(value, "]") {
		return value[1:strings.Index(value, "]")]
	}
	if m := hostPort.FindStringSubmatch(value); m != nil {
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

// formatConnectionTime is the Created column: "just now", "10 minutes ago",
// a time of today, or a date and time.
func formatConnectionTime(value int64, now time.Time) string {
	stamp := timestampMs(value)
	if stamp == 0 {
		return "—"
	}
	elapsed := max(0, now.UnixMilli()-stamp)
	if elapsed < 60_000 {
		return "刚刚"
	}
	if elapsed < 3_600_000 {
		return fmt.Sprintf("%d分钟前", max(1, elapsed/60_000))
	}
	now = timefmt.In(now)
	created := time.UnixMilli(stamp).In(now.Location())
	if y, m, d := created.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return created.Format("15:04:05")
	}
	return created.Format("01月02日 15:04:05")
}

// formatConnectionDateTime is a full date and time: month, day and the
// time to the second.
func formatConnectionDateTime(value int64) string {
	stamp := timestampMs(value)
	if stamp == 0 {
		return "—"
	}
	return timefmt.FormatMilli(stamp, "01月02日 15:04:05")
}

// formatConnectionDuration is how long a connection lasted or has lasted.
func formatConnectionDuration(createdAt, closedAt int64, now time.Time) string {
	created := timestampMs(createdAt)
	if created == 0 {
		return "—"
	}
	var ended time.Time
	if stamp := timestampMs(closedAt); stamp != 0 {
		ended = time.UnixMilli(stamp)
	}
	return timefmt.Duration(time.UnixMilli(created), ended, now)
}

// formatConnectionDetailTime is the inspector's Created time with its duration.
func formatConnectionDetailTime(createdAt, closedAt int64, now time.Time) string {
	return formatConnectionDateTime(createdAt) + "（" + formatConnectionDuration(createdAt, closedAt, now) + "）"
}

var ruleLabels = map[string]string{
	"rule_set": "RuleSet", "domain": "Domain", "domain_suffix": "DomainSuffix",
	"domain_keyword": "DomainKeyword", "domain_regex": "DomainRegex",
	"ip_cidr": "IP CIDR", "source_ip_cidr": "Source IP CIDR",
	"port": "Port", "port_range": "PortRange", "source_port": "SourcePort",
	"process_name": "Process", "process_path": "ProcessPath", "package_name": "Package",
	"network": "Network", "protocol": "Protocol", "inbound": "Inbound",
	"ip_version": "IPVersion", "user": "User", "user_id": "UserID",
}

var (
	ruleField   = regexp.MustCompile(`\b([a-z][a-z_0-9]*)=(\[[^\[\]]*\]|[^\s()&|]+)`)
	verbatimKey = regexp.MustCompile(`path|regex|ssid`)
)

// ruleText is the matched rule as the list and the inspector show it: the
// route table's "<number> <name>", the core's rule otherwise, and the
// default outbound for a connection no rule matched.
func ruleText(c *api.Connection) string {
	if rule := formatRule(c.Rule); rule != "" || c.Outbound == "" {
		return rule
	}
	return "默认出口"
}

// formatRule prettifies the field notation of a rule, keeping logical
// operators and unknown fields.
func formatRule(rule string) string {
	return ruleField.ReplaceAllStringFunc(rule, func(match string) string {
		m := ruleField.FindStringSubmatch(match)
		key, raw := m[1], m[2]
		label, ok := ruleLabels[key]
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
		if strings.HasPrefix(raw, "[") && !verbatimKey.MatchString(key) {
			value = strings.Join(strings.Fields(raw[1:len(raw)-1]), ", ")
		}
		return label + ": " + value
	})
}

func lower(s string) string             { return strings.ToLower(s) }
func trim(s string) string              { return strings.TrimSpace(s) }
func contains(s, sub string) bool       { return strings.Contains(s, sub) }
func upperOr(s, fallback string) string { return firstNonEmpty(strings.ToUpper(s), fallback) }

// connectionNetworkText is the Network row of the inspector: the transport
// and the protocol, with the decryption state of an HTTPS connection.
func connectionNetworkText(c *api.Connection) string {
	network := upperOr(c.Network, "—")
	switch c.Mitm {
	case api.MitmDecrypted:
		return network + " · HTTPS · 已解密"
	case api.MitmFailed:
		return network + " · HTTPS · 解密失败"
	case api.MitmPlain:
		return network + " · HTTP · 改写"
	}
	return network + " · " + firstNonEmpty(c.Protocol, "—")
}

// mitmReasonText is the "Failure reason" / "Skip reason" row of the inspector.
func mitmReasonText(reason string) string {
	return mitmpolicy.ReasonText(reason)
}
