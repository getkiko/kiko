package scripts

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// runState is the manual run of one script of the list: whether its result
// popover is open, the run in flight and what came back.
type runState struct {
	open    bool
	running bool
	// more anchors the popover on the row's More button, as the run was
	// started from its menu.
	more bool
	// refocus returns the keyboard to the anchor as the popover closes.
	refocus bool
	gen     int
	cancel  context.CancelFunc
	res     *api.ScriptRunResult
	err     string
}

// failed reports whether the run did not complete: the request failed or
// the script errored, timed out or hit a limit.
func (s *runState) failed() bool {
	if s.err != "" {
		return true
	}
	if s.res == nil {
		return false
	}
	switch s.res.Action {
	case api.ScriptActionError, api.ScriptActionTimeout, api.ScriptActionLimit:
		return true
	}
	return s.res.Error != ""
}

func (w *Workspace) runOf(id string) *runState {
	if w.runs == nil {
		w.runs = map[string]*runState{}
	}
	s := w.runs[id]
	if s == nil {
		s = &runState{}
		w.runs[id] = s
	}
	return s
}

// stopRuns abandons the runs in flight and closes their popovers, as the
// page stops.
func (w *Workspace) stopRuns() {
	for id, s := range w.runs {
		if s.cancel != nil {
			s.cancel()
		}
		s.gen++
		delete(w.runs, id)
	}
}

// runBlock is why the script cannot be run now; empty when it can. A
// script runs only while it is in the running configuration, which the
// counters tell: they list the scripts the core holds.
func (w *Workspace) runBlock(r scriptRow) string {
	switch {
	case !r.IsRun():
		return "仅通用和定时脚本可运行"
	case w.unsupported():
		return "核心不支持脚本"
	case !MITMEnabled(w.doc.Doc()):
		return "未开启 HTTPS 解密"
	case r.Disabled:
		return "脚本已停用"
	case !w.statsOK:
		return "核心未运行"
	}
	if _, ok := w.stats[r.EffectiveTag()]; !ok {
		return "脚本尚未生效"
	}
	return ""
}

// startRun runs the script by hand and opens its result popover, which
// shows the run until the answer arrives.
func (w *Workspace) startRun(r scriptRow, more bool) {
	s := w.runOf(r.ID)
	if s.running || w.env == nil || w.env.API == nil {
		return
	}
	if reason := w.runBlock(r); reason != "" {
		// A retry from the popover finds the script gone from the core.
		if s.open {
			s.res, s.err = nil, reason
		}
		return
	}
	s.open, s.running, s.more, s.res, s.err, s.refocus = true, true, more, nil, "", false
	s.gen++
	gen := s.gen
	tag, argument := r.EffectiveTag(), r.Argument
	ctx, cancel := context.WithTimeout(context.Background(), evalLimit)
	s.cancel = cancel
	env.Go(w.env, func(context.Context) (api.ScriptRunResult, error) {
		return w.env.API.ScriptRun(ctx, tag, argument)
	}, func(res api.ScriptRunResult, err error) {
		cancel()
		if gen != s.gen {
			return
		}
		s.cancel, s.running = nil, false
		// The counters moved: read them again.
		w.lastPoll = time.Time{}
		if !s.open {
			return
		}
		if err != nil {
			s.err = unavailableText(err)
			return
		}
		s.res = &res
	})
}

// runButton is the "Run now" button of a row.
func (w *Workspace) runButton(c *ui.Context, r scriptRow, o kit.IconButtonOptions) ui.Element {
	title := r.title()
	st := w.runOf(r.ID)
	reason := w.runBlock(r)
	o.Disabled = o.Disabled || reason != "" || st.running
	switch {
	case st.running:
		o.Hint = "运行中…"
	case reason != "":
		o.Hint = reason
	}
	btn := kit.IconButton(c, kit.SymPlay, "立即运行 "+title, o)
	if btn.Clicked() {
		w.startRun(r, false)
	}
	return btn
}

// runPopover shows the result of a manual run beside its anchor while it is
// open.
func (w *Workspace) runPopover(c *ui.Context, anchor ui.Element, r scriptRow) {
	st := w.runOf(r.ID)
	if !st.open {
		return
	}
	k := kit.T(c)
	title := r.title()
	outside := false
	panel := ui.PopoverBase(c, anchor, &st.open, func(panel ui.Element) {
		outside = panel.PressedOutside()
		kit.Popup(c, panel).Margin(6, 0, 0, 0).Width(360).MaxHeight(kit.PopoverRoom(c, anchor, 6)).Padding(14).Gap(8).Label("运行结果 " + title)
		ui.Text(c, title).FontSize(kit.FontBody).FontWeight(600).SingleLine().Ellipsis("…").MinWidth(0)
		switch {
		case st.running:
			ui.Row(c).Gap(8).Children(func() {
				ui.Spinner(c).FontSize(14)
				ui.Text(c, "运行中…").FontSize(kit.FontTable).TextColor(k.Label2)
			})
		case st.err != "":
			ui.Text(c, st.err).FontSize(kit.FontTable).TextColor(k.Danger).Wrap().Selectable()
		case st.res != nil:
			ui.Scroll(c).MaxHeight(280).Shrink(1).MinHeight(0).Children(func() {
				ui.Column(c).Gap(8).Shrink(0).Children(func() { runResult(c, st.res) })
			})
		}
		ui.Row(c).Gap(8).Justify(ui.End).Margin(4, 0, 0).Shrink(0).Children(func() {
			if st.failed() {
				kit.Button(c, "重试", kit.ButtonOptions{Small: true, Variant: kit.Primary}).OnClick(func() { w.startRun(r, st.more) })
			}
			if kit.Button(c, "关闭", kit.ButtonOptions{Small: true}).AutoFocus().Clicked() {
				st.open, st.refocus = false, true
			}
		})
	})
	// Closed by Escape or the button, the keyboard goes back to the button
	// that opened it; a press elsewhere keeps what it focused.
	if (panel.Dismissed() && !outside) || st.refocus {
		anchor.Focus()
		st.refocus = false
	}
	if !st.open {
		// Closed: a finished run is forgotten.
		if !st.running {
			delete(w.runs, r.ID)
		}
	}
}

// runResult is the content of a run's result: what it did, how long it
// took, its error, its console lines and its output.
func runResult(c *ui.Context, res *api.ScriptRunResult) {
	k := kit.T(c)
	resultHead(c, res.Action, "", res.DurationUS, res.Error)
	if len(res.Logs) > 0 {
		logLines(c, res.Logs)
	}
	if res.Output != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Text(c, "输出").FontSize(kit.FontCaption).TextColor(k.Label3).Width(32).Shrink(0).Margin(2, 0, 0)
			ui.Text(c, res.Output).FontSize(kit.FontTable).Font(kit.MonoFont).Selectable().Wrap().Grow(1).MinWidth(0)
			kit.IconButton(c, kit.SymCopy, "复制输出", kit.IconButtonOptions{Size: 24, IconSize: 14}).OnClick(func() { c.WriteClipboard(res.Output) })
		})
	}
}
