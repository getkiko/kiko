package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSecretService is an in-memory Secret Service; all connections share one state.
type fakeSecretService struct {
	unavailable    bool
	noCollection   bool // service runs but has no default collection
	locked         bool
	promptDone     bool // collection unlocks once the unlock prompt completes
	dismiss        bool // user dismisses the prompt
	waitErr        error
	secret         []byte
	hasItem        bool
	itemAttrs      map[string]string // attributes of the main item; nil means the current unscoped attributes
	hasLegacy      bool              // unscoped pre-rename item (application=poppy, e.g. written by another config directory)
	legacySecret   []byte
	legacyReplaced bool
	hasOld         bool // another item written before the rename (e.g. this data directory's scoped poppy item)
	oldAttrs       map[string]string
	oldSecret      []byte
	saved          [][]byte // values passed to CreateItem (to check they are zeroed after the call)
	promptCalls    int
	createCalls    int
	deleteCalls    int
	lastAttrs      map[string]string
	lastLabel      string
	extra          []*fakeSecretItem // further items, e.g. several data directories' pre-rename items
}

type fakeSecretItem struct {
	path   string
	attrs  map[string]string
	secret []byte
}

type fakeSecretConn struct{ svc *fakeSecretService }

func (f *fakeSecretService) dial() (secretBusConn, error) {
	if f.unavailable {
		return nil, errSecretServiceUnavailable
	}
	return &fakeSecretConn{svc: f}, nil
}

func (c *fakeSecretConn) Close() {}
func (c *fakeSecretConn) OpenSession() (string, error) {
	return "/org/freedesktop/secrets/session/s1", nil
}
func (c *fakeSecretConn) CollectionLocked(string) (bool, error) {
	if c.svc.noCollection {
		return false, errSecretNoCollection
	}
	return c.svc.locked, nil
}
func (c *fakeSecretConn) Unlock([]string) (string, error) {
	return "/org/freedesktop/secrets/prompt/p1", nil
}
func (c *fakeSecretConn) WaitPrompt(string, time.Duration) (bool, error) {
	c.svc.promptCalls++
	if c.svc.waitErr != nil {
		return false, c.svc.waitErr
	}
	if c.svc.dismiss {
		return true, nil
	}
	if c.svc.promptDone {
		c.svc.locked = false
	}
	return false, nil
}

