//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

// Windows samples every process from one Toolhelp snapshot (PID, parent,
// threads, executable name) and one limited-information handle per
// process (path, times, memory, I/O, owner). Protected processes refuse
// the handle and keep their snapshot name.

var (
	procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")
	procGetProcessIoCounters = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIoCounters")
	procPostMessage          = windows.NewLazySystemDLL("user32.dll").NewProc("PostMessageW")
)

type processMemoryCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

type windowsSnapshotEntry struct {
	pid, ppid uint32
	threads   uint32
	exe       string
}

func windowsProcessEntries() ([]windowsSnapshotEntry, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err = windows.Process32First(snap, &entry); err != nil {
		return nil, err
	}
	var out []windowsSnapshotEntry
	for {
		out = append(out, windowsSnapshotEntry{
			pid: entry.ProcessID, ppid: entry.ParentProcessID, threads: entry.Threads,
			exe: windows.UTF16ToString(entry.ExeFile[:]),
		})
		if err = windows.Process32Next(snap, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return out, nil
			}
			return nil, err
		}
	}
}

// windowsAccounts names SIDs once: looking an account up can reach a
// domain controller.
var windowsAccounts sync.Map

func windowsAccountName(sid *windows.SID) string {
	key := sid.String()
	if name, ok := windowsAccounts.Load(key); ok {
		return name.(string)
	}
	name := key
	if account, domain, _, err := sid.LookupAccount(""); err == nil {
		name = account
		if domain != "" {
			name = domain + `\` + account
		}
	}
	windowsAccounts.Store(key, name)
	return name
}

// windowsSystemSID reports the built-in service accounts: LocalSystem,
// LocalService, NetworkService, and the per-service, Window Manager and
// font driver accounts.
func windowsSystemSID(sid string) bool {
	switch sid {
	case "S-1-5-18", "S-1-5-19", "S-1-5-20":
		return true
	}
	for _, prefix := range []string{"S-1-5-80-", "S-1-5-90-", "S-1-5-96-"} {
		if strings.HasPrefix(sid, prefix) {
			return true
		}
	}
	return false
}

func tokenSID(token windows.Token) (*windows.SID, error) {
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

// windowsCommands keeps each process's command line by PID and start
// time: reading another process's command line is costly and it does not
// change.
var windowsCommands struct {
	sync.Mutex
	lines map[[2]int64]string
}

func windowsCommandLine(ctx context.Context, pid int32, started int64, seen map[[2]int64]bool) string {
	key := [2]int64{int64(pid), started}
	seen[key] = true
	windowsCommands.Lock()
	line, ok := windowsCommands.lines[key]
	windowsCommands.Unlock()
	if ok {
		return line
	}
	line, _ = (&process.Process{Pid: pid}).CmdlineWithContext(ctx)
	windowsCommands.Lock()
	if windowsCommands.lines == nil {
		windowsCommands.lines = map[[2]int64]string{}
	}
	windowsCommands.lines[key] = line
	windowsCommands.Unlock()
	return line
}

func filetimeSeconds(t windows.Filetime) float64 {
	return float64(uint64(t.HighDateTime)<<32|uint64(t.LowDateTime)) / 1e7
}

func collectMonitoredProcesses(ctx context.Context) ([]monitoredProcess, []string, error) {
	entries, err := windowsProcessEntries()
	if err != nil {
		return nil, nil, err
	}
	var ownSID string
	if sid, err := tokenSID(windows.GetCurrentProcessToken()); err == nil {
		ownSID = sid.String()
	}
	seen := map[[2]int64]bool{}
	path := make([]uint16, windows.MAX_PATH*2)
	items := make([]monitoredProcess, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if entry.pid == 0 {
			// The idle process counts the idle CPU time.
			continue
		}
		item := monitoredProcess{PID: int32(entry.pid), PPID: int32(entry.ppid), Name: entry.exe, Threads: ptr(int32(entry.threads))}
		if entry.pid == 4 {
			item.Name, item.System, item.User = "System", true, `NT AUTHORITY\SYSTEM`
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.pid)
		if err != nil {
			// Protected and other sessions' system processes.
			item.System = true
			items = append(items, item)
			continue
		}
		readWindowsProcess(handle, &item, ownSID, &path)
		windows.CloseHandle(handle)
		if item.StartedAt > 0 {
			item.Command = windowsCommandLine(ctx, item.PID, item.StartedAt, seen)
		}
		items = append(items, item)
	}
	windowsCommands.Lock()
	for key := range windowsCommands.lines {
		if !seen[key] {
			delete(windowsCommands.lines, key)
		}
	}
	windowsCommands.Unlock()
	return items, []string{"当前平台提供 CPU、内存、线程和磁盘计数；功率、唤醒和每进程网络计数取决于系统采集能力。"}, nil
}

func readWindowsProcess(handle windows.Handle, item *monitoredProcess, ownSID string, buf *[]uint16) {
	size := uint32(len(*buf))
	err := windows.QueryFullProcessImageName(handle, 0, &(*buf)[0], &size)
	if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		*buf = make([]uint16, windows.MAX_LONG_PATH)
		size = uint32(len(*buf))
		err = windows.QueryFullProcessImageName(handle, 0, &(*buf)[0], &size)
	}
	if err == nil {
		item.Path = windows.UTF16ToString((*buf)[:size])
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user) == nil {
		item.StartedAt = creation.Nanoseconds() / 1e6
		item.CPUTime = ptr(filetimeSeconds(kernel) + filetimeSeconds(user))
	}
	memory := processMemoryCountersEx{CB: uint32(unsafe.Sizeof(processMemoryCountersEx{}))}
	if ok, _, _ := procGetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&memory)), uintptr(memory.CB)); ok != 0 {
		item.Memory = ptr(uint64(memory.WorkingSetSize))
		item.ResidentMemory = ptr(uint64(memory.WorkingSetSize))
		item.VirtualMemory = ptr(uint64(memory.PrivateUsage))
	}
	var io windows.IO_COUNTERS
	if ok, _, _ := procGetProcessIoCounters.Call(uintptr(handle), uintptr(unsafe.Pointer(&io))); ok != 0 {
		item.ReadBytes = ptr(io.ReadTransferCount)
		item.WriteBytes = ptr(io.WriteTransferCount)
	}
	var token windows.Token
	if windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token) == nil {
		if sid, err := tokenSID(token); err == nil {
			key := sid.String()
			item.User = windowsAccountName(sid)
			item.Mine = ownSID != "" && key == ownSID
			item.System = item.System || windowsSystemSID(key)
		}
		token.Close()
	} else if item.PID != 4 {
		item.System = true
	}
}

// Ending a process without force closes its windows, as Windows asks an
// app to quit; one without windows can only be forced.
var closeWindows struct {
	sync.Mutex
	pid     uint32
	windows []windows.HWND
}

var closeWindowsCallback = syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil && pid == closeWindows.pid {
		closeWindows.windows = append(closeWindows.windows, hwnd)
	}
	return 1
})

const wmClose = 0x0010

var errNoProcessWindow = errors.New("该进程没有窗口，只能强制结束")

func sendMonitoredProcessSignal(_ context.Context, pid int32, force bool) error {
	if force {
		handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			return err
		}
		defer windows.CloseHandle(handle)
		return windows.TerminateProcess(handle, 1)
	}
	closeWindows.Lock()
	defer closeWindows.Unlock()
	closeWindows.pid, closeWindows.windows = uint32(pid), nil
	_ = windows.EnumWindows(closeWindowsCallback, nil)
	if len(closeWindows.windows) == 0 {
		return errNoProcessWindow
	}
	for _, hwnd := range closeWindows.windows {
		_, _, _ = procPostMessage.Call(uintptr(hwnd), wmClose, 0, 0)
	}
	return nil
}
