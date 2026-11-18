package rewrites

import (
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/scripts"
)

const priceSource = `// 价格归零：把商品价格改成 0，并去掉广告字段
const body = JSON.parse($response.body)
if (body.data && body.data.price !== undefined) {
  body.data.price = 0
  console.log('price reset', $request.url)
}
delete body.ads
$done({ body: JSON.stringify(body) })
`

// scriptDoc is fixtureDoc with scripts of each type.
func scriptDoc(mitm bool) *model.Doc {
	doc := fixtureDoc(mitm)
	doc.HTTPTools.Scripts = []*model.HTTPScript{
		{ID: "s-ua", Name: "统一 UA", Type: model.ScriptHTTPRequest, Pattern: `^https://api\.example\.com/`,
			Source: "$request.headers['User-Agent'] = 'Kiko/1'\n$done({ headers: $request.headers })\n"},
		{ID: "s-price", Name: "价格归零", Tag: "price", Type: model.ScriptHTTPResponse, Pattern: `^https://api\.example\.com/items`,
			RequiresBody: true, MaxSize: 512 << 10, Timeout: 5, Source: priceSource},
		{ID: "s-job", Type: model.ScriptGeneric, Disabled: true, Source: "console.log('job')\n$done({})\n"},
		{ID: "s-bad", Name: "坏模式", Type: model.ScriptHTTPRequest, Pattern: "(", Source: "$done({})\n"},
	}
	return doc
}

func TestScriptTab(t *testing.T) {
	f := newFixture(t, scriptDoc(true), 1280, 800)
	tt := f.tt
	testkit.UntilText(t, tt, "广告拦截")
	f.click("脚本")
	testkit.UntilText(t, tt, "价格归零")
	for _, want := range []string{"统一 UA", "s-job", "坏模式", "4 个脚本", "搜索名称、标识、模式", "存储", "日志", "添加脚本"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("广告拦截") {
		t.Error("a rule of another tab")
	}
	// The toolbar adds a script; Escape closes it and the tab stays.
	f.click("添加脚本")
	testkit.UntilText(t, tt, "添加脚本")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.Scripts().Editors().Open() })
	// Store and log pages replace the list; the way back is the first row.
	f.click("存储")
	testkit.Until(t, tt, func() bool { return f.w.Scripts().InSub() })
	if tt.HasText("价格归零") || tt.HasText("搜索名称、标识、模式") {
		t.Errorf("list under a sub page: %q", tt.Texts())
	}
	f.click("脚本")
	testkit.Until(t, tt, func() bool { return !f.w.Scripts().InSub() })
	testkit.UntilText(t, tt, "价格归零")
	// The rule tabs keep working.
	f.click("URL 重写")
	testkit.UntilText(t, tt, "广告拦截")
	if tt.HasText("价格归零") {
		t.Error("a script on the rule tab")
	}
	f.w.Open("script")
	testkit.UntilText(t, tt, "价格归零")
	// The page stopping closes the editor and the sub page.
	f.w.Scripts().Editors().Edit("s-ua")
	testkit.UntilText(t, tt, "编辑脚本")
	f.w.SetActive(false)
	tt.Frame()
	if f.w.Scripts().Editors().Open() {
		t.Error("script editor open after the page stopped")
	}
}

