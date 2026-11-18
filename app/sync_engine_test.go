package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/syncbackend"
	"github.com/getkiko/kiko/app/syncvault"
)

// memBackend is an in-memory backend with ETag and conditional-write
// semantics; faults can be injected.
type memBackend struct {
	mu           sync.Mutex
	data         []byte
	version      int
	exists       bool
	noEtag       bool
	getErr       error
	conflictNext int
	puts         int
	deletes      int
}

func (m *memBackend) etag() string {
	if m.noEtag {
		return ""
	}
	return fmt.Sprintf("v%d", m.version)
}

func (m *memBackend) Get(context.Context) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, "", m.getErr
	}
	if !m.exists {
		return nil, "", syncbackend.ErrNotFound
	}
	return bytes.Clone(m.data), m.etag(), nil
}

func (m *memBackend) Put(_ context.Context, data []byte, cond syncbackend.Cond) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conflictNext > 0 {
		m.conflictNext--
		return "", syncbackend.ErrConflict
	}
	if cond.IfNoneMatch && m.exists {
		return "", syncbackend.ErrConflict
	}
	if cond.IfMatch != "" && (!m.exists || cond.IfMatch != m.etag()) {
		return "", syncbackend.ErrConflict
	}
	m.data, m.exists = bytes.Clone(data), true
	m.version++
	m.puts++
	return m.etag(), nil
}

func (m *memBackend) Delete(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data, m.exists = nil, false
	m.deletes++
	return nil
}

func (m *memBackend) Test(context.Context) error { return nil }

func (m *memBackend) snapshot() ([]byte, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return bytes.Clone(m.data), m.puts
}

func doc(n int) map[string]any { return map[string]any{"version": 1, "n": n} }

const testPassword = "correct horse battery"

func createVault(t *testing.T, s *server) {
	t.Helper()
	if err := s.syncUnlock(context.Background(), syncUnlockRequest{Password: testPassword, Confirm: testPassword, Create: true}); err != nil {
		t.Fatalf("create vault: %v", err)
	}
}

func unlockVault(t *testing.T, s *server) {
	t.Helper()
	if err := s.syncUnlock(context.Background(), syncUnlockRequest{Password: testPassword}); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

func mustSync(t *testing.T, s *server) syncResult {
	t.Helper()
	res, err := s.syncOnce(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res
}

func assertSameProfiles(t *testing.T, a, b *server) {
	t.Helper()
	an, bn := storeProfileNames(a), storeProfileNames(b)
	if fmt.Sprint(an) != fmt.Sprint(bn) {
		t.Fatalf("names differ: %v vs %v", an, bn)
	}
	for id := range an {
		if storeProfileDoc(a, id) != storeProfileDoc(b, id) {
			t.Fatalf("doc of %s differs: %s vs %s", id, storeProfileDoc(a, id), storeProfileDoc(b, id))
		}
	}
}

func TestSyncTwoDevicesConvergeAfterEdits(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(1)}, testProfile{ID: "p2", Name: "公司", Doc: doc(2)})
	createVault(t, a)
	if !backend.exists {
		t.Fatal("vault should exist after creation")
	}

	saveProfiles(t, b, "b1", testProfile{ID: "b1", Name: "手机", Doc: doc(9)})
	unlockVault(t, b)
	if got := storeProfileNames(b); len(got) != 3 || got["p1"] != "家里" || got["p2"] != "公司" || got["b1"] != "手机" {
		t.Fatalf("b after unlock=%v", got)
	}
	clock.Add(time.Minute)
	mustSync(t, a)
	assertSameProfiles(t, a, b)

	clock.Add(time.Minute)
	saveProfiles(t, b, "b1", testProfile{ID: "p1", Name: "家里-新", Doc: doc(1)}, testProfile{ID: "p2", Name: "公司", Doc: doc(2)}, testProfile{ID: "b1", Name: "手机", Doc: doc(9)})
	mustSync(t, b)
	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(1)}, testProfile{ID: "p2", Name: "公司", Doc: doc(22)}, testProfile{ID: "b1", Name: "手机", Doc: doc(9)})
	mustSync(t, a)
	clock.Add(time.Minute)
	mustSync(t, b)
	mustSync(t, a)
	assertSameProfiles(t, a, b)
	if got := storeProfileNames(a); got["p1"] != "家里-新" || storeProfileDoc(a, "p2") != `{"n":22,"version":1}` {
		t.Fatalf("edits from both sides must survive: %v %s", got, storeProfileDoc(a, "p2"))
	}
}

