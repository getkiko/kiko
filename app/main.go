package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var version = "dev"

type storeState struct {
	NetworkConfigs  json.RawMessage `json:"networkConfigs,omitempty"`
	Revision        uint64          `json:"revision"`
	Doc             json.RawMessage `json:"doc"`
	Profiles        json.RawMessage `json:"profiles"`
	ActiveProfileID any             `json:"activeProfileId"`
}
type persistedRuntime struct {
	DesiredRunning bool      `json:"desiredRunning"`
	LastApplied    time.Time `json:"lastApplied,omitempty"`
}
type reloadProgress struct {
	ID        string    `json:"id"`
	ElapsedMS int64     `json:"elapsedMs"`
	Phase     string    `json:"phase"`
	StartedAt time.Time `json:"startedAt"`
}
type runtimeState struct {
	Reload            *reloadProgress `json:"reload,omitempty"`
	State             string          `json:"state"`
	PID               int             `json:"pid,omitempty"`
	StartedAt         time.Time       `json:"startedAt,omitempty"`
	UptimeSeconds     int64           `json:"uptimeSeconds"`
	RestartIn         int             `json:"restartInSeconds,omitempty"`
	LastExit          string          `json:"lastExit,omitempty"`
	Error             string          `json:"error,omitempty"`
	DesiredRunning    bool            `json:"desiredRunning"`
	MemoryBytes       uint64          `json:"memoryBytes,omitempty"`
	ActiveConnections int             `json:"activeConnections,omitempty"`
	UploadRate        uint64          `json:"uploadRate,omitempty"`
	DownloadRate      uint64          `json:"downloadRate,omitempty"`
	UploadTotal       uint64          `json:"uploadTotal,omitempty"`
	DownloadTotal     uint64          `json:"downloadTotal,omitempty"`
	MetricsAvailable  bool            `json:"metricsAvailable"`
}
type clashSelector struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
	// Mode is manual, fastest, fallback, loadbalance or smart; Strategy
	// a load balance group's strategy.
	Mode     string `json:"mode,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	// Members are the members' states, in All's order, when the core
	// reports them.
	Members []clashGroupMember `json:"members,omitempty"`
}

// clashGroupMember is a group member as the core reports it.
type clashGroupMember struct {
	Name string `json:"name"`
	// State is unknown, up, suspect or down.
	State  string `json:"state,omitempty"`
	Active bool   `json:"active,omitempty"`
	// DelayMS is the member's last URL test delay, 0 for none.
	DelayMS int `json:"delayMs,omitempty"`
	// SuccessRate is the rate of successful connections, -1 without data.
	SuccessRate float64 `json:"successRate"`
	// LatencyMS is the decayed time to first byte of connections.
	LatencyMS int     `json:"latencyMs,omitempty"`
	Score     float64 `json:"score,omitempty"`
}
type server struct {
	mu           sync.Mutex
	configMu     sync.Mutex
	activeReload *coreReload
	// liveDial connects to the core API for a live apply; nil dials it.
	liveDial                    func() (liveApplier, func(), error)
	controlMu                   sync.Mutex
	systemDNSMu                 sync.Mutex
	singBox, dataDir, sbVersion string
	// browsers are the browsers opened through one outbound (browser.go).
	browsers browsers
	// favicons are the website icons (favicon.go), made on first use.
	favicons                  *favicons
	faviconOnce               sync.Once
	store                     storeState
	persisted                 persistedRuntime
	state                     runtimeState
	cmd                       *exec.Cmd
	helperPID                 int
	stopping                  bool
	stopDone                  chan struct{}
	startCancel               context.CancelFunc
	coreDone                  chan struct{}
	coreStopped               chan struct{}
	retry                     int
	restartTimer              *time.Timer
	restartGeneration         uint64
	restartAt                 time.Time
	logs                      *logBuffer
	events                    map[chan string]struct{}
	clashAddr, clashSecret    string
	usePrivilegedHelper       bool
	autoStart                 bool
	disableTailscaleUDPDirect bool
	metricsSampleAt           time.Time
	metricsUploadTotal        uint64
	metricsDownloadTotal      uint64
	listenAddr                string
	shutdownToken             string
	shutdownCancel            context.CancelFunc
	shutdownCh                chan struct{}
	shutdownOnce              sync.Once
	shuttingDown              bool
	restoreSystemDNS          func() error
	sys                       sysProxyState
	rpc                       grpcState
	mitm                      mitmState
	connections               connectionHistory
	connQuery                 connectionQueryState
	processMonitor            *processMonitor
	td                        taildropTasks
	ep                        endpointState
	usb                       usbipShare
	sync                      syncRuntime
	usage                     *usageStore
	ruleMap                   ruleMapState
	remote                    *remoteManager
	gateOnce                  sync.Once
	gate                      *writeGate
}

type stopTarget struct {
	cmd       *exec.Cmd
	helperPID int
	exited    <-chan struct{}
	stopped   chan struct{}
}

// legacyCoreFlag is the old name of --core, accepted but not listed.
const legacyCoreFlag = "sing-box"

// printUsage prints the flags of fs without the hidden legacy alias.
func printUsage(fs *flag.FlagSet) {
	fmt.Fprintf(fs.Output(), "Usage of %s:\n", fs.Name())
	listed := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
	listed.SetOutput(fs.Output())
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name != legacyCoreFlag {
			listed.Var(f.Value, f.Name, f.Usage)
			listed.Lookup(f.Name).DefValue = f.DefValue
		}
	})
	listed.PrintDefaults()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pair" {
		os.Exit(runPairCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
	desktopToken := os.Getenv("KIKO_DESKTOP_SHUTDOWN_TOKEN")
	_ = os.Unsetenv("KIKO_DESKTOP_SHUTDOWN_TOKEN")
	var binary, listen, remoteListen, data string
	var privilegedHelper, autoStart bool
	flag.StringVar(&binary, "core", "", "kiko-core absolute path (required)")
	// --sing-box is the flag's name before the core was renamed kiko-core,
	// kept as a hidden alias for existing service files and scripts.
	flag.StringVar(&binary, legacyCoreFlag, "", "")
	flag.StringVar(&listen, "listen", defaultLocalListen, "loopback listen address for the local client")
	flag.StringVar(&remoteListen, "remote-listen", "", "enable remote TLS access on this address (persisted to remote.json)")
	flag.StringVar(&data, "data-dir", "", "data directory")
	flag.BoolVar(&privilegedHelper, "privileged-helper", false, "start kiko-core through the macOS privileged helper")
	flag.BoolVar(&autoStart, "auto-start", false, "start kiko-core on launch and keep retrying failures until stopped")
	flag.Usage = func() { printUsage(flag.CommandLine) }
	flag.Parse()
	if err := validateLoopbackListen(listen); err != nil {
		log.Fatal(err)
	}
	if remoteListen != "" {
		if _, err := normalizeRemoteListen(remoteListen); err != nil {
			log.Fatalf("--remote-listen: %v", err)
		}
	}
	if binary == "" || !filepath.IsAbs(binary) {
		log.Fatal("--core must be an absolute path")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() {
		log.Fatalf("invalid kiko-core: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		log.Fatal("kiko-core is not executable")
	}
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 10*time.Second)
	out, err := hiddenCommandContext(versionCtx, binary, "version").CombinedOutput()
	versionCancel()
	if err != nil {
		log.Fatalf("kiko-core version: %v: %s", err, out)
	}
	if data == "" {
		if data, err = defaultDataDir(true); err != nil {
			log.Fatal(err)
		}
	}
	s, err := newServer(binary, data, strings.TrimSpace(string(out)))
	if err != nil {
		log.Fatal(err)
	}
	s.usePrivilegedHelper = privilegedHelper
	if autoStart {
		if err = s.enableAutoStart(); err != nil {
			log.Fatal(err)
		}
	}
	s.listenAddr = listen
	s.shutdownToken = desktopToken
	if s.shutdownToken != "" {
		prepareDesktopConsole()
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	s.shutdownCancel = cancel
	if s.shutdownToken != "" {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			cancel()
		}()
	}
	handler := s.routes()
	s.remote.setHandler(handler)
	localListener, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
	h := &http.Server{Addr: listen, Handler: localHostGuard(localListener.Addr(), handler), ReadHeaderTimeout: 5 * time.Second}
	s.writeLocalAPIAddress(localListener.Addr().String())
	if remoteListen != "" {
		if status := s.remote.configure(remoteConfig{Enabled: true, Listen: remoteListen}); status.Error != "" {
			log.Fatalf("remote access failed to start: %s", status.Error)
		}
	} else if status := s.remote.startSaved(); status.Error != "" {
		log.Printf("remote access failed to start: %s", status.Error)
	}
	shutdownDone := make(chan struct{})
	if s.shutdownToken != "" {
		watchDesktopConsole(cancel, shutdownDone)
	}
	go func() {
		<-ctx.Done()
		s.shutdown()
		s.remote.close()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(c)
		_ = h.Close()
		close(shutdownDone)
	}()
	if s.persisted.DesiredRunning {
		go s.start()
		go s.syncSysProxy()
	}
	go s.apiSupported()
	go s.monitor(ctx)
	go s.usageMaintenance(ctx)
	go s.sysProxyGuard(ctx)
	go s.syncLoop(ctx)
	log.Printf("Kiko server listening on http://%s", localListener.Addr())
	if err = h.Serve(localListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
	}
	cancel()
	<-shutdownDone
}

func newServer(binary, data, ver string) (*server, error) {
	for _, d := range []string{data, filepath.Join(data, "runtime"), filepath.Join(data, "logs")} {
		if e := os.MkdirAll(d, 0o700); e != nil {
			return nil, e
		}
	}
	s := &server{shutdownCh: make(chan struct{}), singBox: binary, dataDir: data, sbVersion: ver, state: runtimeState{State: "stopped"}, events: map[chan string]struct{}{}, logs: newLogBuffer(filepath.Join(data, "logs"))}
	s.logs.load()
	s.logs.system = privilegedSystemLogs()
	s.usage = newUsageStore(data)
	s.loadRuleMap()
	s.usage.ruleLabel = s.ruleMap.label
	s.connections.resolveRule = s.ruleMap.resolve
	_ = readJSON(filepath.Join(data, "editor.json"), &s.store)
	s.migrateBurrowData()
	_ = readJSON(filepath.Join(data, "runtime-state.json"), &s.persisted)
	s.disableTailscaleUDPDirect = tailscaleUDPDirectDisabled(s.store.Doc)
	s.loadRuntimeControl()
	s.loadConnectionSettings()
	s.loadSysProxy()
	if err := recoverSystemProxy(s.sysProxyRestorePath()); err != nil {
		return nil, fmt.Errorf("restore previous system proxy: %w", err)
	}
	s.loadSync()
	s.remote = newRemoteManager(s)
	s.state.DesiredRunning = s.persisted.DesiredRunning
	return s, nil
}

func (s *server) enableAutoStart() error {
	s.autoStart = true
	s.persisted.DesiredRunning = true
	s.state.DesiredRunning = true
	return atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
}

func (s *server) loadRuntimeControl() {
	b, err := os.ReadFile(filepath.Join(s.dataDir, "runtime", "config.json"))
	if err != nil {
		return
	}
	var value struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
		Services []struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			ListenPort int    `json:"listen_port"`
			Secret     string `json:"secret"`
		} `json:"services"`
	}
	if json.Unmarshal(b, &value) == nil {
		s.clashAddr = value.Experimental.ClashAPI.ExternalController
		s.clashSecret = value.Experimental.ClashAPI.Secret
		for _, service := range value.Services {
			if service.Type == "api" && isAPIServiceTag(service.Tag) {
				s.rpc.port, s.rpc.secret = service.ListenPort, service.Secret
			}
		}
	}
}

func validateOutboundReferences(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Route struct {
			Final string `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(b, &config); err != nil {
		return fmt.Errorf("配置 JSON 无效: %w", err)
	}
	tags := make(map[string]struct{}, len(config.Outbounds))
	for _, outbound := range config.Outbounds {
		if outbound.Tag != "" {
			tags[outbound.Tag] = struct{}{}
		}
	}
	if final := strings.TrimSpace(config.Route.Final); final != "" {
		if _, ok := tags[final]; !ok {
			return fmt.Errorf("default outbound not found: %s", final)
		}
	}
	return nil
}

