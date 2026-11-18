//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
)

func TestReloadSignalsOnlyManagedProcess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "signals")
	cmd := exec.Command("/bin/sh", "-c", `trap 'printf h >> "$1"' HUP; printf r > "$1"; while :; do sleep 0.02; done`, "fixture", marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	processMu.Lock()
	previous := managed
	managed = cmd
	processMu.Unlock()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		processMu.Lock()
		managed = previous
		processMu.Unlock()
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data, _ := os.ReadFile(marker); string(data) == "r" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, pid := range []int{0, 1, os.Getpid(), cmd.Process.Pid + 1} {
		if res := reload(request{PID: pid}); res.Error == "" {
			t.Fatalf("accepted unmanaged PID %d", pid)
		}
	}
	if res := reload(request{PID: cmd.Process.Pid}); res.Error != "" || res.PID != cmd.Process.Pid {
		t.Fatalf("reload=%+v", res)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if data, _ := os.ReadFile(marker); string(data) == "rh" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("managed process did not receive HUP")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStatusReportsProtocolVersion(t *testing.T) {
	if got := status().ProtocolVersion; got != helperpaths.ProtocolVersion {
		t.Fatalf("protocol version: got %d, want %d", got, helperpaths.ProtocolVersion)
	}
}

func TestTailscaleUDPEnvironment(t *testing.T) {
	current := []string{"PATH=/bin", "TS_DEBUG_NEVER_DIRECT_UDP=true", "TS_DEBUG_ALWAYS_USE_DERP=false"}
	if got, want := tailscaleUDPEnvironment(current, true), []string{"PATH=/bin", "TS_DEBUG_ALWAYS_USE_DERP=true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled: got %#v, want %#v", got, want)
	}
	if got, want := tailscaleUDPEnvironment(current, false), []string{"PATH=/bin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled: got %#v, want %#v", got, want)
	}
}

func TestValidatePathsAcceptsDesktopDataDirectory(t *testing.T) {
	root := "/Users/testuser/Library/Application Support/Kiko/kiko-server"
	if !validDataPaths(root+"/runtime/config.json", root) {
		t.Fatal("expected desktop data path to pass")
	}
}

func TestValidatePathsRejectsOtherDirectories(t *testing.T) {
	if validDataPaths("/tmp/config.json", "/tmp") {
		t.Fatal("expected path validation error")
	}
}

func TestTaildropDirectoryInDataDir(t *testing.T) {
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.json")
	config := fmt.Sprintf(`{"endpoints":[{"type":"tailscale","tag":"default"},{"type":"tailscale","tag":"relative","taildrop_directory":"files/inbox"},{"type":"tailscale","tag":"outside","taildrop_directory":%q}]}`, filepath.Join(t.TempDir(), "other"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ tag, want string }{
		{"default", filepath.Join(dataDir, "Taildrop")},
		{"relative", filepath.Join(dataDir, "files", "inbox")},
	} {
		got, err := taildropDirectoryInDataDir(configPath, dataDir, test.tag)
		if err != nil || got != test.want {
			t.Fatalf("tag=%s got=%q err=%v", test.tag, got, err)
		}
	}
	if _, err := taildropDirectoryInDataDir(configPath, dataDir, "outside"); err == nil {
		t.Fatal("outside directory accepted")
	}
	if _, err := taildropDirectoryInDataDir(configPath, dataDir, "missing"); err == nil {
		t.Fatal("missing endpoint accepted")
	}
}

func TestOpenDirectoryNoFollowRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if directory, err := openDirectoryNoFollow(root); err != nil {
		t.Fatal(err)
	} else {
		_ = directory.Close()
	}
	if directory, err := openDirectoryNoFollow(filepath.Join(root, "linked")); err == nil {
		_ = directory.Close()
		t.Fatal("symlink directory accepted")
	}
}

func TestReapedCoreExitDetails(t *testing.T) {
	for _, scenario := range []string{"exit-code", "signal"} {
		t.Run(scenario, func(t *testing.T) {
			processMu.Lock()
			previous, previousExit := managed, lastExit
			processMu.Unlock()
			defer func() { processMu.Lock(); managed, lastExit = previous, previousExit; processMu.Unlock() }()
			script := "exit 7"
			if scenario == "signal" {
				script = "exec sleep 60"
			}
			cmd := exec.Command("/bin/sh", "-c", script)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			logFile, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0600)
			if err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal(err)
			}
			processMu.Lock()
			managed = cmd
			processMu.Unlock()
			done := make(chan struct{})
			go func() { waitManaged(cmd, logFile); close(done) }()
			if scenario == "signal" {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatal("exit not reaped")
			}
			res := status()
			if res.PID != 0 || res.ExitedPID != cmd.Process.Pid {
				t.Fatalf("exit ownership=%+v", res)
			}
			if scenario == "exit-code" && res.ExitCode != 7 || scenario == "signal" && res.Signal == "" {
				t.Fatalf("exit detail=%+v", res)
			}
		})
	}
}
