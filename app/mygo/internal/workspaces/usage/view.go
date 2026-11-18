package usage

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// dimensionIcon is a dimension's row symbol.
func dimensionIcon(dimension string) kit.Symbol {
	switch dimension {
	case "source":
		return kit.SymLaptop
	case "process":
		return kit.SymActivity
	case "outbound", "chain", "rule":
		return kit.SymRoute
	case "inbound", "destination":
		return kit.SymServer
	}
	return kit.SymGlobe2
}

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	if !w.sidebarInit {
		width, _ := c.Size()
		w.sidebar, w.sidebarInit = width > 800, true
	}
	w.freeze.Begin()
	w.applyHeld()
	w.sync()
	k := kit.T(c)
	kit.Workspace(c, func() {
		w.toolbar(c)
		if w.rng == "custom" {
			w.customRange(c)
		}
		q := w.query()
		valid := ValidRange(q.From, q.To)
		if !valid || w.err != "" {
			message := w.err
			if !valid {
				message = "请选择结束晚于开始的有效时间范围"
			}
			ui.Box(c).Padding(8, 16, 0).Shrink(0).Children(func() { kit.Notice(c, kit.DangerTone, "", message, nil) })
		}
		if w.exportErr != "" {
			ui.Box(c).Padding(8, 16, 0).Shrink(0).Children(func() {
				kit.Notice(c, kit.DangerTone, "", w.exportErr+" · 请通过导出菜单重试", nil)
			})
		}
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			filters := w.availableFilters()
			width, lo, hi := kit.FilterPaneSize(c, kit.FilterConnection, &w.filterWidth)
			if w.sidebar && len(filters) > 0 {
				w.filterPane(c, filters, width)
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { w.content(c) })
			if w.selected != nil {
				w.inspector(c)
			}
			if w.sidebar && len(filters) > 0 && kit.ResizeHandle(c, &w.filterWidth, width, lo, hi, kit.FilterResizeLabel) {
				w.env.Prefs.Save(prefFilterWidth, w.filterWidth.Width)
			}
		})
		w.footer(c, q, k)
	})
}

func (w *Workspace) toolbar(c *ui.Context) {
	kit.Toolbar(c, "用量", "", func() {
		if kit.Search(c, "search", &w.search, kit.SearchOptions{Placeholder: "搜索域名、进程、地址、出口…", Toolbar: true, Width: 280}).Changed() {
			w.resetPage()
		}
		kit.ToolbarGroup(c, func() {
			groups := make([]kit.Option, len(Dimensions))
			for i, d := range Dimensions {
				groups[i] = kit.Option{Value: d.Value, Label: d.Plural}
			}
			group := w.group
			if kit.Select(c, "group", &group, groups, kit.SelectOptions{Label: "分组方式", Width: 104, Toolbar: true}).Changed() {
				w.changeGroup(group)
			}
			seconds := []kit.Option{{Value: "", Label: "单维度"}}
			for _, d := range Dimensions {
				if d.Value != w.group {
					seconds = append(seconds, kit.Option{Value: d.Value, Label: "＋ " + d.Label})
				}
			}
			if kit.Select(c, "secondary", &w.secondary, seconds, kit.SelectOptions{Label: "第二分组", Width: 112, Toolbar: true}).Changed() {
				w.resetPage()
			}
		})
		kit.ToolbarGroup(c, func() {
			ranges := make([]kit.Option, len(Ranges))
			for i, r := range Ranges {
				ranges[i] = kit.Option{Value: r.Value, Label: r.Label}
			}
			if kit.Select(c, "range", &w.rng, ranges, kit.SelectOptions{Label: "时间范围", Width: 124, Toolbar: true}).Changed() {
				w.resetPage()
			}
			available := len(w.availableFilters()) > 0
			if kit.IconButton(c, kit.SymSlidersHorizontal, "切换用量筛选", kit.IconButtonOptions{Active: w.sidebar && available, Disabled: !available}).Clicked() {
				w.sidebar = !w.sidebar
			}
			if kit.IconButton(c, kit.SymRotateCcw, "刷新用量", kit.IconButtonOptions{}).Clicked() {
				w.refresh()
			}
			q := w.query()
			summaryDisabled := w.working || w.data == nil || w.data.Groups == 0 || !ValidRange(q.From, q.To) || w.changingGroup()
			exportButton := kit.IconButton(c, kit.SymDownload, "导出用量 CSV", kit.IconButtonOptions{Disabled: w.working,
				Hint: "导出用量 CSV：全部保留明细包含所有时间和维度；当前分组汇总沿用当前筛选"})
			exportButton.PopoverMenuButton(func(m *ui.Menu) {
				if m.Item("全部保留明细 CSV").Disabled(w.working).Chosen() {
					w.exportCSV(true)
				}
				if m.Item("当前分组汇总 CSV").Disabled(summaryDisabled).Chosen() {
					w.exportCSV(false)
				}
			})
			if w.exportCancel != nil {
				if kit.IconButton(c, kit.SymX, "取消导出", kit.IconButtonOptions{}).Clicked() {
					w.exportCancel()
				}
			}
			clear := kit.IconButton(c, kit.SymTrash2, "清空用量历史", kit.IconButtonOptions{Disabled: w.working || w.data == nil})
			if clear.Clicked() {
				w.confirm.Open = true
			}
			kit.ConfirmPopover(c, clear, &w.confirm, kit.ConfirmOptions{
				Title:   "清空用量历史？",
				Message: "当前服务器保存的用量将被清空。已有连接从当前累计值继续记录，新流量会持续累计。",
				Confirm: "清空历史", Danger: true, Run: w.clearHistory,
			})
		})
	})
}

