package scripts

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// openTest opens the editor of script id on its test tab.
func (f *fixture) openTest(id string) *editor {
	f.t.Helper()
	if !f.w.editors.Edit(id) {
		f.t.Fatalf("no script %s", id)
	}
	testkit.UntilText(f.t, f.tt, "编辑脚本")
	f.click("测试")
	testkit.UntilText(f.t, f.tt, "运行测试")
	return f.w.editors.ed
}

func modifyResult(api.ScriptEvalRequest) api.ScriptEvalResult {
	return api.ScriptEvalResult{
		Action: api.ScriptActionModify, DurationUS: 12500,
		URL:            "https://api.example.com/v2/items",
		HeadersChanged: true,
		Headers:        []api.ScriptHeader{{Name: "Accept", Value: "*/*"}, {Name: "X-Test", Value: "1"}},
		Logs: []api.ScriptLogLine{
			{Level: api.ScriptLogLevelLog, Message: "hello from the script"},
			{Level: api.ScriptLogLevelError, Message: "something odd"},
		},
		Notifications: []api.ScriptNotification{{Title: "通知标题", Subtitle: "副标题", Body: "通知正文"}},
	}
}

func TestManualRunShowsResult(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.srv.SetEvaluate(modifyResult)
	e := f.openTest("s-ua")
	x := e.test2
	// A hand-written request: the method, URL, headers and body.
	f.click("手填")
	x.method = "POST"
	x.url = "https://api.example.com/items"
	x.reqHeaders = "Accept: */*\nCookie: a=b"
	x.reqBody.SetText(`{"n":1}`)
	f.click("运行测试")
	testkit.UntilText(t, tt, "已修改")
	f.want("12.5 ms", "差异", "日志 · 2", "hello from the script", "something odd", "通知 · 1", "通知标题", "副标题", "通知正文")

	reqs := f.srv.ScriptEvalRequests()
	if len(reqs) != 1 {
		t.Fatalf("evaluate calls: %d", len(reqs))
	}
	got := reqs[0]
	if got.Type != api.ScriptTypeRequest || got.Source != uaSource || got.Pattern != `^https://api\.example\.com/` || got.Tag != "s-ua" ||
		got.Request == nil || got.Request.URL != "https://api.example.com/items" || got.Request.Method != "POST" ||
		len(got.Request.Headers) != 2 || got.Request.Headers[1].Name != "Cookie" || string(got.Request.Body) != `{"n":1}` || !got.Request.HasBody ||
		got.Response != nil || got.Store != nil {
		t.Fatalf("evaluate request %+v", got)
	}
	// The draft runs, not the saved script.
	e.code.SetText("$done({})\n")
	e.argument = "x=1"
	e.requires, e.binary = true, true
	e.timeout, e.maxSize = "4", "512"
	f.click("运行测试")
	testkit.Until(t, tt, func() bool { return len(f.srv.ScriptEvalRequests()) == 2 })
	got = f.srv.ScriptEvalRequests()[1]
	if got.Source != "$done({})\n" || got.Argument != "x=1" || !got.RequiresBody || !got.BinaryBody || got.TimeoutMS != 4000 || got.MaxSize != 512*1024 {
		t.Fatalf("draft not used: %+v", got)
	}
	if f.scripts()[0].Source != uaSource {
		t.Fatal("run changed the saved script")
	}
}

func TestManualInputProblems(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	testkit.UntilText(t, f.tt, "统一 UA")
	e := f.openTest("s-ua")
	f.click("手填")
	f.click("运行测试")
	testkit.UntilText(t, f.tt, "请填写 URL")
	e.test2.url = "https://x.example/"
	e.test2.reqHeaders = "no colon"
	f.click("运行测试")
	testkit.UntilText(t, f.tt, "请求头：第 1 行应为「名称: 值」")
	if n := len(f.srv.ScriptEvalRequests()); n != 0 {
		t.Fatalf("ran with invalid input: %d calls", n)
	}
}

