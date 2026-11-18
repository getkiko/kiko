package rewrites

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"path/filepath"
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

// fixtureDoc is a document with decryption on and one rule list of each
// kind; one URL rule has a pattern that does not compile.
func fixtureDoc(mitm bool) *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: mitm}, Rewrites: &model.HTTPRewrites{
		URL: []*model.URLRewrite{
			{ID: "u-ads", Name: "广告拦截", Pattern: `^https?://ads\.example\.com/`, Action: model.URLActionReject},
			{ID: "u-old", Name: "旧域名跳转", Pattern: `^https://old\.example\.com/(.*)$`, Action: model.URLActionFound, Target: "https://new.example.com/$1"},
			{ID: "u-bad", Pattern: `(`, Action: model.URLActionReject},
		},
		Headers: []*model.HeaderRewrite{
			{ID: "h-ua", Name: "统一 UA", Pattern: `^https://api\.example\.com/`, Scope: model.HeaderScopeRequest, Ops: []*model.HeaderEdit{
				{Action: model.HeaderActionReplace, Name: "User-Agent", Value: "Kiko/1"},
				{Action: model.HeaderActionDelete, Name: "Cookie"}}},
			{ID: "h-cors", Name: "放开跨域", Pattern: `^https://api\.example\.com/`, Scope: model.HeaderScopeResponse, Ops: []*model.HeaderEdit{
				{Action: model.HeaderActionAdd, Name: "Access-Control-Allow-Origin", Value: "*"}}},
		},
		MapLocal: []*model.MapLocal{
			{ID: "m-cfg", Name: "本地配置", Pattern: `^https://api\.example\.com/config$`, Status: 200,
				Headers: []model.HeaderField{{Name: "Content-Type", Value: "application/json"}}, Body: `{"ok":true}`},
		},
		Body: []*model.BodyRewrite{
			{ID: "b-price", Name: "价格改写", Pattern: `^https://api\.example\.com/items`, Scope: model.HeaderScopeResponse,
				ContentTypes: []string{"json"}, Edits: []*model.BodyEdit{
					{Action: model.BodyActionJSONSet, Path: "$.data.price", Value: "0"},
					{Action: model.BodyActionJSONDelete, Path: "$..ads"}}},
			{ID: "b-html", Pattern: `^https://www\.example\.com/`, Scope: model.HeaderScopeResponse, Edits: []*model.BodyEdit{
				{Action: model.BodyActionReplaceRegex, Regex: `<title>(.*)</title>`, Value: "<title>$1 !</title>"}}},
		},
	}}
	return doc
}

type fixture struct {
	t     *testing.T
	srv   *testkit.Server
	env   *env.Env
	shell *testkit.Shell
	store *store.Store
	w     *Workspace
	tt    *ui.Tester
	pages []string
}

func newFixture(t *testing.T, doc *model.Doc, width, height int) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t)}
	raw, _ := json.Marshal(doc)
	f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	f.env, f.shell = testkit.NewEnv(f.srv)
	f.env.SetNavigator(func(page string, arg any) { f.pages = append(f.pages, page+":"+toString(arg)) })
	f.store = store.New(f.env)
	f.w = New(f.env, f.store)
	f.store.Load()
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		f.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		f.w.View(c)
	}, width, height)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded })
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	return f
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
}

func (f *fixture) rules() *model.HTTPRewrites { return f.store.Doc().HTTPTools.Rewrites }

func (f *fixture) settled() {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return !f.store.Saving() })
}

