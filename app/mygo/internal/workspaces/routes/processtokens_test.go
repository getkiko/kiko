package routes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestProcessEntryParsing(t *testing.T) {
	for in, want := range map[string]model.ProcessSelection{
		"curl":                                  {Source: model.ProcessSourceCustom, Type: model.ProcessTypeName, Value: "curl"},
		"/usr/bin/curl":                         {Source: model.ProcessSourceCustom, Type: model.ProcessTypePath, Value: "/usr/bin/curl"},
		`^/Applications/.*\.app/`:               {Source: model.ProcessSourceCustom, Type: model.ProcessTypeRegex, Value: `^/Applications/.*\.app/`},
		"process_name:Google Chrome Helper":     {Source: model.ProcessSourceCustom, Type: model.ProcessTypeName, Value: "Google Chrome Helper"},
		"process_path_regex:^/opt/":             {Source: model.ProcessSourceCustom, Type: model.ProcessTypeRegex, Value: "^/opt/"},
		`C:\Windows\System32\curl.exe`:          {Source: model.ProcessSourceCustom, Type: model.ProcessTypePath, Value: `C:\Windows\System32\curl.exe`},
		`C:\Program Files (x86)\App\a.exe`:      {Source: model.ProcessSourceCustom, Type: model.ProcessTypePath, Value: `C:\Program Files (x86)\App\a.exe`},
		"name:curl":                             {Source: model.ProcessSourceCustom, Type: model.ProcessTypeName, Value: "curl"},
		"app:/Applications/Safari.app":          model.AppSelection("/Applications/Safari.app", "Safari"),
		"application:/Applications/Safari.app/": model.AppSelection("/Applications/Safari.app", "Safari"),
	} {
		got, problem := processEntry(in, "")
		if problem != "" || got != want {
			t.Errorf("processEntry(%q) = %+v %q", in, got, problem)
		}
	}
	for _, in := range []string{"process_path:curl", "process_path_regex:(", "application:Safari"} {
		if _, problem := processEntry(in, ""); problem == "" {
			t.Errorf("processEntry(%q) took a bad value", in)
		}
	}
	if got := model.AppSelection("/Applications/Safari.app/", "Safari").Value; got != `^/Applications/Safari\.app/` {
		t.Errorf("app regex %q", got)
	}
	// An application of an older document copies as its regex.
	if got := processClip(model.ProcessSelection{Source: model.ProcessSourceApplication, Type: model.ProcessTypeRegex, Value: "/Applications/Safari.app"}); got != `regex:^/Applications/Safari\.app/` {
		t.Errorf("old app clip %q", got)
	}
	// A copy reads back as it was.
	for _, item := range []model.ProcessSelection{
		{Source: model.ProcessSourceCustom, Type: model.ProcessTypePath, Value: "/usr/bin/curl"},
		{Source: model.ProcessSourceCustom, Type: model.ProcessTypeRegex, Value: `^/Applications/Safari\.app/`},
	} {
		if got, _ := processEntry(processClip(item), ""); got != item {
			t.Errorf("clip %q read back %+v", processClip(item), got)
		}
	}
}

// TestRuleEditorProcessTokens pastes processes into the Process field,
// copies one and removes it.
func TestRuleEditorProcessTokens(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	tt.SetSize(1280, 1600)
	tt.Frame()
	// The input sits right above the picker's button.
	button, ok := tt.Find("添加进程条件")
	if !ok {
		t.Fatalf("no process field: %q", tt.Texts())
	}
	tt.ClickAt(button.X+button.W/2, button.Y-16)
	tt.Frame()
	tt.SetClipboard("curl\n/usr/bin/ssh\nprocess_path:relative")
	tt.Key(ui.Cmd, ui.KeyV)
	testkit.Until(t, tt, func() bool { return tt.HasText("curl") && tt.HasText("ssh") })
	if !tt.HasText("1 条格式不正确：process_path:relative") {
		t.Fatalf("no paste problem: %q", tt.Texts())
	}
	f.snapshot("routes-editor-process-tokens-light.png", false)
	r, _ := tt.Find("ssh")
	tt.ClickAtWith(ui.Cmd, r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyC)
	tt.Frame()
	if got := tt.Clipboard(); got != "path:/usr/bin/ssh" {
		t.Fatalf("copied %q", got)
	}
	tt.Key(0, ui.KeyBackspace)
	testkit.Until(t, tt, func() bool { _, ok := tt.Find("ssh"); return !ok })
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(findRule(doc, "r-apple").ConditionTree)
		return strings.Contains(string(raw), `"curl"`) && !strings.Contains(string(raw), "/usr/bin/ssh")
	})
}
