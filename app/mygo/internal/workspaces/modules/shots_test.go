package modules

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// shot writes the screenshot of f in both themes.
func shot(t *testing.T, f *fixture, dir, name string) {
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

// TestAcceptanceScreens writes the acceptance screenshots to
// MODULE_PREVIEW_DIR.
func TestAcceptanceScreens(t *testing.T) {
	dir := os.Getenv("MODULE_PREVIEW_DIR")
	if dir == "" {
		t.Skip("MODULE_PREVIEW_DIR is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pages := func(f *fixture) {
		f.srv.SetFetchBody(modURL, moduleText(baseHosts))
		f.srv.SetFetchBody(scriptURL, scriptBody)
	}

	f := newFixture(t, fixtureDoc(), 1100, 700)
	f.tt.Frame()
	shot(t, f, dir, "40-empty")

	// The add dialog: source, fetching, preview with the unsupported list.
	pages(f)
	f.click("添加模块")
	testkit.UntilText(t, f.tt, "获取")
	shot(t, f, dir, "41-add-url")
	f.click("本地文件")
	shot(t, f, dir, "42-add-file")
	f.click("链接")
	f.click("模块链接")
	f.tt.Type(modURL)
	f.click("获取")
	testkit.UntilText(t, f.tt, "将解密 2 个主机名")
	f.click("未支持 1")
	testkit.UntilText(t, f.tt, "整节未支持")
	shot(t, f, dir, "43-add-preview")

	// The install preview with DIRECT rules and the downloads unfolded.
	h := newFixture(t, fixtureDoc(), 1100, 900)
	h.srv.SetFetchBody(modURL, moduleText(baseHosts)+"[Rule]\nDOMAIN-SUFFIX,cdn.example.com,DIRECT\nIP-CIDR,8.8.0.0/16,DIRECT\n")
	h.srv.SetFetchBody(scriptURL, scriptBody)
	h.fetchInstall(modURL)
	testkit.UntilText(t, h.tt, "将直连 2 条规则")
	h.click("下载 1")
	testkit.UntilText(t, h.tt, scriptURL)
	shot(t, h, dir, "52-add-direct-downloads")

	// The module list: normal, updated, failed, pending, disabled.
	mods := make([]*model.HTTPModule, 5)
	for i := range mods {
		mods[i] = installed(t, moduleText(baseHosts))
		mods[i].ID = "mod_" + string(rune('a'+i))
	}
	mods[0].Name = "YouTube 增强"
	mods[0].Updated = true
	mods[1].Name, mods[1].Source.URL, mods[1].LastError = "哔哩哔哩助手", "https://raw.githubusercontent.com/example/Bilibili.sgmodule", "下载失败：连接超时"
	mods[2].Name, mods[2].Source = "广告拦截", model.ModuleSource{Kind: model.ModuleSourceFile}
	mods[3].Name, mods[3].Enabled = "重定向", false
	mods[4].Name, mods[4].Source.URL = "去广告脚本", "https://example.com/very/long/path/to/a/module/with/a/long/name.sgmodule"
	mods[4].Pending = &model.ModulePending{Name: "去广告脚本", Hash: "x", Raw: "x",
		Added:    model.ModuleDiff{Hostnames: []string{"new.example.com", "cdn2.example.com"}, Direct: []string{"domain-suffix,cdn2.example.com", "ip-cidr,8.8.0.0/16"}, Scripts: []model.ModuleScriptChange{{Tag: "price", Name: "价格", SHA256: model.HashModuleRaw(scriptBody), Key: "k", Type: "http-response", Pattern: `^https://api\.example\.com/items`, Changed: true}}},
		Contents: installed(t, moduleText(baseHosts+", new.example.com, cdn2.example.com")+"[Rule]\nDOMAIN-SUFFIX,cdn2.example.com,DIRECT\nIP-CIDR,8.8.0.0/16,DIRECT\n").Contents}
	g := newFixture(t, fixtureDoc(mods...), 1280, 900)
	testkit.UntilText(t, g.tt, "YouTube 增强")
	shot(t, g, dir, "44-list")
	g.click("查看 去广告脚本")
	testkit.UntilText(t, g.tt, "确认更新")
	shot(t, g, dir, "45-pending")
	g.click("确认解密以上主机名、运行以上脚本、直连以上规则")
	shot(t, g, dir, "46-pending-confirmed")
	g.click("放弃更新")
	g.w.openParams("mod_a")
	testkit.UntilText(t, g.tt, "默认 zh")
	shot(t, g, dir, "47-params")
	g.tt.Key(0, uiEscape)
	testkit.Until(t, g.tt, func() bool { return !g.w.ParamsOpen() })
	g.w.openContent("mod_a")
	testkit.UntilText(t, g.tt, "复制全部")
	g.click("主机名 2")
	g.click("未支持 1")
	shot(t, g, dir, "48-content")
	g.tt.Key(0, uiEscape)
	testkit.Until(t, g.tt, func() bool { return !g.w.ContentOpen() })
	g.click("更多操作")
	g.tt.Frame()
	shot(t, g, dir, "49-menu")
	g.click("删除")
	testkit.UntilText(t, g.tt, "删除「YouTube 增强」？")
	shot(t, g, dir, "50-delete-confirm")

	n := newFixture(t, fixtureDoc(mods[:3]...), 420, 800)
	testkit.UntilText(t, n.tt, "广告拦截")
	shot(t, n, dir, "51-narrow")
}
