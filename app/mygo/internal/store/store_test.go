package store_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// harness drives a store from a tester's frames, which drain what its
// goroutines post.
type harness struct {
	t   *testing.T
	srv *testkit.Server
	env *env.Env
	s   *store.Store
	tt  *ui.Tester
}

func newHarness(t *testing.T, initial *api.ServerStore) *harness {
	t.Helper()
	srv := testkit.NewServer(t)
	if initial != nil {
		srv.SetStore(*initial)
	}
	e, _ := testkit.NewEnv(srv)
	s := store.New(e)
	h := &harness{t: t, srv: srv, env: e, s: s}
	h.tt = ui.NewTester(func(c *ui.Context) { e.Drain(c); ui.Text(c, "store") }, 200, 100)
	e.Server.Start()
	t.Cleanup(e.Server.Stop)
	s.Load()
	h.until(func() bool { return s.Loaded && e.Server.Connected && !s.Saving() })
	return h
}

func (h *harness) until(cond func() bool) { h.t.Helper(); testkit.Until(h.t, h.tt, cond) }

// startCore reports a running core, as unapplied changes are compared
// only while the core runs.
func (h *harness) startCore() {
	h.t.Helper()
	running := api.Runtime{State: "running", StartedAt: "2026-10-31T17:00:00Z"}
	h.srv.SetRuntime(running)
	h.srv.Emit("runtime", running)
	h.until(func() bool { return h.env.Server.Runtime.Running() })
}

// storeWith is a server store holding the profiles, the first active. Their
// documents are completed with the DNS presets, as a store saved since they
// were introduced is, so loading it does not save a migration.
func storeWith(t *testing.T, revision uint64, profiles ...*model.Profile) api.ServerStore {
	t.Helper()
	completed := make([]*model.Profile, len(profiles))
	for i, p := range profiles {
		copied := *p
		copied.Doc = model.AddDNSPresets(p.Doc)
		completed[i] = &copied
	}
	profiles = completed
	raw, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	active := profiles[0].ID
	doc, _ := json.Marshal(profiles[0].Doc)
	return api.ServerStore{Revision: revision, Doc: doc, Profiles: raw, ActiveProfileID: &active}
}

func profile(id, name string, doc *model.Doc) *model.Profile {
	return &model.Profile{ID: id, Name: name, SavedAt: 1, Doc: doc}
}

func TestFirstLoadCreatesProfile(t *testing.T) {
	h := newHarness(t, nil)
	h.until(func() bool { return h.srv.Saves() == 1 })
	saved := h.srv.Store()
	if saved.ActiveProfileID == nil || *saved.ActiveProfileID != h.s.ActiveProfileID() {
		t.Fatalf("active profile %v, store %q", saved.ActiveProfileID, h.s.ActiveProfileID())
	}
	var profiles []model.Profile
	if err := json.Unmarshal(saved.Profiles, &profiles); err != nil || len(profiles) != 1 || profiles[0].Name != "未命名配置" {
		t.Fatalf("profiles %+v (%v)", profiles, err)
	}
}

func TestUpdateSavesAndTracksProfile(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Log.Level = "info"
	h := newHarness(t, ptr(storeWith(t, 7, profile("p1", "家里", doc))))
	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "debug" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { return h.srv.Store().Revision == 8 && !h.s.Saving() })
	var saved model.Doc
	if err := json.Unmarshal(h.srv.Store().Doc, &saved); err != nil || saved.Log.Level != "debug" {
		t.Fatalf("saved doc level %q (%v)", saved.Log.Level, err)
	}
	if got := h.s.Profiles()[0].Doc.Log.Level; got != "debug" {
		t.Errorf("active profile not updated: %q", got)
	}
}

func TestRemoteStoreAdoptedOrDeferred(t *testing.T) {
	doc := model.EmptyDoc("linux")
	h := newHarness(t, ptr(storeWith(t, 3, profile("p1", "家里", doc))))
	remoteDoc := model.Clone(doc)
	remoteDoc.Log.Level = "warn"
	remote := storeWith(t, 4, profile("p1", "家里", remoteDoc))

	// While an editor is open, the change waits.
	h.s.BeginEdit()
	h.srv.Emit("store", remote)
	h.until(func() bool { return h.env.Server.Latest["store"].Type == "store" })
	if h.s.Doc().Log.Level == "warn" {
		t.Fatal("remote store adopted while editing")
	}
	h.s.EndEdit()
	if h.s.Doc().Log.Level != "warn" {
		t.Fatalf("deferred store not adopted after editing: %q", h.s.Doc().Log.Level)
	}

	// An older revision is ignored.
	stale := storeWith(t, 2, profile("p1", "旧", model.EmptyDoc("linux")))
	h.srv.Emit("store", stale)
	h.tt.Frame()
	if h.s.Profiles()[0].Name != "家里" {
		t.Error("stale store adopted")
	}
}

