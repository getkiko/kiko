package processes

import (
	"fmt"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// columns of the table, as ProcessMonitor's.
var columns = []kit.Column{
	{ID: SortName, Title: "名称", MinWidth: 220, Sortable: true},
	{ID: SortCPU, Title: "% CPU", Width: 73, Align: ui.End, Sortable: true},
	{ID: SortMemory, Title: "内存", Width: 90, Align: ui.End, Sortable: true},
	{ID: SortPower, Title: "功率", Width: 90, Align: ui.End, Sortable: true},
	{ID: SortThreads, Title: "线程", Width: 62, Align: ui.End, Sortable: true},
	{ID: SortCPUTime, Title: "CPU 时间", Width: 94, Align: ui.End, Sortable: true},
	{ID: SortPID, Title: "PID", Width: 70, Align: ui.End, Sortable: true},
	{ID: SortUser, Title: "用户", Width: 90, Sortable: true},
}

// Colors of hot and warm values.
var (
	hotColor  = ui.Hex("#FF453A")
	warmColor = ui.Hex("#F28A00")
)

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	v := w.derive()
	k := kit.T(c)
	kit.Workspace(c, func() {
		w.toolbar(c, v)
		if w.err != "" {
			ui.Text(c, w.err).FontSize(kit.FontTable).TextColor(k.Danger).Background(k.DangerWeak).Padding(8, 20).
				FillWidth().MaxHeight(70).Shrink(0).Selectable()
		}
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).PaddingX(4).Children(func() { w.list(c, v) })
			if v.selected != nil && w.details {
				w.inspector(c, v)
			}
		})
		w.footer(c, v)
	})
	if w.copied != "" && time.Since(w.copiedAt) > 1500*time.Millisecond {
		w.copied = ""
	}
}

func (w *Workspace) subtitle() string {
	if w.snapshot == nil {
		return "正在读取进程…"
	}
	total := float64(w.snapshot.Total)
	return FormatCount(&total) + " 个进程 · " + FormatThreads(w.snapshot.Threads, w.snapshot.ThreadsUnavailable)
}

// groupingChoices are the grouping menu's items for the filter.
func groupingChoices(filter string) [][2]string {
	switch filter {
	case FilterApps:
		return [][2]string{{ModeApp, "按 App 分组"}}
	case FilterSystem:
		return [][2]string{{ModeName, "按名称分组"}}
	}
	return [][2]string{{ModeApp, "按 App 分组"}, {ModeParent, "按父子进程分组"}, {"none", "平铺显示"}}
}

func groupingLabel(v *view) string {
	if !v.grouped {
		return "平铺显示"
	}
	switch v.mode {
	case ModeApp:
		return "按 App 分组"
	case ModeName:
		return "按名称分组"
	}
	return "按父子进程分组"
}

func (w *Workspace) groupingItems(m *ui.Menu, v *view) {
	for _, choice := range groupingChoices(v.filter) {
		checked := v.grouped && v.mode == choice[0]
		if choice[0] == "none" {
			checked = !v.grouped
		}
		if m.Item(choice[1]).Checked(checked).Chosen() {
			w.changeGrouping(choice[0], v)
		}
	}
}

