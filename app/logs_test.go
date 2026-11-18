package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogHistoryAPIRequiresPreviewToken(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	if err := os.WriteFile(l.path+".1", []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := (&server{logs: l}).routes()
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/logs/history", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := post(`{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing token: %d %s", w.Code, w.Body)
	}
	if w := post(`{"token":"unknown"}`); w.Code != http.StatusConflict {
		t.Fatalf("unknown token: %d %s", w.Code, w.Body)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/logs/history", nil))
	var preview logHistoryPreview
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Token == "" || preview.Files != 1 {
		t.Fatalf("preview: %+v", preview)
	}
	if w := post(`{"token":"` + preview.Token + `"}`); w.Code != http.StatusOK {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
	if w := post(`{"token":"` + preview.Token + `"}`); w.Code != http.StatusConflict {
		t.Fatalf("replayed token: %d %s", w.Code, w.Body)
	}
}

func TestLogHistoryReplacedArchive(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	archive := l.path + ".1"
	if err := os.WriteFile(archive, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := l.previewHistory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archive, archive+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := l.previews[preview.Token].files[0].ModTime()
	if err := os.Chtimes(archive, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	removed, err := l.cleanupHistory(preview.Token)
	if err != nil || removed.Files != 0 {
		t.Fatalf("replacement cleanup: %+v, %v", removed, err)
	}
	data, err := os.ReadFile(archive)
	if err != nil || string(data) != "new" {
		t.Fatalf("replacement: %q, %v", data, err)
	}
}

func TestLogHistoryPreviewThenRotation(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	if err := os.WriteFile(l.path+".1", []byte("old archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(l.path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(10 << 20)
	current, statErr := f.Stat()
	closeErr := f.Close()
	if err != nil || statErr != nil || closeErr != nil {
		t.Fatalf("prepare rotation: %v, %v, %v", err, statErr, closeErr)
	}
	preview, err := l.previewHistory()
	if err != nil {
		t.Fatal(err)
	}
	l.add("trigger rotation while confirmation is open")
	removed, err := l.cleanupHistory(preview.Token)
	if err != nil || removed != preview.logCleanupResult {
		t.Fatalf("cleanup = %+v, %v; preview = %+v", removed, err, preview)
	}
	preserved, err := os.Stat(l.path + ".1")
	if err != nil || !os.SameFile(current, preserved) {
		t.Fatalf("preview-time current log: %v", err)
	}
	if _, err := os.Stat(l.path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("previewed archive survived: %v", err)
	}
	if _, err := l.cleanupHistory(preview.Token); !errors.Is(err, errLogPreviewExpired) {
		t.Fatalf("replayed token: %v", err)
	}
}

func TestLogHistoryChangedAndExpiredPreview(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	archive := l.path + ".1"
	if err := os.WriteFile(archive, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := l.previewHistory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("changed archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := l.cleanupHistory(preview.Token)
	if err != nil || removed.Files != 0 {
		t.Fatalf("changed archive cleanup: %+v, %v", removed, err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	preview, err = l.previewHistory()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := l.previews[preview.Token]
	snapshot.created = time.Now().Add(-logPreviewTTL)
	l.previews[preview.Token] = snapshot
	if _, err := l.cleanupHistory(preview.Token); !errors.Is(err, errLogPreviewExpired) {
		t.Fatalf("expired preview: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
}

func TestLogHistoryCleanup(t *testing.T) {
	dir := t.TempDir()
	l := newLogBuffer(dir)
	l.add("current log")
	for _, name := range []string{"kiko.log.1", "kiko.log.2", "kiko.log.12", "other.log", "kiko.log.backup", "kiko.log.0"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "kiko.log.3"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A symlink with an archive name must also survive cleanup.
	symlinkErr := os.Symlink(filepath.Join(dir, "other.log"), filepath.Join(dir, "kiko.log.4"))
	if symlinkErr != nil {
		t.Logf("symlink fixture unavailable: %v", symlinkErr)
	}
	before, err := l.previewHistory()
	if err != nil || before.Files != 3 || before.Bytes != 21 {
		t.Fatalf("preview = %+v, %v", before, err)
	}
	removed, err := l.cleanupHistory(before.Token)
	if err != nil || removed != before.logCleanupResult {
		t.Fatalf("cleanup = %+v, %v", removed, err)
	}
	for _, name := range []string{"kiko.log", "other.log", "kiko.log.backup", "kiko.log.0", "kiko.log.3"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("preserved file %s: %v", name, err)
		}
	}
	if len(l.snapshot()) != 1 {
		t.Fatal("current log buffer changed")
	}
	if symlinkErr == nil {
		if _, err := os.Lstat(filepath.Join(dir, "kiko.log.4")); err != nil {
			t.Fatalf("preserved symlink: %v", err)
		}
	}
	l.add("logging after cleanup")
	after, err := l.previewHistory()
	if err != nil || after.Files != 0 || len(l.snapshot()) != 2 {
		t.Fatalf("after cleanup = %+v, %v", after, err)
	}
}

func TestLogHistoryDuringRotation(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	file, err := os.Create(l.path)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate(10 << 20)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("prepare rotation: %v, %v", err, closeErr)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			l.add("live log")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			preview, err := l.previewHistory()
			if err == nil {
				_, err = l.cleanupHistory(preview.Token)
			}
			if err != nil {
				t.Errorf("cleanup during rotation: %v", err)
			}
		}
	}()
	wg.Wait()
	preview, err := l.previewHistory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.cleanupHistory(preview.Token); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(l.path); err != nil || info.Size() == 0 {
		t.Fatalf("current log after rotation: %v, %v", info, err)
	}
	if len(l.snapshot()) != 20 {
		t.Fatal("live logs lost during cleanup")
	}
}

func TestLogHistoryClearsSystemLogsAndReportsSizes(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	if err := os.WriteFile(l.path, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path+".1", []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	systemDir := t.TempDir()
	core := filepath.Join(systemDir, "core.log")
	if err := os.WriteFile(core, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleared := 0
	l.system = &systemLogs{
		groups: func() []logFileGroup {
			return []logFileGroup{
				{Key: "core", Label: "core", Paths: []string{core, core + ".1"}},
				{Key: "helper", Label: "helper", Paths: []string{filepath.Join(systemDir, "missing.log")}},
			}
		},
		clear: func() (int, int64, error) {
			cleared++
			return 1, 10, errors.New("reinstall")
		},
	}
	report := l.sizes()
	if report.Total != 24 || len(report.Items) != 2 || report.Items[1].Key != "core" || report.Items[1].Bytes != 10 {
		t.Fatalf("sizes: %+v", report)
	}
	preview, err := l.previewHistory()
	if err != nil || preview.Files != 2 || preview.Bytes != 17 || preview.SystemBytes != 10 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	removed, err := l.cleanupHistory(preview.Token)
	if err != nil || cleared != 1 || removed.Files != 2 || removed.Bytes != 17 || removed.Warning != "reinstall" {
		t.Fatalf("cleanup: %+v cleared=%d %v", removed, cleared, err)
	}
	if err := os.WriteFile(core, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	preview, err = l.previewHistory()
	if err != nil || preview.Files != 0 || preview.SystemBytes != 0 {
		t.Fatalf("empty system logs: %+v %v", preview, err)
	}
	if _, err := l.cleanupHistory(preview.Token); err != nil || cleared != 1 {
		t.Fatalf("cleared empty system logs: cleared=%d %v", cleared, err)
	}
}

func TestClearSystemLogs(t *testing.T) {
	l := newLogBuffer(t.TempDir())
	if _, err := l.clearSystem(); !errors.Is(err, errNoSystemLogs) {
		t.Fatalf("no system logs: %v", err)
	}
	core := filepath.Join(t.TempDir(), "core.log")
	if err := os.WriteFile(core, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.system = &systemLogs{
		groups: func() []logFileGroup { return []logFileGroup{{Key: "core", Label: "core", Paths: []string{core}}} },
		clear:  func() (int, int64, error) { return 1, 5, nil },
	}
	if report := l.sizes(); len(report.Items) != 2 || report.Items[1].Path != core || report.Items[1].Bytes != 5 {
		t.Fatalf("sizes: %+v", report)
	}
	result, err := l.clearSystem()
	if err != nil || result.Files != 1 || result.Bytes != 5 || result.SystemBytes != 5 {
		t.Fatalf("clear: %+v %v", result, err)
	}
}

// Logs written before the rename to Kiko (sbcfg.log, sbcfg.log.N) become
// kiko.log and kiko.log.N; files already named kiko are kept.
func TestLogBufferMigratesLegacyLogs(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"sbcfg.log": "current", "sbcfg.log.1": "one", "sbcfg.log.2": "two",
		"kiko.log.2": "kept", "sbcfg.logger": "other",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l := newLogBuffer(dir)
	if l.path != filepath.Join(dir, "kiko.log") {
		t.Fatalf("path %s", l.path)
	}
	for name, want := range map[string]string{
		"kiko.log": "current", "kiko.log.1": "one", "kiko.log.2": "kept",
		"sbcfg.log.2": "two", "sbcfg.logger": "other",
	} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != want {
			t.Errorf("%s = %q, %v", name, data, err)
		}
	}
	for _, gone := range []string{"sbcfg.log", "sbcfg.log.1"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s remains: %v", gone, err)
		}
	}
}
