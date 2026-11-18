package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestScriptsProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	log := filepath.Join(t.TempDir(), "calls")
	s := &server{singBox: writeScript(t, "cat \"$3\" >> "+log+"\nexit 0\n"), dataDir: t.TempDir()}
	if !s.scriptsSupported() || !s.scriptsSupported() {
		t.Fatal("check success must report scripts")
	}
	if calls, _ := os.ReadFile(log); string(calls) != `{"scripts":[]}` {
		t.Fatalf("probe configs %q", calls)
	}
	older := &server{singBox: writeScript(t, "if grep -q '\"scripts\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}
	if older.scriptsSupported() {
		t.Fatal("older core must not report scripts")
	}
	if (&server{}).scriptsSupported() {
		t.Fatal("missing binary must not report scripts")
	}
}

func scriptsFixture(source string) map[string]any {
	return map[string]any{"scripts": []any{map[string]any{"tag": "a", "type": "generic", "source_path": "http/forged.js", "source": source}}}
}

func TestInjectRuntimeScripts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	run := func(script string, dataDir string) (map[string]any, string) {
		s := &server{singBox: writeScript(t, script), dataDir: dataDir, logs: newLogBuffer(t.TempDir())}
		s.rpc.probeOnce.Do(func() {})
		out, err := s.injectRuntime(scriptsFixture("$done({});"))
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(out, &config); err != nil {
			t.Fatal(err)
		}
		return config, strings.Join(s.logs.snapshot(), "\n")
	}
	dir := t.TempDir()
	config, _ := run("exit 0\n", dir)
	sum := sha256.Sum256([]byte("$done({});"))
	rel := "http/" + hex.EncodeToString(sum[:]) + ".js"
	item := config["scripts"].([]any)[0].(map[string]any)
	if item["source_path"] != rel || item["source"] != nil {
		t.Fatalf("item = %v", item)
	}
	if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || string(data) != "$done({});" {
		t.Fatalf("file = %q %v", data, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "http")); len(entries) != 1 {
		t.Fatalf("stray files: %v", entries)
	}
	older, log := run("if grep -q '\"scripts\"' \"$3\"; then exit 1; fi\nexit 0\n", t.TempDir())
	if older["scripts"] != nil || !strings.Contains(log, "不支持脚本") {
		t.Fatalf("older core kept scripts: %v %q", older["scripts"], log)
	}
}

func TestScriptsMaterializeRejectsHuge(t *testing.T) {
	s := &server{dataDir: t.TempDir()}
	if err := s.scriptsMaterialize(scriptsFixture(strings.Repeat("a", maxScriptSourceBytes+1))); err == nil {
		t.Fatal("huge script accepted")
	}
	if !scriptsStrip(scriptsFixture("x")) || scriptsStrip(map[string]any{}) {
		t.Fatal("strip result")
	}
}

func TestCoreCheckRunsInDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	out := filepath.Join(t.TempDir(), "cwd")
	dataDir := t.TempDir()
	s := &server{singBox: writeScript(t, "pwd -P > "+out+"\nexit 0\n"), dataDir: dataDir}
	if err := s.coreCheckCommand(context.Background(), filepath.Join(dataDir, "c.json")).Run(); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dataDir)
	if got, _ := os.ReadFile(out); strings.TrimSpace(string(got)) != want {
		t.Fatalf("check ran in %q, want %q", got, want)
	}
}

