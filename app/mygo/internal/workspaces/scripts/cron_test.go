package scripts

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// cronDoc is a document with an HTTP script, a generic one and scheduled
// ones: valid, disabled and with an expression that does not parse.
func cronDoc(mitm bool) *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: mitm}, Scripts: []*model.HTTPScript{
		{ID: "s-ua", Name: "统一 UA", Type: model.ScriptHTTPRequest, Pattern: `^https://api\.example\.com/`, Source: uaSource},
		{ID: "s-sync", Name: "同步任务", Type: model.ScriptGeneric, Source: "console.log('sync')\n$done({})\n"},
		{ID: "s-tick", Name: "每日签到", Tag: "daily", Type: model.ScriptCron, Cron: "30 18 * * *", Source: "$done()\n"},
		{ID: "s-off", Name: "停用定时", Type: model.ScriptCron, Cron: "0 0 * * *", Disabled: true, Source: "$done()\n"},
		{ID: "s-bad", Name: "坏表达式", Type: model.ScriptCron, Cron: "61 * * * *", Source: "$done()\n"},
	}}
	return doc
}

// runningStatus lists the scripts the core holds.
func runningStatus(next int64) api.ScriptStatus {
	return api.ScriptStatus{Scripts: []api.ScriptState{
		{Tag: "s-ua", Type: api.ScriptTypeRequest, Runs: 5},
		{Tag: "s-sync", Type: api.ScriptTypeGeneric, Runs: 2},
		{Tag: "daily", Type: api.ScriptTypeCron, Runs: 9, NextRunMS: next},
	}}
}

func TestNextRunText(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local)
	at := func(y int, m time.Month, d, h, min int) int64 {
		return time.Date(y, m, d, h, min, 0, 0, time.Local).UnixMilli()
	}
	for _, c := range []struct {
		ms   int64
		want string
	}{
		{0, ""},
		{-5, ""},
		{at(2026, 11, 4, 18, 30), "18:30"},
		{at(2026, 11, 4, 10, 1), "10:01"},
		{at(2026, 11, 5, 0, 0), "明天 00:00"},
		{at(2026, 11, 5, 8, 5), "明天 08:05"},
		{at(2026, 11, 6, 8, 0), "11-06 08:00"},
		{at(2026, 12, 31, 23, 59), "12-31 23:59"},
		{at(2027, 1, 1, 0, 0), "2027-01-01 00:00"},
	} {
		if got := NextRunText(c.ms, now); got != c.want {
			t.Errorf("NextRunText(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
	// The month's last day to the next month's first is tomorrow.
	end := time.Date(2026, 11, 30, 23, 0, 0, 0, time.Local)
	if got := NextRunText(time.Date(2026, 12, 1, 6, 0, 0, 0, time.Local).UnixMilli(), end); got != "明天 06:00" {
		t.Errorf("across the month: %q", got)
	}
}

func TestCronTypeLabelAndTemplate(t *testing.T) {
	if TypeLabel(model.ScriptCron) != "定时" {
		t.Error("label")
	}
	src := template(model.ScriptCron)
	if !strings.Contains(src, "$notification.post") || !strings.Contains(src, "$done()") || sourceIssue(src) != "" {
		t.Errorf("template %q (%s)", src, sourceIssue(src))
	}
	if template(model.ScriptCron) == template(model.ScriptGeneric) {
		t.Error("cron starts from the generic template")
	}
}

func TestCronListRows(t *testing.T) {
	f := newInactiveFixture(t, cronDoc(true), 1280, 700)
	tt := f.tt
	tomorrow := time.Date(f.now.Year(), f.now.Month(), f.now.Day()+1, 8, 0, 0, 0, f.now.Location())
	f.srv.SetScriptStatus(runningStatus(tomorrow.UnixMilli()))
	f.activate()
	testkit.Until(t, tt, func() bool { return tt.HasText("明天 08:00") })
	f.want("定时", "30 18 * * *", "每日签到", "停用定时", "0 0 * * *")
	// An invalid expression is a problem of its row; a disabled script
	// shows no next run.
	f.want("分钟段无效", "已停用")
	if tt.HasText("未知的脚本类型") {
		t.Errorf("texts %q", tt.Texts())
	}
	// HTTP and generic rows have no schedule.
	n := 0
	for _, text := range tt.Texts() {
		if text == "明天 08:00" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("next run shown %d times", n)
	}

	// A paused script shows no next run.
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "daily", Type: api.ScriptTypeCron, Paused: true, NextRunMS: tomorrow.UnixMilli()}}})
	tt.Advance(pollEvery + 100*time.Millisecond)
	testkit.Until(t, tt, func() bool { return tt.HasText("已暂停") })
	if tt.HasText("明天 08:00") {
		t.Errorf("next run of a paused script: %q", tt.Texts())
	}

	// The search matches the expression.
	f.search = "18 * *"
	testkit.Until(t, tt, func() bool { return !tt.HasText("统一 UA") })
	f.want("每日签到")
	if tt.HasText("停用定时") {
		t.Errorf("search %q", tt.Texts())
	}
}

