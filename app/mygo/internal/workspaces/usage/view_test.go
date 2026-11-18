package usage

import (
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func counters(up, down uint64) api.UsageCounters {
	return api.UsageCounters{Upload: up, Download: down, Connections: 3}
}

// fakeUsage answers GET /usage from the query's group.
func fakeUsage(empty *bool, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		none := *empty
		mu.Unlock()
		q := r.URL.Query()
		resp := api.UsageResponse{From: 1, To: 2, Limit: 50, Connected: true, Facets: map[string][]api.UsageFacet{}}
		if !none {
			switch q.Get("group") {
			case "source":
				resp.Rows = []api.UsageGroup{
					{Key: "s1", Labels: []string{"10.0.0.2"}, UsageCounters: counters(2048, 8<<20)},
					{Key: "s2", Labels: []string{"10.0.0.3"}, UsageCounters: counters(1024, 1<<20)},
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
			resp.Facets["outbound"] = []api.UsageFacet{{Value: "direct", UsageCounters: counters(5, 5000)}}
		}
		testkit.JSON(resp)(w, r)
	}
}

type fixture struct {
	srv   *testkit.Server
	env   *env.Env
	shell *testkit.Shell
	w     *Workspace
	tt    *ui.Tester
	empty bool
	mu    sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{srv: testkit.NewServer(t)}
	f.srv.Route("GET", "/usage", fakeUsage(&f.empty, &f.mu))
	f.env, f.shell = testkit.NewEnv(f.srv)
	f.w = New(f.env, nil)
	f.w.SetActive(true)
	t.Cleanup(func() { f.w.SetActive(false) })
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		f.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		f.w.View(c)
	}, 1320, 760)
	return f
}

func (f *fixture) requested(t *testing.T, parts ...string) {
	t.Helper()
	testkit.Until(t, f.tt, func() bool {
		for _, r := range f.srv.Requests() {
			ok := true
			for _, p := range parts {
				if !strings.Contains(r, p) {
					ok = false
				}
			}
			if ok {
				return true
			}
		}
		return false
	})
}

func TestUsageListInspectorAndDrill(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "10.0.0.2")
	for _, text := range []string{"用量", "总用量", "下载用量", "上传用量", "出站", "direct"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q", text)
		}
	}
	savePNG(t, f.tt, "usage-light.png")
	if err := f.tt.Click("10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "group=rootDomain", "filter.source=10.0.0.2", "sort=total")
	testkit.UntilText(t, f.tt, "交叉明细")
	testkit.UntilText(t, f.tt, "example.com")
	f.tt.SetDark(true)
	f.tt.Frame()
	savePNG(t, f.tt, "usage-dark.png")
	f.tt.SetDark(false)
	if err := f.tt.Click("筛选 example.com"); err != nil {
		t.Fatal(err)
	}
	if f.w.group != "rootDomain" || !f.w.filters.Has("source", "10.0.0.2") || !f.w.filters.Has("rootDomain", "example.com") || f.w.selected != nil {
		t.Errorf("drill: group %q filters %v", f.w.group, f.w.filters)
	}
	f.requested(t, "group=rootDomain", "filter.rootDomain=example.com", "filter.source=10.0.0.2")
	if !f.w.sidebar {
		t.Error("drill left the filter pane closed")
	}
	if err := f.tt.Click("清除"); err != nil {
		t.Fatal(err)
	}
	if len(f.w.filters) != 0 {
		t.Errorf("filters not cleared: %v", f.w.filters)
	}
}

func TestUsageFilterSortAndPaging(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "direct")
	if err := f.tt.Click("direct"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "filter.outbound=direct")
	if err := f.tt.Click("按下载排序"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "sort=download", "order=desc")
	if err := f.tt.Click("按下载排序"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "sort=download", "order=asc")
	if err := f.tt.Click("按源地址排序"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "sort=label", "order=asc")
}

func TestUsageClearAndExport(t *testing.T) {
	f := newFixture(t)
	var cleared, exported sync.WaitGroup
	cleared.Add(1)
	exported.Add(1)
	f.srv.Route("DELETE", "/usage/history", func(w http.ResponseWriter, r *http.Request) {
		cleared.Done()
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv.Route("GET", "/usage/export", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("time,upload\n1,2\n"))
	})
	testkit.UntilText(t, f.tt, "10.0.0.2")
	if err := f.tt.Click("清空用量历史"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "清空用量历史？")
	if err := f.tt.Click("清空历史"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "DELETE /api/v1/usage/history")
	testkit.Until(t, f.tt, func() bool { return !f.w.confirm.Open })

	if err := f.tt.Click("导出用量 CSV"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if err := f.tt.Click("全部保留明细 CSV"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "GET /api/v1/usage/export?scope=all")
	testkit.Until(t, f.tt, func() bool { return !f.w.working })
	var saved []byte
	for name, data := range f.shell.Saved {
		if strings.HasPrefix(name, "usage-details-") && strings.HasSuffix(name, ".csv") {
			saved = data
		}
	}
	if string(saved) != "time,upload\n1,2\n" {
		t.Errorf("saved %q", f.shell.Saved)
	}
}

func TestUsageEmptyAndError(t *testing.T) {
	f := newFixture(t)
	f.mu.Lock()
	f.empty = true
	f.mu.Unlock()
	testkit.UntilText(t, f.tt, "尚无用量记录")
	f.srv.Route("GET", "/usage", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"用量暂时读取失败"}`, http.StatusInternalServerError)
	})
	f.w.refresh()
	testkit.UntilText(t, f.tt, "用量暂时读取失败")
	f.w.rng = "custom"
	f.w.start, f.w.end = "2026-10-02T10:00", "2026-10-01T10:00"
	f.w.startMS, f.w.endMS = parseLocal(f.w.start), parseLocal(f.w.end)
	testkit.UntilText(t, f.tt, "请选择结束晚于开始的有效时间范围")
}

// savePNG writes the frame to MON_PREVIEW_DIR, for reviewing the look.
func savePNG(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("MON_PREVIEW_DIR")
	if dir == "" {
		return
	}
	file, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestUsageFilterPaneResize(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "direct")
	handle, ok := f.tt.Find(kit.FilterResizeLabel)
	if !ok {
		t.Fatal("no handle")
	}
	x, y := handle.X+handle.W/2, handle.Y+handle.H/2
	f.tt.Press(x, y)
	f.tt.Move(x+40, y)
	f.tt.Move(x+80, y)
	f.tt.Release(x+80, y)
	f.tt.Frame()
	want := float32(kit.MonitorFilterW + 80)
	var saved float32
	if f.w.filterWidth.Width != want || !f.env.Prefs.Load(prefFilterWidth, &saved) || saved != want {
		t.Fatalf("width %v saved %v", f.w.filterWidth.Width, saved)
	}
	if again := New(f.env, nil); again.filterWidth.Width != want {
		t.Fatalf("restored %v", again.filterWidth.Width)
	}
	savePNG(t, f.tt, "usage-filter-resized.png")
}
