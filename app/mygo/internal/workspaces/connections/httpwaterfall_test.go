package connections

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// setDark switches the theme and lets its transition finish before a
// frame is read, as a screenshot of a settled window.
func setDark(tt *ui.Tester, dark bool) {
	tt.SetDark(dark)
	tt.Frame()
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
}

const wfStart = int64(1_000_000_000)

func ms(n float64) int64 { return int64(n * float64(time.Millisecond)) }

func num(d time.Duration) string { return strconv.FormatFloat(float64(d)/1e6, 'f', -1, 64) }

// segText lists the stages as "name start+dur", in milliseconds.
func segText(w httpWaterfall) string {
	var out []string
	for _, s := range w.Segs {
		open := ""
		if s.Open {
			open = "*"
		}
		out = append(out, fmt.Sprintf("%s %s+%s%s", s.Name, num(s.Start), num(s.Dur), open))
	}
	return strings.Join(out, " | ")
}

func TestBuildWaterfall(t *testing.T) {
	at := func(offset float64) int64 { return wfStart + ms(offset) }
	for _, c := range []struct {
		name  string
		t     *api.HTTPTiming
		live  bool
		now   int64
		segs  string
		total time.Duration
		span  time.Duration
	}{
		{name: "nil", total: -1},
		{name: "no start", t: &api.HTTPTiming{End: 5}, total: -1},
		{
			name:  "full",
			t:     &api.HTTPTiming{Start: wfStart, DNS: ms(4), Connect: ms(20), TLS: ms(30), RequestHeaders: at(60), FirstByte: at(160), ResponseHeaders: at(161), End: at(300)},
			segs:  "DNS 0+4 | 连接 4+20 | TLS 24+30 | 请求头 54+60 | 等待响应 114+100 | 响应头 214+1 | 响应体 215+139",
			total: 354 * time.Millisecond, span: 354 * time.Millisecond,
		},
		{
			name: "reused connection: setup missing",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(2), RequestBody: at(3), FirstByte: at(40), ResponseHeaders: at(41), ResponseBody: at(90), End: at(91)},
			segs: "请求头 0+2 | 请求体 2+1 | 等待响应 3+37 | 响应头 40+1 | 响应体 41+49", total: 91 * time.Millisecond, span: 91 * time.Millisecond,
		},
		{
			name: "setup reaches past the first moment",
			t:    &api.HTTPTiming{Start: wfStart, DNS: ms(100), RequestHeaders: at(50), FirstByte: at(150), End: at(160)},
			segs: "DNS 0+100 | 请求头 100+50 | 等待响应 150+100", total: 260 * time.Millisecond, span: 260 * time.Millisecond,
		},
		{
			name: "clock runs backwards",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(10), RequestBody: at(5), FirstByte: at(30), ResponseHeaders: at(20), End: at(40)},
			segs: "请求头 0+10 | 等待响应 10+20 | 响应体 30+10", total: 40 * time.Millisecond, span: 40 * time.Millisecond,
		},
		{
			name: "moment before the start",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: wfStart - 5, FirstByte: at(30), End: at(30)},
			segs: "等待响应 0+30", total: 30 * time.Millisecond, span: 30 * time.Millisecond,
		},
		{
			name: "ended without moments",
			t:    &api.HTTPTiming{Start: wfStart, End: at(12)}, total: 12 * time.Millisecond, span: 12 * time.Millisecond,
		},
		{
			name: "failed before the response",
			t:    &api.HTTPTiming{Start: wfStart, DNS: ms(5), Connect: ms(10), End: at(15)},
			segs: "DNS 0+5 | 连接 5+10", total: 30 * time.Millisecond, span: 30 * time.Millisecond,
		},
		{
			name: "live, nothing reached",
			t:    &api.HTTPTiming{Start: wfStart}, live: true, now: at(500),
			segs: "请求头 0+500*", total: 500 * time.Millisecond, span: 500 * time.Millisecond,
		},
		{
			name: "live, connecting",
			t:    &api.HTTPTiming{Start: wfStart, DNS: ms(4), Connect: ms(20)}, live: true, now: at(500),
			segs: "DNS 0+4 | 连接 4+20 | 请求头 24+500*", total: 524 * time.Millisecond, span: 524 * time.Millisecond,
		},
		{
			name: "live, request sent",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(10)}, live: true, now: at(500),
			segs: "请求头 0+10 | 等待响应 10+490*", total: 500 * time.Millisecond, span: 500 * time.Millisecond,
		},
		{
			name: "live, first byte",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(10), FirstByte: at(80)}, live: true, now: at(500),
			segs: "请求头 0+10 | 等待响应 10+70 | 响应头 80+420*", total: 500 * time.Millisecond, span: 500 * time.Millisecond,
		},
		{
			name: "live, receiving the body",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(10), FirstByte: at(80), ResponseHeaders: at(81)}, live: true, now: at(500),
			segs: "请求头 0+10 | 等待响应 10+70 | 响应头 80+1 | 响应体 81+419*", total: 500 * time.Millisecond, span: 500 * time.Millisecond,
		},
		{
			name: "live, body done",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(10), FirstByte: at(80), ResponseHeaders: at(81), ResponseBody: at(300)}, live: true, now: at(500),
			segs: "请求头 0+10 | 等待响应 10+70 | 响应头 80+1 | 响应体 81+219", total: 500 * time.Millisecond, span: 500 * time.Millisecond,
		},
		{
			name: "live, now before the start",
			t:    &api.HTTPTiming{Start: wfStart}, live: true, now: wfStart - 1, total: -1,
		},
		{
			name: "not live and unfinished",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(10)}, now: at(500),
			segs: "请求头 0+10", total: -1, span: 10 * time.Millisecond,
		},
		{
			name: "setup longer than the exchange",
			t:    &api.HTTPTiming{Start: wfStart, TLS: ms(80), End: at(50)},
			segs: "TLS 0+80", total: 130 * time.Millisecond, span: 130 * time.Millisecond,
		},
		{
			name: "three days",
			t:    &api.HTTPTiming{Start: wfStart, RequestHeaders: at(1), FirstByte: wfStart + int64(72*time.Hour), End: wfStart + int64(72*time.Hour) + ms(1)},
			segs: "请求头 0+1 | 等待响应 1+259199999", total: 72*time.Hour + time.Millisecond, span: 72*time.Hour + time.Millisecond,
		},
	} {
		w := buildWaterfall(c.t, c.live, c.now)
		if got := segText(w); got != c.segs || w.Total != c.total || w.Span != c.span {
			t.Errorf("%s:\n got %q total %v span %v\nwant %q total %v span %v", c.name, got, w.Total, w.Span, c.segs, c.total, c.span)
		}
		for _, s := range w.Segs {
			if s.Dur <= 0 || s.Start < 0 {
				t.Errorf("%s: bad stage %+v", c.name, s)
			}
		}
	}
}