func TestResponseScriptManualInput(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1100)
	tt := f.tt
	testkit.UntilText(t, tt, "价格归零")
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionModify, BodyChanged: true, Body: []byte(`{"price":0}`), Status: 201, DurationUS: 800}
	})
	e := f.openTest("s-json")
	x := e.test2
	f.click("手填")
	x.url = "https://api.example.com/items"
	x.status = "abc"
	f.click("运行测试")
	testkit.UntilText(t, tt, "状态码须在 100–599 之间")
	x.status = "200"
	x.respHeaders = "Content-Type: application/json"
	x.respBody.SetText(`{"price":9}`)
	f.click("运行测试")
	testkit.UntilText(t, tt, "已修改")
	f.want("800 µs")
	got := f.srv.ScriptEvalRequests()[0]
	if got.Type != api.ScriptTypeResponse || got.Response == nil || got.Response.Status != 200 || string(got.Response.Body) != `{"price":9}` ||
		len(got.Response.Headers) != 1 || got.Request == nil || !got.RequiresBody {
		t.Fatalf("evaluate request %+v", got)
	}
}

func TestRunErrorsAndUnavailable(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	e := f.openTest("s-ua")
	f.click("手填")
	e.test2.url = "https://api.example.com/"
	f.srv.SetScriptsUnavailable(true)
	f.click("运行测试")
	testkit.UntilText(t, tt, "核心未运行")
	if e.test2.running {
		t.Fatal("still running after the failure")
	}
	f.srv.SetScriptsUnavailable(false)
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionError, Error: "ReferenceError: foo is not defined", DurationUS: 90}
	})
	f.click("运行测试")
	testkit.UntilText(t, tt, "脚本出错")
	f.want("ReferenceError: foo is not defined", "90 µs")
	if tt.HasText("核心未运行") {
		t.Error("old error kept after a new run")
	}
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionSkipped, Reason: "size"}
	})
	f.click("运行测试")
	testkit.UntilText(t, tt, "未运行")
	f.want("超出缓冲上限")
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionNone}
	})
	f.click("运行测试")
	testkit.UntilText(t, tt, "未修改")
	f.want("无改动")
}

func TestRunOnceAtATimeAndCancelledByClose(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		entered <- struct{}{}
		<-release
		return api.ScriptEvalResult{Action: api.ScriptActionModify, DurationUS: 5}
	})
	e := f.openTest("s-ua")
	f.click("手填")
	e.test2.url = "https://api.example.com/"
	f.click("运行测试")
	<-entered
	testkit.UntilText(t, tt, "运行中…")
	// Another click while it runs starts nothing.
	e.test2.run(e)
	f.click("运行中…")
	tt.Frame()
	// Closing drops the answer.
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.editors.Open() })
	tester := e.test2
	close(release)
	for i := 0; i < 20; i++ {
		tt.Frame()
	}
	if tester.result != nil || tester.running {
		t.Fatalf("answer of a closed dialog kept: %+v running=%v", tester.result, tester.running)
	}
	if n := len(f.srv.ScriptEvalRequests()); n != 1 {
		t.Fatalf("evaluate calls: %d", n)
	}
}

func TestGenericScriptRun(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "s-job")
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionNone, DurationUS: 30, Output: "done: 3 items",
			Logs: []api.ScriptLogLine{{Level: api.ScriptLogLevelInfo, Message: "job ran"}}}
	})
	e := f.openTest("s-job")
	e.argument = "n=3"
	if tt.HasText("已抓取") || tt.HasText("手填") {
		t.Errorf("input choice for a generic script: %q", tt.Texts())
	}
	f.click("运行测试")
	testkit.UntilText(t, tt, "job ran")
	f.want("done: 3 items", "输出")
	got := f.srv.ScriptEvalRequests()[0]
	if got.Type != api.ScriptTypeGeneric || got.Request != nil || got.Response != nil || got.Argument != "n=3" || got.Pattern != "" {
		t.Fatalf("evaluate request %+v", got)
	}
}

