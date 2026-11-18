package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/modules"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const (
	modURL     = "https://modules.example.com/demo.sgmodule"
	scriptURL  = "https://cdn.example.com/price.js"
	scriptBody = "$done({})\n"
)

// moduleText is a module with the given MITM hosts and extra rewrite
// lines; it holds one script, one unsupported section and the argument
// LANG.
func moduleText(hosts string, rewrites ...string) string {
	return "#!name=测试模块\n#!desc=示例\n#!version=1.0\n#!arguments=LANG:zh\n[MITM]\nhostname = %APPEND% " + hosts +
		"\n[URL Rewrite]\n^https://ads\\.example\\.com/ - reject\n" + strings.Join(rewrites, "\n") +
		"\n[Script]\nprice = type=http-response, pattern=^https://api\\.example\\.com/items, script-path=" + scriptURL +
		", requires-body=1\n[General]\nskip-proxy = 1.1.1.1\n"
}

const baseHosts = "api.example.com, cdn.example.com"

// memFetcher serves bodies from a map, as a download that needs no server.
func memFetcher(pages map[string]string) Fetcher {
	return func(_ context.Context, url string) ([]byte, error) {
		body, ok := pages[url]
		if !ok {
			return nil, fmt.Errorf("HTTP 404")
		}
		return []byte(body), nil
	}
}

// installed is a module as an install of text leaves it.
func installed(t *testing.T, text string) *model.HTTPModule {
	t.Helper()
	conv, err := Convert(context.Background(), memFetcher(map[string]string{scriptURL: scriptBody}),
		Input{Data: []byte(text), BaseURL: modURL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModule(conv, model.ModuleSource{Kind: model.ModuleSourceURL, URL: modURL}, "", time.UnixMilli(1_700_000_000_000))
	m.ID = "mod_demo"
	return m
}

func fixtureDoc(mods ...*model.HTTPModule) *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}, Modules: mods}
	return doc
}

type fixture struct {
	t      *testing.T
	srv    *testkit.Server
	env    *env.Env
	shell  *testkit.Shell
	store  *store.Store
	w      *Workspace
	tt     *ui.Tester
	search string
	now    atomic.Int64
}

func newFixture(t *testing.T, doc *model.Doc, width, height int) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t)}
	f.now.Store(1_700_000_000_000)
	raw, _ := json.Marshal(doc)
	f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	f.env, f.shell = testkit.NewEnv(f.srv)
	f.store = store.New(f.env)
	f.w = New(f.env, f.store)
	f.w.SetClock(func() time.Time { return time.UnixMilli(f.now.Load()) })
	f.store.Load()
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		f.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		kit.Workspace(c, func() {
			ui.Row(c).PaddingX(16).PaddingY(8).Shrink(0).Children(func() {
				ui.Spacer(c)
				f.w.Toolbar(c)
			})
			f.w.Body(c, f.search, func(c *ui.Context) {})
		})
		f.w.Overlays(c)
	}, width, height)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded })
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	return f
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
}

func (f *fixture) settled() {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return !f.store.Saving() })
}

func (f *fixture) module(id string) *model.HTTPModule {
	return f.store.Doc().HTTPTools.FindModule(id)
}

func (f *fixture) modules() []*model.HTTPModule { return f.store.Doc().HTTPTools.Modules }

func (f *fixture) want(texts ...string) {
	f.t.Helper()
	for _, s := range texts {
		if !f.tt.HasText(s) {
			f.t.Errorf("missing %q in %q", s, f.tt.Texts())
		}
	}
}

func (f *fixture) wantNot(texts ...string) {
	f.t.Helper()
	for _, s := range texts {
		if f.tt.HasText(s) {
			f.t.Errorf("unexpected %q in %q", s, f.tt.Texts())
		}
	}
}

// fetchInstall types the address, fetches it and waits for the preview.
func (f *fixture) fetchInstall(address string) {
	f.t.Helper()
	f.click("添加模块")
	testkit.UntilText(f.t, f.tt, "获取")
	f.click("模块链接")
	f.tt.Type(address)
	f.click("获取")
}

func TestEmptyState(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 700)
	f.want("暂无模块", "添加模块")
	f.wantNot("个模块")
}

