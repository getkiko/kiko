package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// ServerInfo is GET /info.
type ServerInfo struct {
	Version      string          `json:"version"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	PID          int             `json:"pid"`
	Elevated     bool            `json:"elevated"`
	DataDir      string          `json:"dataDir"`
	SingBox      SingBoxInfo     `json:"singBox"`
	Capabilities map[string]bool `json:"capabilities"`
	// ServerName is the server host's name.
	ServerName string `json:"serverName,omitempty"`
	// Client is who the server takes the caller for.
	Client *InfoClient `json:"client,omitempty"`
}

// InfoClient is the caller as GET /info sees it: this device, or the local server on
// the loopback listener.
type InfoClient struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Local bool   `json:"local"`
}

// SingBoxInfo is the core binary the server runs.
type SingBoxInfo struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// ServerStore is GET/PUT /store: the editor document, the saved profiles
// and the network-config library. The document stays raw here; package
// model decodes it.
type ServerStore struct {
	NetworkConfigs  json.RawMessage `json:"networkConfigs,omitempty"`
	Revision        uint64          `json:"revision"`
	Doc             json.RawMessage `json:"doc"`
	Profiles        json.RawMessage `json:"profiles"`
	ActiveProfileID *string         `json:"activeProfileId"`
}

// Reload is the progress of a configuration reload in the core.
type Reload struct {
	ID        string  `json:"id"`
	Phase     string  `json:"phase"`
	StartedAt string  `json:"startedAt"`
	ElapsedMs float64 `json:"elapsedMs"`
}

// Runtime is GET /runtime and the "runtime" event: the core's state.
type Runtime struct {
	Reload            *Reload  `json:"reload,omitempty"`
	State             string   `json:"state"`
	PID               int      `json:"pid,omitempty"`
	StartedAt         string   `json:"startedAt,omitempty"`
	UptimeSeconds     float64  `json:"uptimeSeconds"`
	RestartInSeconds  *float64 `json:"restartInSeconds,omitempty"`
	LastExit          string   `json:"lastExit,omitempty"`
	Error             string   `json:"error,omitempty"`
	DesiredRunning    bool     `json:"desiredRunning"`
	MemoryBytes       *float64 `json:"memoryBytes,omitempty"`
	ActiveConnections *float64 `json:"activeConnections,omitempty"`
	UploadRate        *float64 `json:"uploadRate,omitempty"`
	DownloadRate      *float64 `json:"downloadRate,omitempty"`
	UploadTotal       *float64 `json:"uploadTotal,omitempty"`
	DownloadTotal     *float64 `json:"downloadTotal,omitempty"`
	MetricsAvailable  bool     `json:"metricsAvailable"`
}

// Running reports whether the core is up.
func (r Runtime) Running() bool { return r.State == "running" }

// RuleSetStatus is one entry of GET /rulesets/status. Type is inline,
// local or remote; LastError the error of the last failed update, empty
// after a successful one.
type RuleSetStatus struct {
	Tag           string  `json:"tag"`
	LastUpdatedAt float64 `json:"lastUpdatedAt"`
	Type          string  `json:"type,omitempty"`
	LastError     string  `json:"lastError,omitempty"`
}

// RuleSetSource is the content of a rule set before its overrides, in
// sing-box source format; From is "core" or "download".
type RuleSetSource struct {
	Version int    `json:"version"`
	Rules   []any  `json:"rules"`
	From    string `json:"from"`
}

// ruleSetTimeout bounds a rule set update or source read: the core allows a
// download 60 s.
const ruleSetTimeout = 90 * time.Second

// Selector is a Clash selector or URLTest group in the running core.
type Selector struct {
	Name string   `json:"name"`
	Type string   `json:"type,omitempty"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
	// Mode is manual, fastest, fallback, loadbalance or smart, "" from a
	// server that does not report it; Strategy a load balance group's.
	Mode     string `json:"mode,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	// Members are the members' states as the core reports them.
	Members []SelectorMember `json:"members,omitempty"`
}

// SelectorMember is a group member's state in the running core.
type SelectorMember struct {
	Name string `json:"name"`
	// State is unknown, up, suspect or down.
	State string `json:"state,omitempty"`
	// Active is whether the group uses the member: its current member,
	// or for a load balance group, one in rotation.
	Active  bool `json:"active,omitempty"`
	DelayMS int  `json:"delayMs,omitempty"`
	// SuccessRate is the rate of successful connections, -1 without data.
	SuccessRate float64 `json:"successRate"`
	LatencyMS   int     `json:"latencyMs,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

// Member is the state of member name, false when not reported.
func (s Selector) Member(name string) (SelectorMember, bool) {
	for _, m := range s.Members {
		if m.Name == name {
			return m, true
		}
	}
	return SelectorMember{}, false
}

// FetchResult is POST /fetch: a subscription fetched by the server.
type FetchResult struct {
	Status               int    `json:"status"`
	Body                 string `json:"body"`
	SubscriptionUserinfo string `json:"subscriptionUserinfo,omitempty"`
}

// LatencyTarget is one target of POST /latency/test.
type LatencyTarget struct {
	ID   string `json:"id"`
	Host string `json:"host"`
	Port int    `json:"port"`
	Tag  string `json:"tag,omitempty"`
	URL  string `json:"url,omitempty"`
	// Unified asks for a unified delay (the core times the second
	// request of one connection).
	Unified bool `json:"unified,omitempty"`
}

// LatencyResult is the measurement of a target.
type LatencyResult struct {
	ID                string   `json:"id"`
	MedianConnectMs   *float64 `json:"medianConnectMs,omitempty"`
	EstimatedOneWayMs *float64 `json:"estimatedOneWayMs,omitempty"`
	SuccessSamples    int      `json:"successSamples"`
	TestedAt          string   `json:"testedAt"`
	Error             string   `json:"error,omitempty"`
	// Mode is how the target was measured: empty for a request through
	// the outbound by the running core, LatencyModeUnified for a unified
	// delay, LatencyModeDirect for a TCP handshake to its server while
	// the core is stopped.
	Mode string `json:"mode,omitempty"`
}

// LatencyModeDirect marks a LatencyResult measured without the core:
// the connect time to the server, not through the outbound.
const LatencyModeDirect = "direct"

// LatencyModeUnified marks a LatencyResult measured as a unified delay:
// the second HTTPS request over one connection through the outbound.
const LatencyModeUnified = "unified"

// LANDevice is a device seen on the local network.
type LANDevice struct {
	MAC       string   `json:"mac,omitempty"`
	Hostname  string   `json:"hostname,omitempty"`
	Addresses []string `json:"addresses"`
	Sources   []string `json:"sources"`
}

// ServerProcess is one entry of GET /processes.
type ServerProcess struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	AppName string `json:"appName,omitempty"`
	AppPath string `json:"appPath,omitempty"`
}

