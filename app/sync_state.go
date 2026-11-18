package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/syncbackend"
	"github.com/getkiko/kiko/app/syncvault"
)

const (
	syncConfigFileName = "sync.json"
	syncKeyFileName    = "sync-key.bin"
	syncStateFileName  = "sync-state.json"
	syncTombstoneTTL   = 30 * 24 * time.Hour
)

// These are variables so tests can override them.
var (
	syncStartDelay  = 5 * time.Second
	syncDebounce    = 30 * time.Second
	syncInterval    = 10 * time.Minute
	syncBackoffBase = 30 * time.Second
	syncBackoffMax  = 30 * time.Minute
)

type syncConfigFile struct {
	Backend  syncbackend.Config `json:"backend"`
	AutoSync bool               `json:"autoSync"`
	DeviceID string             `json:"deviceId"`
}

type syncProfileTrack struct {
	Hash      string `json:"hash"`
	UpdatedAt int64  `json:"updatedAt"`
}

type syncTombstone struct {
	ID        string `json:"id"`
	DeletedAt int64  `json:"deletedAt"`
}

type syncResult struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Conflicts int `json:"conflicts"`
	Deleted   int `json:"deleted"`
}

type syncStateFile struct {
	Profiles        map[string]syncProfileTrack `json:"profiles"`
	Tombstones      []syncTombstone             `json:"tombstones"`
	LastSynced      map[string]string           `json:"lastSynced"`
	Header          *syncvault.Header           `json:"header,omitempty"`
	RemoteRevision  uint64                      `json:"remoteRevision"`
	RemoteEtag      string                      `json:"remoteEtag,omitempty"`
	LastSyncedAt    int64                       `json:"lastSyncedAt,omitempty"`
	LastError       string                      `json:"lastError,omitempty"`
	LastResult      *syncResult                 `json:"lastResult,omitempty"`
	VaultExists     *bool                       `json:"vaultExists,omitempty"`
	TrackedRevision uint64                      `json:"trackedRevision"`
	// LegacyMerged is the SHA-256 of the old (pre-rename) backup last merged
	// whose moved marker is still missing.
	LegacyMerged string `json:"legacyMerged,omitempty"`
}

// syncRuntime lock order: syncMu -> trackMu -> (mu | s.mu). mu and s.mu are
// never nested, and s.emit is never called while holding either of them.
type syncRuntime struct {
	syncMu  sync.Mutex // serializes sync runs and key/header changes
	trackMu sync.Mutex // serializes "write store + update tracking"
	mu      sync.Mutex // guards cfg, st, keys and the status fields

	cfg      syncConfigFile
	st       syncStateFile
	keys     *syncvault.Keys
	syncing  bool
	failures int
	nextTry  time.Time

	trigger  chan struct{}
	debounce *time.Timer

	newBackend func(syncbackend.Config) (syncbackend.Backend, error)
	now        func() time.Time
}

type storeProfile struct {
	ID      string
	Name    string
	Doc     json.RawMessage
	SavedAt int64
	Raw     map[string]json.RawMessage
}

// parseStoreProfiles leniently parses the profiles in the store: entries missing
// an id or doc are skipped, and unknown fields are kept in Raw.
func parseStoreProfiles(raw json.RawMessage) []storeProfile {
	var list []map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]storeProfile, 0, len(list))
	for _, item := range list {
		var p storeProfile
		if json.Unmarshal(item["id"], &p.ID) != nil || p.ID == "" || len(item["doc"]) == 0 {
			continue
		}
		_ = json.Unmarshal(item["name"], &p.Name)
		var savedAt float64
		if json.Unmarshal(item["savedAt"], &savedAt) == nil {
			p.SavedAt = int64(savedAt)
		}
		p.Doc, p.Raw = item["doc"], item
		out = append(out, p)
	}
	return out
}

// syncProfileHash hashes the normalized name+doc, so the result does not depend
// on JSON key order or whitespace.
func syncProfileHash(name string, doc json.RawMessage) string {
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		value = string(doc)
	}
	encoded, _ := json.Marshal(map[string]any{"name": name, "doc": value})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func newSyncID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (s *server) syncPath(name string) string { return filepath.Join(s.dataDir, name) }

func (s *server) loadSync() {
	sr := &s.sync
	sr.now = time.Now
	sr.newBackend = syncbackend.New
	sr.trigger = make(chan struct{}, 1)
	_ = readJSON(s.syncPath(syncConfigFileName), &sr.cfg)
	_ = readJSON(s.syncPath(syncStateFileName), &sr.st)
	if sr.st.Profiles == nil {
		sr.st.Profiles = map[string]syncProfileTrack{}
	}
	if sr.st.LastSynced == nil {
		sr.st.LastSynced = map[string]string{}
	}
	if sr.cfg.DeviceID == "" {
		sr.cfg.DeviceID = newSyncID("dev_")
	}
	if b, err := os.ReadFile(s.syncPath(syncKeyFileName)); err == nil {
		if keys, e := syncvault.UnmarshalKeys(b); e == nil {
			sr.keys = keys
		}
	}
	if sr.cfg.Backend.Type != "" {
		s.syncTrack(s.store)
	}
}

func (s *server) syncConfigured() bool {
	s.sync.mu.Lock()
	defer s.sync.mu.Unlock()
	return s.sync.cfg.Backend.Type != ""
}

func (s *server) syncSaveConfigLocked() error {
	return atomicJSON(s.syncPath(syncConfigFileName), s.sync.cfg)
}

