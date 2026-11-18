package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const updatedAt = 1760000000000

// ruleSetFixture shows the Rule Sets pages over a document with a remote
// rule set the core loaded (my-remote, a.example and b.example), one it
// fails to update (bad-remote), and an inline JSON one.
func ruleSetFixture(t *testing.T, edit func(doc *model.Doc)) (*fixture, *ruleSetCalls) {
	t.Helper()
	calls := &ruleSetCalls{}
	doc := fixtureDoc()
	doc.Route.RuleSets = append(doc.Route.RuleSets,
		&model.RuleSet{ID: "rs-remote", Tag: "my-remote", Enabled: true, Mode: "remote", URL: "https://example.com/my.srs", UpdateInterval: "24h0m0s"},
		&model.RuleSet{ID: "rs-bad", Tag: "bad-remote", Enabled: true, Mode: "remote", URL: "https://example.com/bad.srs", UpdateInterval: "24h0m0s"},
		&model.RuleSet{ID: "rs-inline", Tag: "my-inline", Enabled: true, Mode: "inline", URL: "https://example.com/my.json",
			Rules: []any{map[string]any{"domain": []any{"old.example"}}}})
	if edit != nil {
		edit(doc)
	}
	f := newFixtureDoc(t, doc, func(f *fixture) {
		f.srv.Route("GET", "/rulesets/status", func(w http.ResponseWriter, r *http.Request) {
			calls.mu.Lock()
			at := calls.updatedAt
			calls.mu.Unlock()
			testkit.JSON(map[string]any{"ruleSets": []map[string]any{
				{"tag": "Gfw", "lastUpdatedAt": updatedAt}, {"tag": "my-remote", "lastUpdatedAt": at}, {"tag": "bad-remote", "lastUpdatedAt": 0}}})(w, r)
		})
		f.srv.Route("POST", "/rulesets/update", func(w http.ResponseWriter, r *http.Request) {
			var body struct{ Tag string }
			_ = testkit.Body(r, &body)
			calls.mu.Lock()
			calls.updates = append(calls.updates, body.Tag)
			calls.updatedAt = updatedAt + 3600000
			at := calls.updatedAt
			calls.mu.Unlock()
			if body.Tag == "bad-remote" {
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "下载失败：HTTP 404"})
				return
			}
			testkit.JSON(map[string]any{"tag": body.Tag, "lastUpdatedAt": at})(w, r)
		})
		f.srv.Route("POST", "/rulesets/source", func(w http.ResponseWriter, r *http.Request) {
			var body struct{ Tag, URL string }
			_ = testkit.Body(r, &body)
			calls.mu.Lock()
			calls.sources = append(calls.sources, body.Tag)
			calls.mu.Unlock()
			testkit.JSON(map[string]any{"version": 3, "from": "core", "rules": []any{
				map[string]any{"domain": []any{"a.example", "b.example"}},
				map[string]any{"domain": []any{"p.example"}, "port": []any{443}}}})(w, r)
		})
	})
	f.srv.SetFetchBody("https://example.com/my.json", `{"version":2,"rules":[{"domain_suffix":["new.example"]}]}`)
	f.ruleSets = true
	f.tt.SetSize(1280, 1100)
	f.tt.Frame()
	return f, calls
}

type ruleSetCalls struct {
	mu        sync.Mutex
	updates   []string
	sources   []string
	updatedAt int64
}

func (r *ruleSetCalls) updated() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.updates...)
}

func storedRuleSet(doc *model.Doc, id string) *model.RuleSet {
	for _, r := range doc.Route.RuleSets {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// The list opens a rule set's page; its back button, then Escape, return
// to the list, and Escape on the list to Settings.
func TestRuleSetPagesNavigate(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	for _, text := range []string{"规则集", "Gfw", "内置 · " + formatRuleSetUpdate(updatedAt), "远程 srs", "内联 · 1 条", "下载客户端", "全部更新"} {
		testkit.UntilText(t, tt, text)
	}
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "返回规则集列表")
	testkit.UntilText(t, tt, "b.example")
	if f.w.ruleSets.detail == nil || f.w.editing {
		t.Fatal("the page holds the store's edit without changes")
	}
	f.click("返回规则集列表")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.detail == nil })
	f.click("打开规则集 my-inline")
	testkit.UntilText(t, tt, "old.example")
	// In a text field, a first Escape leaves the field for the back button.
	f.click("搜索规则")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if f.w.ruleSets.detail == nil {
		t.Fatal("Escape in the search left the page")
	}
	if r, ok := tt.Find("返回规则集列表"); !ok || r.W == 0 {
		t.Fatal("no back button")
	}
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.detail == nil && tt.HasText("my-remote") })
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return f.backs == 1 })
}

