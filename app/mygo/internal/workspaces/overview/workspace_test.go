package overview

import (
	"encoding/json"
	"fmt"
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
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/subscriptions/endpoints"
)

type fixture struct {
	w     *Workspace
	tt    *ui.Tester
	srv   *testkit.Server
	env   *env.Env
	store *store.Store
	shell *testkit.Shell

	mu      sync.Mutex
	bodies  map[string]string
	runtime api.Runtime
	// sidebar shows the sidebar's apply entry beside the page, folded
	// with folded.
	sidebar, folded bool
}

func f64(v float64) *float64 { return &v }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, nil)
}

// newFixtureWith is newFixture serving doc, when set, with the Components page's
// Tailscale panel on the page.
func newFixtureWith(t *testing.T, doc *model.Doc) *fixture {
	t.Helper()
	srv := testkit.NewServer(t)
	if doc != nil {
		raw, _ := json.Marshal(doc)
		srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
		srv.Route("GET", "/endpoints/status", testkit.JSON(map[string]any{"tailscale": nil}))
		srv.Route("GET", "/taildrop/sends", testkit.JSON([]any{}))
		srv.Route("GET", "/taildrop/directory", testkit.JSON(map[string]string{"path": "/var/lib/kiko/taildrop"}))
	}
	e, shell := testkit.NewEnv(srv)
	f := &fixture{srv: srv, env: e, shell: shell, bodies: map[string]string{}, runtime: api.Runtime{State: "stopped"}}
	record := func(name string, r *http.Request) {
		var body map[string]any
		_ = testkit.Body(r, &body)
		raw := ""
		for k, v := range body {
			if s, ok := v.(string); ok {
				raw += k + "=" + s + ";"
			}
		}
		f.mu.Lock()
		f.bodies[name] = raw
		f.mu.Unlock()
	}
	points := []api.MetricPoint{}
	for i := range 20 {
		v := float64(i * 4096)
		points = append(points, api.MetricPoint{Time: int64(i), Upload: f64(v / 2), Download: f64(v), Memory: f64(32 << 20),
			Connections: f64(float64(i % 7)), UploadTotal: "1048576", DownloadTotal: "4194304"})
	}
	// The latest sample is what TestRuntimeFollowsEvents's runtime reports:
	// the figures show it.
	points = append(points, api.MetricPoint{Time: 20, Upload: f64(512), Download: f64(1536), Memory: f64(16 << 20),
		Connections: f64(3), UploadTotal: "1048576", DownloadTotal: "3145728"})
	srv.Route("GET", "/metrics/history", testkit.JSON(map[string]any{"points": points}))
	srv.Route("POST", "/config/apply", func(w http.ResponseWriter, r *http.Request) {
		record("apply", r)
		testkit.JSON(map[string]any{"applied": true})(w, r)
	})
	control := func(state string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			runtime := api.Runtime{State: state, DesiredRunning: state == "running", MetricsAvailable: state == "running"}
			srv.SetRuntime(runtime)
			testkit.JSON(runtime)(w, r)
		}
	}
	srv.Route("POST", "/runtime/start", control("running"))
	srv.Route("POST", "/runtime/stop", control("stopped"))
	srv.Route("GET", "/runtime/selectors", testkit.JSON(map[string]any{"selectors": []api.Selector{
		{Name: "自动", Type: "URLTest", Now: "香港", All: []string{"香港", "日本"}},
		{Name: "节点选择", Type: "Selector", Now: "香港", All: []string{"香港", "日本", "美国"}},
	}}))
	srv.Route("PATCH", "/runtime/selector", func(w http.ResponseWriter, r *http.Request) {
		record("select", r)
		w.WriteHeader(http.StatusNoContent)
	})
	f.store = store.New(e)
	f.w = New(e, f.store)
	if doc != nil {
		f.w.Tailscale = endpoints.New(e, f.store).Tailscale()
	}
	e.Server.Start()
	t.Cleanup(e.Server.Stop)
	f.store.Load()
	f.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		e.Drain(c)
		if !f.sidebar {
			f.w.View(c)
			return
		}
		ui.Row(c).Fill().Children(func() {
			width := float32(140)
			if f.folded {
				width = 56
			}
			ui.Column(c).Width(width).Padding(0, 8, 10).Shrink(0).Justify(ui.End).Background(kit.T(c).Sidebar).
				Children(func() { f.w.SidebarApply(c, f.folded) })
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { f.w.View(c) })
		})
	}, 1320, 860)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded && e.Server.Connected && len(f.store.Profiles()) == 1 })
	f.w.SetActive(true)
	t.Cleanup(func() { f.w.SetActive(false) })
	return f
}

// emitRuntime changes the server's runtime and sends it as an event.
func (f *fixture) emitRuntime(runtime api.Runtime) {
	f.srv.SetRuntime(runtime)
	f.srv.Emit("runtime", runtime)
}

func (f *fixture) body(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[name]
}

func (f *fixture) requested(request string) bool {
	return slices.ContainsFunc(f.srv.Requests(), func(r string) bool { return strings.HasPrefix(r, request) })
}

func (f *fixture) click(t *testing.T, label string) {
	t.Helper()
	if err := f.tt.Click(label); err != nil {
		t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
}

func TestRuntimeFollowsEvents(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "已停止")
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true, UptimeSeconds: 75, MetricsAvailable: true,
		DownloadRate: f64(1536), UploadRate: f64(512), MemoryBytes: f64(16 << 20), ActiveConnections: f64(3),
		UploadTotal: f64(1 << 20), DownloadTotal: f64(3 << 20)})
	testkit.UntilText(t, f.tt, "运行中")
	for _, text := range []string{"1分钟", "1.5 KB/s", "512 B/s", "16 MB", "3 MB", "1 MB", "3"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q: %q", text, f.tt.Texts())
		}
	}
	reason := 4.0
	f.emitRuntime(api.Runtime{State: "degraded", RestartInSeconds: &reason, Error: "崩溃"})
	testkit.UntilText(t, f.tt, "等待重试（4 秒）")
	f.emitRuntime(api.Runtime{State: "reloading", Reload: &api.Reload{Phase: "recovering"}})
	testkit.UntilText(t, f.tt, "正在恢复原配置")
}

func TestPowerButtonStartsAndStops(t *testing.T) {
	f := newFixture(t)
	testkit.UntilText(t, f.tt, "已停止")
	f.click(t, "启动")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/runtime/start") })
	if !f.requested("POST /api/v1/config/apply") {
		t.Errorf("start did not apply first: %v", f.srv.Requests())
	}
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find("停止"); return ok })
	f.click(t, "停止")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/runtime/stop") })
	testkit.UntilText(t, f.tt, "已停止")
}

func TestReloadAppliesToTheRunningCore(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "运行中")
	b, ok := f.tt.Find("停止")
	if !ok {
		t.Fatal("no stop button")
	}
	f.tt.Move(b.X+b.W/2, b.Y+b.H/2)
	// The hover unfolds the reload button.
	f.tt.Advance(powerMotion + 50*time.Millisecond)
	f.click(t, "重载配置")
	testkit.Until(t, f.tt, func() bool { return f.requested("POST /api/v1/config/apply") })
	testkit.Until(t, f.tt, func() bool { return f.requested("GET /api/v1/runtime") })
}

func TestSelectorSwitchesOutbound(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true})
	testkit.UntilText(t, f.tt, "自动")
	// Selectors come before URL tests.
	a, _ := f.tt.Find("节点选择")
	b, _ := f.tt.Find("自动")
	if a.X > b.X && a.Y >= b.Y {
		t.Errorf("selector after URL test: %v %v", a, b)
	}
	f.click(t, "节点选择")
	testkit.UntilText(t, f.tt, "美国")
	// The member row follows its region's header (F7 item 21), which
	// tests the region's servers.
	head, ok := f.tt.Find("测速本分组 美国")
	if !ok {
		t.Fatalf("no region header: %q", f.tt.Texts())
	}
	f.tt.ClickAt(head.X+head.W/2, head.Y+head.H+chooserRowH/2)
	testkit.Until(t, f.tt, func() bool { return f.body("select") != "" })
	if got := f.body("select"); !strings.Contains(got, "selector=节点选择") || !strings.Contains(got, "outbound=美国") {
		t.Errorf("select body %q", got)
	}
	testkit.Until(t, f.tt, func() bool { return !f.w.selector.open })
	if !f.tt.HasText("美国") {
		t.Errorf("card does not show the choice: %q", f.tt.Texts())
	}
	// Stopping clears the groups.
	f.emitRuntime(api.Runtime{State: "stopped"})
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("自动") })
}

