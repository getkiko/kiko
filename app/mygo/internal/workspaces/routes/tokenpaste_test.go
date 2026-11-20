package routes

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestTokenPasteParsing(t *testing.T) {
	for in, want := range map[string]model.DomainEntry{
		"Example.com":                {Field: "domain", Value: "example.com"},
		"+.apple.com":                {Field: "domain_suffix", Value: "apple.com"},
		"*.apple.com":                {Field: "domain_suffix", Value: "apple.com"},
		"domain_suffix:icloud.com":   {Field: "domain_suffix", Value: "icloud.com"},
		"domain_keyword:google":      {Field: "domain_keyword", Value: "google"},
		`domain_regex:^api\.x\.com$`: {Field: "domain_regex", Value: `^api\.x\.com$`},
		"full:www.google.com":        {Field: "domain", Value: "www.google.com"},
		"suffix:google.com":          {Field: "domain_suffix", Value: "google.com"},
		"contains:google":            {Field: "domain_keyword", Value: "google"},
		`regex:^a\.b$`:               {Field: "domain_regex", Value: `^a\.b$`},
	} {
		got, problem := domainPaste(in)
		if problem != "" || got != want {
			t.Errorf("domainPaste(%q) = %+v %q", in, got, problem)
		}
	}
	if got := tokenSplit(model.TypedPort)("80, 443\n30 - 500"); !reflect.DeepEqual(got, []string{"80", "443", "30-500"}) {
		t.Errorf("port split %q", got)
	}
	if got := tokenSplit(model.TypedWiFi)("Home WiFi\nOffice"); !reflect.DeepEqual(got, []string{"Home WiFi", "Office"}) {
		t.Errorf("wifi split %q", got)
	}
	for in, want := range map[string]string{"www.google.com": "google.com", "news.bbc.co.uk": "bbc.co.uk", "a.b.example.com.hk": "example.com.hk", "google.com": "", "x.github.io": ""} {
		if got := rootDomain(in); got != want {
			t.Errorf("rootDomain(%q) = %q, want %q", in, got, want)
		}
	}
	if got := domainSplit("regex:^a{1,3}\\.com$\nicloud.com, me.com"); !reflect.DeepEqual(got, []string{`regex:^a{1,3}\.com$`, "icloud.com", "me.com"}) {
		t.Errorf("domain split %q", got)
	}
	if got := domainSuggestions("*.www.google.com"); len(got) == 0 || got[0].ID != "root" || got[0].Label != "google.com" {
		t.Errorf("wildcard suggestions %+v", got)
	}
	if got := domainSuggestions("www.google.com"); len(got) == 0 || got[0].ID != "root" || got[0].Label != "google.com" {
		t.Errorf("suggestions %+v", got)
	}
	if got := pasteProblem([]string{"a", "b", "c", "d"}); got != "4 条格式不正确：a、b、c 等" {
		t.Errorf("problem %q", got)
	}
}

// TestRuleEditorTokenClipboard pastes a list into the Domain field of the
// rule editor, copies chips chosen by click and removes them.
func TestRuleEditorTokenClipboard(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	chip, ok := tt.Find("apple.com")
	if !ok {
		t.Fatal("no domain chip")
	}
	tt.ClickAt(chip.X+chip.W+60, chip.Y+chip.H/2)
	tt.Frame()
	tt.SetClipboard("icloud.com\n+.me.com, mzstatic.com\n@@@")
	tt.Key(ui.Cmd, ui.KeyV)
	testkit.Until(t, tt, func() bool { return tt.HasText("icloud.com") && tt.HasText("me.com") && tt.HasText("mzstatic.com") })
	if !tt.HasText("1 条格式不正确：@@@") {
		t.Fatalf("no paste problem: %q", tt.Texts())
	}

	// Copy the chosen chips, then paste them back as they were.
	for i, label := range []string{"apple.com", "me.com"} {
		r, _ := tt.Find(label)
		mods := ui.Modifiers(0)
		if i > 0 {
			mods = ui.Cmd
		}
		tt.ClickAtWith(mods, r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	f.snapshot("routes-editor-tokens-selected-light.png", false)
	f.snapshot("routes-editor-tokens-selected-dark.png", true)
	tt.Key(ui.Cmd, ui.KeyC)
	tt.Frame()
	if got := tt.Clipboard(); got != "suffix:apple.com\nsuffix:me.com" {
		t.Fatalf("copied %q", got)
	}
	tt.Key(0, ui.KeyBackspace)
	testkit.Until(t, tt, func() bool { _, ok := tt.Find("apple.com"); return !ok })
	if !tt.HasText("icloud.com") || !tt.HasText("mzstatic.com") {
		t.Fatalf("removed too much: %q", tt.Texts())
	}

	// The menu pastes the copy back, suffixes as suffixes.
	r, _ := tt.Find("icloud.com")
	tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	f.snapshot("routes-editor-tokens-menu-light.png", false)
	f.snapshot("routes-editor-tokens-menu-dark.png", true)
	if err := tt.Click("粘贴"); err != nil {
		t.Fatalf("menu: %v; %q", err, tt.Texts())
	}
	testkit.Until(t, tt, func() bool { return tt.HasText("apple.com") && tt.HasText("me.com") })
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(findRule(doc, "r-apple").ConditionTree)
		s := string(raw)
		return strings.Contains(s, "mzstatic.com") && strings.Contains(s, "icloud.com") && strings.Contains(s, "apple.com")
	})
	tokens := model.MatchTokens(findRule(f.saved(), "r-apple").ConditionTree, model.TypedDomain)
	kinds := map[string]string{}
	for _, tok := range tokens {
		kinds[tok.Value.(string)] = tok.Field
	}
	if want := map[string]string{"apple.com": "domain_suffix", "me.com": "domain_suffix", "icloud.com": "domain", "mzstatic.com": "domain"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v", kinds)
	}
}

// TestRuleEditorDomainRootAndPrefix types a host and takes the first
// suggestion, its registrable domain as a suffix, then pastes one value
// naming its kind, added at once as that kind.
func TestRuleEditorDomainRootAndPrefix(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	chip, _ := tt.Find("apple.com")
	tt.ClickAt(chip.X+chip.W+60, chip.Y+chip.H/2)
	tt.Frame()
	tt.Type("www.google.com")
	tt.Frame()
	if !tt.HasText("主域名 · 后缀，包含子域名") {
		t.Fatalf("no root suggestion: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.SetClipboard("full:www.youtube.com")
	tt.Key(ui.Cmd, ui.KeyV)
	testkit.Until(t, tt, func() bool { return tt.HasText("www.youtube.com") })
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(findRule(doc, "r-apple").ConditionTree)
		return strings.Contains(string(raw), "www.youtube.com")
	})
	testkit.Until(t, tt, func() bool {
		return slices.ContainsFunc(tt.Texts(), func(s string) bool {
			return strings.Contains(s, "域名后缀 google.com") && strings.Contains(s, "完整域名 www.youtube.com") && !strings.Contains(s, "www.google.com")
		})
	})
}
