package kit

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// SparklineOptions describe a sparkline.
type SparklineOptions struct {
	// Height is the line's height, 30 when 0.
	Height float32
	// Label names the trend for assistive technology.
	Label string
}

// Sparkline insets: the line keeps clear of the top and bottom edges so
// its stroke shows whole.
const (
	sparkTop    = 4
	sparkBottom = 6
	sparkStroke = 1.5
)

// Sparkline is a faint trend of values across its width, the first at
// the left: a smooth line in color at 55% opacity over the area under
// it at 10%, the lowest value near the bottom and the highest near the
// top, equal values level in the middle. Fewer than two finite values
// draw nothing. It only paints, in its drawing, and never asks for
// frames of its own.
func Sparkline(c *ui.Context, values []float64, color ui.Color, o SparklineOptions) ui.Element {
	height := o.Height
	if height <= 0 {
		height = 30
	}
	box := ui.Box(c).FillWidth().Height(height).Shrink(0)
	if o.Label != "" {
		box.Label(o.Label)
	}
	levels := sparkLevels(values)
	if len(levels) < 2 {
		return box
	}
	box.Draw(func(p *ui.Painter, r ui.Rect) { drawSparkline(p, r, levels, color) })
	return box
}

// sparkLevels are the finite values scaled to 0 (the lowest) to 1 (the
// highest), all 0.5 when they are equal.
func sparkLevels(values []float64) []float32 {
	lo, hi := math.Inf(1), math.Inf(-1)
	finite := make([]float64, 0, len(values))
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		finite = append(finite, v)
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	levels := make([]float32, len(finite))
	for i, v := range finite {
		levels[i] = 0.5
		if hi-lo > 1e-9 {
			levels[i] = float32((v - lo) / (hi - lo))
		}
	}
	return levels
}

// drawSparkline paints levels, each from 0 to 1, as a sparkline over r:
// a monotone cubic curve through them, so it never overshoots a sample,
// at 55% of color over the area under it at 10%.
func drawSparkline(p *ui.Painter, r ui.Rect, levels []float32, color ui.Color) {
	n := len(levels)
	if n < 2 || r.W <= 0 || r.H <= 0 {
		return
	}
	step := r.W / float32(n-1)
	span := max(r.H-sparkTop-sparkBottom, 1)
	x := func(i int) float32 { return r.X + float32(i)*step }
	y := func(v float32) float32 { return r.Y + sparkTop + (1-v)*span }
	// Fritsch–Carlson tangents, in level per sample.
	d := make([]float32, n-1)
	for i := range d {
		d[i] = levels[i+1] - levels[i]
	}
	m := make([]float32, n)
	m[0], m[n-1] = d[0], d[n-2]
	for i := 1; i < n-1; i++ {
		if d[i-1]*d[i] > 0 {
			m[i] = 2 / (1/d[i-1] + 1/d[i])
		}
	}
	curve := func() *ui.Path {
		path := &ui.Path{}
		path.MoveTo(x(0), y(levels[0]))
		for i := 1; i < n; i++ {
			path.CubeTo(x(i-1)+step/3, y(levels[i-1]+m[i-1]/3), x(i)-step/3, y(levels[i]-m[i]/3), x(i), y(levels[i]))
		}
		return path
	}
	bottom := r.Y + r.H
	area := curve()
	area.LineTo(x(n-1), bottom).LineTo(x(0), bottom).Close()
	p.FillPath(area, color.Alpha(0.10))
	p.StrokePath(curve(), sparkStroke, color.Alpha(0.55))
}
