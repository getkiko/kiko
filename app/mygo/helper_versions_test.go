package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func helperInstallationFixture(t *testing.T) (helperPaths, helperVersions) {
	t.Helper()
	dir := t.TempDir()
	cfg := helperPaths{
		Socket: filepath.Join(dir, "helper.sock"), HelperExecutable: filepath.Join(dir, "helper"),
		LaunchDaemon:   filepath.Join(dir, "helper.plist"),
		CoreExecutable: filepath.Join(dir, "kiko-core"), ComponentVersions: filepath.Join(dir, "versions.json"),
	}
	for _, path := range []string{cfg.Socket, cfg.HelperExecutable, cfg.CoreExecutable, cfg.LaunchDaemon} {
		if err := os.WriteFile(path, []byte("installed executable"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	versions := helperVersions{Helper: 1, Core: 2, Installer: 3}
	writeInstalledVersions(t, cfg, versions)
	return cfg, versions
}

func writeInstalledVersions(t *testing.T, cfg helperPaths, versions helperVersions) {
	t.Helper()
	data, err := json.Marshal(versions)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.ComponentVersions, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHelperMatchingRevisionsSurviveRebuildAndRepeatedLaunch(t *testing.T) {
	cfg, versions := helperInstallationFixture(t)
	// Different signing/build bytes and installation times share the same
	// component revisions, as happens when another DMG is installed.
	for _, path := range []string{cfg.HelperExecutable, cfg.CoreExecutable} {
		if err := os.WriteFile(path, []byte("rebuilt/re-signed executable with different metadata"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		status, err := inspectHelperInstallation(cfg, versions)
		if err != nil || !status.Installed || status.NeedsUpdate || len(status.Changes) != 0 {
			t.Fatalf("same revisions triggered update: %+v %v", status, err)
		}
	}
}

func TestHelperComponentRevisionChangesTriggerOneUpdate(t *testing.T) {
	for _, name := range []string{"helper", "core", "installer"} {
		t.Run(name, func(t *testing.T) {
			cfg, desired := helperInstallationFixture(t)
			componentName := ""
			switch name {
			case "helper":
				desired.Helper++
				componentName = "特权助手"
			case "core":
				desired.Core++
				componentName = "核心"
			case "installer":
				desired.Installer++
				componentName = "TUN 安装组件"
			}
			status, err := inspectHelperInstallation(cfg, desired)
			if err != nil || !status.NeedsUpdate || !reflect.DeepEqual(status.Changes, []helperChange{{Name: componentName, Reason: "version"}}) {
				t.Fatalf("revision update: %+v %v", status, err)
			}
			writeInstalledVersions(t, cfg, desired)
			status, err = inspectHelperInstallation(cfg, desired)
			if err != nil || status.NeedsUpdate {
				t.Fatalf("installed revision kept updating: %+v %v", status, err)
			}
		})
	}
}

func TestHelperMissingAndDamagedInstallationsNeedRepair(t *testing.T) {
	for _, scenario := range []string{"helper", "core", "launchd", "receipt", "invalid-receipt", "incomplete-receipt", "non-executable", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, desired := helperInstallationFixture(t)
			var err error
			switch scenario {
			case "launchd":
				err = os.Remove(cfg.LaunchDaemon)
			case "helper":
				err = os.Remove(cfg.HelperExecutable)
			case "core":
				err = os.Remove(cfg.CoreExecutable)
			case "receipt":
				err = os.Remove(cfg.ComponentVersions)
			case "invalid-receipt":
				err = os.WriteFile(cfg.ComponentVersions, []byte("invalid"), 0o644)
			case "incomplete-receipt":
				err = os.WriteFile(cfg.ComponentVersions, []byte(`{"helper":1}`), 0o644)
			case "non-executable":
				err = os.Chmod(cfg.HelperExecutable, 0o644)
			case "directory":
				if err = os.Remove(cfg.HelperExecutable); err == nil {
					err = os.Mkdir(cfg.HelperExecutable, 0o755)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			status, err := inspectHelperInstallation(cfg, desired)
			if err != nil || !status.NeedsUpdate {
				t.Fatalf("damaged installation accepted: %+v %v", status, err)
			}
		})
	}
}

func TestHelperSocketRestartKeepsMatchingInstallation(t *testing.T) {
	cfg, versions := helperInstallationFixture(t)
	if err := os.Remove(cfg.Socket); err != nil {
		t.Fatal(err)
	}
	status, err := inspectHelperInstallation(cfg, versions)
	if err != nil || !status.Installed || status.NeedsUpdate {
		t.Fatalf("launchd restart triggered installation: %+v %v", status, err)
	}
}

// fakeCore writes an executable at path printing version as kiko-core does.
func fakeCore(t *testing.T, path, version string) {
	t.Helper()
	script := "#!/bin/sh\n[ \"$1\" = version ] && printf 'sing-box version " + version + "\\nEnvironment: go\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A core whose revision was not bumped still updates when the installed one
// reports another version; a rebuilt core of the same version does not.
func TestHelperCoreVersionCatchesUnbumpedRevision(t *testing.T) {
	cfg, versions := helperInstallationFixture(t)
	bundled := filepath.Join(t.TempDir(), "kiko-core")
	fakeCore(t, bundled, "1.14.4-f17ce7ed")
	fakeCore(t, cfg.CoreExecutable, "1.14.4-d0991c76")
	status, err := inspectHelperInstallation(cfg, versions)
	if err != nil || status.NeedsUpdate {
		t.Fatalf("revisions agree: %+v %v", status, err)
	}
	status = withCoreVersion(status, bundled, cfg.CoreExecutable)
	if !status.NeedsUpdate || !reflect.DeepEqual(status.Changes, []helperChange{{Name: coreComponent, Reason: "version"}}) {
		t.Fatalf("another core version: %+v", status)
	}

	// The same version, rebuilt with other bytes, asks for nothing.
	fakeCore(t, cfg.CoreExecutable, "1.14.4-f17ce7ed")
	if err := os.WriteFile(cfg.CoreExecutable, append(mustRead(t, cfg.CoreExecutable), []byte("# re-signed\n")...), 0o755); err != nil {
		t.Fatal(err)
	}
	status, _ = inspectHelperInstallation(cfg, versions)
	if status = withCoreVersion(status, bundled, cfg.CoreExecutable); status.NeedsUpdate {
		t.Fatalf("same version: %+v", status)
	}

	// A core that cannot run (the fixture's placeholders) leaves the status alone.
	if err := os.WriteFile(cfg.CoreExecutable, []byte("not a program"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, _ = inspectHelperInstallation(cfg, versions)
	if status = withCoreVersion(status, bundled, cfg.CoreExecutable); status.NeedsUpdate {
		t.Fatalf("unreadable core: %+v", status)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
