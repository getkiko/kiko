package connections

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestHARRequest(t *testing.T) {
	var rows []*httpRow
	for i := 1; i <= harExportLimit+10; i++ {
		rows = append(rows, newHTTPRow(hrec(uint64(i), "a.com", 200, api.HTTPStateComplete)))
	}
	o := defaultHARoptions()
	if o.scope != harFiltered || !o.bodies || !o.auth || !o.cookie || o.query {
		t.Fatalf("defaults %+v", o)
	}
	req := harRequest(o, "3", rows)
	if len(req.IDs) != harExportLimit || req.IDs[0] != "1" || req.IDs[harExportLimit-1] != "5000" {
		t.Fatalf("filtered ids: %d first %v", len(req.IDs), req.IDs[:1])
	}
	if !req.Bodies || req.Sanitize != (api.HARSanitize{AuthHeaders: true, Cookies: true}) {
		t.Fatalf("request %+v", req)
	}
	o.scope, o.bodies, o.query = harSelected, false, true
	req = harRequest(o, "3", rows)
	if !slices.Equal(req.IDs, []string{"3"}) || req.Bodies || req.Sanitize != (api.HARSanitize{AuthHeaders: true, Cookies: true, QuerySecrets: true, Bodies: true}) {
		t.Fatalf("selected %+v", req)
	}
	// Without a selection the scope falls back to the list.
	if req = harRequest(o, "", rows[:2]); !slices.Equal(req.IDs, []string{"1", "2"}) {
		t.Fatalf("no selection %+v", req.IDs)
	}
	if got := harFileName(time.Date(2026, 11, 3, 9, 5, 7, 0, time.Local)); got != "kiko-20261103-090507.har" {
		t.Fatalf("name %q", got)
	}
}

// harFake is the core's /http/har and a recorder for the saver.
type harFake struct {
	mu     sync.Mutex
	reqs   []api.HARRequest
	status int
	// hold keeps the response open after its first chunk until the
	// request ends.
	hold bool
	// saved are the files the saver took; refuse makes it cancel.
	saved  map[string][]byte
	refuse bool
	saves  int
}

const harDoc = `{"log":{"version":"1.2","entries":[]}}`

func (f *harFake) route(srv *testkit.Server) {
	srv.Route("POST", "/http/har", func(w http.ResponseWriter, r *http.Request) {
		var req api.HARRequest
		_ = testkit.Body(r, &req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		status, hold := f.status, f.hold
		f.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"error":"export failed: store busy"}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if hold {
			fmt.Fprint(w, harDoc[:20])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, harDoc)
	})
}

func (f *harFake) save(name string, data []byte, _ env.FileFilter) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.refuse {
		return false, nil
	}
	if f.saved == nil {
		f.saved = map[string][]byte{}
	}
	f.saved[name] = data
	return true, nil
}

func (f *harFake) requests() []api.HARRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func newHARHarness(t *testing.T, width, height int) (*harness, *harFake) {
	fake := &harFake{}
	h := newHTTPHarness(t, width, height, enabled(), newHTTPFeed(inspectorFrame()), fake.route)
	h.w.http.har.saver = fake.save
	testkit.UntilText(t, h.tt, "/json")
	return h, fake
}

func TestHARExportFlow(t *testing.T) {
	h, fake := newHARHarness(t, 1440, 900)
	tt, m := h.tt, h.w.http
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "包含正文")
	for _, want := range []string{"当前筛选 · 7 条", "脱敏", "认证头", "Cookie", "查询参数密钥", "关闭", "导出"} {
		if !tt.HasText(want) {
			t.Errorf("popover lacks %q: %q", want, tt.Texts())
		}
	}
	savePreview(t, tt, "30-export-popover-light.png")
	setDark(tt, true)
	savePreview(t, tt, "30-export-popover-dark.png")
	setDark(tt, false)

	h.click(t, "查询参数密钥")
	testkit.Until(t, tt, func() bool { return m.har.opts.query })
	h.click(t, "Cookie")
	testkit.Until(t, tt, func() bool { return !m.har.opts.cookie })
	h.click(t, "导出")
	testkit.Until(t, tt, func() bool { return !tt.HasText("包含正文") })
	reqs := fake.requests()
	if len(reqs) != 1 || len(reqs[0].IDs) != 7 || reqs[0].IDs[0] != "27" || !reqs[0].Bodies ||
		reqs[0].Sanitize != (api.HARSanitize{AuthHeaders: true, QuerySecrets: true}) {
		t.Fatalf("requests %+v", reqs)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.saved) != 1 {
		t.Fatalf("saved %v", fake.saved)
	}
	for name, data := range fake.saved {
		if !regexp.MustCompile(`^kiko-\d{8}-\d{6}\.har$`).MatchString(name) || string(data) != harDoc {
			t.Fatalf("saved %q %q", name, data)
		}
	}
}

