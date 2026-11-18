package scripts

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// shot writes the screenshot of f in both themes.
func (f *fixture) shot(dir, name string) {
	f.t.Helper()
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
			f.t.Fatal(err)
		}
		if err := png.Encode(file, f.tt.Image()); err != nil {
			f.t.Fatal(err)
		}
		file.Close()
	}
	f.tt.SetDark(false)
	f.tt.Advance(600 * time.Millisecond)
	f.tt.Frame()
}

// TestAcceptanceScreens writes the acceptance screenshots to
// SCRIPT_PREVIEW_DIR.
func TestAcceptanceScreens(t *testing.T) {
	dir := os.Getenv("SCRIPT_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SCRIPT_PREVIEW_DIR is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := func() *model.Doc {
		d := fixtureDoc(true)
		d.HTTPTools.Scripts = append(d.HTTPTools.Scripts,
			&model.HTTPScript{ID: "s-rule", Name: "国内域名", Tag: "cn-domain", Type: model.ScriptRule, Timeout: 100, Source: "const cn = /\\.cn$/.test($request.domain)\n$done(cn)\n"},
			&model.HTTPScript{ID: "s-dns", Name: "内网解析", Tag: "lan-dns", Type: model.ScriptDNS, Source: "if ($request.domain === 'nas.home') {\n  $done({addresses: ['192.168.1.10'], ttl: 60})\n} else {\n  $done()\n}\n"})
		return d
	}

	f := newFixture(t, doc(), 1280, 900)
	testkit.UntilText(t, f.tt, "国内域名")
	f.shot(dir, "64-list")

	f.w.editors.Edit("s-rule")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.shot(dir, "60-edit-rule")
	f.click("取消")
	f.tt.Frame()

	f.w.editors.Edit("s-dns")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.shot(dir, "61-edit-dns")
	f.click("取消")
	f.tt.Frame()

	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionNone, DurationUS: 180, Decision: "true"}
	})
	e := f.openTest("s-rule")
	e.test2.rule.domain = "mirror.example.cn"
	f.click("运行测试")
	testkit.UntilText(t, f.tt, "匹配")
	f.shot(dir, "62-test-rule")
	f.click("取消")
	f.tt.Frame()

	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionNone, DurationUS: 240, Decision: "192.168.1.10",
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelLog, Message: "nas.home"}}}
	})
	e = f.openTest("s-dns")
	e.test2.rule.domain = "nas.home"
	f.click("运行测试")
	testkit.UntilText(t, f.tt, "192.168.1.10")
	f.shot(dir, "63-test-dns")

	n := newFixture(t, doc(), 420, 800)
	testkit.UntilText(t, n.tt, "国内域名")
	n.w.editors.Edit("s-rule")
	testkit.UntilText(t, n.tt, "编辑脚本")
	n.shot(dir, "65-narrow")
}
