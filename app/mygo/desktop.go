package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

// baseServices are the desktop services every console has: file dialogs
// over its own window, opening paths and URLs, and the server directory.
// Their methods may block on dialogs and the network; the console calls
// them from goroutines.
type baseServices struct {
	shell *shell
	win   *serverWindow
}

// desktopServices is the env.Shell of the local server's console: the desktop services,
// with the server process and the macOS helper the app runs.
type desktopServices struct{ baseServices }

// remoteServices is the env.Shell of a remote server's console. That
// server's process, logs directory and helper are on another host: the
// console reaches them through the API, or not at all.
type remoteServices struct{ baseServices }

var (
	_ env.Shell           = desktopServices{}
	_ env.ServerRestarter = desktopServices{}
	_ env.ServerDirectory = desktopServices{}
	_ env.Shell           = remoteServices{}
	_ env.ServerDirectory = remoteServices{}
)

// RestartServer replaces kiko-server, or leaves its crash loop, off the
// UI thread: stopping the old process can take a while.
func (d desktopServices) RestartServer() { go d.shell.server.restart() }

func (d desktopServices) OpenLogsDirectory() error { return d.shell.OpenLogsDirectory() }

func (remoteServices) OpenLogsDirectory() error {
	return errors.New("远程服务器的日志目录不在本机，请在日志页导出")
}

func (remoteServices) HelperStatus() (env.HelperStatus, error) {
	return env.HelperStatus{}, errors.New("特权助手只适用于本机")
}

func (remoteServices) InstallHelper() (bool, error) {
	return false, errors.New("特权助手只适用于本机")
}

// AppIcon finds nothing for a remote server: its paths are on another
// host, and the console shows the server's icons.
func (remoteServices) AppIcon(string) []byte { return nil }

func (d baseServices) Platform() string { return runtime.GOOS }

func (d desktopServices) HelperStatus() (env.HelperStatus, error) {
	status, err := d.shell.PrivilegedHelperStatus()
	if err != nil {
		return env.HelperStatus{}, err
	}
	out := env.HelperStatus{Installed: status.Installed, NeedsUpdate: status.NeedsUpdate}
	for _, change := range status.Changes {
		out.Changes = append(out.Changes, env.HelperChange{Name: change.Name, Reason: change.Reason})
	}
	return out, nil
}

func (d desktopServices) InstallHelper() (bool, error) {
	result, err := d.shell.InstallPrivilegedHelper()
	return result.Completed, err
}

func (d desktopServices) AppIcon(path string) []byte { return d.shell.icons.png(path) }

func (d baseServices) SaveFile(name string, data []byte, filters ...env.FileFilter) (bool, error) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:            d.win.current(),
		DefaultPath:       name,
		Filters:           fileFilters(filters),
		CreateDirectories: true,
	})
	if err != nil || path == "" {
		return false, err
	}
	return true, os.WriteFile(path, data, 0o644)
}

// maxOpenFile bounds what OpenFile reads.
const maxOpenFile = 32 << 20

func (d baseServices) OpenFile(filters ...env.FileFilter) ([]byte, string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  d.win.current(),
		Filters: fileFilters(filters),
	})
	if err != nil || len(paths) == 0 {
		return nil, "", false, err
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		return nil, "", false, err
	}
	if info.Size() > maxOpenFile {
		return nil, "", false, errors.New("文件过大")
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, "", false, err
	}
	return data, filepath.Base(paths[0]), true, nil
}

func (d baseServices) OpenPath(path string) error { return mygo.Shell.OpenPath(path) }

func (d baseServices) RevealPath(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	mygo.Shell.ShowItemInFolder(path)
	return nil
}

func (d baseServices) OpenInConsole(path string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("控制台只适用于 macOS")
	}
	if output, err := exec.Command("/usr/bin/open", "-a", "Console", path).CombinedOutput(); err != nil {
		return fmt.Errorf("打开控制台失败: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (d baseServices) OpenURL(url string) error { return mygo.Shell.OpenExternal(url) }

func fileFilters(filters []env.FileFilter) []mygo.FileFilter {
	out := make([]mygo.FileFilter, 0, len(filters))
	for _, f := range filters {
		out = append(out, mygo.FileFilter{Name: f.Name, Extensions: f.Extensions})
	}
	return out
}

func (d baseServices) Servers() []servers.Server { return d.shell.servers.List() }

func (d baseServices) InsecureTokens() bool { return d.shell.servers.InsecureTokens() }

func (d baseServices) OpenServer(id string) { go d.shell.openServer(id) }

func (d baseServices) AddServer(ctx context.Context, uri string) (servers.Server, error) {
	return d.shell.addServer(ctx, uri)
}

func (d baseServices) AddServerManual(ctx context.Context, addr, code string, confirm func([]byte) bool) (servers.Server, error) {
	return d.shell.addServerManual(ctx, addr, code, confirm)
}

func (d baseServices) RepairServer(ctx context.Context, id, input string, confirm func([]byte) bool) error {
	return d.shell.repairServer(ctx, id, input, confirm)
}

func (d baseServices) RenameServer(id, name string) error { return d.shell.servers.Rename(id, name) }

func (d baseServices) RemoveServer(ctx context.Context, id string) error {
	return d.shell.removeServer(ctx, id)
}

func (d baseServices) RestoreWindows() bool {
	on, _ := d.shell.servers.Restore()
	return on
}

func (d baseServices) SetRestoreWindows(on bool) error { return d.shell.servers.SetRestore(on) }
