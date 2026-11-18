package scripts

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const uaSource = "$request.headers['User-Agent'] = 'Kiko'\n$done({headers: $request.headers})\n"

// fixtureDoc is a document with decryption as given and one script of each
// type; one has a pattern that does not compile, one is disabled.
func fixtureDoc(mitm bool) *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: mitm}, Scripts: []*model.HTTPScript{
		{ID: "s-ua", Name: "统一 UA", Type: model.ScriptHTTPRequest, Pattern: `^https://api\.example\.com/`, Source: uaSource},
		{ID: "s-json", Name: "价格归零", Tag: "price", Type: model.ScriptHTTPResponse, Pattern: `^https://api\.example\.com/items`,
			RequiresBody: true, Source: "let body = JSON.parse($response.body)\nbody.price = 0\n$done({body: JSON.stringify(body)})\n"},
		{ID: "s-job", Type: model.ScriptGeneric, Disabled: true, Source: "console.log('job')\n$done({})\n"},
		{ID: "s-bad", Name: "坏模式", Type: model.ScriptHTTPRequest, Pattern: "(", Source: "$done({})\n"},
	}}
	return doc
}

type fixture struct {
	t      *testing.T
	srv    *testkit.Server
	env    *env.Env
	store  *store.Store
	w      *Workspace
	tt     *ui.Tester
	search string
	pages  []string
	// now is the clock of the last frame.
	now time.Time
}

func newFixture(t *testing.T, doc *model.Doc, width, height int) *fixture {
	t.Helper()
	f := newInactiveFixture(t, doc, width, height)
	f.activate()
	return f
}

// activate shows the page: from here the counters are read. A test that
// serves counters sets them before it.
func (f *fixture) activate() {
	f.t.Helper()
	f.w.SetActive(true)
	f.tt.Frame()
}

// newInactiveFixture is newFixture without showing the page.
func newInactiveFixture(t *testing.T, doc *model.Doc, width, height int) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t)}
	raw, _ := json.Marshal(doc)
	f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	f.env, _ = testkit.NewEnv(f.srv)
	f.env.SetNavigator(func(page string, arg any) {
		s, _ := arg.(string)
		f.pages = append(f.pages, page+":"+s)
	})
	f.store = store.New(f.env)
	f.w = New(f.env, f.store)
	f.store.Load()
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		f.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		if f.store.Loaded {
			f.host(c)
		}
	}, width, height)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded })
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	return f
}

// host draws the workspace as the Rewrites page does.
func (f *fixture) host(c *ui.Context) {
	f.now = c.Now()
	defer f.w.Editors().View(c)
	kit.Workspace(c, func() {
		if f.w.InSub() {
			f.w.SubView(c)
			return
		}
		ui.Row(c).PaddingX(16).PaddingY(8).Gap(10).Shrink(0).Children(func() {
			ui.Spacer(c)
			kit.Search(c, "scripts-search", &f.search, kit.SearchOptions{Placeholder: "搜索名称、标识、模式", Toolbar: true, Width: 260})
			f.w.Toolbar(c)
		})
		f.w.Body(c, f.search, func(c *ui.Context) {
			kit.Button(c, "前往 HTTPS 解密设置", kit.ButtonOptions{Small: true}).OnClick(func() { f.env.Navigate(env.PageSettings, "https") })
		})
	})
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
}

func (f *fixture) scripts() []*model.HTTPScript { return f.store.Doc().HTTPTools.Scripts }

func (f *fixture) settled() {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return !f.store.Saving() })
}

func (f *fixture) want(texts ...string) {
	f.t.Helper()
	for _, s := range texts {
		if !f.tt.HasText(s) {
			f.t.Errorf("missing %q in %q", s, f.tt.Texts())
		}
	}
}