func TestCards(t *testing.T) {
	m := installed(t, moduleText(baseHosts))
	m.Pending = nil
	f := newFixture(t, fixtureDoc(m), 1280, 700)
	testkit.UntilText(t, f.tt, "测试模块")
	f.want("modules.example.com", "1.0", "主机名", "改写", "脚本", "1 个模块")
	f.wantNot("示例")
	// Pending, failed and updated cards carry a tag each.
	for _, c := range []struct {
		mutate func(*model.HTTPModule)
		tag    string
	}{
		{func(m *model.HTTPModule) { m.Updated = true }, "已更新"},
		{func(m *model.HTTPModule) { m.Updated, m.LastError = false, "连接超时" }, "更新失败"},
		{func(m *model.HTTPModule) {
			m.LastError = ""
			m.Pending = &model.ModulePending{Name: "x", Hash: "h"}
		}, "待确认"},
	} {
		f.w.update(func(d *model.Doc) { c.mutate(d.HTTPTools.FindModule("mod_demo")) })
		testkit.UntilText(t, f.tt, c.tag)
	}
}

func TestInstallFromURL(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.srv.SetFetchBody(scriptURL, scriptBody)
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "将解密 2 个主机名")
	f.want("将运行 1 个脚本", "测试模块", "api.example.com", "cdn.example.com", "price", "http-response", "未支持 1", "安装", "确认解密以上主机名并运行以上脚本")
	sha := model.HashModuleRaw(scriptBody)
	f.want(sha[:8])
	// The module is not installed before the confirmation is ticked.
	f.click("安装")
	f.tt.Frame()
	if len(f.modules()) != 0 {
		t.Fatalf("installed without confirmation: %+v", f.modules())
	}
	f.click("确认解密以上主机名并运行以上脚本")
	f.click("安装")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 1 })
	f.settled()
	m := f.modules()[0]
	if !m.Enabled || m.Name != "测试模块" || m.Source.URL != modURL || m.Version != "1.0" || m.Raw == "" || m.Hash == "" {
		t.Errorf("module = %+v", m)
	}
	if diff := m.NeedsApproval(m.Contents); !diff.Empty() {
		t.Errorf("not approved: %+v", diff)
	}
	if len(m.Contents.Scripts) != 1 || m.Contents.Scripts[0].Source != scriptBody {
		t.Errorf("script not embedded: %+v", m.Contents.Scripts)
	}
	if len(m.Unsupported) != 1 || m.Unsupported[0].Section != "General" {
		t.Errorf("unsupported = %+v", m.Unsupported)
	}
	testkit.Until(t, f.tt, func() bool { return !f.w.InstallOpen() })
	f.want("测试模块", "1 个模块")
	// Fetching again the same address is refused.
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "已安装此链接的模块")
}

func TestInstallWithoutSensitiveItemsNeedsNoConfirmation(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	f.srv.SetFetchBody(modURL, "#!name=规则\n[URL Rewrite]\n^https://ads\\.example\\.com/ - reject\n")
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "安装")
	f.wantNot("将解密", "将运行", "确认解密")
	f.click("安装")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 1 })
}

func TestInstallErrorsKeepRetry(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	f.fetchInstall("ftp://x")
	testkit.UntilText(t, f.tt, "请输入 http 或 https 链接")
	// The server cannot reach the address.
	f.srv.SetFetch(modURL, testkit.FetchPage{Fail: "连接超时"})
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.InstallOpen() })
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "获取失败：")
	f.want("连接超时", "重试")
	// A page that is not a module.
	f.srv.SetFetchBody(modURL, "hello")
	f.click("重试")
	testkit.UntilText(t, f.tt, "不是有效的模块文件")
	// A status other than 2xx.
	f.srv.SetFetch(modURL, testkit.FetchPage{Status: 500})
	f.click("重试")
	testkit.UntilText(t, f.tt, "HTTP 500")
	// The retry succeeds once the page is back.
	f.srv.SetFetch(modURL, testkit.FetchPage{Body: moduleText(baseHosts)})
	f.srv.SetFetchBody(scriptURL, scriptBody)
	f.click("重试")
	testkit.UntilText(t, f.tt, "将解密 2 个主机名")
	if len(f.modules()) != 0 {
		t.Error("installed by a fetch")
	}
}

func TestScriptDownloadFailureIsNamed(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.srv.SetFetch(scriptURL, testkit.FetchPage{Status: 404})
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "1 个下载失败")
	f.want(scriptURL + "：HTTP 404")
	f.wantNot("将运行")
	// The failed script is not part of the module: only the hosts need
	// the confirmation.
	f.click("确认解密以上主机名")
	f.click("安装")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 1 })
	if n := len(f.modules()[0].Contents.Scripts); n != 0 {
		t.Errorf("%d scripts", n)
	}
}

