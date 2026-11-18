package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func withKernelPaths(t *testing.T, modules string) (sysDir string) {
	t.Helper()
	dir := t.TempDir()
	sysDir = filepath.Join(dir, "module")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	procPath := filepath.Join(dir, "modules")
	if err := os.WriteFile(procPath, []byte(modules), 0o644); err != nil {
		t.Fatal(err)
	}
	oldSys, oldProc, oldLook, oldRun, oldDirs := kernelSysModuleDir, kernelProcModules, kernelLookPath, kernelRunModinfo, kernelModinfoDirs
	kernelSysModuleDir, kernelProcModules = sysDir, procPath
	kernelModinfoDirs = nil
	t.Cleanup(func() {
		kernelSysModuleDir, kernelProcModules, kernelLookPath, kernelRunModinfo, kernelModinfoDirs = oldSys, oldProc, oldLook, oldRun, oldDirs
	})
	return sysDir
}

func TestKernelModuleLoadedFromSysfsOrProcModules(t *testing.T) {
	sysDir := withKernelPaths(t, "vhci_hcd 57344 0 - Live 0x0000000000000000\nother 1 0 - Live 0x0\n")
	if err := os.Mkdir(filepath.Join(sysDir, "usbip_host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !kernelModuleLoaded("usbip-host") {
		t.Error("builtin/sysfs module should count as loaded")
	}
	if !kernelModuleLoaded("vhci-hcd") {
		t.Error("module listed in /proc/modules should count as loaded")
	}
	if kernelModuleLoaded("nonexistent") {
		t.Error("unknown module should not be loaded")
	}
}

func TestDetectKernelModulesUsesModinfoForUnloadedModules(t *testing.T) {
	withKernelPaths(t, "vhci_hcd 57344 0 - Live 0x0\n")
	kernelLookPath = func(string) (string, error) { return "/fake/modinfo", nil }
	kernelRunModinfo = func(_, module string) error {
		if module == "usbip-host" {
			return nil
		}
		return os.ErrNotExist
	}
	got := detectKernelModules()
	if want := (usbipKernelState{
		USBIPHost: usbipModuleState{Loaded: false, Available: true},
		VHCIHcd:   usbipModuleState{Loaded: true, Available: true},
	}); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestDetectKernelModulesWithoutModinfo(t *testing.T) {
	withKernelPaths(t, "")
	kernelLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	got := detectKernelModules()
	if got.USBIPHost.Available || got.VHCIHcd.Available || got.USBIPHost.Loaded {
		t.Fatalf("got %+v", got)
	}
}

func TestUSBIPPreflightResponse(t *testing.T) {
	oldGOOS, oldElevated, oldUser, oldDetector := usbipGOOS, isElevated, currentUsername, usbipKernelDetector
	t.Cleanup(func() {
		usbipGOOS, isElevated, currentUsername, usbipKernelDetector = oldGOOS, oldElevated, oldUser, oldDetector
	})
	isElevated = func() bool { return true }
	currentUsername = func() string { return "tester" }
	s := &server{sbVersion: "sing-box version 1.14.0\nTags: with_usbip,with_clash_api"}
	call := func() map[string]any {
		rec := httptest.NewRecorder()
		s.usbipPreflight(rec, httptest.NewRequest(http.MethodGet, "/api/v1/usbip/preflight", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	usbipGOOS = "darwin"
	usbipKernelDetector = func() *usbipKernelState { t.Fatal("kernel must not be probed off linux"); return nil }
	body := call()
	if body["serverSupported"] != false || body["singBoxUSBIP"] != true || body["elevated"] != true || body["user"] != "tester" {
		t.Fatalf("darwin body=%v", body)
	}
	if _, ok := body["kernel"]; ok {
		t.Fatalf("kernel should be omitted: %v", body)
	}

	usbipGOOS = "linux"
	usbipKernelDetector = func() *usbipKernelState {
		return &usbipKernelState{USBIPHost: usbipModuleState{Available: true}}
	}
	body = call()
	kernel, ok := body["kernel"].(map[string]any)
	if !ok || body["serverSupported"] != true {
		t.Fatalf("linux body=%v", body)
	}
	if host, _ := kernel["usbipHost"].(map[string]any); host["available"] != true || host["loaded"] != false {
		t.Fatalf("kernel=%v", kernel)
	}
}
