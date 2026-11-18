package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuleSetUpdateAndSource(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		switch r.URL.Path {
		case "/api/v1/rulesets/status":
			_, _ = w.Write([]byte(`{"ruleSets":[{"tag":"geo","lastUpdatedAt":1000,"type":"remote","lastError":"timeout"},{"tag":"mine","lastUpdatedAt":0,"type":"inline"}]}`))
		case "/api/v1/rulesets/update":
			var q struct{ Tag string }
			_ = json.Unmarshal(body, &q)
			if q.Tag == "broken" {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"unexpected status: 500"}`))
				return
			}
			_, _ = w.Write([]byte(`{"tag":"geo","lastUpdatedAt":2000,"type":"remote"}`))
		case "/api/v1/rulesets/source":
			_, _ = w.Write([]byte(`{"version":3,"rules":[{"domain":["a.test"]},{"type":"logical","mode":"or","rules":[]}],"from":"download"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	statuses, err := c.RuleSetStatus(ctx)
	if err != nil || len(statuses) != 2 || statuses[0] != (RuleSetStatus{Tag: "geo", LastUpdatedAt: 1000, Type: "remote", LastError: "timeout"}) || statuses[1].Type != "inline" {
		t.Fatalf("statuses: %+v %v", statuses, err)
	}
	status, err := c.UpdateRuleSet(ctx, "geo")
	if err != nil || status != (RuleSetStatus{Tag: "geo", LastUpdatedAt: 2000, Type: "remote"}) {
		t.Fatalf("update: %+v %v", status, err)
	}
	_, err = c.UpdateRuleSet(ctx, "broken")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway || apiErr.Message != "unexpected status: 500" {
		t.Fatalf("failed update: %v", err)
	}
	source, err := c.RuleSetSource(ctx, "geo", "https://example.com/geo.srs")
	if err != nil || source.Version != 3 || source.From != "download" || len(source.Rules) != 2 {
		t.Fatalf("source: %+v %v", source, err)
	}
	want := []string{
		"GET /api/v1/rulesets/status ",
		`POST /api/v1/rulesets/update {"tag":"geo"}`,
		`POST /api/v1/rulesets/update {"tag":"broken"}`,
		`POST /api/v1/rulesets/source {"tag":"geo","url":"https://example.com/geo.srs"}`,
	}
	for i, w := range want {
		if i >= len(requests) || requests[i] != w {
			t.Errorf("request %d: %q, want %q", i, requests[i], w)
		}
	}
}
