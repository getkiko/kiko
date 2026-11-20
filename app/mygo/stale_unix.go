//go:build !windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
)

// processName is the executable of process pid, as ps reports it.
func processName(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	return string(out), err
}

// terminateProcess asks process pid to end; kiko-server shuts down cleanly.
func terminateProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func killProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
