//go:build !darwin

package main

import "context"

func discoverInstalledApplications(context.Context) ([]installedApplication, []string, error) {
	return []installedApplication{}, []string{}, nil
}
