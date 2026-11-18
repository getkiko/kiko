package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

func TestFakeScriptEndpoints(t *testing.T) {
	srv := NewServer(t)
	c := api.New(srv.URL)
	ctx := context.Background()

	st, err := c.ScriptStatus(ctx)
	if err != nil || len(st.Scripts) != 0 || !st.StorePersistent {
		t.Fatalf("default status: %+v %v", st, err)
	}
	srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "a", Type: api.ScriptTypeRequest, Runs: 3}}})
	if st, _ = c.ScriptStatus(ctx); len(st.Scripts) != 1 || st.Scripts[0].Runs != 3 {
		t.Fatalf("status: %+v", st)
	}

	srv.SetEvaluate(func(req api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionModify, Body: append([]byte("x:"), req.Request.Body...), BodyChanged: true}
	})
	res, err := c.ScriptEvaluate(ctx, api.ScriptEvalRequest{Source: "s", Type: api.ScriptTypeRequest,
		Request: &api.ScriptRequestMessage{URL: "https://e/", Body: []byte{1, 2}, HasBody: true}})
	if err != nil || string(res.Body) != "x:\x01\x02" {
		t.Fatalf("evaluate: %+v %v", res, err)
	}
	if reqs := srv.ScriptEvalRequests(); len(reqs) != 1 || reqs[0].Source != "s" || len(reqs[0].Request.Body) != 2 {
		t.Fatalf("requests: %+v", reqs)
	}

	if _, err = c.ScriptRun(ctx, "cron", "arg"); err != nil {
		t.Fatal(err)
	}
	if runs := srv.ScriptRuns(); len(runs) != 1 || runs[0] != (ScriptRunCall{"cron", "arg"}) {
		t.Fatalf("runs: %+v", runs)
	}

	if _, err = c.ScriptStoreSet(ctx, "n1", "k1", "v1"); err != nil {
		t.Fatal(err)
	}
	srv.SetScriptStore("n2", "k", "v")
	list, err := c.ScriptStoreList(ctx, "n1")
	if err != nil || len(list.Namespaces) != 2 || list.Namespaces[0].Name != "n1" || len(list.Entries) != 1 || list.Entries[0].Value != "v1" || list.Info.Bytes != 6 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if v, _ := c.ScriptStoreGet(ctx, "n2", "k"); !v.Found || v.Value != "v" {
		t.Fatalf("get: %+v", v)
	}
	if _, err = c.ScriptStoreDelete(ctx, "n2", "k"); err != nil {
		t.Fatal(err)
	}
	if store := srv.ScriptStore(); len(store) != 1 || store["n1"]["k1"] != "v1" {
		t.Fatalf("store: %+v", store)
	}

	srv.SetScriptsUnavailable(true)
	if _, err = c.ScriptStatus(ctx); !api.IsScriptUnavailable(err) {
		t.Fatalf("unavailable: %v", err)
	}
	srv.SetScriptsUnavailable(false)
}

func TestFakeScriptLogStream(t *testing.T) {
	srv := NewServer(t)
	c := api.New(srv.URL)
	srv.PushScriptLog(api.ScriptLogEntry{Script: "a", Level: "log", Message: "old"})
	srv.PushScriptLog(api.ScriptLogEntry{Script: "b", Level: "log", Message: "other"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan api.ScriptLogEntry, 8)
	go c.StreamScriptLogs(ctx, 0, "a", func(e api.ScriptLogEntry) { got <- e })
	next := func() api.ScriptLogEntry {
		select {
		case e := <-got:
			return e
		case <-ctx.Done():
			t.Fatal("no entry")
			return api.ScriptLogEntry{}
		}
	}
	if e := next(); e.Message != "old" || e.Seq != 1 {
		t.Fatalf("backlog: %+v", e)
	}
	srv.PushScriptLog(api.ScriptLogEntry{Script: "a", Level: "warn", Message: "live"})
	if e := next(); e.Message != "live" || e.Seq != 3 {
		t.Fatalf("live: %+v", e)
	}
	// A restart of the hub ends the stream; the client reconnects and the
	// numbering starts again from 1.
	srv.RestartScriptHub()
	srv.PushScriptLog(api.ScriptLogEntry{Script: "a", Level: "log", Message: "fresh"})
	for {
		if e := next(); e.Message == "fresh" {
			if e.Seq != 1 {
				t.Fatalf("fresh seq %d", e.Seq)
			}
			break
		}
	}
}

func TestFakeScriptNotificationStream(t *testing.T) {
	srv := NewServer(t)
	c := api.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan api.ScriptNotification, 4)
	go c.StreamScriptNotifications(ctx, 0, func(n api.ScriptNotification) { got <- n })
	srv.PushScriptNotification(api.ScriptNotification{Script: "a", Title: "t"})
	select {
	case n := <-got:
		if n.Title != "t" || n.Seq != 1 {
			t.Fatalf("notification: %+v", n)
		}
	case <-ctx.Done():
		t.Fatal("no notification")
	}
}

func TestFakeScriptRunControls(t *testing.T) {
	srv := NewServer(t)
	srv.SetScriptStatus(api.ScriptStatus{Scripts: []api.ScriptState{{Tag: "tick", Type: api.ScriptTypeCron, NextRunMS: 99}}})
	srv.SetScriptRunResult("tick", api.ScriptRunResult{Action: "none", Output: "out", Error: "boom", DurationUS: 12, Logs: []api.ScriptLogLine{{Level: "log", Message: "m"}}})
	c := api.New(srv.URL)
	ctx := context.Background()
	if st, err := c.ScriptStatus(ctx); err != nil || st.Scripts[0].NextRunMS != 99 {
		t.Fatalf("status %+v %v", st, err)
	}
	res, err := c.ScriptRun(ctx, "tick", "")
	if err != nil || res.Output != "out" || res.Error != "boom" || res.DurationUS != 12 || len(res.Logs) != 1 {
		t.Fatalf("run %+v %v", res, err)
	}
	if res, err = c.ScriptRun(ctx, "other", ""); err != nil || res.Output != "" {
		t.Fatalf("other %+v %v", res, err)
	}
	srv.SetScriptRunDelay(150 * time.Millisecond)
	start := time.Now()
	if _, err = c.ScriptRun(ctx, "tick", ""); err != nil || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("delay %v %v", time.Since(start), err)
	}
	srv.SetScriptRunDelay(0)
	srv.SetScriptRunFailure(404, "no such script")
	if _, err = c.ScriptRun(ctx, "tick", ""); !api.IsStatus(err, 404) {
		t.Fatalf("failure %v", err)
	}
	if len(srv.ScriptRuns()) != 4 {
		t.Fatalf("runs %v", srv.ScriptRuns())
	}
}
