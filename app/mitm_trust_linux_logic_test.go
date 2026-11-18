package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCall struct {
	name string
	args []string
}

type fakeRunner struct {
	mu      sync.Mutex
	calls   []fakeCall
	handler func(call fakeCall) (exit int, err error)
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	call := fakeCall{name: name, args: append([]string(nil), args...)}
	f.calls = append(f.calls, call)
	handler := f.handler
	f.mu.Unlock()
	if handler == nil {
		return nil, nil, 0, nil
	}
	exit, err := handler(call)
	return nil, []byte("secret-output-must-not-leak"), exit, err
}

func (f *fakeRunner) names() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.name)
	}
	return out
}

func linuxTestCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Kiko Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

type linuxTestEnv struct {
	store   *linuxTrustStore
	runner  *fakeRunner
	files   map[string][]byte // virtual system files (target paths)
	dirs    map[string]bool
	logs    []string
	home    string
	trusted bool
}

func newLinuxTestEnv(t *testing.T, tools []string, euid int, env map[string]string) *linuxTestEnv {
	t.Helper()
	e := &linuxTestEnv{runner: &fakeRunner{}, files: map[string][]byte{}, dirs: map[string]bool{}, home: t.TempDir()}
	have := map[string]bool{}
	for _, tool := range tools {
		have[tool] = true
	}
	e.store = &linuxTrustStore{
		runner: e.runner,
		lookPath: func(name string) (string, error) {
			if have[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		euid:   func() int { return euid },
		getenv: func(k string) string { return env[k] },
		home:   func() string { return e.home },
		exists: func(path string) bool {
			if _, ok := e.files[path]; ok || e.dirs[path] {
				return true
			}
			_, err := os.Stat(path)
			return err == nil
		},
		glob: func(pattern string) []string {
			matches, _ := filepath.Glob(pattern)
			return matches
		},
		trusted: func(*x509.Certificate) bool { return e.trusted },
		readFile: func(path string) ([]byte, error) {
			if data, ok := e.files[path]; ok {
				return data, nil
			}
			return os.ReadFile(path)
		},
		logf: func(format string, args ...any) { e.logs = append(e.logs, format) },
	}
	return e
}

var linuxGUI = map[string]string{"DISPLAY": ":0"}

func pemOf(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

func TestLinuxTrustFamilySelection(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
		dirs  []string
		want  linuxTrustFamily
	}{
		{"debian", []string{"update-ca-certificates"}, nil, linuxFamilyDebian},
		{"suse", []string{"update-ca-certificates"}, []string{"/etc/pki/trust/anchors"}, linuxFamilySUSE},
		{"rhel", []string{"update-ca-trust", "trust"}, nil, linuxFamilyRHEL},
		{"arch", []string{"update-ca-trust", "trust"}, []string{"/etc/ca-certificates/trust-source"}, linuxFamilyArch},
		{"none", []string{"pkexec"}, nil, linuxFamilyNone},
	}
	cert := linuxTestCert(t)
	for _, c := range cases {
		e := newLinuxTestEnv(t, c.tools, 1000, linuxGUI)
		for _, d := range c.dirs {
			e.dirs[d] = true
		}
		if got := e.store.family(); got != c.want {
			t.Fatalf("%s: family = %v want %v", c.name, got, c.want)
		}
		target := e.store.target(c.want, cert)
		fp8 := linuxCertFingerprint(cert)[:8]
		switch c.want {
		case linuxFamilyDebian:
			if target != "/usr/local/share/ca-certificates/kiko-root-ca-"+fp8+".crt" {
				t.Fatalf("debian target %s", target)
			}
		case linuxFamilySUSE:
			if target != "/etc/pki/trust/anchors/kiko-root-ca-"+fp8+".crt" {
				t.Fatalf("suse target %s", target)
			}
		case linuxFamilyRHEL:
			if target != "/etc/pki/ca-trust/source/anchors/kiko-root-ca-"+fp8+".crt" {
				t.Fatalf("rhel target %s", target)
			}
		case linuxFamilyArch, linuxFamilyNone:
			if target != "" {
				t.Fatalf("target %s", target)
			}
		}
	}
}

func TestLinuxTrustSupported(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
		euid  int
		env   map[string]string
		want  bool
	}{
		{"desktop", []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI, true},
		{"wayland", []string{"update-ca-certificates", "pkexec"}, 1000, map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"headless user", []string{"update-ca-certificates", "pkexec"}, 1000, nil, false},
		{"no pkexec", []string{"update-ca-certificates"}, 1000, linuxGUI, false},
		{"headless root", []string{"update-ca-certificates"}, 0, nil, true},
		{"root without tool", nil, 0, nil, false},
	}
	for _, c := range cases {
		e := newLinuxTestEnv(t, c.tools, c.euid, c.env)
		if got := e.store.Supported(); got != c.want {
			t.Fatalf("%s: supported = %v", c.name, got)
		}
	}
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, nil)
	if err := e.store.Install(linuxTestCert(t)); !errors.Is(err, errTrustUnsupported) {
		t.Fatalf("install headless: %v", err)
	}
	if len(e.runner.calls) != 0 {
		t.Fatalf("calls: %v", e.runner.calls)
	}
}

