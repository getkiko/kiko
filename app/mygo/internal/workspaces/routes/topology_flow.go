package routes

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// The topology's busy lines carry dots with short tails, from the entry
// to the rule and on to its exit: faster and closer together the more the
// rule takes, easing between rates rather than jumping as they arrive.
const (
	// flowSpeedMin and flowSpeedMax are the dots' speeds, in points a
	// second on screen, for the least and the most traffic; between them
	// the speed grows on a log scale with the rule's flow.
	flowSpeedMin = 14.0
	flowSpeedMax = 200.0
	// flowSpacing is the gap between dots when traffic is heaviest; less
	// traffic shows every second, then every third of them.
	flowSpacing = 46.0
	// flowEase is how long the dots take to follow a change of traffic.
	flowEase = 600 * time.Millisecond
	// flowVisible is the least eased flow that still draws dots.
	flowVisible = 0.03
	// flowFade is the length at either end of a line over which dots
	// fade in and out, so they neither pop up nor vanish at a node.
	flowFade = 12.0
)

// topoFlows are the moving lines' states by line, kept between frames.
type topoFlows struct {
	lines map[string]*topoFlowLine
	frame uint64
}

// topoFlowLine is one line's dots: how far they have travelled, the eased
// flow they move by, and when they last moved.
type topoFlowLine struct {
	travelled float64
	level     float64
	at        time.Time
	frame     uint64
}

// begin starts a frame of the lines.
func (f *topoFlows) begin() {
	if f.lines == nil {
		f.lines = map[string]*topoFlowLine{}
	}
	f.frame++
}

// end forgets the lines the frame did not draw.
func (f *topoFlows) end() {
	for key, line := range f.lines {
		if line.frame != f.frame {
			delete(f.lines, key)
		}
	}
}

// step moves key's line to now, easing its flow toward target, and
// returns it.
func (f *topoFlows) step(key string, target float32, now time.Time) *topoFlowLine {
	line := f.lines[key]
	if line == nil {
		line = &topoFlowLine{at: now}
		f.lines[key] = line
	}
	line.frame = f.frame
	// A long pause, as the page hidden, does not leap ahead.
	dt := min(max(now.Sub(line.at).Seconds(), 0), 0.1)
	line.at = now
	line.level += (float64(target) - line.level) * (1 - math.Exp(-dt/flowEase.Seconds()))
	if target == 0 && line.level < flowVisible {
		line.level = 0
	}
	line.travelled += line.speed() * dt
	return line
}

// speed is how fast the line's dots move, in points a second.
func (l *topoFlowLine) speed() float64 {
	return flowSpeedMin * math.Pow(flowSpeedMax/flowSpeedMin, min(max(l.level, 0), 1))
}

// tint is how strongly the rule's lines take its tone while busy: clear
// from the least traffic, deeper for more, fading as the traffic ends.
func (l *topoFlowLine) tint() float32 {
	strength := min(l.level/rateFlowMin, 1)
	return float32(strength * (0.35 + 0.3*min(l.level, 1)))
}

// visible reports whether the line draws dots.
func (l *topoFlowLine) visible() bool { return l.level >= flowVisible }

// topoPath is a polyline measured along its length.
type topoPath struct {
	pts     [][2]float32
	lengths []float32
}

func measureTopoPath(pts [][2]float32) topoPath {
	lengths := make([]float32, len(pts))
	for i := 1; i < len(pts); i++ {
		dx, dy := pts[i][0]-pts[i-1][0], pts[i][1]-pts[i-1][1]
		lengths[i] = lengths[i-1] + float32(math.Hypot(float64(dx), float64(dy)))
	}
	return topoPath{pts, lengths}
}

// length is the path's length.
func (t topoPath) length() float32 {
	if len(t.lengths) == 0 {
		return 0
	}
	return t.lengths[len(t.lengths)-1]
}

