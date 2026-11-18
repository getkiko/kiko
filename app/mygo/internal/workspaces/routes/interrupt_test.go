package routes

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestExitChanged(t *testing.T) {
	rule := func(enabled bool, action, outbound string) *model.RouteRule {
		r := &model.RouteRule{ID: "r", Enabled: enabled, Action: action}
		if outbound != "" {
			r.Outbound = model.Some(outbound)
		}
		return r
	}
	cases := []struct {
		name      string
		old, next *model.RouteRule
		want      bool
	}{
		{"another outbound", rule(true, model.ActionRoute, "direct"), rule(true, model.ActionRoute, "proxy"), true},
		{"no action is route", rule(true, "", "direct"), rule(true, "", "proxy"), true},
		{"same outbound", rule(true, model.ActionRoute, "direct"), rule(true, model.ActionRoute, "direct"), false},
		{"turned off while changing", rule(true, model.ActionRoute, "direct"), rule(false, model.ActionRoute, "proxy"), true},
		{"was off", rule(false, model.ActionRoute, "direct"), rule(true, model.ActionRoute, "proxy"), false},
		{"was reject", rule(true, model.ActionReject, ""), rule(true, model.ActionRoute, "proxy"), false},
		{"becomes reject", rule(true, model.ActionRoute, "direct"), rule(true, model.ActionReject, ""), false},
		{"had no outbound", rule(true, model.ActionRoute, ""), rule(true, model.ActionRoute, "proxy"), false},
		{"new rule", nil, rule(true, model.ActionRoute, "proxy"), false},
	}
	// Against a pending answer the connections are on the origin, not on
	// the stored outbound: back to the origin changes nothing.
	if exitChanged("direct", rule(true, model.ActionRoute, "proxy"), rule(true, model.ActionRoute, "direct")) {
		t.Error("a rule sent back to its origin asks")
	}
	if !exitChanged("direct", rule(true, model.ActionRoute, "proxy"), rule(true, model.ActionRoute, "third")) {
		t.Error("a second change away from the origin does not ask")
	}
	for _, c := range cases {
		from := ""
		if c.old != nil {
			from = c.old.Outbound.ID()
		}
		if got := exitChanged(from, c.old, c.next); got != c.want {
			t.Errorf("%s: exitChanged = %v, want %v", c.name, got, c.want)
		}
	}
}

func (f *fixture) running(on bool) {
	state := "stopped"
	if on {
		state = "running"
	}
	f.env.Server.SetRuntime(api.Runtime{State: state})
	f.tt.Frame()
}

// editExit opens the rule and chooses another outbound, as the editor's
// Target Outbound does.
func editExit(t *testing.T, f *fixture, id, outbound string) *ruleEditor {
	t.Helper()
	e := openGuarded(t, f, id, false)
	e.route.Outbound = model.RefOf(outbound)
	f.tt.Frame()
	return e
}

func ruleOutbound(f *fixture, id string) string { return findRule(f.saved(), id).Outbound.ID() }

// A changed outbound under a running core asks first; the answer is only
// recorded, and nothing is saved before it.
func TestChangedExitAsksBeforeSaving(t *testing.T) {
	for name, interrupt := range map[string]bool{"interrupt": true, "keep": false} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			testkit.UntilText(t, f.tt, "Apple 服务")
			f.running(true)
			e := editExit(t, f, "r-apple", "proxy")
			f.click("保存规则")
			testkit.UntilText(t, f.tt, "中断已有连接")
			if got := ruleOutbound(f, "r-apple"); got != "direct" || !e.open {
				t.Fatalf("saved before the answer: %q, editor open %v", got, e.open)
			}
			if interrupt {
				f.click("中断该规则下的连接")
				f.snapshot("5-interrupt-light.png", false)
				f.snapshot("5-interrupt-dark.png", true)
			}
			f.click("确定")
			f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-apple").Outbound.ID() == "proxy" })
			testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
			marked := slices.Contains(f.store.InterruptRules(), "r-apple")
			if marked != interrupt {
				t.Fatalf("interrupt mark %v, want %v (%v)", marked, interrupt, f.store.InterruptRules())
			}
		})
	}
}

// Leaving the question saves nothing and keeps the editor and its draft.
func TestChangedExitQuestionCanBeDismissed(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.running(true)
	e := editExit(t, f, "r-apple", "proxy")
	f.click("保存规则")
	testkit.UntilText(t, f.tt, "中断已有连接")
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if f.tt.HasText("中断已有连接") || !e.open || f.w.editor != e || e.route.Outbound.ID() != "proxy" {
		t.Fatalf("after Escape: question %v open %v outbound %q", f.tt.HasText("中断已有连接"), e.open, e.route.Outbound.ID())
	}
	if got := ruleOutbound(f, "r-apple"); got != "direct" || len(f.store.InterruptRules()) != 0 {
		t.Fatalf("dismissing saved %q or marked %v", got, f.store.InterruptRules())
	}
}

