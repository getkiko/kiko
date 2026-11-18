package scripts

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// A refused save on the test tab shows where the problem is.
func TestSaveFromTestTabRevealsProblem(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 900)
	tt := f.tt
	testkit.UntilText(t, tt, "统一 UA")
	f.click("添加脚本")
	testkit.UntilText(t, tt, "添加脚本")
	e := f.w.editors.ed
	e.tab = 1
	tt.Frame()
	if tt.HasText("匹配模式（RE2）") || e.fieldsOpen {
		t.Fatalf("settings open on the test tab: %q", tt.Texts())
	}

	// An empty pattern: the settings unfold and say so.
	f.click("保存")
	testkit.UntilText(t, tt, "请填写匹配模式")
	if !e.fieldsOpen || e.tab != 1 || !e.open {
		t.Fatalf("fieldsOpen %v tab %d open %v", e.fieldsOpen, e.tab, e.open)
	}

	// A problem of the source alone switches to the source.
	e.pattern = "^https://example\\.com/"
	e.code.SetText(strings.Repeat("(", model.MaxScriptDepth+10))
	e.fieldsOpen, e.tab = false, 1
	tt.Frame()
	issue := sourceIssue(e.code.Text())
	if issue == "" {
		t.Fatal("deep source accepted by the model")
	}
	f.click("保存")
	testkit.UntilText(t, tt, issue)
	if e.tab != 0 || e.fieldsOpen {
		t.Fatalf("tab %d fieldsOpen %v", e.tab, e.fieldsOpen)
	}

	// A tag another script has: the settings unfold, the source problem
	// waits.
	e.tag = "s-ua"
	e.fieldsOpen, e.tab = false, 1
	tt.Frame()
	f.click("保存")
	testkit.UntilText(t, tt, "标识已被其他脚本使用")
	if !e.fieldsOpen || e.tab != 1 {
		t.Fatalf("fieldsOpen %v tab %d", e.fieldsOpen, e.tab)
	}
	if len(f.scripts()) != 4 {
		t.Fatal("saved an invalid script")
	}
}

func putGate(info api.ScriptStoreInfo, gate chan struct{}) func(*testkit.Server) {
	return func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.Route(http.MethodPut, "/scripts/store/value", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			testkit.JSON(info)(w, r)
		})
	}
}

// settle runs frames for a moment, to let late replies arrive.
func (h *storeHarness) settle() {
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond)
		h.tt.Frame()
	}
}

func TestStoreSlowSaveReplyKeepsNewDialog(t *testing.T) {
	gate := make(chan struct{})
	h := newStoreHarness(t, 1280, 720, putGate(api.ScriptStoreInfo{Persistent: true, Bytes: api.Uint64(12345), Limit: 1 << 20}, gate))
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "添加")
	testkit.UntilText(t, h.tt, "添加条目")
	h.page.edit.key = "slow"
	h.page.edit.code.SetText("v")
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.page.edit.saving })
	h.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, h.tt, func() bool { return !h.page.edit.open })
	h.tt.Frame()
	slClick(t, h.tt, "添加")
	testkit.UntilText(t, h.tt, "添加条目")
	h.page.edit.key = "draft"
	h.page.edit.code.SetText("keep me")
	h.tt.Frame()
	close(gate)
	testkit.Until(t, h.tt, func() bool { return h.puts() == 1 })
	h.settle()
	e := h.page.edit
	if !e.open || !e.isNew || e.key != "draft" || e.code.Text() != "keep me" || e.saving || e.err != "" {
		t.Fatalf("the new dialog was touched: %+v", e)
	}
}

func TestStoreSaveErrorAfterCloseSkipsNewDraft(t *testing.T) {
	gate := make(chan struct{})
	h := newStoreHarness(t, 1280, 720, func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.Route(http.MethodPut, "/scripts/store/value", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			testkit.JSON(map[string]string{"error": "disk is read-only"})(w, r)
		})
	})
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "添加")
	h.page.edit.key = "slow"
	h.page.edit.code.SetText("v")
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.page.edit.saving })
	slClick(t, h.tt, "取消")
	h.tt.Frame()
	slClick(t, h.tt, "添加")
	h.page.edit.key = "draft"
	h.tt.Frame()
	close(gate)
	h.settle()
	if e := h.page.edit; !e.open || e.err != "" || e.key != "draft" {
		t.Fatalf("the error reached the new draft: %+v", e)
	}
}

