package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScanClient(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/v1/lan-scan":
			if r.Method == http.MethodPost {
				fmt.Fprint(w, `{"id":"7-x"}`)
				return
			}
			fmt.Fprint(w, `{"id":"7-x","state":"running","phase":"identify","step":2,"steps":7,"cursor":3,"devices":[{"ip":"192.168.1.20","name":"Bob","type":"phone","model":"iPhone14,2","mac":"3c:22:fb:00:00:20","macRandom":true,"image":"iphone","sources":["arp","mdns"]}]}`)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	id, err := c.StartScan(ctx)
	if err != nil || id != "7-x" {
		t.Fatalf("%q %v", id, err)
	}
	snap, err := c.PollScan(ctx, id, 2)
	if err != nil || snap.State != ScanRunning || snap.Cursor != 3 || len(snap.Devices) != 1 || !snap.Devices[0].MACRandom || snap.Devices[0].Image != "iphone" {
		t.Fatalf("%+v %v", snap, err)
	}
	if err := c.CancelScan(ctx, id); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /api/v1/lan-scan", "GET /api/v1/lan-scan?id=7-x&since=2", "POST /api/v1/lan-scan/cancel?id=7-x"}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("request %d = %q, want %q", i, seen[i], w)
		}
	}
}

func TestRunningScanID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("old") != "" {
			http.Error(w, "已有扫描在进行", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"已有扫描在进行","id":"3-abc"}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL).StartScan(context.Background())
	if !IsStatus(err, http.StatusConflict) || RunningScanID(err) != "3-abc" {
		t.Fatalf("%v %q", err, RunningScanID(err))
	}
	if RunningScanID(nil) != "" || RunningScanID(fmt.Errorf("x")) != "" {
		t.Error("other errors carry no id")
	}
}
