package scripts

import (
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// slSavePreview writes the last frame when SCRIPT_PREVIEW_DIR is set.
func slSavePreview(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("SCRIPT_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// slClick clicks the element showing label.
func slClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts %q", label, err, tt.Texts())
	}
	tt.Frame()
}

type storeHarness struct {
	t    *testing.T
	srv  *testkit.Server
	env  *env.Env
	page *StorePage
	tt   *ui.Tester
}

// slStoreFixture fills the fake store with three namespaces.
func slStoreFixture(srv *testkit.Server) {
	srv.SetScriptStore("app", "city", "Shanghai")
	srv.SetScriptStore("app", "forecast", `{"today":{"temp":21,"desc":"晴"},"week":[19,22,24,20,18,21,23],"alerts":[]}`)
	srv.SetScriptStore("app", "updated_at", "1730000000")
	srv.SetScriptStore("app", "https://api.example.com/v2/forecast/hourly?city=shanghai&lang=zh-Hans&units=metric&appid=0123456789abcdef", strings.Repeat("hourly-sample-", 40))
	srv.SetScriptStore("cache", "etag:/v1/items", `W/"5d8c72a5edda8"`)
	srv.SetScriptStore("cache", "last-modified:/v1/items", "Tue, 03 Nov 2026 08:12:41 GMT")
	srv.SetScriptStore("tokens", "refresh", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJwb3BweSIsImV4cCI6MTczMDAwMDAwMH0.4s0n8fVxk3Yt1")
}

func newStoreHarness(t *testing.T, width, height int, setup func(*testkit.Server)) *storeHarness {
	t.Helper()
	h := &storeHarness{t: t, srv: testkit.NewServer(t)}
	if setup != nil {
		setup(h.srv)
	}
	h.env, _ = testkit.NewEnv(h.srv)
	h.page = NewStorePage(h.env)
	h.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		h.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		h.page.View(c)
	}, width, height)
	h.page.SetActive(true)
	t.Cleanup(func() { h.page.SetActive(false) })
	return h
}

func (h *storeHarness) puts() int {
	n := 0
	for _, r := range h.srv.Requests() {
		if strings.HasPrefix(r, "PUT /api/v1/scripts/store/value") {
			n++
		}
	}
	return n
}

func TestStoreListsNamespacesAndEntries(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	for _, want := range []string{"命名空间", "app", "cache", "tokens", "city", "Shanghai", "4 条", "已用"} {
		if !h.tt.HasText(want) {
			t.Errorf("missing %q; texts %q", want, h.tt.Texts())
		}
	}
	if h.tt.HasText("未持久化") {
		t.Error("a persistent store shows the warning")
	}
	slClick(t, h.tt, "cache")
	testkit.UntilText(t, h.tt, "etag:/v1/items")
	if h.tt.HasText("forecast") {
		t.Error("the entries of the previous namespace stay")
	}
}

func TestStoreNotPersistent(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.SetScriptStatus(api.ScriptStatus{StorePersistent: false})
	})
	testkit.UntilText(t, h.tt, "未持久化")
}

