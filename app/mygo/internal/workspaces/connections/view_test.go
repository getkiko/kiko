package connections

import (
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// ndjson answers frames as NDJSON lines, then keeps the stream open.
func ndjson(frames ...any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, frame := range frames {
			raw, _ := json.Marshal(frame)
			fmt.Fprintf(w, "%s\n", raw)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
}

func fixtureDNS() api.DNSView {
	query := func(id, name, status string, seq int) api.DNSQuery {
		return api.DNSQuery{ID: id, Revision: api.Uint64(seq), Seq: api.Uint64(seq), StartedAt: fmt.Sprintf("2026-10-16T08:00:%02d.125Z", seq),
			FinishedAt: fmt.Sprintf("2026-10-16T08:00:%02d.150Z", seq), DurationMS: 25, Role: "query", Status: status,
			Source:    api.DNSSource{Address: fmt.Sprintf("172.18.0.1:%d", 53000+seq), Inbound: "tun-in", Process: "Google Chrome Helper"},
			Questions: []api.DNSQuestion{{Name: name, Type: "A", Class: "IN"}}, Resolver: "cloudflare", ResolvedAddresses: []string{"104.18.39.21"},
			Attempts: []api.DNSAttempt{
				{ID: id + "a", Protocol: "udp", Resolver: "cloudflare", Endpoint: "1.1.1.1:53", Connection: &api.DNSConnection{Outbound: []string{"直连"}}},
				{ID: id + "b", Protocol: "tcp", Resolver: "cloudflare", Endpoint: "1.1.1.1:53", Reused: true, Connection: &api.DNSConnection{Outbound: []string{"直连"}}},
			},
			Response: &api.DNSMessage{Rcode: "NOERROR", Size: 64, Answer: []api.DNSRecord{{Name: name, Type: "A", TTL: 300, Data: "104.18.39.21"}}}}
	}
	return api.DNSView{Instance: "a", Seq: 3, Reset: true, Queries: []api.DNSQuery{
		query("1", "example.org.", "success", 1), query("2", "chatgpt.com.", "success", 2), query("3", "blocked.test.", "timeout", 3),
	}, Stats: api.DNSStats{Total: 3, Retained: 3, Errors: 1, Timeouts: 1}}
}

type harness struct {
	srv   *testkit.Server
	env   *env.Env
	shell *testkit.Shell
	conns *fakeConnections
	w     *Workspace
	tt    *ui.Tester
	pages []string
}

func newHarness(t *testing.T, routes func(srv *testkit.Server)) *harness {
	return newSizedHarness(t, 1440, 820, routes)
}

func newSizedHarness(t *testing.T, width, height int, routes func(srv *testkit.Server)) *harness {
	h := &harness{srv: testkit.NewServer(t)}
	h.conns = newFakeConnections(fixtureRows(time.Now()))
	h.srv.Route("GET", "/connections", h.conns.ServeHTTP)
	h.srv.Route("POST", "/connections/close", h.conns.closeMatching)
	h.srv.Route("GET", "/dns/capabilities", testkit.JSON(api.DNSCapabilities{Supported: true, Enabled: true, SchemaVersion: 1}))
	h.srv.Route("GET", "/dns/stream", ndjson(fixtureDNS()))
	routes(h.srv)
	h.env, h.shell = testkit.NewEnv(h.srv)
	h.env.SetNavigator(func(page string, arg any) { h.pages = append(h.pages, fmt.Sprint(page, ":", arg)) })
	h.w = New(h.env, nil)
	h.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		h.env.Drain(c)
		h.w.View(c)
	}, width, height)
	h.w.SetActive(true)
	t.Cleanup(func() { h.w.SetActive(false) })
	return h
}

func (h *harness) click(t *testing.T, label string) {
	t.Helper()
	if err := h.tt.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts %q", label, err, h.tt.Texts())
	}
}

// choose opens the More menu labeled more and clicks the items of path in
// it, the submenus the others label.
func (h *harness) choose(t *testing.T, more string, path ...string) {
	t.Helper()
	h.click(t, more)
	for _, label := range path {
		h.tt.Frame()
		h.click(t, label)
	}
	h.tt.Frame()
}

