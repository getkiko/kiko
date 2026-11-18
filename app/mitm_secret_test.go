package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testSecretKey(fill byte) []byte {
	key := make([]byte, 138)
	for i := range key {
		key[i] = fill ^ byte(i)
	}
	return key
}

func TestFileSecretStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := newPEMSecretStore(dir)

	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load empty: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("delete empty: %v", err)
	}

	first := testSecretKey(0x11)
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("load = %x, %v", got, err)
	}

	second := testSecretKey(0x22)
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, second) {
		t.Fatalf("load after replace = %x, %v", got, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "mitm"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "ca.key" {
		t.Fatalf("mitm dir = %v, %v", entries, err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil || !strings.HasPrefix(string(raw), "-----BEGIN PRIVATE KEY-----\n") {
		t.Fatalf("file = %q, %v", raw, err)
	}

	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load deleted: %v", err)
	}
}

func TestFileSecretStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "mitm"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := newPEMSecretStore(dir)
	if err := store.Save(testSecretKey(0x33)); err != nil {
		t.Fatal(err)
	}
	assertMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, %v; want %v", path, info.Mode().Perm(), err, want)
		}
	}
	assertMode(filepath.Join(dir, "mitm"), 0o700)
	assertMode(store.path, 0o600)

	// Loosened permissions are tightened again on read.
	if err := os.Chmod(store.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	assertMode(store.path, 0o600)
}

func TestFileSecretStoreErrorsOmitKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	store := newPEMSecretStore(dir)
	key := testSecretKey(0x44)
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A PEM block of the wrong type: its content is the private key and must not
	// appear in the error.
	pemText, _ := encodeSecretPEM(key)
	bad := bytes.Replace(pemText, []byte("PRIVATE KEY"), []byte("CERTIFICATE"), 2)
	if err := os.WriteFile(store.path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if !errors.Is(err, errSecretCorrupt) {
		t.Fatalf("load corrupt: %v", err)
	}
	assertNoSecretInError(t, err, key, pemText)

	if err := os.WriteFile(store.path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load()
	if !errors.Is(err, errSecretCorrupt) {
		t.Fatalf("load raw: %v", err)
	}
	assertNoSecretInError(t, err, key, pemText)

	// A failed write (the parent path is a file) likewise reports only the path.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "mitm"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = newPEMSecretStore(blocked).Save(key)
	if err == nil {
		t.Fatal("save under a file succeeded")
	}
	assertNoSecretInError(t, err, key, pemText)
}

func assertNoSecretInError(t *testing.T, err error, key, pemText []byte) {
	t.Helper()
	msg := err.Error()
	body := strings.Join(strings.Split(strings.TrimSpace(string(pemText)), "\n")[1:2], "")
	for _, needle := range []string{string(key), body, hex.EncodeToString(key), hex.EncodeToString(key[:16])} {
		if needle != "" && strings.Contains(msg, needle) {
			t.Fatalf("error leaks key material: %q", msg)
		}
	}
	if strings.Contains(msg, "BEGIN") {
		t.Fatalf("error leaks key encoding: %q", msg)
	}
}

// A private key saved before the rename (old keychain item, etc.) is migrated to
// the current location on read and the old entry is deleted.
func TestMigratingSecretStore(t *testing.T) {
	current, legacy := &fakeSecretStore{}, &fakeSecretStore{key: testSecretKey(0xa1)}
	store := &migratingSecretStore{current: current, legacy: legacy}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xa1)) {
		t.Fatalf("load = %x, %v", got, err)
	}
	if !bytes.Equal(current.snapshot(), testSecretKey(0xa1)) || len(legacy.snapshot()) != 0 {
		t.Fatalf("current %x legacy %x", current.snapshot(), legacy.snapshot())
	}
	// The old location is not read once the current location has a key.
	legacy.key = testSecretKey(0xa2)
	if got, err := store.Load(); err != nil || !bytes.Equal(got, testSecretKey(0xa1)) || legacy.loadCount() != 1 {
		t.Fatalf("second load = %x, %v, legacy loads %d", got, err, legacy.loadCount())
	}
	if err := store.Delete(); err != nil || len(current.snapshot()) != 0 || len(legacy.snapshot()) != 0 {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load empty: %v", err)
	}
	// Writing the current location fails: the old entry is kept and the key is
	// still returned.
	failing := &fakeSecretStore{saveErr: errors.New("keychain busy")}
	kept := &fakeSecretStore{key: testSecretKey(0xa3)}
	if got, err := (&migratingSecretStore{current: failing, legacy: kept}).Load(); err != nil || !bytes.Equal(got, testSecretKey(0xa3)) || kept.deletes != 0 {
		t.Fatalf("save failure: %v, deletes %d", err, kept.deletes)
	}
	// A read error at the old location (e.g. keychain access denied) is reported
	// rather than treated as no key.
	denied := &fakeSecretStore{loadErr: errors.New("denied")}
	if _, err := (&migratingSecretStore{current: &fakeSecretStore{}, legacy: denied}).Load(); err == nil || errors.Is(err, errSecretNotFound) {
		t.Fatalf("legacy error: %v", err)
	}
}

// fileSecretStore reads a pre-rename file with legacyDecode and rewrites it in
// the current format.
func TestFileSecretStoreLegacyDecode(t *testing.T) {
	dir := t.TempDir()
	encode := func(tag string) func([]byte) ([]byte, error) {
		return func(key []byte) ([]byte, error) { return append([]byte(tag), key...), nil }
	}
	decode := func(tag string) func([]byte) ([]byte, error) {
		return func(data []byte) ([]byte, error) {
			if !bytes.HasPrefix(data, []byte(tag)) {
				return nil, errSecretCorrupt
			}
			return bytes.Clone(data[len(tag):]), nil
		}
	}
	path := filepath.Join(dir, "mitm", "ca.key.test")
	legacy := &fileSecretStore{path: path, encode: encode("old:"), decode: decode("old:")}
	key := testSecretKey(0xb1)
	if err := legacy.Save(key); err != nil {
		t.Fatal(err)
	}
	store := &fileSecretStore{path: path, encode: encode("new:"), decode: decode("new:"), legacyDecode: decode("old:")}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load = %x, %v", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(raw, []byte("new:")) {
		t.Fatalf("not rewritten: %q %v", raw, err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, errSecretCorrupt) {
		t.Fatalf("corrupt: %v", err)
	}
}