func TestListRowsAndSearch(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.want("价格归零", "s-job", "坏模式", "请求", "响应", "通用", "已停用", "4 个脚本",
		`^https://api\.example\.com/items`, fixtureDoc(true).HTTPTools.Scripts[3].Issue())
	if tt.HasText("未开启 HTTPS 解密") || tt.HasText("核心不支持脚本") {
		t.Errorf("notice with decryption on: %q", tt.Texts())
	}
	f.click("搜索名称、标识、模式")
	tt.Type("price")
	testkit.Until(t, tt, func() bool { return !tt.HasText("统一 UA") })
	f.want("价格归零", "1 / 4 个脚本")
	f.click("清除搜索")
	testkit.UntilText(t, tt, "统一 UA")
	// The pattern is searched too.
	f.search = "items"
	testkit.Until(t, tt, func() bool { return !tt.HasText("统一 UA") && tt.HasText("价格归零") })
	f.search = "nothing-here"
	testkit.UntilText(t, tt, "没有符合条件的脚本")
}

func TestStatsPolling(t *testing.T) {
	f := newInactiveFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	// The first poll comes as the page shows, so the counters are set before.
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{
		{Tag: "s-ua", Type: "http-request", Runs: 128, Errors: 3},
		{Tag: "price", Type: "http-response", Runs: 7, Paused: true},
	}})
	f.activate()
	// The counters come with the first poll.
	testkit.Until(t, tt, func() bool { return tt.HasText("128") })
	// The error count sits beside the run count of its own row.
	f.want("已暂停")
	if !statsRow(tt, "128", "3") {
		t.Errorf("run and error counts not shown together: %q", tt.Texts())
	}
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "s-ua", Type: "http-request", Runs: 129}}})
	tt.Advance(pollEvery + 100*time.Millisecond)
	testkit.Until(t, tt, func() bool { return tt.HasText("129") })
	if tt.HasText("已暂停") {
		t.Errorf("paused after the script ran again: %q", tt.Texts())
	}
	// A failure shows dashes, no toast.
	f.srv.SetScriptsUnavailable(true)
	tt.Advance(pollEvery + 100*time.Millisecond)
	testkit.Until(t, tt, func() bool { return !tt.HasText("129") })
	f.srv.SetScriptsUnavailable(false)
	tt.Advance(pollEvery + 100*time.Millisecond)
	testkit.Until(t, tt, func() bool { return tt.HasText("129") })
	// Nothing is read while the page is away.
	f.w.SetActive(false)
	before := len(f.srv.Requests())
	tt.Advance(4 * pollEvery)
	tt.Frame()
	if n := len(f.srv.Requests()); n != before {
		t.Errorf("polled while inactive: %d requests more", n-before)
	}
}

func TestNoPollingWithoutDecryption(t *testing.T) {
	f := newFixture(t, fixtureDoc(false), 1280, 700)
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "s-ua", Runs: 128}}})
	f.tt.Advance(2 * pollEvery)
	f.tt.Frame()
	for _, r := range f.srv.Requests() {
		if strings.Contains(r, "/scripts/status") {
			t.Fatalf("polled with decryption off: %v", f.srv.Requests())
		}
	}
	f.want("未开启 HTTPS 解密", "统一 UA")
}

func TestEmptyStates(t *testing.T) {
	off := newFixture(t, model.EmptyDoc(model.PlatformLinux), 1000, 600)
	testkit.UntilText(t, off.tt, "未开启 HTTPS 解密")
	off.click("前往 HTTPS 解密设置")
	if len(off.pages) != 1 || off.pages[0] != "settings:https" {
		t.Fatalf("navigate %v", off.pages)
	}
	off.click("添加脚本")
	testkit.UntilText(t, off.tt, "添加脚本")

	on := model.EmptyDoc(model.PlatformLinux)
	on.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	empty := newFixture(t, on, 1000, 600)
	testkit.UntilText(t, empty.tt, "暂无脚本")
	if empty.tt.HasText("未开启 HTTPS 解密") {
		t.Fatal("decryption state shown while on")
	}

	notice := newFixture(t, fixtureDoc(false), 1000, 600)
	testkit.UntilText(t, notice.tt, "统一 UA")
	notice.want("未开启 HTTPS 解密")

	// A core without scripts says so above the list.
	un := newFixture(t, fixtureDoc(true), 1000, 600)
	un.env.Server.Info = &api.ServerInfo{Capabilities: map[string]bool{"scripts": false}}
	testkit.UntilText(t, un.tt, "核心不支持脚本")
	un.env.Server.Info = &api.ServerInfo{Capabilities: map[string]bool{"scripts": true}}
	testkit.Until(t, un.tt, func() bool { return !un.tt.HasText("核心不支持脚本") })
	un.env.Server.Info = &api.ServerInfo{Capabilities: map[string]bool{}}
	un.tt.Frame()
	if un.tt.HasText("核心不支持脚本") {
		t.Fatal("unsupported without the capability listed")
	}
}

