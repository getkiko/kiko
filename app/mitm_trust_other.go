//go:build !darwin && !windows && !linux

package main

import "crypto/x509"

// unsupportedTrustStore is used on other platforms, where users import the
// root certificate themselves.
type unsupportedTrustStore struct{}

func newTrustStore() trustStore { return unsupportedTrustStore{} }

func (unsupportedTrustStore) Supported() bool { return false }

func (unsupportedTrustStore) Install(*x509.Certificate) error { return errTrustUnsupported }

func (unsupportedTrustStore) Remove(*x509.Certificate) error { return errTrustUnsupported }
