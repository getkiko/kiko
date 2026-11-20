package routes

import (
	"testing"
	"time"
)

// Dots move faster for more traffic, ease toward a new rate rather than
// jumping to it, and fade out when the traffic ends.
func TestFlowLineEases(t *testing.T) {
	var f topoFlows
	now := time.Now()
	f.begin()
	line := f.step("r/in", 1, now)
	for i := 1; i <= 60; i++ {
		f.begin()
		line = f.step("r/in", 1, now.Add(time.Duration(i)*50*time.Millisecond))
	}
	fast := line.speed()
	if fast < flowSpeedMax*0.9 {
		t.Fatalf("full traffic moves at %v", fast)
	}
	at := now.Add(3 * time.Second)
	f.begin()
	line = f.step("r/in", rateFlowMin, at.Add(50*time.Millisecond))
	if eased := line.speed(); eased >= fast || eased < fast*0.8 {
		t.Errorf("one frame after the rate fell the speed is %v of %v", eased, fast)
	}
	for i := 2; i <= 200; i++ {
		f.begin()
		line = f.step("r/in", 0, at.Add(time.Duration(i)*50*time.Millisecond))
	}
	if line.visible() {
		t.Errorf("still visible at level %v after the traffic ended", line.level)
	}
	f.end()
	f.begin()
	f.end()
	if len(f.lines) != 0 {
		t.Error("an undrawn line kept")
	}
}

// A dot stays where it is as the traffic changes; more traffic adds dots
// between, and none shows at the very ends of the line.
func TestFlowDotsKeepPlace(t *testing.T) {
	heads := func(level float64) map[float32]float32 {
		out := map[float32]float32{}
		for _, d := range flowDots(400, 123, 50, level, 1) {
			out[d.head] = d.alpha
		}
		return out
	}
	low, high := heads(rateFlowMin), heads(1)
	if len(high) < 2*len(low) || len(low) == 0 {
		t.Fatalf("dots %d at low, %d at high", len(low), len(high))
	}
	for head := range low {
		if _, ok := high[head]; !ok {
			t.Errorf("dot at %v moved as traffic grew", head)
		}
	}
	for head, alpha := range high {
		if head <= 0 || head >= 400 {
			t.Errorf("dot outside the line at %v", head)
		}
		if (head < flowFade || head > 400-flowFade) && alpha >= 1 {
			t.Errorf("dot at %v not fading at an end", head)
		}
	}
}
