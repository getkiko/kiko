package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/syncbackend"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type testProfile struct {
	ID, Name string
	Doc      map[string]any
	SavedAt  int64
}

func newSyncTestServer(t *testing.T, backend syncbackend.Backend, clock *testClock) *server {
	t.Helper()
	s, err := newServer("/nonexistent", filepath.Join(t.TempDir(), "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	s.sync.newBackend = func(syncbackend.Config) (syncbackend.Backend, error) { return backend, nil }
	s.sync.now = clock.Now
	s.sync.cfg.Backend = syncbackend.Config{Type: "kv", KV: &syncbackend.KV{URL: "https://sync.example.test", Token: "secret-token"}}
	return s
}

// saveProfiles saves through putStore, the same path the client uses
// (including change tracking).
func saveProfiles(t *testing.T, s *server, active string, profiles ...testProfile) {
	t.Helper()
	list := make([]map[string]any, 0, len(profiles))
	var activeDoc any = map[string]any{"version": 1}
	for _, p := range profiles {
		item := map[string]any{"id": p.ID, "name": p.Name, "doc": p.Doc}
		if p.SavedAt > 0 {
			item["savedAt"] = p.SavedAt
		}
		list = append(list, item)
		if p.ID == active {
			activeDoc = p.Doc
		}
	}
	s.mu.Lock()
	revision := s.store.Revision
	s.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"revision": revision, "doc": activeDoc, "profiles": list, "activeProfileId": active})
	rec := httptest.NewRecorder()
	s.putStore(rec, httptest.NewRequest(http.MethodPut, "/api/v1/store", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("putStore code=%d body=%s", rec.Code, rec.Body)
	}
}

func storeProfileNames(s *server) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, p := range parseStoreProfiles(s.store.Profiles) {
		out[p.ID] = p.Name
	}
	return out
}

func storeProfileDoc(s *server, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range parseStoreProfiles(s.store.Profiles) {
		if p.ID == id {
			var v any
			_ = json.Unmarshal(p.Doc, &v)
			b, _ := json.Marshal(v)
			return string(b)
		}
	}
	return ""
}

func newTestClock() *testClock { return &testClock{t: time.UnixMilli(1_800_000_000_000)} }

func TestSyncTrackUpdatesTimestampsOnlyOnRealChanges(t *testing.T) {
	clock := newTestClock()
	s := newSyncTestServer(t, nil, clock)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"version": 1, "x": 1}, SavedAt: 1234})
	first := s.sync.st.Profiles["a"]
	if first.UpdatedAt != 1234 || first.Hash == "" {
		t.Fatalf("first seen should reuse savedAt: %+v", first)
	}

	clock.Add(time.Minute)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"x": 1, "version": 1}, SavedAt: 9999})
	if got := s.sync.st.Profiles["a"]; got != first {
		t.Fatalf("re-saving identical content must not change tracking: %+v vs %+v", got, first)
	}

	clock.Add(time.Minute)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "B", Doc: map[string]any{"version": 1, "x": 1}})
	renamed := s.sync.st.Profiles["a"]
	if renamed.UpdatedAt != clock.Now().UnixMilli() || renamed.Hash == first.Hash {
		t.Fatalf("rename must bump updatedAt: %+v", renamed)
	}

	clock.Add(time.Minute)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "B", Doc: map[string]any{"version": 1, "x": 2}})
	if got := s.sync.st.Profiles["a"]; got.UpdatedAt != clock.Now().UnixMilli() || got.Hash == renamed.Hash {
		t.Fatalf("doc change must bump updatedAt: %+v", got)
	}
}

func TestSyncTrackTombstonesAndResurrection(t *testing.T) {
	clock := newTestClock()
	s := newSyncTestServer(t, nil, clock)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"version": 1}}, testProfile{ID: "b", Name: "B", Doc: map[string]any{"version": 1}})
	clock.Add(time.Minute)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"version": 1}})
	if len(s.sync.st.Tombstones) != 1 || s.sync.st.Tombstones[0].ID != "b" || s.sync.st.Tombstones[0].DeletedAt != clock.Now().UnixMilli() {
		t.Fatalf("tombstones=%+v", s.sync.st.Tombstones)
	}
	if _, ok := s.sync.st.Profiles["b"]; ok {
		t.Fatal("deleted profile must leave tracking")
	}

	clock.Add(time.Minute)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"version": 1}}, testProfile{ID: "b", Name: "B", Doc: map[string]any{"version": 1}})
	if len(s.sync.st.Tombstones) != 0 {
		t.Fatalf("re-added profile must drop its tombstone: %+v", s.sync.st.Tombstones)
	}
}

func TestSyncTrackIgnoresStaleRevisionsAndUnconfiguredServers(t *testing.T) {
	clock := newTestClock()
	s := newSyncTestServer(t, nil, clock)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"version": 1}})
	tracked := s.sync.st.TrackedRevision
	stale := storeState{Revision: tracked - 1, Profiles: json.RawMessage(`[]`)}
	s.syncTrack(stale)
	if len(s.sync.st.Profiles) != 1 || len(s.sync.st.Tombstones) != 0 {
		t.Fatalf("stale revision must be ignored: %+v", s.sync.st)
	}

	plain, err := newServer("/nonexistent", filepath.Join(t.TempDir(), "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	plain.sync.now = clock.Now
	plain.syncTrack(storeState{Revision: 3, Profiles: json.RawMessage(`[{"id":"a","name":"A","doc":{}}]`)})
	if len(plain.sync.st.Profiles) != 0 {
		t.Fatal("unconfigured server must not track")
	}
}

func TestSyncHasLocalChanges(t *testing.T) {
	st := &syncStateFile{
		Profiles:   map[string]syncProfileTrack{"a": {Hash: "h1"}},
		LastSynced: map[string]string{"a": "h1"},
	}
	if syncHasLocalChanges(st) {
		t.Fatal("in sync should not be dirty")
	}
	st.Profiles["a"] = syncProfileTrack{Hash: "h2"}
	if !syncHasLocalChanges(st) {
		t.Fatal("changed hash should be dirty")
	}
	st.Profiles["a"] = syncProfileTrack{Hash: "h1"}
	st.Tombstones = []syncTombstone{{ID: "gone"}}
	if syncHasLocalChanges(st) {
		t.Fatal("tombstone for never-synced profile is not dirty")
	}
	st.LastSynced["gone"] = "h0"
	if !syncHasLocalChanges(st) {
		t.Fatal("tombstone for a synced profile is dirty")
	}
}

func TestSyncStatePersistsAcrossRestart(t *testing.T) {
	clock := newTestClock()
	s := newSyncTestServer(t, nil, clock)
	saveProfiles(t, s, "a", testProfile{ID: "a", Name: "A", Doc: map[string]any{"version": 1}})
	s.sync.mu.Lock()
	s.sync.cfg.AutoSync = true
	_ = s.syncSaveConfigLocked()
	s.sync.mu.Unlock()

	again, err := newServer("/nonexistent", s.dataDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !again.sync.cfg.AutoSync || again.sync.cfg.Backend.Type != "kv" || again.sync.cfg.DeviceID != s.sync.cfg.DeviceID {
		t.Fatalf("config not restored: %+v", again.sync.cfg)
	}
	if again.sync.st.Profiles["a"].Hash == "" {
		t.Fatalf("tracking not restored: %+v", again.sync.st)
	}
}
