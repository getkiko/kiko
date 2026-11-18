//go:build darwin

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
)

var privilegedHelperPaths = helperpaths.Current

func requireReloadHelper() error { return requireCurrentHelper() }

type helperRequest struct {
	Op                        string `json:"op"`
	PID                       int    `json:"pid,omitempty"`
	SingBox                   string `json:"singBox,omitempty"`
	Config                    string `json:"config,omitempty"`
	DataDir                   string `json:"dataDir,omitempty"`
	EndpointTag               string `json:"endpointTag,omitempty"`
	DisableTailscaleUDPDirect bool   `json:"disableTailscaleUDPDirect,omitempty"`
}
type helperResponse struct {
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	ExitedPID       int    `json:"exitedPID,omitempty"`
	ExitCode        int    `json:"exitCode,omitempty"`
	Signal          string `json:"signal,omitempty"`
	OK              bool   `json:"ok"`
	PID             int    `json:"pid,omitempty"`
	Files           int    `json:"files,omitempty"`
	Bytes           int64  `json:"bytes,omitempty"`
	Error           string `json:"error,omitempty"`
}

func privilegedStart(_, config, dataDir, _ string, disableTailscaleUDPDirect bool) (int, error) {
	if err := requireCurrentHelper(); err != nil {
		return 0, err
	}
	var response helperResponse
	err := helperCall(helperRequest{Op: "start", Config: config, DataDir: dataDir, DisableTailscaleUDPDirect: disableTailscaleUDPDirect}, &response)
	if err != nil {
		return 0, fmt.Errorf("macOS 特权助手不可用，请先安装 Kiko Privileged Helper: %w", err)
	}
	if !response.OK || response.PID <= 1 {
		return 0, errors.New(response.Error)
	}
	return response.PID, nil
}

func privilegedStop(pid int) error {
	var response helperResponse
	if err := helperCall(helperRequest{Op: "stop", PID: pid}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func privilegedForceStop(pid int) error {
	var response helperResponse
	if err := helperCall(helperRequest{Op: "force-stop", PID: pid}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func privilegedReload(pid int) error {
	var response helperResponse
	if err := helperCall(helperRequest{Op: "reload", PID: pid}, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func repairTaildropAccess(dataDir, endpointTag string) error {
	if err := requireCurrentHelper(); err != nil {
		return err
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	var response helperResponse
	err = helperCall(helperRequest{
		Op: "taildrop-access", Config: filepath.Join(dataDir, "runtime", "config.json"),
		DataDir: dataDir, EndpointTag: endpointTag,
	}, &response)
	if err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error)
	}
	return nil
}

func requireCurrentHelper() error {
	var response helperResponse
	if err := helperCall(helperRequest{Op: "status"}, &response); err != nil {
		return err
	}
	if !response.OK || response.ProtocolVersion < helperpaths.ProtocolVersion {
		return errors.New("macOS 特权助手需要更新，请在应用菜单选择“重新安装 TUN 特权助手”，完成后重新应用配置")
	}
	return nil
}

func helperCall(request helperRequest, response *helperResponse) error {
	conn, err := net.DialTimeout("unix", privilegedHelperPaths.Socket, 2*time.Second)
	if errors.Is(err, os.ErrNotExist) && helperDaemonInstalled() && waitHelperSocket() {
		conn, err = net.DialTimeout("unix", privilegedHelperPaths.Socket, 2*time.Second)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if err = json.NewEncoder(conn).Encode(request); err != nil {
		return err
	}
	return json.NewDecoder(bufio.NewReader(conn)).Decode(response)
}

func helperLogPath(_ string) string { return privilegedHelperPaths.CoreLog }

// clearLogsProtocolVersion is the first helper protocol with clear-logs.
const clearLogsProtocolVersion = 6

// privilegedSystemLogs exposes the TUN component's logs under /var/log.
// They are world-readable, so sizes come from stat; clearing goes through
// the root helper.
func privilegedSystemLogs() *systemLogs {
	return &systemLogs{
		groups: func() []logFileGroup {
			return []logFileGroup{
				{Key: "core", Label: "TUN 核心日志", Paths: helperpaths.LogFiles(privilegedHelperPaths.CoreLog)},
				{Key: "helper", Label: "特权助手日志", Paths: helperpaths.LogFiles(privilegedHelperPaths.HelperLog)},
			}
		},
		clear: clearPrivilegedLogs,
	}
}

func clearPrivilegedLogs() (int, int64, error) {
	if !helperInstalled() {
		return 0, 0, errors.New("TUN 特权助手未运行，系统日志未清理")
	}
	var status helperResponse
	if err := helperCall(helperRequest{Op: "status"}, &status); err != nil {
		return 0, 0, fmt.Errorf("系统日志未清理: %w", err)
	}
	if !status.OK || status.ProtocolVersion < clearLogsProtocolVersion {
		return 0, 0, errors.New("重新安装 TUN 特权组件后才能清理系统日志")
	}
	var response helperResponse
	if err := helperCall(helperRequest{Op: "clear-logs"}, &response); err != nil {
		return 0, 0, fmt.Errorf("系统日志未清理: %w", err)
	}
	if !response.OK {
		return response.Files, response.Bytes, fmt.Errorf("系统日志未完全清理: %s", response.Error)
	}
	return response.Files, response.Bytes, nil
}
func helperInstalled() bool {
	return helperSocketExists() || (helperDaemonInstalled() && waitHelperSocket())
}

// helperDaemonInstalled reports whether the helper's launch daemon is
// installed, so a missing socket is worth waiting for.
func helperDaemonInstalled() bool {
	_, err := os.Stat(privilegedHelperPaths.LaunchDaemon)
	return err == nil
}

func helperSocketExists() bool { _, err := os.Stat(privilegedHelperPaths.Socket); return err == nil }

// helperSocketRecovery covers the helper re-binding its socket after a
// cleanup tool deletes it from /var/run (the helper checks every second).
const helperSocketRecovery = 2500 * time.Millisecond

func waitHelperSocket() bool {
	deadline := time.Now().Add(helperSocketRecovery)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if helperSocketExists() {
			return true
		}
	}
	return false
}
func privilegedAlive(pid int) bool {
	alive, _ := privilegedAliveChecked(pid)
	return alive
}
func privilegedAliveChecked(pid int) (bool, error) {
	alive, _, err := privilegedProcessStatus(pid)
	return alive, err
}
func privilegedProcessStatus(pid int) (bool, string, error) {
	var response helperResponse
	if err := helperCall(helperRequest{Op: "status"}, &response); err != nil {
		return false, "", err
	}
	if !response.OK {
		return false, "", errors.New(response.Error)
	}
	detail := "退出状态未知"
	if response.ExitedPID == pid {
		detail = fmt.Sprintf("exit code=%d", response.ExitCode)
		if response.Signal != "" {
			detail = "signal=" + response.Signal
		}
	}
	return response.PID == pid, detail, nil
}
