package main

import (
	"encoding/json"
	"errors"
	"os"
)

// Revisions change only when the corresponding privileged component changes.
// The installer stores this manifest alongside the root-owned executables.
type helperVersions struct {
	Helper    int `json:"helper"`
	Core      int `json:"core"`
	Installer int `json:"installer"`
}

func readHelperVersions(path string) (helperVersions, error) {
	var versions helperVersions
	data, err := os.ReadFile(path)
	if err != nil {
		return versions, err
	}
	if err = json.Unmarshal(data, &versions); err != nil {
		return versions, err
	}
	if versions.Helper <= 0 || versions.Core <= 0 || versions.Installer <= 0 {
		return versions, errors.New("特权组件版本清单无效")
	}
	return versions, nil
}

func inspectHelperInstallation(cfg helperPaths, bundled helperVersions) (helperStatus, error) {
	result := helperStatus{Changes: []helperChange{}}
	if cfg.Socket == "" || cfg.ComponentVersions == "" || cfg.HelperExecutable == "" || cfg.CoreExecutable == "" || cfg.LaunchDaemon == "" {
		return result, errors.New("缺少特权组件路径配置")
	}
	// Installation state survives launchd restarts while the socket is recreated.
	result.Installed = true
	installed, manifestErr := readHelperVersions(cfg.ComponentVersions)
	components := []struct {
		name, path       string
		current, desired int
		executable       bool
	}{
		{"特权助手", cfg.HelperExecutable, installed.Helper, bundled.Helper, true},
		{"核心", cfg.CoreExecutable, installed.Core, bundled.Core, true},
		{"TUN 安装组件", cfg.LaunchDaemon, installed.Installer, bundled.Installer, false},
	}
	for _, component := range components {
		file, err := os.Open(component.path)
		reason := ""
		if err != nil {
			result.Installed = false
			if errors.Is(err, os.ErrNotExist) {
				reason = "missing"
			} else {
				reason = "unreadable"
			}
		} else {
			info, statErr := file.Stat()
			_ = file.Close()
			if statErr != nil || !info.Mode().IsRegular() || component.executable && info.Mode().Perm()&0o111 == 0 {
				reason = "unreadable"
			} else if manifestErr != nil {
				reason = "unreadable"
			} else if component.current != component.desired {
				reason = "version"
			}
		}
		if reason != "" {
			result.Changes = append(result.Changes, helperChange{Name: component.name, Reason: reason})
		}
	}
	result.NeedsUpdate = !result.Installed || len(result.Changes) > 0
	return result, nil
}
