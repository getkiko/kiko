//go:build darwin

package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"howett.net/plist"
)

func readInstalledApplication(path string) (installedApplication, bool) {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if !filepath.IsAbs(path) || !strings.HasSuffix(strings.ToLower(path), ".app") || strings.Contains(strings.ToLower(path), ".app/") {
		return installedApplication{}, false
	}
	raw, err := os.ReadFile(filepath.Join(path, "Contents", "Info.plist"))
	if err != nil {
		return installedApplication{}, false
	}
	var info struct {
		Name       string `plist:"CFBundleDisplayName"`
		BundleName string `plist:"CFBundleName"`
		Kind       string `plist:"CFBundlePackageType"`
	}
	if _, err = plist.Unmarshal(raw, &info); err != nil || info.Kind != "" && info.Kind != "APPL" {
		return installedApplication{}, false
	}
	name := info.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".app")
	}
	if name == "" {
		name = info.BundleName
	}
	return installedApplication{ID: applicationID(path), Name: name, Path: path}, true
}

func scanApplicationDirectories(ctx context.Context, roots []string) ([]installedApplication, []string, error) {
	items := map[string]installedApplication{}
	warnings := []string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if !os.IsNotExist(err) {
					warnings = append(warnings, "部分应用目录读取受限："+path)
				}
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(strings.ToLower(entry.Name()), ".app") {
				if app, ok := readInstalledApplication(path); ok {
					items[app.Path] = app
				}
				if entry.IsDir() {
					return filepath.SkipDir
				}
			}
			if entry.IsDir() && path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	result := make([]installedApplication, 0, len(items))
	for _, app := range items {
		result = append(result, app)
	}
	return result, warnings, nil
}

func discoverInstalledApplications(ctx context.Context) ([]installedApplication, []string, error) {
	roots := []string{"/Applications", "/System/Applications", "/System/Library/CoreServices/Applications", "/System/Cryptexes/App/System/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	items, warnings, err := scanApplicationDirectories(ctx, roots)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, app := range items {
		seen[app.Path] = true
	}
	// Spotlight covers applications installed outside the standard directories.
	searchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if raw, err := exec.CommandContext(searchCtx, "/usr/bin/mdfind", "-0", "kMDItemContentType == 'com.apple.application-bundle'").Output(); err == nil {
		for _, path := range strings.Split(string(raw), "\x00") {
			if seen[path] || strings.HasPrefix(path, "/Volumes/") || strings.Contains(path, "/.Trash/") {
				continue
			}
			if app, ok := readInstalledApplication(path); ok {
				if !seen[app.Path] {
					items = append(items, app)
					seen[app.Path] = true
				}
			}
		}
	}
	return items, warnings, nil
}