// matchAttrs reports whether an item with attributes have matches a search for
// query: as in the Secret Service specification, every searched attribute must
// be present with the same value, and the item may have more attributes.
func matchAttrs(query, have map[string]string) bool {
	for k, v := range query {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func sameAttrs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (c *fakeSecretConn) SearchItems(collection string, attrs map[string]string) ([]string, error) {
	if collection != secretServiceCollection || (attrs["application"] != "kiko" && attrs["application"] != "poppy") || attrs["purpose"] != "mitm-root-ca-key" {
		return nil, errors.New("bad search")
	}
	var items []string
	if c.svc.hasItem {
		have := c.svc.itemAttrs
		if have == nil {
			have = secretServiceAttributes
		}
		if matchAttrs(attrs, have) {
			items = append(items, "/item/1")
		}
	}
	if c.svc.hasLegacy && matchAttrs(attrs, legacySecretServiceAttributes) {
		items = append(items, "/item/legacy")
	}
	for _, item := range c.svc.extra {
		if matchAttrs(attrs, item.attrs) {
			items = append(items, item.path)
		}
	}
	if c.svc.hasOld && matchAttrs(attrs, c.svc.oldAttrs) {
		items = append(items, "/item/old")
	}
	return items, nil
}
func (c *fakeSecretConn) ItemAttributes(item string) (map[string]string, error) {
	switch item {
	case "/item/legacy":
		return legacySecretServiceAttributes, nil
	case "/item/old":
		return c.svc.oldAttrs, nil
	case "/item/1":
		if c.svc.itemAttrs != nil {
			return c.svc.itemAttrs, nil
		}
		return secretServiceAttributes, nil
	}
	for _, extra := range c.svc.extra {
		if extra.path == item {
			return extra.attrs, nil
		}
	}
	return nil, errors.New("no such item")
}

func (c *fakeSecretConn) GetSecret(item, _ string) ([]byte, error) {
	for _, extra := range c.svc.extra {
		if extra.path == item {
			return append([]byte(nil), extra.secret...), nil
		}
	}
	if item == "/item/legacy" {
		return append([]byte(nil), c.svc.legacySecret...), nil
	}
	if item == "/item/old" {
		return append([]byte(nil), c.svc.oldSecret...), nil
	}
	return append([]byte(nil), c.svc.secret...), nil
}
func (c *fakeSecretConn) CreateItem(collection, label string, attrs map[string]string, value []byte, session string) (string, string, error) {
	c.svc.createCalls++
	c.svc.lastAttrs, c.svc.lastLabel = attrs, label
	c.svc.saved = append(c.svc.saved, value)
	if c.svc.hasLegacy && sameAttrs(attrs, legacySecretServiceAttributes) {
		c.svc.legacyReplaced = true
	}
	c.svc.secret = append([]byte(nil), value...)
	c.svc.hasItem = true
	c.svc.itemAttrs = attrs
	return "/item/1", "/", nil
}
func (c *fakeSecretConn) DeleteItem(item string) (string, error) {
	c.svc.deleteCalls++
	for i, extra := range c.svc.extra {
		if extra.path == item {
			c.svc.extra = append(c.svc.extra[:i], c.svc.extra[i+1:]...)
			return "/", nil
		}
	}
	if item == "/item/legacy" {
		c.svc.hasLegacy, c.svc.legacySecret = false, nil
		return "/", nil
	}
	if item == "/item/old" {
		c.svc.hasOld, c.svc.oldSecret = false, nil
		return "/", nil
	}
	c.svc.hasItem, c.svc.secret = false, nil
	return "/", nil
}

func TestSecretServiceRoundTrip(t *testing.T) {
	svc := &fakeSecretService{}
	store := newSecretServiceStore(svc.dial)
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load empty: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("delete empty: %v", err)
	}
	key := testSecretKey(0x31)
	if err := store.Save(key); err != nil {
		t.Fatal(err)
	}
	if svc.lastLabel != "Kiko Root CA Key" || svc.lastAttrs["application"] != "kiko" {
		t.Fatalf("label/attrs = %q %v", svc.lastLabel, svc.lastAttrs)
	}
	// The copy passed to the service is zeroed after the call; the caller's data is untouched.
	if len(svc.saved) != 1 || !bytes.Equal(svc.saved[0], make([]byte, len(key))) {
		t.Fatal("service copy not zeroed")
	}
	if !bytes.Equal(key, testSecretKey(0x31)) {
		t.Fatal("caller key modified")
	}
	got, err := store.Load()
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load = %x, %v", got, err)
	}
	if err := store.Delete(); err != nil || svc.hasItem {
		t.Fatalf("delete: %v", err)
	}
}

func TestSecretServiceUnlockPrompt(t *testing.T) {
	svc := &fakeSecretService{locked: true, promptDone: true, hasItem: true, secret: testSecretKey(0x41)}
	store := newSecretServiceStore(svc.dial)
	got, err := store.Load()
	if err != nil || !bytes.Equal(got, testSecretKey(0x41)) || svc.promptCalls != 1 {
		t.Fatalf("load = %v, calls %d", err, svc.promptCalls)
	}

	dismissed := &fakeSecretService{locked: true, dismiss: true, hasItem: true, secret: testSecretKey(1)}
	_, err = newSecretServiceStore(dismissed.dial).Load()
	if !errors.Is(err, errSecretPromptDismissed) || errors.Is(err, errSecretNotFound) {
		t.Fatalf("dismissed: %v", err)
	}
	if err := newSecretServiceStore(dismissed.dial).Save(testSecretKey(2)); !errors.Is(err, errSecretPromptDismissed) || dismissed.createCalls != 0 {
		t.Fatalf("save dismissed: %v", err)
	}

	stuck := &fakeSecretService{locked: true, hasItem: true, secret: testSecretKey(1)}
	if _, err := newSecretServiceStore(stuck.dial).Load(); !errors.Is(err, errSecretLocked) {
		t.Fatalf("still locked: %v", err)
	}

	timeout := &fakeSecretService{locked: true, waitErr: errors.New("timeout")}
	if _, err := newSecretServiceStore(timeout.dial).Load(); err == nil || errors.Is(err, errSecretNotFound) {
		t.Fatalf("timeout: %v", err)
	}
}

