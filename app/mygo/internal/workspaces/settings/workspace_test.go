package settings

import (
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const width, height = 1100, 1200

// fixture is the workspace against a fake server.
type fixture struct {
	t     *testing.T
	srv   *testkit.Server
	env   *env.Env
	store *store.Store
	w     *Workspace
	tt    *ui.Tester

	mu     sync.Mutex
	bodies map[string]json.RawMessage
	sync   api.SyncStatus
	proxy  api.SystemProxyStatus
	conns  api.ConnectionSettings
}

func (f *fixture) record(r *http.Request) json.RawMessage {
	var raw json.RawMessage
	_ = testkit.Body(r, &raw)
	f.mu.Lock()
	f.bodies[r.Method+" "+r.URL.Path] = raw
	f.mu.Unlock()
	return raw
}

func (f *fixture) body(key string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[key]
}

func (f *fixture) setSync(status api.SyncStatus) {
	f.mu.Lock()
	f.sync = status
	f.mu.Unlock()
}

func (f *fixture) syncReply(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	f.mu.Lock()
	status := f.sync
	f.mu.Unlock()
	testkit.JSON(status)(w, r)
}

func newFixture(t *testing.T, doc *model.Doc) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t), bodies: map[string]json.RawMessage{},
		sync: api.SyncStatus{State: api.SyncUnconfigured, DeviceID: "device-1234567890"},
		proxy: api.SystemProxyStatus{Supported: true, SystemProxySettings: api.SystemProxySettings{Host: "", GuardInterval: 30,
			DefaultBypass: true, ValidateBypass: true, Bypass: []string{"*.lan"}}}}
	if doc != nil {
		raw, _ := json.Marshal(doc)
		f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	}
	f.srv.Route("GET", "/sync/status", f.syncReply)
	for _, path := range []string{"/sync/config", "/sync/unlock", "/sync/lock", "/sync/auto", "/sync/reset", "/sync/change-password"} {
		method := "POST"
		if path == "/sync/config" {
			method = "PUT"
		}
		f.srv.Route(method, path, func(w http.ResponseWriter, r *http.Request) {
			f.syncReply(w, r)
		})
	}
	f.srv.Route("POST", "/sync/now", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		f.mu.Lock()
		status := f.sync
		f.mu.Unlock()
		testkit.JSON(map[string]any{"status": status, "result": api.SyncResult{Pulled: 2}})(w, r)
	})
	f.srv.Route("GET", "/system-proxy", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		status := f.proxy
		f.mu.Unlock()
		testkit.JSON(status)(w, r)
	})
	f.srv.Route("PUT", "/system-proxy", func(w http.ResponseWriter, r *http.Request) {
		raw := f.record(r)
		f.mu.Lock()
		_ = json.Unmarshal(raw, &f.proxy.SystemProxySettings)
		status := f.proxy
		f.mu.Unlock()
		testkit.JSON(status)(w, r)
	})
	f.conns = api.ConnectionSettings{ClosedLimit: 1000}
	f.srv.Route("GET", "/connections/settings", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		settings := f.conns
		f.mu.Unlock()
		testkit.JSON(settings)(w, r)
	})
	f.srv.Route("PATCH", "/connections/settings", func(w http.ResponseWriter, r *http.Request) {
		raw := f.record(r)
		f.mu.Lock()
		_ = json.Unmarshal(raw, &f.conns)
		settings := f.conns
		f.mu.Unlock()
		testkit.JSON(settings)(w, r)
	})
	f.env, _ = testkit.NewEnv(f.srv)
	f.store = store.New(f.env)
	f.w = New(f.env, f.store)
	f.env.Server.Start()
	t.Cleanup(f.env.Server.Stop)
	f.store.Load()
	f.tt = ui.NewTester(f.view, width, height)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded && f.env.Server.Connected })
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision >= 2 && !f.store.Saving() })
	f.w.SetActive(true)
	t.Cleanup(func() { f.w.SetActive(false) })
	testkit.Until(t, f.tt, func() bool {
		return f.w.info != nil && f.w.sync != nil && f.w.proxy != nil && f.w.connections != nil
	})
	return f
}

func (f *fixture) view(c *ui.Context) {
	kit.Apply(c)
	f.env.Drain(c)
	c.Root().Background(kit.T(c).Surface)
	f.w.View(c)
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
}

// clickBelow clicks the input under a label of a dialog form.
func (f *fixture) clickBelow(label string) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q in %q", label, f.tt.Texts())
	}
	f.tt.ClickAt(r.X+20, r.Y+r.H+4+15)
}

