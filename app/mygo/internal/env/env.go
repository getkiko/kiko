// Package env is what every workspace of the native console shares: the
// server API, the document store, the server's live state from its event
// stream, desktop services, and the bridge from background goroutines to
// the UI thread.
//
// Threading: a view builds on the UI thread and reads state directly.
// Background work (requests, streams) never touches that state; it hands
// a function to Post, which runs it on the UI thread before the next
// frame.
package env

import (
	"context"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

// Pages of the console, the sidebar's items.
const (
	PageOverview      = "overview"
	PageRoute         = "route"
	PageSubscriptions = "subscriptions"
	PageConnections   = "connections"
	PageUsage         = "usage"
	PageProcesses     = "processes"
	PageRewrites      = "rewrites"
	PageLogs          = "logs"
	PageSettings      = "settings"
)

// RouteRule names a rule of the Routing workspace: Navigate(PageRoute,
// RouteRule{...}) shows its device and tab, scrolls to it and marks it.
// Section is "route" for a traffic rule, "dns" for a DNS routing rule.
// Final names Owner's default row instead of a rule: the default outbound in
// "route", the fallback DNS in "dns".
type RouteRule struct {
	ID, Owner, Section string
	Final              bool
}

// NewRouteRule opens a new traffic rule of Owner (a device ID, as
// model.DeviceForSource names it) in the Routing editor with the name and
// conditions given, unsaved: Navigate(PageRoute, NewRouteRule{...}).
type NewRouteRule struct {
	Owner, Name string
	Tree        *model.Condition
}

// ConnectionsView shows the Network Connections monitor's tab of State with these
// filters (facet → the values chosen in its group, as the filter pane
// lists them), search and process ID: Navigate(PageConnections,
// ConnectionsView{...}).
type ConnectionsView struct {
	Filter map[string][]string
	Search string
	// State is the tab: ConnectionsActive, ConnectionsClosed or
	// ConnectionsRejected; "" for all.
	State string
	// HTTPConnection, when set, shows the HTTP Requests monitor limited to the
	// requests of that connection ID instead; Filter and Search are
	// ignored.
	HTTPConnection string
	// ConnectionID, when set, limits the Network Connections monitor to that
	// connection, on the tab of State; Filter and Search are ignored.
	ConnectionID string
}

// Tabs of ConnectionsView.State, as the server names connection states.
const (
	ConnectionsActive   = "active"
	ConnectionsClosed   = "closed"
	ConnectionsRejected = "rejected"
)

// UsageView shows Usage grouped by Group (a dimension ID; "" keeps the
// current one) with only Filters (dimension → value):
// Navigate(PageUsage, UsageView{...}).
type UsageView struct {
	Group   string
	Filters map[string]string
}

// HelperChange is a component the macOS TUN helper install would update,
// with why.
type HelperChange struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// HelperStatus is the macOS privileged helper's state.
type HelperStatus struct {
	Installed   bool           `json:"installed"`
	NeedsUpdate bool           `json:"needsUpdate"`
	Changes     []HelperChange `json:"changes"`
}

// FileFilter narrows open and save dialogs, as "JSON" with "json".
type FileFilter struct {
	Name       string
	Extensions []string
}

// Shell is the desktop around the console. Its methods may block (dialogs,
// installs): call them from a goroutine, not from a view.
type Shell interface {
	// Platform is the desktop's GOOS.
	Platform() string
	// OpenLogsDirectory reveals kiko-server's logs directory.
	OpenLogsDirectory() error
	// HelperStatus reports the macOS TUN helper; an error elsewhere.
	HelperStatus() (HelperStatus, error)
	// InstallHelper installs or updates the helper, reporting whether the
	// install ran.
	InstallHelper() (bool, error)
	// AppIcon returns the PNG or ICNS bytes of an application or
	// executable's icon, nil when it has none.
	AppIcon(path string) []byte
	// SaveFile asks where to save data, suggesting name; ok is false when
	// the user cancels.
	SaveFile(name string, data []byte, filters ...FileFilter) (ok bool, err error)
	// OpenFile asks for a file and reads it; ok is false when the user
	// cancels.
	OpenFile(filters ...FileFilter) (data []byte, name string, ok bool, err error)
	// OpenPath opens a file or directory with its default application.
	OpenPath(path string) error
	// RevealPath shows a file in the file manager (Finder on macOS).
	RevealPath(path string) error
	// OpenInConsole opens a log file in macOS Console; an error elsewhere.
	OpenInConsole(path string) error
	// OpenURL opens a URL in the browser.
	OpenURL(url string) error
}

// ServerRestarter is a Shell that runs kiko-server itself and can
// restart it, as the desktop shell does. Its process status reaches the
// console through Server.SetProcess.
type ServerRestarter interface {
	// RestartServer replaces the server process, leaving a crash loop. It
	// returns at once; the status follows.
	RestartServer()
}

// ServerDirectory is a Shell that keeps the list of servers and opens a
// window for each, as the desktop shell does; the settings page manages
// the list through it from any window. Servers and InsecureTokens are
// quick; the other methods may block (network, credential store): call
// them from a goroutine.
type ServerDirectory interface {
	// Servers lists the local server and then the paired remote servers.
	Servers() []servers.Server
	// InsecureTokens reports tokens kept in a file for want of a
	// credential store.
	InsecureTokens() bool
	// OpenServer opens the server's window, or brings it forward. It
	// returns at once.
	OpenServer(id string)
	// AddServer pairs with a server from its pairing string.
	AddServer(ctx context.Context, uri string) (servers.Server, error)
	// AddServerManual pairs with the server at addr using code, if
	// confirm accepts its certificate's fingerprint.
	AddServerManual(ctx context.Context, addr, code string, confirm func(fp []byte) bool) (servers.Server, error)
	// RepairServer pairs a listed server again from a pairing string or
	// a code; its open window reconnects with the new token.
	RepairServer(ctx context.Context, id, input string, confirm func(fp []byte) bool) error
	RenameServer(id, name string) error
	// RemoveServer revokes this device at the server, forgets it and
	// closes its window.
	RemoveServer(ctx context.Context, id string) error
	// RestoreWindows reports whether the windows open at quit reopen at
	// launch.
	RestoreWindows() bool
	SetRestoreWindows(on bool) error
}

// Target is the server a console shows.
type Target struct {
	ID   string
	Name string
	URL  string
	// Remote is a paired server reached over TLS, not the app's own.
	Remote bool
	// Color is the server's accent, as "#5856D6"; empty for the local server.
	Color string
}

// Env is shared by the console and its workspaces.
type Env struct {
	API   *api.Client
	Shell Shell
	// Target is the server shown; the zero value is the local one.
	Target Target
	// Server is the server's live state, fed by its event stream.
	Server *Server
	// Prefs keeps view preferences between launches; in memory unless the
	// host replaces it with file-backed NewPrefs(path).
	Prefs *Prefs
	// AppPrefs are this computer's preferences for the whole app, shared
	// by every server window (the display time zone); in memory unless
	// the host replaces it with the shell's file.
	AppPrefs *Prefs
	// AppPrefsChanged, when set (by the shell), redraws every window after
	// an AppPrefs change. Safe from any goroutine.
	AppPrefsChanged func()
	// CacheDir holds caches kept between launches (process icons); empty
	// keeps them in memory.
	CacheDir string

	mu         sync.Mutex
	queue      []func()
	invalidate func()
	// fixedInvalidate is set by SetInvalidate: Drain keeps that function.
	fixedInvalidate bool

	toasts     []string
	navigate   func(page string, arg any)
	visible    bool
	background context.Context
}

// New returns an environment for the server at client. Call Start to
// follow its events.
func New(client *api.Client, shell Shell) *Env {
	e := &Env{API: client, Shell: shell, Prefs: NewPrefs(""), AppPrefs: NewPrefs(""), visible: true, background: context.Background()}
	e.Server = newServer(e)
	return e
}

// Directory returns the shell's server directory, if it keeps one.
func (e *Env) Directory() (ServerDirectory, bool) {
	d, ok := e.Shell.(ServerDirectory)
	return d, ok
}

// Post runs fn on the UI thread before the next frame. It is safe from
// any goroutine.
func (e *Env) Post(fn func()) {
	e.mu.Lock()
	e.queue = append(e.queue, fn)
	invalidate := e.invalidate
	e.mu.Unlock()
	if invalidate != nil {
		invalidate()
	}
}

// PostQuiet queues fn for the next frame like Post, without requesting
// one: for state no view shows until something else redraws. Safe from
// any goroutine.
func (e *Env) PostQuiet(fn func()) {
	e.mu.Lock()
	e.queue = append(e.queue, fn)
	e.mu.Unlock()
}

// Go runs work on a goroutine and posts done with its result to the UI
// thread. done may be nil.
func Go[T any](e *Env, work func(ctx context.Context) (T, error), done func(T, error)) {
	go func() {
		value, err := work(e.background)
		if done != nil {
			e.Post(func() { done(value, err) })
		}
	}()
}

// Run is Go for work without a result.
func (e *Env) Run(work func(ctx context.Context) error, done func(error)) {
	Go(e, func(ctx context.Context) (struct{}, error) { return struct{}{}, work(ctx) }, func(_ struct{}, err error) {
		if done != nil {
			done(err)
		}
	})
}

// Drain runs the functions posted since the last frame. The console calls
// it as each frame begins, with the window's services, which wake the
// window for later posts.
func (e *Env) Drain(c *ui.Context) {
	services := c.Services()
	e.mu.Lock()
	// The window draining is the one to wake: a window opened again after
	// its predecessor closed has services of its own.
	if !e.fixedInvalidate {
		e.invalidate = services.Invalidate
	}
	e.mu.Unlock()
	for {
		e.mu.Lock()
		queue := e.queue
		e.queue = nil
		e.mu.Unlock()
		if len(queue) == 0 {
			break
		}
		for _, fn := range queue {
			fn()
		}
	}
	for _, message := range e.toasts {
		c.Toast(message)
	}
	e.toasts = nil
}

// SetInvalidate sets what wakes the UI when work is posted, for hosts that
// build frames themselves, such as tests.
func (e *Env) SetInvalidate(fn func()) {
	e.mu.Lock()
	e.invalidate, e.fixedInvalidate = fn, fn != nil
	e.mu.Unlock()
}

// Toast shows a short message near the bottom of the window. UI thread.
func (e *Env) Toast(message string) {
	if message != "" {
		e.toasts = append(e.toasts, message)
	}
}

// ToastError shows err's message, if any. UI thread.
func (e *Env) ToastError(err error) {
	if err != nil {
		e.Toast(err.Error())
	}
}

// Navigate switches the console to page, passing arg to its workspace
// (see Opener). UI thread.
func (e *Env) Navigate(page string, arg any) {
	if e.navigate != nil {
		e.navigate(page, arg)
	}
}

// SetNavigator is how the console receives Navigate.
func (e *Env) SetNavigator(fn func(page string, arg any)) { e.navigate = fn }

// Visible reports whether the window is shown and not minimized; streams
// pause while it is not. UI thread.
func (e *Env) Visible() bool { return e.visible }

// SetVisible records the window's visibility. UI thread.
func (e *Env) SetVisible(visible bool) { e.visible = visible }

// Workspace is a page of the console.
type Workspace interface {
	// View builds the workspace, which fills the area it is given.
	View(c *ui.Context)
}

// Activator is a workspace that runs streams while shown: SetActive(true)
// as it becomes the visible page of a visible window, false as it stops.
type Activator interface {
	SetActive(active bool)
}

// Opener is a workspace that takes an argument from Navigate, as the
// processes workspace takes a PID to find.
type Opener interface {
	Open(arg any)
}