func TestSyncAppliesRemoteChangeToActiveDocAndEmitsStore(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	unlockVault(t, b)
	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(5)})
	mustSync(t, a)
	clock.Add(time.Minute)
	events := make(chan string, 8)
	b.mu.Lock()
	b.events[events] = struct{}{}
	b.mu.Unlock()
	mustSync(t, b)
	b.mu.Lock()
	active, live := activeProfileString(b.store.ActiveProfileID), string(b.store.Doc)
	b.mu.Unlock()
	if active != "p1" || !strings.Contains(live, `"n":5`) {
		t.Fatalf("active doc must follow the pulled profile: active=%q doc=%s", active, live)
	}
	sawStore := false
	for len(events) > 0 {
		if strings.Contains(<-events, `"type":"store"`) {
			sawStore = true
		}
	}
	if !sawStore {
		t.Fatal("applying remote data must emit a store event")
	}
}

func TestSyncTrueConflictKeepsBothVersions(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "共享", Doc: doc(1)})
	createVault(t, a)
	unlockVault(t, b)
	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "共享", Doc: doc(100)})
	saveProfiles(t, b, "p1", testProfile{ID: "p1", Name: "共享", Doc: doc(200)})
	clock.Add(time.Minute)
	mustSync(t, a)
	clock.Add(time.Minute)
	res := mustSync(t, b)
	if res.Conflicts != 1 {
		t.Fatalf("result=%+v", res)
	}
	names := storeProfileNames(b)
	copies := 0
	for id, name := range names {
		if strings.Contains(name, "冲突副本") {
			copies++
			if !strings.HasPrefix(name, "共享 (冲突副本 ") {
				t.Fatalf("copy name=%q id=%s", name, id)
			}
		}
	}
	if len(names) != 2 || copies != 1 {
		t.Fatalf("names=%v", names)
	}
	clock.Add(time.Minute)
	mustSync(t, a)
	assertSameProfiles(t, a, b)
	docs := map[string]bool{}
	for id := range storeProfileNames(a) {
		docs[storeProfileDoc(a, id)] = true
	}
	if !docs[`{"n":100,"version":1}`] || !docs[`{"n":200,"version":1}`] {
		t.Fatalf("both versions must be kept: %v", docs)
	}
}

func TestSyncDeletionPropagatesThroughTombstones(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "留下", Doc: doc(1)}, testProfile{ID: "p2", Name: "删掉", Doc: doc(2)})
	createVault(t, a)
	unlockVault(t, b)
	if len(storeProfileNames(b)) != 2 {
		t.Fatalf("b=%v", storeProfileNames(b))
	}

	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "留下", Doc: doc(1)})
	res := mustSync(t, a)
	if res.Deleted != 1 && res.Pushed != 0 {
		t.Logf("result=%+v", res)
	}
	clock.Add(time.Minute)
	mustSync(t, b)
	if got := storeProfileNames(b); len(got) != 1 || got["p1"] == "" {
		t.Fatalf("delete must reach b: %v", got)
	}
	for i := 0; i < 2; i++ {
		clock.Add(time.Minute)
		mustSync(t, a)
		mustSync(t, b)
	}
	if len(storeProfileNames(a)) != 1 || len(storeProfileNames(b)) != 1 {
		t.Fatalf("deleted profile must not come back: %v %v", storeProfileNames(a), storeProfileNames(b))
	}
}

func TestSyncRetriesAfterConflictingWrite(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(2)})
	_, before := backend.snapshot()
	backend.mu.Lock()
	backend.conflictNext = 1
	backend.mu.Unlock()
	res := mustSync(t, a)
	if _, after := backend.snapshot(); after != before+1 {
		t.Fatalf("puts %d -> %d, want exactly one successful write after the retry", before, after)
	}
	if res.Pushed != 1 {
		t.Fatalf("result=%+v", res)
	}

	backend.mu.Lock()
	backend.conflictNext = 10
	backend.mu.Unlock()
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(3)})
	if _, err := a.syncOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("persistent conflicts must surface an error, got %v", err)
	}
	if a.syncStatusView().LastError == "" {
		t.Fatal("last error must be recorded")
	}
}

