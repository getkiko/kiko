package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Linux trust logic (platform-independent so it can be tested on other systems
// with a fake runner).
//
// System trust store: a fixed script runs via pkexec (polkit). The script text
// contains no user data; the certificate path and target path are passed as
// positional arguments. When running as root (headless gateway) the same
// script runs directly. Browser NSS databases (~/.pki/nssdb for Chrome /
// Chromium / Edge, plus every Firefox profile) are handled best-effort with
// certutil; failures are only logged and do not affect the system result.
//
// Headless gateway: with no graphical session and not running as root,
// Supported() is false; the root certificate can still be generated and
// exported, and the LAN download page distributes it to clients.

type commandRunner interface {
	// Run runs a command and returns its stdout, stderr and exit code.
	// err is non-nil when the command cannot start or the context ends; it is
	// nil when the command ran but exited non-zero.
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return out.Bytes(), errOut.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return out.Bytes(), errOut.Bytes(), -1, err
	}
	return out.Bytes(), errOut.Bytes(), 0, nil
}

const (
	linuxPkexecTimeout   = 120 * time.Second // includes the authorization dialog
	linuxCertutilTimeout = 10 * time.Second
	linuxCertPrefix      = "kiko-root-ca-"
	// legacyLinuxCertPrefix and legacyLinuxCertNickname are the system
	// certificate file name prefix and browser certificate database nickname
	// installed before the rename to Kiko; removing trust recognizes them too.
	legacyLinuxCertPrefix   = "poppy-root-ca-"
	legacyLinuxCertNickname = "Poppy Root CA "
)

// linuxTrustFamily is a distribution family; it decides the install directory
// and the update command.
type linuxTrustFamily int

const (
	linuxFamilyNone   linuxTrustFamily = iota
	linuxFamilyDebian                  // update-ca-certificates, /usr/local/share/ca-certificates
	linuxFamilySUSE                    // update-ca-certificates, /etc/pki/trust/anchors
	linuxFamilyRHEL                    // update-ca-trust, /etc/pki/ca-trust/source/anchors
	linuxFamilyArch                    // trust anchor
)

// Fixed scripts: $1 is the temporary certificate file, $2 the target path, and
// $3 the SHA-256 of the certificate PEM file (lowercase hex). The script text
// never depends on input. Install first copies to a root-owned location, then
// verifies the copy's hash; on mismatch it deletes the copy and exits with
// linuxExitHashMismatch before running the update command. A missing required
// command exits with linuxExitToolMissing.
const (
	linuxExitToolMissing  = 90
	linuxExitHashMismatch = 91

	linuxShNeedHash = `command -v sha256sum >/dev/null 2>&1 || command -v openssl >/dev/null 2>&1 || exit 90; `
	linuxShVerifyFn = `H=$3; v() { if command -v sha256sum >/dev/null 2>&1; then h=$(sha256sum "$1" | cut -d' ' -f1); else h=$(openssl dgst -sha256 "$1" | sed 's/.*= *//'); fi; [ "$h" = "$H" ]; }; `
	// Arch's trust command reads the file directly: copy it into a private
	// root temporary directory first, then verify.
	linuxShArchCopy = `d=$(mktemp -d); trap 'rm -rf -- "$d"' EXIT; install -m 0600 "$1" "$d/root.crt"; v "$d/root.crt" || exit 91; `

	linuxScriptDebianInstall = `set -e; for c in install rm cut update-ca-certificates; do command -v "$c" >/dev/null 2>&1 || exit 90; done; ` + linuxShNeedHash + linuxShVerifyFn +
		`install -D -m 0644 "$1" "$2"; v "$2" || { rm -f -- "$2"; exit 91; }; update-ca-certificates || { r=$?; rm -f -- "$2"; update-ca-certificates >/dev/null 2>&1 || true; exit $r; }`
	linuxScriptDebianRemove = `set -e; for c in rm update-ca-certificates; do command -v "$c" >/dev/null 2>&1 || exit 90; done; rm -f -- "$2"; update-ca-certificates --fresh`
	linuxScriptRHELInstall  = `set -e; for c in install rm cut update-ca-trust; do command -v "$c" >/dev/null 2>&1 || exit 90; done; ` + linuxShNeedHash + linuxShVerifyFn +
		`install -D -m 0644 "$1" "$2"; v "$2" || { rm -f -- "$2"; exit 91; }; update-ca-trust extract || { r=$?; rm -f -- "$2"; update-ca-trust extract >/dev/null 2>&1 || true; exit $r; }`
	linuxScriptRHELRemove  = `set -e; for c in rm update-ca-trust; do command -v "$c" >/dev/null 2>&1 || exit 90; done; rm -f -- "$2"; update-ca-trust extract`
	linuxScriptArchInstall = `set -e; for c in trust mktemp install rm cut; do command -v "$c" >/dev/null 2>&1 || exit 90; done; ` + linuxShNeedHash + linuxShVerifyFn + linuxShArchCopy +
		`trust anchor --store "$d/root.crt"`
	// The exit code of trust anchor --remove when nothing is installed varies
	// by version, so removal is treated as idempotent.
	linuxScriptArchRemove = `set -e; for c in trust mktemp install rm cut; do command -v "$c" >/dev/null 2>&1 || exit 90; done; ` + linuxShNeedHash + linuxShVerifyFn + linuxShArchCopy +
		`trust anchor --remove "$d/root.crt" || true`
)

