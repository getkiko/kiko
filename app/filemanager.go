package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// fileManagerCommands are the commands that show path in goos's file
// manager, tried in order until one succeeds: a directory opens, a file
// reveal opens its folder with the file selected where the system can.
func fileManagerCommands(ctx context.Context, goos, path string, reveal bool) ([]*exec.Cmd, error) {
	switch goos {
	case "darwin":
		if reveal {
			return []*exec.Cmd{exec.CommandContext(ctx, "/usr/bin/open", "-R", path)}, nil
		}
		return []*exec.Cmd{exec.CommandContext(ctx, "/usr/bin/open", path)}, nil
	case "windows":
		// Explorer parses its own command line: /select, takes the path
		// quoted after the comma, which Go's argument quoting would wrap
		// whole.
		arg := `"` + path + `"`
		if reveal {
			arg = `/select,"` + path + `"`
		}
		cmd := exec.CommandContext(ctx, "explorer.exe")
		cmd.Args = append(cmd.Args, arg)
		rawCommandLine(cmd, "explorer.exe "+arg)
		return []*exec.Cmd{cmd}, nil
	case "linux":
		dir := path
		var cmds []*exec.Cmd
		if reveal {
			dir = filepath.Dir(path)
			// File managers implementing org.freedesktop.FileManager1
			// (Files, Dolphin, Nemo, Thunar…) select the file.
			uri := (&url.URL{Scheme: "file", Path: path}).String()
			cmds = append(cmds, exec.CommandContext(ctx, "dbus-send", "--session", "--print-reply",
				"--dest=org.freedesktop.FileManager1", "--type=method_call", "/org/freedesktop/FileManager1",
				"org.freedesktop.FileManager1.ShowItems", "array:string:"+uri, "string:"))
		}
		return append(cmds, exec.CommandContext(ctx, "xdg-open", dir)), nil
	}
	return nil, fmt.Errorf("当前系统（%s）没有可用的文件管理器", goos)
}

var errNoFileManager = errors.New("no file manager")

// showInFileManager opens directory path, or reveals file path, in this
// system's file manager.
func showInFileManager(ctx context.Context, path string, reveal bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmds, err := fileManagerCommands(ctx, runtime.GOOS, path, reveal)
	if err != nil {
		return errors.Join(errNoFileManager, err)
	}
	var errs []error
	for _, cmd := range cmds {
		err := cmd.Run()
		if err == nil || explorerSucceeded(cmd, err) {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(cmd.Path), err))
	}
	return errors.Join(errs...)
}

// explorerSucceeded reports Explorer's exit status 1, which it returns
// after opening a window as well.
func explorerSucceeded(cmd *exec.Cmd, err error) bool {
	var exit *exec.ExitError
	return filepath.Base(cmd.Args[0]) == "explorer.exe" && errors.As(err, &exit) && exit.ExitCode() == 1
}
