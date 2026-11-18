package connections

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// dnsTime is a time of day with milliseconds, "12:00:01.250".
func dnsTime(value string) string {
	if value == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "—"
	}
	return timefmt.Format(t, "15:04:05.000")
}

func dnsDuration(q *api.DNSQuery) string {
	if q.FinishedAt == "" {
		return "等待响应"
	}
	return fmt.Sprintf("%.1f ms", q.DurationMS)
}

func dnsTone(q *api.DNSQuery) kit.Tone {
	switch q.Status {
	case "pending":
		return kit.AccentTone
	case "success":
		return kit.SuccessTone
	case "canceled":
		return kit.Neutral
	case "negative":
		return kit.WarningTone
	}
	return kit.DangerTone
}

func dnsTypes(q *api.DNSQuery, sep string) string {
	types := make([]string, len(q.Questions))
	for i, question := range q.Questions {
		types[i] = question.Type
	}
	return strings.Join(types, sep)
}

func (m *dnsMonitor) view(c *ui.Context) {
	width, _ := c.Size()
	narrow := width <= narrowWidth
	if m.sidebar == nil {
		open := width >= 1000
		m.sidebar = &open
	}
	if narrow && *m.sidebar && m.selected != "" && m.inspector {
		*m.sidebar = false
	}
	m.freeze.Begin()
	d := m.freeze.Hold(m.derive)
	state := m.displayed()
	pages := max(1, (len(d.rows)+dnsPageSize-1)/dnsPageSize)
	if m.groups.grouped() {
		pages = m.groupPages(d)
	}
	m.page = min(m.page, pages-1)
	pageRows := d.rows[m.page*dnsPageSize : min((m.page+1)*dnsPageSize, len(d.rows))]
	selected := m.selectedQuery(state)
	if selected != nil && m.inspector && m.active && m.detailFor != selected.ID+"\x00"+state.instance {
		m.detailFor = selected.ID + "\x00" + state.instance
		m.loadDetail(selected.ID, state.instance)
	}
	if selected == nil || !m.inspector {
		m.detailFor = ""
	}
	group := m.selectedGroup()
	filtered := m.search != "" || m.mode != dnsModeAll || len(m.filter) > 0
	showFilters := *m.sidebar && m.active && (len(d.available) > 0 || len(m.filter) > 0)
	if narrow && group != nil {
		showFilters = false
	}

	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		m.toolbar(c, d, selected)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if showFilters && !narrow {
				m.filterPane(c, d, filtered)
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				m.notice(c, state)
				m.list(c, d, pageRows, filtered)
			})
			if selected != nil && m.inspector && m.active && !narrow {
				m.inspectorView(c, selected)
			} else if group != nil && !narrow {
				m.groupInspector(c, group)
			}
			if showFilters && !narrow {
				width, lo, hi := kit.FilterPaneSize(c, kit.FilterConnection, &m.filterWidth)
				if kit.ResizeHandle(c, &m.filterWidth, width, lo, hi, kit.FilterResizeLabel) {
					m.env.Prefs.Save(prefDNSFilterWidth, m.filterWidth.Width)
				}
			}
		})
		m.statusBar(c, d, state, pages)
	})
	if narrow {
		if showFilters {
			sidePanel(c, false, func() { *m.sidebar = false }, func() { m.filterPane(c, d, filtered) })
		}
		if selected != nil && m.inspector && m.active {
			sidePanel(c, true, m.closeInspector, func() { m.inspectorView(c, selected) })
		} else if group != nil {
			sidePanel(c, true, m.closeInspector, func() { m.groupInspector(c, group) })
		}
	}
}

// selectedQuery is the selected query: its detail once loaded, else its
// summary.
func (m *dnsMonitor) selectedQuery(state *dnsState) *api.DNSQuery {
	if m.selected == "" {
		return nil
	}
	if m.detail != nil && m.detail.ID == m.selected {
		return m.detail
	}
	if q, ok := state.queries[m.selected]; ok {
		return &q
	}
	return nil
}

