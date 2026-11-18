package kit

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// AreaChartOptions describe an area chart.
type AreaChartOptions struct {
	Label string
	Tone  ChartTone
	// Color, when set, colors the line instead of Tone; the area under it
	// takes the same color, faint.
	Color ui.Color
	// MinimumScale is the least value the top stands for, so idle noise
	// stays near the bottom.
	MinimumScale float64
	// Columns is how many samples the width holds, 30 when 0, the latest
	// at the right edge.
	Columns int
	// Height is the chart's height, 48 when 0; with Fill, its least.
	Height float32
	// Fill grows the chart into the room its parent leaves.
	Fill bool
	// Format, when set, shows the value under the pointer beside a thin
	// guide line.
	Format func(v float64) string
	// Advance counts the samples appended to values so far, and
	// AdvanceDuration is how long the chart takes to slide left by those
	// appended last: as a sample comes, the curve glides left by a step,
	// quick at first and settling (ease out), the sample sliding in from
	// the right edge, instead of jumping.
	Advance         int
	AdvanceDuration time.Duration
	// State keeps the glide and the top from frame to frame; nil draws
	// each frame at rest.
	State *AreaChartState
}

// AreaChartState is what an area chart keeps between frames. Its motion
// runs in the chart's drawing, which asks to be painted again while it
// moves: the view is built only as samples come or the pointer moves.
type AreaChartState struct {
	ready bool
	// The glide goes from from to to, in samples appended, along ease out
	// from start over duration.
	from, to float64
	start    time.Time
	duration time.Duration
	// The top's log, its velocity and its target at at: a critically
	// damped spring.
	scale, velocity, target float64
	at                      time.Time
}

// advance returns how far the glide is at now, and whether it moves.
func (st *AreaChartState) advance(now time.Time) (float64, bool) {
	if st.duration <= 0 {
		return st.to, false
	}
	t := now.Sub(st.start).Seconds() / st.duration.Seconds()
	if !(t < 1) {
		return st.to, false
	}
	return st.from + (st.to-st.from)*float64(ui.EaseOut(float32(max(t, 0)))), true
}

// step moves the top along its spring to now, and reports whether it
// still moves.
func (st *AreaChartState) step(now time.Time) bool {
	dt := now.Sub(st.at).Seconds()
	if dt > 0 {
		st.at = now
		x, v := st.scale-st.target, st.velocity
		if x != 0 || v != 0 {
			// Critically damped: x(t) = (x + (v + w x) t) e^(-w t).
			w := 2 * math.Pi / areaScaleSpring.Seconds()
			b, decay := v+w*x, math.Exp(-w*dt)
			x, v = (x+b*dt)*decay, (v-w*b*dt)*decay
			if math.Abs(x) <= areaScaleRest && math.Abs(v) <= areaScaleRest*w {
				x, v = 0, 0
			}
			st.scale, st.velocity = st.target+x, v
		}
	}
	return st.scale != st.target || st.velocity != 0
}

// follow heads the glide for advance, over d (at once for 0), and the
// top for target, at now; still sets them there at once.
func (st *AreaChartState) follow(now time.Time, advance int, d time.Duration, target float64, still bool) {
	if !st.ready || still {
		*st = AreaChartState{ready: true, from: float64(advance), to: float64(advance), scale: target, target: target, at: now}
		return
	}
	if to := float64(advance); to != st.to {
		at, _ := st.advance(now)
		st.from, st.to, st.start, st.duration = at, to, now, d
	}
	st.step(now)
	st.target = target // the spring keeps its velocity
}

// Area chart geometry, after FlClash's speed chart: the samples are
// smoothed by a Gaussian kernel and joined by a cubic curve with
// Catmull-Rom slopes, clamped so it never dips below 0. The kernel looks
// back only, centred a sample behind the latest, so a drawn hill keeps
// its shape as samples come. reach more samples left of the view fill it
// while it glides. The zero line lies lift DIPs above the bottom edge, so
// a line at 0 shows whole, with the area under it down to the edge.
const (
	areaSigma  = 1.3
	areaTaps   = 6 // the sample and the 5 before it
	areaReach  = 4
	areaLift   = 3
	areaStroke = 2
	// The top follows the peak of the samples shown, in log scale, with
	// a critically damped spring of this period, so it settles gently and
	// keeps its pace as the peak moves on the way; it rests within
	// areaScaleRest of its target (0.2% of the height).
	areaScaleSpring = 1600 * time.Millisecond
	areaScaleRest   = 0.002
	// areaFrameRate is how often a moving chart is painted again.
	areaFrameRate = 30
)

