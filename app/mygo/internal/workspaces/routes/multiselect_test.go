package routes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// TestRuleEditorRuleSetTokens picks two rule sets from the field's list,
// shown as chips, and saves them.
func TestRuleEditorRuleSetTokens(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	tt.SetSize(1280, 1600)
	tt.Frame()
	field := func() ui.Rect {
		for _, a := range tt.AccessAll() {
			if a.Placeholder == "选择规则集" || a.Name == "规则集" && a.Role == ui.RoleTextField {
				return a.Bounds
			}
		}
		t.Fatalf("no rule set field: %q", tt.Texts())
		return ui.Rect{}
	}
	first, second := model.BuiltinRuleSets[0], model.BuiltinRuleSets[1]
	r := field()
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	testkit.UntilText(t, tt, first.Tag+" · "+first.Desc)
	f.click(first.Tag + " · " + first.Desc)
	testkit.UntilText(t, tt, second.Tag+" · "+second.Desc)
	f.click(second.Tag + " · " + second.Desc)
	testkit.Until(t, tt, func() bool { return tt.HasText(first.Tag) && tt.HasText(second.Tag) })
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	f.snapshot("routes-editor-rulesets-light.png", false)
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(findRule(doc, "r-apple").ConditionTree)
		return strings.Contains(string(raw), first.Tag) && strings.Contains(string(raw), second.Tag)
	})
}
