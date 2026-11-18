package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Route-only edits preserve the applied system DNS and its original restore state.
func sameSystemDNSConfig(previous, next []byte) bool {
	type tunConfig struct {
		Type       string   `json:"type"`
		DNSMode    string   `json:"dns_mode"`
		Address    []string `json:"address"`
		DNSAddress []string `json:"dns_address"`
	}
	key := func(data []byte) []byte {
		var config struct {
			Inbounds []tunConfig `json:"inbounds"`
		}
		_ = json.Unmarshal(data, &config)
		for _, inbound := range config.Inbounds {
			if inbound.Type == "tun" && inbound.DNSMode != "disabled" {
				encoded, _ := json.Marshal(inbound)
				return encoded
			}
		}
		return nil
	}
	return bytes.Equal(key(previous), key(next))
}

type configBackup struct {
	path   string
	data   []byte
	exists bool
}

func backupConfigFiles(paths ...string) ([]configBackup, error) {
	files := make([]configBackup, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		file := configBackup{path: path, data: data, exists: err == nil}
		if file.exists {
			if err := atomicWrite(path+".previous", data, 0o600); err != nil {
				return nil, err
			}
		}
		files = append(files, file)
	}
	return files, nil
}

func restoreConfigFiles(files []configBackup) error {
	var result error
	for _, file := range files {
		var err error
		if file.exists {
			err = atomicWrite(file.path, file.data, 0o600)
		} else {
			err = os.Remove(file.path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		}
		result = errors.Join(result, err)
	}
	return result
}

// coreReload owns one configuration transaction, including a single recovery.
// configMu serializes disk writes, signal delivery and any recovery start.
type coreReload struct {
	s                    *server
	cmd                  *exec.Cmd
	helperPID            int
	done                 <-chan struct{}
	startedAt            time.Time
	transactionID        string
	transactionStartedAt time.Time
	remote               daemon.RemoteClientOptions
	generation           int64
	revision             string
	expectedRevision     string
	epoch                uint64
	signaled             bool
	restored             bool
	ctx                  context.Context
	cancel               context.CancelFunc
}

func (s *server) prepareReload(ctx context.Context, disableUDP bool) (*coreReload, error) {
	s.mu.Lock()
	running := s.cmd != nil || s.helperPID > 0
	if s.stopping || s.shuttingDown {
		s.mu.Unlock()
		return nil, errors.New("核心正在停止，请稍后应用配置")
	}
	if !running {
		s.mu.Unlock()
		return nil, nil
	}
	if disableUDP != s.disableTailscaleUDPDirect {
		s.mu.Unlock()
		return nil, errors.New("Tailscale UDP 直连选项需要更新进程环境，请停止后重新启动以应用此选项")
	}
	target := &coreReload{s: s, cmd: s.cmd, helperPID: s.helperPID, done: s.coreDone, startedAt: s.state.StartedAt, epoch: s.restartGeneration}
	s.mu.Unlock()
	if err := checkReloadSupport(); err != nil {
		return nil, err
	}
	if target.helperPID > 0 {
		if err := requireReloadHelper(); err != nil {
			return nil, err
		}
	}
	s.rpc.mu.Lock()
	port, secret := s.rpc.port, s.rpc.secret
	s.rpc.mu.Unlock()
	if port <= 0 || secret == "" {
		return nil, errors.New("配置重载需要核心 API，请使用随应用提供的 kiko-core")
	}
	target.remote = daemon.RemoteClientOptions{ServerURL: "http://127.0.0.1:" + strconv.Itoa(port), Secret: secret}
	if err := target.waitForReadiness(ctx); err != nil {
		return nil, fmt.Errorf("等待核心就绪失败，运行配置已保留: %w", err)
	}
	return target, nil
}

func (r *coreReload) waitForReadiness(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.s.mu.Lock()
		current := r.currentLocked()
		r.s.mu.Unlock()
		if !current {
			return errors.New("核心进程状态已变化，配置重载已中止")
		}
		value, err := r.instanceState(ctx)
		if err == nil {
			if value.ReloadProtocol != 1 {
				return errors.New("核心需要更新，请重新安装 TUN 特权组件并重启核心")
			}
			if value.Ready && value.InstanceId > 0 {
				r.generation, r.revision = value.InstanceId, value.ConfigRevision
				return nil
			}
		}
		if err != nil && status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *coreReload) instanceState(ctx context.Context) (*daemon.StartedAt, error) {
	// A fresh transport binds each probe to the currently listening API.
	conn, err := daemon.NewRemoteClient(r.remote)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	callCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	return daemon.NewStartedServiceClient(conn).GetStartedAt(callCtx, &emptypb.Empty{})
}

// Caller holds s.mu. Explicit Stop and shutdown cancel the entire transaction.
func (r *coreReload) currentLocked() bool {
	return r.recoveryAllowedLocked() && r.s.cmd == r.cmd && r.s.helperPID == r.helperPID && r.s.state.StartedAt.Equal(r.startedAt)
}
func (r *coreReload) recoveryAllowedLocked() bool {
	return !r.s.stopping && !r.s.shuttingDown && r.s.restartGeneration == r.epoch && r.s.persisted.DesiredRunning
}
func (r *coreReload) phase(phase string) {
	s := r.s
	s.mu.Lock()
	if s.activeReload == r && r.recoveryAllowedLocked() {
		s.state.State = "reloading"
		s.state.Reload = &reloadProgress{ID: r.transactionID, Phase: phase, StartedAt: r.transactionStartedAt}
	}
	s.mu.Unlock()
	s.emit("runtime", s.snapshot())
}
func (r *coreReload) finish() {
	if r.cancel != nil {
		r.cancel()
	}
	s := r.s
	s.mu.Lock()
	if s.activeReload == r {
		s.activeReload = nil
		s.state.Reload = nil
	}
	s.mu.Unlock()
	s.emit("runtime", s.snapshot())
}

func (r *coreReload) execute(ctx context.Context) error {
	began := time.Now()
	r.transactionStartedAt = began
	r.transactionID = fmt.Sprintf("%x", began.UnixNano())
	if err := ctx.Err(); err != nil {
		return err
	}
	s := r.s
	s.mu.Lock()
	if !r.currentLocked() {
		s.mu.Unlock()
		return errors.New("核心进程状态已变化，配置重载已中止")
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	var err error
	if r.helperPID > 0 {
		err = privilegedReload(r.helperPID)
	} else {
		err = reloadProcess(r.cmd)
	}
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("发送配置重载信号失败: %w", err)
	}
	r.signaled = true
	s.activeReload = r
	s.mu.Unlock()
	r.phase("waiting-ready")
	s.logs.add(fmt.Sprintf("[kiko] 重载事务 %s PID=%d 配置=%s，等待完整就绪", r.transactionID, s.snapshot().PID, r.expectedRevision))
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		current := r.currentLocked()
		s.mu.Unlock()
		if !current {
			return errors.New("配置重载期间核心进程已退出或停止")
		}
		value, err := r.instanceState(r.ctx)
		if err == nil && value.ReloadProtocol == 1 && value.Ready && value.InstanceId != r.generation {
			if value.ConfigRevision == r.expectedRevision {
				s.mu.Lock()
				if !r.currentLocked() {
					s.mu.Unlock()
					return errors.New("配置重载已取消")
				}
				s.state.State = "running"
				s.state.Error = ""
				s.activeReload = nil
				s.state.Reload = nil
				s.mu.Unlock()
				s.logs.add(fmt.Sprintf("[kiko] 核心已确认配置重载 instance=%d（耗时 %s）", value.InstanceId, time.Since(began).Round(time.Millisecond)))
				return nil
			}
			if value.ConfigRevision == r.revision && value.ReloadError != "" {
				r.restored = true
				return fmt.Errorf("候选配置启动失败，核心已恢复原配置: %s", value.ReloadError)
			}
		}
		select {
		case <-r.ctx.Done():
			return fmt.Errorf("等待核心完整就绪失败: %w", r.ctx.Err())
		case <-ticker.C:
		}
	}
}

