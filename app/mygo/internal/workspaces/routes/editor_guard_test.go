package routes

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// openGuarded opens a rule of the current section and changes its name.
func openGuarded(t *testing.T, f *fixture, id string, change bool) *ruleEditor {
	t.Helper()
	f.w.openRule(f.w.doc(), id)
	f.tt.Frame()
	e := f.w.editor
	if e == nil || e.id() != id {
		t.Fatalf("editor of %s did not open", id)
	}
	if e.dirty() {
		t.Fatal("an untouched draft is dirty")
	}
	if change {
		*e.name() += "改"
		f.tt.Frame()
		if !e.dirty() {
			t.Fatal("a changed draft is clean")
		}
	}
	return e
}

func asks(f *fixture) bool { return f.tt.HasText("放弃更改？") }

// The close button, Cancel and Escape are a discard: they close at once,
// drop the draft, and never ask.
func TestEditorGuardExplicitWaysDiscard(t *testing.T) {
	ways := map[string]func(f *fixture){
		"close":  func(f *fixture) { f.click("关闭编辑器") },
		"cancel": func(f *fixture) { f.click("取消") },
		"escape": func(f *fixture) { f.tt.Key(0, ui.KeyEscape) },
	}
	for name, way := range ways {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			testkit.UntilText(t, f.tt, "Apple 服务")
			openGuarded(t, f, "r-apple", true)
			way(f)
			f.tt.Frame()
			if asks(f) || f.w.editor != nil {
				t.Fatalf("asks %v editor kept %v", asks(f), f.w.editor != nil)
			}
			if got := findRule(f.saved(), "r-apple").Name; got != "Apple 服务" {
				t.Fatalf("a discard saved %q", got)
			}
			// The draft is gone: the rule opens as stored.
			if e := openGuarded(t, f, "r-apple", false); *e.name() != "Apple 服务" {
				t.Fatalf("reopened with %q", *e.name())
			}
		})
	}
}

// While the question shows, the close button and Cancel still close at once.
func TestEditorGuardExplicitWaysWhileAsking(t *testing.T) {
	for _, label := range []string{"关闭编辑器", "取消"} {
		t.Run(label, func(t *testing.T) {
			f := newFixture(t, nil)
			testkit.UntilText(t, f.tt, "Apple 服务")
			openGuarded(t, f, "r-apple", true)
			f.tt.ClickAt(4, 4)
			f.tt.Frame()
			if !asks(f) {
				t.Fatal("no question")
			}
			f.click(label)
			f.tt.Frame()
			if asks(f) || f.w.editor != nil {
				t.Fatalf("asks %v editor kept %v", asks(f), f.w.editor != nil)
			}
		})
	}
}

// A press outside with changes asks in the editor, over its footer
// actions: "Keep editing" keeps the input, "Discard changes" closes without saving.
func TestEditorGuardPressOutsideAsks(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", true)
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	if !asks(f) || f.w.editor != e || !e.open {
		t.Fatalf("no question, or the editor closed: asks %v open %v", asks(f), e.open)
	}
	assertQuestionOverFooter(t, f, 1280)
	if !f.tt.Focused("继续编辑") {
		t.Fatal("继续编辑 is not focused")
	}
	f.click("继续编辑")
	f.tt.Frame()
	if asks(f) || f.w.editor != e || *e.name() != "Apple 服务改" {
		t.Fatalf("继续编辑: asks %v name %q", asks(f), *e.name())
	}
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	if !asks(f) {
		t.Fatal("no second question")
	}
	f.click("放弃更改")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	if got := findRule(f.saved(), "r-apple").Name; got != "Apple 服务" {
		t.Fatalf("a discard saved %q", got)
	}
}