func (m *dnsMonitor) closeInspector() { m.inspector = false }

func (m *dnsMonitor) selectQuery(id string) {
	if id != m.selected {
		m.disclosures = map[string]*bool{}
		m.copied = false
	}
	m.selected, m.inspector = id, true
}

func (m *dnsMonitor) toolbar(c *ui.Context, d *dnsDerived, selected *api.DNSQuery) {
	stats := m.displayed().stats
	subtitle := fmt.Sprintf("累计 %s · 查询中 %s · 缓存 %s", countUint(stats.Total), kit.Count(stats.Pending), countUint(stats.CacheHits))
	m.header(c, subtitle, func() {
		seg := kit.Segmented(c, "dns-mode", &m.mode,
			kit.Segment{Label: "全部", Count: kit.Count(d.all)},
			kit.Segment{Label: "异常", Count: kit.Count(d.errors)},
			kit.Segment{Label: "缓存", Count: kit.Count(d.cached)})
		seg.Label("DNS 记录范围")
		if seg.Changed() {
			m.page = 0
		}
		if kit.Search(c, "dns-search", &m.search, kit.SearchOptions{Placeholder: "搜索域名、进程、地址、出口…", Toolbar: true, Width: 260}).Changed() {
			m.page = 0
		}
	}, func() {
		kit.ToolbarGroup(c, func() {
			hasFacets := len(d.available) > 0 || len(m.filter) > 0
			kit.IconButton(c, kit.SymSlidersHorizontal, "切换 DNS 筛选", kit.IconButtonOptions{Active: *m.sidebar && hasFacets, Disabled: !hasFacets}).
				OnClick(func() { *m.sidebar = !*m.sidebar; m.env.Prefs.Save(prefDNSSidebar, *m.sidebar) })
			kit.IconButton(c, kit.SymList, "切换 DNS 列表密度", kit.IconButtonOptions{Active: m.detailed}).OnClick(func() { m.detailed = !m.detailed; m.env.Prefs.Save(prefDNSDetailed, m.detailed) })
			m.groupingButton(c)
			pause, label := kit.SymPause, "暂停 DNS 更新"
			if m.paused {
				pause, label = kit.SymPlay, "恢复 DNS 更新"
			}
			kit.IconButton(c, pause, label, kit.IconButtonOptions{Active: m.paused}).OnClick(m.togglePause)
			kit.IconButton(c, kit.SymPanelRight, "切换 DNS 详情", kit.IconButtonOptions{Active: (selected != nil && m.inspector) || m.selectedGroup() != nil, Disabled: m.selected == "" && len(d.rows) == 0}).
				OnClick(func() {
					if m.selected == "" && (!m.groups.grouped() || m.groups.Selected == "") && len(d.rows) > 0 {
						m.selectQuery(d.rows[0].ID)
					} else {
						m.inspector = !m.inspector
					}
				})
			// Export and clearing share the More menu; the confirmation
			// anchors to it.
			enabled := m.cap != nil && m.cap.Enabled
			more := kit.IconButton(c, kit.SymEllipsis, "更多 DNS 操作", kit.IconButtonOptions{})
			more.PopoverMenuButton(func(menu *ui.Menu) {
				if menu.Item("导出 DNS 查询").Disabled(len(d.rows) == 0).Chosen() {
					m.export(d.rows, nil)
				}
				menu.Separator()
				if menu.Item("清空 DNS 历史").Disabled(!enabled).Chosen() {
					m.confirm.Open = true
				}
			})
			kit.ConfirmPopover(c, more, &m.confirm, kit.ConfirmOptions{
				Title:   "清空 DNS 查询历史？",
				Message: "已完成的查询记录将从历史中移除。进行中的查询会继续显示，累计统计继续保留。",
				Confirm: "清空历史", Danger: true, Run: m.clear,
			})
		})
	})
}

func countUint(v api.Uint64) string {
	if v > 1<<62 {
		return v.String()
	}
	return kit.Count(int(v))
}