func newTestSticky(dir string, svc *fakeSecretService, prefer bool) *stickySecretStore {
	return newStickySecretStore(dir, newPEMSecretStore(dir), newSecretServiceStore(svc.dial), func() bool { return prefer })
}

func readBackendMarker(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "mitm", "ca.key.backend"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(data))
}

func TestStickyPrefersSecretService(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{}
	store := newTestSticky(dir, svc, true)
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load: %v", err)
	}
	key := testSecretKey(0x51)
	if err := store.Save(key); err != nil {
		t.Fatal(err)
	}
	if readBackendMarker(t, dir) != "secretservice" {
		t.Fatal("marker")
	}
	if _, err := os.Stat(filepath.Join(dir, "mitm", "ca.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key file written: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "mitm", "ca.key.backend")); info.Mode().Perm() != 0o600 {
		t.Fatalf("marker perm %v", info.Mode().Perm())
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load: %v", err)
	}
	// The service becomes unavailable: a non-NotFound error, no fallback to the
	// file, and no new root can be saved.
	svc.unavailable = true
	if _, err := store.Load(); !errors.Is(err, errSecretServiceUnavailable) || errors.Is(err, errSecretNotFound) {
		t.Fatalf("unavailable load: %v", err)
	}
	if err := store.Save(testSecretKey(0x52)); !errors.Is(err, errSecretServiceUnavailable) {
		t.Fatalf("unavailable save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mitm", "ca.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("silent file fallback")
	}
	if err := store.Delete(); !errors.Is(err, errSecretServiceUnavailable) || readBackendMarker(t, dir) == "" {
		t.Fatalf("delete unavailable: %v", err)
	}
	svc.unavailable = false
	if err := store.Delete(); err != nil || readBackendMarker(t, dir) != "" || svc.hasItem {
		t.Fatalf("delete: %v", err)
	}
}

func TestStickyHeadlessUsesFile(t *testing.T) {
	for name, tc := range map[string]struct {
		svc    *fakeSecretService
		prefer bool
	}{
		"no service": {&fakeSecretService{unavailable: true}, true},
		"no display": {&fakeSecretService{}, false},
	} {
		dir := t.TempDir()
		store := newTestSticky(dir, tc.svc, tc.prefer)
		key := testSecretKey(0x61)
		if err := store.Save(key); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if readBackendMarker(t, dir) != "file" || tc.svc.createCalls != 0 {
			t.Fatalf("%s: marker %q", name, readBackendMarker(t, dir))
		}
		info, err := os.Stat(filepath.Join(dir, "mitm", "ca.key"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: key file: %v", name, err)
		}
		// Even once the service is available it is not switched to: the key is still
		// read from the file.
		tc.svc.unavailable = false
		store = newTestSticky(dir, tc.svc, true)
		if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
			t.Fatalf("%s: load: %v", name, err)
		}
		if err := store.Save(testSecretKey(0x62)); err != nil || tc.svc.createCalls != 0 {
			t.Fatalf("%s: save: %v", name, err)
		}
		if err := store.Delete(); err != nil || readBackendMarker(t, dir) != "" {
			t.Fatalf("%s: delete: %v", name, err)
		}
		if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
			t.Fatalf("%s: load after delete: %v", name, err)
		}
	}
}

