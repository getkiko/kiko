package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHTTPRecordDecodes(t *testing.T) {
	var frame HTTPFrame
	text := `{"instance":"i","seq":"9007199254740993","floor":"3","reset":true,"gap":true,"events":[{"kind":"new","record":{"id":"7","seq":"7","created_seq":"7","method":"GET","host":"a.com","path":"/","url":"https://a.com/","proto":"h2","status":200,"state":"complete","request_headers":[{"name":"A","value":"b"}],"response_body_truncated":true,"rewritten":true,"timing":{"start":5,"dns":2},"connection":{"id":"c","process_id":4}}}],"status":{"instance":"i","total":"12","record_count":3}}`
	if err := json.Unmarshal([]byte(text), &frame); err != nil {
		t.Fatal(err)
	}
	r := frame.Events[0].Record
	if frame.Seq != 9007199254740993 || frame.Floor != 3 || !frame.Reset || !frame.Gap || frame.Status.Total != 12 || frame.Status.RecordCount != 3 {
		t.Fatalf("frame: %+v", frame)
	}
	if r.ID != "7" || r.Seq != 7 || !r.ResponseBodyTrunc || !r.Rewritten || r.Timing.DNS != 2 || r.Connection.ProcessID != 4 || r.RequestHeaders[0].Value != "b" {
		t.Fatalf("record: %+v", r)
	}
}

func TestHTTPRecordDecodesRewrites(t *testing.T) {
	text := `{"id":"1","state":"complete","rewritten":true,"rewrites":[{"kind":"url","rule":"u1","action":"302","target":"https://b.com/"},{"kind":"header","rule":"h1","action":"replace","side":"request","changes":[{"op":"replace","name":"X-A","before":"1","had_before":true,"after":"2","has_after":true},{"op":"add","name":"X-B","after":"","has_after":true}]},{"kind":"map_local","rule":"m1","status":404}]}`
	var r HTTPRecord
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Rewritten || len(r.Rewrites) != 3 || r.Rewrites[0].Target != "https://b.com/" || r.Rewrites[2].Status != 404 {
		t.Fatalf("rewrites: %+v", r.Rewrites)
	}
	c := r.Rewrites[1].Changes
	if len(c) != 2 || !c[0].HadBefore || c[0].Before != "1" || !c[0].HasAfter || c[0].After != "2" || c[1].HadBefore || !c[1].HasAfter || r.Rewrites[1].Side != "request" {
		t.Fatalf("changes: %+v", c)
	}
	var caps HTTPCapabilities
	if err := json.Unmarshal([]byte(`{"supported":true,"rewrite":true}`), &caps); err != nil || !caps.Rewrite {
		t.Fatalf("caps: %+v %v", caps, err)
	}
}

func TestMitmPlainIsNotIntercepted(t *testing.T) {
	var c Connection
	if err := json.Unmarshal([]byte(`{"mitm":"plain"}`), &c); err != nil || c.Mitm != MitmPlain || c.Intercepted() {
		t.Fatalf("plain: %+v %v", c, err)
	}
}

