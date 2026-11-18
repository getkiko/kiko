package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Prefs keeps per-user view preferences between launches: column
// choices, grouping modes, densities. Values are JSON. It is safe from any
// goroutine.
type Prefs struct {
	mu     sync.Mutex
	path   string
	values map[string]json.RawMessage
}

// NewPrefs returns preferences stored in the file at path, or kept in
// memory only when path is empty.
func NewPrefs(path string) *Prefs {
	p := &Prefs{path: path, values: map[string]json.RawMessage{}}
	if path != "" {
		var values map[string]json.RawMessage
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &values) == nil && values != nil {
			p.values = values
		}
		if migrateLegacyPrefKeys(p.values) {
			p.mu.Lock()
			p.writeLocked()
			p.mu.Unlock()
		}
	}
	return p
}

// legacyPrefPrefixes maps the key prefixes used before the app was
// renamed Kiko (sbcfg-…, sbcfg:…) to the current ones.
var legacyPrefPrefixes = [][2]string{{"sbcfg-", "kiko-"}, {"sbcfg:", "kiko:"}}

// migrateLegacyPrefKeys renames the keys saved under a legacy prefix once,
// keeping a value already saved under the new key. It reports whether
// values changed.
func migrateLegacyPrefKeys(values map[string]json.RawMessage) bool {
	changed := false
	for key, value := range values {
		for _, prefix := range legacyPrefPrefixes {
			if !strings.HasPrefix(key, prefix[0]) {
				continue
			}
			if _, exists := values[prefix[1]+key[len(prefix[0]):]]; !exists {
				values[prefix[1]+key[len(prefix[0]):]] = value
			}
			delete(values, key)
			changed = true
			break
		}
	}
	return changed
}

// Load reads the preference key into v, reporting whether it was set.
func (p *Prefs) Load(key string, v any) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	raw, ok := p.values[key]
	p.mu.Unlock()
	return ok && json.Unmarshal(raw, v) == nil
}

// Save stores v as the preference key and writes the file.
func (p *Prefs) Save(key string, v any) {
	if p == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values[key] = raw
	p.writeLocked()
}

// writeLocked writes the values to the file; p.mu is held.
func (p *Prefs) writeLocked() {
	if p.path == "" {
		return
	}
	data, err := json.MarshalIndent(p.values, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p.path), 0o700)
	tmp := p.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, p.path)
	}
}
