package connections

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
)

// inspectors shows the group's or the connection's detail beside the list.
func (m *connMonitor) inspectors(c *ui.Context, d *connDerived, now time.Time) {
	if m.groupDetails && m.selectedGroup != nil {
		candidate, ok := d.index[m.selectedGroup.Key]
		if ok && candidate.group() {
			m.selectedGroup = candidate
		}
		m.groupDetail(c, m.selectedGroup, !ok)
	}
	if row := m.selection(d); row != nil {
		// An open connection's duration goes on as the inspector shows it.
		if !row.Closed() {
			c.After(time.Second)
		}
		m.detail(c, d, row, now)
	}
}

// detailJSON is a connection as the inspector copies and the export
// writes it: its fields with exact totals, rates and closing time.
func detailJSON(row *Row, rates bool) map[string]any {
	raw, _ := json.Marshal(row.Conn)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	out["uplinkTotal"] = strconv.FormatInt(row.UpTotal, 10)
	out["downlinkTotal"] = strconv.FormatInt(row.DownTotal, 10)
	if rates {
		out["uplinkRate"], out["downlinkRate"] = row.UpRate, row.DownRate
	}
	out["peakUplinkRate"], out["peakDownlinkRate"] = row.PeakUp, row.PeakDown
	if row.ClosedAt != 0 {
		out["closedAt"] = row.ClosedAt
	} else {
		out["closedAt"] = nil
	}
	return out
}

func (m *connMonitor) detail(c *ui.Context, d *connDerived, row *Row, now time.Time) {
	k := kit.T(c)
	conn := &row.Conn
	state := "活动中"
	switch {
	case conn.Rejected():
		state = "已拒绝"
	case row.Closed():
		state = "已关闭"
	}
	icon := func() { m.connectionIcon(c, d, conn, 40) }
	kit.ScrollingInspector(c, hostName(conn), state+" · "+upperOr(conn.Network, "连接"), icon, nil, m.closeInspector, func() {
		if !row.Closed() {
			canTerminate := m.loaded && m.streamErr == "" && !m.paused
			kit.Button(c, "关闭此连接", kit.ButtonOptions{Variant: kit.Danger, Small: true, Icon: kit.SymTrash2, Disabled: m.busy || !canTerminate}).
				FillWidth().OnClick(func() { m.terminate(conn.ID) })
		}
		ipVersion := "—"
		switch conn.IPVersion {
		case 6:
			ipVersion = "IPv6"
		case 4:
			ipVersion = "IPv4"
		}
		p := connProcess(d, conn)
		kit.Section(c, "连接", nil, func() {
			if conn.Domain != "" {
				root := ruleactions.RootDomain(conn.Domain)
				m.linkedProperty(c, "主机", hostName(conn), "用量", "查看 "+root+" 的用量", func() {
					m.env.Navigate(env.PageUsage, env.UsageView{Group: "host", Filters: map[string]string{"rootDomain": root}})
				})
			} else {
				property(c, "主机", hostName(conn))
			}
			property(c, "IP 版本", ipVersion)
			kit.NamedProperty(c, "源地址", firstNonEmpty(conn.Source, "—"), sourceName(m.sourceNames(), conn.Source))
			property(c, "目标地址", conn.Destination)
			property(c, "入站", firstNonEmpty(conn.Inbound, "—")+" · "+firstNonEmpty(conn.InboundType, "—"))
			property(c, "网络", connectionNetworkText(conn))
			switch conn.Mitm {
			case api.MitmFailed:
				property(c, "失败原因", mitmReasonText(conn.MitmError))
			case api.MitmSkipped:
				if conn.MitmError != "" {
					property(c, "跳过原因", mitmReasonText(conn.MitmError))
				}
			}
			switch {
			case conn.HTTPRequests > 0:
				kit.LinkProperty(c, "HTTP 请求", kit.Count(int(conn.HTTPRequests)), "查看此连接的 HTTP 请求", func() {
					m.env.Navigate(env.PageConnections, env.ConnectionsView{HTTPConnection: conn.ID})
				})
			case conn.Intercepted():
				property(c, "HTTP 请求", "0")
			}
			property(c, "建立时间", formatConnectionDetailTime(int64(conn.CreatedAt), row.ClosedAt, now))
			if row.Closed() && !conn.Rejected() {
				property(c, "关闭时间", formatConnectionDateTime(row.ClosedAt))
			}
			if conn.Rejected() {
				property(c, "拒绝次数", kit.Count(int(conn.RejectCount)))
			}
		})
		kit.Section(c, "路由", nil, func() {
			m.ruleProperty(c, conn)
			if conn.Rejected() {
				property(c, "出站", "拒绝")
			} else {
				property(c, "出站", firstNonEmpty(conn.Outbound, "—")+" · "+firstNonEmpty(conn.OutboundType, "—"))
			}
			if !conn.Rejected() {
				property(c, "链路", pathDetail(m.path(conn)))
			}
		})
		kit.Section(c, "进程与用量", nil, func() {
			switch name := processName(conn); {
			case p.PID > 0 && name != "":
				kit.LinkProperty(c, "进程", name, "在进程中查看 "+name, func() { m.env.Navigate(env.PageProcesses, p.PID) })
			default:
				property(c, "进程", name)
			}
			pid, path, user := 0, "", conn.User
			if info := conn.ProcessInfo; info != nil {
				pid, path = info.ProcessID, info.ProcessPath
				user = firstNonEmpty(user, info.UserName)
			}
			if pid != 0 {
				m.pidLink(c, pid)
			} else {
				property(c, "PID", "—")
			}
			if path != "" {
				m.linkedProperty(c, "路径", path, "用量", "查看此进程的用量", func() { m.processUsage(path) })
			} else {
				property(c, "路径", path)
			}
			property(c, "用户", user)
			property(c, "上传", kit.Bytes(float64(row.UpTotal)))
			property(c, "下载", kit.Bytes(float64(row.DownTotal)))
			if !conn.Rejected() {
				property(c, "最高上传", kit.Bytes(row.PeakUp)+"/s")
				property(c, "最高下载", kit.Bytes(row.PeakDown)+"/s")
			}
		})
		target := ruleTarget(d, conn)
		kit.Button(c, "添加到规则…", kit.ButtonOptions{Small: true, Icon: kit.SymRoute, Disabled: !ruleactions.Available(m.store, target)}).
			FillWidth().PopoverMenuButton(func(menu *ui.Menu) { ruleactions.Items(menu, m.env, m.store, target) })
		label := "复制连接详情"
		if m.copied {
			label = "已复制详情"
		}
		kit.Button(c, label, kit.ButtonOptions{Small: true, Icon: kit.SymCopy}).FillWidth().OnClick(func() {
			raw, _ := json.MarshalIndent(detailJSON(row, false), "", "  ")
			c.WriteClipboard(string(raw))
			m.copied = true
		})
		ui.Text(c, "ID "+conn.ID).FontSize(10).Font(kit.MonoFont).TextColor(k.Label4).Selectable()
	})
}