func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func token() string { b := make([]byte, 24); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func (s *server) routes() http.Handler { return s.serveAPI(s.routeTable()) }

// routeTable registers every API route with its write-gate class.
func (s *server) routeTable() *routeMux {
	m := newRouteMux(s)
	m.get("GET /api/v1/info", s.info)
	m.get("GET /api/v1/lock", s.lockStatus)
	m.get("GET /api/v1/store", s.getStore)
	m.handle("PUT /api/v1/store", routeWrite, "保存配置", s.putStore)
	m.handle("POST /api/v1/config/apply", routeWrite, "应用配置", s.apply)
	m.handle("POST /api/v1/apply/live", routeWrite, "不停机应用", s.applyLive)
	m.get("GET /api/v1/config/applied", s.appliedConfig)
	m.get("GET /api/v1/runtime", s.runtime)
	m.get("GET /api/v1/rulesets/status", s.ruleSetStatus)
	m.handle("POST /api/v1/rulesets/update", routeOwn, "更新规则集", s.updateRuleSet)
	m.handle("POST /api/v1/rulesets/source", routeRead, "", s.ruleSetSource)
	m.get("GET /api/v1/processes", listProcesses)
	registerApplications(m)
	s.processMonitor = registerProcessMonitor(m, s.singBox)
	m.get("GET /api/v1/lan-devices", listLANDevices)
	registerLANScan(m)
	m.get("GET /api/v1/local-addresses", localAddresses)
	m.handle("POST /api/v1/shutdown", routeExempt, "退出服务", s.desktopShutdown)
	m.handle("POST /api/v1/runtime/start", routeWrite, "启动核心", func(w http.ResponseWriter, r *http.Request) {
		s.logs.add("[kiko] 收到启动请求")
		if err := s.start(); err != nil {
			s.logs.add("[kiko] 启动请求失败: " + err.Error())
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		s.logs.add("[kiko] 启动请求完成")
		writeJSON(w, 200, s.snapshot())
	})
	m.handle("POST /api/v1/runtime/stop", routeWrite, "停止核心", func(w http.ResponseWriter, r *http.Request) {
		s.logs.add("[kiko] 收到停止请求")
		s.stopAsync(true, detachWriteGate(r))
		writeJSON(w, 200, s.snapshot())
	})
	m.handle("POST /api/v1/runtime/restart", routeWrite, "重启核心", func(w http.ResponseWriter, r *http.Request) {
		s.restartAsync(detachWriteGate(r))
		writeJSON(w, 200, s.snapshot())
	})
	m.handle("PATCH /api/v1/runtime/mode", routeWrite, "切换代理模式", s.setMode)
	m.get("GET /api/v1/runtime/selectors", s.getSelectors)
	m.handle("PATCH /api/v1/runtime/selector", routeWrite, "切换出站", s.setSelector)
	m.get("GET /api/v1/logs/core", s.streamCoreLogs)
	m.handle("DELETE /api/v1/logs/core", routeWrite, "清空核心日志", s.clearCoreLogs)
	m.get("GET /api/v1/connections", s.queryConnections)
	m.get("GET /api/v1/connections/{id}", s.getConnection)
	m.get("GET /api/v1/connections/export", s.exportConnections)
	m.handle("DELETE /api/v1/connections/history", routeWrite, "清空连接记录", s.clearConnectionHistory)
	m.get("GET /api/v1/connections/settings", s.getConnectionSettings)
	m.handle("PATCH /api/v1/connections/settings", routeWrite, "修改连接记录设置", s.setConnectionSettings)
	m.get("GET /api/v1/usage", s.getUsage)
	m.get("GET /api/v1/usage/export", s.exportUsage)
	m.handle("PATCH /api/v1/usage/settings", routeWrite, "修改用量设置", s.configureUsage)
	m.handle("DELETE /api/v1/usage/history", routeWrite, "清空用量记录", s.clearUsage)
	m.get("GET /api/v1/metrics/history", s.metricsHistory)
	m.get("GET /api/v1/http/capabilities", s.httpCaptureCapabilities)
	m.get("GET /api/v1/http/stream", s.streamHTTPCapture)
	m.get("GET /api/v1/http/records", s.httpCaptureRecords)
	m.get("GET /api/v1/http/records/{id}", s.httpCaptureRecord)
	m.get("GET /api/v1/http/records/{id}/body", s.httpCaptureBody)
	m.handle("POST /api/v1/http/clear", routeWrite, "清空 HTTP 记录", s.clearHTTPCapture)
	m.handle("DELETE /api/v1/http/records/{id}", routeWrite, "删除 HTTP 记录", s.deleteHTTPRecord)
	m.handle("POST /api/v1/http/persist/purge", routeWrite, "删除已保存的 HTTP 记录", s.purgeHTTPPersisted)
	m.handle("POST /api/v1/http/replay", routeWrite, "重放 HTTP 请求", s.replayHTTPCapture)
	m.handle("POST /api/v1/http/har", routeRead, "", s.exportHTTPHAR)
	m.get("GET /api/v1/scripts/status", s.scriptStatus)
	m.handle("POST /api/v1/scripts/evaluate", routeWrite, "测试脚本", s.scriptEvaluate)
	m.handle("POST /api/v1/scripts/run", routeWrite, "运行脚本", s.scriptRun)
	m.get("GET /api/v1/scripts/logs", s.scriptLogs)
	m.get("GET /api/v1/scripts/notifications", s.scriptNotifications)
	m.get("GET /api/v1/scripts/store", s.scriptStoreList)
	m.get("GET /api/v1/scripts/store/value", s.scriptStoreGet)
	m.handle("PUT /api/v1/scripts/store/value", routeWrite, "修改脚本存储", s.scriptStoreSet)
	m.handle("DELETE /api/v1/scripts/store/value", routeWrite, "删除脚本存储", s.scriptStoreDelete)
	m.get("GET /api/v1/dns/capabilities", s.dnsCapabilities)
	m.get("GET /api/v1/dns/stream", s.streamDNS)
	m.get("GET /api/v1/dns/queries", s.dnsQueries)
	m.get("GET /api/v1/dns/queries/{id}", s.dnsQuery)
	m.get("GET /api/v1/dns/rule-map", s.dnsRuleMap)
	m.handle("DELETE /api/v1/dns/history", routeWrite, "清空 DNS 记录", s.clearDNSHistory)
	m.handle("DELETE /api/v1/connections", routeWrite, "关闭全部连接", s.closeAllConnections)
	m.handle("DELETE /api/v1/connections/{id}", routeWrite, "关闭连接", s.closeConnection)
	m.handle("POST /api/v1/connections/close", routeWrite, "关闭筛选的连接", s.closeMatchingConnections)
	s.registerEndpointRoutes(m)
	s.registerMITMRoutes(m)
	s.registerSyncRoutes(m)
	m.get("GET /api/v1/system-proxy", s.getSysProxy)
	m.handle("PUT /api/v1/system-proxy", routeWrite, "修改系统代理", s.putSysProxy)
	m.get("GET /api/v1/system-proxy/pac", s.sysProxyPAC)
	m.get("GET /api/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"lines": s.logs.snapshot()})
	})
	m.handle("DELETE /api/v1/logs", routeWrite, "清空日志", func(w http.ResponseWriter, r *http.Request) { s.logs.clear(); w.WriteHeader(204) })
	m.get("GET /api/v1/logs/size", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.logs.sizes())
	})
	m.handle("DELETE /api/v1/logs/system", routeWrite, "清空系统日志", func(w http.ResponseWriter, r *http.Request) {
		result, err := s.logs.clearSystem()
		if err != nil {
			status := http.StatusConflict
			if errors.Is(err, errNoSystemLogs) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"error": err.Error(), "files": result.Files, "bytes": result.Bytes})
			return
		}
		writeJSON(w, 200, result)
	})
	m.get("GET /api/v1/logs/history", func(w http.ResponseWriter, r *http.Request) {
		result, err := s.logs.previewHistory()
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, result)
	})
	m.handle("POST /api/v1/logs/history", routeWrite, "清理历史日志", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil || in.Token == "" {
			writeJSON(w, 400, map[string]any{"error": "请先预览日志清理范围"})
			return
		}
		result, err := s.logs.cleanupHistory(in.Token)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errLogPreviewExpired) {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, result)
	})
	m.get("GET /api/v1/events", s.sse)
	m.handle("POST /api/v1/fetch", routeRead, "", s.fetch)
	m.get("GET /api/v1/fetch", s.fetch)
	m.handle("POST /api/v1/latency/test", routeRead, "", s.latency)
	s.registerRemoteRoutes(m)
	return m
}