func TestCronNextRunToday(t *testing.T) {
	f := newInactiveFixture(t, cronDoc(true), 1280, 700)
	soon := f.now.Add(time.Minute)
	f.srv.SetScriptStatus(runningStatus(soon.UnixMilli()))
	f.activate()
	want := NextRunText(soon.UnixMilli(), f.now)
	testkit.Until(t, f.tt, func() bool { return f.tt.HasText(want) })
}

func TestCronNarrowListKeepsExpression(t *testing.T) {
	f := newInactiveFixture(t, cronDoc(true), 700, 600)
	f.srv.SetScriptStatus(runningStatus(f.now.Add(time.Hour).UnixMilli()))
	f.activate()
	testkit.Until(t, f.tt, func() bool { return f.tt.HasText("9") })
	f.want("30 18 * * *", "每日签到")
	if !f.w.narrow {
		t.Error("not narrow")
	}
}

func TestCronEditorValidationAndSave(t *testing.T) {
	f := newFixture(t, cronDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "每日签到")
	f.click("添加脚本")
	testkit.UntilText(t, tt, "添加脚本")
	e := f.w.editors.ed
	e.setType(typeIndex(model.ScriptGeneric))
	tt.Frame()
	if tt.HasText("定时表达式") {
		t.Errorf("schedule field for a generic script: %q", tt.Texts())
	}
	// "Scheduled" is both the segment and the type cell of the rows: choose it by
	// the draft.
	e.setType(typeIndex(model.ScriptCron))
	testkit.UntilText(t, tt, "定时表达式")
	if tt.HasText("匹配模式（RE2）") || tt.HasText("测试 URL") || tt.HasText("读取正文") {
		t.Errorf("HTTP options for a scheduled script: %q", tt.Texts())
	}
	if !strings.Contains(e.code.Text(), "$notification.post") {
		t.Errorf("template %q", e.code.Text())
	}
	if tt.HasText("请填写定时表达式") {
		t.Fatal("required message before saving")
	}
	f.click("保存")
	testkit.UntilText(t, tt, "请填写定时表达式")
	if len(f.scripts()) != 5 {
		t.Fatal("saved a script without a schedule")
	}
	for expr, want := range map[string]string{
		"* * *":        "定时表达式须为 5 段：分 时 日 月 星期",
		"61 * * * *":   "分钟段无效",
		"0 24 * * *":   "小时段无效",
		"*/0 * * * *":  "分钟段无效",
		"* * * * * *":  "定时表达式须为 5 段：分 时 日 月 星期",
		"@sometimes":   "不支持的定时宏",
		"0 0 1 13 *":   "月段无效",
		"0 0 * * mon-": "星期段无效",
	} {
		e.cron = expr
		testkit.UntilText(t, tt, want)
	}
	e.cron = "  */5 * * * *  "
	testkit.Until(t, tt, func() bool { return !tt.HasText("星期段无效") })
	e.name, e.timeout = "每五分钟", "20"
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.scripts()) == 6 })
	got := f.scripts()[5]
	if got.Type != model.ScriptCron || got.Cron != "*/5 * * * *" || got.Pattern != "" || got.RequiresBody || got.MaxSize != 0 ||
		got.Timeout != 20 || got.Name != "每五分钟" || got.Source != template(model.ScriptCron) {
		t.Fatalf("saved %+v", got)
	}
	if got.Issue() != "" {
		t.Errorf("issue %q", got.Issue())
	}
}