func TestDownloadLimits(t *testing.T) {
	pages := map[string]string{}
	for i := 0; i < MaxFetches+5; i++ {
		u := fmt.Sprintf("https://cdn.example.com/s%d.js", i)
		pages[u] = "$done({})"
	}
	var inflight, peak atomic.Int32
	fetch := func(_ context.Context, url string) ([]byte, error) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inflight.Add(-1)
		return []byte(pages[url]), nil
	}
	var list []modules.Fetch
	for u := range pages {
		list = append(list, modules.Fetch{URL: u})
	}
	got, failures, _ := download(context.Background(), fetch, list, nil)
	if len(got) != MaxFetches || len(failures) != 5 || !strings.Contains(failures[0].Reason, "下载上限") {
		t.Errorf("fetched %d, failures %v", len(got), failures)
	}
	if peak.Load() > FetchWorkers {
		t.Errorf("%d downloads at once", peak.Load())
	}
	// A body past the total size stops the rest.
	big := strings.Repeat("a", 5<<20)
	list = []modules.Fetch{{URL: "https://x/a.js"}, {URL: "https://x/b.js"}}
	got, failures, total := download(context.Background(), func(context.Context, string) ([]byte, error) { return []byte(big), nil }, list, nil)
	if len(got) != 1 || len(failures) != 1 || total != 5<<20 || !strings.Contains(failures[0].Reason, "总大小上限") {
		t.Errorf("fetched %d, failures %v, total %d", len(got), failures, total)
	}
}

func TestInstallFromFiles(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	f.srv.SetFetchBody(scriptURL, scriptBody)
	f.shell.Open, f.shell.OpenName = []byte(moduleText(baseHosts)), "demo.sgmodule"
	f.click("添加模块")
	f.click("本地文件")
	f.click("选择文件")
	testkit.UntilText(t, f.tt, "demo.sgmodule")
	f.click("获取")
	testkit.UntilText(t, f.tt, "将解密 2 个主机名")
	f.want("demo.sgmodule")
	f.click("确认解密以上主机名并运行以上脚本")
	f.click("安装")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 1 })
	m := f.modules()[0]
	if m.Source.Kind != model.ModuleSourceFile || m.Source.URL != "" || m.UpdateInterval != 0 {
		t.Errorf("source = %+v interval %d", m.Source, m.UpdateInterval)
	}
	// A Kiko file carries its content and has no raw text.
	exported := modules.ExportKiko(m)
	f.shell.Open, f.shell.OpenName = exported, "测试模块.kikomodule"
	f.click("添加模块")
	f.click("本地文件")
	f.click("选择文件")
	testkit.UntilText(t, f.tt, "测试模块.kikomodule")
	f.click("获取")
	testkit.UntilText(t, f.tt, "将解密 2 个主机名")
	f.click("确认解密以上主机名并运行以上脚本")
	f.click("安装")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 2 })
	if p := f.modules()[1]; p.Raw != "" || len(p.Contents.Scripts) != 1 || p.Contents.Scripts[0].Source != scriptBody {
		t.Errorf("kiko module = %+v", p)
	}
	// A file that is no module.
	f.shell.Open, f.shell.OpenName = []byte("hello"), "x.txt"
	f.click("添加模块")
	f.click("本地文件")
	f.click("选择文件")
	testkit.UntilText(t, f.tt, "x.txt")
	f.click("获取")
	testkit.UntilText(t, f.tt, "获取失败：")
}

func TestUnsupportedListShowsReasons(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 800)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.srv.SetFetchBody(scriptURL, scriptBody)
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "未支持 1")
	f.wantNot("整节未支持")
	f.click("未支持 1")
	testkit.UntilText(t, f.tt, "整节未支持")
	f.want("[General]")
	f.click("复制未支持的行")
	res, err := modules.ParseSurge(moduleText(baseHosts), modules.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := UnsupportedText(res.Unsupported); !strings.Contains(got, "整节未支持") || !strings.Contains(got, "[General]") {
		t.Errorf("text = %q", got)
	}
}

func TestToggle(t *testing.T) {
	f := newFixture(t, fixtureDoc(installed(t, moduleText(baseHosts))), 1000, 700)
	testkit.UntilText(t, f.tt, "测试模块")
	f.click("启用 测试模块")
	testkit.Until(t, f.tt, func() bool { return !f.module("mod_demo").Enabled })
	f.click("启用 测试模块")
	testkit.Until(t, f.tt, func() bool { return f.module("mod_demo").Enabled })
}

func TestDeleteConfirmsAtMoreButton(t *testing.T) {
	f := newFixture(t, fixtureDoc(installed(t, moduleText(baseHosts))), 1000, 700)
	testkit.UntilText(t, f.tt, "测试模块")
	f.click("更多操作")
	f.tt.Frame()
	f.click("删除")
	testkit.UntilText(t, f.tt, "删除「测试模块」？")
	f.click("删除")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 0 })
	testkit.UntilText(t, f.tt, "暂无模块")
}