// Update updates a remote rule set by the core and an inline one from its
// link; a failure shows by its row.
func TestRuleSetManualUpdate(t *testing.T) {
	f, calls := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.statusLoaded })
	f.click("更新规则集 my-remote")
	testkit.Until(t, tt, func() bool { return len(calls.updated()) == 1 })
	testkit.UntilText(t, tt, "远程 srs · "+formatRuleSetUpdate(updatedAt+3600000))
	f.click("更新规则集 bad-remote")
	testkit.UntilText(t, tt, "更新失败：下载失败：HTTP 404")
	f.click("更新规则集 my-inline")
	f.waitSaved(func(doc *model.Doc) bool {
		r := storedRuleSet(doc, "rs-inline")
		return r != nil && r.LastUpdatedAt > 0 && len(r.Rules) == 1 && strings.Contains(fmt.Sprint(r.Rules), "new.example")
	})
	// A catalog set the core did not load cannot update.
	if f.w.ruleSets.updateBlocker(model.DefaultBuiltinRuleSet(model.BuiltinRuleSets[2])) == "" {
		t.Error("geosite-cn updates without the core")
	}
	// On its page: "Update now" reads the source again.
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "a.example")
	calls.mu.Lock()
	before := len(calls.sources)
	calls.mu.Unlock()
	f.click("更新规则集 my-remote")
	testkit.Until(t, tt, func() bool {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		return len(calls.sources) > before && len(calls.updates) == 3
	})
}

// Entries of a remote rule set are removed, added and taken back; the
// overrides are stored with the rule set.
func TestRuleSetOverridesEdit(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "b.example")
	if !tt.HasText("组合规则") || !tt.HasText("3 条") {
		t.Fatalf("content %q", tt.Texts())
	}
	f.click("移除 b.example")
	f.waitSaved(func(doc *model.Doc) bool {
		o := storedRuleSet(doc, "rs-remote").Overrides
		return o != nil && len(o.Remove) == 1 && o.Remove[0] == model.RuleSetEntry{Field: "domain", Value: "b.example"}
	})
	testkit.UntilText(t, tt, "已移除")
	f.click("撤销移除 b.example")
	f.waitSaved(func(doc *model.Doc) bool { return storedRuleSet(doc, "rs-remote").Overrides == nil })
	testkit.Until(t, tt, func() bool { return !tt.HasText("已移除") })
	// "Add rule" adds one value after another.
	f.click("添加规则")
	tt.Frame()
	tt.Type("c.example")
	tt.Key(0, ui.KeyEnter)
	f.waitSaved(func(doc *model.Doc) bool {
		o := storedRuleSet(doc, "rs-remote").Overrides
		return o != nil && len(o.Add) == 1 && o.Add[0] == model.RuleSetEntry{Field: "domain", Value: "c.example"}
	})
	testkit.UntilText(t, tt, "已添加")
	tt.Type("d.example")
	f.click("添加")
	f.waitSaved(func(doc *model.Doc) bool { return len(storedRuleSet(doc, "rs-remote").Overrides.Add) == 2 })
	// What the source or the overrides have is not added twice.
	tt.Type("a.example")
	tt.Key(0, ui.KeyEnter)
	testkit.UntilText(t, tt, "规则已存在")
	f.click("撤销添加 c.example")
	f.waitSaved(func(doc *model.Doc) bool {
		o := storedRuleSet(doc, "rs-remote").Overrides
		return o != nil && len(o.Add) == 1 && o.Add[0].Value == "d.example"
	})
	// The list sums the overrides up.
	f.click("返回规则集列表")
	testkit.UntilText(t, tt, "远程 srs · +1")
}

