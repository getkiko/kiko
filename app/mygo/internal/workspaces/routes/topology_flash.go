package routes

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// The topology's answer to Open: the node of the rule another page
// points at scrolls into view and flashes, as the table highlights its
// row.

const (
	// topoFlashFor is how long a node flashes, in topoFlashPulses pulses.
	topoFlashFor    = time.Second
	topoFlashPulses = 2
	// topoFlashStill is how long it stays marked instead with less motion
	// asked for.
	topoFlashStill = 1500 * time.Millisecond
	// topoRevealMargin is the room kept around a node scrolled into view.
	topoRevealMargin = 24
)

// topoFlash is the node Open pointed at in owner's topology: pending
// until the topology shows it, then flashing from at.
type topoFlash struct {
	id, owner string
	pending   bool
	at        time.Time
}

// topoControlsGuess stands in for the zoom controls' box before the view
// laid them out: their room from the view's bottom right corner.
const (
	topoControlsGuessW = 200
	topoControlsGuessH = 56
)

// revealFlash scrolls the node pending a flash into view (the view at
// bounds, in window DIPs), unless it shows whole already clear of the
// zoom controls, and starts its flash. It waits for the view's size, and
// lets go of a node the topology does not have or a flash of another
// owner's.
func (w *Workspace) revealFlash(l topoLayout, bounds ui.Rect) {
	f := &w.topoFlash
	cam := &w.topoCam
	if !f.pending || !cam.ready || cam.viewW <= 0 || cam.viewH <= 0 {
		return
	}
	b, ok := l.rules[f.id]
	if !ok || f.owner != w.owner {
		*f = topoFlash{}
		return
	}
	z := cam.level()
	x, y, bw, bh := cam.x+b.x*z, cam.y+b.y*z, b.w*z, b.h*z
	m := float32(topoRevealMargin)
	hidden := x < m || y < m || x+bw > cam.viewW-m || y+bh > cam.viewH-m
	// The zoom controls float over the view's bottom right corner.
	ctl := ui.Rect{X: cam.viewW - topoControlsGuessW, Y: cam.viewH - topoControlsGuessH, W: topoControlsGuessW, H: topoControlsGuessH}
	if r := cam.controls; r.W > 0 && r.H > 0 {
		ctl = ui.Rect{X: r.X - bounds.X - m, Y: r.Y - bounds.Y - m, W: r.W + 2*m, H: r.H + 2*m}
	}
	if x < ctl.X+ctl.W && x+bw > ctl.X && y < ctl.Y+ctl.H && y+bh > ctl.Y {
		hidden = true
	}
	if hidden {
		cam.pan(cam.viewW/2-(x+bw/2), cam.viewH/2-(y+bh/2))
	}
	f.pending, f.at = false, time.Now()
}

// flashNode paints the flash on node n of rule id, under its content,
// while it lasts: two pulses from the node's look (base) to the accent's
// stroke and fill, or a still mark with less motion. It asks for frames
// only until the flash ends.
func (w *Workspace) flashNode(c *ui.Context, n ui.Element, id string, base topoNodeLook) {
	f := w.topoFlash
	if f.id != id || f.pending || f.owner != w.owner {
		return
	}
	still := c.Preferences().ReduceMotion
	span := topoFlashFor
	if still {
		span = topoFlashStill
	}
	if time.Since(f.at) >= span {
		w.topoFlash = topoFlash{}
		return
	}
	k := kit.T(c)
	// Opaque, as the canvas draws: the node's own fill and border over
	// the canvas, between pulses as the node shows them.
	fill := base.bg.Over(k.Canvas)
	stroke := base.border.Over(fill)
	n.Draw(func(p *ui.Painter, r ui.Rect) {
		elapsed := p.Now().Sub(f.at)
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed >= span {
			return
		}
		level := float32(1)
		if still {
			p.After(span - elapsed)
		} else {
			// 0 → 1 → 0, topoFlashPulses times.
			phase := float64(elapsed) / float64(span) * topoFlashPulses
			level = float32(0.5 - 0.5*math.Cos(2*math.Pi*phase))
			p.AnimationFrameRate(60)
		}
		ring := base.borderWidth + level*max(0, 2-base.borderWidth)
		p.Fill(r, stroke.Mix(k.Accent, level), 10)
		inner := ui.Rect{X: r.X + ring, Y: r.Y + ring, W: r.W - 2*ring, H: r.H - 2*ring}
		p.Fill(inner, k.Accent.Alpha(0.14*level).Over(fill), 10-ring)
	})
}