func (w *Workspace) toolbar(c *ui.Context, v *view) {
	kit.Toolbar(c, "进程", w.subtitle(), func() {
		w.search(c, v)
		segments := Filters("")
		if w.snapshot != nil {
			segments = Filters(w.snapshot.OS)
		}
		chosen := 0
		parts := make([]kit.Segment, len(segments))
		for i, s := range segments {
			parts[i] = kit.Segment{Label: s.Label}
			if s.Key == v.filter {
				chosen = i
			}
		}
		if kit.Segmented(c, "filter", &chosen, parts...).Label("进程筛选").Changed() {
			w.changeFilter(segments[chosen].Key, v)
		}
		kit.ToolbarGroup(c, func() {
			group := kit.IconButton(c, kit.SymLayers, "分组方式", kit.IconButtonOptions{Active: v.grouped, Hint: groupingLabel(v)})
			group.PopoverMenuButton(func(m *ui.Menu) { w.groupingItems(m, v) })
			canQuit := v.selected != nil && !v.exited && v.selected.PID > 1
			quit := kit.IconButton(c, kit.SymCircleX, "结束选中进程", kit.IconButtonOptions{Disabled: !canQuit})
			if quit.Clicked() && canQuit {
				w.askQuit(*v.selected, false, "toolbar")
			}
			w.confirmPopover(c, quit, "toolbar")
			if kit.IconButton(c, kit.SymPanelRight, "显示详情", kit.IconButtonOptions{Active: w.details && w.selection != nil}).Clicked() {
				w.details = !w.details
			}
		})
		kit.ToolbarGroup(c, func() {
			label, icon := "暂停刷新", kit.SymPause
			if !w.running {
				label, icon = "继续刷新", kit.SymPlay
			}
			pause := kit.IconButton(c, icon, label, kit.IconButtonOptions{})
			if !w.running {
				pause.TextColor(ui.Hex("#ED8700"))
			}
			if pause.Clicked() {
				w.running = !w.running
				if w.running {
					w.startPoll()
				} else {
					w.stopPoll()
				}
			}
			kit.IconButton(c, kit.SymSettings2, "查看选项", kit.IconButtonOptions{}).PopoverMenuButton(func(m *ui.Menu) {
				m.Item("刷新间隔").Disabled(true)
				for _, ms := range []int{1000, 2000, 5000} {
					d := time.Duration(ms) * time.Millisecond
					if m.Item(fmt.Sprintf("%d 秒", ms/1000)).Checked(w.interval == d).Chosen() {
						w.interval = d
						w.restartPoll()
					}
				}
				m.Separator()
				m.Item("分组方式").Disabled(true)
				w.groupingItems(m, v)
				m.Separator()
				m.Item("显示").Disabled(true)
				if m.Item("折叠所有分组").Chosen() {
					w.expanded = map[string]bool{}
				}
			})
		})
	})
}

// search is the toolbar's search field, suggesting matching processes
// below it while the user types.
func (w *Workspace) search(c *ui.Context, v *view) {
	k := kit.T(c)
	box := ui.Row(c.Key("search")).Height(kit.ToolbarGroupH).Width(260).MinWidth(150).Shrink(1).PaddingX(11).Gap(6).
		Radius(kit.RadiusCapsule).Background(k.Fill).TextColor(k.Label3).Cursor(ui.CursorText)
	kit.Glass(c, box)
	box.Children(func() {
		kit.Icon(c, kit.SymSearch, 15)
		before := w.query
		cb := ui.ComboboxBase(c, &w.query)
		cb.Input.Grow(1).MinWidth(0).FontSize(kit.FontBody).TextColor(k.Label).Placeholder("搜索").SingleLine().Label("搜索进程")
		if w.focus {
			cb.Input.Focus()
			w.focus = false
		}
		if cb.Input.Changed() && w.query != before {
			w.exactPID = -1
		}
		var candidates []Row
		if w.exactPID < 0 && w.query != "" {
			candidates = Rows(v.processes, v.grouped, nil, v.filter, w.query, SortName, false, v.mode, -1)
			if len(candidates) > 8 {
				candidates = candidates[:8]
			}
		}
		cb.Popup(func(panel ui.Element) {
			kit.Popup(c, panel).Padding(5).MinWidth(220)
			kit.AttachPopover(c, panel, cb.Input, ui.AnchorBottomStart, ui.AnchorTopStart, 4, 320)
			kit.PopoverBody(c, 0, func() { w.candidates(c, cb, candidates, v) })
		})
		if key, ok := cb.Chosen(); ok {
			w.query = before
			for _, row := range candidates {
				if w.rowKey(row.Process, row.Group, v) == key {
					w.selectRow(row, v)
					w.scrollTo = key
				}
			}
		}
		if w.query != "" {
			clear := ui.ButtonBase(c).Size(18, 18).Radius(kit.RadiusCapsule).Center().Background(k.Fill2).
				TextColor(k.Surface).Label("清除搜索").Children(func() { kit.Icon(c, kit.SymX, 11) })
			if clear.Clicked() {
				w.exactPID, w.query = -1, ""
			}
		}
	})
}