func TestWorkspaceTabsAndRows(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	for _, want := range []string{"旧域名跳转", "302 跳转 → https://new.example.com/$1", "匹配模式无效", "3 条规则", "URL 重写", "请求头", "本地映射"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("未开启 HTTPS 解密") {
		t.Error("notice with decryption on")
	}
	f.click("请求头")
	testkit.UntilText(t, tt, "统一 UA")
	for _, want := range []string{"请求 · 替换 User-Agent、删除 Cookie", "响应 · 添加 Access-Control-Allow-Origin", "2 条规则"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	f.click("本地映射")
	testkit.UntilText(t, tt, "本地配置")
	if !tt.HasText("200 · application/json · 11 B") {
		t.Errorf("summary: %q", tt.Texts())
	}
	// Search narrows the list.
	f.click("URL 重写")
	f.click("搜索名称、模式、动作")
	tt.Type("old")
	testkit.Until(t, tt, func() bool { return !tt.HasText("广告拦截") })
	if !tt.HasText("旧域名跳转") || !tt.HasText("1 / 3 条规则") {
		t.Fatalf("search: %q", tt.Texts())
	}
	f.click("清除搜索")
	testkit.UntilText(t, tt, "广告拦截")
}

func TestBodyTabRowsAndBuffer(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.click("正文")
	testkit.UntilText(t, tt, "价格改写")
	for _, want := range []string{"b-html", "响应", "2 条规则", "缓冲 2.0 MB"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	f.w.update(func(d *model.Doc) { ensure(d).MaxBodySize = 4 << 20 })
	testkit.Until(t, tt, func() bool { return tt.HasText("缓冲 4.0 MB") })
	f.click("上移 价格改写")
	if f.rules().Body[0].ID != "b-price" {
		t.Fatal("first row moved up")
	}
	f.click("下移 价格改写")
	testkit.Until(t, tt, func() bool { return f.rules().Body[1].ID == "b-price" })
}

func TestBodyEditor(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.click("正文")
	f.w.editors.New(KindBody, Seed{Pattern: `^https://b\.example\.com/`})
	testkit.UntilText(t, tt, "添加 正文")
	f.click("保存")
	testkit.UntilText(t, tt, "请填写正则表达式")
	e := f.w.editors.ed
	e.scope = 1
	e.contentTypes = "json, text/plain"
	e.edits[0].regex = "("
	testkit.UntilText(t, tt, "正则表达式无效")
	e.edits[0].regex, e.edits[0].value = `"n":(\d+)`, `"n":$1`
	set := e.addEdit(model.BodyActionJSONSet)
	set.path, set.value = "$.a[", "{"
	testkit.UntilText(t, tt, "路径缺少 ]")
	set.path = "$.a[0]"
	testkit.UntilText(t, tt, "JSON 值无效")
	set.value = `{"k":[1,2]}`
	del := e.addEdit(model.BodyActionJSONDelete)
	del.path = "$..x"
	tt.Frame()
	f.click("上移编辑 3")
	testkit.Until(t, tt, func() bool { return e.edits[1].action == model.BodyActionJSONDelete })
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.rules().Body) == 3 })
	got := f.rules().Body[2]
	if got.Scope != model.HeaderScopeResponse || len(got.Edits) != 3 || got.Edits[1].Action != model.BodyActionJSONDelete || got.Edits[1].Value != "" ||
		got.Edits[2].Value != `{"k":[1,2]}` || strings.Join(got.ContentTypes, "|") != "json|text/plain" || !strings.HasPrefix(got.ID, "body_") {
		t.Fatalf("saved %+v", got)
	}
	// Edit and Escape keep the rule.
	f.click("编辑 价格改写")
	testkit.UntilText(t, tt, "编辑 正文")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })
}

func TestURLHeaderActionEditor(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 800)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.w.editors.New(KindURL, Seed{Pattern: `^https://a\.example\.com/(.*)$`, TestURL: "https://a.example.com/x"})
	testkit.UntilText(t, tt, "添加 URL 重写")
	e := f.w.editors.ed
	e.action, e.target = model.URLActionHeader, "https://u:p@in.example.com"
	testkit.UntilText(t, tt, "目标地址不能含有用户信息")
	e.target = "https://in.example.com/$1"
	testkit.UntilText(t, tt, "https://in.example.com/x")
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.rules().URL) == 4 })
	got := f.rules().URL[3]
	if got.Action != model.URLActionHeader || got.Target != "https://in.example.com/$1" {
		t.Fatalf("saved %+v", got)
	}
	f.click("URL 重写")
	testkit.UntilText(t, tt, "改写主机（内部） → https://in.example.com/$1")
}

