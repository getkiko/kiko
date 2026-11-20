package connections

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/srcname"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

var modeTitles = map[groupMode]string{groupNone: "平铺显示", groupApp: "按 App 分组", groupParent: "按父子进程分组",
	groupRootDomain: "按主域名", groupDomain: "按域名"}

// view builds the connection monitor under the workspace's tabs.
func (m *connMonitor) view(c *ui.Context) {
	width, _ := c.Size()
	narrow := width <= narrowWidth
	if m.sidebar == nil {
		open := !narrow
		m.sidebar = &open
	}
	now := c.Now()
	c.After(30 * time.Second)
	m.sync()
	m.freeze.Begin()
	m.applyHeld()
	d := m.freeze.Hold(m.derive)
	hasFilters := slices.ContainsFunc(facets, func(f facet) bool { return len(withSelected(d.facets[f], m.filter[f])) > 0 })
	showFilters := *m.sidebar && hasFilters

	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		m.toolbar(c, d, hasFilters)
		if message := firstNonEmpty(m.err, m.streamErr, m.exportErr); message != "" {
			ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
				kit.Notice(c, kit.DangerTone, "", message, func() {
					kit.IconButton(c, kit.SymX, "关闭提示", kit.IconButtonOptions{Size: 22, IconSize: 13}).OnClick(func() {
						m.err, m.streamErr, m.exportErr = "", "", ""
					})
				}).Grow(1)
			})
		}
		if m.connID != "" {
			ui.Row(c).PaddingX(16).PaddingY(6).Gap(8).Shrink(0).Children(func() {
				kit.Tag(c, "连接 "+m.connID, func() { m.connID = ""; m.page = 0 })
			})
		}
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if showFilters && !narrow {
				m.filterPane(c, d)
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { m.list(c, d, now) })
			if !narrow {
				m.inspectors(c, d, now)
			}
			if showFilters && !narrow {
				width, lo, hi := kit.FilterPaneSize(c, kit.FilterConnection, &m.filterWidth)
				if kit.ResizeHandle(c, &m.filterWidth, width, lo, hi, kit.FilterResizeLabel) {
					m.env.Prefs.Save(prefConnFilterWidth, m.filterWidth.Width)
				}
			}
		})
		m.statusBar(c, d)
	})
	if narrow {
		if showFilters {
			sidePanel(c, false, func() { *m.sidebar = false }, func() { m.filterPane(c, d) })
		}
		if m.selection(d) != nil || (m.groupDetails && m.selectedGroup != nil) {
			sidePanel(c, true, func() { m.closeInspector() }, func() { m.inspectors(c, d, now) })
		}
	}
	m.columnSettings(c)
}

func (m *connMonitor) closeInspector() {
	m.selectedID, m.groupDetails = "", false
	if m.selectedGroup == nil {
		m.table.Clear()
	}
}

func (m *connMonitor) selection(d *connDerived) *Row {
	if m.selectedID == "" {
		return nil
	}
	if m.fetched != nil && m.fetched.Conn.ID == m.selectedID {
		return m.fetched
	}
	if n := d.index["connection:"+m.selectedID]; n != nil {
		return n.Row
	}
	return nil
}

