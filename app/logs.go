package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type logBuffer struct {
	mu          sync.RWMutex
	lines       []string
	subscribers map[chan string]struct{}
	path        string
	previews    map[string]logHistorySnapshot
	// system reaches logs kept outside the data directory (the macOS TUN
	// component's /var/log files); nil when there are none.
	system *systemLogs
}

// systemLogs lists log files outside the data directory and clears them.
type systemLogs struct {
	groups func() []logFileGroup
	clear  func() (files int, bytes int64, err error)
}

// logFileGroup is one log and its rotated copies.
type logFileGroup struct {
	Key   string
	Label string
	Paths []string
}

func newLogBuffer(dir string) *logBuffer {
	migrateLegacyLogs(dir)
	return &logBuffer{subscribers: map[chan string]struct{}{}, path: filepath.Join(dir, "kiko.log")}
}

// legacyLogName is the application log's name before the rename to Kiko.
const legacyLogName = "sbcfg.log"

// migrateLegacyLogs renames sbcfg.log and its rotated copies (sbcfg.log.N)
// to kiko.log and kiko.log.N once; existing kiko logs are kept.
func migrateLegacyLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), legacyLogName)
		if !ok || (suffix != "" && !strings.HasPrefix(suffix, ".")) || !entry.Type().IsRegular() {
			continue
		}
		target := filepath.Join(dir, "kiko.log"+suffix)
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		_ = os.Rename(filepath.Join(dir, entry.Name()), target)
	}
}
func (l *logBuffer) writer() io.Writer { return &lineWriter{log: l} }

type lineWriter struct {
	log     *logBuffer
	pending []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.pending = append(w.pending, p...)
	for {
		i := -1
		for j, b := range w.pending {
			if b == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		w.log.add(string(w.pending[:i]))
		w.pending = w.pending[i+1:]
	}
	return n, nil
}
func (l *logBuffer) add(line string) {
	line = time.Now().Format(time.RFC3339) + " " + line
	l.mu.Lock()
	l.lines = append(l.lines, line)
	if len(l.lines) > 2000 {
		l.lines = l.lines[len(l.lines)-2000:]
	}
	_ = os.MkdirAll(filepath.Dir(l.path), 0o700)
	l.rotate()
	f, _ := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if f != nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
	for ch := range l.subscribers {
		select {
		case ch <- line:
		default:
		}
	}
	l.mu.Unlock()
}

// record appends a line to kiko.log without showing it in the live log.
func (l *logBuffer) record(line string) {
	line = time.Now().Format(time.RFC3339) + " " + line
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(l.path), 0o700)
	l.rotate()
	if f, _ := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); f != nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
}
func (l *logBuffer) rotate() {
	if s, e := os.Stat(l.path); e == nil && s.Size() >= 10<<20 {
		_ = os.Remove(l.path + ".3")
		_ = os.Rename(l.path+".2", l.path+".3")
		_ = os.Rename(l.path+".1", l.path+".2")
		_ = os.Rename(l.path, l.path+".1")
	}
}
func (l *logBuffer) snapshot() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]string{}, l.lines...)
}
func (l *logBuffer) clear() { l.mu.Lock(); l.lines = nil; _ = os.Remove(l.path); l.mu.Unlock() }