// routeRecords serves the captured requests of the test area.
func routeRecords(srv *testkit.Server, records []api.HTTPRecord, bodies map[string][]byte) {
	srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v1/http/records"), "/")
		if rest == "" {
			testkit.JSON(api.HTTPList{Records: records})(w, r)
			return
		}
		id, tail, _ := strings.Cut(rest, "/")
		if tail == "body" {
			data := bodies[id+"/"+r.URL.Query().Get("side")]
			h := w.Header()
			h.Set("X-Total", strconv.Itoa(len(data)))
			h.Set("X-Original-Size", strconv.Itoa(len(data)))
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

// scriptShot writes the screenshot of f in both themes.
func scriptShot(t *testing.T, f *fixture, dir, name string) {
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

func TestScriptAcceptanceScreens(t *testing.T) {
	dir := os.Getenv("SCRIPT_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SCRIPT_PREVIEW_DIR is not set")
	}
	records := []api.HTTPRecord{
		{ID: "21", Method: "GET", Host: "api.example.com", Path: "/items?page=2", URL: "https://api.example.com/items?page=2", Status: 200,
			RequestHeaders:  []api.HTTPHeader{{Name: "Accept", Value: "application/json"}, {Name: "User-Agent", Value: "curl/8.4"}},
			ResponseHeaders: []api.HTTPHeader{{Name: "Content-Type", Value: "application/json"}}, ResponseBytes: 64, ResponseBodySaved: true,
			Timing: &api.HTTPTiming{Start: time.Date(2026, 11, 4, 9, 41, 7, 0, time.Local).UnixNano()}},
		{ID: "22", Method: "POST", Host: "www.example.org", Path: "/login", URL: "https://www.example.org/login", Status: 302,
			Timing: &api.HTTPTiming{Start: time.Date(2026, 11, 4, 9, 40, 12, 0, time.Local).UnixNano()}},
		{ID: "23", Method: "GET", Host: "cdn.example.net", Path: "/app.js", URL: "https://cdn.example.net/app.js", Status: 200,
			Timing: &api.HTTPTiming{Start: time.Date(2026, 11, 4, 9, 40, 1, 0, time.Local).UnixNano()}},
	}
	bodies := map[string][]byte{"21/response": []byte(`{"data":{"price":19.9,"title":"Kiko"},"ads":[{"id":1}]}`)}

	// 01 the list, with counters.
	f := newFixture(t, scriptDoc(true), 1440, 760)
	f.srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{
		{Tag: "s-ua", Type: "http-request", Runs: 1284},
		{Tag: "price", Type: "http-response", Runs: 96, Errors: 3},
		{Tag: "s-job", Type: "generic", Runs: 2, Paused: true},
	}})
	f.w.SetActive(true)
	f.click("脚本")
	testkit.UntilText(t, f.tt, "价格归零")
	testkit.UntilText(t, f.tt, "1284")
	scriptShot(t, f, dir, "01-script-list")

	// 04 the editor with a highlighted source.
	f.click("编辑 价格归零")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.tt.Frame()
	scriptShot(t, f, dir, "04-editor-source")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.Scripts().Editors().Open() })

	// 06 the test area, a hand-written request with its result, in a window
	// tall enough to show the result under the input.
	f = newFixture(t, scriptDoc(true), 1440, 1180)
	f.click("脚本")
	testkit.UntilText(t, f.tt, "价格归零")
	f.srv.SetEvaluate(func(req api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{
			Action: api.ScriptActionModify, DurationUS: 3400,
			URL:            "https://api.example.com/v2/items",
			HeadersChanged: true,
			Headers: []api.ScriptHeader{{Name: "Accept", Value: "application/json"}, {Name: "User-Agent", Value: "Kiko/1"},
				{Name: "X-Script", Value: "1"}},
			Logs: []api.ScriptLogLine{
				{Level: api.ScriptLogLevelLog, Message: "price reset https://api.example.com/items"},
				{Level: api.ScriptLogLevelWarn, Message: "field ads missing"}},
			Notifications: []api.ScriptNotification{{Title: "价格已重置", Body: "api.example.com"}},
		}
	})
	f.click("编辑 统一 UA")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.click("测试")
	testkit.UntilText(t, f.tt, "运行测试")
	f.click("手填")
	f.click("URL")
	f.tt.Type("https://api.example.com/items")
	f.click("运行测试")
	testkit.UntilText(t, f.tt, "已修改")
	f.tt.Frame()
	scriptShot(t, f, dir, "06-test-manual-result")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.Scripts().Editors().Open() })

	// 07 the test area on a captured request, a response script.
	routeRecords(f.srv, records, bodies)
	f.srv.SetEvaluate(func(req api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionModify, DurationUS: 1800, BodyChanged: true,
			Body: []byte(`{"data":{"price":0,"title":"Kiko"}}`),
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelLog, Message: "price reset https://api.example.com/items?page=2"}}}
	})
	f.click("编辑 价格归零")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.click("测试")
	testkit.UntilText(t, f.tt, "运行测试")
	f.click("已抓取的请求：选择一条请求")
	testkit.UntilText(t, f.tt, "GET api.example.com/items?page=2")
	f.click("GET api.example.com/items?page=2")
	testkit.UntilText(t, f.tt, "请求头 2")
	f.click("运行测试")
	testkit.UntilText(t, f.tt, "已修改")
	f.tt.Frame()
	scriptShot(t, f, dir, "07-test-captured-result")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.Scripts().Editors().Open() })

	// 05 a problem of the source.
	f.w.update(func(d *model.Doc) {
		d.HTTPTools.Scripts[2].Type = model.ScriptHTTPRequest
		d.HTTPTools.Scripts[2].Pattern = `^https://x\.example/`
		d.HTTPTools.Scripts[2].Source = "$done(" + strings.Repeat("(", model.MaxScriptDepth+20) + ")\n"
	})
	f.click("编辑 s-job")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.tt.Frame()
	scriptShot(t, f, dir, "05-editor-source-error")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.Scripts().Editors().Open() })

	// 11 a long script in the editor (the dark shot is part of every shot).
	var long strings.Builder
	long.WriteString("// 汇总页面中的商品数据\nconst items = []\nlet total = 0\n")
	for i := 0; i < 90; i++ {
		long.WriteString("if (/item-" + strconv.Itoa(i) + `/.test($request.url)) { items.push({ id: ` + strconv.Itoa(i) + `, name: "item", price: ` + strconv.Itoa(i*3) + ".5 }); total += " + strconv.Itoa(i) + " }\n")
	}
	long.WriteString("$done({ body: JSON.stringify({ items, total }) })\n")
	f.w.update(func(d *model.Doc) { d.HTTPTools.Scripts[0].Source = long.String() })
	f.click("编辑 统一 UA")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.tt.Frame()
	scriptShot(t, f, dir, "11-editor-long-source")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return !f.w.Scripts().Editors().Open() })

	// 02 and 03 empty states.
	on := model.EmptyDoc(model.PlatformLinux)
	on.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true}}
	empty := newFixture(t, on, 1100, 600)
	empty.click("脚本")
	testkit.UntilText(t, empty.tt, "暂无脚本")
	scriptShot(t, empty, dir, "02-empty")
	off := newFixture(t, model.EmptyDoc(model.PlatformLinux), 1100, 600)
	off.w.Open("script")
	testkit.UntilText(t, off.tt, "未开启 HTTPS 解密")
	scriptShot(t, off, dir, "03-decryption-off")

	// 08 and 09 narrow windows.
	narrow := newFixture(t, scriptDoc(true), 600, 700)
	narrow.w.Open("script")
	testkit.UntilText(t, narrow.tt, "价格归零")
	scriptShot(t, narrow, dir, "08-narrow-list")
	narrow.click("编辑 价格归零")
	testkit.UntilText(t, narrow.tt, "编辑脚本")
	scriptShot(t, narrow, dir, "09-narrow-editor")

	// 12 the leave guard.
	f.click("编辑 统一 UA")
	testkit.UntilText(t, f.tt, "编辑脚本")
	f.click("脚本参数")
	f.tt.Type("n=1")
	f.tt.Key(0, ui.KeyEscape)
	testkit.UntilText(t, f.tt, "放弃未保存的更改？")
	scriptShot(t, f, dir, "12-editor-leave-guard")
}

var _ = scripts.Seed{}
var _ = json.Marshal

// Leaving the Scripts tab stops what its store and log pages read.
func TestScriptSubPagesStopOnOtherTab(t *testing.T) {
	f := newFixture(t, scriptDoc(true), 1280, 800)
	tt := f.tt
	f.w.SetActive(true)
	testkit.UntilText(t, tt, "广告拦截")
	f.click("脚本")
	testkit.UntilText(t, tt, "价格归零")
	f.click("存储")
	testkit.Until(t, tt, func() bool { return f.w.Scripts().Running() })
	f.click("脚本")
	testkit.Until(t, tt, func() bool { return !f.w.Scripts().InSub() })
	f.click("日志")
	testkit.Until(t, tt, func() bool { return f.w.Scripts().Running() })
	// The tab strip is under the sub page; switch by the workspace.
	f.w.Open(KindURL)
	tt.Frame()
	if f.w.Scripts().Running() {
		t.Fatal("the log follows on another tab")
	}
	f.w.Open("script")
	tt.Frame()
	f.w.SetActive(false)
	tt.Frame()
	if f.w.Scripts().Running() {
		t.Fatal("running after the page stopped")
	}
}
