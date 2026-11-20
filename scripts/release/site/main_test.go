package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	out := t.TempDir()
	if err := write(out, Site{Repo: "getkiko/kiko", Version: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		"curl -fsSL https://github.com/getkiko/kiko/releases/latest/download/install.sh | sh -s -- --uninstall",
		"irm https://github.com/getkiko/kiko/releases/latest/download/install.ps1 | iex",
		"curl -fsSL https://github.com/getkiko/kiko/releases/latest/download/install-server.sh | sudo sh",
		"sudo kiko upgrade",
		"sudo kiko pair",
		"https://github.com/getkiko/kiko/releases/download/v0.3.0/kiko-0.3.0-arm64.dmg",
		"https://github.com/getkiko/kiko/releases/download/v0.3.0/kiko-setup-0.3.0-amd64.exe",
		"https://github.com/getkiko/kiko/releases/download/v0.3.0/kiko-server-linux-arm64.tar.gz",
		"--version 0.3.0",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// macOS installs from the disk images, not the install script.
	if strings.Contains(page, "install.sh | sh</code>") {
		t.Error("page offers the macOS install script")
	}
	// The management command is /usr/local/bin/kiko; kiko-server is the service
	// binary.
	if strings.Contains(page, "sudo kiko-server") {
		t.Error("page names the service binary as the management command")
	}
	if _, err := os.Stat(filepath.Join(out, ".nojekyll")); err != nil {
		t.Error(err)
	}
	// The icons the page links to are written beside it.
	for _, name := range []string{"favicon.png", "apple-touch-icon.png", "screenshot-light.png", "screenshot-dark.png"} {
		if !strings.Contains(page, `"`+name+`"`) {
			t.Errorf("page does not link %s", name)
		}
		if info, err := os.Stat(filepath.Join(out, name)); err != nil || info.Size() == 0 {
			t.Errorf("%s not written: %v", name, err)
		}
	}
}

func TestWriteRejectsBadInput(t *testing.T) {
	for _, s := range []Site{
		{Repo: "kiko", Version: "0.3.0"},
		{Repo: "a/b c", Version: "0.3.0"},
		{Repo: "a/b", Version: "v0.3.0"},
		{Repo: "a/b", Version: ""},
	} {
		if err := write(t.TempDir(), s); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}