func (m *connMonitor) toolbar(c *ui.Context, d *connDerived, hasFilters bool) {
	m.header(c, "", func() {
		seg := kit.Segmented(c, "conn-tab", &m.tab,
			kit.Segment{Label: "全部", Count: kit.Count(d.counts.All)},
			kit.Segment{Label: "活动", Count: kit.Count(d.counts.Active)},
			kit.Segment{Label: "已关闭", Count: kit.Count(d.counts.Closed)},
			kit.Segment{Label: "已拒绝", Count: kit.Count(d.counts.Rejected)})
		seg.Label("连接状态")
		if seg.Changed() {
			m.setTab(m.tab)
		}
		if kit.Search(c, "conn-search", &m.search, kit.SearchOptions{Placeholder: "搜索主机、进程、地址、链路…", Toolbar: true, Width: 260}).Changed() {
			m.page = 0
		}
	}, func() {
		kit.ToolbarGroup(c, func() {
			kit.IconButton(c, kit.SymSlidersHorizontal, "筛选", kit.IconButtonOptions{Active: *m.sidebar && hasFilters, Disabled: !hasFilters}).
				OnClick(func() { *m.sidebar = !*m.sidebar; m.env.Prefs.Save(prefConnSidebar, *m.sidebar) })
			pause, label := kit.SymPause, "暂停"
			if m.paused {
				pause, label = kit.SymPlay, "继续"
			}
			kit.IconButton(c, pause, label, kit.IconButtonOptions{Active: m.paused}).OnClick(m.togglePause)
			kit.IconButton(c, kit.SymLayers, modeTitles[m.mode], kit.IconButtonOptions{Active: m.mode != groupNone}).PopoverMenuButton(func(menu *ui.Menu) {
				menu.Item("连接分组").Disabled(true)
				for _, mode := range groupModes {
					if menu.Item(modeTitles[mode]).Checked(m.mode == mode).Chosen() {
						m.setMode(mode)
					}
				}
				if m.mode != groupNone {
					menu.Separator()
					if menu.Item("展开全部").Chosen() {
						m.expansions[m.scope()] = map[string]bool{"*": true}
					}
					if menu.Item("折叠全部").Chosen() {
						m.expansions[m.scope()] = map[string]bool{}
					}
				}
			})
			kit.IconButton(c, kit.SymColumns3, "列设置", kit.IconButtonOptions{}).OnClick(func() { m.settings = true })
			// Export, closing every connection and clearing the records share
			// the More menu; their confirmations anchor to it.
			closeDisabled, listDisabled := m.closeDisabled(d), m.closeListDisabled(d)
			clearDisabled := d.totals.All == 0 || !m.loaded || m.streamErr != "" || m.busy && !m.clearConfirm.Busy
			more := kit.IconButton(c, kit.SymEllipsis, "更多操作", kit.IconButtonOptions{})
			more.PopoverMenuButton(func(menu *ui.Menu) {
				menu.Submenu("导出全部历史", func(sub *ui.Menu) {
					for _, kind := range []string{"csv", "json"} {
						if sub.Item(kindLabel(kind)).Chosen() {
							m.exportHistory(kind)
						}
					}
				})
				menu.Submenu("导出当前筛选结果", func(sub *ui.Menu) {
					for _, kind := range []string{"csv", "json"} {
						if sub.Item(kindLabel(kind)).Chosen() {
							m.exportRows(kind)
						}
					}
				})
				menu.Separator()
				if menu.Item("关闭全部连接").Disabled(closeDisabled).Chosen() {
					m.confirm.Open = true
				}
				if menu.Item("关闭当前列表连接").Disabled(listDisabled).Chosen() {
					m.listConfirm.Open = true
				}
				counts := clearCounts(d)
				menu.Submenu("清空记录", func(sub *ui.Menu) {
					for _, scope := range []struct {
						tab   int
						label string
					}{{tabAll, "全部"}, {tabActive, "已连接"}, {tabClosed, "已关闭"}} {
						label := fmt.Sprintf("%s（%s 条）", scope.label, kit.Count(counts[scope.tab]))
						if sub.Item(label).Disabled(clearDisabled || counts[scope.tab] == 0).Chosen() {
							m.clearScope, m.clearConfirm.Open = scope.tab, true
						}
					}
				})
			})
			kit.ConfirmPopover(c, more, &m.confirm, kit.ConfirmOptions{
				Title:   "关闭全部连接",
				Message: "核心会立即关闭当前全部活动连接。应用可能自动重新建立连接。",
				Confirm: fmt.Sprintf("关闭 %s 条连接", kit.Count(d.totals.Active)),
				Danger:  true,
				Run:     m.closeAll,
			})
			kit.ConfirmPopover(c, more, &m.listConfirm, kit.ConfirmOptions{
				Title:   "关闭当前列表连接",
				Message: "核心会立即关闭筛选结果中的活动连接。应用可能自动重新建立连接。",
				Confirm: fmt.Sprintf("关闭 %s 条连接", kit.Count(d.matchedActive)),
				Danger:  true,
				Run:     m.closeList,
			})
			m.clearConfirmPopover(c, more, d)
		})
	})
}

