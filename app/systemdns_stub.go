//go:build !darwin

package main

func applySystemDNS(_ string) (func() error, error) { return nil, nil }