type linuxTrustStore struct {
	runner   commandRunner
	lookPath func(string) (string, error)
	euid     func() int
	getenv   func(string) string
	home     func() string
	exists   func(path string) bool // whether a directory or file exists
	glob     func(pattern string) []string
	// trusted reports whether the certificate is already in the system trust
	// store (to avoid needless authorization dialogs).
	trusted  func(*x509.Certificate) bool
	readFile func(string) ([]byte, error)
	logf     func(format string, args ...any)
}

func newLinuxTrustStoreDefaults() *linuxTrustStore {
	return &linuxTrustStore{
		runner:   execRunner{},
		lookPath: exec.LookPath,
		euid:     os.Geteuid,
		getenv:   os.Getenv,
		home: func() string {
			home, _ := os.UserHomeDir()
			return home
		},
		exists: func(path string) bool { _, err := os.Stat(path); return err == nil },
		glob: func(pattern string) []string {
			matches, _ := filepath.Glob(pattern)
			return matches
		},
		trusted:  linuxSystemTrusts,
		readFile: os.ReadFile,
		logf:     log.Printf,
	}
}

// linuxSystemTrusts verifies the certificate itself against the system bundle
// (a root is self-signed, so passing verification means it is already trusted).
func linuxSystemTrusts(cert *x509.Certificate) bool {
	roots := mitmSystemRoots()
	if roots == nil || cert == nil {
		return false
	}
	_, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil
}

// family detects the distribution family from the available tools.
func (s *linuxTrustStore) family() linuxTrustFamily {
	has := func(name string) bool { _, err := s.lookPath(name); return err == nil }
	switch {
	case has("trust") && s.exists("/etc/ca-certificates/trust-source"):
		return linuxFamilyArch
	case has("update-ca-trust"):
		return linuxFamilyRHEL
	case has("update-ca-certificates"):
		if s.exists("/etc/pki/trust/anchors") {
			return linuxFamilySUSE
		}
		return linuxFamilyDebian
	}
	return linuxFamilyNone
}

func (s *linuxTrustStore) graphical() bool {
	return s.getenv("DISPLAY") != "" || s.getenv("WAYLAND_DISPLAY") != ""
}

func (s *linuxTrustStore) Supported() bool {
	if s.family() == linuxFamilyNone {
		return false
	}
	if s.euid() == 0 {
		return true
	}
	_, err := s.lookPath("pkexec")
	return err == nil && s.graphical()
}

// linuxCertFingerprint returns the SHA-256 fingerprint in lowercase hex.
func linuxCertFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func (s *linuxTrustStore) nickname(cert *x509.Certificate) string {
	return "Kiko Root CA " + linuxCertFingerprint(cert)[:8]
}

func (s *linuxTrustStore) legacyNickname(cert *x509.Certificate) string {
	return legacyLinuxCertNickname + linuxCertFingerprint(cert)[:8]
}

