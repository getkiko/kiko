package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// Dialog is an edit dialog over the dimmed window, open while *open: a
// title, the form fn builds, scrolling, and the footer's actions on the
// right. Escape and a click outside close it. Width 0 is 460 DIPs.
func Dialog(c *ui.Context, open *bool, title string, width float32, fn func(), footer func()) ui.Element {
	k := T(c)
	if width == 0 {
		width = EditorWidth
	}
	if *open {
		dialogsShown++
	}
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(k.Scrim).Padding(16)
		_, h := c.Size()
		panel.Width(width).MaxWidthPercent(100).MaxHeight(max(h-32, 200)).Radius(RadiusDialog).
			Background(k.Elevated).Border(0.5, k.GlassEdge).Shadow(0, 24, 60, 0, k.Shadow.Alpha(0.3)).Clip().Label(title)
		ui.Row(c).Padding(18, 22, 8).Shrink(0).Children(func() {
			ui.Text(c, title).FontSize(17).FontWeight(600).Grow(1)
			// Closed while building, not by OnClick, which runs after the
			// build: the caller reads *open once Dialog returns, and many
			// pass a variable of the build.
			if IconButton(c, SymX, "关闭", IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
				*open = false
			}
		})
		ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Padding(6, 22, 16).Gap(12).Children(fn)
		if footer != nil {
			ui.Row(c).Padding(12, 22, 18).Gap(8).Justify(ui.End).Shrink(0).Children(footer)
		}
	})
}

// DialogFrame is a large dialog over the dimmed window, open while *open:
// a panel of width × height DIPs (at most the window less 16 DIPs a
// side), its body a column fn fills, which does not scroll: the caller
// lays out its header, its scrolling columns and its footer. Escape and
// a click outside close it. The wheel scrolls only the scroll container
// under the pointer; over the backdrop it scrolls nothing.
func DialogFrame(c *ui.Context, open *bool, label string, width, height float32, fn func()) ui.Element {
	k := T(c)
	if *open {
		dialogsShown++
	}
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		w, h := c.Size()
		box := panel.Bounds()
		backdrop.Background(k.Scrim).Padding(16).HandleInput(func(ev ui.InputEvent) bool {
			// Taken outside the panel, the wheel reaches nothing behind
			// the dialog; inside, it goes on to the panel's own columns.
			return ev.Kind == ui.InputScroll && !box.Contains(ev.X, ev.Y)
		})
		panel.Width(min(width, max(w-32, 280))).Height(min(height, max(h-32, 240))).Radius(RadiusDialog).
			Background(k.Elevated).Border(0.5, k.GlassEdge).Shadow(0, 24, 60, 0, k.Shadow.Alpha(0.3)).Clip().Label(label)
		ui.Column(c).Grow(1).MinHeight(0).FillWidth().Children(fn)
	})
}

// AlertAction is a button of an alert.
type AlertAction struct {
	Label   string
	Variant ButtonVariant
	Run     func()
}

// Alert is a warning window, open while *open: an icon, a short title, a
// message, and actions stacked vertically. Running an action closes it.
func Alert(c *ui.Context, open *bool, icon Symbol, title, message string, actions ...AlertAction) ui.Element {
	k := T(c)
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(k.Scrim).Padding(16)
		panel.Width(AlertWidth).Padding(22, 20, 18).Gap(8).AlignItems(ui.Center).Radius(RadiusDialog).
			Background(k.Elevated).Border(0.5, k.GlassEdge).Shadow(0, 24, 60, 0, k.Shadow.Alpha(0.3)).Label(title)
		if icon != "" {
			Icon(c, icon, 32).TextColor(k.Warning).Margin(0, 0, 4)
		}
		ui.Text(c, title).FontSize(15).FontWeight(600).TextAlign(ui.Center)
		if message != "" {
			ui.Text(c, message).FontSize(FontTable).TextColor(k.Label2).TextAlign(ui.Center).Selectable()
		}
		ui.Column(c).FillWidth().Gap(8).Margin(10, 0, 0).Children(func() {
			for _, a := range actions {
				Button(c, a.Label, ButtonOptions{Variant: a.Variant}).FillWidth().OnClick(func() {
					*open = false
					if a.Run != nil {
						a.Run()
					}
				})
			}
		})
	})
}

// ConfirmState is the state of a ConfirmPopover: whether it is open, the
// action running, and the last failure.
type ConfirmState struct {
	Open  bool
	Busy  bool
	Error string
}

// ConfirmOptions describe a confirmation.
type ConfirmOptions struct {
	Title, Message string
	// Confirm is the button's label, "Confirm" in Chinese by default.
	Confirm string
	Danger  bool
	// Body, when set, builds controls under the message, as a choice of
	// what the action covers.
	Body func()
	// Run performs the action and calls done with its outcome, on the UI
	// thread: nil closes the popover, an error shows in it with a retry.
	Run func(done func(error))
}