func (m *dnsMonitor) resetFilters() {
	m.filter, m.search, m.mode, m.page = kit.FacetChoice[dnsFacet]{}, "", dnsModeAll, 0
}

func (m *dnsMonitor) filterPane(c *ui.Context, d *dnsDerived, filtered bool) {
	width, _, _ := kit.FilterPaneSize(c, kit.FilterConnection, &m.filterWidth)
	names := m.names.get()
	kit.SizedFilterPane(c, kit.FilterConnection, width, filtered, m.resetFilters, func() {
		for _, f := range dnsFacets {
			options := withSelected(d.facets[f.key], m.filter[f.key])
			if len(options) == 0 {
				continue
			}
			kit.FacetGroup(c, f.label, m.facets.Of(string(f.key)), len(options),
				func(i int) string {
					switch f.key {
					case "status":
						return lookup(dnsStatus, options[i].value)
					case "source":
						return names.Text(options[i].value)
					}
					return options[i].value
				},
				func(i int) bool { return m.filter.Has(f.key, options[i].value) },
				func(i int) {
					option := options[i]
					label := option.value
					if f.key == "status" {
						label = lookup(dnsStatus, option.value)
					}
					key := c.Key(string(f.key) + "\x00" + option.value)
					var o ui.Element
					if f.key == "source" {
						o = kit.FilterOptionNamed(key, kit.FilterConnection, m.filter.Has(f.key, option.value), label, names.Name(option.value), optionCount(option.count))
					} else {
						o = kit.FilterOption(key, kit.FilterConnection, m.filter.Has(f.key, option.value), nil, label, option.count)
					}
					if o.Clicked() {
						m.filter.Toggle(f.key, option.value)
						m.page = 0
					}
				})
		}
	})
}

func (m *dnsMonitor) notice(c *ui.Context, state *dnsState) {
	enabled := m.cap != nil && m.cap.Enabled
	if enabled && m.err == "" && !state.gap && state.stats.Dropped == 0 {
		return
	}
	text := m.err
	if text == "" && m.cap != nil {
		text = m.cap.Reason
	}
	if text == "" && !enabled {
		text = "等待核心启动 DNS 监控"
	}
	if state.gap {
		text += " · 历史有淘汰，已恢复最近快照"
	}
	if state.stats.Dropped != 0 {
		text += " · 采集丢弃 " + countUint(state.stats.Dropped) + " 条"
	}
	tone := kit.Neutral
	if m.err != "" {
		tone = kit.DangerTone
	}
	ui.Row(c).Padding(8, 12).Shrink(0).Children(func() { kit.Notice(c, tone, "", strings.TrimPrefix(text, " · "), nil).Grow(1) })
}

var dnsColumns = []kit.Column{
	{ID: "domain", Title: "域名", MinWidth: 220, Grow: 2, Sortable: true},
	{ID: "type", Title: "类型", Width: 53},
	{ID: "status", Title: "结果", Width: 87, Sortable: true},
	{ID: "resolver", Title: "解析器", Width: 140, Sortable: true},
	{ID: "protocol", Title: "协议", Width: 116},
	{ID: "outbound", Title: "出口链", Width: 175, Sortable: true},
	{ID: "addresses", Title: "解析地址", Width: 146},
	{ID: "duration", Title: "耗时", Width: 90, Align: ui.End},
	{ID: "started_at", Title: "查询时间", Width: 114, Sortable: true},
	{ID: "source", Title: "来源", Width: 155},
}

