package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/protobuf/types/known/emptypb"
)

// recordingConnections remembers which connections the server closed.
type recordingConnections struct {
	daemon.UnimplementedStartedServiceServer
	mu        sync.Mutex
	closed    []string
	closedAll bool
}

func (f *recordingConnections) CloseConnection(_ context.Context, req *daemon.CloseConnectionRequest) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, req.Id)
	return &emptypb.Empty{}, nil
}

func (f *recordingConnections) CloseAllConnections(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedAll = true
	return &emptypb.Empty{}, nil
}

func (f *recordingConnections) result() ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	closed := slices.Clone(f.closed)
	slices.Sort(closed)
	return closed, f.closedAll
}

// closeFixture is a server whose history holds five connections: a, b and
// c carried by the route table rule rr-1, d by rr-2, e by rr-1 but closed.
func closeFixture(t *testing.T) (*server, *recordingConnections) {
	t.Helper()
	fake := &recordingConnections{}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + address, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := newGRPCTestServer(t)
	s.rpc.client = daemon.NewStartedServiceClient(conn)
	s.ruleMap.set([]ruleRef{
		{Index: 1, ID: "rr-1", Owner: "local", Section: "route", Ordinal: 1, Name: "one"},
		{Index: 2, ID: "rr-2", Owner: "local", Section: "route", Ordinal: 2, Name: "two"},
	})
	s.connections.resolveRule = s.ruleMap.resolve
	now := time.Now()
	add := func(id, domain, rule string, closed bool) {
		conn := &daemon.Connection{Id: id, Domain: domain, CreatedAt: now.UnixMilli(), Network: "tcp", Rule: rule}
		if closed {
			conn.ClosedAt = now.UnixMilli()
		}
		s.connections.ingest("run", &daemon.ConnectionEvents{Events: []*daemon.ConnectionEvent{{Type: daemon.ConnectionEventType_CONNECTION_EVENT_NEW, Id: id, Connection: conn}}}, now)
	}
	add("a", "a.example.com", "#1 domain=[x]", false)
	add("b", "b.example.com", "#1 domain=[x]", false)
	add("c", "c.other.net", "#1 domain=[x]", false)
	add("d", "d.example.com", "#2 domain=[y]", false)
	add("e", "e.example.com", "#1 domain=[x]", true)
	return s, fake
}

func closeMatching(t *testing.T, s *server, query, body string) (int, closeConnectionsResult) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/close"+query, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.closeMatchingConnections(rec, req)
	var result closeConnectionsResult
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, result
}

// Closing by search closes exactly what the Active list shows for it, and the
// list's matchedActive counts them.
func TestCloseMatchingConnectionsBySearch(t *testing.T) {
	s, fake := closeFixture(t)
	page := queryPage(t, s, "?state=all&search=example.com")
	if page.MatchedActive != 3 {
		t.Fatalf("matchedActive %d, page %+v", page.MatchedActive, page)
	}
	code, result := closeMatching(t, s, "?search=example.com", "")
	closed, all := fake.result()
	if code != http.StatusOK || result.Matched != 3 || result.Closed != 3 || all || !slices.Equal(closed, []string{"a", "b", "d"}) {
		t.Fatalf("status %d result %+v closed %v all %v", code, result, closed, all)
	}
}

// A rule limit closes the connections of that rule only, never a closed one.
func TestCloseMatchingConnectionsByRule(t *testing.T) {
	s, fake := closeFixture(t)
	code, result := closeMatching(t, s, "?rule=rr-1", "")
	closed, _ := fake.result()
	if code != http.StatusOK || result.Matched != 3 || !slices.Equal(closed, []string{"a", "b", "c"}) {
		t.Fatalf("status %d result %+v closed %v", code, result, closed)
	}
}

// Named ids close the active ones among them.
func TestCloseMatchingConnectionsByIDs(t *testing.T) {
	s, fake := closeFixture(t)
	code, result := closeMatching(t, s, "", `{"ids":["a","d","e","missing"]}`)
	closed, all := fake.result()
	if code != http.StatusOK || result.Matched != 2 || all || !slices.Equal(closed, []string{"a", "d"}) {
		t.Fatalf("status %d result %+v closed %v", code, result, closed)
	}
	for _, body := range []string{`{"ids":[]}`, `{"ids":null}`} {
		if code, result := closeMatching(t, s, "", body); code != http.StatusOK || result.Matched != 0 {
			t.Fatalf("%s: %d %+v", body, code, result)
		}
	}
	if _, all := fake.result(); all {
		t.Fatal("an empty id list closed everything")
	}
}

// Without any limit it is "Close all".
func TestCloseMatchingConnectionsWithoutLimitClosesAll(t *testing.T) {
	s, fake := closeFixture(t)
	if code, _ := closeMatching(t, s, "", ""); code != http.StatusBadRequest {
		t.Fatalf("a request without limits or all=1: status %d", code)
	}
	for _, body := range []string{"{}", `{"idz":["a"]}`, "null"} {
		if code, _ := closeMatching(t, s, "", body); code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d", body, code)
		}
	}
	if _, all := fake.result(); all {
		t.Fatal("a limitless request closed everything")
	}
	code, result := closeMatching(t, s, "?all=1", "")
	closed, all := fake.result()
	if code != http.StatusOK || result.Matched != 4 || result.Closed != 4 || !all || len(closed) != 0 {
		t.Fatalf("status %d result %+v closed %v all %v", code, result, closed, all)
	}
}

func TestCloseMatchingConnectionsRejectsBadQuery(t *testing.T) {
	s, _ := closeFixture(t)
	if code, _ := closeMatching(t, s, "?pid=x", ""); code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
}