// InstalledApplication is one entry of GET /applications.
type InstalledApplication struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Applications is GET /applications.
type Applications struct {
	Supported    bool                   `json:"supported"`
	OS           string                 `json:"os"`
	Applications []InstalledApplication `json:"applications"`
	Warnings     []string               `json:"warnings"`
}

// SystemProxySettings is the editable part of the system proxy.
type SystemProxySettings struct {
	Enabled        bool     `json:"enabled"`
	Host           string   `json:"host"`
	PAC            bool     `json:"pac"`
	Guard          bool     `json:"guard"`
	GuardInterval  float64  `json:"guardInterval"`
	DefaultBypass  bool     `json:"defaultBypass"`
	ValidateBypass bool     `json:"validateBypass"`
	Bypass         []string `json:"bypass"`
}

// SystemProxyStatus is GET/PUT /system-proxy.
type SystemProxyStatus struct {
	SystemProxySettings
	Supported       bool     `json:"supported"`
	EffectiveBypass []string `json:"effectiveBypass"`
	Port            int      `json:"port,omitempty"`
	TUN             bool     `json:"tun"`
	Applied         bool     `json:"applied"`
}

// PolicyTestRequest is POST /policy/test.
type PolicyTestRequest struct {
	Section         string `json:"section"`
	Inbound         string `json:"inbound"`
	SourceIP        string `json:"sourceIp"`
	Domain          string `json:"domain"`
	DestinationIP   string `json:"destinationIp"`
	Network         string `json:"network"`
	Protocol        string `json:"protocol"`
	SourcePort      int    `json:"sourcePort"`
	DestinationPort int    `json:"destinationPort"`
}