func TestHARExportSelectedRecord(t *testing.T) {
	h, fake := newHARHarness(t, 1440, 900)
	tt, m := h.tt, h.w.http
	h.click(t, "/text")
	testkit.UntilText(t, tt, "复制 URL")
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "所选记录")
	h.click(t, "所选记录")
	testkit.Until(t, tt, func() bool { return m.har.opts.scope == harSelected })
	h.click(t, "导出")
	testkit.Until(t, tt, func() bool { return len(fake.requests()) == 1 })
	req := fake.requests()[0]
	if !slices.Equal(req.IDs, []string{"22"}) {
		t.Fatalf("ids %v", req.IDs)
	}
}

func TestHARExportBodiesOffSanitizesBodies(t *testing.T) {
	h, fake := newHARHarness(t, 1440, 900)
	tt, m := h.tt, h.w.http
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "包含正文")
	h.click(t, "包含正文开关")
	testkit.Until(t, tt, func() bool { return !m.har.opts.bodies })
	h.click(t, "导出")
	testkit.Until(t, tt, func() bool { return len(fake.requests()) == 1 })
	if req := fake.requests()[0]; req.Bodies || !req.Sanitize.Bodies {
		t.Fatalf("request %+v", req)
	}
}

func TestHARExportFailureAndRetry(t *testing.T) {
	h, fake := newHARHarness(t, 1440, 900)
	tt := h.tt
	fake.mu.Lock()
	fake.status = 500
	fake.mu.Unlock()
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "包含正文")
	h.click(t, "导出")
	testkit.UntilText(t, tt, "export failed: store busy")
	if !tt.HasText("重试") || !tt.HasText("包含正文") {
		t.Fatalf("after failure: %q", tt.Texts())
	}
	fake.mu.Lock()
	saves := fake.saves
	fake.mu.Unlock()
	if saves != 0 {
		t.Fatal("a failed export reached the save dialog")
	}
	savePreview(t, tt, "31-export-failed-light.png")
	fake.mu.Lock()
	fake.status = 0
	fake.mu.Unlock()
	h.click(t, "重试")
	testkit.Until(t, tt, func() bool { return !tt.HasText("包含正文") })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.saves != 1 {
		t.Fatalf("saves %d", fake.saves)
	}
}

func TestHARExportProgressAndCancel(t *testing.T) {
	h, fake := newHARHarness(t, 1440, 900)
	tt, m := h.tt, h.w.http
	fake.mu.Lock()
	fake.hold = true
	fake.mu.Unlock()
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "包含正文")
	h.click(t, "导出")
	testkit.UntilText(t, tt, "取消导出")
	testkit.Until(t, tt, func() bool { return m.har.written.Load() > 0 && tt.HasText("导出中 · ") })
	savePreview(t, tt, "32-export-busy-light.png")
	// A second start while the first runs does nothing.
	m.startExport(nil)
	h.click(t, "取消导出")
	testkit.Until(t, tt, func() bool { return !m.har.busy && !tt.HasText("取消导出") })
	if m.har.err != "" || !m.har.open || tt.HasText("导出中") {
		t.Fatalf("after cancel: err %q open %v", m.har.err, m.har.open)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.saves != 0 || len(fake.reqs) != 1 {
		t.Fatalf("saves %d requests %d", fake.saves, len(fake.reqs))
	}
}

func TestHARExportSaveCancelledKeepsPopover(t *testing.T) {
	h, fake := newHARHarness(t, 1440, 900)
	tt, m := h.tt, h.w.http
	fake.refuse = true
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "包含正文")
	h.click(t, "导出")
	testkit.Until(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.saves == 1 && !m.har.busy
	})
	if !m.har.open || m.har.err != "" || !tt.HasText("包含正文") {
		t.Fatalf("popover after a cancelled save: open %v err %q", m.har.open, m.har.err)
	}
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !tt.HasText("包含正文") })
}