// areaKernel is the smoothing's weights by how far back a sample is.
var areaKernel = func() (k [areaTaps]float64) {
	var sum float64
	for d := range k {
		k[d] = math.Exp(-float64((d-1)*(d-1)) / (2 * areaSigma * areaSigma))
		sum += k[d]
	}
	for d := range k {
		k[d] /= sum
	}
	return
}()

// AreaChart is a compact trend, as FlClash draws traffic: the latest
// samples smoothed into rounded hills over a faint area of the line's
// color, edge to edge, without axes. The bottom stands for 0 and the top
// for the peak of the curve shown (at least MinimumScale): the top eases
// to the peak as it changes, and is never below the curve, which so
// always fits. Samples before the first with a value draw nothing, so a
// young history starts at the right; a nil sample after it counts as 0.
// Hovering marks the nearest sample on the curve and, with Format, reads
// its value out.
func AreaChart(c *ui.Context, values []*float64, o AreaChartOptions) ui.Element {
	k := T(c)
	color := o.Tone.color(k)
	if o.Color != (ui.Color{}) {
		color = o.Color
	}
	columns := o.Columns
	if columns < 2 {
		columns = 30
	}
	height := o.Height
	if height <= 0 {
		height = 48
	}
	// The series: the shown samples, reach before them and those the
	// kernel reads back from the first. Before the history begins there
	// is no curve: the kernel reads the first sample there, so the curve
	// starts level at it instead of rising from 0.
	n := columns + areaReach
	// The history begins at its first sample with a value: the samples
	// before it, taken before the core reported any, are no data.
	for len(values) > 0 && (values[0] == nil || math.IsNaN(*values[0]) || math.IsInf(*values[0], 0)) {
		values = values[1:]
	}
	raw := make([]float64, n+areaTaps-1)
	start := max(len(raw)-len(values), 0)
	for i := range raw {
		if j := len(values) - len(raw) + max(i, start); j >= 0 && j < len(values) {
			if v := values[j]; v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
				raw[i] = math.Max(*v, 0)
			}
		}
	}
	// begin is the first sample of the curve: it starts where the history
	// does, at the right while it is short, and moves left as it fills.
	begin := min(max(start-(areaTaps-1), 0), n)
	heights := make([]float32, n)
	for i := range n {
		var sum float64
		for d, w := range areaKernel {
			sum += w * raw[i+areaTaps-1-d]
		}
		heights[i] = float32(sum)
	}

	box := ui.Box(c).FillWidth().Label(o.Label)
	if o.Fill {
		box.Grow(1).MinHeight(height)
	} else {
		box.Height(height).Shrink(0)
	}
	// The shown samples are the last columns, the latest at the right.
	first := n - columns
	minimum := float32(o.MinimumScale)
	// slopes are the curve's slopes at the samples, lag steps short of the
	// latest. A Catmull-Rom slope takes the next sample, so the latest
	// leans as its last step does, and the one before turns from that lean
	// to its slope as the latest slides in: the curve never jumps.
	slopes := func(lag float32) []float32 {
		s := make([]float32, n)
		for i := begin + 1; i < n; i++ {
			lean := (heights[i] - heights[i-1]) / 2
			if i < n-1 {
				pending := min(max(lag-float32(n-2-i), 0), 1)
				lean += ((heights[i+1]-heights[i-1])/2 - lean) * (1 - pending)
			}
			s[i] = min(max(lean, -3*heights[i]), 3*heights[i])
		}
		return s
	}
	// peak is the curve's highest value from u0 to u1, in samples.
	peak := func(s []float32, u0, u1 float32) float32 {
		top := float32(0)
		if begin < n {
			top = heights[begin]
		}
		for i := begin + 1; i < n; i++ {
			t0, t1 := max(u0-float32(i-1), 0), min(u1-float32(i-1), 1)
			if t0 <= t1 {
				top = max(top, cubicPeak(heights[i-1], heights[i-1]+s[i-1]/3, heights[i]-s[i]/3, heights[i], t0, t1))
			}
		}
		return top
	}
	// The top eases to the peak shown once the curve rests, and is held
	// up by the peak shown on the way.
	target := max(peak(slopes(0), float32(first), float32(n-1)), minimum)
	if !(target > 0) {
		target = 1
	}
	st := o.State
	if st == nil {
		st = &AreaChartState{}
	}
	st.follow(c.Now(), o.Advance, o.AdvanceDuration, math.Log(float64(target)), c.Preferences().ReduceMotion)

	// pointer is where the pointer is across the chart, from 0 to 1, or
	// -1 outside it.
	pointer := float32(-1)
	if x, _, inside := box.PointerPosition(); inside {
		if w := box.Bounds().W; w > 0 {
			pointer = min(max(x/w, 0), 1)
		}
	}
	fill := float32(0.14)
	if k.Dark {
		fill = 0.2
	}
	// The drawing moves the chart to the frame's time and, while the
	// glide or the top moves, asks to be painted again, without the view
	// being built.
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		now := p.Now()
		// lag is how many steps the curve has yet to slide left to show
		// the latest at the right, at most the samples kept left of the
		// view.
		at, gliding := st.advance(now)
		lag := min(max(float32(float64(o.Advance)-at), 0), areaReach)
		settling := st.step(now)
		if gliding || settling {
			p.AnimationFrameRate(areaFrameRate)
		}
		s := slopes(lag)
		shown := max(peak(s, float32(first)-lag, float32(n-1)-lag), minimum)
		scale := max(float32(math.Exp(st.scale)), shown)

		step := r.W / float32(columns-1)
		base, bottom := r.Y+r.H-areaLift, r.Y+r.H
		lift := (base - r.Y - areaStroke/2) / scale
		x := func(i int) float32 { return r.X + (float32(i-first)+lag)*step }
		y := func(v float32) float32 { return base - v*lift }
		if begin >= n-1 {
			// No history yet, or a single sample: nothing to draw.
			return
		}
		curve := func() *ui.Path {
			path := &ui.Path{}
			path.MoveTo(x(begin), y(heights[begin]))
			for i := begin + 1; i < n; i++ {
				path.CubeTo(x(i-1)+step/3, y(heights[i-1]+s[i-1]/3), x(i)-step/3, y(heights[i]-s[i]/3),
					x(i), y(heights[i]))
			}
			return path
		}
		area := curve()
		area.LineTo(x(n-1), bottom).LineTo(x(begin), bottom).Close()
		p.FillPath(area, color.Alpha(fill))
		p.StrokePath(curve(), areaStroke, color)
		if pointer < 0 {
			return
		}
		// The sample nearest the pointer, among those shown.
		u := float32(first) - lag + pointer*float32(columns-1)
		lo, hi := max(int(math.Ceil(float64(float32(first)-lag))), begin), n-1-int(math.Ceil(float64(lag)))
		if lo > hi {
			return
		}
		hover := min(max(int(math.Round(float64(u))), lo), hi)
		hx, hy := x(hover), y(heights[hover])
		p.Line(hx, r.Y, hx, r.Y+r.H, 1, k.Label3.Alpha(0.45))
		p.FillPath((&ui.Path{}).Circle(hx, hy, 4.5), k.Surface)
		p.FillPath((&ui.Path{}).Circle(hx, hy, 3), color)
		if o.Format == nil {
			return
		}
		// The readout, a tag at the top beside the guide line.
		readout := ui.Span{Text: o.Format(raw[hover+areaTaps-1]), Size: 10, Weight: 600, Features: "tnum", Color: k.Label}
		tw, th := p.MeasureText(0, readout)
		tag := ui.Rect{X: hx + 6, Y: r.Y, W: tw + 12, H: th + 2}
		if hx-r.X > r.W/2 {
			tag.X = hx - 6 - tag.W
		}
		p.Shadow(tag, 6, 0, 1, 3, 0, k.Shadow)
		p.Fill(tag, k.Elevated, 6)
		p.RichText(tag.X+6, tag.Y+1, 0, readout)
	})
	return box
}

