// Package settings is the console's Settings workspace and the pages it
// opens: the shared network and TLS config libraries, the system proxy
// settings, the encrypted backup and sync, and Settings › DNS: the DNS
// settings and servers (the package dnsservers). There is no theme toggle:
// the console follows the system's appearance.
package settings

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/settings/dnsservers"
)

// Pages of the workspace.
const (
	pageMain     = ""
	pageNetwork  = "network"
	pageTLS      = "tls"
	pageServers  = "servers"
	pageRemote   = "remote"
	pageDNS      = "dns"
	pageRegions  = "regions"
	pageHTTPS    = "https"
	pageRuleSets = "rulesets"
)

// proxyPollInterval is how often the system proxy's state is read while
// the page shows.
const proxyPollInterval = 10 * time.Second

// Workspace is the settings page.
type Workspace struct {
	env   *env.Env
	store *store.Store

	// RuleSets are the rule set pages of the Routing workspace, shown here as
	// a subpage (set by the console): they open from Network and drop their
	// state as the workspace stops showing.
	RuleSets RuleSetsPages

	page string
	// returned is the page just left: its row on the main page takes the
	// focus, so that the keyboard carries on where it was.
	returned string
	// focusBack moves the focus to the back button: a first Escape in a
	// text field leaves the field.
	focusBack bool
	library   *library
	servers   *serversPage
	remote    *remotePage
	dns       *dnsservers.Section
	regions   regionsPage
	https     *httpsPage

	info  *api.ServerInfo
	proxy *api.SystemProxyStatus
	// proxyWrites counts the system proxy's saves, started and answered:
	// a read that a save overtook does not show its older state.
	proxyWrites int
	sync        *api.SyncStatus
	helper      *env.HelperStatus
	helperErr   string
	helperBusy  bool

	// connections are the server's connection history settings, nil until
	// loaded (or on a server without them); connectionsLast is the limit
	// the switch restores.
	connections     *api.ConnectionSettings
	connectionsLast int
	connectionsBusy bool

	logBusy    bool
	logCleanup *api.LogHistory
	logConfirm kit.ConfirmState
	// favicons is what the server keeps of the websites' icons, nil until
	// read; faviconConfirm confirms clearing them.
	favicons       *api.FaviconCache
	faviconLoading bool
	faviconFailed  bool
	faviconConfirm kit.ConfirmState
	// logSize is the server's log sizes, nil until loaded (or on a server
	// without GET /logs/size).
	logSize     *api.LogSize
	coreConfirm kit.ConfirmState

	proxyDialog proxyDialog
	backup      backupDialog
	zones       zoneOptions
	share       shareOrigin

	active      bool
	cancelPoll  context.CancelFunc
	unsubscribe func()
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	w := &Workspace{env: e, store: s, connectionsLast: defaultClosedLimit}
	w.library = newLibrary(w)
	w.servers = newServersPage(w)
	w.remote = newRemotePage(w)
	w.dns = dnsservers.New(e, s)
	w.regions.w = w
	w.https = newHTTPSPage(w)
	w.backup.w = w
	w.proxyDialog.w = w
	return w
}

// Open shows the network ("network") or TLS ("tls") config library, the
// servers ("servers", "servers:add", "servers:repair:<id>"), remote
// access ("remote"), the DNS settings and servers ("dns"), the node
// region table ("regions"), the rule sets ("rulesets"), the main page
// with the share address focused ("share-origin"), or the main page for
// anything else.
func (w *Workspace) Open(arg any) {
	w.share.reset()
	w.returned, w.focusBack = "", false
	page, _ := arg.(string)
	page, action, _ := strings.Cut(page, ":")
	switch page {
	case pageNetwork, pageTLS:
		w.library.show(page)
		w.page = page
	case pageServers:
		w.page = page
		w.servers.open(action)
	case pageRemote:
		w.page = page
		if w.active {
			w.remote.load()
		}
	case pageDNS:
		w.page = page
	case pageRegions:
		w.page = page
		w.regions.open()
	case pageHTTPS:
		w.page = page
		if w.active {
			w.https.start()
		}
	case profiles.ShareOriginSetting:
		w.page, w.share.focus = pageMain, true
	case pageRuleSets:
		if w.RuleSets == nil {
			w.page = pageMain
			return
		}
		w.page = page
		w.RuleSets.OpenRuleSets()
	default:
		w.page = pageMain
	}
}

