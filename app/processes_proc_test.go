package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcProcesses(t *testing.T) {
	root := t.TempDir()
	write := func(pid, name, content string) {
		t.Helper()
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(pid, target string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, pid, "exe")); err != nil {
			t.Fatal(err)
		}
	}
	link("1", "/usr/lib/systemd/systemd")
	link("20", "/opt/a very long application name/bin/worker (deleted)")
	link("21", "/opt/a very long application name/bin/worker")
	write("30", "cmdline", "/usr/bin/python3\x00-m\x00http.server\x00")
	write("31", "cmdline", "nginx: worker process\x00")
	write("31", "comm", "nginx\n")
	write("40", "comm", "kworker/0:1\n")
	write("self", "comm", "ignored\n")
	got := map[string]string{}
	for _, p := range procProcesses(root) {
		if _, dup := got[p.Name]; dup {
			t.Errorf("duplicate %q", p.Name)
		}
		got[p.Name] = p.Path
	}
	want := map[string]string{
		"systemd": "/usr/lib/systemd/systemd", "worker": "/opt/a very long application name/bin/worker",
		"python3": "/usr/bin/python3", "nginx": "", "kworker/0:1": "",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for name, path := range want {
		if got[name] != path {
			t.Errorf("%s: path %q, want %q", name, got[name], path)
		}
	}
	if procProcesses(filepath.Join(root, "missing")) != nil {
		t.Error("a missing root lists processes")
	}
}