// path is the outbounds conn passes through, with the detours of the
// configuration the core runs and the members its groups chose now.
func (m *connMonitor) path(conn *api.Connection) []hop {
	var detours map[string]string
	if m.store != nil {
		detours = m.store.AppliedDetours()
	}
	var selected map[string]string
	if server := m.env.Server; server != nil {
		for _, g := range server.Selectors {
			if g.Now != "" {
				if selected == nil {
					selected = map[string]string{}
				}
				selected[g.Name] = g.Now
			}
		}
	}
	return connectionPath(conn, detours, selected)
}

// faded reports a line drawn faded: an ended connection among all of
// them. The Closed tab lists only ended ones, and shows them as they are.
func (m *connMonitor) faded(v visibleNode) bool {
	return m.tab == tabAll && v.Node != nil && !v.Node.group() && v.Node.Row.Closed()
}

// closeDisabled is whether "Close all connections" has nothing to close or cannot run.
func (m *connMonitor) closeDisabled(d *connDerived) bool {
	return m.tab == tabClosed || m.tab == tabRejected || d.totals.Active == 0 || !m.loaded || m.streamErr != "" || m.paused || m.busy
}

// closeListDisabled is whether "Close listed connections" cannot run: as
// "Close all connections", or the filtered list holds no active connection.
func (m *connMonitor) closeListDisabled(d *connDerived) bool {
	return m.closeDisabled(d) || d.matchedActive == 0
}

// clearCounts are the records of each tab.
func clearCounts(d *connDerived) [3]int {
	return [...]int{tabAll: d.totals.All, tabActive: d.totals.Active, tabClosed: d.totals.Closed + d.totals.Rejected}
}

// clearConfirmPopover asks, beside anchor, to clear the records of the
// scope chosen in the More menu's "Clear records": all of them, or those of the
// active or the closed connections; connections keep running.
func (m *connMonitor) clearConfirmPopover(c *ui.Context, anchor ui.Element, d *connDerived) {
	counts := clearCounts(d)
	message := "仅从记录中移除，连接继续运行。清空的活动连接直到关闭前不再显示，新连接照常记录。"
	if m.clearScope == tabClosed {
		message = "已关闭连接的记录将从服务端历史中移除，活动连接继续显示。"
	}
	kit.ConfirmPopover(c, anchor, &m.clearConfirm, kit.ConfirmOptions{
		Title:   "清空连接记录",
		Message: message,
		Confirm: fmt.Sprintf("清空 %s 条记录", kit.Count(counts[m.clearScope])),
		Danger:  true,
		Run:     m.clearHistory,
	})
}

func kindLabel(kind string) string {
	if kind == "csv" {
		return "CSV 文件"
	}
	return "JSON 文件"
}

func (m *connMonitor) filterPane(c *ui.Context, d *connDerived) {
	width, _, _ := kit.FilterPaneSize(c, kit.FilterConnection, &m.filterWidth)
	names := m.sourceNames()
	kit.SizedFilterPane(c, kit.FilterConnection, width, len(m.filter) > 0, func() { m.filter = kit.FacetChoice[facet]{}; m.page = 0 }, func() {
		for _, f := range facets {
			options := withSelected(d.facets[f], m.filter[f])
			if len(options) == 0 {
				continue
			}
			kit.FacetGroup(c, facetLabels[f], m.facets.Of(string(f)), len(options),
				func(i int) string {
					if f == facetSource {
						return names.Text(options[i].value)
					}
					return options[i].value
				},
				func(i int) bool { return m.filter.Has(f, options[i].value) },
				func(i int) {
					option := options[i]
					if f == facetSource {
						o := kit.FilterOptionNamed(c.Key(facetKey(f, option.value)), kit.FilterConnection, m.filter.Has(f, option.value), option.value, names.Name(option.value), optionCount(option.count))
						if o.Clicked() {
							m.toggleFilter(f, option.value)
						}
						return
					}
					var icon func()
					if conn := d.facetIcons[facetKey(f, option.value)]; conn != nil {
						icon = func() { m.connectionIcon(c, d, conn, 20) }
					}
					o := kit.FilterOption(c.Key(facetKey(f, option.value)), kit.FilterConnection, m.filter.Has(f, option.value), icon, option.value, option.count)
					if o.Clicked() {
						m.toggleFilter(f, option.value)
					}
				})
		}
	})
}

