package scripts

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

type logsHarness struct {
	t    *testing.T
	srv  *testkit.Server
	env  *env.Env
	page *LogsPage
	tt   *ui.Tester
}

func newLogsHarness(t *testing.T, width, height int, setup func(*testkit.Server)) *logsHarness {
	t.Helper()
	h := &logsHarness{t: t, srv: testkit.NewServer(t)}
	if setup != nil {
		setup(h.srv)
	}
	h.env, _ = testkit.NewEnv(h.srv)
	h.page = NewLogsPage(h.env)
	h.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		h.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		h.page.View(c)
	}, width, height)
	h.page.SetActive(true)
	t.Cleanup(func() { h.page.SetActive(false) })
	return h
}

func slLog(script, phase, level, message string, at int64) api.ScriptLogEntry {
	return api.ScriptLogEntry{Script: script, Phase: phase, Level: level, Message: message, TimeMS: at}
}

// slLogFixture pushes a few lines of each level.
func slLogFixture(srv *testkit.Server) {
	base := time.Date(2026, 11, 3, 8, 12, 41, 250_000_000, time.Local).UnixMilli()
	srv.PushScriptLog(slLog("weather", "http-request", api.ScriptLogLevelLog, "fetching forecast", base))
	srv.PushScriptLog(slLog("weather", "http-response", api.ScriptLogLevelInfo, "forecast updated: 21°C", base+120))
	srv.PushScriptLog(slLog("ads", "http-request", api.ScriptLogLevelWarn, "$persistentStore.write: value larger than 1 MiB", base+300))
	srv.PushScriptLog(slLog("ads", "http-response", api.ScriptLogLevelError, "TypeError: Cannot read property 'items' of undefined\n    at process (ads.js:42:17)\n    at main (ads.js:9:3)", base+410))
	srv.PushScriptLog(slLog("weather", "generic", api.ScriptLogLevelError, strings.Repeat("upstream responded with an unexpected payload; ", 12), base+500))
}

func TestLogsShowLinesWithLevels(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, slLogFixture)
	testkit.UntilText(t, h.tt, "forecast updated: 21°C")
	for _, want := range []string{"时间", "脚本", "阶段", "级别", "消息", "08:12:41.250", "错误", "警告", "信息", "日志", "请求", "响应", "5 条"} {
		if !h.tt.HasText(want) {
			t.Errorf("missing %q; texts %q", want, h.tt.Texts())
		}
	}
	if h.tt.HasText("已断开") {
		t.Error("shows a broken link")
	}
}

func TestLogsEmpty(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, nil)
	testkit.UntilText(t, h.tt, "暂无日志")
}