func TestMoveToggleDeleteDuplicate(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.click("下移 统一 UA")
	testkit.Until(t, tt, func() bool { return f.scripts()[0].ID == "s-json" })
	f.settled()
	f.click("上移 统一 UA")
	testkit.Until(t, tt, func() bool { return f.scripts()[0].ID == "s-ua" })

	f.click("启用 统一 UA")
	testkit.Until(t, tt, func() bool { return f.scripts()[0].Disabled })
	f.click("启用 统一 UA")
	testkit.Until(t, tt, func() bool { return !f.scripts()[0].Disabled })

	// A duplicate sits below the original with its own id and tag.
	f.store.Update(func(d *model.Doc) { duplicateScript(d, "s-json") })
	list := f.scripts()
	if len(list) != 5 || list[2].Name != "价格归零 副本" || list[2].ID == "s-json" || list[2].Tag != "price-2" || list[2].Source != list[1].Source {
		t.Fatalf("duplicate: %+v", list)
	}
	if !strings.HasPrefix(list[2].ID, "script_") {
		t.Fatalf("new id %q", list[2].ID)
	}

	// Deleting asks first, anchored on the row's More button.
	f.w.target, f.w.confirm.Open = "s-ua", true
	testkit.UntilText(t, tt, "删除「统一 UA」？")
	f.click("取消")
	testkit.Until(t, tt, func() bool { return !tt.HasText("删除「统一 UA」？") })
	if len(f.scripts()) != 5 {
		t.Fatal("cancel deleted")
	}
	f.w.target, f.w.confirm.Open = "s-ua", true
	testkit.UntilText(t, tt, "删除「统一 UA」？")
	f.click("删除")
	testkit.Until(t, tt, func() bool { return len(f.scripts()) == 4 })
	if f.scripts()[0].ID != "s-json" {
		t.Fatalf("after delete: %+v", f.scripts())
	}
}

func TestMoveDisabledWhileSearching(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	testkit.UntilText(t, f.tt, "统一 UA")
	f.search = "UA"
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("价格归零") })
	f.tt.Click("下移 统一 UA")
	f.tt.Frame()
	if f.scripts()[0].ID != "s-ua" {
		t.Fatalf("moved while searching: %+v", f.scripts())
	}
}

