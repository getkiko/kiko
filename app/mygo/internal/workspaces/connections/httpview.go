package connections

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
)

var httpColumns = []kit.Column{
	{ID: "time", Title: "时间", Width: 104, Sortable: true},
	{ID: "method", Title: "方法", Width: 72, Sortable: true},
	{ID: "host", Title: "主机", MinWidth: 160, Grow: 2, Sortable: true},
	{ID: "path", Title: "路径", MinWidth: 200, Grow: 3, Sortable: true},
	{ID: "status", Title: "状态", Width: 80, Sortable: true},
	{ID: "type", Title: "类型", Width: 130, Sortable: true},
	{ID: "size", Title: "大小", Width: 82, Align: ui.End, Sortable: true},
	{ID: "duration", Title: "耗时", Width: 82, Align: ui.End, Sortable: true},
	{ID: "process", Title: "进程", Width: 140, Sortable: true},
	{ID: "route", Title: "规则 / 出站", Width: 150},
	{ID: "flags", Title: "标记", Width: 130},
}

// view builds the HTTP Requests monitor under the workspace's tabs.
func (m *httpMonitor) view(c *ui.Context) {
	width, _ := c.Size()
	narrow := width <= narrowWidth
	if m.sidebar == nil {
		open := width >= 1000
		m.sidebar = &open
	}
	// The focus returns to what opened the replay dialog, a frame after it
	// closed.
	m.rp.focusNow, m.rp.focusBack = m.rp.focusBack, false
	if m.rp.focusNow && !m.rp.fromInspector {
		m.refocus, m.rp.focusNow = true, false
	}
	m.freeze.Begin()
	d := m.freeze.Hold(m.derive)
	if m.stale() && !m.freeze.Frozen() {
		c.After(deriveEvery)
	}
	selected := m.inspectedRow()
	showInspector := selected != nil && m.inspector && m.active
	if m.groups.grouped() {
		m.groupLines(d)
	}
	group := m.selectedGroup()
	showFilters := *m.sidebar && m.active && (len(d.available) > 0 || len(m.filter) > 0)
	// An inspector over a narrow list hides the filters for as long as it
	// shows; the saved state stays.
	if narrow && (showInspector || group != nil) {
		showFilters = false
	}

	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		m.toolbar(c, d, selected)
		m.notices(c)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if showFilters && !narrow {
				m.filterPane(c, d)
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { m.list(c, d) })
			if showInspector && !narrow {
				m.inspectorView(c, selected)
			} else if group != nil && !narrow {
				m.groupInspector(c, group)
			}
			if showFilters && !narrow {
				width, lo, hi := kit.FilterPaneSize(c, kit.FilterConnection, &m.filterWidth)
				if kit.ResizeHandle(c, &m.filterWidth, width, lo, hi, kit.FilterResizeLabel) {
					m.env.Prefs.Save(prefHTTPFilterWidth, m.filterWidth.Width)
				}
			}
		})
		m.statusBar(c, d)
	})
	m.replayDialog(c)
	if rw := m.rwEditors(); rw != nil {
		rw.View(c)
		if rw.TakeClosed() {
			m.refocus = true
		}
	}
	if sc := m.scEditors(); sc != nil {
		sc.View(c)
		if sc.TakeClosed() {
			m.refocus = true
		}
	}
	if narrow {
		if showFilters {
			sidePanel(c, false, func() { *m.sidebar = false }, func() { m.filterPane(c, d) })
		}
		if showInspector {
			sidePanel(c, true, m.closeInspector, func() { m.inspectorView(c, selected) })
		} else if group != nil {
			sidePanel(c, true, m.closeInspector, func() { m.groupInspector(c, group) })
		}
	}
}

