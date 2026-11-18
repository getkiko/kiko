package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dnsmonitorapi "github.com/sagernet/sing-box/common/dnsmonitor/api"
	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const apiServiceTag = "kiko-api"

// legacyAPIServiceTag is the API service's tag in runtime configurations
// written before the rename to Kiko; it is replaced like the current one.
const legacyAPIServiceTag = "sbcfg-api"

func isAPIServiceTag(tag any) bool { return tag == apiServiceTag || tag == legacyAPIServiceTag }

type grpcState struct {
	mu             sync.Mutex
	probeOnce      sync.Once
	dnsProbeOnce   sync.Once
	dnsSupported   bool
	groupProbeOnce sync.Once
	groupModes     bool
	dns            dnsmonitorapi.DNSMonitorClient
	httpCapture    mitmapi.HTTPCaptureClient
	script         mitmapi.ScriptClient
	httpProbeOnce  sync.Once
	// persistProbeOnce guards the probe of mitm.capture.persist.
	persistProbeOnce sync.Once
	persistSupported bool
	httpSupported    bool
	supported        bool
	port             int
	secret           string
	cancel           context.CancelFunc
	workerDone       chan struct{}
	runID            int64
	client           daemon.StartedServiceClient
	selectors        []clashSelector
	selectorsReady   bool
}

// apiSupported probes only once: before sing-box 1.14 there is no api service and check fails outright.
func (s *server) apiSupported() bool {
	s.rpc.probeOnce.Do(func() { s.rpc.supported = s.probeAPI() })
	return s.rpc.supported
}

func (s *server) probeAPI() bool {
	if s.singBox == "" || s.dataDir == "" {
		return false
	}
	path := filepath.Join(s.dataDir, "runtime", "api-probe.json")
	config := `{"services":[{"type":"api","tag":"probe","listen":"127.0.0.1"}]}`
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(config), 0o600) != nil {
		return false
	}
	defer os.Remove(path)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
}