func TestViewContent(t *testing.T) {
	m := installed(t, moduleText(baseHosts))
	m.Updated = true
	f := newFixture(t, fixtureDoc(m), 1000, 800)
	testkit.UntilText(t, f.tt, "已更新")
	f.click("查看 测试模块")
	testkit.UntilText(t, f.tt, "复制全部")
	f.want("示例", "主机名 2", "脚本 1", "未支持 1", "URL 重写 1")
	f.click("主机名 2")
	testkit.UntilText(t, f.tt, "api.example.com")
	// Looking at the content clears the mark.
	testkit.Until(t, f.tt, func() bool { return !f.module("mod_demo").Updated })
	f.click("复制全部")
	text := ContentText(f.module("mod_demo"))
	for _, want := range []string{"[主机名]", "api.example.com", "[脚本]", "price", "[未支持]", "[General]"} {
		if !strings.Contains(text, want) {
			t.Errorf("copied text lacks %q:\n%s", want, text)
		}
	}
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.ContentOpen() })
}

func TestExport(t *testing.T) {
	f := newFixture(t, fixtureDoc(installed(t, moduleText(baseHosts))), 1000, 700)
	testkit.UntilText(t, f.tt, "测试模块")
	f.click("更多操作")
	f.tt.Frame()
	f.click("导出")
	testkit.Until(t, f.tt, func() bool { _, ok := f.shell.SavedFile("测试模块.kikomodule"); return ok })
	data, _ := f.shell.SavedFile("测试模块.kikomodule")
	res, err := modules.ParseKiko(data)
	if err != nil || len(res.Contents.Scripts) != 1 {
		t.Fatalf("exported file: %v %+v", err, res)
	}
}

func TestParams(t *testing.T) {
	f := newFixture(t, fixtureDoc(installed(t, moduleText("api.example.com, {{{LANG}}}.example.com"))), 1000, 800)
	f.srv.SetFetchBody(scriptURL, scriptBody)
	testkit.UntilText(t, f.tt, "测试模块")
	f.click("更多操作")
	f.tt.Frame()
	f.click("参数")
	testkit.UntilText(t, f.tt, "默认 zh")
	f.want("LANG", "自动更新", "1 天")
	f.click("LANG 的值")
	f.tt.Type("en")
	f.click("保存")
	// en.example.com is a host the module did not have: the new content
	// waits for the user and the old one stays.
	testkit.Until(t, f.tt, func() bool { return f.module("mod_demo").Pending != nil })
	f.settled()
	m := f.module("mod_demo")
	if got := m.Contents.Hostnames.Include; len(got) != 2 || got[1] != "zh.example.com" {
		t.Errorf("hosts = %v", got)
	}
	if got := m.Pending.Added.Hostnames; len(got) != 1 || got[0] != "en.example.com" {
		t.Errorf("added = %v", got)
	}
	if m.Pending.Arguments[0].Value != "en" {
		t.Errorf("pending arguments = %+v", m.Pending.Arguments)
	}
	testkit.Until(t, f.tt, func() bool { return !f.w.ParamsOpen() })
	testkit.UntilText(t, f.tt, "待确认")
	// The card opens the confirmation, which lists only what is new.
	f.click("查看 测试模块")
	testkit.UntilText(t, f.tt, "将解密 1 个主机名")
	f.want("en.example.com", "确认更新", "放弃更新")
	f.wantNot("zh.example.com", "api.example.com")
	f.click("确认更新")
	f.tt.Frame()
	if f.module("mod_demo").Pending == nil {
		t.Error("approved without the box ticked")
	}
	f.click("确认解密以上主机名")
	f.click("确认更新")
	testkit.Until(t, f.tt, func() bool { return f.module("mod_demo").Pending == nil })
	m = f.module("mod_demo")
	if got := m.Contents.Hostnames.Include; len(got) != 2 || got[1] != "en.example.com" || m.Arguments[0].Value != "en" || !m.Updated {
		t.Errorf("after approval: hosts %v args %+v updated %v", got, m.Arguments, m.Updated)
	}
}

func TestParamsErrorKeepsDialog(t *testing.T) {
	f := newFixture(t, fixtureDoc(installed(t, moduleText("api.example.com, {{{LANG}}}.example.com"))), 1000, 800)
	// The script cannot be downloaded again.
	f.srv.SetFetch(scriptURL, testkit.FetchPage{Fail: "连接被拒绝"})
	testkit.UntilText(t, f.tt, "测试模块")
	f.w.openParams("mod_demo")
	testkit.UntilText(t, f.tt, "默认 zh")
	f.click("LANG 的值")
	f.tt.Type("en")
	f.click("保存")
	testkit.UntilText(t, f.tt, "连接被拒绝")
	if !f.w.ParamsOpen() {
		t.Error("dialog closed on failure")
	}
	if got := f.module("mod_demo").Arguments[0].Value; got != "" {
		t.Errorf("argument saved: %q", got)
	}
	// Escape closes it; the card takes the focus back.
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.ParamsOpen() })
}

