package kit

import (
	"bytes"
	"image"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// spinnerView draws a 40 DIP spinner at (10, 10) while *shown, counting
// the builds of the view.
func spinnerView(shown *bool, builds *int) func(c *ui.Context) {
	return func(c *ui.Context) {
		*builds++
		Apply(c)
		c.Root().Background(ui.RGB(255, 255, 255)).Padding(10).Children(func() {
			if *shown {
				SpinnerIn(c, 40, ui.RGB(0, 0, 0))
			} else {
				ui.Box(c).Size(40, 40)
			}
		})
	}
}

// spinnerPixels are the pixels of the spinner's box.
func spinnerPixels(tt *ui.Tester) []byte {
	return spinnerPixelsAt(tt, image.Rect(10, 10, 50, 50))
}

// A spinner's spokes turn, painted again at each step without building
// the view, and it asks for no frames once it is gone.
func TestSpinnerTurnsWithoutBuildingTheView(t *testing.T) {
	shown, builds := true, 0
	tt := ui.NewTester(spinnerView(&shown, &builds), 60, 60)
	tt.Frame()
	if bytes.Count(spinnerPixels(tt), []byte{255, 255, 255, 255}) == 40*40 {
		t.Fatal("no spinner drawn")
	}
	before := builds
	seen := [][]byte{spinnerPixels(tt)}
	for range 4 {
		tt.Advance(80 * time.Millisecond)
		frame := spinnerPixels(tt)
		if bytes.Equal(frame, seen[len(seen)-1]) {
			t.Fatalf("the spinner did not move in 80 ms (frame %d)", len(seen))
		}
		seen = append(seen, frame)
	}
	if builds != before {
		t.Errorf("turning built the view %d times", builds-before)
	}
	shown = false
	tt.Frame()
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Error("a spinner gone keeps asking for frames")
	}
	after := builds
	still := spinnerPixels(tt)
	tt.Advance(time.Second)
	if builds != after || !bytes.Equal(still, spinnerPixels(tt)) {
		t.Error("the view changes with no spinner")
	}
}

// With less motion the spinner is a still ellipsis asking for no frames.
func TestSpinnerStillWithLessMotion(t *testing.T) {
	shown, builds := true, 0
	tt := ui.NewTester(spinnerView(&shown, &builds), 60, 60)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	tt.Advance(100 * time.Millisecond)
	a := spinnerPixels(tt)
	if bytes.Count(a, []byte{255, 255, 255, 255}) == 40*40 {
		t.Fatal("nothing drawn with less motion")
	}
	tt.Advance(700 * time.Millisecond)
	if !bytes.Equal(a, spinnerPixels(tt)) {
		t.Error("with less motion the spinner moves")
	}
	if tt.FramePending() {
		t.Error("with less motion the spinner asks for frames")
	}
}

// darkest is the lowest red, green or blue of the pixels in r.
func darkest(tt *ui.Tester, r image.Rectangle) uint8 {
	img := tt.Image()
	low := uint8(255)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := img.RGBAAt(x, y)
			low = min(low, p.R, p.G, p.B)
		}
	}
	return low
}

// A busy icon button shows the spinner in its symbol's color, unfaded,
// and takes no clicks; the hint while busy replaces its label's.
func TestIconButtonBusy(t *testing.T) {
	busy, clicks := false, 0
	// The button at (10, 10), and the spinner alone, in the button's
	// symbol color and size, centered in a box as large at (50, 10).
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(ui.RGB(255, 255, 255)).Padding(10).Gap(10).Children(func() {
			ui.Row(c).Gap(10).Children(func() {
				IconButton(c, SymGauge, "测速", IconButtonOptions{Busy: busy, BusyHint: "测速中"}).OnClick(func() { clicks++ })
				ui.Box(c).Size(ControlHeight, ControlHeight).Center().Shrink(0).Children(func() { SpinnerIn(c, 16, T(c).Label2) })
			})
		})
	}, 100, 60)
	tt.Frame()
	box, alone := image.Rect(10, 10, 40, 40), image.Rect(50, 10, 80, 40)
	if err := tt.Click("测速"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if clicks != 1 {
		t.Fatalf("idle button clicks: %d", clicks)
	}
	busy = true
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	if d, want := darkest(tt, box), darkest(tt, alone); int(d) > int(want)+8 {
		t.Errorf("busy button faded: darkest %d, the spinner alone %d", d, want)
	}
	_ = tt.Click("测速")
	tt.Frame()
	if clicks != 1 {
		t.Errorf("busy button took a click: %d", clicks)
	}
	a := spinnerPixelsAt(tt, box)
	tt.Advance(80 * time.Millisecond)
	if bytes.Equal(a, spinnerPixelsAt(tt, box)) {
		t.Error("busy button's spinner does not turn")
	}
	busy = false
	tt.Frame()
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Error("an idle button asks for frames")
	}
}

// spinnerPixelsAt are the pixels of r.
func spinnerPixelsAt(tt *ui.Tester, r image.Rectangle) []byte {
	img := tt.Image()
	var out []byte
	for y := r.Min.Y; y < r.Max.Y; y++ {
		i := img.PixOffset(r.Min.X, y)
		out = append(out, img.Pix[i:i+4*r.Dx()]...)
	}
	return out
}

// TestSpinnerComparePreview renders the turning arc of Lucide's
// loader-circle beside AppKit's spokes, three frames 80 ms apart, light
// and dark, enlarged, for KIT_PREVIEW_DIR.
func TestSpinnerComparePreview(t *testing.T) {
	if os.Getenv("KIT_PREVIEW_DIR") == "" {
		t.Skip("previews only")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		k := T(c)
		c.Root().Background(k.Surface).Padding(8).Gap(12).Children(func() {
			for _, size := range []float32{13, 16} {
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "86 ms").FontSize(FontTable).TextColor(k.Label2)
					color := k.Label3
					ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
						angle := float32(p.Now().UnixMilli()%1000) / 1000 * 360
						p.Transform(ui.Rotation(angle), r.X+r.W/2, r.Y+r.H/2, func() { p.Icon(SymLoaderCircle.SVG(), r, color) })
						p.AnimationFrameRate(60)
					})
					Spinner(c, size)
					ui.Spinner(c)
					IconButton(c, SymGauge, "测速", IconButtonOptions{Busy: true, Size: 30})
				})
			}
		})
	}, 200, 90)
	tt.SetScale(4)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
		tt.Advance(time.Second)
		for i := range 3 {
			tt.Advance(80 * time.Millisecond)
			name := "spinner-compare-light-"
			if dark {
				name = "spinner-compare-dark-"
			}
			savePreview(t, tt, name+string(rune('1'+i))+".png")
		}
	}
}