func TestLogsLevelSearchAndTagFilters(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, slLogFixture)
	testkit.UntilText(t, h.tt, "forecast updated: 21°C")
	// The segment named like a level also heads its column: click the
	// segment, which is the first of the two.
	slClick(t, h.tt, "错误")
	h.tt.Frame()
	if h.tt.HasText("forecast updated: 21°C") || !h.tt.HasText("TypeError: Cannot read property 'items' of undefined") {
		t.Errorf("error filter: %q", h.tt.Texts())
	}
	if !h.tt.HasText("2 / 5 条") {
		t.Errorf("count: %q", h.tt.Texts())
	}
	slClick(t, h.tt, "全部")
	slClick(t, h.tt, "搜索消息、脚本")
	h.tt.Type("PERSISTENT")
	h.tt.Frame()
	if !h.tt.HasText("$persistentStore.write") || h.tt.HasText("forecast updated: 21°C") {
		t.Errorf("search: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	// A tag set from outside, as the list page's Logs action does.
	h.page.SetTag("ads")
	h.tt.Frame()
	if h.tt.HasText("forecast updated: 21°C") || !h.tt.HasText("$persistentStore.write") {
		t.Errorf("tag filter: %q", h.tt.Texts())
	}
	// A filter that hides everything says so, and clears.
	h.page.level = 3
	h.page.version++
	h.tt.Frame()
	testkit.UntilText(t, h.tt, "没有匹配的日志")
	slClick(t, h.tt, "清除筛选")
	if h.page.tag != "" || h.page.level != 0 || !h.tt.HasText("forecast updated: 21°C") {
		t.Errorf("reset: tag=%q level=%d", h.page.tag, h.page.level)
	}
}

func TestLogsSelectionShowsWholeMessage(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, slLogFixture)
	testkit.UntilText(t, h.tt, "forecast updated: 21°C")
	slClick(t, h.tt, "forecast updated: 21°C")
	testkit.UntilText(t, h.tt, "复制消息")
	if h.page.selID == 0 {
		t.Fatal("no selection")
	}
	selected := h.page.selID
	// New lines do not disturb the choice.
	for i := 0; i < 5; i++ {
		h.srv.PushScriptLog(slLog("weather", "http-request", api.ScriptLogLevelLog, fmt.Sprint("later ", i), 0))
	}
	testkit.UntilText(t, h.tt, "10 条")
	if h.page.selID != selected || !h.tt.HasText("复制消息") {
		t.Error("new lines changed the selection")
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.tt.Frame()
	if h.page.selID != 0 {
		t.Error("Escape keeps the inspector")
	}
}

func TestLogsPauseHoldsAndResumeAppends(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, slLogFixture)
	testkit.UntilText(t, h.tt, "5 条")
	slClick(t, h.tt, "暂停滚动")
	if !h.tt.HasText("已暂停") {
		t.Fatalf("not paused: %q", h.tt.Texts())
	}
	h.srv.PushScriptLog(slLog("weather", "http-request", api.ScriptLogLevelLog, "arrived while paused", 0))
	testkit.Until(t, h.tt, func() bool { return len(h.page.entries) == 6 })
	h.tt.Frame()
	if h.tt.HasText("arrived while paused") || !h.tt.HasText("5 条") {
		t.Errorf("paused list moved: %q", h.tt.Texts())
	}
	slClick(t, h.tt, "继续")
	testkit.UntilText(t, h.tt, "arrived while paused")
	if h.tt.HasText("已暂停") {
		t.Error("still shows paused")
	}
}

func TestLogsClear(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, slLogFixture)
	testkit.UntilText(t, h.tt, "5 条")
	slClick(t, h.tt, "清空日志")
	testkit.UntilText(t, h.tt, "暂无日志")
	h.srv.PushScriptLog(slLog("weather", "", api.ScriptLogLevelLog, "after clearing", 0))
	testkit.UntilText(t, h.tt, "after clearing")
	if h.page.version == 0 || len(h.page.entries) != 1 {
		t.Errorf("entries %d", len(h.page.entries))
	}
}

func TestLogsKeepNewest(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, nil)
	h.page.SetActive(false)
	for i := 1; i <= logsKeep+200; i++ {
		h.page.push(api.ScriptLogEntry{Seq: api.Uint64(i), Script: "s", Level: api.ScriptLogLevelLog, Message: fmt.Sprint("m", i)})
	}
	h.page.drain()
	if len(h.page.entries) != logsKeep {
		t.Fatalf("kept %d lines", len(h.page.entries))
	}
	if first := h.page.entries[0].entry.Seq; first != 201 {
		t.Errorf("oldest kept is %d", first)
	}
}

func TestLogsRestartDropsOldLines(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, slLogFixture)
	testkit.UntilText(t, h.tt, "5 条")
	h.srv.RestartScriptHub()
	h.srv.PushScriptLog(slLog("weather", "", api.ScriptLogLevelInfo, "after the restart", 0))
	testkit.UntilText(t, h.tt, "after the restart")
	if h.tt.HasText("fetching forecast") || !h.tt.HasText("1 条") {
		t.Errorf("old lines stay after a restart: %q", h.tt.Texts())
	}
	// The link breaks for the moment of the restart and mends by itself.
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected && !h.tt.HasText("已断开") })
}

