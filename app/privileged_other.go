//go:build !darwin

package main

import "errors"

func privilegedStart(_, _, _, _ string, _ bool) (int, error) {
	return 0, errors.New("privileged kiko-core is available on macOS only")
}
func privilegedStop(_ int) error                 { return nil }
func privilegedForceStop(_ int) error            { return nil }
func privilegedReload(_ int) error               { return errors.New("privileged reload is available on macOS only") }
func requireReloadHelper() error                 { return privilegedReload(0) }
func helperLogPath(dataDir string) string        { return dataDir + "/logs/kiko-core.log" }
func helperInstalled() bool                      { return false }
func privilegedAlive(_ int) bool                 { return false }
func privilegedAliveChecked(_ int) (bool, error) { return false, nil }
func repairTaildropAccess(_, _ string) error {
	return errors.New("Taildrop access repair is available on macOS only")
}

func privilegedProcessStatus(int) (bool, string, error) { return false, "退出状态未知", nil }

// privilegedSystemLogs is nil: only macOS keeps TUN logs outside the data directory.
func privilegedSystemLogs() *systemLogs { return nil }