// list builds the table of the current page, or the empty state.
func (m *connMonitor) list(c *ui.Context, d *connDerived, now time.Time) {
	if m.page != m.lastPage {
		m.lastPage = m.page
		m.table.List.ScrollTo(0, ui.Start)
	}
	visible := flattenConnectionTree(d.tree, m.isExpanded)
	names := m.sourceNames()
	columns := make([]kit.Column, len(m.columns))
	for i, col := range m.columns {
		column := kit.Column{ID: string(col), Title: columnLabels[col], Sortable: true, Width: columnWidths[col]}
		if col == colHost || col == colRule {
			column.Width, column.MinWidth, column.Grow = 0, columnWidths[col], columnWidths[col]/300
		}
		columns[i] = column
	}
	list := kit.Table(c, &m.table, columns, len(visible), kit.TableOptions{
		Label: "连接", Density: kit.TwoLine, Striped: true, KeepOffset: true, Flat: true,
		Resizable: true, OnResize: func() { m.env.Prefs.Save(prefConnColumnWidths, m.table.Widths) },
		// Group titles are as short as the rows of the process list.
		RowHeight: func(row int) float32 {
			if v := visible[row]; v.Node != nil && v.Node.group() {
				return kit.RowHeight - 4
			}
			return 0
		},
		Key: func(row int) any {
			if v := visible[row]; v.Node != nil {
				return v.Node.Key
			}
			return visible[row].MoreKey
		},
		Faded: func(row int) bool { return m.faded(visible[row]) },
		Menu:  m.freeze.Menu(func(menu *ui.Menu, row int) { m.rowMenu(c, d, menu, visible[row]) }),
		Empty: func() { m.empty(c) },
	}, func(row, col int) { m.cell(c, d, names, visible[row], m.columns[col], now) })
	m.freeze.End(c)
	if sorted := Column(m.table.Sort.Column); sorted != m.sortCol {
		m.sortCol = sorted
		m.table.Sort.Descending = sorted.descendingByDefault()
		m.page = 0
	}
	if list.Changed() {
		m.copied = false
		if i, ok := m.table.Chosen(len(visible)); ok {
			v := visible[i]
			switch {
			case v.Node == nil:
				m.childLimits()[v.ParentKey] = max(m.childLimits()[v.ParentKey], 50) + 50
				m.table.Clear()
			case v.Node.group():
				m.selectedID, m.selectedGroup, m.groupDetails = "", v.Node, false
			default:
				m.selectedID, m.selectedGroup, m.groupDetails = v.Node.Row.Conn.ID, nil, false
			}
		}
	}
	if list.Submitted() && m.selectedGroup != nil {
		m.groupDetails = true
	}
	if group := m.selectedGroup; group != nil {
		if list.Shortcut(0, ui.KeyRight) {
			m.expanded()[group.Key] = true
		}
		if list.Shortcut(0, ui.KeyLeft) {
			m.expanded()[group.Key] = false
		}
	}
}

func (m *connMonitor) empty(c *ui.Context) {
	message := "暂无连接"
	switch {
	case !m.loaded:
		message = firstNonEmpty(m.streamErr, "正在读取连接…")
	case m.search != "" || len(m.filter) > 0 || m.connID != "":
		message = "当前筛选条件下暂无连接"
	case m.tab == tabActive:
		message = "暂无活动连接"
	case m.tab == tabClosed:
		message = "暂无已关闭连接"
	case m.tab == tabRejected:
		message = "暂无已拒绝连接"
	}
	kit.EmptyState(c, kit.SymCable, message, "", nil)
}