// target returns the target file in the system store (Arch uses the trust
// command and has no target file).
func (s *linuxTrustStore) target(family linuxTrustFamily, cert *x509.Certificate) string {
	return linuxCertTarget(family, linuxCertPrefix+linuxCertFingerprint(cert)[:8]+".crt")
}

// legacyTarget returns the target file installed before the rename.
func (s *linuxTrustStore) legacyTarget(family linuxTrustFamily, cert *x509.Certificate) string {
	return linuxCertTarget(family, legacyLinuxCertPrefix+linuxCertFingerprint(cert)[:8]+".crt")
}

func linuxCertTarget(family linuxTrustFamily, name string) string {
	switch family {
	case linuxFamilyDebian:
		return "/usr/local/share/ca-certificates/" + name
	case linuxFamilySUSE:
		return "/etc/pki/trust/anchors/" + name
	case linuxFamilyRHEL:
		return "/etc/pki/ca-trust/source/anchors/" + name
	}
	return ""
}

func (s *linuxTrustStore) fail(step string, exit int) error {
	s.logf("HTTPS decryption: Linux trust step failed step=%s exit=%d", step, exit)
	return fmt.Errorf("linux trust %s failed", step)
}

// writeTemp writes the certificate to a user-private temporary directory and
// reads it back to verify; it returns the file path and a cleanup function.
func (s *linuxTrustStore) writeTemp(cert *x509.Certificate) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "kiko-trust-")
	if err != nil {
		return "", "", nil, s.fail("tempdir", 0)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", "", nil, s.fail("tempdir", 0)
	}
	path := filepath.Join(dir, "root.crt")
	if !filepath.IsAbs(path) {
		cleanup()
		return "", "", nil, s.fail("tempdir", 0)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cleanup()
		return "", "", nil, s.fail("tempwrite", 0)
	}
	if !s.sameCert(path, cert) {
		cleanup()
		return "", "", nil, s.fail("tempverify", 0)
	}
	sum := sha256.Sum256(data)
	return path, hex.EncodeToString(sum[:]), cleanup, nil
}

// sameCert re-reads the file and compares the DER.
func (s *linuxTrustStore) sameCert(path string, cert *x509.Certificate) bool {
	data, err := s.readFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	return block != nil && block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, cert.Raw)
}

// runPrivileged runs the fixed script via pkexec (or directly as root).
func (s *linuxTrustStore) runPrivileged(step, script string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), linuxPkexecTimeout)
	defer cancel()
	argv := append([]string{"-c", script, "sh"}, args...)
	name := "/bin/sh"
	viaPkexec := s.euid() != 0
	if viaPkexec {
		argv = append([]string{name}, argv...)
		name = "pkexec"
	}
	_, _, exit, err := s.runner.Run(ctx, name, argv...)
	if err != nil {
		return s.fail(step, -1)
	}
	switch exit {
	case linuxExitToolMissing:
		return s.fail(step+"-tool-missing", exit)
	case linuxExitHashMismatch:
		return s.fail(step+"-hash-mismatch", exit)
	}
	// 126 / 127 mean authorization was denied / dismissed only when pkexec
	// itself returns them; a missing command in the script uses a custom exit code.
	if viaPkexec && (exit == 126 || exit == 127) {
		s.logf("HTTPS decryption: Linux authorization not granted step=%s exit=%d", step, exit)
		return errTrustCanceled
	}
	if exit != 0 {
		return s.fail(step, exit)
	}
	return nil
}

func (s *linuxTrustStore) scripts(family linuxTrustFamily) (install, remove string) {
	switch family {
	case linuxFamilyDebian, linuxFamilySUSE:
		return linuxScriptDebianInstall, linuxScriptDebianRemove
	case linuxFamilyRHEL:
		return linuxScriptRHELInstall, linuxScriptRHELRemove
	case linuxFamilyArch:
		return linuxScriptArchInstall, linuxScriptArchRemove
	}
	return "", ""
}

