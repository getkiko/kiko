package connections

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// Older queries that load while the history is cleared do not come back
// into the cleared history.
func TestDNSHistoryPageAfterClearIsDropped(t *testing.T) {
	srv := testkit.NewServer(t)
	release := make(chan struct{})
	srv.Route("GET", "/dns/queries", func(w http.ResponseWriter, r *http.Request) {
		<-release
		testkit.JSON(api.DNSView{Instance: "a", Seq: 1, Queries: []api.DNSQuery{{ID: "old", Seq: 1, Revision: 1}}})(w, r)
	})
	srv.Route("DELETE", "/dns/history", testkit.JSON(api.DNSView{Instance: "a", Seq: 4}))
	e, _ := testkit.NewEnv(srv)
	m := newDNSMonitor(e, newIconCache(e))
	tt := ui.NewTester(func(c *ui.Context) { e.Drain(c) }, 100, 100)
	m.applyLive(fixtureDNS())
	m.loadHistory()
	cleared := false
	m.clear(func(err error) { cleared = err == nil })
	testkit.Until(t, tt, func() bool { return cleared })
	close(release)
	testkit.Until(t, tt, func() bool { return !m.loading })
	if _, ok := m.live.queries["old"]; ok || len(m.live.queries) != 0 {
		t.Errorf("cleared history has %d queries", len(m.live.queries))
	}
}

// A pending query's detail stops refreshing once the page hides.
func TestDNSDetailStopsRefreshingWhenHidden(t *testing.T) {
	srv := testkit.NewServer(t)
	var mu sync.Mutex
	requests := 0
	srv.Route("GET", "/dns/queries/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		testkit.JSON(api.DNSQuery{ID: "q", Status: "pending"})(w, r)
	})
	count := func() int { mu.Lock(); defer mu.Unlock(); return requests }
	e, _ := testkit.NewEnv(srv)
	m := newDNSMonitor(e, newIconCache(e))
	tt := ui.NewTester(func(c *ui.Context) { e.Drain(c) }, 100, 100)
	m.active = true
	m.detailFor = "q\x00a"
	m.loadDetail("q", "a")
	testkit.Until(t, tt, func() bool { return count() >= 2 })
	m.setActive(false)
	if m.detailFor != "" {
		t.Error("detail not reloaded when the page shows again")
	}
	tt.Frame()
	seen := count()
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if count() > seen+1 {
		t.Errorf("detail refreshed %d times while hidden", count()-seen)
	}
}
