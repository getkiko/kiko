package endpoints

import (
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
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

type fixture struct {
	t     *testing.T
	srv   *testkit.Server
	env   *env.Env
	shell *testkit.Shell
	store *store.Store
	s     *Section
	tt    *ui.Tester

	mu     sync.Mutex
	bodies map[string][]json.RawMessage
	share  api.USBIPShareStatus
}

func testDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	ts := model.MakeEndpoint("tailscale", "Tailscale", false)
	ts.ID = "ep-ts"
	ts.Fields["hostname"] = "home"
	oc := model.MakeEndpoint("openconnect", "oc", false)
	oc.ID = "ep-oc"
	oc.Fields["server"] = "vpn.example.com"
	usb := model.MakeService("usbip-server", "usb")
	usb.ID = "svc-usb"
	usb.Fields["provider"] = "dynamic"
	usb.Fields["listen"] = "127.0.0.1"
	doc.Endpoints = []*model.Endpoint{ts, oc}
	doc.Services = []*model.Service{usb}
	return doc
}

func (f *fixture) record(r *http.Request) {
	var raw json.RawMessage
	_ = testkit.Body(r, &raw)
	key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/v1")
	f.mu.Lock()
	f.bodies[key] = append(f.bodies[key], raw)
	f.mu.Unlock()
}

// body returns the last JSON body of a request, decoded.
func (f *fixture) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.bodies[key]
	if len(list) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(list[len(list)-1], &out)
	return out
}

func (f *fixture) waitBody(key string) map[string]any {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return f.body(key) != nil })
	return f.body(key)
}

func newFixture(t *testing.T, doc *model.Doc) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t), bodies: map[string][]json.RawMessage{}}
	raw, _ := json.Marshal(doc)
	f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	f.srv.Route("GET", "/endpoints/status", testkit.JSON(map[string]any{"tailscale": nil, "openconnect": nil, "openvpn": nil, "usbip": nil}))
	f.srv.Route("GET", "/taildrop/sends", testkit.JSON([]any{}))
	f.srv.Route("GET", "/taildrop/directory", testkit.JSON(map[string]string{"path": "/var/lib/kiko/taildrop"}))
	f.srv.Route("GET", "/usbip/preflight", testkit.JSON(api.USBIPPreflight{OS: "linux", Arch: "amd64", User: "root", Elevated: true, ServerSupported: true, SingBoxUSBIP: true}))
	f.srv.Route("GET", "/usb/devices", testkit.JSON(map[string]any{"devices": []map[string]any{
		{"busId": "1-2", "vendorId": 7531, "productId": 2, "product": "Keyboard"},
		{"busId": "1-3", "vendorId": 1133, "productId": 50475, "serial": "SN1"},
	}}))
	f.srv.Route("GET", "/usbip/share", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		share := f.share
		f.mu.Unlock()
		testkit.JSON(share)(w, r)
	})
	f.srv.Route("POST", "/usbip/share", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		body := f.body("POST /usbip/share")
		f.mu.Lock()
		f.share = api.USBIPShareStatus{Running: true, ServiceTag: fmt.Sprint(body["serviceTag"]), PID: 42}
		share := f.share
		f.mu.Unlock()
		testkit.JSON(share)(w, r)
	})
	for _, route := range [][2]string{{"POST", "/tailscale/exit-node"}, {"POST", "/tailscale/logout"}, {"POST", "/openconnect/auth"},
		{"POST", "/openconnect/cancel"}, {"POST", "/taildrop/read"}, {"DELETE", "/taildrop/file"}, {"POST", "/openvpn/challenge"}} {
		f.srv.Route(route[0], route[1], func(w http.ResponseWriter, r *http.Request) {
			f.record(r)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	f.env, f.shell = testkit.NewEnv(f.srv)
	f.store = store.New(f.env)
	f.s = New(f.env, f.store)
	f.env.Server.Start()
	t.Cleanup(f.env.Server.Stop)
	f.store.Load()
	f.tt = ui.NewTester(f.view, 1100, 1000)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded && !f.store.Saving() && f.env.Server.Connected })
	f.s.SetActive(true)
	t.Cleanup(func() { f.s.SetActive(false) })
	testkit.Until(t, f.tt, func() bool { return f.s.rt.preflight != nil && f.s.rt.share != nil })
	return f
}

func (f *fixture) view(c *ui.Context) {
	kit.Apply(c)
	f.env.Drain(c)
	c.Root().Background(kit.T(c).Surface)
	ui.Scroll(c).Fill().Padding(0, 20, 24).Children(func() { f.s.View(c) })
	f.s.Tailscale().View(c)
}

