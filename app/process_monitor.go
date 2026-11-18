package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
)

// Pointers distinguish unavailable metrics from a measured zero. CPU uses one
// logical core as 100%; host CPU is normalized across all logical cores.
type monitoredProcess struct {
	PID                int32    `json:"pid"`
	PPID               int32    `json:"ppid"`
	Name               string   `json:"name"`
	Path               string   `json:"path"`
	Command            string   `json:"command"`
	AppName            string   `json:"appName,omitempty"`
	AppPath            string   `json:"appPath,omitempty"`
	User               string   `json:"user"`
	Mine               bool     `json:"mine"`
	System             bool     `json:"system"`
	Estimated          bool     `json:"estimated"`
	StartedAt          int64    `json:"startedAt"`
	Status             string   `json:"status"`
	CPU                *float64 `json:"cpu"`
	CPUTime            *float64 `json:"cpuTime"`
	Memory             *uint64  `json:"memory"`
	ResidentMemory     *uint64  `json:"residentMemory"`
	VirtualMemory      *uint64  `json:"virtualMemory"`
	Threads            *int32   `json:"threads"`
	Power              *float64 `json:"power"`
	Wakeups            *float64 `json:"wakeups"`
	ReadBytes          *uint64  `json:"readBytes"`
	WriteBytes         *uint64  `json:"writeBytes"`
	ReceivedBytes      *uint64  `json:"receivedBytes"`
	SentBytes          *uint64  `json:"sentBytes"`
	ReadRate           *float64 `json:"readRate"`
	WriteRate          *float64 `json:"writeRate"`
	ReceiveRate        *float64 `json:"receiveRate"`
	SendRate           *float64 `json:"sendRate"`
	networkSample      *processNetworkSample
	networkSockets     map[string][2]uint64
	networkUnavailable bool
	wakeupTotal        *uint64
	energyNJ           *uint64
	originalPath       string
}
type processSnapshot struct {
	CapturedAt         int64              `json:"capturedAt"`
	Processes          []monitoredProcess `json:"processes"`
	Total              int                `json:"total"`
	Estimated          int                `json:"estimated"`
	Threads            int64              `json:"threads"`
	ThreadsUnavailable int                `json:"threadsUnavailable"`
	CPU                *float64           `json:"cpu"`
	Memory             *uint64            `json:"memory"`
	MemoryTotal        *uint64            `json:"memoryTotal"`
	OS                 string             `json:"os"`
	Warnings           []string           `json:"warnings"`
}
type processMonitor struct {
	mu        sync.Mutex
	snapshot  *processSnapshot
	sampledAt time.Time
	hostCPU   *hostCPUCounters
	appOwners map[string]string
}

func ptr[T any](v T) *T { return &v }
func counterRate(current, previous *uint64, seconds float64) *float64 {
	if current == nil || previous == nil || seconds <= 0 || *current < *previous {
		return nil
	}
	return ptr(float64(*current-*previous) / seconds)
}
func processRates(p *monitoredProcess, old monitoredProcess, seconds float64) {
	if p.StartedAt == 0 || p.StartedAt != old.StartedAt || seconds <= 0 {
		return
	}
	if p.CPUTime != nil && old.CPUTime != nil && *p.CPUTime >= *old.CPUTime {
		p.CPU = ptr((*p.CPUTime - *old.CPUTime) / seconds * 100)
	}
	p.ReadRate = counterRate(p.ReadBytes, old.ReadBytes, seconds)
	p.WriteRate = counterRate(p.WriteBytes, old.WriteBytes, seconds)
	if !p.networkUnavailable && !old.networkUnavailable {
		p.ReceiveRate = counterRate(p.ReceivedBytes, old.ReceivedBytes, seconds)
		p.SendRate = counterRate(p.SentBytes, old.SentBytes, seconds)
	}
	p.Wakeups = counterRate(p.wakeupTotal, old.wakeupTotal, seconds)
	if rate := counterRate(p.energyNJ, old.energyNJ, seconds); rate != nil {
		p.Power = ptr(*rate / 1e6)
	}
}
func identifyProcess(p *monitoredProcess) {
	if index := strings.Index(p.Path, ".app/"); index >= 0 {
		p.AppPath = p.Path[:index+4]
	} else if index := strings.Index(p.Path, ".app.bundle/"); index >= 0 {
		// macOS can execute a code-signing clone while argv[0] retains the
		// installed executable. Join its helpers under the installed App identity.
		p.AppPath = p.Path[:index+11]
		if original := strings.Index(p.originalPath, ".app/"); original >= 0 && filepath.IsAbs(p.originalPath) {
			bundle := p.originalPath[:original+4]
			if filepath.Base(bundle) == strings.TrimSuffix(filepath.Base(p.AppPath), ".bundle") {
				p.AppPath = bundle
			}
		}
	}
	if p.AppPath != "" {
		p.AppName = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p.AppPath), ".bundle"), ".app")
	} else {
		p.AppPath, p.AppName = platformAppIdentity(p.Path)
	}
	if p.Path != "" {
		p.Name = filepath.Base(p.Path)
	}
	if p.AppName != "" && strings.EqualFold(p.Name, p.AppName) {
		p.Name = p.AppName
	}
	if p.Name == "" {
		p.Name = strconv.Itoa(int(p.PID))
	}
}

