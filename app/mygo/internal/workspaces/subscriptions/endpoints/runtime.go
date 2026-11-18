package endpoints

import (
	"context"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// runtimeState is the endpoints' live state: from
// GET /endpoints/status and GET /usbip/share when it starts, then the
// server's events. It runs while a page showing it holds it (hold): the
// Components page, the Overview Tailscale card, one subscription for
// both. The USB/IP environment check and the Taildrop sends live here too.
type runtimeState struct {
	env *env.Env

	tailscale map[string]*api.TailscaleStatus
	// lastTailscale is each endpoint's latest Tailscale state, kept as
	// the core stops: what the Overview card shows, dimmed, meanwhile.
	lastTailscale map[string]*api.TailscaleStatus
	openconnect   map[string]*api.OpenConnectStatus
	openvpn       map[string]*api.OpenVPNStatus
	taildrop      map[string]*api.TaildropInbox
	usbip         map[string]*api.USBIPServerStatus
	share         *api.USBIPShareStatus

	preflight        *api.USBIPPreflight
	preflightLoading bool
	preflightRun     int

	sends *taildropTracker

	// holders are the pages holding the state running.
	holders     map[string]bool
	active      bool
	unsubscribe func()
	generation  int
	// live are the states events brought since the section started
	// ("taildrop:" + tag for an inbox; "all" after the runtime
	// stopped): newer than the snapshot requested at the start, which
	// leaves them as they are.
	live map[string]bool
}

func newRuntimeState(e *env.Env) *runtimeState {
	r := &runtimeState{env: e, holders: map[string]bool{}, lastTailscale: map[string]*api.TailscaleStatus{}}
	r.reset()
	r.sends = newTaildropTracker(func(task api.TaildropSendTask) { e.Toast(taildropOutcome(task)) })
	return r
}

func (r *runtimeState) reset() {
	r.tailscale = map[string]*api.TailscaleStatus{}
	r.openconnect = map[string]*api.OpenConnectStatus{}
	r.openvpn = map[string]*api.OpenVPNStatus{}
	r.taildrop = map[string]*api.TaildropInbox{}
	r.usbip = map[string]*api.USBIPServerStatus{}
}

// hold records whether holder needs the state: it runs while one does.
// Cheap to call every frame. UI thread.
func (r *runtimeState) hold(holder string, on bool) {
	if r.holders[holder] == on {
		return
	}
	if on {
		r.holders[holder] = true
		r.start()
		return
	}
	delete(r.holders, holder)
	if len(r.holders) == 0 {
		r.stop()
	}
}

// start loads the snapshots and follows the events. UI thread.
func (r *runtimeState) start() {
	if r.active {
		return
	}
	r.active = true
	r.generation++
	generation := r.generation
	r.live = map[string]bool{}
	r.unsubscribe = r.env.Server.Subscribe(r.handle, "tailscale", "openconnect", "openvpn", "taildrop", "taildropSend", "usbip", "usbipShare", "runtime")
	// Events that came while the section was hidden.
	for _, t := range []string{"tailscale", "openconnect", "openvpn", "usbip", "usbipShare"} {
		if event, ok := r.env.Server.Latest[t]; ok {
			r.apply(event)
		}
	}
	env.Go(r.env, func(ctx context.Context) (api.EndpointsSnapshot, error) {
		return r.env.API.EndpointsStatus(ctx)
	}, func(snapshot api.EndpointsSnapshot, err error) {
		if err != nil || generation != r.generation {
			return
		}
		r.applySnapshot(snapshot)
	})
	env.Go(r.env, func(ctx context.Context) (api.USBIPShareStatus, error) {
		return r.env.API.USBIPShare(ctx)
	}, func(share api.USBIPShareStatus, err error) {
		if err == nil && generation == r.generation && !r.live["usbipShare"] {
			r.share = &share
		}
	})
	env.Go(r.env, func(ctx context.Context) ([]api.TaildropSendTask, error) {
		return r.env.API.TaildropSends(ctx)
	}, func(tasks []api.TaildropSendTask, err error) {
		if err == nil {
			for _, t := range tasks {
				r.sends.merge(t, false)
			}
		}
	})
}

// stop ends the subscription. UI thread.
func (r *runtimeState) stop() {
	if !r.active {
		return
	}
	r.active = false
	r.generation++
	if r.unsubscribe != nil {
		r.unsubscribe()
		r.unsubscribe = nil
	}
}

// applySnapshot takes the start's snapshot, but for the states events
// have brought since.
func (r *runtimeState) applySnapshot(s api.EndpointsSnapshot) {
	if r.live["all"] {
		return
	}
	if !r.live["tailscale"] {
		r.tailscale = map[string]*api.TailscaleStatus{}
		if s.Tailscale != nil {
			r.setTailscale(*s.Tailscale)
		}
	}
	if !r.live["openconnect"] {
		r.openconnect = map[string]*api.OpenConnectStatus{}
		if s.OpenConnect != nil {
			r.setOpenConnect(*s.OpenConnect)
		}
	}
	if !r.live["openvpn"] {
		r.openvpn = map[string]*api.OpenVPNStatus{}
		if s.OpenVPN != nil {
			r.setOpenVPN(*s.OpenVPN)
		}
	}
	taildrop := map[string]*api.TaildropInbox{}
	for tag, inbox := range r.taildrop {
		if r.live["taildrop:"+tag] {
			taildrop[tag] = inbox
		}
	}
	for tag, inbox := range s.Taildrop {
		if !r.live["taildrop:"+tag] {
			taildrop[tag] = &inbox
		}
	}
	r.taildrop = taildrop
	if !r.live["usbip"] {
		r.usbip = map[string]*api.USBIPServerStatus{}
		if s.USBIP != nil {
			r.setUSBIP(*s.USBIP)
		}
	}
}

func (r *runtimeState) setTailscale(u api.TailscaleUpdate) {
	r.tailscale = map[string]*api.TailscaleStatus{}
	for i := range u.Endpoints {
		r.tailscale[u.Endpoints[i].EndpointTag] = &u.Endpoints[i]
		if r.lastTailscale != nil {
			r.lastTailscale[u.Endpoints[i].EndpointTag] = &u.Endpoints[i]
		}
	}
}

func (r *runtimeState) setOpenConnect(u api.OpenConnectUpdate) {
	r.openconnect = map[string]*api.OpenConnectStatus{}
	for i := range u.Endpoints {
		r.openconnect[u.Endpoints[i].EndpointTag] = &u.Endpoints[i]
	}
}

func (r *runtimeState) setOpenVPN(u api.OpenVPNUpdate) {
	r.openvpn = map[string]*api.OpenVPNStatus{}
	for i := range u.Endpoints {
		r.openvpn[u.Endpoints[i].EndpointTag] = &u.Endpoints[i]
	}
}

func (r *runtimeState) setUSBIP(u api.USBIPUpdate) {
	r.usbip = map[string]*api.USBIPServerStatus{}
	for i := range u.Servers {
		r.usbip[u.Servers[i].ServerTag] = &u.Servers[i]
	}
}

// handle applies a live event, newer than the start's snapshot. UI
// thread.
func (r *runtimeState) handle(event api.Event) {
	if r.live != nil {
		switch event.Type {
		case "taildrop":
			var inbox api.TaildropInbox
			if event.Decode(&inbox) == nil {
				r.live["taildrop:"+inbox.EndpointTag] = true
			}
		case "runtime":
			var rt struct {
				State string `json:"state"`
			}
			if event.Decode(&rt) == nil && rt.State != "running" {
				r.live["all"] = true
			}
		default:
			r.live[event.Type] = true
		}
	}
	r.apply(event)
}

// apply applies an event. UI thread.
func (r *runtimeState) apply(event api.Event) {
	switch event.Type {
	case "tailscale":
		var u api.TailscaleUpdate
		if event.Decode(&u) == nil {
			r.setTailscale(u)
		}
	case "openconnect":
		var u api.OpenConnectUpdate
		if event.Decode(&u) == nil {
			r.setOpenConnect(u)
		}
	case "openvpn":
		var u api.OpenVPNUpdate
		if event.Decode(&u) == nil {
			r.setOpenVPN(u)
		}
	case "taildrop":
		var inbox api.TaildropInbox
		if event.Decode(&inbox) == nil {
			r.taildrop[inbox.EndpointTag] = &inbox
		}
	case "taildropSend":
		var task api.TaildropSendTask
		if event.Decode(&task) == nil {
			r.sends.merge(task, true)
		}
	case "usbip":
		var u api.USBIPUpdate
		if event.Decode(&u) == nil {
			r.setUSBIP(u)
		}
	case "usbipShare":
		var share api.USBIPShareStatus
		if event.Decode(&share) == nil {
			r.share = &share
		}
	case "runtime":
		var rt struct {
			State string `json:"state"`
		}
		if event.Decode(&rt) == nil && rt.State != "running" {
			r.reset()
		}
	}
}

// tailscaleStatus is an endpoint's Tailscale state, marked stale while
// the event stream is down.
func (r *runtimeState) tailscaleStatus(tag string) *api.TailscaleStatus {
	s := r.tailscale[r.liveTailscaleTag(tag)]
	if s == nil || r.env.Server.Connected {
		return s
	}
	stale := *s
	stale.ObservedAt = "1"
	return &stale
}

// liveTailscaleTag is the tag the running core knows the Tailscale
// endpoint of tag by: tag, or, until the document's renamed endpoint is
// applied (model.TailscaleTag), the one endpoint the core reports.
func (r *runtimeState) liveTailscaleTag(tag string) string {
	if r.tailscale[tag] != nil || r.lastTailscale[tag] != nil {
		return tag
	}
	if len(r.tailscale) == 1 {
		for live := range r.tailscale {
			return live
		}
	}
	if len(r.lastTailscale) == 1 {
		for last := range r.lastTailscale {
			return last
		}
	}
	return tag
}

// tailscaleStatuses is every endpoint's state, for the USB/IP guide.
func (r *runtimeState) tailscaleStatuses() map[string]*api.TailscaleStatus {
	out := map[string]*api.TailscaleStatus{}
	for tag := range r.tailscale {
		out[tag] = r.tailscaleStatus(tag)
	}
	return out
}

// refreshPreflight checks the host for USB/IP; a failure leaves nil.
func (r *runtimeState) refreshPreflight() {
	r.preflightRun++
	run := r.preflightRun
	r.preflightLoading = true
	env.Go(r.env, func(ctx context.Context) (api.USBIPPreflight, error) {
		return r.env.API.USBIPPreflightCheck(ctx)
	}, func(p api.USBIPPreflight, err error) {
		if run != r.preflightRun {
			return
		}
		r.preflightLoading = false
		if err != nil {
			r.preflight = nil
			return
		}
		r.preflight = &p
	})
}

// guideContext is the checklist's view of the host.
func (r *runtimeState) guideContext() guideContext {
	ctx := guideContext{preflight: r.preflight, tailscale: r.tailscaleStatuses(), serverRuntime: true}
	if r.preflight != nil {
		ctx.os = r.preflight.OS
	}
	return ctx
}

// refreshSends reloads the sends, recovering missed outcomes.
func (r *runtimeState) refreshSends() {
	env.Go(r.env, func(ctx context.Context) ([]api.TaildropSendTask, error) {
		return r.env.API.TaildropSends(ctx)
	}, func(tasks []api.TaildropSendTask, err error) {
		if err == nil {
			for _, t := range tasks {
				r.sends.merge(t, false)
			}
		}
	})
}

// freshness is how often a view showing Tailscale states redraws, so an
// expired observation loses its green state while the stream is quiet.
const freshness = 5 * time.Second