// groupModesSupported probes only once: upstream and older cores have no fallback /
// loadbalance / smart groups, and check fails outright.
func (s *server) groupModesSupported() bool {
	s.rpc.groupProbeOnce.Do(func() {
		if s.singBox == "" || s.dataDir == "" {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "group-modes-probe.json")
		config := `{"outbounds":[{"type":"direct","tag":"probe-direct"},{"type":"fallback","tag":"probe-fallback","outbounds":["probe-direct"]},{"type":"loadbalance","tag":"probe-loadbalance","outbounds":["probe-direct"]},{"type":"smart","tag":"probe-smart","outbounds":["probe-direct"]}]}`
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(config), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.rpc.groupModes = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.rpc.groupModes
}

func (s *server) runtimeAPI(avoidPort string) (int, string, error) {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	if s.rpc.port > 0 && strconv.Itoa(s.rpc.port) == avoidPort {
		s.rpc.port = 0
	}
	for i := 0; s.rpc.port <= 0 && i < 10; i++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, "", err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if strconv.Itoa(port) != avoidPort {
			s.rpc.port = port
		}
	}
	if s.rpc.port <= 0 {
		return 0, "", errors.New("no free port for kiko api service")
	}
	if s.rpc.secret == "" {
		s.rpc.secret = token()
	}
	return s.rpc.port, s.rpc.secret, nil
}

func (s *server) injectAPIService(config map[string]any, clashAddress string) error {
	_, clashPort, _ := net.SplitHostPort(clashAddress)
	port, secret, err := s.runtimeAPI(clashPort)
	if err != nil {
		return err
	}
	services, _ := config["services"].([]any)
	kept := make([]any, 0, len(services)+1)
	for _, item := range services {
		if service, ok := item.(map[string]any); ok && isAPIServiceTag(service["tag"]) {
			continue
		}
		kept = append(kept, item)
	}
	injected := map[string]any{
		"type": "api", "tag": apiServiceTag, "listen": "127.0.0.1", "listen_port": port, "secret": secret,
	}
	config["services"] = append(kept, injected)
	canonical, err := json.Marshal(config)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	injected["config_revision"] = hex.EncodeToString(digest[:])
	return nil
}

// grpcTick is called by the monitor every second: while kiko-core runs it keeps exactly one connection goroutine bound to the current run.
func (s *server) grpcTick() {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	s.mu.Lock()
	running := !s.shuttingDown && (s.cmd != nil || s.helperPID > 0) && (s.state.State == "running" || s.state.State == "reloading")
	runID := s.state.StartedAt.UnixNano()
	s.mu.Unlock()
	if !running || s.rpc.port <= 0 || s.rpc.secret == "" {
		s.stopGRPCLocked()
		return
	}
	if s.rpc.cancel != nil && s.rpc.runID == runID {
		return
	}
	s.stopGRPCLocked()
	ctx, cancel := context.WithCancel(context.Background())
	s.rpc.cancel, s.rpc.runID = cancel, runID
	done := make(chan struct{})
	s.rpc.workerDone = done
	go func(url, secret string) {
		defer close(done)
		s.grpcWorker(ctx, url, secret)
	}("http://127.0.0.1:"+strconv.Itoa(s.rpc.port), s.rpc.secret)
}

func (s *server) stopGRPC() {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	s.stopGRPCLocked()
}

func (s *server) stopGRPCLocked() {
	if s.rpc.cancel != nil {
		s.rpc.cancel()
		s.rpc.cancel = nil
	}
	s.rpc.client = nil
	s.rpc.dns = nil
	s.rpc.httpCapture = nil
	s.rpc.script = nil
	s.rpc.selectors = nil
	s.rpc.selectorsReady = false
	s.resetEndpointSnapshots()
	s.stopUSBIPShare()
}

// The core pushes one status frame per second. With no new frame for grpcStatusTimeout the session
// is considered stuck and reconnects; after a session ends, wait at most grpcCollectorGrace for the
// collector goroutines to exit so a single goroutine cannot hold up the reconnect.
var (
	grpcStatusTimeout  = 5 * time.Second
	grpcCollectorGrace = 3 * time.Second
)

func (s *server) grpcWorker(ctx context.Context, url, secret string) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		if s.grpcSession(ctx, url, secret) {
			backoff = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

// grpcSession reports whether this session received a first status frame, used to reset the backoff.
func (s *server) grpcSession(ctx context.Context, url, secret string) bool {
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: url, Secret: secret})
	if err != nil {
		return false
	}
	defer conn.Close()
	client := daemon.NewStartedServiceClient(conn)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	statusStream, err := client.SubscribeStatus(sessionCtx, &daemon.SubscribeStatusRequest{Interval: int64(time.Second)})
	if err != nil {
		return false
	}
	groupsStream, err := client.SubscribeGroups(sessionCtx, &emptypb.Empty{})
	if err != nil {
		return false
	}
	var lastFrame atomic.Int64
	lastFrame.Store(time.Now().UnixNano())
	var connected atomic.Bool
	var streams, collectors sync.WaitGroup
	ended := make(chan struct{}, 2)
	streams.Add(2)
	go func() {
		defer streams.Done()
		defer func() { ended <- struct{}{} }()
		for {
			status, recvErr := statusStream.Recv()
			if recvErr != nil || sessionCtx.Err() != nil {
				return
			}
			lastFrame.Store(time.Now().UnixNano())
			s.applyGRPCStatus(status, time.Second)
			if connected.CompareAndSwap(false, true) {
				s.grpcConnected(sessionCtx, conn, client, &collectors)
			}
		}
	}()
	go func() {
		defer streams.Done()
		defer func() { ended <- struct{}{} }()
		for {
			groups, recvErr := groupsStream.Recv()
			if recvErr != nil || sessionCtx.Err() != nil {
				return
			}
			s.applyGRPCGroups(groups)
		}
	}()

	watchdog := time.NewTicker(min(time.Second, grpcStatusTimeout/2))
	defer watchdog.Stop()
watch:
	for {
		select {
		case <-ended:
			break watch
		case <-watchdog.C:
			// Allow more time before the first frame to avoid misjudging a core that just started or is under load.
			limit := grpcStatusTimeout
			if !connected.Load() {
				limit *= 3
			}
			if time.Since(time.Unix(0, lastFrame.Load())) > limit {
				if s.logs != nil {
					s.logs.add("[kiko] gRPC 状态流长时间无数据，重新连接")
				}
				break watch
			}
		}
	}
	cancel()
	// Wait for both stream goroutines to exit first so an in-flight applyGRPC* cannot write stale data after the reset.
	deadline := time.Now().Add(grpcCollectorGrace)
	streamsDone := waitUntil(&streams, deadline)

	// A still-valid ctx means this worker is still the current one and only this session is running,
	// so the session state can be reset; once ctx is canceled stopGRPCLocked has already reset it and
	// the new worker's state must not be touched.
	s.rpc.mu.Lock()
	if ctx.Err() == nil {
		s.rpc.client = nil
		s.rpc.dns = nil
		s.rpc.httpCapture = nil
		s.rpc.script = nil
		s.rpc.selectorsReady = false
	}
	s.rpc.mu.Unlock()
	if ctx.Err() == nil {
		s.resetEndpointSnapshots()
	}
	if !streamsDone || !waitUntil(&collectors, deadline) {
		if s.logs != nil {
			s.logs.add("[kiko] gRPC 会话的采集协程未及时退出，先行重连")
		}
	}
	return connected.Load()
}

