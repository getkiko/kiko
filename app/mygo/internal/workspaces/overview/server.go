package overview

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// serverStatus shows, in the state card, the server the window shows:
// its name large, a dot while it is not connected, and its address; a remote
// server's name carries the server's color.
func (w *Workspace) serverStatus(c *ui.Context) {
	k := kit.T(c)
	target := w.env.Target
	name := target.Name
	if name == "" {
		name = "本机"
	}
	dot, state, healthy := k.Success, "已连接", false
	switch {
	case w.env.Server.NeedsPairing():
		dot, state = k.Danger, "需要重新配对"
	case !w.env.Server.Connected:
		dot, state = k.Warning, "未连接"
	default:
		healthy = true
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(target.URL, "https://"), "http://"), "/")
	ui.Column(c).Gap(3).MinWidth(0).Label(name + " " + host + " · " + state).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
			title := ui.Text(c, name).FontSize(22).FontWeight(600).TextColor(k.Label).SingleLine().Ellipsis("…").
				Shrink(1).MinWidth(0)
			if target.Remote {
				color := k.Accent
				if target.Color != "" {
					color = ui.Hex(target.Color)
				}
				title.TextColor(color)
			}
			// The dot shows only a problem: connected is the usual state.
			if !healthy {
				dotted := ui.Box(c).Size(8, 8).Radius(4).Background(dot).Shrink(0).Role(ui.RoleStatus).Label(state)
				kit.Hint(c, dotted, state)
			}
		})
		ui.Text(c, host).FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Ellipsis("…").
			Selectable()
	})
}