func TestMoveToggleDeleteDuplicate(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.click("下移 广告拦截")
	testkit.Until(t, tt, func() bool { return f.rules().URL[0].ID == "u-old" })
	f.settled()
	if f.rules().URL[1].ID != "u-ads" {
		t.Fatalf("order after move: %v", f.rules().URL)
	}
	f.click("上移 广告拦截")
	testkit.Until(t, tt, func() bool { return f.rules().URL[0].ID == "u-ads" })

	f.click("启用 广告拦截")
	testkit.Until(t, tt, func() bool { return f.rules().URL[0].Disabled })
	testkit.UntilText(t, tt, "已停用")
	f.click("启用 广告拦截")
	testkit.Until(t, tt, func() bool { return !f.rules().URL[0].Disabled })

	// Duplicate sits below the original with its own id.
	f.store.Update(func(d *model.Doc) { duplicateRule(d, KindURL, "u-old") })
	r := f.rules().URL
	if len(r) != 4 || r[2].Name != "旧域名跳转 副本" || r[2].ID == "u-old" || r[2].Target != r[1].Target {
		t.Fatalf("duplicate: %+v", r)
	}

	// Deleting asks first, anchored on the row's More button.
	f.w.target.kind, f.w.target.id, f.w.confirm.Open = KindURL, "u-ads", true
	testkit.UntilText(t, tt, "删除「广告拦截」？")
	f.click("取消")
	testkit.Until(t, tt, func() bool { return !tt.HasText("删除「广告拦截」？") })
	if len(f.rules().URL) != 4 {
		t.Fatal("cancel deleted")
	}
	f.w.target.kind, f.w.target.id, f.w.confirm.Open = KindURL, "u-ads", true
	testkit.UntilText(t, tt, "删除「广告拦截」？")
	f.click("删除")
	testkit.Until(t, tt, func() bool { return len(f.rules().URL) == 3 })
	if f.rules().URL[0].ID != "u-old" {
		t.Fatalf("after delete: %+v", f.rules().URL)
	}
}

func TestEmptyStates(t *testing.T) {
	// Off and no rules: the decryption empty state with its two buttons.
	doc := model.EmptyDoc(model.PlatformLinux)
	f := newFixture(t, doc, 1000, 600)
	testkit.UntilText(t, f.tt, "未开启 HTTPS 解密")
	f.click("前往 HTTPS 解密设置")
	if len(f.pages) != 1 || f.pages[0] != "settings:https" {
		t.Fatalf("navigate %v", f.pages)
	}
	f.click("添加 URL 重写")
	testkit.UntilText(t, f.tt, "添加 URL 重写")

	// On and no rules.
	on := model.EmptyDoc(model.PlatformLinux)
	on.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	f = newFixture(t, on, 1000, 600)
	testkit.UntilText(t, f.tt, "暂无改写")
	if f.tt.HasText("未开启 HTTPS 解密") {
		t.Fatal("decryption state shown while on")
	}

	// Off with rules: the rules list under a notice.
	f = newFixture(t, fixtureDoc(false), 1000, 600)
	testkit.UntilText(t, f.tt, "广告拦截")
	if !f.tt.HasText("未开启 HTTPS 解密") {
		t.Fatalf("notice missing: %q", f.tt.Texts())
	}
	// A tab without rules while others have some.
	f.store.Update(func(d *model.Doc) { d.HTTPTools.Rewrites.MapLocal = nil })
	f.click("本地映射")
	testkit.UntilText(t, f.tt, "暂无改写")
}