// ConfirmPopover asks to confirm a simple action (delete, clear, restore
// defaults, sign out) beside the button that triggered it, anchor. Open it
// by setting s.Open. While the action runs it cannot run again.
func ConfirmPopover(c *ui.Context, anchor ui.Element, s *ConfirmState, o ConfirmOptions) ui.Element {
	if !s.Open {
		s.Error = ""
	}
	return ui.PopoverBase(c, anchor, &s.Open, func(panel ui.Element) {
		Popup(c, panel).Width(280).Padding(14).Label(o.Title)
		AttachPopover(c, panel, anchor, ui.AnchorBottomStart, ui.AnchorTopStart, 6, 0)
		PopoverBody(c, 8, func() { confirmBody(c, s, o) })
	})
}

// confirmBody is the content of a ConfirmPopover.
func confirmBody(c *ui.Context, s *ConfirmState, o ConfirmOptions) {
	k := T(c)
	ui.Text(c, o.Title).FontSize(FontBody).FontWeight(600)
	if o.Message != "" {
		ui.Text(c, o.Message).FontSize(FontTable).TextColor(k.Label2)
	}
	if o.Body != nil {
		o.Body()
	}
	if s.Error != "" {
		ui.Text(c, s.Error).FontSize(FontTable).TextColor(k.Danger).Selectable()
	}
	ui.Row(c).Gap(8).Justify(ui.End).Margin(4, 0, 0).Children(func() {
		Button(c, "取消", ButtonOptions{Small: true}).OnClick(func() { s.Open = false })
		label := o.Confirm
		if label == "" {
			label = "确认"
		}
		if s.Error != "" {
			label = "重试"
		}
		variant := Primary
		if o.Danger {
			variant = Danger
		}
		Button(c, label, ButtonOptions{Small: true, Variant: variant, Disabled: s.Busy}).AutoFocus().OnClick(func() {
			if s.Busy || o.Run == nil {
				return
			}
			s.Busy, s.Error = true, ""
			o.Run(func(err error) {
				s.Busy = false
				if err != nil {
					s.Error = err.Error()
					s.Open = true
					return
				}
				s.Open = false
			})
		})
	})
}

// EditorSidebar is the floating editor over the right of a list: a title
// with a header action (an enable switch), the form fn builds, scrolling
// on its own, and fixed footer actions. It keeps the list's width under
// it; a press outside it closes it, unless an overlay of its own is open.
func EditorSidebar(c *ui.Context, open *bool, title string, header func(), fn func(), footer func()) ui.Element {
	return EditorSidebarKeeping(c, open, false, title, header, fn, footer)
}

// EditorSidebarKeeping is EditorSidebar whose press outside keeps it open
// while keep is true, as a press on another row of the list it edits,
// whose click switches it. Its close button and Escape close it always.
func EditorSidebarKeeping(c *ui.Context, open *bool, keep bool, title string, header func(), fn func(), footer func()) ui.Element {
	return EditorSidebarGuarded(c, open, keep, nil, title, header, fn, footer)
}

// EditorGuard protects an editor sidebar's unsaved changes from the ways
// out the user did not choose as a discard: a press outside the sidebar
// and the caller's switches to another draft (Ask). While Dirty, they
// show "Discard changes?" in the sidebar, over its footer actions, with
// "Keep editing", which keeps every input, and "Discard changes", which closes the
// sidebar or runs the switch that asked. Without changes they act at
// once. The close button, Escape and the caller's Cancel (Discard) are a
// discard already: they close at once, asking or not.
type EditorGuard struct {
	// Dirty is whether the draft differs from what was opened, set by the
	// caller each frame.
	Dirty bool
	// asking is whether the question shows; then is the switch waiting on
	// the answer, nil closing the sidebar.
	asking bool
	then   func()
}

// Ask leaves the draft: without changes it runs then, or closes *open when
// then is nil; with changes it shows the question and runs the same after
// a discard. Asked again while the question shows, the latest way out
// waits instead. Reports whether it left at once.
func (g *EditorGuard) Ask(open *bool, then func()) bool {
	if g != nil && g.Dirty {
		g.then = then
		g.asking = true
		return false
	}
	if then != nil {
		then()
	} else if open != nil {
		*open = false
	}
	return true
}

// Discard closes the sidebar at once, dropping the draft, as its close
// button and Escape do: for the caller's Cancel.
func (g *EditorGuard) Discard(open *bool) {
	if g != nil {
		g.asking, g.then, g.Dirty = false, nil, false
	}
	*open = false
}

// Keep answers the question with "Keep editing": it goes, and the draft stays.
func (g *EditorGuard) Keep() {
	if g != nil {
		g.asking, g.then = false, nil
	}
}

// Pending is whether the question shows.
func (g *EditorGuard) Pending() bool { return g != nil && g.asking }

// discard answers the question with "Discard changes": it runs the switch waiting,
// or closes the sidebar.
func (g *EditorGuard) discard(open *bool) {
	then := g.then
	g.asking, g.then, g.Dirty = false, nil, false
	if then != nil {
		then()
	} else {
		*open = false
	}
}