func TestConflictAdoptsServerState(t *testing.T) {
	doc := model.EmptyDoc("linux")
	h := newHarness(t, ptr(storeWith(t, 5, profile("p1", "家里", doc))))
	// Another client saved meanwhile.
	other := model.Clone(doc)
	other.Log.Level = "error"
	h.srv.SetStore(storeWith(t, 9, profile("p1", "家里", other)))
	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "debug" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { return !h.s.Saving() && h.s.Doc().Log.Level == "error" })
	if h.srv.Store().Revision != 9 {
		t.Errorf("conflicting save written: revision %d", h.srv.Store().Revision)
	}
}

func ptr[T any](v T) *T { return &v }

// putRecorder answers PUT /store itself: the first save waits for release
// and answers first, later ones are accepted at the server's revision.
type putRecorder struct {
	mu        sync.Mutex
	revision  uint64
	first     func(next api.ServerStore) (int, api.ServerStore)
	release   chan struct{}
	released  sync.Once
	calls     int
	revisions []uint64
	levels    []string
}

func (p *putRecorder) handle(w http.ResponseWriter, r *http.Request) {
	var next api.ServerStore
	if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var doc model.Doc
	_ = json.Unmarshal(next.Doc, &doc)
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.revisions = append(p.revisions, next.Revision)
	p.levels = append(p.levels, doc.Log.Level)
	p.mu.Unlock()
	if call == 1 && p.first != nil {
		<-p.release
		status, body := p.first(next)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if next.Revision != p.revision {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(api.ServerStore{Revision: p.revision})
		return
	}
	p.revision++
	next.Revision = p.revision
	_ = json.NewEncoder(w).Encode(next)
}

// unblock lets the first save answer. A failing test unblocks it as it
// ends, so the fake server can close.
func (p *putRecorder) unblock() { p.released.Do(func() { close(p.release) }) }

func (p *putRecorder) snapshot() (revisions []uint64, levels []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.revisions), slices.Clone(p.levels)
}

// A save that conflicts with a remote change an editor has merged since
// writes the merge, rather than dropping it.
func TestConflictAfterMergeWritesTheMerge(t *testing.T) {
	doc := model.EmptyDoc("linux")
	h := newHarness(t, ptr(storeWith(t, 5, profile("p1", "家里", doc))))
	other := storeWith(t, 7, profile("p1", "家里", model.Clone(doc)), profile("p2", "公司", model.Clone(doc)))
	puts := &putRecorder{revision: 7, release: make(chan struct{}),
		first: func(api.ServerStore) (int, api.ServerStore) { return http.StatusConflict, other }}
	h.srv.Route("PUT", "/store", puts.handle)
	t.Cleanup(puts.unblock)

	h.s.BeginEdit()
	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "warn" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { calls, _ := puts.snapshot(); return len(calls) == 1 })
	// Another client saved revision 7 meanwhile; the editor defers it.
	h.srv.Emit("store", other)
	h.until(func() bool { return h.env.Server.Latest["store"].Type == "store" })
	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "debug" }); err != nil {
		t.Fatal(err)
	}
	h.s.EndEdit()
	puts.unblock()
	h.until(func() bool { revisions, _ := puts.snapshot(); return len(revisions) == 2 && !h.s.Saving() })
	revisions, levels := puts.snapshot()
	if revisions[1] != 7 || levels[1] != "debug" {
		t.Fatalf("merge written at %v with levels %v", revisions, levels)
	}
	if h.s.Doc().Log.Level != "debug" || len(h.s.Profiles()) != 2 {
		t.Errorf("merged state lost: level %q, %d profiles", h.s.Doc().Log.Level, len(h.s.Profiles()))
	}
}

