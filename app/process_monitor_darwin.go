//go:build darwin

package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// These are public libproc entry points. Keeping the handle open avoids loading
// and unloading libproc for every metric on every process in each snapshot.
var darwinMonitor struct {
	hostStatistics   func(uint32, int32, unsafe.Pointer, *uint32) int32
	hostStatistics64 func(uint32, int32, unsafe.Pointer, *uint32) int32
	machHostSelf     func() uint32
	hostPort         uint32
	getpwuid         func(uint32, unsafe.Pointer, unsafe.Pointer, uintptr, unsafe.Pointer) int32
	users            sync.Map
	once             sync.Once
	err              error
	pidInfo          func(int32, int32, uint64, unsafe.Pointer, int32) int32
	pidPath          func(int32, unsafe.Pointer, uint32) int32
	rusage           func(int32, int32, unsafe.Pointer) int32
	timebase         func(unsafe.Pointer) int32
	scale            float64
}

func loadDarwinMonitor() error {
	darwinMonitor.once.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			darwinMonitor.err = err
			return
		}
		purego.RegisterLibFunc(&darwinMonitor.pidInfo, lib, "proc_pidinfo")
		purego.RegisterLibFunc(&darwinMonitor.pidPath, lib, "proc_pidpath")
		purego.RegisterLibFunc(&darwinMonitor.rusage, lib, "proc_pid_rusage")
		purego.RegisterLibFunc(&darwinMonitor.timebase, lib, "mach_timebase_info")
		purego.RegisterLibFunc(&darwinMonitor.hostStatistics, lib, "host_statistics")
		purego.RegisterLibFunc(&darwinMonitor.hostStatistics64, lib, "host_statistics64")
		purego.RegisterLibFunc(&darwinMonitor.machHostSelf, lib, "mach_host_self")
		purego.RegisterLibFunc(&darwinMonitor.getpwuid, lib, "getpwuid_r")
		darwinMonitor.hostPort = darwinMonitor.machHostSelf()
		var base struct{ Numer, Denom uint32 }
		darwinMonitor.timebase(unsafe.Pointer(&base))
		darwinMonitor.scale = 1e-9
		if base.Denom != 0 {
			darwinMonitor.scale *= float64(base.Numer) / float64(base.Denom)
		}
	})
	return darwinMonitor.err
}

// rusage_info_v6: UUID followed by the uint64 fields in sys/resource.h.
// V2 ends at diskio_byteswritten; V6 adds energy_nj at index 40.
type darwinProcessUsage struct {
	UUID   [16]byte
	Values [56]uint64
}

