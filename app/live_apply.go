package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/protobuf/types/known/emptypb"
)

const liveUnsupportedMessage = "核心版本不支持不停机应用，请重装 TUN 组件或重启应用"

// liveApplier is the part of the core API a live apply uses; tests replace it.
type liveApplier interface {
	GetStartedAt(ctx context.Context, in *emptypb.Empty) (*daemon.StartedAt, error)
	ApplyLiveConfig(ctx context.Context, in *daemon.LiveConfigRequest) (*daemon.LiveConfigResult, error)
}

// dialLiveApplier connects to the running core's API.
func (s *server) dialLiveApplier() (liveApplier, func(), error) {
	s.rpc.mu.Lock()
	port, secret := s.rpc.port, s.rpc.secret
	s.rpc.mu.Unlock()
	if port <= 0 || secret == "" {
		return nil, nil, errLiveUnsupported
	}
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://127.0.0.1:" + strconv.Itoa(port), Secret: secret})
	if err != nil {
		return nil, nil, err
	}
	return startedClient{daemon.NewStartedServiceClient(conn)}, func() { _ = conn.Close() }, nil
}

type startedClient struct{ daemon.StartedServiceClient }

func (c startedClient) GetStartedAt(ctx context.Context, in *emptypb.Empty) (*daemon.StartedAt, error) {
	return c.StartedServiceClient.GetStartedAt(ctx, in)
}
func (c startedClient) ApplyLiveConfig(ctx context.Context, in *daemon.LiveConfigRequest) (*daemon.LiveConfigResult, error) {
	return c.StartedServiceClient.ApplyLiveConfig(ctx, in)
}

var errLiveUnsupported = errors.New(liveUnsupportedMessage)

// applyLive applies route rules, outbounds and DNS to the running core
// without recreating it. Changes the core cannot take live answer 409 with
// restartRequired and leave the saved configuration untouched.
func (s *server) applyLive(w http.ResponseWriter, r *http.Request) {
	// The write gate already queues concurrent applies; configMu still
	// excludes background starts and reload recovery.
	if !s.lockConfig(r.Context(), 30*time.Second) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "配置正在应用，请稍后重试"})
		return
	}
	defer s.configMu.Unlock()
	s.logs.add("[kiko] 开始不停机应用配置")
	c, ok := s.checkCandidate(w, r)
	if !ok {
		return
	}
	restart := func(reason string) {
		s.logs.add("[kiko] 不停机应用需要重启核心: " + reason)
		writeJSON(w, http.StatusConflict, map[string]any{"error": reason, "restartRequired": true})
	}
	s.mu.Lock()
	running := (s.cmd != nil || s.helperPID > 0) && !s.stopping && !s.shuttingDown
	udpChanged := c.disableUDP != s.disableTailscaleUDPDirect
	s.mu.Unlock()
	if !running {
		restart("核心未运行")
		return
	}
	if udpChanged {
		restart("Tailscale UDP 直连选项有改动，需要重启核心")
		return
	}
	dial := s.liveDial
	if dial == nil {
		dial = s.dialLiveApplier
	}
	client, closeClient, err := dial()
	if errors.Is(err, errLiveUnsupported) {
		restart(liveUnsupportedMessage)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "连接核心失败: " + err.Error()})
		return
	}
	defer closeClient()
	stateCtx, cancelState := context.WithTimeout(r.Context(), 3*time.Second)
	started, err := client.GetStartedAt(stateCtx, &emptypb.Empty{})
	cancelState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "读取核心状态失败: " + err.Error()})
		return
	}
	if started.LiveApplyProtocol < 1 {
		restart(liveUnsupportedMessage)
		return
	}
	config := filepath.Join(s.dataDir, "config.json")
	runtimePath := filepath.Join(s.dataDir, "runtime", "config.json")
	previous, _ := os.ReadFile(runtimePath)
	same := bytes.Equal(bytes.TrimSpace(previous), bytes.TrimSpace(c.runtime))
	if same {
		s.logs.add("[kiko] 配置内容相同，保留当前运行实例")
	} else {
		// Finish the transaction even if the browser disconnects meanwhile.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		result, err := client.ApplyLiveConfig(ctx, &daemon.LiveConfigRequest{ConfigJson: string(c.runtime), Revision: time.Now().UnixNano()})
		cancel()
		if err != nil {
			s.logs.add("[kiko] 不停机应用失败: " + err.Error())
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "不停机应用失败: " + err.Error()})
			return
		}
		if result.RestartRequired {
			restart(liveRestartReason(result.Reason))
			return
		}
		if !result.Applied {
			s.logs.add("[kiko] 不停机应用失败，核心保留原配置: " + result.Reason)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "不停机应用失败，核心保留原配置: " + result.Reason})
			return
		}
	}
	// Before answering, so that clients reading the groups after the
	// apply see the new ones; also when the content is the same, which
	// recovers a snapshot an earlier refresh failed to update.
	s.refreshGRPCGroups(context.WithoutCancel(r.Context()))
	// The core runs the new configuration now: the files follow it.
	if _, err := backupConfigFiles(config, runtimePath); err != nil {
		s.logs.add("[kiko] 备份配置失败: " + err.Error())
	}
	err = atomicWrite(config, append(c.clean, '\n'), 0o600)
	if err == nil {
		err = atomicWrite(runtimePath, c.runtime, 0o600)
	}
	if err != nil {
		s.logs.add("[kiko] 核心已应用新配置，但保存配置文件失败: " + err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "核心已应用新配置，但保存配置文件失败: " + err.Error()})
		return
	}
	s.storeRuleMap(c.ruleMap)
	s.storeDNSRuleMap(c.dnsRuleMap, same)
	s.mu.Lock()
	s.persisted.LastApplied = time.Now()
	_ = atomicJSON(filepath.Join(s.dataDir, "runtime-state.json"), s.persisted)
	s.mu.Unlock()
	s.mitmSyncAfterApply()
	s.syncSysProxy()
	writeJSON(w, http.StatusOK, map[string]any{"applied": true, "live": true, "output": string(c.output), "runtime": s.snapshot()})
	s.emit("runtime", s.snapshot())
	s.logs.add("[kiko] 不停机应用完成")
}

// appliedConfig answers the configuration last applied (config.json), so
// clients can tell which parts of their document are not applied yet.
func (s *server) appliedConfig(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(filepath.Join(s.dataDir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("{}")
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
