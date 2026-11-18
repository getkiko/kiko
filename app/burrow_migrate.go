package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// The core renamed the Burrow protocol type from holepunch to burrow. At startup, inbounds
// and outbounds of type holepunch in the saved document, profiles and applied config in the
// data directory are changed to burrow and written back; only files containing the old
// type are rewritten. Every later write path converts too: a store saved by a client
// (putStore), profiles pulled by sync (syncApplyLocal) and a configuration posted for
// checking and applying (checkCandidate).
const (
	burrowType       = "burrow"
	legacyBurrowType = "holepunch"
)

// renameBurrowEntries changes old-type entries in the object's top-level inbounds and
// outbounds to burrow.
func renameBurrowEntries(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for _, key := range []string{"inbounds", "outbounds"} {
		list, _ := object[key].([]any)
		for _, item := range list {
			if entry, ok := item.(map[string]any); ok && entry["type"] == legacyBurrowType {
				entry["type"] = burrowType
				changed = true
			}
		}
	}
	return changed
}

// decodeJSONNumbers decodes numbers verbatim so re-encoding does not change their values.
func decodeJSONNumbers(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	err := decoder.Decode(&value)
	return value, err
}

// migrateBurrowDoc migrates one document or sing-box config; it returns the input
// unchanged when there is no old type.
func migrateBurrowDoc(raw []byte) ([]byte, bool) {
	if !bytes.Contains(raw, []byte(legacyBurrowType)) {
		return raw, false
	}
	value, err := decodeJSONNumbers(raw)
	if err != nil || !renameBurrowEntries(value) {
		return raw, false
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw, false
	}
	return out, true
}

// migrateBurrowProfiles migrates the doc of every profile in the profile list.
func migrateBurrowProfiles(raw []byte) ([]byte, bool) {
	if !bytes.Contains(raw, []byte(legacyBurrowType)) {
		return raw, false
	}
	value, err := decodeJSONNumbers(raw)
	if err != nil {
		return raw, false
	}
	list, _ := value.([]any)
	changed := false
	for _, item := range list {
		if profile, ok := item.(map[string]any); ok && renameBurrowEntries(profile["doc"]) {
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(list)
	if err != nil {
		return raw, false
	}
	return out, true
}

// migrateBurrowStore migrates already-loaded editor.json content and reports whether
// anything changed.
func migrateBurrowStore(store *storeState) bool {
	doc, docChanged := migrateBurrowDoc(store.Doc)
	profiles, profilesChanged := migrateBurrowProfiles(store.Profiles)
	if docChanged {
		store.Doc = doc
	}
	if profilesChanged {
		store.Profiles = profiles
	}
	return docChanged || profilesChanged
}

// migrateBurrowData migrates the data directory: the document and profiles in
// editor.json, plus the applied config.json and runtime/config.json (read directly by
// the core at startup). Failures are skipped and never block startup.
func (s *server) migrateBurrowData() {
	if migrateBurrowStore(&s.store) {
		_ = atomicJSON(filepath.Join(s.dataDir, "editor.json"), s.store)
	}
	for _, path := range []string{filepath.Join(s.dataDir, "config.json"), filepath.Join(s.dataDir, "runtime", "config.json")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		migrated, changed := migrateBurrowDoc(raw)
		if !changed {
			continue
		}
		var indented bytes.Buffer
		if json.Indent(&indented, migrated, "", "  ") == nil {
			migrated = append(indented.Bytes(), '\n')
		}
		_ = atomicWrite(path, migrated, 0o600)
	}
}