// With KIKO_TEST_CORE set (a core built from the submodule), a real
// `check` from another working directory accepts a configuration whose
// script source sits in the data directory.
func TestRealCoreCheckWithScript(t *testing.T) {
	binary := os.Getenv("KIKO_TEST_CORE")
	if binary == "" {
		t.Skip("KIKO_TEST_CORE not set")
	}
	dataDir := t.TempDir()
	s := &server{singBox: binary, dataDir: dataDir, logs: newLogBuffer(t.TempDir())}
	config := scriptsFixture("$done({});")
	config["scripts"].([]any)[0].(map[string]any)["type"] = "http-response"
	config["scripts"].([]any)[0].(map[string]any)["pattern"] = `^https://a\.test/`
	config["scripts"].([]any)[0].(map[string]any)["max_size"] = "64KiB"
	config["scripts"].([]any)[0].(map[string]any)["timeout"] = "5s"
	if err := s.scriptsMaterialize(config); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(config)
	path := filepath.Join(dataDir, "runtime", "candidate.json")
	if err := atomicWrite(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if out, err := s.coreCheckCommand(context.Background(), path).CombinedOutput(); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	bare := exec.Command(binary, "check", "-c", path)
	bare.Dir = t.TempDir()
	if out, err := bare.CombinedOutput(); err == nil {
		t.Fatalf("check outside the data directory passed: %s", out)
	}
}

func TestScriptsMaterializeKeepsOnlyOwnPaths(t *testing.T) {
	s := &server{dataDir: t.TempDir()}
	good := "http/" + strings.Repeat("a", 64) + ".js"
	config := map[string]any{"scripts": []any{
		map[string]any{"tag": "a", "source_path": "../../etc/passwd"},
		map[string]any{"tag": "b", "source_path": good},
		map[string]any{"tag": "c", "source_path": "http/" + strings.Repeat("A", 64) + ".js"},
		map[string]any{"tag": "d"},
	}}
	if err := s.scriptsMaterialize(config); err != nil {
		t.Fatal(err)
	}
	items := config["scripts"].([]any)
	for i, want := range []any{nil, good, nil, nil} {
		if got := items[i].(map[string]any)["source_path"]; got != want {
			t.Errorf("item %d source_path = %v, want %v", i, got, want)
		}
	}
}

func TestScriptsPrune(t *testing.T) {
	dir := t.TempDir()
	s := &server{dataDir: dir}
	name := func(c string) string { return "http/" + strings.Repeat(c, 64) + ".js" }
	write := func(c string, age time.Duration) {
		path := filepath.Join(dir, filepath.FromSlash(name(c)))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// a: old and unreferenced; b: old, in the current config; c: old, in the
	// last applied runtime config; d: recent; e: old but not ours by name.
	write("a", 48*time.Hour)
	write("b", 48*time.Hour)
	write("c", 48*time.Hour)
	write("d", time.Hour)
	other := filepath.Join(dir, "http", "notes.js")
	_ = os.WriteFile(other, []byte("x"), 0o600)
	_ = os.Chtimes(other, time.Now().Add(-72*time.Hour), time.Now().Add(-72*time.Hour))
	if err := atomicWrite(filepath.Join(dir, "runtime", "config.json"), []byte(`{"scripts":[{"source_path":"`+name("c")+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.scriptsPrune([]any{map[string]any{"source_path": name("b")}}, time.Now())
	exists := func(c string) bool {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name(c))))
		return err == nil
	}
	if exists("a") || !exists("b") || !exists("c") || !exists("d") {
		t.Fatalf("prune: a=%v b=%v c=%v d=%v", exists("a"), exists("b"), exists("c"), exists("d"))
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("foreign file removed")
	}
}

func TestScriptsCronProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	log := filepath.Join(t.TempDir(), "calls")
	s := &server{singBox: writeScript(t, "cat \"$3\" >> "+log+"\nexit 0\n"), dataDir: t.TempDir()}
	if !s.scriptsCronSupported() || !s.scriptsCronSupported() {
		t.Fatal("check success must report cron")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), `"type":"cron"`) || !strings.Contains(string(calls), `"enabled":false`) || strings.Count(string(calls), `"scripts"`) != 2 {
		t.Fatalf("probe configs %q", calls)
	}
	// package L core: knows scripts but not the cron type.
	older := &server{singBox: writeScript(t, "if grep -q '\"cron\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}
	if !older.scriptsSupported() || older.scriptsCronSupported() {
		t.Fatal("core without cron")
	}
	if (&server{singBox: writeScript(t, "if grep -q '\"scripts\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}).scriptsCronSupported() {
		t.Fatal("core without scripts must not report cron")
	}
}

func TestScriptsCronStrip(t *testing.T) {
	config := map[string]any{"scripts": []any{
		map[string]any{"tag": "a", "type": "generic"}, map[string]any{"tag": "c", "type": "cron"}, map[string]any{"tag": "d", "type": "http-request"},
	}}
	if n := scriptsCronStrip(config); n != 1 {
		t.Fatal(n)
	}
	list := config["scripts"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["tag"] != "a" || list[1].(map[string]any)["tag"] != "d" {
		t.Fatalf("kept %v", list)
	}
	only := map[string]any{"scripts": []any{map[string]any{"tag": "c", "type": "cron"}}}
	if scriptsCronStrip(only) != 1 || only["scripts"] != nil {
		t.Fatalf("only cron: %v", only)
	}
	if scriptsCronStrip(map[string]any{}) != 0 || scriptsCronStrip(scriptsFixture("x")) != 0 {
		t.Fatal("nothing to strip")
	}
}

func TestInjectRuntimeStripsCronOnOlderCore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	s := &server{singBox: writeScript(t, "if grep -q '\"cron\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir(), logs: newLogBuffer(t.TempDir())}
	s.rpc.probeOnce.Do(func() {})
	config := scriptsFixture("$done({});")
	list := config["scripts"].([]any)
	config["scripts"] = append(list, map[string]any{"tag": "c", "type": "cron", "cron": "* * * * *", "source": "$done();", "source_path": "http/x.js"})
	out, err := s.injectRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	items := got["scripts"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["tag"] != "a" || !strings.Contains(strings.Join(s.logs.snapshot(), "\n"), "不支持定时脚本") {
		t.Fatalf("scripts %v logs %v", items, s.logs.snapshot())
	}
}

func TestScriptsRuleProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	log := filepath.Join(t.TempDir(), "calls")
	s := &server{singBox: writeScript(t, "cat \"$3\" >> "+log+"\nexit 0\n"), dataDir: t.TempDir()}
	if !s.scriptsRuleSupported() || !s.scriptsRuleSupported() {
		t.Fatal("check success must report rule scripts")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), `"type":"rule"`) || !strings.Contains(string(calls), `"type":"dns"`) || !strings.Contains(string(calls), `"enabled":false`) {
		t.Fatalf("probe configs %q", calls)
	}
	// package N core: knows scripts and cron but not rule and dns.
	older := &server{singBox: writeScript(t, "if grep -q '\"type\":\"rule\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}
	if !older.scriptsSupported() || older.scriptsRuleSupported() {
		t.Fatal("core without rule scripts")
	}
	if (&server{singBox: writeScript(t, "if grep -q '\"scripts\"' \"$3\"; then exit 1; fi\nexit 0\n"), dataDir: t.TempDir()}).scriptsRuleSupported() {
		t.Fatal("core without scripts must not report rule scripts")
	}
}

func TestScriptsRuleStrip(t *testing.T) {
	config := map[string]any{
		"scripts": []any{
			map[string]any{"tag": "a", "type": "generic"}, map[string]any{"tag": "r", "type": "rule"}, map[string]any{"tag": "d", "type": "dns"},
		},
		"route": map[string]any{"rules": []any{
			map[string]any{"domain": []any{"a.test"}, "action": "route", "outbound": "x"},
			map[string]any{"script": []any{"r"}, "action": "reject"},
			map[string]any{"type": "logical", "mode": "and", "rules": []any{map[string]any{"domain": []any{"b.test"}}, map[string]any{"script": []any{"r"}}}, "action": "reject"},
		}},
		"dns": map[string]any{"rules": []any{
			map[string]any{"domain": []any{"a.test"}, "server": "s"},
			map[string]any{"action": "script", "dns_script": "d"},
			map[string]any{"domain": []any{"c.test"}, "script": []any{"r"}, "server": "s"},
		}},
	}
	if n, r := scriptsRuleStrip(config); n != 2 || r != 4 {
		t.Fatal(n, r)
	}
	if list := config["scripts"].([]any); len(list) != 1 || list[0].(map[string]any)["tag"] != "a" {
		t.Fatalf("scripts %v", list)
	}
	if rules := config["route"].(map[string]any)["rules"].([]any); len(rules) != 1 {
		t.Fatalf("route rules %v", rules)
	}
	if rules := config["dns"].(map[string]any)["rules"].([]any); len(rules) != 1 || rules[0].(map[string]any)["server"] != "s" {
		t.Fatalf("dns rules %v", rules)
	}
	only := map[string]any{"scripts": []any{map[string]any{"tag": "r", "type": "rule"}}}
	if n, _ := scriptsRuleStrip(only); n != 1 || only["scripts"] != nil {
		t.Fatalf("only rule: %v", only)
	}
	if n, r := scriptsRuleStrip(map[string]any{}); n != 0 || r != 0 {
		t.Fatal("nothing to strip")
	}
}
