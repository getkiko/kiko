package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/procicon"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
	"github.com/getkiko/kiko/app/mygo/internal/supervisor"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// shell runs the desktop app: the kiko-server child process, the macOS
// privileged helper, the menus, and a window per server — the local server, the
// server the app runs, and each paired remote server — each showing a
// native console of its own.
type shell struct {
	paths   *Paths
	server  *serverSupervisor
	helper  *helperManager
	icons   *iconCache
	menu    *menuBuilder
	servers *servers.Directory
	// prefs are the app's preferences on this computer, shared by every
	// window (Env.AppPrefs).
	prefs *env.Prefs

	mu       sync.Mutex
	starting bool
	quitting bool
	windows  map[string]*serverWindow
}

func newShell() *shell {
	paths, err := newPaths()
	if err != nil {
		log.Fatalf("resolve app paths: %v", err)
	}
	server, err := newServerSupervisor(paths)
	if err != nil {
		log.Fatalf("initialize server supervisor: %v", err)
	}
	s := &shell{
		paths:   paths,
		server:  server,
		icons:   newIconCache(),
		windows: map[string]*serverWindow{},
	}
	s.helper = newHelperManager(paths)
	s.servers = openDirectory(paths)
	// The app preferences come before the first window, which shares them.
	s.prefs = env.NewPrefs(filepath.Join(paths.userData, "app-preferences.json"))
	var zone string
	if s.prefs.Load(timefmt.PrefZone, &zone) {
		timefmt.SetZone(zone)
	}
	go timefmt.Zones()
	local, _ := s.windowFor(servers.LocalID)
	// The console of the local server shows the server process's status and restarts
	// it.
	s.server.onChange = func(status supervisor.Status) {
		local.env.Post(func() { local.env.Server.SetProcess(status) })
	}
	s.servers.OnChange(s.serversChanged)
	s.menu = newMenuBuilder(s)
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		s.mu.Lock()
		s.quitting = true
		s.mu.Unlock()
		s.shutdown()
	})
	return s
}

// openDirectory reads the server list from the app's data directory. A
// list that cannot be read is set aside so the app still starts.
func openDirectory(paths *Paths) *servers.Directory {
	local := servers.Server{Name: "本机", URL: paths.serverURL()}
	dir, err := servers.Open(paths.userData, local, serverSecrets(), deviceName())
	if err == nil {
		return dir
	}
	log.Printf("[servers] %v", err)
	list := filepath.Join(paths.userData, "servers.json")
	_ = os.Rename(list, list+".bad")
	dir, err = servers.Open(paths.userData, local, serverSecrets(), deviceName())
	if err != nil {
		log.Fatalf("read server list: %v", err)
	}
	return dir
}

// deviceName is how this device introduces itself to servers it pairs
// with: the host name without its .local suffix.
func deviceName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "Kiko"
	}
	return strings.TrimSuffix(name, ".local")
}

// serverSecrets is the credential store of the server tokens. Tokens
// saved under the service name used before the rename to Kiko
// (sbcfg-servers) move to kiko-servers on first use.
func serverSecrets() servers.SecretStore {
	return servers.MigratingSecrets(keychain{mygo.Secrets.Service("kiko-servers")}, keychain{mygo.Secrets.Service("sbcfg-servers")})
}

// keychain is the system's credential store as servers.SecretStore.
type keychain struct{ secrets mygo.SecretsModule }

func (k keychain) Set(key string, value []byte) error { return k.secrets.Set(key, value) }
func (k keychain) Delete(key string) error            { return k.secrets.Delete(key) }

func (k keychain) Get(key string) ([]byte, error) {
	value, err := k.secrets.Get(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, servers.ErrSecretNotFound
	}
	return value, err
}

// start brings the app up in order: install the helper on macOS, run the
// server, then open the window. A failure reports through a dialog and
// quits.
func (s *shell) start() {
	if !s.beginStart() {
		return
	}
	if err := s.bootstrap(); err != nil {
		mygo.Dialog.Error("Kiko 启动失败", err.Error())
		mygo.App.Quit()
		return
	}
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			s.openServer(servers.LocalID)
		}
	})
	mygo.App.OnWindowAllClosed(func() {
		if runtimeIsDarwin() {
			return // macOS keeps the app running with its Dock icon
		}
		mygo.App.Quit()
	})
}

func (s *shell) beginStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starting {
		return false
	}
	s.starting = true
	return true
}

func (s *shell) bootstrap() error {
	if runtimeIsDarwin() {
		// A missing helper is expected on first launch; the prompt follows.
		if _, err := s.helper.ensure(false); err != nil {
			log.Printf("[helper] install check failed: %v", err)
		}
	}
	// start waits until the server serves; later exits restart it.
	if err := s.server.start(); err != nil {
		return err
	}
	s.menu.install()
	s.startTray()
	restore, open := s.servers.Restore()
	s.openServer(servers.LocalID)
	if restore {
		for _, id := range open {
			s.openServer(id)
		}
	}
	startDebugProbe(s)
	return nil
}

// shutdown stops the server for good before the app quits.
func (s *shell) shutdown() {
	s.server.stop()
}

// openServer opens the server's window, or brings it forward. Windows open
// on the main thread, between frames; it is safe from any goroutine and
// waits for the window.
func (s *shell) openServer(id string) {
	mygo.RunOnMain(func() {
		if w, ok := s.windowFor(id); ok {
			w.open()
		}
	})
}

// local is the console of the local server.
func (s *shell) local() *serverWindow {
	w, _ := s.windowFor(servers.LocalID)
	return w
}