func TestWaterBar(t *testing.T) {
	span := 100 * time.Millisecond
	for _, c := range []struct {
		seg         waterSeg
		span        time.Duration
		left, width float32
	}{
		{waterSeg{Start: 0, Dur: 50 * time.Millisecond}, span, 0, 60},
		{waterSeg{Start: 50 * time.Millisecond, Dur: 50 * time.Millisecond}, span, 60, 60},
		// A stage under a pixel is still seen, and stays inside the axis.
		{waterSeg{Start: 100 * time.Millisecond, Dur: time.Microsecond}, span, 118, 2},
		{waterSeg{Start: 150 * time.Millisecond, Dur: 400 * time.Millisecond}, span, 0, 120},
		{waterSeg{Dur: time.Second}, 0, 0, 2},
		{waterSeg{Start: 36 * time.Hour, Dur: time.Nanosecond}, 72 * time.Hour, 60, 2},
	} {
		left, width := waterBar(c.seg, c.span, 120)
		if left != c.left || width != c.width {
			t.Errorf("%+v span %v: left %v width %v, want %v %v", c.seg, c.span, left, width, c.left, c.width)
		}
	}
}

func TestWaterTipAndColors(t *testing.T) {
	tip := waterTip(waterSeg{Name: "TLS", Start: 24 * time.Millisecond, Dur: 30 * time.Millisecond})
	if tip != "TLS · 起 24 ms · 止 54 ms" {
		t.Errorf("tip %q", tip)
	}
	if tip := waterTip(waterSeg{Name: "响应体", Start: time.Second, Dur: 1500 * time.Millisecond, Open: true}); tip != "响应体 · 起 1.00 s · 进行中" {
		t.Errorf("open tip %q", tip)
	}
}

