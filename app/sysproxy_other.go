//go:build !darwin && !windows && !linux

package main

import "errors"

var errSysProxyUnsupported = errors.New("当前系统不支持设置系统代理")

func setSystemProxy(_ sysProxyApply, _ string) error { return errSysProxyUnsupported }
func clearSystemProxy(_ string) error                { return errSysProxyUnsupported }
func recoverSystemProxy(_ string) error              { return nil }
func systemProxyMatches(_ sysProxyApply) bool        { return true }
