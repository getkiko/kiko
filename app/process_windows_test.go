//go:build windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsConsoleChild(t *testing.T) {
	if os.Getenv("KIKO_CONSOLE_TEST_CHILD") != "1" {
		return
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	fmt.Println("ready")
	<-interrupt
	if err := os.WriteFile(os.Getenv("KIKO_CONSOLE_TEST_MARKER"), []byte("cleaned up"), 0600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestWindowsProcessGroupGracefulCleanup(t *testing.T) {
	prepareDesktopConsole()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchDesktopConsole(cancel, make(chan struct{}))
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "cleanup.txt")
	cmd := exec.Command(binary, "-test.run=^TestWindowsConsoleChild$")
	cmd.Env = append(os.Environ(), "KIKO_CONSOLE_TEST_CHILD=1", "KIKO_CONSOLE_TEST_MARKER="+marker)
	prepareProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child startup failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child startup timeout")
	}
	if err = gracefulProcess(cmd); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CTRL_BREAK cleanup timeout")
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("child cleanup was skipped:", err)
	}
	if ctx.Err() != nil {
		t.Fatal("child CTRL_BREAK reached the parent")
	}
}
