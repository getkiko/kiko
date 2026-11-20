package routes

import (
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A new traffic rule can sniff, and a sniff rule shows its "Override
// destination" switch.
func TestNewSniffRuleShowsOverride(t *testing.T) {
	f := newFixture(t, nil)
	f.w.addRule(f.w.doc())
	f.tt.Frame()
	e := f.w.editor
	if e == nil || !e.isNew {
		t.Fatal("no new rule editor")
	}
	if f.tt.HasText("覆盖连接目标") {
		t.Fatal("a forwarding rule shows the override switch")
	}
	e.setAction(model.ActionSniff)
	e.route.OverrideDestination = true
	f.tt.Frame()
	f.tt.Advance(time.Second)
	if !f.tt.HasText("覆盖连接目标") || !f.tt.HasText("用嗅探到的域名连接，由出口（代理服务器）解析域名") {
		t.Fatalf("no override switch: %q", f.tt.Texts())
	}
	if dir := os.Getenv("CONSOLE_PREVIEW_DIR"); dir != "" {
		out, _ := os.Create(dir + "/sniff-rule.png")
		defer out.Close()
		_ = png.Encode(out, f.tt.Image())
	}
}
