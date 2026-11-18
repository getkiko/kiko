//go:build !darwin && !windows

package main

import (
	"crypto/x509"
	"os"
)

// mitmSystemRoots reloads the system certificates on every call: Go's system
// pool is loaded only once, so installing or removing trust is not reflected
// without rereading.
func mitmSystemRoots() *x509.CertPool {
	return loadMITMSystemRoots(os.Getenv("SSL_CERT_FILE"), os.Getenv("SSL_CERT_DIR"), mitmCertFiles, mitmCertDirectories)
}
