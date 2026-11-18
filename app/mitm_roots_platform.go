//go:build darwin || windows

package main

import "crypto/x509"

// mitmSystemRoots returns nil: on macOS and Windows the system verifier reads
// trust settings live.
func mitmSystemRoots() *x509.CertPool { return nil }