func (m *httpMonitor) toolbar(c *ui.Context, d *httpDerived, selected *httpRow) {
	subtitle := ""
	if m.store.total > 0 {
		subtitle = fmt.Sprintf("累计 %s", countUint(m.store.total))
	}
	m.header(c, subtitle, func() {
		seg := kit.Segmented(c, "http-mode", &m.mode,
			kit.Segment{Label: "全部", Count: kit.Count(d.all)},
			kit.Segment{Label: "异常", Count: kit.Count(d.errors)},
			kit.Segment{Label: "进行中", Count: kit.Count(d.live)})
		seg.Label("请求范围")
		kit.Search(c, "http-search", &m.search, kit.SearchOptions{Placeholder: "搜索 URL、方法、状态、进程…", Toolbar: true, Width: 260})
	}, func() {
		kit.ToolbarGroup(c, func() {
			hasFacets := len(d.available) > 0 || len(m.filter) > 0
			kit.IconButton(c, kit.SymSlidersHorizontal, "切换 HTTP 筛选", kit.IconButtonOptions{Active: *m.sidebar && hasFacets, Disabled: !hasFacets}).
				OnClick(func() { *m.sidebar = !*m.sidebar; m.env.Prefs.Save(prefHTTPSidebar, *m.sidebar) })
			kit.IconButton(c, kit.SymList, "切换 HTTP 列表密度", kit.IconButtonOptions{Active: m.detailed}).
				OnClick(func() { m.detailed = !m.detailed; m.env.Prefs.Save(prefHTTPDetailed, m.detailed) })
			m.groupingButton(c)
			pause, label := kit.SymPause, "暂停 HTTP 更新"
			if m.paused {
				pause, label = kit.SymPlay, "恢复 HTTP 更新"
			}
			kit.IconButton(c, pause, label, kit.IconButtonOptions{Active: m.paused}).OnClick(m.togglePause)
			kit.IconButton(c, kit.SymPanelRight, "切换 HTTP 详情", kit.IconButtonOptions{Active: (selected != nil && m.inspector) || m.selectedGroup() != nil, Disabled: m.selected == "" && len(d.rows) == 0}).
				OnClick(func() {
					if m.selected == "" && (!m.groups.grouped() || m.groups.Selected == "") && len(d.rows) > 0 {
						m.selectRow(d.rows[0].ID)
					} else {
						m.inspector = !m.inspector
					}
				})
			export := kit.IconButton(c, kit.SymDownload, "导出 HAR", kit.IconButtonOptions{Active: m.har.open, Disabled: len(d.rows) == 0})
			export.OnClick(func() { m.har.open = !m.har.open })
			m.exportPopover(c, export, d)
			clear := kit.IconButton(c, kit.SymTrash2, "清空 HTTP 请求", kit.IconButtonOptions{Disabled: len(m.store.rows) == 0})
			clear.OnClick(func() { m.confirm.Open = true })
			kit.ConfirmPopover(c, clear, &m.confirm, kit.ConfirmOptions{
				Title:   "清空 HTTP 请求？",
				Message: "已完成的请求将被移除，进行中的请求继续显示。",
				Confirm: "清空请求", Danger: true, Run: m.clear,
			})
		})
	})
}

// notices shows the failure of the stream, a gap in the records, and the
// connection the list is limited to.
func (m *httpMonitor) notices(c *ui.Context) {
	text := m.err
	tone := kit.DangerTone
	if text == "" && m.store.gap {
		text, tone = "部分记录已被核心淘汰，已恢复最近快照", kit.Neutral
	}
	if text != "" {
		ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
			kit.Notice(c, tone, "", text, func() {
				kit.IconButton(c, kit.SymX, "关闭提示", kit.IconButtonOptions{Size: 22, IconSize: 13}).OnClick(func() { m.err, m.store.gap = "", false })
			}).Grow(1)
		})
	}
	if m.conn != "" {
		ui.Row(c).PaddingX(16).PaddingY(6).Gap(8).Shrink(0).Children(func() {
			kit.Tag(c, "连接 "+m.conn, func() { m.showConnection("") })
		})
	}
}

func (m *httpMonitor) filterPane(c *ui.Context, d *httpDerived) {
	width, _, _ := kit.FilterPaneSize(c, kit.FilterConnection, &m.filterWidth)
	names := m.names.get()
	kit.SizedFilterPane(c, kit.FilterConnection, width, m.filtered(), m.resetFilters, func() {
		for _, f := range httpFacets {
			options := withSelected(d.facets[f.key], m.filter[f.key])
			if len(options) == 0 {
				continue
			}
			kit.FacetGroup(c, f.label, m.facets.Of(string(f.key)), len(options),
				func(i int) string {
					if f.key == hfSource {
						return names.Text(options[i].value)
					}
					return options[i].value
				},
				func(i int) bool { return m.filter.Has(f.key, options[i].value) },
				func(i int) {
					option := options[i]
					key := c.Key(string(f.key) + "\x00" + option.value)
					var o ui.Element
					if f.key == hfSource {
						o = kit.FilterOptionNamed(key, kit.FilterConnection, m.filter.Has(f.key, option.value), option.value, names.Name(option.value), optionCount(option.count))
					} else {
						o = kit.FilterOption(key, kit.FilterConnection, m.filter.Has(f.key, option.value), nil, option.value, option.count)
					}
					if o.Clicked() {
						m.filter.Toggle(f.key, option.value)
					}
				})
		}
	})
}

