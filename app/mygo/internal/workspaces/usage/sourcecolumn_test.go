package usage

import (
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const longDevice = "书房里那台名字特别特别长的 Mac mini 设备"

// sourceUsage answers GET /usage with source groups, or the process ×
// source groups, or one domain for a drill.
func sourceUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp := api.UsageResponse{From: 1, To: 2, Limit: 50, Connected: true, Facets: map[string][]api.UsageFacet{
		"source": {
			{Value: "10.0.0.2", UsageCounters: counters(1, 8<<20)},
			{Value: "10.0.0.4", UsageCounters: counters(1, 4<<20)},
			{Value: "172.18.0.1", UsageCounters: counters(1, 2<<20)},
			{Value: "192.168.1.5", UsageCounters: counters(1, 1<<20)},
			{Value: "10.0.0.3", UsageCounters: counters(1, 512<<10)},
		},
	}}
	switch {
	case q.Get("group") == "source":
		for i, ip := range []string{"10.0.0.2", "10.0.0.4", "172.18.0.1", "192.168.1.5", "10.0.0.3"} {
			resp.Rows = append(resp.Rows, api.UsageGroup{Key: "s" + ip, Labels: []string{ip}, UsageCounters: counters(uint64(1024*(i+1)), uint64(8<<20)>>i)})
		}
	case q.Get("group") == "process" && q.Get("secondary") == "source":
		resp.Rows = []api.UsageGroup{
			{Key: "p1", Labels: []string{"/usr/bin/curl", "10.0.0.2"}, UsageCounters: counters(10, 4096)},
			{Key: "p2", Labels: []string{"/usr/bin/curl", "10.0.0.3"}, UsageCounters: counters(10, 2048)},
		}
	default:
		resp.Rows = []api.UsageGroup{{Key: "d1", Labels: []string{"example.com"}, UsageCounters: counters(10, 4096)}}
	}
	resp.Groups = len(resp.Rows)
	for _, row := range resp.Rows {
		resp.Totals.Upload += row.Upload
		resp.Totals.Download += row.Download
		resp.Totals.Connections += row.Connections
	}
	resp.Trend = []api.UsageTrend{{Time: 1_700_000_000_000, UsageCounters: counters(1, 100)}, {Time: 1_700_000_060_000, UsageCounters: counters(5, 400)}}
	testkit.JSON(resp)(w, r)
}

type sourceFixture struct {
	w  *Workspace
	s  *store.Store
	tt *ui.Tester
}

// newSourceFixture is the usage page over a document with a named device,
// a device with a long name and a TUN inbound at 172.18.0.1/30, the host's
// interface being 192.168.1.5.
func newSourceFixture(t *testing.T, width, height int) *sourceFixture {
	t.Helper()
	srv := testkit.NewServer(t)
	doc := model.EmptyDoc("darwin")
	tun := model.MakeInbound("tun", "tun-in", "darwin", false)
	tun.Enabled = true
	doc.Inbounds = append(doc.Inbounds, tun)
	doc.Route.Devices = []*model.RouteDevice{
		{ID: "d1", Name: "客厅 Apple TV", Addresses: []string{"10.0.0.2"}},
		{ID: "d2", Name: longDevice, Addresses: []string{"10.0.0.4"}},
	}
	raw, _ := json.Marshal(doc)
	srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	srv.Route("GET", "/usage", sourceUsage)
	e, _ := testkit.NewEnv(srv)
	s := store.New(e)
	f := &sourceFixture{w: New(e, s), s: s}
	f.w.SetActive(true)
	t.Cleanup(func() { f.w.SetActive(false) })
	s.Load()
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		e.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		f.w.View(c)
	}, width, height)
	testkit.Until(t, f.tt, func() bool { return s.Loaded && f.tt.HasText("10.0.0.3") })
	s.LocalAddresses = []string{"192.168.1.5/32"}
	testkit.UntilText(t, f.tt, "客厅 Apple TV")
	return f
}

// open selects the row of value, as a click on it does.
func (f *sourceFixture) open(t *testing.T, value string) {
	t.Helper()
	testkit.Until(t, f.tt, func() bool {
		return f.w.data != nil && len(f.w.data.Rows) > 0 && f.w.data.Rows[0].Label(0) != "" && f.tt.HasText(value)
	})
	for _, row := range f.w.data.Rows {
		if row.Label(0) == value {
			f.w.openRow(row)
			f.tt.Frame()
			return
		}
	}
	t.Fatalf("no row %q", value)
}