// openTailscale opens the Tailscale sheet as the Overview card does.
func (f *fixture) openTailscale() {
	f.s.Tailscale().Open("Tailscale")
	f.tt.Frame()
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
	f.tt.Frame()
}

// clickBelow clicks the control under a label.
func (f *fixture) clickBelow(label string) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q in %q", label, f.tt.Texts())
	}
	f.tt.ClickAt(r.X+20, r.Y+r.H+4+15)
	f.tt.Frame()
}

func (f *fixture) snapshot(name string) {
	dir := os.Getenv("ENDPOINTS_PREVIEW_DIR")
	if dir == "" {
		return
	}
	for _, dark := range []bool{false, true} {
		f.tt.SetDark(dark)
		f.tt.Frame()
		suffix := "-light.png"
		if dark {
			suffix = "-dark.png"
		}
		out, err := os.Create(filepath.Join(dir, name+suffix))
		if err != nil {
			f.t.Fatal(err)
		}
		_ = png.Encode(out, f.tt.Image())
		out.Close()
	}
	f.tt.SetDark(false)
	f.tt.Frame()
}

func nowMs() string { return fmt.Sprint(time.Now().UnixMilli()) }

func tailscaleEvent() map[string]any {
	return map[string]any{"endpoints": []map[string]any{{
		"endpointTag": "Tailscale", "backendState": "Running", "observedAt": nowMs(), "canShareFiles": true,
		"self": map[string]any{"hostName": "me", "dnsName": "me.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.1"}, "online": true},
		"userGroups": []map[string]any{{"displayName": "Alice", "peers": []map[string]any{{
			"hostName": "nas", "dnsName": "nas.tail.ts.net.", "tailscaleIPs": []string{"100.64.0.2"}, "online": true,
			"stableID": "n1", "exitNodeOption": true, "canReceiveFiles": true, "sshHostKeys": []string{"ssh-ed25519 AAA"}, "os": "linux",
		}}}},
	}}}
}

func TestCardsAndStatusFromEvents(t *testing.T) {
	f := newFixture(t, testDoc())
	for _, text := range []string{"端点", "2 个 · 1 个 USB/IP", "Endpoint", "Tailscale", "home", "oc", "vpn.example.com", "USB/IP", "usb", "未运行", "设备与共享"} {
		if !f.tt.HasText(text) {
			t.Fatalf("missing %q in %q", text, f.tt.Texts())
		}
	}
	f.srv.Emit("tailscale", tailscaleEvent())
	f.srv.Emit("openconnect", map[string]any{"endpoints": []map[string]any{{"endpointTag": "oc", "state": "CONNECTED", "stateText": "已连接"}}})
	testkit.UntilText(t, f.tt, "已连接")
	testkit.Until(t, f.tt, func() bool { return f.s.rt.tailscale["Tailscale"] != nil })
	// The Tailscale card shows its configuration; its state is the Overview
	// card's.
	if f.tt.HasText("控制连接在线") || !f.tt.HasText("链接登录") {
		t.Fatalf("tailscale card: %q", f.tt.Texts())
	}
	if _, ok := f.tt.Find("ts 运行状态"); ok {
		t.Fatal("tailscale card keeps a runtime button")
	}
	f.snapshot("endpoints-cards")
	f.srv.Emit("runtime", map[string]any{"state": "stopped"})
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("已连接") })
}

// TestTailscaleCardConfiguration: the Tailscale card tells how it logs
// in and marks its UDP path and system interface.
func TestTailscaleCardConfiguration(t *testing.T) {
	doc := testDoc()
	ts := doc.Endpoints[0]
	ts.Fields["auth_key"] = "tskey-auth-1"
	ts.Fields["udp_path.default"] = "direct"
	ts.Fields["system_interface"] = true
	f := newFixture(t, doc)
	for _, text := range []string{"home", "TAILSCALE", "已填 Auth Key", "UDP 直连", "系统接口"} {
		if !f.tt.HasText(text) {
			t.Fatalf("missing %q in %q", text, f.tt.Texts())
		}
	}
	f.click("编辑 Tailscale")
	testkit.Until(t, f.tt, func() bool { return f.s.editor.open && f.s.editor.endpoint != nil && f.s.editor.endpoint.ID == "ep-ts" })
}

