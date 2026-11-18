//go:build !darwin

package main

import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

type hostCPUCounters struct{ Total, Idle float64 }

func collectMonitorHost(ctx context.Context) (*hostCPUCounters, *uint64, *uint64) {
	var counters *hostCPUCounters
	if times, err := cpu.TimesWithContext(ctx, false); err == nil && len(times) > 0 {
		counters = &hostCPUCounters{Total: times[0].Total(), Idle: times[0].Idle + times[0].Iowait}
	}
	if memory, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		return counters, ptr(memory.Used), ptr(memory.Total)
	}
	return counters, nil, nil
}
func monitoredProcessStartedAt(ctx context.Context, pid int32) (int64, error) {
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return 0, err
	}
	return p.CreateTimeWithContext(ctx)
}
func monitoredProcessPath(ctx context.Context, pid int32) (string, error) {
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return "", err
	}
	return p.ExeWithContext(ctx)
}
