//go:build !darwin && !windows

package main

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/process"
)

func collectMonitoredProcesses(ctx context.Context) ([]monitoredProcess, []string, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	own, _ := process.NewProcess(int32(os.Getpid()))
	ownUser, _ := own.UsernameWithContext(ctx)
	items := make([]monitoredProcess, 0, len(processes))
	for _, p := range processes {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		item := monitoredProcess{PID: p.Pid}
		item.StartedAt, _ = p.CreateTimeWithContext(ctx)
		item.PPID, _ = p.PpidWithContext(ctx)
		item.Name, _ = p.NameWithContext(ctx)
		item.Path, _ = p.ExeWithContext(ctx)
		item.Command, _ = p.CmdlineWithContext(ctx)
		item.User, _ = p.UsernameWithContext(ctx)
		item.Mine = ownUser != "" && item.User == ownUser
		if uids, err := p.UidsWithContext(ctx); err == nil && len(uids) > 1 {
			item.System = systemUID(uids[1], loginUIDMin())
		} else {
			item.System = item.User == "root"
		}
		if status, err := p.StatusWithContext(ctx); err == nil {
			item.Status = strings.Join(status, ", ")
		}
		if times, err := p.TimesWithContext(ctx); err == nil {
			item.CPUTime = ptr(times.User + times.System)
		}
		if memory, err := p.MemoryInfoWithContext(ctx); err == nil {
			item.Memory = ptr(memory.RSS)
			item.ResidentMemory = ptr(memory.RSS)
			item.VirtualMemory = ptr(memory.VMS)
		}
		if threads, err := p.NumThreadsWithContext(ctx); err == nil {
			item.Threads = ptr(threads)
		}
		if io, err := p.IOCountersWithContext(ctx); err == nil {
			item.ReadBytes = ptr(io.ReadBytes)
			item.WriteBytes = ptr(io.WriteBytes)
		}
		items = append(items, item)
	}
	return items, []string{"当前平台提供 CPU、内存、线程和磁盘计数；功率、唤醒和每进程网络计数取决于系统采集能力。"}, nil
}

var loginUIDMin = sync.OnceValue(func() uint32 {
	data, err := os.ReadFile("/etc/login.defs")
	if err != nil {
		return 1000
	}
	return parseLoginUIDMin(string(data))
})

func sendMonitoredProcessSignal(ctx context.Context, pid int32, force bool) error {
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return err
	}
	if force {
		return p.KillWithContext(ctx)
	}
	return p.TerminateWithContext(ctx)
}
