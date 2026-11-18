package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// SegmentedFill spans its container, its segments sharing the width
// equally whatever their labels' lengths; Segmented keeps its own width.
func TestSegmentedFillSharesWidth(t *testing.T) {
	selected := 0
	fill := true
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(20).Children(func() {
			segments := []Segment{{Label: "短"}, {Label: "较长的一项"}, {Label: "中等长"}}
			if fill {
				SegmentedFill(c, "seg", &selected, segments...)
			} else {
				Segmented(c, "seg", &selected, segments...)
			}
		})
	}, 600, 100)
	tt.Frame()
	tt.Frame()
	center := func(label string) float32 {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("no %s in %q", label, tt.Texts())
		}
		return r.X + r.W/2
	}
	// The track spans 20..580; each segment is a third of it, less the
	// track's padding.
	for i, label := range []string{"短", "较长的一项", "中等长"} {
		want := 20 + 2 + (560-4)*(float32(i)+0.5)/3
		if got := center(label); got < want-2 || got > want+2 {
			t.Errorf("%s centered at %.1f, want %.1f", label, got, want)
		}
	}
	if err := tt.Click("中等长"); err != nil || selected != 2 {
		t.Fatalf("click: %v, selected %d", err, selected)
	}
	fill = false
	tt.Frame()
	tt.Frame()
	if got := center("中等长"); got > 400 {
		t.Errorf("Segmented filled the row: last label at %.1f", got)
	}
}
