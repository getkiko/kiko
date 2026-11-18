package connections

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestHTTPCurlRedactsURLAndReferer(t *testing.T) {
	r := hrec(1, "api.example.com", 200, "complete")
	r.Method = "GET"
	r.URL = "https://user:pa55@api.example.com/v1/x?id=7&access_token=abc&Session=zz&sig=1&csrf_token=q#frag"
	r.RequestHeaders = []api.HTTPHeader{
		{Name: "Referer", Value: "https://site.example.com/p?token=abc&page=2"},
		{Name: "X-Session-Id", Value: "s1"}, {Name: "X-Csrf", Value: "c1"}, {Name: "X-Signature", Value: "sg"}, {Name: "Accept", Value: "*/*"},
	}
	got := httpCurl(&r, nil)
	for _, want := range []string{"curl 'https://<redacted>@api.example.com/v1/x?id=7&access_token=<redacted>&Session=<redacted>&sig=1&csrf_token=<redacted>#frag'",
		"Referer: https://site.example.com/p?token=<redacted>&page=2", "X-Session-Id: <redacted>", "X-Csrf: <redacted>", "X-Signature: <redacted>", "Accept: */*"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, leak := range []string{"pa55", "abc", "=zz", "=q#", "=sg", "c1"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaks %q in\n%s", leak, got)
		}
	}
	if got := redactURL("https://example.com/a?b=c"); got != "https://example.com/a?b=c" {
		t.Errorf("plain url changed: %s", got)
	}
}

func TestHostExcludedWildcard(t *testing.T) {
	d := model.EmptyDoc("")
	d.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Hostnames: &model.MITMHostnames{Exclude: []string{"*.example.com"}}}}
	if !hostExcluded(d, "api.example.com") || hostExcluded(d, "example.com") || hostExcluded(d, "other.org") {
		t.Fatal("wildcard match")
	}
}

func TestHTTPStoreConnectionRecordsStayOutOfPaging(t *testing.T) {
	s := newHTTPStore()
	s.apply(frame(true, 50, 51))
	old := hrec(3, "old.com", 200, "complete")
	s.mergeConnection(api.HTTPList{Instance: "i", Records: []api.HTTPRecord{old}}, s.epoch)
	if _, ok := s.byID["3"]; !ok || s.exhausted || s.oldest().ID != "50" {
		t.Fatalf("exhausted %v oldest %v", s.exhausted, s.oldest())
	}
	// A page of another epoch is dropped.
	stale := s.epoch
	s.apply(frame(true, 60))
	s.mergeConnection(api.HTTPList{Instance: "i", Records: []api.HTTPRecord{hrec(4, "x.com", 200, "complete")}}, stale)
	if _, ok := s.byID["4"]; ok || s.extra != nil {
		t.Fatal("stale page kept")
	}
}

func TestHTTPPausedListNotRebuilt(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), nil)
	m := h.w.http
	m.applyFrames([]api.HTTPFrame{frame(true, 1, 2)})
	m.togglePause()
	d := m.derive()
	m.applyFrames([]api.HTTPFrame{frame(false, 3)})
	m.derivedAt = time.Now().Add(-time.Second)
	if m.derive() != d || m.stale() {
		t.Fatal("paused list rebuilt")
	}
	m.togglePause()
	if m.derive() == d || len(m.derive().rows) != 3 {
		t.Fatal("resumed list not rebuilt")
	}
}

func TestHTTPCursorAdvancesAtDrain(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), nil)
	m := h.w.http
	f := frame(false, 1)
	f.Seq = 5
	m.enqueue(m.gen, f)
	m.mu.Lock()
	early := m.cursorSeq
	m.mu.Unlock()
	if early != 0 {
		t.Fatalf("cursor moved on enqueue: %d", early)
	}
	testkit.Until(t, h.tt, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.cursorInstance == "i" && m.cursorSeq == 5
	})
}

// A body read that fails is asked once; the retry button asks again.
func TestHTTPBodyFailureRequestedOnce(t *testing.T) {
	var mu sync.Mutex
	count := 0
	h := newHTTPHarness(t, 1440, 1300, enabled(), newHTTPFeed(inspectorFrame()), func(srv *testkit.Server) {
		srv.Route("GET", "/http/records/", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			count++
			mu.Unlock()
			http.Error(w, "boom", http.StatusInternalServerError)
		})
	})
	tt := h.tt
	testkit.UntilText(t, tt, "/json")
	h.click(t, "/json")
	testkit.UntilText(t, tt, "boom")
	for i := 0; i < 20; i++ {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	n := count
	mu.Unlock()
	if n != 1 {
		t.Fatalf("requests %d, want 1", n)
	}
	h.click(t, "重试")
	testkit.Until(t, tt, func() bool { mu.Lock(); defer mu.Unlock(); return count == 2 })
}

