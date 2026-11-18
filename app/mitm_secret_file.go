//go:build !darwin && !windows && !linux

package main

// newSecretStore stores the private key as PEM in <dataDir>/mitm/ca.key, readable and
// writable only by the current user, since these platforms have no unified credential store.
func newSecretStore(dataDir string) secretStore {
	return newPEMSecretStore(dataDir)
}