func TestProfilesCreateSwitchAndDelete(t *testing.T) {
	f := newFixture(t)
	first := f.store.ActiveProfileID()
	f.click(t, "添加配置")
	f.click(t, "新建配置")
	f.tt.Type("公司")
	f.click(t, "保存")
	testkit.UntilText(t, f.tt, "已新建「公司」")
	if len(f.store.Profiles()) != 2 || f.store.ActiveProfileID() == first {
		t.Fatalf("profiles %d active %q", len(f.store.Profiles()), f.store.ActiveProfileID())
	}
	// Switch back through the chooser.
	f.click(t, "选择配置")
	f.click(t, "未命名配置")
	testkit.Until(t, f.tt, func() bool { return f.store.ActiveProfileID() == first })
	testkit.UntilText(t, f.tt, "已加载「未命名配置」")
	// Delete the active profile: the other one loads.
	f.click(t, "管理配置")
	f.click(t, "删除当前配置")
	testkit.UntilText(t, f.tt, "删除「未命名配置」？")
	if len(f.store.Profiles()) != 2 {
		t.Fatal("deleted before confirming")
	}
	f.click(t, "删除")
	testkit.UntilText(t, f.tt, "已删除「未命名配置」")
	if len(f.store.Profiles()) != 1 || f.store.Profiles()[0].Name != "公司" || f.store.ActiveProfileID() != f.store.Profiles()[0].ID {
		t.Errorf("after delete: %v active %q", f.store.Profiles(), f.store.ActiveProfileID())
	}
	testkit.Until(t, f.tt, func() bool {
		s := f.srv.Store()
		return s.ActiveProfileID != nil && *s.ActiveProfileID == f.store.ActiveProfileID()
	})
}

func TestProfilesRenameAndDuplicate(t *testing.T) {
	f := newFixture(t)
	f.click(t, "管理配置")
	f.click(t, "复制")
	testkit.UntilText(t, f.tt, "已复制为「未命名配置 副本」")
	f.click(t, "管理配置")
	f.click(t, "改名")
	f.tt.Key(ui.Cmd, ui.KeyA)
	f.tt.Type("未命名配置 副本")
	testkit.UntilText(t, f.tt, "已存在同名配置")
	f.tt.Key(ui.Cmd, ui.KeyA)
	f.tt.Type("家里")
	f.click(t, "保存")
	testkit.UntilText(t, f.tt, "已改名为「家里」")
}

func TestExportWritesThroughTheShell(t *testing.T) {
	f := newFixture(t)
	f.click(t, "管理配置")
	f.click(t, "导出全部")
	testkit.Until(t, f.tt, func() bool { return f.saved("kiko-all-profiles.json") != "" })
	if data := f.saved("kiko-all-profiles.json"); !strings.Contains(data, `"format": "kiko-profiles"`) || !strings.Contains(data, "未命名配置") {
		t.Errorf("export %s", data)
	}
	f.click(t, "管理配置")
	f.click(t, "导出当前")
	testkit.Until(t, f.tt, func() bool { return f.saved("未命名配置.kiko.json") != "" })
}

func (f *fixture) saved(name string) string {
	data, _ := f.shell.SavedFile(name)
	return string(data)
}