// JSON longer than the first chunk still shows as a tree; text that only
// starts like JSON falls back to text.
func TestHTTPLargeJSONTree(t *testing.T) {
	var big strings.Builder
	big.WriteString("[")
	for i := 0; i < 9000; i++ {
		if i > 0 {
			big.WriteString(",")
		}
		big.WriteString(`{"n":` + strconv.Itoa(i) + `}`)
	}
	big.WriteString("]")
	bad := "{" + strings.Repeat("x", 100<<10)
	if big.Len() <= httpBodyChunk {
		t.Fatal("fixture too small")
	}
	f := inspectorFrame()
	f.Events = f.Events[:2]
	f.Events[0].Record.ResponseBytes = int64(big.Len())
	f.Events[1].Record.ResponseBytes = int64(len(bad))
	f.Events[1].Record.ResponseHeaders = []api.HTTPHeader{{Name: "Content-Type", Value: "application/json"}}
	bodies := map[string]string{"21": big.String(), "22": bad}
	h := newHTTPHarness(t, 1440, 1300, enabled(), newHTTPFeed(f), func(srv *testkit.Server) {
		srv.Route("GET", "/http/records/", func(w http.ResponseWriter, r *http.Request) {
			id, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/v1/http/records/"), "/")
			data := bodies[id]
			if r.URL.Query().Get("side") == "request" {
				data = ""
			}
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			length, _ := strconv.Atoi(r.URL.Query().Get("length"))
			end := len(data)
			if length > 0 {
				end = min(end, offset+length)
			}
			hd := w.Header()
			hd.Set("X-Total", strconv.Itoa(len(data)))
			hd.Set("X-Saved", "true")
			hd.Set("X-Complete", strconv.FormatBool(end == len(data)))
			_, _ = w.Write([]byte(data[min(offset, len(data)):end]))
		})
	})
	tt := h.tt
	testkit.UntilText(t, tt, "/json")
	h.click(t, "/json")
	testkit.UntilText(t, tt, "[9000]")
	if !tt.HasText("树") {
		t.Fatalf("no tree: %q", tt.Texts())
	}
	h.click(t, "/text")
	testkit.Until(t, tt, func() bool { return tt.HasText("复制正文") })
	if tt.HasText("树") || h.w.http.insp.bodies[sideResponse].kind != bodyText {
		t.Fatalf("invalid JSON not shown as text: kind %v", h.w.http.insp.bodies[sideResponse].kind)
	}
}

// A connection's records are read again after a reset.
func TestHTTPConnectionReloadedAfterReset(t *testing.T) {
	var mu sync.Mutex
	count := 0
	feed := newHTTPFeed()
	h := newHTTPHarness(t, 1440, 820, enabled(), feed, func(srv *testkit.Server) {
		srv.Route("GET", "/http/records", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("connection_id") == "c2" {
				mu.Lock()
				count++
				mu.Unlock()
			}
			testkit.JSON(api.HTTPList{Instance: "i"})(w, r)
		})
	})
	h.w.Open(envConnection("c2"))
	testkit.Until(t, h.tt, func() bool { mu.Lock(); defer mu.Unlock(); return count == 1 })
	feed.ch <- httpFixture()
	testkit.Until(t, h.tt, func() bool { mu.Lock(); defer mu.Unlock(); return count == 2 })
}

// A selected record the store dropped is read from the core.
func TestHTTPSelectedRecordFetchedWhenDropped(t *testing.T) {
	feed := newHTTPFeed(httpFixture())
	h := newHTTPHarness(t, 1440, 820, enabled(), feed, func(srv *testkit.Server) {
		srv.Route("GET", "/http/records/11", func(w http.ResponseWriter, r *http.Request) {
			rec := hrec(11, "api.github.com", 200, "complete")
			rec.Path, rec.URL = "/user", "https://api.github.com/user"
			testkit.JSON(rec)(w, r)
		})
	})
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	h.click(t, "/user")
	testkit.UntilText(t, tt, "概览")
	// A reset without the record drops it from the list.
	feed.ch <- frame(true, 99)
	testkit.Until(t, tt, func() bool { return h.w.http.selectedRow() == nil && h.w.http.fetched != nil })
	if !tt.HasText("概览") || !hasProperty(tt, "URL", "https://api.github.com/user") {
		t.Fatalf("inspector lost: %q", tt.Texts())
	}
}