// An install from before the upgrade that only has a ca.key file: after reading,
// the backend is recorded as file and the key is not migrated to the service.
func TestStickyLegacyFileKey(t *testing.T) {
	dir := t.TempDir()
	key := testSecretKey(0x71)
	if err := newPEMSecretStore(dir).Save(key); err != nil {
		t.Fatal(err)
	}
	svc := &fakeSecretService{}
	store := newTestSticky(dir, svc, true)
	got, err := store.Load()
	if err != nil || !bytes.Equal(got, key) || readBackendMarker(t, dir) != "file" {
		t.Fatalf("load: %v marker %q", err, readBackendMarker(t, dir))
	}
	if svc.createCalls != 0 {
		t.Fatal("migrated")
	}
}

// The marker is lost but the service holds a scoped key: it is recovered and
// recorded again; unscoped legacy items are not adopted.
func TestStickyRecoversServiceKeyWithoutMarker(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{hasItem: true, secret: testSecretKey(0x81), itemAttrs: scopedTestAttrs(dir)}
	store := newScopedTestSticky(dir, svc)
	got, err := store.Load()
	if err != nil || !bytes.Equal(got, testSecretKey(0x81)) || readBackendMarker(t, dir) != "secretservice" {
		t.Fatalf("load: %v", err)
	}
	dir2 := t.TempDir()
	legacy := &fakeSecretService{hasLegacy: true, legacySecret: testSecretKey(0x82)}
	if _, err := newScopedTestSticky(dir2, legacy).Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("legacy adopted without marker: %v", err)
	}
}

func scopedTestAttrs(dir string) map[string]string {
	attrs := map[string]string{"data_dir_hash": secretServiceScope(dir)}
	for k, v := range secretServiceAttributes {
		attrs[k] = v
	}
	return attrs
}

// newScopedTestSticky wires stores the same way as newSecretStore: legacy items
// are only read when the marker says secretservice.
func newScopedTestSticky(dir string, svc *fakeSecretService) *stickySecretStore {
	service := newSecretServiceStore(svc.dial)
	sticky := newStickySecretStore(dir, newPEMSecretStore(dir), service, func() bool { return true })
	service.scoped(dir, func() bool {
		backend, err := sticky.backend()
		return err == nil && backend == secretBackendService
	})
	return sticky
}

func TestSecretServiceScopedAttributes(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if secretServiceScope(a) == secretServiceScope(b) || len(secretServiceScope(a)) != 16 {
		t.Fatal("scope hashes")
	}
	// Two config directories save separately without overwriting each other or the
	// unscoped legacy item.
	svc := &fakeSecretService{hasLegacy: true, legacySecret: testSecretKey(0xd0)}
	sa, sb := newScopedTestSticky(a, svc), newScopedTestSticky(b, svc)
	if err := sa.Save(testSecretKey(0xd1)); err != nil {
		t.Fatal(err)
	}
	if svc.lastAttrs["data_dir_hash"] != secretServiceScope(a) || svc.legacyReplaced {
		t.Fatalf("attrs %v replaced %v", svc.lastAttrs, svc.legacyReplaced)
	}
	// b has no marker: it sees neither a's item nor adopts the legacy item.
	if _, err := sb.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("b load: %v", err)
	}
	if got, err := sa.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xd1)) {
		t.Fatalf("a load: %v", err)
	}
	if !svc.hasLegacy || !bytes.Equal(svc.legacySecret, testSecretKey(0xd0)) {
		t.Fatal("legacy item touched")
	}
}

