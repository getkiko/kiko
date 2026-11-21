package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeConnections struct {
	daemon.UnimplementedStartedServiceServer
	canceled  chan struct{}
	emit      chan *daemon.ConnectionEvents
	closed    atomic.Bool
	closedAll atomic.Bool
	interval  atomic.Int64
}

func (f *fakeConnections) SubscribeConnections(req *daemon.SubscribeConnectionsRequest, stream grpc.ServerStreamingServer[daemon.ConnectionEvents]) error {
	f.interval.Store(req.Interval)
	if err := stream.Send(&daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: "test-id", Connection: &daemon.Connection{Id: "test-id", Domain: "example.com", Source: "127.0.0.1:5050", UplinkTotal: 12}}}}); err != nil {
		return err
	}
	for {
		select {
		case <-stream.Context().Done():
			close(f.canceled)
			return nil
		case frame := <-f.emit:
			if err := stream.Send(frame); err != nil {
				return err
			}
		}
	}
}
func (f *fakeConnections) CloseConnection(_ context.Context, req *daemon.CloseConnectionRequest) (*emptypb.Empty, error) {
	if req.Id == "test-id" {
		f.closed.Store(true)
	}
	return &emptypb.Empty{}, nil
}
func (f *fakeConnections) CloseAllConnections(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	f.closedAll.Store(true)
	return &emptypb.Empty{}, nil
}

func TestConnectionsUseOfficialRPC(t *testing.T) {
	fake := &fakeConnections{canceled: make(chan struct{}), emit: make(chan *daemon.ConnectionEvents)}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + address, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.rpc.client = daemon.NewStartedServiceClient(conn)
	collectorCtx, stopCollector := context.WithCancel(context.Background())
	defer stopCollector()
	go s.collectConnectionHistory(collectorCtx, s.rpc.client, time.Now())
	deadline := time.Now().Add(2 * time.Second)
	for len(s.connections.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	page := queryPage(t, s, "")
	if page.Counts.All != 1 || len(page.Rows) != 1 || page.Rows[0].Domain != "example.com" || page.Rows[0].UplinkTotal != 12 {
		t.Fatalf("page %+v", page)
	}
	if fake.interval.Load() != int64(time.Second) {
		t.Fatalf("interval %d", fake.interval.Load())
	}
	select {
	case <-fake.canceled:
		t.Fatal("UI cancellation stopped background collection")
	default:
	}
	select {
	case fake.emit <- &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED, Id: "test-id", ClosedAt: time.Now().UnixMilli()}}}:
	case <-time.After(2 * time.Second):
		t.Fatal("background collector stopped")
	}
	deadline = time.Now().Add(2 * time.Second)
	for s.connections.snapshot()[0].GetClosedAt() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.connections.snapshot()[0].GetClosedAt() == 0 {
		t.Fatal("closed connection missing after UI left")
	}
	stopCollector()
	select {
	case <-fake.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("stream cancellation did not reach RPC")
	}
	closeOne := httptest.NewRecorder()
	oneReq := httptest.NewRequest(http.MethodDelete, "/api/v1/connections/test-id", nil)
	oneReq.SetPathValue("id", "test-id")
	s.closeConnection(closeOne, oneReq)
	if closeOne.Code != 204 || !fake.closed.Load() {
		t.Fatalf("close one status %d", closeOne.Code)
	}
	closeAll := httptest.NewRecorder()
	s.closeAllConnections(closeAll, httptest.NewRequest(http.MethodDelete, "/api/v1/connections", nil))
	if closeAll.Code != 204 || !fake.closedAll.Load() {
		t.Fatalf("close all status %d", closeAll.Code)
	}
}

