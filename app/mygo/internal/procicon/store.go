// Package procicon is the process icon store shared by Connections, Processes and Usage:
// icons keyed by App bundle or executable path, decoded bitmaps kept in
// memory and their bytes on disk between launches, misses loaded on
// goroutines.
package procicon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// Process is what identifies a process's icon. PID and StartedAt, when
// known, let the server draw the icon of a running App process; Path
// alone serves once the process is gone.
type Process struct {
	PID       int
	StartedAt int64
	Path      string
	Name      string
	AppPath   string
}

// FromPath is the process a path names (Usage's process value): its file
// name, and the App bundle holding it. A value without a separator (the
// joined package names, "Unknown process") only has a name.
func FromPath(value string) Process {
	if !strings.ContainsAny(value, `/\`) {
		return Process{Name: value}
	}
	name := value[strings.LastIndexAny(value, `/\`)+1:]
	return Process{Path: value, Name: name}.withBundle()
}

// Bundle is the macOS App bundle path holds or is, "" when none.
func Bundle(path string) string {
	if i := strings.Index(path, ".app/"); i >= 0 {
		return path[:i+len(".app")]
	}
	if strings.HasSuffix(path, ".app") {
		return path
	}
	return ""
}

// withBundle fills AppPath from the path when the caller did not know it,
// so a connection's executable shares its App's icon.
func (p Process) withBundle() Process {
	if p.AppPath == "" {
		p.AppPath = Bundle(p.Path)
	}
	return p
}

// running reports whether p is an App process the server can identify:
// without its start time the server refuses it.
func (p Process) running() bool { return p.AppPath != "" && p.PID > 0 && p.StartedAt > 0 }

// key is the store's key of p's icon: its App bundle, shared by every
// process of the App, else its executable; "" when it has neither.
func (p Process) key() string {
	switch {
	case p.AppPath != "":
		return "app:" + p.AppPath
	case p.Path != "":
		return "path:" + p.Path
	}
	return ""
}

const (
	// memoryEntries bounds the decoded icons kept; the least recently
	// shown go first, back from disk when shown again.
	memoryEntries = 512
	// missRetry is how long a missing icon stays missing before the
	// store asks again (an App installed meanwhile, a server back up).
	missRetry = 10 * time.Minute
	// fetchers bounds the lookups running at once.
	fetchers = 4
	// fetchTimeout bounds one lookup.
	fetchTimeout = 5 * time.Second
)

type identity struct {
	pid     int
	started int64
}

type entry struct {
	bitmap  *ui.Bitmap
	pending bool
	// retry is when a missing icon may be asked again.
	retry time.Time
	// failed are the running processes asked in vain: another process of
	// the App asks again at once.
	failed map[identity]bool
	used   uint64
}

// Store holds the process icons of one console's server. Bitmap and View
// run on the UI thread; lookups run on goroutines and post their results.
type Store struct {
	env     *env.Env
	disk    *disk
	entries map[string]*entry
	tick    uint64
	slots   chan struct{}
	now     func() time.Time
}

var stores sync.Map // *env.Env → *Store

// Forget drops the disk cache of e's console (a removed server) when the
// server is remote. Frames the closing window still builds get a store
// without a disk, so nothing writes the directory back.
func Forget(e *env.Env) {
	forgotten := &Store{env: e, entries: map[string]*entry{}, slots: make(chan struct{}, fetchers), now: time.Now}
	v, loaded := stores.Swap(e, forgotten)
	if !loaded {
		return
	}
	if s := v.(*Store); s.disk != nil && e.Target.Remote {
		go s.disk.drop()
	}
}

// For returns the store of e's console, creating it on first use. Its
// disk cache lives under e.CacheDir, one directory per server, so a remote
// server's icons never mix with this host's.
func For(e *env.Env) *Store {
	if s, ok := stores.Load(e); ok {
		return s.(*Store)
	}
	s, _ := stores.LoadOrStore(e, newStore(e))
	return s.(*Store)
}

func newStore(e *env.Env) *Store {
	s := &Store{env: e, entries: map[string]*entry{}, slots: make(chan struct{}, fetchers), now: time.Now}
	if e.CacheDir != "" {
		s.disk = newDisk(filepath.Join(e.CacheDir, "process-icons", targetDir(e.Target)))
	}
	return s
}

// targetDir names a server's cache directory.
func targetDir(t env.Target) string {
	if !t.Remote {
		return "local"
	}
	sum := sha256.Sum256([]byte(t.ID + "\x00" + t.URL))
	return "remote-" + hex.EncodeToString(sum[:8])
}

// Bitmap returns p's icon, starting its lookup when it is not known; nil
// while it loads or when there is none. UI thread.
func (s *Store) Bitmap(p Process) *ui.Bitmap {
	p = p.withBundle()
	key := p.key()
	if key == "" {
		return nil
	}
	e := s.entries[key]
	if e != nil {
		if e.bitmap != nil {
			s.tick++
			e.used = s.tick
			return e.bitmap
		}
		if e.pending || !s.retryable(e, p) {
			// A shown miss stays, keeping its backoff and failed processes.
			s.tick++
			e.used = s.tick
			return nil
		}
	} else {
		s.trim()
		e = &entry{}
		s.entries[key] = e
	}
	s.tick++
	e.used, e.pending = s.tick, true
	go s.load(key, p, e)
	return nil
}

// retryable reports whether a missing icon is asked again for p: by a
// running App process not yet asked, or once the miss has expired.
func (s *Store) retryable(e *entry, p Process) bool {
	if p.running() && !e.failed[identity{p.PID, p.StartedAt}] {
		return true
	}
	return !s.now().Before(e.retry)
}

// trim drops the least recently shown quarter of the entries once the
// memory bound is reached; pending lookups stay.
func (s *Store) trim() {
	if len(s.entries) < memoryEntries {
		return
	}
	type aged struct {
		key  string
		used uint64
	}
	var all []aged
	for k, e := range s.entries {
		if !e.pending {
			all = append(all, aged{k, e.used})
		}
	}
	slices.SortFunc(all, func(a, b aged) int {
		switch {
		case a.used < b.used:
			return -1
		case a.used > b.used:
			return 1
		}
		return 0
	})
	for _, a := range all[:min(len(all), memoryEntries/4)] {
		delete(s.entries, a.key)
	}
}

// load runs on a goroutine: the disk copy first, then the lookup when
// there is none or it is due for a refresh.
func (s *Store) load(key string, p Process, e *entry) {
	if s.disk != nil {
		if data, fresh, ok := s.disk.read(key); ok {
			if bitmap, err := ui.DecodeBitmap(data); err == nil {
				// A copy due for a refresh shows while it stays pending, so
				// trim keeps it and no second lookup starts.
				s.env.Post(func() { e.bitmap, e.pending = bitmap, !fresh })
				if fresh {
					return
				}
			} else {
				s.disk.remove(key)
			}
		}
	}
	s.slots <- struct{}{}
	data := s.fetch(p)
	<-s.slots
	var bitmap *ui.Bitmap
	if len(data) > 0 {
		var err error
		if bitmap, err = ui.DecodeBitmap(data); err != nil {
			bitmap = nil
		}
	}
	if bitmap != nil && s.disk != nil {
		s.disk.write(key, data)
	}
	s.env.Post(func() {
		e.pending = false
		if bitmap != nil {
			e.bitmap, e.failed = bitmap, nil
			return
		}
		if e.bitmap != nil {
			return // the refresh failed: keep the copy shown
		}
		e.retry = s.now().Add(missRetry)
		if p.running() {
			if e.failed == nil {
				e.failed = map[identity]bool{}
			}
			e.failed[identity{p.PID, p.StartedAt}] = true
		}
	})
}

// fetch looks p's icon up: the server's drawing of a running App process,
// else the desktop's icon of the path (this host's paths only), else the
// server's icon of the path, which serves a remote server's paths and
// Apps whose icons only the system draws.
func (s *Store) fetch(p Process) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	if p.running() {
		if data, err := s.env.API.ProcessIcon(ctx, p.PID, p.StartedAt); err == nil && len(data) > 0 {
			return data
		}
	}
	if p.Path == "" {
		return nil
	}
	// A remote server's paths are on its host: only its icons serve.
	if !s.env.Target.Remote && s.env.Shell != nil {
		if data := s.env.Shell.AppIcon(p.Path); len(data) > 0 {
			return data
		}
	}
	if data, err := s.env.API.ProcessPathIcon(ctx, p.Path); err == nil && len(data) > 0 {
		return data
	}
	return nil
}