// assertQuestionOverFooter checks the question shows inside the editor
// sidebar of a window width wide, above its "Save rule" and Cancel.
func assertQuestionOverFooter(t *testing.T, f *fixture, width float32) {
	t.Helper()
	// The Tester's clock stands still: the sidebar's entrance and the
	// question's rise end once it moves on.
	f.tt.Advance(time.Second)
	bar, ok := f.tt.Find("放弃更改？")
	save, ok2 := f.tt.Find("保存规则")
	cancel, ok3 := f.tt.Find("取消")
	if !ok || !ok2 || !ok3 {
		t.Fatalf("missing the question or the footer: %q", f.tt.Texts())
	}
	left, right := width-12-kit.EditorWidth, width-12
	if bar.H == 0 || bar.Y+bar.H > save.Y || bar.Y+bar.H > cancel.Y || bar.X < left || bar.X+bar.W > right {
		t.Fatalf("question at %+v, 保存规则 at %+v, 取消 at %+v", bar, save, cancel)
	}
	// It sits at the bottom of the editor, not by its title bar.
	if closeButton, _ := f.tt.Find("关闭编辑器"); bar.Y < closeButton.Y+200 {
		t.Fatalf("question at %+v, close button at %+v", bar, closeButton)
	}
}

func TestEditorGuardEscapeClosesOnlyTheQuestion(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", true)
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	if !asks(f) {
		t.Fatal("no question")
	}
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if asks(f) || f.w.editor != e || !e.open || *e.name() != "Apple 服务改" {
		t.Fatalf("escape in the question: asks %v editor %v", asks(f), f.w.editor)
	}
	// Without the question, Escape closes.
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	if asks(f) || f.w.editor != nil {
		t.Fatalf("second escape: asks %v editor %v", asks(f), f.w.editor)
	}
}

func TestEditorGuardCleanClosesAtOnce(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	for _, way := range []func(){
		func() { f.click("关闭编辑器") },
		func() { f.click("取消") },
		func() { f.tt.Key(0, ui.KeyEscape) },
		func() { f.tt.ClickAt(4, 4) },
	} {
		openGuarded(t, f, "r-apple", false)
		way()
		testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
		if asks(f) {
			t.Fatal("asked without changes")
		}
	}
}

// A DNS rule's editor discards and asks the same ways: its close button
// closes at once; a press outside, a device, the Traffic Rules tab and the
// fallback row ask over the footer first.
func TestEditorGuardDNSRule(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.click("DNS 分流")
	f.tt.Frame()
	openGuarded(t, f, "d-cn", true)
	f.click("关闭编辑器")
	f.tt.Frame()
	if asks(f) || f.w.editor != nil {
		t.Fatal("the close button asked")
	}
	cases := []struct {
		name     string
		leave    func(f *fixture)
		switched func(f *fixture) bool
	}{
		{"outside", func(f *fixture) { f.tt.ClickAt(4, 4) }, func(f *fixture) bool { return f.w.editor == nil }},
		{"device", func(f *fixture) { f.click("iPhone") }, func(f *fixture) bool { return f.w.owner == "phone" }},
		{"section", func(f *fixture) { f.click("流量规则") }, func(f *fixture) bool { return f.w.sec != sectionDNS }},
		{"fallback", func(f *fixture) { f.click("编辑 兜底 DNS") }, func(f *fixture) bool { return f.w.fallback != nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			testkit.UntilText(t, f.tt, "Apple 服务")
			f.click("DNS 分流")
			f.tt.Frame()
			e := openGuarded(t, f, "d-cn", true)
			tc.leave(f)
			f.tt.Frame()
			f.tt.Advance(time.Second)
			if !asks(f) || f.w.editor != e || tc.switched(f) {
				t.Fatalf("asks %v editor kept %v switched %v", asks(f), f.w.editor == e, tc.switched(f))
			}
			assertQuestionOverFooter(t, f, 1280)
			f.click("继续编辑")
			f.tt.Frame()
			if f.w.editor != e || *e.name() != "国内域名改" || tc.switched(f) {
				t.Fatal("继续编辑 lost the draft or switched")
			}
			tc.leave(f)
			f.tt.Frame()
			f.click("放弃更改")
			f.tt.Frame()
			if !tc.switched(f) || f.w.editor != nil {
				t.Fatalf("no switch after a discard: switched %v editor %v", tc.switched(f), f.w.editor)
			}
		})
	}
}

// Opening another rule drops the draft, so it asks first and switches
// after a discard.
func TestEditorGuardSwitchingRows(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", true)
	f.click("编辑 开发与协作")
	f.tt.Frame()
	if !asks(f) || f.w.editor != e {
		t.Fatalf("a switch with changes did not ask: editor %v", f.w.editor.id())
	}
	f.click("继续编辑")
	f.tt.Frame()
	if f.w.editor != e || *e.name() != "Apple 服务改" {
		t.Fatal("继续编辑 lost the draft")
	}
	f.click("编辑 开发与协作")
	f.tt.Frame()
	f.click("放弃更改")
	testkit.Until(t, f.tt, func() bool { return f.w.editor != nil && f.w.editor.id() == "r-dev" })
	// Clean drafts switch at once.
	f.click("编辑 Apple 服务")
	testkit.Until(t, f.tt, func() bool { return f.w.editor != nil && f.w.editor.id() == "r-apple" })
	if asks(f) {
		t.Fatal("asked without changes")
	}
}