func (h *harness) requested(prefix string) bool {
	return slices.ContainsFunc(h.srv.Requests(), func(r string) bool { return strings.HasPrefix(r, prefix) })
}

func TestConnectionMonitor(t *testing.T) {
	now := time.Now()
	h := newHarness(t, func(srv *testkit.Server) {
		srv.Route("DELETE", "/connections", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	tt := h.tt
	// Grouped by App by default.
	testkit.UntilText(t, tt, "Google Chrome Helper")
	if tt.HasText("mtalk.google.com") {
		t.Fatal("groups start collapsed")
	}
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, tt, "mtalk.google.com")
	if !tt.HasText("显示 1–4 / 4 条连接") || !tt.HasText("RuleSet: Gfw, Telegram, openai") {
		t.Fatalf("rows: %q", tt.Texts())
	}

	h.click(t, "活动")
	testkit.Until(t, tt, func() bool { return tt.HasText("显示 1–3 / 3 条连接") })

	// Search.
	h.click(t, "搜索主机、进程、地址、链路…")
	tt.Type("github")
	testkit.Until(t, tt, func() bool { return !tt.HasText("mtalk.google.com") })
	if !tt.HasText("api.github.com") {
		t.Fatalf("search: %q", tt.Texts())
	}
	h.click(t, "清除搜索")
	testkit.UntilText(t, tt, "mtalk.google.com")

	// A filter option filters and shows "Clear".
	if tt.HasText("清除") {
		t.Fatal("清除 shown without a filter")
	}
	h.click(t, "直连")
	testkit.Until(t, tt, func() bool { return !tt.HasText("api.github.com") })
	if !tt.HasText("ime.doubao.com") || !tt.HasText("清除") {
		t.Fatalf("filter: %q", tt.Texts())
	}
	h.click(t, "清除")
	testkit.UntilText(t, tt, "api.github.com")

	// Selecting a row opens the inspector; PID opens Processes.
	h.click(t, "api.github.com")
	testkit.UntilText(t, tt, "进程与用量")
	if !tt.HasText("142.250.141.181:443") || !tt.HasText("复制连接详情") {
		t.Fatalf("inspector: %q", tt.Texts())
	}
	savePreview(t, tt, "connections-light.png")
	h.click(t, "在进程中查看 PID 101")
	if !slices.Contains(h.pages, "processes:101") {
		t.Fatalf("navigate: %v", h.pages)
	}
	h.click(t, "关闭此连接")
	testkit.Until(t, tt, func() bool { return h.requested("DELETE /api/v1/connections/c2") })

	// Close all through the confirmation.
	h.choose(t, "更多操作", "关闭全部连接")
	h.click(t, "关闭 3 条连接")
	testkit.Until(t, tt, func() bool { return slices.Contains(h.srv.Requests(), "DELETE /api/v1/connections") })

	// Export of the filtered rows.
	csv := connectionsCSV([]*Row{rowOf(fixtureRows(now)[1])}, h.w.conn.columns)
	if !strings.Contains(string(csv), `"主机 / 进程","规则 / 链路"`) || !strings.Contains(string(csv), "api.github.com") {
		t.Fatalf("csv %s", csv)
	}
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "connections-dark.png")
}

// "Close listed connections" closes the filtered list only, after a
// confirmation that counts them, and keeps the confirmation open with the
// error when the server refuses.
func TestCloseFilteredConnections(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {})
	tt := h.tt
	testkit.UntilText(t, tt, "Google Chrome Helper")
	h.w.conn.setMode(groupNone)
	h.click(t, "活动")
	testkit.Until(t, tt, func() bool { return tt.HasText("显示 1–3 / 3 条连接") })

	// A search without matches leaves nothing to close.
	h.click(t, "搜索主机、进程、地址、链路…")
	tt.Type("nothing-matches")
	testkit.Until(t, tt, func() bool { return tt.HasText("显示 0–0 / 0 条连接") })
	if !h.w.conn.closeListDisabled(h.w.conn.derive()) || h.w.conn.closeDisabled(h.w.conn.derive()) {
		t.Fatal("an empty list can be closed, or 关闭全部连接 followed it")
	}

	// Search narrows the list to one connection; the confirmation counts it.
	h.click(t, "清除搜索")
	h.click(t, "搜索主机、进程、地址、链路…")
	tt.Type("github")
	testkit.Until(t, tt, func() bool { return tt.HasText("显示 1–1 / 1 条连接") })
	h.conns.mu.Lock()
	h.conns.failClose = 1
	h.conns.mu.Unlock()
	if h.w.conn.closeListDisabled(h.w.conn.derive()) {
		t.Fatal("a list with an active connection cannot be closed")
	}
	h.w.conn.listConfirm.Open = true
	testkit.UntilText(t, tt, "关闭 1 条连接")
	savePreview(t, tt, "3-close-list-light.png")
	tt.SetDark(true)
	tt.Advance(time.Second)
	tt.Frame()
	savePreview(t, tt, "3-close-list-dark.png")
	tt.SetDark(false)
	tt.Advance(time.Second)
	tt.Frame()
	h.click(t, "关闭 1 条连接")
	testkit.UntilText(t, tt, "核心未响应")
	savePreview(t, tt, "3-close-list-error-light.png")
	if !tt.HasText("重试") {
		t.Fatalf("no retry after the error: %q", tt.Texts())
	}
	h.click(t, "重试")
	testkit.Until(t, tt, func() bool {
		return len(h.conns.closeRequests()) == 2 && !tt.HasText("核心未响应") && !tt.HasText("重试") && !tt.HasText("关闭 1 条连接")
	})
	requests := h.conns.closeRequests()
	if len(requests) != 2 || !strings.Contains(requests[1], "search=github") {
		t.Fatalf("close requests %v", requests)
	}
	// Only the listed connection closed: the list and the counts follow.
	h.click(t, "清除搜索")
	testkit.Until(t, tt, func() bool { return tt.HasText("显示 1–2 / 2 条连接") })
	if tt.HasText("api.github.com") || !tt.HasText("mtalk.google.com") || !tt.HasText("ime.doubao.com") {
		t.Fatalf("after close: %q", tt.Texts())
	}

	// Without a filter it closes every active connection, as "Close all connections".
	h.w.conn.listConfirm.Open = true
	testkit.UntilText(t, tt, "关闭 2 条连接")
	h.click(t, "关闭 2 条连接")
	testkit.Until(t, tt, func() bool { return len(h.conns.closeRequests()) == 3 })
	if last := h.conns.closeRequests()[2]; strings.Contains(last, "search") || strings.Contains(last, "filter.") {
		t.Fatalf("unfiltered close carried a filter: %s", last)
	}
	testkit.Until(t, tt, func() bool { return tt.HasText("暂无连接") || tt.HasText("显示 0–0 / 0 条连接") })
}

