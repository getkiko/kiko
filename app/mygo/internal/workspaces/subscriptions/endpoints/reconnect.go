package endpoints

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// reconnectHold is how long after a reconnect the endpoint shows it under
// way while its state has not come back yet.
const reconnectHold = 10 * time.Second

// lostControl is the state line of a lost control connection, the one a
// reconnect mends. A stale state is the app's stream gone quiet, and a
// connection warning is about relays or UDP: restarting the control
// connection would not help them.
const lostControl = "控制连接已断开"

// reconnectable reports whether a live endpoint's state line offers a
// reconnect.
func reconnectable(live bool, line stateLine) bool {
	return live && line.text == lostControl
}

// reconnects are the Tailscale endpoints reconnecting, by tag, with when
// each began.
type reconnects map[string]time.Time

// busy reports whether tag's reconnect is under way at now.
func (r reconnects) busy(tag string, now time.Time) bool {
	at, ok := r[tag]
	return ok && now.Sub(at) < reconnectHold
}

// reconnectTailscale restarts tag's connection to its control server,
// once at a time; a failure shows as a toast.
func (s *Section) reconnectTailscale(tag string) {
	now := time.Now()
	if s.reconnects.busy(tag, now) {
		return
	}
	if s.reconnects == nil {
		s.reconnects = reconnects{}
	}
	s.reconnects[tag] = now
	s.env.Run(func(ctx context.Context) error { return s.env.API.TailscaleReconnect(ctx, tag) }, func(err error) {
		if err != nil {
			delete(s.reconnects, tag)
			s.env.Toast("重新连接失败：" + err.Error())
		}
	})
}

// ReconnectButton is the "Reconnect" button of a summary that offers it,
// "Reconnecting…" and disabled while one is under way; nothing otherwise.
func (p *TailscalePanel) ReconnectButton(c *ui.Context, m TailscaleSummary) {
	if !m.Reconnect {
		return
	}
	label := "重新连接"
	if m.Reconnecting {
		label = "重新连接中…"
		c.After(time.Second)
	}
	button := kit.Button(c, label, kit.ButtonOptions{Small: true, Icon: kit.SymRotateCw, Disabled: m.Reconnecting})
	if button.Clicked() && !m.Reconnecting {
		p.s.reconnectTailscale(m.tag)
	}
}
