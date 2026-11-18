package connections

import (
	"fmt"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// Process is a process of the monitor, as connections group by it.
type Process = api.ConnectionProcess

// groupMode is how the connection list groups its rows; the server
// groups them.
type groupMode string

const (
	groupNone   groupMode = "none"
	groupApp    groupMode = "app"
	groupParent groupMode = "parent"
	// groupRootDomain and groupDomain group by registrable and by full
	// domain; connections without a domain share ipGroupKey.
	groupRootDomain groupMode = "rootDomain"
	groupDomain     groupMode = "domain"
)

// groupModes are the grouping menu's modes, in order.
var groupModes = []groupMode{groupNone, groupApp, groupParent, groupRootDomain, groupDomain}

// domainGroupKey reports whether key names a domain group, as the
// server keys them: the list may still show another mode's page.
func domainGroupKey(key string) bool {
	return key == ipGroupKey || strings.HasPrefix(key, "root:") || strings.HasPrefix(key, "host:")
}

// ipGroupKey is the key of the domain group of the connections without a
// domain, as the server names it.
const ipGroupKey = "ip"

// groupTotals are a group's summed rates and amounts.
type groupTotals struct {
	UpRate, DownRate   float64
	UpTotal, DownTotal int64
}

// node is a row of the connection list: a connection or a group, as the
// page of the server lists them.
type node struct {
	Key string
	// Row is set for a connection.
	Row *Row

	Label   string
	Process Process
	// Children are those the server sent: an expanded group's first ones,
	// Remaining more.
	Children     []*node
	Remaining    int
	Count        int
	Totals       groupTotals
	Users        []string
	Rules        int
	Chains       int
	CreatedAt    int64
	ProcessCount int
}

func (n *node) group() bool { return n.Row == nil }

func leafNode(row *Row) *node { return &node{Key: "connection:" + row.Conn.ID, Row: row} }

// rowOf is the row of a connection of the server's page.
func rowOf(r api.ConnectionRow) *Row {
	return &Row{Conn: r.Connection, UpRate: r.UpRate, DownRate: r.DownRate, PeakUp: r.PeakUpRate, PeakDown: r.PeakDownRate,
		UpTotal: int64(r.UplinkTotal), DownTotal: int64(r.DownlinkTotal), ClosedAt: max(int64(r.ClosedAt), 0), CloseReason: r.CloseReason}
}

// groupNode is a group of the server's page with its children.
func groupNode(g api.ConnectionGroup) *node {
	n := &node{Key: g.Key, Label: g.Label, Process: g.Process, Remaining: g.Remaining, Count: g.Count,
		Totals: groupTotals{UpRate: g.UpRate, DownRate: g.DownRate, UpTotal: int64(g.UplinkTotal), DownTotal: int64(g.DownlinkTotal)},
		Users:  g.Users, Rules: g.Rules, Chains: g.Chains, CreatedAt: int64(g.CreatedAt), ProcessCount: g.ProcessCount}
	for _, child := range g.Children {
		switch {
		case child.Group != nil:
			n.Children = append(n.Children, groupNode(*child.Group))
		case child.Row != nil:
			n.Children = append(n.Children, leafNode(rowOf(*child.Row)))
		}
	}
	return n
}

// pageNodes are the top-level lines of the server's page.
func pageNodes(page *api.ConnectionPage) []*node {
	var roots []*node
	for _, g := range page.Groups {
		roots = append(roots, groupNode(g))
	}
	for _, r := range page.Rows {
		roots = append(roots, leafNode(rowOf(r)))
	}
	return roots
}

// visibleNode is a line of the expanded tree: a node, or a "more" line
// that shows further children of ParentKey.
type visibleNode struct {
	Node  *node
	Depth int
	// More line.
	MoreKey, ParentKey string
	Remaining          int
}

// flattenConnectionTree lists the lines of the tree with the expanded
// groups open: the children the server sent and a "more" line for the
// rest.
func flattenConnectionTree(roots []*node, expanded func(key string) bool) []visibleNode {
	var result []visibleNode
	var add func(n *node, depth int)
	add = func(n *node, depth int) {
		result = append(result, visibleNode{Node: n, Depth: depth})
		if !n.group() || !expanded(n.Key) {
			return
		}
		for _, child := range n.Children {
			add(child, depth+1)
		}
		if n.Remaining > 0 {
			result = append(result, visibleNode{Depth: depth + 1, MoreKey: "more:" + n.Key, ParentKey: n.Key, Remaining: n.Remaining})
		}
	}
	for _, root := range roots {
		add(root, 0)
	}
	return result
}

// connectionGroupText is a group's summary of a column.
func connectionGroupText(group *node, column Column) string {
	switch column {
	case colHost:
		return group.Label
	case colRule:
		return fmt.Sprintf("%d 条规则 · %d 条链路", group.Rules, group.Chains)
	case colUser:
		switch {
		case len(group.Users) > 1:
			return fmt.Sprintf("多项 · %d", len(group.Users))
		case len(group.Users) == 1:
			return group.Users[0]
		}
	}
	return "—"
}

// indexNodes maps every node of the tree by key.
func indexNodes(roots []*node) map[string]*node {
	result := map[string]*node{}
	var visit func(n *node)
	visit = func(n *node) {
		result[n.Key] = n
		for _, child := range n.Children {
			visit(child)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return result
}