func TestConnectionGroups(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
	})
	tt := h.tt
	testkit.UntilText(t, tt, "Doubao")
	h.click(t, "展开 Doubao")
	testkit.UntilText(t, tt, "ime.doubao.com")
	h.click(t, "Doubao")
	tt.Key(0, ui.KeyEnter)
	testkit.UntilText(t, tt, "用量汇总")
	if !tt.HasText("1 条连接 · 1 个进程") {
		t.Fatalf("group detail: %q", tt.Texts())
	}
}

func TestConnectionEmpty(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", newFakeConnections(nil).ServeHTTP)
	})
	testkit.UntilText(t, h.tt, "暂无连接")
	if h.tt.HasText("源地址") && h.tt.HasText("清除") {
		t.Fatal("filters shown without rows")
	}
}

func TestDNSMonitor(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", newFakeConnections(nil).ServeHTTP)
		srv.Route("GET", "/dns/queries/", func(w http.ResponseWriter, r *http.Request) {
			q := fixtureDNS().Queries[1]
			q.Policy = []string{"rule 3: domain_suffix=chatgpt.com → proxy"}
			testkit.JSON(q)(w, r)
		})
		srv.Route("DELETE", "/dns/history", testkit.JSON(api.DNSView{Instance: "a", Seq: 4}))
	})
	tt := h.tt
	h.click(t, "DNS 查询")
	testkit.UntilText(t, tt, "example.org.")
	if !tt.HasText("UDP → TCP") || !tt.HasText("显示 1–3 / 3 条查询") || !tt.HasText("源 IP") || !tt.HasText("172.18.0.1") {
		t.Fatalf("dns rows: %q", tt.Texts())
	}
	if h.w.conn.active || !h.w.dns.active {
		t.Fatal("streams follow the tab")
	}

	// Segments and filters.
	h.click(t, "异常")
	testkit.Until(t, tt, func() bool { return !tt.HasText("example.org.") })
	h.click(t, "全部")
	h.click(t, "超时")
	testkit.Until(t, tt, func() bool { return !tt.HasText("example.org.") && tt.HasText("清除") })
	h.click(t, "清除")
	testkit.UntilText(t, tt, "example.org.")

	// Inspector with the detail.
	h.click(t, "chatgpt.com.")
	testkit.UntilText(t, tt, "上游尝试")
	testkit.Until(t, tt, func() bool { return h.requested("GET /api/v1/dns/queries/2?instance=a") })
	testkit.UntilText(t, tt, "规则与策略")
	savePreview(t, tt, "dns-light.png")
	h.click(t, "切换 DNS 列表密度")
	testkit.UntilText(t, tt, "复用连接")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "dns-dark.png")
	tt.SetDark(false)

	// Export and clear.
	h.choose(t, "更多 DNS 操作", "导出 DNS 查询")
	testkit.Until(t, tt, func() bool { return len(h.shellSaved()) > 0 })
	if name := h.shellSaved()[0]; !strings.HasPrefix(name, "dns-") || !strings.HasSuffix(name, ".json") {
		t.Fatal(name)
	}
	h.choose(t, "更多 DNS 操作", "清空 DNS 历史")
	h.click(t, "清空历史")
	testkit.Until(t, tt, func() bool { return h.requested("DELETE /api/v1/dns/history") && tt.HasText("尚无 DNS 查询") })
}

