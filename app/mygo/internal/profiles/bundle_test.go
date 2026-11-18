package profiles

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func mustImport(t *testing.T, config string, platform string) *model.Doc {
	t.Helper()
	doc, err := ImportSingBoxConfig([]byte(config), platform)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBundleKeepsOnlyReferencedNetworkConfigs(t *testing.T) {
	doc := mustImport(t, `{"inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890}],
		"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "direct"}}`, "linux")
	doc.Log.Disabled = true
	doc.Inbounds[0].NetworkConfigRefs = model.NetworkConfigRefs{"default": "net-used"}
	profile := &model.Profile{ID: "profile-1", Name: "Main", SavedAt: 123, Doc: doc}
	used := &model.NetworkConfig{ID: "net-used", Name: "Ethernet", Kind: "network", Fields: model.Fields{"bind_interface": "en0"}, UpdatedAt: 10}
	unused := &model.NetworkConfig{ID: "net-unused", Name: "Other", Kind: "network", Fields: model.Fields{"bind_interface": "en1"}, UpdatedAt: 11}

	serialized, err := SerializeBundle([]*model.Profile{profile}, []*model.NetworkConfig{used, unused}, false)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Format   string `json:"format"`
		Profiles []struct {
			Doc json.RawMessage `json:"doc"`
		} `json:"profiles"`
		NetworkConfigs []*model.NetworkConfig `json:"networkConfigs"`
	}
	if err := json.Unmarshal(serialized, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Format != "kiko-profiles" || !reflect.DeepEqual(parsed.NetworkConfigs, []*model.NetworkConfig{used}) {
		t.Errorf("bundle %s", serialized)
	}
	profiles, library, err := ParseImport(serialized, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if profiles[0].ID != "profile-1" || !profiles[0].Doc.Log.Disabled || asJSON(t, profiles[0].Doc) != asJSON(t, doc) {
		t.Errorf("imported %s", asJSON(t, profiles[0]))
	}
	if !reflect.DeepEqual(library, []*model.NetworkConfig{used}) {
		t.Errorf("library %s", asJSON(t, library))
	}
	all, _ := SerializeBundle([]*model.Profile{profile}, []*model.NetworkConfig{used, unused}, true)
	if _, library, _ := ParseImport(all, "linux"); len(library) != 2 {
		t.Errorf("export all kept %d configs", len(library))
	}
	// Backups exported before the rename to Kiko carry sbcfg-profiles.
	legacy := []byte(strings.Replace(string(serialized), `"kiko-profiles"`, `"sbcfg-profiles"`, 1))
	if profiles, library, err := ParseImport(legacy, "darwin"); err != nil || len(profiles) != 1 || profiles[0].ID != "profile-1" || len(library) != 1 {
		t.Errorf("legacy bundle: %d profiles, %d configs, %v", len(profiles), len(library), err)
	}
}

func TestImportsLegacyProfileArrays(t *testing.T) {
	profile := &model.Profile{ID: "legacy-profile", Name: "Legacy profile", SavedAt: 123,
		Doc: mustImport(t, `{"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "direct"}}`, "darwin")}
	profiles, library, err := ParseImport([]byte(asJSON(t, []*model.Profile{profile})), "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].ID != profile.ID || profiles[0].Name != profile.Name || profiles[0].Doc.Outbounds[0].Tag != "direct" || len(library) != 0 {
		t.Errorf("imported %s, %v", asJSON(t, profiles), library)
	}
}

func TestImportsSingBoxConfigWithVersion(t *testing.T) {
	profiles, _, err := ParseImport([]byte(`{"version": 1, "outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "direct"}}`), "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Doc.Platform != "darwin" || profiles[0].Doc.Outbounds[0].Tag != "direct" {
		t.Errorf("imported %s", asJSON(t, profiles))
	}
}

func TestParseImportRejectsOtherJSON(t *testing.T) {
	if _, _, err := ParseImport([]byte(`"text"`), "linux"); err == nil {
		t.Error("a string was imported")
	}
	if _, _, err := ParseImport([]byte(`{"format": "kiko-profiles", "schema": 3}`), "linux"); err == nil {
		t.Error("a newer bundle was imported")
	}
	if profiles, _, err := ParseImport([]byte(`{"name": "x"}`), "linux"); err != nil || len(profiles) != 0 {
		t.Errorf("unknown object: %v %v", profiles, err)
	}
}
