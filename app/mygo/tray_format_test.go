package main

import (
	"bytes"
	"image/png"
	"testing"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

func TestFormatRateKeepsFourCharacters(t *testing.T) {
	cases := map[float64]string{
		-1:                "  0K",
		0:                 "  0K",
		30:                "0.1K",
		245:               "0.2K",
		999:               "1.0K",
		1024:              "1.0K",
		1536:              "1.5K",
		12 * 1024:         " 12K",
		999 * 1024:        "999K",
		1023 * 1024:       "1.0M",
		120 * 1024 * 1024: "120M",
		1.2 * (1 << 30):   "1.2G",
	}
	for rate, want := range cases {
		if got := formatRate(rate); got != want {
			t.Errorf("formatRate(%v) = %q, want %q", rate, got, want)
		}
	}
	for rate := 0.0; rate < 1<<40; rate = rate*1.37 + 1 {
		if n := utf8.RuneCountInString(formatRate(rate)); n != 4 {
			t.Fatalf("formatRate(%v) = %q, %d characters", rate, formatRate(rate), n)
		}
	}
}

func TestSpeedTitle(t *testing.T) {
	if got := speedTitle(1536, 0); got != "↓1.5K ↑  0K" {
		t.Fatalf("speedTitle = %q", got)
	}
}

func TestDelayBadge(t *testing.T) {
	ms := func(v float64) *float64 { return &v }
	cases := []struct {
		delay       trayDelay
		text, color string
	}{
		{trayDelay{}, "", ""},
		{trayDelay{Testing: true}, "…", ""},
		{trayDelay{Failed: true}, "超时", delayBad},
		{delayOf(api.LatencyResult{MedianConnectMs: ms(85.4)}), "85 ms", delayGood},
		{delayOf(api.LatencyResult{MedianConnectMs: ms(450)}), "450 ms", delayFair},
		{delayOf(api.LatencyResult{MedianConnectMs: ms(1200)}), "1200 ms", delayBad},
		{delayOf(api.LatencyResult{Error: "timeout"}), "超时", delayBad},
		{delayOf(api.LatencyResult{}), "超时", delayBad},
	}
	for _, c := range cases {
		if text, color := c.delay.badge(); text != c.text || color != c.color {
			t.Errorf("%+v badge = %q %q, want %q %q", c.delay, text, color, c.text, c.color)
		}
	}
}

func TestSwitchable(t *testing.T) {
	cases := []struct {
		group api.Selector
		want  bool
	}{
		{api.Selector{Type: "Selector", Mode: "manual"}, true},
		{api.Selector{Type: "URLTest", Mode: "fastest"}, false},
		{api.Selector{Type: "Selector", Mode: "smart"}, false},
		{api.Selector{Type: "Selector"}, true},
		{api.Selector{Type: "URLTest"}, false},
	}
	for _, c := range cases {
		if got := switchable(c.group); got != c.want {
			t.Errorf("switchable(%+v) = %v", c.group, got)
		}
	}
}

func TestTrayShapeIgnoresChoicesAndDelays(t *testing.T) {
	groups := []api.Selector{{Name: "Proxy", Type: "Selector", Now: "a", All: []string{"a", "b"}}}
	base := trayShape(true, groups, true)
	moved := []api.Selector{{Name: "Proxy", Type: "Selector", Now: "b", All: []string{"a", "b"},
		Members: []api.SelectorMember{{Name: "a", DelayMS: 80}}}}
	if trayShape(true, moved, true) != base {
		t.Fatal("a new choice or delay must not rebuild the menu")
	}
	grown := []api.Selector{{Name: "Proxy", Type: "Selector", All: []string{"a", "b", "c"}}}
	if trayShape(true, grown, true) == base || trayShape(false, groups, true) == base {
		t.Fatal("new members or a stopped core must rebuild the menu")
	}
}

func TestMemberDelayPrefersOwnTest(t *testing.T) {
	g := api.Selector{Members: []api.SelectorMember{{Name: "a", DelayMS: 120}}}
	if d := memberDelay(g, "a", nil); d.Ms != 120 {
		t.Fatalf("core delay = %+v", d)
	}
	if d := memberDelay(g, "a", map[string]trayDelay{"a": {Ms: 40}}); d.Ms != 40 {
		t.Fatalf("own delay = %+v", d)
	}
	if d := memberDelay(g, "b", nil); d != (trayDelay{}) {
		t.Fatalf("untested = %+v", d)
	}
}

func TestTrayIconIsTemplate(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(trayIconPNG()))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 36 || b.Dy() != 36 {
		t.Fatalf("size %v", b)
	}
	opaque := 0
	for y := 0; y < 36; y++ {
		for x := 0; x < 36; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0 && (r != 0 || g != 0 || b != 0) {
				t.Fatalf("pixel %d,%d is not black", x, y)
			}
			if a == 0xffff {
				opaque++
			}
		}
	}
	if opaque < 100 {
		t.Fatalf("only %d opaque pixels", opaque)
	}
}