// grpcConnected registers the client and starts all collectors once the session receives its first status frame.
func (s *server) grpcConnected(ctx context.Context, conn *grpc.ClientConn, client daemon.StartedServiceClient, collectors *sync.WaitGroup) {
	s.rpc.mu.Lock()
	if ctx.Err() != nil {
		s.rpc.mu.Unlock()
		return
	}
	s.rpc.client = client
	s.rpc.dns = dnsmonitorapi.NewDNSMonitorClient(conn)
	s.rpc.httpCapture = mitmapi.NewHTTPCaptureClient(conn)
	s.rpc.script = mitmapi.NewScriptClient(conn)
	runID := s.rpc.runID
	s.rpc.mu.Unlock()
	if s.logs != nil {
		s.logs.add("[kiko] gRPC API 已连接")
	}
	s.startEndpointSubscriptions(ctx, client)
	// mitmSession may block on a keychain authorization dialog (not governed by ctx), so it is not
	// counted in collectors, keeping it from holding up the stats reconnect.
	go s.mitmSession(ctx, mitmapi.NewMITMClient(conn), runID)
	started := s.snapshot().StartedAt
	collectors.Add(2)
	go func() {
		defer collectors.Done()
		s.collectUsageConnections(ctx, client, started)
	}()
	go func() {
		defer collectors.Done()
		s.collectConnectionHistory(ctx, client, started)
	}()
}

// waitUntil waits for wg to finish, at most until deadline, and reports whether it finished in time.
func waitUntil(wg *sync.WaitGroup, deadline time.Time) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (s *server) grpcClient() daemon.StartedServiceClient {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	return s.rpc.client
}

// grpcActive means gRPC has received the first status frame; metrics then come from gRPC and Clash is no longer polled.
func (s *server) grpcActive() bool { return s.grpcClient() != nil }

func (s *server) applyGRPCStatus(status *daemon.Status, interval time.Duration) {
	seconds := interval.Seconds()
	rate := func(delta int64) uint64 {
		if delta <= 0 || seconds <= 0 {
			return 0
		}
		return uint64(float64(delta) / seconds)
	}
	s.mu.Lock()
	s.state.MemoryBytes = status.GetMemory()
	// Clash /connections entries come from trafficManager, matching connectionsIn; connectionsOut counts the outbound connection manager.
	s.state.ActiveConnections = int(status.GetConnectionsIn())
	s.state.UploadRate = rate(status.GetUplink())
	s.state.DownloadRate = rate(status.GetDownlink())
	s.state.UploadTotal = uint64(max(status.GetUplinkTotal(), 0))
	s.state.DownloadTotal = uint64(max(status.GetDownlinkTotal(), 0))
	s.state.MetricsAvailable = status.GetTrafficAvailable()
	s.metricsSampleAt = time.Time{}
	s.mu.Unlock()
}

func sortSelectors(selectors []clashSelector) {
	rank := func(value string) int {
		if strings.EqualFold(value, "Selector") {
			return 0
		}
		return 1
	}
	sort.SliceStable(selectors, func(i, j int) bool {
		if left, right := rank(selectors[i].Type), rank(selectors[j].Type); left != right {
			return left < right
		}
		return selectors[i].Name < selectors[j].Name
	})
}

// groupKinds are the policy groups' Clash type names by core type.
var groupKinds = map[string]string{
	"selector": "Selector", "urltest": "URLTest", "fallback": "Fallback", "loadbalance": "LoadBalance", "smart": "Smart",
}

