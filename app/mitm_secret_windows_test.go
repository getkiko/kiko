//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDPAPISecretStoreRoundTrip(t *testing.T) {
	store := newSecretStore(t.TempDir()).(*fileSecretStore)
	key := testSecretKey(0x55)
	if err := store.Save(key); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil || bytes.Contains(raw, key) {
		t.Fatalf("blob stores plaintext: %v", err)
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load = %v", err)
	}
	if err := os.WriteFile(store.path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, errSecretCorrupt) {
		t.Fatalf("load corrupt: %v", err)
	}
}

// A private key encrypted with the pre-rename entropy is rewritten with the new
// entropy on read.
func TestDPAPISecretStoreMigratesLegacyEntropy(t *testing.T) {
	store := newSecretStore(t.TempDir()).(*fileSecretStore)
	key := testSecretKey(0x56)
	var out windows.DataBlob
	if err := windows.CryptProtectData(dpapiBlob(key), nil, dpapiBlob(legacyDPAPIEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, dpapiTake(&out, false), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load legacy = %v", err)
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := dpapiUnprotect(raw); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("file not rewritten with the current entropy: %v", err)
	}
}
