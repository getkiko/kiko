package routes

import (
	"math"
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// The topology's canvas is unbounded: the view shows it panned and
// zoomed, as a camera looks at it. Pinching, ⌘-scrolling and the zoom
// buttons zoom; scrolling and dragging pan.

const (
	topoZoomMin = 0.4
	topoZoomMax = 2.5
	// topoKeep is how much of the topology stays in the view however far
	// it is panned, so that it is never lost.
	topoKeep = 64
	// topoFitMargin is the room left around the topology when it is fit
	// into the view, beyond the layout's own padding.
	topoFitMargin = 0
)

// topoZoomSteps are the zooms the buttons and shortcuts step through.
var topoZoomSteps = []float32{0.4, 0.5, 0.67, 0.8, 1, 1.25, 1.5, 2, 2.5}

// topoCamera is how the view shows the topology: zoomed by zoom, the
// topology's top left at (x, y) of the view. It keeps the view's size and
// the topology's to hold the middle in place as either changes.
type topoCamera struct {
	ready        bool
	owner        string
	zoom, x, y   float32
	viewW, viewH float32
	contentW     float32
	contentH     float32
	// fitted is that the camera shows the whole topology as reset placed
	// it, so that it fits again as the view or the topology changes.
	fitted bool
	// dragKey is the node a drag started on, drag how far it went and
	// panned that it moved the topology, so that letting go is no click.
	dragKey string
	drag    float32
	panned  bool
	// controls is the zoom controls' box in window DIPs as the last frame
	// laid it out, empty before.
	controls ui.Rect
}

// level is the zoom, 1 before the camera is placed.
func (cam *topoCamera) level() float32 {
	if !cam.ready || cam.zoom <= 0 {
		return 1
	}
	return cam.zoom
}

// frame takes the view's size and the topology's for this frame: it fits
// a topology shown for the first time or of another owner, fits it again
// as it changes while it is fit, and otherwise keeps the middle of the
// view where it was as the view resizes.
func (cam *topoCamera) frame(owner string, viewW, viewH, contentW, contentH float32) {
	if viewW <= 0 || viewH <= 0 {
		// The view is not laid out yet: the camera waits for its size.
		return
	}
	if !cam.ready || cam.owner != owner {
		cam.ready, cam.owner = true, owner
		cam.viewW, cam.viewH, cam.contentW, cam.contentH = viewW, viewH, contentW, contentH
		cam.reset()
		return
	}
	if cam.fitted {
		if viewW != cam.viewW || viewH != cam.viewH || contentW != cam.contentW || contentH != cam.contentH {
			cam.viewW, cam.viewH, cam.contentW, cam.contentH = viewW, viewH, contentW, contentH
			cam.reset()
		}
		return
	}
	cam.x += (viewW - cam.viewW) / 2
	cam.y += (viewH - cam.viewH) / 2
	cam.viewW, cam.viewH, cam.contentW, cam.contentH = viewW, viewH, contentW, contentH
	cam.clamp()
}

// fitZoom is the zoom that shows the whole topology in the view, at most
// 100%.
func (cam *topoCamera) fitZoom() float32 {
	if cam.contentW <= 0 || cam.contentH <= 0 {
		return 1
	}
	w := max(cam.viewW-2*topoFitMargin, 1)
	h := max(cam.viewH-2*topoFitMargin, 1)
	return min(1, w/cam.contentW, h/cam.contentH)
}

// minZoom is how far the topology zooms out: far enough to show it whole.
func (cam *topoCamera) minZoom() float32 {
	return min(topoZoomMin, cam.fitZoom())
}

// reset shows the whole topology in the middle of the view, at 100% when
// it fits and smaller when it does not.
func (cam *topoCamera) reset() {
	cam.zoom = cam.fitZoom()
	cam.x = (cam.viewW - cam.contentW*cam.zoom) / 2
	cam.y = (cam.viewH - cam.contentH*cam.zoom) / 2
	cam.fitted = true
}

// zoomAt zooms to z, keeping the point (px, py) of the view on the same
// point of the topology.
func (cam *topoCamera) zoomAt(z, px, py float32) {
	old := cam.level()
	z = min(max(z, cam.minZoom()), topoZoomMax)
	if z == old {
		return
	}
	cam.fitted = false
	cam.x = px - (px-cam.x)*z/old
	cam.y = py - (py-cam.y)*z/old
	cam.zoom = z
	cam.clamp()
}

// step zooms to the next step in or out, about the middle of the view.
func (cam *topoCamera) step(in bool) {
	z := cam.level()
	next := z
	if in {
		for _, s := range topoZoomSteps {
			if s > z+0.001 {
				next = s
				break
			}
		}
	} else {
		for i := len(topoZoomSteps) - 1; i >= 0; i-- {
			if topoZoomSteps[i] < z-0.001 {
				next = topoZoomSteps[i]
				break
			}
		}
		if next == z {
			next = cam.minZoom()
		}
	}
	cam.zoomAt(next, cam.viewW/2, cam.viewH/2)
}

// pan moves the topology by (dx, dy) DIPs of the view.
func (cam *topoCamera) pan(dx, dy float32) {
	if dx == 0 && dy == 0 {
		return
	}
	cam.x += dx
	cam.y += dy
	cam.fitted = false
	cam.clamp()
}

// clamp keeps topoKeep DIPs of the topology in the view each way.
func (cam *topoCamera) clamp() {
	z := cam.level()
	w, h := cam.contentW*z, cam.contentH*z
	keepX, keepY := min(topoKeep, w), min(topoKeep, h)
	cam.x = min(max(cam.x, keepX-w), cam.viewW-keepX)
	cam.y = min(max(cam.y, keepY-h), cam.viewH-keepY)
}

// topologyInput zooms and pans the topology by the pointer's gestures over
// the view: pinching (and Ctrl-scrolling on Windows) and ⌘-scrolling
// zoom about the pointer, scrolling pans.
func (w *Workspace) topologyInput(ev ui.InputEvent) bool {
	cam := &w.topoCam
	switch ev.Kind {
	case ui.InputZoom:
		if ev.Scale <= 0 {
			return false
		}
		cam.zoomAt(cam.level()*ev.Scale, ev.X, ev.Y)
		return true
	case ui.InputScroll:
		if ev.Mods&(ui.Cmd|ui.Ctrl) != 0 {
			cam.zoomAt(cam.level()*float32(math.Pow(1.0025, float64(-ev.DY))), ev.X, ev.Y)
			return true
		}
		cam.pan(-ev.DX, -ev.DY)
		return true
	}
	return false
}

// topologyShortcuts zoom by the keyboard: ⌘= in, ⌘- out, ⌘0 to the
// whole topology.
func (w *Workspace) topologyShortcuts(c *ui.Context) {
	cam := &w.topoCam
	switch {
	case c.Shortcut(ui.Cmd, ui.KeyEqual), c.Shortcut(ui.Cmd|ui.Shift, ui.KeyEqual):
		cam.step(true)
	case c.Shortcut(ui.Cmd, ui.KeyMinus):
		cam.step(false)
	case c.Shortcut(ui.Cmd, ui.Key0):
		cam.reset()
	}
}

// drawTopologyDots paints the view's dots, as many as fill it, moving and
// spreading with the topology. Zoomed out they keep their size and grow
// apart, never closer than at 75% of their spacing at 100%.
func drawTopologyDots(p *ui.Painter, r ui.Rect, k *kit.Tokens, cam *topoCamera) {
	z := cam.level()
	step := float32(topoDot)
	for step*z < topoDot*0.75 {
		step *= 2
	}
	gap := step * z
	radius := min(max(z, 1), 1.5)
	dot := k.Label4.Alpha(0.45).Over(k.Canvas)
	// The dots sit at the middles of the topology's cells of step.
	x0 := cam.x + step/2*z
	y0 := cam.y + step/2*z
	// The first dot inside the view each way.
	x0 -= float32(math.Floor(float64(x0/gap))) * gap
	y0 -= float32(math.Floor(float64(y0/gap))) * gap
	if x0 < radius {
		x0 += gap
	}
	if y0 < radius {
		y0 += gap
	}
	for y := y0; y+radius <= r.H; y += gap {
		for x := x0; x+radius <= r.W; x += gap {
			p.Fill(ui.Rect{X: r.X + x - radius, Y: r.Y + y - radius, W: 2 * radius, H: 2 * radius}, dot, radius)
		}
	}
}

// topologyZoomControls float at the view's bottom right: zoom out, the
// zoom, zoom in, and back to the whole topology in the middle.
func (w *Workspace) topologyZoomControls(c *ui.Context) {
	k := kit.T(c)
	cam := &w.topoCam
	z := cam.level()
	group := ui.Row(c.Key("topology-zoom")).Absolute().Right(16).Bottom(16).Height(kit.ToolbarGroupH).Padding(0, 4).Gap(2).
		Radius(kit.RadiusCapsule)
	cam.controls = group.Bounds()
	kit.Glass(c, group).Children(func() {
		kit.IconButton(c, kit.SymMinus, "缩小", kit.IconButtonOptions{Disabled: z <= cam.minZoom()+0.001, Hint: "缩小 " + kit.ShortcutLabel("−")}).
			OnClick(func() { cam.step(false) })
		percent := strconv.Itoa(int(math.Round(float64(z*100)))) + "%"
		ui.Text(c, percent).FontSize(kit.FontCaption).FontFeatures("tnum").
			TextColor(k.Label2).Width(40).TextAlign(ui.Center).Label("缩放比例 " + percent)
		kit.IconButton(c, kit.SymPlus, "放大", kit.IconButtonOptions{Disabled: z >= topoZoomMax, Hint: "放大 " + kit.ShortcutLabel("=")}).
			OnClick(func() { cam.step(true) })
		kit.IconButton(c, kit.SymRotateCcw, "恢复原始大小", kit.IconButtonOptions{Hint: "恢复原始大小 " + kit.ShortcutLabel("0")}).
			OnClick(func() { cam.reset() })
	})
}
