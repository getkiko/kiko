//go:build linux

package main

func setSystemProxy(a sysProxyApply, restorePath string) error {
	return setGsettingsProxy(a, restorePath)
}
func clearSystemProxy(restorePath string) error   { return clearGsettingsProxy(restorePath) }
func recoverSystemProxy(restorePath string) error { return clearGsettingsProxy(restorePath) }
func systemProxyMatches(a sysProxyApply) bool     { return gsettingsProxyMatches(a) }
