package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestHTTPCapturePersistRequests(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/http/capabilities":
			fmt.Fprint(w, `{"supported":true,"enabled":true,"persist":true,"status":{"persist_enabled":true,"persist_records":12,"persist_bytes":2048,"persist_written":"30","persist_dropped":"2","persist_errors":"1"}}`)
		case "/api/v1/http/persist/purge":
			fmt.Fprint(w, `{"status":{"persist_enabled":true}}`)
		default:
			if r.Method == http.MethodDelete && r.URL.EscapedPath() == "/api/v1/http/records/a%2Fb" {
				fmt.Fprint(w, `{"status":{"persist_records":11,"persist_bytes":1024}}`)
				return
			}
			http.Error(w, `{"error":"record not found"}`, 404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	caps, err := c.HTTPCapabilities(ctx)
	st := caps.Status
	if err != nil || !caps.Persist || st == nil || !st.PersistEnabled || st.PersistRecords != 12 || st.PersistBytes != 2048 || st.PersistWritten != 30 || st.PersistDropped != 2 || st.PersistErrors != 1 {
		t.Fatalf("capabilities: %+v %v", caps, err)
	}
	if got, err := c.DeleteHTTPRecord(ctx, "a/b", "inst"); err != nil || got.PersistRecords != 11 {
		t.Fatalf("delete: %+v %v", got, err)
	}
	if _, err = c.DeleteHTTPRecord(ctx, "gone", ""); !IsStatus(err, 404) {
		t.Fatalf("missing: %v", err)
	}
	if got, err := c.PurgeHTTPPersisted(ctx); err != nil || !got.PersistEnabled {
		t.Fatalf("purge: %+v %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/http/capabilities",
		"DELETE /api/v1/http/records/a%2Fb?instance=inst",
		"DELETE /api/v1/http/records/gone",
		"POST /api/v1/http/persist/purge",
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("requests %v", seen)
	}
}