func capturedFixture(t *testing.T) (*fixture, *[]string) {
	t.Helper()
	records := []api.HTTPRecord{
		{ID: "11", Method: "GET", Host: "api.example.com", Path: "/items", URL: "https://api.example.com/items", Status: 200,
			RequestHeaders:  []api.HTTPHeader{{Name: "Accept", Value: "*/*"}},
			ResponseHeaders: []api.HTTPHeader{{Name: "Content-Type", Value: "application/json"}},
			ResponseBytes:   11, ResponseBodySaved: true},
		{ID: "12", Method: "POST", Host: "www.example.org", Path: "/login", URL: "https://www.example.org/login", Status: 302,
			RequestBytes: 4, RequestBodySaved: true},
	}
	bodies := map[string][]byte{"11/response": []byte(`{"price":9}`), "12/request": {0xff, 0xfe, 0, 1}}
	queries := new([]string)
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	routeRecords(f.srv, records, bodies, queries)
	return f, queries
}

func TestCapturedRecordRun(t *testing.T) {
	f, queries := capturedFixture(t)
	tt := f.tt
	testkit.UntilText(t, tt, "价格归零")
	f.srv.SetEvaluate(func(req api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionModify, BodyChanged: true, Body: []byte(`{"price":0}`), DurationUS: 100}
	})
	e := f.openTest("s-json")
	x := e.test2
	testkit.Until(t, tt, func() bool { return x.listed })
	if len(*queries) == 0 || (*queries)[0] != "" {
		t.Fatalf("list queries %q", *queries)
	}
	f.click("已抓取的请求：选择一条请求")
	testkit.UntilText(t, tt, "GET api.example.com/items")
	f.want("POST www.example.org/login")
	// Typing asks the core for the matching records.
	tt.Type("login")
	testkit.Until(t, tt, func() bool { return len(*queries) >= 2 && (*queries)[len(*queries)-1] == "login" })
	testkit.Until(t, tt, func() bool { return len(x.records) == 1 })
	f.click("POST www.example.org/login")
	testkit.Until(t, tt, func() bool { return x.msg != nil })
	f.want("请求头 0", "二进制 4 字节")
	// Back to the other record: its response body is read decoded.
	x.recID = ""
	x.msg = nil
	x.fetch("11", e.maxBytes())
	testkit.Until(t, tt, func() bool { return x.msg != nil })
	f.click("运行测试")
	testkit.UntilText(t, tt, "已修改")
	got := f.srv.ScriptEvalRequests()[0]
	if got.Type != api.ScriptTypeResponse || got.Request == nil || got.Request.URL != "https://api.example.com/items" || got.Request.Method != "GET" ||
		len(got.Request.Headers) != 1 || got.Response == nil || got.Response.Status != 200 || string(got.Response.Body) != `{"price":9}` ||
		len(got.Response.Headers) != 1 || !got.Response.HasBody {
		t.Fatalf("evaluate request %+v", got)
	}
	var sawDecoded bool
	for _, r := range f.srv.Requests() {
		if strings.Contains(r, "/http/records/11/body") && strings.Contains(r, "decoded=true") {
			sawDecoded = true
		}
	}
	if !sawDecoded {
		t.Errorf("body not read decoded: %v", f.srv.Requests())
	}
}

func TestCapturedSeedPreselects(t *testing.T) {
	f, _ := capturedFixture(t)
	tt := f.tt
	testkit.UntilText(t, tt, "价格归零")
	f.w.Add(Seed{Pattern: "^https://api", TestURL: "https://api.example.com/items", Record: &TestSeed{ID: "11", Side: api.HTTPSideResponse}})
	testkit.UntilText(t, tt, "添加脚本")
	e := f.w.editors.ed
	if e.typeName() != model.ScriptHTTPResponse || !strings.Contains(e.code.Text(), "$response") {
		t.Fatalf("seed type %q source %q", e.typeName(), e.code.Text())
	}
	f.click("测试")
	testkit.Until(t, tt, func() bool { return e.test2.msg != nil })
	if e.test2.recID != "11" || e.test2.source != sourceCaptured {
		t.Fatalf("seed %+v", e.test2)
	}
	testkit.UntilText(t, tt, "GET api.example.com/items")
}

