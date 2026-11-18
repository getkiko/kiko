package processes

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/ruleactions"
)

// This file holds the process list's context menu and the inspector's
// links to Connections, Usage and Routing.

// fileName is a path's last element, the name the connection monitor
// lists a process by.
func fileName(path string) string {
	path = strings.TrimRight(path, `/\`)
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// connectionName is the process's name in the connection monitor.
func connectionName(p Process) string {
	if name := fileName(p.Path); name != "" {
		return name
	}
	return p.Name
}

// ruleTarget is what "Add to Rule" offers of a process: its name, path and
// App; processes run on the server's host, the local server or gateway.
func ruleTarget(p Process) ruleactions.Target {
	return ruleactions.Target{ProcessName: connectionName(p), ProcessPath: p.Path, AppPath: p.AppPath, AppName: p.AppName}
}

// showConnections opens the connection monitor on the process: this PID,
// or every process of its name.
func (w *Workspace) showConnections(p Process, onlyPID bool) {
	v := env.ConnectionsView{Filter: map[string][]string{"process": {connectionName(p)}}}
	if onlyPID {
		v = env.ConnectionsView{Filter: map[string][]string{"pid": {strconv.Itoa(p.PID)}}}
	}
	w.env.Navigate(env.PageConnections, v)
}

// showUsage opens Usage on the process's path, by main domain.
func (w *Workspace) showUsage(p Process) {
	w.env.Navigate(env.PageUsage, env.UsageView{Group: "rootDomain", Filters: map[string]string{"process": p.Path}})
}

// rowMenu is the context menu of a process or a group of processes: a
// group's connections and usage are those of its name and path.
func (w *Workspace) rowMenu(c *ui.Context, menu *ui.Menu, row Row, v *view) {
	p := row.Process
	group := row.Group && row.Count > 1
	if !group {
		if menu.Item(fmt.Sprintf("查看连接（PID %d）", p.PID)).Disabled(p.PID <= 0).Chosen() {
			w.showConnections(p, true)
		}
	}
	if menu.Item("查看连接（同名全部）").Disabled(connectionName(p) == "").Chosen() {
		w.showConnections(p, false)
	}
	if p.AppPath != "" {
		// The connection monitor's App filter names the bundle.
		app := p.AppName
		if app == "" {
			app = strings.TrimSuffix(path.Base(p.AppPath), ".app")
		}
		if menu.Item(fmt.Sprintf("查看连接（%s 的全部进程）", app)).Chosen() {
			w.env.Navigate(env.PageConnections, env.ConnectionsView{Filter: map[string][]string{"app": {app}}})
		}
	}
	if menu.Item("查看用量").Disabled(p.Path == "").Chosen() {
		w.showUsage(p)
	}
	menu.Separator()
	ruleactions.Menu(menu, w.env, w.store, ruleTarget(p))
	if menu.Item("显示文件位置").Disabled(p.Path == "").Chosen() {
		w.reveal(p)
	}
	menu.Submenu("复制", func(sub *ui.Menu) {
		if sub.Item("PID").Disabled(p.PID <= 0).Chosen() {
			c.WriteClipboard(strconv.Itoa(p.PID))
		}
		if sub.Item("路径").Disabled(p.Path == "").Chosen() {
			c.WriteClipboard(p.Path)
		}
	})
	menu.Separator()
	// The confirmation anchors to the inspector's button of the process,
	// which the choice opens.
	disabled := p.PID <= 1
	quit := func(force bool, anchor string) {
		w.selectRow(row, v)
		w.details = true
		w.askQuit(p, force, anchor)
	}
	if menu.Item("退出…").Disabled(disabled).Chosen() {
		quit(false, "quit")
	}
	if menu.Item("强制退出…").Disabled(disabled).Chosen() {
		quit(true, "force")
	}
}