// focused is the console of the focused window, of window when given, or
// the local server.
func (s *shell) focused(window *mygo.Window) *serverWindow {
	if window == nil {
		window = mygo.FocusedWindow()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if window != nil {
		for _, w := range s.windows {
			if w.current() == window {
				return w
			}
		}
	}
	return s.windows[servers.LocalID]
}

// opened lists the consoles in the directory's order.
func (s *shell) opened() []*serverWindow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*serverWindow
	for _, entry := range s.servers.List() {
		if w := s.windows[entry.ID]; w != nil {
			out = append(out, w)
		}
	}
	return out
}

// recordOpenWindows remembers the remote windows open, to restore them at
// the next launch. Quitting closes every window, which keeps the list.
func (s *shell) recordOpenWindows() {
	s.mu.Lock()
	quitting := s.quitting
	s.mu.Unlock()
	if quitting {
		return
	}
	var ids []string
	for _, w := range s.opened() {
		if w.id != servers.LocalID && w.current() != nil {
			ids = append(ids, w.id)
		}
	}
	s.servers.SetOpen(ids)
}

// serversChanged follows the directory: the menus list the servers, the
// windows show their names, and every settings page redraws.
func (s *shell) serversChanged() {
	s.menu.install()
	for _, w := range s.opened() {
		entry, ok := s.servers.Get(w.id)
		if !ok {
			continue
		}
		target := targetOf(entry)
		w.env.Post(func() { w.env.Target = target })
		if window := w.current(); window != nil {
			window.SetTitle(windowTitle(entry.Name))
		}
	}
}

// showAddServer opens the settings' add server dialog in the console of
// window, or of the focused window.
func (s *shell) showAddServer(window *mygo.Window) {
	w := s.focused(window)
	mygo.RunOnMain(w.open)
	w.update(func() { w.console.Navigate(env.PageSettings, "servers:add") })
}

// repaired gives a server's console the token or certificate it was
// paired again with, and reconnects it.
func (s *shell) repaired(id string) {
	s.mu.Lock()
	w := s.windows[id]
	s.mu.Unlock()
	if w == nil {
		return
	}
	options, err := s.servers.Options(id)
	if err != nil {
		return
	}
	w.env.API.Reconfigure(options)
	w.env.Post(func() {
		w.env.Server.Retry()
		w.console.Store.Resync()
	})
}

// appPrefsChanged redraws every window after an app preference changed
// (Env.AppPrefsChanged).
func (s *shell) appPrefsChanged() {
	s.mu.Lock()
	windows := make([]*serverWindow, 0, len(s.windows))
	for _, w := range s.windows {
		windows = append(windows, w)
	}
	s.mu.Unlock()
	for _, w := range windows {
		w.env.Post(func() {})
	}
}

// removed closes a forgotten server's window and drops its console.
func (s *shell) removed(id string) {
	s.mu.Lock()
	w := s.windows[id]
	delete(s.windows, id)
	s.mu.Unlock()
	if w == nil {
		return
	}
	w.env.Post(w.env.Server.Stop)
	procicon.Forget(w.env)
	if window := w.current(); window != nil {
		window.Close()
	}
}

// OpenLogsDirectory reveals the kiko-server logs directory.
func (s *shell) OpenLogsDirectory() error {
	directory := filepath.Join(s.paths.dataDir(), "logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return mygo.Shell.OpenPath(directory)
}

// PrivilegedHelperStatus reports whether the installed macOS helper
// matches the bundled one. Other platforms have none.
func (s *shell) PrivilegedHelperStatus() (helperStatus, error) {
	if !runtimeIsDarwin() {
		return helperStatus{}, errors.New("仅 macOS 提供特权助手")
	}
	return s.helper.status()
}

// InstallPrivilegedHelper installs or updates the helper, then reports the
// resulting status so the settings page can refresh in place.
func (s *shell) InstallPrivilegedHelper() (helperInstallResult, error) {
	if !runtimeIsDarwin() {
		return helperInstallResult{}, errors.New("仅 macOS 提供特权助手")
	}
	completed, err := s.helper.ensure(true)
	if err != nil {
		return helperInstallResult{}, err
	}
	status, err := s.helper.status()
	if err != nil {
		return helperInstallResult{Completed: completed}, err
	}
	return helperInstallResult{Completed: completed, helperStatus: status}, nil
}

// ShowConnections focuses the console of the focused window on its
// connections page, as the View menu's "Show established connections" does.
func (s *shell) ShowConnections(window *mygo.Window) {
	w := s.focused(window)
	mygo.RunOnMain(w.open)
	w.update(func() { w.console.Navigate(env.PageConnections, nil) })
}

// The server directory, for every console's settings (env.ServerDirectory
// through baseServices).

func (s *shell) addServer(ctx context.Context, uri string) (servers.Server, error) {
	entry, err := s.servers.Add(ctx, uri)
	if err == nil {
		s.repaired(entry.ID)
	}
	return entry, err
}

func (s *shell) addServerManual(ctx context.Context, addr, code string, confirm func([]byte) bool) (servers.Server, error) {
	entry, err := s.servers.AddManual(ctx, addr, code, confirm)
	if err == nil {
		s.repaired(entry.ID)
	}
	return entry, err
}

func (s *shell) repairServer(ctx context.Context, id, input string, confirm func([]byte) bool) error {
	if _, err := s.servers.Repair(ctx, id, input, confirm); err != nil {
		return err
	}
	s.repaired(id)
	return nil
}

func (s *shell) removeServer(ctx context.Context, id string) error {
	err := s.servers.Remove(ctx, id)
	if err == nil {
		s.removed(id)
	}
	return err
}
