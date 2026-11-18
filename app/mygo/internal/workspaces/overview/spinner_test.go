package overview

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// previewScale is the scale of the F7 previews: the spinner is small.
const previewScale = 2

// crop is the window's pixels in r, in DIPs grown by pad, enlarged zoom
// times.
func crop(tt *ui.Tester, r ui.Rect, pad float32, zoom int) *image.RGBA {
	img := tt.Image()
	s := float32(previewScale)
	b := image.Rect(int((r.X-pad)*s), int((r.Y-pad)*s), int((r.X+r.W+pad)*s), int((r.Y+r.H+pad)*s)).Intersect(img.Bounds())
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*zoom, b.Dy()*zoom))
	for y := range out.Rect.Dy() {
		for x := range out.Rect.Dx() {
			out.SetRGBA(x, y, img.RGBAAt(b.Min.X+x/zoom, b.Min.Y+y/zoom))
		}
	}
	return out
}

// savePNG writes img where OVERVIEW_PREVIEW_DIR says.
func savePNG(t *testing.T, img image.Image, name string) {
	dir := os.Getenv("OVERVIEW_PREVIEW_DIR")
	if dir == "" {
		return
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		t.Fatal(err)
	}
}

// find is where label shows, failing the test when it does not.
func find(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("no %q in %q", label, tt.Texts())
	}
	return r
}

// holdLatency makes speed tests wait until the returned func runs.
func holdLatency(f *fixture) (release func()) {
	ch := make(chan struct{})
	var once sync.Once
	f.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		<-ch
		var results []api.LatencyResult
		for _, target := range body.Targets {
			ms := 90.0
			results = append(results, api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 1})
		}
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	return func() { once.Do(func() { close(ch) }) }
}

// TestChooserSpinnerTurns: a member under test shows the turning
// spinner in its row, the test button at the top turns as well, and both
// stop once the test is done; with less motion the row says "Testing". The
// frames go where OVERVIEW_PREVIEW_DIR says.
func TestChooserSpinnerTurns(t *testing.T) {
	doc := groupDoc()
	doc.Outbounds = append(doc.Outbounds, nodeOutbound("o_sg", "新加坡", "sg.example"))
	f := newFixtureWith(t, doc)
	f.tt.SetScale(previewScale)
	feed := &selectorFeed{}
	feed.set(api.Selector{Name: "节点选择", Type: "Selector", Now: "香港", All: []string{"香港", "日本", "美国", "新加坡"}})
	f.srv.Route("GET", "/runtime/selectors", feed.handler)
	release := holdLatency(f)
	defer release()
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "节点选择")
	hk, us := 86.0, 240.0
	f.store.Latency["o_hk"] = api.LatencyResult{ID: "o_hk", MedianConnectMs: &hk, SuccessSamples: 1}
	f.store.Latency["o_us"] = api.LatencyResult{ID: "o_us", MedianConnectMs: &us, SuccessSamples: 1}
	f.store.Latency["o_sg"] = api.LatencyResult{ID: "o_sg", Error: "Timeout"}
	f.click(t, "节点选择")
	testkit.UntilText(t, f.tt, "美国")
	f.store.MeasureLatencyOf([]string{"o_jp"})
	f.tt.Frame()
	f.tt.Advance(time.Second)
	panel := find(t, f.tt, "选择 节点选择")
	spin := find(t, f.tt, "测速中")
	button := find(t, f.tt, "测速")
	var last []byte
	for i := range 4 {
		f.tt.Advance(80 * time.Millisecond)
		row := crop(f.tt, ui.Rect{X: spin.X - 150, Y: spin.Y, W: 190, H: spin.H}, 10, 3)
		if last != nil && bytes.Equal(last, row.Pix) {
			t.Errorf("frame %d: the row's spinner did not move in 80 ms", i+1)
		}
		last = row.Pix
		n := string(rune('1' + i))
		savePNG(t, row, "19-spin-"+n+"-light.png")
		savePNG(t, crop(f.tt, button, 14, 4), "19-popup-button-"+n+"-light.png")
		if i == 0 {
			savePNG(t, crop(f.tt, panel, 0, 1), "19-popup-light.png")
		}
	}
	f.tt.SetDark(true)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	savePNG(t, crop(f.tt, panel, 0, 1), "19-popup-dark.png")
	for i := range 2 {
		f.tt.Advance(80 * time.Millisecond)
		savePNG(t, crop(f.tt, ui.Rect{X: spin.X - 150, Y: spin.Y, W: 190, H: spin.H}, 10, 3), "19-spin-"+string(rune('1'+i))+"-dark.png")
	}
	f.tt.SetDark(false)
	f.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	f.tt.Frame()
	f.tt.Advance(time.Second)
	if !slices.Contains(f.tt.Texts(), "测速中") {
		t.Errorf("with less motion the row does not say 测速中: %q", f.tt.Texts())
	}
	savePNG(t, crop(f.tt, panel, 0, 1), "19-reduce-motion-light.png")
	// The page's uptime counts on under the chooser, and the search's
	// caret blinks: the list under it stays.
	list := ui.Rect{X: panel.X, Y: panel.Y + chooserSearchH + 1, W: panel.W, H: panel.H - chooserSearchH - 1}
	a := crop(f.tt, list, 0, 1).Pix
	f.tt.Advance(300 * time.Millisecond)
	if !bytes.Equal(a, crop(f.tt, list, 0, 1).Pix) {
		t.Error("with less motion the chooser moves")
	}
	f.tt.SetPreferences(ui.Preferences{})
	release()
	testkit.Until(t, f.tt, func() bool { return !f.store.Testing("o_jp") })
	f.tt.Frame()
	f.tt.Advance(time.Second)
	a = crop(f.tt, list, 0, 1).Pix
	f.tt.Advance(300 * time.Millisecond)
	if !bytes.Equal(a, crop(f.tt, list, 0, 1).Pix) {
		t.Error("the chooser moves after the test")
	}
}

