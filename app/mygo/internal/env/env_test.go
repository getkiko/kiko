package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// A preferences file holding null starts empty instead of failing the
// first save.
func TestPrefsNullFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPrefs(path)
	p.Save("density", 2)
	var density int
	if !NewPrefs(path).Load("density", &density) || density != 2 {
		t.Errorf("density %d after save", density)
	}
}

// Posts wake the window that drains last, as a window reopened after the
// first one closed.
func TestPostWakesTheCurrentWindow(t *testing.T) {
	e := New(api.New("http://127.0.0.1:1"), nil)
	first := ui.NewTester(func(c *ui.Context) { e.Drain(c) }, 100, 100)
	first.Frame()
	second := ui.NewTester(func(c *ui.Context) { e.Drain(c) }, 100, 100)
	second.Frame()
	e.Post(func() {})
	if !second.FramePending() {
		t.Error("the current window was not woken")
	}
}

// Preferences saved before the rename to Kiko (sbcfg-… and sbcfg:… keys)
// move to the kiko-… and kiko:… keys once, and the file is rewritten.
func TestPrefsMigratesLegacyKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	legacy := `{"sbcfg-connections-columns":["host"],"sbcfg:ssh:[\"a\",\"b\"]":"{}","sbcfg-dns-sidebar":false,"kiko-dns-sidebar":true,"configShareOrigin":"x"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPrefs(path)
	var columns []string
	if !p.Load("kiko-connections-columns", &columns) || len(columns) != 1 || columns[0] != "host" {
		t.Errorf("columns %v", columns)
	}
	var ssh string
	if !p.Load(`kiko:ssh:["a","b"]`, &ssh) || ssh != "{}" {
		t.Errorf("ssh options %q", ssh)
	}
	var sidebar bool
	if !p.Load("kiko-dns-sidebar", &sidebar) || !sidebar {
		t.Error("an existing kiko- key was overwritten by its legacy value")
	}
	var origin string
	if !p.Load("configShareOrigin", &origin) || origin != "x" {
		t.Errorf("unprefixed key %q", origin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sbcfg") {
		t.Errorf("legacy keys remain in the file: %s", data)
	}
	reopened := NewPrefs(path)
	if !reopened.Load("kiko-connections-columns", &columns) {
		t.Error("migrated key missing after reopening")
	}
}