// connectionIcon is ConnectionProcessIcon: the process's icon, or a
// network glyph for a connection without a process.
func (m *connMonitor) connectionIcon(c *ui.Context, d *connDerived, conn *api.Connection, size float32) {
	info := conn.ProcessInfo
	pid, path := 0, ""
	if info != nil {
		pid, path = info.ProcessID, info.ProcessPath
	}
	known, ok := d.iconProcesses[pid]
	if pid == 0 {
		ok = false
	}
	if ok && path == "" {
		path = known.Path
	}
	p := iconProcess{PID: pid, Path: path, Name: processName(conn)}
	if ok && (info == nil || info.ProcessPath == "" || known.Path == path) {
		p = iconProcess{PID: known.PID, StartedAt: known.StartedAt, Path: known.Path, Name: known.Name, AppPath: known.AppPath}
	}
	if path == "" && pid == 0 {
		k := kit.T(c)
		ui.Box(c).Size(size, size).Center().Shrink(0).TextColor(k.Label3).Children(func() { kit.Icon(c, kit.SymNetwork, size*0.6) })
		return
	}
	m.icons.processIcon(c, p, size)
}

func groupIcon(p Process) iconProcess {
	return iconProcess{PID: p.PID, StartedAt: p.StartedAt, Path: p.Path, Name: p.Name, AppPath: p.AppPath}
}

func (m *connMonitor) cell(c *ui.Context, d *connDerived, names *srcname.Names, v visibleNode, col Column, now time.Time) {
	k := kit.T(c)
	if v.Node == nil {
		if col == m.columns[0] {
			ui.Row(c).Gap(8).PaddingStart(float32(min(v.Depth, 8))*18 + 26).TextColor(k.AccentText).FontSize(kit.FontCaption).Children(func() {
				ui.Text(c, fmt.Sprintf("再显示 %d 项", min(50, v.Remaining)))
				ui.Text(c, fmt.Sprintf("剩余 %s 项", kit.Count(v.Remaining))).TextColor(k.Label3)
			})
		}
		return
	}
	if v.Node.group() {
		m.groupCell(c, v, col)
		return
	}
	row := v.Node.Row
	conn := &row.Conn
	switch col {
	case colHost:
		indent := float32(0)
		if m.mode != groupNone {
			indent = float32(min(v.Depth, 8))*18 + 26
		}
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).PaddingStart(indent).Children(func() {
			m.connectionIcon(c, d, conn, 28)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(5).MinWidth(0).Children(func() {
					protocol := connectionProtocolLabel(conn)
					ui.Box(c).PaddingX(4).Radius(4).Background(k.AccentWeak).TextColor(k.AccentText).Shrink(0).Children(func() {
						ui.Text(c, protocol).FontSize(9).FontWeight(700).SingleLine()
					})
					if conn.IPVersion == 6 {
						kit.Hint(c, kit.Badge(c, "6", kit.Neutral), "IPv6")
					}
					kit.Clipped(c, hostName(conn), kit.FontTable, 600)
				})
				kit.Clipped(c, firstNonEmpty(processName(conn), conn.Protocol, conn.Destination, "未知进程"), kit.FontCaption, 400).
					TextColor(k.Label3)
			})
		})
	case colRule:
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			kit.Clipped(c, firstNonEmpty(ruleText(conn), "—"), kit.FontTable, 600)
			if conn.Rejected() {
				ui.Row(c).Gap(5).Children(func() {
					ui.Box(c).Size(6, 6).Radius(3).Background(k.Danger).Shrink(0)
					ui.Text(c, rejectedLabel(conn)).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine()
				})
				return
			}
			kit.Clipped(c, pathText(m.path(conn)), kit.FontCaption, 400).TextColor(k.Label3)
		})
	case colUploadRate:
		rate(c, kit.SymArrowUp, upColor, kit.Rate(row.UpRate))
	case colDownloadRate:
		rate(c, kit.SymArrowDown, downColor, kit.Rate(row.DownRate))
	case colUploadTotal:
		kit.NumberCell(c, kit.Bytes(float64(row.UpTotal)))
	case colDownloadTotal:
		kit.NumberCell(c, kit.Bytes(float64(row.DownTotal)))
	case colSource:
		kit.NamedCell(c, firstNonEmpty(conn.Source, "—"), sourceName(names, conn.Source), "")
	case colCreated:
		ui.Text(c, formatConnectionTime(int64(conn.CreatedAt), now)).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").SingleLine()
	case colNetwork:
		ui.Box(c).PaddingX(7).PaddingY(3).Radius(5).Background(k.Fill).Children(func() {
			ui.Text(c, upperOr(conn.Network, "—")).FontSize(kit.FontCaption).SingleLine()
		})
	default:
		kit.Cell(c, firstNonEmpty(columnText(row, col), "—"), "")
	}
}

