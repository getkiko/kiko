package kit

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// tailscaleDots marks the solid dots of Tailscale's logo, a three by
// three grid whose middle row and bottom middle dot form a T; the others
// are faint.
var tailscaleDots = [9]bool{
	false, false, false,
	true, true, true,
	false, true, false,
}

// MarkMotion is how the logo's dots move.
type MarkMotion int

const (
	// MarkStill draws the dots at rest.
	MarkStill MarkMotion = iota
	// MarkBreathe brightens the solid dots one after another, slowly and
	// gently: a device that is online.
	MarkBreathe
	// MarkFlow runs a faster wave through all nine dots: a device that is
	// connecting.
	MarkFlow
)

const (
	// markFaint is the faint dots' alpha at rest.
	markFaint = 0.22
	// markBreathePeriod and markFlowPeriod are the waves' lengths.
	markBreathePeriod = 3200 * time.Millisecond
	markFlowPeriod    = 1100 * time.Millisecond
	// markColorMotion is how long a change of color takes.
	markColorMotion = 240 * time.Millisecond
	// markFrameRate is the frames a second the moving dots ask for; the
	// waves are slow, so 30 looks as smooth as the display's rate.
	markFrameRate = 30
)

// markWave is a wave from 0 to 1 and back over one period, phase of a
// period behind the others' start.
func markWave(elapsed, period time.Duration, phase float64) float64 {
	t := float64(elapsed%period)/float64(period) - phase
	return 0.5 + 0.5*math.Cos(2*math.Pi*t)
}

// markAlpha is the alpha of dot index (0 to 8, in reading order) at
// elapsed time into the motion. Still dots keep their resting alpha; the
// breathing logo dims each solid dot by up to 40% in turn, the faint ones
// staying; the flowing logo runs the wave through all nine dots, solid
// ones dimming by up to 45% and faint ones lighting up to 0.55.
func markAlpha(motion MarkMotion, index int, elapsed time.Duration) float32 {
	solid := tailscaleDots[index]
	rest := float32(1)
	if !solid {
		rest = markFaint
	}
	switch motion {
	case MarkBreathe:
		if !solid {
			return rest
		}
		order := 0
		for i := 0; i < index; i++ {
			if tailscaleDots[i] {
				order++
			}
		}
		return 0.6 + 0.4*float32(markWave(elapsed, markBreathePeriod, float64(order)/4))
	case MarkFlow:
		w := float32(markWave(elapsed, markFlowPeriod, float64(index)/9))
		if solid {
			return 0.55 + 0.45*w
		}
		return markFaint + (0.55-markFaint)*w
	}
	return rest
}

// markState is the color a mark moves from and to.
type markState struct {
	from, to ui.Color
	at       time.Time
	set      bool
}

// color is the color at now, easing from the old color to the new.
func (s *markState) color(now time.Time, d time.Duration) (ui.Color, bool) {
	if d <= 0 || s.at.IsZero() {
		return s.to, false
	}
	t := float32(now.Sub(s.at)) / float32(d)
	if t >= 1 {
		return s.to, false
	}
	if t < 0 {
		t = 0
	}
	// Ease out.
	t = 1 - (1-t)*(1-t)
	return s.from.Mix(s.to, t), true
}

// retarget moves the mark to color at now, from the color it shows.
func (s *markState) retarget(color ui.Color, now time.Time, d time.Duration) {
	switch {
	case !s.set:
		s.from, s.to, s.set = color, color, true
	case color != s.to:
		shown, _ := s.color(now, d)
		s.from, s.to, s.at = shown, color, now
		if d <= 0 {
			s.from = color
		}
	}
}

// TailscaleMark is Tailscale's logo, side DIPs square, its dots in color.
func TailscaleMark(c *ui.Context, side float32, color ui.Color) ui.Element {
	return TailscaleMarkMoving(c, side, color, MarkStill)
}

// TailscaleMarkMoving is TailscaleMark with its dots moving as motion
// says: the dots breathe or flow while drawn, and the mark asks the
// window for frames only then and while its color changes. With less
// motion asked for the dots stay still and a new color shows at once.
func TailscaleMarkMoving(c *ui.Context, side float32, color ui.Color, motion MarkMotion) ui.Element {
	d := markColorMotion
	if c.Preferences().ReduceMotion {
		motion, d = MarkStill, 0
	}
	box := ui.Box(c).Size(side, side).Shrink(0).Label("Tailscale")
	st := ui.Local(box, "mark", func() markState { return markState{} })
	st.retarget(color, c.Now(), d)
	dot := side * 0.26
	gap := (side - 3*dot) / 2
	return box.Draw(func(p *ui.Painter, r ui.Rect) {
		now := p.Now()
		base, moving := st.color(now, d)
		elapsed := time.Duration(now.UnixNano())
		for i := range tailscaleDots {
			alpha := markAlpha(motion, i, elapsed)
			x := r.X + float32(i%3)*(dot+gap)
			y := r.Y + float32(i/3)*(dot+gap)
			p.Fill(ui.Rect{X: x, Y: y, W: dot, H: dot}, base.Alpha(alpha), dot/2)
		}
		switch {
		case moving:
			p.AnimationFrameRate(60)
		case motion != MarkStill:
			p.AnimationFrameRate(markFrameRate)
		}
	})
}
