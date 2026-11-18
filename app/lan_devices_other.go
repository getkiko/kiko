//go:build !linux

package main

import "errors"

func lanDevicesSupported() bool { return false }
func discoverLANDevices(paths []string) ([]lanDevice, []string, error) {
	return nil, nil, errors.New("设备发现适用于 Linux server")
}
