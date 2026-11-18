package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Closing without any limit says so: the server refuses a close that merely
// lost its limits.
func TestCloseConnectionsWithoutLimitsAsksForAll(t *testing.T) {
	var urls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urls = append(urls, r.Method+" "+r.URL.RequestURI())
		_, _ = w.Write([]byte(`{"matched":2,"closed":2}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	for _, q := range []ConnectionQuery{{}, {State: "active", Sort: "created", Page: 3}, {Search: "x"}, {Rule: "rr-1"}, {Filter: map[string][]string{"process": {"p"}}}} {
		if _, err := c.CloseConnections(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"POST /api/v1/connections/close?all=1", "POST /api/v1/connections/close?all=1",
		"POST /api/v1/connections/close?search=x", "POST /api/v1/connections/close?rule=rr-1", "POST /api/v1/connections/close?filter.process=p"}
	for i, w := range want {
		if urls[i] != w {
			t.Errorf("request %d: %s, want %s", i, urls[i], w)
		}
	}
}