// PolicyTestResult is the outcome of a policy test.
type PolicyTestResult struct {
	Action            string   `json:"action"`
	Rule              string   `json:"rule"`
	RuleIndex         int      `json:"ruleIndex"`
	OutboundPath      []string `json:"outboundPath"`
	DNSServer         string   `json:"dnsServer"`
	DNSDetour         string   `json:"dnsDetour"`
	ResolvedAddresses []string `json:"resolvedAddresses"`
	SelectedIP        string   `json:"selectedIp"`
	DNSRcode          string   `json:"dnsRcode"`
	TestedAt          string   `json:"testedAt"`
	ConfigRevision    string   `json:"configRevision"`
	// MITM is the HTTPS decryption outcome of the tested connection:
	// "decrypted", "skipped:<reason>" (scope, not_included, excluded,
	// builtin, pinned, authority_pending, override_skip, not_tls) or
	// "blocked-quic". Empty when the feature is off or the core predates
	// the policy result.
	MITM string `json:"mitm"`
	// ScriptMaybe lists the rule and dns scripts the test skipped; the
	// result is exact only if each gives the answer the test assumed.
	ScriptMaybe []string `json:"scriptMaybe"`
}

// LogHistory is GET /logs/history: rotated log files the server keeps,
// plus the macOS TUN logs under /var/log (SystemBytes), which cleanup
// empties. Warning reports a part the cleanup could not reach.
type LogHistory struct {
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	SystemBytes int64  `json:"systemBytes"`
	Token       string `json:"token"`
	Warning     string `json:"warning"`
}

// LogSize is GET /logs/size: what the server's logs occupy on disk.
type LogSize struct {
	Total int64 `json:"total"`
	Items []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Bytes int64  `json:"bytes"`
		// Path is a system log's live file (the macOS TUN core log).
		Path string `json:"path"`
	} `json:"items"`
}

// CoreLog is the TUN core log under /var/log: its live file and the size
// with rotated copies; ok is false when the server keeps none.
func (s LogSize) CoreLog() (path string, bytes int64, ok bool) {
	for _, item := range s.Items {
		if item.Key == "core" && item.Path != "" {
			return item.Path, item.Bytes, true
		}
	}
	return "", 0, false
}

// ---- endpoints ----

func (c *Client) Info(ctx context.Context) (ServerInfo, error) {
	var v ServerInfo
	gen := c.info.generation()
	if err := c.Get(ctx, "/info", &v); err != nil {
		return v, err
	}
	c.info.store(gen, v)
	return v, nil
}

// cachedInfo is the last GET /info of this connection, read when there is
// none yet.
func (c *Client) cachedInfo(ctx context.Context) (ServerInfo, error) {
	if info := c.info.load(); info != nil {
		return *info, nil
	}
	return c.Info(ctx)
}

func (c *Client) Store(ctx context.Context) (ServerStore, error) {
	var v ServerStore
	return v, c.Get(ctx, "/store", &v)
}