func TestAddSaveAndValidation(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.click("添加脚本")
	testkit.UntilText(t, tt, "添加脚本")
	e := f.w.editors.ed
	// The source starts as the type's template.
	if !strings.Contains(e.code.Text(), "$request.headers") || !strings.Contains(e.code.Text(), "$done") {
		t.Fatalf("template %q", e.code.Text())
	}
	// A required field says so only after a save was tried.
	if tt.HasText("请填写匹配模式") {
		t.Fatal("required message before saving")
	}
	f.click("保存")
	testkit.UntilText(t, tt, "请填写匹配模式")
	if len(f.scripts()) != 4 {
		t.Fatal("saved an invalid script")
	}
	f.click("匹配模式（RE2）")
	tt.Type("(")
	testkit.UntilText(t, tt, "匹配模式无效")
	e.pattern = `^https://(\w+)\.example\.com/`
	e.test = "https://api.example.com/v1"
	testkit.UntilText(t, tt, "命中")
	e.test = "https://other.org/"
	testkit.UntilText(t, tt, "未命中")

	// The tag is checked for characters and for taking another's.
	e.tag = "bad tag"
	testkit.UntilText(t, tt, "标识只能含字母、数字、点、下划线和连字符")
	e.tag = "price"
	testkit.UntilText(t, tt, "标识已被其他脚本使用")
	e.tag = "s-ua"
	testkit.UntilText(t, tt, "标识已被其他脚本使用")
	e.tag = "mine.v1"
	e.name = "我的脚本"
	e.timeout = "99"
	testkit.UntilText(t, tt, "超时须在 1 到 30 秒之间")
	e.timeout = "5"
	e.requires, e.binary = true, true
	e.maxSize = "512"
	e.argument = "a=1"
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.scripts()) == 5 })
	got := f.scripts()[4]
	if got.Name != "我的脚本" || got.Tag != "mine.v1" || got.Type != model.ScriptHTTPRequest || got.Pattern != `^https://(\w+)\.example\.com/` ||
		!got.RequiresBody || !got.BinaryBody || got.MaxSize != 512*1024 || got.Timeout != 5 || got.Argument != "a=1" ||
		!strings.HasPrefix(got.ID, "script_") || got.Source != template(model.ScriptHTTPRequest) || got.Disabled {
		t.Fatalf("saved %+v", got)
	}
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })
	// The new script is chosen in the list.
	testkit.UntilText(t, tt, "我的脚本")
	if f.w.table.Selected != 4 {
		t.Errorf("selected %d", f.w.table.Selected)
	}
}

func TestTypeSwitchKeepsEditedSource(t *testing.T) {
	on := model.EmptyDoc(model.PlatformLinux)
	on.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	f := newFixture(t, on, 1280, 900)
	testkit.UntilText(t, f.tt, "暂无脚本")
	f.w.Add(Seed{})
	testkit.UntilText(t, f.tt, "添加脚本")
	e := f.w.editors.ed
	f.click("响应")
	testkit.Until(t, f.tt, func() bool { return strings.Contains(e.code.Text(), "$response") })
	f.click("通用")
	testkit.Until(t, f.tt, func() bool { return !e.http() })
	if f.tt.HasText("匹配模式") {
		t.Errorf("pattern field for a generic script: %q", f.tt.Texts())
	}
	e.code.SetText("console.log(1)\n$done({})\n")
	f.click("请求")
	testkit.Until(t, f.tt, func() bool { return e.http() })
	if e.code.Text() != "console.log(1)\n$done({})\n" {
		t.Errorf("edited source replaced: %q", e.code.Text())
	}
}

func TestSourceProblemShown(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	testkit.UntilText(t, f.tt, "统一 UA")
	f.w.Add(Seed{Type: model.ScriptGeneric})
	testkit.UntilText(t, f.tt, "添加脚本")
	e := f.w.editors.ed
	e.code.SetText(strings.Repeat("(", model.MaxScriptDepth+10))
	f.tt.Frame()
	issue := sourceIssue(e.code.Text())
	if issue == "" {
		t.Fatal("deep source accepted by the model")
	}
	testkit.UntilText(t, f.tt, issue)
	f.click("保存")
	f.tt.Frame()
	if len(f.scripts()) != 4 {
		t.Fatal("saved a script with a source problem")
	}
	e.code.SetText("")
	f.tt.Frame()
	if f.tt.HasText(issue) {
		t.Error("old problem still shown")
	}
	f.click("保存")
	testkit.UntilText(t, f.tt, "请填写脚本内容")
}

