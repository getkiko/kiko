//go:build !windows

package main

// platformAppIdentity is empty: macOS Apps are bundles, which
// identifyProcess finds in the path.
func platformAppIdentity(string) (appPath, appName string) { return "", "" }
