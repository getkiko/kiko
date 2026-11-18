package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApplicationCatalogCachesAndRefreshes(t *testing.T) {
	calls := 0
	catalog := applicationCatalog{discover: func(context.Context) ([]installedApplication, []string, error) {
		calls++
		return []installedApplication{{ID: "b", Name: "Zulu", Path: "/Zulu.app"}, {ID: "a", Name: "Alpha", Path: "/Alpha.app"}}, nil, nil
	}}
	for i := 0; i < 2; i++ {
		items, warnings, err := catalog.snapshot(context.Background(), false)
		if err != nil || len(items) != 2 || items[0].Name != "Alpha" || warnings == nil {
			t.Fatalf("snapshot = %v, %v, %v", items, warnings, err)
		}
	}
	if calls != 1 {
		t.Fatalf("discovery calls = %d", calls)
	}
	_, _, _ = catalog.snapshot(context.Background(), true)
	if calls != 2 {
		t.Fatalf("refresh discovery calls = %d", calls)
	}
}

func TestApplicationIconAcceptsCatalogIDsOnly(t *testing.T) {
	mux := newRouteMux(nil)
	registerApplications(mux)
	r := httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/applications/arbitrary/icon?path=/etc/passwd", nil))
	if r.Code != http.StatusNotFound {
		t.Fatalf("unknown application icon = %d", r.Code)
	}
}
