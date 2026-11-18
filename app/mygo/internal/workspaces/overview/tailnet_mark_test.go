package overview

import (
	"fmt"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// logoPixel is the color of the middle dot of the card's logo: the logo
// sits at the card's left padding, centered vertically.
func logoPixel(t *testing.T, f *fixture) color.RGBA {
	t.Helper()
	card, ok := f.tt.Find(tailscaleLabel)
	if !ok {
		t.Fatalf("no card: %q", f.tt.Texts())
	}
	return f.tt.Image().RGBAAt(int(card.X+18+17), int(card.Y+card.H/2))
}

func (f *fixture) tailscaleState(status map[string]any) {
	status["endpointTag"] = "Tailscale"
	status["observedAt"] = fmt.Sprint(time.Now().UnixMilli())
	f.srv.Emit("tailscale", map[string]any{"endpoints": []map[string]any{status}})
}

// TestTailscaleLogoColorsFollowTheState: the logo's dots carry the state,
// in light and dark, and no dot sits left of the state's words.
func TestTailscaleLogoColorsFollowTheState(t *testing.T) {
	f := newFixtureWith(t, tailscaleDoc())
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find(tailscaleLabel); return ok })
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	self := map[string]any{"hostName": "me", "dnsName": "me.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.1"}, "online": true}
	cases := []struct {
		name, text string
		status     map[string]any
		is         func(p color.RGBA) bool
	}{
		{"online", "控制连接在线", map[string]any{"backendState": "Running", "self": self},
			func(p color.RGBA) bool { return p.G > p.R+30 && p.G > p.B+30 }},
		{"connecting", "启动中", map[string]any{"backendState": "Starting"},
			func(p color.RGBA) bool { return p.R > p.B+40 && p.G > p.B+20 }},
		{"needs login", "需要登录", map[string]any{"backendState": "NeedsLogin"},
			func(p color.RGBA) bool { return p.R > p.B+40 && p.G > p.B+20 }},
		{"error", "失败", map[string]any{"backendState": "Failed"},
			func(p color.RGBA) bool { return p.R > p.G+60 && p.R > p.B+60 }},
	}
	for _, dark := range []bool{false, true} {
		f.tt.SetDark(dark)
		for _, tc := range cases {
			f.tailscaleState(tc.status)
			testkit.UntilText(t, f.tt, tc.text)
			// Past the color's move and a breathing wave's dimmest.
			f.tt.Advance(time.Second)
			var lit color.RGBA
			for range 40 {
				f.tt.Advance(100 * time.Millisecond)
				if p := logoPixel(t, f); p.A == 255 && (!tc.is(lit) || tc.is(p)) {
					lit = p
				}
			}
			if !tc.is(lit) {
				t.Errorf("dark=%v %s: logo %v", dark, tc.name, lit)
			}
			name := "b-tailscale-" + tc.name
			if dark {
				name += "-dark"
			}
			savePreview(t, f.tt, name+".png")
		}
	}
	// Stopped: gray.
	f.emitRuntime(api.Runtime{State: "stopped"})
	testkit.UntilText(t, f.tt, "未运行")
	for _, dark := range []bool{false, true} {
		f.tt.SetDark(dark)
		f.tt.Advance(time.Second)
		if p := logoPixel(t, f); absDiff(p.R, p.B) > 24 || absDiff(p.R, p.G) > 24 || (!dark && p.R > 200) {
			t.Errorf("dark=%v stopped logo: %v", dark, p)
		}
		name := "b-tailscale-stopped"
		if dark {
			name += "-dark"
		}
		savePreview(t, f.tt, name+".png")
	}
	if f.tt.FramePending() {
		t.Error("a stopped logo keeps asking for frames")
	}
}

func absDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

// TestTailscaleLogoMovesOnlyWhileOnline: the dots breathe while online
// and hold still with less motion.
func TestTailscaleLogoMovesOnlyWhileOnline(t *testing.T) {
	f := newFixtureWith(t, tailscaleDoc())
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find(tailscaleLabel); return ok })
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	f.tailscaleState(map[string]any{"backendState": "Running",
		"self": map[string]any{"hostName": "me", "dnsName": "me.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.1"}, "online": true}})
	testkit.UntilText(t, f.tt, "控制连接在线")
	f.tt.Advance(time.Second)
	seen := map[uint8]bool{}
	for range 30 {
		f.tt.Advance(100 * time.Millisecond)
		seen[logoPixel(t, f).G] = true
	}
	if len(seen) < 3 {
		t.Errorf("the online logo does not breathe: %v", seen)
	}
	f.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	f.tt.Frame()
	f.tt.Advance(time.Second)
	a := logoPixel(t, f)
	f.tt.Advance(900 * time.Millisecond)
	if b := logoPixel(t, f); a != b {
		t.Errorf("with less motion the logo moves: %v then %v", a, b)
	}
}