func TestEditorGuardSavedEditorClosesWithoutAsking(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	openGuarded(t, f, "r-apple", true)
	f.click("保存规则")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	if asks(f) {
		t.Fatal("asked after saving")
	}
}

func TestEditorGuardNewRule(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.w.addRule(f.w.doc())
	f.tt.Frame()
	f.click("关闭编辑器")
	testkit.Until(t, f.tt, func() bool { return f.w.editor == nil })
	f.w.addRule(f.w.doc())
	f.tt.Frame()
	*f.w.editor.name() = "新规则"
	f.tt.Frame()
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	if !asks(f) || f.w.editor == nil {
		t.Fatal("a new rule with input closed without asking")
	}
	f.click("关闭编辑器")
	f.tt.Frame()
	if asks(f) || f.w.editor != nil {
		t.Fatal("the close button kept a new rule")
	}
}

// TestF6DiscardScreenshots saves the question over the editor's footer
// after a press outside (light, dark, narrow) and after a device switch of
// a DNS rule, when ROUTES_PREVIEW_DIR is set.
func TestF6DiscardScreenshots(t *testing.T) {
	if os.Getenv("ROUTES_PREVIEW_DIR") == "" {
		t.Skip("ROUTES_PREVIEW_DIR is not set")
	}
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	openGuarded(t, f, "r-apple", true)
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.snapshot("15-ask-outside-light.png", false)
	f.snapshot("15-ask-outside-dark.png", true)

	f = newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.click("DNS 分流")
	f.tt.Frame()
	openGuarded(t, f, "d-cn", true)
	f.click("iPhone")
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.snapshot("15-ask-dns-light.png", false)

	f = newFixture(t, nil)
	f.tt.SetSize(560, 820)
	testkit.UntilText(t, f.tt, "Apple 服务")
	openGuarded(t, f, "r-apple", true)
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	f.tt.Advance(time.Second)
	f.snapshot("15-ask-narrow-light.png", false)
}

// Navigation away from a rule editor with changes asks first: the editor
// and its draft stay until a discard, which then switches.
func TestEditorGuardNavigation(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(f *fixture)
		leave    func(f *fixture)
		switched func(f *fixture) bool
	}{
		{"device", nil, func(f *fixture) { f.click("iPhone") }, func(f *fixture) bool { return f.w.owner == "phone" }},
		{"section", nil, func(f *fixture) { f.click("DNS 分流") }, func(f *fixture) bool { return f.w.sec == sectionDNS }},
		{"fallback", nil, func(f *fixture) { f.click("编辑 默认出口") }, func(f *fixture) bool { return f.w.fallback != nil }},
		{"topology", func(f *fixture) {
			f.click("拓扑")
			testkit.UntilText(f.t, f.tt, "入口")
			f.click("查看 Apple 服务")
			testkit.UntilText(f.t, f.tt, "匹配条件")
			f.click("编辑")
			testkit.UntilText(f.t, f.tt, "编辑路由规则")
		}, func(f *fixture) { f.click("查看 开发与协作") }, func(f *fixture) bool { return f.w.topoPreview == "r-dev" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			testkit.UntilText(t, f.tt, "Apple 服务")
			if tc.prepare != nil {
				tc.prepare(f)
			}
			e := openGuarded(t, f, "r-apple", true)
			tc.leave(f)
			f.tt.Frame()
			f.tt.Frame()
			if !asks(f) || f.w.editor != e || !e.open || tc.switched(f) {
				t.Fatalf("asks %v editor kept %v switched %v", asks(f), f.w.editor == e, tc.switched(f))
			}
			assertQuestionOverFooter(t, f, 1280)
			f.click("继续编辑")
			f.tt.Frame()
			if f.w.editor != e || *e.name() != "Apple 服务改" || tc.switched(f) {
				t.Fatal("继续编辑 lost the draft or switched")
			}
			// A second click on the same device within the double-click time
			// would edit the device.
			f.tt.Advance(time.Second)
			tc.leave(f)
			f.tt.Frame()
			f.click("放弃更改")
			f.tt.Frame()
			if !tc.switched(f) || f.w.editor != nil && tc.name != "topology" {
				t.Fatalf("no switch after a discard: switched %v editor kept %v owner %q sec %v asks %v", tc.switched(f), f.w.editor == e, f.w.owner, f.w.sec, asks(f))
			}
		})
	}
}