// waterfallFrame has a record with every stage and a running one.
func waterfallFrame() api.HTTPFrame {
	f := inspectorFrame()
	f.Events = f.Events[:1]
	r := f.Events[0].Record
	r.Timing = &api.HTTPTiming{
		Start: wfStart, DNS: ms(12), Connect: ms(38), TLS: ms(45), RequestHeaders: wfStart + ms(100), RequestBody: wfStart + ms(101),
		FirstByte: wfStart + ms(420), ResponseHeaders: wfStart + ms(424), ResponseBody: wfStart + ms(980), End: wfStart + ms(985),
	}
	live := hrec(31, "slow.example.com", 0, api.HTTPStateInProgress)
	live.Method, live.Path, live.URL = "GET", "/stream", "https://slow.example.com/stream"
	live.Timing = &api.HTTPTiming{Start: time.Now().Add(-1500 * time.Millisecond).UnixNano(), DNS: ms(8), Connect: ms(25), RequestHeaders: time.Now().Add(-1400 * time.Millisecond).UnixNano()}
	f.Events = append(f.Events, api.HTTPEvent{Kind: api.HTTPEventNew, Record: &live})
	f.Seq = 31
	return f
}

func TestWaterfallInspector(t *testing.T) {
	h := newHTTPHarness(t, 1440, 1000, enabled(), newHTTPFeed(waterfallFrame()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/json")
	h.click(t, "/json")
	testkit.UntilText(t, tt, "计时")
	for _, want := range []string{"DNS", "连接", "TLS", "请求头", "请求体", "等待响应", "响应头", "响应体", "总计", "12 ms", "38 ms", "45 ms", "319 ms", "556 ms", "1.08 s"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	savePreview(t, tt, "26-waterfall-light.png")
	setDark(tt, true)
	savePreview(t, tt, "26-waterfall-dark.png")
	setDark(tt, false)

	// The tip names the stage with its start and end.
	r, ok := tt.Find("TLS")
	if !ok {
		t.Fatal("no TLS row")
	}
	tt.Move(r.X+92+10+waterTrack/2, r.Y+r.H/2)
	tt.Advance(3 * time.Second)
	testkit.UntilText(t, tt, "TLS · 起 50 ms · 止 95 ms")
	savePreview(t, tt, "26-waterfall-tip-light.png")

	// A running exchange grows to now and shows its open stage.
	h.click(t, "/stream")
	testkit.UntilText(t, tt, "概览")
	testkit.UntilText(t, tt, "等待响应")
	if !tt.HasText("总计") {
		t.Fatalf("live: %q", tt.Texts())
	}
	savePreview(t, tt, "27-waterfall-live-light.png")
}

func TestWaterfallNarrowDark(t *testing.T) {
	h := newHTTPHarness(t, 760, 900, enabled(), newHTTPFeed(waterfallFrame()), nil)
	tt := h.tt
	setDark(tt, true)
	testkit.UntilText(t, tt, "/json")
	h.click(t, "/json")
	testkit.UntilText(t, tt, "总计")
	savePreview(t, tt, "28-waterfall-narrow-dark.png")
}

// spread is the difference between the brightest and the darkest pixel
// (0-255) inside the box of a text: how well it reads on its background.
func spread(tt *ui.Tester, text string) (float64, bool) {
	r, ok := tt.Find(text)
	if !ok {
		return 0, false
	}
	img := tt.Image()
	lo, hi := 255.0, 0.0
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			c := img.RGBAAt(x, y)
			l := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
			lo, hi = min(lo, l), max(hi, l)
		}
	}
	return hi - lo, true
}

// In the dark theme the toolbar's segments read once the theme's
// transition has run; the frame right after the switch is the transition's
// middle, which the round 23 screenshot caught.
func TestToolbarDarkSegmentsSettle(t *testing.T) {
	h := newHTTPHarness(t, 1440, 700, enabled(), newHTTPFeed(waterfallFrame()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/json")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "34-toolbar-dark-switching.png")
	before, _ := spread(tt, "全部")
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	savePreview(t, tt, "34-toolbar-dark-settled.png")
	for _, label := range []string{"全部", "异常", "进行中", "HTTP 请求", "网络连接"} {
		if s, ok := spread(tt, label); !ok || s < 90 {
			t.Errorf("%q reads with a spread of %.0f of 255 (found %v)", label, s, ok)
		}
	}
	t.Logf("全部 spread right after the switch %.0f", before)
}