func TestTailscaleExitNodeAndLogout(t *testing.T) {
	f := newFixture(t, testDoc())
	f.srv.Emit("tailscale", tailscaleEvent())
	testkit.Until(t, f.tt, func() bool { return f.s.rt.tailscale["Tailscale"] != nil })
	f.openTailscale()
	testkit.UntilText(t, f.tt, "/var/lib/kiko/taildrop")
	if !f.tt.HasText("Alice") || !f.tt.HasText("nas") {
		t.Fatalf("peers: %q", f.tt.Texts())
	}
	f.snapshot("endpoints-tailscale")
	f.click("nas 操作")
	f.click("设为出口节点")
	body := f.waitBody("POST /tailscale/exit-node")
	if body["endpointTag"] != "Tailscale" || body["stableID"] != "n1" {
		t.Fatalf("exit node body %v", body)
	}
	f.click("退出登录")
	if err := f.tt.Click("退出"); err != nil {
		t.Fatal(err)
	}
	if body := f.waitBody("POST /tailscale/logout"); body["endpointTag"] != "Tailscale" {
		t.Fatalf("logout body %v", body)
	}
}

func TestPeerDetail(t *testing.T) {
	f := newFixture(t, testDoc())
	f.srv.Emit("tailscale", tailscaleEvent())
	testkit.Until(t, f.tt, func() bool { return f.s.rt.tailscale["Tailscale"] != nil })
	f.openTailscale()
	testkit.UntilText(t, f.tt, "Alice")
	f.click("nas")
	for _, text := range []string{"控制面在线", "MagicDNS", "nas.tail.ts.net", "100.64.0.2", "Ping", "等待开始", "Taildrop", "通过 SSH 连接"} {
		if !f.tt.HasText(text) {
			t.Fatalf("peer detail lacks %q: %q", text, f.tt.Texts())
		}
	}
	f.snapshot("endpoints-peer")
	f.click("通过 SSH 连接")
	testkit.UntilText(t, f.tt, "SSH 连接")
	if !f.tt.HasText("终端类型") {
		t.Fatalf("ssh form: %q", f.tt.Texts())
	}
	f.snapshot("endpoints-ssh")
}

func TestTaildropInbox(t *testing.T) {
	f := newFixture(t, testDoc())
	f.srv.Emit("tailscale", tailscaleEvent())
	f.srv.Emit("taildrop", map[string]any{"endpointTag": "Tailscale",
		"files":     []map[string]any{{"name": "photo.jpg", "size": "2048", "senderName": "nas"}},
		"receiving": []map[string]any{{"name": "big.iso", "size": "1000", "receivedBytes": "250", "senderID": "n1"}}})
	testkit.Until(t, f.tt, func() bool { return f.s.rt.tailscale["Tailscale"] != nil })
	f.openTailscale()
	testkit.UntilText(t, f.tt, "Taildrop 收件箱")
	for _, text := range []string{"Taildrop 接收目录", "photo.jpg", "2.0 KB · nas", "big.iso", "接收中 25%"} {
		if !f.tt.HasText(text) {
			t.Fatalf("inbox lacks %q: %q", text, f.tt.Texts())
		}
	}
	f.snapshot("endpoints-taildrop")
	f.click("删除")
	if body := f.waitBody("DELETE /taildrop/file"); body["endpointTag"] != "Tailscale" || body["name"] != "photo.jpg" {
		t.Fatalf("delete body %v", body)
	}
}

func TestOpenConnectAuth(t *testing.T) {
	f := newFixture(t, testDoc())
	f.srv.Emit("openconnect", map[string]any{"endpoints": []map[string]any{{
		"endpointTag": "oc", "state": "AUTHENTICATING",
		"authChallenge": map[string]any{"id": "ch1", "message": "Please log in", "form": map[string]any{"fields": []map[string]any{
			{"submissionKey": "username", "label": "Username"},
			{"submissionKey": "password", "label": "Password", "kind": "password"},
			{"submissionKey": "token", "kind": "hidden", "value": "abc"},
		}}},
	}}})
	testkit.UntilText(t, f.tt, "AUTHENTICATING")
	f.click("oc 运行状态")
	testkit.UntilText(t, f.tt, "Please log in")
	f.clickBelow("Username")
	f.tt.Type("alice")
	f.clickBelow("Password")
	f.tt.Type("secret")
	f.tt.Frame()
	f.snapshot("endpoints-openconnect")
	f.click("提交")
	body := f.waitBody("POST /openconnect/auth")
	values, _ := body["form"].(map[string]any)["values"].(map[string]any)
	if body["challengeID"] != "ch1" || values["username"] != "alice" || values["password"] != "secret" || values["token"] != "abc" {
		t.Fatalf("auth body %v", body)
	}
}

