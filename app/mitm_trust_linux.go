//go:build linux

package main

func newTrustStore() trustStore { return newLinuxTrustStoreDefaults() }