// count is how many shown texts are s.
func count(tt *ui.Tester, s string) int {
	n := 0
	for _, text := range tt.Texts() {
		if text == s {
			n++
		}
	}
	return n
}

func TestSourceColumnShowsDeviceNames(t *testing.T) {
	f := newSourceFixture(t, 1320, 760)
	tt := f.tt
	// Each name shows in the filter and in the list row of its address;
	// the TUN interface's address and the host's are Local; an unnamed
	// address shows alone.
	testkit.Until(t, tt, func() bool {
		return count(tt, "客厅 Apple TV") == 2 && count(tt, longDevice) == 2 && count(tt, "本机") == 4
	})
	// The filter option (its label and its text) and the row.
	if n := count(tt, "10.0.0.3"); n != 3 {
		t.Errorf("unnamed address shown %d times: %q", n, tt.Texts())
	}

	// The inspector names the address; the value stays the IP.
	f.open(t, "10.0.0.2")
	testkit.UntilText(t, tt, "交叉明细")
	testkit.Until(t, tt, func() bool { return count(tt, "客厅 Apple TV") == 3 })
	if f.w.selected == nil || f.w.selected.Label(0) != "10.0.0.2" {
		t.Fatalf("selected %+v", f.w.selected)
	}
	if v := f.w.rowValues(*f.w.selected); v["source"] != "10.0.0.2" {
		t.Errorf("row values %v", v)
	}

	// A rename shows everywhere at once.
	if err := f.s.Update(func(d *model.Doc) { d.Route.Devices[0].Name = "卧室" }); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, tt, func() bool { return count(tt, "卧室") == 3 })
	for _, text := range tt.Texts() {
		if strings.Contains(text, "客厅") {
			t.Errorf("old name still shown: %q", text)
		}
	}
	// The names are kept while nothing changes.
	names := f.w.sourceNames()
	tt.Frame()
	if f.w.sourceNames() != names {
		t.Error("names rebuilt without a change")
	}
}

func TestSecondarySourceColumnAndDetailNames(t *testing.T) {
	f := newSourceFixture(t, 1320, 760)
	tt := f.tt
	f.w.changeGroup("process")
	f.w.secondary = "source"
	f.w.resetPage()
	testkit.Until(t, tt, func() bool { return tt.HasText("curl") && count(tt, "客厅 Apple TV") == 2 })

	// Cross details by source name their addresses too.
	f.w.group, f.w.secondary = "rootDomain", ""
	f.w.resetPage()
	testkit.UntilText(t, tt, "example.com")
	f.open(t, "example.com")
	testkit.UntilText(t, tt, "交叉明细")
	f.w.detailGroup, f.w.detail = "source", nil
	testkit.UntilText(t, tt, "筛选 10.0.0.2")
	testkit.Until(t, tt, func() bool { return tt.HasText("10.0.0.2  客厅 Apple TV") })
}

// TestSourceColumnScreens writes the acceptance frames of review item 18
// where MON_PREVIEW_DIR says.
func TestSourceColumnScreens(t *testing.T) {
	dir := os.Getenv("MON_PREVIEW_DIR")
	if dir == "" {
		t.Skip("MON_PREVIEW_DIR is not set")
	}
	shot := func(tt *ui.Tester, name string, dark bool) {
		tt.SetDark(dark)
		tt.Advance(time.Second)
		tt.Frame()
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if err := png.Encode(out, tt.Image()); err != nil {
			t.Fatal(err)
		}
		tt.SetDark(false)
		tt.Advance(time.Second)
	}
	f := newSourceFixture(t, 1320, 760)
	testkit.Until(t, f.tt, func() bool { return count(f.tt, longDevice) == 2 })
	f.open(t, "10.0.0.4")
	testkit.UntilText(t, f.tt, "交叉明细")
	testkit.UntilText(t, f.tt, "example.com")
	shot(f.tt, "18-usage-light.png", false)
	shot(f.tt, "18-usage-dark.png", true)

	n := newSourceFixture(t, 760, 760)
	testkit.Until(t, n.tt, func() bool { return count(n.tt, longDevice) >= 1 })
	shot(n.tt, "18-usage-narrow-light.png", false)
	shot(n.tt, "18-usage-narrow-dark.png", true)
}
