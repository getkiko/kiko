package overview

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// blockingMessage is why a configuration with broken references does not
// start.
const blockingMessage = "配置存在需要修正的引用，请先处理侧边栏红点标记"

// blockingProblem is why the document cannot be applied, "" when it can:
// more than one Tailscale endpoint, or a broken reference.
func blockingProblem(doc *model.Doc, library []*model.NetworkConfig) string {
	if problem := model.TailscaleLimitError(doc); problem != "" {
		return problem
	}
	if model.HasBlockingReferenceWarnings(doc, library) {
		return blockingMessage
	}
	return ""
}

// powerControl is the power button's state (ServerPowerButton).
type powerControl struct {
	busy, reloading bool
	// reloadFocus keeps the reload button unfolded while it has the
	// focus.
	reloadFocus bool
}

// tailscaleUDPDirectDisabled reports a Tailscale endpoint that turns off
// direct UDP, a desktop runtime option sent with the configuration.
func tailscaleUDPDirectDisabled(doc *model.Doc) bool {
	for _, e := range doc.Endpoints {
		if e.Enabled && e.Type == "tailscale" && e.Fields["disable_udp_direct"] == true {
			return true
		}
	}
	return false
}

// emitForApply emits the document with the host's current addresses, and
// the runtime options to apply it with.
func (w *Workspace) emitForApply(ctx context.Context, doc *model.Doc, library []*model.NetworkConfig) ([]byte, api.ApplyOptions, error) {
	addresses, err := w.env.API.LocalAddresses(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, api.ApplyOptions{}, err
		}
		addresses = w.store.LocalAddresses
	} else {
		w.env.Post(func() { w.store.UseLocalAddresses(addresses) })
	}
	result := emit.Emit(doc, emit.Options{NetworkConfigs: library, LocalAddresses: addresses})
	if len(result.Errors) > 0 {
		return nil, api.ApplyOptions{}, errors.New(strings.Join(result.Errors, "；"))
	}
	return result.JSON, api.ApplyOptions{DisableTailscaleUDPDirect: tailscaleUDPDirectDisabled(doc), RuleMap: result.RuleMap, DNSRuleMap: result.DNSRuleMap}, nil
}

// toggle stops a running core, or applies the configuration and starts
// it.
func (w *Workspace) toggle() {
	p := &w.power
	if p.busy {
		return
	}
	server := w.env.Server
	running := runtimePowerState(server.Runtime.State, server.Runtime.DesiredRunning).running
	limit := toggleDeadline(running)
	doc, library := w.store.Doc(), w.store.NetworkConfigs()
	if problem := blockingProblem(doc, library); !running && problem != "" {
		w.env.Toast(problem)
		return
	}
	p.busy = true
	gotRuntime := false
	var runtime api.Runtime
	w.env.Run(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, limit.timeout)
		defer cancel()
		var err error
		if running {
			runtime, err = w.env.API.Control(ctx, "stop")
			gotRuntime = err == nil
			return err
		}
		config, options, err := w.emitForApply(ctx, doc, library)
		if err != nil {
			return err
		}
		if _, err := w.env.API.Apply(ctx, config, options); err != nil {
			return err
		}
		runtime, err = w.env.API.Control(ctx, "start")
		gotRuntime = err == nil
		return err
	}, func(err error) {
		p.busy = false
		if gotRuntime {
			w.setRuntime(runtime)
		}
		if !running {
			w.store.RefreshApplied()
		}
		if err == nil {
			w.store.ClearInterrupts()
			if !running {
				w.store.NoteApplied(doc)
			}
		}
		if err != nil {
			w.env.Toast(messageFor(err, limit))
		}
	})
}

// reload applies the configuration to the running core, waiting while
// another apply holds the server.
func (w *Workspace) reload() { w.reloadThen(nil) }

// reloadThen reloads and hands the outcome to done, which then reports it,
// instead of a toast for failures.
func (w *Workspace) reloadThen(done func(error)) {
	p := &w.power
	if p.busy || p.reloading {
		if done != nil {
			done(errors.New("配置正在应用，请稍后重试"))
		}
		return
	}
	p.reloading = true
	limit := reloadDeadline()
	var runtime api.Runtime
	var appliedDoc *model.Doc
	w.env.Run(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, limit.timeout)
		defer cancel()
		type snapshot struct {
			doc     *model.Doc
			library []*model.NetworkConfig
		}
		applied, err := retryBusyApply(ctx, func() (api.ApplyResult, error) {
			// Each try applies the document as it is then.
			current := make(chan snapshot, 1)
			w.env.Post(func() { current <- snapshot{w.store.Doc(), w.store.NetworkConfigs()} })
			var s snapshot
			select {
			case s = <-current:
			case <-ctx.Done():
				return api.ApplyResult{}, ctx.Err()
			}
			if problem := blockingProblem(s.doc, s.library); problem != "" {
				return api.ApplyResult{}, errors.New(problem)
			}
			appliedDoc = s.doc
			config, options, err := w.emitForApply(ctx, s.doc, s.library)
			if err != nil {
				return api.ApplyResult{}, err
			}
			return w.env.API.Apply(ctx, config, options)
		}, nil)
		if err != nil {
			return err
		}
		if applied.Runtime != nil {
			runtime = *applied.Runtime
			return nil
		}
		runtime, err = w.env.API.Runtime(ctx)
		return err
	}, func(err error) {
		p.reloading = false
		if err == nil {
			w.setRuntime(runtime)
			// The services restarted, which closed the connections.
			w.store.ClearInterrupts()
			if appliedDoc != nil {
				w.store.NoteApplied(appliedDoc)
			}
		}
		w.store.RefreshApplied()
		switch {
		case done != nil:
			if err != nil {
				err = errors.New(messageFor(err, limit))
			}
			done(err)
		case err != nil:
			w.env.Toast(messageFor(err, limit))
		}
	})
}

