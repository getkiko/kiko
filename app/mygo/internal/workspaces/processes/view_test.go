package processes

import (
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

type fixture struct {
	srv  *testkit.Server
	env  *env.Env
	w    *Workspace
	tt   *ui.Tester
	mu   sync.Mutex
	body []map[string]any
}

func snapshotProcesses() []Process {
	return []Process{
		proc(1, 0, "launchd", func(p *Process) { p.User, p.System, p.Mine = "root", true, false }),
		proc(10, 1, "goland", app("/Applications/GoLand.app", "GoLand"), cpu(30), func(p *Process) {
			p.Path = "/Applications/GoLand.app/Contents/MacOS/goland"
			p.Command = "/Applications/GoLand.app/Contents/MacOS/goland --wait"
		}),
		proc(11, 10, "fsnotifier", app("/Applications/GoLand.app", "GoLand"), cpu(2)),
		proc(20, 1, "worker", cpu(90), func(p *Process) { p.Path = "/usr/bin/worker"; p.Command = "/usr/bin/worker -v" }),
		proc(42, 1, "target", cpu(0)),
		proc(142, 1, "other42", cpu(4)),
	}
}

func newFixture(t *testing.T, processHandler ...http.HandlerFunc) *fixture {
	t.Helper()
	f := &fixture{srv: testkit.NewServer(t)}
	var captured atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testkit.JSON(api.ProcessSnapshot{CapturedAt: 1_700_000_000_000 + captured.Add(1000), Processes: snapshotProcesses(),
			Total: 6, Threads: 7206, OS: "darwin", CPU: f64(16), Memory: f64(38.58 * (1 << 30)), Warnings: []string{}})(w, r)
	})
	if len(processHandler) > 0 {
		handler = processHandler[0]
	}
	f.srv.Route("GET", "/process-monitor", handler)
	f.srv.Route("POST", "/process-monitor/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/signal") {
			var body map[string]any
			_ = testkit.Body(r, &body)
			f.mu.Lock()
			f.body = append(f.body, body)
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv.Route("GET", "/process-monitor/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	f.env, _ = testkit.NewEnv(f.srv)
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

func f64(v float64) *float64 { return &v }

func (f *fixture) requested(t *testing.T, part string) {
	t.Helper()
	testkit.Until(t, f.tt, func() bool {
		for _, r := range f.srv.Requests() {
			if strings.Contains(r, part) {
				return true
			}
		}
		return false
	})
}

func TestProcessTableGroupsSortsAndInspects(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "GoLand")
	for _, text := range []string{"进程", "6 个进程 · 7,206 个线程", "App", "我的进程", "worker", "显示 6 个进程，共 6 个", "16%"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q: %q", text, f.tt.Texts())
		}
	}
	if f.tt.HasText("fsnotifier") {
		t.Error("collapsed group shows its members")
	}
	if err := f.tt.Click("展开 GoLand"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "fsnotifier")
	if err := f.tt.Click("按内存排序"); err != nil {
		t.Fatal(err)
	}
	if f.w.sortKey != SortMemory || !f.w.desc {
		t.Errorf("sort %s desc %v", f.w.sortKey, f.w.desc)
	}
	if err := f.tt.Click("按名称排序"); err != nil {
		t.Fatal(err)
	}
	if f.w.sortKey != SortName || f.w.desc {
		t.Errorf("sort %s desc %v", f.w.sortKey, f.w.desc)
	}
	if err := f.tt.Click("worker"); err != nil {
		t.Fatal(err)
	}
	if err := f.tt.Click("显示详情"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "详细信息")
	for _, text := range []string{"PID 20 · user", "父进程", "launchd", "命令", "路径", "/usr/bin/"} {
		if !f.tt.HasText(text) {
			t.Errorf("inspector lacks %q", text)
		}
	}
	// History grows with each sample.
	testkit.Until(t, f.tt, func() bool { return len(f.w.history[f.w.selection.identity]) >= 2 })
	savePNG(t, f.tt, "processes-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	savePNG(t, f.tt, "processes-dark.png")
	f.tt.SetDark(false)
	if err := f.tt.Click("显示进程文件位置"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "POST /api/v1/process-monitor/20/reveal?startedAt=2000")
	if err := f.tt.Click("launchd"); err != nil {
		t.Fatal(err)
	}
	if f.w.selection == nil || f.w.selection.original.PID != 1 {
		t.Errorf("parent not selected: %+v", f.w.selection)
	}
}

func TestProcessQuitConfirms(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "worker")
	if err := f.tt.Click("worker"); err != nil {
		t.Fatal(err)
	}
	if err := f.tt.Click("结束选中进程"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "退出进程")
	if err := f.tt.Click("退出"); err != nil {
		t.Fatal(err)
	}
	f.requested(t, "POST /api/v1/process-monitor/20/signal")
	testkit.Until(t, f.tt, func() bool { return !f.w.confirm.Open })
	f.mu.Lock()
	body := f.body[0]
	f.mu.Unlock()
	if body["force"] != false || body["startedAt"] != float64(2000) {
		t.Errorf("signal body %v", body)
	}
}

func TestProcessOpenSearchesPID(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "worker")
	f.w.Open(42)
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("worker") })
	if !f.tt.HasText("target") || f.tt.HasText("other42") || f.w.query != "42" || f.w.grouped {
		t.Errorf("Open(42): query %q texts %q", f.w.query, f.tt.Texts())
	}
	if err := f.tt.Click("清除搜索"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "worker")
}

func TestProcessFiltersAndErrors(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "worker")
	if err := f.tt.Click("系统"); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("worker") && f.tt.HasText("launchd") })
	if err := f.tt.Click("全部"); err != nil {
		t.Fatal(err)
	}
	f.w.query = "nothing-matches"
	testkit.UntilText(t, f.tt, "没有匹配的进程")

	g := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "读取进程失败", http.StatusInternalServerError)
	})
	testkit.UntilText(t, g.tt, "读取进程失败")
	if !g.tt.HasText("等待重新采样…") {
		t.Errorf("no waiting text: %q", g.tt.Texts())
	}
}

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
