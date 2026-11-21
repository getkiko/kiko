package favicon

import "github.com/getkiko/kiko/app/mygo/internal/env"

// Inbound is the inbound tag of the connections the server opens to fetch
// website icons (the server's faviconInbound).
const Inbound = "favicon"

// PrefHideConnections is the app preference hiding those connections
// from Connections, true when unset.
const PrefHideConnections = "hideFaviconConnections"

// HideConnections reports whether Connections leaves out the connections
// fetching website icons: true unless turned off in Settings.
func HideConnections(e *env.Env) bool {
	hide := true
	if e != nil {
		e.AppPrefs.Load(PrefHideConnections, &hide)
	}
	return hide
}

// SetHideConnections stores the choice and tells the windows.
func SetHideConnections(e *env.Env, hide bool) {
	if e == nil {
		return
	}
	e.AppPrefs.Save(PrefHideConnections, hide)
	if e.AppPrefsChanged != nil {
		e.AppPrefsChanged()
	}
}
