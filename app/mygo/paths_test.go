package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
)

func TestMigrateLegacyData(t *testing.T) {
	for _, legacyName := range []string{"Poppy", "sbcfg-desktop"} {
		root := t.TempDir()
		legacy := filepath.Join(root, legacyName)
		if err := os.MkdirAll(filepath.Join(legacy, "data"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := migrateLegacyData(root, "Kiko"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "Kiko", "data")); err != nil {
			t.Fatalf("%s: %v", legacyName, err)
		}
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("%s: legacy directory remains: %v", legacyName, err)
		}
	}
}

func TestMigrateLegacyDataPrefersNewestName(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Poppy", "sbcfg-desktop"} {
		if err := os.MkdirAll(filepath.Join(root, name, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migrateLegacyData(root, "Kiko"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Kiko", "Poppy")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sbcfg-desktop")); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyDataPreservesCurrent(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Kiko", "Poppy", "sbcfg-desktop"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Kiko", "servers.json"), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateLegacyData(root, "Kiko"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Poppy", "sbcfg-desktop"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateLegacyServerData(t *testing.T) {
	userData := t.TempDir()
	legacy := filepath.Join(userData, "sbcfg-server")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "editor.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateLegacyServerData(userData); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(userData, "kiko-server", "editor.json")); err != nil || string(data) != "{}" {
		t.Fatalf("migrated editor.json = %q, %v", data, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory remains: %v", err)
	}
	// A second run finds nothing to move.
	if _, err := migrateLegacyServerData(userData); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyServerDataKeepsCurrent(t *testing.T) {
	userData := t.TempDir()
	for _, name := range []string{"kiko-server", "sbcfg-server"} {
		if err := os.Mkdir(filepath.Join(userData, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(userData, name, "editor.json"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migrateLegacyServerData(userData); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(userData, "kiko-server", "editor.json")); string(data) != "kiko-server" {
		t.Fatalf("current data replaced: %q", data)
	}
	if _, err := os.Stat(filepath.Join(userData, "sbcfg-server")); err != nil {
		t.Fatalf("legacy directory touched: %v", err)
	}
}

func TestMigrateLegacyServerDataWithoutLegacy(t *testing.T) {
	userData := t.TempDir()
	if _, err := migrateLegacyServerData(userData); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(userData, "kiko-server")); !os.IsNotExist(err) {
		t.Fatalf("unexpected kiko-server: %v", err)
	}
}

// A data directory that cannot move (a file in it is open on Windows) is used
// where it is, never replaced by an empty one, and moves at a later start.
func TestMigrateLegacyDirsKeepOldOnRenameFailure(t *testing.T) {
	failing := func(string, string) error { return errors.New("in use") }
	renameDir = failing
	defer func() { renameDir = os.Rename }()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Poppy", "sbcfg-server"), 0o700); err != nil {
		t.Fatal(err)
	}
	appDir, err := migrateLegacyData(root, "Kiko")
	if err != nil || appDir != filepath.Join(root, "Poppy") {
		t.Fatalf("app dir %q, %v", appDir, err)
	}
	serverDir, err := migrateLegacyServerData(appDir)
	if err != nil || serverDir != filepath.Join(root, "Poppy", "sbcfg-server") {
		t.Fatalf("server dir %q, %v", serverDir, err)
	}
	for _, name := range []string{"Kiko", filepath.Join("Poppy", "kiko-server")} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s created: %v", name, err)
		}
	}
	if p := (&Paths{userData: appDir, serverData: serverDir}); p.dataDir() != serverDir {
		t.Fatalf("dataDir %q", p.dataDir())
	}

	renameDir = os.Rename
	if appDir, err = migrateLegacyData(root, "Kiko"); err != nil || appDir != filepath.Join(root, "Kiko") {
		t.Fatalf("retry app dir %q, %v", appDir, err)
	}
	if serverDir, err = migrateLegacyServerData(appDir); err != nil || serverDir != filepath.Join(root, "Kiko", "kiko-server") {
		t.Fatalf("retry server dir %q, %v", serverDir, err)
	}
}

// A current directory holding only MyGo's own files and empty directories,
// left by a run that fell back to the legacy directory, gives way to it.
func TestMigrateLegacyDataReplacesFrameworkOnlyCurrent(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("Kiko", "window-state.json"), "new")
	write(filepath.Join("Kiko", "updater.json"), "{}")
	if err := os.MkdirAll(filepath.Join(root, "Kiko", "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("Poppy", "servers.json"), "[1]")
	dir, err := migrateLegacyData(root, "Kiko")
	if err != nil || dir != filepath.Join(root, "Kiko") {
		t.Fatalf("dir %q, %v", dir, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "Kiko", "servers.json")); string(data) != "[1]" {
		t.Fatalf("legacy data not moved: %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "Poppy")); !os.IsNotExist(err) {
		t.Fatalf("legacy directory remains: %v", err)
	}

	// Any other file keeps the current directory and the legacy one.
	write(filepath.Join("Poppy", "servers.json"), "[2]")
	write(filepath.Join("Kiko", "window-state.json"), "x")
	if dir, err := migrateLegacyData(root, "Kiko"); err != nil || dir != filepath.Join(root, "Kiko") {
		t.Fatalf("dir %q, %v", dir, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "Poppy", "servers.json")); string(data) != "[2]" {
		t.Fatal("legacy directory touched")
	}

	// An empty kiko-server gives way to sbcfg-server; one with data stays.
	userData := t.TempDir()
	if err := os.MkdirAll(filepath.Join(userData, "kiko-server", "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(userData, "sbcfg-server"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userData, "sbcfg-server", "editor.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dir, err := migrateLegacyServerData(userData); err != nil || dir != filepath.Join(userData, "kiko-server") {
		t.Fatalf("server dir %q, %v", dir, err)
	}
	if data, _ := os.ReadFile(filepath.Join(userData, "kiko-server", "editor.json")); string(data) != "old" {
		t.Fatalf("server data not moved: %q", data)
	}
}

// After a failed move, MyGo's PathUserData points at the legacy directory
// before anything asks for it, so <appData>/<appName> is never created.
func TestUserDataDirFallbackSetsMyGoPath(t *testing.T) {
	renameDir = func(string, string) error { return errors.New("in use") }
	t.Cleanup(func() {
		renameDir = os.Rename
		mygo.App.SetPath(mygo.PathUserData, "")
	})
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Poppy"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := userDataDir(root, "Kiko")
	if err != nil || dir != filepath.Join(root, "Poppy") {
		t.Fatalf("dir %q, %v", dir, err)
	}
	if got, err := mygo.App.Path(mygo.PathUserData); err != nil || got != dir {
		t.Fatalf("PathUserData %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Kiko")); !os.IsNotExist(err) {
		t.Fatalf("Kiko created: %v", err)
	}
}
