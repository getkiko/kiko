//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A Chrome left running on a profile, as its SingletonLock names it, ends
// before a new one starts; a lock of another host is left alone.
func TestEndProfileInstance(t *testing.T) {
	profile := t.TempDir()
	host, _ := os.Hostname()
	lock := filepath.Join(profile, "SingletonLock")
	// A process that took the number of a crashed Chrome is left alone.
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, other.Process.Pid), lock); err != nil {
		t.Fatal(err)
	}
	endProfileInstance(profile)
	if other.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("ended a process not on the profile")
	}
	_ = os.Remove(lock)

	// The shell stands for Chrome: its arguments name the profile.
	stale := exec.Command("sh", "-c", "sleep 30; :", "chrome", "--user-data-dir="+profile)
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = stale.Wait(); close(exited) }()
	if err := os.Symlink(fmt.Sprintf("elsewhere-%d", stale.Process.Pid), lock); err != nil {
		t.Fatal(err)
	}
	endProfileInstance(profile)
	select {
	case <-exited:
		t.Fatal("ended the process another host's lock names")
	case <-time.After(200 * time.Millisecond):
	}
	_ = os.Remove(lock)
	if err := os.Symlink(fmt.Sprintf("%s-%d", host, stale.Process.Pid), lock); err != nil {
		t.Fatal(err)
	}
	endProfileInstance(profile)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = stale.Process.Kill()
		t.Fatal("the stale instance still runs")
	}
}