// cubicPeak is the highest value, for t from t0 to t1 within [0, 1], of
// the cubic Bézier through a, b, c, d (its end values a and d, its
// control values b and c).
func cubicPeak(a, b, c, d, t0, t1 float32) float32 {
	fa, fb, fc, fd := float64(a), float64(b), float64(c), float64(d)
	at := func(t float64) float64 {
		u := 1 - t
		return u*u*u*fa + 3*u*u*t*fb + 3*u*t*t*fc + t*t*t*fd
	}
	lo, hi := float64(t0), float64(t1)
	peak := max(at(lo), at(hi))
	if float64(max(a, b, c, d)) <= peak { // the curve keeps within its control values
		return float32(peak)
	}
	value := func(t float64) {
		if t > lo && t < hi {
			peak = max(peak, at(t))
		}
	}
	// Where the derivative, qa t² + qb t + qc, is 0.
	qa, qb, qc := -fa+3*fb-3*fc+fd, 2*(fa-2*fb+fc), fb-fa
	if qa == 0 {
		if qb != 0 {
			value(-qc / qb)
		}
		return float32(peak)
	}
	disc := qb*qb - 4*qa*qc
	if disc < 0 {
		return float32(peak)
	}
	q := -0.5 * (qb + math.Copysign(math.Sqrt(disc), qb))
	value(q / qa)
	if q != 0 {
		value(qc / q)
	}
	return float32(peak)
}
