package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestScriptStatusDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/scripts/status" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"scripts":[{"tag":"a","type":"http-response","runs":"9007199254740993","errors":"2","timeouts":"0","limits":"0","skipped":"1","paused":true,"paused_until":1790000000000,"last_error":"boom","last_run_ms":1789999999000,"last_duration_us":1500}],"store_persistent":true,"store_bytes":"120","store_limit":"1048576"}`)
	}))
	defer srv.Close()
	st, err := New(srv.URL).ScriptStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := st.Scripts[0]
	if s.Runs != 9007199254740993 || s.Errors != 2 || s.Skipped != 1 || !s.Paused || s.PausedUntil != 1790000000000 || s.LastError != "boom" ||
		s.LastRunMS != 1789999999000 || s.LastDurationUS != 1500 || !st.StorePersistent || st.StoreBytes != 120 || st.StoreLimit != 1048576 {
		t.Fatalf("status: %+v", st)
	}
}

// Bodies cross as base64 both ways; has_body separates an empty body from
// none, and omitted fields stay omitted.
func TestScriptEvaluateRoundTrip(t *testing.T) {
	var got map[string]any
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/scripts/evaluate" {
			http.NotFound(w, r)
			return
		}
		raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		fmt.Fprint(w, `{"action":"modify","duration_us":420,"logs":[{"level":"info","message":"hi","time_ms":5}],`+
			`"notifications":[{"title":"t","time_ms":6}],`+
			`"changes":[{"kind":"header","op":"add","name":"X-A","after":"1","has_after":true},{"kind":"header","op":"remove","name":"X-B","before":"2","had_before":true}],`+
			`"url":"https://e/2","headers_changed":true,"headers":[{"name":"X-A","value":"1"}],"body_changed":true,"body":"AAEC/yE=","status":203,`+
			`"response":{"status":404,"headers":[{"name":"A","value":"b"}],"body":"","has_body":true},"output":"{}"}`)
	}))
	defer srv.Close()
	res, err := New(srv.URL).ScriptEvaluate(context.Background(), ScriptEvalRequest{
		Source: "x", Type: ScriptTypeRequest, Pattern: "^https://", RequiresBody: true, BinaryBody: true,
		Request:  &ScriptRequestMessage{URL: "https://e/1", Method: "POST", Headers: []ScriptHeader{{"A", "b"}}, Body: []byte{0, 1, 2, 0xff, '!'}, HasBody: true},
		Response: &ScriptResponseMessage{Status: 200, HasBody: true},
		Store:    []ScriptStoreEntry{{"k", "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := got["request"].(map[string]any)
	if req["body"] != "AAEC/yE=" || req["has_body"] != true || req["url"] != "https://e/1" || req["method"] != "POST" {
		t.Fatalf("request sent: %s", raw)
	}
	// An empty body with has_body keeps has_body only.
	resp := got["response"].(map[string]any)
	if _, ok := resp["body"]; ok || resp["has_body"] != true || resp["status"] != float64(200) {
		t.Fatalf("response sent: %s", raw)
	}
	for _, absent := range []string{"timeout_ms", "max_size", "argument", "tag"} {
		if _, ok := got[absent]; ok {
			t.Fatalf("%s must be omitted: %s", absent, raw)
		}
	}
	if got["requires_body"] != true || got["binary_body"] != true || got["pattern"] != "^https://" {
		t.Fatalf("flags sent: %s", raw)
	}
	if !bytes.Equal(res.Body, []byte{0, 1, 2, 0xff, '!'}) || !res.BodyChanged || res.Action != ScriptActionModify || res.DurationUS != 420 || res.Status != 203 {
		t.Fatalf("result: %+v", res)
	}
	if len(res.Changes) != 2 || !res.Changes[0].HasAfter || res.Changes[0].After != "1" || !res.Changes[1].HadBefore || res.Changes[1].Before != "2" {
		t.Fatalf("changes: %+v", res.Changes)
	}
	if res.Response == nil || res.Response.Status != 404 || !res.Response.HasBody || len(res.Response.Body) != 0 || res.Response.Headers[0].Name != "A" {
		t.Fatalf("response: %+v", res.Response)
	}
	if res.URL != "https://e/2" || !res.HeadersChanged || res.Headers[0].Value != "1" || res.Logs[0].Message != "hi" || res.Notifications[0].Title != "t" || res.Output != "{}" {
		t.Fatalf("result: %+v", res)
	}
}

func TestScriptEvaluateErrorsAndUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("namespace") == "bad" {
			http.Error(w, `{"error":"x"}`, 400)
			return
		}
		http.Error(w, `{"error":"脚本等待核心连接"}`, 503)
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.ScriptEvaluate(context.Background(), ScriptEvalRequest{Source: "x", Type: ScriptTypeGeneric})
	if !IsScriptUnavailable(err) || err.Error() != "脚本等待核心连接" {
		t.Fatalf("evaluate error: %v", err)
	}
	if _, err = c.ScriptStatus(context.Background()); !IsScriptUnavailable(err) {
		t.Fatalf("status error: %v", err)
	}
	_, err = c.ScriptStoreList(context.Background(), "bad")
	if err == nil || IsScriptUnavailable(err) || !IsStatus(err, 400) {
		t.Fatalf("400 must not read as unavailable: %v", err)
	}
}

func TestScriptRunRequest(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = r.Method + " " + r.URL.Path + " " + string(raw)
		fmt.Fprint(w, `{"action":"none","duration_us":7,"logs":[{"level":"log","message":"m","time_ms":1}],"output":"ok"}`)
	}))
	defer srv.Close()
	res, err := New(srv.URL).ScriptRun(context.Background(), "cron-1", "a=1")
	if err != nil || res.Action != "none" || res.DurationUS != 7 || res.Output != "ok" || res.Logs[0].Message != "m" {
		t.Fatalf("run: %+v %v", res, err)
	}
	if want := `POST /api/v1/scripts/run {"tag":"cron-1","argument":"a=1"}`; body != want {
		t.Fatalf("sent %q, want %q", body, want)
	}
	if _, err = New(srv.URL).ScriptRun(context.Background(), "t", ""); err != nil || body != `POST /api/v1/scripts/run {"tag":"t"}` {
		t.Fatalf("sent %q %v", body, err)
	}
}