func TestEditKeepsFieldsAndEscape(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.click("编辑 价格归零")
	testkit.UntilText(t, tt, "编辑脚本")
	e := f.w.editors.ed
	if e.name != "价格归零" || e.tag != "price" || e.typ != 1 || !e.requires || e.code.Text() != f.scripts()[1].Source {
		t.Fatalf("draft %+v", e)
	}
	// Escape closes without saving, and the list gets the focus back.
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })
	if f.store.Saving() {
		t.Fatal("saved on escape")
	}
	// The table has the keyboard again: Down chooses a row, Enter edits it.
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	testkit.Until(t, tt, func() bool { return f.w.editors.Open() })
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })

	// A changed draft asks before it is dropped.
	f.click("编辑 价格归零")
	testkit.UntilText(t, tt, "编辑脚本")
	f.w.editors.ed.name = "改过的名字"
	tt.Key(0, ui.KeyEscape)
	testkit.UntilText(t, tt, "放弃未保存的更改？")
	if !f.w.editors.Open() {
		t.Fatal("closed without asking")
	}
	f.click("放弃")
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })
	if f.scripts()[1].Name != "价格归零" {
		t.Fatalf("name saved: %+v", f.scripts()[1])
	}

	// An edit saves into the same script.
	f.click("编辑 价格归零")
	testkit.UntilText(t, tt, "编辑脚本")
	f.w.editors.ed.name = "改过的名字"
	f.click("保存")
	testkit.Until(t, tt, func() bool { return f.scripts()[1].Name == "改过的名字" })
	if len(f.scripts()) != 4 || f.scripts()[1].ID != "s-json" || f.scripts()[1].Tag != "price" {
		t.Fatalf("after edit: %+v", f.scripts())
	}
}

// Escape reaches the dialog from the focused source editor, and a selection
// in it takes the first Escape.
func TestEscapeFromSourceEditor(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.click("编辑 统一 UA")
	testkit.UntilText(t, tt, "编辑脚本")
	e := f.w.editors.ed
	e.code.Focus()
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })
}

func TestSaveDoesNotResurrectDeletedScript(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	testkit.UntilText(t, f.tt, "统一 UA")
	f.click("编辑 统一 UA")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.store.Update(func(d *model.Doc) { removeScript(d, "s-ua") })
	f.click("保存")
	testkit.UntilText(t, f.tt, "脚本已被删除")
	for _, s := range f.scripts() {
		if s.ID == "s-ua" {
			t.Fatal("resurrected")
		}
	}
}

func TestEditGuardHeldWhileOpen(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	testkit.UntilText(t, f.tt, "统一 UA")
	f.click("编辑 统一 UA")
	testkit.UntilText(t, f.tt, "编辑脚本")
	if !f.w.editors.guarded {
		t.Fatal("no edit guard while open")
	}
	f.w.SetActive(false)
	f.tt.Frame()
	if f.w.editors.Open() || f.w.editors.guarded {
		t.Fatal("editor open after the page stopped")
	}
}

func TestSubPagesAndBack(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.click("存储")
	testkit.UntilText(t, tt, "脚本")
	if tt.HasText("统一 UA") || !f.w.InSub() {
		t.Fatalf("list under the store page: %q", tt.Texts())
	}
	f.want("存储")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.InSub() })
	testkit.UntilText(t, tt, "统一 UA")

	f.click("日志")
	testkit.Until(t, tt, func() bool { return f.w.InSub() })
	f.want("日志")
	f.click("脚本")
	testkit.Until(t, tt, func() bool { return !f.w.InSub() })

	// The row menu opens the log of one script.
	f.w.OpenLogs("price")
	tt.Frame()
	if f.w.sub != subLogs || f.w.logs.tag != "price" {
		t.Fatalf("sub %d tag %q", f.w.sub, f.w.logs.tag)
	}
	// The toolbar's Logs button shows all.
	f.w.Back()
	tt.Frame()
	f.click("日志")
	if f.w.logs.tag != "" {
		t.Fatalf("tag %q", f.w.logs.tag)
	}
	// Going away resets to the list.
	f.w.SetActive(false)
	if f.w.InSub() {
		t.Fatal("sub page kept after the page stopped")
	}
}