// The desktop server knows which bundled App supplied its privileged binaries.
// Keep the installed executable path and attach that App's identity separately.
func processAppOwners(bundledPath string) map[string]string {
	app := monitoredProcess{Path: bundledPath}
	identifyProcess(&app)
	if app.AppPath == "" {
		return nil
	}
	return map[string]string{helperpaths.Current.HelperExecutable: app.AppPath, helperpaths.Current.CoreExecutable: app.AppPath}
}
func identifyOwnedProcess(p *monitoredProcess, owners map[string]string) {
	if bundle := owners[p.Path]; bundle != "" {
		p.AppPath = bundle
		p.AppName = strings.TrimSuffix(filepath.Base(bundle), ".app")
	}
}
func (m *processMonitor) sample(ctx context.Context) (*processSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshot != nil && time.Since(m.sampledAt) < 750*time.Millisecond {
		return m.snapshot, nil
	}
	items, warnings, err := collectMonitoredProcesses(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	snapshot := &processSnapshot{CapturedAt: now.UnixMilli(), Processes: items, Total: len(items), OS: runtime.GOOS, Warnings: warnings}
	previous := map[int32]monitoredProcess{}
	if m.snapshot != nil {
		for _, p := range m.snapshot.Processes {
			previous[p.PID] = p
		}
	}
	seconds := now.Sub(m.sampledAt).Seconds()
	detachReusedParents(items)
	for i := range items {
		p := &items[i]
		if p.Estimated {
			snapshot.Estimated++
		}
		identifyProcess(p)
		identifyOwnedProcess(p, m.appOwners)
		retainProcessNetwork(p, previous[p.PID])
		if old, ok := previous[p.PID]; ok {
			processRates(p, old, seconds)
		}
	}
	snapshot.Threads, snapshot.ThreadsUnavailable = summarizeProcessThreads(items)
	current, memory, totalMemory := collectMonitorHost(ctx)
	if current != nil {
		if m.hostCPU != nil {
			total := current.Total - m.hostCPU.Total
			idle := current.Idle - m.hostCPU.Idle
			if total > 0 && idle >= 0 {
				snapshot.CPU = ptr(math.Max(0, math.Min(100, (total-idle)/total*100)))
			}
		}
		m.hostCPU = current
	}
	snapshot.Memory = memory
	snapshot.MemoryTotal = totalMemory

	sort.Slice(items, func(i, j int) bool { return items[i].PID < items[j].PID })
	m.snapshot = snapshot
	m.sampledAt = now
	return snapshot, nil
}
func registerProcessMonitor(mux *routeMux, bundledPaths ...string) *processMonitor {
	monitor := &processMonitor{}
	if len(bundledPaths) > 0 {
		monitor.appOwners = processAppOwners(bundledPaths[0])
	}
	registerProcessMonitorActions(mux, monitor.appOwners)
	mux.get("GET /api/v1/process-monitor", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		snapshot, err := monitor.sample(ctx)
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, snapshot)
	})
	mux.handle("POST /api/v1/process-monitor/{pid}/signal", routeWrite, "发送进程信号", signalMonitoredProcess)
	return monitor
}
func signalMonitoredProcess(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.ParseInt(r.PathValue("pid"), 10, 32)
	if err != nil || pid <= 1 || int(pid) == os.Getpid() {
		http.Error(w, "受保护的进程或 PID 无效", 400)
		return
	}
	var body struct {
		StartedAt int64 `json:"startedAt"`
		Force     bool  `json:"force"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body) != nil || body.StartedAt <= 0 {
		http.Error(w, "需要进程启动时间", 400)
		return
	}
	started, err := monitoredProcessStartedAt(r.Context(), int32(pid))
	if err != nil {
		http.Error(w, "进程已退出", 404)
		return
	}
	if started != body.StartedAt {
		http.Error(w, "进程身份已变更，请刷新后重试", 409)
		return
	}
	err = sendMonitoredProcessSignal(r.Context(), int32(pid), body.Force)

	if err != nil {
		http.Error(w, fmt.Sprintf("结束进程失败：%v", err), 403)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// systemUID reports a system account's user ID: below the first login
// user's (UID_MIN), or nobody.
func systemUID(uid, min uint32) bool { return uid < min || uid == 65534 }

// parseLoginUIDMin is UID_MIN of a login.defs, 1000 without one.
func parseLoginUIDMin(defs string) uint32 {
	for line := range strings.SplitSeq(defs, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "UID_MIN" {
			if n, err := strconv.ParseUint(fields[1], 10, 32); err == nil {
				return uint32(n)
			}
		}
	}
	return 1000
}

// detachReusedParents unlinks processes from a parent PID that now names
// a process started after them: Windows reuses the PIDs of exited
// parents.
func detachReusedParents(items []monitoredProcess) {
	started := make(map[int32]int64, len(items))
	for _, p := range items {
		started[p.PID] = p.StartedAt
	}
	for i := range items {
		p := &items[i]
		if parent, ok := started[p.PPID]; ok && p.PPID != p.PID && p.StartedAt > 0 && parent > p.StartedAt {
			p.PPID = 0
		}
	}
}