// updateFixture is a module installed from modURL, with its pages served.
func updateFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, fixtureDoc(installed(t, moduleText(baseHosts))), 1000, 700)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.srv.SetFetchBody(scriptURL, scriptBody)
	testkit.UntilText(t, f.tt, "测试模块")
	f.now.Store(1_700_000_500_000)
	return f
}

func (f *fixture) update() {
	f.t.Helper()
	f.w.Update("mod_demo")
	testkit.Until(f.t, f.tt, func() bool { return !f.w.Busy() })
	f.settled()
}

func TestUpdateUnchangedOnlyRefreshesTheCheckTime(t *testing.T) {
	f := updateFixture(t)
	before := f.module("mod_demo")
	hash, updatedAt := before.Hash, before.UpdatedAt
	f.update()
	m := f.module("mod_demo")
	if m.Hash != hash || m.UpdatedAt != updatedAt || m.Updated || m.LastCheckedAt != f.now.Load() || m.Pending != nil {
		t.Errorf("module = %+v", m)
	}
	f.wantNot("已更新", "待确认", "更新失败")
}

func TestUpdateChangedTakesEffect(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts, "^https://old\\.example\\.com/ - reject"))
	f.update()
	m := f.module("mod_demo")
	if !m.Updated || m.Pending != nil || m.UpdatedAt != f.now.Load() || len(m.Contents.Rewrites.URL) != 2 {
		t.Errorf("module = %+v", m)
	}
	testkit.UntilText(t, f.tt, "已更新")
	// The script changed: the same hosts, a script text that is new, so
	// it needs the confirmation.
	f.srv.SetFetchBody(scriptURL, "$done({body: ''})\n")
	f.update()
	m = f.module("mod_demo")
	if m.Pending == nil || len(m.Pending.Added.Scripts) != 1 || !m.Pending.Added.Scripts[0].Changed {
		t.Fatalf("pending = %+v", m.Pending)
	}
	testkit.UntilText(t, f.tt, "待确认")
}

func TestUpdateWithNewHostWaits(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts+", new.example.com"))
	f.update()
	m := f.module("mod_demo")
	if m.Pending == nil || len(m.Contents.Hostnames.Include) != 2 || m.Pending.Added.Hostnames[0] != "new.example.com" {
		t.Fatalf("module = %+v", m)
	}
	testkit.UntilText(t, f.tt, "待确认")
	// Discarding drops it.
	f.click("查看 测试模块")
	testkit.UntilText(t, f.tt, "放弃更新")
	f.click("放弃更新")
	testkit.Until(t, f.tt, func() bool { return f.module("mod_demo").Pending == nil })
	f.settled()
	if f.module("mod_demo").Pending != nil {
		t.Error("pending kept")
	}
}

func TestUpdateFailureOnlyRecords(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetch(modURL, testkit.FetchPage{Fail: "连接超时"})
	f.now.Store(f.now.Load() + 48*3600_000)
	if n := f.w.CheckDue(); n != 1 {
		t.Fatalf("queued %d", n)
	}
	testkit.Until(t, f.tt, func() bool { return !f.w.Busy() })
	f.settled()
	m := f.module("mod_demo")
	if !strings.Contains(m.LastError, "连接超时") || m.Hash != installed(t, moduleText(baseHosts)).Hash {
		t.Errorf("module = %+v", m)
	}
	testkit.UntilText(t, f.tt, "更新失败")
	// A manual update that works clears it.
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.update()
	if f.module("mod_demo").LastError != "" {
		t.Error("error kept")
	}
}

func TestUpdateKeepsSwitchFlippedMeanwhile(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetch(modURL, testkit.FetchPage{Body: moduleText(baseHosts, "^https://old\\.example\\.com/ - reject"), Delay: 300 * time.Millisecond})
	f.w.Update("mod_demo")
	testkit.UntilText(t, f.tt, "更新中")
	// The user switches the module off while the download runs.
	f.click("启用 测试模块")
	testkit.Until(t, f.tt, func() bool { return !f.module("mod_demo").Enabled })
	testkit.Until(t, f.tt, func() bool { return !f.w.Busy() })
	f.settled()
	m := f.module("mod_demo")
	if m.Enabled {
		t.Error("the update switched the module back on")
	}
	if len(m.Contents.Rewrites.URL) != 2 {
		t.Errorf("the update did not land: %+v", m.Contents.Rewrites)
	}
	// And the other way: a module deleted meanwhile stays deleted.
	f.srv.SetFetch(modURL, testkit.FetchPage{Body: moduleText(baseHosts), Delay: 200 * time.Millisecond})
	f.w.Update("mod_demo")
	f.w.update(func(d *model.Doc) { d.HTTPTools.Modules = nil })
	testkit.Until(t, f.tt, func() bool { return !f.w.Busy() })
	f.settled()
	if len(f.modules()) != 0 {
		t.Errorf("deleted module came back: %+v", f.modules())
	}
}