// property is a key-value row showing "—" for an empty value.
func property(c *ui.Context, label, value string) {
	kit.Property(c, label, firstNonEmpty(value, "—"))
}

// ruleProperty is the Rule row: a rule of the route table, or the default
// outbound, linking to its row in Routing.
func (m *connMonitor) ruleProperty(c *ui.Context, conn *api.Connection) {
	rule := ruleText(conn)
	if conn.RuleID == "" && rule == defaultOutboundRule {
		kit.LinkProperty(c, "规则", rule, "在路由中查看默认出口", func() {
			m.env.Navigate(env.PageRoute, env.RouteRule{Owner: finalOwner(conn), Section: "route", Final: true})
		})
		return
	}
	if conn.RuleID == "" || rule == "" {
		property(c, "规则", rule)
		// An unnamed rule shows what the core reported: "#<index> …" from a
		// core that numbers rules, which the applied rule map lacks.
		if conn.CoreRule != "" && conn.CoreRule != rule {
			property(c, "核心规则", conn.CoreRule)
		} else if conn.CoreRule != "" {
			property(c, "核心规则", "未编号（核心未更新）")
		}
		return
	}
	target := env.RouteRule{ID: conn.RuleID, Owner: conn.RuleOwner, Section: conn.RuleSection}
	kit.LinkProperty(c, "规则", rule, "在路由中查看规则 "+rule, func() { m.env.Navigate(env.PageRoute, target) })
}

// defaultOutboundRule is ruleText of a connection a default outbound
// carried.
const defaultOutboundRule = "默认出口"

// finalOwner is the scope whose default outbound carried conn: the one
// its rule names, or Other Devices for route.final ("route", or no owner
// from an older record).
func finalOwner(conn *api.Connection) string {
	if owner := conn.RuleOwner; owner != "" && owner != "route" {
		return owner
	}
	return model.OtherDeviceID
}