// TestLinuxTrustInstallArgvShape checks that the pkexec command line is the
// fixed script plus positional arguments and that the certificate path never
// appears in the script text.
func TestLinuxTrustInstallArgvShape(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI)
	dest := e.store.target(linuxFamilyDebian, cert)
	var tmpPath string
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.name == "pkexec" {
			tmpPath = call.args[4]
			// Simulate the privileged copy: the target file gets the temp file's content.
			data, err := os.ReadFile(tmpPath)
			if err != nil {
				t.Errorf("tmp read: %v", err)
			}
			e.files[dest] = data
			info, _ := os.Stat(filepath.Dir(tmpPath))
			if info.Mode().Perm() != 0o700 {
				t.Errorf("tmp dir perm %v", info.Mode().Perm())
			}
		}
		return 0, nil
	}
	if err := e.store.Install(cert); err != nil {
		t.Fatal(err)
	}
	call := e.runner.calls[0]
	if call.name != "pkexec" || len(call.args) != 7 || call.args[0] != "/bin/sh" || call.args[1] != "-c" || call.args[3] != "sh" {
		t.Fatalf("argv = %v", call)
	}
	if call.args[2] != linuxScriptDebianInstall {
		t.Fatalf("script = %q", call.args[2])
	}
	if strings.Contains(call.args[2], tmpPath) || strings.Contains(call.args[2], dest) {
		t.Fatal("script interpolates paths")
	}
	if !filepath.IsAbs(call.args[4]) || call.args[5] != dest {
		t.Fatalf("positional = %v", call.args[4:])
	}
	if _, err := os.Stat(filepath.Dir(tmpPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp dir not cleaned: %v", err)
	}
}

func TestLinuxTrustRootRunsDirectly(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-trust"}, 0, nil)
	dest := e.store.target(linuxFamilyRHEL, cert)
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.name == "/bin/sh" {
			e.files[dest], _ = os.ReadFile(call.args[3])
		}
		return 0, nil
	}
	if err := e.store.Install(cert); err != nil {
		t.Fatal(err)
	}
	call := e.runner.calls[0]
	if call.name != "/bin/sh" || call.args[1] != linuxScriptRHELInstall || call.args[2] != "sh" {
		t.Fatalf("call = %v", call)
	}
}

func TestLinuxTrustCancelMapping(t *testing.T) {
	cert := linuxTestCert(t)
	for _, exit := range []int{126, 127} {
		e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec", "certutil"}, 1000, linuxGUI)
		e.runner.handler = func(call fakeCall) (int, error) { return exit, nil }
		err := e.store.Install(cert)
		if !errors.Is(err, errTrustCanceled) {
			t.Fatalf("exit %d: %v", exit, err)
		}
		if len(e.runner.calls) != 1 {
			t.Fatalf("NSS touched after cancel: %v", e.runner.names())
		}
	}
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI)
	e.runner.handler = func(call fakeCall) (int, error) { return 1, nil }
	err := e.store.Install(cert)
	if err == nil || errors.Is(err, errTrustCanceled) || strings.Contains(err.Error(), "secret-output") {
		t.Fatalf("err = %v", err)
	}
	if len(e.logs) == 0 {
		t.Fatal("no log line")
	}
}

func TestLinuxTrustDestinationMismatchRollsBack(t *testing.T) {
	cert := linuxTestCert(t)
	other := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI)
	dest := e.store.target(linuxFamilyDebian, cert)
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.args[2] == linuxScriptDebianInstall {
			e.files[dest] = pemOf(other)
		}
		return 0, nil
	}
	if err := e.store.Install(cert); err == nil {
		t.Fatal("expected mismatch error")
	}
	if len(e.runner.calls) != 2 || e.runner.calls[1].args[2] != linuxScriptDebianRemove {
		t.Fatalf("calls = %v", e.runner.calls)
	}
}