func TestStoreSearch(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "搜索键、值")
	h.tt.Type("shanghai")
	h.tt.Frame()
	if h.tt.HasText("updated_at") || !h.tt.HasText("city") {
		t.Errorf("search by value: texts %q", h.tt.Texts())
	}
	if !h.tt.HasText("2 / 4 条") {
		t.Errorf("filtered count missing: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Type("zzz-none")
	h.tt.Frame()
	testkit.UntilText(t, h.tt, "没有匹配的条目")
	slClick(t, h.tt, "清除搜索")
	if !h.tt.HasText("updated_at") {
		t.Errorf("clearing the search keeps rows hidden: %q", h.tt.Texts())
	}
}

func TestStoreInspectorAndKeyboard(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "city")
	for _, want := range []string{"编辑", "删除", "复制值"} {
		if !h.tt.HasText(want) {
			t.Errorf("inspector lacks %q: %q", want, h.tt.Texts())
		}
	}
	if h.page.selKey != "city" {
		t.Fatalf("selected %q", h.page.selKey)
	}
	// Down moves the choice, Enter edits it.
	h.tt.Key(0, ui.KeyDown)
	h.tt.Frame()
	if h.page.selKey != "forecast" {
		t.Fatalf("Down selected %q", h.page.selKey)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if !h.page.edit.open || h.page.edit.key != "forecast" {
		t.Fatalf("Enter did not edit: open=%v key=%q", h.page.edit.open, h.page.edit.key)
	}
	if got := h.page.edit.code.Text(); !strings.Contains(got, `"temp":21`) {
		t.Errorf("draft %q", got)
	}
	// Escape closes the dialog and the table takes the keyboard back.
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.tt.Frame()
	if h.page.edit.open {
		t.Fatal("Escape left the dialog open")
	}
	if !h.tt.Focused("存储条目") {
		t.Error("the table does not have the keyboard after the dialog closed")
	}
	// Delete asks first.
	h.tt.Key(0, ui.KeyDelete)
	h.tt.Frame()
	if !h.page.confirm.Open {
		t.Fatal("Delete did not ask")
	}
	if got := h.srv.ScriptStore()["app"]; got["forecast"] == "" {
		t.Fatal("deleted without confirming")
	}
}

func TestStoreAddAndEdit(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "添加")
	testkit.UntilText(t, h.tt, "添加条目")
	// The namespace of the page is filled in; the key has the keyboard.
	if h.page.edit.ns != "app" {
		t.Fatalf("namespace %q", h.page.edit.ns)
	}
	h.tt.Type("humidity")
	h.tt.Frame()
	h.page.edit.code.SetText("63%")
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.srv.ScriptStore()["app"]["humidity"] == "63%" })
	testkit.Until(t, h.tt, func() bool { return !h.page.edit.open && h.tt.HasText("humidity") })
	if h.page.selKey != "humidity" {
		t.Errorf("the saved key is not selected: %q", h.page.selKey)
	}
	// A new namespace.
	slClick(t, h.tt, "添加")
	h.page.edit.ns, h.page.edit.key = "fresh", "k"
	h.page.edit.code.SetText("v")
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.srv.ScriptStore()["fresh"]["k"] == "v" })
	testkit.Until(t, h.tt, func() bool { return h.page.nsSel == "fresh" && h.tt.HasText("k") && h.page.entriesNS == "fresh" })
	// Editing an existing key.
	slClick(t, h.tt, "编辑")
	h.page.edit.code.SetText("Beijing")
	h.tt.Frame()
	if h.page.edit.key != "k" {
		t.Fatalf("edit key %q", h.page.edit.key)
	}
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.srv.ScriptStore()["fresh"]["k"] == "Beijing" })
}

func TestStoreClientLimits(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "添加")
	e := &h.page.edit
	e.key = strings.Repeat("k", 257)
	e.code.SetText("v")
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	if !h.tt.HasText("键超过 256 字节") {
		t.Errorf("no key limit error: %q", h.tt.Texts())
	}
	e.key = "ok"
	e.code.SetText(strings.Repeat("x", 1<<20+1))
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	if !h.tt.HasText("值超过 1 MiB") {
		t.Errorf("no value limit error: %q", h.tt.Texts())
	}
	e.ns = " "
	e.code.SetText("v")
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	if !h.tt.HasText("请填写命名空间") {
		t.Errorf("no namespace error: %q", h.tt.Texts())
	}
	if h.puts() != 0 {
		t.Errorf("an invalid entry reached the server: %v", h.srv.Requests())
	}
	if !e.open {
		t.Error("the dialog closed on an invalid entry")
	}
}

func TestStoreServerRejection(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.Route(http.MethodPut, "/scripts/store/value", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			testkit.JSON(map[string]string{"error": "store is full: 1048576 bytes used"})(w, r)
		})
	})
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "city")
	slClick(t, h.tt, "编辑")
	h.page.edit.code.SetText("Beijing")
	slClick(t, h.tt, "保存")
	testkit.UntilText(t, h.tt, "store is full: 1048576 bytes used")
	if !h.page.edit.open || h.page.edit.saving {
		t.Errorf("open=%v saving=%v after a rejection", h.page.edit.open, h.page.edit.saving)
	}
	if h.srv.ScriptStore()["app"]["city"] != "Shanghai" {
		t.Error("a rejected write changed the store")
	}
}