func (m *dnsMonitor) list(c *ui.Context, d *dnsDerived, rows []*api.DNSQuery, filtered bool) {
	if m.page != m.lastPage {
		m.lastPage = m.page
		m.table.List.ScrollTo(0, ui.Start)
	}
	if len(d.rows) == 0 {
		title, description := "尚无 DNS 查询", "启动核心后在此查看解析请求。"
		switch {
		case len(d.queries) > 0:
			title, description = "没有符合筛选条件的查询", "调整搜索或左侧筛选查看记录。"
		case m.cap != nil && m.cap.Enabled:
			description = "新的解析请求会实时显示在这里。"
		}
		var action func()
		if filtered {
			action = func() { kit.Button(c, "清除筛选", kit.ButtonOptions{Small: true}).OnClick(m.resetFilters) }
		}
		kit.EmptyState(c, kit.SymFileSearch, title, description, action)
		return
	}
	if m.groups.grouped() {
		m.groupedList(c, d)
		return
	}
	density := kit.Compact
	if m.detailed {
		density = kit.TwoLine
	}
	m.table.Selected = -1
	for i, q := range rows {
		if q.ID == m.selected && m.inspector {
			m.table.Selected = i
		}
	}
	list := kit.Table(c, &m.table, dnsColumns, len(rows), kit.TableOptions{
		Resizable: true, OnResize: func() { m.env.Prefs.Save(prefDNSColumnWidths, m.table.Widths) },
		Label: "DNS 查询记录", Density: density, Striped: true, KeepOffset: true,
		Key:     func(row int) any { return rows[row].ID },
		Menu:    m.freeze.Menu(func(menu *ui.Menu, row int) { m.exitTests(menu, rows[row]) }),
		HasMenu: func(row int) bool { return m.exitTest(rows[row]) != "" },
	}, func(row, col int) { m.cell(c, rows[row], dnsColumns[col].ID) })
	m.freeze.End(c)
	if m.table.Sort.Column != m.sortCol {
		m.sortCol = m.table.Sort.Column
		m.table.Sort.Descending = m.sortCol == "started_at"
		m.page = 0
	}
	if list.Changed() {
		if i, ok := m.table.Chosen(len(rows)); ok {
			m.selectQuery(rows[i].ID)
		}
	}
	if list.Shortcut(0, ui.KeyEscape) {
		m.closeInspector()
	}
}

// dnsExitHop is the outbound query q's exit chain ends at, that of its
// latest attempt with one; "" for none.
func dnsExitHop(q *api.DNSQuery) string {
	for i := len(q.Attempts) - 1; i >= 0; i-- {
		if conn := q.Attempts[i].Connection; conn != nil {
			if hop := nodeview.LastHop(conn.Outbound...); hop != "" {
				return hop
			}
		}
	}
	return ""
}

// exitTest is the server "Test Exit" tests of query q, the one its exit
// chain ends at (dnsExitHop, nodeview.ExitTargets); "" for none.
func (m *dnsMonitor) exitTest(q *api.DNSQuery) string {
	if m.nodes == nil {
		return ""
	}
	x := m.exits.Of(m.nodes, nodeview.LiveGroups(m.env))
	final, _ := nodeview.ExitTargets(m.nodes, x, dnsExitHop(q), false)
	return final
}

// exitTests is the context menu of query q: "Test Exit", testing the server
// its exit chain ends at (dnsExitHop, nodeview.ExitTests).
func (m *dnsMonitor) exitTests(menu *ui.Menu, q *api.DNSQuery) {
	if m.nodes == nil {
		return
	}
	x := m.exits.Of(m.nodes, nodeview.LiveGroups(m.env))
	nodeview.ExitTests(menu, m.nodes, x, dnsExitHop(q), "", false, m.env.Toast)
}

// queryIcon is the process's icon of a query, or a globe.
func (m *dnsMonitor) queryIcon(c *ui.Context, q *api.DNSQuery, size float32) {
	if q.Source.ProcessPath != "" {
		m.icons.processIcon(c, iconProcess{PID: q.Source.PID, Path: q.Source.ProcessPath, Name: q.Source.Process}, size)
		return
	}
	k := kit.T(c)
	ui.Box(c).Size(size, size).Center().Shrink(0).TextColor(k.Label3).Children(func() { kit.Icon(c, kit.SymGlobe2, size*0.9) })
}