type logCleanupResult struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// SystemBytes is the part of Bytes held by logs outside the data
	// directory, which cleanup empties including the live files.
	SystemBytes int64  `json:"systemBytes,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

type logHistoryPreview struct {
	logCleanupResult
	Token string `json:"token"`
}

type logHistorySnapshot struct {
	created time.Time
	files   []os.FileInfo
	system  bool
}

var errLogPreviewExpired = errors.New("日志清理预览已过期，请重新预览")

const logPreviewTTL = 15 * time.Minute

// archiveFiles is called with the writer lock held. Only regular, numbered
// archives belong to cleanup; current logs, directories and symlinks survive.
func (l *logBuffer) archiveFiles() ([]os.FileInfo, error) {
	var files []os.FileInfo
	entries, err := os.ReadDir(filepath.Dir(l.path))
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	prefix := filepath.Base(l.path) + "."
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Name(), prefix)
		n, err := strconv.Atoi(suffix)
		if err != nil || n < 1 || strconv.Itoa(n) != suffix {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			// File.Stat captures Windows file identity immediately. Directory
			// entry metadata loads that identity lazily from a mutable path.
			file, err := os.Open(filepath.Join(filepath.Dir(l.path), entry.Name()))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			info, err = file.Stat()
			closeErr := file.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if !info.Mode().IsRegular() {
				continue
			}
			files = append(files, info)
		}
	}
	return files, nil
}

func (l *logBuffer) previewHistory() (logHistoryPreview, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result logHistoryPreview
	files, err := l.archiveFiles()
	if err != nil {
		return result, err
	}
	now := time.Now()
	if l.previews == nil {
		l.previews = make(map[string]logHistorySnapshot)
	}
	for token, preview := range l.previews {
		if now.Sub(preview.created) >= logPreviewTTL {
			delete(l.previews, token)
		}
	}
	// Bound abandoned previews, including dialogs that were cancelled.
	if len(l.previews) >= 32 {
		var oldestToken string
		var oldest time.Time
		for token, preview := range l.previews {
			if oldestToken == "" || preview.created.Before(oldest) {
				oldestToken, oldest = token, preview.created
			}
		}
		delete(l.previews, oldestToken)
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return result, err
	}
	result.Token = hex.EncodeToString(tokenBytes[:])
	for _, info := range files {
		result.Files++
		result.Bytes += info.Size()
	}
	if l.system != nil {
		for _, group := range l.system.groups() {
			count, size, _ := statLogFiles(group.Paths)
			result.Files += count
			result.Bytes += size
			result.SystemBytes += size
		}
	}
	l.previews[result.Token] = logHistorySnapshot{created: now, files: files, system: result.SystemBytes > 0}
	return result, nil
}

// cleanupHistory removes the archives a preview listed and, when the preview
// found any, empties the system logs. A system failure is a warning: the
// archives are already gone.
func (l *logBuffer) cleanupHistory(token string) (logCleanupResult, error) {
	result, system, err := l.cleanupArchives(token)
	if err != nil || !system || l.system == nil {
		return result, err
	}
	// Outside the lock: the helper call may wait on its socket.
	files, size, err := l.system.clear()
	result.Files += files
	result.Bytes += size
	result.SystemBytes += size
	if err != nil {
		result.Warning = err.Error()
	}
	return result, nil
}

func (l *logBuffer) cleanupArchives(token string) (logCleanupResult, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result logCleanupResult
	preview, ok := l.previews[token]
	delete(l.previews, token)
	if !ok || time.Since(preview.created) >= logPreviewTTL {
		return result, false, errLogPreviewExpired
	}
	files, err := l.archiveFiles()
	if err != nil {
		return result, false, err
	}
	for _, info := range files {
		matched := false
		for _, original := range preview.files {
			// Identity follows an archive when rotation renames it. Metadata
			// also protects files replaced or changed after the preview.
			if os.SameFile(original, info) && original.Size() == info.Size() && original.ModTime().Equal(info.ModTime()) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if err := os.Remove(filepath.Join(filepath.Dir(l.path), info.Name())); err != nil {
			return result, false, fmt.Errorf("清理 %s: %w", info.Name(), err)
		}
		result.Files++
		result.Bytes += info.Size()
	}
	return result, preview.system, nil
}

// clearSystem empties the system logs, live files included.
func (l *logBuffer) clearSystem() (logCleanupResult, error) {
	if l.system == nil {
		return logCleanupResult{}, errNoSystemLogs
	}
	files, size, err := l.system.clear()
	return logCleanupResult{Files: files, Bytes: size, SystemBytes: size}, err
}

var errNoSystemLogs = errors.New("没有可清空的系统日志")

// statLogFiles counts the non-empty regular files among paths and their
// size; ok is false when none of them could be read.
func statLogFiles(paths []string) (files int, size int64, ok bool) {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		ok = true
		if info.Size() > 0 {
			files++
			size += info.Size()
		}
	}
	return files, size, ok
}

type logSizeItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Bytes int64  `json:"bytes"`
	// Path is a system log's live file; rotated copies add .1, .2.
	Path string `json:"path,omitempty"`
}

// logSizeReport is GET /logs/size: what all logs occupy on disk.
type logSizeReport struct {
	Total int64         `json:"total"`
	Items []logSizeItem `json:"items"`
}

// sizes reports the logs directory (current logs and archives) and each
// readable system log group.
func (l *logBuffer) sizes() logSizeReport {
	report := logSizeReport{Items: []logSizeItem{}}
	var dirSize int64
	if entries, err := os.ReadDir(filepath.Dir(l.path)); err == nil {
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
				dirSize += info.Size()
			}
		}
	}
	report.Items = append(report.Items, logSizeItem{Key: "server", Label: "服务日志", Bytes: dirSize})
	if l.system != nil {
		for _, group := range l.system.groups() {
			if _, size, ok := statLogFiles(group.Paths); ok {
				report.Items = append(report.Items, logSizeItem{Key: group.Key, Label: group.Label, Bytes: size, Path: group.Paths[0]})
			}
		}
	}
	for _, item := range report.Items {
		report.Total += item.Bytes
	}
	return report
}
func (l *logBuffer) subscribe() (chan string, func()) {
	ch := make(chan string, 128)
	l.mu.Lock()
	l.subscribers[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() { l.mu.Lock(); delete(l.subscribers, ch); close(ch); l.mu.Unlock() }
}
func (l *logBuffer) load() {
	f, e := os.Open(l.path)
	if e != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		l.lines = append(l.lines, s.Text())
		if len(l.lines) > 2000 {
			l.lines = l.lines[1:]
		}
	}
}