func TestStoreDelete(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "city")
	slClick(t, h.tt, "删除")
	testkit.UntilText(t, h.tt, "删除此条目？")
	slClick(t, h.tt, "确认删除")
	testkit.Until(t, h.tt, func() bool { _, ok := h.srv.ScriptStore()["app"]["city"]; return !ok })
	testkit.Until(t, h.tt, func() bool { return !h.tt.HasText("Shanghai") && !h.page.confirm.Open })
	if h.page.selKey != "" {
		t.Errorf("the deleted key is still selected: %q", h.page.selKey)
	}
}

func TestStoreDeleteFailureStaysInline(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.Route(http.MethodDelete, "/scripts/store/value", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			testkit.JSON(map[string]string{"error": "disk is read-only"})(w, r)
		})
	})
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "city")
	slClick(t, h.tt, "删除")
	slClick(t, h.tt, "确认删除")
	testkit.UntilText(t, h.tt, "disk is read-only")
	if !h.page.confirm.Open || !h.tt.HasText("重试") {
		t.Error("the confirmation closed or lost its retry")
	}
	if h.page.confirm.Busy {
		t.Error("still busy after a failure")
	}
}

// truncatedStore serves one entry whose list value is a cut preview, and
// the whole value through gate.
func truncatedStore(full string, gate chan struct{}, fail bool) func(*testkit.Server) {
	return func(srv *testkit.Server) {
		srv.SetScriptStore("ns", "big", full)
		srv.Route(http.MethodGet, "/scripts/store", func(w http.ResponseWriter, r *http.Request) {
			testkit.JSON(api.ScriptStoreList{
				Namespaces: []api.ScriptNamespace{{Name: "ns", Entries: 1, Bytes: api.Uint64(len(full))}},
				Entries:    []api.ScriptStoreItem{{Key: "big", Value: full[:16], Size: uint32(len(full)), Truncated: true}},
				Info:       &api.ScriptStoreInfo{Persistent: true, Bytes: api.Uint64(len(full)), Limit: 1 << 20},
			})(w, r)
		})
		srv.Route(http.MethodGet, "/scripts/store/value", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				testkit.JSON(map[string]string{"error": "read failed"})(w, r)
				return
			}
			testkit.JSON(api.ScriptStoreValue{Namespace: "ns", Key: "big", Value: full, Found: true})(w, r)
		})
	}
}

func TestStoreTruncatedValueIsNeverWrittenBack(t *testing.T) {
	full := strings.Repeat("0123456789", 30)
	gate := make(chan struct{})
	h := newStoreHarness(t, 1280, 720, truncatedStore(full, gate, false))
	testkit.UntilText(t, h.tt, "big")
	// The table shows the preview with an ellipsis.
	if !h.tt.HasText("0123456789012345…") {
		t.Errorf("the truncated preview shows no ellipsis: %q", h.tt.Texts())
	}
	slClick(t, h.tt, "big")
	testkit.UntilText(t, h.tt, "加载中…")
	slClick(t, h.tt, "编辑")
	if !h.page.edit.loading {
		t.Fatal("the dialog did not wait for the whole value")
	}
	// Saving while the whole value is not there writes nothing.
	h.page.save()
	h.tt.Frame()
	if h.puts() != 0 {
		t.Fatal("saved before the whole value was read")
	}
	close(gate)
	testkit.Until(t, h.tt, func() bool { return !h.page.edit.loading })
	if got := h.page.edit.code.Text(); got != full {
		t.Fatalf("the draft is %q, not the whole value", got)
	}
	// Saved unchanged, the whole value stays.
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.puts() == 1 && !h.page.edit.open })
	if got := h.srv.ScriptStore()["ns"]["big"]; got != full {
		t.Fatalf("the store holds %q after saving unchanged", got)
	}
	// An edit applies to the whole value.
	slClick(t, h.tt, "编辑")
	testkit.Until(t, h.tt, func() bool { return !h.page.edit.loading })
	h.page.edit.code.SetText(h.page.edit.code.Text() + "!")
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.srv.ScriptStore()["ns"]["big"] == full+"!" })
}

