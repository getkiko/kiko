package subscriptions

import (
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
	store *store.Store
	w     *Workspace
	tt    *ui.Tester

	mu      sync.Mutex
	fetched []map[string]any
}

func newFixture(t *testing.T, doc *model.Doc) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t)}
	if doc != nil {
		raw, _ := json.Marshal(doc)
		f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	}
	f.env, _ = testkit.NewEnv(f.srv)
	f.store = store.New(f.env)
	f.w = New(f.env, f.store)
	f.store.Load()
	f.tt = ui.NewTester(f.view, 1280, 1000)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded && !f.store.Saving() })
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
	f.tt.Frame()
}

// saved waits for the store to be written and returns the server's doc.
func (f *fixture) saved(cond func(doc *model.Doc) bool) *model.Doc {
	f.t.Helper()
	var doc *model.Doc
	testkit.Until(f.t, f.tt, func() bool {
		if f.store.Saving() {
			return false
		}
		doc = model.DecodeDoc(f.srv.Store().Doc)
		return doc != nil && cond(doc)
	})
	return doc
}

func (f *fixture) save(name string) {
	dir := os.Getenv("SUBS_PREVIEW_DIR")
	if dir == "" {
		return
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, f.tt.Image()); err != nil {
		f.t.Fatal(err)
	}
}

func outboundOf(id, typ, tag string, fields model.Fields) *model.Outbound {
	return &model.Outbound{Entity: model.Entity{ID: id, Tag: tag, Enabled: true}, Kind: "outbound", Type: typ, Fields: fields}
}

func sampleDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.Inbounds = []*model.Inbound{model.MakeInbound("mixed", "mixed-in", "linux", false)}
	doc.Inbounds[0].Fields["listen_port"] = 7890.0
	ss := outboundOf("out_ss", "shadowsocks", "香港 SS", model.Fields{"server": "hk.example", "server_port": 8388.0, "method": "aes-128-gcm", "password": "secret"})
	vless := outboundOf("out_vless", "vless", "日本 VLESS", model.Fields{"server": "jp.example", "server_port": 443.0, "uuid": "00000000-0000-0000-0000-000000000000"})
	sel := outboundOf("out_sel", "selector", "节点选择", model.Fields{})
	sel.Members = []string{"out_ss"}
	doc.Outbounds = append(doc.Outbounds, ss, vless, sel)
	sub := &model.Subscription{ID: "sub_a", Tag: "机场 A", URL: "https://sub.example/a", Enabled: true,
		Traffic: &model.Traffic{Upload: 1 << 30, Download: 2 << 30, Total: 100 << 30}}
	doc.Subscriptions = append(doc.Subscriptions, sub)
	node := outboundOf("out_node", "trojan", "A-新加坡", model.Fields{"server": "sg.example", "server_port": 443.0, "password": "p", "__key": "trojan|sg.example|443|p"})
	node.SubID = "sub_a"
	doc.Outbounds = append(doc.Outbounds, node)
	return doc
}