func (m *dnsMonitor) cell(c *ui.Context, q *api.DNSQuery, col string) {
	k := kit.T(c)
	secondary := func(s string) string {
		if m.detailed {
			return s
		}
		return ""
	}
	role := firstNonEmpty(q.Source.Process, lookup(dnsRole, q.Role))
	switch col {
	case "domain":
		size := float32(17)
		if m.detailed {
			size = 24
		}
		m.queryIcon(c, q, size)
		kit.Cell(c, dnsDomain(q), secondary(role))
	case "type":
		kit.Badge(c, firstNonEmpty(dnsTypes(q, "/"), "DNS"), kit.Neutral)
	case "status":
		text := lookup(dnsStatus, q.Status)
		if q.Status == "negative" {
			text = "负响应"
			if q.Response != nil && q.Response.Rcode != "" {
				text = q.Response.Rcode
			}
		}
		detail := ""
		if q.Response != nil {
			detail = q.Response.Rcode
		}
		if detail == "" && q.Error != nil {
			detail = q.Error.Stage
		}
		ui.Column(c).Gap(1).MinWidth(0).Children(func() {
			kit.Status(c, text, dnsTone(q))
			if m.detailed && detail != "" {
				ui.Text(c, detail).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine()
			}
		})
	case "resolver":
		kit.Cell(c, firstNonEmpty(dnsResolver(q), "本地策略"), secondary(firstNonEmpty(dnsCache[q.Cache], "—")))
	case "protocol":
		kit.Cell(c, dnsProtocol(q), "")
	case "outbound":
		detail := lookup(dnsRole, q.Role)
		switch {
		case len(q.Attempts) > 0:
			detail = "新建连接"
			for _, a := range q.Attempts {
				if a.Reused {
					detail = "复用连接"
				}
			}
		case dnsCached(q):
			detail = "缓存结果"
		}
		kit.Cell(c, dnsOutbound(q), secondary(detail))
	case "addresses":
		kit.Cell(c, firstNonEmpty(strings.Join(q.ResolvedAddresses, " · "), "—"), "")
	case "duration":
		text := dnsDuration(q)
		if q.Status == "pending" {
			text = "—"
		}
		kit.NumberCell(c, text).TextColor(k.Label2)
	case "started_at":
		kit.Cell(c, dnsTime(q.StartedAt), "").FontFeatures("tnum")
	case "source":
		detail := q.Source.Inbound
		if detail == "" && q.Source.PID != 0 {
			detail = fmt.Sprintf("PID %d", q.Source.PID)
		}
		kit.NamedCell(c, firstNonEmpty(q.Source.Address, "核心内部"), sourceName(m.names.get(), q.Source.Address), secondary(firstNonEmpty(detail, "—")))
	}
}

func (m *dnsMonitor) statusBar(c *ui.Context, d *dnsDerived, state *dnsState, pages int) {
	total, unit := len(d.rows), "条查询"
	if m.groups.grouped() {
		total, unit = len(m.groups.groups), "个分组"
	}
	first := 0
	if total > 0 {
		first = m.page*dnsPageSize + 1
	}
	left := fmt.Sprintf("显示 %d–%d / %s %s", first, min((m.page+1)*dnsPageSize, total), kit.Count(total), unit)
	if m.groups.grouped() {
		left += fmt.Sprintf(" · %s 条查询", kit.Count(len(d.rows)))
	}
	status := "连接中"
	switch {
	case m.paused:
		status = "已暂停显示"
	case m.connected:
		status = "实时更新"
	}
	center := fmt.Sprintf("%s · 错误 %s · 超时 %s · %.1f MB", status, countUint(state.stats.Errors), countUint(state.stats.Timeouts), state.stats.Bytes/1048576)
	kit.StatusBar(c, left, center, func() {
		ui.Row(c).Gap(8).Children(func() {
			if state.stats.Retained > len(state.queries) {
				label := "加载更早查询"
				if m.loading {
					label = "加载中…"
				}
				kit.Button(c, label, kit.ButtonOptions{Variant: kit.TextButton, Small: true, Disabled: m.loading}).OnClick(m.loadHistory)
			}
			kit.Pagination(c, &m.page, pages)
		})
	})
}

func prettyJSON(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}
