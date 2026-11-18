package usage

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// Dimension is what usage is grouped or filtered by.
type Dimension struct {
	Value  string
	Label  string
	Plural string
}

// Dimensions are the 13 dimensions of usage, in display order.
var Dimensions = []Dimension{
	{"source", "源地址", "设备"}, {"process", "进程", "进程"},
	{"host", "完整域名", "完整域名"}, {"rootDomain", "主域名", "主域名"},
	{"outbound", "出站", "出站"}, {"chain", "出口链", "出口链"},
	{"rule", "匹配规则", "规则"},
	{"inbound", "入站", "入站"}, {"user", "入站用户", "用户"},
	{"network", "网络", "网络"}, {"protocol", "应用协议", "协议"},
	{"destination", "目标地址", "目标地址"}, {"port", "目标端口", "端口"},
}

// DimensionLabel is the singular label of a dimension, the value itself
// when unknown.
func DimensionLabel(value string) string {
	for _, d := range Dimensions {
		if d.Value == value {
			return d.Label
		}
	}
	return value
}

// Label shows a group's value: a process path shows its file name.
func Label(value, dimension string) string {
	if dimension == "process" && strings.ContainsAny(value, `/\`) {
		parts := strings.Split(strings.ReplaceAll(value, `\`, "/"), "/")
		if last := parts[len(parts)-1]; last != "" {
			return last
		}
	}
	return value
}

// Bytes formats a byte count as usageBytes: zh-CN digits, at most one
// decimal, up to PB.
func Bytes(bytes uint64) string {
	value := float64(bytes)
	unit := 0
	if value > 0 {
		unit = min(5, int(math.Floor(math.Log(value)/math.Log(1024))))
	}
	scaled := value / math.Pow(1024, float64(unit))
	digits := 1
	if unit == 0 {
		digits = 0
	}
	return groupDigits(strconv.FormatFloat(roundTo(scaled, digits), 'f', -1, 64)) + " " + []string{"B", "KB", "MB", "GB", "TB", "PB"}[unit]
}

func roundTo(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

// groupDigits adds thousands separators to the integer part of a decimal.
func groupDigits(s string) string {
	whole, frac, _ := strings.Cut(s, ".")
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	if frac != "" {
		return whole + "." + frac
	}
	return whole
}

// Number formats a count with thousands separators.
func Number(n uint64) string { return groupDigits(strconv.FormatUint(n, 10)) }

// Date formats Unix milliseconds as the zh-CN locale does, "—" when unset.
func Date(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return timefmt.FormatMilli(ms, "2006/1/2 15:04:05")
}

// Query is a usage query.
type Query struct {
	From, To  int64
	Group     string
	Secondary string
	Search    string
	// Filters is the chosen values of each dimension: a record passes a
	// dimension with any of them, and every dimension.
	Filters map[string][]string
	Sort    string
	Order   string
	Page    int
	Limit   int
	// HasPage sends page even when 0, as the console always does.
	HasPage bool
}

// Params encodes the query as usageQueryParams.
func (q Query) Params() string {
	values := url.Values{}
	values.Set("from", strconv.FormatInt(q.From, 10))
	values.Set("to", strconv.FormatInt(q.To, 10))
	values.Set("group", q.Group)
	set := func(key, value string) {
		if value != "" {
			values.Set(key, value)
		}
	}
	set("secondary", q.Secondary)
	set("search", q.Search)
	set("sort", q.Sort)
	set("order", q.Order)
	if q.HasPage || q.Page != 0 {
		values.Set("page", strconv.Itoa(q.Page))
	}
	if q.Limit != 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	keys := make([]string, 0, len(q.Filters))
	for k := range q.Filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		chosen := slices.Clone(q.Filters[k])
		slices.Sort(chosen)
		for _, v := range chosen {
			if v != "" {
				values.Add("filter."+k, v)
			}
		}
	}
	return values.Encode()
}

// ExportParams are the parameters of GET /usage/export: all details
// ignore the view's query, a summary keeps it.
func ExportParams(q Query, all bool) string {
	if all {
		return "scope=all"
	}
	return q.Params()
}

// Ranges are the time ranges of the toolbar.
var Ranges = []struct{ Value, Label string }{
	{"hour", "最近 1 小时"}, {"today", "今天"}, {"day", "最近 24 小时"}, {"week", "最近 7 天"},
	{"month", "最近 30 天"}, {"all", "全部历史"}, {"custom", "自定义"},
}

// invalidTime marks an unparsable custom time.
const invalidTime = math.MinInt64

// TimeRange returns the range's bounds in Unix milliseconds at now; a
// custom bound that does not parse is invalidTime.
func TimeRange(rng, start, end string, now time.Time) (from, to int64) {
	to = now.UnixMilli()
	switch rng {
	case "custom":
		return parseLocal(start), parseLocal(end)
	case "all":
		return 0, to
	case "today":
		y, m, d := timefmt.In(now).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, timefmt.Location()).UnixMilli(), to
	}
	durations := map[string]int64{"hour": 3600000, "day": 86400000, "week": 604800000, "month": 2592000000}
	duration, ok := durations[rng]
	if !ok {
		duration = 3600000
	}
	return to - duration, to
}

// ValidRange reports whether the bounds parse and end after they start.
func ValidRange(from, to int64) bool {
	return from != invalidTime && to != invalidTime && to > from
}

// localLayout is the datetime-local format.
const localLayout = "2006-01-02T15:04"

func parseLocal(s string) int64 {
	t, err := time.ParseInLocation(localLayout, strings.TrimSpace(s), timefmt.Location())
	if err != nil {
		return invalidTime
	}
	return t.UnixMilli()
}

// LocalDateTime formats Unix milliseconds for the custom range fields.
func LocalDateTime(ms int64) string { return timefmt.FormatMilli(ms, localLayout) }

// PreferredDetail is the cross-detail dimension the inspector starts
// with: registrable domains first, never the shown dimensions.
func PreferredDetail(group, secondary string) string {
	order := []string{"rootDomain", "host", "source"}
	for _, d := range Dimensions {
		order = append(order, d.Value)
	}
	for _, v := range order {
		if v != group && v != secondary {
			return v
		}
	}
	return ""
}

// Share returns a group's share of the totals in percent, truncated to
// two decimals.
func Share(row, totals api.UsageCounters) float64 {
	total := totals.Total()
	if total == 0 {
		return 0
	}
	return math.Floor(float64(row.Total())/float64(total)*10000) / 100
}

// Percent formats a share as "12.5%".
func Percent(share float64) string { return fmt.Sprintf("%.1f%%", share) }