func TestSectionsInOrder(t *testing.T) {
	f := newFixture(t, sampleDoc())
	var last float32 = -1
	for _, heading := range []string{"订阅", "入站", "出站", "端点"} {
		r, ok := f.tt.Find(heading)
		if !ok {
			t.Fatalf("no %q in %q", heading, f.tt.Texts())
		}
		if r.Y <= last {
			t.Errorf("%q at %v, above the previous section (%v)", heading, r.Y, last)
		}
		last = r.Y
	}
	for _, text := range []string{"机场 A", "3.00 GB", "/ 100 GB", "1 节点", "mixed-in", "香港 SS", "hk.example", ":8388", "手动 · 1", "URLTest", "节点选择"} {
		if text == "URLTest" {
			if f.tt.HasText(text) {
				t.Errorf("empty URLTest group shown")
			}
			continue
		}
		if !f.tt.HasText(text) {
			t.Errorf("missing %q", text)
		}
	}
	f.save("subscriptions-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("subscriptions-dark.png")
}

func TestAddSubscriptionFetchesNodes(t *testing.T) {
	f := newFixture(t, sampleDoc())
	links := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw")) + "@a.example:8388#节点1\n" +
		"trojan://pw@b.example:443?sni=b.example#节点2\n"
	f.srv.Route("POST", "/fetch", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = testkit.Body(r, &body)
		f.mu.Lock()
		f.fetched = append(f.fetched, body)
		f.mu.Unlock()
		testkit.JSON(api.FetchResult{Status: 200, Body: base64.StdEncoding.EncodeToString([]byte(links)),
			SubscriptionUserinfo: "upload=1024; download=1024; total=1073741824; expire=0"})(w, r)
	})
	f.click("添加订阅")
	if !f.w.subs.editor.open || f.w.subs.editor.draft.Tag != "订阅 2" || !f.tt.HasText("添加订阅") {
		t.Fatalf("draft %+v; texts %q", f.w.subs.editor.draft, f.tt.Texts())
	}
	f.w.subs.editor.draft.URL = "https://sub.example/b"
	f.w.subs.editor.draft.NamePrefix = "B-"
	saves := f.srv.Saves()
	f.click("更新订阅")
	testkit.UntilText(t, f.tt, "解析到 2 个节点")
	if f.srv.Saves() != saves || findSubscription(f.store.Doc(), f.w.subs.editor.draft.ID) != nil {
		t.Fatal("fetch saved before 保存")
	}
	f.mu.Lock()
	body := f.fetched[0]
	f.mu.Unlock()
	headers, _ := body["headers"].(map[string]any)
	if body["url"] != "https://sub.example/b" || headers["User-Agent"] != "clash-verge/v2.5.4" {
		t.Errorf("fetch request %v", body)
	}
	id := f.w.subs.editor.draft.ID
	f.click("保存")
	if f.w.subs.editor.open {
		t.Error("editor still open")
	}
	doc := f.saved(func(d *model.Doc) bool { return len(subscriptionNodes(d, id)) == 2 })
	sub := findSubscription(doc, id)
	if sub == nil || sub.Tag != "订阅 2" || sub.Traffic == nil || sub.Traffic.Total != 1<<30 || sub.UpdatedAt == 0 {
		t.Fatalf("subscription %+v", sub)
	}
	nodes := subscriptionNodes(doc, id)
	if nodes[0].Tag != "B-节点1" || nodes[1].Tag != "B-节点2" || nodes[1].Fields["__key"] != "trojan|b.example|443|pw" {
		t.Errorf("nodes %q %q %v", nodes[0].Tag, nodes[1].Tag, nodes[1].Fields["__key"])
	}
	testkit.UntilText(t, f.tt, "订阅 2")
	// The card opens the nodes sheet, which shows the nodes without the
	// subscription's prefix.
	f.click("查看 订阅 2 的节点")
	if !f.w.subs.nodes.sheet.Open || !f.tt.HasText("节点1") || !f.tt.HasText("2 个节点") {
		t.Errorf("nodes sheet %q", f.tt.Texts())
	}
}

func TestSubscriptionFetchFailure(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.srv.Route("POST", "/fetch", testkit.JSON(api.FetchResult{Status: 403, Body: "forbidden"}))
	f.w.subs.editor.openEdit(f.store.Doc(), "sub_a")
	f.tt.Frame()
	f.click("更新订阅")
	testkit.UntilText(t, f.tt, "拉取失败（forbidden）")
	f.click("保存")
	doc := f.saved(func(d *model.Doc) bool { s := findSubscription(d, "sub_a"); return s != nil && s.LastError != "" })
	if len(subscriptionNodes(doc, "sub_a")) != 1 {
		t.Error("failed fetch changed the nodes")
	}
}

