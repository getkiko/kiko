package connections

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// rewriteFrame is the inspector fixture whose /json record went through
// three rules: a redirect rule, a request header rule and a response
// header rule.
func rewriteFrame() api.HTTPFrame {
	f := inspectorFrame()
	r := f.Events[0].Record
	r.Rewritten = true
	r.RequestHeaders = append(r.RequestHeaders, api.HTTPHeader{Name: "X-Kiko", Value: "1"})
	r.ResponseHeaders = append(r.ResponseHeaders,
		api.HTTPHeader{Name: "Content-Encoding", Value: "gzip"}, api.HTTPHeader{Name: "Content-Length", Value: "40"},
		api.HTTPHeader{Name: "Server", Value: "edge/1"}, api.HTTPHeader{Name: "X-Id", Value: "7"})
	r.Rewrites = []api.HTTPRewriteHit{
		{Kind: "url", Rule: "u1", Action: "302", Target: "https://new.example.com/json", Status: 302},
		{Kind: "header", Rule: "h1", Action: "add", Side: "request", Changes: []api.HTTPRewriteChange{
			{Op: "add", Name: "X-Kiko", After: "1", HasAfter: true},
			{Op: "replace", Name: "Accept-Encoding", Before: "gzip", HadBefore: true, After: "identity", HasAfter: true},
			{Op: "delete", Name: "Cookie", Before: "sid=abc", HadBefore: true}}},
		{Kind: "header", Rule: "h-gone", Action: "replace", Side: "response", Changes: []api.HTTPRewriteChange{
			{Op: "replace", Name: "Server", Before: "nginx", HadBefore: true, After: "edge/1", HasAfter: true}}},
	}
	return f
}

func rewriteDoc() *model.Doc {
	doc := model.EmptyDoc("")
	doc.HTTPTools = &model.HTTPTools{Rewrites: &model.HTTPRewrites{
		URL:     []*model.URLRewrite{{ID: "u1", Name: "旧域名跳转", Pattern: "^x", Action: "302", Target: "https://new.example.com/json"}},
		Headers: []*model.HeaderRewrite{{ID: "h1", Name: "统一头", Pattern: "^x", Scope: "request", Ops: []*model.HeaderEdit{{Action: "add", Name: "X-Kiko", Value: "1"}}}},
	}}
	return doc
}

// bodyRewriteFrame adds an internal host rewrite and body hits (edited and
// skipped) to the rewrite fixture.
func bodyRewriteFrame() api.HTTPFrame {
	f := rewriteFrame()
	r := f.Events[0].Record
	r.Rewrites = append(r.Rewrites,
		api.HTTPRewriteHit{Kind: "url", Rule: "u-int", Action: "header", Target: "https://internal.example.net:8443/json"},
		api.HTTPRewriteHit{Kind: "body", Rule: "b1", Action: "edit", Side: "response", Changes: []api.HTTPRewriteChange{
			{Op: "json-set", Name: "$.data.price", After: "1", HasAfter: true},
			{Op: "replace-regex", Name: `"id":(\d+)`, After: "12", HasAfter: true}}},
		api.HTTPRewriteHit{Kind: "body", Rule: "b-fix", Action: "edit", Side: "response", Changes: []api.HTTPRewriteChange{
			{Op: "delete", Name: "Content-Digest", Before: "sha-256=:abc:", HadBefore: true},
			{Op: "trailer-dropped", Name: "Trailer", Before: "X-T", HadBefore: true, After: "X-T", HasAfter: true}}},
		api.HTTPRewriteHit{Kind: "body", Rule: "b2", Action: "skipped", Side: "response", Target: "size"},
		api.HTTPRewriteHit{Kind: "body", Rule: "b3", Action: "skipped", Side: "request", Target: "decoded-size"})
	return f
}

func newRewriteHarness(t *testing.T, width, height int) (*harness, *fakeDoc) {
	return newRewriteHarnessFrame(t, width, height, rewriteFrame())
}

