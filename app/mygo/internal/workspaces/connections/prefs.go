package connections

import "slices"

// Preference keys of the Connections workspace.
const (
	prefColumns     = "kiko-connections-columns"
	prefColumnOrder = "kiko-connections-column-order"
	prefGroupMode   = "kiko-connections-group-mode"
	prefConnSidebar = "kiko-connections-sidebar"
	prefDNSDetailed = "kiko-dns-detailed"
	prefDNSSidebar  = "kiko-dns-sidebar"

	prefConnFilterWidth = "kiko-connections-filter-width"
	prefDNSFilterWidth  = "kiko-dns-filter-width"

	prefConnColumnWidths = "kiko-connections-column-widths"
	prefDNSColumnWidths  = "kiko-dns-column-widths"

	prefHTTPGrouping = "kiko-http-grouping"
	prefDNSGrouping  = "kiko-dns-grouping"
)

// validColumns keeps the known columns of saved, once each.
func validColumns(saved []Column) []Column {
	var out []Column
	for _, col := range saved {
		if slices.Contains(allColumns, col) && !slices.Contains(out, col) {
			out = append(out, col)
		}
	}
	return out
}

// loadPrefs restores the columns, their order, the grouping mode and the
// filter pane, as loadColumns, loadColumnOrder and loadGroupMode do.
func (m *connMonitor) loadPrefs() {
	prefs := m.env.Prefs
	var columns []Column
	if prefs.Load(prefColumns, &columns) {
		if valid := validColumns(columns); len(valid) > 0 {
			m.columns = valid
		}
	}
	var order []Column
	if prefs.Load(prefColumnOrder, &order) {
		m.columnOrder = validColumns(append(order, allColumns...))
	} else {
		m.columnOrder = append(slices.Clone(m.columns), restColumns(m.columns)...)
	}
	var mode groupMode
	if prefs.Load(prefGroupMode, &mode) && slices.Contains(groupModes, mode) {
		m.mode = mode
	}
	var sidebar bool
	if prefs.Load(prefConnSidebar, &sidebar) {
		m.sidebar = &sidebar
	}
	prefs.Load(prefConnFilterWidth, &m.filterWidth.Width)
	prefs.Load(prefConnColumnWidths, &m.table.Widths)
	if !slices.Contains(m.columns, colCreated) {
		m.sortCol = m.columns[0]
		m.table.Sort.Column, m.table.Sort.Descending = string(m.sortCol), m.sortCol.descendingByDefault()
	}
}

func (m *connMonitor) saveColumns() {
	m.env.Prefs.Save(prefColumns, m.columns)
	m.env.Prefs.Save(prefColumnOrder, m.columnOrder)
}

func (m *dnsMonitor) loadPrefs() {
	m.env.Prefs.Load(prefDNSDetailed, &m.detailed)
	m.loadGrouping()
	m.env.Prefs.Load(prefDNSFilterWidth, &m.filterWidth.Width)
	m.env.Prefs.Load(prefDNSColumnWidths, &m.table.Widths)
	var sidebar bool
	if m.env.Prefs.Load(prefDNSSidebar, &sidebar) {
		m.sidebar = &sidebar
	}
}
