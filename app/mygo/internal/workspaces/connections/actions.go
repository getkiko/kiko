package connections

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
)

// This file holds the connection list's context menus and the links of
// the inspector to Processes, Usage and Routing.

// facetOf is a connection's value of a filter facet, as the server's
// filter pane lists it.
func facetOf(conn *api.Connection, f facet) string {
	switch f {
	case facetSource:
		return firstNonEmpty(sourceHost(conn.Source), "—")
	case facetProcess:
		return firstNonEmpty(processName(conn), "未知进程")
	case facetApp:
		return appName(conn)
	case facetPID:
		if conn.ProcessInfo == nil || conn.ProcessInfo.ProcessID <= 0 {
			return ""
		}
		return strconv.Itoa(conn.ProcessInfo.ProcessID)
	case facetInbound:
		return firstNonEmpty(conn.Inbound, "—")
	case facetOutbound:
		return firstNonEmpty(conn.Outbound, "—")
	}
	return firstNonEmpty(strings.ToUpper(conn.Network), "—")
}

// connProcess is what the monitor knows of a connection's process: its
// PID and path, with its App from the process snapshot.
func connProcess(d *connDerived, conn *api.Connection) Process {
	var p Process
	if info := conn.ProcessInfo; info != nil {
		p.PID, p.Path = info.ProcessID, info.ProcessPath
	}
	if known, ok := d.iconProcesses[p.PID]; ok && p.PID != 0 && (p.Path == "" || known.Path == p.Path) {
		p.Name, p.AppName, p.AppPath = known.Name, known.AppName, known.AppPath
		if p.Path == "" {
			p.Path = known.Path
		}
	}
	p.Name = firstNonEmpty(baseName(p.Path), p.Name)
	return p
}

// ruleTarget is what "Add to Rule" offers of a connection.
func ruleTarget(d *connDerived, conn *api.Connection) ruleactions.Target {
	p := connProcess(d, conn)
	return ruleactions.Target{
		Domain: conn.Domain, IP: sourceHost(conn.Destination), Port: destinationPort(conn.Destination),
		ProcessName: baseName(p.Path), ProcessPath: p.Path, AppPath: p.AppPath, AppName: p.AppName,
		SourceIP: sourceHost(conn.Source),
	}
}

// processUsage shows the usage of a process path by main domain.
func (m *connMonitor) processUsage(path string) {
	m.env.Navigate(env.PageUsage, env.UsageView{Group: "rootDomain", Filters: map[string]string{"process": path}})
}

// rowMenu is the context menu of a line of the list.
func (m *connMonitor) rowMenu(c *ui.Context, d *connDerived, menu *ui.Menu, v visibleNode) {
	switch {
	case v.Node == nil:
		return
	case v.Node.group():
		m.groupMenu(c, menu, v.Node)
		return
	}
	row := v.Node.Row
	conn := &row.Conn
	p := connProcess(d, conn)

	for _, f := range []facet{facetProcess, facetSource, facetOutbound} {
		value := facetOf(conn, f)
		if menu.Item(fmt.Sprintf("只看此%s（%s）", facetLabels[f], value)).Checked(m.filter.IsOnly(f, value)).Chosen() {
			m.onlyFilter(f, value)
		}
	}
	if app := appName(conn); app != "" {
		if menu.Item(fmt.Sprintf("只看此 App（%s 的全部进程）", app)).Checked(m.filter.IsOnly(facetApp, app)).Chosen() {
			m.onlyFilter(facetApp, app)
		}
	}
	if pid := facetOf(conn, facetPID); pid != "" {
		if menu.Item(fmt.Sprintf("只看此进程（PID %s）", pid)).Checked(m.filter.IsOnly(facetPID, pid)).Chosen() {
			m.onlyFilter(facetPID, pid)
		}
	}
	menu.Separator()
	if menu.Item("查看进程详情").Disabled(p.PID <= 0).Chosen() {
		m.env.Navigate(env.PageProcesses, p.PID)
	}
	if menu.Item("查看此进程的用量").Disabled(p.Path == "").Chosen() {
		m.processUsage(p.Path)
	}
	if menu.Item("查看此域名的用量").Disabled(conn.Domain == "").Chosen() {
		m.env.Navigate(env.PageUsage, env.UsageView{Group: "host", Filters: map[string]string{"rootDomain": ruleactions.RootDomain(conn.Domain)}})
	}
	m.exitTests(menu, conn)
	menu.Separator()
	ruleactions.Menu(menu, m.env, m.store, ruleTarget(d, conn))
	menu.Submenu("复制", func(sub *ui.Menu) {
		copyItem(c, sub, "主机", hostName(conn))
		copyItem(c, sub, "目标地址", conn.Destination)
		copyItem(c, sub, "进程路径", p.Path)
		copyItem(c, sub, "规则", ruleText(conn))
	})
	if !row.Closed() {
		menu.Separator()
		canTerminate := m.loaded && m.streamErr == "" && !m.paused && !m.busy
		if menu.Item("关闭连接").Disabled(!canTerminate).Chosen() {
			m.terminate(conn.ID)
		}
	}
}