// A newer store adopted while a save is in flight keeps its revision when
// the save's answer arrives.
func TestSaveAnswerDoesNotRewindRevision(t *testing.T) {
	doc := model.EmptyDoc("linux")
	h := newHarness(t, ptr(storeWith(t, 5, profile("p1", "家里", doc))))
	remoteDoc := model.Clone(doc)
	remoteDoc.Log.Level = "error"
	remote := storeWith(t, 7, profile("p1", "家里", remoteDoc))
	puts := &putRecorder{revision: 7, release: make(chan struct{}),
		first: func(next api.ServerStore) (int, api.ServerStore) { next.Revision = 6; return http.StatusOK, next }}
	h.srv.Route("PUT", "/store", puts.handle)
	t.Cleanup(puts.unblock)

	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "warn" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { calls, _ := puts.snapshot(); return len(calls) == 1 })
	h.srv.Emit("store", remote)
	h.until(func() bool { return h.s.Doc().Log.Level == "error" })
	puts.unblock()
	h.until(func() bool { return !h.s.Saving() })
	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "debug" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { revisions, _ := puts.snapshot(); return len(revisions) == 2 && !h.s.Saving() })
	if revisions, _ := puts.snapshot(); revisions[1] != 7 {
		t.Errorf("next save at revision %d, want 7", revisions[1])
	}
}

// Library and profile changes made while an editor is open survive a
// remote store deferred meanwhile.
func TestLibraryChangeWhileEditingSurvivesRemote(t *testing.T) {
	doc := model.EmptyDoc("linux")
	h := newHarness(t, ptr(storeWith(t, 3, profile("p1", "家里", doc))))
	h.s.BeginEdit()
	remote := storeWith(t, 4, profile("p1", "家里", model.Clone(doc)), profile("p2", "公司", model.Clone(doc)))
	h.srv.SetStore(remote)
	h.srv.Emit("store", remote)
	h.until(func() bool { return h.env.Server.Latest["store"].Type == "store" })
	added := &model.NetworkConfig{ID: "net-1", Name: "本地", Kind: model.NetworkConfigNetwork, Fields: model.Fields{}}
	h.s.SetNetworkConfigs([]*model.NetworkConfig{added})
	h.s.EndEdit()
	if got := h.s.NetworkConfigs(); len(got) != 1 || got[0].ID != "net-1" {
		t.Fatalf("library after merge: %+v", got)
	}
	if len(h.s.Profiles()) != 2 {
		t.Errorf("remote profiles not merged: %d", len(h.s.Profiles()))
	}
	h.until(func() bool { return h.srv.Store().Revision == 5 && !h.s.Saving() })
	var library []model.NetworkConfig
	if err := json.Unmarshal(h.srv.Store().NetworkConfigs, &library); err != nil || len(library) != 1 {
		t.Errorf("saved library %s (%v)", h.srv.Store().NetworkConfigs, err)
	}
}

// A store event that arrives while the first load is in flight is taken
// once the load finishes.
func TestStoreEventDuringLoadIsAdopted(t *testing.T) {
	doc := model.EmptyDoc("linux")
	srv := testkit.NewServer(t)
	srv.SetStore(storeWith(t, 3, profile("p1", "家里", doc)))
	e, _ := testkit.NewEnv(srv)
	s := store.New(e)
	tt := ui.NewTester(func(c *ui.Context) { e.Drain(c); ui.Text(c, "store") }, 200, 100)
	newer := model.Clone(doc)
	newer.Log.Level = "error"
	raw, _ := json.Marshal(storeWith(t, 4, profile("p1", "家里", newer)))
	e.Server.Dispatch(api.Event{Type: "store", Data: raw})
	s.Load()
	testkit.Until(t, tt, func() bool { return s.Loaded })
	if s.Doc().Log.Level != "error" {
		t.Errorf("event during load dropped: level %q", s.Doc().Log.Level)
	}
}

// After the event stream connects again, as after the server restarted,
// the store reads the server's store and adopts what changed meanwhile
// without an event.
func TestReconnectResyncsTheStore(t *testing.T) {
	doc := model.EmptyDoc("linux")
	h := newHarness(t, ptr(storeWith(t, 3, profile("p1", "家里", doc))))
	remoteDoc := model.Clone(doc)
	remoteDoc.Log.Level = "warn"
	h.srv.SetStore(storeWith(t, 4, profile("p1", "家里", remoteDoc)))

	h.env.Server.Retry()
	h.until(func() bool { return h.s.Doc().Log.Level == "warn" })
	if saves := h.srv.Saves(); saves != 0 {
		t.Errorf("%d saves for a resync", saves)
	}
}

