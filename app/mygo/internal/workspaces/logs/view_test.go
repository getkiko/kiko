package logs

import (
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

type fixture struct {
	srv   *testkit.Server
	env   *env.Env
	shell *testkit.Shell
	w     *Workspace
	tt    *ui.Tester
	lines chan string
}

// newFixture serves a core log stream whose lines the test sends.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{srv: testkit.NewServer(t), lines: make(chan string, 16)}
	f.srv.Route("GET", "/logs/core", func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		fmt.Fprintln(w, `{"defaultLevel":4}`)
		fmt.Fprintln(w, `{"reset":true,"messages":[{"level":"INFO","message":"\u001b[36mINFO\u001b[0m router started"},{"level":"DEBUG","message":"debug detail"}]}`)
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case line := <-f.lines:
				fmt.Fprintln(w, line)
				flusher.Flush()
			}
		}
	})
	f.srv.Route("DELETE", "/logs", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.env, f.shell = testkit.NewEnv(f.srv)
	f.env.Server.Start()
	t.Cleanup(f.env.Server.Stop)
	f.w = New(f.env, nil)
	f.w.SetActive(true)
	t.Cleanup(func() { f.w.SetActive(false) })
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		f.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		f.w.View(c)
	}, 1200, 640)
	return f
}

func TestCoreLogsStreamFilterAndClear(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "INFO router started")
	if f.tt.HasText("debug detail") {
		t.Error("debug line shown at the default INFO level")
	}
	f.lines <- `{"messages":[{"level":"WARN","message":"WARN disk almost full"}]}`
	testkit.UntilText(t, f.tt, "WARN disk almost full")
	savePNG(t, f.tt, "logs-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	savePNG(t, f.tt, "logs-dark.png")
	f.tt.SetDark(false)

	// A drag selects across lines; Cmd+C copies them joined by newlines.
	first, ok1 := findLine(f.tt, "INFO router started")
	last, ok2 := findLine(f.tt, "WARN disk almost full")
	if !ok1 || !ok2 {
		t.Fatal("log lines not found")
	}
	f.tt.Press(first.X+1, first.Y+first.H/2)
	f.tt.Move(last.X+last.W-1, last.Y+last.H/2)
	f.tt.Release(last.X+last.W-1, last.Y+last.H/2)
	f.tt.Key(ui.Cmd, ui.KeyC)
	if clip := f.tt.Clipboard(); !strings.Contains(clip, "router started") || !strings.Contains(clip, "disk almost full") || !strings.Contains(clip, "\n") {
		t.Errorf("cross-line selection copied %q", clip)
	}

	f.w.level = 5
	testkit.UntilText(t, f.tt, "debug detail")
	f.w.query = "disk"
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("debug detail") })

	if err := f.tt.Click("暂停日志"); err != nil {
		t.Fatal(err)
	}
	f.w.query = ""
	f.lines <- `{"messages":[{"level":"ERROR","message":"ERROR while paused"}]}`
	testkit.Until(t, f.tt, func() bool { return len(f.w.entries) == 4 })
	if f.tt.HasText("ERROR while paused") {
		t.Error("paused view shows new lines")
	}
	if err := f.tt.Click("继续日志"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "ERROR while paused")

	f.srv.Route("DELETE", "/logs/core", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if err := f.tt.Click("日志操作"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if err := f.tt.Click("复制到剪贴板"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if clip := f.tt.Clipboard(); !strings.Contains(clip, "INFO router started") || strings.Contains(clip, "\x1b") {
		t.Errorf("clipboard %q", clip)
	}
	if err := f.tt.Click("日志操作"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if err := f.tt.Click("清空日志"); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool {
		for _, r := range f.srv.Requests() {
			if r == "DELETE /api/v1/logs/core" {
				return true
			}
		}
		return false
	})
}

func TestAppLogsFollowEvents(t *testing.T) {
	f := newFixture(t)
	testkit.Until(t, f.tt, func() bool { return f.env.Server.Connected })
	if err := f.tt.Click("应用"); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, f.tt, "等待日志…")
	f.srv.Emit("log", "[kiko] 配置应用完成")
	f.srv.Emit("log", "INFO[0000] core output")
	testkit.UntilText(t, f.tt, "[kiko] 配置应用完成")
	if f.tt.HasText("INFO[0000] core output") {
		t.Error("core output in the application source")
	}
	if f.w.cancel != nil {
		t.Error("core stream runs for the application source")
	}
	if err := f.tt.Click("日志操作"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if err := f.tt.Click("清空日志"); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool { return len(f.env.Server.Logs) == 0 })
	testkit.UntilText(t, f.tt, "等待日志…")
}

func TestCoreLogStreamError(t *testing.T) {
	srv := testkit.NewServer(t)
	srv.Route("GET", "/logs/core", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "核心未运行", http.StatusServiceUnavailable)
	})
	e, _ := testkit.NewEnv(srv)
	w := New(e, nil)
	w.SetActive(true)
	t.Cleanup(func() { w.SetActive(false) })
	tt := ui.NewTester(func(c *ui.Context) { kit.Apply(c); e.Drain(c); w.View(c) }, 900, 500)
	testkit.UntilText(t, tt, "核心未运行")
	if !tt.HasText("等待日志…") {
		t.Errorf("texts %q", tt.Texts())
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

// findLine is the box of the shown log line containing s.
func findLine(tt *ui.Tester, s string) (ui.Rect, bool) {
	for _, text := range tt.Texts() {
		if strings.Contains(text, s) {
			return tt.Find(text)
		}
	}
	return ui.Rect{}, false
}
