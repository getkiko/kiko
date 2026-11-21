package connections

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestReplayHeaders(t *testing.T) {
	in := []api.HTTPHeader{
		{Name: ":method", Value: "POST"}, {Name: ":authority", Value: "a.com"}, {Name: "Host", Value: "a.com"},
		{Name: "Content-Length", Value: "7"}, {Name: "Connection", Value: "keep-alive, X-Hop"}, {Name: "X-Hop", Value: "1"},
		{Name: "Keep-Alive", Value: "timeout=5"}, {Name: "Transfer-Encoding", Value: "chunked"}, {Name: "Upgrade", Value: "h2c"},
		{Name: "Proxy-Authorization", Value: "x"}, {Name: "TE", Value: "trailers"},
		{Name: "Authorization", Value: "Bearer t"}, {Name: " Content-Encoding ", Value: "gzip"}, {Name: "Accept", Value: "*/*"}, {Name: "", Value: "x"},
	}
	names := func(h []api.HTTPHeader) string {
		var out []string
		for _, x := range h {
			out = append(out, x.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(replayHeaders(in, false)); got != "Authorization,Content-Encoding,Accept" {
		t.Errorf("plain: %s", got)
	}
	// A body prefilled decoded takes its Content-Encoding away.
	if got := names(replayHeaders(in, true)); got != "Authorization,Accept" {
		t.Errorf("encoded: %s", got)
	}
	if got := replayHeaders(nil, false); got != nil {
		t.Errorf("nil: %v", got)
	}
}

func TestReplayBodyPlan(t *testing.T) {
	row := func(edit func(*httpRow)) *httpRow {
		r := newHTTPRow(hrec(1, "a.com", 200, api.HTTPStateComplete))
		r.RequestBytes, r.RequestBodySaved = 100, true
		if edit != nil {
			edit(r)
		}
		return r
	}
	for _, c := range []struct {
		name string
		r    *httpRow
		read bool
		note string
	}{
		{"none", row(func(r *httpRow) { r.RequestBytes = 0 }), false, ""},
		{"saved", row(nil), true, ""},
		{"edge", row(func(r *httpRow) { r.RequestBytes = replayBodyLimit }), true, ""},
		{"dropped", row(func(r *httpRow) { r.BodyDropped = true }), false, "已丢弃"},
		{"unsaved", row(func(r *httpRow) { r.RequestBodySaved = false }), false, "未保存"},
		{"truncated", row(func(r *httpRow) { r.RequestBodyTruncated = true }), false, "已截断"},
		{"big", row(func(r *httpRow) { r.RequestBytes = replayBodyLimit + 1 }), false, "超过 1.0 MB"},
	} {
		read, note := replayBodyPlan(c.r)
		if read != c.read || !strings.Contains(note, c.note) || (c.note == "") != (note == "") {
			t.Errorf("%s: read %v note %q", c.name, read, note)
		}
	}
	whole := api.HTTPBody{Data: []byte("a=1"), Complete: true, Total: 3}
	if text, note := replayBodyText(whole, nil); text != "a=1" || note != "" {
		t.Errorf("text %q %q", text, note)
	}
	for name, c := range map[string]struct {
		body api.HTTPBody
		err  error
		want string
	}{
		"error":     {whole, http.ErrAbortHandler, "读取原请求正文失败"},
		"truncated": {api.HTTPBody{Data: []byte("a"), Complete: true, Truncated: true}, nil, "已截断"},
		"partial":   {api.HTTPBody{Data: []byte("a")}, nil, "已截断"},
		"decode":    {api.HTTPBody{Data: []byte("a"), Complete: true, DecodeError: "bad"}, nil, "解码失败"},
		"binary":    {api.HTTPBody{Data: []byte{0, 1, 2, 255}, Complete: true}, nil, "不是文本"},
		"utf8":      {api.HTTPBody{Data: []byte{0xff, 0xfe}, Complete: true}, nil, "不是文本"},
	} {
		if text, note := replayBodyText(c.body, c.err); text != "" || !strings.Contains(note, c.want) {
			t.Errorf("%s: %q %q", name, text, note)
		}
	}
}

func TestReplayFormRequest(t *testing.T) {
	r := newHTTPRow(hrec(7, "api.example.com", 200, api.HTTPStateComplete))
	r.Method, r.URL = "post", "https://api.example.com/v1?a=1"
	r.RequestHeaders = []api.HTTPHeader{{Name: "Accept", Value: "*/*"}, {Name: "Host", Value: "api.example.com"}}
	f := newReplayForm(r)
	if f.sourceID != "7" || f.method != "post" || len(f.headers) != 1 || f.bodyMode != replayBodyEditable || !f.verify {
		t.Fatalf("form %+v", f)
	}
	f.addHeader("", "")
	f.addHeader(" X-Test ", "1")
	f.body = "hello"
	f.outbound = "香港"
	req, err := f.request()
	if err != nil || req.Method != "POST" || req.URL != r.URL || req.SourceID != "7" || req.Outbound != "香港" || !req.VerifyTLS || string(req.Body) != "hello" {
		t.Fatalf("request %+v %v", req, err)
	}
	if len(req.Headers) != 2 || req.Headers[1] != (api.HTTPHeader{Name: "X-Test", Value: "1"}) {
		t.Fatalf("headers %+v", req.Headers)
	}
	// A body that cannot be taken over is not sent, whatever the field holds.
	f.bodyMode = replayBodyBlocked
	if req, _ = f.request(); req.Body != nil {
		t.Fatalf("blocked body sent: %q", req.Body)
	}
	f.removeHeader(f.headers[1].key)
	f.removeHeader(f.headers[1].key)
	if len(f.headers) != 1 {
		t.Fatalf("remove: %+v", f.headers)
	}
	for _, c := range []struct {
		edit func(*replayForm)
		want string
	}{
		{func(f *replayForm) { f.url = "ftp://a.com/" }, "URL"},
		{func(f *replayForm) { f.url = "https:///x" }, "URL"},
		{func(f *replayForm) { f.url = "" }, "URL"},
		{func(f *replayForm) { f.url = "https://u:p@a.com/" }, "账号和密码"},
		{func(f *replayForm) { f.url = "https://a.com/\r\nX: 1" }, "URL"},
		{func(f *replayForm) { f.addHeader("X-A", "v\r\nX-B: 1") }, "不能包含换行"},
		{func(f *replayForm) { f.addHeader("X-A", "v\nw") }, "不能包含换行"},
		{func(f *replayForm) { f.method = " " }, "方法"},
		{func(f *replayForm) { f.addHeader("Bad Name", "v") }, "请求头名称无效"},
		{func(f *replayForm) { f.addHeader("X:Y", "v") }, "请求头名称无效"},
		{func(f *replayForm) { f.addHeader("", "orphan") }, "请求头名称无效"},
	} {
		g := newReplayForm(r)
		c.edit(g)
		if _, err := g.request(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
}

func TestReplayUpsert(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), nil)
	m := h.w.http
	m.applyFrames([]api.HTTPFrame{frame(true, 1, 2)})
	rec := hrec(50, "r.com", 200, api.HTTPStateComplete)
	rec.Connection.Inbound = api.ReplayInbound
	m.replayUpsert(rec)
	row := m.store.byID["50"]
	if row == nil || !row.replayed() || !slices.ContainsFunc(httpFlags(row), func(f httpFlag) bool { return f.label == "重放" }) {
		t.Fatalf("upsert: %+v", row)
	}
	// The stream delivers the same record later: one row, and an older
	// revision does not replace the answer.
	older := rec
	older.Seq = 1
	older.Status = 500
	m.replayUpsert(older)
	if len(m.store.rows) != 3 || m.store.byID["50"].Status != 200 {
		t.Fatalf("rows %v", ids(m.store))
	}
	// A paused list shows it at once.
	m.togglePause()
	rec2 := hrec(51, "r.com", 200, api.HTTPStateComplete)
	m.replayUpsert(rec2)
	if !slices.ContainsFunc(m.snap, func(r *httpRow) bool { return r.ID == "51" }) {
		t.Fatal("paused snapshot lacks the replay")
	}
	m.replayUpsert(api.HTTPRecord{})
	if len(m.store.rows) != 4 {
		t.Fatalf("empty record stored: %v", ids(m.store))
	}
}

// replayFake is the core's replay and request-body endpoints.
type replayFake struct {
	mu       sync.Mutex
	reqs     []api.ReplayRequest
	fail     bool
	gate     chan struct{}
	bodyReqs []string
}

func (f *replayFake) requests() []api.ReplayRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *replayFake) route(srv *testkit.Server) {
	bodies := map[string][]byte{
		"22": []byte("a=1&b=2"), "24": {0, 1, 2, 255}, "21": []byte(`{"decoded":true}`),
	}
	srv.Route("GET", "/http/records/", func(w http.ResponseWriter, r *http.Request) {
		id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/v1/http/records/"), "/")
		f.mu.Lock()
		f.bodyReqs = append(f.bodyReqs, id+" "+r.URL.RawQuery)
		f.mu.Unlock()
		data := bodies[id]
		h := w.Header()
		h.Set("X-Total", strconv.Itoa(len(data)))
		h.Set("X-Saved", "true")
		h.Set("X-Complete", "true")
		_, _ = w.Write(data)
	})
	srv.Route("POST", "/http/replay", func(w http.ResponseWriter, r *http.Request) {
		var req api.ReplayRequest
		if err := testkit.Body(r, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		n, fail, gate := len(f.reqs), f.fail, f.gate
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
		rec := hrec(uint64(100+n), "api.github.com", 200, api.HTTPStateComplete)
		rec.Seq = api.Uint64(1000 + n)
		rec.Method, rec.URL, rec.Path = req.Method, req.URL, "/replayed"
		rec.Connection = &api.HTTPConnection{ID: "c77", Inbound: api.ReplayInbound, Outbound: "直连"}
		out := map[string]any{"record": rec, "error": ""}
		if fail {
			rec.State, rec.Status, rec.Error = api.HTTPStateFailed, 0, "dial tcp 1.2.3.4:443: connect: connection refused"
			out = map[string]any{"record": rec, "error": rec.Error}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

func replayCaps() api.HTTPCapabilities {
	return api.HTTPCapabilities{Supported: true, Enabled: true, Replay: true}
}

// replayFrame is the inspector's fixture with request bodies of every kind.
func replayFrame() api.HTTPFrame {
	f := inspectorFrame()
	for _, e := range f.Events {
		r := e.Record
		switch r.Path {
		case "/json":
			r.RequestBytes, r.RequestBodySaved, r.RequestEncoding = 16, true, "gzip"
			r.RequestHeaders = append(r.RequestHeaders, api.HTTPHeader{Name: "Content-Encoding", Value: "gzip"})
		case "/binary":
			r.RequestBytes, r.RequestBodySaved = 4, true
		case "/big":
			r.RequestBytes, r.RequestBodySaved = 3<<20, true
		case "/dropped":
			r.RequestBytes, r.RequestBodySaved = 5, true
		case "/unsaved":
			r.RequestBytes = 5
		}
	}
	return f
}

func newReplayHarness(t *testing.T, width, height int) (*harness, *replayFake) {
	fake := &replayFake{}
	h := newHTTPHarness(t, width, height, replayCaps(), newHTTPFeed(replayFrame()), fake.route)
	d := model.EmptyDoc("")
	for i, tag := range []string{"香港", "美国"} {
		d.Outbounds = append(d.Outbounds, &model.Outbound{Entity: model.Entity{ID: "o" + strconv.Itoa(i), Tag: tag, Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}})
	}
	h.w.http.doc = &fakeDoc{doc: d}
	testkit.UntilText(t, h.tt, "/json")
	return h, fake
}

func TestReplayHiddenWithoutSupport(t *testing.T) {
	h := newHTTPHarness(t, 1440, 1000, enabled(), newHTTPFeed(replayFrame()), nil)
	h.w.http.doc = &fakeDoc{doc: model.EmptyDoc("")}
	testkit.UntilText(t, h.tt, "/json")
	h.click(t, "/text")
	testkit.UntilText(t, h.tt, "复制 URL")
	if h.tt.HasText("重放") {
		t.Fatalf("replay offered: %q", h.tt.Texts())
	}
}

func TestReplayDialogFlow(t *testing.T) {
	h, fake := newReplayHarness(t, 1440, 1000)
	tt, m := h.tt, h.w.http
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	// The form holds the record: the text body, headers without the hop-by-hop
	// and pseudo ones, the route rules as outbound, the certificate checked.
	testkit.Until(t, tt, func() bool { return m.rp.form != nil && m.rp.form.bodyMode == replayBodyEditable })
	if m.rp.form.body != "a=1&b=2" || len(m.rp.form.headers) != 3 || m.rp.form.outbound != "" || !m.rp.form.verify {
		t.Fatalf("form %+v", m.rp.form)
	}
	for _, want := range []string{"方法", "URL", "请求头", "请求正文", "出站", "校验证书", "按路由规则", "POST", "发送", "取消", "添加请求头"} {
		if !tt.HasText(want) {
			t.Errorf("dialog lacks %q: %q", want, tt.Texts())
		}
	}
	savePreview(t, tt, "20-replay-dialog-light.png")

	// Headers can be added and removed.
	h.click(t, "添加请求头")
	testkit.Until(t, tt, func() bool { return len(m.rp.form.headers) == 4 })
	h.click(t, "移除请求头 Accept-Encoding")
	testkit.Until(t, tt, func() bool { return len(m.rp.form.headers) == 3 })

	// The outbound is chosen from the document's.
	h.click(t, "按路由规则")
	testkit.UntilText(t, tt, "美国")
	h.click(t, "美国")
	testkit.Until(t, tt, func() bool { return m.rp.form.outbound == "美国" })
	h.click(t, "校验证书开关")
	testkit.Until(t, tt, func() bool { return !m.rp.form.verify })
	m.rp.form.url = "https://api.github.com/text?edited=1"
	m.rp.form.body = "a=9"
	m.rp.form.headers[len(m.rp.form.headers)-1].name = "X-Added"
	m.rp.form.headers[len(m.rp.form.headers)-1].value = "yes"

	h.click(t, "发送")
	testkit.Until(t, tt, func() bool { return !tt.HasText("重放请求") })
	reqs := fake.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests %d", len(reqs))
	}
	got := reqs[0]
	if got.SourceID != "22" || got.Method != "POST" || got.URL != "https://api.github.com/text?edited=1" || string(got.Body) != "a=9" || got.Outbound != "美国" || got.VerifyTLS {
		t.Fatalf("request %+v", got)
	}
	var names []string
	for _, hd := range got.Headers {
		names = append(names, hd.Name)
	}
	if !slices.Equal(names, []string{"Authorization", "Content-Type", "X-Added"}) {
		t.Fatalf("headers %v", names)
	}

	// The new record is in the list, selected and shown in the inspector,
	// marked as a replay.
	testkit.Until(t, tt, func() bool { return m.selected == "101" })
	testkit.UntilText(t, tt, "/replayed")
	if row := m.store.byID["101"]; row == nil || !row.replayed() {
		t.Fatalf("row %+v", row)
	}
	testkit.UntilText(t, tt, "#101")
	if !tt.HasText("重放") || !tt.HasText("显示 8 / 8 条请求") {
		t.Fatalf("list: %q", tt.Texts())
	}
	savePreview(t, tt, "21-replay-result-light.png")
}

func TestReplayFailureKeepsDialogAndRetries(t *testing.T) {
	h, fake := newReplayHarness(t, 1440, 1000)
	tt, m := h.tt, h.w.http
	fake.fail = true
	h.click(t, "/json")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	testkit.Until(t, tt, func() bool { return m.rp.form.bodyMode == replayBodyEditable })
	// The encoded body was read decoded, and its Content-Encoding left out.
	if !slices.ContainsFunc(fake.bodyReqs, func(q string) bool {
		return strings.HasPrefix(q, "21 ") && strings.Contains(q, "decoded=true") && strings.Contains(q, "side=request")
	}) {
		t.Fatalf("body requests %q", fake.bodyReqs)
	}
	if m.rp.form.body != `{"decoded":true}` {
		t.Fatalf("body %q", m.rp.form.body)
	}
	for _, hd := range m.rp.form.headers {
		if strings.EqualFold(hd.name, "Content-Encoding") {
			t.Fatalf("encoding kept: %+v", m.rp.form.headers)
		}
	}
	h.click(t, "发送")
	testkit.UntilText(t, tt, "connection refused")
	if !tt.HasText("重放请求") || !tt.HasText("重试") || m.rp.busy {
		t.Fatalf("dialog after failure: %q", tt.Texts())
	}
	// The failed exchange is in the list; the dialog stays for another try.
	if row := m.store.byID["101"]; row == nil || row.State != api.HTTPStateFailed {
		t.Fatalf("failed record %+v", row)
	}
	savePreview(t, tt, "22-replay-failed-light.png")
	setDark(tt, true)
	savePreview(t, tt, "22-replay-failed-dark.png")
	setDark(tt, false)

	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	h.click(t, "重试")
	testkit.Until(t, tt, func() bool { return !tt.HasText("重放请求") })
	if len(fake.requests()) != 2 || m.selected != "102" {
		t.Fatalf("requests %d selected %q", len(fake.requests()), m.selected)
	}
}

func TestReplayInvalidFormShowsErrorWithoutSending(t *testing.T) {
	h, fake := newReplayHarness(t, 1440, 1000)
	tt, m := h.tt, h.w.http
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	m.rp.form.url = "not a url"
	h.click(t, "发送")
	testkit.UntilText(t, tt, "URL 需为 http:// 或 https:// 开头的完整地址")
	if len(fake.requests()) != 0 {
		t.Fatal("invalid form sent")
	}
}

func TestReplayNoDoubleSubmit(t *testing.T) {
	h, fake := newReplayHarness(t, 1440, 1000)
	tt, m := h.tt, h.w.http
	fake.gate = make(chan struct{})
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.Until(t, tt, func() bool { return m.rp.form != nil && m.rp.form.bodyMode == replayBodyEditable })
	h.click(t, "发送")
	testkit.UntilText(t, tt, "发送中…")
	// While the request is out the button does nothing: a click, the
	// method and a second call all leave one request.
	if err := tt.Click("发送中…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	m.sendReplay()
	time.Sleep(50 * time.Millisecond)
	if n := len(fake.requests()); n != 1 {
		t.Fatalf("%d requests while busy", n)
	}
	close(fake.gate)
	testkit.Until(t, tt, func() bool { return !tt.HasText("重放请求") })
	if n := len(fake.requests()); n != 1 || m.rp.busy {
		t.Fatalf("%d requests, busy %v", n, m.rp.busy)
	}
}

func TestReplayBodyStates(t *testing.T) {
	h, _ := newReplayHarness(t, 1440, 1000)
	tt, m := h.tt, h.w.http
	for _, c := range []struct{ row, note string }{
		{"/binary", "不是文本"},
		{"/big", "超过 1.0 MB"},
		{"/dropped", "已丢弃"},
		{"/unsaved", "未保存"},
	} {
		h.click(t, c.row)
		testkit.UntilText(t, tt, "复制 URL")
		h.click(t, "重放")
		testkit.UntilText(t, tt, c.note)
		if m.rp.form.bodyMode != replayBodyBlocked {
			t.Fatalf("%s: mode %d", c.row, m.rp.form.bodyMode)
		}
		req, err := m.rp.form.request()
		if err != nil || req.Body != nil {
			t.Fatalf("%s: %+v %v", c.row, req, err)
		}
		if c.row == "/binary" {
			savePreview(t, tt, "23-replay-body-blocked-light.png")
		}
		tt.Key(0, ui.KeyEscape)
		testkit.Until(t, tt, func() bool { return !tt.HasText("重放请求") })
	}
	// A record without a body takes one to be typed.
	h.click(t, "/image")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	if m.rp.form.bodyMode != replayBodyEditable || m.rp.form.body != "" {
		t.Fatalf("empty body form %+v", m.rp.form)
	}
}

func TestReplayEscapeAndFocus(t *testing.T) {
	h, fake := newReplayHarness(t, 1440, 1000)
	tt, m := h.tt, h.w.http
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	// The URL field has the focus on opening.
	testkit.Until(t, tt, func() bool { return tt.Focused("https://") })
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !tt.HasText("重放请求") })
	// The focus returns to the button that opened the dialog.
	testkit.Until(t, tt, func() bool { return tt.Focused("重放") })
	if len(fake.requests()) != 0 {
		t.Fatal("escape sent a request")
	}

	// Opened from the list's menu (the tester shows no popover menus), the
	// focus returns to the list.
	m.openReplay(m.store.byID["21"], false)
	testkit.UntilText(t, tt, "重放请求")
	if m.rp.form.sourceID != "21" || m.rp.fromInspector {
		t.Fatalf("form %+v", m.rp.form)
	}
	h.click(t, "取消")
	testkit.Until(t, tt, func() bool { return !tt.HasText("重放请求") })
	tt.Frame()
	tt.Frame()
	if m.refocus {
		t.Fatal("list focus not returned")
	}
}

func TestReplayDialogNarrowAndDark(t *testing.T) {
	h, _ := newReplayHarness(t, 760, 900)
	tt, m := h.tt, h.w.http
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	testkit.Until(t, tt, func() bool { return m.rp.form.bodyMode == replayBodyEditable })
	savePreview(t, tt, "24-replay-narrow-light.png")
	setDark(tt, true)
	savePreview(t, tt, "24-replay-narrow-dark.png")
	setDark(tt, false)
}

// A long URL and many headers keep the dialog inside the window.
func TestReplayDialogLongContent(t *testing.T) {
	f := longFrame()
	for _, e := range f.Events {
		if e.Record.ID == "30" {
			for i := 0; i < 14; i++ {
				e.Record.RequestHeaders = append(e.Record.RequestHeaders, api.HTTPHeader{Name: "X-Extra-Header-" + strconv.Itoa(i), Value: strings.Repeat("value-", 12)})
			}
		}
	}
	fake := &replayFake{}
	h := newHTTPHarness(t, 1440, 900, replayCaps(), newHTTPFeed(f), fake.route)
	tt, m := h.tt, h.w.http
	testkit.UntilText(t, tt, "/json")
	h.click(t, "/v2/collect/events/batch/2026/11/03/segment/user-profile-sync")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "重放")
	testkit.UntilText(t, tt, "重放请求")
	if len(m.rp.form.headers) != 15 {
		t.Fatalf("headers %d", len(m.rp.form.headers))
	}
	savePreview(t, tt, "25-replay-long-light.png")
	setDark(tt, true)
	savePreview(t, tt, "25-replay-long-dark.png")
	// The dialog's panel fits the window.
	title, ok1 := tt.Find("重放请求")
	send, ok2 := tt.Find("发送")
	if !ok1 || !ok2 || title.Y < 0 || send.Y+send.H > 900 {
		t.Fatalf("title %+v send %+v", title, send)
	}
}
