package connections

import (
	"net"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
	"golang.org/x/net/idna"
)

// domainGrouping is how the HTTP and DNS lists group their rows: flat,
// by registrable domain (eTLD+1) or by full domain.
type domainGrouping string

const (
	domainFlat domainGrouping = ""
	domainRoot domainGrouping = "rootDomain"
	domainHost domainGrouping = "domain"
)

var domainGroupings = []domainGrouping{domainFlat, domainRoot, domainHost}

var domainGroupingTitles = map[domainGrouping]string{domainFlat: "平铺显示", domainRoot: "按主域名", domainHost: "按域名"}

// ipGroupLabel names the group of the rows without a domain.
const ipGroupLabel = "IP 地址"

// domainGroupOf is the group of host under g: its key and label. Rows
// without a domain, or with an IP address, share the IP group.
func domainGroupOf(g domainGrouping, host string) (key, label string) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" || net.ParseIP(host) != nil {
		return ipGroupKey, ipGroupLabel
	}
	// As the server and Usage name domains: in ASCII (punycode).
	if ascii, err := idna.Lookup.ToASCII(host); err == nil {
		host = strings.TrimSuffix(ascii, ".")
	}
	if g == domainRoot {
		host = ruleactions.RootDomain(host)
		return "root:" + host, host
	}
	return "host:" + host, host
}

// domainGroupIcon is the glyph of a domain group, a network glyph for the
// IP group.
func domainGroupIcon(c *ui.Context, ip bool, size float32) {
	symbol, scale := kit.SymGlobe, float32(0.85)
	if ip {
		symbol, scale = kit.SymNetwork, 0.7
	}
	ui.Box(c).Size(size, size).Center().Shrink(0).TextColor(kit.T(c).Label3).Children(func() { kit.Icon(c, symbol, size*scale) })
}

// rowGroup is a domain group of a list.
type rowGroup[T any] struct {
	Key, Label string
	Rows       []T
	// Sum is the rows' summed weight, such as their bytes.
	Sum int64
	// summary is what the inspector shows of the group, computed once:
	// a regroup makes new groups.
	summary any
}

func (g *rowGroup[T]) ip() bool { return g.Key == ipGroupKey }

// groupLine is a line of a grouped list: a group's title or a row.
type groupLine[T any] struct {
	Group *rowGroup[T]
	Row   T
}

// groupOrder orders the groups: by label, by sum, or (groupByFirst) where
// their first row stands in the sorted rows.
type groupOrder int

const (
	groupByFirst groupOrder = iota
	groupByLabel
	groupBySum
)

// domainGroups keeps a list's grouping, its open groups and its selected
// group, and the lines it last built.
type domainGroups[T comparable] struct {
	Mode domainGrouping
	// open are the expanded groups, by key.
	open map[string]bool
	rev  int
	// Selected is the key of the selected group, "" for none.
	Selected string

	built, linesValid          bool
	builtRows                  []T
	builtMode                  domainGrouping
	builtOrder                 groupOrder
	builtDesc                  bool
	builtRev, builtLo, builtHi int
	groups                     []*rowGroup[T]
	lines                      []groupLine[T]
}

func (g *domainGroups[T]) grouped() bool { return g.Mode != domainFlat }

// setMode changes the grouping; the groups close and none stays selected.
func (g *domainGroups[T]) setMode(mode domainGrouping) {
	g.Mode, g.open, g.Selected = mode, nil, ""
	g.rev++
}

func (g *domainGroups[T]) expanded(key string) bool { return g.open[key] }

func (g *domainGroups[T]) setOpen(key string, open bool) {
	if g.open == nil {
		g.open = map[string]bool{}
	}
	g.open[key] = open
	g.rev++
}

// setAll opens or closes every group.
func (g *domainGroups[T]) setAll(open bool) {
	g.open = map[string]bool{}
	if open {
		for _, group := range g.groups {
			g.open[group.Key] = true
		}
	}
	g.rev++
}

// group is the group with key among those last built, nil when gone.
func (g *domainGroups[T]) group(key string) *rowGroup[T] {
	for _, group := range g.groups {
		if group.Key == key {
			return group
		}
	}
	return nil
}

// regroup groups rows, sorted already, by host, weighing each by weight
// (nil for none), unless the rows and the order are those last grouped.
// It returns the groups.
func (g *domainGroups[T]) regroup(rows []T, host func(T) string, weight func(T) int64, order groupOrder, descending bool) []*rowGroup[T] {
	same := g.built && len(rows) == len(g.builtRows) && (len(rows) == 0 || &rows[0] == &g.builtRows[0]) &&
		g.builtMode == g.Mode && g.builtOrder == order && g.builtDesc == descending
	if !same {
		g.groups = groupRows(rows, g.Mode, host, weight, order, descending)
		g.built, g.builtRows, g.builtMode, g.builtOrder, g.builtDesc = true, rows, g.Mode, order, descending
		g.linesValid = false
		if g.Selected != "" && g.group(g.Selected) == nil {
			g.Selected = ""
		}
	}
	return g.groups
}