func TestEditorValidationAndPatternTest(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 800)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.click("添加规则")
	testkit.UntilText(t, tt, "添加 URL 重写")
	// A required field says so only after a save was tried.
	if tt.HasText("请填写匹配模式") {
		t.Fatal("required message before saving")
	}
	f.click("保存")
	testkit.UntilText(t, tt, "请填写匹配模式")
	if len(f.rules().URL) != 3 {
		t.Fatal("saved an invalid rule")
	}
	f.click("匹配模式（RE2）")
	tt.Type("(")
	testkit.UntilText(t, tt, "匹配模式无效")

	e := f.w.editors.ed
	e.pattern = `^https://(\w+)\.example\.com/(.*)$`
	f.click("测试地址")
	tt.Type("https://api.example.com/v1?x=1")
	testkit.UntilText(t, tt, "命中")
	for _, want := range []string{"$1", "api", "$2", "v1?x=1"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	e.test = "https://other.org/"
	testkit.UntilText(t, tt, "未命中")

	// A redirect needs an absolute http(s) target; the test URL expands it.
	e.action, e.target = model.URLActionFound, "ftp://x"
	e.test = "https://api.example.com/v1"
	testkit.UntilText(t, tt, "跳转地址须为 http 或 https 地址")
	e.target = "https://www.example.com/$1/$2"
	testkit.UntilText(t, tt, "https://www.example.com/api/v1")
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.rules().URL) == 4 })
	got := f.rules().URL[3]
	if got.Action != model.URLActionFound || got.Target != "https://www.example.com/$1/$2" || !strings.HasPrefix(got.ID, "url_") || got.Disabled {
		t.Fatalf("saved %+v", got)
	}
	testkit.Until(t, tt, func() bool { return !tt.HasText("添加 URL 重写") })
	if f.w.editors.Open() {
		t.Fatal("dialog open after saving")
	}
}

func TestEditorEscapeKeepsRules(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	testkit.UntilText(t, f.tt, "广告拦截")
	f.click("编辑 广告拦截")
	testkit.UntilText(t, f.tt, "编辑 URL 重写")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("编辑 URL 重写") })
	if f.w.editors.Open() || len(f.rules().URL) != 3 {
		t.Fatal("escape changed something")
	}
}

func TestHeaderEditor(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 800)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.click("请求头")
	f.w.editors.New(KindHeader, Seed{Pattern: `^https://h\.example\.com/`})
	testkit.UntilText(t, tt, "添加 请求头")
	f.click("保存")
	testkit.UntilText(t, tt, "请填写头名称")
	e := f.w.editors.ed
	e.scope = 1
	op := e.ops[0]
	op.name, op.action = "Server", model.HeaderActionReplaceRegex
	testkit.UntilText(t, tt, "匹配的正则表达式")
	f.click("保存")
	testkit.UntilText(t, tt, "替换表达式无效")
	op.regex, op.value = `nginx/(\d+)`, "edge/$1"
	e.addOp(model.HeaderActionDelete).name = "Via"
	tt.Frame()
	// The second operation moves up.
	f.click("上移操作 2")
	testkit.Until(t, tt, func() bool { return e.ops[0].action == model.HeaderActionDelete })
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.rules().Headers) == 3 })
	got := f.rules().Headers[2]
	if got.Scope != model.HeaderScopeResponse || len(got.Ops) != 2 || got.Ops[0].Name != "Via" || got.Ops[0].Value != "" ||
		got.Ops[1].Regex != `nginx/(\d+)` || got.Ops[1].Value != "edge/$1" {
		t.Fatalf("saved %+v %+v", got, got.Ops)
	}
	// A reserved header is refused.
	f.click("编辑 统一 UA")
	testkit.UntilText(t, tt, "编辑 请求头")
	f.w.editors.ed.ops[0].name = "Host"
	testkit.UntilText(t, tt, "该头由代理管理，不能改写")
	f.click("保存")
	time.Sleep(20 * time.Millisecond)
	if f.rules().Headers[0].Ops[0].Name != "User-Agent" {
		t.Fatal("reserved header saved")
	}
}