// A catalog set shown by its defaults is stored as its overrides change.
func TestRuleSetOverridesOfCatalogSet(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Gfw")
	f.click("打开规则集 Gfw")
	testkit.UntilText(t, tt, "a.example")
	f.click("移除 a.example")
	f.waitSaved(func(doc *model.Doc) bool {
		r := storedRuleSet(doc, model.BuiltinRuleSetID("Gfw"))
		return r != nil && r.Mode == "remote" && r.Overrides != nil && len(r.Overrides.Remove) == 1
	})
}

// The search filters the entries after typing pauses.
func TestRuleSetContentSearch(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "b.example")
	f.click("搜索规则")
	tt.Type("b.ex")
	tt.Frame()
	if !tt.HasText("a.example") {
		t.Fatal("the search applies before typing pauses")
	}
	tt.Advance(searchDelay)
	testkit.Until(t, tt, func() bool { return !tt.HasText("a.example") && tt.HasText("b.example") && tt.HasText("1 / 3 条") })
	tt.Type("zzz")
	tt.Advance(searchDelay)
	testkit.UntilText(t, tt, "没有匹配的规则")
	f.click("清除搜索")
	testkit.UntilText(t, tt, "a.example")
}

// A rule set of 200 000 entries loads in the background: frames go on
// while it loads, and build only the rows in view.
func TestRuleSetLargeContent(t *testing.T) {
	const n = 200000
	values := make([]any, n)
	for i := range values {
		values[i] = fmt.Sprintf("d%d.example", i)
	}
	f, _ := ruleSetFixture(t, func(doc *model.Doc) {
		doc.Route.RuleSets = append(doc.Route.RuleSets, &model.RuleSet{ID: "rs-big", Tag: "big", Enabled: true, Mode: "inline",
			URL: "https://example.com/big.json", Rules: []any{map[string]any{"domain_suffix": values}}})
	})
	tt := f.tt
	testkit.UntilText(t, tt, "big")
	f.click("打开规则集 big")
	var slowest time.Duration
	testkit.Until(t, tt, func() bool {
		start := time.Now()
		tt.Frame()
		slowest = max(slowest, time.Since(start))
		return tt.HasText("200,000 条")
	})
	shown := 0
	for _, text := range tt.Texts() {
		if strings.HasSuffix(text, ".example") {
			shown++
		}
	}
	if shown == 0 || shown > 60 {
		t.Errorf("%d rows built", shown)
	}
	if slowest > 500*time.Millisecond {
		t.Errorf("a frame took %v while loading", slowest)
	}
	// Saving an override does not read the set again.
	loads := f.w.ruleSets.detail.content.loads
	f.click("移除 d0.example")
	f.waitSaved(func(doc *model.Doc) bool { return storedRuleSet(doc, "rs-big").Overrides != nil })
	testkit.UntilText(t, tt, "已移除")
	if got := f.w.ruleSets.detail.content.loads; got != loads {
		t.Errorf("the source loaded again: %d loads, was %d", got, loads)
	}
	f.click("搜索规则")
	tt.Type("d199999")
	tt.Advance(searchDelay)
	testkit.Until(t, tt, func() bool { return tt.HasText("1 / 200,000 条") && tt.HasText("d199999.example") })
}

// An update under way outlives the pages: opened again, they show it, and
// it does not start twice.
func TestRuleSetUpdateOutlivesPages(t *testing.T) {
	f, rc := ruleSetFixture(t, nil)
	tt := f.tt
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	f.srv.Route("POST", "/rulesets/update", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		rc.mu.Lock()
		rc.updatedAt = updatedAt + 7200000
		rc.mu.Unlock()
		testkit.JSON(map[string]any{"tag": "my-remote", "lastUpdatedAt": updatedAt + 7200000})(w, r)
	})
	testkit.UntilText(t, tt, "my-remote")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.statusLoaded })
	f.click("更新规则集 my-remote")
	testkit.Until(t, tt, func() bool { mu.Lock(); defer mu.Unlock(); return calls == 1 })
	f.w.CloseRuleSets()
	f.w.OpenRuleSets()
	tt.Frame()
	if !f.w.ruleSets.updating["rs-remote"] {
		t.Fatal("the reopened page lost the update")
	}
	f.w.updateRuleSet(storedRuleSet(f.w.doc(), "rs-remote"))
	close(release)
	testkit.UntilText(t, tt, "远程 srs · "+formatRuleSetUpdate(updatedAt+7200000))
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("%d updates", calls)
	}
}

