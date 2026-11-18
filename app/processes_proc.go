package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procProcesses lists the executables running under a Linux /proc root:
// each process's executable path, else its command's absolute path, else
// its short name without a path.
func procProcesses(root string) []processInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var items []processInfo
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		path, _ := os.Readlink(filepath.Join(dir, "exe"))
		path = strings.TrimSuffix(path, " (deleted)")
		if path == "" {
			if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
				if argv0, _, _ := strings.Cut(string(cmdline), "\x00"); filepath.IsAbs(argv0) {
					path = argv0
				}
			}
		}
		name := filepath.Base(path)
		if path == "" {
			comm, err := os.ReadFile(filepath.Join(dir, "comm"))
			if err != nil {
				continue
			}
			name = strings.TrimSpace(string(comm))
		}
		key := path + "\x00" + name
		if name == "" || name == "." || name == "/" || seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, processInfo{Name: name, Path: path})
	}
	return items
}