// clickRowControl clicks the control at the right end of a settings row,
// in the left or the right of the two settings columns.
func (f *fixture) clickRowControl(label string, inset float32) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q in %q", label, f.tt.Texts())
	}
	content := min(float32(width-2*28), kit.SettingsWidth)
	if f.w.page != pageMain {
		content = min(float32(width-2*28), kit.SettingsFormWidth)
	}
	left := (float32(width) - content) / 2
	right := left + content
	if column := (content - kit.SettingsGap) / 2; f.w.page == pageMain && r.X < left+column {
		right = left + column
	}
	f.tt.ClickAt(right-16-inset, r.Y+r.H/2)
}

func (f *fixture) requested(request string) bool {
	return slices.Contains(f.srv.Requests(), request)
}

func (f *fixture) save(name string) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f.tt.Advance(time.Second) // switches and indicators at rest
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, f.tt.Image()); err != nil {
		f.t.Fatal(err)
	}
}

func mixedDoc() *model.Doc {
	doc := model.EmptyDoc("linux")
	doc.Inbounds = []*model.Inbound{inbound("mixed", "mixed", true, model.Fields{"listen": "127.0.0.1", "listen_port": 7890.0})}
	return doc
}

func TestSettingsRowsEditTheDocument(t *testing.T) {
	f := newFixture(t, mixedDoc())
	for _, text := range []string{"设置", "通用网络配置", "高级 TLS 配置", "启用 IPv6", "DNS 服务器", "日志等级", "加密备份与同步", "127.0.0.1:7890", "kiko-server"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q", text)
		}
	}
	revision := f.srv.Store().Revision
	f.click("INFO")
	f.click("DEBUG")
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision > revision && !f.store.Saving() })
	if f.store.Doc().Log.Level != "debug" {
		t.Errorf("log level %q", f.store.Doc().Log.Level)
	}
	revision = f.srv.Store().Revision
	f.clickRowControl("启用 IPv6", 16)
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision > revision && !f.store.Saving() })
	if doc := f.store.Doc(); !doc.IPv6Enabled || doc.DNS.Strategy != "prefer_ipv6" {
		t.Errorf("ipv6 %v strategy %q", doc.IPv6Enabled, doc.DNS.Strategy)
	}
	var saved model.Doc
	_ = json.Unmarshal(f.srv.Store().Doc, &saved)
	if !saved.IPv6Enabled || saved.Log.Level != "debug" {
		t.Errorf("server doc %+v", saved.Log)
	}
	f.save("settings-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("settings-dark.png")
}

func TestHelperHiddenOffMac(t *testing.T) {
	f := newFixture(t, nil)
	if f.tt.HasText("TUN 特权助手") || f.tt.HasText("安装") {
		t.Errorf("helper row shown on linux: %q", f.tt.Texts())
	}
}

func TestCopyProxyCommand(t *testing.T) {
	f := newFixture(t, mixedDoc())
	f.click("复制系统代理")
	f.tt.Frame()
	f.click("复制为 Fish")
	if got := f.tt.Clipboard(); !strings.HasPrefix(got, "set -gx http_proxy http://127.0.0.1:7890") {
		t.Errorf("clipboard %q", got)
	}
}

func TestSystemProxySave(t *testing.T) {
	f := newFixture(t, mixedDoc())
	f.click("系统代理设置")
	testkit.UntilText(t, f.tt, "当前绕过：")
	if !f.tt.HasText("*.lan") || !f.tt.HasText("<local>") {
		t.Errorf("bypass chips %q", f.tt.Texts())
	}
	f.w.proxyDialog.pac = true
	f.click("保存")
	testkit.Until(t, f.tt, func() bool { return !f.w.proxyDialog.shown })
	var sent api.SystemProxySettings
	if err := json.Unmarshal(f.body("PUT /api/v1/system-proxy"), &sent); err != nil {
		t.Fatal(err)
	}
	if !sent.PAC || sent.GuardInterval != 30 || !slices.Equal(sent.Bypass, []string{"*.lan"}) {
		t.Errorf("sent %+v", sent)
	}
	// An invalid interval is refused before sending.
	f.click("系统代理设置")
	f.w.proxyDialog.interval = "0"
	f.click("保存")
	if !f.w.proxyDialog.shown {
		t.Error("invalid interval closed the dialog")
	}
}

