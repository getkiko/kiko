package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type usbipModuleState struct {
	Loaded    bool `json:"loaded"`
	Available bool `json:"available"`
}

type usbipKernelState struct {
	USBIPHost usbipModuleState `json:"usbipHost"`
	VHCIHcd   usbipModuleState `json:"vhciHcd"`
}

// These variables are overridden by tests.
var (
	kernelSysModuleDir = "/sys/module"
	kernelProcModules  = "/proc/modules"
	kernelModinfoDirs  = []string{"/usr/local/sbin", "/usr/sbin", "/sbin", "/usr/local/bin", "/usr/bin", "/bin"}
	kernelLookPath     = exec.LookPath
	kernelRunModinfo   = func(path, module string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, path, module).Run()
	}
)

// kernelModuleLoaded reports whether a kernel module is loaded; built-in modules
// only show up in /sys/module.
func kernelModuleLoaded(name string) bool {
	name = strings.ReplaceAll(name, "-", "_")
	if _, err := os.Stat(filepath.Join(kernelSysModuleDir, name)); err == nil {
		return true
	}
	file, err := os.Open(kernelProcModules)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

func findModinfo() string {
	if path, err := kernelLookPath("modinfo"); err == nil {
		return path
	}
	for _, dir := range kernelModinfoDirs {
		candidate := filepath.Join(dir, "modinfo")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func kernelModuleState(name, modinfo string) usbipModuleState {
	if kernelModuleLoaded(name) {
		return usbipModuleState{Loaded: true, Available: true}
	}
	return usbipModuleState{Available: modinfo != "" && kernelRunModinfo(modinfo, name) == nil}
}

func detectKernelModules() usbipKernelState {
	modinfo := findModinfo()
	return usbipKernelState{
		USBIPHost: kernelModuleState("usbip-host", modinfo),
		VHCIHcd:   kernelModuleState("vhci-hcd", modinfo),
	}
}