// Closing the editor drops what a fetch brought: nothing is saved.
func TestSubscriptionFetchRolledBackOnClose(t *testing.T) {
	f := newFixture(t, sampleDoc())
	links := "trojan://pw@n1.example:443#N1\ntrojan://pw@n2.example:443#N2\n"
	f.srv.Route("POST", "/fetch", testkit.JSON(api.FetchResult{Status: 200, Body: links,
		SubscriptionUserinfo: "upload=1; download=1; total=2"}))
	saves := f.srv.Saves()
	f.w.subs.editor.openEdit(f.store.Doc(), "sub_a")
	f.tt.Frame()
	f.click("更新订阅")
	testkit.UntilText(t, f.tt, "解析到 2 个节点")
	if !f.tt.HasText("2 个节点") {
		t.Errorf("editor count %q", f.tt.Texts())
	}
	f.click("取消")
	f.tt.Frame()
	if f.w.subs.editor.open || f.srv.Saves() != saves {
		t.Fatalf("open %v saves %d → %d", f.w.subs.editor.open, saves, f.srv.Saves())
	}
	doc := f.store.Doc()
	nodes := subscriptionNodes(doc, "sub_a")
	if len(nodes) != 1 || nodes[0].ID != "out_node" || findSubscription(doc, "sub_a").Traffic.Total != 100<<30 {
		t.Errorf("rolled back doc: %d nodes, traffic %+v", len(nodes), findSubscription(doc, "sub_a").Traffic)
	}
	if model.DecodeDoc(f.srv.Store().Doc) == nil || len(subscriptionNodes(model.DecodeDoc(f.srv.Store().Doc), "sub_a")) != 1 {
		t.Error("server doc changed")
	}
}

// TestRefreshSubscriptionFromCard: "Update" on a card fetches the
// subscription again and saves its nodes and traffic, without the editor.
func TestRefreshSubscriptionFromCard(t *testing.T) {
	f := newFixture(t, sampleDoc())
	links := "trojan://pw@n1.example:443#N1\ntrojan://pw@n2.example:443#N2\n"
	f.srv.Route("POST", "/fetch", testkit.JSON(api.FetchResult{Status: 200, Body: links,
		SubscriptionUserinfo: "upload=1; download=1; total=2"}))
	f.click("更新 机场 A")
	doc := f.saved(func(d *model.Doc) bool { return len(subscriptionNodes(d, "sub_a")) == 2 })
	sub := findSubscription(doc, "sub_a")
	if sub.Traffic == nil || sub.Traffic.Total != 2 || sub.UpdatedAt == 0 || sub.LastError != "" || sub.Raw != links {
		t.Errorf("subscription %+v", sub)
	}
	if f.w.subs.editor.open || f.w.subs.refreshing["sub_a"] {
		t.Errorf("editor open %v, refreshing %v", f.w.subs.editor.open, f.w.subs.refreshing)
	}
	if nodes := subscriptionNodes(doc, "sub_a"); nodes[0].Tag != "N1" {
		t.Errorf("nodes %q", nodes[0].Tag)
	}
}

// TestRefreshSubscriptionFailure: a failed refresh keeps the nodes and
// shows the failure on the card.
func TestRefreshSubscriptionFailure(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.srv.Route("POST", "/fetch", testkit.JSON(api.FetchResult{Status: 403, Body: "forbidden"}))
	f.click("更新 机场 A")
	doc := f.saved(func(d *model.Doc) bool {
		s := findSubscription(d, "sub_a")
		return s != nil && s.LastError == "拉取失败（forbidden）"
	})
	if len(subscriptionNodes(doc, "sub_a")) != 1 || findSubscription(doc, "sub_a").Traffic.Total != 100<<30 {
		t.Error("failed refresh changed the nodes or the traffic")
	}
	// The card's bottom line says "Fetch failed", the whole failure in its hint.
	testkit.UntilText(t, f.tt, "拉取失败")
}

func TestEditOutboundAndSave(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.click("编辑 香港 SS")
	if !f.tt.HasText("编辑出站 · 香港 SS") {
		t.Fatalf("editor %q", f.tt.Texts())
	}
	f.w.outbounds.editor.draft.Tag = "香港 SS 2"
	f.w.outbounds.editor.draft.Fields["server"] = "hk2.example"
	f.click("保存")
	doc := f.saved(func(d *model.Doc) bool {
		return slices.ContainsFunc(d.Outbounds, func(o *model.Outbound) bool { return o.Tag == "香港 SS 2" })
	})
	for _, o := range doc.Outbounds {
		if o.ID == "out_ss" && o.Fields["server"] != "hk2.example" {
			t.Errorf("server %v", o.Fields["server"])
		}
	}
	if f.w.outbounds.editor.open {
		t.Error("editor still open")
	}
}