func (h *harness) shellSaved() []string {
	var names []string
	for name := range h.shell.Saved {
		names = append(names, name)
	}
	return names
}

func TestNarrowOverlays(t *testing.T) {
	h := newSizedHarness(t, 760, 640, func(srv *testkit.Server) {
	})
	tt := h.tt
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, tt, "mtalk.google.com")
	if tt.HasText("tun-in") {
		t.Fatal("narrow windows start without the filter pane")
	}
	h.click(t, "筛选")
	testkit.UntilText(t, tt, "tun-in")
	savePreview(t, tt, "connections-narrow-filter.png")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !tt.HasText("tun-in") })
	h.click(t, "mtalk.google.com")
	testkit.UntilText(t, tt, "进程与用量")
	savePreview(t, tt, "connections-narrow-inspector.png")
	h.click(t, "关闭详情")
	testkit.Until(t, tt, func() bool { return !tt.HasText("进程与用量") })
}

func TestDNSDisabled(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", newFakeConnections(nil).ServeHTTP)
		srv.Route("GET", "/dns/capabilities", testkit.JSON(api.DNSCapabilities{Supported: true}))
	})
	h.click(t, "DNS 查询")
	testkit.UntilText(t, h.tt, "尚无 DNS 查询")
	testkit.UntilText(t, h.tt, "等待核心启动 DNS 监控")
	if h.tt.HasText("源 IP") {
		t.Fatal("filters shown without queries")
	}
}

