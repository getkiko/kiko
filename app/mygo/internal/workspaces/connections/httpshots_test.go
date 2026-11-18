package connections

import (
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// longFrame adds a request with a very long URL, error and header value to
// the inspector fixture.
func longFrame() api.HTTPFrame {
	f := inspectorFrame()
	r := hrec(30, "tracking.very-long-subdomain-name.example-cdn.com", 502, api.HTTPStateFailed)
	query := strings.Repeat("utm_campaign=autumn-sale&utm_source=newsletter&", 6)
	r.Path = "/v2/collect/events/batch/2026/11/03/segment/user-profile-sync"
	r.URL = "https://" + r.Host + r.Path + "?" + query + "token=0123456789abcdef"
	r.Error = "upstream: tls: failed to verify certificate: x509: certificate signed by unknown authority (possibly because of crypto/rsa: verification error while trying to verify candidate authority certificate)"
	r.RequestHeaders = []api.HTTPHeader{{Name: "X-Request-Context", Value: strings.Repeat("eyJhbGciOiJIUzI1NiJ9.", 12)}}
	r.Timing = &api.HTTPTiming{Start: 1_000_000_000, End: 1_400_000_000}
	r.Unverified = true
	f.Events = append(f.Events, api.HTTPEvent{Kind: api.HTTPEventNew, Record: &r})
	f.Seq = 30
	return f
}

// The acceptance screenshots of review round 23: they are written when
// CONN_PREVIEW_DIR is set.
func TestHTTPAcceptanceScreens(t *testing.T) {
	requests := new([]string)
	h := newHTTPHarness(t, 1440, 900, enabled(), newHTTPFeed(longFrame()), bodyRoute(requests))
	tt := h.tt
	testkit.UntilText(t, tt, "/json")
	savePreview(t, tt, "01-list-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "02-list-dark.png")
	tt.SetDark(false)

	// Detailed mode and filters.
	h.click(t, "切换 HTTP 列表密度")
	testkit.UntilText(t, tt, "upstream: tls: failed")
	savePreview(t, tt, "03-list-detailed-light.png")
	h.click(t, "切换 HTTP 列表密度")
	h.click(t, "失败")
	testkit.UntilText(t, tt, "显示 1 / 8 条请求")
	savePreview(t, tt, "04-filtered-failed-light.png")
	h.click(t, "清除")
	testkit.UntilText(t, tt, "/json")

	// Selected row with the inspector, long values.
	h.click(t, "/v2/collect/events/batch/2026/11/03/segment/user-profile-sync")
	testkit.UntilText(t, tt, "概览")
	savePreview(t, tt, "05-selected-long-url-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "06-selected-long-url-dark.png")
	tt.SetDark(false)
	h.click(t, "关闭详情")
	time.Sleep(10 * time.Millisecond)
}

func TestHTTPAcceptanceEmpty(t *testing.T) {
	for _, c := range []struct {
		name string
		caps api.HTTPCapabilities
		want string
	}{
		{"07-empty-unsupported", api.HTTPCapabilities{Reason: "core too old"}, "当前核心不支持 HTTP 请求"},
		{"08-empty-disabled", api.HTTPCapabilities{Supported: true}, "HTTPS 解密未启用"},
		{"09-empty-none", enabled(), "暂无 HTTP 请求"},
	} {
		h := newHTTPHarness(t, 1440, 700, c.caps, newHTTPFeed(), nil)
		testkit.UntilText(t, h.tt, c.want)
		savePreview(t, h.tt, c.name+"-light.png")
		h.tt.SetDark(true)
		h.tt.Frame()
		savePreview(t, h.tt, c.name+"-dark.png")
	}
}

func TestHTTPAcceptanceBodies(t *testing.T) {
	for _, dark := range []bool{false, true} {
		suffix := "-light.png"
		if dark {
			suffix = "-dark.png"
		}
		h, _ := newInspectorHarness(t, 1440, 1500)
		tt := h.tt
		tt.SetDark(dark)
		for _, c := range []struct{ row, want, name string }{
			{"/json", "login: \"octocat\"", "10-body-json"},
			{"/image", "40 × 20", "11-body-image"},
			{"/binary", "仅显示前 4.0 KB", "12-body-binary"},
			{"/big", "正文已截断，保存 2.0 MB / 3.0 MB", "13-body-truncated"},
			{"/unsaved", "未保存正文", "14-body-unsaved"},
		} {
			h.click(t, c.row)
			testkit.UntilText(t, tt, c.want)
			savePreview(t, tt, c.name+suffix)
		}
	}
}

func TestHTTPAcceptanceNarrow(t *testing.T) {
	h := newHTTPHarness(t, 760, 760, enabled(), newHTTPFeed(longFrame()), bodyRoute(new([]string)))
	tt := h.tt
	testkit.UntilText(t, tt, "/json")
	savePreview(t, tt, "15-narrow-list-light.png")
	h.click(t, "切换 HTTP 筛选")
	testkit.UntilText(t, tt, "状态码")
	savePreview(t, tt, "16-narrow-filters-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "17-narrow-filters-dark.png")
	tt.SetDark(false)
	tt.Frame()
	h.w.http.sidebar = new(bool)
	h.click(t, "/json")
	testkit.UntilText(t, tt, "概览")
	testkit.UntilText(t, tt, "login: \"octocat\"")
	savePreview(t, tt, "18-narrow-inspector-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "19-narrow-inspector-dark.png")
}

// A feed of 1000 records a second for ten seconds reaches the list
// without piling up: the store applies a batch in one pass, and the list
// rebuilds once per interval.
func TestHTTPStreamThroughput(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(), nil)
	m := h.w.http
	m.filter[hfStatus] = []string{"2xx"}
	next := uint64(1)
	var apply, derive time.Duration
	batches := 0
	for sec := 0; sec < 10; sec++ {
		// Ten batches of 100 records per second, as the stream delivers them.
		for b := 0; b < 10; b++ {
			var f api.HTTPFrame
			f.Instance = "i"
			for i := 0; i < 100; i++ {
				r := hrec(next, "host"+string(rune('a'+int(next)%20))+".com", 200, api.HTTPStateComplete)
				f.Events = append(f.Events, api.HTTPEvent{Kind: api.HTTPEventNew, Record: &r})
				next++
			}
			f.Seq = api.Uint64(next - 1)
			start := time.Now()
			m.applyFrames([]api.HTTPFrame{f})
			apply += time.Since(start)
			start = time.Now()
			m.derivedAt = time.Time{}
			d := m.derive()
			derive += time.Since(start)
			if len(d.rows) != len(m.store.rows) {
				t.Fatalf("rows %d of %d", len(d.rows), len(m.store.rows))
			}
			batches++
		}
	}
	if len(m.store.rows) != httpLimit || m.store.evicted != 10000-httpLimit {
		t.Fatalf("rows %d evicted %d", len(m.store.rows), m.store.evicted)
	}
	t.Logf("10000 records in %d batches: apply %v, derive %v (%.2f ms per rebuild)", batches, apply, derive, float64(derive.Microseconds())/1000/float64(batches))
	if apply+derive > 3*time.Second {
		t.Fatalf("too slow: apply %v derive %v", apply, derive)
	}
}
