// Package console is the native Kiko console: the app navigation and the
// workspaces it switches between, drawn by MyGo's ui package.
package console

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/connections"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/logs"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/overview"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/processes"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/rewrites"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/routes"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/settings"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/subscriptions"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/usage"
)

// navItem is an entry of the app navigation.
type navItem struct {
	page  string
	label string
	icon  kit.Symbol
}

// navGroup is a titled group of the navigation.
type navGroup struct {
	title string
	items []navItem
}

// navigation is the sidebar's order: the workspaces Overview, Routing, Connections,
// Usage, Processes and Rewrites, the network group entered through Components, then Logs
// and Settings.
var navigation = []navGroup{
	{"工作区", []navItem{
		{env.PageOverview, "概览", kit.SymHouse},
		{env.PageRoute, "路由", kit.SymRoute},
		{env.PageConnections, "连接", kit.SymActivity},
		{env.PageUsage, "用量", kit.SymChartNoAxesCombined},
		{env.PageProcesses, "进程", kit.SymCpu},
		{env.PageRewrites, "改写", kit.SymWandSparkles},
	}},
	{"网络分组", []navItem{
		{env.PageSubscriptions, "组件", kit.SymRss},
	}},
	{"", []navItem{
		{env.PageLogs, "日志", kit.SymFileText},
		{env.PageSettings, "设置", kit.SymSettings2},
	}},
}

// Console is the window's state: the environment, the document store,
// every workspace (kept, so each keeps its state while hidden), and the
// page shown.
type Console struct {
	Env   *env.Env
	Store *store.Store

	page       string
	workspaces map[string]env.Workspace
	active     env.Workspace
	collapsed  bool
	started    bool

	warningsVersion int
	warnings        map[string]bool
	unapplied       store.Unapplied

	// showStderr expands the server's last error output in its notice.
	showStderr bool
}

// New returns the console of e. Call Start before showing it.
func New(e *env.Env) *Console {
	s := store.New(e)
	c := &Console{Env: e, Store: s, page: env.PageOverview}
	route, set := routes.New(e, s), settings.New(e, s)
	set.RuleSets = route
	// The Overview Tailscale card shares the Components page's endpoint state.
	subs, home := subscriptions.New(e, s), overview.New(e, s)
	home.Tailscale = subs.Tailscale()
	c.workspaces = map[string]env.Workspace{
		env.PageOverview:      home,
		env.PageRoute:         route,
		env.PageSubscriptions: subs,
		env.PageConnections:   connections.New(e, s),
		env.PageUsage:         usage.New(e, s),
		env.PageProcesses:     processes.New(e, s),
		env.PageRewrites:      rewrites.New(e, s),
		env.PageLogs:          logs.New(e, s),
		env.PageSettings:      set,
	}
	e.SetNavigator(c.Navigate)
	e.Server.OnReconnect(c.reactivate)
	return c
}

// reactivate restarts the shown page's streams and its reads of the
// server once the event stream connected again, the server possibly
// restarted: nothing it shows stays from before. UI thread.
func (c *Console) reactivate() {
	if a, ok := c.active.(env.Activator); ok {
		a.SetActive(false)
		a.SetActive(true)
	}
}

// Start loads the document and follows the server's events. UI thread.
func (c *Console) Start() {
	if c.started {
		return
	}
	c.started = true
	c.Env.Server.Start()
	c.Store.Load()
	c.startUpdates()
}

// updateSchedule is the workspace that updates modules on a timer.
type updateSchedule interface {
	StartUpdates()
	StopUpdates()
}

// startUpdates starts the Modules tab's schedule, which updates the modules from
// their addresses.
func (c *Console) startUpdates() {
	if u, ok := c.workspaces[env.PageRewrites].(updateSchedule); ok {
		u.StartUpdates()
	}
}

// Stop ends the timers the console runs, as its window closes. A window
// that shows again starts them with SetVisible. UI thread.
func (c *Console) Stop() {
	if u, ok := c.workspaces[env.PageRewrites].(updateSchedule); ok {
		u.StopUpdates()
	}
}

// Page is the page shown.
func (c *Console) Page() string { return c.page }

// Workspace returns the workspace of a page.
func (c *Console) Workspace(page string) env.Workspace { return c.workspaces[page] }

// Navigate shows page, handing arg to its workspace when it takes one.
// UI thread.
func (c *Console) Navigate(page string, arg any) {
	if _, ok := c.workspaces[page]; !ok {
		return
	}
	c.page = page
	if opener, ok := c.workspaces[page].(env.Opener); ok && arg != nil {
		opener.Open(arg)
	}
	// The page shown runs its streams now: no frame may follow to start
	// them, as when the navigation came from the menu.
	if c.started {
		c.syncActive()
	}
}

