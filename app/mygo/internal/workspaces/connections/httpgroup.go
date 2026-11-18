package connections

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// setGrouping groups the HTTP list by mode and keeps the choice.
func (m *httpMonitor) setGrouping(mode domainGrouping) {
	m.groups.setMode(mode)
	m.env.Prefs.Save(prefHTTPGrouping, string(mode))
}

func (m *httpMonitor) loadGrouping() {
	var mode string
	if m.env.Prefs.Load(prefHTTPGrouping, &mode) && slices.Contains(domainGroupings, domainGrouping(mode)) {
		m.groups.Mode = domainGrouping(mode)
	}
}

// groupingButton is the toolbar's grouping menu.
func (m *httpMonitor) groupingButton(c *ui.Context) {
	kit.IconButton(c, kit.SymLayers, domainGroupingTitles[m.groups.Mode], kit.IconButtonOptions{Active: m.groups.grouped()}).PopoverMenuButton(func(menu *ui.Menu) {
		groupingMenu(menu, "请求分组", &m.groups, m.setGrouping)
	})
}

// groupLines are the lines of the grouped list over d's rows.
func (m *httpMonitor) groupLines(d *httpDerived) []groupLine[*httpRow] {
	order := groupByFirst
	switch m.sortCol {
	case "host":
		order = groupByLabel
	case "size":
		order = groupBySum
	}
	return m.groups.build(d.rows, func(r *httpRow) string { return r.Host }, func(r *httpRow) int64 { return r.ResponseBytes },
		order, m.table.Sort.Descending, 0, -1)
}

// selectedGroup is the group the inspector shows, nil without one.
func (m *httpMonitor) selectedGroup() *rowGroup[*httpRow] {
	if !m.groups.grouped() || m.groups.Selected == "" || m.selected != "" || !m.inspector || !m.active {
		return nil
	}
	return m.groups.group(m.groups.Selected)
}

// groupedColumns are the columns with the host first, where the groups
// show their titles.
func groupedColumns(columns []kit.Column, first string) []kit.Column {
	out := make([]kit.Column, 0, len(columns))
	for _, col := range columns {
		if col.ID == first {
			out = append(out, col)
		}
	}
	for _, col := range columns {
		if col.ID != first {
			out = append(out, col)
		}
	}
	return out
}

// groupedList is the list grouped by domain.
func (m *httpMonitor) groupedList(c *ui.Context, d *httpDerived) {
	lines := m.groupLines(d)
	columns := groupedColumns(httpColumns, "host")
	density := kit.Compact
	if m.detailed {
		density = kit.TwoLine
	}
	m.table.Selected = -1
	for i, line := range lines {
		if (line.Group != nil && m.selected == "" && line.Group.Key == m.groups.Selected) || (line.Group == nil && m.selected != "" && line.Row.ID == m.selected) {
			if m.inspector {
				m.table.Selected = i
			}
			break
		}
	}
	list := kit.Table(c, &m.table, columns, len(lines), kit.TableOptions{
		Resizable: true, OnResize: func() { m.env.Prefs.Save(prefHTTPColumnWidths, m.table.Widths) },
		Label: "HTTP 请求记录", Density: density, Striped: true, KeepOffset: true,
		Key: func(row int) any {
			if g := lines[row].Group; g != nil {
				return "group:" + g.Key
			}
			return lines[row].Row.ID
		},
		Menu: m.freeze.Menu(func(menu *ui.Menu, row int) {
			if g := lines[row].Group; g != nil {
				m.groupMenu(c, menu, g)
				return
			}
			m.rowMenu(c, menu, lines[row].Row)
		}),
	}, func(row, col int) {
		line := lines[row]
		id := columns[col].ID
		if line.Group != nil {
			switch id {
			case "host":
				groupTitle(c, &m.groups, line.Group)
			case "size":
				kit.NumberCell(c, kit.Bytes(float64(line.Group.Sum))).TextColor(kit.T(c).Label2)
			default:
				emptyCell(c)
			}
			return
		}
		if id == "host" {
			groupChild(c, true, func() { m.cell(c, line.Row, id) })
			return
		}
		m.cell(c, line.Row, id)
	})
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
		if i, ok := m.table.Chosen(len(lines)); ok {
			if g := lines[i].Group; g != nil {
				m.selected, m.groups.Selected, m.inspector = "", g.Key, true
			} else {
				m.groups.Selected = ""
				m.selectRow(lines[i].Row.ID)
			}
		}
	}
	if key := m.groups.Selected; key != "" && m.selected == "" {
		if list.Shortcut(0, ui.KeyRight) {
			m.groups.setOpen(key, true)
		}
		if list.Shortcut(0, ui.KeyLeft) {
			m.groups.setOpen(key, false)
		}
	}
	if list.Shortcut(0, ui.KeyEscape) {
		m.closeInspector()
	}
}

// groupMenu is the context menu of a domain group.
func (m *httpMonitor) groupMenu(c *ui.Context, menu *ui.Menu, g *rowGroup[*httpRow]) {
	groupFold(menu, &m.groups, g.Key)
	if g.ip() {
		return
	}
	menu.Separator()
	if menu.Item("查看此域名的用量").Chosen() {
		m.env.Navigate(env.PageUsage, domainUsageView(m.groups.Mode, g.Label))
	}
	copyItem(c, menu, "复制域名", g.Label)
}

// domainUsageView is the Usage view of a domain group: its hosts.
func domainUsageView(mode domainGrouping, domain string) env.UsageView {
	if mode == domainRoot {
		return env.UsageView{Group: "host", Filters: map[string]string{"rootDomain": domain}}
	}
	return env.UsageView{Group: "host", Filters: map[string]string{"host": domain}}
}

// httpGroupSummary is what a group's inspector shows.
type httpGroupSummary struct {
	hosts, problems int
	first, last     int64
}

func httpSummary(g *rowGroup[*httpRow]) httpGroupSummary {
	if s, ok := g.summary.(httpGroupSummary); ok {
		return s
	}
	var s httpGroupSummary
	hosts := map[string]bool{}
	for _, r := range g.Rows {
		hosts[r.Host] = true
		if r.problem() {
			s.problems++
		}
		if r.start > 0 && (s.first == 0 || r.start < s.first) {
			s.first = r.start
		}
		s.last = max(s.last, r.start)
	}
	s.hosts = len(hosts)
	g.summary = s
	return s
}

// groupInspector shows a domain group's summary.
func (m *httpMonitor) groupInspector(c *ui.Context, g *rowGroup[*httpRow]) {
	s := httpSummary(g)
	subtitle := fmt.Sprintf("%s 条请求", kit.Count(len(g.Rows)))
	icon := func() { domainGroupIcon(c, g.ip(), 40) }
	kit.ScrollingInspector(c.Key("group:"+g.Key), g.Label, subtitle, icon, nil, m.closeInspector, func() {
		kit.Section(c, "汇总", nil, func() {
			if m.groups.Mode == domainRoot || g.ip() {
				property(c, "主机", kit.Count(s.hosts))
			}
			property(c, "异常", kit.Count(s.problems))
			property(c, "响应大小", kit.Bytes(float64(g.Sum)))
			property(c, "最早请求", httpTime(s.first))
			property(c, "最近请求", httpTime(s.last))
		})
	})
}