func TestBackupConfigureUnlockSyncReset(t *testing.T) {
	f := newFixture(t, nil)
	f.click("加密备份与同步")
	testkit.UntilText(t, f.tt, "WebDAV 地址")
	f.click("保存")
	if !f.tt.HasText("请填写 WebDAV 地址") || f.requested("PUT /api/v1/sync/config") {
		t.Fatalf("empty form saved: %q", f.tt.Texts())
	}
	f.clickBelow("WebDAV 地址")
	f.tt.Type("https://dav.example.com/dav")
	f.setSync(api.SyncStatus{Configured: true, State: api.SyncLocked, DeviceID: "device-1234567890",
		Backend: &api.SyncBackendConfig{Type: api.SyncWebDAV, WebDAV: &api.SyncWebDAVConfig{URL: "https://dav.example.com/dav", Path: "kiko/kiko-vault.json"}}})
	f.click("保存")
	testkit.UntilText(t, f.tt, "解锁")
	var config struct {
		Backend api.SyncBackendConfig `json:"backend"`
	}
	_ = json.Unmarshal(f.body("PUT /api/v1/sync/config"), &config)
	if config.Backend.WebDAV == nil || config.Backend.WebDAV.URL != "https://dav.example.com/dav" || config.Backend.WebDAV.Path != "kiko/kiko-vault.json" {
		t.Errorf("config %s", f.body("PUT /api/v1/sync/config"))
	}
	f.w.backup.password = "my-password"
	unlocked := f.sync
	unlocked.State, unlocked.LastSyncedAt = api.SyncUnlocked, 1
	f.setSync(unlocked)
	f.click("解锁")
	testkit.UntilText(t, f.tt, "立即同步")
	if string(f.body("POST /api/v1/sync/unlock")) != `{"password":"my-password"}` {
		t.Errorf("unlock body %s", f.body("POST /api/v1/sync/unlock"))
	}
	f.click("立即同步")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/sync/now") && f.w.backup.busy == "" })
	f.click("重置云端备份…")
	f.click("删除云端备份")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/sync/reset") })
	if string(f.body("POST /api/v1/sync/reset")) != `{"confirm":true}` {
		t.Errorf("reset body %s", f.body("POST /api/v1/sync/reset"))
	}
	f.save("backup-light.png")
}

func TestSyncEventUpdatesSummary(t *testing.T) {
	f := newFixture(t, nil)
	f.srv.Emit("sync", api.SyncStatus{Configured: true, State: api.SyncLocked})
	testkit.UntilText(t, f.tt, "已锁定 · 输入密码后即可同步")
}

func TestNetworkLibraryCreateAndDelete(t *testing.T) {
	doc := model.EmptyDoc("linux")
	doc.Outbounds = []*model.Outbound{{Entity: model.Entity{ID: "o1", Tag: "节点", Enabled: true,
		NetworkConfigRefs: model.NetworkConfigRefs{"default": "net-used"}}, Kind: "outbound", Type: "direct", Fields: model.Fields{}}}
	f := newFixture(t, doc)
	f.click("通用网络配置")
	testkit.UntilText(t, f.tt, "新建配置")
	f.click("新建配置")
	testkit.UntilText(t, f.tt, "使用位置 · 0")
	f.save("library-editor.png")
	revision := f.srv.Store().Revision
	f.click("保存配置")
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision > revision && !f.store.Saving() })
	library := f.store.NetworkConfigs()
	if len(library) != 1 || library[0].Name != "新建网络配置" || library[0].Kind != model.NetworkConfigNetwork {
		t.Fatalf("library %+v", library)
	}
	if !f.tt.HasText("新建网络配置") || !f.tt.HasText("核心默认") || f.w.library.editorOpen {
		t.Errorf("after save: %q", f.tt.Texts())
	}
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("library-dark.png")
	f.tt.SetDark(false)
	// A config in use cannot be deleted.
	used := *library[0]
	used.ID, used.Name = "net-used", "已引用"
	f.store.SetNetworkConfigs([]*model.NetworkConfig{library[0], &used})
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	f.click("已引用")
	testkit.UntilText(t, f.tt, "使用位置 · 1")
	f.click("删除")
	if !f.tt.HasText("此配置有 1 处引用，请先更换引用") || f.w.library.remove.Open {
		t.Errorf("used config deletable: %q", f.tt.Texts())
	}
	f.click("取消")
	// An unused one can.
	f.click("新建网络配置")
	testkit.UntilText(t, f.tt, "使用位置 · 0")
	f.click("删除")
	f.click("删除配置")
	testkit.Until(t, f.tt, func() bool { return len(f.store.NetworkConfigs()) == 1 && !f.store.Saving() })
	if f.store.NetworkConfigs()[0].ID != "net-used" {
		t.Errorf("deleted the wrong config: %+v", f.store.NetworkConfigs())
	}
	f.click("返回设置")
	if f.w.page != pageMain {
		t.Error("back to settings")
	}
}