func TestAutoUpdateWaitsForDueAndVisibleWindow(t *testing.T) {
	m := installed(t, moduleText(baseHosts))
	m.UpdateInterval = 6
	f := newFixture(t, fixtureDoc(m), 1000, 700)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.srv.SetFetchBody(scriptURL, scriptBody)
	testkit.UntilText(t, f.tt, "测试模块")
	// Not due after five hours.
	f.now.Store(1_700_000_000_000 + 5*3600_000)
	if n := f.w.CheckDue(); n != 0 {
		t.Errorf("queued %d before the interval", n)
	}
	// Due after six, but not while the window is hidden.
	f.now.Store(1_700_000_000_000 + 6*3600_000)
	f.env.SetVisible(false)
	if n := f.w.CheckDue(); n != 0 {
		t.Errorf("queued %d while hidden", n)
	}
	f.tt.Frame()
	if calls := f.srv.FetchCalls(); len(calls) != 0 {
		t.Fatalf("requests while hidden: %v", calls)
	}
	f.env.SetVisible(true)
	if n := f.w.CheckDue(); n != 1 {
		t.Fatalf("queued %d when due", n)
	}
	testkit.Until(t, f.tt, func() bool { return !f.w.Busy() })
	f.settled()
	if got := f.module("mod_demo").LastCheckedAt; got != f.now.Load() {
		t.Errorf("checked at %d, want %d", got, f.now.Load())
	}
	// Checked now: not due again; a disabled module and a manual-only
	// one are never due.
	if n := f.w.CheckDue(); n != 0 {
		t.Errorf("queued %d right after a check", n)
	}
	for _, mut := range []func(*model.HTTPModule){
		func(m *model.HTTPModule) { m.Enabled = false },
		func(m *model.HTTPModule) { m.Enabled, m.UpdateInterval = true, 0 },
		func(m *model.HTTPModule) { m.UpdateInterval, m.Source.Kind = 6, model.ModuleSourceFile },
	} {
		c := *m
		mut(&c)
		if Due(&c, time.UnixMilli(1_800_000_000_000)) {
			t.Errorf("due: %+v", c)
		}
	}
}

func TestHiddenWindowDropsQueuedAutoUpdates(t *testing.T) {
	a, b := installed(t, moduleText(baseHosts)), installed(t, moduleText(baseHosts))
	b.ID, b.Name, b.Source.URL = "mod_two", "第二个", "https://modules.example.com/two.sgmodule"
	f := newFixture(t, fixtureDoc(a, b), 1000, 700)
	f.srv.SetFetch(modURL, testkit.FetchPage{Body: moduleText(baseHosts), Delay: 150 * time.Millisecond})
	f.srv.SetFetchBody(b.Source.URL, moduleText(baseHosts))
	f.srv.SetFetchBody(scriptURL, scriptBody)
	f.now.Store(1_700_000_000_000 + 48*3600_000)
	if n := f.w.CheckDue(); n != 2 {
		t.Fatalf("queued %d", n)
	}
	// The window hides while the first runs: the second never starts.
	f.env.SetVisible(false)
	testkit.Until(t, f.tt, func() bool { return !f.w.Busy() })
	for _, u := range f.srv.FetchCalls() {
		if u == b.Source.URL {
			t.Errorf("requested while hidden: %v", f.srv.FetchCalls())
		}
	}
}

func TestUpdateAllRunsOneAtATime(t *testing.T) {
	a, b := installed(t, moduleText(baseHosts)), installed(t, moduleText(baseHosts))
	b.ID, b.Name, b.Source.URL = "mod_two", "第二个", "https://modules.example.com/two.sgmodule"
	f := newFixture(t, fixtureDoc(a, b), 1000, 700)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts))
	f.srv.SetFetchBody(b.Source.URL, moduleText(baseHosts))
	f.srv.SetFetchBody(scriptURL, scriptBody)
	testkit.UntilText(t, f.tt, "第二个")
	f.now.Store(1_700_000_500_000)
	f.click("更新全部")
	testkit.Until(t, f.tt, func() bool { return f.module("mod_two").LastCheckedAt > 1_700_000_000_000 && !f.w.Busy() })
	if peak := f.srv.FetchPeak(); peak > 1 {
		t.Errorf("%d downloads at once for sequential updates", peak)
	}
}

