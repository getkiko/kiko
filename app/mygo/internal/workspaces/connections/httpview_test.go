package connections

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// httpFeed is the core's HTTP stream: it writes the frames sent to it, and
// keeps the stream open.
type httpFeed struct{ ch chan api.HTTPFrame }

func newHTTPFeed(frames ...api.HTTPFrame) *httpFeed {
	f := &httpFeed{ch: make(chan api.HTTPFrame, 64)}
	for _, frame := range frames {
		f.ch <- frame
	}
	return f
}

func (f *httpFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.(http.Flusher).Flush()
	for {
		select {
		case frame := <-f.ch:
			raw, _ := json.Marshal(frame)
			fmt.Fprintf(w, "%s\n", raw)
			w.(http.Flusher).Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// httpFixture is four exchanges: a plain one, a failing one, an image
// that was neither verified nor left alone, and one still running.
func httpFixture() api.HTTPFrame {
	at := func(sec int) int64 { return time.Date(2026, 10, 16, 8, 0, sec, 0, time.Local).UnixNano() }
	mk := func(id uint64, method, host, path string, status int, state, ctype, process, conn string) api.HTTPRecord {
		r := hrec(id, host, status, state)
		r.Method, r.Path, r.URL, r.ConnectionID = method, path, "https://"+host+path, conn
		r.Connection = &api.HTTPConnection{ID: conn, Source: "172.18.0.1:5000", Outbound: "美国板瓦", Rule: "rule_set=[Gfw]", ProcessName: process, ProcessID: 4242}
		r.ResponseHeaders = []api.HTTPHeader{{Name: "Content-Type", Value: ctype}}
		r.Timing = &api.HTTPTiming{Start: at(int(id)), End: at(int(id)) + int64(120*time.Millisecond)}
		r.ResponseBytes = 2048 * int64(id)
		return r
	}
	a := mk(11, "GET", "api.github.com", "/user", 200, "complete", "application/json", "curl", "c1")
	b := mk(12, "POST", "chatgpt.com", "/v1/chat", 502, "complete", "text/html", "Google Chrome", "c2")
	b.Error = "upstream reset"
	c := mk(13, "GET", "cdn.example.com", "/logo.png", 200, "complete", "image/png", "Google Chrome", "c2")
	c.Unverified, c.Rewritten = true, true
	d := mk(14, "GET", "slow.example.com", "/stream", 0, "in_progress", "", "curl", "c1")
	d.Timing.End = 0
	f := api.HTTPFrame{Instance: "i", Seq: 14, Floor: 1, Reset: true, Status: &api.HTTPStatus{Instance: "i", RecordCount: 4, Total: 4}}
	for _, r := range []api.HTTPRecord{a, b, c, d} {
		r := r
		f.Events = append(f.Events, api.HTTPEvent{Kind: api.HTTPEventNew, Record: &r})
	}
	return f
}

func newHTTPHarness(t *testing.T, width, height int, caps api.HTTPCapabilities, feed *httpFeed, routes func(*testkit.Server)) *harness {
	h := newSizedHarness(t, width, height, func(srv *testkit.Server) {
		srv.Route("GET", "/http/capabilities", testkit.JSON(caps))
		srv.Route("GET", "/http/stream", feed.ServeHTTP)
		srv.Route("POST", "/http/clear", testkit.JSON(map[string]any{"status": api.HTTPStatus{}}))
		if routes != nil {
			routes(srv)
		}
	})
	h.w.view = viewHTTP
	h.w.SetActive(true)
	return h
}

func enabled() api.HTTPCapabilities { return api.HTTPCapabilities{Supported: true, Enabled: true} }

func TestHTTPMonitorList(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(httpFixture()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/v1/chat")
	for _, want := range []string{"/user", "/logo.png", "/stream", "502", "200", "未验证", "已改写", "错误", "显示 4 / 4 条请求", "实时更新", "累计 4", "美国板瓦"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	for _, f := range []string{"主机", "方法", "状态码", "内容类型", "进程", "来源"} {
		if !tt.HasText(f) {
			t.Errorf("filter group %q missing", f)
		}
	}
	// Newest first.
	if texts := tt.Texts(); slices.Index(texts, "/stream") > slices.Index(texts, "/user") {
		t.Errorf("order: %q", texts)
	}
	savePreview(t, tt, "http-list-light.png")

	// A status filter narrows the list; clearing restores it.
	h.click(t, "5xx")
	testkit.Until(t, tt, func() bool { return !tt.HasText("/user") })
	if !tt.HasText("/v1/chat") || !tt.HasText("显示 1 / 4 条请求") {
		t.Fatalf("filtered: %q", tt.Texts())
	}
	h.click(t, "清除")
	testkit.UntilText(t, tt, "/user")

	// Search.
	h.click(t, "搜索 URL、方法、状态、进程…")
	tt.Type("logo")
	testkit.Until(t, tt, func() bool { return !tt.HasText("/user") })
	if !tt.HasText("/logo.png") {
		t.Fatalf("search: %q", tt.Texts())
	}
	h.click(t, "清除搜索")
	testkit.UntilText(t, tt, "/user")

	// The scope: "Errors" holds the 502, "In progress" the running exchange.
	h.click(t, "异常")
	testkit.Until(t, tt, func() bool { return !tt.HasText("/user") })
	if !tt.HasText("/v1/chat") || tt.HasText("/stream") {
		t.Fatalf("errors: %q", tt.Texts())
	}
	h.click(t, "进行中")
	testkit.Until(t, tt, func() bool { return !tt.HasText("/v1/chat") })
	if !tt.HasText("/stream") {
		t.Fatalf("live: %q", tt.Texts())
	}
	h.click(t, "全部")
	testkit.UntilText(t, tt, "/user")

	// Selecting a row opens the inspector; Escape and the close button
	// close it.
	h.click(t, "/user")
	testkit.UntilText(t, tt, "概览")
	if h.w.http.selected != "11" || !hasProperty(tt, "URL", "https://api.github.com/user") {
		t.Fatalf("inspector: %q", tt.Texts())
	}
	h.click(t, "关闭详情")
	testkit.Until(t, tt, func() bool { return !tt.HasText("概览") })
}

func TestHTTPMonitorDetailed(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(httpFixture()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/v1/chat")
	// The compact list shows no secondary lines.
	if tt.HasText("upstream reset") || tt.HasText("PID 4242") {
		t.Fatalf("compact: %q", tt.Texts())
	}
	h.click(t, "切换 HTTP 列表密度")
	testkit.UntilText(t, tt, "PID 4242")
	if !tt.HasText("upstream reset") || !tt.HasText("rule_set=[Gfw]") {
		t.Fatalf("detailed: %q", tt.Texts())
	}
	var saved bool
	if !h.env.Prefs.Load(prefHTTPDetailed, &saved) || !saved {
		t.Fatal("density not saved")
	}
	savePreview(t, tt, "http-detailed-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "http-detailed-dark.png")
}

func TestHTTPMonitorEmptyStates(t *testing.T) {
	for _, c := range []struct {
		name string
		caps api.HTTPCapabilities
		want string
	}{
		{"unsupported", api.HTTPCapabilities{Reason: "core too old"}, "当前核心不支持 HTTP 请求"},
		{"disabled", api.HTTPCapabilities{Supported: true}, "HTTPS 解密未启用"},
		{"none", enabled(), "暂无 HTTP 请求"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHTTPHarness(t, 1440, 820, c.caps, newHTTPFeed(), nil)
			testkit.UntilText(t, h.tt, c.want)
			if h.tt.HasText("主机") && h.tt.HasText("状态码") {
				t.Fatal("filters shown without records")
			}
			if c.name == "none" {
				if h.tt.HasText("前往 HTTPS 解密设置") {
					t.Fatal("settings link on an enabled core")
				}
				return
			}
			h.click(t, "前往 HTTPS 解密设置")
			if !slices.Contains(h.pages, "settings:https") {
				t.Fatalf("navigate: %v", h.pages)
			}
		})
	}
}

// A filter that matches nothing leaves the empty list with a way back.
func TestHTTPMonitorNoMatch(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(httpFixture()), nil)
	testkit.UntilText(t, h.tt, "/user")
	h.click(t, "搜索 URL、方法、状态、进程…")
	h.tt.Type("zzz-nothing")
	testkit.UntilText(t, h.tt, "没有符合条件的请求")
	h.click(t, "清除筛选")
	testkit.UntilText(t, h.tt, "/user")
}

// Pausing freezes the list while records keep arriving; resuming shows
// them.
func TestHTTPMonitorPause(t *testing.T) {
	feed := newHTTPFeed(httpFixture())
	h := newHTTPHarness(t, 1440, 820, enabled(), feed, nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	h.click(t, "暂停 HTTP 更新")
	more := hrec(15, "later.example.com", 200, api.HTTPStateComplete)
	more.Path = "/later"
	// The running exchange finishes while paused.
	done := httpFixture().Events[3].Record
	finished := *done
	finished.Seq, finished.State, finished.Status = 99, api.HTTPStateComplete, 204
	feed.ch <- api.HTTPFrame{Instance: "i", Seq: 15, Events: []api.HTTPEvent{{Kind: "new", Record: &more}, {Kind: "done", Record: &finished}}}
	testkit.UntilText(t, tt, "已暂停显示 · 新增 1")
	if tt.HasText("/later") || tt.HasText("204") || !tt.HasText("显示 4 / 4 条请求") {
		t.Fatalf("paused list changed: %q", tt.Texts())
	}
	h.click(t, "恢复 HTTP 更新")
	testkit.UntilText(t, tt, "/later")
	if !tt.HasText("204") || !tt.HasText("显示 5 / 5 条请求") || !tt.HasText("实时更新") {
		t.Fatalf("resumed: %q", tt.Texts())
	}
}

// Clearing asks first, then asks the core and drops the finished records,
// keeping the running exchange.
func TestHTTPMonitorClear(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(httpFixture()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	h.click(t, "清空 HTTP 请求")
	testkit.UntilText(t, tt, "清空 HTTP 请求？")
	if h.requested("POST /api/v1/http/clear") {
		t.Fatal("cleared without confirming")
	}
	h.click(t, "清空请求")
	testkit.Until(t, tt, func() bool { return h.requested("POST /api/v1/http/clear") && !tt.HasText("/user") })
	if !tt.HasText("/stream") || tt.HasText("/v1/chat") || !tt.HasText("显示 1 / 1 条请求") {
		t.Fatalf("after clear: %q", tt.Texts())
	}
}

// "Load earlier" reads the page before the oldest record and ends when
// the core has no more.
func TestHTTPMonitorLoadEarlier(t *testing.T) {
	frame := httpFixture()
	frame.Status.RecordCount = 6
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(frame), func(srv *testkit.Server) {
		srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
			a, b := hrec(10, "old.example.com", 200, "complete"), hrec(9, "old.example.com", 200, "complete")
			a.Path, b.Path = "/old-a", "/old-b"
			testkit.JSON(api.HTTPList{Instance: "i", Records: []api.HTTPRecord{a, b}})(w, r)
		})
	})
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	h.click(t, "加载更早请求")
	testkit.UntilText(t, tt, "/old-b")
	if !h.requestedAll("GET /api/v1/http/records", "before=11", "limit=500") || !tt.HasText("显示 6 / 6 条请求") || tt.HasText("加载更早请求") {
		t.Fatalf("requests %v texts %q", h.srv.Requests(), tt.Texts())
	}
}

// Another page opens the list limited to a connection, which also reads
// that connection's older records.
func TestHTTPMonitorConnectionView(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(httpFixture()), func(srv *testkit.Server) {
		srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
			old := hrec(3, "old.example.com", 200, "complete")
			old.Path, old.ConnectionID = "/older-on-c2", "c2"
			testkit.JSON(api.HTTPList{Instance: "i", Records: []api.HTTPRecord{old}})(w, r)
		})
	})
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	h.w.Open(env.ConnectionsView{HTTPConnection: "c2"})
	testkit.UntilText(t, tt, "/older-on-c2")
	if tt.HasText("/user") || !tt.HasText("/v1/chat") || !tt.HasText("/logo.png") || !tt.HasText("连接 c2") {
		t.Fatalf("limited: %q", tt.Texts())
	}
	if !h.requestedAll("GET /api/v1/http/records", "connection_id=c2", "limit=500") {
		t.Fatalf("requests %v", h.srv.Requests())
	}
	// Removing the tag lifts the limit.
	h.click(t, "移除 连接 c2")
	testkit.UntilText(t, tt, "/user")
}

// The narrow window shows the inspector over the list, and the filters in
// their own panel.
func TestHTTPMonitorNarrow(t *testing.T) {
	h := newHTTPHarness(t, 760, 640, enabled(), newHTTPFeed(httpFixture()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	if tt.HasText("状态码") {
		t.Fatalf("filters beside a narrow list: %q", tt.Texts())
	}
	h.click(t, "切换 HTTP 筛选")
	testkit.UntilText(t, tt, "状态码")
	savePreview(t, tt, "http-narrow-filters.png")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !tt.HasText("状态码") })
	h.click(t, "/user")
	testkit.UntilText(t, tt, "概览")
	savePreview(t, tt, "http-narrow-inspector.png")
	h.click(t, "关闭详情")
	testkit.Until(t, tt, func() bool { return !tt.HasText("概览") })
}

// Hiding the page stops the stream; showing it again resumes from the
// cursor and keeps the records.
func TestHTTPMonitorStopsHidden(t *testing.T) {
	feed := newHTTPFeed(httpFixture())
	h := newHTTPHarness(t, 1440, 820, enabled(), feed, nil)
	testkit.UntilText(t, h.tt, "/user")
	h.w.SetActive(false)
	if h.w.http.active || h.w.http.cancel != nil {
		t.Fatal("stream kept")
	}
	h.w.SetActive(true)
	if h.w.http.cursorSeq != 14 || h.w.http.cursorInstance != "i" {
		t.Fatalf("cursor %v %q", h.w.http.cursorSeq, h.w.http.cursorInstance)
	}
	testkit.Until(t, h.tt, func() bool { return h.requestedAll("GET /api/v1/http/stream", "instance=i", "after=14") })
	if !h.tt.HasText("/user") {
		t.Fatalf("records dropped: %q", h.tt.Texts())
	}
}

// Many frames queued between two UI frames apply in one pass, and the
// list rebuilds from the store without keeping evicted rows.
func TestHTTPMonitorBatchesFrames(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), nil)
	m := h.w.http
	var frames []api.HTTPFrame
	for i := 0; i < 600; i++ {
		frames = append(frames, frame(false, uint64(i*10+1), uint64(i*10+2), uint64(i*10+3), uint64(i*10+4), uint64(i*10+5), uint64(i*10+6), uint64(i*10+7), uint64(i*10+8), uint64(i*10+9), uint64(i*10+10)))
	}
	before := m.version
	m.applyFrames(frames)
	if m.version != before+1 || len(m.store.rows) != httpLimit || m.store.evicted != 1000 {
		t.Fatalf("version %d, rows %d, evicted %d", m.version-before, len(m.store.rows), m.store.evicted)
	}
	d := m.derive()
	if len(d.rows) != httpLimit || d.rows[0].ID != "6000" || d.total != httpLimit {
		t.Fatalf("derived %d rows, newest %s", len(d.rows), d.rows[0].ID)
	}
	// A sorted list is rebuilt at once when the sort changes.
	m.sortCol = "host"
	m.table.Sort.Descending = false
	if d2 := m.derive(); d2 == d || len(d2.rows) != httpLimit {
		t.Fatal("sort not applied")
	}
	// Events during the throttle interval show once it has passed.
	m.applyFrames([]api.HTTPFrame{frame(false, 7000)})
	time.Sleep(deriveEvery + 20*time.Millisecond)
	d3 := m.derive()
	if len(d3.rows) != httpLimit || !slices.ContainsFunc(d3.rows, func(r *httpRow) bool { return r.ID == "7000" }) {
		t.Fatal("new record missing after the throttle")
	}
}

// requestedAll reports whether a request started with prefix and held
// every part.
func (h *harness) requestedAll(prefix string, parts ...string) bool {
	return slices.ContainsFunc(h.srv.Requests(), func(r string) bool {
		return strings.HasPrefix(r, prefix) && !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(r, p) })
	})
}