func TestMapLocalEditorFiles(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.w.editors.New(KindMapLocal, Seed{Pattern: `^https://m\.example\.com/a\.bin$`})
	testkit.UntilText(t, tt, "添加 本地映射")
	e := f.w.editors.ed

	// A file that cannot be read says so and changes nothing.
	f.shell.OpenErr = errors.New("permission denied")
	f.click("选择文件")
	testkit.UntilText(t, tt, "读取失败：permission denied")
	f.shell.OpenErr = nil

	// A cancelled dialog changes nothing.
	f.click("选择文件")
	testkit.Until(t, tt, func() bool { return !e.importing })
	if e.body != "" || e.binary != "" {
		t.Fatal("cancel changed the body")
	}

	// Beyond the limit: refused with the error beside the body.
	f.shell.Open, f.shell.OpenName = bytes.Repeat([]byte("a"), model.MaxMapLocalBody+1), "huge.txt"
	f.click("选择文件")
	testkit.UntilText(t, tt, "正文超过 2 MiB")
	if e.body != "" {
		t.Fatal("oversize file taken")
	}

	// Text stays text; binary becomes base64.
	f.shell.Open, f.shell.OpenName = []byte(`{"a":1}`), "a.json"
	f.click("选择文件")
	testkit.Until(t, tt, func() bool { return e.body == `{"a":1}` })
	f.shell.Open, f.shell.OpenName = []byte{0, 1, 2, 255}, "a.bin"
	f.click("选择文件")
	testkit.Until(t, tt, func() bool { return e.binary != "" })
	testkit.UntilText(t, tt, "a.bin")
	if e.body != "" || e.bodySize() != 4 {
		t.Fatalf("binary: body %q size %d", e.body, e.bodySize())
	}

	// A status outside 200–599 is refused.
	e.status = "99"
	f.click("保存")
	testkit.UntilText(t, tt, "状态码须在 200–599 之间")
	e.status = "201"
	e.addField("Content-Type", "application/octet-stream")
	f.click("保存")
	testkit.Until(t, tt, func() bool { return len(f.rules().MapLocal) == 2 })
	got := f.rules().MapLocal[1]
	if got.BodyBase64 != "AAEC/w==" || got.Body != "" || got.Status != 201 || len(got.Headers) != 1 {
		t.Fatalf("saved %+v", got)
	}
}

func TestMapLocalTotalLimit(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1, 0}, 3<<18))
	doc := fixtureDoc(true)
	doc.HTTPTools.Rewrites.MapLocal[0].BodyBase64, doc.HTTPTools.Rewrites.MapLocal[0].Body = big, ""
	doc.HTTPTools.Rewrites.MapLocal = append(doc.HTTPTools.Rewrites.MapLocal,
		&model.MapLocal{ID: "m-2", Pattern: "^b", Status: 200, BodyBase64: big})
	f := newFixture(t, doc, 1280, 900)
	f.w.editors.New(KindMapLocal, Seed{Pattern: `^https://m\.example\.com/`})
	testkit.UntilText(t, f.tt, "添加 本地映射")
	f.w.editors.ed.binary, f.w.editors.ed.binaryName = big, "c.bin"
	f.tt.Frame()
	f.click("保存")
	testkit.UntilText(t, f.tt, "本地映射的正文合计超过 4 MiB")
}

func TestTestPatternAndSeeds(t *testing.T) {
	r := TestPattern(`^https://(?P<host>[^/]+)/a/(\d+)$`, "https://x.org/a/42")
	if !r.Hit || len(r.Groups) != 2 || r.Groups[0] != "x.org" || r.Groups[1] != "42" || r.Names[0] != "host" {
		t.Fatalf("%+v", r)
	}
	if out, _ := r.Expand("https://y/$2/${host}"); out != "https://y/42/x.org" {
		t.Fatal(out)
	}
	if r := TestPattern(`(`, "x"); r.Err == "" || r.Hit {
		t.Fatalf("%+v", r)
	}
	if r := TestPattern(`^a$`, "b"); r.Hit || r.Err != "" {
		t.Fatalf("%+v", r)
	}
	if r := TestPattern(`a(b)?`, "a"); !r.Hit || len(r.Groups) != 1 || r.Groups[0] != "" {
		t.Fatalf("%+v", r)
	}
	if got := URLPattern("https://a.com/x?y=1"); got != `^https://a\.com/x\?y=1$` {
		t.Fatal(got)
	}
	if !TestPattern(URLPattern("https://a.com/x?y=1"), "https://a.com/x?y=1").Hit {
		t.Fatal("pattern of a URL misses it")
	}
}