func TestEscapeClosesAndRestoresFocus(t *testing.T) {
	f := newFixture(t, fixtureDoc(installed(t, moduleText(baseHosts))), 1000, 700)
	testkit.UntilText(t, f.tt, "测试模块")
	f.click("添加模块")
	testkit.UntilText(t, f.tt, "获取")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.InstallOpen() })
	f.wantNot("获取")
	if f.w.focusAdd {
		// The add card took the focus request and used it.
		t.Error("focus request not consumed")
	}
	// A fetch that is running when the dialog closes does not reopen it.
	f.srv.SetFetch("https://slow.example.com/m", testkit.FetchPage{Body: moduleText(baseHosts), Delay: 200 * time.Millisecond})
	f.fetchInstall("https://slow.example.com/m")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.InstallOpen() })
	time.Sleep(300 * time.Millisecond)
	f.tt.Frame()
	if f.w.InstallOpen() || len(f.modules()) != 1 {
		t.Errorf("open %v modules %d", f.w.InstallOpen(), len(f.modules()))
	}
}

func TestSearch(t *testing.T) {
	other := installed(t, moduleText(baseHosts))
	other.ID, other.Name, other.Source.URL = "mod_two", "广告拦截", "https://rules.example.org/ads.sgmodule"
	f := newFixture(t, fixtureDoc(installed(t, moduleText(baseHosts)), other), 1000, 700)
	testkit.UntilText(t, f.tt, "广告拦截")
	f.search = "rules.example"
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("测试模块") })
	f.want("广告拦截", "1 / 2 个模块")
	f.search = "没有这个"
	testkit.UntilText(t, f.tt, "没有符合条件的模块")
}

func TestNarrowWindowUsesOneColumn(t *testing.T) {
	m := installed(t, moduleText(baseHosts))
	n := installed(t, moduleText(baseHosts))
	n.ID, n.Name = "mod_two", "第二个"
	f := newFixture(t, fixtureDoc(m, n), 420, 800)
	testkit.UntilText(t, f.tt, "第二个")
	f.want("测试模块", "主机名")
}

const uiEscape = ui.KeyEscape

func TestInstallPreviewShowsTriggersDirectRulesAndDownloads(t *testing.T) {
	f := newFixture(t, fixtureDoc(), 1000, 900)
	text := moduleText(baseHosts) + "[Rule]\nDOMAIN-SUFFIX,cdn.example.com,DIRECT\nIP-CIDR,8.8.0.0/16,DIRECT\nDOMAIN-SUFFIX,com,DIRECT\nDOMAIN-SUFFIX,ads.example.com,REJECT\n"
	f.srv.SetFetchBody(modURL, text)
	f.srv.SetFetchBody(scriptURL, scriptBody)
	f.fetchInstall(modURL)
	testkit.UntilText(t, f.tt, "将直连 2 条规则")
	// The hostnames and the script's pattern are visible together; the
	// DIRECT rules need the confirmation too; the too wide rule is listed
	// as unsupported.
	f.want("将解密 2 个主机名", "api.example.com", `^https://api\.example\.com/items`, "domain-suffix,cdn.example.com", "ip-cidr,8.8.0.0/16",
		"确认解密以上主机名、运行以上脚本、直连以上规则", "下载 1", "未支持 2")
	f.click("确认解密以上主机名、运行以上脚本、直连以上规则")
	f.click("安装")
	testkit.Until(t, f.tt, func() bool { return len(f.modules()) == 1 })
	f.settled()
	m := f.modules()[0]
	if len(m.Contents.Rules) != 3 || len(m.Approved.Direct) != 2 || !m.NeedsApproval(m.Contents).Empty() {
		t.Errorf("module = %+v approved %+v", m.Contents.Rules, m.Approved)
	}
}

func TestUpdateWithNewDirectRuleWaits(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts)+"[Rule]\nDOMAIN-SUFFIX,cdn.example.com,DIRECT\n")
	f.update()
	m := f.module("mod_demo")
	if m.Pending == nil || len(m.Pending.Added.Direct) != 1 || len(m.Contents.Rules) != 0 {
		t.Fatalf("module = %+v", m)
	}
	f.click("查看 测试模块")
	testkit.UntilText(t, f.tt, "将直连 1 条规则")
	f.want("确认直连以上规则", "domain-suffix,cdn.example.com")
}

