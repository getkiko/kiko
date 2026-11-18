package main

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
)

// System certificate locations, the same as crypto/x509's root_linux.go and
// root_unix.go.
var (
	mitmCertFiles = []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/pki/tls/cacert.pem",
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
		"/etc/ssl/cert.pem",
	}
	mitmCertDirectories = []string{
		"/etc/ssl/certs",
		"/etc/pki/tls/certs",
		"/system/etc/security/cacerts",
		"/data/misc/keychain/cacerts-added",
	}
)

func loadMITMSystemRoots(certFile, certDir string, files, dirs []string) *x509.CertPool {
	pool := x509.NewCertPool()
	if certFile != "" {
		files = []string{certFile}
	}
	for _, file := range files {
		if data, err := os.ReadFile(file); err == nil {
			pool.AppendCertsFromPEM(data)
			break
		}
	}
	if certDir != "" {
		dirs = strings.Split(certDir, ":")
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if data, err := os.ReadFile(filepath.Join(dir, entry.Name())); err == nil {
				pool.AppendCertsFromPEM(data)
			}
		}
	}
	return pool
}
