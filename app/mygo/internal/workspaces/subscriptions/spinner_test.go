package subscriptions

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

// savePNG writes img where SUBS_PREVIEW_DIR says.
func savePNG(t *testing.T, img image.Image, name string) {
	dir := os.Getenv("SUBS_PREVIEW_DIR")
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

// TestCardSpinnerTurns: a manual outbound's card shows the turning
// spinner while its test runs and takes no click on it, "Test all" turns
// while all are tested, and nothing asks for frames once the tests are
// done. The frames go where SUBS_PREVIEW_DIR says.
func TestCardSpinnerTurns(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.tt.SetScale(previewScale)
	release := holdLatency(f)
	defer release()
	f.click("测速 香港 SS")
	testkit.Until(t, f.tt, func() bool { return f.store.Testing("out_ss") })
	f.tt.Frame()
	f.tt.Advance(time.Second)
	card := find(t, f.tt, "编辑 香港 SS")
	var last []byte
	for i := range 3 {
		f.tt.Advance(80 * time.Millisecond)
		img := crop(f.tt, card, 8, 1)
		if last != nil && bytes.Equal(last, img.Pix) {
			t.Errorf("frame %d: the card's spinner did not move in 80 ms", i+1)
		}
		last = img.Pix
		savePNG(t, img, "19-card-"+string(rune('1'+i))+"-light.png")
	}
	// The spinner takes the click: no second test starts.
	_ = f.tt.Click("测速 香港 SS")
	f.tt.Frame()
	f.store.MeasureLatency()
	f.tt.Frame()
	f.tt.Advance(time.Second)
	all := find(t, f.tt, "全部测速")
	for i := range 2 {
		f.tt.Advance(80 * time.Millisecond)
		savePNG(t, crop(f.tt, all, 14, 4), "19-all-"+string(rune('1'+i))+"-light.png")
	}
	f.tt.SetDark(true)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	savePNG(t, f.tt.Image(), "19-components-testing-dark.png")
	f.tt.SetDark(false)
	f.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	f.tt.Frame()
	f.tt.Advance(time.Second)
	if !slices.Contains(f.tt.Texts(), "测速中") {
		t.Errorf("with less motion the card does not say 测速中: %q", f.tt.Texts())
	}
	savePNG(t, crop(f.tt, card, 8, 1), "19-card-reduce-motion-light.png")
	f.tt.SetPreferences(ui.Preferences{})
	release()
	testkit.Until(t, f.tt, func() bool { return !f.store.LatencyProgress.Running && !f.store.Testing("out_ss") })
	f.tt.Frame()
	f.tt.Advance(time.Second)
	if f.tt.FramePending() {
		t.Error("the page asks for frames after the tests")
	}
}

// statusDoc is a Selector of eight members in three regions, for the
// five ways a member's availability shows.
func statusDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	sel := outboundOf("out_sel", "selector", "节点选择", model.Fields{})
	for _, n := range [][2]string{{"o_hk1", "香港 01"}, {"o_hk2", "香港 02"}, {"o_hk3", "香港 03"}, {"o_jp1", "日本 01"},
		{"o_jp2", "日本 02"}, {"o_jp3", "日本 03"}, {"o_us1", "美国 01"}, {"o_us2", "美国 02"}} {
		doc.Outbounds = append(doc.Outbounds, outboundOf(n[0], "trojan", n[1], model.Fields{"server": n[0] + ".example", "server_port": 443.0, "password": "p"}))
		sel.Members = append(sel.Members, n[0])
	}
	doc.Outbounds = append(doc.Outbounds, sel)
	return doc
}

// statusSelector is statusDoc's group as the core reports it: Hong Kong 03
// down, Japan 02 suspect, US 02 suspect though the app's test failed,
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

// TestGroupSheetMemberStatus: without status dots, the members of the
// group's sheet show their availability in the latency tag alone: times
// in their tone, "Timeout", "Unavailable" from a failed test or the core's
// down, "Unconfirmed"
// from its suspect, the app's test over the core's state, nothing
// untested; a member down keeps its dimmed name. Light, dark and narrow
// go where SUBS_PREVIEW_DIR says.
func TestGroupSheetMemberStatus(t *testing.T) {
	f := newFixture(t, statusDoc())
	f.tt.SetScale(previewScale)
	ms := func(v float64) api.LatencyResult { return api.LatencyResult{MedianConnectMs: &v, SuccessSamples: 1} }
	f.store.Latency["o_hk1"] = ms(86)
	f.store.Latency["o_hk2"] = api.LatencyResult{Error: "Timeout"}
	f.store.Latency["o_jp1"] = ms(240)
	f.store.Latency["o_us1"] = ms(380)
	f.store.Latency["o_us2"] = api.LatencyResult{Error: "connection refused"}
	runCore(f, statusSelector())
	f.w.outbounds.sheet.show("out_sel")
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.tt.Frame()
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
	// The members' cards on the page show their tests as well, and the
	// group's card its choice's 86 ms; the core's states show in the
	// sheet alone.
	for want, n := range map[string]int{"86 ms": 3, "240 ms": 2, "380 ms": 2, "超时": 2, "不可用": 3, "待确认": 1, "连接失败": 0, "失败": 0} {
		if got := count(want); got != n {
			t.Errorf("%q shows %d times, want %d: %q", want, got, n, texts)
		}
	}
	f.save("22-components-sheet-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.save("22-components-sheet-dark.png")
	f.tt.SetDark(false)
	f.tt.SetSize(560, 900)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.save("22-components-sheet-narrow-light.png")
}