// linkedProperty is a key-value row whose value is followed by a short
// accent link, such as Usage; hint names the link.
func (m *connMonitor) linkedProperty(c *ui.Context, label, value, link, hint string, open func()) {
	k := kit.T(c)
	kit.PropertyWith(c, label, func() {
		ui.Text(c, firstNonEmpty(value, "—")).FontSize(kit.FontTable).Selectable().Grow(1).MinWidth(0).Wrap()
		button := ui.ButtonBase(c).Label(hint).TextColor(k.AccentText).FontSize(kit.FontTable).Shrink(0)
		if button.Hovered() {
			button.Opacity(0.75)
		}
		button.Children(func() { ui.Text(c, link).SingleLine() })
		if button.Clicked() {
			open()
		}
	})
}

// pidLink is the PID row, opening the process in Processes.
func (m *connMonitor) pidLink(c *ui.Context, pid int) {
	k := kit.T(c)
	kit.PropertyWith(c, "PID", func() {
		link := ui.ButtonBase(c).Label(fmt.Sprintf("在进程中查看 PID %d", pid)).TextColor(k.AccentText).FontSize(kit.FontTable)
		if link.Hovered() {
			link.Opacity(0.75)
		}
		link.Children(func() { ui.Text(c, strconv.Itoa(pid)).FontFeatures("tnum") })
		if link.Clicked() {
			m.env.Navigate(env.PageProcesses, pid)
		}
	})
}

func (m *connMonitor) groupDetail(c *ui.Context, group *node, missing bool) {
	k := kit.T(c)
	subtitle := fmt.Sprintf("%s 条连接 · %s 个进程", kit.Count(group.Count), kit.Count(group.ProcessCount))
	icon := func() { m.icons.processIcon(c, groupIcon(group.Process), 40) }
	if domainGroupKey(group.Key) {
		icon = func() { domainGroupIcon(c, group.Key == ipGroupKey, 40) }
	}
	kit.ScrollingInspector(c, group.Label, subtitle, icon, nil, func() { m.groupDetails = false }, func() {
		if missing {
			ui.Text(c, "当前列表已无此分组 · 显示最后选中快照").FontSize(kit.FontTable).TextColor(k.Label3)
		}
		kit.Section(c, "用量汇总", nil, func() {
			property(c, "上传速度", kit.Rate(group.Totals.UpRate))
			property(c, "下载速度", kit.Rate(group.Totals.DownRate))
			property(c, "上传量", kit.Bytes(float64(group.Totals.UpTotal)))
			property(c, "下载量", kit.Bytes(float64(group.Totals.DownTotal)))
			property(c, "最早连接", formatConnectionDateTime(group.CreatedAt))
		})
		if !domainGroupKey(group.Key) {
			kit.Section(c, "进程", nil, func() {
				property(c, "名称", group.Process.Name)
				if group.Process.PID > 0 {
					m.pidLink(c, group.Process.PID)
				}
				property(c, "路径", firstNonEmpty(group.Process.AppPath, group.Process.Path))
				property(c, "关联进程", kit.Count(group.ProcessCount))
			})
		}
		kit.Section(c, "路由", nil, func() {
			ui.Text(c, connectionGroupText(group, colRule)).FontSize(kit.FontTable).TextColor(k.Label3)
		})
	})
}

// connectionsCSV writes rows as CSV of the shown columns, with a BOM.
func connectionsCSV(rows []*Row, columns []Column) []byte {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	quote := func(values []string) string {
		for i, v := range values {
			values[i] = `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
		}
		return strings.Join(values, ",")
	}
	lines := make([]string, 0, len(rows)+1)
	header := make([]string, len(columns))
	for i, col := range columns {
		header[i] = columnLabels[col]
	}
	lines = append(lines, quote(header))
	for _, row := range rows {
		values := make([]string, len(columns))
		for i, col := range columns {
			values[i] = columnText(row, col)
		}
		lines = append(lines, quote(values))
	}
	buf.WriteString(strings.Join(lines, "\r\n"))
	return buf.Bytes()
}

// connectionsJSON writes rows as JSON with their totals, rates and
// closing times.
func connectionsJSON(rows []*Row) []byte {
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		out[i] = detailJSON(row, true)
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	return raw
}

func joinComma(values []string) string { return strings.Join(values, ", ") }
