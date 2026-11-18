//go:build darwin

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, path string, count int, width int) []byte {
	t.Helper()
	var content bytes.Buffer
	for index := 0; index < count; index++ {
		line := strings.Repeat(string(rune('a'+index%26)), width-1)
		content.WriteString(line + "\n")
	}
	if err := os.WriteFile(path, content.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return content.Bytes()
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestRotateLogKeepsSmallFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	want := writeLines(t, path, 5, 10)
	rotated, err := rotateLog(path, 100, 2)
	if err != nil || rotated {
		t.Fatalf("rotated=%v err=%v", rotated, err)
	}
	if got := readFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("small log changed: %q", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("unexpected archive: %v", err)
	}
	if rotated, err := rotateLog(filepath.Join(t.TempDir(), "missing.log"), 100, 2); err != nil || rotated {
		t.Fatalf("missing log: rotated=%v err=%v", rotated, err)
	}
}

func TestRotateLogCopiesAndTruncatesForAppendWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	want := writeLines(t, path, 20, 10) // 200 bytes, between limit and 4×limit
	writer, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	rotated, err := rotateLog(path, 100, 2)
	if err != nil || !rotated {
		t.Fatalf("rotated=%v err=%v", rotated, err)
	}
	if got := readFile(t, path+".1"); !bytes.Equal(got, want) {
		t.Fatalf("archive: %q", got)
	}
	if _, err := writer.WriteString("after\n"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); string(got) != "after\n" {
		t.Fatalf("append writer did not continue at the new end: %q", got)
	}
}

func TestRotateLogKeepsTailOfHugeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	writeLines(t, path, 100, 10) // 1000 bytes > 4×limit
	if _, err := rotateLog(path, 95, 2); err != nil {
		t.Fatal(err)
	}
	archive := readFile(t, path+".1")
	if len(archive) == 0 || len(archive) > 95 {
		t.Fatalf("archive size %d", len(archive))
	}
	// The tail starts at a line boundary: 90 bytes are nine complete lines.
	if len(archive) != 90 || !bytes.HasSuffix(archive, []byte("\n")) {
		t.Fatalf("archive is not whole lines: %q", archive)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(archive), "\n"), "\n") {
		if len(line) != 9 {
			t.Fatalf("partial line %q", line)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("live log not truncated: %v %v", info, err)
	}
}

func TestRotateLogKeepsAlignedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	writeLines(t, path, 100, 10)
	// 1000-100 lands exactly after a newline, so all ten tail lines stay.
	if _, err := rotateLog(path, 100, 2); err != nil {
		t.Fatal(err)
	}
	if archive := readFile(t, path+".1"); len(archive) != 100 {
		t.Fatalf("aligned tail: %d bytes", len(archive))
	}
}

func TestRotateLogShiftsArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	var generations [][]byte
	for round := 0; round < 3; round++ {
		generations = append(generations, writeLines(t, path, 20+round, 10))
		if _, err := rotateLog(path, 100, 2); err != nil {
			t.Fatal(err)
		}
	}
	if got := readFile(t, path+".1"); !bytes.Equal(got, generations[2]) {
		t.Fatalf(".1 is not the newest generation")
	}
	if got := readFile(t, path+".2"); !bytes.Equal(got, generations[1]) {
		t.Fatalf(".2 is not the previous generation")
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("kept more than two archives: %v", err)
	}
}

func TestRotateLogRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	want := writeLines(t, target, 20, 10)
	path := filepath.Join(dir, "core.log")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if rotated, err := rotateLog(path, 100, 2); err == nil || rotated {
		t.Fatalf("symlink rotated=%v err=%v", rotated, err)
	}
	if got := readFile(t, target); !bytes.Equal(got, want) {
		t.Fatal("symlink target changed")
	}
}

func TestClearLogEmptiesLiveFileAndRemovesArchives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.log")
	writeLines(t, path, 3, 10)
	writeLines(t, path+".1", 2, 10)
	writeLines(t, path+".2", 1, 10)
	other := filepath.Join(dir, "core.log.3")
	writeLines(t, other, 1, 10)
	files, released, err := clearLog(path, 2)
	if err != nil || files != 3 || released != 60 {
		t.Fatalf("files=%d released=%d err=%v", files, released, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("live log: %v %v", info, err)
	}
	for _, archive := range []string{path + ".1", path + ".2"} {
		if _, err := os.Stat(archive); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", archive, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("file outside the archive range removed: %v", err)
	}
	if files, released, err := clearLog(filepath.Join(dir, "missing.log"), 2); err != nil || files != 0 || released != 0 {
		t.Fatalf("missing log: files=%d released=%d err=%v", files, released, err)
	}
}

func TestClearLogsTouchesOnlyConfiguredPaths(t *testing.T) {
	dir := t.TempDir()
	previous := helperPaths
	t.Cleanup(func() { helperPaths = previous })
	helperPaths.CoreLog = filepath.Join(dir, "kiko-core.log")
	helperPaths.HelperLog = filepath.Join(dir, "helper.log")
	writeLines(t, helperPaths.CoreLog, 4, 10)
	writeLines(t, helperPaths.HelperLog+".1", 1, 10)
	unrelated := filepath.Join(dir, "unrelated.log")
	writeLines(t, unrelated, 1, 10)
	res := clearLogs()
	if res.Error != "" || res.Files != 2 || res.Bytes != 50 {
		t.Fatalf("clear-logs: %+v", res)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated log removed: %v", err)
	}
}