func TestUSBIPShare(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("usb 设备与共享")
	testkit.UntilText(t, f.tt, "Keyboard")
	f.click("Keyboard")
	f.snapshot("endpoints-usbip")
	f.click("开始共享")
	body := f.waitBody("POST /usbip/share")
	devices, _ := body["devices"].([]any)
	if body["serviceTag"] != "usb" || len(devices) != 1 || devices[0] != "1-2" {
		t.Fatalf("share body %v", body)
	}
	testkit.UntilText(t, f.tt, "停止共享")
}

func TestQuickAddTailscale(t *testing.T) {
	doc := testDoc()
	doc.Endpoints = doc.Endpoints[1:] // one Tailscale endpoint per document
	f := newFixture(t, doc)
	f.click("添加端点")
	f.click("Tailscale 链接登录")
	testkit.UntilText(t, f.tt, "登录 Tailscale")
	f.click("完成")
	f.snapshot("endpoints-editor")
	f.click("保存")
	testkit.Until(t, f.tt, func() bool {
		return !f.store.Saving() && len(model.DecodeDoc(f.srv.Store().Doc).Endpoints) == 2
	})
	added := model.DecodeDoc(f.srv.Store().Doc).Endpoints[1]
	if added.Type != "tailscale" || added.Tag != model.TailscaleTag || text(added.Fields["hostname"]) != "" || text(added.Fields["auth_key"]) != "" {
		t.Fatalf("added %+v", added)
	}
}

func TestSecondTailscaleUnavailable(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("添加端点")
	testkit.UntilText(t, f.tt, "（已有 Tailscale 端点）")
	if f.s.editor.addEndpoint(f.store.Doc(), "tailscale") || f.s.editor.open {
		t.Fatal("a second Tailscale endpoint must be refused")
	}
}

// The editor opens from a card's menu while the core is stopped: the card
// is not only its run controls.
func TestEditEndpointWhileStopped(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("更多操作")
	f.click("编辑")
	testkit.Until(t, f.tt, func() bool { return f.s.editor.open && f.s.editor.endpoint != nil })
}

func TestDeleteEndpoint(t *testing.T) {
	f := newFixture(t, testDoc())
	if err := f.tt.Click("更多操作"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	f.click("删除")
	if err := f.tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool {
		return !f.store.Saving() && len(model.DecodeDoc(f.srv.Store().Doc).Endpoints) == 1
	})
}

func TestWizard(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("连接向导")
	f.click("使用远程的 USB 设备")
	f.click("下一步")
	f.click("Tailscale（核心内置）")
	f.click("下一步")
	testkit.UntilText(t, f.tt, "将要创建")
	if !f.tt.HasText("复用已有的 Tailscale 端点") {
		t.Fatalf("plan: %q", f.tt.Texts())
	}
	f.snapshot("endpoints-wizard")
	f.click("创建并继续编辑")
	testkit.UntilText(t, f.tt, "连接途径")
	f.snapshot("endpoints-service-editor")
}

// TestTailscalePanelEndpoint: the Overview card shows an enabled Tailscale
// endpoint whose configuration is complete; its sheet closes as the
// endpoint stops qualifying.
func TestTailscalePanelEndpoint(t *testing.T) {
	f := newFixture(t, testDoc())
	p := f.s.Tailscale()
	if e := p.Endpoint(); e == nil || e.Tag != "Tailscale" {
		t.Fatalf("endpoint %v", e)
	}
	f.openTailscale()
	testkit.Until(t, f.tt, func() bool { return p.Opened() == model.TailscaleTag })
	update := func(fn func(ts *model.Endpoint)) {
		t.Helper()
		if err := f.store.Update(func(doc *model.Doc) { fn(doc.Endpoints[0]) }); err != nil {
			t.Fatal(err)
		}
		f.tt.Frame()
	}
	// "Via proxy" without a detour is what the editor refuses to save.
	update(func(ts *model.Endpoint) { ts.Fields["udp_path.default"] = "detour" })
	if p.Endpoint() != nil {
		t.Fatal("incomplete endpoint shown")
	}
	testkit.Until(t, f.tt, func() bool { return p.Opened() == "" })
	update(func(ts *model.Endpoint) { delete(ts.Fields, "udp_path.default") })
	if p.Endpoint() == nil {
		t.Fatal("complete endpoint hidden")
	}
	update(func(ts *model.Endpoint) { ts.Enabled = false })
	if p.Endpoint() != nil {
		t.Fatal("disabled endpoint shown")
	}
}
