package main

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

// menuBuilder installs the application menu: the app menu with the update
// check and the helper reinstall, native Edit roles so Cmd+C/V/X/A work in every input, the
// View entries, the Servers menu opening a window per server, and Help.
// The Dock menu lists the servers too. Both follow the server directory.
type menuBuilder struct {
	shell *shell
}

func newMenuBuilder(shell *shell) *menuBuilder { return &menuBuilder{shell: shell} }

func (m *menuBuilder) install() {
	appName := mygo.App.Name()
	if appName == "" {
		appName = "Kiko"
	}

	appItems := []*mygo.MenuItem{{Role: mygo.RoleAbout}, updater.MenuItem()}
	if runtimeIsDarwin() {
		appItems = append(appItems, &mygo.MenuItem{
			Label: "重新安装 TUN 特权助手",
			Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
				if _, err := m.shell.helper.ensure(true); err != nil {
					mygo.Dialog.Error("特权助手安装失败", err.Error())
				}
			},
		})
	}
	appItems = append(appItems, mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleQuit})

	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: appName, Submenu: appItems},
		// Native edit roles keep the standard clipboard shortcuts working
		// in every text input of the console.
		{Label: "编辑", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleUndo},
			{Role: mygo.RoleRedo},
			mygo.Separator(),
			{Role: mygo.RoleCut},
			{Role: mygo.RoleCopy},
			{Role: mygo.RolePaste},
			{Role: mygo.RoleSelectAll},
		}},
		{Label: "视图", Submenu: []*mygo.MenuItem{
			{Label: "打开 Kiko 控制台", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
				m.shell.openServer(servers.LocalID)
			}},
			{Label: "查看建立的连接", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
				m.shell.ShowConnections(w)
			}},
		}},
		{Label: "服务器", Submenu: append(m.serverItems(true), mygo.Separator(), &mygo.MenuItem{
			Label:       "连接到服务器…",
			Accelerator: "CmdOrCtrl+Shift+K",
			Click:       func(_ *mygo.MenuItem, w *mygo.Window) { m.shell.showAddServer(w) },
		})},
		{Label: "帮助", Submenu: []*mygo.MenuItem{
			{Label: "打开 Kiko 网站", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
				_ = mygo.Shell.OpenExternal("https://getkiko.github.io/kiko/")
			}},
		}},
	})
	mygo.App.SetMenu(menu)
	if runtimeIsDarwin() {
		mygo.App.Dock.SetMenu(mygo.NewMenu(m.serverItems(false)))
	}
}

// serverItems open each server's window, the first nine with
// CmdOrCtrl+Alt+1…9 when shortcuts.
func (m *menuBuilder) serverItems(shortcuts bool) []*mygo.MenuItem {
	var items []*mygo.MenuItem
	for i, entry := range m.shell.servers.List() {
		item := &mygo.MenuItem{Label: entry.Name, Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			go m.shell.openServer(entry.ID)
		}}
		if shortcuts && i < 9 {
			item.Accelerator = fmt.Sprintf("CmdOrCtrl+Alt+%d", i+1)
		}
		items = append(items, item)
	}
	return items
}