func TestSyncRecreatesVaultAfterRemoteWasCleared(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	_ = backend.Delete(context.Background())
	clock.Add(time.Minute)
	mustSync(t, a)
	if !backend.exists {
		t.Fatal("sync should recreate the missing remote vault with the same key")
	}
	b := newSyncTestServer(t, backend, clock)
	unlockVault(t, b)
	if got := storeProfileNames(b); got["p1"] != "A" {
		t.Fatalf("b=%v", got)
	}
}

func TestSyncWrongPasswordDoesNotTouchRemote(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	before, puts := backend.snapshot()
	err := b.syncUnlock(context.Background(), syncUnlockRequest{Password: "totally wrong pass"})
	if !errors.Is(err, syncvault.ErrWrongPassword) {
		t.Fatalf("err=%v", err)
	}
	after, putsAfter := backend.snapshot()
	if !bytes.Equal(before, after) || puts != putsAfter {
		t.Fatal("a wrong password must not modify the remote")
	}
	if b.syncStatusView().State != "locked" {
		t.Fatalf("state=%s", b.syncStatusView().State)
	}
}

func TestSyncCreateRequiresMatchingStrongPassword(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	if err := a.syncUnlock(context.Background(), syncUnlockRequest{Password: testPassword}); !errors.Is(err, errSyncVaultMissing) {
		t.Fatalf("missing vault without create: %v", err)
	}
	if got := a.syncStatusView().State; got != "needs-setup" {
		t.Fatalf("state=%s", got)
	}
	if err := a.syncUnlock(context.Background(), syncUnlockRequest{Password: testPassword, Confirm: "different one!", Create: true}); !errors.Is(err, errSyncConfirm) {
		t.Fatalf("mismatch: %v", err)
	}
	if err := a.syncUnlock(context.Background(), syncUnlockRequest{Password: "short", Confirm: "short", Create: true}); err == nil {
		t.Fatal("short password must be rejected")
	}
	if backend.exists {
		t.Fatal("nothing may be written for rejected passwords")
	}
}

func TestSyncCreateLosesRaceToAnotherDevice(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	backend.mu.Lock()
	backend.conflictNext = 1
	backend.mu.Unlock()
	err := a.syncUnlock(context.Background(), syncUnlockRequest{Password: testPassword, Confirm: testPassword, Create: true})
	if !errors.Is(err, errSyncVaultExists) {
		t.Fatalf("err=%v", err)
	}
}

func TestSyncPasswordChangeLocksOtherDevices(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	unlockVault(t, b)

	if err := a.syncChangePassword(context.Background(), "a brand new password", "a brand new password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	clock.Add(time.Minute)
	mustSync(t, a)
	if _, err := b.syncOnce(context.Background()); !errors.Is(err, syncvault.ErrWrongKey) {
		t.Fatalf("other device must notice the rotation: %v", err)
	}
	if b.syncStatusView().State != "locked" {
		t.Fatalf("state=%s", b.syncStatusView().State)
	}
	if err := b.syncUnlock(context.Background(), syncUnlockRequest{Password: testPassword}); !errors.Is(err, syncvault.ErrWrongPassword) {
		t.Fatalf("old password must stop working: %v", err)
	}
	if err := b.syncUnlock(context.Background(), syncUnlockRequest{Password: "a brand new password"}); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if b.syncStatusView().State != "unlocked" {
		t.Fatalf("state=%s", b.syncStatusView().State)
	}
	if err := a.syncChangePassword(context.Background(), "short", "short"); err == nil {
		t.Fatal("weak new password must be rejected")
	}
}

func TestSyncResetClearsRemoteAndKeepsBackendConfig(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	if err := a.syncReset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.exists {
		t.Fatal("remote vault must be deleted")
	}
	view := a.syncStatusView()
	if view.State != "needs-setup" || !view.Configured {
		t.Fatalf("view=%+v", view)
	}
	if _, err := a.syncOnce(context.Background()); !errors.Is(err, errSyncLocked) {
		t.Fatalf("sync after reset: %v", err)
	}
	createVault(t, a)
	if !backend.exists {
		t.Fatal("a fresh vault can be created after reset")
	}
}

func TestSyncWorksWithoutETagsViaReadback(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{noEtag: true}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	unlockVault(t, b)
	clock.Add(time.Minute)
	saveProfiles(t, b, "p1", testProfile{ID: "p1", Name: "A2", Doc: doc(1)})
	mustSync(t, b)
	clock.Add(time.Minute)
	mustSync(t, a)
	assertSameProfiles(t, a, b)
}

func TestSyncApplyLocalRejectsStaleSnapshot(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	snap := a.syncSnapshot()
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(2)})
	applied, err := a.syncApplyLocal(snap, mergeOutput{Profiles: snap.profiles})
	if err != nil || applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
}

