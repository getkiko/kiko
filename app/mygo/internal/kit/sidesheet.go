package kit

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// CapsuleTag is a small capsule beside a name: its text in fg on bg,
// outlined in border unless border is the zero color.
func CapsuleTag(c *ui.Context, text string, fg, bg, border ui.Color) {
	tag := ui.Row(c).Height(18).PaddingX(6).Radius(5).AlignItems(ui.Center).Shrink(0).Background(bg)
	if border != (ui.Color{}) {
		tag.Border(1, border)
	}
	tag.Children(func() {
		ui.Text(c, text).FontSize(10).FontWeight(600).TextColor(fg).FontFeatures("tnum").SingleLine()
	})
}

// The side sheet slides from beyond the window's right edge, past its
// margin and shadow: in fast and settling (ease out), out speeding up
// (ease in), moving only, without fading.
const (
	sheetSlide = EditorWidth + 12 + 60
	sheetIn    = 280 * time.Millisecond
	sheetOut   = 220 * time.Millisecond
)

// SideSheetState is a side sheet's state: Open while it shows, and
// whether it was built in the last frame, sliding out after it closes.
type SideSheetState struct {
	Open bool
	// Refocus is set as Escape or the close button closes the sheet: the
	// view moves the focus back to what opened it, and clears it. A press
	// outside leaves the focus where it went.
	Refocus bool
	shown   bool
}

// Shown reports whether the sheet is open or still sliding out: whether
// SideSheet builds it this frame.
func (s *SideSheetState) Shown() bool { return s.Open || s.shown }

// SideSheet is a panel over the right of the window, sliding in from the
// right edge and out the same way: a title row with the header's actions
// (nil for none) before its close button, and the content fn builds
// below it, which scrolls on its own. A press outside it, unless in a
// dialog opened from it, and Escape close it. It stays built while it
// slides out; with less motion asked for, it shows and goes at once.
func SideSheet(c *ui.Context, s *SideSheetState, title string, header, fn func()) ui.Element {
	if !s.Open && !s.shown {
		return ui.Element{}
	}
	k := T(c)
	var panel ui.Element
	shown := dialogsShown
	ui.Overlay(c, func() {
		_, h := c.Size()
		panel = ui.Column(c.Key("side-sheet")).Absolute().Top(12).Right(12).Bottom(12).Width(EditorWidth).
			MaxHeight(h-24).Radius(RadiusGroup).Background(k.Elevated).Border(0.5, k.GlassEdge).
			Shadow(0, 18, 48, 0, k.Shadow.Alpha(0.25)).Clip().Label(title)
		if !s.shown {
			panel.Jump("show", 0)
		}
		target := float32(0)
		if s.Open {
			target = 1
		}
		var p float32
		switch {
		case c.Preferences().ReduceMotion:
			p = panel.Jump("show", target)
		case s.Open:
			p = panel.AnimateWith("show", target, sheetIn, ui.EaseOut)
		default:
			p = panel.AnimateWith("show", target, sheetOut, ui.EaseIn)
		}
		s.shown = s.Open || p > 0.001
		panel.Translate((1-p)*sheetSlide, 0)
		panel.Children(func() {
			ui.Row(c).Height(56).Padding(0, 12, 0, 18).Gap(10).Shrink(0).Children(func() {
				ui.Text(c, title).FontSize(16).FontWeight(600).TextColor(k.Label).SingleLine().Ellipsis("…").
					Grow(1).MinWidth(0)
				if header != nil {
					header()
				}
				if IconButton(c.Key("sheet-close"), SymX, "关闭", IconButtonOptions{Size: 32, IconSize: 18}).Clicked() {
					s.Open, s.Refocus = false, true
				}
			})
			if fn != nil {
				fn()
			}
		})
		if s.Open && panel.OverlayShortcut(0, ui.KeyEscape) {
			s.Open, s.Refocus = false, true
		}
	})
	// A dialog opened from the sheet stacks over it: presses in the
	// dialog keep the sheet open.
	if s.Open && dialogsShown == shown && panel.PressedOutside() {
		s.Open = false
	}
	return panel
}
