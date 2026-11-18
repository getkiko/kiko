package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// holepunch inbounds and outbounds saved before the rename become burrow at startup: in
// the document being edited, the profiles and the applied config.
func TestMigrateBurrowData(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"inbounds":[{"id":"in_1","type":"holepunch","tag":"hp-in","fields":{"signal_address":"s.example:9960/p"}},{"id":"in_2","type":"mixed","tag":"holepunch-like"}],` +
		`"outbounds":[{"id":"out_1","type":"holepunch","tag":"hp-out","fields":{"listen_port":12345678901234567}}]}`
	store := `{"revision":7,"doc":` + doc + `,"profiles":[{"id":"p1","name":"a","savedAt":1731900000000,"doc":` + doc + `},{"id":"p2","name":"b","doc":{"version":1}}],"activeProfileId":"p1"}`
	config := `{"inbounds":[{"type":"holepunch","tag":"hp-in","signal_server":"s.example"}],"outbounds":[{"type":"direct","tag":"direct"},{"type":"holepunch","tag":"hp-out"}],"route":{"final":"direct"}}`
	files := map[string]string{"editor.json": store, "config.json": config, filepath.Join("runtime", "config.json"): config}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{dataDir: dir}
	if err := readJSON(filepath.Join(dir, "editor.json"), &s.store); err != nil {
		t.Fatal(err)
	}
	s.migrateBurrowData()

	if strings.Contains(string(s.store.Doc), `"holepunch"`) || strings.Contains(string(s.store.Profiles), `"holepunch"`) {
		t.Fatalf("store not migrated: %s %s", s.store.Doc, s.store.Profiles)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "editor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reread storeState
	if err := json.Unmarshal(saved, &reread); err != nil {
		t.Fatal(err)
	}
	if reread.Revision != 7 || strings.Contains(string(saved), `"holepunch"`) || !strings.Contains(string(saved), `"burrow"`) {
		t.Fatalf("editor.json %s", saved)
	}
	// Numbers and other fields stay as they were (holepunch inside a tag is not a type and is left alone).
	for _, want := range []string{"12345678901234567", "1731900000000", "holepunch-like", "s.example:9960/p"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("editor.json lost %q", want)
		}
	}
	for _, name := range []string{"config.json", filepath.Join("runtime", "config.json")} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Inbounds  []struct{ Type, Tag string } `json:"inbounds"`
			Outbounds []struct{ Type, Tag string } `json:"outbounds"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		if value.Inbounds[0].Type != "burrow" || value.Outbounds[0].Type != "direct" || value.Outbounds[1].Type != "burrow" {
			t.Errorf("%s: %s", name, data)
		}
		if !strings.Contains(string(data), `"signal_server": "s.example"`) {
			t.Errorf("%s lost fields: %s", name, data)
		}
	}
}

// Files without the old type are not rewritten.
func TestMigrateBurrowDataLeavesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	config := `{"outbounds":[{"type":"direct","tag":"holepunch"}]}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &server{dataDir: dir, store: storeState{Doc: json.RawMessage(`{"version":1,"outbounds":[]}`)}}
	s.migrateBurrowData()
	if data, _ := os.ReadFile(path); string(data) != config {
		t.Fatalf("config.json rewritten: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "editor.json")); !os.IsNotExist(err) {
		t.Fatalf("editor.json written: %v", err)
	}
}

// holepunchDoc is a document as a client from before the Burrow rename saves it.
func holepunchDoc() map[string]any {
	return map[string]any{"version": 1, "outbounds": []any{map[string]any{"id": "out_1", "type": "holepunch", "tag": "hp"}}}
}

// A store saved after startup, as an old client or a restored backup sends it,
// is converted before it is written.
func TestPutStoreMigratesBurrow(t *testing.T) {
	s := newSyncTestServer(t, nil, newTestClock())
	saveProfiles(t, s, "p1", testProfile{ID: "p1", Name: "A", Doc: holepunchDoc()})
	s.mu.Lock()
	doc, profiles := string(s.store.Doc), string(s.store.Profiles)
	s.mu.Unlock()
	if strings.Contains(doc+profiles, `"holepunch"`) || !strings.Contains(doc, `"burrow"`) || !strings.Contains(profiles, `"burrow"`) {
		t.Fatalf("store not migrated: %s %s", doc, profiles)
	}
	saved, _ := os.ReadFile(filepath.Join(s.dataDir, "editor.json"))
	if strings.Contains(string(saved), `"holepunch"`) {
		t.Fatalf("editor.json: %s", saved)
	}
}

// Profiles pulled from a device from before the rename are converted when the
// sync result is written to the local store.
func TestSyncPullMigratesBurrow(t *testing.T) {
	clock, backend := newTestClock(), &memBackend{}
	a, b := newSyncTestServer(t, backend, clock), newSyncTestServer(t, backend, clock)
	// a's store holds an entry from before the rename that was never resaved.
	a.mu.Lock()
	raw, _ := json.Marshal([]map[string]any{{"id": "p1", "name": "A", "savedAt": 1, "doc": holepunchDoc()}})
	a.store.Profiles = raw
	a.store.Doc, _ = json.Marshal(holepunchDoc())
	a.store.ActiveProfileID = "p1"
	a.mu.Unlock()
	createVault(t, a)
	unlockVault(t, b)
	if got := storeProfileDoc(b, "p1"); got == "" || strings.Contains(got, "holepunch") || !strings.Contains(got, "burrow") {
		t.Fatalf("pulled profile: %q", got)
	}
}

// A configuration posted for applying is converted before the core checks it.
func TestCheckCandidateMigratesBurrow(t *testing.T) {
	dir := t.TempDir()
	s, err := newServer(writeScript(t, "exit 0\n"), filepath.Join(dir, "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"outbounds":[{"type":"holepunch","tag":"hp"},{"type":"direct","tag":"d"}],"route":{"final":"d"}}`
	rec := httptest.NewRecorder()
	c, ok := s.checkCandidate(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if !ok {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	candidate, _ := os.ReadFile(filepath.Join(s.dataDir, "runtime", "candidate.json"))
	for name, data := range map[string][]byte{"clean": c.clean, "runtime": c.runtime, "candidate.json": candidate} {
		if strings.Contains(string(data), `"holepunch"`) || !strings.Contains(string(data), `"burrow"`) {
			t.Fatalf("%s: %s", name, data)
		}
	}
}
