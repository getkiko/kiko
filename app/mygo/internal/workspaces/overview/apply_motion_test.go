package overview

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

var someChanges = store.Unapplied{Parts: []string{"路由规则", "出站"}, Pages: map[string]bool{env.PageRoute: true, env.PageSubscriptions: true}}

// motionFixture is a running core with the apply card's changes set by
// the returned switch.
func motionFixture(t *testing.T) (*fixture, *bool) {
	t.Helper()
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	on := new(bool)
	f.w.apply.changes = func() store.Unapplied {
		if *on {
			return someChanges
		}
		return store.Unapplied{}
	}
	return f, on
}

// belowY is the top of the state card's words, which sit under the apply
// card.
func belowY(t *testing.T, f *fixture) float32 {
	t.Helper()
	r, ok := f.tt.Find("运行中")
	if !ok {
		t.Fatalf("no state card: %q", f.tt.Texts())
	}
	return r.Y
}

// sweep advances the clock in 16 ms steps for d, the page's position
// after each.
func sweep(t *testing.T, f *fixture, d time.Duration) []float32 {
	t.Helper()
	var ys []float32
	for at := time.Duration(0); at < d; at += 16 * time.Millisecond {
		f.tt.Advance(16 * time.Millisecond)
		ys = append(ys, belowY(t, f))
	}
	return ys
}

func TestApplyCardSlidesTheContentBelowInAndOut(t *testing.T) {
	f, on := motionFixture(t)
	y0 := belowY(t, f)

	*on = true
	f.tt.Frame()
	if y := belowY(t, f); y-y0 > 2 {
		t.Fatalf("the page jumps by %v as the card appears", y-y0)
	}
	in := sweep(t, f, applyIn+80*time.Millisecond)
	total := in[len(in)-1] - y0
	if total < 50 {
		t.Fatalf("the page moves %v for the card", total)
	}
	prev, biggest := y0, float32(0)
	for i, y := range in {
		if y < prev-0.01 {
			t.Fatalf("enter step %d moves back: %v after %v", i, y, prev)
		}
		biggest = max(biggest, y-prev)
		prev = y
	}
	if biggest > 0.4*total {
		t.Errorf("enter has a step of %v of %v", biggest, total)
	}
	// Entering decelerates: the first steps are longer than the last.
	if first, last := in[2]-y0, in[len(in)-1]-in[len(in)-4]; first <= last {
		t.Errorf("enter does not decelerate: first three steps %v, last three %v", first, last)
	}
	savePreview(t, f.tt, "b-apply-rest.png")

	*on = false
	f.tt.Frame()
	if y := belowY(t, f); y-(y0+total) < -2 {
		t.Fatalf("the page jumps by %v as the card goes", y-(y0+total))
	}
	out := sweep(t, f, applyOut+80*time.Millisecond)
	prev, biggest = y0+total, 0
	for i, y := range out {
		if y > prev+0.01 {
			t.Fatalf("exit step %d moves forward: %v after %v", i, y, prev)
		}
		biggest = max(biggest, prev-y)
		prev = y
	}
	if biggest > 0.45*total {
		t.Errorf("exit has a step of %v of %v", biggest, total)
	}
	// Leaving accelerates: the first steps are shorter than the last.
	if first, last := (y0+total)-out[2], out[len(out)-4]-out[len(out)-1]; first >= last && out[len(out)-1] != out[len(out)-4] {
		t.Errorf("exit does not accelerate: first three steps %v, last three %v", first, last)
	}
	if out[len(out)-1] != y0 {
		t.Errorf("the page rests at %v, not %v", out[len(out)-1], y0)
	}
	if _, ok := f.tt.Find("未应用的更改"); ok {
		t.Error("the card stays in the page after it left")
	}
}

func TestApplyCardTakesPressesWhileEntering(t *testing.T) {
	f, on := motionFixture(t)
	var navigated string
	f.env.SetNavigator(func(page string, _ any) { navigated = page })
	*on = true
	f.tt.Frame()
	f.tt.Advance(100 * time.Millisecond)
	f.click(t, "前往路由")
	if navigated != env.PageRoute {
		t.Errorf("a press while entering was lost: %q", navigated)
	}
}

func TestApplyCardTakesNoPressesWhileLeaving(t *testing.T) {
	f, on := motionFixture(t)
	*on = true
	f.tt.Frame()
	f.tt.Advance(applyIn + 50*time.Millisecond)
	var navigated string
	f.env.SetNavigator(func(page string, _ any) { navigated = page })
	*on = false
	f.tt.Frame()
	f.tt.Advance(40 * time.Millisecond)
	_ = f.tt.Click("前往路由")
	if navigated != "" {
		t.Errorf("the leaving card still navigates: %q", navigated)
	}
}

func TestApplyCardSwitchesAtOnceWithLessMotion(t *testing.T) {
	f, on := motionFixture(t)
	y0 := belowY(t, f)
	*on = true
	f.tt.Frame()
	f.tt.Advance(applyIn + 80*time.Millisecond)
	rest := belowY(t, f)
	*on = false
	f.tt.Frame()
	f.tt.Advance(applyOut + 80*time.Millisecond)
	if belowY(t, f) != y0 {
		t.Fatal("the page does not return")
	}
	f.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	*on = true
	f.tt.Frame()
	if y := belowY(t, f); y != rest {
		t.Errorf("with less motion the page is at %v after one frame, not %v", y, rest)
	}
	*on = false
	f.tt.Frame()
	if y := belowY(t, f); y != y0 {
		t.Errorf("with less motion the page is at %v after one frame, not %v", y, y0)
	}
	if _, ok := f.tt.Find("未应用的更改"); ok {
		t.Error("the card stays with less motion")
	}
}

func TestApplyCardIdleAsksForNoFrames(t *testing.T) {
	f, on := motionFixture(t)
	*on = true
	f.tt.Frame()
	f.tt.Advance(applyIn + 100*time.Millisecond)
	if f.tt.FramePending() {
		t.Error("the resting card keeps asking for frames")
	}
}

// TestApplyCardFrames saves the card's frames for the visual record.
func TestApplyCardFrames(t *testing.T) {
	f, on := motionFixture(t)
	for _, dark := range []bool{false, true} {
		suffix := ""
		if dark {
			suffix = "-dark"
		}
		f.tt.SetDark(dark)
		f.tt.Frame()
		f.tt.Advance(500 * time.Millisecond)
		*on = true
		f.tt.Frame()
		f.tt.Advance(70 * time.Millisecond)
		savePreview(t, f.tt, "b-apply-enter-early"+suffix+".png")
		f.tt.Advance(70 * time.Millisecond)
		savePreview(t, f.tt, "b-apply-enter-mid"+suffix+".png")
		f.tt.Advance(300 * time.Millisecond)
		savePreview(t, f.tt, "b-apply-rest"+suffix+".png")
		*on = false
		f.tt.Frame()
		f.tt.Advance(70 * time.Millisecond)
		savePreview(t, f.tt, "b-apply-exit-early"+suffix+".png")
		f.tt.Advance(70 * time.Millisecond)
		savePreview(t, f.tt, "b-apply-exit-mid"+suffix+".png")
		f.tt.Advance(300 * time.Millisecond)
		savePreview(t, f.tt, "b-apply-gone"+suffix+".png")
	}
}