func TestStoreTruncatedReadFailureBlocksSaving(t *testing.T) {
	full := strings.Repeat("abcdefghij", 30)
	gate := make(chan struct{})
	close(gate)
	h := newStoreHarness(t, 1280, 720, truncatedStore(full, gate, true))
	testkit.UntilText(t, h.tt, "big")
	slClick(t, h.tt, "big")
	slClick(t, h.tt, "编辑")
	testkit.Until(t, h.tt, func() bool { return h.page.edit.unreadable })
	if !h.tt.HasText("read failed") {
		t.Errorf("the failure is not shown: %q", h.tt.Texts())
	}
	h.page.save()
	h.tt.Frame()
	if h.puts() != 0 {
		t.Fatal("saved a preview after the read failed")
	}
}

func TestStoreUnavailableAndRetry(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.SetScriptsUnavailable(true)
	})
	testkit.UntilText(t, h.tt, "核心未运行")
	if h.tt.HasText("已用") {
		t.Error("the usage shows while the core is down")
	}
	h.srv.SetScriptsUnavailable(false)
	slClick(t, h.tt, "重试")
	testkit.UntilText(t, h.tt, "forecast")
	if h.tt.HasText("核心未运行") {
		t.Error("the error stays after a retry")
	}
}

func TestStoreEmptyStates(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, nil)
	testkit.UntilText(t, h.tt, "暂无存储")
	if _, ok := h.tt.Find("筛选"); ok {
		t.Error("the filter pane shows without namespaces")
	}
}

func TestStoreInactivePageIgnoresResults(t *testing.T) {
	gate := make(chan struct{})
	h := newStoreHarness(t, 1280, 720, func(srv *testkit.Server) {
		slStoreFixture(srv)
		srv.Route(http.MethodGet, "/scripts/store", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			testkit.JSON(api.ScriptStoreList{Namespaces: []api.ScriptNamespace{{Name: "late", Entries: 0}}, Entries: []api.ScriptStoreItem{}})(w, r)
		})
	})
	h.tt.Frame()
	h.page.SetActive(false)
	close(gate)
	time.Sleep(50 * time.Millisecond)
	h.tt.Frame()
	h.tt.Frame()
	if h.page.loaded || len(h.page.namespaces) != 0 {
		t.Error("a result arrived after the page was hidden")
	}
	if h.page.loading {
		t.Error("loading stays set after the page was hidden")
	}
}

func TestStoreValidate(t *testing.T) {
	for _, c := range []struct {
		ns, key, value, want string
	}{
		{"a", "b", "c", ""},
		{"", "b", "c", "请填写命名空间"},
		{"a", "", "c", "请填写键"},
		{"a", strings.Repeat("é", 129), "c", "键超过 256 字节"},
		{"a", strings.Repeat("k", 256), "c", ""},
		{"a", "b", strings.Repeat("v", storeMaxValue), ""},
		{"a", "b", strings.Repeat("v", storeMaxValue+1), "值超过 1 MiB"},
	} {
		if got := storeValidate(c.ns, c.key, c.value); got != c.want {
			t.Errorf("storeValidate(%q, %d, %d) = %q, want %q", c.ns, len(c.key), len(c.value), got, c.want)
		}
	}
}

func TestStorePreviewAndFormat(t *testing.T) {
	if got := storePreview("a\n  b\tc", false); got != "a b c" {
		t.Errorf("preview %q", got)
	}
	if got := storePreview("abc", true); got != "abc…" {
		t.Errorf("truncated preview %q", got)
	}
	long := strings.Repeat("好", 300)
	if got := storePreview(long, false); !strings.HasSuffix(got, "…") || len(got) > 410 {
		t.Errorf("long preview has %d bytes", len(got))
	}
	if out, ok := storeFormatJSON(`{"a":[1,2]}`); !ok || !strings.Contains(out, "\n  \"a\"") {
		t.Errorf("format %q %v", out, ok)
	}
	for _, v := range []string{"", "plain", `"str"`, "123", "{bad"} {
		if _, ok := storeFormatJSON(v); ok {
			t.Errorf("%q formats as JSON", v)
		}
	}
	items := []api.ScriptStoreItem{{Key: "Alpha", Value: "x"}, {Key: "b", Value: "ALPHA beta"}, {Key: "c", Value: "z"}}
	got := storeFilter(items, " alpha ")
	if len(got) != 2 || got[0].Key != "Alpha" || got[1].Key != "b" {
		t.Errorf("filter %+v", got)
	}
}

