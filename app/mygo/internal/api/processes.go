package api

import (
	"context"
	"net/http"
	"strconv"
)

// MonitoredProcess is a process of GET /process-monitor. Metrics are nil
// when the system does not report them.
type MonitoredProcess struct {
	PID            int      `json:"pid"`
	PPID           int      `json:"ppid"`
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	Command        string   `json:"command"`
	AppName        string   `json:"appName,omitempty"`
	AppPath        string   `json:"appPath,omitempty"`
	User           string   `json:"user"`
	Mine           bool     `json:"mine"`
	System         bool     `json:"system"`
	Estimated      bool     `json:"estimated,omitempty"`
	StartedAt      int64    `json:"startedAt"`
	Status         string   `json:"status"`
	CPU            *float64 `json:"cpu"`
	CPUTime        *float64 `json:"cpuTime"`
	Memory         *float64 `json:"memory"`
	ResidentMemory *float64 `json:"residentMemory"`
	VirtualMemory  *float64 `json:"virtualMemory"`
	Threads        *float64 `json:"threads"`
	Power          *float64 `json:"power"`
	Wakeups        *float64 `json:"wakeups"`
	ReadBytes      *float64 `json:"readBytes"`
	WriteBytes     *float64 `json:"writeBytes"`
	ReceivedBytes  *float64 `json:"receivedBytes"`
	SentBytes      *float64 `json:"sentBytes"`
	ReadRate       *float64 `json:"readRate"`
	WriteRate      *float64 `json:"writeRate"`
	ReceiveRate    *float64 `json:"receiveRate"`
	SendRate       *float64 `json:"sendRate"`
}

// ProcessSnapshot is GET /process-monitor: the host's processes at
// CapturedAt (Unix milliseconds).
type ProcessSnapshot struct {
	CapturedAt         int64              `json:"capturedAt"`
	Processes          []MonitoredProcess `json:"processes"`
	Total              int                `json:"total"`
	Estimated          int                `json:"estimated,omitempty"`
	Threads            int64              `json:"threads"`
	ThreadsUnavailable int                `json:"threadsUnavailable,omitempty"`
	CPU                *float64           `json:"cpu"`
	Memory             *float64           `json:"memory"`
	MemoryTotal        *float64           `json:"memoryTotal"`
	OS                 string             `json:"os"`
	Warnings           []string           `json:"warnings"`
}

// ProcessMonitor samples the host's processes.
func (c *Client) ProcessMonitor(ctx context.Context) (ProcessSnapshot, error) {
	var v ProcessSnapshot
	return v, c.Get(ctx, "/process-monitor", &v)
}

// RevealProcess shows a process's executable in the file manager of the
// server's host.
func (c *Client) RevealProcess(ctx context.Context, pid int, startedAt int64) error {
	return c.Send(ctx, http.MethodPost, "/process-monitor/"+strconv.Itoa(pid)+"/reveal"+Query("startedAt", strconv.FormatInt(startedAt, 10)), nil, nil)
}

// SignalProcess asks a process to quit, or kills it when force.
func (c *Client) SignalProcess(ctx context.Context, pid int, startedAt int64, force bool) error {
	return c.Send(ctx, http.MethodPost, "/process-monitor/"+strconv.Itoa(pid)+"/signal", map[string]any{"startedAt": startedAt, "force": force}, nil)
}

// ProcessIcon downloads the icon of an App process (PNG bytes).
func (c *Client) ProcessIcon(ctx context.Context, pid int, startedAt int64) ([]byte, error) {
	return c.Raw(ctx, http.MethodGet, "/process-monitor/"+strconv.Itoa(pid)+"/icon"+Query("startedAt", strconv.FormatInt(startedAt, 10), "v", "3"))
}

// ProcessPathIcon downloads the icon of the App holding an executable
// path on the server's host (PNG bytes), whether or not it runs.
func (c *Client) ProcessPathIcon(ctx context.Context, path string) ([]byte, error) {
	return c.Raw(ctx, http.MethodGet, "/process-icon"+Query("path", path))
}

// Favicon downloads the website icon of domain's root (PNG bytes), the
// server fetching it through outbound, the tag in the running core: an
// *Error of status 404 for a site without one.
func (c *Client) Favicon(ctx context.Context, domain, outbound string) ([]byte, error) {
	return c.Raw(ctx, http.MethodGet, "/favicon"+Query("domain", domain, "outbound", outbound))
}

// FaviconCache is the website icons the server keeps.
type FaviconCache struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// FaviconCacheStats counts the website icons the server keeps.
func (c *Client) FaviconCacheStats(ctx context.Context) (FaviconCache, error) {
	var out FaviconCache
	return out, c.Get(ctx, "/favicons/cache", &out)
}

// ClearFaviconCache removes them, and tells how many it removed.
func (c *Client) ClearFaviconCache(ctx context.Context) (FaviconCache, error) {
	var out FaviconCache
	return out, c.Send(ctx, http.MethodDelete, "/favicons/cache", nil, &out)
}
