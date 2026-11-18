package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tasks live independently of the control sheet and its HTTP connection.
type taildropTasks struct {
	closing bool
	mu      sync.Mutex
	tasks   map[string]*taildropTask
}
type taildropTask struct {
	ID          string                  `json:"id"`
	EndpointTag string                  `json:"endpointTag"`
	Peer        string                  `json:"peer"`
	Files       []taildropManifestEntry `json:"files"`
	SentBytes   int64                   `json:"sentBytes"`
	TotalBytes  int64                   `json:"totalBytes"`
	State       string                  `json:"state"`
	Error       string                  `json:"error,omitempty"`
	Revision    uint64                  `json:"revision"`
	UpdatedAt   time.Time               `json:"updatedAt"`
	cancel      context.CancelFunc
}

func (s *server) updateTaildropTask(task *taildropTask, update func()) {
	s.td.mu.Lock()
	update()
	task.UpdatedAt = time.Now()
	task.Revision++
	snapshot := *task
	s.td.mu.Unlock()
	s.emit("taildropSend", snapshot)
}
func (s *server) taildropSends(w http.ResponseWriter, r *http.Request) {
	tasks := []taildropTask{}
	s.td.mu.Lock()
	for _, task := range s.td.tasks {
		if tag := r.URL.Query().Get("endpointTag"); tag == "" || tag == task.EndpointTag {
			tasks = append(tasks, *task)
		}
	}
	s.td.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].UpdatedAt.Before(tasks[j].UpdatedAt) })
	writeJSON(w, http.StatusOK, tasks)
}
func (s *server) taildropSendCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.td.mu.Lock()
	task := s.td.tasks[body.ID]
	if task == nil {
		s.td.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "发送任务不存在"})
		return
	}
	active := task.State == "uploading" || task.State == "sending"
	if active {
		task.State = "canceled"
		task.UpdatedAt = time.Now()
		task.Revision++
		task.cancel()
	}
	snapshot := *task
	s.td.mu.Unlock()
	if active {
		s.emit("taildropSend", snapshot)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type taildropContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r taildropContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Spool the browser upload before starting a detached transfer. Temp files are
// private, bounded by disk space, and removed after success, failure or cancel.
func (s *server) taildropSendAsync(w http.ResponseWriter, r *http.Request) {
	tag, peer := r.URL.Query().Get("endpointTag"), r.URL.Query().Get("peer")
	if tag == "" || peer == "" {
		writeJSON(w, 400, map[string]string{"error": "缺少 endpointTag 或 peer"})
		return
	}
	if s.requireClient(w) == nil {
		return
	}
	spool, err := os.CreateTemp("", "kiko-taildrop-*")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	cleanup := func() { spool.Close(); os.Remove(spool.Name()) }
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup()
		}
	}()
	upload := r.Clone(r.Context())
	upload.Body = io.NopCloser(io.TeeReader(r.Body, spool))
	reader, err := upload.MultipartReader()
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "manifest" {
		writeJSON(w, 400, map[string]string{"error": "第一个 part 必须是 manifest"})
		return
	}
	var files []taildropManifestEntry
	if json.NewDecoder(io.LimitReader(part, 1<<20)).Decode(&files) != nil || len(files) == 0 {
		writeJSON(w, 400, map[string]string{"error": "manifest 无效"})
		return
	}
	var total int64
	for _, file := range files {
		if !validTaildropName(file.Name) || file.Size < 0 || file.Size > (1<<63-1)-total {
			writeJSON(w, 400, map[string]string{"error": "manifest 文件名或大小无效"})
			return
		}
		total += file.Size
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &taildropTask{EndpointTag: tag, Peer: peer, Files: files, TotalBytes: total, State: "uploading", Revision: 1, UpdatedAt: time.Now(), cancel: cancel}
	s.td.mu.Lock()
	if s.td.closing {
		s.td.mu.Unlock()
		cancel()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	if s.td.tasks == nil {
		s.td.tasks = map[string]*taildropTask{}
	}
	// Keep recent outcomes for reopening the sheet, without growing indefinitely.
	for id, old := range s.td.tasks {
		if old.State != "uploading" && old.State != "sending" && time.Since(old.UpdatedAt) > time.Hour {
			delete(s.td.tasks, id)
		}
	}
	task.ID = rand.Text()
	s.td.tasks[task.ID] = task
	snapshot := *task
	s.td.mu.Unlock()
	s.emit("taildropSend", snapshot)
	stopClose := context.AfterFunc(ctx, func() { _ = r.Body.Close() })
	defer stopClose()
	// MultipartReader may prefetch; TeeReader already saved those bytes. Copy
	// the remaining underlying body to preserve the exact multipart encoding.
	_, err = io.Copy(spool, taildropContextReader{ctx: ctx, reader: r.Body})
	if err == nil {
		_, err = spool.Seek(0, io.SeekStart)
	}
	if err != nil || ctx.Err() != nil {
		if err == nil {
			err = ctx.Err()
		}
		s.updateTaildropTask(task, func() {
			if task.State != "canceled" {
				task.State = "failed"
				task.Error = err.Error()
			}
		})
		cancel()
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	background := r.Clone(ctx)
	background.Body = spool
	query := background.URL.Query()
	query.Del("async")
	background.URL.RawQuery = query.Encode()
	s.updateTaildropTask(task, func() {
		if task.State != "canceled" {
			task.State = "sending"
		}
	})
	s.td.mu.Lock()
	snapshot = *task
	s.td.mu.Unlock()
	handedOff = true
	go func() {
		defer cleanup()
		defer cancel()
		output := &taildropTaskWriter{s: s, task: task, header: make(http.Header)}
		s.taildropSend(output, background)
		s.updateTaildropTask(task, func() {
			if task.State == "canceled" || task.State == "failed" {
				return
			}
			if ctx.Err() != nil {
				task.State = "canceled"
				return
			}
			if output.code >= 400 {
				task.State = "failed"
				task.Error = fmt.Sprintf("发送失败 (%d)", output.code)
				return
			}
			task.State = "completed"
			task.SentBytes = task.TotalBytes
		})
	}()
	writeJSON(w, http.StatusAccepted, snapshot)
}

// Adapt the existing validated gRPC sender's NDJSON progress to task snapshots.
type taildropTaskWriter struct {
	s              *server
	task           *taildropTask
	header         http.Header
	code           int
	completed      int64
	completedFiles map[int]bool
}

func (w *taildropTaskWriter) Header() http.Header  { return w.header }
func (w *taildropTaskWriter) WriteHeader(code int) { w.code = code }
func (w *taildropTaskWriter) Flush()               {}
func (w *taildropTaskWriter) Write(data []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var progress struct {
			Error         string `json:"error"`
			FileIndex     int    `json:"fileIndex"`
			SentBytes     string `json:"sentBytes"`
			FileCompleted bool   `json:"fileCompleted"`
		}
		if json.Unmarshal([]byte(line), &progress) != nil {
			continue
		}
		w.s.updateTaildropTask(w.task, func() {
			if w.task.State == "canceled" || w.task.State == "failed" {
				return
			}
			if progress.Error != "" {
				w.task.State = "failed"
				w.task.Error = progress.Error
				return
			}
			if progress.FileIndex < 0 || progress.FileIndex >= len(w.task.Files) {
				return
			}
			if w.completedFiles == nil {
				w.completedFiles = map[int]bool{}
			}
			sent, _ := strconv.ParseInt(progress.SentBytes, 10, 64)
			if sent < 0 {
				sent = 0
			}
			if sent > w.task.Files[progress.FileIndex].Size {
				sent = w.task.Files[progress.FileIndex].Size
			}
			if progress.FileCompleted && !w.completedFiles[progress.FileIndex] {
				w.completed += w.task.Files[progress.FileIndex].Size
				w.completedFiles[progress.FileIndex] = true
			}
			current := w.completed
			if !w.completedFiles[progress.FileIndex] {
				current += sent
			}
			if current > w.task.SentBytes {
				w.task.SentBytes = current
			}
		})
	}
	return len(data), nil
}
