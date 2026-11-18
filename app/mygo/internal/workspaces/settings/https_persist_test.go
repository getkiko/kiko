package settings

import (
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// persistCore stands for the capture endpoints of the core.
type persistCore struct {
	mu     sync.Mutex
	status api.HTTPStatus
	// failPurge answers the purge with this error text while set.
	failPurge string
	purges    int
}

func (p *persistCore) purged() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.purges
}

func newPersistFixture(t *testing.T, persist *model.MITMPersist, stored int, storedBytes int64) (*fixture, *persistCore) {
	t.Helper()
	doc := mixedDoc()
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true, SaveBody: true, Persist: persist}}
	core := &persistCore{status: api.HTTPStatus{PersistEnabled: persist != nil && persist.Enabled, PersistRecords: stored, PersistBytes: storedBytes}}
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true), Core: api.MITMCore{Connected: true, Enabled: true, AuthorityFingerprint: "ab"}}, doc)
	f.srv.Route("GET", "/http/capabilities", func(w http.ResponseWriter, r *http.Request) {
		core.mu.Lock()
		st := core.status
		core.mu.Unlock()
		testkit.JSON(api.HTTPCapabilities{Supported: true, Enabled: true, Persist: true, Status: &st})(w, r)
	})
	f.srv.Route("POST", "/http/persist/purge", func(w http.ResponseWriter, r *http.Request) {
		core.mu.Lock()
		core.purges++
		message := core.failPurge
		if message == "" {
			core.status.PersistRecords, core.status.PersistBytes = 0, 0
		}
		st := core.status
		core.mu.Unlock()
		if message != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
			return
		}
		testkit.JSON(map[string]any{"status": st})(w, r)
	})
	f.w.https.load()
	testkit.Until(t, f.tt, func() bool { return f.w.https.persist.caps != nil })
	return f, core
}

func (f *fixture) persistSettings() model.MITMPersist { return persistOf(f.mitmSettings()) }

func TestHTTPSPersistOffByDefault(t *testing.T) {
	f, _ := newPersistFixture(t, nil, 0, 0)
	if !f.tt.HasText("抓取存储") || !f.tt.HasText("保存到磁盘") {
		t.Fatalf("no group: %q", f.tt.Texts())
	}
	for _, hidden := range []string{"容量", "条数", "保留", "脱敏请求头", "全部删除"} {
		if f.tt.HasText(hidden) {
			t.Errorf("%q shown while off", hidden)
		}
	}
	f.clickRowControl("保存到磁盘", 16)
	testkit.Until(t, f.tt, func() bool { return f.persistSettings().Enabled && !f.store.Saving() })
	f.tt.Frame()
	for _, shown := range []string{"正文", "容量", "条数", "保留", "脱敏请求头", "已保存"} {
		if !f.tt.HasText(shown) {
			t.Errorf("%q missing once on: %q", shown, f.tt.Texts())
		}
	}
}

func TestHTTPSPersistLimitsAndBody(t *testing.T) {
	f, _ := newPersistFixture(t, &model.MITMPersist{Enabled: true}, 3, 2048)
	f.click("1 GB")
	testkit.Until(t, f.tt, func() bool { return f.persistSettings().MaxMB == 1024 && !f.store.Saving() })
	f.click("10 万")
	testkit.Until(t, f.tt, func() bool { return f.persistSettings().MaxRecords == 100000 && !f.store.Saving() })
	f.click("30 天")
	testkit.Until(t, f.tt, func() bool { return f.persistSettings().RetentionDays == 30 && !f.store.Saving() })
	f.clickRowControl("正文", 16)
	testkit.Until(t, f.tt, func() bool { return f.persistSettings().Body && !f.store.Saving() })
	if !f.persistSettings().Enabled {
		t.Fatal("edits switched the copy off")
	}
	if !f.tt.HasText("3 条 · 2.0 KB") && !f.tt.HasText("3 条 · 2 KB") {
		t.Errorf("summary missing: %q", f.tt.Texts())
	}
}

func TestHTTPSPersistRedactHeaders(t *testing.T) {
	f, _ := newPersistFixture(t, &model.MITMPersist{Enabled: true, RedactHeaders: []string{"X-Old"}}, 0, 0)
	if !f.tt.HasText("X-Old") {
		t.Fatalf("existing name missing: %q", f.tt.Texts())
	}
	f.w.https.persist.redact.text = "X-New"
	f.tt.Frame()
	f.click("添加脱敏请求头")
	testkit.Until(t, f.tt, func() bool { return len(f.persistSettings().RedactHeaders) == 2 && !f.store.Saving() })
	if got := f.persistSettings().RedactHeaders; got[1] != "X-New" {
		t.Fatalf("stored %v", got)
	}
	for text, reason := range map[string]string{"bad name": "请求头名称无效", "x-old": "已在列表中"} {
		f.w.https.persist.redact.text = text
		f.tt.Frame()
		f.click("添加脱敏请求头")
		f.tt.Frame()
		if !f.tt.HasText(reason) {
			t.Errorf("%q: no reason %q in %q", text, reason, f.tt.Texts())
		}
	}
	if len(f.persistSettings().RedactHeaders) != 2 {
		t.Fatalf("invalid names stored: %v", f.persistSettings().RedactHeaders)
	}
	f.click("移除 X-Old")
	testkit.Until(t, f.tt, func() bool { return len(f.persistSettings().RedactHeaders) == 1 && !f.store.Saving() })
}

