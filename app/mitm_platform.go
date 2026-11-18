package main

import (
	"crypto/x509"
	"errors"
)

// Platform interfaces for the HTTPS decryption root certificate: private key
// storage and system trust. Implementations live in the per-platform files
// mitm_secret_*.go and mitm_trust_*.go.

// errSecretNotFound means the credential store holds no root private key.
var errSecretNotFound = errors.New("secret not found")

// errTrustUnsupported means the app cannot install trust on this platform.
var errTrustUnsupported = errors.New("trust installation is not supported on this platform")

// errTrustCanceled means the user canceled the system trust authorization dialog.
var errTrustCanceled = errors.New("trust change canceled")

// secretStore stores the root private key (PKCS#8 DER). The private key enters
// and leaves only through this interface; implementations must not log it or
// include it in error messages.
type secretStore interface {
	// Load returns the private key, or errSecretNotFound if there is none.
	Load() ([]byte, error)
	// Save writes or replaces the private key.
	Save(key []byte) error
	// Delete deletes the private key; it returns nil if there is none.
	Delete() error
}

// trustStore installs or removes TLS server-authentication trust for the root
// certificate in the current user's domain. The trust state is taken from the
// actual x509.Verify result (see mitm_ca.go), not from records kept by this
// interface.
type trustStore interface {
	// Supported reports whether Install / Remove are available on this platform.
	Supported() bool
	// Install adds the certificate to the user domain as a trusted root; it
	// returns errTrustUnsupported when unsupported.
	Install(cert *x509.Certificate) error
	// Remove removes the certificate's trust settings and the certificate
	// itself; it returns nil if it is not installed.
	Remove(cert *x509.Certificate) error
}