// savePreview writes the frame to CONN_PREVIEW_DIR, for reviewing the look.
func savePreview(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("CONN_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestPreferencesRestore(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
	})
	tt := h.tt
	testkit.UntilText(t, tt, "Doubao")
	h.click(t, "列设置")
	h.click(t, "目标地址")
	testkit.Until(t, tt, func() bool { return slices.Contains(h.w.conn.columns, colDestination) })
	h.click(t, "关闭")
	h.w.conn.setMode(groupParent)
	testkit.Until(t, tt, func() bool { return !h.w.conn.settings })
	h.click(t, "筛选")
	h.click(t, "DNS 查询")
	testkit.UntilText(t, tt, "example.org.")
	h.click(t, "切换 DNS 列表密度")
	h.click(t, "切换 DNS 筛选")

	again := New(h.env, nil)
	conn, dns := again.conn, again.dns
	if !slices.Contains(conn.columns, colDestination) || conn.mode != groupParent || conn.sidebar == nil || *conn.sidebar {
		t.Fatalf("connections prefs: %v %v %v", conn.columns, conn.mode, conn.sidebar)
	}
	if !slices.Equal(conn.columnOrder, h.w.conn.columnOrder) {
		t.Fatalf("order %v", conn.columnOrder)
	}
	if !dns.detailed || dns.sidebar == nil || *dns.sidebar {
		t.Fatalf("dns prefs: %v %v", dns.detailed, dns.sidebar)
	}
	var mode string
	if !h.env.Prefs.Load("kiko-connections-group-mode", &mode) || mode != "parent" {
		t.Fatal(mode)
	}
}

func TestConnectionClearHistory(t *testing.T) {
	var mu sync.Mutex
	var scopes []string
	fail := true
	conns := newFakeConnections(fixtureRows(time.Now()))
	h := newHarness(t, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", conns.ServeHTTP)
		srv.Route("DELETE", "/connections/history", func(w http.ResponseWriter, r *http.Request) {
			var body struct{ Scope string }
			if err := testkit.Body(r, &body); err != nil || r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			scopes = append(scopes, body.Scope)
			if fail {
				fail = false
				http.Error(w, `{"error":"暂时无法清空"}`, http.StatusInternalServerError)
				return
			}
			conns.clear(body.Scope)
			w.WriteHeader(http.StatusNoContent)
		})
	})
	tt := h.tt
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, tt, "mtalk.google.com")
	sent := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(scopes) }

	// "Clear records" is a submenu of the scopes with their counts.
	h.choose(t, "更多操作", "清空记录")
	for _, scope := range []string{"全部（4 条）", "已连接（3 条）", "已关闭（1 条）"} {
		if !tt.HasText(scope) {
			t.Fatalf("submenu: %q", tt.Texts())
		}
	}
	h.click(t, "已关闭（1 条）")
	testkit.UntilText(t, tt, "清空连接记录")
	testkit.UntilText(t, tt, "清空 1 条记录")
	savePreview(t, tt, "connections-clear.png")

	// A failure stays in the popover with a retry, then the retry closes it.
	h.click(t, "清空 1 条记录")
	testkit.Until(t, tt, func() bool { return tt.HasText("重试") })
	if got := sent(); !slices.Equal(got, []string{"closed"}) || !tt.HasText("清空连接记录") {
		t.Fatalf("first attempt %v: %q", got, tt.Texts())
	}
	savePreview(t, tt, "connections-clear-error.png")
	h.click(t, "重试")
	testkit.Until(t, tt, func() bool { return !tt.HasText("清空连接记录") })
	if got := sent(); !slices.Equal(got, []string{"closed", "closed"}) {
		t.Fatalf("retry %v", got)
	}

	// "Connected" clears the active records; a paused display reads the page
	// again after the server cleared them.
	h.click(t, "活动")
	testkit.UntilText(t, tt, "显示 1–3 / 3 条连接")
	h.click(t, "暂停")
	h.choose(t, "更多操作", "清空记录", "已连接（3 条）")
	testkit.UntilText(t, tt, "清空 3 条记录")
	before := len(conns.queried())
	h.click(t, "清空 3 条记录")
	testkit.Until(t, tt, func() bool { return len(sent()) == 3 && tt.HasText("暂无活动连接") })
	if got := sent(); got[2] != "active" || len(conns.queried()) <= before {
		t.Fatalf("scopes %v, queries %d", got, len(conns.queried()))
	}
}

