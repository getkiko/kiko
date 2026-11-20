// Package overview is the console's Overview page: the unapplied changes
// card, the console's one apply entry (apply.go), a dashboard grid of
// cards (the core's state and uptime with the round power button, upload
// and download with their peaks, the outbound mode (mode.go), memory and
// active connections, each metric with a smooth area chart), the
// Tailscale card (tailnet.go), the policy group card, the current profile
// with its management, with its preview and sharing.
package overview

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/subscriptions/endpoints"
)

// pageWidth is the most the page's content spreads (max-w-6xl).
const pageWidth = 1152

// cardsTop is where the first card starts, leaving the page some air
// below the window's top edge. A strip dragging the window fills the room
// above the page.
const cardsTop = 40

// cardShadowRoom is the page's top padding inside the scroll, which
// clips: room for the shadow above the first row of cards.
const cardShadowRoom = 3

// Workspace is the overview page.
type Workspace struct {
	env   *env.Env
	store *store.Store

	// Tailscale is the Tailscale endpoint's live state and control
	// sheet, shared with the Components page's Endpoints section; nil shows no
	// Tailscale card.
	Tailscale *endpoints.TailscalePanel

	active      bool
	cancel      context.CancelFunc
	unsubscribe []func()
	sync        runtimeSync

	metrics  metricsState
	mode     modeState
	selector selectorState
	power    powerControl
	apply    applyPanel
	profiles profilePanel
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	w := &Workspace{env: e, store: s}
	return w
}

// SetActive starts the page's streams as it shows (metrics every two
// seconds, the runtime watchdog, selector events; the Tailscale state
// while its card shows) and stops them as it hides.
func (w *Workspace) SetActive(active bool) {
	if active == w.active {
		return
	}
	w.active = active
	w.sync.reset()
	w.selector.invalidate()
	if !active {
		if w.Tailscale != nil {
			w.Tailscale.Follow(false)
		}
		if w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
		for _, stop := range w.unsubscribe {
			stop()
		}
		w.unsubscribe = nil
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	server := w.env.Server
	w.unsubscribe = append(w.unsubscribe,
		server.Subscribe(func(api.Event) { w.sync.update(time.Now()) }, "runtime"),
		server.Subscribe(func(event api.Event) {
			var payload struct {
				Selectors []api.Selector `json:"selectors"`
			}
			if event.Decode(&payload) == nil && payload.Selectors != nil {
				w.selector.setGroups(payload.Selectors)
				w.selector.err = ""
			}
		}, "selectors"),
	)
	w.refreshRuntime()
	go w.pollMetrics(ctx)
	go w.watchRuntime(ctx)
}

// refreshRuntime queries the runtime unless a query is pending; an event
// that arrives meanwhile wins over its answer.
func (w *Workspace) refreshRuntime() {
	query, ok := w.sync.begin()
	if !ok {
		return
	}
	env.Go(w.env, func(ctx context.Context) (api.Runtime, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return w.env.API.Runtime(ctx)
	}, func(runtime api.Runtime, err error) {
		if w.sync.finish(query, err == nil, time.Now()) {
			w.env.Server.SetRuntime(runtime)
		}
	})
}

// watchRuntime recovers missed events: without a runtime state for three
// seconds, it queries one.
func (w *Workspace) watchRuntime(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.env.Post(func() {
				if w.active && w.sync.stale(time.Now()) {
					w.refreshRuntime()
				}
			})
		}
	}
}

// View builds the page: a strip dragging the window, then the dashboard
// and the profile card scrolling on the window surface.
func (w *Workspace) View(c *ui.Context) {
	k := kit.T(c)
	w.selector.follow(w, c)
	w.selector.syncChooser()
	ui.Column(c).Fill().MinWidth(0).MinHeight(0).Background(k.Canvas).Children(func() {
		ui.Box(c.Key("overview-drag")).Height(cardsTop - cardShadowRoom).FillWidth().Shrink(0).DragWindow()
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			// The grid's columns follow the page column's width less the
			// side padding.
			page := ui.Column(c.Key("overview-page")).FillWidth().MaxWidth(pageWidth).Margin(0, ui.Auto).Padding(cardShadowRoom, 20, 24)
			if b := page.Bounds(); b.W > 40 {
				w.metrics.width = b.W - 40
			}
			page.Children(func() {
				w.dashboard(c)
				w.tailscaleCard(c)
				w.selectorGroups(c)
			})
		})
	})
	w.groupSheet(c)
	if w.Tailscale != nil {
		w.Tailscale.View(c)
	}
	w.selectorDialog(c)
	w.profileDialogs(c)
}