func TestLinuxTrustNSSBestEffort(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec", "certutil"}, 1000, linuxGUI)
	nick := "Kiko Root CA " + linuxCertFingerprint(cert)[:8]
	mk := func(rel string) string {
		dir := filepath.Join(e.home, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	shared := mk(".pki/nssdb")
	ff := mk(".mozilla/firefox/abc.default")
	snap := mk("snap/firefox/common/.mozilla/firefox/def.default")
	flat := mk(".var/app/org.mozilla.firefox/.mozilla/firefox/ghi.default-release")
	xdg := mk(".config/mozilla/firefox/xyz.default")
	dbs := e.store.nssDatabases()
	if len(dbs) != 5 {
		t.Fatalf("dbs = %v", dbs)
	}
	e.files[e.store.target(linuxFamilyDebian, cert)] = pemOf(cert)
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.name == "certutil" && call.args[0] == "-A" && call.args[len(call.args)-1] == "sql:"+ff {
			return 255, nil // one database failing does not affect the others or the result
		}
		if call.name == "certutil" && call.args[0] == "-L" {
			return 255, nil // not installed yet
		}
		return 0, nil
	}
	if err := e.store.Install(cert); err != nil {
		t.Fatalf("install: %v", err)
	}
	var added []string
	for _, c := range e.runner.calls {
		if c.name == "certutil" && c.args[0] == "-A" {
			added = append(added, c.args[len(c.args)-1])
			want := []string{"-A", "-n", nick, "-t", "C,,", "-i"}
			for i, w := range want {
				if c.args[i] != w {
					t.Fatalf("certutil args = %v", c.args)
				}
			}
			if !filepath.IsAbs(c.args[6]) {
				t.Fatalf("relative -i path: %v", c.args)
			}
		}
	}
	if len(added) != 5 {
		t.Fatalf("added = %v", added)
	}
	_ = shared
	_ = snap
	_ = flat
	_ = xdg
	if len(e.logs) != 1 {
		t.Fatalf("logs = %v", e.logs)
	}
}

func TestLinuxTrustNSSSkippedWithoutCertutil(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI)
	if err := os.MkdirAll(filepath.Join(e.home, ".pki", "nssdb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.home, ".pki", "nssdb", "cert9.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.files[e.store.target(linuxFamilyDebian, cert)] = pemOf(cert)
	if err := e.store.Install(cert); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.runner.calls {
		if c.name == "certutil" {
			t.Fatal("certutil called")
		}
	}
}

func TestLinuxTrustRemoveIdempotent(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec", "certutil"}, 1000, linuxGUI)
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.name == "certutil" {
			return 255, nil // not in the database
		}
		return 0, nil
	}
	// Nothing installed: no authorization prompt, no deletion.
	if err := e.store.Remove(cert); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.runner.calls {
		if c.name == "pkexec" || (c.name == "certutil" && c.args[0] == "-D") {
			t.Fatalf("unexpected call %v", c)
		}
	}
	// Installed: run the removal script with the target path as a positional argument.
	dest := e.store.target(linuxFamilyDebian, cert)
	e.files[dest] = pemOf(cert)
	e.runner.calls = nil
	if err := e.store.Remove(cert); err != nil {
		t.Fatal(err)
	}
	call := e.runner.calls[0]
	if call.name != "pkexec" || call.args[2] != linuxScriptDebianRemove || call.args[5] != dest {
		t.Fatalf("call = %v", call)
	}
	// A system store trusted by other means (e.g. Arch) also triggers removal.
	delete(e.files, dest)
	e.trusted = true
	e.runner.calls = nil
	if err := e.store.Remove(cert); err != nil || len(e.runner.calls) == 0 || e.runner.calls[0].name != "pkexec" {
		t.Fatalf("trusted remove: %v %v", err, e.runner.calls)
	}
}

// TestLinuxTrustRemoveLegacyInstall checks that poppy-root-ca-*.crt files
// installed before the rename and the Poppy nickname in browser certificate
// databases are deleted when trust is removed.
func TestLinuxTrustRemoveLegacyInstall(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec", "certutil"}, 1000, linuxGUI)
	dir := filepath.Join(e.home, ".pki", "nssdb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := e.store.legacyTarget(linuxFamilyDebian, cert)
	if legacy != "/usr/local/share/ca-certificates/poppy-root-ca-"+linuxCertFingerprint(cert)[:8]+".crt" {
		t.Fatalf("legacy target %s", legacy)
	}
	e.files[legacy] = pemOf(cert)
	e.trusted = true
	legacyNick := "Poppy Root CA " + linuxCertFingerprint(cert)[:8]
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.name == "certutil" && call.args[0] == "-L" && call.args[2] != legacyNick {
			return 255, nil // the database has only the old nickname
		}
		return 0, nil
	}
	if err := e.store.Remove(cert); err != nil {
		t.Fatal(err)
	}
	var removed []string
	var nssDeleted []string
	for _, c := range e.runner.calls {
		if c.name == "pkexec" {
			removed = append(removed, c.args[5])
		}
		if c.name == "certutil" && c.args[0] == "-D" {
			nssDeleted = append(nssDeleted, c.args[2])
		}
	}
	if len(removed) != 1 || removed[0] != legacy {
		t.Fatalf("removed %v", removed)
	}
	if len(nssDeleted) != 1 || nssDeleted[0] != legacyNick {
		t.Fatalf("nss deleted %v", nssDeleted)
	}
	// When both old and new files exist, remove both.
	dest := e.store.target(linuxFamilyDebian, cert)
	e.files[dest] = pemOf(cert)
	e.runner.calls = nil
	if err := e.store.Remove(cert); err != nil {
		t.Fatal(err)
	}
	removed = nil
	for _, c := range e.runner.calls {
		if c.name == "pkexec" {
			removed = append(removed, c.args[5])
		}
	}
	if len(removed) != 2 || removed[0] != legacy || removed[1] != dest {
		t.Fatalf("removed %v", removed)
	}
}

func TestLinuxTrustRemoveDeletesNSSAndCancel(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec", "certutil"}, 1000, linuxGUI)
	dir := filepath.Join(e.home, ".pki", "nssdb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Remove(cert); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	for _, c := range e.runner.calls {
		if c.name == "certutil" && c.args[0] == "-D" && c.args[len(c.args)-1] == "sql:"+dir {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("calls = %v", e.runner.calls)
	}
	// Authorization canceled: return errTrustCanceled and leave NSS unchanged.
	e.files[e.store.target(linuxFamilyDebian, cert)] = pemOf(cert)
	e.runner.calls = nil
	e.runner.handler = func(call fakeCall) (int, error) {
		if call.name == "pkexec" {
			return 126, nil
		}
		return 0, nil
	}
	if err := e.store.Remove(cert); !errors.Is(err, errTrustCanceled) {
		t.Fatalf("err = %v", err)
	}
	if len(e.runner.calls) != 1 {
		t.Fatalf("calls = %v", e.runner.names())
	}
}

func TestLinuxTrustArchUsesTrustAnchor(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"trust", "update-ca-trust", "pkexec"}, 1000, linuxGUI)
	e.dirs["/etc/ca-certificates/trust-source"] = true
	if err := e.store.Install(cert); err != nil {
		t.Fatal(err)
	}
	call := e.runner.calls[0]
	if call.args[2] != linuxScriptArchInstall || len(call.args) != 7 || call.args[5] != "" {
		t.Fatalf("call = %v", call)
	}
}

func TestLinuxTrustTempFileMismatchRejected(t *testing.T) {
	cert := linuxTestCert(t)
	e := newLinuxTestEnv(t, []string{"update-ca-certificates", "pkexec"}, 1000, linuxGUI)
	e.store.readFile = func(string) ([]byte, error) { return pemOf(linuxTestCert(t)), nil }
	if err := e.store.Install(cert); err == nil {
		t.Fatal("expected verify failure")
	}
	if len(e.runner.calls) != 0 {
		t.Fatalf("calls = %v", e.runner.calls)
	}
}

func TestMITMCertBundlePathsCoverDistros(t *testing.T) {
	want := []string{
		"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu, Arch
		"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL
		"/etc/ssl/ca-bundle.pem",                            // openSUSE
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // Fedora, RHEL extracted bundle
	}
	for _, path := range want {
		found := false
		for _, have := range mitmCertFiles {
			found = found || have == path
		}
		if !found {
			t.Fatalf("missing %s", path)
		}
	}
}