func TestConnectionHistorySurvivesUISubscriptionsAndExportsAll(t *testing.T) {
	s := newGRPCTestServer(t)
	now := time.Now()
	for i := range 1205 {
		id := strconv.Itoa(i)
		conn := &daemon.Connection{Id: id, Domain: "example.com", CreatedAt: now.UnixMilli(), ClosedAt: now.Add(time.Second).UnixMilli(), UplinkTotal: int64(i)}
		s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: conn}}}, now)
	}
	// Every query answers the whole history at once, a page at a time.
	for range 2 {
		page := queryPage(t, s, "?limit=200&page=6")
		if page.Counts.All != 1205 || page.Counts.Closed != 1205 || page.Total != 1205 || page.Page != 6 || len(page.Rows) != 5 {
			t.Fatalf("page %+v", page.Counts)
		}
	}
	for _, format := range []string{"csv", "json"} {
		rec := httptest.NewRecorder()
		s.exportConnections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections/export?format="+format+"&page=999&search=missing", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status %d", format, rec.Code)
		}
		if format == "csv" {
			if got := len(strings.Split(strings.TrimSpace(rec.Body.String()), "\r\n")); got != 1206 {
				t.Fatalf("csv rows %d", got)
			}
		} else {
			var rows []json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1205 {
				t.Fatalf("json rows %d: %v", len(rows), err)
			}
		}
	}
}

func TestConnectionHistoryClearScopes(t *testing.T) {
	now := time.Now()
	newEvent := func(id string, closedAt int64) *daemon.ConnectionEvent {
		return &daemon.ConnectionEvent{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: &daemon.Connection{Id: id, CreatedAt: now.UnixMilli(), ClosedAt: closedAt}}
	}
	ids := func(rows []*daemon.Connection) string {
		var out []string
		for _, row := range rows {
			out = append(out, row.GetId())
		}
		return strings.Join(out, ",")
	}
	clear := func(s *server, body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/connections/history", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.clearConnectionHistory(rec, req)
		return rec.Code
	}
	seed := func() *server {
		s := newGRPCTestServer(t)
		s.connections.ingest("run", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{newEvent("a", 0), newEvent("b", 0), newEvent("c", now.UnixMilli())}}, now)
		return s
	}
	for _, tc := range []struct{ scope, left string }{{"all", ""}, {"active", "c"}, {"closed", "a,b"}} {
		s := seed()
		if code := clear(s, `{"scope":"`+tc.scope+`"}`); code != http.StatusNoContent {
			t.Fatalf("%s status %d", tc.scope, code)
		}
		if got := ids(s.connections.snapshot()); got != tc.left {
			t.Fatalf("%s left %q", tc.scope, got)
		}
	}
	s := seed()
	for _, body := range []string{`{"scope":"open"}`, `{}`, `not json`} {
		if code := clear(s, body); code != http.StatusBadRequest {
			t.Fatalf("%s status %d", body, code)
		}
	}

	// Clearing changes the revision that the ETag of queries follows.
	before := s.connections.revision
	clear(s, `{"scope":"active"}`)
	if s.connections.revision == before || ids(s.connections.snapshot()) != "c" {
		t.Fatal("clear kept the revision")
	}
	// Cleared active connections stay hidden, also through a resubscription
	// of the core; new connections keep arriving.
	s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "a", UplinkDelta: 5}, newEvent("d", 0)}}, now)
	s.connections.ingest("run", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{newEvent("a", 0), newEvent("d", 0)}}, now)
	if got := ids(s.connections.snapshot()); got != "c,d" {
		t.Fatalf("after updates %q", got)
	}
	// b was missing from that snapshot: it ended and leaves the tombstones.
	if _, ok := s.connections.cleared["b"]; ok {
		t.Fatal("tombstone of an ended connection kept")
	}
	// The close event drops the tombstone; a new run drops the rest.
	s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED, Id: "a", ClosedAt: now.UnixMilli()}}}, now)
	if _, ok := s.connections.cleared["a"]; ok || ids(s.connections.snapshot()) != "c,d" {
		t.Fatalf("close of a cleared connection: %v %q", s.connections.cleared, ids(s.connections.snapshot()))
	}
	clear(s, `{"scope":"active"}`)
	s.connections.ingest("next", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{newEvent("e", 0)}}, now)
	if len(s.connections.cleared) != 0 || ids(s.connections.snapshot()) != "c,e" {
		t.Fatalf("new run: %v %q", s.connections.cleared, ids(s.connections.snapshot()))
	}
}