func TestEditorGuardNavigationCleanSwitchesAtOnce(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	openGuarded(t, f, "r-apple", false)
	f.click("编辑 默认出口")
	testkit.Until(t, f.tt, func() bool { return f.w.fallback != nil && f.w.editor == nil })
	openGuarded(t, f, "r-apple", false)
	f.click("iPhone")
	testkit.Until(t, f.tt, func() bool { return f.w.owner == "phone" && f.w.editor == nil })
	if asks(f) {
		t.Fatal("asked without changes")
	}
	f.click("本机 / 网关")
	testkit.UntilText(t, f.tt, "Apple 服务")
	openGuarded(t, f, "r-apple", false)
	f.click("DNS 分流")
	testkit.Until(t, f.tt, func() bool { return f.w.sec == sectionDNS && f.w.editor == nil })
	if asks(f) {
		t.Fatal("asked without changes")
	}
}

// A click elsewhere while the question is open (press and release in
// separate frames) keeps the question and the draft: it neither closes
// nor asks again, and a click on a device makes the device switch the one
// waiting on the answer, which then switches.
func TestEditorGuardClickElsewhereKeepsQuestion(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", true)
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	if !asks(f) {
		t.Fatal("no question")
	}
	f.tt.ClickAt(4, 4)
	f.tt.Frame()
	if !asks(f) || f.w.editor != e || !e.open {
		t.Fatalf("a blank click: asks %v", asks(f))
	}
	r, ok := f.tt.Find("iPhone")
	if !ok {
		t.Fatal("no device")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	f.tt.Press(x, y)
	f.tt.Frame()
	if !asks(f) || f.w.editor != e {
		t.Fatalf("a press on a device: asks %v", asks(f))
	}
	f.tt.Release(x, y)
	f.tt.Frame()
	f.tt.Frame()
	if !asks(f) || f.w.editor != e || f.w.owner == "phone" || *e.name() != "Apple 服务改" {
		t.Fatalf("a device click: asks %v owner %q", asks(f), f.w.owner)
	}
	f.click("放弃更改")
	f.tt.Frame()
	if f.w.owner != "phone" || f.w.editor != nil {
		t.Fatalf("discard: owner %q editor %v", f.w.owner, f.w.editor)
	}
}

// Text typed into a token field's test box does not change the rule.
func TestEditorGuardIgnoresTestBoxText(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", false)
	e.cond.init()
	e.cond.drafts["root:domain:test"] = "apple.com"
	if e.dirty() {
		t.Fatal("test box text made the draft dirty")
	}
	e.cond.drafts["root:domain"] = "x.com"
	if !e.dirty() {
		t.Fatal("a typed condition left the draft clean")
	}
}

// Dragging another row with a changed draft open keeps the editor.
func TestEditorGuardDragKeepsDraft(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", true)
	r, ok := f.tt.Find("编辑 开发与协作")
	if !ok {
		t.Fatal("no row")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	f.tt.Press(x, y)
	for i := float32(1); i <= 6; i++ {
		f.tt.Move(x, y+i*6)
		f.tt.Frame()
	}
	if f.w.editor != e || !e.open || *e.name() != "Apple 服务改" {
		t.Fatal("a drag dropped the draft")
	}
	f.tt.Release(x, y+36)
	f.tt.Frame()
}

// Pressing the row of the rule being edited keeps its editor and draft
// without asking.
func TestEditorGuardSameRuleKeepsDraft(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	e := openGuarded(t, f, "r-apple", true)
	f.w.openRule(f.w.doc(), "r-apple")
	f.tt.Frame()
	if f.w.editor != e || *e.name() != "Apple 服务改" || asks(f) {
		t.Fatalf("same rule: kept %v asks %v", f.w.editor == e, asks(f))
	}
}
