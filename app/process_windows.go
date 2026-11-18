//go:build windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	user32                 = syscall.NewLazyDLL("user32.dll")
	getConsoleWindow       = kernel32.NewProc("GetConsoleWindow")
	allocConsole           = kernel32.NewProc("AllocConsole")
	setConsoleCtrlHandler  = kernel32.NewProc("SetConsoleCtrlHandler")
	desktopConsoleCallback uintptr
	showWindow             = user32.NewProc("ShowWindow")
)

func prepareDesktopConsole() {
	handle, _, _ := getConsoleWindow.Call()
	if handle == 0 {
		_, _, _ = allocConsole.Call()
		handle, _, _ = getConsoleWindow.Call()
		if handle != 0 {
			_, _, _ = showWindow.Call(handle, 0)
		}
	}
}

// AllocConsole resets the handler table, so restore our shutdown callback after
// allocating the desktop console. Keep the callback alive for the process lifetime.
func watchDesktopConsole(cancel context.CancelFunc, done <-chan struct{}) {
	desktopConsoleCallback = syscall.NewCallback(func(event uint32) uintptr {
		switch event {
		case 0, 1:
			cancel()
			return 1
		case 2, 5, 6:
			cancel()
			// Windows terminates the process when a close handler returns.
			select {
			case <-done:
			case <-time.After(4 * time.Second):
			}
			return 1
		}
		return 0
	})
	_, _, _ = setConsoleCtrlHandler.Call(desktopConsoleCallback, 1)
}

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
func hideProcessWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

// rawCommandLine starts cmd with line as its whole command line, for
// programs that parse it themselves.
func rawCommandLine(cmd *exec.Cmd, line string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
}
func gracefulProcess(cmd *exec.Cmd) error {
	// Go maps CTRL_BREAK_EVENT to os.Interrupt in the child process.
	return windows.GenerateConsoleCtrlEvent(syscall.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}
func forceProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }
func checkReloadSupport() error {
	return errors.New("当前 Windows 核心尚未提供配置重载，请停止后重新启动以应用配置")
}
func reloadProcess(_ *exec.Cmd) error { return checkReloadSupport() }