// Closed records past the limit go, the earliest closed first, in batches;
// the limit is saved, and 0 keeps every record.
func TestConnectionHistoryClosedLimit(t *testing.T) {
	now := time.Now()
	s := newGRPCTestServer(t)
	s.loadConnectionSettings()
	if got := s.connections.getClosedLimit(); got != defaultClosedConnectionLimit {
		t.Fatalf("default limit %d", got)
	}
	settings := func(method, body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/connections/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if method == http.MethodGet {
			s.getConnectionSettings(rec, req)
		} else {
			s.setConnectionSettings(rec, req)
		}
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	if code, body := settings(http.MethodPatch, `{"closedLimit":20}`); code != http.StatusOK || body != `{"closedLimit":20}` {
		t.Fatalf("set: %d %s", code, body)
	}
	for _, body := range []string{`{"closedLimit":-1}`, `{"closedLimit":2000000}`, `nope`} {
		if code, _ := settings(http.MethodPatch, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, code)
		}
	}
	closeN := func(from, n int) {
		for i := from; i < from+n; i++ {
			id := fmt.Sprintf("c%03d", i)
			s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{
				Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id,
				Connection: &daemon.Connection{Id: id, CreatedAt: int64(i), ClosedAt: int64(1000 + i)},
			}}}, now)
		}
	}
	s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{
		Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: "open", Connection: &daemon.Connection{Id: "open"},
	}}}, now)
	closeN(0, 30) // 20 + slack 10: still all kept
	if got := len(s.connections.snapshot()); got != 31 {
		t.Fatalf("within the slack, %d records", got)
	}
	revision := s.connections.revision
	closeN(30, 1)
	rows := s.connections.snapshot()
	if len(rows) != 21 || rows[1].GetId() != "c011" {
		t.Fatalf("after the trim %d records from %s", len(rows), rows[1].GetId())
	}
	if s.connections.revision <= revision {
		t.Fatal("the trim kept the revision")
	}

	reloaded := &server{dataDir: s.dataDir}
	reloaded.loadConnectionSettings()
	if got := reloaded.connections.getClosedLimit(); got != 20 {
		t.Fatalf("reloaded limit %d", got)
	}
	settings(http.MethodPatch, `{"closedLimit":0}`)
	closeN(100, 200)
	if got := len(s.connections.snapshot()); got != 221 {
		t.Fatalf("unlimited, %d records", got)
	}
	if code, body := settings(http.MethodGet, ""); code != http.StatusOK || body != `{"closedLimit":0}` {
		t.Fatalf("get: %d %s", code, body)
	}
}

// testPage is the part of a query's answer the tests read.
type testPage struct {
	Revision string `json:"revision"`
	Counts   connCounts
	Totals   connCounts
	Facets   map[string][]connFacetOption
	Rows     []struct {
		ID          string `json:"id"`
		Domain      string `json:"domain"`
		UplinkTotal int64  `json:"uplinkTotal,string"`
		ClosedAt    int64  `json:"closedAt,string"`
		UpRate      float64
		CloseReason string
	}
	Groups []connGroupJSON
	Total  int
	Page   int
	// MatchedActive are the matched connections still open.
	MatchedActive int
}

func queryPage(t *testing.T, s *server, query string) testPage {
	t.Helper()
	rec := httptest.NewRecorder()
	s.queryConnections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("query %s: %d %s", query, rec.Code, rec.Body.String())
	}
	var page testPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// pid= keeps the connections of one process, the facets' too.
func TestConnectionQueryPID(t *testing.T) {
	s := newGRPCTestServer(t)
	now := time.Now()
	event := func(id string, pid uint32) *daemon.ConnectionEvent {
		conn := &daemon.Connection{Id: id, Domain: id + ".test", CreatedAt: now.UnixMilli(), Network: "tcp"}
		if pid != 0 {
			conn.ProcessInfo = &daemon.ProcessInfo{ProcessId: pid, ProcessPath: "/usr/bin/curl"}
		}
		return &daemon.ConnectionEvent{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: conn}
	}
	s.connections.ingest("one", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{event("a", 41), event("b", 42), event("c", 0)}}, now)
	page := queryPage(t, s, "?pid=42")
	if page.Total != 1 || page.Rows[0].ID != "b" || len(page.Facets["process"]) != 1 || page.Totals.All != 3 {
		t.Fatalf("pid %+v", page)
	}
	rec := httptest.NewRecorder()
	s.queryConnections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections?pid=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad pid: %d", rec.Code)
	}
}