func TestFilterPaneResize(t *testing.T) {
	h := newHarness(t, func(srv *testkit.Server) {
	})
	tt := h.tt
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, tt, "mtalk.google.com")
	handle, ok := tt.Find(kit.FilterResizeLabel)
	if !ok {
		t.Fatalf("no handle: %q", tt.Texts())
	}
	x, y := handle.X+handle.W/2, handle.Y+handle.H/2
	if x < kit.MonitorFilterW-4 || x > kit.MonitorFilterW+4 {
		t.Fatalf("handle at %v", handle)
	}
	drag := func(dx float32) {
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x+dx*float32(i)/4, y)
		}
		tt.Release(x+dx, y)
		tt.Frame()
	}
	drag(112)
	if w := h.w.conn.filterWidth.Width; w != kit.MonitorFilterW+112 {
		t.Fatalf("dragged width %v", w)
	}
	var saved float32
	if !h.env.Prefs.Load(prefConnFilterWidth, &saved) || saved != kit.MonitorFilterW+112 {
		t.Fatalf("saved %v", saved)
	}
	if again := New(h.env, nil); again.conn.filterWidth.Width != saved {
		t.Fatalf("restored %v", again.conn.filterWidth.Width)
	}
	savePreview(t, tt, "connections-filter-resized.png")

	// The bounds hold, and a double click returns to the default.
	handle, _ = tt.Find(kit.FilterResizeLabel)
	x = handle.X + handle.W/2
	drag(600)
	if w := h.w.conn.filterWidth.Width; w != kit.FilterPaneMaxW {
		t.Fatalf("max width %v", w)
	}
	handle, _ = tt.Find(kit.FilterResizeLabel)
	x = handle.X + handle.W/2
	tt.ClickAt(x, y)
	tt.ClickAt(x, y)
	tt.Frame()
	if h.w.conn.filterWidth.Width != 0 || !h.env.Prefs.Load(prefConnFilterWidth, &saved) || saved != 0 {
		t.Fatalf("reset %v %v", h.w.conn.filterWidth.Width, saved)
	}
	if handle, _ = tt.Find(kit.FilterResizeLabel); handle.X+handle.W/2 != kit.MonitorFilterW {
		t.Fatalf("default handle at %v", handle)
	}
}

// frames runs the view for d, as the window would.
func frames(tt *ui.Tester, d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
}

// A tab shows the server's whole page at once: no count between the old
// tab's and the new one's.
func TestConnectionTabSwitchWholePage(t *testing.T) {
	now := time.Now()
	var rows []api.ConnectionRow
	for i := range 200 {
		row := fixtureRows(now)[i%3]
		row.ID = fmt.Sprint("r", i)
		if i >= 120 {
			row.ClosedAt = api.Int64(now.UnixMilli())
		}
		rows = append(rows, row)
	}
	conns := newFakeConnections(rows)
	h := newHarness(t, func(srv *testkit.Server) { srv.Route("GET", "/connections", conns.ServeHTTP) })
	tt := h.tt
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, tt, "显示 1–50 / 200 条连接")
	h.click(t, "已关闭")
	testkit.Until(t, tt, func() bool {
		for _, text := range tt.Texts() {
			if strings.HasPrefix(text, "显示 ") && text != "显示 1–50 / 200 条连接" && text != "显示 1–50 / 80 条连接" {
				t.Fatalf("intermediate %q", text)
			}
		}
		return tt.HasText("显示 1–50 / 80 条连接")
	})
	last := conns.queried()[len(conns.queried())-1]
	if !strings.Contains(last, "state=closed") || !strings.Contains(last, "limit=50") {
		t.Fatalf("query %q", last)
	}
}