func (s *linuxTrustStore) Install(cert *x509.Certificate) error {
	if cert == nil || len(cert.Raw) == 0 {
		return errors.New("linux trust: no certificate")
	}
	if !s.Supported() {
		return errTrustUnsupported
	}
	family := s.family()
	tmp, sum, cleanup, err := s.writeTemp(cert)
	if err != nil {
		return err
	}
	defer cleanup()
	install, remove := s.scripts(family)
	dest := s.target(family, cert)
	if err := s.runPrivileged("system-install", install, tmp, dest, sum); err != nil {
		return err
	}
	// The copy happens after authorization: the target file must still hold
	// the certificate we wrote, otherwise roll it back.
	if dest != "" && !s.sameCert(dest, cert) {
		_ = s.runPrivileged("system-rollback", remove, tmp, dest, sum)
		return s.fail("system-verify", 0)
	}
	s.nssInstall(cert, tmp)
	return nil
}

func (s *linuxTrustStore) Remove(cert *x509.Certificate) error {
	if cert == nil || len(cert.Raw) == 0 {
		return nil
	}
	if !s.Supported() {
		return errTrustUnsupported
	}
	family := s.family()
	// Also remove files installed before the rename. If the certificate is
	// still trusted but no file is found, remove via the current target (e.g. Arch).
	var targets []string
	if legacy := s.legacyTarget(family, cert); legacy != "" && s.exists(legacy) {
		targets = append(targets, legacy)
	}
	if dest := s.target(family, cert); (dest != "" && s.exists(dest)) || (len(targets) == 0 && s.trusted(cert)) {
		targets = append(targets, dest)
	}
	if len(targets) > 0 {
		tmp, sum, cleanup, err := s.writeTemp(cert)
		if err != nil {
			return err
		}
		defer cleanup()
		_, remove := s.scripts(family)
		for _, dest := range targets {
			if err := s.runPrivileged("system-remove", remove, tmp, dest, sum); err != nil {
				return err
			}
		}
	}
	s.nssRemove(cert)
	return nil
}

// nssDatabases lists the NSS database directories that exist.
func (s *linuxTrustStore) nssDatabases() []string {
	home := s.home()
	if home == "" {
		return nil
	}
	var dirs []string
	if shared := filepath.Join(home, ".pki", "nssdb"); s.exists(filepath.Join(shared, "cert9.db")) {
		dirs = append(dirs, shared)
	}
	for _, pattern := range []string{
		filepath.Join(home, ".mozilla", "firefox", "*", "cert9.db"),
		filepath.Join(home, ".config", "mozilla", "firefox", "*", "cert9.db"),
		filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox", "*", "cert9.db"),
		filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox", "*", "cert9.db"),
	} {
		for _, match := range s.glob(pattern) {
			dirs = append(dirs, filepath.Dir(match))
		}
	}
	return dirs
}

func (s *linuxTrustStore) certutil(args ...string) int {
	ctx, cancel := context.WithTimeout(context.Background(), linuxCertutilTimeout)
	defer cancel()
	_, _, exit, err := s.runner.Run(ctx, "certutil", args...)
	if err != nil {
		return -1
	}
	return exit
}

func (s *linuxTrustStore) hasCertutil() bool {
	_, err := s.lookPath("certutil")
	return err == nil
}

// nssInstall adds the certificate to each NSS database best-effort; failures
// are only logged.
func (s *linuxTrustStore) nssInstall(cert *x509.Certificate, file string) {
	if !s.hasCertutil() {
		return
	}
	name := s.nickname(cert)
	for _, dir := range s.nssDatabases() {
		db := "sql:" + dir
		if s.certutil("-L", "-n", name, "-d", db) == 0 {
			continue
		}
		if exit := s.certutil("-A", "-n", name, "-t", "C,,", "-i", file, "-d", db); exit != 0 {
			s.logf("HTTPS decryption: browser certificate database write failed step=nss-add exit=%d", exit)
		}
	}
}

// nssRemove removes the certificate from each NSS database best-effort,
// skipping databases that do not contain it.
func (s *linuxTrustStore) nssRemove(cert *x509.Certificate) {
	if !s.hasCertutil() {
		return
	}
	for _, dir := range s.nssDatabases() {
		db := "sql:" + dir
		for _, name := range []string{s.nickname(cert), s.legacyNickname(cert)} {
			if s.certutil("-L", "-n", name, "-d", db) != 0 {
				continue
			}
			if exit := s.certutil("-D", "-n", name, "-d", db); exit != 0 {
				s.logf("HTTPS decryption: browser certificate database removal failed step=nss-delete exit=%d", exit)
			}
		}
	}
}
