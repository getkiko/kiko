package routes

import "github.com/getkiko/kiko/app/mygo/internal/kit"

// Preference keys of the tables' column orders, lists of column IDs, and
// of their column widths, by column ID.
const (
	prefTrafficColumns = "kiko-routes-traffic-column-order"
	prefDNSColumns     = "kiko-routes-dns-column-order"
	prefTrafficWidths  = "kiko-routes-traffic-column-widths"
	prefDNSWidths      = "kiko-routes-dns-column-widths"
)

// columnIDs are the columns' IDs of the section's table in default order,
// as columns lists them.
func columnIDs(s section) []string {
	ids := []string{"index", "action", "name", "match", "target"}
	if s == sectionDNS {
		ids = append(ids, "query")
	}
	return ids
}

// columnOrder is the column order of the current table, loaded from the
// preferences on first use. Each table keeps its own, apart from the order
// of the rules.
func (w *Workspace) columnOrder() *kit.ColumnOrder {
	i, key := 0, prefTrafficColumns
	if w.sec == sectionDNS {
		i, key = 1, prefDNSColumns
	}
	if w.orders[i] == nil {
		o := &kit.ColumnOrder{}
		w.env.Prefs.Load(key, &o.Order)
		w.orders[i] = o
	}
	return w.orders[i]
}

// saveColumnOrder keeps the current table's column order.
func (w *Workspace) saveColumnOrder() {
	key := prefTrafficColumns
	if w.sec == sectionDNS {
		key = prefDNSColumns
	}
	w.env.Prefs.Save(key, w.columnOrder().Order)
}

// columnWidths are the column widths of the current table, loaded from the
// preferences on first use. Each table keeps its own, by column ID, apart
// from its column order: a width moves with its column.
func (w *Workspace) columnWidths() *kit.ColumnWidths {
	i, key := 0, prefTrafficWidths
	if w.sec == sectionDNS {
		i, key = 1, prefDNSWidths
	}
	if w.widths[i] == nil {
		cw := &kit.ColumnWidths{}
		w.env.Prefs.Load(key, &cw.Widths)
		w.widths[i] = cw
	}
	return w.widths[i]
}

// saveColumnWidths keeps the current table's column widths.
func (w *Workspace) saveColumnWidths() {
	key := prefTrafficWidths
	if w.sec == sectionDNS {
		key = prefDNSWidths
	}
	w.env.Prefs.Save(key, w.columnWidths().Widths)
}

// orderView binds the current table's column order and widths for the
// frame: its reset restores both.
func (w *Workspace) orderView() kit.OrderView {
	return w.columnOrder().View(columnIDs(w.sec), w.saveColumnOrder).WithWidths(w.columnWidths(), w.saveColumnWidths)
}
