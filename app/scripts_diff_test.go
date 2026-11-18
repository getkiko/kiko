package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/mitm"
	"github.com/sagernet/sing-box/script"
)

// TestScriptModelNeverExceedsCore compiles every script item the model
// accepted and the emitter wrote (corpus from mygo emit's
// TestDumpScriptCorpus, path in $SCRIPT_CORPUS) with the core's compile step.
// The core must accept all of them; only JavaScript syntax errors, which the
// model cannot see, are tolerated.
func TestScriptModelNeverExceedsCore(t *testing.T) {
	corpus := os.Getenv("SCRIPT_CORPUS")
	if corpus == "" {
		t.Skip("SCRIPT_CORPUS not set")
	}
	data, err := os.ReadFile(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	failures := 0
	for _, item := range items {
		text, _ := item["source"].(string)
		delete(item, "source")
		path, _ := item["source_path"].(string)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal([]any{item})
		var options []option.ScriptOptions
		if err := json.Unmarshal(raw, &options); err != nil {
			t.Errorf("core cannot decode %s: %v", raw, err)
			failures++
		} else {
			configs, err := mitm.CompileScriptConfigs(context.Background(), options, 2<<20)
			if err == nil {
				_, err = script.NewManager(script.ManagerOptions{}).Compile(configs)
			}
			if err != nil && !strings.Contains(strings.ToLower(err.Error()), "syntax") {
				t.Errorf("core rejects what the model accepts: %v\n  %s", err, raw)
				failures++
			}
		}
		if failures > 10 {
			t.FailNow()
		}
	}
	t.Logf("%d scripts compiled", len(items))
}

// The emitted max_size unit must mean the same bytes to the core's option
// type (byteformats: KB is 1000, KiB is 1024).
func TestScriptMaxSizeUnit(t *testing.T) {
	var options []option.ScriptOptions
	text := `[{"tag":"a","type":"generic","source_path":"http/x.js","max_size":"64KiB"}]`
	if err := json.Unmarshal([]byte(text), &options); err != nil {
		t.Fatal(err)
	}
	if got := options[0].MaxSize.Value(); got != 64<<10 {
		t.Fatalf("64KiB = %d bytes", got)
	}
}

// TestScriptSourceScanAgreesWithCore compares the model's source scan with
// the core's CheckSource on the corpus sources: no source the model accepts
// may be refused, and none the model's scan refuses may be accepted.
func TestScriptSourceScanAgreesWithCore(t *testing.T) {
	corpus := os.Getenv("SCRIPT_CORPUS")
	if corpus == "" {
		t.Skip("SCRIPT_CORPUS not set")
	}
	data, err := os.ReadFile(corpus + ".sources")
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []struct {
		Source string `json:"source"`
		Scan   bool   `json:"scan"`
		OK     bool   `json:"ok"`
	}
	if err := json.Unmarshal(data, &verdicts); err != nil {
		t.Fatal(err)
	}
	accepted, refused := 0, 0
	for i, v := range verdicts {
		err := script.CheckSource(v.Source, script.ScriptLimits)
		switch {
		case v.OK && err != nil:
			t.Errorf("source %d (%d bytes): model accepts, core refuses: %v", i, len(v.Source), err)
		case v.Scan && err == nil:
			t.Errorf("source %d (%d bytes): model scan refuses, core accepts", i, len(v.Source))
		}
		if v.OK {
			accepted++
		}
		if v.Scan {
			refused++
		}
	}
	t.Logf("%d sources, %d accepted by the model, %d refused by its scan", len(verdicts), accepted, refused)
}

// TestCronModelMatchesCore compiles a cron script for every expression of
// the corpus (mygo emit's TestDumpCronCorpus, $SCRIPT_CORPUS.cron) with the
// real core and requires the same verdict as the model's CronIssue.
func TestCronModelMatchesCore(t *testing.T) {
	corpus := os.Getenv("SCRIPT_CORPUS")
	if corpus == "" {
		t.Skip("SCRIPT_CORPUS not set")
	}
	data, err := os.ReadFile(corpus + ".cron")
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []struct {
		Expr string `json:"expr"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal(data, &verdicts); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("http", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("http/c.js", []byte("$done();"), 0o644); err != nil {
		t.Fatal(err)
	}
	compile := func(expr string) error {
		raw, _ := json.Marshal([]any{map[string]any{"tag": "c", "type": "cron", "cron": expr, "source_path": "http/c.js"}})
		var options []option.ScriptOptions
		if err := json.Unmarshal(raw, &options); err != nil {
			return err
		}
		configs, err := mitm.CompileScriptConfigs(context.Background(), options, 2<<20)
		if err == nil {
			_, err = script.NewManager(script.ManagerOptions{}).Compile(configs)
		}
		return err
	}
	if err := compile("* * * * *"); err != nil {
		t.Skipf("core has no cron support: %v", err)
	}
	mismatch := 0
	for _, v := range verdicts {
		err := compile(v.Expr)
		if (err == nil) != v.OK {
			t.Errorf("cron %q: model ok=%v, core err=%v", v.Expr, v.OK, err)
			if mismatch++; mismatch > 20 {
				t.FailNow()
			}
		}
	}
	t.Logf("%d expressions compared", len(verdicts))
}