func (w *Workspace) customRange(c *ui.Context) {
	k := kit.T(c)
	// The fields show the range's instants in the display time zone.
	if zone := timefmt.Location().String(); zone != w.rangeZone {
		w.rangeZone = zone
		for _, f := range []struct {
			ms   int64
			text *string
		}{{w.startMS, &w.start}, {w.endMS, &w.end}} {
			if f.ms != invalidTime {
				*f.text = LocalDateTime(f.ms)
			}
		}
	}
	ui.Row(c).Padding(8, 16).Gap(16).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
		field := func(label, key string, value *string, ms *int64, name string) {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, label).FontSize(kit.FontTable).TextColor(k.Label3)
				ui.Box(c).Width(170).Children(func() {
					if kit.Input(c, key, value, "YYYY-MM-DDTHH:MM").Label(name).Changed() {
						*ms = parseLocal(*value)
						w.resetPage()
					}
				})
			})
		}
		field("开始", "start", &w.start, &w.startMS, "用量开始时间")
		field("结束", "end", &w.end, &w.endMS, "用量结束时间")
	})
}

func (w *Workspace) filterPane(c *ui.Context, dims []Dimension, width float32) {
	names := w.sourceNames()
	kit.SizedFilterPane(c, kit.FilterConnection, width, w.filtered(), w.clearFilters, func() {
		for _, d := range dims {
			var facets []api.UsageFacet
			if w.data != nil {
				facets = w.data.Facets[d.Value]
			}
			// A selected value the page no longer lists stays first.
			var missing []api.UsageFacet
			for _, value := range w.filters[d.Value] {
				if !slices.ContainsFunc(facets, func(f api.UsageFacet) bool { return f.Value == value }) {
					missing = append(missing, api.UsageFacet{Value: value})
				}
			}
			if len(missing) > 0 {
				facets = append(missing, facets...)
			}
			chosen := func(value string) bool { return w.filters.Has(d.Value, value) }
			kit.FacetGroup(c, d.Label, w.facets.Of(d.Value), len(facets),
				func(i int) string {
					if d.Value == "source" {
						return names.Text(facets[i].Value)
					}
					return Label(facets[i].Value, d.Value)
				},
				func(i int) bool { return chosen(facets[i].Value) },
				func(i int) {
					f := facets[i]
					detail := Bytes(f.Total())
					if chosen(f.Value) && f.Total() == 0 {
						detail = ""
					}
					ui.Box(c.Key(f.Value)).Children(func() {
						if d.Value == "source" {
							if kit.FilterOptionNamed(c, kit.FilterConnection, chosen(f.Value), f.Value, names.Name(f.Value), detail).Clicked() {
								w.toggleFilter(d.Value, f.Value)
							}
							return
						}
						var icon func()
						if d.Value == "process" {
							icon = func() { w.processIcon(c, f.Value, 20) }
						}
						if kit.FilterOptionIconDetail(c, kit.FilterConnection, chosen(f.Value), icon, Label(f.Value, d.Value), detail).Clicked() {
							w.toggleFilter(d.Value, f.Value)
						}
					})
				})
		}
	})
}

func (w *Workspace) content(c *ui.Context) {
	w.summary(c)
	if w.data != nil && w.data.Groups > 0 {
		w.trends(c)
	}
	if w.data != nil && w.data.Error != "" {
		ui.Box(c).Padding(0, 10, 8).Shrink(0).Children(func() { kit.Notice(c, kit.WarningTone, "", w.data.Error, nil) })
	}
	ui.Column(c).Grow(1).MinHeight(0).PaddingX(10).Children(func() { w.list(c) })
}

