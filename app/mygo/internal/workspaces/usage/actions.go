package usage

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
)

// This file holds the group list's context menu and the links of the
// inspector to Connections, Processes and Routing.

// connectionFacets are the dimensions the connection monitor filters by
// in its filter pane; connectionSearch those it finds by its search.
var (
	connectionFacets = map[string]bool{"source": true, "process": true, "outbound": true, "inbound": true, "rule": true, "network": true, "protocol": true}
	connectionSearch = []string{"host", "rootDomain", "destination", "user"}
)

// facetValue is a usage value as the connection monitor's filter pane
// lists it: the rule route.final carries is "Default Outbound", a
// connection without a sniffed protocol "Unrecognized".
func facetValue(value, dimension string) string {
	switch {
	case dimension == "rule" && value == "—":
		return "默认出口"
	case dimension == "process" && value == "—":
		return "未知进程"
	case dimension == "protocol" && value == "—":
		return "未识别"
	case dimension == "protocol" && value == "BITTORRENT":
		return "BitTorrent"
	}
	return Label(value, dimension)
}

// connectionsView is the connection monitor's view of usage values by
// dimension: a process by its file name, as the monitor lists it, the
// facets as filters and the first domain, address, rule, user or
// protocol as the search. ok is false when no value maps.
func connectionsView(values map[string]string) (v env.ConnectionsView, ok bool) {
	v.Filter = map[string][]string{}
	for dimension, value := range values {
		if value == "" || !connectionFacets[dimension] {
			continue
		}
		v.Filter[dimension] = []string{facetValue(value, dimension)}
		ok = true
	}
	for _, dimension := range connectionSearch {
		if value := values[dimension]; value != "" && value != "—" {
			v.Search, ok = value, true
			break
		}
	}
	return v, ok
}

// rowValues are a group's values by dimension.
func (w *Workspace) rowValues(row api.UsageGroup) map[string]string {
	values := map[string]string{w.group: row.Label(0)}
	if w.secondary != "" {
		values[w.secondary] = row.Label(1)
	}
	return values
}

// ruleTarget is what "Add to Rule" offers of usage values: a domain, an
// address, a port or a process path; the source names the device.
func ruleTarget(values map[string]string) ruleactions.Target {
	known := func(dimension string) string {
		if value := values[dimension]; value != "—" {
			return value
		}
		return ""
	}
	t := ruleactions.Target{Domain: firstNonEmpty(known("host"), known("rootDomain")), IP: known("destination"), SourceIP: known("source")}
	if port, err := strconv.Atoi(known("port")); err == nil {
		t.Port = port
	}
	if process := known("process"); process != "" && process != "未知进程" {
		t.ProcessName = Label(process, "process")
		if t.ProcessName != process {
			t.ProcessPath = process
		}
	}
	return t
}

// showConnections opens the connection monitor on values.
func (w *Workspace) showConnections(values map[string]string) {
	if v, ok := connectionsView(values); ok {
		w.env.Navigate(env.PageConnections, v)
	}
}

// rowMenu is the context menu of a group of the list.
func (w *Workspace) rowMenu(c *ui.Context, menu *ui.Menu, row api.UsageGroup) {
	values := w.rowValues(row)
	value := row.Label(0)
	_, mapped := connectionsView(values)
	if menu.Item("在连接中查看").Disabled(!mapped).Chosen() {
		w.showConnections(values)
	}
	if w.group == "process" {
		name := Label(value, "process")
		if menu.Item("查看进程").Disabled(value == "" || value == "未知进程").Chosen() {
			w.env.Navigate(env.PageProcesses, name)
		}
	}
	menu.Separator()
	if menu.Item("筛选此项").Checked(w.filters.IsOnly(w.group, value)).Chosen() {
		w.onlyFilter(w.group, value)
	}
	if w.selected == nil || w.selected.Key != row.Key {
		if menu.Item("查看明细").Chosen() {
			w.openRow(row)
		}
	}
	w.exitTests(menu, w.withFilters(values))
	menu.Separator()
	ruleactions.Menu(menu, w.env, w.store, ruleTarget(w.withFilters(values)))
	menu.Submenu("复制", func(sub *ui.Menu) {
		for _, dimension := range []string{w.group, w.secondary} {
			if v := values[dimension]; dimension != "" {
				if sub.Item(DimensionLabel(dimension)).Disabled(v == "" || v == "—").Chosen() {
					c.WriteClipboard(v)
				}
			}
		}
	})
}

// exitTests adds to a group's menu, after a separator, "Test Exit": testing
// the server its values' outbound or chain ends at (nodeview.ExitTests),
// a group there brought down to its server. Groups by other dimensions
// have no single exit.
func (w *Workspace) exitTests(menu *ui.Menu, values map[string]string) {
	if w.store == nil {
		return
	}
	name := nodeview.LastHop(values["outbound"])
	if name == "" {
		name = nodeview.LastHop(values["chain"])
	}
	if name == "" {
		return
	}
	x := w.exits.Of(w.store, nodeview.LiveGroups(w.env))
	nodeview.ExitTests(menu, w.store, x, name, "", true, w.env.Toast)
}

// withFilters are values with the filters of the list that keep one
// value, which the groups share, as the source of a group of domains.
func (w *Workspace) withFilters(values map[string]string) map[string]string {
	next := make(map[string]string, len(w.filters)+len(values))
	for k, chosen := range w.filters {
		if len(chosen) == 1 {
			next[k] = chosen[0]
		}
	}
	for k, v := range values {
		next[k] = v
	}
	return next
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
