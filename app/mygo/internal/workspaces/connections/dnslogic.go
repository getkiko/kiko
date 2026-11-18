package connections

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// This file holds the DNS query history kept from pages and the live
// stream, and its labels.

// dnsLimit is how many queries the console keeps.
const dnsLimit = 5000

// dnsState is the DNS history of one core instance.
type dnsState struct {
	instance string
	seq      api.Uint64
	queries  map[string]api.DNSQuery
	stats    api.DNSStats
	gap      bool
}

func newDNSState() *dnsState { return &dnsState{queries: map[string]api.DNSQuery{}} }

func (s *dnsState) clone() *dnsState {
	next := *s
	next.queries = maps.Clone(s.queries)
	return &next
}

// apply merges a view, as applyDNSView: a reset or another instance
// replaces the history, a stale cursor is ignored and older revisions
// never replace newer ones. It reports whether the state changed.
func (s *dnsState) apply(view api.DNSView, limit int) bool {
	reset := view.Reset || s.instance != view.Instance
	if !reset && view.Seq < s.seq {
		return false
	}
	if reset {
		s.queries = map[string]api.DNSQuery{}
	}
	for _, q := range view.Queries {
		if old, ok := s.queries[q.ID]; !ok || q.Revision >= old.Revision {
			s.queries[q.ID] = q
		}
	}
	if len(s.queries) > limit {
		newest := slices.Collect(maps.Values(s.queries))
		slices.SortFunc(newest, func(a, b api.DNSQuery) int { return parseTime(b.StartedAt).Compare(parseTime(a.StartedAt)) })
		keep := map[string]bool{}
		for _, q := range newest[:limit] {
			keep[q.ID] = true
		}
		for id := range s.queries {
			if !keep[id] {
				delete(s.queries, id)
			}
		}
	}
	s.gap = view.Gap || (!reset && s.gap)
	s.instance, s.seq, s.stats = view.Instance, view.Seq, view.Stats
	return true
}

// mergeHistory adds an older page of the same instance, as
// mergeDNSHistory, keeping the cursor and the statistics.
func (s *dnsState) mergeHistory(view api.DNSView) {
	if s.instance != view.Instance {
		return
	}
	view.Seq, view.Stats, view.Reset, view.Gap = s.seq, s.stats, false, false
	s.apply(view, dnsLimit)
}

// oldestSeq is the cursor before which older queries load.
func (s *dnsState) oldestSeq() string {
	var oldest api.Uint64
	first := true
	for _, q := range s.queries {
		if first || q.Seq < oldest {
			oldest, first = q.Seq, false
		}
	}
	return oldest.String()
}

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

var dnsStatus = map[string]string{
	"pending": "查询中", "success": "成功", "negative": "DNS 负响应", "error": "错误", "timeout": "超时",
	"canceled": "取消", "rejected": "策略拒绝", "dropped": "策略丢弃",
}

var dnsCache = map[string]string{
	"hit": "缓存命中", "stale": "过期缓存", "miss": "缓存未命中", "disabled": "禁用缓存", "coalesced": "合并等待",
}

var dnsScriptOutcome = map[string]string{
	"answer": "返回应答", "server": "转发到服务器", "pass": "继续下一条", "reject": "拒绝", "error": "出错", "maybe": "可能",
}

func dnsScriptTone(outcome string) kit.Tone {
	switch outcome {
	case "error", "reject":
		return kit.DangerTone
	case "maybe":
		return kit.WarningTone
	}
	return kit.Neutral
}

var dnsRole = map[string]string{
	"query": "客户端查询", "lookup_operation": "域名解析", "lookup": "内部解析", "bootstrap": "引导解析",
	"background_refresh": "后台刷新", "reuse_probe": "复用探测", "malformed_request": "无效请求",
}

var dnsEvidence = map[string]string{
	"observed": "实际观测", "configured_direct": "直连配置", "logical_socket": "逻辑套接字地址",
	"observed_socket": "实际套接字地址", "unknown": "未知", "local": "本地结果",
	"platform_managed": "系统负责网络访问", "transport_managed": "解析器内部管理",
}

var dnsDelivery = map[string]string{"submitted": "已提交到客户端连接", "submitted_truncated": "已截断并提交", "error": "写回失败"}

var dnsDiagnostics = map[string]string{
	"no_matching_response_before_deadline": "截止时间内未收到对应 DNS 响应",
	"invalid_response_discarded":           "已丢弃无效 DNS 回包",
	"udp_truncated_fallback":               "UDP 响应截断，继续通过 TCP 查询",
	"h2_alpn_fallback_to_http_1_1":         "服务器协商回退到 HTTP/1.1",
}

func lookup(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return key
}

func uniqueJoin(values []string, sep string) string {
	var seen []string
	for _, v := range values {
		if v != "" && !slices.Contains(seen, v) {
			seen = append(seen, v)
		}
	}
	return strings.Join(seen, sep)
}

// dnsProtocol is the transport path of a query, "UDP → TCP", or where its
// answer came from.
func dnsProtocol(q *api.DNSQuery) string {
	protocols := make([]string, len(q.Attempts))
	for i, a := range q.Attempts {
		protocols[i] = strings.ToUpper(a.Protocol)
	}
	sep := " → "
	if len(q.Candidates) > 1 {
		sep = " / "
	}
	if s := uniqueJoin(protocols, sep); s != "" {
		return s
	}
	if dnsCached(q) {
		return "缓存"
	}
	return firstNonEmpty(strings.ToUpper(q.Transport), "本地策略")
}

// dnsResolver is the server that answered; a concurrent query without one
// (a cached answer, every server failing) lists the servers it asked.
func dnsResolver(q *api.DNSQuery) string {
	if len(q.Candidates) > 1 && !slices.Contains(q.Candidates, q.Resolver) {
		return strings.Join(q.Candidates, "、")
	}
	return q.Resolver
}

// dnsOutbound is the outbound chains of the query's attempts.
func dnsOutbound(q *api.DNSQuery) string {
	var chains []string
	for _, a := range q.Attempts {
		if a.Connection != nil {
			chains = append(chains, strings.Join(a.Connection.Outbound, " → "))
		}
	}
	return firstNonEmpty(uniqueJoin(chains, " / "), "—")
}

// dnsSourceIP groups all source ports of an address together.
func dnsSourceIP(q *api.DNSQuery) string {
	return firstNonEmpty(sourceHost(q.Source.Address), "核心内部")
}

func dnsDomain(q *api.DNSQuery) string {
	names := make([]string, len(q.Questions))
	for i, question := range q.Questions {
		names[i] = question.Name
	}
	return firstNonEmpty(strings.Join(names, ", "), "无效 DNS 报文")
}

func dnsCached(q *api.DNSQuery) bool { return q.Cache == "hit" || q.Cache == "stale" }

func dnsFailed(q *api.DNSQuery) bool {
	return slices.Contains([]string{"negative", "error", "timeout", "rejected", "dropped", "canceled"}, q.Status)
}