// A save that failed while the server was down is written once it is back.
func TestReconnectRetriesAFailedSave(t *testing.T) {
	h := newHarness(t, ptr(storeWith(t, 3, profile("p1", "家里", model.EmptyDoc("linux")))))
	h.srv.Route("PUT", "/store", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := h.s.Update(func(d *model.Doc) { d.Log.Level = "error" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { return h.s.SaveError != "" })
	h.srv.Unroute("PUT", "/store")

	h.env.Server.Retry()
	h.until(func() bool { return h.s.SaveError == "" && !h.s.Saving() })
	if saves := h.srv.Saves(); saves != 1 {
		t.Fatalf("%d saves after the reconnect, want 1", saves)
	}
	var saved model.Doc
	if err := json.Unmarshal(h.srv.Store().Doc, &saved); err != nil || saved.Log.Level != "error" {
		t.Errorf("server doc log level %q (%v)", saved.Log.Level, err)
	}
}

// A store that never loaded, the server down at start, loads on reconnect.
func TestReconnectLoadsAStoreThatFailed(t *testing.T) {
	srv := testkit.NewServer(t)
	srv.Route("GET", "/store", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	e, _ := testkit.NewEnv(srv)
	s := store.New(e)
	tt := ui.NewTester(func(c *ui.Context) { e.Drain(c); ui.Text(c, "store") }, 200, 100)
	e.Server.Start()
	t.Cleanup(e.Server.Stop)
	s.Load()
	testkit.Until(t, tt, func() bool { return s.LoadError != "" && e.Server.Connected })
	srv.Unroute("GET", "/store")
	e.Server.Retry()
	testkit.Until(t, tt, func() bool { return s.Loaded && s.LoadError == "" })
}

// The store compares its document with the applied configuration once it
// is known, and an apply refreshes it.
func TestUnappliedFollowsTheAppliedConfig(t *testing.T) {
	h := newHarness(t, nil)
	h.until(func() bool { return h.s.Loaded })
	h.startCore()
	if u := h.s.Unapplied(); u.Count() != 0 {
		t.Fatalf("unknown applied configuration reported changes: %v", u.Parts)
	}
	var applied atomic.Value
	applied.Store([]byte(`{}`))
	h.srv.Route("GET", "/config/applied", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(applied.Load().([]byte))
	})
	h.env.Post(h.s.RefreshApplied)
	h.until(func() bool { return h.s.Unapplied().Count() > 0 })
	if u := h.s.Unapplied(); !u.Pages[env.PageRoute] || !u.Pages[env.PageSettings] {
		t.Errorf("pages %v", u.Pages)
	}
	applied.Store(h.s.Emit().JSON)
	done := make(chan error, 1)
	h.env.Post(func() { h.s.ApplyLive(func(err error) { done <- err }) })
	h.until(func() bool { return len(done) == 1 })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { return h.s.Unapplied().Count() == 0 })
}

// A core that starts again compares only once the applied configuration
// is read in its run: the configuration of the last run is not reported
// against, so the changes card does not flash as the core starts.
func TestUnappliedWaitsForTheRunsConfig(t *testing.T) {
	h := newHarness(t, nil)
	h.until(func() bool { return h.s.Loaded })
	h.startCore()
	h.srv.Route("GET", "/config/applied", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	h.env.Post(h.s.RefreshApplied)
	h.until(func() bool { return h.s.Unapplied().Count() > 0 })
	started := make(chan int, 1)
	h.env.Post(func() {
		runtime := h.env.Server.Runtime
		runtime.StartedAt = "2026-10-31T18:00:00Z"
		h.env.Server.SetRuntime(runtime)
		started <- h.s.Unapplied().Count()
	})
	h.until(func() bool { return len(started) == 1 })
	if n := <-started; n != 0 {
		t.Fatalf("a new run reported %d changes against the last run's configuration", n)
	}
	// The read Unapplied asked for brings the changes back.
	h.until(func() bool { return h.s.Unapplied().Count() > 0 })
}

// A save refused by the server's write gate (423) keeps the change, says
// whom it waits for, and is retried until the gate is free.
func TestSaveRetriesWhileLocked(t *testing.T) {
	defer store.SetLockBackoff(20 * time.Millisecond)()
	h := newHarness(t, nil)
	var mu sync.Mutex
	refusals := 0
	h.srv.Route(http.MethodPut, "/store", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refusals++
		n := refusals
		mu.Unlock()
		if n >= 3 {
			h.srv.Unroute(http.MethodPut, "/store")
		}
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusLocked)
		_, _ = w.Write([]byte(`{"error":"「NAS」正在应用配置，请稍后重试","holder":{"id":"d1","name":"NAS"},"op":"应用配置"}`))
	})
	saves := h.srv.Saves()
	if err := h.s.Update(func(d *model.Doc) { d.LatencyTestURL = "https://locked.example/204" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { return h.s.LockWait == "等待「NAS」完成操作" })
	if h.s.Doc().LatencyTestURL != "https://locked.example/204" || h.s.SaveError != "" {
		t.Fatalf("change lost while waiting: %q, error %q", h.s.Doc().LatencyTestURL, h.s.SaveError)
	}
	h.until(func() bool { return h.srv.Saves() > saves && !h.s.Saving() })
	if h.s.LockWait != "" || h.s.SaveError != "" || !strings.Contains(string(h.srv.Store().Doc), "locked.example") {
		t.Errorf("after the gate freed: wait %q, error %q", h.s.LockWait, h.s.SaveError)
	}
}

// After five refusals the save fails as any other.
func TestSaveGivesUpWhenLockedTooLong(t *testing.T) {
	defer store.SetLockBackoff(time.Millisecond)()
	h := newHarness(t, nil)
	h.srv.Route(http.MethodPut, "/store", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusLocked)
		_, _ = w.Write([]byte(`{"error":"「NAS」正在应用配置，请稍后重试","holder":{"id":"d1","name":"NAS"},"op":"应用配置"}`))
	})
	before := len(h.srv.Requests())
	if err := h.s.Update(func(d *model.Doc) { d.LatencyTestURL = "https://x.example/204" }); err != nil {
		t.Fatal(err)
	}
	h.until(func() bool { return h.s.SaveError != "" })
	if h.s.LockWait != "" || h.s.SaveError != "「NAS」正在应用配置，请稍后重试" {
		t.Errorf("wait %q, error %q", h.s.LockWait, h.s.SaveError)
	}
	puts := 0
	for _, r := range h.srv.Requests()[before:] {
		if strings.HasPrefix(r, "PUT /api/v1/store") {
			puts++
		}
	}
	if puts != 6 {
		t.Errorf("%d saves, want 1 and 5 retries", puts)
	}
}