func (w *Workspace) summary(c *ui.Context) {
	k := kit.T(c)
	var t *api.UsageCounters
	if w.data != nil {
		t = &w.data.Totals
	}
	value := func(fn func(api.UsageCounters) string) string {
		if t == nil {
			return "—"
		}
		return fn(*t)
	}
	stats := [][2]string{
		{"总用量", value(func(t api.UsageCounters) string { return Bytes(t.Total()) })},
		{"下载", value(func(t api.UsageCounters) string { return Bytes(t.Download) })},
		{"上传", value(func(t api.UsageCounters) string { return Bytes(t.Upload) })},
		{"新建连接", value(func(t api.UsageCounters) string { return Number(t.Connections) })},
	}
	ui.Row(c).Padding(16, 20, 12).Gap(16).Shrink(0).Label("所选范围汇总").Children(func() {
		for _, s := range stats {
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, s[0]).FontSize(kit.FontCaption).TextColor(k.Label3)
				ui.Text(c, s[1]).FontSize(20).FontWeight(600).FontFeatures("tnum").LetterSpacing(-0.5).SingleLine()
			})
		}
	})
}

func (w *Workspace) trends(c *ui.Context) {
	d := w.data
	chart := func(label string, tone kit.ChartTone, total uint64, pick func(api.UsageCounters) uint64) {
		values := make([]*float64, len(d.Trend))
		labels := make([]string, len(d.Trend))
		for i, item := range d.Trend {
			values[i] = kit.Sample(float64(pick(item.UsageCounters)))
			labels[i] = Date(item.Time) + " · " + Bytes(pick(item.UsageCounters))
		}
		kit.DotChart(c, values, kit.DotChartOptions{Label: label, Value: Bytes(total), Tone: tone,
			Columns: max(1, len(d.Trend)), SampleLabels: labels}).Grow(1).Basis(0)
	}
	ui.Row(c).Padding(0, 20, 16).Gap(12).Shrink(0).Label("所选用量趋势").Children(func() {
		chart("下载用量", kit.ChartBlue, d.Totals.Download, func(u api.UsageCounters) uint64 { return u.Download })
		chart("上传用量", kit.ChartGreen, d.Totals.Upload, func(u api.UsageCounters) uint64 { return u.Upload })
	})
}

// sortColumns maps the table's sortable columns to the server's keys.
var sortColumns = map[string]bool{"label": true, "download": true, "upload": true, "total": true, "connections": true}