// The page polls while shown and not paused; hidden, it stops and keeps
// its page, shown at once on return until the server's answer replaces it.
func TestConnectionPolling(t *testing.T) {
	h := newHarness(t, func(*testkit.Server) {})
	tt := h.tt
	testkit.UntilText(t, tt, "Doubao")
	start := len(h.conns.queried())
	frames(tt, 1500*time.Millisecond)
	if n := len(h.conns.queried()); n <= start {
		t.Fatalf("no poll: %d", n)
	}

	h.click(t, "暂停")
	frames(tt, 200*time.Millisecond)
	paused := len(h.conns.queried())
	frames(tt, 1500*time.Millisecond)
	if n := len(h.conns.queried()); n != paused || !tt.HasText("Doubao") {
		t.Fatalf("paused polled: %d → %d", paused, n)
	}
	h.click(t, "继续")

	h.w.SetActive(false)
	frames(tt, 200*time.Millisecond)
	hidden := len(h.conns.queried())
	if h.w.conn.data == nil {
		t.Fatal("hidden page dropped")
	}
	frames(tt, 1500*time.Millisecond)
	if n := len(h.conns.queried()); n != hidden {
		t.Fatalf("hidden polled: %d → %d", hidden, n)
	}
	h.w.SetActive(true)
	tt.Frame()
	if tt.HasText("正在读取连接…") || !tt.HasText("Doubao") {
		t.Fatalf("kept page not shown at once: %q", tt.Texts())
	}
	testkit.Until(t, tt, func() bool { return len(h.conns.queried()) > hidden })
	testkit.UntilText(t, tt, "Doubao")
}

// A filter another workspace chose that no connection matches stays in
// its group, to be seen and cleared.
func TestConnectionFilterKeepsUnlistedValue(t *testing.T) {
	h := newHarness(t, func(*testkit.Server) {})
	tt := h.tt
	testkit.UntilText(t, tt, "Google Chrome Helper")
	h.w.Open(env.ConnectionsView{Filter: map[string][]string{"rule": {"9 · 不存在的规则"}}})
	testkit.UntilText(t, tt, "当前筛选条件下暂无连接")
	if !tt.HasText("规则") || !tt.HasText("9 · 不存在的规则") {
		t.Fatalf("unlisted rule filter not shown: %q", tt.Texts())
	}
	h.click(t, "9 · 不存在的规则")
	testkit.UntilText(t, tt, "Google Chrome Helper")
	if tt.HasText("9 · 不存在的规则") {
		t.Fatal("cleared filter still listed")
	}
}

// Dragging a header's right edge widens its column, which is saved; a
// double click restores it.
func TestConnectionColumnResize(t *testing.T) {
	h := newHarness(t, func(*testkit.Server) {})
	tt := h.tt
	testkit.UntilText(t, tt, "Google Chrome Helper")
	label := "调整" + columnLabels[colSource] + "列宽"
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("no %s: %q", label, tt.Texts())
	}
	// The grip spans the header's height, where its hairline shows.
	if r.H < float32(kit.RowHeight)-2 {
		t.Errorf("grip %v high, header %v", r.H, kit.RowHeight)
	}
	// It straddles the hairline: the pointer shows the column cursor
	// either side of it.
	for _, dx := range []float32{-4, 0, 4} {
		tt.Move(r.X+r.W/2+dx, r.Y+r.H/2)
		tt.Frame()
		if got := tt.Cursor(); got != ui.CursorResizeColumn {
			t.Errorf("cursor %v at %+v from the hairline", got, dx)
		}
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+20, y)
	tt.Move(x+60, y)
	tt.Release(x+60, y)
	tt.Frame()
	width := h.w.conn.table.Widths[string(colSource)]
	if width < columnWidths[colSource]+50 {
		t.Fatalf("dragged %s to %v, want about %v", columnLabels[colSource], width, columnWidths[colSource]+60)
	}
	var saved map[string]float32
	if !h.env.Prefs.Load(prefConnColumnWidths, &saved) || saved[string(colSource)] != width {
		t.Errorf("widths not saved: %v", saved)
	}
	tt.Advance(time.Second)
	r, _ = tt.Find(label)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if _, ok := h.w.conn.table.Widths[string(colSource)]; ok {
		t.Errorf("double click kept %v", h.w.conn.table.Widths)
	}
}
