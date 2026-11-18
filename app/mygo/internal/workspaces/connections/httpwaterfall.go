package connections

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// waterKind names a stage of an exchange, which also picks its color.
type waterKind int

const (
	waterDNS waterKind = iota
	waterConnect
	waterTLS
	waterRequestHeaders
	waterRequestBody
	waterWait
	waterResponseHeaders
	waterResponseBody
)

// waterSeg is one drawn stage: where it starts and how long it runs, both
// from the start of the exchange.
type waterSeg struct {
	Name  string
	Kind  waterKind
	Start time.Duration
	Dur   time.Duration
	// Open: the stage is still running and reaches to the moment of the
	// reading, which is the viewer's clock: its duration is not shown.
	Open bool
}

// End is where the stage stops.
func (s waterSeg) End() time.Duration { return s.Start + s.Dur }

// httpWaterfall is the stages of an exchange on one axis. Span is the
// axis' length; Total the exchange's duration with its setup, -1 when unknown.
type httpWaterfall struct {
	Segs  []waterSeg
	Span  time.Duration
	Total time.Duration
}

// waterPoints are the moments of an exchange in order, each ending the
// stage named.
var waterPoints = []struct {
	name string
	kind waterKind
}{
	{"请求头", waterRequestHeaders}, {"请求体", waterRequestBody}, {"等待响应", waterWait}, {"响应头", waterResponseHeaders}, {"响应体", waterResponseBody},
}

// buildWaterfall turns the timing of an exchange into drawable stages.
//
// DNS, connect and TLS are durations laid end to end, ending where the
// exchange's Start is (the core counts Start after them), so the axis
// begins before Start; the other stages are the gaps between consecutive moments that were reached,
// none beginning before the setup ends. A stage without a duration is
// left out, as is a moment before the one preceding it (a clock that ran
// backwards). While live and unfinished, the stage in progress reaches to
// now (Unix nanoseconds).
func buildWaterfall(t *api.HTTPTiming, live bool, now int64) httpWaterfall {
	w := httpWaterfall{Total: -1}
	if t == nil || t.Start <= 0 {
		return w
	}
	var at time.Duration
	for _, s := range []waterSeg{{Name: "DNS", Kind: waterDNS, Dur: time.Duration(t.DNS)}, {Name: "连接", Kind: waterConnect, Dur: time.Duration(t.Connect)}, {Name: "TLS", Kind: waterTLS, Dur: time.Duration(t.TLS)}} {
		if s.Dur <= 0 {
			continue
		}
		s.Start = at
		at += s.Dur
		w.Segs = append(w.Segs, s)
	}
	setupEnd := at
	moments := [...]int64{t.RequestHeaders, t.RequestBody, t.FirstByte, t.ResponseHeaders, t.ResponseBody}
	// A body that never reported its end ends with the exchange.
	if moments[4] == 0 && moments[3] > 0 && t.End > 0 {
		moments[4] = t.End
	}
	var prev time.Duration
	last := -1
	for i, ns := range moments {
		if ns <= 0 {
			continue
		}
		off := setupEnd + max(0, time.Duration(ns-t.Start))
		if off < prev {
			continue
		}
		// A stage begins after the setup, whatever clock the moments came by.
		from := min(max(prev, setupEnd), off)
		if off > from {
			w.Segs = append(w.Segs, waterSeg{Name: waterPoints[i].name, Kind: waterPoints[i].kind, Start: from, Dur: off - from})
		}
		prev, last = off, i
	}
	// The setup comes before Start: the axis begins where it does.
	reach := time.Duration(0)
	if t.End >= t.Start {
		w.Total = setupEnd + time.Duration(t.End-t.Start)
		reach = w.Total
	} else if live && now > t.Start {
		w.Total = setupEnd + time.Duration(now-t.Start)
		reach = w.Total
		// The stage after the last moment reached is the one running.
		next := waterPoints[0]
		switch {
		case last == 0 || last == 1:
			next = waterPoints[2]
		case last >= 2 && last < 4:
			next = waterPoints[last+1]
		case last == 4:
			next.name = ""
		}
		from := min(max(prev, setupEnd), reach)
		if next.name != "" && reach > from {
			w.Segs = append(w.Segs, waterSeg{Name: next.name, Kind: next.kind, Start: from, Dur: reach - from, Open: true})
		}
	}
	for _, s := range w.Segs {
		w.Span = max(w.Span, s.End())
	}
	w.Span = max(w.Span, reach)
	return w
}

// color is the stage's color, a mix of theme colors: setup in the muted
// and warm tones, the request in blue, the wait green, the response in
// deep blue.
func (k waterKind) color(t *kit.Tokens) ui.Color {
	switch k {
	case waterDNS:
		return t.Success.Mix(t.Accent, 0.5)
	case waterConnect:
		return t.Warning
	case waterTLS:
		return t.Danger.Mix(t.Accent, 0.5)
	case waterRequestHeaders, waterRequestBody:
		return t.Accent
	case waterWait:
		return t.Success
	}
	return t.Accent.Mix(t.Label, 0.35)
}

// waterTrack is the width of the axis in the inspector.
const waterTrack = 120

// waterBar is the left edge and the width of a stage on an axis of width
// DIPs: at least 2 DIPs wide, inside the axis.
func waterBar(s waterSeg, span time.Duration, width float32) (left, w float32) {
	if span <= 0 {
		return 0, 2
	}
	w = max(2, width*float32(s.Dur)/float32(span))
	w = min(w, width)
	left = min(width-w, max(0, width*float32(s.Start)/float32(span)))
	return left, w
}

// waterTip is the hover text of a stage.
func waterTip(s waterSeg) string {
	if s.Open {
		return fmt.Sprintf("%s · 起 %s · 进行中", s.Name, httpDuration(s.Start))
	}
	return fmt.Sprintf("%s · 起 %s · 止 %s", s.Name, httpDuration(s.Start), httpDuration(s.End()))
}

// timing shows the stages of the exchange as a waterfall: a bar on a
// common axis for each stage with its duration beside it.
func (m *httpMonitor) timing(c *ui.Context, r *httpRow) {
	w := buildWaterfall(r.Timing, r.live(), time.Now().UnixNano())
	if len(w.Segs) == 0 && w.Total < 0 {
		return
	}
	k := kit.T(c)
	kit.Section(c, "计时", nil, func() {
		for _, s := range w.Segs {
			s := s
			kit.PropertyWith(c, s.Name, func() {
				left, width := waterBar(s, w.Span, waterTrack)
				track := ui.Row(c).Width(waterTrack).Height(8).Radius(4).Background(k.Fill).Shrink(0).Margin(5, 0, 0).Children(func() {
					ui.Box(c).Width(width).Height(8).Radius(4).Background(s.Kind.color(k)).Shrink(0).Margin(0, 0, 0, left)
				})
				kit.Hint(c, track, waterTip(s))
				// The reading time of a running stage is the viewer's clock, not
				// the core's: the bar shows it, no duration is claimed.
				if !s.Open {
					ui.Text(c, httpDuration(s.Dur)).FontSize(kit.FontTable).FontFeatures("tnum")
				}
			})
		}
		if w.Total >= 0 {
			kit.PropertyWith(c, "总计", func() {
				ui.Text(c, httpDuration(w.Total)).FontSize(kit.FontTable).FontWeight(600).FontFeatures("tnum")
			})
		}
	})
	if r.live() && r.Timing != nil && r.Timing.End == 0 {
		c.After(250 * time.Millisecond)
	}
}