func TestSelectorMemberPicker(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.click("编辑 节点选择")
	if !f.tt.HasText("手动添加") || !f.tt.HasText("香港 SS") {
		t.Fatalf("chosen members %q", f.tt.Texts())
	}
	f.click("＋ 添加节点")
	if !f.w.outbounds.editor.open {
		t.Fatal("closed after 添加节点")
	}
	f.click("加入 日本 VLESS")
	if got := f.w.outbounds.editor.draft.Members; !slices.Equal(got, []string{"out_ss", "out_vless"}) {
		t.Fatalf("not added: %v", got)
	}
	f.click("加入 A-新加坡")
	f.click("保存")
	doc := f.saved(func(d *model.Doc) bool {
		for _, o := range d.Outbounds {
			if o.ID == "out_sel" {
				return len(o.Members) == 3
			}
		}
		return false
	})
	for _, o := range doc.Outbounds {
		if o.ID == "out_sel" && !slices.Equal(o.Members, []string{"out_ss", "out_vless", "out_node"}) {
			t.Errorf("members %v", o.Members)
		}
	}
	// An empty group cannot be saved.
	f.click("编辑 节点选择")
	f.w.outbounds.editor.draft.Members = nil
	f.tt.Frame()
	f.click("保存")
	if !f.w.outbounds.editor.open {
		t.Error("empty group saved")
	}
}

func TestDeleteOutboundWithConfirm(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.w.outbounds.remove.start("out_vless", "")
	f.tt.Frame()
	if !f.tt.HasText("「日本 VLESS」将从出站列表中删除。") {
		t.Fatalf("confirm %q", f.tt.Texts())
	}
	f.click("删除")
	f.saved(func(d *model.Doc) bool {
		return !slices.ContainsFunc(d.Outbounds, func(o *model.Outbound) bool { return o.ID == "out_vless" })
	})
}

func TestDeleteFromCardMenu(t *testing.T) {
	f := newFixture(t, sampleDoc())
	body, ok := f.tt.Find("编辑 日本 VLESS")
	if !ok {
		t.Fatal("no card")
	}
	// The card's "More actions" button, 26 DIPs, sits over its body's top right
	// corner, 11 DIPs in.
	f.tt.ClickAt(body.X+body.W-11-13, body.Y+11+13)
	f.tt.Frame()
	f.click("删除")
	if !f.tt.HasText("删除出站？") {
		t.Fatalf("no confirmation: %q", f.tt.Texts())
	}
	f.click("删除")
	f.saved(func(d *model.Doc) bool {
		return !slices.ContainsFunc(d.Outbounds, func(o *model.Outbound) bool { return o.ID == "out_vless" })
	})
}

func TestLatencyTestShowsResults(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		var results []api.LatencyResult
		for _, target := range body.Targets {
			ms := 86.4
			res := api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 3}
			if target.ID == "out_vless" {
				res = api.LatencyResult{ID: target.ID, Error: "timeout"}
			}
			results = append(results, res)
		}
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	f.click("全部测速")
	testkit.Until(t, f.tt, func() bool { return !f.store.LatencyProgress.Running && f.store.LatencyProgress.Completed == 3 })
	f.tt.Frame()
	if !f.tt.HasText("86 ms") || !f.tt.HasText("超时") {
		t.Errorf("latency badges missing: %q", f.tt.Texts())
	}
	f.click("查看 机场 A 的节点")
	if !f.tt.HasText("86 ms") {
		t.Errorf("node latency missing")
	}
}

func TestClipboardImport(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.tt.SetClipboard("")
	f.click("从剪贴板导入")
	f.tt.Frame()
	if f.w.outbounds.editor.open {
		t.Error("empty clipboard opened the editor")
	}
	f.tt.SetClipboard("trojan://pw@c.example:443#香港 SS\nhy2://pw@d.example:443#HY2\nfoo://x")
	before := len(f.store.Doc().Outbounds)
	f.click("从剪贴板导入")
	doc := f.saved(func(d *model.Doc) bool { return len(d.Outbounds) == before+2 })
	var tags []string
	for _, o := range doc.Outbounds {
		tags = append(tags, o.Tag)
	}
	if !slices.Contains(tags, "香港 SS 2") || !slices.Contains(tags, "HY2") {
		t.Errorf("tags %q", tags)
	}
	// One link opens the editor to confirm.
	f.tt.SetClipboard("vless://00000000-0000-0000-0000-000000000000@e.example:443?security=tls#单个")
	f.click("从剪贴板导入")
	if !f.w.outbounds.editor.open || !f.w.outbounds.editor.isNew || f.w.outbounds.editor.draft.Tag != "单个" {
		t.Fatalf("editor %+v", f.w.outbounds.editor.draft)
	}
	if !strings.Contains(strings.Join(f.tt.Texts(), "|"), "添加出站 · 单个") {
		t.Errorf("title missing: %q", f.tt.Texts())
	}
}