// TestMeasureLatencyOfKeepsOtherResults: testing some outbounds replaces
// their results only; outbounds without a server are skipped.
func TestMeasureLatencyOfKeepsOtherResults(t *testing.T) {
	doc := model.EmptyDoc("linux")
	node := func(id, server string) *model.Outbound {
		fields := model.Fields{"server_port": 443.0}
		if server != "" {
			fields["server"] = server
		}
		return &model.Outbound{Entity: model.Entity{ID: id, Tag: id, Enabled: true}, Kind: "outbound", Type: "trojan", Fields: fields}
	}
	doc.Outbounds = append(doc.Outbounds, node("out_a", "a.example"), node("out_b", "b.example"), node("out_c", ""))
	h := newHarness(t, ptr(storeWith(t, 1, profile("p1", "家里", doc))))
	var mu sync.Mutex
	var asked []string
	h.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		var results []api.LatencyResult
		for _, target := range body.Targets {
			mu.Lock()
			asked = append(asked, target.ID)
			mu.Unlock()
			ms := 50.0
			results = append(results, api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 3})
		}
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	old := 99.0
	h.s.Latency["out_b"] = api.LatencyResult{ID: "out_b", MedianConnectMs: &old, SuccessSamples: 1}
	gen := h.s.LatencyGen()
	if !h.s.MeasureLatencyOf([]string{"out_a", "out_c"}) {
		t.Fatal("out_a not testable")
	}
	if h.s.LatencyProgress.Total != 0 {
		t.Fatalf("progress %+v, only test-all counts", h.s.LatencyProgress)
	}
	h.until(func() bool { return h.s.Latency["out_a"].ID != "" && !h.s.Testing("out_a") })
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []string{"out_a"}) {
		t.Errorf("tested %q", asked)
	}
	if r := h.s.Latency["out_a"]; r.MedianConnectMs == nil || *r.MedianConnectMs != 50 {
		t.Errorf("out_a %+v", r)
	}
	if r := h.s.Latency["out_b"]; r.MedianConnectMs == nil || *r.MedianConnectMs != 99 {
		t.Errorf("out_b result lost: %+v", r)
	}
	if h.s.LatencyGen() == gen {
		t.Error("LatencyGen unchanged")
	}
	if h.s.MeasureLatencyOf([]string{"out_c", "gone"}) {
		t.Error("nothing to test reported as tested")
	}
}
