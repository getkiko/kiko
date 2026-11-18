package connections

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// dnsQueryName is the name a query groups by, its first question's.
func dnsQueryName(q *api.DNSQuery) string {
	if len(q.Questions) == 0 {
		return ""
	}
	return q.Questions[0].Name
}

// setGrouping groups the DNS list by mode and keeps the choice.
func (m *dnsMonitor) setGrouping(mode domainGrouping) {
	m.groups.setMode(mode)
	m.page = 0
	m.env.Prefs.Save(prefDNSGrouping, string(mode))
}

func (m *dnsMonitor) loadGrouping() {
	var mode string
	if m.env.Prefs.Load(prefDNSGrouping, &mode) && slices.Contains(domainGroupings, domainGrouping(mode)) {
		m.groups.Mode = domainGrouping(mode)
	}
}

// groupingButton is the toolbar's grouping menu.
func (m *dnsMonitor) groupingButton(c *ui.Context) {
	kit.IconButton(c, kit.SymLayers, domainGroupingTitles[m.groups.Mode], kit.IconButtonOptions{Active: m.groups.grouped()}).PopoverMenuButton(func(menu *ui.Menu) {
		groupingMenu(menu, "查询分组", &m.groups, m.setGrouping)
	})
}

func (m *dnsMonitor) groupOrder() groupOrder {
	if m.sortCol == "domain" {
		return groupByLabel
	}
	return groupByFirst
}

// groupPages are the pages of the grouped list, a page of groups each.
func (m *dnsMonitor) groupPages(d *dnsDerived) int {
	groups := m.groups.regroup(d.rows, dnsQueryName, nil, m.groupOrder(), m.table.Sort.Descending)
	return max(1, (len(groups)+dnsPageSize-1)/dnsPageSize)
}

// groupLines are the lines of the page's groups.
func (m *dnsMonitor) groupLines(d *dnsDerived) []groupLine[*api.DNSQuery] {
	return m.groups.build(d.rows, dnsQueryName, nil, m.groupOrder(), m.table.Sort.Descending, m.page*dnsPageSize, (m.page+1)*dnsPageSize)
}

// selectedGroup is the group the inspector shows, nil without one.
func (m *dnsMonitor) selectedGroup() *rowGroup[*api.DNSQuery] {
	if !m.groups.grouped() || m.groups.Selected == "" || m.selected != "" || !m.inspector || !m.active {
		return nil
	}
	return m.groups.group(m.groups.Selected)
}

// groupedList is the list grouped by query name.
func (m *dnsMonitor) groupedList(c *ui.Context, d *dnsDerived) {
	lines := m.groupLines(d)
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
	list := kit.Table(c, &m.table, dnsColumns, len(lines), kit.TableOptions{
		Resizable: true, OnResize: func() { m.env.Prefs.Save(prefDNSColumnWidths, m.table.Widths) },
		Label: "DNS 查询记录", Density: density, Striped: true, KeepOffset: true,
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
			m.exitTests(menu, lines[row].Row)
		}),
		HasMenu: func(row int) bool { return lines[row].Group != nil || m.exitTest(lines[row].Row) != "" },
	}, func(row, col int) {
		line := lines[row]
		id := dnsColumns[col].ID
		if line.Group != nil {
			if id == "domain" {
				groupTitle(c, &m.groups, line.Group)
			} else {
				emptyCell(c)
			}
			return
		}
		if id == "domain" {
			groupChild(c, true, func() { m.cell(c, line.Row, id) })
			return
		}
		m.cell(c, line.Row, id)
	})
	m.freeze.End(c)
	if m.table.Sort.Column != m.sortCol {
		m.sortCol = m.table.Sort.Column
		m.table.Sort.Descending = m.sortCol == "started_at"
		m.page = 0
	}
	if list.Changed() {
		if i, ok := m.table.Chosen(len(lines)); ok {
			if g := lines[i].Group; g != nil {
				m.selected, m.groups.Selected, m.inspector = "", g.Key, true
			} else {
				m.groups.Selected = ""
				m.selectQuery(lines[i].Row.ID)
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
func (m *dnsMonitor) groupMenu(c *ui.Context, menu *ui.Menu, g *rowGroup[*api.DNSQuery]) {
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

// dnsGroupSummary is what a group's inspector shows.
type dnsGroupSummary struct {
	names, failed, cached int
	first, last           string
}

func dnsSummary(g *rowGroup[*api.DNSQuery]) dnsGroupSummary {
	if s, ok := g.summary.(dnsGroupSummary); ok {
		return s
	}
	var s dnsGroupSummary
	names := map[string]bool{}
	for _, q := range g.Rows {
		names[dnsQueryName(q)] = true
		if dnsFailed(q) {
			s.failed++
		}
		if dnsCached(q) {
			s.cached++
		}
		if q.StartedAt != "" && (s.first == "" || q.StartedAt < s.first) {
			s.first = q.StartedAt
		}
		if q.StartedAt > s.last {
			s.last = q.StartedAt
		}
	}
	s.names = len(names)
	g.summary = s
	return s
}

// groupInspector shows a domain group's summary.
func (m *dnsMonitor) groupInspector(c *ui.Context, g *rowGroup[*api.DNSQuery]) {
	s := dnsSummary(g)
	subtitle := fmt.Sprintf("%s 次查询", kit.Count(len(g.Rows)))
	icon := func() { domainGroupIcon(c, g.ip(), 40) }
	kit.ScrollingInspector(c.Key("group:"+g.Key), g.Label, subtitle, icon, nil, m.closeInspector, func() {
		kit.Section(c, "汇总", nil, func() {
			if m.groups.Mode == domainRoot || g.ip() {
				property(c, "域名", kit.Count(s.names))
			}
			property(c, "异常", kit.Count(s.failed))
			property(c, "缓存", kit.Count(s.cached))
			property(c, "最早查询", dnsTime(s.first))
			property(c, "最近查询", dnsTime(s.last))
		})
	})
}