func envConnection(id string) env.ConnectionsView { return env.ConnectionsView{HTTPConnection: id} }

func TestHTTPFetchFailureClearsFetching(t *testing.T) {
	var mu sync.Mutex
	count := 0
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), func(srv *testkit.Server) {
		srv.Route("GET", "/http/records/", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			count++
			mu.Unlock()
			http.Error(w, "gone", http.StatusNotFound)
		})
	})
	m := h.w.http
	for round := 1; round <= 2; round++ {
		m.selected = "77"
		m.inspectedRow()
		testkit.Until(t, h.tt, func() bool { return m.selected == "" })
		if m.fetching != "" {
			t.Fatalf("round %d: fetching %q kept", round, m.fetching)
		}
		mu.Lock()
		n := count
		mu.Unlock()
		if n != round {
			t.Fatalf("round %d: %d requests", round, n)
		}
	}
}

func TestHTTPShowConnectionClearsSearchAndFilters(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), nil)
	m := h.w.http
	m.search, m.filter[hfHost] = "x", []string{"a.com"}
	m.showConnection("c9")
	if m.search != "" || len(m.filter) != 0 || m.conn != "c9" {
		t.Fatalf("search %q filter %v", m.search, m.filter)
	}
}

// A record read for a connection and later met in a page of the list
// joins the paging, so that loading earlier moves on.
func TestHTTPExtraJoinsPaging(t *testing.T) {
	s := newHTTPStore()
	s.apply(frame(true, 50, 51))
	var conn []api.HTTPRecord
	for id := uint64(1); id <= 500; id++ {
		conn = append(conn, hrec(id, "c.com", 200, "complete"))
	}
	s.mergeConnection(api.HTTPList{Instance: "i", Records: conn}, s.epoch)
	if s.oldest().ID != "50" {
		t.Fatalf("oldest %s", s.oldest().ID)
	}
	page := api.HTTPList{Instance: "i", Records: []api.HTTPRecord{hrec(40, "d.com", 200, "complete"), hrec(7, "c.com", 200, "complete")}}
	s.mergeList(page, s.epoch, 2, 10)
	if s.extra["7"] || !s.extra["8"] || s.oldest().ID != "7" {
		t.Fatalf("extra[7] %v oldest %s", s.extra["7"], s.oldest().ID)
	}
	s.trim(len(s.rows)-1, nil)
	s.dropFinished()
	if len(s.extra) != 0 {
		t.Fatalf("extra kept: %d", len(s.extra))
	}
}

func TestHTTPCurlDecodedBodyDropsEncoding(t *testing.T) {
	r := api.HTTPRecord{Method: "POST", URL: "https://a.com/", RequestHeaders: []api.HTTPHeader{{Name: "Content-Encoding", Value: "gzip"}, {Name: "X-A", Value: "1"}}}
	if got := httpCurlBody(&r, []byte("hi"), true); strings.Contains(got, "Content-Encoding") || !strings.Contains(got, "X-A") {
		t.Errorf("decoded: %s", got)
	}
	if got := httpCurlBody(&r, []byte("hi"), false); !strings.Contains(got, "Content-Encoding: gzip") {
		t.Errorf("raw: %s", got)
	}
	if got := httpCurlBody(&r, nil, true); !strings.Contains(got, "Content-Encoding: gzip") {
		t.Errorf("no body: %s", got)
	}
}

// An inspector over a narrow list hides the filters without changing
// their saved state; closing it brings them back.
func TestHTTPNarrowFiltersRestoredAfterInspector(t *testing.T) {
	h := newHTTPHarness(t, 760, 640, enabled(), newHTTPFeed(httpFixture()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	open := true
	h.w.http.sidebar = &open
	testkit.UntilText(t, tt, "状态码")
	// Selected by key, not by a press outside the filter panel (which
	// dismisses it).
	h.w.http.selectRow("11")
	testkit.Until(t, tt, func() bool { return tt.HasText("概览") && !tt.HasText("状态码") })
	if !*h.w.http.sidebar {
		t.Fatal("filter state lost")
	}
	h.click(t, "关闭详情")
	testkit.UntilText(t, tt, "状态码")
	var saved bool
	if h.env.Prefs.Load(prefHTTPSidebar, &saved) {
		t.Fatalf("filter state saved: %v", saved)
	}
}
