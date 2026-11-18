package routes

import (
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// scriptDoc is fixtureDoc with a rule script, a dns script, a route rule
// using the script condition and a DNS rule with the script action.
func scriptDoc() *model.Doc {
	doc := fixtureDoc()
	doc.HTTPTools = &model.HTTPTools{Scripts: []*model.HTTPScript{
		{ID: "s1", Name: "地区判断", Tag: "geo", Type: model.ScriptRule, Source: "$done(true)"},
		{ID: "s2", Name: "海外应答", Tag: "answer", Type: model.ScriptDNS, Source: "$done({addresses:['203.0.113.9']})"},
	}}
	doc.Route.Rules = append(doc.Route.Rules, &model.RouteRule{ID: "r-script", GroupID: "rg-app", Name: "脚本判定", Enabled: true,
		Action: model.ActionRoute, Outbound: model.Some("proxy"), Match: map[string]any{},
		ConditionTree: &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
			{Kind: model.ConditionMatch, Match: map[string]any{"domain_suffix": anyList([]string{"example.com"})}},
			{Kind: model.ConditionMatch, Match: map[string]any{"script": anyList([]string{"geo"})}}}}})
	doc.DNS.Rules = append(doc.DNS.Rules, &model.DNSRule{ID: "d-script", GroupID: "dg-local", DeviceID: model.LocalDeviceID, Name: "脚本应答",
		Enabled: true, Action: model.ActionScript, Script: "answer", OnError: model.ScriptOnErrorReject, Match: map[string]any{},
		ConditionTree: &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
			{Kind: model.ConditionMatch, Match: map[string]any{"domain_suffix": anyList([]string{"example.com"})}}}}})
	return doc
}

// shotTo writes the screenshot of the fixture in both themes.
func (f *fixture) shotTo(dir, name string) {
	f.t.Helper()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		suffix := "-light.png"
		if dark {
			suffix = "-dark.png"
		}
		f.tt.SetDark(dark)
		f.tt.Frame()
		f.tt.Advance(600 * time.Millisecond)
		f.tt.Frame()
		file, err := os.Create(filepath.Join(dir, name+suffix))
		if err != nil {
			f.t.Fatal(err)
		}
		if err := png.Encode(file, f.tt.Image()); err != nil {
			f.t.Fatal(err)
		}
		file.Close()
	}
	f.tt.SetDark(false)
	f.tt.Advance(600 * time.Millisecond)
	f.tt.Frame()
}

func TestPolicyTestShowsScriptMaybe(t *testing.T) {
	dir := os.Getenv("ROUTE_PREVIEW_DIR")
	f := newFixtureDoc(t, scriptDoc(), func(f *fixture) {
		f.srv.Route("POST", "/policy/test", func(w http.ResponseWriter, r *http.Request) {
			f.record(r)
			testkit.JSON(api.PolicyTestResult{Action: "route", Rule: "Apple 服务", RuleIndex: 1, OutboundPath: []string{"直连"},
				TestedAt: "1760000000000", ScriptMaybe: []string{"geo", "answer"}})(w, r)
		})
	})
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("测试规则")
	testkit.UntilText(t, tt, "测试路由规则")
	f.click("测试域名")
	tt.Type("www.example.com")
	f.click("运行测试")
	testkit.UntilText(t, tt, "最终出口")
	for _, text := range []string{"可能", "geo", "answer"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, tt.Texts())
		}
	}
	f.shotTo(dir, "70-route-test-maybe")
}

func TestRuleEditorShowsScriptCondition(t *testing.T) {
	dir := os.Getenv("ROUTE_PREVIEW_DIR")
	f := newFixtureDoc(t, scriptDoc(), nil)
	tt := f.tt
	testkit.UntilText(t, tt, "脚本判定")
	f.click("编辑 脚本判定")
	testkit.UntilText(t, tt, "编辑路由规则")
	for _, text := range []string{"脚本条件", "geo"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, tt.Texts())
		}
	}
	// The editor's own field (not the table's summary) sits below the fold.
	f.reveal("脚本条件", 1040, 450)
	tt.Scroll(1040, 450, 0, 300)
	tt.Frame()
	f.shotTo(dir, "71-rule-editor-script")
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		r := findRule(doc, "r-script")
		raw, _ := json.Marshal(r.ConditionTree)
		return r != nil && containsAll(string(raw), "script", "geo")
	})
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func TestDNSEditorScriptAction(t *testing.T) {
	dir := os.Getenv("ROUTE_PREVIEW_DIR")
	f := newFixtureDoc(t, scriptDoc(), nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("DNS 分流")
	testkit.UntilText(t, tt, "脚本应答")
	f.shotTo(dir, "73-dns-rules-script-row")
	if !tt.HasText("脚本 answer") {
		t.Fatalf("row target: %q", tt.Texts())
	}
	f.click("编辑 脚本应答")
	testkit.UntilText(t, tt, "编辑 DNS 分流规则")
	for _, text := range []string{"DNS 脚本", "失败时", "answer"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, tt.Texts())
		}
	}
	f.shotTo(dir, "72-dns-editor-script-action")
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		for _, r := range doc.DNS.Rules {
			if r.ID == "d-script" {
				return r.Action == model.ActionScript && r.Script == "answer" && r.OnError == model.ScriptOnErrorReject
			}
		}
		return false
	})
}
