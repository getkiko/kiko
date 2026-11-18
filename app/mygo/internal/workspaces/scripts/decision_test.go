package scripts

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestDecisionTypeLogic(t *testing.T) {
	if TypeLabel(model.ScriptRule) != "规则" || TypeLabel(model.ScriptDNS) != "DNS" {
		t.Fatal("type labels")
	}
	if logsPhaseName("rule") != "规则" || logsPhaseName("dns") != "DNS" {
		t.Fatal("phase names")
	}
	cases := map[[2]string]string{
		{model.ScriptRule, "true"}:        "匹配",
		{model.ScriptRule, "false"}:       "不匹配",
		{model.ScriptDNS, "pass"}:         "继续下一条规则",
		{model.ScriptDNS, "server dns-a"}: "转发到 dns-a",
		{model.ScriptDNS, "203.0.113.1"}:  "203.0.113.1",
		{model.ScriptDNS, ""}:             "",
	}
	for in, want := range cases {
		if got := DecisionLabel(in[0], in[1]); got != want {
			t.Errorf("DecisionLabel(%v) = %q, want %q", in, got, want)
		}
	}
	for _, typ := range []string{model.ScriptRule, model.ScriptDNS} {
		if template(typ) == template(model.ScriptHTTPRequest) || sourceIssue(template(typ)) != "" {
			t.Errorf("template of %s", typ)
		}
	}
	if !isDecisionType(model.ScriptRule) || isDecisionType(model.ScriptGeneric) {
		t.Fatal("isDecisionType")
	}
}

func TestRuleInputConversion(t *testing.T) {
	in, err := ruleInput{domain: " a.example.com ", port: "443", network: "tcp", queryType: "AAAA"}.apiInput(model.ScriptRule)
	if err != nil || in.Domain != "a.example.com" || in.Port != 443 || in.QueryType != "" {
		t.Fatalf("rule input %+v %v", in, err)
	}
	in, err = ruleInput{domain: "a.example.com", queryType: "AAAA"}.apiInput(model.ScriptDNS)
	if err != nil || in.QueryType != "AAAA" {
		t.Fatalf("dns input %+v %v", in, err)
	}
	if _, err = (ruleInput{domain: "a", port: "70000"}).apiInput(model.ScriptRule); err == nil {
		t.Fatal("port out of range accepted")
	}
	if _, err = (ruleInput{}).apiInput(model.ScriptRule); err == nil {
		t.Fatal("empty input accepted")
	}
}

func TestEditorDecisionType(t *testing.T) {
	e := &editor{enabled: true, test2: newTester(nil)}
	e.typ = typeIndex(model.ScriptRule)
	e.pattern, e.timeout = "x", "5"
	e.code.Reset("$done(true)")
	e.setType(typeIndex(model.ScriptDNS))
	if e.timeout != "5" || e.pattern != "x" && e.pattern != "" {
		t.Fatalf("switch between decision types %+v", e)
	}
	e.setType(typeIndex(model.ScriptHTTPRequest))
	if e.timeout != "" {
		t.Fatal("timeout unit change keeps the value")
	}
	e.setType(typeIndex(model.ScriptRule))
	e.timeout, e.tag = "150", "r1"
	s := e.script()
	if s.Type != model.ScriptRule || s.Timeout != 150 || s.Pattern != "" || s.RequiresBody || s.MaxSize != 0 {
		t.Fatalf("script %+v", s)
	}
	if issue := s.Issue(); issue != "" {
		t.Fatalf("issue %q", issue)
	}
	e.timeout = "5000"
	if (&Editors{}).compute(e).timeout.msg == "" {
		t.Fatal("5000 ms accepted")
	}
}

func TestRuleScriptTrialRun(t *testing.T) {
	doc := fixtureDoc(true)
	doc.HTTPTools.Scripts = append(doc.HTTPTools.Scripts, &model.HTTPScript{ID: "s-rule", Name: "国内域名", Type: model.ScriptRule, Source: "$done(true)\n"})
	f := newFixture(t, doc, 1280, 1000)
	tt := f.tt
	testkit.UntilText(t, tt, "国内域名")
	f.srv.SetEvaluate(func(api.ScriptEvalRequest) api.ScriptEvalResult {
		return api.ScriptEvalResult{Action: api.ScriptActionNone, DurationUS: 40, Decision: "true"}
	})
	e := f.openTest("s-rule")
	if tt.HasText("已抓取") || tt.HasText("手填") {
		t.Errorf("input choice for a rule script: %q", tt.Texts())
	}
	e.test2.rule.domain = "a.example.com"
	f.click("运行测试")
	testkit.UntilText(t, tt, "匹配")
	f.want("判定")
	got := f.srv.ScriptEvalRequests()[0]
	if got.Type != api.ScriptTypeRule || got.Rule == nil || got.Rule.Domain != "a.example.com" || got.Rule.Port != 443 || got.Request != nil {
		t.Fatalf("evaluate request %+v", got)
	}
}

// The trial form of a rule script stacks its field pairs on a narrow window and sets them side by
// side on a wide one.
func TestRuleScriptTrialFormStacksWhenNarrow(t *testing.T) {
	rows := func(width int) (port, protocol float64) {
		doc := fixtureDoc(true)
		doc.HTTPTools.Scripts = append(doc.HTTPTools.Scripts, &model.HTTPScript{ID: "s-rule", Name: "国内域名", Type: model.ScriptRule, Source: "$done(true)\n"})
		f := newFixture(t, doc, width, 1000)
		testkit.UntilText(t, f.tt, "国内域名")
		f.openTest("s-rule")
		a, ok := f.tt.Find("端口")
		b, ok2 := f.tt.Find("应用协议")
		if !ok || !ok2 {
			t.Fatalf("form labels missing in %q", f.tt.Texts())
		}
		return float64(a.Y), float64(b.Y)
	}
	if a, b := rows(1280); a != b {
		t.Errorf("wide: port at %v, protocol at %v", a, b)
	}
	if a, b := rows(420); b <= a {
		t.Errorf("narrow: port at %v, protocol at %v, want stacked", a, b)
	}
}
