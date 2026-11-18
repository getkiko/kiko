package connections

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// disclosure returns the open state of a disclosure of the selected
// query, open at first when open.
func (m *dnsMonitor) disclosure(key string, open bool) *bool {
	if state, ok := m.disclosures[key]; ok {
		return state
	}
	state := &open
	m.disclosures[key] = state
	return state
}

func (m *dnsMonitor) inspectorView(c *ui.Context, q *api.DNSQuery) {
	subtitle := fmt.Sprintf("%s · %s · %s", dnsTypes(q, " / "), lookup(dnsStatus, q.Status), dnsDuration(q))
	actions := func() {
		label := "复制详情"
		if m.copied {
			label = "已复制"
		}
		kit.Button(c, label, kit.ButtonOptions{Small: true, Icon: kit.SymCopy}).OnClick(func() {
			c.WriteClipboard(prettyJSON(q))
			m.copied = true
		})
		kit.Button(c, "导出记录", kit.ButtonOptions{Small: true, Icon: kit.SymDownload}).OnClick(func() { m.export(nil, q) })
	}
	kit.ScrollingInspector(c.Key(q.ID), dnsDomain(q), subtitle, func() { m.queryIcon(c, q, 36) }, nil, m.closeInspector, func() {
		ui.Row(c).Gap(8).Shrink(0).Children(actions)
		if m.detailError != "" {
			kit.Notice(c, kit.DangerTone, "", m.detailError, nil)
		}
		m.queryDetails(c, q)
	})
}

func failureText(f *api.DNSFailure) string { return f.Kind + " · " + f.Stage + " · " + f.Message }

