package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/common/mitm/hostmatch"
)

// The probe checks a config with the mitm option once, and info reports it.
func TestMitmSupported(t *testing.T) {
	probe := func(body string) (*server, string) {
		log := filepath.Join(t.TempDir(), "calls")
		s := &server{singBox: writeScript(t, "cat \"$3\" >> "+log+"\n"+body), dataDir: t.TempDir()}
		return s, log
	}
	s, log := probe("exit 0\n")
	if !s.mitmSupported() || !s.mitmSupported() {
		t.Fatal("check success must report mitm")
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != `{"mitm":{"enabled":false}}` {
		t.Fatalf("probe config %q", calls)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "runtime", "mitm-probe.json")); !os.IsNotExist(err) {
		t.Fatalf("probe config left behind: %v", err)
	}
	if s, _ := probe("echo 'unknown field \"mitm\"' >&2\nexit 1\n"); s.mitmSupported() {
		t.Fatal("check failure must hide mitm")
	}
	if (&server{}).mitmSupported() {
		t.Fatal("missing binary must hide mitm")
	}
	s, _ = probe("exit 0\n")
	s.rpc.groupProbeOnce.Do(func() {})
	rec := httptest.NewRecorder()
	s.info(rec, httptest.NewRequest(http.MethodGet, "/api/v1/info", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"mitm":true`) {
		t.Fatalf("info %s", body)
	}
}

// Older cores reject the mitm key, so the runtime config drops it unless
// the probe passed.
func TestInjectRuntimeDropsMitmWhenUnsupported(t *testing.T) {
	mitm := func() map[string]any {
		return map[string]any{"mitm": map[string]any{"enabled": true}}
	}
	s := &server{}
	s.rpc.probeOnce.Do(func() {})
	out, err := s.injectRuntime(mitm())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"mitm"`) {
		t.Fatalf("unsupported core received mitm: %s", out)
	}
	s = &server{singBox: writeScript(t, "exit 0\n"), dataDir: t.TempDir()}
	s.rpc.probeOnce.Do(func() {})
	out, err = s.injectRuntime(mitm())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"mitm"`) {
		t.Fatalf("supported core lost mitm: %s", out)
	}
}

// A core with the base mitm option but without the package F keys keeps the
// mitm section minus those keys.
func TestInjectRuntimeStripsPolicyKeysOnBaseCore(t *testing.T) {
	section := func() map[string]any {
		return map[string]any{"mitm": map[string]any{
			"enabled":               true,
			"hostnames":             map[string]any{"builtin_exclude": false, "include": []any{"a.com"}},
			"sources":               []any{"local"},
			"upstream":              map[string]any{"fingerprint": "chrome"},
			"quic":                  "allow",
			"pinning_skip_duration": "90m",
		}}
	}
	script := "if grep -q builtin_exclude \"$3\"; then exit 1; fi\nexit 0\n"
	s := &server{singBox: writeScript(t, script), dataDir: t.TempDir()}
	s.rpc.probeOnce.Do(func() {})
	out, err := s.injectRuntime(section())
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MITM map[string]any `json:"mitm"`
	}
	if err := json.Unmarshal(out, &config); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"enabled": true, "hostnames": map[string]any{"include": []any{"a.com"}}}
	if !reflect.DeepEqual(config.MITM, want) {
		t.Fatalf("mitm = %v", config.MITM)
	}
	s = &server{singBox: writeScript(t, "exit 0\n"), dataDir: t.TempDir()}
	s.rpc.probeOnce.Do(func() {})
	out, err = s.injectRuntime(section())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "pinning_skip_duration") {
		t.Fatalf("new core lost policy keys: %s", out)
	}
}

func TestInjectRuntimeStripsUpstreamExtKeys(t *testing.T) {
	section := func() map[string]any {
		return map[string]any{"mitm": map[string]any{
			"enabled":  true,
			"upstream": map[string]any{"fingerprint": "safari", "h2_profile": "chrome", "mirror_client_hello": true},
		}}
	}
	// A core that knows the policy options but not package R.
	script := "if grep -q h2_profile \"$3\"; then exit 1; fi\nexit 0\n"
	s := &server{singBox: writeScript(t, script), dataDir: t.TempDir()}
	s.rpc.probeOnce.Do(func() {})
	out, err := s.injectRuntime(section())
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MITM map[string]any `json:"mitm"`
	}
	if err := json.Unmarshal(out, &config); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"enabled": true, "upstream": map[string]any{"fingerprint": "safari"}}
	if !reflect.DeepEqual(config.MITM, want) {
		t.Fatalf("mitm = %v", config.MITM)
	}
	// A new core keeps them.
	s = &server{singBox: writeScript(t, "exit 0\n"), dataDir: t.TempDir()}
	s.rpc.probeOnce.Do(func() {})
	out, err = s.injectRuntime(section())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "h2_profile") || !strings.Contains(string(out), "mirror_client_hello") {
		t.Fatalf("new core lost the keys: %s", out)
	}
	only := map[string]any{"upstream": map[string]any{"h2_profile": "chrome"}}
	if !mitmUpstreamExtStrip(only) || len(only) != 0 {
		t.Fatalf("empty upstream stays: %v", only)
	}
}

func TestMITMDropUnknownBuiltin(t *testing.T) {
	entry := hostmatch.Builtin()[0]
	section := map[string]any{"hostnames": map[string]any{"disabled_builtin": []any{entry.ID, "no-such-id", " " + strings.ToUpper(entry.ID) + " "}}}
	dropped := mitmDropUnknownBuiltin(section)
	if len(dropped) != 1 || dropped[0] != "no-such-id" {
		t.Fatalf("dropped %v", dropped)
	}
	kept, _ := section["hostnames"].(map[string]any)["disabled_builtin"].([]string)
	if len(kept) != 2 {
		t.Fatalf("kept %v", kept)
	}
	only := map[string]any{"hostnames": map[string]any{"disabled_builtin": []any{"nope"}}}
	if dropped = mitmDropUnknownBuiltin(only); len(dropped) != 1 {
		t.Fatalf("dropped %v", dropped)
	}
	if _, ok := only["hostnames"].(map[string]any)["disabled_builtin"]; ok {
		t.Fatal("empty list kept")
	}
	if mitmDropUnknownBuiltin(map[string]any{"enabled": true}) != nil {
		t.Fatal("no hostnames")
	}
}