// An old install with secretservice recorded: without a scoped item the legacy
// item is read, and saving writes a new scoped item.
func TestSecretServiceLegacyAdoptedWithMarker(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{hasLegacy: true, legacySecret: testSecretKey(0xe1)}
	store := newScopedTestSticky(dir, svc)
	if err := store.writeMarker(secretBackendService); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xe1)) {
		t.Fatalf("load: %v", err)
	}
	if err := store.Save(testSecretKey(0xe2)); err != nil {
		t.Fatal(err)
	}
	if svc.legacyReplaced || !bytes.Equal(svc.legacySecret, testSecretKey(0xe1)) {
		t.Fatal("legacy overwritten")
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xe2)) {
		t.Fatalf("load after save: %v", err)
	}
}

func TestStickySaveDismissedDoesNotFallBack(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{locked: true, dismiss: true}
	store := newTestSticky(dir, svc, true)
	if err := store.Save(testSecretKey(0x91)); !errors.Is(err, errSecretPromptDismissed) {
		t.Fatalf("save: %v", err)
	}
	if readBackendMarker(t, dir) != "" {
		t.Fatal("marker written")
	}
	if _, err := os.Stat(filepath.Join(dir, "mitm", "ca.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file fallback")
	}
}

func TestStickyCorruptMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mitm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mitm", "ca.key.backend"), []byte("bogus"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestSticky(dir, &fakeSecretService{}, true)
	if _, err := store.Load(); !errors.Is(err, errSecretCorrupt) {
		t.Fatalf("load: %v", err)
	}
	if err := store.Delete(); err != nil || readBackendMarker(t, dir) != "" {
		t.Fatalf("delete: %v", err)
	}
}

// The service runs but has no default collection: with no recorded backend the
// store falls back to the file.
func TestStickyNoDefaultCollectionFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{noCollection: true}
	store := newTestSticky(dir, svc, true)
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load: %v", err)
	}
	key := testSecretKey(0xa1)
	if err := store.Save(key); err != nil {
		t.Fatal(err)
	}
	if readBackendMarker(t, dir) != "file" || svc.createCalls != 0 {
		t.Fatalf("marker %q", readBackendMarker(t, dir))
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load: %v", err)
	}
}

// With the secretservice backend recorded, a missing default collection still
// fails closed.
func TestStickyRecordedServiceNoCollectionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{}
	store := newTestSticky(dir, svc, true)
	if err := store.Save(testSecretKey(0xa2)); err != nil {
		t.Fatal(err)
	}
	svc.noCollection = true
	if _, err := store.Load(); !errors.Is(err, errSecretNoCollection) || errors.Is(err, errSecretNotFound) {
		t.Fatalf("load: %v", err)
	}
	if err := store.Save(testSecretKey(0xa3)); !errors.Is(err, errSecretNoCollection) {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mitm", "ca.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("silent file fallback")
	}
}

func writeCorruptMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "mitm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mitm", "ca.key.backend"), []byte("bogus"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The marker is corrupt and neither backend has a key: Save may proceed and
// rewrite the marker.
func TestStickyCorruptMarkerSaveWhenNoKey(t *testing.T) {
	for name, svc := range map[string]*fakeSecretService{
		"empty service": {},
		"no collection": {noCollection: true},
		"unavailable":   {unavailable: true},
	} {
		dir := t.TempDir()
		writeCorruptMarker(t, dir)
		store := newTestSticky(dir, svc, true)
		key := testSecretKey(0xb1)
		if err := store.Save(key); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if marker := readBackendMarker(t, dir); marker != "file" && marker != "secretservice" {
			t.Fatalf("%s: marker %q", name, marker)
		}
		if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
			t.Fatalf("%s: load: %v", name, err)
		}
	}
}