func TestHARExportEmptyList(t *testing.T) {
	fake := &harFake{}
	h := newHTTPHarness(t, 1440, 900, enabled(), newHTTPFeed(), fake.route)
	testkit.UntilText(t, h.tt, "暂无 HTTP 请求")
	if err := h.tt.Click("导出 HAR"); err == nil && h.w.http.har.open {
		t.Fatal("export opened without records")
	}
	h.w.http.startExport(nil)
	if h.w.http.har.err == "" || len(fake.requests()) != 0 {
		t.Fatalf("empty export: %q", h.w.http.har.err)
	}
}

func TestHARExportNarrow(t *testing.T) {
	h, _ := newHARHarness(t, 760, 800)
	tt := h.tt
	h.click(t, "导出 HAR")
	testkit.UntilText(t, tt, "包含正文")
	savePreview(t, tt, "33-export-narrow-light.png")
	setDark(tt, true)
	savePreview(t, tt, "33-export-narrow-dark.png")
	if strings.Contains(strings.Join(tt.Texts(), "|"), "导出中") {
		t.Fatal("busy without an export")
	}
}

// Every monitor's last toolbar action stays whole in the page, with the
// toolbar's 16 DIP margin, and the search and the statistics (eight-digit
// DNS counts) keep their widths, from a narrow window through the desktop
// app's narrowest page (980 less the 140 DIP sidebar) to a wide one.
func TestToolbarActionsFit(t *testing.T) {
	const stats = "累计 12,345,678 · 查询中 12 · 缓存 4,567,890"
	views := []struct{ tab, search, last string }{
		{"HTTP 请求", "搜索 URL、方法、状态、进程…", "清空 HTTP 请求"},
		{"网络连接", "搜索主机、进程、地址、链路…", "更多操作"},
		{"DNS 查询", "搜索域名、进程、地址、出口…", "更多 DNS 操作"},
	}
	search := map[string]float32{}
	statsWidth := float32(0)
	for _, width := range []int{1440, 1240, 1100, 1000, 840, 760} {
		h := newHTTPHarness(t, width, 800, enabled(), newHTTPFeed(inspectorFrame()), nil)
		testkit.UntilText(t, h.tt, "/json")
		for _, view := range views {
			h.click(t, view.tab)
			if view.tab == "DNS 查询" {
				testkit.UntilText(t, h.tt, "累计 3 · 查询中 0 · 缓存 0")
				h.w.dns.live.stats = api.DNSStats{Total: 12345678, Pending: 12, CacheHits: 4567890}
			}
			// Two frames: the toolbar reads its width from the last one.
			h.tt.Frame()
			h.tt.Frame()
			if view.tab == "DNS 查询" {
				box, ok := h.tt.Find(stats)
				if !ok {
					t.Errorf("width %d: no statistics %q in %q", width, stats, h.tt.Texts())
				} else if statsWidth == 0 {
					statsWidth = box.W
				} else if box.W < statsWidth-1 {
					t.Errorf("width %d: statistics %v DIPs wide, %v at 1440", width, box.W, statsWidth)
				}
			}
			box, ok := h.tt.Find(view.last)
			if !ok || box.W <= 0 || box.X < 0 || box.X+box.W > float32(width)-15 {
				t.Errorf("width %d, %s: %q at %v", width, view.tab, view.last, box)
			}
			field, ok := h.tt.Find(view.search)
			if !ok {
				t.Errorf("width %d, %s: no search %q", width, view.tab, view.search)
				continue
			}
			if want, seen := search[view.tab]; !seen {
				search[view.tab] = field.W
			} else if field.W < want-1 {
				t.Errorf("width %d, %s: search %v DIPs wide, %v at 1440", width, view.tab, field.W, want)
			}
		}
	}
}

// A document beyond the limit stops the transfer and is not kept.
func TestCountingBufferLimit(t *testing.T) {
	var written atomic.Int64
	aborted := false
	b := &countingBuffer{written: &written, limit: 10, abort: func() { aborted = true }}
	if n, err := b.Write([]byte("12345678")); n != 8 || err != nil || b.over() {
		t.Fatalf("within: %d %v", n, err)
	}
	if n, err := b.Write([]byte("abc")); n != 0 || !errors.Is(err, errHARTooLarge) || !aborted || !b.over() {
		t.Fatalf("beyond: %d %v aborted %v", n, err, aborted)
	}
	if written.Load() != 8 || b.buf.Len() != 8 {
		t.Fatalf("kept %d/%d", written.Load(), b.buf.Len())
	}
	if errHARTooLarge.Error() != "导出过大，关闭包含正文后重试" {
		t.Fatal(errHARTooLarge)
	}
}
