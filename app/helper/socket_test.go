//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeSocketRebindsAfterRemoval(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kiko-helper-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "helper.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveSocket(ctx, path) }()

	waitStatus(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, path)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serveSocket did not stop")
	}
}

// waitStatus dials the socket until a status request succeeds.
func waitStatus(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * socketCheckInterval)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = json.NewEncoder(conn).Encode(request{Op: "status"})
			var res response
			err = json.NewDecoder(conn).Decode(&res)
			conn.Close()
			if err == nil && res.OK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("helper socket did not answer")
}

// A socket path taken over by another helper (an upgrade in progress) is
// left to it, not bound back.
func TestServeSocketLeavesReplacedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kiko-helper-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "helper.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveSocket(ctx, path) }()
	waitStatus(t, path)

	// Swap the path atomically so the helper never sees it missing.
	staged := filepath.Join(dir, "other.sock")
	other, err := net.Listen("unix", staged)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2*socketCheckInterval + 200*time.Millisecond)
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("replacement socket was taken over: %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serveSocket did not stop")
	}
}