func TestHTTPCaptureRequests(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/api/v1/http/capabilities":
			fmt.Fprint(w, `{"supported":true,"enabled":false,"reason":"x"}`)
		case "/api/v1/http/records":
			fmt.Fprint(w, `{"instance":"i","records":[{"id":"5"}],"next_before":"5"}`)
		case "/api/v1/http/records/a%2Fb":
			http.Error(w, `{"error":"record not found"}`, 404)
		case "/api/v1/http/records/5/body":
			h := w.Header()
			h.Set("X-Total", "5")
			h.Set("X-Original-Size", "9")
			h.Set("X-Offset", "2")
			h.Set("X-Truncated", "true")
			h.Set("X-Encoding", "gzip")
			h.Set("X-Saved", "true")
			fmt.Fprint(w, "hello")
		case "/api/v1/http/clear":
			fmt.Fprint(w, `{"status":{"instance":"i","total":"4"}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if v, err := c.HTTPCapabilities(ctx); err != nil || !v.Supported || v.Enabled || v.Reason != "x" {
		t.Fatalf("capabilities: %+v %v", v, err)
	}
	list, err := c.ListHTTPRecords(ctx, HTTPQuery{Search: "a b", Host: "h", ConnectionID: "c", Limit: 50, Before: "9"})
	if err != nil || len(list.Records) != 1 || list.NextBefore != "5" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err = c.GetHTTPRecord(ctx, "a/b", ""); !IsStatus(err, 404) || err.Error() != "record not found" {
		t.Fatalf("record: %v", err)
	}
	body, err := c.HTTPBody(ctx, "5", HTTPSideResponse, 2, 5, true)
	if err != nil || string(body.Data) != "hello" || body.Total != 5 || body.OriginalSize != 9 || body.Offset != 2 || !body.Truncated || !body.Saved || body.Complete || body.Encoding != "gzip" {
		t.Fatalf("body: %+v %v", body, err)
	}
	if st, err := c.ClearHTTP(ctx); err != nil || st.Total != 4 {
		t.Fatalf("clear: %+v %v", st, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/http/capabilities",
		"GET /api/v1/http/records?before=9&connection_id=c&host=h&limit=50&search=a+b",
		"GET /api/v1/http/records/a%2Fb",
		"GET /api/v1/http/records/5/body?decoded=true&length=5&offset=2&side=response",
		"POST /api/v1/http/clear",
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("requests:\n%q\nwant\n%q", seen, want)
	}
}

// The stream resumes from the last frame after the connection ends or the
// core answers 503, and follows a reset under a new instance.
func TestStreamHTTPReconnectsAndResumes(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		n := len(queries)
		mu.Unlock()
		switch n {
		case 1:
			fmt.Fprint(w, `{"instance":"i1","seq":"4","floor":"1","events":[]}`+"\n"+`{"instance":"i1","seq":"9007199254740993","floor":"1","events":[{"kind":"new","record":{"id":"2"}}]}`+"\n")
		case 2:
			http.Error(w, `{"error":"HTTP 抓取等待核心连接"}`, 503)
		case 3:
			// The core restarted: a reset under a new instance.
			fmt.Fprint(w, `{"instance":"i2","seq":"2","floor":"1","reset":true,"events":[]}`+"\n")
		default:
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames := make(chan HTTPFrame, 8)
	done := make(chan error, 1)
	go func() { done <- New(srv.URL).StreamHTTP(ctx, 0, "", func(f HTTPFrame) { frames <- f }) }()
	var got []HTTPFrame
	for len(got) < 3 {
		select {
		case f := <-frames:
			got = append(got, f)
		case <-ctx.Done():
			t.Fatalf("frames: %+v", got)
		}
	}
	if got[1].Seq != 9007199254740993 || got[1].Events[0].Record.ID != "2" || !got[2].Reset || got[2].Instance != "i2" {
		t.Fatalf("frames: %+v", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(queries)
		mu.Unlock()
		if n >= 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("stream ended with %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"after=0", "after=9007199254740993&instance=i1", "after=9007199254740993&instance=i1", "after=2&instance=i2"}
	if len(queries) < 4 {
		t.Fatalf("queries: %q", queries)
	}
	for i, w := range want {
		if queries[i] != w {
			t.Fatalf("query %d = %q, want %q (all %q)", i, queries[i], w, queries)
		}
	}
}

// A reset snapshot split into batches leaves Seq 0 on the middle frame; the
// reconnect uses the last frame's Seq and resent records apply by id.
func TestStreamHTTPResumesAfterSplitReset(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		n := len(queries)
		mu.Unlock()
		switch n {
		case 1:
			fmt.Fprint(w, `{"instance":"i1","seq":"5","floor":"1","reset":true,"events":[{"kind":"new","record":{"id":"7"}}]}`+"\n"+`{"instance":"i1","seq":"0","floor":"1","events":[{"kind":"new","record":{"id":"6"}}]}`+"\n")
		case 2:
			fmt.Fprint(w, `{"instance":"i1","seq":"6","floor":"1","events":[{"kind":"new","record":{"id":"7"}},{"kind":"new","record":{"id":"6"}}]}`+"\n")
		default:
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	byID := map[string]bool{}
	var last Uint64
	var hmu sync.Mutex
	frames := make(chan HTTPFrame, 8)
	done := make(chan error, 1)
	go func() {
		done <- New(srv.URL).StreamHTTP(ctx, 0, "", func(f HTTPFrame) {
			hmu.Lock()
			for _, e := range f.Events {
				byID[e.Record.ID] = true
			}
			last = f.Seq
			hmu.Unlock()
			frames <- f
		})
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-frames:
		case <-ctx.Done():
			t.Fatal("frames not received")
		}
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		n := len(queries)
		mu.Unlock()
		if n >= 3 {
			break
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("stream ended with %v", err)
	}
	hmu.Lock()
	if len(byID) != 2 || !byID["6"] || !byID["7"] || last != 6 {
		t.Fatalf("records %v last %d", byID, last)
	}
	hmu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	want := []string{"after=0", "after=0&instance=i1", "after=6&instance=i1"}
	if len(queries) < 3 {
		t.Fatalf("queries: %q", queries)
	}
	for i, w := range want {
		if queries[i] != w {
			t.Fatalf("query %d = %q, want %q (all %q)", i, queries[i], w, queries)
		}
	}
}

// A refused cursor starts over from a snapshot.
func TestStreamHTTPRestartsAfterBadRequest(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	queries := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.RawQuery
		if r.URL.Query().Get("instance") != "" {
			http.Error(w, `{"error":"resume cursor lost"}`, 400)
			return
		}
		fmt.Fprint(w, `{"instance":"fresh","seq":"1","floor":"1","reset":true,"events":[]}`+"\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames := make(chan HTTPFrame, 2)
	go New(srv.URL).StreamHTTP(ctx, 5, "stale", func(f HTTPFrame) { frames <- f })
	select {
	case f := <-frames:
		if f.Instance != "fresh" || !f.Reset {
			t.Fatalf("frame: %+v", f)
		}
	case <-ctx.Done():
		t.Fatal("no snapshot after a refused cursor")
	}
	if first := <-queries; first != "after=5&instance=stale" {
		t.Fatalf("first query %q", first)
	}
}

func TestStreamHTTPStopsOnRevokedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "", http.StatusUnauthorized)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := New(srv.URL).StreamHTTP(ctx, 0, "", func(HTTPFrame) {})
	if !Fatal(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestDNSQueryKeepsRewritten(t *testing.T) {
	var q DNSQuery
	if err := json.Unmarshal([]byte(`{"id":"1","rewritten":"dns-rewrite","questions":[]}`), &q); err != nil || q.Rewritten != "dns-rewrite" {
		t.Fatalf("query: %+v %v", q, err)
	}
}

func TestReplayAndHARRequests(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(raw))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/http/replay":
			var req ReplayRequest
			_ = json.Unmarshal(raw, &req)
			switch req.URL {
			case "https://bad.example/":
				fmt.Fprint(w, `{"record":{"id":"9","state":"failed","error":"dial tcp: refused"},"error":"dial tcp: refused"}`)
			case "https://invalid/":
				http.Error(w, `{"error":"url: missing host"}`, 400)
			default:
				fmt.Fprint(w, `{"record":{"id":"8","state":"complete","status":200,"connection":{"id":"c","inbound":"kiko-internal"}},"error":""}`)
			}
		case "/api/v1/http/har":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"log":{"version":"1.2"`)
			w.(http.Flusher).Flush()
			fmt.Fprint(w, `,"entries":[]}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	rec, err := c.ReplayHTTP(ctx, ReplayRequest{SourceID: "5", Method: "POST", URL: "https://ok.example/", Body: []byte("hi"), Outbound: "out", VerifyTLS: true})
	if err != nil || rec.ID != "8" || rec.Connection.Inbound != ReplayInbound {
		t.Fatalf("replay: %+v %v", rec, err)
	}
	rec, err = c.ReplayHTTP(ctx, ReplayRequest{Method: "GET", URL: "https://bad.example/"})
	if err == nil || err.Error() != "dial tcp: refused" || rec.ID != "9" || rec.State != HTTPStateFailed {
		t.Fatalf("failed replay: %+v %v", rec, err)
	}
	if _, err = c.ReplayHTTP(ctx, ReplayRequest{Method: "GET", URL: "https://invalid/"}); !IsStatus(err, 400) || err.Error() != "url: missing host" {
		t.Fatalf("invalid replay: %v", err)
	}
	var out bytes.Buffer
	if err = c.ExportHAR(ctx, HARRequest{Filter: HARFilter{Host: "a.com"}, Bodies: true, Sanitize: HARSanitize{AuthHeaders: true}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"log":{"version":"1.2","entries":[]}}` {
		t.Fatalf("har %q", out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if bodies[0] != `POST /api/v1/http/replay {"source_id":"5","method":"POST","url":"https://ok.example/","headers":[],"body":"aGk=","outbound":"out","verify_tls":true}` {
		t.Errorf("replay body %q", bodies[0])
	}
	if bodies[3] != `POST /api/v1/http/har {"ids":[],"filter":{"search":"","host":"a.com","connection_id":""},"bodies":true,"sanitize":{"auth_headers":true,"cookies":false,"query_secrets":false,"bodies":false}}` {
		t.Errorf("har body %q", bodies[3])
	}
}

// A cancelled export stops and reports the cancellation.
func TestExportHARCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "{")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	w := &cancelWriter{cancel: cancel}
	err := New(srv.URL).ExportHAR(ctx, HARRequest{}, w)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

type cancelWriter struct{ cancel func() }

func (w *cancelWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }
