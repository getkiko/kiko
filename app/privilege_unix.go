//go:build !windows

package main

import "os"

var isElevated = func() bool { return os.Geteuid() == 0 }