// An inline set that changed while its download ran keeps what it has.
func TestInlineUpdateKeepsChangedMode(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	f.srv.SetFetch("https://example.com/my.json", testkit.FetchPage{Body: `{"version":2,"rules":[{"domain":["late.example"]}]}`, Delay: 300 * time.Millisecond})
	testkit.UntilText(t, tt, "my-inline")
	f.click("更新规则集 my-inline")
	if !f.w.save(func(doc *model.Doc) error { storedRuleSet(doc, "rs-inline").Mode = "remote"; return nil }) {
		t.Fatal(f.w.err)
	}
	testkit.Until(t, tt, func() bool { return !f.w.ruleSetStatus().updating["rs-inline"] })
	r := storedRuleSet(f.w.doc(), "rs-inline")
	if r.Mode != "remote" || strings.Contains(fmt.Sprint(r.Rules), "late.example") || f.w.ruleSetStatus().updateErr["rs-inline"] == "" {
		t.Errorf("rule set %+v, error %q", r, f.w.ruleSetStatus().updateErr["rs-inline"])
	}
}

// Becoming custom, an inline set folds its overrides into its rules; a
// remote one drops them.
func TestRuleSetBecomesCustom(t *testing.T) {
	f, _ := ruleSetFixture(t, func(doc *model.Doc) {
		for _, r := range doc.Route.RuleSets {
			switch r.ID {
			case "rs-inline":
				r.Rules = []any{map[string]any{"domain": []any{"old.example", "gone.example"}}}
				r.Overrides = &model.RuleSetOverrides{Add: []model.RuleSetEntry{{Field: "domain", Value: "new.example"}},
					Remove: []model.RuleSetEntry{{Field: "domain", Value: "gone.example"}}}
			case "rs-remote":
				r.Overrides = &model.RuleSetOverrides{Add: []model.RuleSetEntry{{Field: "domain", Value: "x.example"}}}
			}
		}
	})
	tt := f.tt
	for _, id := range []string{"rs-inline", "rs-remote"} {
		f.w.openRuleSet(storedRuleSet(f.w.doc(), id))
		tt.Frame()
		d := f.w.ruleSets.detail
		d.kind, d.value.Mode, d.value.URL = kindCustom, "inline", ""
		f.w.saveRuleSet(f.w.doc(), d, "", "")
		if d.validation != "" {
			t.Fatalf("%s: %s", id, d.validation)
		}
	}
	f.waitSaved(func(doc *model.Doc) bool {
		inline, remote := storedRuleSet(doc, "rs-inline"), storedRuleSet(doc, "rs-remote")
		raw, _ := json.Marshal(inline.Rules)
		return string(raw) == `[{"domain":["old.example","new.example"]}]` && inline.Overrides == nil && model.IsCustomRuleSet(inline) &&
			remote.Rules == nil && remote.Overrides == nil && model.IsCustomRuleSet(remote)
	})
}

// A rule set the user builds keeps its entries in the document: added,
// edited and deleted on its page, and offered to the rules' pickers.
func TestCustomRuleSetEditing(t *testing.T) {
	f, _ := ruleSetFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("新建规则集")
	testkit.UntilText(t, tt, "快捷模板")
	d := f.w.ruleSets.detail
	d.kind, d.value.Mode, d.value.Tag = kindCustom, "inline", "我的规则"
	tt.Frame()
	if tt.HasText("来源链接") {
		t.Error("a custom rule set asks for a link")
	}
	if !f.w.editing {
		t.Error("a draft does not hold the store's edit")
	}
	f.click("添加规则集")
	f.waitSaved(func(doc *model.Doc) bool {
		for _, r := range doc.Route.RuleSets {
			if r.Tag == "我的规则" && model.IsCustomRuleSet(r) {
				return true
			}
		}
		return false
	})
	testkit.UntilText(t, tt, "没有规则")
	id := f.w.ruleSets.detail.id
	f.click("添加规则")
	tt.Frame()
	tt.Type("corp.example")
	tt.Key(0, ui.KeyEnter)
	tt.Type("home.example")
	tt.Key(0, ui.KeyEnter)
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(storedRuleSet(doc, id).Rules)
		return string(raw) == `[{"domain":["corp.example","home.example"]}]`
	})
	// Edit: the context menu opens the row on the entry.
	testkit.UntilText(t, tt, "home.example")
	if err := tt.RightClick("corp.example"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	f.click("编辑")
	tt.Frame()
	if f.w.ruleSets.detail.editing == nil || f.w.ruleSets.detail.add.value != "corp.example" {
		t.Fatalf("editing %+v", f.w.ruleSets.detail.editing)
	}
	f.w.ruleSets.detail.add.value = "work.example"
	f.click("保存")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(storedRuleSet(doc, id).Rules)
		return string(raw) == `[{"domain":["work.example","home.example"]}]`
	})
	f.click("删除 home.example")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(storedRuleSet(doc, id).Rules)
		return string(raw) == `[{"domain":["work.example"]}]`
	})
	options := model.RuleSetOptions(f.w.doc().Route.RuleSets)
	found := false
	for _, o := range options {
		found = found || o.Value == "我的规则"
	}
	if !found {
		t.Errorf("pickers lack the custom set: %v", options)
	}
}