func TestImportFromFile(t *testing.T) {
	f := newFixture(t)
	backup, err := os.ReadFile(filepath.Join("testdata", "backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.shell.Open = backup
	f.click(t, "管理配置")
	f.click(t, "导入")
	testkit.UntilText(t, f.tt, "已导入 1 套配置")
	if len(f.store.Profiles()) != 2 || f.store.Doc().Inbounds[0].Tag != "导入入站" {
		t.Errorf("profiles %d", len(f.store.Profiles()))
	}
}

func TestImportFromURL(t *testing.T) {
	f := newFixture(t)
	backup, err := os.ReadFile(filepath.Join("testdata", "backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var headers string
	f.srv.Route("POST", "/fetch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		_ = testkit.Body(r, &body)
		f.mu.Lock()
		headers = body.Headers["Authorization"]
		f.mu.Unlock()
		testkit.JSON(api.FetchResult{Status: 200, Body: string(backup)})(w, r)
	})
	f.click(t, "添加配置")
	f.click(t, "从 URL 导入")
	f.tt.Type("https://example.com/config.json")
	f.w.profiles.headers = "Authorization: Bearer abc"
	f.click(t, "下载并导入")
	testkit.UntilText(t, f.tt, "已导入 1 套配置")
	f.mu.Lock()
	defer f.mu.Unlock()
	if headers != "Bearer abc" || f.w.profiles.urlOpen {
		t.Errorf("headers %q open %v", headers, f.w.profiles.urlOpen)
	}
}

func TestProfilePreview(t *testing.T) {
	f := newFixture(t)
	f.click(t, "管理配置")
	f.click(t, "预览")
	testkit.UntilText(t, f.tt, "未命名配置 · config.json")
	f.w.profiles.preview.query = "outbounds"
	f.tt.Frame()
	if len(f.w.profiles.preview.matches) == 0 || !f.tt.HasText("1 / 1") {
		t.Errorf("matches %v", f.w.profiles.preview.matches)
	}
	f.click(t, "复制原始配置")
	if !strings.Contains(f.tt.Clipboard(), "outbounds") {
		t.Errorf("clipboard %q", f.tt.Clipboard())
	}
	savePreview(t, f.tt, "overview-preview.png")
}

func TestShareSavesAndOpensUploadPage(t *testing.T) {
	f := newFixture(t)
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "https://share.example.com")
	f.click(t, "管理配置")
	f.click(t, "分享当前配置")
	testkit.UntilText(t, f.tt, "上传 Kiko 配置源文件")
	f.w.profiles.share.confirmed = true
	f.click(t, "验证并上传")
	testkit.Until(t, f.tt, func() bool { return f.saved("config.kiko.json") != "" && f.w.profiles.share.done })
}

func TestScreenshots(t *testing.T) {
	f := newFixture(t)
	f.emitRuntime(api.Runtime{State: "running", DesiredRunning: true, UptimeSeconds: 3725, MetricsAvailable: true,
		DownloadRate: f64(393216), UploadRate: f64(131072), MemoryBytes: f64(17 << 20), ActiveConnections: f64(12),
		UploadTotal: f64(12 << 20), DownloadTotal: f64(48 << 20)})
	testkit.UntilText(t, f.tt, "自动")
	testkit.Until(t, f.tt, func() bool { return len(f.w.metrics.points) > 0 })
	savePreview(t, f.tt, "overview-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	savePreview(t, f.tt, "overview-dark.png")
	f.click(t, "管理配置")
	testkit.UntilText(t, f.tt, "导出全部")
	savePreview(t, f.tt, "overview-menu-dark.png")
}

// savePreview writes the frame where OVERVIEW_PREVIEW_DIR says.
func savePreview(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("OVERVIEW_PREVIEW_DIR")
	if dir == "" {
		return
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func tailscaleDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	ts := model.MakeEndpoint("tailscale", "Tailscale", false)
	ts.ID = "ep-ts"
	ts.Fields["hostname"] = "home"
	doc.Endpoints = []*model.Endpoint{ts}
	return doc
}

// TestTailscaleCard: the card shows an enabled Tailscale endpoint, "Not running"
// before a state, then the live state; the core stopped, it keeps the
// last device seen; it opens the control sheet, and leaves with the
// endpoint disabled.
func TestTailscaleCard(t *testing.T) {
	f := newFixtureWith(t, tailscaleDoc())
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find(tailscaleLabel); return ok })
	if !f.tt.HasText("未运行") || !f.tt.HasText("Tailscale") {
		t.Fatalf("stopped card: %q", f.tt.Texts())
	}
	f.srv.Emit("tailscale", map[string]any{"endpoints": []map[string]any{{
		"endpointTag": "Tailscale", "backendState": "Running", "observedAt": fmt.Sprint(time.Now().UnixMilli()), "networkName": "alice@example.com",
		"self":     map[string]any{"hostName": "me", "dnsName": "me.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.1"}, "online": true},
		"exitNode": map[string]any{"hostName": "nas", "dnsName": "nas.tail.ts.net."},
		"userGroups": []map[string]any{{"displayName": "Alice", "peers": []map[string]any{{
			"hostName": "nas", "dnsName": "nas.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.2"}, "online": true, "udpPath": "direct",
		}}}},
	}}})
	testkit.UntilText(t, f.tt, "控制连接在线")
	for _, text := range []string{"me", "100.64.0.1", "alice@example.com", "nas", "直连 1", "1 台设备"} {
		if !f.tt.HasText(text) {
			t.Fatalf("live card lacks %q: %q", text, f.tt.Texts())
		}
	}
	savePreview(t, f.tt, "overview-tailscale.png")
	f.click(t, tailscaleLabel)
	testkit.UntilText(t, f.tt, "Tailscale")
	testkit.UntilText(t, f.tt, "Alice")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return f.w.Tailscale.Opened() == "" })

	// The core stops: the card stays, gray, with the last device seen.
	f.emitRuntime(api.Runtime{State: "stopped"})
	testkit.Until(t, f.tt, func() bool { return f.tt.HasText("未运行") && !f.tt.HasText("控制连接在线") })
	if !f.tt.HasText("me") || f.tt.HasText("1 台设备") {
		t.Fatalf("stopped card: %q", f.tt.Texts())
	}
	f.click(t, tailscaleLabel)
	testkit.UntilText(t, f.tt, "上次状态")
	f.tt.Key(0, ui.KeyEscape)
	testkit.Until(t, f.tt, func() bool { return f.w.Tailscale.Opened() == "" })

	// Disabled, the card leaves.
	if err := f.store.Update(func(doc *model.Doc) { doc.Endpoints[0].Enabled = false }); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find(tailscaleLabel); return !ok })
}