func (w *Workspace) list(c *ui.Context) {
	k := kit.T(c)
	group, secondary := w.group, w.secondary
	if w.data != nil {
		group, secondary = w.dataGroup, w.dataSecondary
	}
	var rows []api.UsageGroup
	if w.data != nil {
		rows = w.data.Rows
	}
	columns := []kit.Column{{ID: "label", Title: DimensionLabel(group), MinWidth: 200, Sortable: true}}
	if secondary != "" {
		columns = append(columns, kit.Column{ID: "secondary", Title: DimensionLabel(secondary), MinWidth: 200})
	}
	columns = append(columns,
		kit.Column{ID: "download", Title: "下载", Width: 105, Sortable: true},
		kit.Column{ID: "upload", Title: "上传", Width: 105, Sortable: true},
		kit.Column{ID: "total", Title: "总用量", Width: 105, Sortable: true},
		kit.Column{ID: "connections", Title: "新建连接", Width: 105, Sortable: true},
		kit.Column{ID: "share", Title: "用量占比", Width: 120},
	)
	// The selection is ours: show it, and read the list's choice back.
	w.table.Selected = -1
	if w.selected != nil {
		for i, r := range rows {
			if r.Key == w.selected.Key {
				w.table.Selected = i
			}
		}
	}
	w.table.Sort = ui.SortOrder{Column: w.sortKey, Descending: !w.ascending}
	if w.scrollTop {
		w.table.List.ScrollTo(0, ui.Start)
		w.scrollTop = false
	}
	var totals api.UsageCounters
	if w.data != nil {
		totals = w.data.Totals
	}
	icon := dimensionIcon(group)
	names := w.sourceNames()
	// cellText is a group's value; a source address is followed by its
	// device's name.
	cellText := func(value, dimension string) {
		if dimension == "source" {
			kit.NamedCell(c, value, names.Name(value), "")
			return
		}
		kit.Cell(c, Label(value, dimension), "")
	}
	list := kit.Table(c, &w.table, columns, len(rows), kit.TableOptions{
		Label: "用量分组列表", Density: kit.Compact, Striped: true,
		Key:  func(row int) any { return rows[row].Key },
		Menu: w.freeze.Menu(func(menu *ui.Menu, row int) { w.rowMenu(c, menu, rows[row]) }),
		Empty: func() {
			if w.data == nil || w.loading {
				return
			}
			if w.filtered() {
				kit.EmptyState(c, kit.SymActivity, "没有符合筛选条件的用量", "调整时间范围或筛选查看记录。", func() {
					if kit.Button(c, "清除筛选", kit.ButtonOptions{Small: true}).Clicked() {
						w.clearFilters()
					}
				})
				return
			}
			kit.EmptyState(c, kit.SymActivity, "尚无用量记录", "核心运行期间会持续记录新的流量。", nil)
		},
	}, func(row, col int) {
		r := rows[row]
		id := columns[col].ID
		switch id {
		case "label":
			if group == "process" {
				w.processIcon(c, r.Label(0), 16)
			} else {
				ic := kit.Icon(c, icon, 15)
				if row != w.table.Selected {
					ic.TextColor(k.Label3)
				}
			}
			cellText(r.Label(0), group)
		case "secondary":
			if secondary == "process" {
				w.processIcon(c, r.Label(1), 16)
			}
			cellText(r.Label(1), secondary)
		case "download":
			kit.NumberCell(c, Bytes(r.Download))
		case "upload":
			kit.NumberCell(c, Bytes(r.Upload))
		case "total":
			kit.NumberCell(c, Bytes(r.Total()))
		case "connections":
			kit.NumberCell(c, Number(r.Connections))
		case "share":
			share := Share(r.UsageCounters, totals)
			selected := row == w.table.Selected
			ui.Box(c).Grow(1).Height(4).MinWidth(20).Draw(func(p *ui.Painter, b ui.Rect) {
				track, fill := k.Fill, k.Accent
				if selected {
					fill = k.OnAccent
				}
				p.Fill(b, track, 2)
				if share > 0 {
					p.Fill(ui.Rect{X: b.X, Y: b.Y, W: b.W * float32(share) / 100, H: b.H}, fill, 2)
				}
			})
			t := ui.Text(c, Percent(share)).FontSize(10).FontFeatures("tnum").MinWidth(38).TextAlign(ui.End)
			if !selected {
				t.TextColor(k.Label3)
			}
		}
	})
	w.freeze.End(c)
	if list.Changed() {
		if i, ok := w.table.Chosen(len(rows)); ok {
			w.openRow(rows[i])
		} else if w.selected != nil {
			w.selected = nil
		}
	}
	if w.table.Sort.Column != w.sortKey || w.table.Sort.Descending == w.ascending {
		if sortColumns[w.table.Sort.Column] {
			w.changeSort(w.table.Sort.Column)
		}
	}
	if w.selected != nil && list.Shortcut(0, ui.KeyEscape) {
		w.selected = nil
	}
}