func TestSyncDeletedActiveProfileFallsBackToNewest(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "旧", Doc: doc(1)}, testProfile{ID: "p2", Name: "新", Doc: doc(2)})
	createVault(t, a)
	unlockVault(t, b)
	clock.Add(time.Minute)
	saveProfiles(t, b, "p1", testProfile{ID: "p1", Name: "旧", Doc: doc(1)})
	mustSync(t, b)
	clock.Add(time.Minute)
	saveProfiles(t, a, "p2", testProfile{ID: "p1", Name: "旧", Doc: doc(1)}, testProfile{ID: "p2", Name: "新", Doc: doc(2)})
	mustSync(t, a)
	a.mu.Lock()
	active := activeProfileString(a.store.ActiveProfileID)
	a.mu.Unlock()
	if active != "p1" || len(storeProfileNames(a)) != 1 {
		t.Fatalf("active=%q names=%v", active, storeProfileNames(a))
	}
}

func TestSyncLoopPushesAfterDebounceWhenAutoSyncIsOn(t *testing.T) {
	oldStart, oldDebounce, oldInterval := syncStartDelay, syncDebounce, syncInterval
	syncStartDelay, syncDebounce, syncInterval = time.Hour, 30*time.Millisecond, time.Hour
	t.Cleanup(func() { syncStartDelay, syncDebounce, syncInterval = oldStart, oldDebounce, oldInterval })

	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	a.sync.mu.Lock()
	a.sync.cfg.AutoSync = true
	a.sync.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.syncLoop(ctx)

	_, puts := backend.snapshot()
	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(2)})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, now := backend.snapshot(); now > puts {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("auto sync did not push the local change")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSyncLoopStaysIdleWhenAutoSyncIsOff(t *testing.T) {
	oldStart, oldDebounce := syncStartDelay, syncDebounce
	syncStartDelay, syncDebounce = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { syncStartDelay, syncDebounce = oldStart, oldDebounce })
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	_, puts := backend.snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.syncLoop(ctx)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(2)})
	time.Sleep(300 * time.Millisecond)
	if _, now := backend.snapshot(); now != puts {
		t.Fatal("no automatic push may happen while auto sync is off")
	}
}

func TestSyncFailureBacksOffExponentially(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "A", Doc: doc(1)})
	createVault(t, a)
	backend.mu.Lock()
	backend.getErr = errors.New("network down")
	backend.mu.Unlock()
	var last time.Duration
	for i := 0; i < 3; i++ {
		if _, err := a.syncOnce(context.Background()); err == nil {
			t.Fatal("expected failure")
		}
		a.sync.mu.Lock()
		wait := a.sync.nextTry.Sub(clock.Now())
		a.sync.mu.Unlock()
		if wait <= last {
			t.Fatalf("backoff must grow: %v then %v", last, wait)
		}
		last = wait
	}
	if last != 4*syncBackoffBase {
		t.Fatalf("third failure should wait %v, got %v", 4*syncBackoffBase, last)
	}
	backend.mu.Lock()
	backend.getErr = nil
	backend.mu.Unlock()
	mustSync(t, a)
	if a.sync.failures != 0 || a.syncStatusView().LastError != "" {
		t.Fatal("success must reset the failure state")
	}
}

// strayBackend is a memBackend whose old (pre-rename) backup an old client
// wrote after it was migrated.
type strayBackend struct {
	*memBackend
	stray     []byte
	base      []byte
	retireErr error
	retired   []string
	rechecks  int
}

