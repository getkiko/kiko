//go:build windows

package main

import "golang.org/x/sys/windows"

var isElevated = func() bool { return windows.GetCurrentProcessToken().IsElevated() }