func (m *dnsMonitor) queryDetails(c *ui.Context, q *api.DNSQuery) {
	k := kit.T(c)
	muted := func(text string) {
		ui.Text(c, text).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
	}
	kit.Section(c, "查询", nil, func() {
		property(c, "查询 ID", "#"+q.ID)
		kit.NamedProperty(c, "源 IP", dnsSourceIP(q), sourceName(m.names.get(), q.Source.Address))
		questions := make([]string, len(q.Questions))
		for i, question := range q.Questions {
			questions[i] = question.Name + " " + question.Type + " " + question.Class
		}
		property(c, "问题", strings.Join(questions, "\n"))
		property(c, "解析地址", strings.Join(q.ResolvedAddresses, "\n"))
		kit.PropertyWith(c, "结果", func() { kit.Status(c, lookup(dnsStatus, q.Status), dnsTone(q)) })
		property(c, "耗时", dnsDuration(q))
		property(c, "解析器", dnsResolver(q))
		m.ruleProperty(c, q)
		if len(q.Candidates) > 1 {
			property(c, "并发服务器", strings.Join(q.Candidates, "\n"))
		}
		property(c, "缓存", dnsCache[q.Cache])
		if q.Rewritten != "" {
			kit.PropertyWith(c, "已改写", func() { kit.Badge(c, q.Rewritten, kit.AccentTone) })
		}
		property(c, "开始时间", dnsTime(q.StartedAt))
		property(c, "完成时间", dnsTime(q.FinishedAt))
	})
	if q.Error != nil {
		kit.Notice(c, kit.DangerTone, "", failureText(q.Error), nil)
	}
	if q.Incomplete {
		kit.Notice(c, kit.WarningTone, "", "此查询的采集详情达到预算上限", nil)
	}
	if len(q.Scripts) > 0 {
		scriptCount := func() { ui.Text(c, fmt.Sprint(len(q.Scripts))).FontSize(kit.FontCaption).TextColor(k.Label3) }
		kit.Section(c, "脚本", scriptCount, func() {
			for i, run := range q.Scripts {
				if i > 0 {
					kit.Separator(c).Margin(4, 0)
				}
				property(c, "脚本", run.Tag)
				kit.PropertyWith(c, "结果", func() {
					kit.Status(c, lookup(dnsScriptOutcome, run.Outcome), dnsScriptTone(run.Outcome))
				})
				if run.Detail != "" {
					property(c, "详情", run.Detail)
				}
				if run.Cached {
					property(c, "缓存", "命中")
				}
				if run.DurationMS > 0 {
					property(c, "耗时", strconv.FormatFloat(run.DurationMS, 'f', -1, 64)+" ms")
				}
			}
		})
	}
	count := func() { ui.Text(c, fmt.Sprint(len(q.Attempts))).FontSize(kit.FontCaption).TextColor(k.Label3) }
	kit.Section(c, "上游尝试", count, func() {
		for i := range q.Attempts {
			m.attempt(c, &q.Attempts[i], i)
		}
		if len(q.Attempts) == 0 {
			switch {
			case q.Role == "lookup_operation":
				muted("上游请求记录于关联的 A/AAAA 子查询")
			case q.Status == "pending":
				muted("等待上游尝试记录")
			default:
				muted("缓存或本地策略处理结果")
			}
		}
	})
	kit.Section(c, "返回给调用方的响应", nil, func() { dnsMessage(c, q.Response) })
	role := lookup(dnsRole, q.Role)
	kit.Disclosure(c, "来源与进程 · "+firstNonEmpty(q.Source.Process, role), m.disclosure("source", false), func() {
		property(c, "类型", role)
		kit.NamedProperty(c, "源地址", firstNonEmpty(q.Source.Address, "核心内部"), sourceName(m.names.get(), q.Source.Address))
		if q.Source.Destination != "" {
			property(c, "目标地址", q.Source.Destination)
		}
		if q.Source.Network != "" {
			property(c, "客户端协议", strings.ToUpper(q.Source.Network))
		}
		property(c, "入站", q.Source.Inbound)
		process := firstNonEmpty(q.Source.Process, "—")
		if q.Source.PID != 0 {
			process += fmt.Sprintf(" · %d", q.Source.PID)
		}
		property(c, "进程", process)
		if q.Source.ProcessPath != "" {
			property(c, "进程路径", q.Source.ProcessPath)
		}
		if q.Source.User != "" {
			property(c, "用户", q.Source.User)
		}
		property(c, "客户端写回", firstNonEmpty(dnsDelivery[q.Delivery], "交付观测待回传"))
		var related []string
		if q.ParentID != "" {
			related = append(related, "父查询 "+q.ParentID)
		}
		if q.OperationID != "" {
			related = append(related, "操作 "+q.OperationID)
		}
		if q.SharedWith != "" {
			related = append(related, "共享查询 "+q.SharedWith)
		}
		if len(related) > 0 {
			property(c, "关联", strings.Join(related, "\n"))
		}
		if q.DeliveryError != nil {
			kit.Notice(c, kit.DangerTone, "", q.DeliveryError.Message, nil)
		}
	})
	if len(q.Policy) > 0 {
		kit.Disclosure(c, "规则与策略", m.disclosure("policy", false), func() {
			for _, line := range q.Policy {
				ui.Text(c, line).FontSize(kit.FontCaption).Wrap().Selectable()
			}
		})
	}
	kit.Disclosure(c, "请求报文", m.disclosure("request", false), func() { dnsMessage(c, q.Request) })
	kit.Disclosure(c, "原始结构化记录", m.disclosure("raw", false), func() { kit.CodeBlock(c, prettyJSON(q)) })
}

