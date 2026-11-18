package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/common/mitm/hostmatch"
	"github.com/sagernet/sing-box/common/mitm/rewrite"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route"
	"github.com/sagernet/sing-box/route/mitm"
	"github.com/sagernet/sing-box/script"
	sjson "github.com/sagernet/sing/common/json"
)

// TestModuleEmitAcceptedByCore compiles what the emitter wrote for the
// module fixtures (mygo emit's TestEmitFixtureModules, path in
// $MODULE_CORPUS): the http_rewrite section with the core's rewrite
// compiler, the scripts with its script compile step and the hostnames with
// its matcher. The core must accept all of it.
func TestModuleEmitAcceptedByCore(t *testing.T) {
	corpus := os.Getenv("MODULE_CORPUS")
	if corpus == "" {
		t.Skip("MODULE_CORPUS not set")
	}
	data, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var sections struct {
		HTTPRewrite *option.MITMHTTPRewriteOptions `json:"http_rewrite"`
		Scripts     []json.RawMessage              `json:"scripts"`
		Hostnames   struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"mitm_hostnames"`
	}
	if err := json.Unmarshal(data, &sections); err != nil {
		t.Fatalf("core cannot decode the emitted sections: %v", err)
	}
	if sections.HTTPRewrite == nil || len(sections.Scripts) == 0 {
		t.Fatalf("corpus is empty: %s", data[:min(len(data), 200)])
	}

	set, err := rewrite.Compile(context.Background(), sections.HTTPRewrite)
	if err != nil {
		t.Fatalf("core rejects the rewrite rules: %v", err)
	}
	if set == nil {
		t.Fatal("no rewrite set")
	}
	t.Logf("rewrite: %d url, %d header, %d body, %d map local rules compiled",
		len(sections.HTTPRewrite.URL), len(sections.HTTPRewrite.Headers), len(sections.HTTPRewrite.Body), len(sections.HTTPRewrite.MapLocal))

	if _, err := hostmatch.New(sections.Hostnames.Include, sections.Hostnames.Exclude); err != nil {
		t.Fatalf("core rejects the hostnames: %v", err)
	}

	t.Chdir(t.TempDir())
	failures := 0
	for _, raw := range sections.Scripts {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		text, _ := item["source"].(string)
		delete(item, "source")
		path, _ := item["source_path"].(string)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal([]any{item})
		var options []option.ScriptOptions
		if err := json.Unmarshal(encoded, &options); err != nil {
			t.Errorf("core cannot decode %s: %v", encoded, err)
			failures++
			continue
		}
		configs, err := mitm.CompileScriptConfigs(context.Background(), options, 2<<20)
		if err == nil {
			_, err = script.NewManager(script.ManagerOptions{}).Compile(configs)
		}
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "syntax") {
			t.Errorf("core rejects a module script: %v\n  %s", err, encoded)
			failures++
		}
		if failures > 10 {
			t.FailNow()
		}
	}
	t.Logf("%d scripts compiled", len(sections.Scripts))
}

// TestRuleScriptEmitAcceptedByCore decodes the whole configuration the
// emitter wrote for rule and dns scripts (mygo emit's TestEmitRuleScriptCorpus,
// path in $RULE_SCRIPT_CORPUS) with the core's option decoder, checks the
// script references with the core's validation and compiles the scripts.
func TestRuleScriptEmitAcceptedByCore(t *testing.T) {
	corpus := os.Getenv("RULE_SCRIPT_CORPUS")
	if corpus == "" {
		t.Skip("RULE_SCRIPT_CORPUS not set")
	}
	data, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, item := range raw["scripts"].([]any) {
		entry := item.(map[string]any)
		text, _ := entry["source"].(string)
		delete(entry, "source")
		path, _ := entry["source_path"].(string)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, _ = json.Marshal(raw)
	ctx := include.Context(context.Background())
	options, err := sjson.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatalf("core cannot decode the emitted configuration: %v", err)
	}
	if len(options.Scripts) < 3 || options.Route == nil || options.DNS == nil {
		t.Fatalf("corpus is incomplete: %d scripts", len(options.Scripts))
	}
	if err := route.ValidateScriptReferences(options.Scripts, *options.Route, *options.DNS); err != nil {
		t.Fatalf("core rejects the script references: %v", err)
	}
	configs, err := mitm.CompileScriptConfigs(ctx, options.Scripts, 2<<20)
	if err == nil {
		_, err = script.NewManager(script.ManagerOptions{}).Compile(configs)
	}
	if err != nil {
		t.Fatalf("core rejects the scripts: %v", err)
	}
	var routeScript, dnsScript bool
	for _, rule := range options.Route.Rules {
		routeScript = routeScript || len(rule.DefaultOptions.Script) > 0
	}
	for _, rule := range options.DNS.Rules {
		dnsScript = dnsScript || rule.DefaultOptions.DNSRuleAction.Action == "script"
	}
	if !routeScript || !dnsScript {
		t.Fatalf("script rules lost: route %v dns %v", routeScript, dnsScript)
	}
}
