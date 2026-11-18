package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBundleIconStampFollowsIconSources(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Test.app")
	resources := filepath.Join(bundle, "Contents", "Resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := bundleIconStamp(bundle)
	if stamp != bundleIconStamp(bundle) {
		t.Fatal("unchanged bundle changed its stamp")
	}
	// Set an old time first, so the following change is visible on any
	// file system time resolution.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(resources, old, old); err != nil {
		t.Fatal(err)
	}
	stamp = bundleIconStamp(bundle)
	if err := os.WriteFile(filepath.Join(resources, "Icon.icns"), []byte("icns"), 0o600); err != nil {
		t.Fatal(err)
	}
	if bundleIconStamp(bundle) == stamp {
		t.Fatal("added icon file kept the stamp")
	}
	// An icon file rewritten in place keeps the directory's time.
	icon := filepath.Join(resources, "Icon.icns")
	if err := os.Chtimes(resources, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(icon, old, old); err != nil {
		t.Fatal(err)
	}
	stamp = bundleIconStamp(bundle)
	if err := os.WriteFile(icon, []byte("icns, complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(resources, old, old); err != nil {
		t.Fatal(err)
	}
	if bundleIconStamp(bundle) == stamp {
		t.Fatal("icon file written in place kept the stamp")
	}
	stamp = bundleIconStamp(bundle)
	if err := os.WriteFile(plist, []byte("<plist></plist>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if bundleIconStamp(bundle) == stamp {
		t.Fatal("rewritten Info.plist kept the stamp")
	}
}

func TestBundleIconCacheRetriesChangedBundle(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Test.app")
	resources := filepath.Join(bundle, "Contents", "Resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := newBundleIconCache()
	stamp := bundleIconStamp(bundle)
	at := time.Now().Add(-time.Second)
	cache.failed[bundle] = bundleIconFailure{at: at, stamp: stamp}
	if _, err := cache.icon(t.Context(), bundle); err == nil {
		t.Fatal("cached failure returned an icon")
	}
	if got := cache.failed[bundle]; !got.at.Equal(at) || got.stamp != stamp {
		t.Fatal("unchanged bundle was converted again")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(resources, old, old); err != nil {
		t.Fatal(err)
	}
	// The bundle changed: the failure no longer applies and conversion runs
	// again (it fails on this test bundle, recording the new stamp).
	_, _ = cache.icon(t.Context(), bundle)
	if got := cache.failed[bundle]; got.stamp == stamp {
		t.Fatal("changed bundle reused the old failure")
	}
}
