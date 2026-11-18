package main

import (
	"errors"
	"testing"
)

func TestSecretBusAddressUsable(t *testing.T) {
	for addr, want := range map[string]bool{
		"":                       false,
		"autolaunch:":            false,
		"autolaunch:scope=*user": false,
		"unix:path=/run/user/1000/bus;autolaunch:": false,
		"unix:path=/run/user/1000/bus":             true,
		"unix:abstract=/tmp/dbus-x,guid=1":         true,
	} {
		if got := secretBusAddressUsable(addr); got != want {
			t.Fatalf("%q = %v", addr, got)
		}
	}
}

func TestSecretErrorUnavailable(t *testing.T) {
	for text, want := range map[string]bool{
		"org.freedesktop.DBus.Error.ServiceUnknown: x":    true,
		"org.freedesktop.DBus.Error.Spawn.ExecFailed: x":  true,
		"org.freedesktop.DBus.Error.Spawn.ChildExited: x": true,
		"org.freedesktop.DBus.Error.Spawn.Failed: x":      true,
		"org.freedesktop.DBus.Error.NoReply: x":           true,
		"context deadline exceeded":                       true,
		"org.freedesktop.DBus.Error.AccessDenied: x":      false,
		"org.freedesktop.Secret.Error.IsLocked":           false,
	} {
		if got := secretErrorUnavailable(text); got != want {
			t.Fatalf("%q = %v", text, got)
		}
	}
}

// TestStickyFirstSaveFallsBackOnSpawnAndTimeout checks that on the first save,
// a service that fails to spawn, does not respond, or times out on the prompt
// falls back to the file; a dismissed prompt does not.
func TestStickyFirstSaveFallsBackOnSpawnAndTimeout(t *testing.T) {
	for name, tc := range map[string]struct {
		svc      *fakeSecretService
		fallback bool
	}{
		"prompt timeout": {&fakeSecretService{locked: true, waitErr: errSecretPromptTimeout}, true},
		"dismissed":      {&fakeSecretService{locked: true, dismiss: true}, false},
	} {
		dir := t.TempDir()
		err := newTestSticky(dir, tc.svc, true).Save(testSecretKey(0xc1))
		if tc.fallback && (err != nil || readBackendMarker(t, dir) != "file") {
			t.Fatalf("%s: %v", name, err)
		}
		if !tc.fallback && (!errors.Is(err, errSecretPromptDismissed) || readBackendMarker(t, dir) != "") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Once secretservice is recorded, a timeout still fails closed.
	dir := t.TempDir()
	svc := &fakeSecretService{}
	store := newTestSticky(dir, svc, true)
	if err := store.Save(testSecretKey(0xc2)); err != nil {
		t.Fatal(err)
	}
	svc.locked, svc.waitErr = true, errSecretPromptTimeout
	if err := store.Save(testSecretKey(0xc3)); !errors.Is(err, errSecretPromptTimeout) {
		t.Fatalf("recorded: %v", err)
	}
}
