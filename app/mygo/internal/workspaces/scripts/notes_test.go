package scripts

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

type notesHarness struct {
	t    *testing.T
	srv  *testkit.Server
	env  *env.Env
	page *NotesPage
	tt   *ui.Tester
}

func newNotesHarness(t *testing.T, width, height int, setup func(*testkit.Server)) *notesHarness {
	t.Helper()
	h := &notesHarness{t: t, srv: testkit.NewServer(t)}
	if setup != nil {
		setup(h.srv)
	}
	h.env, _ = testkit.NewEnv(h.srv)
	h.page = NewNotesPage(h.env)
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

func slNote(script, title, subtitle, body string, at int64) api.ScriptNotification {
	return api.ScriptNotification{Script: script, Title: title, Subtitle: subtitle, Body: body, TimeMS: at}
}

func slNoteFixture(srv *testkit.Server) {
	base := time.Date(2026, 11, 3, 8, 12, 41, 0, time.Local).UnixMilli()
	srv.PushScriptNotification(slNote("daily", "每日签到", "已完成", "连续签到 12 天，获得 30 积分", base))
	srv.PushScriptNotification(slNote("weather", "降雨提醒", "", "未来一小时有雨", base+60_000))
	n := slNote("weather", "气温骤降", "明日最低 3°C", "请注意添衣。\n来源：气象台", base+120_000)
	n.URL = "https://weather.example.com/alerts/42"
	srv.PushScriptNotification(n)
}

func TestNotesShowRows(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, slNoteFixture)
	testkit.UntilText(t, h.tt, "降雨提醒")
	for _, want := range []string{"时间", "脚本", "标题", "内容", "08:12:41", "每日签到", "已完成 · 连续签到 12 天，获得 30 积分", "未来一小时有雨", "3 条"} {
		if !h.tt.HasText(want) {
			t.Errorf("missing %q; texts %q", want, h.tt.Texts())
		}
	}
	if h.tt.HasText("已断开") {
		t.Error("shows a broken link")
	}
}

func TestNotesEmpty(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, nil)
	testkit.UntilText(t, h.tt, "暂无通知")
}

func TestNotesSearchAndScriptFilter(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, slNoteFixture)
	testkit.UntilText(t, h.tt, "降雨提醒")
	slClick(t, h.tt, "搜索标题、内容、脚本")
	h.tt.Type("积分")
	h.tt.Frame()
	if !h.tt.HasText("每日签到") || h.tt.HasText("降雨提醒") || !h.tt.HasText("1 / 3 条") {
		t.Errorf("body search: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	// The URL and the script name are searched too.
	h.page.search = "alerts/42"
	h.tt.Frame()
	if !h.tt.HasText("气温骤降") || h.tt.HasText("降雨提醒") {
		t.Errorf("url search: %q", h.tt.Texts())
	}
	h.page.search = "DAILY"
	h.tt.Frame()
	if !h.tt.HasText("每日签到") || h.tt.HasText("气温骤降") {
		t.Errorf("script search: %q", h.tt.Texts())
	}
	h.page.search = ""
	h.page.tag = "weather"
	h.page.version++
	h.tt.Frame()
	if h.tt.HasText("每日签到") || !h.tt.HasText("降雨提醒") || !h.tt.HasText("2 / 3 条") {
		t.Errorf("script filter: %q", h.tt.Texts())
	}
	// A filter that hides everything says so, and clears.
	h.page.search = "nothing like this"
	testkit.UntilText(t, h.tt, "没有匹配的通知")
	slClick(t, h.tt, "清除筛选")
	if h.page.tag != "" || h.page.search != "" || !h.tt.HasText("每日签到") {
		t.Errorf("reset: tag=%q search=%q", h.page.tag, h.page.search)
	}
}

func TestNotesInspectorShowsWholeNotification(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, slNoteFixture)
	testkit.UntilText(t, h.tt, "气温骤降")
	slClick(t, h.tt, "气温骤降")
	testkit.UntilText(t, h.tt, "复制通知")
	for _, want := range []string{"2026-11-03 08:14:41", "副标题", "明日最低 3°C", "链接", "https://weather.example.com/alerts/42", "内容"} {
		if !h.tt.HasText(want) {
			t.Errorf("missing %q; texts %q", want, h.tt.Texts())
		}
	}
	slClick(t, h.tt, "复制通知")
	want := "气温骤降\n明日最低 3°C\n请注意添衣。\n来源：气象台\nhttps://weather.example.com/alerts/42"
	if got := h.tt.Clipboard(); got != want {
		t.Errorf("clipboard %q", got)
	}
	selected := h.page.selID
	// New notifications do not disturb the choice.
	for i := 0; i < 3; i++ {
		h.srv.PushScriptNotification(slNote("daily", fmt.Sprint("later ", i), "", "", 0))
	}
	testkit.UntilText(t, h.tt, "6 条")
	if h.page.selID != selected || !h.tt.HasText("复制通知") {
		t.Error("new notifications changed the selection")
	}
	// Escape works from the list.
	slClick(t, h.tt, "气温骤降")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.tt.Frame()
	if h.page.selID != 0 {
		t.Error("Escape keeps the inspector")
	}
}

func TestNotesPauseHoldsAndResumeAppends(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, slNoteFixture)
	testkit.UntilText(t, h.tt, "3 条")
	slClick(t, h.tt, "暂停滚动")
	if !h.tt.HasText("已暂停") {
		t.Fatalf("not paused: %q", h.tt.Texts())
	}
	h.srv.PushScriptNotification(slNote("daily", "arrived while paused", "", "", 0))
	testkit.Until(t, h.tt, func() bool { return len(h.page.entries) == 4 })
	h.tt.Frame()
	if h.tt.HasText("arrived while paused") || !h.tt.HasText("3 条") {
		t.Errorf("paused list moved: %q", h.tt.Texts())
	}
	slClick(t, h.tt, "继续")
	testkit.UntilText(t, h.tt, "arrived while paused")
	if h.tt.HasText("已暂停") {
		t.Error("still shows paused")
	}
}

