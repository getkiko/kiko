package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLinuxScriptsVerifyBeforeUpdate checks that each script verifies the hash
// after copying and before the update command.
func TestLinuxScriptsVerifyBeforeUpdate(t *testing.T) {
	cases := map[string][2]string{
		"debian": {linuxScriptDebianInstall, "update-ca-certificates"},
		"rhel":   {linuxScriptRHELInstall, "update-ca-trust extract"},
		"arch":   {linuxScriptArchInstall, "trust anchor --store"},
	}
	for name, tc := range cases {
		script, update := tc[0], tc[1]
		verify := strings.Index(script, "exit 91")
		last := strings.LastIndex(script, update)
		if verify < 0 || last < 0 || verify > last {
			t.Fatalf("%s: verify does not precede update", name)
		}
		if !strings.Contains(script, "exit 90") {
			t.Fatalf("%s: no tool check", name)
		}
	}
}

func TestLinuxInstallPassesPEMHash(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI)
	dest := e.store.target(linuxFamilyDebian, cert)
	e.runner.handler = func(call fakeCall) (int, error) {
		e.files[dest] = pemOf(cert)
		return 0, nil
	}
	if err := e.store.Install(cert); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pemOf(cert))
	call := e.runner.calls[0]
	if call.args[6] != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash arg = %q", call.args[6])
	}
}

func TestLinuxPrivilegedExitMapping(t *testing.T) {
	cert := linuxTestCert(t)
	cases := []struct {
		name     string
		euid     int
		exit     int
		canceled bool
		logPart  string
	}{
		{"not authorized", 1000, 126, true, ""},
		{"dismissed", 1000, 127, true, ""},
		{"tool missing", 1000, 90, false, "tool-missing"},
		{"hash mismatch", 1000, 91, false, "hash-mismatch"},
		{"generic", 1000, 1, false, "exit=1"},
		{"root 127 is not a cancel", 0, 127, false, "exit=127"},
		{"root tool missing", 0, 90, false, "tool-missing"},
	}
	for _, tc := range cases {
		e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, tc.euid, linuxGUI)
		var logs []string
		e.store.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
		e.runner.handler = func(call fakeCall) (int, error) { return tc.exit, nil }
		err := e.store.Install(cert)
		if err == nil || errors.Is(err, errTrustCanceled) != tc.canceled {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if len(e.runner.calls) != 1 {
			t.Fatalf("%s: calls = %v", tc.name, e.runner.names())
		}
		joined := strings.Join(logs, "\n")
		if !strings.Contains(joined, "step=system-install") || !strings.Contains(joined, tc.logPart) {
			t.Fatalf("%s: logs = %q", tc.name, joined)
		}
	}
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestLinuxInstallScriptSimulation runs the fixed script with fake commands:
// on a hash mismatch the update command must not run and the target copy is deleted.
func TestLinuxInstallScriptSimulation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	for name, script := range map[string]string{"debian": linuxScriptDebianInstall, "rhel": linuxScriptRHELInstall} {
		update := "update-ca-certificates"
		if name == "rhel" {
			update = "update-ca-trust"
		}
		stubs := t.TempDir()
		work := t.TempDir()
		writeStub(t, stubs, "install", `shift 3; mkdir -p "$(dirname "$2")"; cp "$1" "$2"`)
		writeStub(t, stubs, update, `echo x >> "$STUB_MARK"; [ -z "$STUB_FAIL" ]`)
		writeStub(t, stubs, "sha256sum", `if [ -x /usr/bin/sha256sum ]; then exec /usr/bin/sha256sum "$@"; fi; exec shasum -a 256 "$@"`)
		src := filepath.Join(work, "src.crt")
		data := []byte("certificate-bytes")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		good := hex.EncodeToString(sum[:])
		failUpdate := ""
		run := func(hash, path string) (int, error) {
			dest := filepath.Join(work, "dest.crt")
			mark := filepath.Join(work, "updated")
			_ = os.Remove(mark)
			_ = os.Remove(dest)
			cmd := exec.Command("/bin/sh", "-c", script, "sh", src, dest, hash)
			cmd.Env = []string{"PATH=" + path, "STUB_MARK=" + mark, "STUB_FAIL=" + failUpdate}
			err := cmd.Run()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode(), nil
			}
			return 0, err
		}
		path := stubs + ":/usr/bin:/bin"
		work2 := func(file string) bool { _, err := os.Stat(filepath.Join(work, file)); return err == nil }

		if code, err := run(good, path); err != nil || code != 0 || !work2("updated") || !work2("dest.crt") {
			t.Fatalf("%s good: code %d err %v", name, code, err)
		}
		if code, err := run(strings.Repeat("0", 64), path); err != nil || code != linuxExitHashMismatch || work2("updated") || work2("dest.crt") {
			t.Fatalf("%s mismatch: code %d err %v updated=%v dest=%v", name, code, err, work2("updated"), work2("dest.crt"))
		}
		// The update command fails: roll back the copied anchor and run the
		// update command once more.
		failUpdate = "1"
		code, err := run(good, path)
		marks, _ := os.ReadFile(filepath.Join(work, "updated"))
		failUpdate = ""
		if err != nil || code == 0 || work2("dest.crt") || strings.Count(string(marks), "x") != 2 {
			t.Fatalf("%s update failure: code %d err %v dest=%v marks=%q", name, code, err, work2("dest.crt"), marks)
		}
		empty := t.TempDir()
		if code, err := run(good, empty); err != nil || code != linuxExitToolMissing || work2("updated") {
			t.Fatalf("%s missing tool: code %d err %v", name, code, err)
		}
	}
}