func TestMapLocalFromResponse(t *testing.T) {
	headers := []model.HeaderField{{Name: "Content-Type", Value: "application/json"}, {Name: "Content-Length", Value: "9"},
		{Name: "Content-Encoding", Value: "gzip"}, {Name: "Connection", Value: "keep-alive"}, {Name: "X-Id", Value: "7"}, {Name: ":status", Value: "200"}}
	rule, err := MapLocalFromResponse("https://a.com/x", 200, headers, []byte(`{"a":1}`))
	if err != nil || rule.Body != `{"a":1}` || rule.BodyBase64 != "" || len(rule.Headers) != 2 || rule.Headers[1].Name != "X-Id" ||
		rule.Pattern != `^https://a\.com/x$` || rule.Status != 200 {
		t.Fatalf("%+v %v", rule, err)
	}
	rule, err = MapLocalFromResponse("https://a.com/x", 0, nil, []byte{0, 1, 2})
	if err != nil || rule.BodyBase64 != "AAEC" || rule.Body != "" || rule.Status != 200 {
		t.Fatalf("%+v %v", rule, err)
	}
	if _, err = MapLocalFromResponse("https://a.com/x", 200, nil, make([]byte, model.MaxMapLocalBody+1)); err == nil {
		t.Fatal("oversize body accepted")
	}
	if rule.Issue() != "" {
		t.Fatalf("issue %q", rule.Issue())
	}
}

func TestRuleName(t *testing.T) {
	doc := fixtureDoc(true)
	for id, want := range map[string]string{"u-ads": "广告拦截", "h-cors": "放开跨域", "m-cfg": "本地配置", "u-bad": "(", "gone": "gone", "": ""} {
		if got := RuleName(doc, id); got != want {
			t.Errorf("%q: %q", id, got)
		}
	}
	if RuleName(nil, "x") != "x" {
		t.Error("nil document")
	}
}

