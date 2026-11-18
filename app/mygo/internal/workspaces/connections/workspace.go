// Package connections is the console's Connections workspace: the live
// Network Connections monitor, the HTTP Requests monitor and the DNS
// Queries monitor, one shown at a time.
package connections

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// Views of the workspace.
const (
	viewNetwork = 0
	viewHTTP    = 1
	viewDNS     = 2
)

// Workspace is the connections page.
type Workspace struct {
	env   *env.Env
	store *store.Store

	view   int
	active bool
	conn   *connMonitor
	http   *httpMonitor
	dns    *dnsMonitor

	// toolbarWidth is the toolbar's last laid-out width, kept while
	// another page is shown.
	toolbarWidth float32
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	icons := newIconCache(e)
	w := &Workspace{env: e, store: s, conn: newConnMonitor(e, icons), http: newHTTPMonitor(e, icons), dns: newDNSMonitor(e, icons)}
	w.conn.header, w.http.header, w.dns.header = w.header, w.header, w.header
	w.conn.store = s
	names := &sourceNamer{store: s}
	w.conn.names, w.http.names, w.dns.names = names, names, names
	if s != nil {
		w.http.doc, w.http.nodes, w.dns.nodes = s, s, s
	}
	return w
}

// header is the page's toolbar: the Network Connections / HTTP Requests /
// DNS Queries tabs with the shown monitor's statistics, then its filters
// (the scope and the search) and its actions on the right, in one row.
// Narrow, the filters take a second row so the actions stay in the window.
// Its empty space drags the window.
func (w *Workspace) header(c *ui.Context, subtitle string, filters, actions func()) {
	head := ui.Column(c).PaddingX(16).PaddingY(8).Gap(8).Shrink(0).DragWindow()
	// The page's own width (the window's less the app sidebar), from the
	// previous frame, or the last one this page was shown in; the window's
	// until the toolbar has been laid out once.
	if box := head.Bounds(); box.W > 0 {
		w.toolbarWidth = box.W
	}
	width := w.toolbarWidth
	if width <= 0 {
		width, _ = c.Size()
	}
	narrow := width < toolbarWrapWidth
	head.Children(func() {
		ui.Row(c).Gap(10).DragWindow().Children(func() {
			tabs := kit.Segmented(c, "monitor-view", &w.view, kit.Segment{Label: "网络连接"}, kit.Segment{Label: "HTTP 请求"}, kit.Segment{Label: "DNS 查询"})
			tabs.Label("监控视图")
			if tabs.Changed() {
				w.SetActive(w.active)
			}
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(kit.FontCaption).SingleLine().TextColor(kit.T(c).Label3).FontFeatures("tnum")
			}
			ui.Spacer(c)
			if !narrow {
				filters()
			}
			actions()
		})
		if narrow {
			ui.Row(c).Gap(10).DragWindow().Children(filters)
		}
	})
}

// SetActive starts the shown monitor's streams while the page is visible.
func (w *Workspace) SetActive(active bool) {
	w.active = active
	w.conn.setActive(active && w.view == viewNetwork)
	w.http.setActive(active && w.view == viewHTTP)
	w.dns.setActive(active && w.view == viewDNS)
}

// Open shows the Network Connections monitor as another page asks
// (env.ConnectionsView): its filters, search and process.
func (w *Workspace) Open(arg any) {
	v, ok := arg.(env.ConnectionsView)
	if !ok {
		return
	}
	if v.HTTPConnection != "" {
		w.view = viewHTTP
		w.http.showConnection(v.HTTPConnection)
		w.SetActive(w.active)
		return
	}
	w.view = viewNetwork
	w.conn.open(v)
	w.SetActive(w.active)
}

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	kit.Workspace(c, func() {
		switch w.view {
		case viewNetwork:
			w.conn.view(c)
		case viewHTTP:
			w.http.view(c)
		default:
			w.dns.view(c)
		}
	})
}

// sleep waits d unless ctx ends first, reporting whether it waited.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// toolbarWrapWidth is the page width below which the monitor toolbar's
// filters take a second row. One row holds the tabs, the statistics, the
// scope, the search and the actions uncompressed from about 1200 DIPs with
// eight-digit counts (DNS Queries, the longest statistics).
const toolbarWrapWidth = 1240

// narrowWidth is the window width at and below which filters and
// inspectors become overlays.
const narrowWidth = 800

// sidePanel shows a panel over the window's left or right edge, full
// height, for narrow windows; a press outside it calls onClose.
func sidePanel(c *ui.Context, right bool, onClose func(), fn func()) {
	k := kit.T(c)
	var panel ui.Element
	ui.Overlay(c, func() {
		panel = ui.Row(c).Absolute().Top(0).Bottom(0).AlignItems(ui.Stretch).
			Shadow(0, 12, 40, 0, k.Shadow.Alpha(0.25)).Children(fn)
		if right {
			panel.Right(0)
		} else {
			panel.Left(0)
		}
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			onClose()
		}
	})
	if panel.PressedOutside() {
		onClose()
	}
}

// saveFile asks where to save data on a goroutine; done gets the error.
func saveFile(e *env.Env, name string, data []byte, filter env.FileFilter, done func(error)) {
	e.Run(func(context.Context) error {
		_, err := e.Shell.SaveFile(name, data, filter)
		return err
	}, done)
}
