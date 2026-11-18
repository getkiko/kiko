//go:build darwin

package main

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestCoreLockFixture(t *testing.T) {
	if os.Getenv("KIKO_HELPER_FIXTURE") != "core" {
		return
	}
	file, err := os.OpenFile(os.Getenv("KIKO_HELPER_CACHE"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		os.Exit(2)
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		os.Exit(3)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	_ = os.WriteFile(os.Getenv("KIKO_HELPER_READY"), []byte(strconv.Itoa(os.Getpid())), 0600)
	<-sig
	file.Close()
	os.Exit(0)
}

func TestHelperLifetimeFixture(t *testing.T) {
	if os.Getenv("KIKO_HELPER_FIXTURE") != "helper" {
		return
	}
	helperPaths.Socket = os.Getenv("KIKO_HELPER_SOCKET")
	helperPaths.CoreExecutable = filepath.Join(filepath.Dir(helperPaths.Socket), "unique-installed-core")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoreLockFixture$")
	cmd.Env = append(os.Environ(), "KIKO_HELPER_FIXTURE=core")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(4)
	}
	managed = cmd
	go func() { _ = cmd.Wait(); processMu.Lock(); managed = nil; processMu.Unlock() }()
	main()
}

func TestHelperTerminationReleasesCoreAndCache(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kiko-helper-lifetime-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "helper.sock")
	cache := filepath.Join(dir, "cache.db")
	ready := filepath.Join(dir, "ready")
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperLifetimeFixture$")
	helper.Env = append(os.Environ(), "KIKO_HELPER_FIXTURE=helper", "KIKO_HELPER_SOCKET="+socketPath, "KIKO_HELPER_CACHE="+cache, "KIKO_HELPER_READY="+ready)
	if err = helper.Start(); err != nil {
		t.Fatal(err)
	}
	var child int
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		if child > 1 {
			_ = syscall.Kill(-child, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(ready); len(data) > 0 {
			child, _ = strconv.Atoi(string(data))
			if _, err := os.Stat(socketPath); err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child <= 1 {
		t.Fatal("core fixture not ready")
	}
	if err = helper.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	file, err := os.OpenFile(cache, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("helper exit orphaned core PID %d and left cache locked: %v", child, err)
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func TestOrphanParentFixture(t *testing.T) {
	if os.Getenv("KIKO_HELPER_FIXTURE") != "orphan-parent" {
		return
	}
	cmd := exec.Command(os.Getenv("KIKO_HELPER_CORE"), "-test.run=^TestCoreLockFixture$")
	cmd.Env = append(os.Environ(), "KIKO_HELPER_FIXTURE=core")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestStartupReapsVerifiedOrphanAndUnlocksCache(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "kiko-helper-orphan-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "installed-core")
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	cache, ready := filepath.Join(dir, "cache.db"), filepath.Join(dir, "ready")
	parent := exec.Command(os.Args[0], "-test.run=^TestOrphanParentFixture$")
	parent.Env = append(os.Environ(), "KIKO_HELPER_FIXTURE=orphan-parent", "KIKO_HELPER_CORE="+binary, "KIKO_HELPER_CACHE="+cache, "KIKO_HELPER_READY="+ready)
	if err = parent.Run(); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, _ := os.ReadFile(ready); len(raw) > 0 {
			pid, _ = strconv.Atoi(string(raw))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 1 {
		t.Fatal("orphan not ready")
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	previous := helperPaths.CoreExecutable
	helperPaths.CoreExecutable = binary
	defer func() { helperPaths.CoreExecutable = previous }()
	if err = cleanupOrphanedCores(context.Background()); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(cache, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("orphan still holds cache", err)
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func TestOrphanIdentificationIsRestricted(t *testing.T) {
	expected := coreIdentity{12345, 1, os.Geteuid(), 12345, helperPaths.CoreExecutable}
	if !orphanCore(expected) {
		t.Fatal("valid orphan rejected")
	}
	cases := []coreIdentity{expected, expected, expected, expected}
	cases[0].parent = 100
	cases[1].uid++
	cases[2].group = 100
	cases[3].executable += ".other"
	for _, identity := range cases {
		if orphanCore(identity) {
			t.Fatalf("unrelated process selected: %+v", identity)
		}
	}
}

func TestForceStopRestrictsPID(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	previous := managed
	managed = cmd
	t.Cleanup(func() { _ = cmd.Process.Kill(); managed = previous })
	for _, pid := range []int{0, 1, os.Getpid(), cmd.Process.Pid + 1} {
		if res := forceStop(request{PID: pid}); res.Error == "" {
			t.Fatalf("unmanaged PID %d accepted", pid)
		}
	}
	if res := forceStop(request{PID: cmd.Process.Pid}); res.Error != "" {
		t.Fatal(res.Error)
	}
	_ = cmd.Wait()
}