func TestCapturedErrors(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	e := f.openTest("s-ua")
	f.click("运行测试")
	testkit.UntilText(t, tt, "请选择一条请求")
	if n := len(f.srv.ScriptEvalRequests()); n != 0 {
		t.Fatalf("ran without a request: %d", n)
	}
	// The record list is not served: the choice says so.
	testkit.Until(t, tt, func() bool { return e.test2.recsErr != "" })
	f.want("无法读取请求记录")
}

func TestDiffOf(t *testing.T) {
	in := message{url: "https://a.example/x", method: "GET",
		reqHeaders:  []api.ScriptHeader{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}},
		respHeaders: []api.ScriptHeader{{Name: "C", Value: "3"}}, status: 200,
		reqBody: []byte(`{"a":1}`), respBody: []byte(`{"p":9}`)}
	// A header list diff and a URL change on a request script.
	d := diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{
		Action: api.ScriptActionModify, URL: "https://a.example/y", HeadersChanged: true,
		Headers: []api.ScriptHeader{{Name: "A", Value: "1"}, {Name: "X", Value: "9"}}}})
	if !d.any || !d.hasURL || d.url[1] != "https://a.example/y" || !d.hasHeaders ||
		d.headers[0] != "A: 1\nB: 2\n" || d.headers[1] != "A: 1\nX: 9\n" || d.hasBody || d.hasStatus {
		t.Fatalf("request diff %+v", d)
	}
	// Changes without the whole list are applied to the input headers.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{Action: api.ScriptActionModify, Changes: []api.ScriptChange{
		{Kind: "header", Op: "set", Name: "a", HasAfter: true, After: "7"},
		{Kind: "header", Op: "delete", Name: "B", HadBefore: true, Before: "2"},
		{Kind: "header", Op: "add", Name: "N", HasAfter: true, After: "new"}}}})
	if !d.hasHeaders || d.headers[1] != "A: 7\nN: new\n" {
		t.Fatalf("applied changes %+v", d)
	}
	// A response script: body (JSON indented) and status.
	d = diffOf(&outcome{typ: model.ScriptHTTPResponse, in: in, res: api.ScriptEvalResult{Action: api.ScriptActionModify,
		BodyChanged: true, Body: []byte(`{"p":0}`), Status: 404}})
	if !d.hasBody || d.body[0] != "{\n  \"p\": 9\n}" || d.body[1] != "{\n  \"p\": 0\n}" || !d.hasStatus || d.status != [2]int32{200, 404} {
		t.Fatalf("response diff %+v", d)
	}
	// A binary body shows as its size.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{BodyChanged: true, Body: []byte{0xff, 0, 1}}})
	if d.body[1] != "二进制 3 字节" {
		t.Fatalf("binary body %+v", d)
	}
	// Binary bodies of one size that differ say so.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: message{reqBody: []byte{0xff, 0, 1}}, res: api.ScriptEvalResult{BodyChanged: true, Body: []byte{0xff, 0, 2}}})
	if !d.hasBody || d.body[0] != "二进制 3 字节" || d.body[1] != "二进制内容已变化" {
		t.Fatalf("same-size binary body %+v", d)
	}
	// A text body only reformatted shows its text difference, not the
	// binary line.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{BodyChanged: true, Body: []byte("{\n  \"a\": 1\n}")}})
	if !d.hasBody || d.body[0] != `{"a":1}` || d.body[1] != "{\n  \"a\": 1\n}" || strings.Contains(d.body[1], "二进制") {
		t.Fatalf("reformatted body %+v", d)
	}
	// A reply replaces the exchange.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{Action: api.ScriptActionRespond,
		Response: &api.ScriptResponseMessage{Status: 403, Headers: []api.ScriptHeader{{Name: "X", Value: "1"}}, Body: []byte("no")}}})
	if !d.isReply || !d.any || responseText(&api.ScriptResponseMessage{Status: 403, Headers: []api.ScriptHeader{{Name: "X", Value: "1"}}, Body: []byte("no")}) != "HTTP 403\nX: 1\n\nno" {
		t.Fatalf("reply %+v", d)
	}
	// Nothing changed.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{Action: api.ScriptActionNone}})
	if d.any {
		t.Fatalf("unchanged %+v", d)
	}
	// Headers equal to the input are no change.
	d = diffOf(&outcome{typ: model.ScriptHTTPRequest, in: in, res: api.ScriptEvalResult{HeadersChanged: true, Headers: in.reqHeaders}})
	if d.hasHeaders {
		t.Fatalf("same headers %+v", d)
	}
}