func TestScriptStoreRequests(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(raw))
		mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/scripts/store":
			fmt.Fprint(w, `{"namespaces":[{"name":"n","entries":2,"bytes":"18446744073709551615"}],"entries":[{"key":"k","value":"vv","size":4096,"truncated":true}],"info":{"persistent":true,"bytes":"9","limit":"100"}}`)
		case "GET /api/v1/scripts/store/value":
			fmt.Fprint(w, `{"namespace":"n","key":"k","value":"v","found":true}`)
		case "PUT /api/v1/scripts/store/value":
			fmt.Fprint(w, `{"persistent":false,"bytes":"10","limit":"100"}`)
		case "DELETE /api/v1/scripts/store/value":
			fmt.Fprint(w, `null`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	list, err := c.ScriptStoreList(ctx, "n")
	if err != nil || len(list.Namespaces) != 1 || list.Namespaces[0].Bytes != 18446744073709551615 || list.Namespaces[0].Entries != 2 ||
		!list.Entries[0].Truncated || list.Entries[0].Size != 4096 || list.Info == nil || list.Info.Bytes != 9 || !list.Info.Persistent {
		t.Fatalf("list: %+v %v", list, err)
	}
	v, err := c.ScriptStoreGet(ctx, "n", "a b")
	if err != nil || !v.Found || v.Value != "v" {
		t.Fatalf("get: %+v %v", v, err)
	}
	info, err := c.ScriptStoreSet(ctx, "n", "k", "新值")
	if err != nil || info.Bytes != 10 || info.Limit != 100 {
		t.Fatalf("set: %+v %v", info, err)
	}
	info, err = c.ScriptStoreDelete(ctx, "n", "k")
	if err != nil || info != (ScriptStoreInfo{}) {
		t.Fatalf("delete with null info: %+v %v", info, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/scripts/store?namespace=n ",
		"GET /api/v1/scripts/store/value?key=a+b&namespace=n ",
		`PUT /api/v1/scripts/store/value {"namespace":"n","key":"k","value":"新值"}`,
		"DELETE /api/v1/scripts/store/value?key=k&namespace=n {}",
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("requests:\n%q\nwant\n%q", seen, want)
	}
}

// The log stream resumes from the last entry after the connection ends,
// restarts from 0 after a 503 (the core restarted and numbers from 1), and
// carries the tag.
func TestStreamScriptLogsReconnectsAndRestarts(t *testing.T) {
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
			fmt.Fprint(w, `{"seq":"1","script":"a","phase":"http-request","level":"log","message":"one","time_ms":1}`+"\n"+`{"seq":"9007199254740993","script":"a","level":"warn","message":"two","time_ms":2}`+"\n")
		case 2:
			http.Error(w, `{"error":"脚本等待核心连接"}`, 503)
		case 3:
			// The restarted core numbers from 1 again.
			fmt.Fprint(w, `{"seq":"1","script":"a","level":"log","message":"fresh","time_ms":3}`+"\n")
		default:
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entries := make(chan ScriptLogEntry, 8)
	done := make(chan error, 1)
	go func() {
		done <- New(srv.URL).StreamScriptLogs(ctx, 0, "a", func(e ScriptLogEntry) { entries <- e })
	}()
	var got []ScriptLogEntry
	for len(got) < 3 {
		select {
		case e := <-entries:
			got = append(got, e)
		case <-ctx.Done():
			t.Fatalf("entries: %+v", got)
		}
	}
	if got[0].Message != "one" || got[0].Phase != "http-request" || got[1].Seq != 9007199254740993 || got[1].Level != "warn" || got[2].Message != "fresh" || got[2].Seq != 1 {
		t.Fatalf("entries: %+v", got)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		n := len(queries)
		mu.Unlock()
		if n >= 4 {
			break
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("stream ended with %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"after=0&tag=a", "after=9007199254740993&tag=a", "after=0&tag=a", "after=1&tag=a"}
	if len(queries) < 4 {
		t.Fatalf("queries: %q", queries)
	}
	for i, w := range want {
		if queries[i] != w {
			t.Fatalf("query %d = %q, want %q (all %q)", i, queries[i], w, queries)
		}
	}
}

// After a 503 the next connection asks from 0 so that a restarted core's
// entries (numbered from 1) are not skipped.
func TestStreamScriptLogsAfter503AsksFromZero(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	queries := make(chan string, 8)
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.RawQuery
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		if call == 1 {
			http.Error(w, `{"error":"x"}`, 503)
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go New(srv.URL).StreamScriptLogs(ctx, 40, "", func(ScriptLogEntry) {})
	first, second := <-queries, <-queries
	cancel()
	if first != "after=40" || second != "after=0" {
		t.Fatalf("queries %q then %q", first, second)
	}
}

func TestStreamScriptNotifications(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	queries := make(chan string, 8)
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.Path + "?" + r.URL.RawQuery
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		if call == 1 {
			fmt.Fprint(w, `{"seq":"3","script":"a","title":"标题","body":"b","url":"https://e","time_ms":9}`+"\n")
			return
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan ScriptNotification, 2)
	go New(srv.URL).StreamScriptNotifications(ctx, 2, func(n ScriptNotification) { got <- n })
	note := <-got
	if note.Seq != 3 || note.Title != "标题" || note.URL != "https://e" || note.TimeMS != 9 {
		t.Fatalf("notification: %+v", note)
	}
	if first, second := <-queries, <-queries; first != "/api/v1/scripts/notifications?after=2" || second != "/api/v1/scripts/notifications?after=3" {
		t.Fatalf("queries %q then %q", first, second)
	}
	cancel()
}

// A revoked token ends the stream with that error.
func TestStreamScriptLogsFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no"}`, 401)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := New(srv.URL).StreamScriptLogs(ctx, 0, "", func(ScriptLogEntry) {})
	if !Fatal(err) {
		t.Fatalf("stream ended with %v", err)
	}
}

// Evaluate and run have no overall timeout of their own: a server slower
// than the ordinary request limit still answers; ordinary requests keep
// the limit, and ctx stops both.
func TestScriptEvaluateHasNoOverallTimeout(t *testing.T) {
	defer func(d time.Duration) { requestTimeout = d }(requestTimeout)
	requestTimeout = 50 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"action":"none","duration_us":1}`)
	}))
	defer srv.Close()
	c := New(srv.URL)
	if _, err := c.ScriptEvaluate(context.Background(), ScriptEvalRequest{Source: "x", Type: ScriptTypeGeneric}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if _, err := c.ScriptRun(context.Background(), "t", ""); err != nil {
		t.Fatalf("run: %v", err)
	}
	var v ScriptStatus
	if err := c.Get(context.Background(), "/scripts/status", &v); err == nil {
		t.Fatal("an ordinary request outlived the limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.ScriptEvaluate(ctx, ScriptEvalRequest{Source: "x", Type: ScriptTypeGeneric}); err == nil || time.Since(start) > 150*time.Millisecond {
		t.Fatalf("ctx did not stop the run: %v after %v", err, time.Since(start))
	}
}

// The state callback reports a failed connection, then an established one.
func TestStreamScriptLogsStateCallback(t *testing.T) {
	defer func(min, max time.Duration) { eventsBackoff, eventsMaxBackoff = min, max }(eventsBackoff, eventsMaxBackoff)
	eventsBackoff, eventsMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		if call == 1 {
			http.Error(w, `{"error":"x"}`, 503)
			return
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	states := make(chan bool, 8)
	go New(srv.URL).StreamScriptLogsState(ctx, 0, "", func(ScriptLogEntry) {}, func(ok bool) { states <- ok })
	if first, second := <-states, <-states; first || !second {
		t.Fatalf("states %v then %v", first, second)
	}
}

// A server that sends nothing, not even headers, counts as connected once
// the request has stayed up for scriptSettle; a failure before that does
// not.
func TestStreamScriptLogsStateSilentServer(t *testing.T) {
	defer func(d time.Duration) { scriptSettle = d }(scriptSettle)
	scriptSettle = 30 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	states := make(chan bool, 8)
	go New(srv.URL).StreamScriptLogsState(ctx, 0, "", func(ScriptLogEntry) {}, func(ok bool) { states <- ok })
	if ok := <-states; !ok {
		t.Fatal("a silent open stream is not connected")
	}
}

func TestScriptStatusNextRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"scripts":[{"tag":"tick","type":"cron","runs":"0","errors":"0","timeouts":"0","limits":"0","skipped":"0","next_run_ms":1790000060000},{"tag":"g","type":"generic","runs":"1","errors":"0","timeouts":"0","limits":"0","skipped":"0"}],"store_persistent":true,"store_bytes":"0","store_limit":"1"}`)
	}))
	defer srv.Close()
	st, err := New(srv.URL).ScriptStatus(context.Background())
	if err != nil || len(st.Scripts) != 2 || st.Scripts[0].Type != ScriptTypeCron || st.Scripts[0].NextRunMS != 1790000060000 || st.Scripts[1].NextRunMS != 0 {
		t.Fatalf("status: %+v %v", st, err)
	}
}