func newRewriteHarnessFrame(t *testing.T, width, height int, frame api.HTTPFrame) (*harness, *fakeDoc) {
	requests := new([]string)
	h := newHTTPHarness(t, width, height, enabled(), newHTTPFeed(frame), bodyRoute(requests))
	doc := &fakeDoc{doc: rewriteDoc()}
	h.w.http.doc = doc
	testkit.UntilText(t, h.tt, "/json")
	return h, doc
}

func TestRewriteInspectorGroup(t *testing.T) {
	h, _ := newRewriteHarness(t, 1440, 1500)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	for _, want := range []string{"改写 · 3", "URL 重写", "旧域名跳转", "302 跳转 → https://new.example.com/json · 状态 302",
		"请求头", "统一头", "响应头", "h-gone", "X-Kiko", "Accept-Encoding", "identity", "Cookie", "sid=abc", "edge/1", "nginx", "已改写"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	h.click(t, "请求头 · 4")
	h.click(t, "响应头 · 6")
	testkit.UntilText(t, tt, "X-Id")
	savePreview(t, tt, "rewrite-inspector-light.png")
}

func TestRewriteInspectorBodyHits(t *testing.T) {
	h, _ := newRewriteHarnessFrame(t, 1440, 1700, bodyRewriteFrame())
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	for _, want := range []string{"改写 · 8", "改写主机（内部） → https://internal.example.net:8443/json", "响应正文", "请求正文", "已改写",
		"JSON 设置", "$.data.price", "1 处", "正则替换", "12 处", "未改写 · 超出缓冲上限", "未改写 · 解码后过大", "Content-Digest", "sha-256=:abc:", "丢弃", "Trailer"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	for _, bad := range []string{"trailer-dropped", "X-T 处", "Content-Digest 处"} {
		if tt.HasText(bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
	for reason, want := range map[string]string{"stream": "流式传输", "timeout": "缓冲超时", "encoding": "不支持的编码", "decode": "解码失败", "busy": "缓冲繁忙"} {
		if got := bodySkipLabel(reason); got != want {
			t.Errorf("%s: %q", reason, got)
		}
	}
}

func TestHeaderMarks(t *testing.T) {
	r := newHTTPRow(*rewriteFrame().Events[0].Record)
	marks := headerMarks(rewriteDoc(), r, api.HTTPSideRequest)
	if len(marks) != 2 || marks["x-kiko"].had || marks["x-kiko"].rule != "统一头" || !marks["accept-encoding"].had || marks["accept-encoding"].before != "gzip" {
		t.Fatalf("%+v", marks)
	}
	if _, ok := marks["cookie"]; ok {
		t.Error("a deleted header is marked in the list")
	}
	if got := marks["accept-encoding"].tip(); got != "原值：gzip · 统一头" {
		t.Error(got)
	}
	if got := marks["x-kiko"].tip(); got != "新增 · 统一头" {
		t.Error(got)
	}
	resp := headerMarks(rewriteDoc(), r, api.HTTPSideResponse)
	if resp["server"].rule != "h-gone" || resp["server"].before != "nginx" {
		t.Fatalf("%+v", resp)
	}
	if len(headerMarks(nil, newHTTPRow(api.HTTPRecord{}), api.HTTPSideRequest)) != 0 {
		t.Error("marks without rewrites")
	}
}

func TestRecordActionsOpenEditors(t *testing.T) {
	h, doc := newRewriteHarness(t, 1440, 1500)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")

	// "Save as local mapping" reads the decoded response body and keeps the
	// headers a mapping can carry.
	h.click(t, "保存为本地映射")
	testkit.UntilText(t, tt, "添加 本地映射")
	tt.Frame()
	h.click(t, "保存")
	testkit.Until(t, tt, func() bool { return len(doc.doc.HTTPTools.Rewrites.MapLocal) == 1 })
	m := doc.doc.HTTPTools.Rewrites.MapLocal[0]
	if m.Pattern != `^https://api\.github\.com/json$` || m.Status != 200 || m.Body != `{"id":7,"login":"octocat","tags":["a","b"]}` || m.BodyBase64 != "" {
		t.Fatalf("mapping %+v", m)
	}
	for _, f := range m.Headers {
		switch f.Name {
		case "Content-Encoding", "Content-Length":
			t.Errorf("header %s kept", f.Name)
		}
	}
	if len(m.Headers) != 3 {
		t.Errorf("headers %+v", m.Headers)
	}
	testkit.Until(t, tt, func() bool { return !tt.HasText("添加 本地映射") })

	// "Add rewrite for this URL" opens the URL rule with the URL's pattern.
	h.click(t, "为此 URL 添加改写")
	testkit.UntilText(t, tt, "添加 URL 重写")
	tt.Frame()
	h.click(t, "保存")
	testkit.Until(t, tt, func() bool { return len(doc.doc.HTTPTools.Rewrites.URL) == 2 })
	u := doc.doc.HTTPTools.Rewrites.URL[1]
	if u.Pattern != `^https://api\.github\.com/json$` || u.Action != model.URLActionReject {
		t.Fatalf("rule %+v", u)
	}
}

func TestRecordActionWithoutBodyOrDocument(t *testing.T) {
	h, _ := newRewriteHarness(t, 1440, 1500)
	tt := h.tt
	// /unsaved has no stored body: the mapping opens empty.
	h.click(t, "/unsaved")
	testkit.UntilText(t, tt, "概览")
	h.click(t, "保存为本地映射")
	testkit.UntilText(t, tt, "添加 本地映射")
}

// A body that is truncated, undecodable or not stored is not prefilled; the
// editor says so and saves an empty body.
func TestRecordActionIncompleteBody(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		decodeErr bool
	}{{"truncated", "/big", false}, {"unsaved", "/unsaved", false}, {"decode error", "/json", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.decodeErr {
				decodeErrorIDs["21"] = true
				t.Cleanup(func() { delete(decodeErrorIDs, "21") })
			}
			h, doc := newRewriteHarness(t, 1440, 1500)
			tt := h.tt
			h.click(t, tc.row)
			testkit.UntilText(t, tt, "概览")
			h.click(t, "保存为本地映射")
			testkit.UntilText(t, tt, "添加 本地映射")
			testkit.UntilText(t, tt, "正文不完整，请手动填写")
			tt.Frame()
			h.click(t, "保存")
			testkit.Until(t, tt, func() bool { return len(doc.doc.HTTPTools.Rewrites.MapLocal) == 1 })
			if m := doc.doc.HTTPTools.Rewrites.MapLocal[0]; m.Body != "" || m.BodyBase64 != "" {
				t.Fatalf("body prefilled: %+v", m)
			}
		})
	}
}

func TestPlainConnectionState(t *testing.T) {
	c := sample
	c.Mitm = api.MitmPlain
	c.Protocol = "http"
	if got := connectionNetworkText(&c); got != "TCP · HTTP · 改写" {
		t.Errorf("network %q", got)
	}
	if got := connectionProtocolLabel(&c); got != "HTTP" {
		t.Errorf("label %q", got)
	}
	if c.Intercepted() {
		t.Error("a plain connection counts as decrypted")
	}
}

// The acceptance screenshots of the inspector's Rewrites group: written when
// REWRITE_PREVIEW_DIR is set.
func TestRewriteInspectorScreens(t *testing.T) {
	dir := os.Getenv("REWRITE_PREVIEW_DIR")
	if dir == "" {
		t.Skip("REWRITE_PREVIEW_DIR is not set")
	}
	h, _ := newRewriteHarness(t, 1440, 1700)
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	h.click(t, "请求头 · 4")
	h.click(t, "响应头 · 6")
	testkit.UntilText(t, tt, "X-Id")
	shotInspector(t, tt, dir, "13-inspector-rewrites")
	h2, _ := newRewriteHarnessFrame(t, 1440, 1700, bodyRewriteFrame())
	h2.click(t, "/json")
	testkit.UntilText(t, h2.tt, "概览")
	testkit.UntilText(t, h2.tt, "未改写 · 超出缓冲上限")
	shotInspector(t, h2.tt, dir, "18-inspector-body-hits")
}

func shotInspector(t *testing.T, tt *ui.Tester, dir, name string) {
	t.Helper()
	for _, dark := range []bool{false, true} {
		suffix := "-light.png"
		if dark {
			suffix = "-dark.png"
		}
		tt.SetDark(dark)
		tt.Frame()
		tt.Advance(600 * time.Millisecond)
		tt.Frame()
		f, err := os.Create(filepath.Join(dir, name+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

// scriptRewriteFrame adds script hits of every action to the rewrite
// fixture.
func scriptRewriteFrame() api.HTTPFrame {
	f := rewriteFrame()
	r := f.Events[0].Record
	r.Rewrites = append(r.Rewrites,
		api.HTTPRewriteHit{Kind: "script", Action: "modify", Side: "response", Target: "fix-feed"},
		api.HTTPRewriteHit{Kind: "script", Action: "respond", Side: "request", Target: "mock", Status: 200},
		api.HTTPRewriteHit{Kind: "script", Action: "error", Side: "request", Target: "broken"},
		api.HTTPRewriteHit{Kind: "script", Action: "timeout", Side: "response", Target: "slow"},
		api.HTTPRewriteHit{Kind: "script", Action: "skipped", Side: "response", Target: "big", Changes: []api.HTTPRewriteChange{{Op: "skipped", Name: "size"}}},
		api.HTTPRewriteHit{Kind: "script", Action: "skipped", Side: "request", Target: "streamed", Run: &api.HTTPScriptRun{Tag: "streamed", Reason: "stream"}},
		api.HTTPRewriteHit{Kind: "script", Action: "error", Side: "request", Target: "noisy", Run: &api.HTTPScriptRun{Tag: "noisy", DurationUS: 2500, Error: "TypeError: x is undefined", Logs: []api.HTTPScriptLog{{Level: "info", Message: "console line"}}}})
	return f
}

func TestRewriteInspectorScriptHits(t *testing.T) {
	h, doc := newRewriteHarnessFrame(t, 1440, 1700, scriptRewriteFrame())
	doc.doc.HTTPTools.Scripts = []*model.HTTPScript{{ID: "s1", Tag: "fix-feed", Name: "修复列表", Type: model.ScriptHTTPResponse}}
	tt := h.tt
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	for _, want := range []string{"改写 · 10", "响应脚本", "请求脚本", "修复列表", "已修改", "已应答 · 状态 200", "mock", "脚本出错", "broken", "脚本超时", "slow", "未运行 · 超出缓冲上限", "未运行 · 流式传输", "脚本出错 · 2.5 ms", "TypeError: x is undefined", "console line"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
}

func TestScriptHitLabels(t *testing.T) {
	for action, want := range map[string]string{"modify": "已修改", "respond": "已应答", "abort": "已中止", "none": "未修改",
		"error": "脚本出错", "timeout": "脚本超时", "limit": "超出资源限制", "paused": "已暂停", "skipped": "未运行"} {
		if got := rewriteDetail(api.HTTPRewriteHit{Kind: "script", Action: action}); got != want {
			t.Errorf("%s: %q", action, got)
		}
	}
	if got := rewriteKindLabel(api.HTTPRewriteHit{Kind: "script"}); got != "脚本" {
		t.Fatal(got)
	}
	if got := hitTitle(nil, api.HTTPRewriteHit{Kind: "script", Target: "t"}); got != "t" {
		t.Fatal(got)
	}
}