// EditorSidebarGuarded is EditorSidebarKeeping that asks before a press
// outside or a switch drops the unsaved changes g reports, in a bar over
// the footer actions. A nil g never asks.
func EditorSidebarGuarded(c *ui.Context, open *bool, keep bool, g *EditorGuard, title string, header func(), fn func(), footer func()) ui.Element {
	k := T(c)
	if !*open {
		if g != nil {
			g.asking, g.then = false, nil
		}
		return ui.Element{}
	}
	var panel ui.Element
	shown := dialogsShown
	ui.Overlay(c, func() {
		_, h := c.Size()
		panel = ui.Column(c).Absolute().Top(12).Right(12).Bottom(12).Width(EditorWidth).MaxHeight(h-24).
			Radius(RadiusGroup).Background(k.Elevated).Border(0.5, k.GlassEdge).
			Shadow(0, 18, 48, 0, k.Shadow.Alpha(0.25)).Clip().Transition(editorMotion).Label(title).Children(func() {
			ui.Row(c).Height(52).PaddingX(16).Gap(10).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
				ui.Text(c, title).FontSize(15).FontWeight(600).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				if header != nil {
					header()
				}
				if IconButton(c, SymX, "关闭编辑器", IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
					g.Discard(open)
				}
			})
			ui.Scroll(c).Grow(1).MinHeight(0).Padding(14, 16, 20).Gap(12).Children(fn)
			asking := g.Pending()
			if footer != nil || asking {
				// The question shows where the eye is after leaving the
				// draft: over the actions that save or drop it.
				ui.Column(c).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
					if asking {
						discardBar(c, g, open)
					}
					if footer != nil {
						ui.Row(c.Key("editor-footer")).Padding(12, 16).Gap(8).Justify(ui.End).Children(footer)
					}
				})
			}
		})
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			// Escape answers the question on top, keeping the draft; without
			// one it closes, as the close button does.
			if g.Pending() {
				g.Keep()
			} else {
				g.Discard(open)
			}
		}
	})
	// A dialog opened from the sidebar stacks over it: presses in the
	// dialog keep the sidebar open. A press outside while asking keeps the
	// question, now for closing the sidebar, so its click cannot ask again.
	if dialogsShown == shown && panel.PressedOutside() && !keep {
		g.Ask(open, nil)
	}
	return panel
}

// discardBar is the question "Discard changes?" of a guarded editor sidebar, over
// its footer actions: "Keep editing" has the focus, and "Discard changes" drops the draft.
func discardBar(c *ui.Context, g *EditorGuard, open *bool) {
	k := T(c)
	ui.Row(c.Key("editor-discard")).Margin(12, 16, 0).Padding(10, 10, 10, 14).Gap(8).Radius(12).
		Background(k.WarningWeak).Border(0.5, k.Warning.Alpha(0.45)).
		Transition(discardMotion).Label("放弃更改？").Children(func() {
		Icon(c, SymTriangleAlert, 16).TextColor(k.Warning).Shrink(0)
		ui.Text(c, "放弃更改？").FontSize(FontBody).FontWeight(600).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		Button(c, "继续编辑", ButtonOptions{}).AutoFocus().OnClick(g.Keep)
		Button(c, "放弃更改", ButtonOptions{Variant: Danger}).OnClick(func() { g.discard(open) })
	})
}

// editorMotion slides an editor sidebar in from the right as it fades in,
// and out the same way.
var editorMotion = ui.ElementTransition{Duration: 220 * time.Millisecond, Position: true,
	Enter: &ui.Motion{X: 48}, Exit: &ui.Motion{X: 48}}

// discardMotion brings the question of an editor sidebar up from its
// footer, slowing as it arrives, and takes it back down as it goes.
var discardMotion = ui.ElementTransition{Duration: 200 * time.Millisecond, Position: true,
	Enter: &ui.Motion{Y: 14}, Exit: &ui.Motion{Y: 14}}

// HoverMotion fades the background and border colors a control changes
// on hover and press, which every hovered element takes:
//
//	b.Transition(kit.HoverMotion)
var HoverMotion = ui.ElementTransition{Duration: 90 * time.Millisecond, Colors: true}

// dialogsShown counts the dialogs built, so an editor sidebar knows a
// dialog of its own is over it. Views build on the UI thread only; the
// windows of several consoles share it safely, every window building its
// frames on the main thread one after another.
var dialogsShown int

// FormRow is a compact form row: a label on the left, the control on the
// right, and an optional hint or error under the control.
func FormRow(c *ui.Context, label, hint string, control func()) ui.Element {
	k := T(c)
	return ui.Row(c).Gap(12).AlignItems(ui.Start).MinHeight(ControlHeight).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(FontBody).TextColor(k.Label2).Width(104).Shrink(0).Margin(7, 0, 0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			control()
			if hint != "" {
				ui.Text(c, hint).FontSize(FontCaption).TextColor(k.Label3).Wrap()
			}
		})
	})
}

// FormError shows a field's error in the danger color.
func FormError(c *ui.Context, message string) {
	if message != "" {
		ui.Text(c, message).FontSize(FontCaption).TextColor(T(c).Danger).Wrap()
	}
}