func (b *strayBackend) PendingLegacy(context.Context, []byte) ([]byte, string, error) {
	if b.stray == nil {
		return nil, "", nil
	}
	return b.stray, "stray-etag", nil
}

func (b *strayBackend) RetireLegacy(_ context.Context, data []byte, etag string) error {
	if !bytes.Equal(data, b.stray) {
		return syncbackend.ErrConflict
	}
	if b.retireErr != nil {
		return b.retireErr
	}
	b.retired = append(b.retired, etag)
	b.stray = nil
	return nil
}

func (b *strayBackend) LegacyBase(context.Context) ([]byte, error) { return b.base, nil }

func (b *strayBackend) RecheckLegacy() { b.rechecks++ }

// sealStray seals a's current remote payload changed by edit, as an old client
// would have written it to the old backup.
func sealStray(t *testing.T, a *server, data []byte, edit func(*syncPayload)) []byte {
	t.Helper()
	a.sync.mu.Lock()
	keys := a.sync.keys
	a.sync.mu.Unlock()
	plain, hdr, err := keys.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseSyncPayload(plain)
	if err != nil {
		t.Fatal(err)
	}
	edit(payload)
	raw, _ := json.Marshal(payload)
	sealed, err := keys.Seal(hdr, hdr.Revision, raw)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func conflictCopies(s *server) int {
	n := 0
	for _, name := range storeProfileNames(s) {
		if strings.Contains(name, "冲突副本") {
			n++
		}
	}
	return n
}

// The moved marker keeps failing: the merged old backup is remembered and later
// rounds only retry the marker, so no conflict copy is added again.
func TestSyncStrayLegacyRetireFailingAddsNoCopies(t *testing.T) {
	clock := newTestClock()
	backend := &strayBackend{memBackend: &memBackend{}, retireErr: errors.New("offline")}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(1)})
	createVault(t, a)
	data, _ := backend.snapshot()
	backend.stray = sealStray(t, a, data, func(p *syncPayload) {
		p.Profiles[0].Doc, p.Profiles[0].UpdatedAt = json.RawMessage(`{"n":5,"version":1}`), clock.Now().Add(time.Minute).UnixMilli()
	})
	for round := range 5 {
		clock.Add(11 * time.Minute)
		mustSync(t, a)
		if n := len(storeProfileNames(a)); n != 2 || conflictCopies(a) != 1 {
			t.Fatalf("round %d: %d profiles, %v", round, n, storeProfileNames(a))
		}
	}
	a.sync.mu.Lock()
	merged := a.sync.st.LegacyMerged
	a.sync.mu.Unlock()
	if merged == "" {
		t.Fatal("merged old backup not remembered")
	}
	// The marker finally works: nothing is remembered any more.
	backend.retireErr = nil
	clock.Add(11 * time.Minute)
	mustSync(t, a)
	a.sync.mu.Lock()
	merged = a.sync.st.LegacyMerged
	a.sync.mu.Unlock()
	if merged != "" || len(backend.retired) != 1 || len(storeProfileNames(a)) != 2 {
		t.Fatalf("after retire: %q %v %v", merged, backend.retired, storeProfileNames(a))
	}
}

// A conflicting write retries the attempt, which merges the old backup again
// at once; its conflict copy is the same and not added twice.
func TestSyncStrayLegacyPutConflictAddsNoCopies(t *testing.T) {
	clock := newTestClock()
	backend := &strayBackend{memBackend: &memBackend{}}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(1)})
	createVault(t, a)
	data, _ := backend.snapshot()
	backend.stray = sealStray(t, a, data, func(p *syncPayload) {
		p.Profiles[0].Doc, p.Profiles[0].UpdatedAt = json.RawMessage(`{"n":5,"version":1}`), clock.Now().Add(time.Minute).UnixMilli()
	})
	backend.memBackend.mu.Lock()
	backend.memBackend.conflictNext = 1
	backend.memBackend.mu.Unlock()
	clock.Add(2 * time.Minute)
	mustSync(t, a)
	if backend.rechecks == 0 {
		t.Fatal("retried attempt did not ask to check the old backup again")
	}
	if n := len(storeProfileNames(a)); n != 2 || conflictCopies(a) != 1 || len(backend.retired) != 1 {
		t.Fatalf("%d profiles %v, retired %v", n, storeProfileNames(a), backend.retired)
	}
}

