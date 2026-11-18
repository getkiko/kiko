package settings

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// dnsView is Settings › DNS Servers: the global DNS settings, then every DNS
// server as a row (the public presets listed disabled until enabled),
// with the editor for a custom server.
func (w *Workspace) dnsView(c *ui.Context) {
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backRow(c, w)
			kit.PaneTitle(c, "DNS 服务器", "", nil)
			w.dns.Settings(c)
			w.dns.View(c)
		})
	})
}