func collectMonitoredProcesses(ctx context.Context) ([]monitoredProcess, []string, error) {
	if err := loadDarwinMonitor(); err != nil {
		return nil, nil, err
	}
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, nil, err
	}
	network, networkErr := darwinProcessNetwork(ctx)
	warnings := []string{}
	if networkErr != nil {
		warnings = append(warnings, "每进程网络计数暂不可用："+networkErr.Error())
	}
	fallback := darwinProcessPS(ctx)
	items := make([]monitoredProcess, 0, len(kprocs))
	for _, k := range kprocs {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		uid := k.Eproc.Ucred.Uid
		username := darwinProcessUser(uid)
		p := monitoredProcess{PID: k.Proc.P_pid, PPID: k.Eproc.Ppid, Name: unix.ByteSliceToString(k.Proc.P_comm[:]), User: username, Mine: uid == uint32(os.Getuid()), System: uid < 500, StartedAt: k.Proc.P_starttime.Sec*1000 + int64(k.Proc.P_starttime.Usec)/1000}
		p.Status = map[int8]string{1: "idle", 2: "running", 3: "sleep", 4: "stopped", 5: "zombie"}[k.Proc.P_stat]
		var path [4096]byte
		if darwinMonitor.pidPath(p.PID, unsafe.Pointer(&path[0]), uint32(len(path))) > 0 {
			p.Path = unix.ByteSliceToString(path[:])
		}
		args := darwinProcessArgs(p.PID)
		p.Command = formatDarwinProcessArgs(args)
		if len(args) > 0 {
			p.originalPath = args[0]
		}
		var task darwinTaskInfo
		if darwinMonitor.pidInfo(p.PID, 4, 0, unsafe.Pointer(&task), int32(unsafe.Sizeof(task))) == int32(unsafe.Sizeof(task)) {
			p.ResidentMemory = ptr(task.ResidentSize)
			p.Memory = ptr(task.ResidentSize)
			p.VirtualMemory = ptr(task.VirtualSize)
			p.Threads = ptr(task.ThreadCount)
			p.CPUTime = ptr((float64(task.TotalUser) + float64(task.TotalSystem)) * darwinMonitor.scale)
		}
		var usage darwinProcessUsage
		flavor := int32(6)
		if darwinMonitor.rusage(p.PID, flavor, unsafe.Pointer(&usage)) != 0 {
			flavor = 2
			usage = darwinProcessUsage{}
		}
		if flavor == 6 || darwinMonitor.rusage(p.PID, flavor, unsafe.Pointer(&usage)) == 0 {
			values := usage.Values
			p.CPUTime = ptr((float64(values[0]) + float64(values[1])) * darwinMonitor.scale)
			p.ResidentMemory = ptr(values[6])
			p.Memory = ptr(values[7])
			p.wakeupTotal = ptr(values[3])
			p.ReadBytes = ptr(values[16])
			p.WriteBytes = ptr(values[17])
			if flavor == 6 {
				p.energyNJ = ptr(values[40])
			}
		}
		if ps, ok := fallback[p.PID]; ok {
			if p.Command == "" {
				p.Command = ps.Command
			}
			p.Status = ps.Status
			p.CPU = ps.CPU
			if p.CPUTime == nil {
				p.Estimated = true
				p.CPUTime = ps.CPUTime
			}
			if p.Memory == nil {
				p.Memory = ps.Memory
				p.ResidentMemory = ps.ResidentMemory
				p.VirtualMemory = ps.VirtualMemory
			}
		}
		if networkErr == nil {
			p.networkSample = &processNetworkSample{sockets: network[p.PID]}
		} else {
			p.networkUnavailable = true
		}
		items = append(items, p)
	}
	return items, warnings, nil
}
func darwinProcessNetwork(ctx context.Context) (map[int32]map[string][2]uint64, error) {
	networkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// One cumulative CSV sample for every process, including processes whose name
	// contains spaces or dots. Connection rows provide separate baselines for sockets that close.
	out, err := exec.CommandContext(networkCtx, "/usr/bin/nettop", "-L", "1", "-x", "-J", "bytes_in,bytes_out", "-n").Output()
	if err != nil {
		return nil, err
	}
	return parseDarwinProcessNetwork(string(out)), nil
}
func parseDarwinProcessNetwork(raw string) map[int32]map[string][2]uint64 {
	result := map[int32]map[string][2]uint64{}
	var currentPID int32
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if len(row) < 3 {
			continue
		}
		name := strings.TrimSpace(row[0])
		if !strings.Contains(name, "<->") {
			currentPID = 0
			dot := strings.LastIndexByte(name, '.')
			if dot < 0 {
				continue
			}
			pid, err := strconv.ParseInt(name[dot+1:], 10, 32)
			if err != nil || pid <= 0 {
				continue
			}
			currentPID = int32(pid)
			result[currentPID] = map[string][2]uint64{}
			continue
		}
		if currentPID == 0 {
			continue
		}
		received, err1 := strconv.ParseUint(strings.TrimSpace(row[1]), 10, 64)
		sent, err2 := strconv.ParseUint(strings.TrimSpace(row[2]), 10, 64)
		if err1 == nil && err2 == nil {
			counts := result[currentPID][name]
			counts[0] += received
			counts[1] += sent
			result[currentPID][name] = counts
		}
	}
	return result
}

// ps exposes basic accounting for other users' processes even when libproc
// denies task/rusage access. Its state also reflects the threads' current state.
func darwinProcessPS(ctx context.Context) map[int32]monitoredProcess {
	out, err := exec.CommandContext(ctx, "/bin/ps", "-axww", "-o", "pid=,pcpu=,rss=,vsz=,time=,state=,command=").Output()
	if err != nil {
		return map[int32]monitoredProcess{}
	}
	return parseDarwinProcessPS(string(out))
}
func parseDarwinProcessPS(raw string) map[int32]monitoredProcess {
	result := map[int32]monitoredProcess{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		pid, err := strconv.ParseInt(fields[0], 10, 32)
		if err != nil {
			continue
		}
		p := monitoredProcess{PID: int32(pid)}
		command := strings.TrimSpace(line)
		for i := 0; i < 6; i++ {
			index := strings.IndexFunc(command, unicode.IsSpace)
			if index < 0 {
				command = ""
				break
			}
			command = strings.TrimLeftFunc(command[index:], unicode.IsSpace)
		}
		p.Command = command
		if value, err := strconv.ParseFloat(fields[1], 64); err == nil {
			p.CPU = ptr(value)
		}
		if value, err := strconv.ParseUint(fields[2], 10, 64); err == nil {
			p.Memory = ptr(value * 1024)
			p.ResidentMemory = p.Memory
		}
		if value, err := strconv.ParseUint(fields[3], 10, 64); err == nil {
			p.VirtualMemory = ptr(value * 1024)
		}
		if value, err := parsePSCPUTime(fields[4]); err == nil {
			p.CPUTime = ptr(value)
		}
		if len(fields[5]) > 0 {
			p.Status = map[byte]string{'R': "running", 'S': "sleep", 'I': "idle", 'T': "stopped", 'Z': "zombie", 'U': "blocked"}[fields[5][0]]
		}
		result[p.PID] = p
	}
	return result
}
func parsePSCPUTime(raw string) (float64, error) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid ps CPU time: %s", raw)
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("invalid ps CPU time: %s", raw)
		}
		total = total*60 + value
	}
	return total, nil
}