// Renaming keeps the overrides; deleting a rule set from its page asks
// first and returns to the list.
func TestRuleSetRenameAndDelete(t *testing.T) {
	f, _ := ruleSetFixture(t, func(doc *model.Doc) {
		r := doc.Route.RuleSets[len(doc.Route.RuleSets)-3]
		r.Overrides = &model.RuleSetOverrides{Add: []model.RuleSetEntry{{Field: "domain", Value: "x.example"}}}
	})
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "x.example")
	f.w.ruleSets.detail.value.Tag = "renamed"
	tt.Frame()
	if !f.w.editing {
		t.Error("edited settings do not hold the store's edit")
	}
	f.click("保存")
	f.waitSaved(func(doc *model.Doc) bool {
		r := storedRuleSet(doc, "rs-remote")
		return r.Tag == "renamed" && r.Overrides != nil && len(r.Overrides.Add) == 1
	})
	testkit.Until(t, tt, func() bool { return !f.w.editing })
	f.click("删除规则集")
	testkit.UntilText(t, tt, "删除「renamed」后，关联匹配条件需要重新选择。")
	// Escape closes the confirmation, not the page.
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return !f.w.ruleSets.detail.del.Open })
	if f.w.ruleSets.detail == nil {
		t.Fatal("Escape left the page")
	}
	f.click("删除规则集")
	testkit.UntilText(t, tt, "删除「renamed」后，关联匹配条件需要重新选择。")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	f.waitSaved(func(doc *model.Doc) bool { return storedRuleSet(doc, "rs-remote") == nil })
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.detail == nil })
}

