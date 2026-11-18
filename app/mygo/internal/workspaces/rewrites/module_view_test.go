package rewrites

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// moduleDoc is fixtureDoc with one installed module.
func moduleDoc() *model.Doc {
	doc := fixtureDoc(true)
	doc.HTTPTools.Modules = []*model.HTTPModule{{
		ID: "mod_demo", Name: "广告模块", Enabled: true, Version: "2.1", UpdateInterval: 24,
		Source: model.ModuleSource{Kind: model.ModuleSourceURL, URL: "https://modules.example.com/ads.sgmodule"},
		Contents: model.ModuleContents{
			Hostnames: &model.MITMHostnames{Include: []string{"ads.example.com"}},
			Rewrites:  &model.HTTPRewrites{URL: []*model.URLRewrite{{ID: "u", Pattern: `^https://ads\.example\.com/`, Action: model.URLActionReject}}},
		},
		Approved: model.ModuleApproved{Hostnames: []string{"ads.example.com"}},
	}}
	return doc
}

func TestModuleTab(t *testing.T) {
	f := newFixture(t, moduleDoc(), 1280, 800)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	for _, want := range []string{"URL 重写", "脚本", "模块"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	f.click("模块")
	testkit.UntilText(t, tt, "广告模块")
	for _, want := range []string{"modules.example.com", "2.1", "搜索名称、来源", "添加模块", "更新全部", "1 个模块"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("旧域名跳转") {
		t.Error("a rule of another tab")
	}
	// The add dialog opens from the toolbar; leaving the tab closes it.
	f.click("添加模块")
	testkit.UntilText(t, tt, "获取")
	if !f.w.Modules().Dialogs() {
		t.Error("dialog not open")
	}
	f.w.Open("url")
	testkit.UntilText(t, tt, "旧域名跳转")
	if f.w.Modules().Dialogs() {
		t.Error("dialog open on another tab")
	}
	// The rule tabs keep working and Open shows the tab.
	f.w.Open("module")
	testkit.UntilText(t, tt, "广告模块")
	f.w.Open("script")
	testkit.UntilText(t, tt, "添加脚本")
}

func TestModuleTabNoticeWithoutDecryption(t *testing.T) {
	doc := moduleDoc()
	doc.HTTPTools.MITM.Enabled = false
	f := newFixture(t, doc, 1280, 800)
	f.w.Open("module")
	testkit.UntilText(t, f.tt, "未开启 HTTPS 解密")
}

func TestOpenModuleByID(t *testing.T) {
	f := newFixture(t, moduleDoc(), 1280, 800)
	f.w.Open("module:mod_demo")
	testkit.UntilText(t, f.tt, "广告模块")
}