func TestCronEditorKeepsExpressionAndTimeoutLimit(t *testing.T) {
	f := newFixture(t, cronDoc(true), 1280, 900)
	testkit.UntilText(t, f.tt, "每日签到")
	if !f.w.editors.Edit("s-tick") {
		t.Fatal("no script")
	}
	testkit.UntilText(t, f.tt, "编辑脚本")
	e := f.w.editors.ed
	if e.cron != "30 18 * * *" || e.typeName() != model.ScriptCron {
		t.Fatalf("draft %+v", e)
	}
	f.want("定时表达式")
	// The timeout has the generic default and limit.
	e.timeout = "61"
	testkit.UntilText(t, f.tt, model.ScriptTimeoutIssue(model.ScriptCron, 61))
	e.timeout = ""
	// A change of the expression is a change of the draft.
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if e.snapshot() != e.base {
		t.Fatal("untouched draft differs")
	}
	f.w.editors.Close()
	f.w.editors.Edit("s-tick")
	f.w.editors.ed.cron = "0 9 * * *"
	if f.w.editors.ed.snapshot() == f.w.editors.ed.base {
		t.Error("the expression is not part of the draft")
	}
	f.w.editors.Close()
}

func TestCronSwitchBackToHTTPTemplate(t *testing.T) {
	on := model.EmptyDoc(model.PlatformLinux)
	on.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	f := newFixture(t, on, 1280, 900)
	testkit.UntilText(t, f.tt, "暂无脚本")
	f.w.Add(Seed{Type: model.ScriptCron})
	testkit.UntilText(t, f.tt, "添加脚本")
	e := f.w.editors.ed
	if e.typeName() != model.ScriptCron || e.code.Text() != template(model.ScriptCron) {
		t.Fatalf("seed: %q", e.code.Text())
	}
	e.setType(typeIndex(model.ScriptHTTPRequest))
	if e.code.Text() != template(model.ScriptHTTPRequest) {
		t.Errorf("template kept: %q", e.code.Text())
	}
	e.setType(typeIndex(model.ScriptCron))
	e.code.SetText("console.log(1)\n$done()\n")
	e.setType(typeIndex(model.ScriptGeneric))
	if e.code.Text() != "console.log(1)\n$done()\n" {
		t.Errorf("edited source replaced: %q", e.code.Text())
	}
	f.w.editors.Close()
}

func TestCronTestRunLikeGeneric(t *testing.T) {
	f := newFixture(t, cronDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "每日签到")
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionNone, DurationUS: 30, Output: "signed",
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelInfo, Message: "tick ran"}}}
	})
	e := f.openTest("s-tick")
	e.argument = "n=3"
	if tt.HasText("已抓取") || tt.HasText("手填") {
		t.Errorf("input choice for a scheduled script: %q", tt.Texts())
	}
	f.click("运行测试")
	testkit.UntilText(t, tt, "tick ran")
	f.want("signed")
	got := f.srv.ScriptEvalRequests()[0]
	if got.Type != api.ScriptTypeCron || got.Request != nil || got.Response != nil || got.Argument != "n=3" || got.Pattern != "" {
		t.Fatalf("evaluate request %+v", got)
	}
}

func TestCronLogsPhaseNames(t *testing.T) {
	if logsPhaseName("cron") != "定时" {
		t.Error("phase")
	}
}

// ---- run now ----

func runFixture(t *testing.T, mitm bool) *fixture {
	t.Helper()
	f := newInactiveFixture(t, cronDoc(mitm), 1280, 700)
	f.srv.SetScriptStatus(runningStatus(f.now.Add(time.Hour).UnixMilli()))
	f.activate()
	testkit.Until(t, f.tt, func() bool { return f.w.statsOK || !mitm })
	return f
}

func (f *fixture) runs() []testkit.ScriptRunCall { return f.srv.ScriptRuns() }

func TestRunNowSuccessShowsResult(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	f.srv.SetScriptRun(func(call testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 4200, Output: "synced 3 items",
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelInfo, Message: "start"}, {Level: api.ScriptLogLevelWarn, Message: "slow upstream"}}}
	})
	// Only generic and scheduled scripts have the button.
	if err := tt.Click("立即运行 统一 UA"); err == nil {
		t.Error("run button on an HTTP script")
	}
	before := countStatusReads(f)
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "synced 3 items")
	f.want("未修改", "4.2 ms", "start", "slow upstream", "输出", "复制输出", "关闭")
	if r := f.runs(); len(r) != 1 || r[0].Tag != "s-sync" {
		t.Fatalf("runs %+v", r)
	}
	// The counters are read again after the run.
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "s-sync", Type: api.ScriptTypeGeneric, Runs: 3}}})
	f.click("复制输出")
	if tt.Clipboard() != "synced 3 items" {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
	f.click("关闭")
	testkit.Until(t, tt, func() bool { return !tt.HasText("synced 3 items") })
	if f.w.runs["s-sync"] != nil && f.w.runs["s-sync"].open {
		t.Error("popover state stays open")
	}
	// The counters are read again after the run, before the next poll is due.
	testkit.Until(t, tt, func() bool { return countStatusReads(f) > before && tt.HasText("3") })
}