// localHostGuard rejects requests whose Host is not a loopback name on the
// listening port, which stops DNS rebinding from a web page. A listener
// bound to a non-loopback address is left alone.
func localHostGuard(listen net.Addr, next http.Handler) http.Handler {
	tcp, ok := listen.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return next
	}
	port := strconv.Itoa(tcp.Port)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedLocalHost(r.Host, port) {
			http.Error(w, "host rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedLocalHost(host, port string) bool {
	name, p, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	if p != port {
		return false
	}
	switch strings.ToLower(name) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func (s *server) serveAPI(m http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		closing := s.shuttingDown
		s.mu.Unlock()
		if closing && r.Method != http.MethodGet && r.URL.Path != "/api/v1/shutdown" {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !requestClient(r).Remote {
			// Browsers label cross-site and same-site (another local port)
			// requests; only same-origin and user-initiated ones pass.
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-site request rejected", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != "GET" {
			requiredType := "application/json"
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/taildrop/send" {
				requiredType = "multipart/form-data"
			}
			mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mediaType != requiredType {
				http.Error(w, requiredType+" required", http.StatusUnsupportedMediaType)
				return
			}
			// Bearer-authenticated remote clients carry no cookies, so a
			// cross-site request cannot borrow their credentials.
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host && o != "https://"+r.Host && !requestClient(r).Remote {
				http.Error(w, "origin rejected", 403)
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}

type processInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	AppName string `json:"appName,omitempty"`
	AppPath string `json:"appPath,omitempty"`
}

func listProcesses(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS == "windows" {
		writeWindowsProcesses(w)
		return
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "android" {
		items := procProcesses("/proc")
		if items == nil {
			items = []processInfo{}
		}
		sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
		writeJSON(w, 200, map[string]any{"processes": items})
		return
	}
	out, err := exec.Command("ps", "-axo", "comm=").Output()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	seen := map[string]bool{}
	items := make([]processInfo, 0)
	for _, line := range strings.Split(string(out), "\n") {
		path := strings.TrimSpace(line)
		if path == "" || seen[path] {
			continue
		}
		appPath, appName := "", ""
		if index := strings.Index(path, ".app/"); index >= 0 {
			appPath = path[:index+4]
			appName = strings.TrimSuffix(filepath.Base(appPath), ".app")
		}
		seen[path] = true
		name := filepath.Base(path)
		if name != "." && name != "/" {
			items = append(items, processInfo{Name: name, Path: path, AppName: appName, AppPath: appPath})
		}
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	writeJSON(w, 200, map[string]any{"processes": items})
}

type windowsProcess struct {
	Name string `json:"ProcessName"`
	Path string `json:"Path"`
}

func writeWindowsProcesses(w http.ResponseWriter) {
	// Get-Process exposes the executable path, which lets the desktop shell
	// resolve the native application icon. Processes without a readable path
	// are omitted because they cannot produce a useful process rule.
	script := `[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false; $ErrorActionPreference = 'SilentlyContinue'; Get-Process | ForEach-Object { $path = $_.Path; if ($path) { [PSCustomObject]@{ ProcessName = $_.ProcessName; Path = $path } } } | ConvertTo-Json -Compress`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	hideProcessWindow(cmd)
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		writeJSON(w, 200, map[string]any{"processes": []processInfo{}})
		return
	}
	var values []windowsProcess
	if json.Unmarshal(out, &values) != nil {
		var one windowsProcess
		if json.Unmarshal(out, &one) == nil {
			values = []windowsProcess{one}
		}
	}
	seen := map[string]bool{}
	items := make([]processInfo, 0, len(values))
	for _, value := range values {
		path := strings.TrimSpace(value.Path)
		if path == "" || seen[strings.ToLower(path)] {
			continue
		}
		seen[strings.ToLower(path)] = true
		name := filepath.Base(path)
		if name == "." || name == "\\" || name == "" {
			name = strings.TrimSpace(value.Name)
		}
		items = append(items, processInfo{Name: name, Path: path})
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	writeJSON(w, 200, map[string]any{"processes": items})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	if e := json.NewDecoder(r.Body).Decode(v); e != nil {
		http.Error(w, e.Error(), 400)
		return false
	}
	return true
}
func (s *server) info(w http.ResponseWriter, r *http.Request) {
	client := requestClient(r)
	writeJSON(w, 200, map[string]any{
		"version": version, "os": runtime.GOOS, "arch": runtime.GOARCH, "pid": os.Getpid(), "elevated": isElevated(), "dataDir": s.dataDir,
		"serverName": serverName(),
		"client":     map[string]any{"id": client.ID, "name": client.Name, "local": !client.Remote},
		"singBox":    map[string]any{"path": s.singBox, "version": s.sbVersion},
		"capabilities": map[string]bool{
			"lanDevices": lanDevicesSupported(), "process": true, "processMonitor": true, "logs": true, "fetch": true, "fetchPublic": true, "fetchPublicRedirects": true, "latency": true,
			"clashMetrics": strings.Contains(s.sbVersion, "with_clash_api"), "usbip": strings.Contains(s.sbVersion, "with_usbip"), "tailscale": strings.Contains(s.sbVersion, "with_tailscale"),
			// kikoRuntime: apply takes runtime options under _kiko_runtime.
			"kikoRuntime": true, "remoteAccess": true, "writeLock": true, "groupModes": s.groupModesSupported(), "mitm": s.mitmSupported(), "rewriteBody": s.httpRewriteBodySupported(), "scripts": s.scriptsSupported(), "scriptsCron": s.scriptsCronSupported(), "scriptsRule": s.scriptsRuleSupported(),
		},
	})
}

func (s *server) desktopShutdown(w http.ResponseWriter, r *http.Request) {
	if s.shutdownToken == "" || s.shutdownCancel == nil {
		http.NotFound(w, r)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || requestClient(r).Remote || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Kiko-Shutdown-Token")), []byte(s.shutdownToken)) != 1 {
		http.Error(w, "shutdown denied", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	go s.shutdownCancel()
}

func (s *server) getStore(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, s.store)
}
func (s *server) putStore(w http.ResponseWriter, r *http.Request) {
	var v storeState
	if !decode(w, r, &v) {
		return
	}
	s.mu.Lock()
	if v.Revision != s.store.Revision {
		current := s.store
		s.mu.Unlock()
		writeJSON(w, 409, current)
		return
	}
	if len(v.NetworkConfigs) == 0 {
		v.NetworkConfigs = s.store.NetworkConfigs
	}
	// A client from before the Burrow rename may still save holepunch entries.
	migrateBurrowStore(&v)
	v.Revision++
	if e := atomicJSON(filepath.Join(s.dataDir, "editor.json"), v); e != nil {
		s.mu.Unlock()
		http.Error(w, e.Error(), 500)
		return
	}
	s.store = v
	s.mu.Unlock()
	writeJSON(w, 200, v)
	s.emit("store", v)
	s.syncTrack(v)
}

// applyCandidate is a configuration that passed generation and checks,
// ready for either apply path.
type applyCandidate struct {
	clean, runtime []byte
	disableUDP     bool
	output         []byte
	// ruleMap names the configuration's route rules, dnsRuleMap its DNS
	// rules.
	ruleMap, dnsRuleMap []ruleRef
}

// checkCandidate decodes the posted configuration, injects the runtime
// services and runs the core's checks. On failure it has answered w.
func (s *server) checkCandidate(w http.ResponseWriter, r *http.Request) (*applyCandidate, bool) {
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return nil, false
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		http.Error(w, "configuration must be a JSON object", 400)
		return nil, false
	}
	// The core only knows the burrow type; a configuration generated from a
	// document with holepunch entries is converted before it is checked.
	renameBurrowEntries(obj)
	var runtimeOptions struct {
		DisableTailscaleUDPDirect bool      `json:"disableTailscaleUDPDirect"`
		RuleMap                   []ruleRef `json:"ruleMap"`
		DNSRuleMap                []ruleRef `json:"dnsRuleMap"`
	}
	// _sbcfg_runtime is the key desktop apps sent before the rename to Kiko.
	options, ok := obj["_kiko_runtime"].(map[string]any)
	if !ok {
		options, ok = obj["_sbcfg_runtime"].(map[string]any)
	}
	delete(obj, "_kiko_runtime")
	delete(obj, "_sbcfg_runtime")
	if ok {
		encoded, _ := json.Marshal(options)
		_ = json.Unmarshal(encoded, &runtimeOptions)
	} else {
		s.mu.Lock()
		runtimeOptions.DisableTailscaleUDPDirect = tailscaleUDPDirectDisabled(s.store.Doc)
		s.mu.Unlock()
	}
	cleanConfig, marshalErr := json.MarshalIndent(obj, "", "  ")
	if marshalErr != nil {
		http.Error(w, marshalErr.Error(), 500)
		return nil, false
	}
	runtimeConfig, err := s.injectRuntime(obj)
	if err != nil {
		s.logs.add("[kiko] 生成运行配置失败: " + err.Error())
		http.Error(w, err.Error(), 500)
		return nil, false
	}
	candidate := filepath.Join(s.dataDir, "runtime", "candidate.json")
	if e := atomicWrite(candidate, runtimeConfig, 0o600); e != nil {
		http.Error(w, e.Error(), 500)
		return nil, false
	}
	checkBegan := time.Now()
	checkCtx, cancelCheck := context.WithTimeout(r.Context(), 15*time.Second)
	out, e := s.coreCheckCommand(checkCtx, candidate).CombinedOutput()
	cancelCheck()
	if e != nil {
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			s.logs.add("[kiko] 配置校验超时（15秒）")
			writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "配置校验超时（15秒）"})
			return nil, false
		}
		s.logs.add("[kiko] 配置校验失败: " + strings.TrimSpace(string(out)))
		writeJSON(w, 422, map[string]any{"error": string(out)})
		return nil, false
	}
	if err := validateOutboundReferences(candidate); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return nil, false
	}
	s.logs.add(fmt.Sprintf("[kiko] 配置校验通过（耗时 %s）", time.Since(checkBegan).Round(time.Millisecond)))
	return &applyCandidate{clean: cleanConfig, runtime: runtimeConfig, disableUDP: runtimeOptions.DisableTailscaleUDPDirect, output: out,
		ruleMap: runtimeOptions.RuleMap, dnsRuleMap: runtimeOptions.DNSRuleMap}, true
}

func (s *server) apply(w http.ResponseWriter, r *http.Request) {
	// The write gate already queues concurrent applies; configMu still
	// excludes background starts and reload recovery.
	if !s.lockConfig(r.Context(), 30*time.Second) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "配置正在应用，请稍后重试"})
		return
	}
	defer s.configMu.Unlock()
	s.logs.add("[kiko] 开始应用配置")
	defer func() {
		if errors.Is(r.Context().Err(), context.Canceled) {
			s.logs.add("[kiko] 应用请求已取消")
		}
	}()
	c, ok := s.checkCandidate(w, r)
	if !ok {
		return
	}
	cleanConfig, runtimeConfig, out := c.clean, c.runtime, c.output
	reload, err := s.prepareReload(r.Context(), c.disableUDP)
	if err != nil {
		s.logs.add("[kiko] 配置重载准备失败: " + err.Error())
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if reload != nil {
		defer reload.finish()
		reload.expectedRevision = configRevision(runtimeConfig)
	}
	config := filepath.Join(s.dataDir, "config.json")
	runtimePath := filepath.Join(s.dataDir, "runtime", "config.json")
	files, err := backupConfigFiles(config, runtimePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err = r.Context().Err(); err != nil {
		writeJSON(w, http.StatusRequestTimeout, map[string]any{"error": err.Error()})
		return
	}
	if err = atomicWrite(config, append(cleanConfig, '\n'), 0o600); err == nil {
		err = atomicWrite(runtimePath, runtimeConfig, 0o600)
	}
	unchanged := reload != nil && bytes.Equal(bytes.TrimSpace(files[1].data), bytes.TrimSpace(runtimeConfig))
	if err == nil && unchanged {
		s.logs.add("[kiko] 配置内容相同，保留当前运行实例")
	}
	if err == nil && reload != nil && !unchanged {
		// Complete the transaction even if the browser disconnects after commit.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		err = reload.execute(ctx)
		cancel()
	}
	if err != nil {
		restoreErr := restoreConfigFiles(files)
		if restoreErr == nil && reload != nil && reload.signaled {
			restoreErr = reload.recover()
		}
		if restoreErr != nil && reload != nil {
			s.mu.Lock()
			if reload.recoveryAllowedLocked() {
				s.state.State = "failed"
				s.state.Error = restoreErr.Error()
			}
			s.mu.Unlock()
		}
		message := "配置应用失败，已恢复原配置: " + err.Error()
		if restoreErr != nil {
			message = "配置应用失败: " + err.Error() + "；恢复状态: " + restoreErr.Error()
		}
		s.logs.add("[kiko] " + message)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": message})
		return
	}
	s.storeRuleMap(c.ruleMap)
	s.storeDNSRuleMap(c.dnsRuleMap, unchanged)
	s.mu.Lock()
	s.disableTailscaleUDPDirect = c.disableUDP
	s.persisted.LastApplied = time.Now()
	_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	retryPending := s.autoStart && s.persisted.DesiredRunning && s.restartTimer != nil && s.cmd == nil && s.helperPID == 0
	s.mu.Unlock()
	if reload != nil && !sameSystemDNSConfig(files[1].data, runtimeConfig) {
		s.refreshSystemDNSConfig(reload, runtimePath)
	}
	if retryPending {
		go func() { _ = s.start() }()
	}
	s.mitmSyncAfterApply()
	s.syncSysProxy()
	writeJSON(w, 200, map[string]any{"applied": true, "output": string(out), "runtime": s.snapshot()})
	s.logs.add("[kiko] 配置应用完成")
}

// lockConfig waits for configMu until ctx ends or the timeout passes.
func (s *server) lockConfig(ctx context.Context, timeout time.Duration) bool {
	if s.configMu.TryLock() {
		return true
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
			if s.configMu.TryLock() {
				return true
			}
		}
	}
}