var (
	upColor   = ui.Hex("#E18A28")
	downColor = ui.Hex("#29A76B")
)

func rate(c *ui.Context, icon kit.Symbol, color ui.Color, text string) {
	ui.Row(c).Gap(3).Children(func() {
		kit.Icon(c, icon, 11).TextColor(color)
		kit.NumberCell(c, text)
	})
}

func (m *connMonitor) groupCell(c *ui.Context, v visibleNode, col Column) {
	k := kit.T(c)
	group := v.Node
	switch col {
	case colHost:
		open := m.isExpanded(group.Key)
		ui.Row(c).Grow(1).MinWidth(0).Gap(6).PaddingStart(float32(min(v.Depth, 8)) * 18).Children(func() {
			verb := "展开"
			if open {
				verb = "折叠"
			}
			disclosure := ui.ButtonBase(c.Key("disclosure")).Size(20, 24).Radius(5).Center().TextColor(k.Label3).Label(verb + " " + group.Label)
			disclosure.Children(func() {
				chevron := kit.Icon(c, kit.SymChevronRight, 15)
				if open {
					chevron.Rotate(90)
				}
			})
			if disclosure.Clicked() {
				m.expanded()[group.Key] = !open
			}
			if domainGroupKey(group.Key) {
				domainGroupIcon(c, group.Key == ipGroupKey, 16)
			} else {
				m.icons.processIcon(c, groupIcon(group.Process), 16)
			}
			kit.Clipped(c, group.Label, kit.FontTable, 400)
			ui.Box(c).PaddingX(5).Radius(8).Background(k.Fill).TextColor(k.Label3).Shrink(0).Children(func() {
				ui.Text(c, kit.Count(group.Count)).FontSize(10).FontWeight(600).FontFeatures("tnum")
			})
		})
	case colUploadRate:
		rate(c, kit.SymArrowUp, upColor, kit.Rate(group.Totals.UpRate))
	case colDownloadRate:
		rate(c, kit.SymArrowDown, downColor, kit.Rate(group.Totals.DownRate))
	case colUploadTotal:
		rate(c, kit.SymUpload, upColor, kit.Bytes(float64(group.Totals.UpTotal)))
	case colDownloadTotal:
		rate(c, kit.SymDownload, downColor, kit.Bytes(float64(group.Totals.DownTotal)))
	case colUser:
		users := firstNonEmpty(joinComma(group.Users), "-")
		kit.Cell(c, users, "")
	default:
		ui.Text(c, "-").TextColor(k.Label4)
	}
}

func (m *connMonitor) statusBar(c *ui.Context, d *connDerived) {
	total := d.total
	first := 0
	if total > 0 {
		first = m.page*connPageSize + 1
	}
	unit := "条连接"
	if m.mode != groupNone {
		unit = "个分组"
	}
	left := fmt.Sprintf("显示 %d–%d / %s %s", first, min((m.page+1)*connPageSize, total), kit.Count(total), unit)
	if m.mode != groupNone {
		left += fmt.Sprintf(" · %s 条连接", kit.Count(d.matched))
	}
	status := "连接中"
	switch {
	case m.paused:
		status = "已暂停显示"
	case m.streamErr != "":
		status = "连接中断"
	case m.loaded:
		status = "实时更新"
	}
	center := fmt.Sprintf("%s · 活动 %s · 已关闭 %s", status, kit.Count(d.totals.Active), kit.Count(d.totals.Closed))
	if d.totals.Rejected > 0 {
		center += " · 已拒绝 " + kit.Count(d.totals.Rejected)
	}
	pages := max(1, (total+connPageSize-1)/connPageSize)
	kit.StatusBar(c, left, center, func() { kit.Pagination(c, &m.page, pages) })
}