// statusDoc is a group of eight members in three regions, for the five
// ways a member's availability shows.
func statusDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	for _, n := range [][2]string{{"o_hk1", "香港 01"}, {"o_hk2", "香港 02"}, {"o_hk3", "香港 03"}, {"o_jp1", "日本 01"},
		{"o_jp2", "日本 02"}, {"o_jp3", "日本 03"}, {"o_us1", "美国 01"}, {"o_us2", "美国 02"}} {
		doc.Outbounds = append(doc.Outbounds, nodeOutbound(n[0], n[1], n[0]+".example"))
	}
	return doc
}

// statusSelector is statusDoc's group as the core reports it: Hong Kong
// 03 down, Japan 02 suspect, US 02 suspect though the app's test failed,
// Hong Kong 01 down though the app measured it, the others up.
func statusSelector() api.Selector {
	names := []string{"香港 01", "香港 02", "香港 03", "日本 01", "日本 02", "日本 03", "美国 01", "美国 02"}
	states := map[string]string{"香港 01": "down", "香港 03": "down", "日本 02": "suspect", "美国 02": "suspect"}
	var members []api.SelectorMember
	for _, n := range names {
		state := states[n]
		if state == "" {
			state = "up"
		}
		members = append(members, api.SelectorMember{Name: n, State: state, SuccessRate: -1})
	}
	return api.Selector{Name: "节点选择", Type: "Selector", Mode: "manual", Now: "香港 01", All: names, Members: members}
}

// statusLatency sets the app's tests of statusDoc: fast, slow and
// middling times, a timeout and a failure; Hong Kong 03, Japan 02 and
// Japan 03 untested.
func statusLatency(results map[string]api.LatencyResult) {
	ms := func(v float64) api.LatencyResult { return api.LatencyResult{MedianConnectMs: &v, SuccessSamples: 1} }
	results["o_hk1"] = ms(86)
	results["o_hk2"] = api.LatencyResult{Error: "Timeout"}
	results["o_jp1"] = ms(240)
	results["o_us1"] = ms(380)
	results["o_us2"] = api.LatencyResult{Error: "connection refused"}
}

// TestChooserMemberStatus: without status dots, the chooser's members
// show their availability in the latency tag alone: times in their tone,
// "Timeout", "Unavailable" from a failed test or the core's down,
// "Unconfirmed" from its suspect, the app's test over the core's state,
// nothing untested.
// The sheet of all groups shows the same. Light, dark and narrow go where
// OVERVIEW_PREVIEW_DIR says.
func TestChooserMemberStatus(t *testing.T) {
	f := newFixtureWith(t, statusDoc())
	f.tt.SetScale(previewScale)
	feed := &selectorFeed{}
	feed.set(statusSelector())
	f.srv.Route("GET", "/runtime/selectors", feed.handler)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "节点选择")
	statusLatency(f.store.Latency)
	f.click(t, "节点选择")
	testkit.UntilText(t, f.tt, "美国 02")
	f.tt.Advance(time.Second)
	texts := f.tt.Texts()
	count := func(s string) int {
		n := 0
		for _, x := range texts {
			if x == s {
				n++
			}
		}
		return n
	}
	// The card under the chooser shows 86 ms as well, the choice's; the
	// page's empty metrics show dashes of their own.
	for want, n := range map[string]int{"86 ms": 2, "240 ms": 1, "380 ms": 1, "超时": 1, "不可用": 2, "待确认": 1} {
		if got := count(want); got != n {
			t.Errorf("%q shows %d times, want %d: %q", want, got, n, texts)
		}
	}
	panel := find(t, f.tt, "选择 节点选择")
	savePNG(t, crop(f.tt, panel, 0, 1), "22-popup-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	savePNG(t, crop(f.tt, panel, 0, 1), "22-popup-dark.png")
	f.tt.SetDark(false)
	f.tt.SetSize(520, 860)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	savePreview(t, f.tt, "22-popup-narrow-light.png")
	f.tt.SetSize(1320, 860)
	f.w.selector.open = false
	f.w.selector.sheet.Open = true
	f.w.selector.expanded = map[string]bool{"节点选择": true}
	f.tt.Frame()
	f.tt.Advance(time.Second)
	if !f.tt.HasText("待确认") || !f.tt.HasText("380 ms") {
		t.Errorf("sheet tags: %q", f.tt.Texts())
	}
	savePreview(t, f.tt, "22-overview-sheet-light.png")
}