// coreCheckCommand is `kiko-core check` for a runtime configuration. It runs
// in the data directory, the working directory of the core, so relative
// paths of the configuration (script sources) resolve as they do in run.
func (s *server) coreCheckCommand(ctx context.Context, configPath string) *exec.Cmd {
	cmd := hiddenCommandContext(ctx, s.singBox, "check", "-c", configPath)
	cmd.Dir = s.dataDir
	return cmd
}

func (s *server) injectRuntime(config map[string]any) ([]byte, error) {
	address, secret, err := s.runtimeController()
	if err != nil {
		return nil, err
	}
	experimental, _ := config["experimental"].(map[string]any)
	if experimental == nil {
		experimental = map[string]any{}
		config["experimental"] = experimental
	}
	clash, _ := experimental["clash_api"].(map[string]any)
	if clash == nil {
		clash = map[string]any{}
		experimental["clash_api"] = clash
	}
	clash["external_controller"] = address
	clash["secret"] = secret
	if _, ok := config["mitm"]; ok && !s.mitmSupported() {
		// Older cores reject unknown top-level keys.
		delete(config, "mitm")
		if s.logs != nil {
			s.logs.add("[kiko] 核心不支持 HTTPS 解密，已忽略该配置")
		}
	} else if section, ok := config["mitm"].(map[string]any); ok && s.mitmPolicySupported() {
		if dropped := mitmDropUnknownBuiltin(section); len(dropped) > 0 && s.logs != nil {
			s.logs.add("[kiko] 内置排除表不含 " + strings.Join(dropped, "、") + "，已忽略")
		}
	} else if ok {
		// A core with the base mitm option but without the interception
		// policy options rejects those keys: drop only them.
		if mitmStripPolicy(section) && s.logs != nil {
			s.logs.add("[kiko] 核心不支持 HTTPS 解密的拦截策略选项，已忽略")
		}
	}
	if section, ok := config["mitm"].(map[string]any); ok && mitmUpstreamExtPresent(section) && !s.mitmUpstreamExtSupported() {
		if mitmUpstreamExtStrip(section) && s.logs != nil {
			s.logs.add("[kiko] 核心不支持上游 HTTP/2 指纹选项，已忽略")
		}
	}
	if section, ok := config["mitm"].(map[string]any); ok && !s.httpCaptureSupported() {
		if httpCaptureStrip(section) && s.logs != nil {
			s.logs.add("[kiko] 核心不支持 HTTP 抓取，已忽略该配置")
		}
	} else if ok && s.httpCapturePersistPrepare(section) && s.logs != nil {
		s.logs.add("[kiko] 核心不支持抓取存储，已忽略该配置")
	}
	if section, ok := config["mitm"].(map[string]any); ok && !s.httpRewriteSupported() {
		if httpRewriteStrip(section) && s.logs != nil {
			s.logs.add("[kiko] 核心不支持 HTTP 改写，已忽略该配置")
		}
	} else if ok && !s.httpRewriteBodySupported() {
		if removed := httpRewriteBodyStrip(section); len(removed) > 0 && s.logs != nil {
			s.logs.add("[kiko] 核心不支持" + strings.Join(removed, "与") + "，已忽略相应规则")
		}
	}
	if section, ok := config["mitm"].(map[string]any); ok {
		s.mitmProtectPorts(section)
	}
	if _, ok := config["scripts"]; ok {
		if !s.scriptsSupported() {
			scriptsStrip(config)
			scriptsRuleStrip(config)
			if s.logs != nil {
				s.logs.add("[kiko] 核心不支持脚本，已忽略该配置")
			}
		} else {
			if !s.scriptsCronSupported() {
				if n := scriptsCronStrip(config); n > 0 && s.logs != nil {
					s.logs.add("[kiko] 核心不支持定时脚本，已忽略 " + strconv.Itoa(n) + " 个定时脚本")
				}
			}
			if !s.scriptsRuleSupported() {
				if n, r := scriptsRuleStrip(config); (n > 0 || r > 0) && s.logs != nil {
					s.logs.add("[kiko] 核心不支持规则脚本与 DNS 脚本，已忽略 " + strconv.Itoa(n) + " 个脚本和 " + strconv.Itoa(r) + " 条相关规则")
				}
			}
			if err = s.scriptsMaterialize(config); err != nil {
				return nil, err
			}
		}
	}
	if s.apiSupported() {
		s.injectDNSMonitor(experimental)
		if err = s.injectAPIService(config, address); err != nil {
			return nil, err
		}
	}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (s *server) runtimeController() (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validControllerAddress(s.clashAddr) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", "", err
		}
		s.clashAddr = listener.Addr().String()
		_ = listener.Close()
	}
	if s.clashSecret == "" {
		s.clashSecret = token()
	}
	return s.clashAddr, s.clashSecret, nil
}

func validControllerAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) start() error {
	return s.startForGeneration(0)
}

// scheduleRetryLocked keeps the requested running state until an explicit stop.
// The retry count saturates so unattended processes can retry indefinitely.
func (s *server) scheduleRetryLocked(message string) {
	s.restartGeneration++
	if s.restartTimer != nil {
		s.restartTimer.Stop()
	}
	s.state.State = "degraded"
	s.state.Error = message
	s.state.DesiredRunning = true
	s.persisted.DesiredRunning = true
	_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	if s.retry < 6 {
		s.retry++
	}
	delay := time.Second * time.Duration(1<<min(s.retry-1, 5))
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	s.state.RestartIn = int(delay.Seconds())
	s.restartAt = time.Now().Add(delay)
	generation := s.restartGeneration
	s.restartTimer = time.AfterFunc(delay, func() { s.restartAfter(generation) })
}

func (s *server) startFailureLocked(message, state string, clearRequested bool) {
	if s.activeReload != nil {
		s.state.State = "reloading"
		s.state.Error = message
		return
	}
	if s.autoStart {
		s.scheduleRetryLocked(message)
		return
	}
	s.state.State = state
	s.state.Error = message
	s.state.RestartIn = 0
	if clearRequested {
		s.state.DesiredRunning = false
		s.persisted.DesiredRunning = false
		_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	}
}

func (s *server) startForGeneration(generation uint64) error {
	s.mu.Lock()
	attempt := s.restartGeneration
	s.mu.Unlock()
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.startForGenerationLocked(generation, attempt)
}

// Caller holds configMu, including the reload recovery path.
func (s *server) startForGenerationLocked(generation, attempt uint64) error {
	s.mu.Lock()
	if s.cmd == nil && s.helperPID == 0 && s.coreCleanupPendingLocked() {
		done := s.coreDone
		s.mu.Unlock()
		<-done
		s.mu.Lock()
	}
	if generation == 0 && attempt != s.restartGeneration {
		s.mu.Unlock()
		return errors.New("启动已取消")
	}
	if s.shuttingDown {
		s.mu.Unlock()
		return errors.New("服务正在关闭")
	}
	if generation == 0 && s.stopping {
		s.mu.Unlock()
		return errors.New("核心正在停止，请稍后再启动")
	}
	if generation != 0 && (generation != s.restartGeneration || !s.persisted.DesiredRunning || s.stopping) {
		s.mu.Unlock()
		return nil
	}
	if generation == 0 && s.autoStart {
		s.stopping = false
		s.persisted.DesiredRunning = true
		s.state.DesiredRunning = true
	}
	if s.cmd != nil || s.helperPID > 0 {
		if s.state.State == "failed" {
			s.mu.Unlock()
			return errors.New("核心进程仍在运行，请先停止后再启动")
		}
		s.mu.Unlock()
		return nil
	}
	configPath := filepath.Join(s.dataDir, "runtime", "config.json")
	if info, err := os.Stat(configPath); err != nil || info.IsDir() {
		message := "请先应用配置，再启动核心"
		s.startFailureLocked(message, "stopped", true)
		s.mu.Unlock()
		return errors.New(message)
	}
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 15*time.Second)
	s.startCancel = cancelCheck
	attempt = s.restartGeneration
	s.state.State = "starting"
	s.mu.Unlock()
	checkCommand := s.coreCheckCommand(checkCtx, configPath)
	prepareProcess(checkCommand)
	checkCommand.Cancel = func() error { return forceProcess(checkCommand) }
	checkCommand.WaitDelay = 200 * time.Millisecond
	output, checkErr := checkCommand.CombinedOutput()
	checkTimedOut := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
	cancelCheck()
	s.mu.Lock()
	s.startCancel = nil
	if attempt != s.restartGeneration || s.stopping || s.shuttingDown {
		s.mu.Unlock()
		return errors.New("启动已取消")
	}
	if checkErr != nil {
		message := strings.TrimSpace(string(output))
		if checkTimedOut {
			message = "配置校验超时（15秒）"
		} else if message == "" {
			message = checkErr.Error()
		}
		s.startFailureLocked(message, "failed", true)
		s.mu.Unlock()
		s.logs.add("[kiko] 配置检查失败: " + message)
		return fmt.Errorf("配置检查失败: %s", message)
	}
	if err := validateOutboundReferences(configPath); err != nil {
		s.startFailureLocked(err.Error(), "failed", true)
		s.mu.Unlock()
		s.logs.add("[kiko] 配置引用检查失败: " + err.Error())
		return err
	}
	if runtime.GOOS == "windows" && !isElevated() {
		_, _, tun := s.proxyTarget()
		if tun {
			message := "Windows TUN 需要管理员权限，请以管理员身份重新启动 Kiko 或 kiko-server"
			s.startFailureLocked(message, "failed", true)
			s.mu.Unlock()
			return errors.New(message)
		}
	}
	if runtime.GOOS == "linux" {
		if _, _, tun := s.proxyTarget(); tun {
			if message := tunPrivilegeError(s.singBox); message != "" {
				s.startFailureLocked(message, "failed", true)
				s.mu.Unlock()
				s.logs.add("[kiko] " + message)
				return errors.New(message)
			}
		}
	}
	s.logs.add("[kiko] 正在启动核心")
	s.restartGeneration++
	if s.activeReload != nil {
		s.activeReload.epoch = s.restartGeneration
	}
	if s.restartTimer != nil {
		s.restartTimer.Stop()
		s.restartTimer = nil
	}
	s.stopping = false
	s.persisted.DesiredRunning = true
	s.state.DesiredRunning = true
	s.state.State = "starting"
	_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	s.state.RestartIn = 0
	s.restartAt = time.Time{}
	// macOS utun requires a privileged process. The installed launchd helper
	// starts kiko-core as root while this server keeps user-owned state.
	if runtime.GOOS == "darwin" && s.usePrivilegedHelper {
		if !helperInstalled() {
			e := errors.New("macOS TUN 需要 Kiko 特权助手，请从应用菜单安装")
			s.startFailureLocked(e.Error(), "failed", true)
			s.mu.Unlock()
			s.logs.add("[kiko] " + e.Error())
			return e
		}
		var logOffset int64
		if info, err := os.Stat(helperLogPath(s.dataDir)); err == nil {
			logOffset = info.Size()
		}
		pid, e := privilegedStart(s.singBox, configPath, s.dataDir, helperLogPath(s.dataDir), s.disableTailscaleUDPDirect)
		if e != nil {
			s.startFailureLocked(e.Error(), "failed", false)
			s.mu.Unlock()
			s.logs.add("[kiko] 特权助手启动失败: " + e.Error())
			return e
		}
		s.helperPID = pid
		exited := make(chan struct{})
		s.coreDone = exited
		s.coreStopped = make(chan struct{})
		s.state.PID = pid
		s.state.StartedAt = time.Now()
		s.state.State = "running"
		s.state.Error = ""
		s.mu.Unlock()
		s.logs.add(fmt.Sprintf("[kiko] 核心已由特权助手启动，PID %d", pid))
		go func() { defer close(exited); s.waitPrivileged(pid, logOffset) }()
		s.applySystemDNS(configPath)
		s.emit("runtime", s.snapshot())
		return nil
	}
	cmd := exec.Command(s.singBox, "run", "-c", configPath)
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Env = tailscaleUDPEnvironment(os.Environ(), s.disableTailscaleUDPDirect)
	// kiko-core resolves relative paths (for example cache.db) against its
	// working directory. Keep that directory writable inside the user data
	// directory when running from a read-only macOS .app bundle.
	cmd.Dir = s.dataDir
	prepareProcess(cmd)
	w := s.logs.writer()
	cmd.Stdout = w
	cmd.Stderr = w
	if e := cmd.Start(); e != nil {
		s.startFailureLocked(e.Error(), "failed", false)
		s.mu.Unlock()
		s.logs.add("[kiko] 核心启动失败: " + e.Error())
		return e
	}
	s.cmd = cmd
	exited := make(chan struct{})
	s.coreDone = exited
	s.state.PID = cmd.Process.Pid
	s.state.StartedAt = time.Now()
	s.state.State = "running"
	s.state.Error = ""
	s.mu.Unlock()
	s.logs.add(fmt.Sprintf("[kiko] 核心已启动，PID %d", cmd.Process.Pid))
	go func() { defer close(exited); s.wait(cmd) }()
	s.applySystemDNS(configPath)
	s.emit("runtime", s.snapshot())
	go func(current *exec.Cmd) {
		time.Sleep(60 * time.Second)
		s.mu.Lock()
		if s.cmd == current && s.state.State == "running" {
			s.retry = 0
		}
		s.mu.Unlock()
	}(cmd)
	return nil
}

