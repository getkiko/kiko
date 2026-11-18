//go:build !darwin && !windows

package main

import (
	"context"
	"errors"
)

func processBundleIcon(context.Context, string) ([]byte, error) {
	return nil, errors.New("bundle icons require macOS")
}
func identifyProcessIcon(p *monitoredProcess) {
	identifyProcess(p)
}