// The marker is corrupt but a backend still has a key: Save fails closed and
// overwrites nothing.
func TestStickyCorruptMarkerSaveFailsClosedWithKey(t *testing.T) {
	dir := t.TempDir()
	writeCorruptMarker(t, dir)
	if err := newPEMSecretStore(dir).Save(testSecretKey(0xb2)); err != nil {
		t.Fatal(err)
	}
	store := newTestSticky(dir, &fakeSecretService{}, true)
	if err := store.Save(testSecretKey(0xb3)); !errors.Is(err, errSecretCorrupt) {
		t.Fatalf("file key: %v", err)
	}
	dir2 := t.TempDir()
	writeCorruptMarker(t, dir2)
	svc := &fakeSecretService{hasItem: true, secret: testSecretKey(0xb4)}
	if err := newTestSticky(dir2, svc, true).Save(testSecretKey(0xb5)); !errors.Is(err, errSecretCorrupt) || svc.createCalls != 0 {
		t.Fatalf("service key: %v", err)
	}
	// The service is locked and the prompt dismissed: absence of a key cannot be
	// confirmed, so fail closed.
	dir3 := t.TempDir()
	writeCorruptMarker(t, dir3)
	locked := &fakeSecretService{locked: true, dismiss: true}
	if err := newTestSticky(dir3, locked, true).Save(testSecretKey(0xb6)); !errors.Is(err, errSecretCorrupt) || locked.createCalls != 0 {
		t.Fatalf("locked service: %v", err)
	}
}

// This data directory's scoped pre-rename item (application=poppy) is migrated to
// a kiko item on read and the old item is deleted; the root CA key is unchanged
// and other data directories' legacy items are left alone.
func TestSecretServiceMigratesScopedPoppyItem(t *testing.T) {
	dir := t.TempDir()
	oldAttrs := scopedSecretAttributes(legacySecretServiceAttributes, dir)
	svc := &fakeSecretService{hasOld: true, oldAttrs: oldAttrs, oldSecret: testSecretKey(0xf1)}
	store := newScopedTestSticky(dir, svc)
	if err := store.writeMarker(secretBackendService); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || !bytes.Equal(got, testSecretKey(0xf1)) {
		t.Fatalf("load: %x %v", got, err)
	}
	if svc.hasOld || !svc.hasItem || !sameAttrs(svc.itemAttrs, scopedTestAttrs(dir)) || svc.lastLabel != "Kiko Root CA Key" {
		t.Fatalf("not migrated: old %v item %v attrs %v", svc.hasOld, svc.hasItem, svc.itemAttrs)
	}
	if !bytes.Equal(svc.secret, testSecretKey(0xf1)) {
		t.Fatal("migrated secret differs")
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xf1)) || svc.createCalls != 1 {
		t.Fatalf("second load: %v, creates %d", err, svc.createCalls)
	}

	// Without a marker the item is likewise recovered and migrated.
	dir2 := t.TempDir()
	svc2 := &fakeSecretService{hasOld: true, oldAttrs: scopedSecretAttributes(legacySecretServiceAttributes, dir2), oldSecret: testSecretKey(0xf2)}
	store2 := newScopedTestSticky(dir2, svc2)
	if got, err := store2.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xf2)) || svc2.hasOld || readBackendMarker(t, dir2) != "secretservice" {
		t.Fatalf("without marker: %v old %v", err, svc2.hasOld)
	}

	// Another data directory's legacy item is neither migrated nor deleted.
	other := &fakeSecretService{hasOld: true, oldAttrs: scopedSecretAttributes(legacySecretServiceAttributes, t.TempDir()), oldSecret: testSecretKey(0xf3)}
	if _, err := newScopedTestSticky(t.TempDir(), other).Load(); !errors.Is(err, errSecretNotFound) || !other.hasOld {
		t.Fatalf("other directory: %v old %v", err, other.hasOld)
	}
}

// Delete also removes this data directory's pre-rename item.
func TestSecretServiceDeleteRemovesScopedPoppyItem(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{
		hasItem: true, secret: testSecretKey(0xf4), itemAttrs: scopedTestAttrs(dir),
		hasOld: true, oldAttrs: scopedSecretAttributes(legacySecretServiceAttributes, dir), oldSecret: testSecretKey(0xf5),
		hasLegacy: true, legacySecret: testSecretKey(0xf6),
	}
	store := newScopedTestSticky(dir, svc)
	if err := store.writeMarker(secretBackendService); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if svc.hasItem || svc.hasOld || !svc.hasLegacy {
		t.Fatalf("item %v old %v unscoped legacy %v", svc.hasItem, svc.hasOld, svc.hasLegacy)
	}
}

