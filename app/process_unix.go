//go:build !windows

package main

import (
	"context"
	"os/exec"
	"syscall"
)

func prepareDesktopConsole()        {}
func hideProcessWindow(_ *exec.Cmd) {}

// rawCommandLine has nothing to do: Unix programs take their arguments.
func rawCommandLine(*exec.Cmd, string) {}

func prepareProcess(cmd *exec.Cmd)        { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func gracefulProcess(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
func forceProcess(cmd *exec.Cmd) error    { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
func checkReloadSupport() error           { return nil }
func reloadProcess(cmd *exec.Cmd) error   { return cmd.Process.Signal(syscall.SIGHUP) }

func watchDesktopConsole(context.CancelFunc, <-chan struct{}) {}
