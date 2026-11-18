package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

func (f *fixture) shareOrigin() string { return profiles.ShareOrigin(f.env.AppPrefs) }

func (f *fixture) loadShareOrigin() (string, bool) {
	var s string
	ok := f.env.AppPrefs.Load(profiles.PrefShareOrigin, &s)
	return s, ok
}

// typeKeys types text one character at a time, as a keyboard does, each
// in its own event, checking after each that the address stays saved.
func (f *fixture) typeKeys(text, saved string) {
	f.t.Helper()
	for _, r := range text {
		f.tt.Type(string(r))
		if got := f.shareOrigin(); got != saved {
			f.t.Fatalf("typing %q saved %q", f.w.share.draft, got)
		}
	}
	f.tt.Frame()
}

// backspace deletes n characters one at a time, checking after each that
// the address stays saved.
func (f *fixture) backspace(n int, saved string) {
	f.t.Helper()
	for range n {
		f.tt.Key(0, ui.KeyBackspace)
		if got := f.shareOrigin(); got != saved {
			f.t.Fatalf("deleting to %q saved %q", f.w.share.draft, got)
		}
	}
	f.tt.Frame()
}

// clearField selects the field's text and deletes it.
func (f *fixture) clearField(saved string) {
	f.t.Helper()
	f.tt.Key(ui.Cmd, ui.KeyA)
	f.backspace(1, saved)
}

// focusShareOrigin opens the main page with the field focused.
func (f *fixture) focusShareOrigin() {
	f.t.Helper()
	f.w.Open(profiles.ShareOriginSetting)
	f.tt.Frame()
	f.tt.Frame()
	if !f.tt.Focused("配置分享地址") {
		f.t.Fatal("Open did not focus the field")
	}
}

// TestShareOriginSetting: no address until the user enters one; typing
// and deleting save nothing until Enter or leaving the field; a valid
// address saves normalized, a wrong one saves nothing and shows its
// problem, an empty one clears.
func TestShareOriginSetting(t *testing.T) {
	f := newFixture(t, nil)
	f.tt.Frame()
	if f.shareOrigin() != "" || f.w.share.draft != "" {
		t.Fatalf("unset %q draft %q", f.shareOrigin(), f.w.share.draft)
	}
	if _, ok := f.loadShareOrigin(); ok {
		t.Fatal("an address is stored before the user enters one")
	}
	f.focusShareOrigin()
	f.typeKeys("https://share.example.com/", "")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()
	if got := f.shareOrigin(); got != "https://share.example.com" {
		t.Fatalf("saved %q after Enter", got)
	}

	// Deleting back through valid prefixes (https://s…) saves none of them.
	f.backspace(len("hare.example.com"), "https://share.example.com")
	f.typeKeys("?x", "https://share.example.com")
	if f.tt.HasText("地址不能包含") {
		t.Fatal("the problem shows while typing")
	}
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()
	if got := f.shareOrigin(); got != "https://share.example.com" {
		t.Fatalf("a wrong address saved %q", got)
	}
	if !f.tt.HasText("地址不能包含用户名、查询或片段") || f.w.share.draft != "https://s?x" {
		t.Fatalf("draft %q, texts %q", f.w.share.draft, f.tt.Texts())
	}

	// Escape gives the field the saved address back.
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if f.w.share.draft != "https://share.example.com" || f.w.share.err != "" || f.tt.HasText("地址不能包含") {
		t.Fatalf("after Escape draft %q err %q", f.w.share.draft, f.w.share.err)
	}

	// Leaving the field commits: here the empty address.
	f.clearField("https://share.example.com")
	f.tt.ClickAt(10, 10)
	f.tt.Frame()
	if got, ok := f.loadShareOrigin(); !ok || got != "" {
		t.Fatalf("cleared to %q (%v)", got, ok)
	}
	if f.w.share.draft != "" {
		t.Fatalf("draft %q after clearing", f.w.share.draft)
	}
}

// TestShareOriginWrongAddressOnLeaving: a wrong address left in the field
// saves nothing and shows its problem; leaving the page gives the field
// the saved address back.
func TestShareOriginWrongAddressOnLeaving(t *testing.T) {
	f := newFixture(t, nil)
	def := "https://share.example.com"
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, def)
	f.focusShareOrigin()
	f.clearField(def)
	f.typeKeys("share.example.com", def)
	f.tt.ClickAt(10, 10)
	f.tt.Frame()
	if f.shareOrigin() != def || !f.tt.HasText("需要 https 地址") || f.w.share.draft != "share.example.com" {
		t.Fatalf("draft %q saved %q texts %q", f.w.share.draft, f.shareOrigin(), f.tt.Texts())
	}
	f.w.SetActive(false)
	f.w.SetActive(true)
	f.tt.Frame()
	if f.w.share.draft != def || f.w.share.err != "" {
		t.Fatalf("draft %q err %q after leaving", f.w.share.draft, f.w.share.err)
	}
	f.focusShareOrigin()
	f.clearField(def)
	f.typeKeys("https://a.example.com?x=1", def)
	f.w.Open(pageDNS)
	f.tt.Frame()
	f.w.Open("")
	f.tt.Frame()
	if f.w.share.draft != def || f.shareOrigin() != def {
		t.Fatalf("draft %q saved %q after a subpage", f.w.share.draft, f.shareOrigin())
	}
}

// TestShareOriginSavedAddressChecked: a saved address the share service
// cannot take shows its problem as soon as the page renders.
func TestShareOriginSavedAddressChecked(t *testing.T) {
	f := newFixture(t, nil)
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "https://share.example.com/kiko")
	f.tt.Frame()
	if !f.tt.HasText("只填写域名，不要包含路径") {
		t.Fatalf("saved wrong address shows no problem: %q", f.tt.Texts())
	}
}
