package connections

import (
	"net/http"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// Deleting a finished record asks first, then asks the core and drops the
// row; the reset the core answers with is not reported as a gap.
func TestHTTPMonitorDeleteRecord(t *testing.T) {
	feed := newHTTPFeed(httpFixture())
	fail := false
	h := newHTTPHarness(t, 1440, 820, enabled(), feed, func(srv *testkit.Server) {
		srv.Route("DELETE", "/http/records/11", func(w http.ResponseWriter, r *http.Request) {
			if fail {
				http.Error(w, `{"error":"磁盘忙"}`, http.StatusConflict)
				return
			}
			testkit.JSON(map[string]any{"status": api.HTTPStatus{}})(w, r)
		})
	})
	tt := h.tt
	testkit.UntilText(t, tt, "/user")
	h.click(t, "/user")
	testkit.UntilText(t, tt, "概览")
	h.click(t, "删除")
	testkit.UntilText(t, tt, "删除此请求？")
	savePreview(t, tt, "http-delete-confirm-light.png")
	tt.SetDark(true)
	tt.Advance(time.Second)
	savePreview(t, tt, "http-delete-confirm-dark.png")
	tt.SetDark(false)
	if h.requested("DELETE /api/v1/http/records/11") {
		t.Fatal("deleted without confirming")
	}
	fail = true
	h.click(t, "删除请求")
	testkit.UntilText(t, tt, "磁盘忙")
	if !tt.HasText("/user") || !tt.HasText("重试") {
		t.Fatalf("failure must keep the row and offer a retry: %q", tt.Texts())
	}
	fail = false
	h.click(t, "重试")
	testkit.Until(t, tt, func() bool { return !tt.HasText("/user") })
	if !h.requested("DELETE /api/v1/http/records/11?instance=i") {
		t.Fatalf("requests %q", h.srv.Requests())
	}
	if !tt.HasText("/v1/chat") || h.w.http.selected != "" {
		t.Fatalf("after delete: %q", tt.Texts())
	}
	// The core answers with a reset; no gap notice for it.
	reset := httpFixture()
	reset.Events = reset.Events[1:]
	reset.Gap, reset.Seq, reset.Floor = true, 20, 20
	feed.ch <- reset
	testkit.Until(t, tt, func() bool { return h.w.http.store.instance == "i" && len(h.w.http.store.rows) == 3 })
	if tt.HasText("部分记录已被核心淘汰") {
		t.Fatalf("gap shown for a deletion: %q", tt.Texts())
	}
}

// A running record cannot be deleted.
func TestHTTPMonitorDeleteDisabledWhileRunning(t *testing.T) {
	h := newHTTPHarness(t, 1440, 820, enabled(), newHTTPFeed(httpFixture()), nil)
	tt := h.tt
	testkit.UntilText(t, tt, "/stream")
	h.click(t, "/stream")
	testkit.UntilText(t, tt, "概览")
	_ = h.tt.Click("删除")
	if tt.HasText("删除此请求？") {
		t.Fatal("confirmation opened for a running record")
	}
}
