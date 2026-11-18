package kit

import (
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMarkAlphaStillKeepsRestingDots(t *testing.T) {
	for i := range tailscaleDots {
		want := float32(markFaint)
		if tailscaleDots[i] {
			want = 1
		}
		for _, at := range []time.Duration{0, 700 * time.Millisecond, 5 * time.Second} {
			if got := markAlpha(MarkStill, i, at); got != want {
				t.Errorf("still dot %d at %v: %v, want %v", i, at, got, want)
			}
		}
	}
}

func TestMarkAlphaBreatheDimsSolidDotsInTurn(t *testing.T) {
	var solid []int
	for i, s := range tailscaleDots {
		if s {
			solid = append(solid, i)
		}
	}
	// Faint dots stay; solid ones stay within 0.6 to 1.
	for at := time.Duration(0); at < 2*markBreathePeriod; at += 100 * time.Millisecond {
		for i := range tailscaleDots {
			got := markAlpha(MarkBreathe, i, at)
			if !tailscaleDots[i] && got != markFaint {
				t.Fatalf("faint dot %d moves: %v", i, got)
			}
			if tailscaleDots[i] && (got < 0.599 || got > 1.001) {
				t.Fatalf("solid dot %d at %v: %v", i, at, got)
			}
		}
	}
	// The dots peak one after another, a quarter period apart.
	peak := func(i int) time.Duration {
		best, at := float32(-1), time.Duration(0)
		for d := time.Duration(0); d < markBreathePeriod; d += 10 * time.Millisecond {
			if a := markAlpha(MarkBreathe, i, d); a > best+1e-4 {
				best, at = a, d
			}
		}
		return at
	}
	if peak(solid[0]) == peak(solid[1]) || peak(solid[1]) == peak(solid[2]) {
		t.Errorf("solid dots peak together: %v %v %v", peak(solid[0]), peak(solid[1]), peak(solid[2]))
	}
}

func TestMarkAlphaFlowIsFasterAndReachesFaintDots(t *testing.T) {
	if markFlowPeriod >= markBreathePeriod {
		t.Fatal("flow is not faster than breathing")
	}
	lit := float32(0)
	for at := time.Duration(0); at < markFlowPeriod; at += 10 * time.Millisecond {
		lit = max(lit, markAlpha(MarkFlow, 0, at))
	}
	if lit < 0.5 {
		t.Errorf("flow does not light faint dots: %v", lit)
	}
}

func TestMarkStateColorEasesToTheNewColor(t *testing.T) {
	green, red := ui.RGB(0, 200, 0), ui.RGB(200, 0, 0)
	now := time.Unix(100, 0)
	s := &markState{}
	s.retarget(green, now, markColorMotion)
	if c, moving := s.color(now, markColorMotion); c != green || moving {
		t.Fatalf("first color: %v %v", c, moving)
	}
	s.retarget(red, now, markColorMotion)
	if c, moving := s.color(now, markColorMotion); c != green || !moving {
		t.Fatalf("at the change: %v %v", c, moving)
	}
	mid, moving := s.color(now.Add(markColorMotion/2), markColorMotion)
	if !moving || mid == green || mid == red {
		t.Fatalf("halfway: %v %v", mid, moving)
	}
	if c, moving := s.color(now.Add(markColorMotion), markColorMotion); c != red || moving {
		t.Fatalf("after: %v %v", c, moving)
	}
	// A change in the middle starts from the color shown.
	s.retarget(green, now.Add(markColorMotion/2), markColorMotion)
	if c, _ := s.color(now.Add(markColorMotion/2), markColorMotion); c != mid {
		t.Fatalf("restart jumps: %v, was %v", c, mid)
	}
	// No motion: at once.
	s.retarget(red, now.Add(time.Second), 0)
	if c, moving := s.color(now.Add(time.Second), 0); c != red || moving {
		t.Fatalf("without motion: %v %v", c, moving)
	}
}

// markView draws a 100 DIP mark in the color and motion held by the
// pointers; its middle dot sits at (50, 50).
func markView(col *ui.Color, motion *MarkMotion) func(c *ui.Context) {
	return func(c *ui.Context) {
		Apply(c)
		c.Root().Background(ui.RGB(255, 255, 255)).Padding(0).Children(func() {
			TailscaleMarkMoving(c, 100, *col, *motion)
		})
	}
}

func pixel(tt *ui.Tester, x, y int) color.RGBA { return tt.Image().RGBAAt(x, y) }

func TestTailscaleMarkColorsAndStillIsIdle(t *testing.T) {
	col, motion := ui.RGB(0, 160, 0), MarkStill
	tt := ui.NewTester(markView(&col, &motion), 120, 120)
	tt.Frame()
	if p := pixel(tt, 50, 50); p.G < 140 || p.R > 40 {
		t.Fatalf("green mark: %v", p)
	}
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Error("a still mark keeps asking for frames")
	}
}

func TestTailscaleMarkBreathesAndStopsWithLessMotion(t *testing.T) {
	col, motion := ui.RGB(0, 160, 0), MarkBreathe
	tt := ui.NewTester(markView(&col, &motion), 120, 120)
	tt.Frame()
	seen := map[uint8]bool{}
	for range 16 {
		tt.Advance(200 * time.Millisecond)
		seen[pixel(tt, 50, 50).R] = true
	}
	if len(seen) < 3 {
		t.Errorf("a breathing mark does not change: %v", seen)
	}
	if !tt.FramePending() {
		t.Error("a breathing mark does not ask for frames")
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Advance(time.Second)
	a := pixel(tt, 50, 50)
	tt.Advance(700 * time.Millisecond)
	if b := pixel(tt, 50, 50); a != b {
		t.Errorf("with less motion the mark moves: %v then %v", a, b)
	}
	if tt.FramePending() {
		t.Error("with less motion the mark asks for frames")
	}
}

func TestTailscaleMarkRecolorsSmoothlyAndAtOnceWithLessMotion(t *testing.T) {
	col, motion := ui.RGB(0, 160, 0), MarkStill
	tt := ui.NewTester(markView(&col, &motion), 120, 120)
	tt.Frame()
	col = ui.RGB(200, 0, 0)
	tt.Frame()
	tt.Advance(60 * time.Millisecond)
	mid := pixel(tt, 50, 50)
	if mid.R == 0 || mid.R >= 200 || mid.G == 0 {
		t.Errorf("not between the colors: %v", mid)
	}
	tt.Advance(markColorMotion)
	if p := pixel(tt, 50, 50); p.R < 190 || p.G > 10 {
		t.Errorf("after the change: %v", p)
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	col = ui.RGB(0, 160, 0)
	tt.Frame()
	if p := pixel(tt, 50, 50); p.G < 150 || p.R > 10 {
		t.Errorf("with less motion the color does not change at once: %v", p)
	}
}