// SaveStore writes the store at its revision. A 409 *Error carries the
// server's current store in Body.
func (c *Client) SaveStore(ctx context.Context, store ServerStore) (ServerStore, error) {
	var v ServerStore
	return v, c.Send(ctx, http.MethodPut, "/store", store, &v)
}

func (c *Client) LocalAddresses(ctx context.Context) ([]string, error) {
	var v struct {
		Addresses []string `json:"addresses"`
	}
	return v.Addresses, c.Get(ctx, "/local-addresses", &v)
}

func (c *Client) Fetch(ctx context.Context, url string, headers map[string]string) (FetchResult, error) {
	var v FetchResult
	return v, c.Send(ctx, http.MethodPost, "/fetch", map[string]any{"url": url, "headers": headers}, &v)
}

// FetchPublic is Fetch limited to public addresses: the server refuses a
// connection (including each redirect) to a loopback, private, link-local,
// unspecified or multicast address.
func (c *Client) FetchPublic(ctx context.Context, url string) (FetchResult, error) {
	var v FetchResult
	return v, c.Send(ctx, http.MethodPost, "/fetch", map[string]any{"url": url, "publicOnly": true}, &v)
}

// FetchPublicRedirects is Fetch where only the redirects are limited to
// public addresses; the first address is free.
func (c *Client) FetchPublicRedirects(ctx context.Context, url string) (FetchResult, error) {
	var v FetchResult
	return v, c.Send(ctx, http.MethodPost, "/fetch", map[string]any{"url": url, "publicRedirects": true}, &v)
}

func (c *Client) Runtime(ctx context.Context) (Runtime, error) {
	var v Runtime
	return v, c.Get(ctx, "/runtime", &v)
}

func (c *Client) RuleSetStatus(ctx context.Context) ([]RuleSetStatus, error) {
	var v struct {
		RuleSets []RuleSetStatus `json:"ruleSets"`
	}
	return v.RuleSets, c.Get(ctx, "/rulesets/status", &v)
}

// UpdateRuleSet downloads a remote rule set, or reads a local one again, in
// the running core now.
func (c *Client) UpdateRuleSet(ctx context.Context, tag string) (RuleSetStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, ruleSetTimeout)
	defer cancel()
	var v RuleSetStatus
	return v, c.doWith(c.stream(), ctx, http.MethodPost, "/rulesets/update", map[string]string{"tag": tag}, &v)
}

// RuleSetSource reads a rule set's rules before its overrides: from the
// running core when it holds tag, otherwise downloaded from url ("" to read
// from the core only).
func (c *Client) RuleSetSource(ctx context.Context, tag, url string) (RuleSetSource, error) {
	ctx, cancel := context.WithTimeout(ctx, ruleSetTimeout)
	defer cancel()
	var v RuleSetSource
	return v, c.doWith(c.stream(), ctx, http.MethodPost, "/rulesets/source", map[string]string{"tag": tag, "url": url}, &v)
}

// Control starts, stops or restarts the core.
func (c *Client) Control(ctx context.Context, action string) (Runtime, error) {
	var v Runtime
	return v, c.Send(ctx, http.MethodPost, "/runtime/"+action, nil, &v)
}

// ApplyOptions are the desktop runtime options sent with a configuration.
type ApplyOptions struct {
	DisableTailscaleUDPDirect bool `json:"disableTailscaleUDPDirect"`
	// RuleMap names the configuration's route rules, so the server labels
	// connections by the route table's rules.
	RuleMap []RuleRef `json:"ruleMap,omitempty"`
	// DNSRuleMap names the configuration's DNS rules by their index in
	// dns.rules, dns.final at index -1, so the DNS inspector names the
	// DNS table's rules.
	DNSRuleMap []RuleRef `json:"dnsRuleMap,omitempty"`
}