// build lists the lines of the groups lo to hi (hi < 0 for all) of rows
// (regroup): each group's title, then its rows while it is open. It
// rebuilds only as the rows, the order or the open groups change.
func (g *domainGroups[T]) build(rows []T, host func(T) string, weight func(T) int64, order groupOrder, descending bool, lo, hi int) []groupLine[T] {
	g.regroup(rows, host, weight, order, descending)
	if g.linesValid && g.builtRev == g.rev && g.builtLo == lo && g.builtHi == hi {
		return g.lines
	}
	g.linesValid, g.builtRev, g.builtLo, g.builtHi = true, g.rev, lo, hi
	page := g.groups
	if hi >= 0 {
		page = page[min(lo, len(page)):min(hi, len(page))]
	}
	g.lines = nil
	for _, group := range page {
		g.lines = append(g.lines, groupLine[T]{Group: group})
		if g.open[group.Key] {
			for _, row := range group.Rows {
				g.lines = append(g.lines, groupLine[T]{Row: row})
			}
		}
	}
	return g.lines
}

// groupRows groups rows by the domain of host under mode, the rows of a
// group keeping their order.
func groupRows[T any](rows []T, mode domainGrouping, host func(T) string, weight func(T) int64, order groupOrder, descending bool) []*rowGroup[T] {
	var groups []*rowGroup[T]
	index := map[string]*rowGroup[T]{}
	for _, row := range rows {
		key, label := domainGroupOf(mode, host(row))
		group := index[key]
		if group == nil {
			group = &rowGroup[T]{Key: key, Label: label}
			index[key] = group
			groups = append(groups, group)
		}
		group.Rows = append(group.Rows, row)
		if weight != nil {
			group.Sum += weight(row)
		}
	}
	if order == groupByFirst {
		return groups
	}
	slices.SortStableFunc(groups, func(a, b *rowGroup[T]) int {
		var o int
		if order == groupBySum {
			o = int(sign(a.Sum - b.Sum))
		} else {
			o = collateCompare(a.Label, b.Label)
		}
		if descending {
			o = -o
		}
		return o
	})
	return groups
}

// groupingMenu fills the grouping menu of a list: its modes, then
// opening and closing every group.
func groupingMenu[T comparable](menu *ui.Menu, title string, g *domainGroups[T], choose func(domainGrouping)) {
	menu.Item(title).Disabled(true)
	for _, mode := range domainGroupings {
		if menu.Item(domainGroupingTitles[mode]).Checked(g.Mode == mode).Chosen() {
			choose(mode)
		}
	}
	if g.grouped() {
		menu.Separator()
		if menu.Item("展开全部").Chosen() {
			g.setAll(true)
		}
		if menu.Item("折叠全部").Chosen() {
			g.setAll(false)
		}
	}
}

// groupTitle is the title cell of a domain group: its disclosure, glyph,
// label and count.
func groupTitle[T comparable](c *ui.Context, g *domainGroups[T], group *rowGroup[T]) {
	k := kit.T(c)
	open := g.expanded(group.Key)
	ui.Row(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
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
			g.setOpen(group.Key, !open)
		}
		domainGroupIcon(c, group.ip(), 16)
		kit.Clipped(c, group.Label, kit.FontTable, 400)
		ui.Box(c).PaddingX(5).Radius(8).Background(k.Fill).TextColor(k.Label3).Shrink(0).Children(func() {
			ui.Text(c, kit.Count(len(group.Rows))).FontSize(10).FontWeight(600).FontFeatures("tnum")
		})
	})
}

// groupChild indents a row's first cell under its group.
func groupChild(c *ui.Context, grouped bool, fn func()) {
	if !grouped {
		fn()
		return
	}
	ui.Row(c).Grow(1).MinWidth(0).PaddingStart(26).Children(fn)
}

// groupFold is the menu item opening or closing a group.
func groupFold[T comparable](menu *ui.Menu, g *domainGroups[T], key string) {
	open := g.expanded(key)
	if menu.Item(map[bool]string{true: "折叠分组", false: "展开分组"}[open]).Chosen() {
		g.setOpen(key, !open)
	}
}

// emptyCell is a group's cell of a column it does not sum.
func emptyCell(c *ui.Context) { ui.Text(c, "-").TextColor(kit.T(c).Label4) }