func TestPendingChangedMeanwhileIsNotApproved(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts+", new.example.com"))
	f.update()
	if f.module("mod_demo").Pending == nil {
		t.Fatal("no pending update")
	}
	f.click("查看 测试模块")
	testkit.UntilText(t, f.tt, "将解密 1 个主机名")
	f.click("确认解密以上主机名")
	// The pending update changes under the open dialog: it keeps its text
	// but gains a host the user has not seen.
	if err := f.store.Update(func(d *model.Doc) {
		p := d.HTTPTools.FindModule("mod_demo").Pending
		p.Contents.Hostnames = &model.MITMHostnames{Include: append(append([]string{}, p.Contents.Hostnames.Include...), "evil.example.com")}
	}); err != nil {
		t.Fatal(err)
	}
	f.settled()
	f.click("确认更新")
	testkit.UntilText(t, f.tt, "更新内容已变化")
	m := f.module("mod_demo")
	if m.Pending == nil || slicesContains(m.Approved.Hostnames, "evil.example.com") || slicesContains(m.Approved.Hostnames, "new.example.com") {
		t.Fatalf("approved something unseen: %+v", m.Approved)
	}
	// The box is ticked again for the update as it is now.
	f.click("确认解密以上主机名")
	f.click("确认更新")
	testkit.Until(t, f.tt, func() bool { return f.module("mod_demo").Pending == nil })
}

func slicesContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestEnqueueAnswersJobsItRefuses(t *testing.T) {
	f := updateFixture(t)
	var got []error
	f.w.enqueue(job{id: "mod_gone", done: func(_ Outcome, err error) { got = append(got, err) }})
	if len(got) != 1 || got[0] != errMissing {
		t.Fatalf("missing module: %v", got)
	}
	f.w.updates.queued["mod_demo"] = true
	f.w.enqueue(job{id: "mod_demo", done: func(_ Outcome, err error) { got = append(got, err) }})
	delete(f.w.updates.queued, "mod_demo")
	if len(got) != 2 || got[1] != errBusy {
		t.Fatalf("busy module: %v", got)
	}
}

// failingDoc is a document that refuses every change.
type failingDoc struct{ inner Doc }

func (d failingDoc) Doc() *model.Doc { return d.inner.Doc() }

func (failingDoc) Update(func(*model.Doc)) error { return fmt.Errorf("保存失败") }

func TestRecordDoesNotReportAnUnsavedChange(t *testing.T) {
	f := updateFixture(t)
	f.srv.SetFetchBody(modURL, moduleText(baseHosts, "^https://old\\.example\\.com/ - reject"))
	f.w.doc = failingDoc{f.store}
	var outcome Outcome = -1
	var gotErr error
	f.w.enqueue(job{id: "mod_demo", done: func(o Outcome, err error) { outcome, gotErr = o, err }})
	testkit.Until(t, f.tt, func() bool { return outcome != -1 && !f.w.Busy() })
	if outcome != Unchanged || gotErr == nil || !strings.Contains(gotErr.Error(), "保存失败") {
		t.Errorf("outcome %v err %v", outcome, gotErr)
	}
}

func TestApplyUpdateReportsChangedContentWithSameText(t *testing.T) {
	text := moduleText(baseHosts, `^https://{{{LANG}}}\.example\.com/ - reject`)
	m := installed(t, text)
	fetch := memFetcher(map[string]string{scriptURL: scriptBody})
	// The same text, another script text: the update waits, it is not
	// "up to date".
	changed, err := Convert(context.Background(), memFetcher(map[string]string{scriptURL: "$done({body: ''})\n"}), Input{Text: m.Raw, BaseURL: modURL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyUpdate(m, changed.Update, time.UnixMilli(1_700_000_900_000)); got != Awaiting {
		t.Errorf("new script text: %v", got)
	}
	m.DiscardPending()
	same, err := Convert(context.Background(), fetch, Input{Text: m.Raw, BaseURL: modURL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyUpdate(m, same.Update, time.UnixMilli(1_700_000_901_000)); got != Unchanged {
		t.Errorf("identical result: %v", got)
	}
	// Other argument values change a rewrite rule and add nothing
	// sensitive: the text keeps its hash, the update is a change.
	hash := m.Hash
	en, err := Convert(context.Background(), fetch, Input{Text: m.Raw, BaseURL: modURL, Arguments: map[string]string{"LANG": "en"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyUpdate(m, en.Update, time.UnixMilli(1_700_000_902_000)); got != Changed || m.Hash != hash || !m.Updated {
		t.Errorf("changed arguments with the same text: %v", got)
	}
	if rules := m.Contents.Rewrites.URL; len(rules) != 2 || !strings.Contains(rules[1].Pattern, "en") {
		t.Errorf("rewrites = %+v", rules)
	}
}

func TestStopUpdatesStopsTheTimer(t *testing.T) {
	f := updateFixture(t)
	f.w.StartUpdates()
	if f.w.timer == nil {
		t.Fatal("timer not started")
	}
	f.w.StopUpdates()
	if f.w.timer != nil {
		t.Error("timer kept")
	}
	f.w.StopUpdates()
	f.w.StartUpdates()
	if f.w.timer == nil {
		t.Error("timer not restarted")
	}
	f.w.StopUpdates()
}
