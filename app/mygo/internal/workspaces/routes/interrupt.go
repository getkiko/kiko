package routes

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// routesTraffic reports whether the rule sends its connections to an
// outbound; a rule without an action does.
func routesTraffic(r *model.RouteRule) bool {
	return r.Action == "" || r.Action == model.ActionRoute
}

// exitChanged reports whether saving next moves the connections of the
// stored traffic rule old to another outbound: both route to an outbound
// and the outbound differs from from, the one the connections use (the
// stored one, or the one a pending answer was given against). Other
// fields, a rule that was off or sent nowhere, and a reject or resolve
// action ask nothing.
func exitChanged(from string, old, next *model.RouteRule) bool {
	if old == nil || next == nil || !old.Enabled || !routesTraffic(old) || !routesTraffic(next) {
		return false
	}
	return from != "" && from != next.Outbound.ID()
}

// exitBase is the outbound the connections of the stored rule use: the one
// of the configuration last applied, else the one a pending answer was
// given against, else the stored one.
func (w *Workspace) exitBase(old *model.RouteRule) string {
	if exit, ok := w.store.AppliedExit(old.ID); ok {
		return exit
	}
	if from, ok := w.store.InterruptFrom(old.ID); ok {
		return from
	}
	return old.Outbound.ID()
}

// asksToInterrupt reports whether saving next for the open editor asks
// about the rule's existing connections: a stored traffic rule whose
// outbound changes while the core runs. DNS rules never ask: no
// connection records a DNS rule, so there is none to close.
func (w *Workspace) asksToInterrupt(e *ruleEditor, next *model.RouteRule) bool {
	if e.isNew || e.dns {
		return false
	}
	server := w.env.Server
	if !server.HasRuntime || !server.Runtime.Running() {
		return false
	}
	doc := w.doc()
	for _, r := range doc.Route.Rules {
		if r.ID == next.ID {
			return w.scopeCarriesConnections(doc, r.PolicyOwner()) && exitChanged(w.exitBase(r), r, next)
		}
	}
	return false
}

// scopeCarriesConnections reports whether the connections of the core can
// match rules of the scope: a fixed scope always, a device's only while it
// is the profile in effect and the one last applied. The rules of another
// profile have no connections, so editing their exits asks nothing.
func (w *Workspace) scopeCarriesConnections(doc *model.Doc, scope string) bool {
	d, _, ok := model.ScopeDevice(doc, scope)
	if !ok {
		return true
	}
	if model.ActiveScope(d) != scope {
		return false
	}
	if applied, known := w.store.AppliedScope(d.ID); known && applied != scope {
		return false
	}
	return true
}

// interruptChoices are the answers of the question; the first keeps the
// connections where they are.
const (
	keepConnections      = 0
	interruptConnections = 1
)

// interruptPopover asks, beside the save button, whether the connections
// the rule carries are closed when the configuration is applied. Saving
// only records the answer; the Overview apply closes the connections.
func (w *Workspace) interruptPopover(c *ui.Context, anchor ui.Element, e *ruleEditor) {
	if e.dns || e.isNew {
		return
	}
	kit.ConfirmPopover(c, anchor, &e.ask, kit.ConfirmOptions{
		Title:   "中断已有连接",
		Message: "「" + displayName(e.route.Name) + "」的出口已更改。",
		Confirm: "确定",
		Body: func() {
			kit.ChoiceList(c, "已有连接", &e.askChoice,
				kit.Choice{Label: "保持现有连接"},
				kit.Choice{Label: "中断该规则下的连接"})
		},
		Run: func(done func(error)) {
			if w.commitRoute(e, e.askRule, e.askChoice == interruptConnections) {
				done(nil)
				w.closeEditor()
				return
			}
			done(errString(w.err))
		},
	})
}