func (s *server) syncSaveStateLocked() error {
	return atomicJSON(s.syncPath(syncStateFileName), s.sync.st)
}

func (s *server) syncSaveKey(keys *syncvault.Keys) error {
	return atomicWrite(s.syncPath(syncKeyFileName), keys.Marshal(), 0o600)
}

// syncTrack is called after the store is saved. It records each profile's
// content hash and update time plus deletion tombstones, and schedules a
// debounced push when there are local changes.
func (s *server) syncTrack(st storeState) {
	if !s.syncConfigured() {
		return
	}
	s.sync.trackMu.Lock()
	dirty := s.syncTrackLocked(st)
	s.sync.trackMu.Unlock()
	if dirty {
		s.syncScheduleDirty()
	}
}

// syncTrackLocked must be called with trackMu held. It reports whether there are
// local changes not yet synced.
func (s *server) syncTrackLocked(st storeState) bool {
	profiles := storeSyncEntries(st)
	sr := &s.sync
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if st.Revision < sr.st.TrackedRevision {
		return false
	}
	now := sr.now().UnixMilli()
	next := make(map[string]syncProfileTrack, len(profiles))
	changed := st.Revision != sr.st.TrackedRevision
	for _, p := range profiles {
		hash := syncProfileHash(p.Name, p.Doc)
		if old, ok := sr.st.Profiles[p.ID]; ok && old.Hash == hash {
			next[p.ID] = old
			continue
		}
		updatedAt := now
		if _, seen := sr.st.Profiles[p.ID]; !seen && p.SavedAt > 0 {
			updatedAt = p.SavedAt
		}
		next[p.ID] = syncProfileTrack{Hash: hash, UpdatedAt: updatedAt}
		changed = true
	}
	tombstones := make(map[string]int64, len(sr.st.Tombstones))
	for _, t := range sr.st.Tombstones {
		tombstones[t.ID] = t.DeletedAt
	}
	for id := range sr.st.Profiles {
		if _, ok := next[id]; !ok {
			tombstones[id] = now
			changed = true
		}
	}
	for id, deletedAt := range tombstones {
		if _, present := next[id]; present {
			delete(tombstones, id)
			changed = true
		} else if time.UnixMilli(now).Sub(time.UnixMilli(deletedAt)) > syncTombstoneTTL {
			delete(tombstones, id)
			changed = true
		}
	}
	sr.st.Profiles = next
	sr.st.Tombstones = sortedTombstones(tombstones)
	sr.st.TrackedRevision = st.Revision
	if changed {
		_ = s.syncSaveStateLocked()
	}
	return syncHasLocalChanges(&sr.st)
}

func sortedTombstones(m map[string]int64) []syncTombstone {
	out := make([]syncTombstone, 0, len(m))
	for id, deletedAt := range m {
		out = append(out, syncTombstone{ID: id, DeletedAt: deletedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func syncHasLocalChanges(st *syncStateFile) bool {
	for id, track := range st.Profiles {
		if st.LastSynced[id] != track.Hash {
			return true
		}
	}
	for _, t := range st.Tombstones {
		if _, ok := st.LastSynced[t.ID]; ok {
			return true
		}
	}
	return false
}

// syncScheduleDirty schedules a push after syncDebounce when auto sync is
// enabled and unlocked.
func (s *server) syncScheduleDirty() {
	sr := &s.sync
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if !sr.cfg.AutoSync || sr.keys == nil || sr.trigger == nil {
		return
	}
	if sr.debounce != nil {
		sr.debounce.Stop()
	}
	trigger := sr.trigger
	sr.debounce = time.AfterFunc(syncDebounce, func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	})
}

type syncStatusView struct {
	Configured     bool                `json:"configured"`
	State          string              `json:"state"`
	Backend        *syncbackend.Config `json:"backend,omitempty"`
	VaultExists    *bool               `json:"vaultExists"`
	AutoSync       bool                `json:"autoSync"`
	Syncing        bool                `json:"syncing"`
	LastSyncedAt   int64               `json:"lastSyncedAt,omitempty"`
	LastError      string              `json:"lastError,omitempty"`
	RemoteRevision uint64              `json:"remoteRevision,omitempty"`
	DeviceID       string              `json:"deviceId"`
	LastResult     *syncResult         `json:"lastResult,omitempty"`
}

func (s *server) syncStatusView() syncStatusView {
	sr := &s.sync
	sr.mu.Lock()
	defer sr.mu.Unlock()
	view := syncStatusView{
		Configured: sr.cfg.Backend.Type != "", State: "unconfigured", VaultExists: sr.st.VaultExists,
		AutoSync: sr.cfg.AutoSync, Syncing: sr.syncing, LastSyncedAt: sr.st.LastSyncedAt, LastError: sr.st.LastError,
		RemoteRevision: sr.st.RemoteRevision, DeviceID: sr.cfg.DeviceID, LastResult: sr.st.LastResult,
	}
	if view.Configured {
		redacted := sr.cfg.Backend.Redacted()
		view.Backend = &redacted
		switch {
		case sr.keys != nil:
			view.State = "unlocked"
		case sr.st.VaultExists != nil && !*sr.st.VaultExists:
			view.State = "needs-setup"
		default:
			view.State = "locked"
		}
	}
	return view
}

func (s *server) syncEmit() { s.emit("sync", s.syncStatusView()) }

func syncHashes(profiles []mergeProfile) map[string]string {
	out := make(map[string]string, len(profiles))
	for _, p := range profiles {
		out[p.ID] = p.Hash
	}
	return out
}