func countStatusReads(f *fixture) int {
	n := 0
	for _, r := range f.srv.Requests() {
		if strings.Contains(r, "/scripts/status") {
			n++
		}
	}
	return n
}

func TestRunNowCronScriptAndMenuPath(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 800, Output: "checked in"}
	})
	f.click("更多 每日签到")
	f.click("立即运行")
	testkit.UntilText(t, tt, "checked in")
	if r := f.runs(); len(r) != 1 || r[0].Tag != "daily" {
		t.Fatalf("runs %+v", r)
	}
	// The popover hangs on the More button of the row.
	if !f.w.runs["s-tick"].more {
		t.Error("anchored on the run button")
	}
	// Escape closes it.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if tt.HasText("checked in") {
		t.Error("Escape keeps the popover")
	}
}

func TestRunNowSendsArgument(t *testing.T) {
	doc := cronDoc(true)
	doc.HTTPTools.Scripts[1].Argument = "n=3"
	f := newInactiveFixture(t, doc, 1280, 700)
	f.srv.SetScriptStatus(runningStatus(0))
	f.activate()
	testkit.Until(t, f.tt, func() bool { return f.w.statsOK })
	f.click("立即运行 同步任务")
	testkit.Until(t, f.tt, func() bool { return len(f.runs()) == 1 })
	if got := f.runs()[0]; got.Argument != "n=3" {
		t.Errorf("argument %q", got.Argument)
	}
}

func TestRunNowFailureKeepsPopoverWithRetry(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	calls := 0
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		calls++
		if calls == 1 {
			return api.ScriptRunResult{Action: api.ScriptActionError, DurationUS: 900, Error: "ReferenceError: nope is not defined",
				Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelError, Message: "boom"}}}
		}
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 700, Output: "second time"}
	})
	f.click("立即运行 每日签到")
	testkit.UntilText(t, tt, "ReferenceError: nope is not defined")
	f.want("脚本出错", "boom", "重试", "关闭")
	f.click("重试")
	testkit.UntilText(t, tt, "second time")
	if tt.HasText("重试") || tt.HasText("ReferenceError: nope is not defined") {
		t.Errorf("failure stays after a good run: %q", tt.Texts())
	}
	if len(f.runs()) != 2 {
		t.Errorf("runs %d", len(f.runs()))
	}
}

func TestRunNowRequestFailure(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	f.srv.SetScriptsUnavailable(true)
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "核心未运行")
	f.want("重试")
	// The core is away: the retry says so while the counters do not list
	// the script.
	f.click("重试")
	f.want("核心未运行")
	f.srv.SetScriptsUnavailable(false)
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 5, Output: "back"}
	})
	tt.Advance(pollEvery + 100*time.Millisecond)
	testkit.Until(t, tt, func() bool { return f.w.statsOK })
	f.click("重试")
	testkit.UntilText(t, tt, "back")
}

func TestRunNowDisabledWhileRunning(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	release := make(chan struct{})
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		<-release
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 5, Output: "late"}
	})
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "运行中…")
	// A second press does not start another run.
	_ = tt.Click("立即运行 同步任务")
	tt.Frame()
	if len(f.runs()) > 1 {
		t.Errorf("%d runs", len(f.runs()))
	}
	if !f.w.runs["s-sync"].running {
		t.Error("not running")
	}
	close(release)
	testkit.UntilText(t, tt, "late")
	if f.w.runs["s-sync"].running || tt.HasText("运行中…") {
		t.Error("still running")
	}
	if len(f.runs()) != 1 {
		t.Errorf("%d runs", len(f.runs()))
	}
}

