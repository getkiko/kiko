//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// endProfileInstance ends a Chrome running on profile, which its
// SingletonLock names as "<host>-<pid>", when it runs on this host with
// that profile.
func endProfileInstance(profile string) {
	link, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return
	}
	at := strings.LastIndex(link, "-")
	host, _ := os.Hostname()
	if at <= 0 || link[:at] != host {
		return
	}
	pid, err := strconv.Atoi(link[at+1:])
	if err != nil || pid <= 1 || syscall.Kill(pid, 0) != nil {
		return
	}
	// A lock left by a crash may name a process that has since taken the
	// number: only a Chrome started on this profile is ended.
	args, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil || !strings.Contains(string(args), "--user-data-dir="+profile) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// terminate asks a browser to quit, so that it saves its cookies and
// session first.
func terminate(process *os.Process) { _ = process.Signal(syscall.SIGTERM) }