// TestNoTailscaleCard: without a Tailscale endpoint the page has no card
// and follows no endpoint state.
func TestNoTailscaleCard(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	f := newFixtureWith(t, doc)
	testkit.UntilText(t, f.tt, "已停止")
	if _, ok := f.tt.Find(tailscaleLabel); ok {
		t.Fatal("card without an endpoint")
	}
	if f.requested("GET /api/v1/endpoints/status") {
		t.Fatal("endpoint state followed without a card")
	}
}

// TestTailscaleReconnect: a lost control connection offers "Reconnect"
// on the card; it asks the core to reconnect, without opening the sheet,
// and shows it under way; back online, the button leaves.
func TestTailscaleReconnect(t *testing.T) {
	f := newFixtureWith(t, tailscaleDoc())
	testkit.Until(t, f.tt, func() bool { _, ok := f.tt.Find(tailscaleLabel); return ok })
	f.srv.Route("POST", "/tailscale/reconnect", testkit.JSON(map[string]any{}))
	emit := func(online bool) {
		f.srv.Emit("tailscale", map[string]any{"endpoints": []map[string]any{{
			"endpointTag": "Tailscale", "backendState": "Running", "observedAt": fmt.Sprint(time.Now().UnixMilli()),
			"self": map[string]any{"hostName": "me", "dnsName": "me.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.1"}, "online": online},
		}}})
	}
	emit(false)
	testkit.UntilText(t, f.tt, "控制连接已断开")
	savePreview(t, f.tt, "overview-tailscale-reconnect.png")
	f.click(t, "重新连接")
	testkit.UntilText(t, f.tt, "重新连接中…")
	testkit.Until(t, f.tt, func() bool { return slices.Contains(f.srv.Requests(), "POST /api/v1/tailscale/reconnect") })
	if f.w.Tailscale.Opened() != "" {
		t.Fatal("the reconnect button opened the sheet")
	}
	emit(true)
	testkit.Until(t, f.tt, func() bool {
		return f.tt.HasText("控制连接在线") && !f.tt.HasText("重新连接中…") && !f.tt.HasText("重新连接")
	})
}
