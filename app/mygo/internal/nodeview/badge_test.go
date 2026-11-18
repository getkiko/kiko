package nodeview

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// tokens are the light tokens, as a view gets them.
func tokens(t *testing.T) *kit.Tokens {
	var k *kit.Tokens
	tt := ui.NewTester(func(c *ui.Context) { k = kit.T(c) }, 10, 10)
	tt.Frame()
	if k == nil {
		t.Fatal("no tokens")
	}
	return k
}

// A member's tag is the app's last test when there is one, else the
// core's state: down "Unavailable", suspect "Unconfirmed", nothing for up or unknown.
func TestMemberTag(t *testing.T) {
	k := tokens(t)
	ms := 86.0
	measured := api.LatencyResult{SuccessSamples: 1, MedianConnectMs: &ms}
	timeout := api.LatencyResult{Error: "Timeout"}
	failed := api.LatencyResult{Error: "connection refused"}
	cases := []struct {
		name   string
		r      api.LatencyResult
		state  string
		text   string
		fg     ui.Color
		hint   string
		absent bool
	}{
		{name: "measured", r: measured, text: "86 ms", fg: k.Success, hint: LatencyHint(measured)},
		{name: "timeout", r: timeout, text: "超时", fg: k.Danger, hint: "Timeout"},
		{name: "failed", r: failed, text: "不可用", fg: k.Danger, hint: "connection refused"},
		{name: "core down", state: CoreDown, text: "不可用", fg: k.Danger, hint: "核心状态：不可用"},
		{name: "core suspect", state: CoreSuspect, text: "待确认", fg: k.Warning, hint: "核心状态：待确认"},
		{name: "core up", state: "up", absent: true},
		{name: "untested", absent: true},
		{name: "the app's test wins over down", r: measured, state: CoreDown, text: "86 ms", fg: k.Success, hint: LatencyHint(measured)},
		{name: "the app's failure wins over suspect", r: failed, state: CoreSuspect, text: "不可用", fg: k.Danger, hint: "connection refused"},
	}
	for _, c := range cases {
		text, fg, _, hint, ok := MemberTag(k, c.r, c.state)
		if c.absent {
			if ok {
				t.Errorf("%s: shows %q", c.name, text)
			}
			continue
		}
		if !ok || text != c.text || fg != c.fg || hint != c.hint {
			t.Errorf("%s: %q %v %q %v, want %q %v %q", c.name, text, fg, hint, ok, c.text, c.fg, c.hint)
		}
	}
}

// The badges show their words, nothing before a test (no "—"), the
// spinner while testing, which stops asking for frames once the test is
// done, and "Testing" in words with less motion.
func TestMemberBadgeStates(t *testing.T) {
	ms := 86.0
	busy := true
	tt := ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		c.Root().Padding(10).Gap(6).Children(func() {
			MemberBadge(c, api.LatencyResult{SuccessSamples: 1, MedianConnectMs: &ms}, false, CoreDown)
			MemberBadge(c, api.LatencyResult{Error: "Timeout"}, false, "")
			MemberBadge(c, api.LatencyResult{}, false, CoreSuspect)
			MemberBadge(c, api.LatencyResult{}, false, "up")
			LatencyBadge(c, api.LatencyResult{}, false)
			LatencyBadge(c, api.LatencyResult{}, busy)
		})
	}, 200, 200)
	tt.Frame()
	texts := tt.Texts()
	for _, want := range []string{"86 ms", "超时", "待确认"} {
		if !slices.Contains(texts, want) {
			t.Errorf("missing %q in %q", want, texts)
		}
	}
	for _, unwanted := range []string{"—", "不可用"} {
		if slices.Contains(texts, unwanted) {
			t.Errorf("%q shows in %q", unwanted, texts)
		}
	}
	tt.Advance(200 * time.Millisecond)
	if !spinnerTurns(tt) {
		t.Error("the spinner of a test under way does not turn")
	}
	busy = false
	tt.Frame()
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Error("badges ask for frames after the test")
	}
	busy = true
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if !tt.HasText("测速中") {
		t.Errorf("with less motion no 测速中: %q", tt.Texts())
	}
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Error("with less motion the badge asks for frames")
	}
}

// spinnerTurns tells whether the window changes over 80 ms.
func spinnerTurns(tt *ui.Tester) bool {
	a := slices.Clone(tt.Image().Pix)
	tt.Advance(80 * time.Millisecond)
	return !slices.Equal(a, tt.Image().Pix)
}