// TestF14RuleSetScreenshots writes the acceptance frames of the rule set
// pages when ROUTES_PREVIEW_DIR is set.
func TestF14RuleSetScreenshots(t *testing.T) {
	if os.Getenv("ROUTES_PREVIEW_DIR") == "" {
		t.Skip("ROUTES_PREVIEW_DIR is not set")
	}
	values := make([]any, 1200)
	for i := range values {
		values[i] = fmt.Sprintf("cdn%d.example.com", i)
	}
	f, _ := ruleSetFixture(t, func(doc *model.Doc) {
		r := doc.Route.RuleSets[len(doc.Route.RuleSets)-3]
		r.Overrides = &model.RuleSetOverrides{Add: []model.RuleSetEntry{{Field: "domain_suffix", Value: "added.example"}, {Field: "ip_cidr", Value: "10.8.0.0/16"}},
			Remove: []model.RuleSetEntry{{Field: "domain", Value: "b.example"}}}
		doc.Route.RuleSets = append(doc.Route.RuleSets,
			&model.RuleSet{ID: "rs-cdn", Tag: "CDN 域名", Enabled: true, Mode: "inline", URL: "https://example.com/cdn.json", LastUpdatedAt: updatedAt,
				Rules: []any{map[string]any{"domain_suffix": values}}},
			&model.RuleSet{ID: "rs-mine", Tag: "公司内网", Enabled: true, Mode: "inline",
				Rules: []any{map[string]any{"domain_suffix": []any{"corp.example", "intra.example"}}, map[string]any{"ip_cidr": []any{"10.20.0.0/16", "172.16.0.0/12"}},
					map[string]any{"process_name": []any{"Slack"}}}})
	})
	tt := f.tt
	tt.SetSize(1180, 860)
	testkit.UntilText(t, tt, "公司内网")
	testkit.Until(t, tt, func() bool { return f.w.ruleSets.statusLoaded })
	f.click("更新规则集 bad-remote")
	testkit.UntilText(t, tt, "更新失败")
	tt.Move(4, 600)
	f.snapshot("f14-rulesets-light.png", false)
	f.snapshot("f14-rulesets-dark.png", true)
	f.click("打开规则集 my-remote")
	testkit.UntilText(t, tt, "已添加")
	f.snapshot("f14-ruleset-overrides-light.png", false)
	f.snapshot("f14-ruleset-overrides-dark.png", true)
	f.click("返回规则集列表")
	f.click("打开规则集 CDN 域名")
	testkit.UntilText(t, tt, "1,200 条")
	f.click("搜索规则")
	tt.Type("cdn11")
	tt.Advance(searchDelay)
	testkit.UntilText(t, tt, "11 / 1,200 条")
	f.snapshot("f14-ruleset-search-light.png", false)
	f.click("返回规则集列表")
	f.click("打开规则集 公司内网")
	testkit.UntilText(t, tt, "intra.example")
	f.click("添加规则")
	tt.Frame()
	tt.Type("vpn.example")
	tt.Frame()
	f.snapshot("f14-ruleset-custom-light.png", false)
	f.snapshot("f14-ruleset-custom-dark.png", true)
	tt.SetSize(640, 860)
	tt.Frame()
	f.snapshot("f14-ruleset-custom-narrow.png", false)
	f.click("返回规则集列表")
	testkit.UntilText(t, tt, "公司内网")
	f.snapshot("f14-rulesets-narrow.png", false)
	f.snapshot("f14-rulesets-narrow-dark.png", true)
	tt.SetSize(420, 860)
	tt.Frame()
	f.reveal("下载客户端", 200, 400)
	f.snapshot("f14-rulesets-420.png", false)
	f.w.openRuleSet(storedRuleSet(f.w.doc(), "rs-remote"))
	testkit.UntilText(t, tt, "已添加")
	f.snapshot("f14-ruleset-420.png", false)
}

// "Update all" stores a catalog set used for the first time only once its JSON
// downloaded: one that failed keeps its default.
func TestUpdateAllStoresDownloadedCatalogSetsOnly(t *testing.T) {
	f, _ := ruleSetFixture(t, func(doc *model.Doc) {
		rule := *doc.Route.Rules[0]
		rule.ID, rule.Name, rule.Enabled = "r-catalog", "目录规则集", true
		rule.ConditionTree = &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
			{Kind: model.ConditionMatch, Match: map[string]any{"rule_set": []any{"Gfw", "geosite-cn"}}}}}
		doc.Route.Rules = append(doc.Route.Rules, &rule)
	})
	f.srv.SetFetchBody(model.BuiltinRuleSetURL("Gfw", ""), `{"version":2,"rules":[{"domain_suffix":["blocked.example"]}]}`)
	tt := f.tt
	testkit.UntilText(t, tt, "my-remote")
	f.click("全部更新")
	testkit.Until(t, tt, func() bool { return !f.w.ruleSetStatus().busy && f.w.ruleSetStatus().message != "" })
	f.waitSaved(func(doc *model.Doc) bool {
		gfw := storedRuleSet(doc, model.BuiltinRuleSetID("Gfw"))
		return gfw != nil && gfw.Mode == "inline" && len(gfw.Rules) == 1
	})
	if r := storedRuleSet(f.saved(), model.BuiltinRuleSetID("geosite-cn")); r != nil {
		t.Errorf("geosite-cn stored without content: %+v", r)
	}
}

func TestEntryActionHintNamesTheRange(t *testing.T) {
	item := model.RuleSetViewItem{RuleSetItem: model.RuleSetItem{Field: "ip_cidr", Value: "10.1.0.0/16"}, State: model.EntryRemoved,
		By: model.RuleSetEntry{Field: "ip_cidr", Value: "10.0.0.0/8"}}
	if got := entryActionHint(item, "撤销移除"); got != "撤销移除 10.0.0.0/8" {
		t.Errorf("hint %q", got)
	}
	item.By = model.RuleSetEntry{}
	if got := entryActionHint(item, "撤销移除"); got != "撤销移除" {
		t.Errorf("hint %q", got)
	}
}