func TestHTTPSPersistSwitchOffKeepsData(t *testing.T) {
	f, core := newPersistFixture(t, &model.MITMPersist{Enabled: true}, 5, 4096)
	f.clickRowControl("保存到磁盘", 16)
	f.tt.Frame()
	if !f.w.https.persist.offConfirm.Open || !f.tt.HasText("保留数据") || !f.tt.HasText("删除数据") {
		t.Fatalf("no choice: %q", f.tt.Texts())
	}
	if !f.persistSettings().Enabled {
		t.Fatal("switched off before the confirmation")
	}
	f.tt.Key(0, ui.KeyEnter)
	testkit.Until(t, f.tt, func() bool {
		return !f.w.https.persist.offConfirm.Open && !f.persistSettings().Enabled && !f.store.Saving()
	})
	if core.purged() != 0 {
		t.Fatal("data deleted although kept was chosen")
	}
	if f.persistSettings().MaxMB != 0 || f.tt.HasText("脱敏请求头") {
		t.Fatalf("settings or rows after switching off: %q", f.tt.Texts())
	}
	// Stored data stays listed with its delete action.
	if !f.tt.HasText("全部删除") {
		t.Errorf("stored data not offered for deletion: %q", f.tt.Texts())
	}
}

func TestHTTPSPersistSwitchOffDeletesData(t *testing.T) {
	f, core := newPersistFixture(t, &model.MITMPersist{Enabled: true}, 5, 4096)
	f.clickRowControl("保存到磁盘", 16)
	f.tt.Frame()
	f.click("删除数据")
	f.tt.Frame()
	core.mu.Lock()
	core.failPurge = "磁盘忙"
	core.mu.Unlock()
	f.click("关闭")
	testkit.UntilText(t, f.tt, "磁盘忙")
	if !f.persistSettings().Enabled || !f.tt.HasText("重试") {
		t.Fatalf("failure must keep the copy on and offer a retry: %q", f.tt.Texts())
	}
	core.mu.Lock()
	core.failPurge = ""
	core.mu.Unlock()
	f.click("重试")
	testkit.Until(t, f.tt, func() bool {
		return !f.w.https.persist.offConfirm.Open && !f.persistSettings().Enabled && !f.store.Saving()
	})
	if core.purged() != 2 {
		t.Fatalf("purge calls %d", core.purged())
	}
}

func TestHTTPSPersistPurgeAll(t *testing.T) {
	f, core := newPersistFixture(t, &model.MITMPersist{Enabled: true}, 7, 1<<20)
	f.click("全部删除")
	f.tt.Frame()
	if !f.tt.HasText("删除全部已保存的请求？") {
		t.Fatalf("no confirmation: %q", f.tt.Texts())
	}
	if core.purged() != 0 {
		t.Fatal("deleted before the confirmation")
	}
	f.tt.Key(0, ui.KeyEnter)
	testkit.Until(t, f.tt, func() bool { return core.purged() == 1 && !f.w.https.persist.purgeAll.Open })
	testkit.Until(t, f.tt, func() bool {
		return f.w.https.persist.caps != nil && f.w.https.persist.caps.Status != nil && f.w.https.persist.caps.Status.PersistRecords == 0
	})
	if !f.persistSettings().Enabled {
		t.Fatal("deleting the data switched the copy off")
	}
}

func TestHTTPSPersistShowsWriteFailures(t *testing.T) {
	f, core := newPersistFixture(t, &model.MITMPersist{Enabled: true}, 1, 100)
	core.mu.Lock()
	core.status.PersistErrors, core.status.PersistDropped = 3, 9
	core.mu.Unlock()
	f.w.https.load()
	testkit.UntilText(t, f.tt, "3 条写入失败")
}

func TestHTTPSPersistScreenshot(t *testing.T) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SETTINGS_PREVIEW_DIR not set")
	}
	f, _ := newPersistFixture(t, &model.MITMPersist{Enabled: true, Body: true, MaxMB: 256, RetentionDays: 7, RedactHeaders: []string{"X-Session-Key", "X-Device-Token"}}, 1284, 38<<20)
	f.tt.SetSize(width, 2200)
	f.tt.Frame()
	f.save("persist-settings-light.png")
	f.tt.SetDark(true)
	f.tt.Advance(time.Second)
	f.tt.Frame()
	f.save("persist-settings-dark.png")
	f.tt.SetDark(false)
	f.tt.Frame()
	f.clickRowControl("保存到磁盘", 16)
	f.tt.Frame()
	f.click("删除数据")
	f.tt.Frame()
	f.save("persist-off-confirm-light.png")
	f.tt.SetDark(true)
	f.tt.Advance(time.Second)
	f.tt.Frame()
	f.save("persist-off-confirm-dark.png")
}