func TestRunNowClosedWhileRunningDropsResult(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	release := make(chan struct{})
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		<-release
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 5, Output: "unseen"}
	})
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "运行中…")
	f.click("关闭")
	testkit.Until(t, tt, func() bool { return !tt.HasText("运行中…") })
	close(release)
	testkit.Until(t, tt, func() bool { return !f.w.runs["s-sync"].running })
	tt.Frame()
	if tt.HasText("unseen") {
		t.Error("a result opens the popover again")
	}
}

func TestRunNowBlockReasons(t *testing.T) {
	f := runFixture(t, true)
	rows := map[string]scriptRow{}
	for _, r := range rowsOf(f.store.Doc()) {
		rows[r.ID] = r
	}
	for id, want := range map[string]string{
		"s-ua":   "仅通用和定时脚本可运行",
		"s-sync": "",
		"s-tick": "",
		"s-off":  "脚本已停用",
		"s-bad":  "脚本尚未生效",
	} {
		if got := f.w.runBlock(rows[id]); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	// Nothing runs while the core is away.
	f.srv.SetScriptsUnavailable(true)
	f.tt.Advance(pollEvery + 100*time.Millisecond)
	testkit.Until(t, f.tt, func() bool { return !f.w.statsOK })
	if got := f.w.runBlock(rows["s-sync"]); got != "核心未运行" {
		t.Errorf("core away: %q", got)
	}
	_ = f.tt.Click("立即运行 同步任务")
	f.tt.Frame()
	if len(f.runs()) != 0 {
		t.Error("ran without a core")
	}

	// Decryption off.
	off := newFixture(t, cronDoc(false), 1280, 700)
	orows := rowsOf(off.store.Doc())
	if got := off.w.runBlock(orows[1]); got != "未开启 HTTPS 解密" {
		t.Errorf("decryption off: %q", got)
	}
	_ = off.tt.Click("立即运行 同步任务")
	off.tt.Frame()
	if len(off.runs()) != 0 {
		t.Error("ran with decryption off")
	}
}

func TestRunNowDisabledButtonsDoNotRun(t *testing.T) {
	f := runFixture(t, true)
	for _, label := range []string{"立即运行 停用定时", "立即运行 坏表达式"} {
		_ = f.tt.Click(label)
		f.tt.Frame()
	}
	if len(f.runs()) != 0 {
		t.Errorf("runs %+v", f.runs())
	}
	for _, id := range []string{"s-off", "s-bad"} {
		if s := f.w.runs[id]; s != nil && (s.open || s.running) {
			t.Errorf("%s state %+v", id, s)
		}
	}
}

func TestRunNowStopsWithThePage(t *testing.T) {
	f := runFixture(t, true)
	release := make(chan struct{})
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		<-release
		return api.ScriptRunResult{Action: api.ScriptActionNone}
	})
	f.click("立即运行 同步任务")
	testkit.UntilText(t, f.tt, "运行中…")
	f.w.SetActive(false)
	if len(f.w.runs) != 0 {
		t.Error("runs kept after the page stopped")
	}
	close(release)
	f.tt.Frame()
}

func TestRunNowCloseButtonReturnsFocus(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 5, Output: "done"}
	})
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "done")
	f.click("关闭")
	tt.Frame()
	tt.Frame()
	if tt.HasText("done") {
		t.Fatal("popover open")
	}
	if !tt.Focused("立即运行 同步任务") {
		t.Error("the run button does not have the keyboard after 关闭")
	}
	if s := f.w.runs["s-sync"]; s != nil && s.refocus {
		t.Error("refocus left set")
	}
	// The next run keeps the focus on its own Close button.
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "done")
	if !tt.Focused("关闭") {
		t.Error("关闭 lost the focus to the anchor")
	}
}

func TestRunNowFocusReturnsToButton(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 5, Output: "done"}
	})
	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "done")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if tt.HasText("done") {
		t.Fatal("popover open")
	}
	if !tt.Focused("立即运行 同步任务") {
		t.Error("the run button does not have the keyboard after the popover closed")
	}
}