// groupKind is the Clash type name of a policy group type, as the core
// or the Clash API names it, "" for other types.
func groupKind(value string) string {
	value = strings.ToLower(value)
	if kind, ok := groupKinds[value]; ok {
		return kind
	}
	for _, kind := range groupKinds {
		if strings.ToLower(kind) == value {
			return kind
		}
	}
	return ""
}

// rounded is v to three decimals.
func rounded(v float32) float64 { return math.Round(float64(v)*1000) / 1000 }

var memberStates = []string{"unknown", "up", "suspect", "down"}

func selectorsFromGroups(groups *daemon.Groups) []clashSelector {
	selectors := make([]clashSelector, 0, len(groups.GetGroup()))
	for _, group := range groups.GetGroup() {
		kind := groupKind(group.GetType())
		if kind == "" {
			continue
		}
		all := make([]string, 0, len(group.GetItems()))
		var members []clashGroupMember
		for _, item := range group.GetItems() {
			all = append(all, item.GetTag())
			if group.GetMode() == "" {
				continue
			}
			member := clashGroupMember{Name: item.GetTag(), Active: item.GetActive(), DelayMS: int(item.GetUrlTestDelay()),
				SuccessRate: rounded(item.GetSuccessRate()), LatencyMS: int(item.GetPassiveDelay()), Score: rounded(item.GetScore())}
			if state := int(item.GetState()); state >= 0 && state < len(memberStates) {
				member.State = memberStates[state]
			}
			members = append(members, member)
		}
		selectors = append(selectors, clashSelector{Name: group.GetTag(), Type: kind, Now: group.GetSelected(), All: all,
			Mode: group.GetMode(), Strategy: group.GetStrategy(), Members: members})
	}
	sortSelectors(selectors)
	return selectors
}

func (s *server) applyGRPCGroups(groups *daemon.Groups) {
	selectors := selectorsFromGroups(groups)
	s.rpc.mu.Lock()
	changed := !s.rpc.selectorsReady || !reflect.DeepEqual(s.rpc.selectors, selectors)
	s.rpc.selectors, s.rpc.selectorsReady = selectors, true
	s.rpc.mu.Unlock()
	if changed {
		s.emit("selectors", map[string]any{"selectors": selectors})
	}
}

// refreshGRPCGroups re-reads the core's groups once into the snapshot (emitting a selectors event on change).
// The core's group stream only pushes on latency tests, outbound switches and service state changes, not
// after a live apply adds, removes or renames policy groups, so the snapshot and events would stay on the
// old groups; it is therefore re-read here after a successful apply: a new subscription's first frame is
// the core's current groups.
func (s *server) refreshGRPCGroups(parent context.Context) {
	client := s.grpcClient()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	stream, err := client.SubscribeGroups(ctx, &emptypb.Empty{})
	if err != nil {
		return
	}
	groups, err := stream.Recv()
	if err != nil {
		return
	}
	if s.grpcClient() == client {
		s.applyGRPCGroups(groups)
	}
}

// grpcSelectors returns the latest gRPC group snapshot; ok=false when not ready, and the caller falls back to Clash.
func (s *server) grpcSelectors() ([]clashSelector, bool) {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	if s.rpc.client == nil || !s.rpc.selectorsReady {
		return nil, false
	}
	return append([]clashSelector{}, s.rpc.selectors...), true
}

// grpcSelect switches the outbound via gRPC; on failure it returns an error and the caller falls back to Clash.
func (s *server) grpcSelect(ctx context.Context, group, outbound string) error {
	client := s.grpcClient()
	if client == nil {
		return errors.New("gRPC client unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := client.SelectOutbound(ctx, &daemon.SelectOutboundRequest{GroupTag: group, OutboundTag: outbound}); err != nil {
		return err
	}
	s.rpc.mu.Lock()
	var snapshot []clashSelector
	for i := range s.rpc.selectors {
		if s.rpc.selectors[i].Name == group && s.rpc.selectors[i].Now != outbound {
			s.rpc.selectors[i].Now = outbound
			snapshot = append([]clashSelector{}, s.rpc.selectors...)
		}
	}
	s.rpc.mu.Unlock()
	if snapshot != nil {
		s.emit("selectors", map[string]any{"selectors": snapshot})
	}
	return nil
}
