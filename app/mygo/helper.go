package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// helperPaths mirrors the fields of helperpaths/paths.json the shell needs.
type helperPaths struct {
	Label             string `json:"label"`
	Socket            string `json:"socket"`
	HelperExecutable  string `json:"helperExecutable"`
	CoreExecutable    string `json:"coreExecutable"`
	HelperLog         string `json:"helperLog"`
	CoreLog           string `json:"coreLog"`
	LaunchDaemon      string `json:"launchDaemon"`
	ComponentVersions string `json:"componentVersions"`
}

// helperChange describes one component that differs from the installed copy.
type helperChange struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// helperStatus is what the settings panel reads.
type helperStatus struct {
	Installed   bool           `json:"installed"`
	NeedsUpdate bool           `json:"needsUpdate"`
	Changes     []helperChange `json:"changes"`
}

// helperInstallResult extends the status with whether the install ran.
type helperInstallResult struct {
	helperStatus
	Completed bool `json:"completed"`
}

// helperManager checks explicit component revisions before privileged installation.
type helperManager struct {
	paths *Paths
	cfg   helperPaths

	mu         sync.Mutex
	installing bool
	installErr error
}

func newHelperManager(paths *Paths) *helperManager {
	manager := &helperManager{paths: paths}
	if raw, err := os.ReadFile(paths.helperConfigPath()); err == nil {
		_ = json.Unmarshal(raw, &manager.cfg)
	}
	return manager
}

// status uses revisions shipped with the app and recorded by the root installer.
func (m *helperManager) status() (helperStatus, error) {
	versions, err := readHelperVersions(m.paths.helperVersionsPath())
	if err != nil {
		return helperStatus{}, fmt.Errorf("读取特权组件版本: %w", err)
	}
	return inspectHelperInstallation(m.cfg, versions)
}

// ensure installs the helper when needed, or always when force is set.
// Concurrent callers share one installation.
func (m *helperManager) ensure(force bool) (bool, error) {
	m.mu.Lock()
	if m.installing {
		m.mu.Unlock()
		return false, errors.New("特权助手正在安装")
	}
	m.installing = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.installing = false
		m.mu.Unlock()
	}()

	status, err := m.status()
	if err != nil {
		return false, err
	}
	if !force && !status.NeedsUpdate {
		return false, nil
	}
	return m.install(status)
}

// install prompts, then runs the privileged installer script.
func (m *helperManager) install(status helperStatus) (bool, error) {
	prompt := helperInstallationPrompt(status)
	result, err := mygo.Dialog.Message(mygo.MessageOptions{
		Type:          mygo.MessageInfo,
		Title:         prompt.title,
		Message:       prompt.message,
		Detail:        "系统将请求管理员授权，用于安装 TUN 特权组件。完成后请重新应用配置。",
		Buttons:       []string{prompt.action, "稍后"},
		DefaultButton: 0,
		CancelButton:  1,
	})
	if err != nil {
		return false, err
	}
	if result.Button != 0 {
		return false, nil
	}

	helper := m.paths.helperExecutable()
	core := m.paths.binaryPath("kiko-core")
	installer := m.paths.helperInstaller()
	versions := m.paths.helperVersionsPath()
	if _, err := readHelperVersions(versions); err != nil {
		return false, err
	}
	for _, file := range []string{helper, installer, core, versions} {
		if !fileExists(file) {
			return false, fmt.Errorf("缺少特权助手资源: %s", file)
		}
	}
	args := []string{
		installer, helper, core,
		m.cfg.Label, m.cfg.HelperExecutable, m.cfg.CoreExecutable,
		m.cfg.LaunchDaemon, m.cfg.Socket, m.cfg.HelperLog, m.cfg.CoreLog,
		versions, m.cfg.ComponentVersions,
	}
	if err := runPrivileged(args); err != nil {
		return false, err
	}
	// The socket appears once the launch daemon starts.
	if err := waitForSocket(m.cfg.Socket); err != nil {
		return false, fmt.Errorf("特权助手安装完成后未能启动，请查看 %s", m.cfg.HelperLog)
	}
	return true, nil
}

// helperInstallationPrompt keeps installation and update messages focused on TUN.
func helperInstallationPrompt(status helperStatus) struct{ title, action, message string } {
	if !status.Installed {
		return struct{ title, action, message string }{
			title: "安装 TUN 特权助手", action: "安装助手",
			message: "Kiko 需要安装一个 macOS 特权助手来创建 TUN 网络接口。",
		}
	}
	if len(status.Changes) == 0 {
		return struct{ title, action, message string }{
			title: "重新安装 TUN 特权助手", action: "重新安装",
			message: "系统中的特权助手和核心已是当前版本，将重新安装。",
		}
	}
	repairs := false
	names := make([]string, 0, len(status.Changes))
	for _, change := range status.Changes {
		switch change.Reason {
		case "missing", "unreadable":
			repairs = true
		}
		names = append(names, change.Name)
	}
	joined := strings.Join(names, "、")
	switch {
	case repairs:
		return struct{ title, action, message string }{
			title: "修复 TUN 特权组件", action: "修复组件",
			message: joined + " 需要修复，将同步应用内的版本。",
		}
	default:
		return struct{ title, action, message string }{
			title: "更新 TUN 特权组件", action: "同步更新",
			message: joined + " 已更新，请安装对应的 TUN 特权组件。",
		}
	}
}

// waitForSocket waits until the helper's socket exists.
func waitForSocket(path string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fileExists(path) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("socket 未出现")
}