// A data directory renamed with Kiko (sbcfg-server to kiko-server, the desktop
// app's Poppy to Kiko) finds its item scoped by the old path's hash, migrates it
// to the new scope and deletes the old item.
func TestSecretServiceMigratesRenamedDataDir(t *testing.T) {
	for _, tc := range []struct{ current, old string }{
		{"kiko-server", "sbcfg-server"},
		{filepath.Join("Kiko", "kiko-server"), filepath.Join("Poppy", "sbcfg-server")},
		{filepath.Join("Kiko", "kiko-server"), filepath.Join("Poppy", "kiko-server")},
		{filepath.Join("Kiko", "kiko-server"), filepath.Join("Kiko", "sbcfg-server")},
		{filepath.Join("Kiko", "kiko-server"), filepath.Join("sbcfg-desktop", "sbcfg-server")},
	} {
		for _, base := range []map[string]string{legacySecretServiceAttributes, secretServiceAttributes} {
			root := t.TempDir()
			dir, oldDir := filepath.Join(root, tc.current), filepath.Join(root, tc.old)
			svc := &fakeSecretService{hasOld: true, oldAttrs: scopedSecretAttributes(base, oldDir), oldSecret: testSecretKey(0xc1)}
			store := newScopedTestSticky(dir, svc)
			if err := store.writeMarker(secretBackendService); err != nil {
				t.Fatal(err)
			}
			got, err := store.Load()
			if err != nil || !bytes.Equal(got, testSecretKey(0xc1)) {
				t.Fatalf("%s from %s (%s): load %v", tc.current, tc.old, base["application"], err)
			}
			if svc.hasOld || !svc.hasItem || !sameAttrs(svc.itemAttrs, scopedTestAttrs(dir)) {
				t.Fatalf("%s from %s (%s): old %v item %v attrs %v", tc.current, tc.old, base["application"], svc.hasOld, svc.hasItem, svc.itemAttrs)
			}
		}
	}

	// A sibling data directory that was never this one's is left alone.
	root := t.TempDir()
	other := &fakeSecretService{hasOld: true, oldAttrs: scopedSecretAttributes(legacySecretServiceAttributes, filepath.Join(root, "other", "sbcfg-server")), oldSecret: testSecretKey(0xc2)}
	if _, err := newScopedTestSticky(filepath.Join(root, "Kiko", "kiko-server"), other).Load(); !errors.Is(err, errSecretNotFound) || !other.hasOld {
		t.Fatalf("unrelated directory: %v old %v", err, other.hasOld)
	}
}

func TestLegacySecretScopeDirs(t *testing.T) {
	root := t.TempDir()
	if dirs := legacySecretScopeDirs(filepath.Join(root, "custom")); dirs != nil {
		t.Fatalf("custom directory: %v", dirs)
	}
	got := legacySecretScopeDirs(filepath.Join(root, "kiko-server"))
	if len(got) != 1 || got[0] != filepath.Join(root, "sbcfg-server") {
		t.Fatalf("server directory: %v", got)
	}
	got = legacySecretScopeDirs(filepath.Join(root, "Kiko", "kiko-server") + "/")
	want := []string{
		filepath.Join(root, "Kiko", "sbcfg-server"),
		filepath.Join(root, "Poppy", "sbcfg-server"), filepath.Join(root, "Poppy", "kiko-server"),
		filepath.Join(root, "sbcfg-desktop", "sbcfg-server"), filepath.Join(root, "sbcfg-desktop", "kiko-server"),
	}
	if len(got) != len(want) {
		t.Fatalf("desktop directory: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("desktop directory: %v, want %v", got, want)
		}
	}
}