// The acceptance screenshots of round 27: they are written
// when SCRIPT_PREVIEW_DIR is set.
func TestStoreLogsAcceptanceScreens(t *testing.T) {
	shoot := func(tt *ui.Tester, name string) {
		t.Helper()
		slSavePreview(t, tt, name+"-light.png")
		tt.SetDark(true)
		tt.Advance(600 * time.Millisecond)
		tt.Frame()
		slSavePreview(t, tt, name+"-dark.png")
		tt.SetDark(false)
		tt.Advance(600 * time.Millisecond)
		tt.Frame()
	}

	h := newStoreHarness(t, 1440, 800, slStoreFixture)
	testkit.UntilText(t, h.tt, "forecast")
	slClick(t, h.tt, "forecast")
	testkit.UntilText(t, h.tt, "复制值")
	shoot(h.tt, "12-store")
	slClick(t, h.tt, "编辑")
	testkit.UntilText(t, h.tt, "编辑条目")
	shoot(h.tt, "13-store-edit")

	empty := newStoreHarness(t, 1440, 800, nil)
	testkit.UntilText(t, empty.tt, "暂无存储")
	shoot(empty.tt, "14-store-empty")

	down := newStoreHarness(t, 1440, 800, func(srv *testkit.Server) { srv.SetScriptsUnavailable(true) })
	testkit.UntilText(t, down.tt, "核心未运行")
	shoot(down.tt, "15-store-unavailable")

	narrow := newStoreHarness(t, 600, 800, slStoreFixture)
	testkit.UntilText(t, narrow.tt, "forecast")
	slClick(t, narrow.tt, "切换命名空间筛选")
	testkit.UntilText(t, narrow.tt, "命名空间")
	shoot(narrow.tt, "18-store-narrow-filter")
	slClick(t, narrow.tt, "cache")
	testkit.UntilText(t, narrow.tt, "etag:/v1/items")
	slClick(t, narrow.tt, "etag:/v1/items")
	testkit.UntilText(t, narrow.tt, "复制值")
	shoot(narrow.tt, "18-store-narrow-inspector")
}

func TestRestoreEOL(t *testing.T) {
	cases := []struct{ orig, edited, want string }{
		{"a\r\nb", "a\nb", "a\r\nb"},
		{"a\r\nb", "a\nb!", "a\r\nb!"},
		{"a\r\nb\r\n", "a\nX\nc\n", "a\r\nX\r\nc\r\n"},
		{"a\rb", "a\nb", "a\rb"},
		{"a\rb\nc", "a\nb\nc!", "a\nb\nc!"},
		{"a\nb", "a\nb!", "a\nb!"},
	}
	for _, c := range cases {
		if got := restoreEOL(c.orig, c.edited); got != c.want {
			t.Errorf("restoreEOL(%q, %q) = %q, want %q", c.orig, c.edited, got, c.want)
		}
	}
}

func TestStoreCRLFValueSurvivesEditing(t *testing.T) {
	h := newStoreHarness(t, 1280, 720, func(s *testkit.Server) { s.SetScriptStore("app", "crlf", "a\r\nb") })
	testkit.UntilText(t, h.tt, "crlf")
	h.page.openEdit(api.ScriptStoreItem{Key: "crlf", Value: "a\r\nb"})
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return !h.page.edit.open })
	if got := h.srv.ScriptStore()["app"]["crlf"]; got != "a\r\nb" {
		t.Fatalf("untouched value rewritten: %q", got)
	}
	h.page.openEdit(api.ScriptStoreItem{Key: "crlf", Value: "a\r\nb"})
	h.page.edit.code.SetText(h.page.edit.code.Text() + "!")
	h.tt.Frame()
	slClick(t, h.tt, "保存")
	testkit.Until(t, h.tt, func() bool { return h.srv.ScriptStore()["app"]["crlf"] == "a\r\nb!" })
}