// Every open of the record choice reads the records again.
func TestCapturedListRereadOnOpen(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	recs := []api.HTTPRecord{{ID: "1", Method: "GET", Host: "one.example.com", Path: "/a", URL: "https://one.example.com/a", Status: 200}}
	var mu sync.Mutex
	f.srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		testkit.JSON(api.HTTPList{Records: slices.Clone(recs)})(w, r)
	})
	testkit.UntilText(t, tt, "统一 UA")
	e := f.openTest("s-ua")
	testkit.Until(t, tt, func() bool { return e.test2.listed && !e.test2.loading })
	f.click("已抓取的请求：选择一条请求")
	testkit.UntilText(t, tt, "GET one.example.com/a")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	mu.Lock()
	recs = append(recs, api.HTTPRecord{ID: "2", Method: "GET", Host: "two.example.com", Path: "/b", URL: "https://two.example.com/b", Status: 200})
	mu.Unlock()
	f.click("已抓取的请求：选择一条请求")
	testkit.UntilText(t, tt, "GET two.example.com/b")
}

// The pattern is sent as typed, trailing spaces included, as it is saved.
func TestEvaluateKeepsPatternAsTyped(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.srv.SetEvaluate(modifyResult)
	e := f.openTest("s-ua")
	f.click("手填")
	e.test2.url = "https://api.example.com/items"
	e.pattern += "  "
	want := e.pattern
	f.click("运行测试")
	testkit.UntilText(t, tt, "已修改")
	if got := f.srv.ScriptEvalRequests()[0].Pattern; got != want {
		t.Fatalf("pattern %q, want %q", got, want)
	}
}

// A failed read still answers the text typed meanwhile, once.
func TestCapturedListRetriesLatestQueryAfterFailure(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 1000)
	tt := f.tt
	gate := make(chan struct{})
	var mu sync.Mutex
	var queries []string
	f.srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("search"))
		first := len(queries) == 1
		mu.Unlock()
		if first {
			<-gate
			http.Error(w, `{"error":"boom"}`, 500)
			return
		}
		testkit.JSON(api.HTTPList{Records: []api.HTTPRecord{{ID: "1", Method: "GET", Host: "x.example.com", Path: "/", URL: "https://x.example.com/", Status: 200}}})(w, r)
	})
	testkit.UntilText(t, tt, "统一 UA")
	e := f.openTest("s-ua")
	testkit.Until(t, tt, func() bool { return e.test2.loading })
	f.click("已抓取的请求：选择一条请求")
	tt.Type("x")
	tt.Frame()
	close(gate)
	testkit.Until(t, tt, func() bool { return len(e.test2.records) == 1 && e.test2.recsErr == "" })
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || queries[1] != "x" {
		t.Fatalf("queries %q", queries)
	}
}