// The unscoped fallback searches attribute subsets, which also match other
// data directories' scoped items; it reads the items' attributes and adopts
// only an item without data_dir_hash.
func TestSecretServiceUnscopedFallbackSkipsScopedItems(t *testing.T) {
	dir := t.TempDir()
	svc := &fakeSecretService{
		hasOld: true, oldAttrs: scopedSecretAttributes(legacySecretServiceAttributes, t.TempDir()), oldSecret: testSecretKey(0xc4),
	}
	store := newScopedTestSticky(dir, svc)
	if err := store.writeMarker(secretBackendService); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("another directory's item adopted: %v", err)
	}
	svc.hasLegacy, svc.legacySecret = true, testSecretKey(0xc3)
	if got, err := store.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xc3)) {
		t.Fatalf("unscoped item: %x %v", got, err)
	}
	if !svc.hasLegacy || !svc.hasOld || svc.createCalls != 0 {
		t.Fatal("items touched")
	}
}

// With several pre-rename items, migration adopts the one holding the key of
// this directory's root certificate and deletes only that one; when none fits,
// nothing is written or deleted.
func TestSecretServiceMigrateChecksRootKey(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Kiko", "kiko-server")
	mine, other := testSecretKey(0xa7), testSecretKey(0xa8)
	newSvc := func() *fakeSecretService {
		return &fakeSecretService{extra: []*fakeSecretItem{
			{path: "/item/a", attrs: scopedSecretAttributes(legacySecretServiceAttributes, filepath.Join(root, "Poppy", "sbcfg-server")), secret: other},
			{path: "/item/b", attrs: scopedSecretAttributes(legacySecretServiceAttributes, filepath.Join(root, "sbcfg-desktop", "sbcfg-server")), secret: mine},
		}}
	}
	open := func(svc *fakeSecretService, matches func([]byte) bool) *stickySecretStore {
		service := newSecretServiceStore(svc.dial)
		sticky := newStickySecretStore(dir, newPEMSecretStore(dir), service, func() bool { return true })
		service.scoped(dir, func() bool { return true })
		service.keyMatches = matches
		return sticky
	}

	svc := newSvc()
	got, err := open(svc, func(k []byte) bool { return bytes.Equal(k, mine) }).Load()
	if err != nil || !bytes.Equal(got, mine) {
		t.Fatalf("load: %x %v", got, err)
	}
	if len(svc.extra) != 1 || svc.extra[0].path != "/item/a" || !sameAttrs(svc.itemAttrs, scopedTestAttrs(dir)) || !bytes.Equal(svc.secret, mine) {
		t.Fatalf("extra %v item %v", svc.extra, svc.itemAttrs)
	}

	svc = newSvc()
	if _, err := open(svc, func([]byte) bool { return false }).Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("no fitting key: %v", err)
	}
	if len(svc.extra) != 2 || svc.createCalls != 0 || svc.deleteCalls != 0 {
		t.Fatalf("items touched: %d left, %d creates, %d deletes", len(svc.extra), svc.createCalls, svc.deleteCalls)
	}
}

// The root key check reads the certificate saved in the data directory.
func TestMITMRootKeyMatcher(t *testing.T) {
	dir := t.TempDir()
	certDER, keyDER, err := generateMITMRoot(time.Now())
	if err != nil {
		t.Skipf("root generator: %v", err)
	}
	matches := mitmRootKeyMatcher(dir)
	if matches(keyDER) {
		t.Fatal("matched without a certificate")
	}
	if err := os.MkdirAll(filepath.Join(dir, "mitm"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, _ := json.Marshal(mitmCAFile{Version: mitmCAFileVersion, Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))})
	if err := os.WriteFile(filepath.Join(dir, "mitm", "ca.json"), file, 0o600); err != nil {
		t.Fatal(err)
	}
	if !matches(keyDER) {
		t.Fatal("own key does not match")
	}
	_, otherKey, _ := generateMITMRoot(time.Now())
	if matches(otherKey) {
		t.Fatal("another key matches")
	}
}
