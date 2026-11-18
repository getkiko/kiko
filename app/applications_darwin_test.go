//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationDiscoveryIncludesStoppedAppsAndSkipsNestedHelpers(t *testing.T) {
	root := t.TempDir()
	write := func(path, name string) {
		if err := os.MkdirAll(filepath.Join(path, "Contents"), 0700); err != nil {
			t.Fatal(err)
		}
		content := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleDisplayName</key><string>` + name + `</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
		if err := os.WriteFile(filepath.Join(path, "Contents", "Info.plist"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	app := filepath.Join(root, "Stopped App.app")
	write(app, "Stopped App")
	canonical, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(app, "Contents", "Helpers", "Helper.app"), "Helper")
	if err := os.Symlink(app, filepath.Join(root, "Alias.app")); err != nil {
		t.Fatal(err)
	}
	items, _, err := scanApplicationDirectories(context.Background(), []string{root, filepath.Join(root, "missing")})
	if err != nil || len(items) != 1 || items[0].Path != canonical || items[0].Name != "Stopped App" {
		t.Fatalf("applications = %+v, %v", items, err)
	}
	if _, ok := readInstalledApplication(filepath.Join(app, "Contents", "Helpers", "Helper.app")); ok {
		t.Fatal("nested Helper was listed")
	}
	if items[0].ID != applicationID(canonical) {
		t.Fatal("application ID is unstable")
	}
}