// empty fills the list's area while it has no rows.
func (m *httpMonitor) empty(c *ui.Context, d *httpDerived) {
	switch {
	case d.total > 0:
		kit.EmptyState(c, kit.SymFileSearch, "没有符合条件的请求", "", func() {
			kit.Button(c, "清除筛选", kit.ButtonOptions{Small: true}).OnClick(m.resetFilters)
		})
	case m.cap == nil:
		ui.Box(c).Grow(1)
	case !m.cap.Supported:
		kit.EmptyState(c, kit.SymLock, "当前核心不支持 HTTP 请求", m.cap.Reason, func() { m.openSettings(c) })
	case !m.cap.Enabled:
		kit.EmptyState(c, kit.SymLock, "HTTPS 解密未启用", m.cap.Reason, func() { m.openSettings(c) })
	default:
		kit.EmptyState(c, kit.SymGlobe, "暂无 HTTP 请求", "", nil)
	}
}

func (m *httpMonitor) openSettings(c *ui.Context) {
	kit.Button(c, "前往 HTTPS 解密设置", kit.ButtonOptions{Small: true}).OnClick(func() { m.env.Navigate(env.PageSettings, "https") })
}

func (m *httpMonitor) list(c *ui.Context, d *httpDerived) {
	if len(d.rows) == 0 {
		m.empty(c, d)
		return
	}
	if m.groups.grouped() {
		m.groupedList(c, d)
		return
	}
	rows := d.rows
	density := kit.Compact
	if m.detailed {
		density = kit.TwoLine
	}
	m.table.Selected = -1
	if m.selected != "" && m.inspector {
		for i, r := range rows {
			if r.ID == m.selected {
				m.table.Selected = i
				break
			}
		}
	}
	list := kit.Table(c, &m.table, httpColumns, len(rows), kit.TableOptions{
		Resizable: true, OnResize: func() { m.env.Prefs.Save(prefHTTPColumnWidths, m.table.Widths) },
		Label: "HTTP 请求记录", Density: density, Striped: true, KeepOffset: true,
		Key:  func(row int) any { return rows[row].ID },
		Menu: m.freeze.Menu(func(menu *ui.Menu, row int) { m.rowMenu(c, menu, rows[row]) }),
	}, func(row, col int) { m.cell(c, rows[row], httpColumns[col].ID) })
	m.freeze.End(c)
	if m.refocus {
		list.Focus()
		m.refocus = false
	}
	if m.table.Sort.Column != m.sortCol {
		m.sortCol = m.table.Sort.Column
		m.table.Sort.Descending = m.sortCol == "time"
	}
	if list.Changed() {
		if i, ok := m.table.Chosen(len(rows)); ok {
			m.selectRow(rows[i].ID)
		}
	}
	if list.Shortcut(0, ui.KeyEscape) {
		m.closeInspector()
	}
}

func (m *httpMonitor) rowMenu(c *ui.Context, menu *ui.Menu, r *httpRow) {
	copyItem(c, menu, "复制 URL", r.URL)
	if menu.Item("复制为 cURL").Chosen() {
		c.WriteClipboard(httpCurl(&r.HTTPRecord, nil))
	}
	if m.canReplay() && menu.Item("编辑并重放").Chosen() {
		m.openReplay(r, false)
	}
	menu.Separator()
	if r.Host != "" && menu.Item("只看此主机").Checked(m.filter.IsOnly(hfHost, r.Host)).Chosen() {
		m.filter.Only(hfHost, r.Host)
	}
	if conn := r.Connection; conn != nil && m.nodes != nil {
		x := m.exits.Of(m.nodes, nodeview.LiveGroups(m.env))
		nodeview.ExitTests(menu, m.nodes, x, nodeview.LastHop(conn.Outbound), "", true, m.env.Toast)
	}
	m.rewriteMenu(menu, r)
}