// With the copied version as the base, a profile only new devices changed and
// one only the old client changed both merge without a conflict copy.
func TestSyncStrayLegacyUsesMigrationBase(t *testing.T) {
	clock := newTestClock()
	backend := &strayBackend{memBackend: &memBackend{}}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(1)}, testProfile{ID: "p2", Name: "公司", Doc: doc(2)})
	createVault(t, a)
	base, _ := backend.snapshot()
	backend.base = base

	// A new device changes p1; the old client changes p2 in the old backup.
	clock.Add(time.Minute)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(7)}, testProfile{ID: "p2", Name: "公司", Doc: doc(2)})
	mustSync(t, a)
	clock.Add(time.Minute)
	backend.stray = sealStray(t, a, base, func(p *syncPayload) {
		for i := range p.Profiles {
			if p.Profiles[i].ID == "p2" {
				p.Profiles[i].Doc, p.Profiles[i].UpdatedAt = json.RawMessage(`{"n":22,"version":1}`), clock.Now().UnixMilli()
			}
		}
	})
	clock.Add(time.Minute)
	mustSync(t, a)
	if conflictCopies(a) != 0 || len(storeProfileNames(a)) != 2 {
		t.Fatalf("profiles %v", storeProfileNames(a))
	}
	if storeProfileDoc(a, "p1") != `{"n":7,"version":1}` || storeProfileDoc(a, "p2") != `{"n":22,"version":1}` {
		t.Fatalf("p1 %s p2 %s", storeProfileDoc(a, "p1"), storeProfileDoc(a, "p2"))
	}
	if len(backend.retired) != 1 {
		t.Fatalf("retired %v", backend.retired)
	}
}

// Updates an old client wrote to the old backup after migration are merged into
// the current backup before the old one is retired: a profile only it has is
// added, and a profile it changed differently is kept as a conflict copy.
func TestSyncMergesStrayLegacyBackup(t *testing.T) {
	clock := newTestClock()
	backend := &strayBackend{memBackend: &memBackend{}}
	a := newSyncTestServer(t, backend, clock)
	saveProfiles(t, a, "p1", testProfile{ID: "p1", Name: "家里", Doc: doc(1)})
	createVault(t, a)

	// The old client's version: p1 changed, p9 added.
	data, _ := backend.snapshot()
	a.sync.mu.Lock()
	keys := a.sync.keys
	a.sync.mu.Unlock()
	plain, hdr, err := keys.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseSyncPayload(plain)
	if err != nil {
		t.Fatal(err)
	}
	later := clock.Now().Add(time.Minute).UnixMilli()
	payload.Profiles = []syncPayloadProfile{
		{ID: "p1", Name: "家里", UpdatedAt: later, Doc: json.RawMessage(`{"n":5,"version":1}`)},
		{ID: "p9", Name: "旧设备", UpdatedAt: later, Doc: json.RawMessage(`{"n":9,"version":1}`)},
	}
	raw, _ := json.Marshal(payload)
	if backend.stray, err = keys.Seal(hdr, hdr.Revision, raw); err != nil {
		t.Fatal(err)
	}

	clock.Add(2 * time.Minute)
	mustSync(t, a)
	if len(backend.retired) != 1 || backend.retired[0] != "stray-etag" {
		t.Fatalf("old backup not retired: %v", backend.retired)
	}
	names := storeProfileNames(a)
	if names["p9"] != "旧设备" || storeProfileDoc(a, "p1") != `{"n":5,"version":1}` || len(names) != 3 {
		t.Fatalf("merged profiles %v, p1 %s", names, storeProfileDoc(a, "p1"))
	}
	// The current backup holds the merge.
	current, _ := backend.snapshot()
	plain, _, err = keys.Open(current)
	if err != nil {
		t.Fatal(err)
	}
	if merged, _ := parseSyncPayload(plain); len(merged.Profiles) != 3 {
		t.Fatalf("remote profiles %d", len(merged.Profiles))
	}
}
