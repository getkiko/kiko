//go:build !darwin && !windows && !linux

package main

import (
	"path/filepath"
	"testing"
)

func TestNewSecretStoreUsesPEMFile(t *testing.T) {
	dir := t.TempDir()
	store, ok := newSecretStore(dir).(*fileSecretStore)
	if !ok || store.path != filepath.Join(dir, "mitm", "ca.key") {
		t.Fatalf("store = %#v", store)
	}
}
