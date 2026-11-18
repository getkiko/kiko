package overview

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// clashModes are the core's outbound modes (the Clash API mode), in the
// order the Outbound Mode card shows them.
var clashModes = []struct {
	value, label string
	icon         kit.Symbol
}{
	{"Rule", "规则", kit.SymList},
	{"Global", "全局", kit.SymGlobe},
	{"Direct", "直连", kit.SymCircleDot},
}

// modeDeadline bounds switching the mode.
var modeDeadline = deadline{8 * time.Second, "切换出站模式超时（8 秒）"}

// modeState is the Outbound Mode card's state. kiko-server sets the running
// core's mode but does not report it, so the card remembers the mode it
// set and the core run it set it in.
type modeState struct {
	// live is the mode set in the core started at startedAt.
	live, startedAt string
	// switching is the mode being set, "" when none.
	switching string
}

// configuredMode is the configuration's default mode (the Clash API's
// default_mode, Rule unless set).
func configuredMode(doc *model.Doc) string {
	if doc != nil && doc.Experimental.ClashAPI {
		for _, m := range clashModes {
			if strings.EqualFold(m.value, doc.Experimental.DefaultMode) {
				return m.value
			}
		}
	}
	return "Rule"
}

// currentMode is the mode the card shows: the one set in the running
// core, which a restart keeps when the cache file stores it, else the
// configuration's.
func (w *Workspace) currentMode(running bool) string {
	doc := w.store.Doc()
	s := &w.mode
	if running && s.live != "" && (s.startedAt == w.env.Server.Runtime.StartedAt || doc.Experimental.CacheFile) {
		return s.live
	}
	return configuredMode(doc)
}

// setMode switches the running core to mode, one switch at a time.
func (w *Workspace) setMode(mode string) {
	s := &w.mode
	if s.switching != "" {
		return
	}
	s.switching = mode
	startedAt := w.env.Server.Runtime.StartedAt
	w.env.Run(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, modeDeadline.timeout)
		defer cancel()
		return w.env.API.SetMode(ctx, mode)
	}, func(err error) {
		s.switching = ""
		if err != nil {
			w.env.Toast("切换出站模式失败：" + messageFor(err, modeDeadline))
			return
		}
		s.live, s.startedAt = mode, startedAt
	})
}

// modeCard is the Outbound Mode card: Rule, Global and Direct as large segments,
// the current one filled blue. It switches the running core; while the
// core is stopped it shows the configuration's mode, disabled.
func (w *Workspace) modeCard(c *ui.Context, span int) {
	k := kit.T(c)
	server := w.env.Server
	running := server.HasRuntime && server.Runtime.Running()
	current := w.currentMode(running)
	s := &w.mode
	// One row: the title on the left, centered on the segments filling
	// the rest.
	dashboardCard(c, "出站模式", span).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
				kit.Icon(c, kit.SymRoute, 16).TextColor(k.Label2)
				ui.Text(c, "出站模式").FontSize(kit.FontBody).FontWeight(600).TextColor(k.Label).SingleLine()
			})
			// While the core is stopped the segments show the configured mode
			// without inviting a press: the others outlined, their words in
			// the secondary label color, the configured one on a neutral fill
			// in the label color with a blue symbol, never faded. Running, the current one is
			// filled blue and the others take hover and press fills.
			ui.Row(c).Gap(8).Grow(1).MinWidth(0).Label("出站模式选项").Children(func() {
				for _, m := range clashModes {
					selected := m.value == current
					b := ui.ButtonBase(c.Key("mode-" + m.value)).Height(40).Grow(1).Basis(0).MinWidth(0).
						Radius(14).Gap(6).Center().Label(m.label).Role(ui.RoleRadio).Disabled(!running || s.switching != "")
					fg, bg, iconColor := k.Label, k.Fill, k.Label
					switch {
					case !running && selected:
						fg, bg, iconColor = k.Label, k.Fill2, k.Accent
						b.Border(1, k.Hairline)
					case !running:
						fg, bg, iconColor = k.Label2, ui.Color{}, k.Label3
						b.Border(1, k.Hairline)
					case selected:
						fg, bg, iconColor = k.OnAccent, k.Accent, k.OnAccent
					case b.Pressed():
						bg = k.Fill2
					case b.Hovered():
						bg = k.Fill.Mix(k.Fill2, 0.5)
					}
					// Selected reports the choice to assistive technologies;
					// the colors after it replace its accent look.
					b.Selected(selected).Background(bg).TextColor(fg).Transition(kit.HoverMotion)
					b.Children(func() {
						if s.switching == m.value {
							kit.SpinnerIn(c, 16, iconColor)
						} else {
							kit.Icon(c, m.icon, 16).TextColor(iconColor)
						}
						ui.Text(c, m.label).FontSize(14).FontWeight(600).SingleLine()
					})
					if b.Clicked() && running && !selected && s.switching == "" {
						w.setMode(m.value)
					}
				}
			})
		})
	})
}