// SetVisible records whether the window is visible, pausing the active
// workspace's streams while it is not. UI thread.
func (c *Console) SetVisible(visible bool) {
	c.Env.SetVisible(visible)
	if visible && c.started {
		c.startUpdates()
	}
	c.syncActive()
}

// syncActive tells workspaces whether they are the shown page of a
// visible window.
func (c *Console) syncActive() {
	var shown env.Workspace
	if c.Env.Visible() {
		shown = c.workspaces[c.page]
	}
	if shown == c.active {
		return
	}
	if a, ok := c.active.(env.Activator); ok {
		a.SetActive(false)
	}
	c.active = shown
	if a, ok := shown.(env.Activator); ok {
		a.SetActive(true)
	}
}

// View builds the window.
func (c *Console) View(ctx *ui.Context) {
	kit.Apply(ctx)
	c.Env.Drain(ctx)
	if !c.started {
		c.Start()
	}
	c.syncActive()
	k := kit.T(ctx)
	// Over the window's material (macOS), the sidebar shows what is behind
	// the window, as a standard macOS sidebar; the content stays opaque.
	// Set once the view is built: SetTheme, as a flat table's, fills the
	// root with the theme's background again.
	defer func() {
		if ctx.Vibrancy() {
			ctx.Root().Background(ui.Transparent)
		} else {
			ctx.Root().Background(k.Canvas)
		}
	}()
	ui.Row(ctx).Fill().AlignItems(ui.Stretch).Children(func() {
		c.sidebar(ctx)
		ui.Column(ctx).Grow(1).MinWidth(0).Background(k.Surface).Children(func() {
			c.connectionNotice(ctx)
			ui.Column(ctx).Grow(1).MinHeight(0).Children(func() {
				switch {
				case c.Store.LoadError != "" && !c.Store.Loaded:
					kit.EmptyState(ctx, kit.SymTriangleAlert, "无法读取配置", c.Store.LoadError, func() {
						kit.Button(ctx, "重试", kit.ButtonOptions{Variant: kit.Primary}).OnClick(c.Store.Load)
					})
				case !c.Store.Loaded && c.page != env.PageLogs:
					ui.Column(ctx).Grow(1).Center().Gap(10).Children(func() {
						ui.Spinner(ctx)
						ui.Text(ctx, "正在载入配置…").TextColor(k.Label3)
					})
				default:
					ui.Column(ctx.Key(c.page)).Grow(1).MinHeight(0).Children(func() {
						c.workspaces[c.page].View(ctx)
					})
				}
			})
		})
	})
	// Ctrl/Cmd+1… switch among the workspaces in the navigation's order.
	index := 0
	for _, group := range navigation {
		for _, item := range group.items {
			if index < 9 && ctx.Shortcut(ui.Cmd, ui.Key1+ui.Key(index)) {
				c.Navigate(item.page, nil)
			}
			index++
		}
	}
}

// Sizes of the sidebar.
const (
	sidebarWidth   = 140
	sidebarFolded  = 68
	sidebarToggle  = 24 // the collapse button, beside the window controls
	sidebarHeaderH = 40 // the header holding it where no title bar is drawn
)

// sidebarMotion is the sidebar's folding.
var sidebarMotion = ui.Animation{Duration: 220 * time.Millisecond, Ease: ui.CubicBezier(0.77, 0, 0.175, 1)}

// sidebarInset is where the sidebar's header starts its content: right of
// the window controls of a hidden title bar (the macOS traffic lights),
// otherwise in line with the navigation's icons.
func sidebarInset(bar ui.TitleBar) float32 {
	if bar.Left > 0 {
		return bar.Left + 8
	}
	return 15
}

// sidebarWidthOf is the sidebar's width, folded or not: folded, it keeps
// room for the collapse button beside the window controls, so that the
// button stays clear of the page.
func sidebarWidthOf(bar ui.TitleBar, collapsed bool) float32 {
	if !collapsed {
		return max(sidebarWidth, sidebarInset(bar)+sidebarToggle+8)
	}
	return max(sidebarFolded, sidebarInset(bar)+sidebarToggle+8)
}