// exitTests adds to a connection's menu, after a separator, "Test Exit":
// testing the server its chain ends at (nodeview.ExitTests), the last
// group on it brought down to its server where the chain stops at one.
func (m *connMonitor) exitTests(menu *ui.Menu, conn *api.Connection) {
	if m.store == nil || conn.Rejected() {
		return
	}
	name := nodeview.LastHop(conn.ChainList...)
	if name == "" {
		name = nodeview.LastHop(conn.Outbound)
	}
	x := m.exits.Of(m.store, nodeview.LiveGroups(m.env))
	nodeview.ExitTests(menu, m.store, x, name, "", true, m.env.Toast)
}

// groupMenu is the context menu of an App or process group.
func (m *connMonitor) groupMenu(c *ui.Context, menu *ui.Menu, group *node) {
	if domainGroupKey(group.Key) {
		m.domainGroupMenu(c, menu, group)
		return
	}
	p := group.Process
	name := firstNonEmpty(baseName(p.Path), p.Name)
	// An App group holds several processes: only the App keeps all of them.
	if p.AppPath != "" {
		app := bundleName(p.AppPath)
		if menu.Item(fmt.Sprintf("只看此 App（%s 的全部进程）", app)).Checked(m.filter.IsOnly(facetApp, app)).Chosen() {
			m.onlyFilter(facetApp, app)
		}
	}
	if name != "" {
		if menu.Item(fmt.Sprintf("只看此进程（%s）", name)).Checked(m.filter.IsOnly(facetProcess, name)).Chosen() {
			m.onlyFilter(facetProcess, name)
		}
	}
	open := m.isExpanded(group.Key)
	if menu.Item(map[bool]string{true: "折叠分组", false: "展开分组"}[open]).Chosen() {
		m.expanded()[group.Key] = !open
	}
	menu.Separator()
	if menu.Item("查看进程详情").Disabled(p.PID <= 0).Chosen() {
		m.env.Navigate(env.PageProcesses, p.PID)
	}
	if menu.Item("查看此进程的用量").Disabled(p.Path == "").Chosen() {
		m.processUsage(p.Path)
	}
	menu.Separator()
	ruleactions.Menu(menu, m.env, m.store, ruleactions.Target{ProcessName: baseName(p.Path), ProcessPath: p.Path, AppPath: p.AppPath, AppName: p.AppName})
	menu.Submenu("复制", func(sub *ui.Menu) {
		copyItem(c, sub, "名称", group.Label)
		copyItem(c, sub, "进程路径", p.Path)
		copyItem(c, sub, "App 路径", p.AppPath)
	})
}

// copyItem adds an item copying value, disabled while it is empty.
func copyItem(c *ui.Context, menu *ui.Menu, label, value string) {
	if menu.Item(label).Disabled(value == "" || value == "—").Chosen() {
		c.WriteClipboard(value)
	}
}

// domainGroupMenu is the context menu of a domain group; the IP group
// only folds.
func (m *connMonitor) domainGroupMenu(c *ui.Context, menu *ui.Menu, group *node) {
	open := m.isExpanded(group.Key)
	if menu.Item(map[bool]string{true: "折叠分组", false: "展开分组"}[open]).Chosen() {
		m.expanded()[group.Key] = !open
	}
	if group.Key == ipGroupKey {
		return
	}
	domain := group.Label
	menu.Separator()
	if menu.Item("查看此域名的用量").Chosen() {
		filter := map[string]string{"host": domain}
		if strings.HasPrefix(group.Key, "root:") {
			filter = map[string]string{"rootDomain": domain}
		}
		m.env.Navigate(env.PageUsage, env.UsageView{Group: "host", Filters: filter})
	}
	menu.Separator()
	ruleactions.Menu(menu, m.env, m.store, ruleactions.Target{Domain: domain})
	menu.Submenu("复制", func(sub *ui.Menu) {
		copyItem(c, sub, "域名", domain)
	})
}