// The acceptance screenshots of round 25: written when
// REWRITE_PREVIEW_DIR is set.
func TestRewriteAcceptanceScreens(t *testing.T) {
	dir := os.Getenv("REWRITE_PREVIEW_DIR")
	if dir == "" {
		t.Skip("REWRITE_PREVIEW_DIR is not set")
	}
	shot := func(f *fixture, name string) {
		t.Helper()
		for _, dark := range []bool{false, true} {
			suffix := "-light.png"
			if dark {
				suffix = "-dark.png"
			}
			f.tt.SetDark(dark)
			f.tt.Frame()
			// The theme's transition runs before the colors read.
			f.tt.Advance(600 * time.Millisecond)
			f.tt.Frame()
			file, err := os.Create(filepath.Join(dir, name+suffix))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(file, f.tt.Image()); err != nil {
				t.Fatal(err)
			}
			file.Close()
		}
		f.tt.SetDark(false)
		f.tt.Advance(600 * time.Millisecond)
		f.tt.Frame()
	}
	f := newFixture(t, fixtureDoc(true), 1440, 760)
	testkit.UntilText(t, f.tt, "广告拦截")
	shot(f, "01-url-rules")
	f.click("请求头")
	testkit.UntilText(t, f.tt, "统一 UA")
	shot(f, "02-header-rules")
	f.click("本地映射")
	testkit.UntilText(t, f.tt, "本地配置")
	shot(f, "03-map-local-rules")

	f.click("正文")
	testkit.UntilText(t, f.tt, "价格改写")
	shot(f, "13-body-rules")

	f.click("编辑 价格改写")
	testkit.UntilText(t, f.tt, "编辑 正文")
	f.tt.Frame()
	shot(f, "14-editor-body")
	{
		e := f.w.editors.ed
		e.addEdit(model.BodyActionJSONAppend)
		e.edits[0].path, e.edits[0].value = "$.data[", "{x"
		e.edits[1].action = model.BodyActionReplaceRegex
		e.edits[1].regex = "("
		e.contentTypes = "json, "
		e.submitted = true
		f.tt.Frame()
		shot(f, "15-editor-body-error")
	}
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.editors.Open() })

	f.click("URL 重写")
	f.w.editors.New(KindURL, Seed{Pattern: `^https://api\.example\.com/(.*)$`, TestURL: "https://api.example.com/v2/items"})
	testkit.UntilText(t, f.tt, "添加 URL 重写")
	f.w.editors.ed.action, f.w.editors.ed.target = model.URLActionHeader, "https://internal.example.net:8443/$1"
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "https://internal.example.net:8443/v2/items")
	shot(f, "16-editor-url-header")
	f.w.editors.ed.target = "ftp://x"
	f.w.editors.ed.submitted = true
	f.tt.Frame()
	shot(f, "17-editor-url-header-error")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.editors.Open() })

	// Editors.
	f.click("编辑 旧域名跳转")
	testkit.UntilText(t, f.tt, "编辑 URL 重写")
	f.w.editors.ed.test = "https://old.example.com/docs/intro"
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "命中")
	shot(f, "04-editor-url-pattern-hit")
	f.w.editors.ed.pattern, f.w.editors.ed.target = "(", ""
	f.w.editors.ed.submitted = true
	f.tt.Frame()
	shot(f, "05-editor-url-error")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.editors.Open() })

	f.click("请求头")
	f.click("编辑 统一 UA")
	testkit.UntilText(t, f.tt, "编辑 请求头")
	shot(f, "06-editor-header")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.editors.Open() })

	f.click("本地映射")
	f.click("编辑 本地配置")
	testkit.UntilText(t, f.tt, "编辑 本地映射")
	shot(f, "07-editor-map-local")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.editors.Open() })

	// Empty states.
	off := newFixture(t, model.EmptyDoc(model.PlatformLinux), 1100, 600)
	testkit.UntilText(t, off.tt, "未开启 HTTPS 解密")
	shot(off, "08-empty-decryption-off")
	on := model.EmptyDoc(model.PlatformLinux)
	on.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	empty := newFixture(t, on, 1100, 600)
	testkit.UntilText(t, empty.tt, "暂无改写")
	shot(empty, "09-empty-none")
	notice := newFixture(t, fixtureDoc(false), 1100, 600)
	testkit.UntilText(t, notice.tt, "广告拦截")
	shot(notice, "10-rules-decryption-off")

	narrow := newFixture(t, fixtureDoc(true), 760, 700)
	testkit.UntilText(t, narrow.tt, "广告拦截")
	shot(narrow, "11-narrow")
	narrow.w.editors.New(KindMapLocal, Seed{Pattern: "^x$"})
	testkit.UntilText(t, narrow.tt, "添加 本地映射")
	shot(narrow, "12-narrow-editor")

	phone := newFixture(t, fixtureDoc(true), 600, 700)
	testkit.UntilText(t, phone.tt, "广告拦截")
	phone.click("正文")
	testkit.UntilText(t, phone.tt, "价格改写")
	if !phone.tt.HasText("添加规则") {
		t.Fatal("add button missing at 600px")
	}
	shot(phone, "19-body-narrow-600")
}

// guardDoc counts the editor's hold on the document.
type guardDoc struct {
	inner        Doc
	begun, ended int
}

func (g *guardDoc) Doc() *model.Doc                  { return g.inner.Doc() }
func (g *guardDoc) Update(fn func(*model.Doc)) error { return g.inner.Update(fn) }

func (g *guardDoc) BeginEdit() { g.begun++ }
func (g *guardDoc) EndEdit()   { g.ended++ }