// sidebar is the app navigation, 140 DIPs wide or folded to its icons. Its
// header holds the small collapse button beside the window controls; it
// shows the window's material where the window has one (macOS).
func (c *Console) sidebar(ctx *ui.Context) {
	k := kit.T(ctx)
	if c.warningsVersion != c.Store.Version()+1 {
		c.warnings = model.TabWarnings(c.Store.Doc(), c.Store.NetworkConfigs())
		c.warningsVersion = c.Store.Version() + 1
	}
	c.unapplied = c.Store.Unapplied()
	bar := ctx.TitleBar()
	side := ui.Column(ctx).Shrink(0).Padding(0, 8, 10).Gap(10).Clip().
		BorderWidth(0, 1, 0, 0).BorderColor(k.HairlineSoft).Label("侧边栏")
	// Folding and unfolding animate the width.
	side.Width(side.AnimateTo("width", sidebarWidthOf(bar, c.collapsed), sidebarMotion))
	if !ctx.Vibrancy() {
		side.Background(k.Sidebar)
	}
	side.Children(func() {
		// The title bar's row drags the window.
		ui.Row(ctx).Height(max(bar.Height, sidebarHeaderH)).Shrink(0).DragWindow()
		ui.Column(ctx).Grow(1).Gap(20).Label("主要导航").Children(func() {
			for _, group := range navigation {
				ui.Column(ctx).Gap(3).Children(func() {
					if group.title != "" {
						title := ui.Text(ctx, group.title).FontSize(kit.FontCaption).FontWeight(600).
							TextColor(k.Label3).Padding(0, 12, 3).SingleLine()
						if c.collapsed {
							title.Invisible()
						}
					}
					for _, item := range group.items {
						c.navButton(ctx, item)
					}
				})
			}
		})
	})
}

// navButton is an entry of the navigation: its icon and label start where
// the group titles' text does, folded or not.
func (c *Console) navButton(ctx *ui.Context, item navItem) {
	k := kit.T(ctx)
	active := c.page == item.page
	b := ui.ButtonBase(ctx.Key(item.page)).Height(40).PaddingX(12).Gap(12).Justify(ui.Start).Radius(8).
		Label(item.label).TextColor(k.Label2).Transition(kit.HoverMotion)
	if c.collapsed {
		// The folded rail keeps the icons where they were, in pills as
		// wide as a 68 DIP rail's.
		b.Width(sidebarFolded - 16)
		kit.Hint(ctx, b, item.label)
	}
	hover, selected := k.SidebarHover, k.SidebarActive
	if ctx.Vibrancy() {
		// A translucent hover over the material, as macOS sidebars draw;
		// the chosen item keeps its light blue.
		hover = k.Label.Alpha(0.06)
	}
	if active {
		// Selected fills the item with the theme's accent: it comes first,
		// so the fill below replaces it.
		b.Role(ui.RoleTab).Selected(true)
	}
	switch {
	case active:
		b.Background(selected).TextColor(k.Label).FontWeight(600)
	case b.Hovered():
		b.Background(hover)
	}
	b.Children(func() {
		ui.Box(ctx).Size(19, 19).Shrink(0).Children(func() {
			kit.Icon(ctx, item.icon, 19)
			if c.warnings[item.page] {
				ui.Box(ctx).Size(8, 8).Radius(4).Background(k.Danger).Border(2, k.Sidebar).
					Absolute().Top(-3).Right(-3).Label("有需要修正的配置")
			}
			if c.unapplied.Pages[item.page] {
				// Beside a warning's red dot the blue one moves to the
				// lower corner.
				dot := ui.Box(ctx).Size(8, 8).Radius(4).Background(k.Accent).Border(1, k.Sidebar).
					Absolute().Right(-3).Label("有未应用的更改")
				if c.warnings[item.page] {
					dot.Bottom(-3)
				} else {
					dot.Top(-3)
				}
			}
		})
		if !c.collapsed {
			ui.Text(ctx, item.label).FontSize(14).SingleLine()
		}
	})
	if b.Clicked() {
		c.Navigate(item.page, nil)
	}
}

// connectionNotice warns while the server's process or its event stream
// is down, a remote server needs pairing again, or another device holds
// the server's write gate.
func (c *Console) connectionNotice(ctx *ui.Context) {
	if c.processNotice(ctx) || c.fatalNotice(ctx) {
		return
	}
	server := c.Env.Server
	if server.Connected {
		c.lockNotice(ctx)
		return
	}
	message := "正在连接服务端，实时数据等待更新…"
	if server.EverConnected {
		message = "服务端连接已中断，实时数据已过期，正在自动重试。"
	}
	if c.Env.Target.Remote {
		message = "正在连接「" + c.Env.Target.Name + "」，实时数据等待更新…"
		if server.EverConnected {
			message = "与「" + c.Env.Target.Name + "」的连接已中断，实时数据已过期，正在自动重试。"
		}
		if server.Failure != nil {
			message += "\n" + server.Failure.Error()
		}
	}
	ui.Box(ctx).Padding(6, 12, 4).Shrink(0).Role(ui.RoleStatus).Children(func() {
		kit.Notice(ctx, kit.WarningTone, "", message, func() {
			kit.Button(ctx, "立即重试", kit.ButtonOptions{Small: true}).OnClick(server.Retry)
		})
	})
}
