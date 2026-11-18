//go:build !darwin && !windows && !linux

package main

import (
	"crypto/x509"
	"errors"
	"testing"
)

func TestTrustStoreUnsupported(t *testing.T) {
	store := newTrustStore()
	if store.Supported() {
		t.Fatal("supported")
	}
	if err := store.Install(&x509.Certificate{}); !errors.Is(err, errTrustUnsupported) {
		t.Fatalf("install: %v", err)
	}
	if err := store.Remove(&x509.Certificate{}); !errors.Is(err, errTrustUnsupported) {
		t.Fatalf("remove: %v", err)
	}
}
