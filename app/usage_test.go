package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bolt "github.com/sagernet/bbolt"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
)

func usageConnection(id, source, host string, up, down int64) *daemon.ConnectionEvent {
	return &daemon.ConnectionEvent{Id: id, Connection: &daemon.Connection{Id: id, Source: source + ":5000", Destination: "203.0.113.1:443", Domain: host, Network: "tcp", Protocol: "tls", Inbound: "tun", Outbound: "proxy", ChainList: []string{"proxy", "node"}, Rule: "domain_suffix=example.com", UplinkTotal: up, DownlinkTotal: down}}
}

func usageIngest(t *testing.T, u *usageStore, run string, now time.Time, reset bool, events ...*daemon.ConnectionEvent) {
	t.Helper()
	if err := u.ingest(run, &daemon.ConnectionEvents{Reset_: reset, Events: events}, now); err != nil {
		t.Fatal(err)
	}
}

func usageRead(t *testing.T, u *usageStore, now time.Time) usageResponse {
	t.Helper()
	value, err := u.query(usageQuery{From: now.Add(-time.Hour).UnixMilli(), To: now.Add(time.Hour).UnixMilli(), Group: "source", Filters: map[string][]string{}, Sort: "total", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUsageReconciliationAndPersistence(t *testing.T) {
	dir := t.TempDir()
	u := newUsageStore(dir)
	now := time.Now().Truncate(time.Minute)
	oldClosed := usageConnection("history", "192.0.2.2", "a.example.com", 9999, 9999)
	oldClosed.Connection.ClosedAt = now.Add(-time.Minute).UnixMilli()
	usageIngest(t, u, "run1", now, true, usageConnection("a", "192.0.2.1", "a.example.com", 100, 200), oldClosed)
	if got := usageRead(t, u, now).Totals; got != (usageCounters{Connections: 1}) {
		t.Fatalf("initial baseline: %+v", got)
	}
	usageIngest(t, u, "run1", now.Add(time.Second), false, &daemon.ConnectionEvent{Id: "a", UplinkDelta: 10, DownlinkDelta: 20})
	// A reset supplies cumulative totals for the same connection, including a gap.
	usageIngest(t, u, "run1", now.Add(2*time.Second), true, usageConnection("a", "192.0.2.1", "a.example.com", 120, 240), oldClosed)
	closed := usageConnection("a", "192.0.2.1", "a.example.com", 130, 260)
	closed.Type = daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED
	closed.Connection.ClosedAt = now.Add(3 * time.Second).UnixMilli()
	usageIngest(t, u, "run1", now.Add(3*time.Second), false, closed, closed)
	usageIngest(t, u, "run1", now.Add(4*time.Second), true, closed, oldClosed)
	if got := usageRead(t, u, now).Totals; got != (usageCounters{Upload: 30, Download: 60, Connections: 1}) {
		t.Fatalf("close/reset double counted: %+v", got)
	}
	// The database contains both the minute deltas and the cumulative checkpoint.
	u = newUsageStore(dir)
	usageIngest(t, u, "run1", now.Add(5*time.Second), true, closed)
	usageIngest(t, u, "run2", now.Add(6*time.Second), false, usageConnection("a", "192.0.2.1", "a.example.com", 7, 11))
	if got := usageRead(t, u, now).Totals; got != (usageCounters{Upload: 37, Download: 71, Connections: 2}) {
		t.Fatalf("reopen/run isolation: %+v", got)
	}
	// Clearing preserves the active baseline and survives reopening the store.
	if err := u.configure(0, true); err != nil {
		t.Fatal(err)
	}
	u = newUsageStore(dir)
	usageIngest(t, u, "run2", now.Add(7*time.Second), true, usageConnection("a", "192.0.2.1", "a.example.com", 10, 16))
	if got := usageRead(t, u, now).Totals; got != (usageCounters{Upload: 3, Download: 5}) {
		t.Fatalf("clear/reopen continuation: %+v", got)
	}
}

func TestUsageActiveReconnectAndMinuteAttribution(t *testing.T) {
	dir := t.TempDir()
	u := newUsageStore(dir)
	now := time.Now().Truncate(time.Minute)
	usageIngest(t, u, "r", now, false, usageConnection("a", "192.0.2.1", "cdn.example.co.uk.", 10, 20))
	if err := u.flush(); err != nil {
		t.Fatal(err)
	}
	u = newUsageStore(dir)
	usageIngest(t, u, "r", now.Add(time.Minute), true, usageConnection("a", "192.0.2.1", "cdn.example.co.uk.", 30, 50))
	q := usageQuery{From: now.Add(time.Minute).UnixMilli(), To: now.Add(2 * time.Minute).UnixMilli(), Group: "rootDomain", Secondary: "rule", Filters: map[string][]string{}, Sort: "total"}
	value, err := u.query(q)
	if err != nil || value.Totals != (usageCounters{Upload: 20, Download: 30}) || len(value.Rows) != 1 || value.Rows[0].Labels[0] != "example.co.uk" || value.Rows[0].Labels[1] != "domain_suffix=example.com" {
		t.Fatalf("minute attribution / domains / rule: %+v %v", value, err)
	}
	var trend usageCounters
	for _, point := range value.Trend {
		trend.add(point.usageCounters)
	}
	if trend != value.Totals {
		t.Fatalf("trend totals: %+v != %+v", trend, value.Totals)
	}
}

func TestUsageReconnectNewConnections(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-process", true: "reopened-store"}[reopen], func(t *testing.T) {
			dir := t.TempDir()
			u := newUsageStore(dir)
			now := time.Now().Truncate(time.Minute)
			// An empty snapshot still establishes a durable observation boundary.
			usageIngest(t, u, "r", now, true)
			if reopen {
				if err := u.flush(); err != nil {
					t.Fatal(err)
				}
				u = newUsageStore(dir)
			}
			closed := usageConnection("gap-closed", "192.0.2.1", "closed.example", 1234, 5678)
			closed.Connection.CreatedAt = now.Add(time.Second).UnixMilli()
			closed.Connection.ClosedAt = now.Add(2 * time.Second).UnixMilli()
			active := usageConnection("gap-active", "192.0.2.2", "active.example", 100, 200)
			active.Connection.CreatedAt = now.Add(time.Second).UnixMilli()
			old := usageConnection("old", "192.0.2.3", "old.example", 9999, 9999)
			old.Connection.CreatedAt = now.Add(-time.Hour).UnixMilli()
			old.Connection.ClosedAt = now.Add(-time.Minute).UnixMilli()
			usageIngest(t, u, "r", now.Add(3*time.Second), true, closed, active, old)
			want := usageCounters{Upload: 1334, Download: 5878, Connections: 2}
			if got := usageRead(t, u, now).Totals; got != want {
				t.Fatalf("new connections during reconnect: got %+v, want %+v", got, want)
			}
			usageIngest(t, u, "r", now.Add(4*time.Second), true, closed, active, old)
			// Even after a closed checkpoint is pruned, an old replay stays historical.
			delete(u.checkpoints, "r:gap-closed")
			usageIngest(t, u, "r", now.Add(5*time.Second), true, closed, active, old)
			if got := usageRead(t, u, now).Totals; got != want {
				t.Fatalf("replayed closed connection: got %+v, want %+v", got, want)
			}
			usageIngest(t, u, "next-run", now.Add(6*time.Second), true, closed, active)
			want.Connections++ // The new core run establishes its own active baseline.
			if got := usageRead(t, u, now).Totals; got != want {
				t.Fatalf("new run baseline: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestUsageReconnectAfterClear(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "observation", true: "legacy-checkpoints"}[legacy], func(t *testing.T) {
			dir := t.TempDir()
			u := newUsageStore(dir)
			now := time.Now().Add(-time.Minute).Truncate(time.Minute)
			usageIngest(t, u, "r", now, true, usageConnection("existing", "192.0.2.1", "example.com", 100, 200))
			beforeClear := usageConnection("before-clear", "192.0.2.1", "example.com", 100, 200)
			beforeClear.Connection.CreatedAt = now.Add(time.Second).UnixMilli()
			beforeClear.Connection.ClosedAt = now.Add(2 * time.Second).UnixMilli()
			if err := u.configure(0, true); err != nil {
				t.Fatal(err)
			}
			if legacy {
				usageLegacyObservation(t, u)
			}
			u = newUsageStore(dir)
			// Clearing advances the boundary for connections first seen on a later reset.
			usageIngest(t, u, "r", time.Now().Add(time.Second), true, beforeClear)
			if got := usageRead(t, u, now).Totals; got != (usageCounters{}) {
				t.Fatalf("closed connection from before history clear: %+v", got)
			}
		})
	}
}

func usageLegacyObservation(t *testing.T, u *usageStore) {
	t.Helper()
	db, err := u.database()
	if err != nil {
		t.Fatal(err)
	}
	// Original v1 databases stored checkpoints without an observation record.
	err = db.Update(func(tx *bolt.Tx) error { return tx.Bucket(usageStateBucket).Delete([]byte("observation")) })
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsageReconnectLegacyCheckpoints(t *testing.T) {
	dir := t.TempDir()
	u := newUsageStore(dir)
	now := time.Now()
	usageIngest(t, u, "r", now, true, usageConnection("existing", "192.0.2.1", "example.com", 100, 200))
	if err := u.flush(); err != nil {
		t.Fatal(err)
	}
	usageLegacyObservation(t, u)
	u = newUsageStore(dir)
	closed := usageConnection("gap", "192.0.2.2", "example.com", 1234, 5678)
	closed.Connection.CreatedAt = now.Add(time.Second).UnixMilli()
	closed.Connection.ClosedAt = now.Add(2 * time.Second).UnixMilli()
	usageIngest(t, u, "r", now.Add(3*time.Second), true, usageConnection("existing", "192.0.2.1", "example.com", 110, 220), closed)
	if got := usageRead(t, u, now).Totals; got != (usageCounters{Upload: 1244, Download: 5698, Connections: 2}) {
		t.Fatalf("legacy checkpoints and new gap connection: %+v", got)
	}
}

func TestUsageGroupingFilteringSortingAndFullHistory(t *testing.T) {
	dir := t.TempDir()
	u := newUsageStore(dir)
	now := time.Now().Truncate(time.Minute)
	usageIngest(t, u, "r", now.Add(-48*time.Hour), false, usageConnection("old", "192.0.2.9", "old.example", 999, 999))
	usageIngest(t, u, "r", now, false,
		usageConnection("a", "192.0.2.1", "a.example.com", 10, 50),
		usageConnection("b", "192.0.2.1", "b.example.com", 20, 100),
		usageConnection("c", "192.0.2.2", "a.example.com", 100, 200))
	u.meta.RetentionDays = 1 // Simulate a database created by the previous retention policy.
	u.dirty = true
	if err := u.flush(); err != nil {
		t.Fatal(err)
	}
	u = newUsageStore(dir)
	if err := u.configure(0, false); err != nil {
		t.Fatal(err)
	}
	q := usageQuery{From: now.Add(-72 * time.Hour).UnixMilli(), To: now.Add(time.Minute).UnixMilli(), Group: "source", Secondary: "host", Filters: map[string][]string{}, Sort: "download", Limit: 1, Page: 1}
	value, err := u.query(q)
	if err != nil || value.Groups != 4 || len(value.Rows) != 1 || value.Rows[0].Labels[1] != "a.example.com" || value.Totals != (usageCounters{Upload: 1129, Download: 1349, Connections: 4}) || value.EvictedBuckets != 0 {
		t.Fatalf("dual grouping / page / full history: %+v %v", value, err)
	}
	q.Filters = map[string][]string{"source": {"192.0.2.1"}, "network": {"TCP"}}
	q.Group, q.Secondary, q.Page, q.Limit, q.Search = "host", "", 0, 50, "B.EXAMPLE"
	value, err = u.query(q)
	if err != nil || value.Groups != 1 || value.Totals.Download != 100 || value.Facets["source"][0].Value != "192.0.2.1" {
		t.Fatalf("intersection / search / facet: %+v %v", value, err)
	}
	q.Filters, q.Search, q.Group, q.Sort, q.Ascending = nil, "", "source", "total", true
	value, err = u.query(q)
	if err != nil || value.Rows[0].Labels[0] != "192.0.2.1" || value.Rows[0].Connections != 2 {
		t.Fatalf("ascending / aggregate: %+v %v", value, err)
	}
}

func TestUsagePendingRetainsEveryDistinctRecord(t *testing.T) {
	u := newUsageStore(t.TempDir())
	now := time.Now().Truncate(time.Minute)
	for index := range 20005 {
		dimensions := map[string]string{"source": fmt.Sprintf("device-%d", index)}
		u.addLocked(now, dimensions, usageCounters{Upload: 1})
	}
	if len(u.pending) != 20005 {
		t.Fatalf("pending records %d", len(u.pending))
	}
	if err := u.flush(); err != nil {
		t.Fatal(err)
	}
	value := usageRead(t, u, now)
	if value.Totals.Upload != 20005 || value.Groups != 20005 {
		t.Fatalf("persisted total=%d groups=%d", value.Totals.Upload, value.Groups)
	}
}

func TestUsageHTTPAndIntegerPrecision(t *testing.T) {
	s := newGRPCTestServer(t)
	s.usage = newUsageStore(s.dataDir)
	now := time.Now()
	large := int64(9007199254740993)
	usageIngest(t, s.usage, "r", now, false, usageConnection("a", "192.0.2.1", "=SUM(A1)", large, 10))
	rec := httptest.NewRecorder()
	s.getUsage(rec, httptest.NewRequest("GET", "/api/v1/usage?group=host", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"upload":"9007199254740993"`) {
		t.Fatalf("precision: %d %s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"group=bad", "group=source&secondary=source", "from=foo", "from=2&to=1", "limit=0", "limit=201", "page=-1", "sort=oops", "filter.host=" + strings.Repeat("a", 8193)} {
		rec = httptest.NewRecorder()
		s.getUsage(rec, httptest.NewRequest("GET", "/api/v1/usage?"+query, nil))
		if rec.Code != 400 {
			t.Errorf("query %s: %d", query[:min(len(query), 40)], rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	s.getUsage(rec, httptest.NewRequest("GET", "/api/v1/usage?from=0&to=999999999999&group=host", nil))
	if rec.Code != 200 {
		t.Fatalf("full history range: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.exportUsage(rec, httptest.NewRequest("GET", "/api/v1/usage/export?group=host&limit=1&page=4", nil))
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(rec.Body.String(), "\ufeff"))).ReadAll()
	if err != nil || rec.Code != 200 || len(rows) != 2 || rows[1][0] != "'=sum(a1)" || rows[1][1] != "9007199254740993" {
		t.Fatalf("CSV: %v %v", rows, err)
	}
	rec = postJSON(s.configureUsage, "PATCH", `{"retentionDays":0}`)
	if rec.Code != 200 || s.usage.meta.RetentionDays != 0 {
		t.Fatal(rec.Body.String())
	}
	if rec = postJSON(s.configureUsage, "PATCH", `{"retentionDays":7}`); rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	s.clearUsage(rec, httptest.NewRequest("DELETE", "/api/v1/usage/history", nil))
	if rec.Code != 204 || usageRead(t, s.usage, now).Groups != 0 {
		t.Fatal("clear history", rec.Code)
	}
}

func TestMetricsHistoryAvailabilityAndBound(t *testing.T) {
	s := newGRPCTestServer(t)
	s.usage = newUsageStore(s.dataDir)
	for i := range 305 {
		s.recordMetrics(runtimeState{State: "running", MetricsAvailable: true, UploadRate: uint64(i), MemoryBytes: 4096, ActiveConnections: 3}, time.Unix(int64(i), 0))
	}
	s.recordMetrics(runtimeState{State: "stopped"}, time.Unix(305, 0))
	rec := httptest.NewRecorder()
	s.metricsHistory(rec, httptest.NewRequest("GET", "/api/v1/metrics/history", nil))
	var response struct{ Points []metricPoint }
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response.Points) != 300 || response.Points[0].Time != 6000 || response.Points[299].Upload != nil || *response.Points[298].Upload != 304 {
		t.Fatalf("ring / missing samples: %+v %v", response, err)
	}
}

type fakeUsageService struct {
	daemon.UnimplementedStartedServiceServer
	canceled chan struct{}
}

func (f *fakeUsageService) SubscribeConnections(req *daemon.SubscribeConnectionsRequest, stream grpc.ServerStreamingServer[daemon.ConnectionEvents]) error {
	if req.GetInterval() != int64(time.Second) {
		return context.Canceled
	}
	for _, frame := range []*daemon.ConnectionEvents{
		{Reset_: true, Events: []*daemon.ConnectionEvent{usageConnection("a", "192.0.2.1", "a.example.com", 100, 200)}},
		{Events: []*daemon.ConnectionEvent{{Id: "a", UplinkDelta: 11, DownlinkDelta: 22}}},
	} {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	close(f.canceled)
	return nil
}

func TestUsageOfficialCollectorWithoutBrowser(t *testing.T) {
	fake := &fakeUsageService{canceled: make(chan struct{})}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + address, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.usage = newUsageStore(s.dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.collectUsageConnections(ctx, daemon.NewStartedServiceClient(conn), time.Now())
	}()
	eventually(t, "background connection deltas", func() bool {
		s.usage.mu.Lock()
		defer s.usage.mu.Unlock()
		for _, record := range s.usage.pending {
			if record.Upload == 11 && record.Download == 22 {
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not finish")
	}
	select {
	case <-fake.canceled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach official stream")
	}
	if got := usageRead(t, newUsageStore(s.dataDir), time.Now()); got.Totals != (usageCounters{Upload: 11, Download: 22, Connections: 1}) || s.usage.connected {
		t.Fatalf("shutdown persistence: %+v", got)
	}
}