// hideInbound leaves the connections of an inbound out of the rows, the
// facets and the totals, as the website icons' own.
func TestConnectionQueryHidesInbound(t *testing.T) {
	s := newGRPCTestServer(t)
	now := time.Now()
	event := func(id, inbound string) *daemon.ConnectionEvent {
		conn := &daemon.Connection{Id: id, Domain: id + ".test", CreatedAt: now.UnixMilli(), Network: "tcp", Inbound: inbound}
		return &daemon.ConnectionEvent{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: conn}
	}
	s.connections.ingest("one", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{event("a", "mixed-in"), event("b", "favicon"), event("c", "favicon")}}, now)
	if page := queryPage(t, s, ""); page.Total != 3 {
		t.Fatalf("all %+v", page)
	}
	page := queryPage(t, s, "?hideInbound=favicon")
	if page.Total != 1 || page.Rows[0].ID != "a" || page.Totals.All != 1 || len(page.Facets["inbound"]) != 1 {
		t.Fatalf("hidden %+v", page)
	}
}

// Rates come from the last update and lapse; a core restart closes the
// rows of the old run with its reason; queries answer 304 while unchanged
// and gzip large answers.
func TestConnectionQuery(t *testing.T) {
	s := newGRPCTestServer(t)
	now := time.Now()
	newEvent := func(id, domain string) *daemon.ConnectionEvent {
		return &daemon.ConnectionEvent{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: &daemon.Connection{Id: id, Domain: domain, CreatedAt: now.UnixMilli(), Network: "tcp", Inbound: "tun-in"}}
	}
	s.connections.ingest("one", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{newEvent("a", "a.test"), newEvent("b", "b.test")}}, now)
	s.connections.ingest("one", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "a", UplinkDelta: 300}}}, now)
	rows, _ := s.connections.queryRows(now.Add(time.Second))
	if rows[0].UpRate != 300 || rows[0].UpTotal != 300 {
		t.Fatalf("rate %+v", rows[0])
	}
	if rows, _ = s.connections.queryRows(now.Add(3 * time.Second)); rows[0].UpRate != 0 {
		t.Fatal("stale rate kept")
	}
	page := queryPage(t, s, "?search=A.TEST&sort=host&order=asc")
	if page.Total != 1 || page.Rows[0].ID != "a" || page.Counts.All != 1 || page.Totals.All != 2 || len(page.Facets["network"]) != 1 || page.Facets["network"][0].Count != 1 {
		t.Fatalf("search %+v", page)
	}
	if page := queryPage(t, s, "?group=app&expand=*"); len(page.Groups) != 1 || page.Groups[0].Count != 2 || len(page.Groups[0].Children) != 2 {
		t.Fatalf("groups %+v", page.Groups)
	}
	for _, bad := range []string{"?state=open", "?limit=201", "?limit=0", "?sort=nope", "?group=x", "?more=x"} {
		rec := httptest.NewRecorder()
		s.queryConnections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections"+bad, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	s.queryConnections(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	tag := rec.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	req.Header.Set("If-None-Match", tag)
	rec = httptest.NewRecorder()
	s.queryConnections(rec, req)
	if tag == "" || rec.Code != http.StatusNotModified {
		t.Fatalf("etag %q: %d", tag, rec.Code)
	}

	s.connections.ingest("two", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{newEvent("c", "c.test")}}, now)
	page = queryPage(t, s, "?state=closed&sort=host&order=asc")
	if page.Counts.Active != 1 || page.Counts.Closed != 2 || page.Rows[0].CloseReason != closeReasonCoreRestart || page.Rows[0].ClosedAt == 0 {
		t.Fatalf("restart %+v", page)
	}
	one := httptest.NewRequest(http.MethodGet, "/api/v1/connections/c", nil)
	one.SetPathValue("id", "c")
	rec = httptest.NewRecorder()
	s.getConnection(rec, one)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"run":"two"`) {
		t.Fatalf("one: %d %s", rec.Code, rec.Body.String())
	}
	missing := httptest.NewRequest(http.MethodGet, "/api/v1/connections/x", nil)
	missing.SetPathValue("id", "x")
	rec = httptest.NewRecorder()
	s.getConnection(rec, missing)
	if rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code)
	}

	for i := range 100 {
		id := strconv.Itoa(i)
		s.connections.ingest("two", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{newEvent(id, id+".example.com")}}, now)
	}
	gz := httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	gz.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	s.queryConnections(rec, gz)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("no gzip")
	}
}

// The decryption state arrives with the new connection, follows an update
// that carries the connection without touching the totals, and keeps the
// final values once closed; the inspector's answer holds them.
func TestConnectionHistoryMitmFields(t *testing.T) {
	s := newGRPCTestServer(t)
	now := time.Now()
	conn := &daemon.Connection{Id: "m", Domain: "example.com", Protocol: "tls", CreatedAt: now.UnixMilli(), UplinkTotal: 100, Mitm: "pending"}
	s.connections.ingest("run", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: "m", Connection: conn}}}, now)
	inspect := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/connections/m", nil)
		req.SetPathValue("id", "m")
		s.getConnection(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := inspect(); got["mitm"] != "pending" || got["httpRequests"] != nil || got["mitmError"] != nil {
		t.Fatalf("new %v", got)
	}
	// The connection of the update has stale totals: the deltas count.
	update := &daemon.Connection{Id: "m", Domain: "example.com", UplinkTotal: 1, Mitm: "decrypted", HttpRequests: 3}
	s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "m", UplinkDelta: 20, Connection: update}}}, now)
	got := inspect()
	if got["mitm"] != "decrypted" || got["httpRequests"] != "3" || got["uplinkTotal"] != "120" || got["upRate"] != float64(20) || got["protocol"] != "tls" {
		t.Fatalf("update %v", got)
	}
	// An update without the connection keeps the state.
	s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_UPDATE, Id: "m", UplinkDelta: 5}}}, now)
	if got := inspect(); got["mitm"] != "decrypted" || got["httpRequests"] != "3" || got["uplinkTotal"] != "125" {
		t.Fatalf("delta update %v", got)
	}
	final := &daemon.Connection{Id: "m", Domain: "example.com", Protocol: "tls", CreatedAt: now.UnixMilli(), UplinkTotal: 125, Mitm: "failed", HttpRequests: 4, MitmError: "remote error: tls: bad certificate", ClosedAt: now.UnixMilli()}
	s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_CLOSED, Id: "m", ClosedAt: now.UnixMilli(), Connection: final}}}, now)
	if got := inspect(); got["mitm"] != "failed" || got["httpRequests"] != "4" || got["mitmError"] != "remote error: tls: bad certificate" || got["closedAt"] == "0" {
		t.Fatalf("closed %v", got)
	}
	// The export keeps them too.
	if rows := s.connections.snapshot(); len(rows) != 1 || rows[0].GetMitm() != "failed" || rows[0].GetHttpRequests() != 4 {
		t.Fatalf("snapshot %v", rows)
	}
}

// A skipped row keeps the core's reason text unchanged.
func TestConnectionHistoryMitmSkippedReason(t *testing.T) {
	s := newGRPCTestServer(t)
	now := time.Now()
	reason := "certificate pinning: remote error: tls: bad certificate"
	conn := &daemon.Connection{Id: "k", Domain: "pin.example.com", Protocol: "tls", CreatedAt: now.UnixMilli(), Mitm: "skipped", MitmError: reason}
	s.connections.ingest("run", &daemon.ConnectionEvents{Reset_: true, Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: "k", Connection: conn}}}, now)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connections/k", nil)
	req.SetPathValue("id", "k")
	s.getConnection(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["mitm"] != "skipped" || out["mitmError"] != reason {
		t.Fatalf("skipped %v %v", out, err)
	}
}
