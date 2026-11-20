package overview

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// applyPanel is the state of the sidebar's apply entry.
type applyPanel struct {
	// restart asks to restart when the core cannot take the changes live.
	restart kit.ConfirmState
	reason  string

	// changes, when set, stands in for the store's unapplied changes
	// (tests).
	changes func() store.Unapplied
}

// unapplied are the changes not applied yet.
func (w *Workspace) unapplied() store.Unapplied {
	if w.apply.changes != nil {
		return w.apply.changes()
	}
	return w.store.Unapplied()
}

// SidebarApply is the console's one apply entry, at the foot of the
// sidebar on every page while the document differs from the applied
// configuration: how many parts changed, a hint naming them, and Apply,
// live while the core runs (Restart and apply below it), starting the core
// while it is stopped. Folded, it is one Apply button carrying the count.
// The pages the changes belong to carry blue dots in the navigation.
func (w *Workspace) SidebarApply(c *ui.Context, folded bool) {
	changes := w.unapplied()
	if changes.Count() == 0 {
		return
	}
	k := kit.T(c)
	server := w.env.Server
	running := server.HasRuntime && runtimePowerState(server.Runtime.State, server.Runtime.DesiredRunning).running
	busy := w.store.Applying() || w.power.busy || w.power.reloading || server.Runtime.State == "reloading"
	parts := strings.Join(changes.Parts, "、")
	count := fmt.Sprintf("%d 处更改", changes.Count())
	apply := func() {
		if running {
			w.applyLive()
		} else {
			w.toggle()
		}
	}
	label := "应用"
	if !running {
		label = "应用并启动"
	}
	var anchor ui.Element
	if folded {
		row := ui.Row(c).Justify(ui.Center).Shrink(0)
		row.Children(func() { anchor = w.foldedApply(c, label, count, parts, busy || !server.HasRuntime, busy, apply) })
	} else {
		ui.Column(c.Key("sidebar-apply")).Padding(10).Gap(8).Radius(12).Background(k.Accent.Alpha(0.08)).
			Shrink(0).Label("未应用的更改").Children(func() {
			head := ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0)
			head.Children(func() {
				ui.Box(c).Size(7, 7).Radius(3.5).Background(k.Accent).Shrink(0)
				ui.Text(c, count).FontSize(kit.FontTable).FontWeight(600).TextColor(k.Label).FontFeatures("tnum").
					SingleLine().Ellipsis("…").MinWidth(0)
			})
			kit.Hint(c, head, parts)
			anchor = kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Small: true, Disabled: busy || !server.HasRuntime}).FillWidth()
			if anchor.Clicked() {
				apply()
			}
			if running {
				restart := ui.ButtonBase(c).Height(20).Center().Radius(6).Label("重启并应用").Disabled(busy).
					Transition(kit.HoverMotion)
				restart.Children(func() {
					ui.Text(c, "重启并应用").FontSize(kit.FontCaption).TextColor(k.Label2).SingleLine()
				})
				kit.Hint(c, restart, "重启核心服务以应用全部更改")
				if restart.Clicked() {
					w.reload()
				}
			}
		})
	}
	kit.ConfirmPopover(c, anchor, &w.apply.restart, kit.ConfirmOptions{
		Title: "需要重启核心", Message: w.apply.reason, Confirm: "重启并应用",
		Run: func(done func(error)) { w.reloadThen(done) },
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
		return "已应用，中断连接失败：" + outcome.Error
	case outcome.Closed > 0:
		return fmt.Sprintf("已应用，已中断 %d 条连接", outcome.Closed)
	}
	return "已应用"
}

// foldedApply is the folded sidebar's apply entry: an Apply button
// carrying the count of changes, its hint naming them.
func (w *Workspace) foldedApply(c *ui.Context, label, count, parts string, disabled, busy bool, apply func()) ui.Element {
	k := kit.T(c)
	changes := w.unapplied()
	b := ui.ButtonBase(c.Key("sidebar-apply")).Size(40, 40).Radius(10).Center().Background(k.Accent).
		TextColor(k.OnAccent).Label(label + " " + count).Disabled(disabled).Transition(kit.HoverMotion)
	if b.Pressed() {
		b.Opacity(0.75)
	}
	b.Children(func() {
		if busy {
			kit.SpinnerIn(c, 16, k.OnAccent)
		} else {
			kit.Icon(c, kit.SymArrowUp, 18)
		}
		badge := ui.Box(c).Absolute().Top(-5).Right(-5).MinWidth(16).Height(16).PaddingX(4).Radius(8).
			Background(k.Label).Border(1.5, k.Sidebar).Center()
		badge.Children(func() {
			ui.Text(c, fmt.Sprint(changes.Count())).FontSize(10).FontWeight(700).TextColor(k.Surface).
				FontFeatures("tnum").SingleLine()
		})
	})
	kit.Hint(c, b, label+"："+parts)
	if b.Clicked() {
		apply()
	}
	return b
}