func (w *Workspace) list(c *ui.Context, v *view) {
	k := kit.T(c)
	rows := v.rows
	w.table.Selected = -1
	for i, row := range rows {
		key := w.rowKey(row.Process, row.Group, v)
		if w.selection != nil && key == w.selection.identity {
			w.table.Selected = i
		}
		if w.scrollTo != "" && key == w.scrollTo {
			w.table.List.ScrollTo(i, ui.Center)
			w.scrollTo = ""
		}
	}
	w.table.Sort = ui.SortOrder{Column: w.sortKey, Descending: w.desc}
	list := kit.Table(c, &w.table, columns, len(rows), kit.TableOptions{
		Label: "进程", Density: kit.Compact, Striped: true, KeepOffset: true,
		// 4 DIPs shorter than the compact rows: the process list runs long.
		RowHeight: func(int) float32 { return kit.RowHeight - 4 },
		Key:       func(row int) any { return w.rowKey(rows[row].Process, rows[row].Group, v) },
		Menu:      func(menu *ui.Menu, row int) { w.rowMenu(c, menu, rows[row], v) },
		Empty: func() {
			text := "没有匹配的进程"
			if w.snapshot == nil {
				text = "正在采集系统进程…"
				if w.err != "" {
					text = "等待重新采样…"
				}
			}
			ui.Text(c, text).TextColor(k.Label3).TextAlign(ui.Center).FillWidth().Padding(40, 20)
		},
	}, func(i, col int) {
		row := rows[i]
		p := row.Process
		selected := i == w.table.Selected
		color := func(e ui.Element, cl ui.Color) {
			if !selected {
				e.TextColor(cl)
			}
		}
		switch columns[col].ID {
		case SortName:
			ui.Box(c).Width(float32(min(row.Depth, 8)) * 18).Shrink(0)
			if row.Group && row.Count > 1 {
				open := w.expanded[GroupIdentity(p, v.mode)]
				action := "展开 "
				if open {
					action = "折叠 "
				}
				d := ui.ButtonBase(c).Size(20, 20).Radius(5).Center().Shrink(0).Label(action + p.Name).Transition(kit.HoverMotion)
				if d.Hovered() {
					d.Background(k.Fill)
				}
				d.Children(func() {
					chev := kit.Icon(c, kit.SymChevronRight, 15)
					color(chev, k.Label3)
					if open {
						chev.Rotate(90)
					}
				})
				if d.Clicked() {
					w.toggleExpanded(p, v.mode)
				}
			} else {
				ui.Box(c).Size(20, 20).Shrink(0)
			}
			w.icon(c, p, false)
			name := p.Path
			if name == "" {
				name = p.Name
			}
			ui.Text(c, p.Name).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
			if row.Group {
				badge := ui.Row(c).Height(14).PaddingX(4).Radius(7).Shrink(0)
				if selected {
					badge.Background(ui.RGBA(255, 255, 255, 0.2)).TextColor(ui.Hex("#D3E8FF"))
				} else {
					badge.Background(k.Fill).TextColor(k.Label3)
				}
				badge.Children(func() {
					ui.Text(c, strconv.Itoa(row.Count)).FontSize(10).FontWeight(600).FontFeatures("tnum")
				})
			}
		case SortCPU:
			t := kit.NumberCell(c, FormatPercent(p.CPU, 1))
			switch {
			case p.CPU != nil && *p.CPU == 0:
				color(t, k.Label4)
			case p.CPU != nil && *p.CPU >= 80:
				color(t, hotColor)
				t.FontWeight(600)
			case p.CPU != nil && *p.CPU >= 25:
				color(t, warmColor)
				t.FontWeight(600)
			}
		case SortMemory:
			t := kit.NumberCell(c, FormatBytes(p.Memory))
			if p.Memory != nil && *p.Memory >= 4*(1<<30) {
				color(t, warmColor)
				t.FontWeight(600)
			}
		case SortPower:
			t := kit.NumberCell(c, FormatPower(p.Power))
			if p.Power != nil && *p.Power == 0 {
				color(t, k.Label4)
			}
		case SortThreads:
			kit.NumberCell(c, FormatCount(p.Threads))
		case SortCPUTime:
			kit.NumberCell(c, FormatCPUTime(p.CPUTime))
		case SortPID:
			color(kit.NumberCell(c, strconv.Itoa(p.PID)), k.Label3)
		case SortUser:
			user := p.User
			if user == "" {
				user = "—"
			}
			color(kit.Cell(c, user, ""), k.Label3)
		}
	})
	if list.Changed() {
		if i, ok := w.table.Chosen(len(rows)); ok {
			w.selectRow(rows[i], v)
		}
	}
	if list.Submitted() {
		if i, ok := w.table.Chosen(len(rows)); ok {
			w.selectRow(rows[i], v)
			w.details = true
		}
	}
	if i, ok := w.table.Chosen(len(rows)); ok && rows[i].Group && rows[i].Count > 1 {
		key := GroupIdentity(rows[i].Process, v.mode)
		if w.table.List.Shortcut(c, 0, ui.KeyRight) {
			w.expanded[key] = true
		}
		if w.table.List.Shortcut(c, 0, ui.KeyLeft) {
			delete(w.expanded, key)
		}
	}
	if w.details && w.table.List.Shortcut(c, 0, ui.KeyEscape) {
		w.details = false
	}
	// The header sorts: a new column starts descending, except names.
	if w.table.Sort.Column != w.sortKey {
		w.sortKey = w.table.Sort.Column
		w.desc = w.sortKey != SortName && w.sortKey != SortUser
	} else if w.table.Sort.Descending != w.desc {
		w.desc = w.table.Sort.Descending
	}
}

