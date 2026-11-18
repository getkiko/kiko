package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/console"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

// serverWindow is the console of one server and the window showing it.
// Each has a ui.View of its own: windows sharing a Content would all show
// the same console.
//
// The kit package keeps a few frame-scoped globals (the dialogs built, the
// row colors on the accent); several consoles share them safely because
// every window builds its frames on the main thread, one after another.
type serverWindow struct {
	shell   *shell
	id      string
	env     *env.Env
	console *console.Console
	content *ui.Content

	mu     sync.Mutex
	window *mygo.Window
}

// windowFor returns the server's console, creating it on first use.
// ok is false for a server not in the directory.
func (s *shell) windowFor(id string) (*serverWindow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.windows[id]; w != nil {
		return w, true
	}
	entry, ok := s.servers.Get(id)
	if !ok {
		return nil, false
	}
	w := &serverWindow{shell: s, id: id}
	base := baseServices{shell: s, win: w}
	var services env.Shell
	var client *api.Client
	prefs := filepath.Join(s.paths.dataDir(), "console-preferences.json")
	if entry.Remote() {
		services = remoteServices{base}
		var err error
		if client, err = s.servers.Client(id); err != nil {
			// Without a token the server answers 401, and the console asks
			// to pair again.
			client = api.NewWith(entry.URL, api.Options{Fingerprint: entry.Pin()})
		}
		prefs = filepath.Join(s.paths.userData, "console-preferences-"+id+".json")
	} else {
		services = desktopServices{base}
		client = api.New(entry.URL)
	}
	e := env.New(client, services)
	e.Prefs = env.NewPrefs(prefs)
	e.AppPrefs = s.prefs
	e.AppPrefsChanged = s.appPrefsChanged
	e.CacheDir = filepath.Join(s.paths.userData, "Cache")
	e.Target = targetOf(entry)
	w.env = e
	w.console = console.New(e)
	w.content = ui.View(w.console.View)
	if entry.Remote() {
		e.Server.OnConnect(func() { go s.servers.Touch(id, time.Now()) })
	}
	s.windows[id] = w
	return w, true
}

func targetOf(entry servers.Server) env.Target {
	return env.Target{ID: entry.ID, Name: entry.Name, URL: entry.URL, Remote: entry.Remote(), Color: entry.Color}
}

// windowTitle is "Kiko — <server>".
func windowTitle(name string) string { return "Kiko — " + name }

// open creates the window, or brings the existing one forward. Main
// thread: windows open one at a time.
func (w *serverWindow) open() {
	if window := w.current(); window != nil {
		window.Show()
		window.Focus()
		return
	}
	stateKey := "console" // the local server keeps the window position of earlier versions
	if w.id != servers.LocalID {
		stateKey = "console:" + w.id
	}
	options := mygo.WindowOptions{
		Title:     windowTitle(w.name()),
		Width:     1320,
		Height:    860,
		MinWidth:  980,
		MinHeight: 640,
		StateKey:  stateKey,
		// The console is drawn natively by MyGo's ui package; no webview.
		Content: w.content,
	}
	if runtimeIsDarwin() {
		// The navigation draws under the traffic lights, kept at (16, 16).
		options.TitleBarStyle = mygo.TitleBarHidden
		options.TrafficLightPosition = &mygo.Point{X: 16, Y: 16}
		// The sidebar shows the sidebar material, as a standard macOS
		// sidebar, where the console draws no background (the content is
		// opaque); not while the desktop asks for less transparency.
		options.Vibrancy = sidebarVibrancy()
	}
	window := mygo.NewWindow(options)
	if window == nil || window.IsDestroyed() {
		return
	}
	if runtimeIsDarwin() {
		off := mygo.Theme.OnUpdated(func() { window.SetVibrancy(sidebarVibrancy()) })
		window.OnClosed(off)
	}
	reopened := w.attach(window)
	if reopened {
		// A remote window's event stream stopped with its window.
		window.Update(w.env.Server.Start)
	}
}

// sidebarVibrancy is the material behind the console's sidebar: none while
// the desktop asks for less transparency, which the sidebar then fills.
func sidebarVibrancy() mygo.Vibrancy {
	if mygo.Theme.Preferences().ReduceTransparency {
		return mygo.VibrancyNone
	}
	return mygo.VibrancySidebar
}

func (w *serverWindow) name() string {
	if entry, ok := w.shell.servers.Get(w.id); ok {
		return entry.Name
	}
	return w.id
}

// attach wires the live window's visibility to the console. It reports
// whether the console was shown before, in a window since closed.
func (w *serverWindow) attach(window *mygo.Window) bool {
	w.mu.Lock()
	reopened := w.window != nil
	w.window = window
	w.mu.Unlock()

	publish := func() {
		visible := window.IsVisible() && !window.IsMinimized()
		window.Update(func() { w.console.SetVisible(visible) })
	}
	window.OnShow(publish)
	window.OnHide(publish)
	window.OnMinimize(publish)
	window.OnRestore(publish)
	window.OnClosed(func() {
		// A closed window (the app stays in the Dock on macOS) pauses the
		// streams until a window opens again; a remote server's event
		// stream stops too, not to stay connected to it unseen.
		remote := w.id != servers.LocalID
		window.Update(func() {
			w.console.SetVisible(false)
			w.console.Stop()
			if remote {
				w.env.Server.Stop()
			}
		})
		w.shell.recordOpenWindows()
	})
	w.shell.recordOpenWindows()
	return reopened
}

// update runs fn on the UI thread of the window and redraws it; nothing
// while it is closed.
func (w *serverWindow) update(fn func()) {
	if window := w.current(); window != nil {
		window.Update(fn)
	}
}

func (w *serverWindow) current() *mygo.Window {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.window == nil || w.window.IsDestroyed() {
		return nil
	}
	return w.window
}

// String makes a window printable in logs without exposing state.
func (w *serverWindow) String() string {
	if window := w.current(); window != nil {
		return fmt.Sprintf("serverWindow(%s, %d)", w.id, window.ID())
	}
	return fmt.Sprintf("serverWindow(%s, closed)", w.id)
}
