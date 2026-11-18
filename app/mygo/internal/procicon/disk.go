package procicon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// diskEntries and diskBytes bound the icons kept on disk; the oldest
	// written go first.
	diskEntries = 1000
	diskBytes   = 32 << 20
	// iconBytes is the largest icon kept.
	iconBytes = 1 << 20
	// refreshAge is when a kept icon is looked up again (an App updated
	// its icon); the kept one shows meanwhile, and a successful lookup
	// writes it anew, so icons in use stay while unused ones age out.
	refreshAge = 7 * 24 * time.Hour
)

// disk keeps icon bytes in a directory, one file per key named by its
// hash. Safe from any goroutine; the directory is read once, on first use.
type disk struct {
	dir    string
	mu     sync.Mutex
	loaded bool
	// dropped stops writes once the directory is removed.
	dropped bool
	files   map[string]diskFile
	total   int64
	now     func() time.Time
}

type diskFile struct {
	size    int64
	written time.Time
}

func newDisk(dir string) *disk { return &disk{dir: dir, now: time.Now} }

func fileName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:20]) + ".icon"
}

// index reads the directory once. Caller holds mu.
func (d *disk) index() {
	if d.loaded {
		return
	}
	d.loaded, d.files = true, map[string]diskFile{}
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(d.dir, name))
			continue
		}
		if !strings.HasSuffix(name, ".icon") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		d.files[name] = diskFile{size: info.Size(), written: info.ModTime()}
		d.total += info.Size()
	}
	d.evict()
}

// read returns the kept bytes of key, and whether they are recent enough
// to skip a lookup.
func (d *disk) read(key string) (data []byte, fresh, ok bool) {
	name := fileName(key)
	d.mu.Lock()
	d.index()
	file, ok := d.files[name]
	d.mu.Unlock()
	if !ok {
		return nil, false, false
	}
	data, err := os.ReadFile(filepath.Join(d.dir, name))
	if err != nil || len(data) == 0 {
		d.remove(key)
		return nil, false, false
	}
	return data, d.now().Sub(file.written) < refreshAge, true
}

// write keeps data as key's icon, evicting the oldest past the bounds.
func (d *disk) write(key string, data []byte) {
	if len(data) == 0 || len(data) > iconBytes {
		return
	}
	name := fileName(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropped {
		return
	}
	d.index()
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return
	}
	temp := filepath.Join(d.dir, name+".tmp")
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		_ = os.Remove(temp)
		return
	}
	if err := os.Rename(temp, filepath.Join(d.dir, name)); err != nil {
		_ = os.Remove(temp)
		return
	}
	if old, ok := d.files[name]; ok {
		d.total -= old.size
	}
	d.files[name] = diskFile{size: int64(len(data)), written: d.now()}
	d.total += int64(len(data))
	d.evict()
}

// drop removes the directory, keeping nothing more.
func (d *disk) drop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dropped, d.loaded, d.files, d.total = true, true, map[string]diskFile{}, 0
	_ = os.RemoveAll(d.dir)
}

// remove forgets key's icon.
func (d *disk) remove(key string) {
	name := fileName(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	if file, ok := d.files[name]; ok {
		d.total -= file.size
		delete(d.files, name)
	}
	_ = os.Remove(filepath.Join(d.dir, name))
}

// evict removes the oldest written files past the bounds. Caller holds mu.
func (d *disk) evict() {
	if len(d.files) <= diskEntries && d.total <= diskBytes {
		return
	}
	names := make([]string, 0, len(d.files))
	for name := range d.files {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return d.files[a].written.Compare(d.files[b].written) })
	for _, name := range names {
		if len(d.files) <= diskEntries && d.total <= diskBytes {
			break
		}
		d.total -= d.files[name].size
		delete(d.files, name)
		_ = os.Remove(filepath.Join(d.dir, name))
	}
}