func TestHelpers(t *testing.T) {
	if got := Duration(850); got != "850 µs" {
		t.Error(got)
	}
	if got := Duration(12500); got != "12.5 ms" {
		t.Error(got)
	}
	if got := Duration(2_345_000); got != "2.35 s" {
		t.Error(got)
	}
	for action, want := range map[string]string{"modify": "已修改", "respond": "已应答", "abort": "已中止", "none": "未修改", "error": "脚本出错",
		"timeout": "脚本超时", "limit": "超出资源限制", "paused": "已暂停", "skipped": "未运行", "x": "x"} {
		if got := ActionLabel(action); got != want {
			t.Errorf("%s: %q", action, got)
		}
	}
	if SkipLabel("size") != "超出缓冲上限" || SkipLabel("") != "已跳过" || SkipLabel("odd") != "odd" {
		t.Error("skip labels")
	}
	if TypeLabel(model.ScriptHTTPRequest) != "请求" || TypeLabel(model.ScriptGeneric) != "通用" {
		t.Error("type labels")
	}
	tp := TestPattern(`^https://a\.com/`, "https://a.com/x")
	if !tp.Tried || !tp.Hit || TestPattern("(", "x").Err == "" || TestPattern("", "x").Tried {
		t.Errorf("pattern test %+v", tp)
	}
	list := []*model.HTTPScript{{ID: "a", Tag: "t"}, {ID: "b", Tag: "t-2"}}
	if uniqueTag(list, "t") != "t-3" || uniqueTag(list, "u") != "u" {
		t.Error("unique tag")
	}
	long := strings.Repeat("x", model.MaxScriptTag)
	if got := uniqueTag([]*model.HTTPScript{{ID: "a", Tag: long}}, long); len(got) != model.MaxScriptTag || got == long {
		t.Errorf("long tag copy %q", got)
	}
	if h, err := parseHeaders("A: 1\n\nB:  two:three \r\n"); err != nil || len(h) != 2 || h[1].Value != "two:three" {
		t.Errorf("headers %v %v", h, err)
	}
	if _, err := parseHeaders("A: 1\nbroken"); err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Errorf("broken header line: %v", err)
	}
}

// route installs the fake record endpoints of the test area.
func routeRecords(srv *testkit.Server, records []api.HTTPRecord, bodies map[string][]byte, queries *[]string) {
	srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v1/http/records"), "/")
		if rest == "" {
			*queries = append(*queries, r.URL.Query().Get("search"))
			var out []api.HTTPRecord
			q := r.URL.Query().Get("search")
			for _, rec := range records {
				if q == "" || strings.Contains(rec.URL, q) {
					out = append(out, rec)
				}
			}
			testkit.JSON(api.HTTPList{Records: out})(w, r)
			return
		}
		id, tail, _ := strings.Cut(rest, "/")
		if tail == "body" {
			data := bodies[id+"/"+r.URL.Query().Get("side")]
			h := w.Header()
			h.Set("X-Total", itoa(len(data)))
			h.Set("X-Original-Size", itoa(len(data)))
			h.Set("X-Saved", "true")
			h.Set("X-Complete", "true")
			_, _ = w.Write(data)
			return
		}
		for _, rec := range records {
			if rec.ID == id {
				testkit.JSON(rec)(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// statsRow reports whether the texts of the window hold runs followed
// right away by errors.
func statsRow(tt *ui.Tester, runs, errs string) bool {
	texts := tt.Texts()
	for i, s := range texts {
		if s == runs && i+1 < len(texts) && texts[i+1] == errs {
			return true
		}
	}
	return false
}

// Turning decryption off clears the counters; a poll in flight does not
// bring them back.
func TestStatsClearWhenDecryptionTurnsOff(t *testing.T) {
	f := newInactiveFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "s-ua", Type: "http-request", Runs: 128}}})
	f.activate()
	testkit.Until(t, tt, func() bool { return tt.HasText("128") })
	f.store.Update(func(d *model.Doc) { d.HTTPTools.MITM.Enabled = false })
	testkit.Until(t, tt, func() bool { return !tt.HasText("128") })
	if f.w.statsOK || len(f.w.stats) != 0 || f.w.polling {
		t.Fatalf("stats kept: ok %v stats %v polling %v", f.w.statsOK, f.w.stats, f.w.polling)
	}
	f.store.Update(func(d *model.Doc) { d.HTTPTools.MITM.Enabled = true })
	testkit.Until(t, tt, func() bool { return tt.HasText("128") })
}
