//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Layout of proc_taskinfo from the public sys/proc_info.h header. Time fields
// use Mach absolute ticks; the conversion scale comes from mach_timebase_info.
type darwinTaskInfo struct {
	VirtualSize, ResidentSize                   uint64
	TotalUser, TotalSystem                      uint64
	ThreadsUser, ThreadsSystem                  uint64
	Policy, Faults, Pageins, CowFaults          int32
	MessagesSent, MessagesReceived              int32
	SyscallsMach, SyscallsUnix, ContextSwitches int32
	ThreadCount, RunningCount, Priority         int32
}
type hostCPUCounters struct{ Total, Idle float64 }

func monitoredProcessStartedAt(ctx context.Context, pid int32) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil {
		return 0, err
	}
	if k.Proc.P_pid != pid {
		return 0, errors.New("process exited")
	}
	return k.Proc.P_starttime.Sec*1000 + int64(k.Proc.P_starttime.Usec)/1000, nil
}
func monitoredProcessPath(ctx context.Context, pid int32) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := loadDarwinMonitor(); err != nil {
		return "", err
	}
	var path [4096]byte
	if darwinMonitor.pidPath(pid, unsafe.Pointer(&path[0]), uint32(len(path))) <= 0 {
		return "", errors.New("process path unavailable")
	}
	return unix.ByteSliceToString(path[:]), nil
}
func sendMonitoredProcessSignal(ctx context.Context, pid int32, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	signal := unix.SIGTERM
	if force {
		signal = unix.SIGKILL
	}
	return unix.Kill(int(pid), signal)
}
func darwinProcessCommand(pid int32) string {
	return formatDarwinProcessArgs(darwinProcessArgs(pid))
}
func darwinProcessArgs(pid int32) []string {
	raw, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil {
		return nil
	}
	return parseDarwinProcessArgs(raw)
}
func formatDarwinProcessArgs(raw []string) string {
	args := append([]string(nil), raw...)
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\r\n'\"\\$`") {
			args[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
	}
	return strings.Join(args, " ")
}

// KERN_PROCARGS2 is argc (native int32), executable\0, padding, then exactly
// argc NUL-terminated arguments. Environment values follow and stay excluded.
func parseDarwinProcessArgs(raw []byte) []string {
	if len(raw) < 5 {
		return nil
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc <= 0 || argc > len(raw) {
		return nil
	}
	end := bytes.IndexByte(raw[4:], 0)
	if end < 0 {
		return nil
	}
	offset := 4 + end + 1
	for offset < len(raw) && raw[offset] == 0 {
		offset++
	}
	args := make([]string, 0, argc)
	for len(args) < argc && offset < len(raw) {
		end = bytes.IndexByte(raw[offset:], 0)
		if end < 0 {
			return nil
		}
		args = append(args, string(raw[offset:offset+end]))
		offset += end + 1
	}
	if len(args) != argc {
		return nil
	}
	return args
}

// vm_statistics64 from mach/vm_statistics.h, including the rev2 swapped_count.
type darwinVMStatistics struct {
	Free, Active, Inactive, Wired                                                        uint32
	ZeroFill, Reactivations, Pageins, Pageouts, Faults, CowFaults, Lookups, Hits, Purges uint64
	Purgeable, Speculative                                                               uint32
	Decompressions, Compressions, Swapins, Swapouts                                      uint64
	Compressor, Throttled, External, Internal                                            uint32
	UncompressedPages, Swapped                                                           uint64
}

func collectMonitorHost(ctx context.Context) (*hostCPUCounters, *uint64, *uint64) {
	if ctx.Err() != nil || loadDarwinMonitor() != nil {
		return nil, nil, nil
	}
	var cpuCounters *hostCPUCounters
	var ticks [4]uint32 // CPU_STATE_USER, SYSTEM, IDLE, NICE
	count := uint32(len(ticks))
	if darwinMonitor.hostStatistics(darwinMonitor.hostPort, 3, unsafe.Pointer(&ticks[0]), &count) == 0 {
		cpuCounters = &hostCPUCounters{Total: float64(ticks[0]) + float64(ticks[1]) + float64(ticks[2]) + float64(ticks[3]), Idle: float64(ticks[2])}
	}
	var vm darwinVMStatistics
	count = uint32(unsafe.Sizeof(vm) / 4)
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return cpuCounters, nil, nil
	}
	if darwinMonitor.hostStatistics64(darwinMonitor.hostPort, 4, unsafe.Pointer(&vm), &count) != 0 {
		return cpuCounters, nil, ptr(total)
	}
	// Activity Monitor's memory used: anonymous + wired + compressed, excluding
	// purgeable anonymous pages. File-backed cache remains available memory.
	pages := uint64(vm.Wired) + uint64(vm.Compressor) + uint64(vm.Internal)
	if pages >= uint64(vm.Purgeable) {
		pages -= uint64(vm.Purgeable)
	}
	used := pages * uint64(unix.Getpagesize())
	if used > total {
		used = total
	}
	return cpuCounters, ptr(used), ptr(total)
}

// getpwuid_r keeps username lookup available in CGO_ENABLED=0 builds.
type darwinPasswd struct {
	Name, Password                 *byte
	UID, GID                       uint32
	Change                         int64
	Class, Gecos, Directory, Shell *byte
	Expire                         int64
}

func darwinProcessUser(uid uint32) string {
	if cached, ok := darwinMonitor.users.Load(uid); ok {
		return cached.(string)
	}
	name := strconv.FormatUint(uint64(uid), 10)
	var entry darwinPasswd
	var result *darwinPasswd
	buffer := make([]byte, 16384)
	if darwinMonitor.getpwuid(uid, unsafe.Pointer(&entry), unsafe.Pointer(&buffer[0]), uintptr(len(buffer)), unsafe.Pointer(&result)) == 0 && result != nil && entry.Name != nil {
		name = unix.BytePtrToString(entry.Name)
	}
	darwinMonitor.users.Store(uid, name)
	return name
}