func TestNotesClear(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, slNoteFixture)
	testkit.UntilText(t, h.tt, "3 条")
	slClick(t, h.tt, "清空通知")
	testkit.UntilText(t, h.tt, "暂无通知")
	h.srv.PushScriptNotification(slNote("daily", "after clearing", "", "", 0))
	testkit.UntilText(t, h.tt, "after clearing")
	if len(h.page.entries) != 1 {
		t.Errorf("entries %d", len(h.page.entries))
	}
}

func TestNotesKeepNewest(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, nil)
	h.page.SetActive(false)
	for i := 1; i <= notesKeep+200; i++ {
		h.page.push(api.ScriptNotification{Seq: api.Uint64(i), Script: "s", Title: fmt.Sprint("t", i)})
	}
	h.page.drain()
	if len(h.page.entries) != notesKeep {
		t.Fatalf("kept %d", len(h.page.entries))
	}
	if first := h.page.entries[0].n.Seq; first != 201 {
		t.Errorf("oldest kept is %d", first)
	}
}

func TestNotesRestartDropsOldOnes(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, slNoteFixture)
	testkit.UntilText(t, h.tt, "3 条")
	slClick(t, h.tt, "每日签到")
	testkit.UntilText(t, h.tt, "复制通知")
	h.srv.RestartScriptHub()
	h.srv.PushScriptNotification(slNote("daily", "after the restart", "", "", 0))
	testkit.UntilText(t, h.tt, "after the restart")
	if h.tt.HasText("降雨提醒") || !h.tt.HasText("1 条") {
		t.Errorf("old notifications stay after a restart: %q", h.tt.Texts())
	}
	if h.page.selID != 0 {
		t.Error("the inspector keeps a notification of before the restart")
	}
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected && !h.tt.HasText("已断开") })
}

func TestNotesDisconnectedWhileCoreUnavailable(t *testing.T) {
	h := newNotesHarness(t, 1280, 720, nil)
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected })
	h.srv.SetScriptsUnavailable(true)
	testkit.UntilText(t, h.tt, "已断开")
	h.srv.SetScriptsUnavailable(false)
	testkit.Until(t, h.tt, func() bool { return !h.page.disconnected && !h.tt.HasText("已断开") })
}

func TestNotesNarrowShowsInspectorAsPanel(t *testing.T) {
	h := newNotesHarness(t, 700, 600, slNoteFixture)
	testkit.UntilText(t, h.tt, "降雨提醒")
	slClick(t, h.tt, "气温骤降")
	testkit.UntilText(t, h.tt, "复制通知")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.tt.Frame()
	if h.page.selID != 0 || h.tt.HasText("复制通知") {
		t.Error("Escape keeps the panel")
	}
}

func TestNoteSummaryAndText(t *testing.T) {
	n := api.ScriptNotification{Title: "t", Subtitle: " s ", Body: "a\n  b\tc"}
	if got := noteSummary(n); got != "s · a b c" {
		t.Errorf("summary %q", got)
	}
	if got := noteSummary(api.ScriptNotification{Title: "t"}); got != "" {
		t.Errorf("summary %q", got)
	}
	if got := noteText(api.ScriptNotification{Title: "t", URL: "u"}); got != "t\nu" {
		t.Errorf("text %q", got)
	}
	if noteTime(0) != "—" || noteDateTime(-1) != "—" {
		t.Error("unset time")
	}
}

// ---- the page inside the workspace ----

func TestNotesSubPageFromToolbar(t *testing.T) {
	f := newFixture(t, cronDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "每日签到")
	slNoteFixture(f.srv)
	f.click("通知")
	testkit.Until(t, tt, func() bool { return f.w.InSub() && f.w.sub == subNotes })
	testkit.UntilText(t, tt, "降雨提醒")
	if tt.HasText("统一 UA") {
		t.Fatalf("list under the notification page: %q", tt.Texts())
	}
	if !f.w.notes.active || !f.w.Running() {
		t.Error("page not following")
	}
	// Escape returns and the toolbar button has the keyboard again.
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.InSub() })
	testkit.UntilText(t, tt, "统一 UA")
	tt.Frame()
	if !tt.Focused("通知") {
		t.Error("the 通知 button does not have the keyboard after returning")
	}
	if f.w.notes.active {
		t.Error("page keeps following after it closed")
	}
	// The back button returns too.
	f.click("通知")
	testkit.Until(t, tt, func() bool { return f.w.InSub() })
	f.click("脚本")
	testkit.Until(t, tt, func() bool { return !f.w.InSub() })
	// Going away stops it.
	f.click("通知")
	testkit.Until(t, tt, func() bool { return f.w.notes.active })
	f.w.SetActive(false)
	if f.w.notes.active || f.w.InSub() {
		t.Error("notifications run while the page is away")
	}
}

func TestNotesAcceptanceScreens(t *testing.T) {
	h := newNotesHarness(t, 1440, 800, slNoteFixture)
	testkit.UntilText(t, h.tt, "3 条")
	h.page.selID = h.page.entries[len(h.page.entries)-1].id
	h.tt.Frame()
	testkit.UntilText(t, h.tt, "复制通知")
	shootBoth(t, h.tt, "35-notes")

	empty := newNotesHarness(t, 1440, 800, nil)
	testkit.UntilText(t, empty.tt, "暂无通知")
	shootBoth(t, empty.tt, "36-notes-empty")
}