func (m *httpMonitor) processIcon(c *ui.Context, r *httpRow, size float32) {
	if conn := r.Connection; conn != nil && conn.ProcessPath != "" {
		m.icons.processIcon(c, iconProcess{PID: int(conn.ProcessID), Path: conn.ProcessPath, Name: conn.ProcessName}, size)
		return
	}
	k := kit.T(c)
	ui.Box(c).Size(size, size).Center().Shrink(0).TextColor(k.Label3).Children(func() { kit.Icon(c, kit.SymGlobe2, size*0.9) })
}

func (m *httpMonitor) cell(c *ui.Context, r *httpRow, col string) {
	k := kit.T(c)
	secondary := func(s string) string {
		if m.detailed {
			return s
		}
		return ""
	}
	switch col {
	case "time":
		kit.Cell(c, httpTime(r.start), secondary(r.Proto)).FontFeatures("tnum")
	case "method":
		kit.Badge(c, firstNonEmpty(r.Method, "—"), kit.Neutral)
	case "host":
		kit.Cell(c, firstNonEmpty(r.Host, "—"), secondary(r.Scheme))
	case "path":
		kit.Cell(c, firstNonEmpty(r.Path, "/"), secondary(r.URL))
	case "status":
		detail := ""
		if r.Error != "" {
			detail = r.Error
		}
		ui.Column(c).Gap(1).MinWidth(0).Children(func() {
			kit.Status(c, httpStatusText(r), httpClassTone(r.class))
			if m.detailed && detail != "" {
				kit.Clipped(c, detail, kit.FontCaption, 200).TextColor(k.Label3)
			}
		})
	case "type":
		kit.Cell(c, firstNonEmpty(r.ctype, "—"), secondary(r.ResponseEncoding))
	case "size":
		kit.NumberCell(c, httpSize(r)).TextColor(k.Label2)
	case "duration":
		kit.NumberCell(c, httpDuration(r.total)).TextColor(k.Label2)
	case "process":
		size := float32(17)
		if m.detailed {
			size = 24
		}
		m.processIcon(c, r, size)
		pid := ""
		if r.Connection != nil && r.Connection.ProcessID != 0 {
			pid = fmt.Sprintf("PID %d", r.Connection.ProcessID)
		}
		kit.Cell(c, firstNonEmpty(r.process, "—"), secondary(pid))
	case "route":
		rule := ""
		if r.Connection != nil {
			rule = r.Connection.Rule
		}
		kit.Cell(c, firstNonEmpty(r.outbound, "—"), secondary(rule))
	case "flags":
		ui.Row(c).Gap(4).MinWidth(0).Children(func() {
			for _, f := range httpFlags(r) {
				kit.Badge(c, f.label, f.tone)
			}
		})
	}
}

// httpSize is the response size, "—" without a response.
func httpSize(r *httpRow) string {
	if r.live() && r.ResponseBytes == 0 {
		return "—"
	}
	return kit.Bytes(float64(r.ResponseBytes))
}

func (m *httpMonitor) statusBar(c *ui.Context, d *httpDerived) {
	left := fmt.Sprintf("显示 %s / %s 条请求", kit.Count(len(d.rows)), kit.Count(d.total))
	if m.groups.grouped() {
		left += fmt.Sprintf(" · %s 个分组", kit.Count(len(m.groups.groups)))
	}
	status := "连接中"
	switch {
	case m.paused:
		status = "已暂停显示"
		if n := m.sincePause(); n > 0 {
			status += fmt.Sprintf(" · 新增 %s", kit.Count(n))
		}
	case m.connected:
		status = "实时更新"
	}
	center := status
	if m.store.evicted > 0 {
		center += fmt.Sprintf(" · 已淘汰 %s", kit.Count(m.store.evicted))
	}
	kit.StatusBar(c, left, strings.TrimPrefix(center, " · "), func() {
		if m.canLoadEarlier() {
			label := "加载更早请求"
			if m.loading {
				label = "加载中…"
			}
			kit.Button(c, label, kit.ButtonOptions{Variant: kit.TextButton, Small: true, Disabled: m.loading}).OnClick(m.loadEarlier)
		}
	})
}
