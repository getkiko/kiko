package routes

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// The traffic rules' hit lights. Each second the server counts the
// connections each rule took ("ruleHits"); a rule's light blinks for
// them, faster the more there were, as a switch port's: dark, the rule
// carries nothing.

const (
	// hitRise and hitFall are a blink's lighting and fading.
	hitRise = 60 * time.Millisecond
	hitFall = 260 * time.Millisecond
	// hitMaxBlinks bounds the blinks of a second.
	hitMaxBlinks = 6
	// hitSpan is how far back a light's hint counts.
	hitSpan = time.Minute
	// hitHold is how long a still light (less motion) shows a second's
	// hits.
	hitHold = 1500 * time.Millisecond
	// hitLightSize is the light's box, its square 6 DIPs across and its
	// glow the rest.
	hitLightSize = 14
)

// hitSecond is the hits of a rule in one second.
type hitSecond struct {
	at time.Time
	n  int
}

// hitLight is a rule's light: the blinks to show and the last minute's
// hits.
type hitLight struct {
	// pulses are the starts of the blinks, in order, some ahead.
	pulses []time.Time
	recent []hitSecond
}

// ruleHits are the lights by rule hit key: a rule's ID, "final:<owner>"
// for a device's default outbound.
type ruleHits struct {
	lights map[string]*hitLight
	// rates are the bytes per second of each rule's open connections, as
	// of ratesAt.
	rates   map[string]float64
	ratesAt time.Time
}

// hitBlinks is how many blinks n hits in a second make: one for one,
// three for five, at most hitMaxBlinks from about 32.
func hitBlinks(n int) int {
	if n <= 0 {
		return 0
	}
	return min(1+int(math.Log2(float64(n))*1.1), hitMaxBlinks)
}

// add takes a second's hits, arriving at now: their blinks spread over
// the next second, a little apart from even, as a port's lights flicker.
func (h *ruleHits) add(hits map[string]int, now time.Time) {
	if h.lights == nil {
		h.lights = map[string]*hitLight{}
	}
	for key, n := range hits {
		light := h.lights[key]
		if light == nil {
			light = &hitLight{}
			h.lights[key] = light
		}
		light.prune(now)
		light.recent = append(light.recent, hitSecond{at: now, n: n})
		blinks := hitBlinks(n)
		for i := range blinks {
			offset := (float64(i) + rand.Float64()*0.6) / float64(blinks)
			light.pulses = append(light.pulses, now.Add(time.Duration(offset*float64(time.Second))))
		}
	}
}

// prune drops the blinks shown and the hits older than hitSpan.
func (l *hitLight) prune(now time.Time) {
	i := 0
	for i < len(l.pulses) && now.Sub(l.pulses[i]) > hitRise+hitFall {
		i++
	}
	l.pulses = l.pulses[i:]
	j := 0
	for j < len(l.recent) && now.Sub(l.recent[j].at) > hitSpan {
		j++
	}
	l.recent = l.recent[j:]
}

// total is the hits of key in the last hitSpan.
func (h *ruleHits) total(key string, now time.Time) int {
	light := h.lights[key]
	if light == nil {
		return 0
	}
	n := 0
	for _, s := range light.recent {
		if now.Sub(s.at) <= hitSpan {
			n += s.n
		}
	}
	return n
}

// flowSpan is how long after a second's hits the topology's lines
// still flow for it.
const flowSpan = 3 * time.Second

// setRates takes the rates of the rules' open connections, arriving at now.
func (h *ruleHits) setRates(rates map[string]float64, now time.Time) {
	h.rates, h.ratesAt = rates, now
}

// Rates flow from rateFloor, a trickle, to full at rateFull, on a log
// scale, from rateFlowMin up.
const (
	rateFloor   = 256.0
	rateFull    = 8 << 20
	rateFlowMin = 0.15
)

// flow is how busy key's rule is, 0 for idle to 1: the new connections it
// took over the last flowSpan, as its light's blinks count them, or the
// traffic of its open connections now, whichever is more. The topology's
// lines flow by it.
func (h *ruleHits) flow(key string, now time.Time) float32 {
	var hits float32
	if light := h.lights[key]; light != nil {
		n := 0
		for _, s := range light.recent {
			if now.Sub(s.at) <= flowSpan {
				n += s.n
			}
		}
		hits = float32(hitBlinks(n)) / hitMaxBlinks
	}
	var traffic float32
	if rate := h.rates[key]; rate >= rateFloor && now.Sub(h.ratesAt) <= flowSpan {
		f := math.Log(rate/rateFloor) / math.Log(rateFull/rateFloor)
		traffic = rateFlowMin + (1-rateFlowMin)*float32(min(f, 1))
	}
	return max(hits, traffic)
}