// at is the point s along the path.
func (t topoPath) at(s float32) [2]float32 {
	i := 1
	for i < len(t.pts)-1 && t.lengths[i] < s {
		i++
	}
	span := t.lengths[i] - t.lengths[i-1]
	if span <= 0 {
		return t.pts[i]
	}
	f := (s - t.lengths[i-1]) / span
	return [2]float32{t.pts[i-1][0] + (t.pts[i][0]-t.pts[i-1][0])*f, t.pts[i-1][1] + (t.pts[i][1]-t.pts[i-1][1])*f}
}

// section is the part of the path from from to to, with its bends.
func (t topoPath) section(from, to float32) [][2]float32 {
	out := [][2]float32{t.at(from)}
	for i, l := range t.lengths {
		if l > from && l < to {
			out = append(out, t.pts[i])
		}
	}
	return append(out, t.at(to))
}

// topoFlowDot is a dot on a line: where its head is, how long its tail,
// and how opaque it is, 0 to 1.
type topoFlowDot struct {
	head, tail, alpha float32
}

// flowDots are the dots of a line length long, travelled along it at
// speed, level its eased flow, in points scaled by unit (zoomed out, the
// dots keep their size and pace on screen). A dot keeps its place in the
// pattern as it moves: every third shows from the least traffic and the
// others fade in as traffic grows, so a change thins or fills the line
// without a jump. Dots fade in at the start and out before the end.
func flowDots(length float32, travelled, speed, level float64, unit float32) []topoFlowDot {
	spacing := flowSpacing * unit
	if length <= 0 || spacing <= 0 {
		return nil
	}
	// The pattern repeats every three spacings.
	moved := float32(math.Mod(travelled*float64(unit), float64(spacing)*3))
	if moved < 0 {
		// A rule's exit line starts behind its entry's dots.
		moved += spacing * 3
	}
	tail := float32(min(max(0.12*speed, 14), 32)) * unit
	fade := flowFade * unit
	strength := float32(min(level/rateFlowMin, 1))
	var out []topoFlowDot
	for k := -3; ; k++ {
		head := moved + float32(k)*spacing
		if head >= length {
			break
		}
		if head <= 0 {
			continue
		}
		var tier float32 = 1
		switch (k%3 + 3) % 3 {
		case 1:
			tier = smoothstep(0.55, 0.8, float32(level))
		case 2:
			tier = smoothstep(0.3, 0.55, float32(level))
		}
		alpha := strength * tier * min(head/fade, (length-head)/fade, 1)
		if alpha > 0.01 {
			out = append(out, topoFlowDot{head: head, tail: tail, alpha: alpha})
		}
	}
	return out
}

func smoothstep(lo, hi, x float32) float32 {
	t := min(max((x-lo)/(hi-lo), 0), 1)
	return t * t * (3 - 2*t)
}

// drawFlowDots paints a line's dots, travelled along it at speed and
// level: a fading tail behind a round head, in tone. The tail lies on the
// line, solid over its color under; the head, wider than the line, is
// drawn translucent, so a fading dot fades into whatever is behind it.
func drawFlowDots(p *ui.Painter, path topoPath, travelled, speed, level float64, unit float32, tone, under ui.Color) {
	length := path.length()
	for _, d := range flowDots(length, travelled, speed, level, unit) {
		head := d.head
		// The tail, in steps brightening toward the head.
		const steps = 4
		from := max(d.head-d.tail, 0)
		for s := 0; s < steps; s++ {
			a := from + (head-from)*float32(s)/steps
			b := from + (head-from)*float32(s+1)/steps
			if b <= a {
				continue
			}
			level := d.alpha * (0.18 + 0.6*float32(s+1)/steps)
			p.StrokePolyline(path.section(a, b), 1.75*unit, tone.Alpha(level).Over(under))
		}
		at := path.at(head)
		r := 2.75 * unit
		p.Fill(ui.Rect{X: at[0] - r, Y: at[1] - r, W: 2 * r, H: 2 * r}, tone.Alpha(d.alpha), r)
	}
}