func (m *dnsMonitor) attempt(c *ui.Context, a *api.DNSAttempt, index int) {
	reuse := "新建连接"
	if a.Reused {
		reuse = "复用连接"
	}
	title := fmt.Sprintf("%d · %s · %s · %s", index+1, strings.ToUpper(a.Protocol), firstNonEmpty(a.Endpoint, a.Resolver), reuse)
	kit.Disclosure(c.Key("attempt"+a.ID), title, m.disclosure("attempt:"+a.ID, true), func() {
		conn := a.Connection
		if conn == nil {
			conn = &api.DNSConnection{}
		}
		property(c, "解析器", a.Resolver)
		property(c, "端点", firstNonEmpty(a.Endpoint, conn.Configured))
		network := "本地处理"
		if a.NetworkUsed {
			network = "已访问网络"
		}
		property(c, "传输", a.Transport+" · "+network)
		property(c, "出口链", firstNonEmpty(strings.Join(conn.Outbound, " → "), "未知"))
		connID := "关联未知"
		if conn.ID != "" {
			connID = "#" + conn.ID
		}
		property(c, "连接 ID", connID)
		property(c, "本地地址", conn.Local)
		property(c, "对端地址", conn.Peer)
		if conn.CreatedAt != "" {
			property(c, "建连时间", dnsTime(conn.CreatedAt))
		}
		property(c, "加密", uniqueJoin([]string{conn.TLSVersion, conn.ServerName, conn.ALPN}, " · "))
		if a.HTTPVersion != "" {
			status := "—"
			if a.HTTPStatus != 0 {
				status = fmt.Sprint(a.HTTPStatus)
			}
			property(c, "HTTP", a.HTTPVersion+" · "+status)
		}
		wire := fmt.Sprint(a.WireID)
		if a.StreamID != "" {
			wire += " / " + a.StreamID
		}
		property(c, "wire ID / 流", wire)
		property(c, "开始 / 发送", dnsTime(a.StartedAt)+" / "+dnsTime(a.SentAt))
		property(c, "首字节", dnsTime(a.FirstByteAt))
		property(c, "完成时间", dnsTime(a.FinishedAt))
		var evidence []string
		for _, v := range []string{a.Visibility, conn.OutboundEvidence, conn.PeerEvidence} {
			if v != "" {
				evidence = append(evidence, lookup(dnsEvidence, v))
			}
		}
		property(c, "观测证据", strings.Join(evidence, " · "))
		if a.Error != nil {
			kit.Notice(c, kit.DangerTone, "", failureText(a.Error), nil)
		}
		for _, diagnostic := range a.Diagnostics {
			kit.Notice(c, kit.Neutral, "", lookup(dnsDiagnostics, diagnostic), nil)
		}
		kit.Disclosure(c, "上游响应", m.disclosure("response:"+a.ID, false), func() { dnsMessage(c, a.Response) })
	})
}

// dnsMessage shows a DNS message: its header, questions and records.
func dnsMessage(c *ui.Context, message *api.DNSMessage) {
	k := kit.T(c)
	if message == nil {
		ui.Text(c, "等待响应记录").FontSize(kit.FontCaption).TextColor(k.Label3)
		return
	}
	ui.Column(c).Gap(4).Children(func() {
		meta := fmt.Sprintf("%s · ID %d · %d B", message.Rcode, message.ID, message.Size)
		if len(message.Flags) > 0 {
			meta += " · " + strings.Join(message.Flags, " ")
		}
		ui.Text(c, meta).FontSize(10).TextColor(k.Label3).Wrap().Selectable()
		if message.TruncatedCapture {
			kit.Notice(c, kit.WarningTone, "", "详情已按采集预算截取", nil)
		}
		for _, q := range message.Questions {
			ui.Text(c, q.Name+" "+q.Type+" "+q.Class).FontSize(kit.FontCaption).Wrap().Selectable()
		}
		dnsRecords(c, "Answer", message.Answer)
		dnsRecords(c, "Authority", message.Authority)
		dnsRecords(c, "Additional / EDNS", message.Additional)
	})
}

func dnsRecords(c *ui.Context, title string, records []api.DNSRecord) {
	k := kit.T(c)
	ui.Column(c).Margin(10, 0, 0).Children(func() {
		ui.Row(c).Margin(0, 0, 3).Children(func() {
			ui.Text(c, title).FontSize(10).FontWeight(600).TextColor(k.Label3).Grow(1)
			ui.Text(c, fmt.Sprint(len(records))).FontSize(10).TextColor(k.Label3)
		})
		if len(records) == 0 {
			ui.Text(c, "空").FontSize(kit.FontCaption).TextColor(k.Label3)
		}
		for _, rr := range records {
			ui.Column(c).PaddingY(7).BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
				ui.Row(c).Gap(7).Children(func() {
					kit.Badge(c, rr.Type, kit.Neutral)
					ui.Text(c, fmt.Sprintf("TTL %ds", rr.TTL)).FontSize(10).TextColor(k.Label3).FontFeatures("tnum")
				})
				ui.Text(c, rr.Data).Font(kit.MonoFont).FontSize(10).TextColor(k.Label2).Wrap().Selectable().Margin(5, 0, 0)
			})
		}
	})
}
