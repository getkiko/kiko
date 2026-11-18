//go:build !linux

package main

// tunPrivilegeError is empty: macOS starts TUN through its helper and
// Windows checks elevation on its own.
func tunPrivilegeError(string) string { return "" }