func TestStoreTruncatedReadAfterCloseKeepsNewDraft(t *testing.T) {
	full := strings.Repeat("0123456789", 30)
	gate := make(chan struct{})
	h := newStoreHarness(t, 1280, 720, truncatedStore(full, gate, false))
	testkit.UntilText(t, h.tt, "big")
	slClick(t, h.tt, "big")
	slClick(t, h.tt, "编辑")
	if !h.page.edit.loading {
		t.Fatal("not loading")
	}
	h.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, h.tt, func() bool { return !h.page.edit.open })
	h.tt.Frame()
	slClick(t, h.tt, "添加")
	testkit.UntilText(t, h.tt, "添加条目")
	h.page.edit.key = "draft"
	h.tt.Frame()
	close(gate)
	h.settle()
	e := h.page.edit
	if !e.open || !e.isNew || e.key != "draft" || e.code.Text() != "" || e.loading {
		t.Fatalf("the late read touched the new draft: %+v", e)
	}
}

func TestStorePageStopClearsDialog(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "添加")
	testkit.UntilText(t, h.tt, "添加条目")
	token := h.page.editToken
	h.page.SetActive(false)
	if h.page.edit.open || h.page.editToken == token {
		t.Fatalf("dialog kept after the page stopped: %+v", h.page.edit)
	}
	h.page.SetActive(true)
	h.tt.Frame()
	if h.tt.HasText("添加条目") {
		t.Error("dialog back after the page showed again")
	}
}

func TestStoreExistingKeyWarns(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "添加")
	testkit.UntilText(t, h.tt, "添加条目")
	if h.tt.HasText("已存在，保存将覆盖") {
		t.Fatal("warning on an empty key")
	}
	h.page.edit.key = "city"
	h.tt.Frame()
	if !h.tt.HasText("已存在，保存将覆盖") {
		t.Errorf("no warning for an existing key: %q", h.tt.Texts())
	}
	// Another namespace has no such key.
	h.page.edit.ns = "fresh"
	h.tt.Frame()
	if h.tt.HasText("已存在，保存将覆盖") {
		t.Error("warning for a key of another namespace")
	}
	h.page.edit.ns, h.page.edit.key = "app", "brand-new"
	h.tt.Frame()
	if h.tt.HasText("已存在，保存将覆盖") {
		t.Error("warning for a new key")
	}
}

func TestHiddenStopsSubPages(t *testing.T) {
	f := newFixture(t, fixtureDoc(true), 1280, 700)
	testkit.UntilText(t, f.tt, "统一 UA")
	f.click("存储")
	testkit.Until(t, f.tt, func() bool { return f.w.Running() })
	f.w.SetHidden(true)
	if f.w.Running() || f.w.store.active {
		t.Fatal("the store page reads while the tab is hidden")
	}
	f.w.SetHidden(false)
	if !f.w.store.active {
		t.Fatal("the store page does not resume")
	}
	f.w.Back()
	f.tt.Frame()
	f.click("日志")
	testkit.Until(t, f.tt, func() bool { return f.w.logs.active })
	f.w.SetHidden(true)
	if f.w.Running() {
		t.Fatal("the log follows while the tab is hidden")
	}
	f.w.SetHidden(false)
	f.w.SetActive(false)
	f.w.SetHidden(true)
	f.w.SetHidden(false)
	if f.w.Running() {
		t.Fatal("an inactive page runs after the tab showed")
	}
}

// The source is scanned once per version of its text, not once per frame.
func TestSourceCheckCachedPerVersion(t *testing.T) {
	scans := 0
	defer func(f func(string) string) { sourceChecker = f }(sourceChecker)
	sourceChecker = func(src string) string { scans++; return sourceIssue(src) }
	e := &editor{}
	e.resetCode("let a = 1\n")
	for i := 0; i < 20; i++ {
		if e.sourceCheck().msg != "" {
			t.Fatal("clean source refused")
		}
	}
	if scans != 1 {
		t.Fatalf("%d scans for one text", scans)
	}
	e.code.SetText(strings.Repeat("(", model.MaxScriptDepth+10))
	if e.sourceCheck().msg == "" || e.sourceCheck().msg == "" || scans != 2 {
		t.Fatalf("an edit is not checked once: %d scans", scans)
	}
	// A reset starts the versions over: the new text is scanned.
	e.resetCode("let b = 2\n")
	if e.sourceCheck().msg != "" || scans != 3 {
		t.Fatalf("a reset is not checked: %d scans", scans)
	}
	e.resetCode("  ")
	if got := e.sourceCheck(); !got.required || scans != 4 {
		t.Fatalf("blank source: %+v %d scans", got, scans)
	}
}
