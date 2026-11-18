package modules

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// standIn answers a download: scripts with a small script, mapping data
// with a small JSON document.
func standIn(res *Result) map[string][]byte {
	fetched := map[string][]byte{}
	for _, f := range res.Fetches {
		switch f.Kind {
		case FetchScript:
			fetched[f.URL] = []byte("$done({});")
		default:
			fetched[f.URL] = []byte("{}")
		}
	}
	return fetched
}

// summarize renders a result for the golden files: counts, the converted
// entries and the unsupported list.
func summarize(res *Result) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("name: %s", res.Name)
	line("desc: %s", res.Description)
	line("version: %s", res.Version)
	for _, a := range res.Arguments {
		line("argument: %s = %q", a.Name, a.Default)
	}
	counts := res.Contents.Counts()
	line("counts: hostnames=%d rewrites=%d scripts=%d rules=%d fetches=%d unsupported=%d", counts.Hostnames, counts.Rewrites, counts.Scripts, counts.Rules, len(res.Fetches), len(res.Unsupported))
	if h := res.Contents.Hostnames; h != nil {
		line("include: %s", strings.Join(h.Include, " "))
		line("exclude: %s", strings.Join(h.Exclude, " "))
	}
	if rw := res.Contents.Rewrites; rw != nil {
		for _, r := range rw.URL {
			line("url: %s %s -> %s", r.Action, r.Pattern, r.Target)
		}
		for _, r := range rw.Headers {
			for _, op := range r.Ops {
				line("header: %s %s %s %s %q %q", r.Scope, r.Pattern, op.Action, op.Name, op.Regex, op.Value)
			}
		}
		for _, r := range rw.Body {
			for _, e := range r.Edits {
				line("body: %s %s %s %q %q %q", r.Scope, r.Pattern, e.Action, e.Regex, e.Path, e.Value)
			}
		}
		for _, r := range rw.MapLocal {
			line("maplocal: %d %s headers=%v body=%q b64=%q", r.Status, r.Pattern, r.Headers, r.Body, r.BodyBase64)
		}
	}
	for _, s := range res.Contents.Scripts {
		line("script: %s %s tag=%s pattern=%s cron=%q body=%v bin=%v max=%d timeout=%d disabled=%v arg=%q source=%q", s.Name, s.Type, s.Tag, s.Pattern, s.Cron, s.RequiresBody, s.BinaryBody, s.MaxSize, s.Timeout, s.Disabled, s.Argument, s.Source)
	}
	for _, r := range res.Contents.Rules {
		line("rule: %s %s %s noresolve=%v", r.Type, r.Value, r.Action, r.NoResolve)
	}
	var fetches []string
	for _, f := range res.Fetches {
		fetches = append(fetches, f.Kind+" "+f.URL)
	}
	sort.Strings(fetches)
	for _, f := range fetches {
		line("fetch: %s", f)
	}
	for _, u := range res.Unsupported {
		line("unsupported: [%s:%d] %s | %s", u.Section, u.Line, u.Reason, u.Text)
	}
	return b.String()
}

func TestGoldenFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*.sgmodule")
	if err != nil || len(files) < 5 {
		t.Fatalf("fixtures: %v %v", files, err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".sgmodule")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			res, err := ParseSurge(string(data), Options{})
			if err != nil {
				t.Fatal(err)
			}
			missing, err := res.Resolve(standIn(res))
			if err != nil || len(missing) > 0 {
				t.Fatalf("resolve: %v missing %v", err, missing)
			}
			if issue := res.Contents.Issue(); issue != "" {
				t.Fatalf("contents invalid: %s", issue)
			}
			got := summarize(res)
			golden := filepath.Join("testdata", "golden", name+".txt")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("conversion of %s changed.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
			}
			// Every converted module becomes a valid HTTPModule.
			m := model.NewHTTPModule()
			m.Name = res.Name
			m.Source.Kind = model.ModuleSourceInline
			m.Raw = string(data)
			m.Contents = res.Contents
			if issue := m.Issue(); issue != "" {
				t.Errorf("module invalid: %s", issue)
			}
		})
	}
}

// With arguments the substituted values reach the converted entries.
func TestYouTubeArguments(t *testing.T) {
	data, _ := os.ReadFile("testdata/youtube_enhance.sgmodule")
	res, err := ParseSurge(string(data), Options{Arguments: map[string]string{"字幕翻译语言": "zh-CN", "屏蔽Shorts按钮": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := res.Resolve(standIn(res)); err != nil {
		t.Fatal(err)
	}
	arg := res.Contents.Scripts[0].Argument
	want := `{"captionLang":"zh-CN","blockUpload":true,"blockImmersive":true,"blockShorts":true,"debug":false}`
	if arg != want {
		t.Errorf("argument = %s, want %s", arg, want)
	}
	for _, a := range res.Arguments {
		if a.Name == "字幕翻译语言" && a.Value != "zh-CN" || a.Name == "启用调试模式" && a.Value != "" {
			t.Errorf("argument values: %+v", res.Arguments)
		}
	}
}

func readFixture(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join("testdata", name+".sgmodule"))
	return string(data), err
}
