package overview

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// applyPages are the workspaces an unapplied change can belong to, in the
// sidebar's order.
var applyPages = []struct{ page, label string }{
	{env.PageRoute, "路由"},
	{env.PageSubscriptions, "组件"},
	{env.PageSettings, "设置"},
}

// applyPanel is the state of the unapplied changes card.
type applyPanel struct {
	// restart asks to restart when the core cannot take the changes live.
	restart kit.ConfirmState
	reason  string

	// last is the changes the card listed last, which it keeps listing
	// while it leaves; shown tells the card is in the layout, entering,
	// resting or leaving; height is its natural height, measured.
	last   store.Unapplied
	shown  bool
	height float32

	// changes, when set, stands in for the store's unapplied changes
	// (tests).
	changes func() store.Unapplied
}

// Entering decelerates and leaving accelerates, so the card and the page
// below it move at one pace.
const (
	applyIn  = 240 * time.Millisecond
	applyOut = 190 * time.Millisecond
	// applyGuess is the card's height before it was measured.
	applyGuess = 84
)

// applyProgress is how far the card has come in, 0 to 1: it moves at the
// time of its entry or exit, at once with less motion asked for.
func applyProgress(slot ui.Element, want, shown, still bool) float32 {
	target := float32(0)
	if want {
		target = 1
	}
	switch {
	case still:
		return slot.Jump("show", target)
	case !shown:
		slot.Jump("show", 0)
	}
	if want {
		return slot.AnimateWith("show", target, applyIn, ui.EaseOut)
	}
	return slot.AnimateWith("show", target, applyOut, ui.EaseIn)
}

// applyCard is the console's one apply entry: while the document differs
// from the applied configuration it lists the affected pages and applies
// live, by restarting the core's services, or, while the core is stopped,
// by starting it.
func (w *Workspace) applyCard(c *ui.Context) {
	a := &w.apply
	changes := w.store.Unapplied()
	if a.changes != nil {
		changes = a.changes()
	}
	want := changes.Count() > 0
	if want {
		a.last = changes
	}
	if !want && !a.shown {
		return
	}
	// The slot is as high as the part of the card that has come in, and
	// clips it: the page below follows the card's edge, and the card
	// slides in from above its slot or out to it.
	slot := ui.Column(c.Key("apply-slot")).Shrink(0)
	p := applyProgress(slot, want, a.shown, c.Preferences().ReduceMotion)
	a.shown = want || p > 0.001
	if !a.shown {
		return
	}
	height := a.height
	if height <= 0 {
		height = applyGuess
	}
	if p < 0.999 || !want {
		slot.Clip().Height(height * p)
	}
	slot.Children(func() {
		// The card starts at the page's top; the room under it parts it
		// from the dashboard and comes and goes with it.
		body := ui.Column(c.Key("apply-body")).Padding(0, 0, 16).Shrink(0)
		if b := body.Bounds(); b.H > 0 {
			a.height = b.H
		}
		if p < 0.999 || !want {
			body.Translate(0, -(1-p)*height)
		}
		body.Children(func() { w.applyBody(c, a.last, want) })
	})
}

// applyBody is the card listing changes; it takes no presses while it
// leaves, its changes applied.
func (w *Workspace) applyBody(c *ui.Context, changes store.Unapplied, active bool) {
	k := kit.T(c)
	server := w.env.Server
	running := server.HasRuntime && runtimePowerState(server.Runtime.State, server.Runtime.DesiredRunning).running
	busy := w.store.Applying() || w.power.busy || w.power.reloading || server.Runtime.State == "reloading" || !active
	a := &w.apply
	ui.Row(c).Padding(12, 16).Gap(12).Radius(18).Border(1, k.HairlineSoft).Background(k.Surface).Shrink(0).
		AlignItems(ui.Center).Label("未应用的更改").Children(func() {
		ui.Box(c).Size(8, 8).Radius(4).Background(k.Accent).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, fmt.Sprintf("有 %d 处未应用的更改", changes.Count())).FontSize(kit.FontBody).FontWeight(600)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, strings.Join(changes.Parts, "、")).FontSize(kit.FontTable).TextColor(k.Label2)
				for _, p := range applyPages {
					if !changes.Pages[p.page] {
						continue
					}
					page := p.page
					link := ui.ButtonBase(c.Key("apply-page-" + page)).Height(22).PaddingX(9).Radius(11).
						Background(k.Accent.Alpha(0.1)).TextColor(k.AccentText).Label("前往" + p.label).Disabled(!active)
					link.Children(func() { ui.Text(c, p.label).FontSize(kit.FontCaption).FontWeight(600) })
					if link.Clicked() {
						w.env.Navigate(page, nil)
					}
				}
			})
		})
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			if !running {
				kit.Button(c, "应用并启动", kit.ButtonOptions{Variant: kit.Primary, Disabled: busy || !server.HasRuntime}).OnClick(w.toggle)
				return
			}
			kit.Button(c, "重启并应用", kit.ButtonOptions{Disabled: busy, Hint: "重启核心服务以应用全部更改"}).OnClick(w.reload)
			live := kit.Button(c, "不停机应用", kit.ButtonOptions{Variant: kit.Primary, Disabled: busy, Hint: "保持现有连接，直接更新路由、出站与 DNS"})
			if live.Clicked() {
				w.applyLive()
			}
			kit.ConfirmPopover(c, live, &a.restart, kit.ConfirmOptions{
				Title: "需要重启核心", Message: a.reason, Confirm: "重启并应用",
				Run: func(done func(error)) { w.reloadThen(done) },
			})
		})
	})
}

// applyLive applies the document without restarting the core; changes it
// cannot take live ask to restart instead.
func (w *Workspace) applyLive() {
	doc, library := w.store.Doc(), w.store.NetworkConfigs()
	if problem := blockingProblem(doc, library); problem != "" {
		w.env.Toast(problem)
		return
	}
	w.store.ApplyLive(func(err error) {
		var restart *api.RestartRequiredError
		switch {
		case errors.As(err, &restart):
			w.apply.reason = restart.Reason
			w.apply.restart.Open = true
		case err != nil:
			w.env.ToastError(err)
		default:
			w.env.Toast(liveAppliedMessage(w.store.InterruptOutcome()))
		}
	})
}

// liveAppliedMessage is the toast of a live apply, naming the connections it
// closed for the rules marked for interruption.
func liveAppliedMessage(outcome store.InterruptResult) string {
	switch {
	case outcome.Error != "":
		return "已不停机应用，中断连接失败：" + outcome.Error
	case outcome.Closed > 0:
		return fmt.Sprintf("已不停机应用，已中断 %d 条连接", outcome.Closed)
	}
	return "已不停机应用"
}
