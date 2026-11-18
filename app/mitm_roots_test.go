package main

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// System certificates are reread on every check, so a newly installed root
// takes effect without a restart.
func TestMITMSystemRootsReloadEachCheck(t *testing.T) {
	root, rootKey, _ := mustRoot(t, time.Now())
	authority, err := issueMITMIntermediate(root, rootKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.pem")
	certDir := filepath.Join(dir, "certs")
	_ = os.Mkdir(certDir, 0o700)
	if verifyMITMTrust(authority, loadMITMSystemRoots(bundle, certDir, nil, nil), time.Now()) {
		t.Fatal("not installed yet")
	}
	_ = os.WriteFile(filepath.Join(certDir, "kiko.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), 0o600)
	if !verifyMITMTrust(authority, loadMITMSystemRoots(bundle, certDir, nil, nil), time.Now()) {
		t.Fatal("directory root must be picked up")
	}
	_ = os.Remove(filepath.Join(certDir, "kiko.pem"))
	_ = os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), 0o600)
	if !verifyMITMTrust(authority, loadMITMSystemRoots(bundle, certDir, nil, nil), time.Now()) {
		t.Fatal("bundle root must be picked up")
	}
	_ = os.Remove(bundle)
	if verifyMITMTrust(authority, loadMITMSystemRoots(bundle, certDir, nil, nil), time.Now()) {
		t.Fatal("removed root must not verify")
	}
}
