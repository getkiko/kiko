package connections

import (
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// moduleHitDoc has an installed module with one URL rewrite rule and one
// script, besides the user's own rules.
func moduleHitDoc() *model.Doc {
	doc := rewriteDoc()
	m := model.NewHTTPModule()
	m.ID, m.Name, m.Enabled = "mod_ads", "去广告", true
	m.Contents = model.ModuleContents{
		Rewrites: &model.HTTPRewrites{
			URL:     []*model.URLRewrite{{ID: "x", Pattern: `^https://ads\.example\.com/`, Action: "reject"}},
			Headers: []*model.HeaderRewrite{{ID: "y", Pattern: `^https://hdr\.example\.com/`, Scope: "request"}},
		},
		Scripts: []*model.HTTPScript{{ID: "s", Tag: "feed", Name: "信息流", Type: model.ScriptHTTPResponse}, {ID: "t", Tag: "plain", Type: model.ScriptHTTPResponse}},
	}
	doc.HTTPTools.Modules = []*model.HTTPModule{m}
	return doc
}

func TestModuleOfHit(t *testing.T) {
	doc := moduleHitDoc()
	cases := []struct {
		name string
		hit  api.HTTPRewriteHit
		want string
	}{
		{"url rule", api.HTTPRewriteHit{Kind: "url", Rule: "mod:mod_ads:1"}, `去广告 · ^https://ads\.example\.com/`},
		{"header rule", api.HTTPRewriteHit{Kind: "header", Rule: "mod:mod_ads:2"}, `去广告 · ^https://hdr\.example\.com/`},
		{"rule beyond the module's", api.HTTPRewriteHit{Kind: "url", Rule: "mod:mod_ads:9"}, "去广告 · 第 9 条"},
		{"named script", api.HTTPRewriteHit{Kind: "script", Target: "mod_ads-feed"}, "去广告 · 信息流"},
		{"script by tag", api.HTTPRewriteHit{Kind: "script", Rule: "mod_ads-plain"}, "去广告 · plain"},
		{"script by run tag", api.HTTPRewriteHit{Kind: "script", Run: &api.HTTPScriptRun{Tag: "mod_ads-feed"}}, "去广告 · 信息流"},
	}
	for _, tc := range cases {
		o, ok := moduleOfHit(doc, tc.hit)
		if !ok || o.title() != tc.want || o.id != "mod_ads" || hitTitle(doc, tc.hit) != tc.want {
			t.Errorf("%s: %+v %v, title %q, want %q", tc.name, o, ok, hitTitle(doc, tc.hit), tc.want)
		}
	}
	// The user's own ids, a module that is gone and a user tag shaped like a
	// module's stay the user's.
	for _, hit := range []api.HTTPRewriteHit{
		{Kind: "url", Rule: "u1"},
		{Kind: "url", Rule: "mod:mod_gone:1"},
		{Kind: "script", Target: "mod_gone-feed"},
		{Kind: "script", Target: "mod_ads-missing"},
		{Kind: "script", Target: "fix-feed"},
	} {
		if o, ok := moduleOfHit(doc, hit); ok {
			t.Errorf("%+v taken for module %+v", hit, o)
		}
	}
	if got := hitTitle(doc, api.HTTPRewriteHit{Kind: "url", Rule: "u1"}); got != "旧域名跳转" {
		t.Errorf("user rule title %q", got)
	}
	if _, ok := moduleOfHit(nil, api.HTTPRewriteHit{Kind: "url", Rule: "mod:mod_ads:1"}); ok {
		t.Error("module found without a document")
	}
}

func TestRewriteInspectorModuleHits(t *testing.T) {
	f := rewriteFrame()
	r := f.Events[0].Record
	r.Rewrites = append(r.Rewrites,
		api.HTTPRewriteHit{Kind: "url", Rule: "mod:mod_ads:1", Action: "reject"},
		api.HTTPRewriteHit{Kind: "header", Rule: "mod:mod_ads:2", Action: "replace", Side: "response", Changes: []api.HTTPRewriteChange{
			{Op: "replace", Name: "Server", Before: "nginx", HadBefore: true, After: "edge/1", HasAfter: true}}},
		api.HTTPRewriteHit{Kind: "script", Action: "modify", Side: "response", Target: "mod_ads-feed"},
		api.HTTPRewriteHit{Kind: "script", Action: "modify", Side: "response", Target: "fix-feed"})
	h, doc := newRewriteHarnessFrame(t, 1440, 1700, f)
	doc.doc = moduleHitDoc()
	doc.doc.HTTPTools.Scripts = []*model.HTTPScript{{ID: "s1", Tag: "fix-feed", Name: "修复列表", Type: model.ScriptHTTPResponse}}
	h.w.http.doc = doc
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	for _, want := range []string{`去广告 · ^https://ads\.example\.com/`, "去广告 · 信息流", "修复列表", "前往模块", "编辑脚本"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	// Each of the three module hits has the way to its module, and only the
	// user's script keeps "Edit script" (a button's label shows twice in Texts).
	count := func(text string) int {
		n := 0
		for _, s := range tt.Texts() {
			if s == text {
				n++
			}
		}
		return n
	}
	if count("前往模块") != 3*count("编辑脚本") {
		t.Errorf("%d 前往模块 for %d 编辑脚本: %q", count("前往模块"), count("编辑脚本"), tt.Texts())
	}
	h.click(t, "前往模块")
	if len(h.pages) == 0 || h.pages[len(h.pages)-1] != "rewrites:module:mod_ads" {
		t.Fatalf("navigation %v", h.pages)
	}
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "去广告") {
		t.Errorf("texts %q", tt.Texts())
	}
	savePreview(t, tt, "hits-module-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "hits-module-dark.png")
	tt.SetDark(false)
}