func tailscaleUDPDirectDisabled(raw json.RawMessage) bool {
	var doc struct {
		Endpoints []struct {
			Type    string `json:"type"`
			Enabled bool   `json:"enabled"`
			Fields  struct {
				DisableUDPDirect bool `json:"disable_udp_direct"`
			} `json:"fields"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for _, endpoint := range doc.Endpoints {
		if endpoint.Type == "tailscale" && endpoint.Enabled && endpoint.Fields.DisableUDPDirect {
			return true
		}
	}
	return false
}

func tailscaleUDPEnvironment(current []string, disabled bool) []string {
	result := make([]string, 0, len(current)+1)
	for _, entry := range current {
		if strings.HasPrefix(entry, "TS_DEBUG_NEVER_DIRECT_UDP=") || strings.HasPrefix(entry, "TS_DEBUG_ALWAYS_USE_DERP=") {
			continue
		}
		result = append(result, entry)
	}
	if disabled {
		result = append(result, "TS_DEBUG_ALWAYS_USE_DERP=true")
	}
	return result
}
func (s *server) wait(cmd *exec.Cmd) {
	e := cmd.Wait()
	s.mu.Lock()
	if s.cmd != cmd {
		s.mu.Unlock()
		return
	}
	s.cmd = nil
	s.state.PID = 0
	s.state.LastExit = fmt.Sprint(e)
	if s.activeReload != nil && !s.stopping && !s.shuttingDown {
		s.state.State = "reloading"
		s.mu.Unlock()
		s.restoreSystemDNSConfig()
		s.logs.add("[kiko] 重载事务中的核心已退出: " + fmt.Sprint(e))
		s.emit("runtime", s.snapshot())
		return
	}
	wanted := s.persisted.DesiredRunning && !s.stopping
	fatalConfig := false
	lines := s.logs.snapshot()
	for _, line := range lines[max(0, len(lines)-8):] {
		if strings.Contains(line, "default outbound not found") || strings.Contains(line, "FATAL[") {
			fatalConfig = true
			break
		}
	}
	if wanted && fatalConfig && !s.autoStart {
		s.state.State = "failed"
		s.state.Error = "核心启动失败，请检查日志中的致命错误"
		s.state.RestartIn = 0
		s.state.DesiredRunning = false
		s.persisted.DesiredRunning = false
		_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
		s.mu.Unlock()
		s.restoreSystemDNSConfig()
		s.logs.add("[kiko] 检测到核心致命启动错误，已停止自动重启")
		return
	}
	if wanted {
		message := ""
		if fatalConfig {
			message = "核心启动失败，请检查日志中的致命错误"
		}
		s.scheduleRetryLocked(message)
		s.mu.Unlock()
		s.restoreSystemDNSConfig()
		s.emit("runtime", s.snapshot())
		return
	}
	s.state.State = "stopped"
	s.mu.Unlock()
	s.restoreSystemDNSConfig()
	s.emit("runtime", s.snapshot())
}
func (s *server) waitPrivileged(pid int, offset int64) {
	s.mu.Lock()
	confirmed := s.coreStopped
	s.mu.Unlock()
	logPath := helperLogPath(s.dataDir)
	writer := s.logs.writer()
	exitDetail := "已确认停止"
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
watch:
	for {
		select {
		case <-confirmed:
			break watch
		default:
		}
		select {
		case <-confirmed:
			break watch
		case <-ticker.C:
		}
		if file, err := os.Open(logPath); err == nil {
			if info, err := file.Stat(); err == nil && info.Size() < offset {
				offset = 0
				writer = s.logs.writer()
			}
			section := io.NewSectionReader(file, offset, 256<<10)
			read, _ := io.Copy(writer, section)
			offset += read
			_ = file.Close()
		}
		alive, detail, aliveErr := privilegedProcessStatus(pid)
		// Keep ownership across transient socket failures; only a successful status
		// response can confirm that the managed process has exited.
		if aliveErr != nil || alive {
			continue
		}
		exitDetail = detail
		break
	}
	// Drain final fatal/exit lines before recording the termination details.
	if file, err := os.Open(logPath); err == nil {
		_, _ = io.Copy(writer, io.NewSectionReader(file, offset, 256<<10))
		_ = file.Close()
	}

	s.mu.Lock()
	if s.helperPID != pid {
		s.mu.Unlock()
		return
	}
	s.helperPID = 0
	s.state.PID = 0
	s.state.LastExit = exitDetail
	if s.activeReload != nil && !s.stopping && !s.shuttingDown {
		s.state.State = "reloading"
		s.mu.Unlock()
		s.restoreSystemDNSConfig()
		s.logs.add("[kiko] 重载事务中的特权核心已退出: " + exitDetail)
		s.emit("runtime", s.snapshot())
		return
	}
	wanted := s.persisted.DesiredRunning && !s.stopping
	if wanted {
		message := "核心特权进程已退出，请检查日志"
		if s.autoStart {
			s.scheduleRetryLocked(message)
		} else {
			s.state.State = "failed"
			s.state.Error = message
			s.persisted.DesiredRunning = false
			s.state.DesiredRunning = false
			_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
		}
	} else {
		s.state.State = "stopped"
	}
	s.mu.Unlock()
	s.restoreSystemDNSConfig()
	s.emit("runtime", s.snapshot())
}
func (s *server) restartAfter(generation uint64) {
	_ = s.startForGeneration(generation)
}
func (s *server) beginStop(persist bool) (stopTarget, <-chan struct{}, bool) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.beginStopOperation(persist)
}

func (s *server) beginStopOperation(persist bool) (stopTarget, <-chan struct{}, bool) {
	s.mu.Lock()
	if s.activeReload != nil && s.activeReload.cancel != nil {
		s.activeReload.cancel()
	}
	if s.stopping {
		if persist {
			s.restartGeneration++
			s.persisted.DesiredRunning = false
			s.state.DesiredRunning = false
			_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
		}
		done := s.stopDone
		s.mu.Unlock()
		return stopTarget{}, done, false
	}
	s.restartGeneration++
	if s.restartTimer != nil {
		s.restartTimer.Stop()
		s.restartTimer = nil
	}
	if s.startCancel != nil {
		s.startCancel()
		s.startCancel = nil
	}
	cmd := s.cmd
	helperPID := s.helperPID
	exited := s.coreDone
	stopped := s.coreStopped
	s.stopping = true
	done := make(chan struct{})
	s.stopDone = done
	s.state.RestartIn = 0
	s.restartAt = time.Time{}
	s.state.Error = ""
	if persist {
		s.retry = 0
		s.persisted.DesiredRunning = false
		s.state.DesiredRunning = false
		_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	}
	if cmd == nil && helperPID == 0 && !s.coreCleanupPendingLocked() && s.restoreSystemDNS == nil {
		s.state.State = "stopped"
		s.stopping = false
		s.stopDone = nil
		close(done)
		s.mu.Unlock()
		s.restoreSystemDNSConfig()
		s.emit("runtime", s.snapshot())
		return stopTarget{}, done, false
	}
	s.state.State = "stopping"
	s.mu.Unlock()
	s.emit("runtime", s.snapshot())
	return stopTarget{cmd: cmd, helperPID: helperPID, exited: exited, stopped: stopped}, done, true
}

func (s *server) completeStop(done <-chan struct{}) {
	s.mu.Lock()
	if s.stopDone == done {
		active := s.stopDone
		s.stopDone = nil
		s.stopping = false
		close(active)
	}
	s.mu.Unlock()
}

func (s *server) finishStop(target stopTarget, done <-chan struct{}) {
	defer s.completeStop(done)
	s.finishCoreStop(target)
}

func (s *server) stop(persist bool) {
	target, done, owner := s.beginStop(persist)
	if !owner {
		if done != nil {
			<-done
		}
		return
	}
	s.finishStop(target, done)
}

// stopAsync stops the core in the background. finished, when given, runs
// once the stop completes; the write gate stays held until then.
func (s *server) stopAsync(persist bool, finished ...func()) {
	target, done, owner := s.beginStop(persist)
	if !owner {
		if done == nil {
			runAll(finished)
			return
		}
		go func() { <-done; runAll(finished) }()
		return
	}
	go func() {
		defer runAll(finished)
		s.finishStop(target, done)
		if s.snapshot().State == "stopped" {
			s.logs.add("[kiko] 核心已停止")
		}
	}()
}

func (s *server) applySystemDNS(configPath string) {
	s.systemDNSMu.Lock()
	defer s.systemDNSMu.Unlock()
	s.applySystemDNSLocked(configPath)
}

func (s *server) applySystemDNSLocked(configPath string) {
	s.mu.Lock()
	running := (s.cmd != nil || s.helperPID > 0) && !s.stopping && !s.shuttingDown
	s.mu.Unlock()
	if !running {
		return
	}
	// Preserve the original DNS snapshot if an earlier restoration needs retry.
	if !s.restoreSystemDNSLocked() {
		return
	}
	restore, err := applySystemDNS(configPath)
	if restore != nil {
		s.mu.Lock()
		s.restoreSystemDNS = restore
		s.mu.Unlock()
	}
	if err != nil {
		s.logs.add("[kiko] 应用 macOS 系统 DNS 失败: " + err.Error())
		return
	}
	if restore == nil {
		return
	}
	s.logs.add("[kiko] 已应用 TUN DNS 到 macOS 网络服务")
}

func (s *server) restoreSystemDNSConfig() {
	s.systemDNSMu.Lock()
	defer s.systemDNSMu.Unlock()
	s.restoreSystemDNSLocked()
}

func (s *server) refreshSystemDNSConfig(reload *coreReload, configPath string) {
	s.systemDNSMu.Lock()
	defer s.systemDNSMu.Unlock()
	s.mu.Lock()
	current := reload.currentLocked()
	s.mu.Unlock()
	if current {
		s.applySystemDNSLocked(configPath)
	}
}

// Caller holds systemDNSMu. Keep failed restorations available for retry.
func (s *server) restoreSystemDNSLocked() bool {
	s.mu.Lock()
	restore := s.restoreSystemDNS
	s.mu.Unlock()
	if restore == nil {
		return true
	}
	began := time.Now()
	if err := restore(); err != nil {
		s.logs.add("[kiko] 恢复 macOS 系统 DNS 失败: " + err.Error())
		return false
	}
	s.mu.Lock()
	s.restoreSystemDNS = nil
	s.mu.Unlock()
	s.logs.add(fmt.Sprintf("[kiko] 已恢复 macOS 系统 DNS（耗时 %s）", time.Since(began).Round(time.Millisecond)))
	return true
}
func (s *server) restart() { s.restartAsync() }

func runAll(fns []func()) {
	for _, fn := range fns {
		if fn != nil {
			fn()
		}
	}
}

// restartAsync stops and starts the core in the background. finished, when
// given, runs after the new start attempt completes.
func (s *server) restartAsync(finished ...func()) {
	s.controlMu.Lock()
	s.mu.Lock()
	if s.stopping || s.shuttingDown {
		s.mu.Unlock()
		s.controlMu.Unlock()
		runAll(finished)
		return
	}
	s.mu.Unlock()
	target, done, owner := s.beginStopOperation(false)
	s.mu.Lock()
	generation := s.restartGeneration
	s.persisted.DesiredRunning = true
	s.state.DesiredRunning = true
	if owner {
		s.state.State = "restarting"
	}
	_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	s.mu.Unlock()
	s.controlMu.Unlock()
	s.emit("runtime", s.snapshot())
	go func() {
		defer runAll(finished)
		if owner {
			s.finishStop(target, done)
		} else if done != nil {
			<-done
		}
		s.mu.Lock()
		current := generation == s.restartGeneration && s.persisted.DesiredRunning && !s.shuttingDown && s.state.State == "stopped"
		s.mu.Unlock()
		if current {
			_ = s.startForGeneration(generation)
		}
	}()
}

func (s *server) shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.shuttingDown = true
		s.mu.Unlock()
		s.mitmShareStop()
		s.closeBrowsers()
		s.faviconService().close()
		if s.shutdownCh != nil {
			close(s.shutdownCh)
		}
		s.td.mu.Lock()
		s.td.closing = true
		for _, task := range s.td.tasks {
			if task.cancel != nil {
				task.cancel()
			}
		}
		s.td.mu.Unlock()
		s.stopGRPC()
		s.rpc.mu.Lock()
		workerDone := s.rpc.workerDone
		s.rpc.mu.Unlock()
		if workerDone != nil {
			select {
			case <-workerDone:
			case <-time.After(3 * time.Second):
			}
		}
		if s.usage != nil {
			if err := s.usage.flush(); err != nil {
				s.logs.add("[kiko] 保存用量失败：" + err.Error())
			}
		}
		deadline := time.Now().Add(6 * time.Second)
		for s.usbipShareState().Running && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		s.stop(false)
		s.releaseSysProxy()
	})
}
func (s *server) snapshot() runtimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	if v.Reload != nil {
		progress := *v.Reload
		progress.ElapsedMS = max(0, time.Since(progress.StartedAt).Milliseconds())
		v.Reload = &progress
	}
	if !v.StartedAt.IsZero() && v.PID > 0 {
		v.UptimeSeconds = int64(time.Since(v.StartedAt).Seconds())
	}
	if !s.restartAt.IsZero() && v.State == "degraded" {
		remaining := time.Until(s.restartAt)
		if remaining > 0 {
			v.RestartIn = int((remaining + time.Second - 1) / time.Second)
		} else {
			v.RestartIn = 0
		}
	}
	return v
}
func (s *server) monitor(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.grpcTick()
			s.collectSingBoxMetrics()
			snap := s.snapshot()
			s.recordMetrics(snap, time.Now())
			if snap.State != "failed" {
				s.emit("runtime", snap)
			}
		}
	}
}
func (s *server) collectSingBoxMetrics() {
	s.mu.Lock()
	address, secret := s.clashAddr, s.clashSecret
	running := (s.cmd != nil || s.helperPID > 0) && (s.state.State == "running" || s.state.State == "reloading")
	s.mu.Unlock()
	if !running || address == "" {
		s.mu.Lock()
		s.state.MetricsAvailable = false
		s.state.MemoryBytes = 0
		s.state.ActiveConnections = 0
		s.state.UploadRate = 0
		s.state.DownloadRate = 0
		s.state.UploadTotal = 0
		s.state.DownloadTotal = 0
		s.metricsSampleAt = time.Time{}
		s.metricsUploadTotal = 0
		s.metricsDownloadTotal = 0
		s.mu.Unlock()
		return
	}
	if s.grpcActive() {
		return
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+address+"/connections", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (&http.Client{Timeout: 800 * time.Millisecond}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var value struct {
		Memory        uint64            `json:"memory"`
		UploadTotal   uint64            `json:"uploadTotal"`
		DownloadTotal uint64            `json:"downloadTotal"`
		Connections   []json.RawMessage `json:"connections"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&value) != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	var uploadRate, downloadRate uint64
	if !s.metricsSampleAt.IsZero() {
		seconds := now.Sub(s.metricsSampleAt).Seconds()
		if seconds > 0 {
			if value.UploadTotal >= s.metricsUploadTotal {
				uploadRate = uint64(float64(value.UploadTotal-s.metricsUploadTotal) / seconds)
			}
			if value.DownloadTotal >= s.metricsDownloadTotal {
				downloadRate = uint64(float64(value.DownloadTotal-s.metricsDownloadTotal) / seconds)
			}
		}
	}
	s.metricsSampleAt = now
	s.metricsUploadTotal = value.UploadTotal
	s.metricsDownloadTotal = value.DownloadTotal
	s.state.MemoryBytes = value.Memory
	s.state.ActiveConnections = len(value.Connections)
	s.state.UploadRate = uploadRate
	s.state.DownloadRate = downloadRate
	s.state.UploadTotal = value.UploadTotal
	s.state.DownloadTotal = value.DownloadTotal
	s.state.MetricsAvailable = true
	s.mu.Unlock()
}
func (s *server) runtime(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.snapshot())
}

