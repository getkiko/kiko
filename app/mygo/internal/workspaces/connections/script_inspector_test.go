package connections

import (
	"os"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// "Add script for this request" opens the script editor for the record:
// its URL as the pattern and a response script, since the record has a
// response.
func TestInspectorAddScript(t *testing.T) {
	h, doc := newRewriteHarness(t, 1440, 1500)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	h.click(t, "为此请求添加脚本")
	testkit.UntilText(t, tt, "添加脚本")
	tt.Frame()
	h.click(t, "保存")
	testkit.Until(t, tt, func() bool { return len(doc.doc.HTTPTools.Scripts) == 1 })
	s := doc.doc.HTTPTools.Scripts[0]
	if s.Pattern != `^https://api\.github\.com/json$` || s.Type != model.ScriptHTTPResponse || !strings.Contains(s.Source, "$response") ||
		!strings.HasPrefix(s.ID, "script_") {
		t.Fatalf("script %+v", s)
	}
	testkit.Until(t, tt, func() bool { return !h.w.http.sc.Open() })
}

// A script hit opens its script for editing; a hit whose script is gone
// cannot.
func TestInspectorEditScriptFromHit(t *testing.T) {
	h, doc := newRewriteHarnessFrame(t, 1440, 1700, scriptRewriteFrame())
	doc.doc.HTTPTools.Scripts = []*model.HTTPScript{{ID: "s1", Tag: "fix-feed", Name: "修复列表", Type: model.ScriptHTTPResponse,
		Pattern: "^x", Source: "$done({})\n"}}
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	h.click(t, "编辑脚本")
	testkit.UntilText(t, tt, "编辑脚本")
	testkit.Until(t, tt, func() bool { return h.w.http.sc.Open() })
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !h.w.http.sc.Open() })
	// The other six hits name scripts that do not exist: their buttons
	// are disabled by that lookup.
	for _, tag := range []string{"mock", "broken", "slow", "big", "streamed", "noisy"} {
		if scriptFound(doc.doc, tag) {
			t.Fatalf("unexpected script %q", tag)
		}
	}
	if !scriptFound(doc.doc, "fix-feed") {
		t.Fatal("the hit's own script is missing")
	}
}

func scriptFound(doc *model.Doc, tag string) bool {
	for _, s := range doc.HTTPTools.Scripts {
		if s.EffectiveTag() == tag {
			return true
		}
	}
	return false
}

// The acceptance screenshot of the inspector's script hits and the add
// action: written when SCRIPT_PREVIEW_DIR is set.
func TestScriptInspectorScreens(t *testing.T) {
	dir := os.Getenv("SCRIPT_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SCRIPT_PREVIEW_DIR is not set")
	}
	h, doc := newRewriteHarnessFrame(t, 1440, 1500, scriptRewriteFrame())
	doc.doc.HTTPTools.Scripts = []*model.HTTPScript{{ID: "s1", Tag: "fix-feed", Name: "修复列表", Type: model.ScriptHTTPResponse,
		Pattern: "^x", Source: "$done({})\n"}}
	h.click(t, "/json")
	testkit.UntilText(t, h.tt, "概览")
	testkit.UntilText(t, h.tt, "脚本出错 · 2.5 ms")
	shotInspector(t, h.tt, dir, "10-inspector-script-hits")
}