// RuleRef ties route.rules[Index] of an emitted configuration to the route
// table: the rule's id, owner device, number and name. Final marks a
// device's default outbound rule, which has no id.
type RuleRef struct {
	Index   int    `json:"index"`
	ID      string `json:"id,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Section string `json:"section,omitempty"`
	Ordinal int    `json:"ordinal,omitempty"`
	Name    string `json:"name,omitempty"`
	Final   bool   `json:"final,omitempty"`
}

// ApplyResult is POST /config/apply.
type ApplyResult struct {
	Applied bool     `json:"applied"`
	Runtime *Runtime `json:"runtime,omitempty"`
}

// Apply sends a sing-box configuration (a JSON object) to the core.
func (c *Client) Apply(ctx context.Context, config []byte, options ApplyOptions) (ApplyResult, error) {
	return c.sendConfig(ctx, "/config/apply", config, options)
}

// ApplyLive applies a configuration to the running core without restarting
// it (POST /apply/live). Changes the core can only take by restarting fail
// with a *RestartRequiredError, leaving the applied configuration as it was.
func (c *Client) ApplyLive(ctx context.Context, config []byte, options ApplyOptions) (ApplyResult, error) {
	v, err := c.sendConfig(ctx, "/apply/live", config, options)
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		var body struct {
			Error           string `json:"error"`
			RestartRequired bool   `json:"restartRequired"`
		}
		if json.Unmarshal(apiErr.Body, &body) == nil && body.RestartRequired {
			return v, &RestartRequiredError{Reason: body.Error}
		}
	}
	return v, err
}

// RestartRequiredError is a live apply the core can only take by
// restarting; Reason says why.
type RestartRequiredError struct{ Reason string }

func (e *RestartRequiredError) Error() string { return e.Reason }

// AppliedConfig is the configuration last applied (GET /config/applied),
// an empty object before the first apply.
func (c *Client) AppliedConfig(ctx context.Context) (json.RawMessage, error) {
	var v json.RawMessage
	return v, c.Get(ctx, "/config/applied", &v)
}

// Runtime options travel inside the posted configuration under one of
// these keys. Servers from before the rename to Kiko only take the legacy
// key, and their core rejects any other unknown key.
const (
	runtimeOptionsKey       = "_kiko_runtime"
	legacyRuntimeOptionsKey = "_sbcfg_runtime"
)

// CapabilityKikoRuntime is the capability of servers that take runtime
// options under runtimeOptionsKey; servers without it are outdated.
const CapabilityKikoRuntime = "kikoRuntime"

// Outdated reports whether the server predates the rename to Kiko and
// should be upgraded.
func (i ServerInfo) Outdated() bool { return !i.Capabilities[CapabilityKikoRuntime] }

// OutdatedServerError is a failed apply on an outdated server.
type OutdatedServerError struct{ Err error }

func (e *OutdatedServerError) Error() string {
	return "服务端版本过旧，请升级：" + e.Err.Error()
}
func (e *OutdatedServerError) Unwrap() error { return e.Err }

func (c *Client) sendConfig(ctx context.Context, path string, config []byte, options ApplyOptions) (ApplyResult, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(config, &object); err != nil {
		return ApplyResult{}, err
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return ApplyResult{}, err
	}
	// The server's capabilities pick the key; when they cannot be read the
	// apply goes ahead with the current key and fails or succeeds on its own.
	key, outdated := runtimeOptionsKey, false
	if info, err := c.cachedInfo(ctx); err == nil && info.Outdated() {
		key, outdated = legacyRuntimeOptionsKey, true
	}
	object[key] = raw
	var v ApplyResult
	err = c.Send(ctx, http.MethodPost, path, object, &v)
	if err != nil && outdated {
		err = &OutdatedServerError{Err: err}
	}
	return v, err
}

func (c *Client) Logs(ctx context.Context) ([]string, error) {
	var v struct {
		Lines []string `json:"lines"`
	}
	return v.Lines, c.Get(ctx, "/logs", &v)
}

func (c *Client) ClearLogs(ctx context.Context) error {
	return c.Send(ctx, http.MethodDelete, "/logs", nil, nil)
}

func (c *Client) ClearCoreLogs(ctx context.Context) error {
	return c.Send(ctx, http.MethodDelete, "/logs/core", nil, nil)
}

func (c *Client) LogHistory(ctx context.Context) (LogHistory, error) {
	var v LogHistory
	return v, c.Get(ctx, "/logs/history", &v)
}

func (c *Client) LogSize(ctx context.Context) (LogSize, error) {
	var v LogSize
	return v, c.Get(ctx, "/logs/size", &v)
}

// ClearSystemLogs empties the TUN core and helper logs under /var/log.
func (c *Client) ClearSystemLogs(ctx context.Context) (LogHistory, error) {
	var v LogHistory
	return v, c.Send(ctx, http.MethodDelete, "/logs/system", nil, &v)
}

func (c *Client) CleanupLogHistory(ctx context.Context, token string) (LogHistory, error) {
	var v LogHistory
	return v, c.Send(ctx, http.MethodPost, "/logs/history", map[string]string{"token": token}, &v)
}

// SetMode sets the Clash mode (Rule, Global, Direct).
func (c *Client) SetMode(ctx context.Context, mode string) error {
	return c.Send(ctx, http.MethodPatch, "/runtime/mode", map[string]string{"mode": mode}, nil)
}

func (c *Client) Selectors(ctx context.Context) ([]Selector, error) {
	var v struct {
		Selectors []Selector `json:"selectors"`
	}
	return v.Selectors, c.Get(ctx, "/runtime/selectors", &v)
}

// Select chooses outbound in a selector of the running core.
func (c *Client) Select(ctx context.Context, selector, outbound string) error {
	return c.Send(ctx, http.MethodPatch, "/runtime/selector", map[string]string{"selector": selector, "outbound": outbound}, nil)
}

func (c *Client) Latency(ctx context.Context, targets []LatencyTarget) ([]LatencyResult, error) {
	var v struct {
		Results []LatencyResult `json:"results"`
	}
	return v.Results, c.Send(ctx, http.MethodPost, "/latency/test", map[string]any{"targets": targets}, &v)
}

func (c *Client) SystemProxy(ctx context.Context) (SystemProxyStatus, error) {
	var v SystemProxyStatus
	return v, c.Get(ctx, "/system-proxy", &v)
}

func (c *Client) SaveSystemProxy(ctx context.Context, settings SystemProxySettings) (SystemProxyStatus, error) {
	var v SystemProxyStatus
	return v, c.Send(ctx, http.MethodPut, "/system-proxy", settings, &v)
}

func (c *Client) Processes(ctx context.Context) ([]ServerProcess, error) {
	var v struct {
		Processes []ServerProcess `json:"processes"`
	}
	return v.Processes, c.Get(ctx, "/processes", &v)
}

func (c *Client) Applications(ctx context.Context, refresh bool) (Applications, error) {
	path := "/applications"
	if refresh {
		path += "?refresh=1"
	}
	var v Applications
	return v, c.Get(ctx, path, &v)
}

// LANDevices lists devices on the LAN, reading the given DHCP lease files.
func (c *Client) LANDevices(ctx context.Context, leaseFiles []string) ([]LANDevice, []string, error) {
	path := "/lan-devices"
	sep := "?"
	for _, file := range leaseFiles {
		if file != "" {
			path += sep + "lease_file=" + esc(file)
			sep = "&"
		}
	}
	var v struct {
		Devices  []LANDevice `json:"devices"`
		Warnings []string    `json:"warnings"`
	}
	err := c.Get(ctx, path, &v)
	return v.Devices, v.Warnings, err
}

func (c *Client) TestPolicy(ctx context.Context, request PolicyTestRequest) (PolicyTestResult, error) {
	var v PolicyTestResult
	return v, c.Send(ctx, http.MethodPost, "/policy/test", request, &v)
}