func TestLogsFollowEndAndBackToBottom(t *testing.T) {
	h := newLogsHarness(t, 1280, 520, nil)
	for i := 1; i <= 200; i++ {
		h.srv.PushScriptLog(slLog("s", "", api.ScriptLogLevelLog, fmt.Sprint("line-", i), 0))
	}
	testkit.UntilText(t, h.tt, "line-200")
	if h.tt.HasText("回到底部") {
		t.Error("the button shows at the end")
	}
	// Leaving the end stops following; new lines do not pull it back.
	h.page.table.List.ScrollTo(20, ui.Start)
	h.tt.Frame()
	h.tt.Frame()
	if h.page.table.List.AtEnd() {
		t.Fatal("the list is still at its end")
	}
	testkit.UntilText(t, h.tt, "回到底部")
	h.srv.PushScriptLog(slLog("s", "", api.ScriptLogLevelLog, "line-201", 0))
	testkit.UntilText(t, h.tt, "201 条")
	if h.tt.HasText("line-201") {
		t.Error("the list followed the end after the user scrolled away")
	}
	slClick(t, h.tt, "回到底部")
	testkit.Until(t, h.tt, func() bool { return h.tt.HasText("line-201") })
}

func TestLogsPhaseAndLevelNames(t *testing.T) {
	if logsPhaseName("") != "—" || logsPhaseName("http-request") != "请求" || logsPhaseName("response") != "响应" || logsPhaseName("cron") != "定时" || logsPhaseName("x") != "x" {
		t.Error("phase names")
	}
	for level, want := range map[string]string{"error": "错误", "warn": "警告", "info": "信息", "log": "日志", "x": "x"} {
		if logsLevelName(level) != want {
			t.Errorf("level %q", level)
		}
	}
	if logsTone("error") != kit.DangerTone || logsTone("warn") != kit.WarningTone || logsTone("log") != kit.Neutral {
		t.Error("tones")
	}
}

func TestLogsAcceptanceScreens(t *testing.T) {
	shoot := func(tt *ui.Tester, name string) {
		t.Helper()
		slSavePreview(t, tt, name+"-light.png")
		tt.SetDark(true)
		tt.Advance(600 * time.Millisecond)
		tt.Frame()
		slSavePreview(t, tt, name+"-dark.png")
		tt.SetDark(false)
		tt.Advance(600 * time.Millisecond)
		tt.Frame()
	}
	h := newLogsHarness(t, 1440, 800, slLogFixture)
	testkit.UntilText(t, h.tt, "5 条")
	last := h.page.entries[len(h.page.entries)-1]
	h.page.selID = last.id
	h.tt.Frame()
	testkit.UntilText(t, h.tt, "复制消息")
	shoot(h.tt, "16-logs")

	empty := newLogsHarness(t, 1440, 800, nil)
	testkit.UntilText(t, empty.tt, "暂无日志")
	shoot(empty.tt, "17-logs-empty")
}

func TestLogsReconnectClearsBrokenMark(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, nil)
	h.page.SetActive(false)
	h.page.disconnected = true
	h.tt.Frame()
	if !h.tt.HasText("已断开") {
		t.Fatalf("no broken mark: %q", h.tt.Texts())
	}
	// The stream comes back and stays up without a single line.
	h.page.SetActive(true)
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected && !h.tt.HasText("已断开") })
}

// A core that is not available shows as a broken link, which mends with
// the core.
func TestLogsShowDisconnectedWhileCoreUnavailable(t *testing.T) {
	h := newLogsHarness(t, 1280, 720, nil)
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected })
	h.srv.SetScriptsUnavailable(true)
	testkit.UntilText(t, h.tt, "已断开")
	h.srv.SetScriptsUnavailable(false)
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected && !h.tt.HasText("已断开") })
}
