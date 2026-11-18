package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// fakeRuleSets stands for the Routing workspace's rule set pages: a list and,
// when detail is set, a rule set's page with a text field.
type fakeRuleSets struct {
	opens, closes, builds, focusBacks int
	detail, focus                     bool
	query                             string
	back                              func()
}

func (f *fakeRuleSets) OpenRuleSets()      { f.opens++ }
func (f *fakeRuleSets) CloseRuleSets()     { f.closes++; f.detail = false }
func (f *fakeRuleSets) FocusRuleSetsBack() { f.focusBacks++; f.focus = true }
func (f *fakeRuleSets) RuleSetsEscape() bool {
	if !f.detail {
		return false
	}
	f.detail = false
	return true
}
func (f *fakeRuleSets) RuleSetsPage(c *ui.Context, back func()) {
	f.builds++
	f.back = back
	button := kit.Button(c, "返回", kit.ButtonOptions{})
	if f.focus {
		button.Focus()
		f.focus = false
	}
	if f.detail {
		ui.Text(c, "规则集详情")
		kit.Search(c, "q", &f.query, kit.SearchOptions{Placeholder: "搜索规则"}).Label("搜索规则")
		return
	}
	ui.Text(c, "规则集页面")
}

// Settings › Rule Sets opens the pages in place of the main page; their back
// returns to it, and leaving the workspace drops their state.
func TestRuleSetsSubpage(t *testing.T) {
	f := newFixture(t, nil)
	pages := &fakeRuleSets{}
	f.w.RuleSets = pages
	testkit.UntilText(t, f.tt, "规则集")
	if !f.tt.HasText("个规则集") {
		t.Errorf("row summary missing: %q", f.tt.Texts())
	}
	f.click("规则集")
	testkit.UntilText(t, f.tt, "规则集页面")
	if pages.opens != 1 || f.w.page != pageRuleSets || f.tt.HasText("通用网络配置") {
		t.Fatalf("opens %d page %q", pages.opens, f.w.page)
	}
	pages.back()
	f.tt.Frame()
	if f.w.page != pageMain || pages.closes != 1 || !f.tt.HasText("通用网络配置") {
		t.Fatalf("back: page %q closes %d", f.w.page, pages.closes)
	}
	f.w.Open("rulesets")
	f.tt.Frame()
	f.w.SetActive(false)
	if pages.closes != 2 {
		t.Errorf("leaving keeps the pages: closes %d", pages.closes)
	}
	f.w.SetActive(true)
	f.tt.Frame()
	if !f.tt.HasText("规则集页面") {
		t.Error("the page does not show again")
	}
}

// Escape on the rule set pages: in a text field it moves the focus to the
// page's back button, then returns from a rule set to the list, then to
// Settings.
func TestRuleSetsEscape(t *testing.T) {
	f := newFixture(t, nil)
	pages := &fakeRuleSets{}
	f.w.RuleSets = pages
	f.w.Open("rulesets")
	pages.detail = true
	testkit.UntilText(t, f.tt, "规则集详情")
	f.click("搜索规则")
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if pages.focusBacks != 1 || !pages.detail || f.w.page != pageRuleSets {
		t.Fatalf("text field escape: focusBacks %d detail %v page %q", pages.focusBacks, pages.detail, f.w.page)
	}
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if pages.detail || f.w.page != pageRuleSets {
		t.Fatalf("detail escape: detail %v page %q", pages.detail, f.w.page)
	}
	testkit.UntilText(t, f.tt, "规则集页面")
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if f.w.page != pageMain || pages.closes != 1 {
		t.Fatalf("list escape: page %q closes %d", f.w.page, pages.closes)
	}
}