func TestInboundEditorRejectsDuplicatePort(t *testing.T) {
	f := newFixture(t, sampleDoc())
	f.w.inbounds.editor.openAdd(f.store.Doc(), "socks")
	f.w.inbounds.editor.draft.Fields["listen_port"] = 7890.0
	f.tt.Frame()
	f.click("保存")
	if !f.w.inbounds.editor.open {
		t.Fatal("duplicate port saved")
	}
	f.w.inbounds.editor.draft.Fields["listen_port"] = 7891.0
	f.click("保存")
	f.saved(func(d *model.Doc) bool { return len(d.Inbounds) == 2 })
}

// TestEditorPreviews renders the editors for SUBS_PREVIEW_DIR.
func TestEditorPreviews(t *testing.T) {
	if os.Getenv("SUBS_PREVIEW_DIR") == "" {
		t.Skip("previews only")
	}
	f := newFixture(t, sampleDoc())
	f.w.subs.editor.openEdit(f.store.Doc(), "sub_a")
	f.tt.Frame()
	f.save("sub-editor.png")
	f.w.subs.editor.close()
	f.w.subs.nodes.show("sub_a", "")
	f.tt.Frame()
	f.save("nodes-sheet.png")
	f.w.subs.nodes.sheet.Open = false
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_sel")
	f.w.outbounds.editor.group.nodeMenu = true
	f.tt.Frame()
	f.save("selector-editor.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("selector-editor-dark.png")
	f.tt.SetDark(false)
	f.w.outbounds.editor.close()
	f.w.outbounds.editor.openEdit(f.store.Doc(), "out_ss")
	f.tt.Frame()
	f.save("outbound-editor.png")
	f.w.outbounds.editor.close()
	f.w.outbounds.add.Open = true
	f.tt.Frame()
	f.tt.Frame()
	f.save("add-menu.png")
}

// TestLatencyButtonsAreIndependent: while one outbound is being tested,
// another card's latency test stays usable and shows its own result.
func TestLatencyButtonsAreIndependent(t *testing.T) {
	f := newFixture(t, sampleDoc())
	release := make(chan struct{})
	var once sync.Once
	var asked atomic.Int32
	defer once.Do(func() { close(release) })
	f.srv.Route("POST", "/latency/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Targets []api.LatencyTarget `json:"targets"`
		}
		_ = testkit.Body(r, &body)
		var results []api.LatencyResult
		for _, target := range body.Targets {
			if target.ID == "out_ss" {
				asked.Add(1)
				<-release
			}
			ms := 61.0
			results = append(results, api.LatencyResult{ID: target.ID, MedianConnectMs: &ms, SuccessSamples: 1})
		}
		testkit.JSON(map[string]any{"results": results})(w, r)
	})
	f.click("测速 香港 SS")
	if !f.store.Testing("out_ss") {
		t.Fatal("out_ss not testing")
	}
	f.click("测速 日本 VLESS")
	testkit.Until(t, f.tt, func() bool { return f.store.Latency["out_vless"].ID != "" && !f.store.Testing("out_vless") })
	if !f.store.Testing("out_ss") || f.store.Latency["out_ss"].ID != "" {
		t.Error("out_ss changed while its own test runs")
	}
	_ = f.tt.Click("测速 香港 SS")
	once.Do(func() { close(release) })
	testkit.Until(t, f.tt, func() bool { return !f.store.Testing("out_ss") })
	if n := asked.Load(); n != 1 {
		t.Errorf("out_ss asked %d times", n)
	}
}