// exportRows saves the connections of the tab, filters and search as
// CSV or JSON, read from the server a page at a time.
func (m *connMonitor) exportRows(kind string) {
	name := fmt.Sprintf("kiko-connections-%s-%s.%s", tabNames[m.tab], timefmt.Format(time.Now(), "2006-01-02"), kind)
	filter := env.FileFilter{Name: "JSON", Extensions: []string{"json"}}
	if kind == "csv" {
		filter = env.FileFilter{Name: "CSV", Extensions: []string{"csv"}}
	}
	q := m.query()
	q.Group, q.Expand, q.Collapse, q.More, q.Limit = string(groupNone), nil, nil, nil, 200
	columns := slices.Clone(m.columns)
	m.exportErr = ""
	m.env.Run(func(ctx context.Context) error {
		var rows []*Row
		for q.Page = 0; ; q.Page++ {
			page, err := m.env.API.QueryConnections(ctx, q)
			if err != nil {
				return err
			}
			for _, r := range page.Rows {
				rows = append(rows, rowOf(r))
			}
			if len(page.Rows) == 0 || (q.Page+1)*q.Limit >= page.Total {
				break
			}
		}
		data := connectionsJSON(rows)
		if kind == "csv" {
			data = connectionsCSV(rows, columns)
		}
		_, err := m.env.Shell.SaveFile(name, data, filter)
		return err
	}, func(err error) {
		if err != nil {
			m.exportErr = err.Error()
		}
	})
}

// exportHistory saves the server's whole connection history.
func (m *connMonitor) exportHistory(kind string) {
	m.exportErr = ""
	name := fmt.Sprintf("kiko-connections-history-%s.%s", timefmt.Format(time.Now(), "2006-01-02"), kind)
	filter := env.FileFilter{Name: map[string]string{"csv": "CSV", "json": "JSON"}[kind], Extensions: []string{kind}}
	m.env.Run(func(ctx context.Context) error {
		data, err := m.env.API.ExportConnections(ctx, kind)
		if err != nil {
			return err
		}
		_, err = m.env.Shell.SaveFile(name, data, filter)
		return err
	}, func(err error) {
		if err != nil {
			m.exportErr = err.Error()
		}
	})
}

// columnSettings is the connection list's column settings dialog: shown
// columns and their order, dragged by their rows.
func (m *connMonitor) columnSettings(c *ui.Context) {
	k := kit.T(c)
	kit.Dialog(c, &m.settings, "连接列表列设置", 0, func() {
		ui.Text(c, "选择显示的列，并调整排列顺序。").FontSize(kit.FontTable).TextColor(k.Label2)
		m.columnList.Reorder = func(rows []int, to int) {
			var moved []Column
			for _, i := range rows {
				moved = append(moved, m.columnOrder[i])
			}
			var rest []Column
			insert := 0
			for i, col := range m.columnOrder {
				if slices.Contains(rows, i) {
					continue
				}
				if i < to {
					insert++
				}
				rest = append(rest, col)
			}
			m.columnOrder = slices.Insert(rest, insert, moved...)
			var shown []Column
			for _, col := range m.columnOrder {
				if slices.Contains(m.columns, col) {
					shown = append(shown, col)
				}
			}
			m.setColumns(shown)
		}
		ui.List(c, &m.columnList, len(m.columnOrder)).Height(float32(len(m.columnOrder)) * 34).Label("列").Rows(func(r ui.ListRow) {
			col := m.columnOrder[r.Index]
			rc := r.Context
			ui.Row(rc).Height(34).PaddingX(4).Gap(8).Children(func() {
				checked := slices.Contains(m.columns, col)
				locked := col == colHost && m.mode != groupNone
				box := kit.Checkbox(rc, &checked, columnLabels[col]).Disabled(locked)
				if locked {
					kit.Hint(rc, box, "分组显示需要主机 / 进程列")
				}
				if box.Changed() {
					var next []Column
					for _, c := range m.columnOrder {
						if (c == col && checked) || (c != col && slices.Contains(m.columns, c)) {
							next = append(next, c)
						}
					}
					m.setColumns(next)
				}
				ui.Spacer(rc)
				kit.Icon(rc, kit.SymGripVertical, 16).TextColor(k.Label3)
			})
		})
	}, func() {
		kit.Button(c, "恢复默认列", kit.ButtonOptions{}).OnClick(func() {
			m.columnOrder = append(slices.Clone(defaultColumns), restColumns(defaultColumns)...)
			m.setColumns(slices.Clone(defaultColumns))
		})
	})
}
