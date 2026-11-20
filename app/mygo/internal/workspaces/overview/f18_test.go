package overview

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// formerTitle is where a page's kit.Toolbar puts its title: where the
// overview's title stood.
func formerTitle(t *testing.T) ui.Rect {
	t.Helper()
	tt := ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		ui.Column(c).Fill().Children(func() { kit.Toolbar(c, "概览", "", nil) })
	}, 980, 200)
	tt.Frame()
	r, ok := tt.Find("概览")
	if !ok {
		t.Fatal("the toolbar shows no title")
	}
	return r
}

// runningFixture is a running core with its metrics, as the screenshots.
func runningFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true, UptimeSeconds: 3725, MetricsAvailable: true,
		DownloadRate: f64(393216), UploadRate: f64(131072), MemoryBytes: f64(17 << 20), ActiveConnections: f64(12),
		UploadTotal: f64(12 << 20), DownloadTotal: f64(48 << 20)})
	testkit.UntilText(t, f.tt, "自动")
	testkit.Until(t, f.tt, func() bool { return len(f.w.metrics.points) > 0 })
	return f
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) <= 0.5 }

// centeredX is where the page's first card starts in a workspace this
// wide: the title's inset, the page centered once it reaches pageWidth.
func centeredX(title ui.Rect, width int) float32 {
	x := title.X
	if room := float32(width) - pageWidth; room > 0 {
		x += room / 2
	}
	return x
}

// The page has no title: its first card starts cardsTop down, at the title's inset,
// the page centered in wide workspaces as before F18 (1180 is the
// default window's).
func TestCardsStartWhereTheTitleStood(t *testing.T) {
	title := formerTitle(t)
	f := runningFixture(t)
	for _, width := range []int{760, 980, 1180, 1192, 1320, 1600, 1728} {
		f.tt.SetSize(width, 800)
		f.tt.Frame()
		if _, ok := f.tt.Find("概览"); ok {
			t.Errorf("width %d: the page shows its title: %q", width, f.tt.Texts())
		}
		card, ok := f.tt.Find("核心状态")
		if !ok {
			t.Fatalf("width %d: no state card: %q", width, f.tt.Texts())
		}
		wantX := centeredX(title, width)
		if !near(card.X, wantX) || !near(card.Y, cardsTop) {
			t.Errorf("width %d: the first card starts at (%v, %v), want (%v, %v)", width, card.X, card.Y, wantX, float32(cardsTop))
		}
		// Where the first card stood before F18, measured.
		if measured, ok := map[int]float32{980: 20, 1320: 104, 1600: 244}[width]; ok && !near(card.X, measured) {
			t.Errorf("width %d: the first card starts at x=%v, before F18 at %v", width, card.X, measured)
		}
		if grid, _ := f.tt.Find("核心运行指标"); grid.W > pageWidth-40+0.5 {
			t.Errorf("width %d: the grid is %v wide, more than %v", width, grid.W, pageWidth-40)
		}
	}
}

// The first row of cards keeps its shadow above it, inside the scroll's
// clip: the pixels just above a card are not the bare window surface.
func TestFirstCardsShadowIsNotClipped(t *testing.T) {
	f := runningFixture(t)
	f.tt.SetSize(1180, 800)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	for _, label := range []string{"核心状态", "内存"} {
		card, ok := f.tt.Find(label)
		if !ok {
			t.Fatalf("no %s card: %q", label, f.tt.Texts())
		}
		img := f.tt.Image()
		scale := float32(img.Bounds().Dx()) / 1180
		x := int((card.X + card.W/2) * scale)
		canvas := img.RGBAAt(x, int(2*scale))
		top := int(card.Y * scale)
		if above := img.RGBAAt(x, top-1); above == canvas {
			t.Errorf("%s card: the pixel above its top (y=%d) is the bare surface %v: its shadow is clipped", label, top-1, canvas)
		}
	}
}

// TestF19Screenshots writes the page without its title and centered,
// light and dark in the default window's workspace (1320 less the 140
// DIP sidebar), wide and narrow, where
// OVERVIEW_PREVIEW_DIR says.
func TestF19Screenshots(t *testing.T) {
	if os.Getenv("OVERVIEW_PREVIEW_DIR") == "" {
		t.Skip("OVERVIEW_PREVIEW_DIR is not set")
	}
	f := runningFixture(t)
	// Each shot waits for the colors' transitions to settle.
	settle := func() { f.tt.Frame(); f.tt.Advance(time.Second) }
	f.tt.SetSize(1180, 800)
	settle()
	savePreview(t, f.tt, "f19-after-light.png")
	f.tt.SetDark(true)
	settle()
	savePreview(t, f.tt, "f19-after-dark.png")
	f.tt.SetDark(false)
	f.tt.SetSize(1600, 800)
	settle()
	savePreview(t, f.tt, "f19-after-wide.png")
	f.tt.SetSize(760, 800)
	settle()
	savePreview(t, f.tt, "f19-after-narrow.png")
}