func (w *Workspace) footer(c *ui.Context, v *view) {
	k := kit.T(c)
	seen := map[int]bool{}
	for _, row := range v.rows {
		for _, m := range row.Members {
			seen[m.PID] = true
		}
	}
	total := "—"
	if w.snapshot != nil {
		total = strconv.Itoa(w.snapshot.Total)
	}
	left := fmt.Sprintf("显示 %d 个进程，共 %s 个", len(seen), total)
	if w.snapshot != nil && w.snapshot.Estimated > 0 {
		left += fmt.Sprintf(" · %d 个进程使用系统估算值", w.snapshot.Estimated)
	}
	if !w.running {
		left += " · 已暂停"
	}
	kit.StatusBar(c, left, "", func() {
		ui.Row(c).Gap(20).Children(func() {
			metric := func(dot ui.Color, label, value string) {
				ui.Row(c).Gap(6).Children(func() {
					ui.Box(c).Size(6, 6).Radius(3).Background(dot)
					ui.Text(c, label).SingleLine()
					ui.Text(c, value).SingleLine().FontWeight(600).TextColor(k.Label).FontFeatures("tnum")
				})
			}
			cpu := "—"
			if w.snapshot != nil && w.snapshot.CPU != nil {
				cpu = fmt.Sprintf("%.0f%%", *w.snapshot.CPU)
			}
			var memory *float64
			if w.snapshot != nil {
				memory = w.snapshot.Memory
			}
			metric(ui.Hex("#0A84FF"), "CPU", cpu)
			metric(ui.Hex("#30B34C"), "内存", FormatBytes(memory))
		})
	})
}

// candidates are the options of the search's combobox: the processes
// matching the query.
func (w *Workspace) candidates(c *ui.Context, cb ui.ComboboxParts, candidates []Row, v *view) {
	k := kit.T(c)
	for _, row := range candidates {
		key := w.rowKey(row.Process, row.Group, v)
		item := cb.Item(key).MinHeight(28).PaddingX(8).Gap(8).Radius(5).FontSize(kit.FontTable)
		muted := k.Label3
		if item.Highlighted() {
			item.Background(k.Accent).TextColor(k.OnAccent)
			muted = k.OnAccent.Alpha(0.8)
		}
		item.Children(func() {
			w.icon(c, row.Process, false)
			ui.Text(c, row.Process.Name).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
			ui.Text(c, fmt.Sprintf("%d 个进程", row.Count)).FontSize(kit.FontCaption).TextColor(muted).Shrink(0)
		})
	}
}