func TestRunNowFromContextMenu(t *testing.T) {
	f := runFixture(t, true)
	tt := f.tt
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 100, Output: "from the menu"}
	})
	if err := tt.RightClick("同步任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	f.click("立即运行")
	testkit.UntilText(t, tt, "from the menu")
	if r := f.runs(); len(r) != 1 || r[0].Tag != "s-sync" {
		t.Fatalf("runs %+v", r)
	}
	// A script that cannot run has the item disabled.
	f.click("关闭")
	if err := tt.RightClick("停用定时"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	_ = tt.Click("立即运行")
	tt.Frame()
	if len(f.runs()) != 1 {
		t.Errorf("a disabled item ran: %+v", f.runs())
	}
}

// shootBoth saves the screen in the light and the dark look when
// SCRIPT_PREVIEW_DIR is set.
func shootBoth(t *testing.T, tt *ui.Tester, name string) {
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

// hover moves the pointer onto label, as a click after the clock moved
// needs the element under it first.
func hover(tt *ui.Tester, label string) {
	if r, ok := tt.Find(label); ok {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
}

func TestCronAcceptanceScreens(t *testing.T) {
	doc := cronDoc(true)
	doc.HTTPTools.Scripts = append(doc.HTTPTools.Scripts, &model.HTTPScript{ID: "s-hour", Name: "整点检查", Tag: "hourly", Type: model.ScriptCron, Cron: "@hourly", Source: "$done()\n"})
	f := newInactiveFixture(t, doc, 1440, 800)
	tt := f.tt
	tomorrow := time.Date(f.now.Year(), f.now.Month(), f.now.Day()+1, 18, 30, 0, 0, f.now.Location())
	next := f.now.Truncate(time.Hour).Add(time.Hour)
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{
		{Tag: "s-ua", Type: api.ScriptTypeRequest, Runs: 128, Errors: 3},
		{Tag: "s-sync", Type: api.ScriptTypeGeneric, Runs: 14},
		{Tag: "daily", Type: api.ScriptTypeCron, Runs: 9, NextRunMS: tomorrow.UnixMilli()},
		{Tag: "hourly", Type: api.ScriptTypeCron, Runs: 312, Errors: 1, NextRunMS: next.UnixMilli()},
	}})
	f.activate()
	testkit.Until(t, tt, func() bool { return tt.HasText("明天 18:30") })
	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 18400, Output: "synced 3 items",
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelInfo, Message: "start"}, {Level: api.ScriptLogLevelWarn, Message: "slow upstream: 1.8 s"}}}
	})
	shootBoth(t, tt, "30-cron-list")

	f.click("立即运行 同步任务")
	testkit.UntilText(t, tt, "synced 3 items")
	shootBoth(t, tt, "33-run-now-success")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()

	f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
		return api.ScriptRunResult{Action: api.ScriptActionError, DurationUS: 2100, Error: "TypeError: Cannot read property 'token' of undefined\n    at main (daily.js:7:12)",
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelError, Message: "sign-in failed"}}}
	})
	hover(tt, "立即运行 每日签到")
	f.click("立即运行 每日签到")
	testkit.UntilText(t, tt, "重试")
	shootBoth(t, tt, "34-run-now-error")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()

	f.w.editors.Edit("s-tick")
	testkit.UntilText(t, tt, "编辑脚本")
	shootBoth(t, tt, "31-cron-editor")
	f.w.editors.Close()
	tt.Frame()

	f.w.Add(Seed{Type: model.ScriptCron})
	testkit.UntilText(t, tt, "添加脚本")
	f.w.editors.ed.cron = "61 * * * *"
	hover(tt, "保存")
	f.click("保存")
	testkit.UntilText(t, tt, "分钟段无效")
	shootBoth(t, tt, "32-cron-editor-invalid")
}

func TestNarrowListFitsAndRunsFromMenu(t *testing.T) {
	for _, width := range []int{360, 480, 560, 640, 700} {
		f := newInactiveFixture(t, cronDoc(true), width, 600)
		f.srv.SetScriptStatus(runningStatus(0))
		f.activate()
		testkit.Until(t, f.tt, func() bool { return f.w.statsOK })
		f.srv.SetScriptRun(func(testkit.ScriptRunCall) api.ScriptRunResult {
			return api.ScriptRunResult{Action: api.ScriptActionNone, DurationUS: 5, Output: "narrow ok"}
		})
		for _, label := range []string{"启用 同步任务", "编辑 同步任务", "更多 同步任务"} {
			r, ok := f.tt.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(width) {
				t.Errorf("%d: %q at %+v outside the window", width, label, r)
			}
		}
		f.click("更多 同步任务")
		f.click("立即运行")
		testkit.UntilText(t, f.tt, "narrow ok")
		if r := f.runs(); len(r) != 1 || r[0].Tag != "s-sync" {
			t.Errorf("%d: runs %+v", width, r)
		}
	}
}