// The latest answer for a rule replaces the earlier one.
func TestChangedExitAnswerReplacesTheEarlierOne(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.running(true)
	if err := f.store.Update(func(doc *model.Doc) {
		spare := model.MakeOutbound("socks", "备用节点")
		spare.ID = "spare"
		spare.Fields["server"] = "203.0.113.6"
		spare.Fields["server_port"] = float64(1080)
		doc.Outbounds = append(doc.Outbounds, spare)
	}); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	editExit(t, f, "r-apple", "proxy")
	f.click("保存规则")
	testkit.UntilText(t, f.tt, "中断已有连接")
	f.click("中断该规则下的连接")
	f.click("确定")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	if !slices.Equal(f.store.InterruptRules(), []string{"r-apple"}) {
		t.Fatalf("marks %v", f.store.InterruptRules())
	}
	editExit(t, f, "r-apple", "spare")
	f.click("保存规则")
	testkit.UntilText(t, f.tt, "中断已有连接")
	f.click("确定")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	if len(f.store.InterruptRules()) != 0 {
		t.Fatalf("marks %v", f.store.InterruptRules())
	}
}

// No question: the core is stopped, the outbound stays, other fields
// change, the rule is new, or it is a DNS rule.
func TestNoQuestionWithoutAnExitChange(t *testing.T) {
	t.Run("core stopped", func(t *testing.T) {
		f := newFixture(t, nil)
		testkit.UntilText(t, f.tt, "Apple 服务")
		f.running(false)
		editExit(t, f, "r-apple", "proxy")
		f.click("保存规则")
		f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-apple").Outbound.ID() == "proxy" })
		if f.tt.HasText("中断已有连接") || len(f.store.InterruptRules()) != 0 {
			t.Fatalf("asked with the core stopped: %v", f.store.InterruptRules())
		}
	})
	t.Run("other field", func(t *testing.T) {
		f := newFixture(t, nil)
		testkit.UntilText(t, f.tt, "Apple 服务")
		f.running(true)
		e := openGuarded(t, f, "r-apple", true)
		f.click("保存规则")
		f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-apple").Name == *e.name() })
		if f.tt.HasText("中断已有连接") || len(f.store.InterruptRules()) != 0 {
			t.Fatal("asked for a rename")
		}
	})
	t.Run("dns rule", func(t *testing.T) {
		f := newFixture(t, nil)
		testkit.UntilText(t, f.tt, "Apple 服务")
		f.running(true)
		f.click("DNS 分流")
		f.tt.Frame()
		e := openGuarded(t, f, "d-cn", false)
		e.dnsRule.Detour = model.RefOf("proxy")
		f.tt.Frame()
		f.click("保存规则")
		f.waitSaved(func(doc *model.Doc) bool {
			for _, r := range doc.DNS.Rules {
				if r.ID == "d-cn" {
					return r.Detour.ID() == "proxy"
				}
			}
			return false
		})
		if f.tt.HasText("中断已有连接") || len(f.store.InterruptRules()) != 0 {
			t.Fatal("asked for a DNS rule")
		}
	})
}

// A → B (interrupt) → A: the rule is back where its connections are, so
// nothing asks and the mark is gone.
func TestExitSentBackClearsTheMark(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.running(true)
	editExit(t, f, "r-apple", "proxy")
	f.click("保存规则")
	testkit.UntilText(t, f.tt, "中断已有连接")
	f.click("中断该规则下的连接")
	f.click("确定")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	if from, ok := f.store.InterruptFrom("r-apple"); !ok || from != "direct" {
		t.Fatalf("mark %q %v", from, ok)
	}
	editExit(t, f, "r-apple", "direct")
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-apple").Outbound.ID() == "direct" })
	if f.tt.HasText("中断已有连接") || len(f.store.InterruptRules()) != 0 {
		t.Fatalf("asked, or the mark stayed: %v", f.store.InterruptRules())
	}
}

// A (applied) → B (keep) → A: back at the applied outbound nothing asks.
func TestBackToTheAppliedExitAsksNothing(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.running(true)
	f.store.NoteApplied(f.w.doc())
	editExit(t, f, "r-apple", "proxy")
	f.click("保存规则")
	testkit.UntilText(t, f.tt, "中断已有连接")
	f.click("确定")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-apple").Outbound.ID() == "proxy" })
	editExit(t, f, "r-apple", "direct")
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-apple").Outbound.ID() == "direct" })
	if f.tt.HasText("中断已有连接") || len(f.store.InterruptRules()) != 0 {
		t.Fatalf("asked back at the applied exit: %v", f.store.InterruptRules())
	}
}