// Caller already restored the previous files and holds configMu. Recovery never
// sends another SIGHUP to an instance whose lifecycle is still in progress.
func (r *coreReload) recover() (result error) {
	s := r.s
	defer func() {
		if result == nil {
			return
		}
		s.controlMu.Lock()
		s.mu.Lock()
		if !r.recoveryAllowedLocked() {
			s.mu.Unlock()
			s.controlMu.Unlock()
			return
		}
		s.persisted.DesiredRunning = false
		s.state.DesiredRunning = false
		_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
		s.mu.Unlock()
		target, done, owner := s.beginStopOperation(false)
		s.mu.Lock()
		cleanupGeneration := s.restartGeneration
		s.mu.Unlock()
		s.controlMu.Unlock()
		if owner {
			s.finishStop(target, done)
		} else if done != nil {
			<-done
		}
		s.mu.Lock()
		if s.restartGeneration == cleanupGeneration && !s.stopping && !s.shuttingDown {
			s.state.State = "failed"
			s.state.Error = result.Error()
		}
		s.mu.Unlock()
	}()
	s.mu.Lock()
	allowed := r.recoveryAllowedLocked()
	s.mu.Unlock()
	if !allowed {
		return errors.New("用户已停止核心，恢复启动已取消")
	}
	if r.restored {
		s.mu.Lock()
		current := r.currentLocked()
		if current {
			s.state.State = "running"
			s.activeReload = nil
			s.state.Reload = nil
		}
		s.mu.Unlock()
		if current {
			return nil
		}
	}
	r.phase("recovering")
	s.logs.add("[kiko] 正在结束未完成的重载，准备恢复原配置（最多启动一次）")
	// Confirm process exit and DNS cleanup before acquiring the old resources.
	s.mu.Lock()
	same := s.cmd == r.cmd && s.helperPID == r.helperPID
	if same {
		if r.helperPID > 0 {
			_ = privilegedStop(r.helperPID)
		} else if r.cmd != nil {
			_ = gracefulProcess(r.cmd)
		}
	}
	s.mu.Unlock()
	gone := func() bool {
		if r.done == nil {
			return true
		}
		select {
		case <-r.done:
			return true
		default:
			return false
		}
	}
	if !awaitCore(coreGraceTimeout, gone) {
		s.mu.Lock()
		if s.cmd == r.cmd && r.cmd != nil {
			_ = forceProcess(r.cmd)
		}
		if s.helperPID == r.helperPID && r.helperPID > 0 {
			_ = privilegedForceStop(r.helperPID)
		}
		s.mu.Unlock()
		if !awaitCore(coreKillTimeout, gone) {
			return errors.New("旧核心退出或 DNS 清理超时，恢复启动已中止")
		}
	}
	s.mu.Lock()
	allowed = r.recoveryAllowedLocked()
	s.mu.Unlock()
	if !allowed {
		return errors.New("用户已停止核心，恢复启动已取消")
	}
	if err := s.startForGenerationLocked(r.epoch, r.epoch); err != nil {
		return err
	}
	s.mu.Lock()
	if s.stopping || s.shuttingDown || !s.persisted.DesiredRunning || (s.cmd == nil && s.helperPID == 0) {
		s.mu.Unlock()
		return errors.New("恢复启动已取消")
	}
	r.epoch = s.restartGeneration
	target := &coreReload{s: s, cmd: s.cmd, helperPID: s.helperPID, startedAt: s.state.StartedAt, epoch: r.epoch, remote: r.remote}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := target.waitForReadiness(ctx); err != nil {
		return fmt.Errorf("恢复原配置后等待就绪失败: %w", err)
	}
	if target.revision != r.revision {
		return errors.New("恢复配置标识校验失败")
	}
	s.mu.Lock()
	if !target.currentLocked() {
		s.mu.Unlock()
		return errors.New("恢复启动已取消")
	}
	s.state.State = "running"
	s.activeReload = nil
	s.state.Reload = nil
	s.mu.Unlock()
	s.logs.add("[kiko] 原配置已恢复并确认完整就绪")
	return nil
}

func configRevision(data []byte) string {
	var config struct {
		Services []struct {
			Tag      string `json:"tag"`
			Revision string `json:"config_revision"`
		} `json:"services"`
	}
	_ = json.Unmarshal(data, &config)
	for _, entry := range config.Services {
		if entry.Tag == apiServiceTag {
			return entry.Revision
		}
	}
	return ""
}
