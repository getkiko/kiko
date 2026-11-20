package overview

import (
	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// tailscaleLabel names the Tailscale card's button.
const tailscaleLabel = "Tailscale 设备与状态"

// tailscaleCard is the Tailscale endpoint's live state across the page,
// under the metrics: its logo, coloured by the state, and the state's words, this device's name and
// Tailscale IPv4, the tailnet, the exit node, the online devices' UDP
// paths and avatars. It shows while the document's Tailscale endpoint is
// enabled and complete (TailscalePanel.Endpoint); with the core stopped
// the logo turns gray and the last facts seen dim. The card opens the
// endpoint's control sheet. Showing, it keeps the endpoint's state
// followed.
func (w *Workspace) tailscaleCard(c *ui.Context) {
	p := w.Tailscale
	if p == nil {
		return
	}
	endpoint := p.Endpoint()
	p.Follow(w.active && endpoint != nil)
	if endpoint == nil {
		return
	}
	k := kit.T(c)
	m := p.Summary(c, endpoint.Tag)
	_, glow := kit.TypeLook(c, "endpoint", "tailscale")
	value, caption, detail := k.Label, k.Label2, k.Label3
	if !m.Live {
		value, caption, detail = k.Label3, k.Label3, k.Label4
	}
	// The facts beside the state need the room of a two-column grid.
	wide := w.metrics.width <= 0 || w.metrics.width >= 640
	tag := endpoint.Tag
	card := kit.CardSurface(c, ui.ButtonBase(c.Key("tailscale-card")), 20).Margin(12, 0, 0).Padding(16, 18).Gap(16).
		MinWidth(0).Shrink(0).AlignItems(ui.Center).Justify(ui.Start).Clip().Label(tailscaleLabel).Transition(kit.HoverMotion)
	// Hovered, the shadow deepens or, in dark mode, the border, as the
	// Components page's hero cards.
	if card.Hovered() {
		if k.Dark {
			card.Border(1, k.Hairline)
		} else {
			card.Shadow(0, 2, 10, 0, k.Shadow)
		}
	}
	if card.Pressed() {
		card.Opacity(0.75)
	}
	card.Children(func() {
		alpha := float32(0.14)
		if k.Dark {
			alpha = 0.18
		}
		ui.Box(c).Absolute().Top(-26).Right(-26).Size(84, 84).Radius(42).Background(glow.Alpha(alpha)).PassThrough()
		kit.TailscaleMarkMoving(c, 34, m.StateColor(c), m.Motion)
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(2).AlignItems(ui.Start).Children(func() {
			ui.Text(c, "Tailscale").FontSize(kit.FontCaption).TextColor(caption).SingleLine().Ellipsis("…").
				Shrink(1).MinWidth(0)
			ui.Text(c, m.State).FontSize(18).FontWeight(600).TextColor(value).SingleLine().Ellipsis("…").
				Shrink(1).MinWidth(0)
			if m.Self != "" || m.IPv4 != "" {
				ui.Row(c).Gap(6).MinWidth(0).AlignItems(ui.Center).FontSize(kit.FontCaption).TextColor(detail).Children(func() {
					if m.Self != "" {
						ui.Text(c, m.Self).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
					}
					if m.IPv4 != "" {
						ui.Text(c, m.IPv4).Font(kit.MonoFont).FontFeatures("tnum").SingleLine().Shrink(0)
					}
				})
			}
		})
		if wide {
			ui.Row(c).Gap(28).Shrink(1).MinWidth(0).AlignItems(ui.Center).Children(func() {
				tailscaleFact(c, "Tailnet", m.Tailnet, value)
				tailscaleFact(c, "出口节点", m.ExitNode, value)
				tailscaleFact(c, "UDP 路径", m.UDPPath, value)
			})
		}
		// A stuck control connection offers a reconnect on the card, as
		// well as in its sheet.
		p.ReconnectButton(c, m)
		m.Avatars(c)
		kit.Icon(c, kit.SymChevronRight, 16).TextColor(k.Label3)
	})
	if card.Clicked() {
		p.Open(tag)
	}
}

// tailscaleFact is a caption over a value on the Tailscale card, nothing
// without a value.
func tailscaleFact(c *ui.Context, label, value string, color ui.Color) {
	if value == "" {
		return
	}
	k := kit.T(c)
	ui.Column(c.Key(label)).Gap(3).MinWidth(0).Shrink(1).Label(label).Children(func() {
		ui.Text(c, label).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine()
		ui.Text(c, value).FontSize(kit.FontBody).FontWeight(600).TextColor(color).FontFeatures("tnum").
			SingleLine().Ellipsis("…").MinWidth(0)
	})
}