// SetActive follows the backup state and polls the system proxy while the
// page shows; the rule set pages drop their state as it stops showing.
func (w *Workspace) SetActive(active bool) {
	if active == w.active {
		return
	}
	w.active = active
	if active {
		// The cache changes as pages show icons: read it anew.
		w.favicons, w.faviconFailed = nil, false
	}
	if !active {
		w.share.reset()
		if w.RuleSets != nil {
			w.RuleSets.CloseRuleSets()
		}
		w.https.stop()
		if w.cancelPoll != nil {
			w.cancelPoll()
			w.cancelPoll = nil
		}
		if w.unsubscribe != nil {
			w.unsubscribe()
			w.unsubscribe = nil
		}
		return
	}
	w.unsubscribe = w.env.Server.Subscribe(func(event api.Event) {
		var status api.SyncStatus
		if event.Decode(&status) == nil {
			w.sync = &status
		}
	}, "sync")
	w.refreshSync()
	w.loadInfo()
	w.loadHelper()
	w.loadConnections()
	w.loadLogSize()
	w.remote.load()
	if w.page == pageHTTPS {
		w.https.start()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancelPoll = cancel
	go func() {
		ticker := time.NewTicker(proxyPollInterval)
		defer ticker.Stop()
		for {
			w.env.Post(w.loadProxy)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (w *Workspace) refreshSync() {
	env.Go(w.env, func(ctx context.Context) (api.SyncStatus, error) {
		return w.env.API.SyncStatus(ctx)
	}, func(status api.SyncStatus, err error) {
		if err == nil {
			w.sync = &status
		}
	})
}

func (w *Workspace) loadInfo() {
	env.Go(w.env, func(ctx context.Context) (api.ServerInfo, error) {
		return w.env.API.Info(ctx)
	}, func(info api.ServerInfo, err error) {
		if err == nil {
			w.info = &info
		}
	})
}

func (w *Workspace) loadConnections() {
	env.Go(w.env, func(ctx context.Context) (api.ConnectionSettings, error) {
		return w.env.API.ConnectionSettings(ctx)
	}, func(settings api.ConnectionSettings, err error) {
		if err == nil && !w.connectionsBusy {
			w.connections = &settings
		}
	})
}

func (w *Workspace) loadProxy() {
	writes := w.proxyWrites
	env.Go(w.env, func(ctx context.Context) (api.SystemProxyStatus, error) {
		return w.env.API.SystemProxy(ctx)
	}, func(status api.SystemProxyStatus, err error) {
		if err == nil && writes == w.proxyWrites {
			w.proxy = &status
		}
	})
}

// saveProxy writes the system proxy's settings; done receives the
// outcome after the state shown follows it.
func (w *Workspace) saveProxy(settings api.SystemProxySettings, done func(error)) {
	w.proxyWrites++
	env.Go(w.env, func(ctx context.Context) (api.SystemProxyStatus, error) {
		return w.env.API.SaveSystemProxy(ctx, settings)
	}, func(status api.SystemProxyStatus, err error) {
		w.proxyWrites++
		if err == nil {
			w.proxy = &status
		}
		done(err)
	})
}

// RuleSetsPages are the rule set pages, which another workspace owns:
// OpenRuleSets shows their list, RuleSetsPage builds the page shown (back
// returns to Settings), CloseRuleSets drops their state. Escape is the
// workspace's: RuleSetsEscape goes back a level within the pages (from a
// rule set to the list), false when there is none, and FocusRuleSetsBack
// moves the focus to the page's back button.
type RuleSetsPages interface {
	OpenRuleSets()
	RuleSetsPage(c *ui.Context, back func())
	RuleSetsEscape() bool
	FocusRuleSetsBack()
	CloseRuleSets()
}

// ruleSetsSummary is Settings › Rule Sets on its row: how many there are.
func ruleSetsSummary(doc *model.Doc) string {
	n := len(model.BuiltinRuleSets)
	for _, r := range doc.Route.RuleSets {
		if _, builtin := model.BuiltinRuleSetForEntry(r); !builtin {
			n++
		}
	}
	return fmt.Sprintf("%d 个规则集", n)
}

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	kit.BeginTextFocus(c)
	switch w.page {
	case pageNetwork, pageTLS:
		w.library.view(c)
	case pageServers:
		w.servers.view(c)
	case pageRemote:
		w.remote.view(c)
	case pageDNS:
		w.dnsView(c)
	case pageRegions:
		w.regions.view(c)
	case pageHTTPS:
		w.https.view(c)
	case pageRuleSets:
		if w.RuleSets == nil {
			w.mainView(c)
			return
		}
		w.RuleSets.RuleSetsPage(c, w.back)
	default:
		w.mainView(c)
	}
	// Escape returns from a subpage unless something around the focus, a
	// popover or a dialog takes it first (a dialog makes the window's
	// shortcuts wait); in a text field it first leaves the field for the
	// back button.
	if w.page != pageMain && c.Shortcut(0, ui.KeyEscape) {
		ruleSets := w.page == pageRuleSets && w.RuleSets != nil
		switch {
		case kit.TextFocused(c) && ruleSets:
			w.RuleSets.FocusRuleSetsBack()
		case kit.TextFocused(c):
			w.focusBack = true
		case ruleSets && w.RuleSets.RuleSetsEscape():
		default:
			w.back()
		}
	}
}

// back returns from a subpage to the main page, closing what the subpage
// left open.
func (w *Workspace) back() {
	switch w.page {
	case pageMain:
		return
	case pageNetwork, pageTLS:
		w.library.close()
	case pageRuleSets:
		if w.RuleSets != nil {
			w.RuleSets.CloseRuleSets()
		}
	}
	w.returned, w.page, w.focusBack = w.page, pageMain, false
}