func (w *Workspace) inspector(c *ui.Context) {
	k := kit.T(c)
	sel := *w.selected
	icon := dimensionIcon(w.group)
	kit.Inspector(c, Label(sel.Label(0), w.group), DimensionLabel(w.group)+" · "+Bytes(sel.Total()),
		func() {
			if w.group == "process" {
				w.processIcon(c, sel.Label(0), 40)
				return
			}
			kit.Icon(c, icon, 22).TextColor(k.Label3)
		}, nil, func() { w.selected = nil }, func() {
			kit.Section(c, "", nil, func() {
				w.dimensionProperty(c, w.group, sel.Label(0))
				if w.secondary != "" {
					w.dimensionProperty(c, w.secondary, sel.Label(1))
				}
				kit.Property(c, "下载", Bytes(sel.Download))
				kit.Property(c, "上传", Bytes(sel.Upload))
				kit.Property(c, "新建连接", Number(sel.Connections))
			})
			ui.Row(c).Gap(8).Margin(6, 0, 0).Children(func() {
				ui.Text(c, "交叉明细").FontSize(kit.FontTable).FontWeight(600).Grow(1)
				var options []kit.Option
				for _, d := range Dimensions {
					if d.Value != w.group && d.Value != w.secondary {
						options = append(options, kit.Option{Value: d.Value, Label: d.Plural})
					}
				}
				if kit.Select(c, "detail", &w.detailGroup, options, kit.SelectOptions{Label: "明细分组", Width: 120}).Changed() {
					w.detail = nil
				}
			})
			if w.detailErr != "" {
				kit.Notice(c, kit.DangerTone, "", w.detailErr, nil)
			}
			if w.detail != nil {
				names := w.sourceNames()
				ui.Column(c).Shrink(0).Children(func() {
					for _, row := range w.detail.Rows {
						label := Label(row.Label(0), w.detailGroup)
						name := ""
						if w.detailGroup == "source" {
							name = names.Name(row.Label(0))
						}
						b := ui.ButtonBase(c.Key(row.Key)).PaddingY(8).Gap(8).FontSize(kit.FontTable).
							BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Label("筛选 " + row.Label(0)).Transition(kit.HoverMotion)
						if b.Hovered() {
							b.Background(k.Fill)
						}
						b.Children(func() {
							if w.detailGroup == "process" {
								w.processIcon(c, row.Label(0), 16)
							}
							if name == "" {
								ui.Text(c, label).Grow(1).MinWidth(0).Wrap()
							} else {
								ui.RichText(c, ui.Span{Text: label}, ui.Span{Text: "  " + name, Color: k.Label2}).Grow(1).MinWidth(0).Wrap()
							}
							ui.Text(c, Bytes(row.Total())).FontFeatures("tnum").FontWeight(500).TextColor(k.Label2).Shrink(0)
						})
						if b.Clicked() {
							w.drill(row)
						}
					}
					ui.Text(c, Number(uint64(w.detail.Groups))+" 个分组 · 显示前 50 项").FontSize(kit.FontCaption).
						TextColor(k.Label3).Margin(12, 0, 0)
				})
			}
			if kit.Button(c, "筛选此项并查看明细", kit.ButtonOptions{}).FillWidth().Margin(8, 0, 0).Clicked() {
				w.filters, w.sidebar = w.selectedFilters(), true
				w.changeGroup(w.detailGroup)
				w.secondary = ""
			}
			values := w.rowValues(sel)
			_, mapped := connectionsView(values)
			target := ruleTarget(w.withFilters(values))
			ui.Row(c).Gap(8).Children(func() {
				if kit.Button(c, "在连接中查看", kit.ButtonOptions{Small: true, Icon: kit.SymCable, Disabled: !mapped}).Grow(1).Clicked() {
					w.showConnections(values)
				}
				kit.Button(c, "添加到规则…", kit.ButtonOptions{Small: true, Icon: kit.SymRoute, Disabled: !ruleactions.Available(w.store, target)}).
					Grow(1).PopoverMenuButton(func(menu *ui.Menu) { ruleactions.Items(menu, w.env, w.store, target) })
			})
		})
}

// dimensionProperty is a value of the selected group; a rule of the route
// table links to its editor in Routing.
func (w *Workspace) dimensionProperty(c *ui.Context, dimension, value string) {
	if dimension == "rule" && w.data != nil {
		if link, ok := w.data.RuleLinks[value]; ok && link.ID != "" {
			target := env.RouteRule{ID: link.ID, Owner: link.Owner, Section: link.Section}
			kit.LinkProperty(c, DimensionLabel(dimension), value, "在路由中查看规则 "+value, func() { w.env.Navigate(env.PageRoute, target) })
			return
		}
	}
	if dimension == "source" {
		kit.NamedProperty(c, DimensionLabel(dimension), value, w.sourceNames().Name(value))
		return
	}
	if dimension == "process" {
		kit.PropertyWith(c, DimensionLabel(dimension), func() {
			w.processIcon(c, value, 16)
			ui.Text(c, value).FontSize(kit.FontTable).Selectable().Grow(1).MinWidth(0).Wrap()
		})
		return
	}
	kit.Property(c, DimensionLabel(dimension), value)
}

func (w *Workspace) footer(c *ui.Context, q Query, k *kit.Tokens) {
	ui.Row(c).Height(32).PaddingX(16).Gap(12).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).
		FontSize(kit.FontCaption).TextColor(k.Label3).Children(func() {
		ui.Spacer(c)
		if width, _ := c.Size(); width > 1100 {
			ui.Text(c, rangeText(q)).SingleLine().FontFeatures("tnum")
		}
		pages := 1
		if w.data != nil {
			pages = max(1, (w.data.Groups+pageSize-1)/pageSize)
		}
		page := min(w.page, pages-1)
		kit.Pagination(c, &page, pages)
		if page != min(w.page, pages-1) {
			w.page, w.selected, w.scrollTop = page, nil, true
		}
	})
}

func rangeText(q Query) string {
	date := func(ms int64) string {
		if ms == invalidTime {
			return "—"
		}
		return Date(ms)
	}
	return date(q.From) + " — " + date(q.To)
}

// processIcon shows the icon of a process value (its path) from the
// console's shared store, or the glyph of its kind.
func (w *Workspace) processIcon(c *ui.Context, value string, size float32) {
	procicon.View(c, w.icons, procicon.FromPath(value), procicon.Options{Size: size})
}