// A read of the system proxy that a save overtakes does not show the
// state from before the save.
func TestSystemProxyReadOvertakenBySave(t *testing.T) {
	f := newFixture(t, mixedDoc())
	gate := make(chan struct{})
	f.srv.Route("GET", "/system-proxy", func(w http.ResponseWriter, r *http.Request) {
		<-gate
		testkit.JSON(api.SystemProxyStatus{Supported: true})(w, r)
	})
	f.w.loadProxy()
	f.w.toggleSystemProxy(true)
	testkit.Until(t, f.tt, func() bool { return f.w.proxy.Enabled })
	close(gate)
	for range 20 {
		f.tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if !f.w.proxy.Enabled {
		t.Error("an older read replaced the saved system proxy state")
	}
}

// A sync request without a busy key leaves the running one's busy state.
func TestBackupKeylessRunKeepsBusy(t *testing.T) {
	f := newFixture(t, nil)
	b := &f.w.backup
	b.busy = "sync"
	done := false
	b.run("", func(ctx context.Context) (api.SyncStatus, error) { return f.w.env.API.SyncAuto(ctx, true) }, func() { done = true }, nil)
	if b.busy != "sync" {
		t.Fatalf("busy %q while the keyless request runs", b.busy)
	}
	testkit.Until(t, f.tt, func() bool { return done })
	if b.busy != "sync" {
		t.Errorf("busy %q after the keyless request", b.busy)
	}
}

// The groups lay out in two columns side by side in a wide window and
// stack in order in a narrow one.
func TestSettingsColumns(t *testing.T) {
	f := newFixture(t, mixedDoc())
	find := func(label string) ui.Rect {
		t.Helper()
		r, ok := f.tt.Find(label)
		if !ok {
			t.Fatalf("no %q in %q", label, f.tt.Texts())
		}
		return r
	}
	network, logs := find("网络"), find("日志管理")
	if network.Y != logs.Y || logs.X < network.X+kit.SettingsColumn {
		t.Errorf("wide: 网络 at %v, 日志管理 at %v, not side by side", network, logs)
	}
	f.tt.SetSize(640, height)
	f.tt.Frame()
	network, logs = find("网络"), find("日志管理")
	if network.X != logs.X || logs.Y <= find("通用").Y {
		t.Errorf("narrow: 网络 at %v, 日志管理 at %v, not stacked after 通用", network, logs)
	}
	f.save("settings-narrow.png")
}

// The closed connection limit: a count to pick, or every record with the
// switch off; switched on again, the last count comes back.
func TestSettingsClosedConnectionLimit(t *testing.T) {
	f := newFixture(t, mixedDoc())
	limit := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.conns.ClosedLimit
	}
	settled := func() bool { return !f.w.connectionsBusy }
	if !f.tt.HasText("只保留最近关闭的 1,000 条连接") {
		t.Fatalf("no default limit in %q", f.tt.Texts())
	}
	f.click("1,000 条（默认）")
	f.click("5,000 条")
	testkit.Until(t, f.tt, func() bool { return settled() && limit() == 5000 })
	f.clickRowControl("自动清理已关闭连接", 16)
	testkit.Until(t, f.tt, func() bool { return settled() && limit() == 0 })
	if f.tt.HasText("保留数量") || !f.tt.HasText("关闭后保留全部已关闭连接，记录会持续占用服务端内存") {
		t.Errorf("switched off, texts %q", f.tt.Texts())
	}
	f.save("settings-connections-off.png")
	f.clickRowControl("自动清理已关闭连接", 16)
	testkit.Until(t, f.tt, func() bool { return settled() && limit() == 5000 })
	if !f.tt.HasText("5,000 条") {
		t.Errorf("switched on, texts %q", f.tt.Texts())
	}
	f.save("settings-connections.png")
}

// The browser's home page is a setting of the document, empty for the
// server's default.
func TestBrowserHomeSetting(t *testing.T) {
	f := newFixture(t, mixedDoc())
	if !f.tt.HasText("浏览器默认页面") {
		t.Fatalf("no browser home row: %q", f.tt.Texts())
	}
	revision := f.srv.Store().Revision
	// The input sits right of the row's title.
	r, _ := f.tt.Find("浏览器默认页面")
	f.tt.ClickAt(r.X+300, r.Y+r.H/2)
	f.tt.Frame()
	f.tt.Type(" https://example.com/ip ")
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision > revision && !f.store.Saving() })
	testkit.Until(t, f.tt, func() bool { return f.store.Doc().BrowserHomeURL == "https://example.com/ip" })
	f.save("settings-browser-home.png")
}