// setRuntime shows a state a request answered, newer than any query.
func (w *Workspace) setRuntime(runtime api.Runtime) {
	w.env.Server.SetRuntime(runtime)
	w.sync.update(w.now())
}

// powerSize is the side of the state card's round power button.
const powerSize = 72

// The power control's reload button: its side, and the room it takes
// unfolded, with the gap to the power button.
const (
	reloadSize = 52
	reloadSlot = reloadSize + 10
)

// powerMotion is how long the power control unfolds and folds.
const powerMotion = 180 * time.Millisecond

// powerControl is the state card's power button. While the core runs, a
// hover on it (or a reload in flight) unfolds the control to its left: a
// soft capsule grows from under the button and a reload button slides
// and scales in, its arrow turning a half circle on hover.
func (w *Workspace) powerControl(c *ui.Context, runtime api.Runtime, known bool) {
	k := kit.T(c)
	p := &w.power
	power := runtimePowerState(runtime.State, runtime.DesiredRunning)
	canReload := known && power.running
	group := ui.Row(c.Key("power-control")).Height(powerSize).Radius(powerSize / 2).AlignItems(ui.Center).
		Justify(ui.End).Shrink(0).Clip()
	reloading := p.reloading || runtime.State == "reloading"
	target := float32(0)
	if canReload && (group.Hovered() || reloading || p.reloadFocus) {
		target = 1
	}
	t := group.Animate("unfold", target, powerMotion)
	if t > 0.001 {
		group.Background(k.Fill.Alpha(t))
	}
	group.Children(func() {
		p.reloadFocus = false
		if t > 0.001 {
			ui.Row(c.Key("reload-slot")).Width(reloadSlot * t).Height(powerSize).Shrink(0).AlignItems(ui.Center).
				Clip().Children(func() {
				disabled := p.busy || reloading || !canReload
				b := ui.ButtonBase(c.Key("reload")).Size(reloadSize, reloadSize).Radius(reloadSize/2).Center().
					Shrink(0).Margin(0, 0, 0, (powerSize-reloadSize)/2).Label("重载配置").Disabled(disabled).
					TextColor(k.Label2).Transition(kit.HoverMotion)
				kit.Hint(c, b, "重载配置")
				switch {
				case disabled:
				case b.Pressed():
					b.Background(k.Fill2.Mix(k.Label4, 0.3))
				case b.Hovered():
					b.Background(k.Fill2).TextColor(k.Label)
				}
				if b.FocusVisible() {
					b.Shadow(0, 0, 0, 3, k.Accent.Alpha(0.35))
				}
				if b.Focused() {
					p.reloadFocus = true
				}
				scale := 0.6 + 0.4*t
				b.Scale(scale, scale).Opacity(t).Translate((1-t)*reloadSlot*0.5, 0)
				b.Children(func() {
					if reloading {
						ui.Spinner(c).FontSize(20)
						return
					}
					turn := float32(0)
					if b.Hovered() && !disabled {
						turn = 180
					}
					icon := kit.Icon(c, kit.SymRotateCw, 22)
					icon.Rotate(icon.Animate("turn", turn, 260*time.Millisecond))
				})
				if b.Clicked() && !disabled {
					w.reload()
				}
			})
		}
		w.powerButton(c, runtime, known)
	})
}

// powerButton is the state card's round button, tinted by what it does:
// a green play while the core is stopped, a red stop while it runs, a
// spinner while it starts, stops or the state is unknown, when it takes
// no presses.
func (w *Workspace) powerButton(c *ui.Context, runtime api.Runtime, known bool) {
	k := kit.T(c)
	power := runtimePowerState(runtime.State, runtime.DesiredRunning)
	label := power.label
	if !known {
		label = "正在读取状态"
	}
	waiting := w.power.busy || power.stopping || !known
	b := ui.ButtonBase(c.Key("power")).Size(powerSize, powerSize).Radius(powerSize / 2).Center().Shrink(0).
		Label(label).Disabled(waiting).Transition(kit.HoverMotion)
	hint := label
	if !waiting {
		hint = label + "核心"
	}
	kit.Hint(c, b, hint)
	tint, fill := k.Success, k.SuccessWeak
	if power.running {
		tint, fill = k.Danger, k.DangerWeak
	}
	if waiting {
		tint, fill = k.Label3, k.Fill
	}
	grow := float32(1)
	switch {
	case waiting:
	case b.Pressed():
		fill = fill.Mix(tint, 0.3)
		grow = 0.94
	case b.Hovered():
		fill = fill.Mix(tint, 0.16)
		grow = 1.04
	}
	scale := b.Animate("grow", grow, 110*time.Millisecond)
	b.Scale(scale, scale)
	b.Background(fill).TextColor(tint)
	if b.FocusVisible() {
		b.Shadow(0, 0, 0, 3, k.Accent.Alpha(0.35))
	}
	b.Children(func() {
		switch {
		case waiting:
			ui.Spinner(c).FontSize(26)
		case power.running:
			kit.Icon(c, kit.SymSquareFill, 24)
		default:
			kit.Icon(c, kit.SymPlayFill, 30).Margin(0, 0, 0, 3)
		}
	})
	if b.Clicked() {
		w.toggle()
	}
}

// now is the time runtime states are recorded at.
func (w *Workspace) now() time.Time { return time.Now() }
