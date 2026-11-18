package kit

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// The spinner is AppKit's spinning progress indicator: twelve spokes, the
// leading one solid and those behind it fading, a spoke on every twelfth
// of a turn.
const (
	spinnerSpokes = 12
	spinnerTurn   = 900 * time.Millisecond
)

// Spinner is the indicator of work under way, as a speed test or a
// subscription's update, size DIPs square in Label3. See SpinnerIn.
func Spinner(c *ui.Context, size float32) ui.Element {
	return SpinnerIn(c, size, T(c).Label3)
}

// SpinnerIn is Spinner in color, as OnAccent on an accent button. Its
// spokes turn while it is drawn: it asks the window to paint it again at
// its next step (Painter.After), without building the view, and asks for
// nothing once it is gone or out of view. With less motion asked for it
// shows a still ellipsis instead, which reads as busy rather than as a
// circle stuck; where there is room for words, show "Testing" and the like
// instead (see nodeview.TestingMark).
func SpinnerIn(c *ui.Context, size float32, color ui.Color) ui.Element {
	if c.Preferences().ReduceMotion {
		return Icon(c, SymEllipsis, size).TextColor(color).Role(ui.RoleProgress).Label("进行中")
	}
	box := ui.Box(c).Size(size, size).Shrink(0).Role(ui.RoleProgress).Label("进行中")
	return box.Draw(func(p *ui.Painter, r ui.Rect) {
		drawSpokes(p, r, color)
	})
}

// BusyCover stands in for a control while the work it started is under
// way: an empty button named label over the whole of the element it is
// built in, disabled, so that it takes the pointer's clicks and keeps the
// control's name for assistive technology without fading what shows
// under it, as disabling the control would (MyGo paints a disabled
// element at half opacity). Build the control as a plain element showing
// its busy look (SpinnerIn), the cover last inside it, and return the
// cover for its Clicked and OnClick, which report nothing.
func BusyCover(c *ui.Context, label string) ui.Element {
	return ui.ButtonBase(c).Absolute().Top(0).Right(0).Bottom(0).Left(0).Label(label).Disabled(true)
}

// drawSpokes paints the spinner's spokes in r at the painter's time and
// asks for the next step.
func drawSpokes(p *ui.Painter, r ui.Rect, color ui.Color) {
	turn := spinnerTurn.Milliseconds()
	ms := p.Now().UnixMilli() % turn
	lead := int(ms * spinnerSpokes / turn)
	next := (int64(lead)+1)*turn/spinnerSpokes - ms
	p.After(time.Duration(next) * time.Millisecond)
	side := min(r.W, r.H)
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	in, out, width := side*0.22, side*0.46, max(side*0.09, 1)
	for i := range spinnerSpokes {
		a := float64(i) * 2 * math.Pi / spinnerSpokes
		sin, cos := float32(math.Sin(a)), float32(math.Cos(a))
		// The spokes fade behind the one leading, the faint half alike,
		// so that the leading ones stand out as they turn.
		age := (lead - i + spinnerSpokes) % spinnerSpokes
		alpha := max(1-float32(age)*0.14, 0.14)
		var path ui.Path
		path.MoveTo(cx+sin*in, cy-cos*in).LineTo(cx+sin*out, cy-cos*out)
		p.StrokePath(&path, width, color.Alpha(alpha))
	}
}