func TestCloseReleasesEditGuard(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	g := &guardDoc{inner: f.store}
	x := NewEditors(f.env, g)
	f.w.editors = x
	testkit.UntilText(t, f.tt, "广告拦截")
	f.click("编辑 广告拦截")
	testkit.UntilText(t, f.tt, "编辑 URL 重写")
	if g.begun != 1 || g.ended != 0 {
		t.Fatalf("open: begun %d ended %d", g.begun, g.ended)
	}
	f.w.SetActive(false)
	if g.ended != 1 {
		t.Fatalf("SetActive(false) kept the guard: ended %d", g.ended)
	}
	x.Close()
	if g.begun != 1 || g.ended != 1 {
		t.Fatalf("close twice: begun %d ended %d", g.begun, g.ended)
	}
}

func TestCloseAppliesPendingRemote(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	testkit.UntilText(t, f.tt, "广告拦截")
	f.click("编辑 广告拦截")
	testkit.UntilText(t, f.tt, "编辑 URL 重写")
	if !f.store.Editing() {
		t.Fatal("store not editing while the dialog is open")
	}
	remote := fixtureDoc(true)
	remote.HTTPTools.Rewrites.URL[0].Name = "远端改名"
	raw, _ := json.Marshal(remote)
	f.srv.SetStore(api.ServerStore{Revision: 9, Doc: raw, Profiles: json.RawMessage("[]")})
	f.store.Resync()
	// The remote change waits while the dialog is open.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		f.tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if f.rules().URL[0].Name == "远端改名" {
		t.Fatal("remote change replaced the document under the open editor")
	}
	f.w.SetActive(false)
	if f.store.Editing() {
		t.Fatal("store still editing after SetActive(false)")
	}
	testkit.Until(t, f.tt, func() bool { return f.rules().URL[0].Name == "远端改名" })
}

func TestMapLocalNoBodyStatusBlocksSave(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	n := len(f.rules().MapLocal)
	f.w.editors.New(KindMapLocal, Seed{Pattern: "a", MapLocal: &model.MapLocal{Status: 204, Body: "x"}})
	f.tt.Frame()
	f.click("保存")
	testkit.UntilText(t, f.tt, "该状态码不能带正文")
	if !f.w.editors.Open() || len(f.rules().MapLocal) != n {
		t.Fatal("saved a 204 with a body")
	}
	if !f.w.editors.ed.compute().any() {
		t.Fatal("no field issue")
	}
	// Without a body it saves.
	f.w.editors.ed.body = ""
	f.tt.Frame()
	f.click("保存")
	testkit.Until(t, f.tt, func() bool { return len(f.rules().MapLocal) == n+1 })
}

func TestMapLocalFromResponseStatuses(t *testing.T) {
	headers := []model.HeaderField{{Name: "Content-Range", Value: "bytes 0-1/9"}, {Name: "ETag", Value: "x"}}
	for _, status := range []int{206, 304} {
		rule, err := MapLocalFromResponse("https://a.com/x", status, headers, []byte("ab"))
		if err != nil || rule.Status != 200 || len(rule.Headers) != 1 || rule.Headers[0].Name != "ETag" || rule.Issue() != "" {
			t.Fatalf("%d: %+v %v", status, rule, err)
		}
	}
	if rule, _ := MapLocalFromResponse("https://a.com/x", 404, nil, nil); rule.Status != 404 {
		t.Fatalf("404 became %d", rule.Status)
	}
}

func TestSaveDoesNotResurrectDeletedRule(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	testkit.UntilText(t, f.tt, "广告拦截")
	f.click("编辑 广告拦截")
	testkit.UntilText(t, f.tt, "编辑 URL 重写")
	id := f.w.editors.ed.id
	f.store.Update(func(d *model.Doc) {
		r := d.HTTPTools.Rewrites
		for i, u := range r.URL {
			if u.ID == id {
				r.URL = append(r.URL[:i], r.URL[i+1:]...)
				break
			}
		}
	})
	n := len(f.rules().URL)
	f.tt.Frame()
	f.click("保存")
	testkit.UntilText(t, f.tt, "规则已被删除")
	if !f.w.editors.Open() || len(f.rules().URL) != n {
		t.Fatalf("resurrected or closed: open=%v urls=%d want %d", f.w.editors.Open(), len(f.rules().URL), n)
	}
}