// runningController is the Clash API of the running core.
func (s *server) runningController() (address, secret string, ok bool) {
	s.mu.Lock()
	address, secret = s.clashAddr, s.clashSecret
	running := (s.cmd != nil || s.helperPID > 0) && s.state.State == "running"
	s.mu.Unlock()
	return address, secret, running && validControllerAddress(address)
}

func (s *server) ruleSetStatus(w http.ResponseWriter, r *http.Request) {
	address, secret, ok := s.runningController()
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "core runtime unavailable"})
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+address+"/rulesets", nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "core rule-set status unavailable"})
		return
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "core rule-set status unavailable"})
		return
	}
	defer response.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (s *server) setMode(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &value) {
		return
	}
	if value.Mode == "" {
		http.Error(w, "mode required", 400)
		return
	}
	s.mu.Lock()
	address, secret := s.clashAddr, s.clashSecret
	s.mu.Unlock()
	if address == "" {
		http.Error(w, "Clash API unavailable", 503)
		return
	}
	req, _ := http.NewRequest(http.MethodPatch, "http://"+address+"/configs", strings.NewReader(`{"mode":`+fmt.Sprintf("%q", value.Mode)+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		http.Error(w, resp.Status, 502)
		return
	}
	w.WriteHeader(204)
}
func (s *server) clashAccess() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clashAddr, s.clashSecret
}
func (s *server) getSelectors(w http.ResponseWriter, r *http.Request) {
	if selectors, ok := s.grpcSelectors(); ok {
		writeJSON(w, http.StatusOK, map[string]any{"selectors": selectors})
		return
	}
	address, secret := s.clashAccess()
	if address == "" {
		http.Error(w, "Clash API unavailable", http.StatusServiceUnavailable)
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+address+"/proxies", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		http.Error(w, strings.TrimSpace(string(body)), http.StatusBadGateway)
		return
	}
	var value struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			Name string   `json:"name"`
			Now  string   `json:"now"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&value); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	selectors := make([]clashSelector, 0)
	for key, proxy := range value.Proxies {
		if groupKind(proxy.Type) == "" {
			continue
		}
		name := proxy.Name
		if name == "" {
			name = key
		}
		selectors = append(selectors, clashSelector{Name: name, Type: groupKind(proxy.Type), Now: proxy.Now, All: proxy.All})
	}
	sortSelectors(selectors)
	writeJSON(w, http.StatusOK, map[string]any{"selectors": selectors})
}
func (s *server) setSelector(w http.ResponseWriter, r *http.Request) {
	var value struct {
		Selector string `json:"selector"`
		Outbound string `json:"outbound"`
	}
	if !decode(w, r, &value) {
		return
	}
	if value.Selector == "" || value.Outbound == "" {
		http.Error(w, "selector and outbound required", http.StatusBadRequest)
		return
	}
	if s.grpcSelect(r.Context(), value.Selector, value.Outbound) == nil {
		s.logs.add(fmt.Sprintf("[kiko] Selector「%s」已切换至「%s」", value.Selector, value.Outbound))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	address, secret := s.clashAccess()
	if address == "" {
		http.Error(w, "Clash API unavailable", http.StatusServiceUnavailable)
		return
	}
	body, _ := json.Marshal(map[string]string{"name": value.Outbound})
	endpoint := "http://" + address + "/proxies/" + url.PathEscape(value.Selector)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPut, endpoint, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		http.Error(w, strings.TrimSpace(string(responseBody)), http.StatusBadGateway)
		return
	}
	s.logs.add(fmt.Sprintf("[kiko] Selector「%s」已切换至「%s」", value.Selector, value.Outbound))
	w.WriteHeader(http.StatusNoContent)
}
func (s *server) emit(kind string, value any) {
	b, _ := json.Marshal(map[string]any{"type": kind, "data": value})
	s.mu.Lock()
	for ch := range s.events {
		select {
		case ch <- string(b):
		default:
		}
	}
	s.mu.Unlock()
}

// sseHeartbeat is the heartbeat interval of an idle event stream; clients use it
// to detect a stalled connection.
const sseHeartbeat = 10 * time.Second

func (s *server) sse(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan string, 128)
	s.mu.Lock()
	s.events[ch] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.events, ch); s.mu.Unlock() }()
	b, _ := json.Marshal(map[string]any{"type": "runtime", "data": s.snapshot()})
	fmt.Fprintf(w, "data: %s\n\n", b)
	if state := s.writeGate().watcherState(); state.Held {
		b, _ = json.Marshal(map[string]any{"type": "lock", "data": state})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if selectors, ok := s.grpcSelectors(); ok {
		b, _ = json.Marshal(map[string]any{"type": "selectors", "data": map[string]any{"selectors": selectors}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	for _, message := range s.endpointSnapshotMessages() {
		fmt.Fprintf(w, "data: %s\n\n", message)
	}
	for _, line := range s.logs.snapshot() {
		b, _ = json.Marshal(map[string]any{"type": "log", "data": line})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	f.Flush()
	lc, unsub := s.logs.subscribe()
	defer unsub()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.shutdownCh:
			return
		case <-heartbeat.C:
			// An SSE comment line fires no event; it only tells the client the
			// connection is still alive.
			fmt.Fprint(w, ": ping\n\n")
			f.Flush()
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			f.Flush()
		case line := <-lc:
			b, _ := json.Marshal(map[string]any{"type": "log", "data": line})
			fmt.Fprintf(w, "data: %s\n\n", b)
			f.Flush()
		}
	}
}

// fetchUserAgent is the User-Agent of downloads made for the client.
const fetchUserAgent = "clash-verge/v2.5.4"

// newFetchClient is the client of downloads made for the client: at most
// five redirects, dropping the request headers when one changes host.
func newFetchClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		previous := via[len(via)-1].URL
		if req.URL.Scheme != previous.Scheme || !strings.EqualFold(req.URL.Host, previous.Host) {
			req.Header = make(http.Header)
			req.Header.Set("User-Agent", fetchUserAgent)
		}
		return nil
	}}
}

func (s *server) fetch(w http.ResponseWriter, r *http.Request) {
	var q struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		// PublicOnly refuses connections to loopback, private, link-local,
		// unspecified and multicast addresses, redirects included.
		PublicOnly bool `json:"publicOnly"`
		// PublicRedirects leaves the first address free (the caller typed
		// it) and refuses redirects to non-public addresses.
		PublicRedirects bool `json:"publicRedirects"`
	}
	if r.Method == http.MethodGet {
		q.URL = r.URL.Query().Get("url")
	} else if !decode(w, r, &q) {
		return
	}
	u := q.URL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		http.Error(w, "http/https URL required", 400)
		return
	}
	client := newFetchClient()
	if q.PublicOnly {
		client.Transport = publicOnlyTransport()
	} else if q.PublicRedirects {
		client.Transport = &redirectTransport{first: http.DefaultTransport, rest: publicOnlyTransport()}
	}
	upstream, e := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	for name, value := range q.Headers {
		if strings.TrimSpace(name) != "" {
			upstream.Header.Set(name, value)
		}
	}
	if upstream.Header.Get("User-Agent") == "" {
		upstream.Header.Set("User-Agent", fetchUserAgent)
	}
	resp, e := client.Do(upstream)
	if e != nil {
		if errors.Is(e, errNonPublicAddress) {
			http.Error(w, errNonPublicAddress.Error(), 403)
			return
		}
		http.Error(w, e.Error(), 502)
		return
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if e != nil || len(body) > 16<<20 {
		http.Error(w, "response exceeds 16 MiB", 502)
		return
	}
	if r.Method == http.MethodGet {
		// The body is foreign content: never serve it as a document under
		// the API origin.
		header := w.Header()
		header.Set("Content-Type", "application/octet-stream")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Content-Security-Policy", "sandbox")
		header.Set("Content-Disposition", "attachment")
		if userinfo := subscriptionUserinfo(resp.Header); userinfo != "" {
			w.Header().Set("Subscription-Userinfo", userinfo)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	writeJSON(w, 200, map[string]any{
		"status":               resp.StatusCode,
		"body":                 string(body),
		"subscriptionUserinfo": subscriptionUserinfo(resp.Header),
	})
}

func subscriptionUserinfo(header http.Header) string {
	for name, values := range header {
		lower := strings.ToLower(name)
		prefix, found := strings.CutSuffix(lower, "subscription-userinfo")
		if !found || (prefix != "" && !strings.HasSuffix(prefix, "-")) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}

type latencyTarget struct {
	ID, Host, Tag, URL string
	Port               int
	// Unified asks the core for a unified delay (the second request of
	// one connection is timed).
	Unified bool
}
type latencyResult struct {
	ID       string    `json:"id"`
	MedianMS float64   `json:"medianConnectMs,omitempty"`
	OneWayMS float64   `json:"estimatedOneWayMs,omitempty"`
	Success  int       `json:"successSamples"`
	TestedAt time.Time `json:"testedAt"`
	Error    string    `json:"error,omitempty"`
	// Mode is "direct" for a TCP handshake to the server (the core is
	// stopped), "unified" for a unified delay through the outbound, empty
	// for a plain request through the outbound.
	Mode string `json:"mode,omitempty"`
}

func (s *server) latency(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Targets []latencyTarget `json:"targets"`
	}
	if !decode(w, r, &q) {
		return
	}
	results := make([]latencyResult, len(q.Targets))
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i, t := range q.Targets {
		wg.Add(1)
		go func(i int, t latencyTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(r.Context(), latencyCtxTimeout(t.Unified))
			defer cancel()
			results[i] = s.testProxyTarget(ctx, t)
		}(i, t)
	}
	wg.Wait()
	writeJSON(w, 200, map[string]any{"results": results})
}
func testTarget(t latencyTarget) latencyResult {
	v := latencyResult{ID: t.ID, TestedAt: time.Now(), Mode: "direct"}
	if t.Port < 1 || t.Port > 65535 || t.Host == "" {
		v.Error = "invalid target"
		return v
	}
	var samples []float64
	for i := 0; i < 3; i++ {
		start := time.Now()
		c, e := net.DialTimeout("tcp", net.JoinHostPort(t.Host, fmt.Sprint(t.Port)), 3*time.Second)
		if e == nil {
			_ = c.Close()
			samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		}
	}
	v.Success = len(samples)
	if len(samples) == 0 {
		v.Error = "all samples failed"
		return v
	}
	sort.Float64s(samples)
	v.MedianMS = samples[len(samples)/2]
	v.OneWayMS = v.MedianMS / 2
	return v
}

// latencyCoreTimeout is the time the core gives one delay test. A unified
// delay makes two requests over one connection that share it.
func latencyCoreTimeout(unified bool) time.Duration {
	if unified {
		return 8 * time.Second
	}
	return 5 * time.Second
}

// latencyClientTimeout bounds the HTTP call to the core's delay route: a
// second beyond the core's own timeout.
func latencyClientTimeout(unified bool) time.Duration {
	return latencyCoreTimeout(unified) + time.Second
}

// latencyCtxTimeout bounds one whole target test, two seconds beyond the
// core's timeout.
func latencyCtxTimeout(unified bool) time.Duration {
	return latencyCoreTimeout(unified) + 2*time.Second
}

// latencyQuery is the query of the core's delay route for testURL, with
// unified=true when a unified delay is wanted.
func latencyQuery(testURL string, unified bool) url.Values {
	query := url.Values{"url": {testURL}, "timeout": {strconv.Itoa(int(latencyCoreTimeout(unified) / time.Millisecond))}, "expected": {"200-299"}}
	if unified {
		query.Set("unified", "true")
	}
	return query
}

func (s *server) testProxyTarget(ctx context.Context, target latencyTarget) latencyResult {
	result := latencyResult{ID: target.ID, TestedAt: time.Now()}
	// The managed kiko-core process may be stopped while the configuration editor
	// remains available. Keep reachability measurement useful in that state.
	s.mu.Lock()
	running := s.state.State == "running" || s.state.State == "starting"
	s.mu.Unlock()
	if !running {
		return testTarget(target)
	}
	address, secret := s.clashAccess()
	if address == "" || target.Tag == "" {
		return testTarget(target)
	}
	testURL := target.URL
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}
	query := latencyQuery(testURL, target.Unified)
	if target.Unified {
		result.Mode = "unified"
	}
	endpoint := "http://" + address + "/proxies/" + url.PathEscape(target.Tag) + "/delay?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := (&http.Client{Timeout: latencyClientTimeout(target.Unified)}).Do(req)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		result.Error = strings.TrimSpace(string(body))
		if result.Error == "" {
			result.Error = resp.Status
		}
		return result
	}
	var value struct {
		Delay uint16 `json:"delay"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&value); err != nil {
		result.Error = err.Error()
		return result
	}
	result.Success = 1
	result.MedianMS = float64(value.Delay)
	result.OneWayMS = result.MedianMS / 2
	return result
}