// level is how lit key's light is at now, 0 to 1, and whether it still
// changes. With less motion it holds the last second's hits as a steady
// brightness instead of blinking, still changing as the hold ends.
func (h *ruleHits) level(key string, now time.Time, still bool) (float32, bool) {
	light := h.lights[key]
	if light == nil {
		return 0, false
	}
	if still {
		if len(light.recent) == 0 {
			return 0, false
		}
		last := light.recent[len(light.recent)-1]
		if now.Sub(last.at) > hitHold {
			return 0, false
		}
		return float32(hitBlinks(last.n)) / hitMaxBlinks, true
	}
	var lit float32
	moving := false
	for _, p := range light.pulses {
		dt := now.Sub(p)
		switch {
		case dt < 0:
			moving = true
		case dt < hitRise:
			lit, moving = max(lit, float32(dt)/float32(hitRise)), true
		case dt < hitRise+hitFall:
			x := 1 - float32(dt-hitRise)/float32(hitFall)
			lit, moving = max(lit, x*x), true
		}
	}
	return lit, moving
}

// followHits subscribes to the server's rule hits while on, and lets the
// lights go dark off.
func (w *Workspace) followHits(on bool) {
	if !on {
		if w.unsubscribeHits != nil {
			w.unsubscribeHits()
			w.unsubscribeHits = nil
		}
		w.hits = ruleHits{}
		return
	}
	if w.unsubscribeHits != nil {
		return
	}
	w.unsubscribeHits = w.env.Server.Subscribe(func(event api.Event) {
		var payload struct {
			Hits  map[string]int     `json:"hits"`
			Rates map[string]float64 `json:"rates"`
		}
		if event.Decode(&payload) == nil {
			now := time.Now()
			w.hits.add(payload.Hits, now)
			w.hits.setRates(payload.Rates, now)
		}
	}, "ruleHits")
}

// finalHitKey is the hit key of owner's default outbound row: its own
// default outbound rule's, "final:route" for the connections route.final
// carries (other devices, or this device without its own). The local server has its
// own as the emitter gives it one: a local final set, even to null (the
// core's default, its first outbound), or else route.final.
func finalHitKey(doc *model.Doc, owner string) string {
	if owner == model.OtherDeviceID || owner == model.LocalDeviceID && !doc.Route.LocalFinal.Set() && !doc.Route.Final.Valid() {
		return "final:route"
	}
	return "final:" + owner
}

// SetActive follows the rule hits while the page shows; a page left stops
// the topology preview's Current Connections poll.
func (w *Workspace) SetActive(active bool) {
	w.followHits(active)
	if !active {
		w.stopRuleConns()
	}
}

// hitLight is the light of a traffic rule or default row (key), a
// square lit in tone before the row's number; its hint counts the last
// minute's connections.
func (w *Workspace) hitLight(c *ui.Context, key string, tone ui.Color) {
	k := kit.T(c)
	still := c.Preferences().ReduceMotion
	box := ui.Box(c).Size(hitLightSize, hitLightSize).Shrink(0).Margin(0, 0, 0, -2)
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		lit, moving := w.hits.level(key, p.Now(), still)
		switch {
		case moving && still:
			// Only to go dark as the hold ends.
			p.AnimationFrameRate(4)
		case moving:
			p.AnimationFrameRate(60)
		}
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		square := func(half, radius float32, color ui.Color) {
			p.Fill(ui.Rect{X: cx - half, Y: cy - half, W: 2 * half, H: 2 * half}, color, radius)
		}
		if lit > 0 {
			square(3+3*lit, 2, tone.Alpha(0.22*lit))
		}
		square(3, 1, k.Label4.Alpha(0.6).Mix(tone, lit))
	})
	if n := w.hits.total(key, time.Now()); n > 0 {
		kit.Hint(c, box, fmt.Sprintf("最近 1 分钟 %d 个连接", n))
	}
}
