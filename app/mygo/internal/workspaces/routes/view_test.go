package routes

import (
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
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// fixtureDoc is a document with a proxy, a phone, groups and rules of the
// local scope, and a DNS rule.
func fixtureDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	direct := doc.Outbounds[0]
	direct.ID = "direct"
	proxy := model.MakeOutbound("socks", "美国节点")
	proxy.ID = "proxy"
	proxy.Fields["server"] = "203.0.113.5"
	proxy.Fields["server_port"] = float64(1080)
	doc.Outbounds = append(doc.Outbounds, proxy)
	doc.Route.Final = model.Some("direct")
	doc.Route.LocalFinal = model.Some("direct")
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "iPhone", Icon: "phone", Addresses: []string{"192.168.1.20/32"}, Final: "proxy"}}
	doc.Route.Groups = []*model.RouteGroup{
		{ID: "rg-pre", Name: "前置", DeviceID: model.PreDeviceID},
		{ID: "rg-sys", Name: "系统服务"},
		{ID: "rg-app", Name: "应用分流"},
		{ID: "rg-phone", Name: "默认分组", DeviceID: "phone"},
		{ID: "rg-other", Name: "默认分组", DeviceID: model.OtherDeviceID},
	}
	leaf := func(field string, values ...string) *model.Condition {
		return &model.Condition{Kind: model.ConditionGroup, Mode: "and", Rules: []*model.Condition{
			{Kind: model.ConditionMatch, Match: map[string]any{field: anyList(values)}}}}
	}
	doc.Route.Rules = []*model.RouteRule{
		{ID: "r-ads", GroupID: "rg-sys", Name: "广告与追踪", Enabled: false, Action: model.ActionReject, Match: map[string]any{}, ConditionTree: leaf("domain_suffix", "ads.example.com")},
		{ID: "r-apple", GroupID: "rg-app", Name: "Apple 服务", Enabled: true, Action: model.ActionRoute, Outbound: model.Some("direct"), Match: map[string]any{}, ConditionTree: leaf("domain_suffix", "apple.com")},
		{ID: "r-dev", GroupID: "rg-app", Name: "开发与协作", Enabled: true, Action: model.ActionRoute, Outbound: model.Some("proxy"), Match: map[string]any{}, ConditionTree: leaf("domain_suffix", "github.com", "gitlab.com")},
	}
	doc.DNS.Groups = append(doc.DNS.Groups, &model.RouteGroup{ID: "dg-phone", Name: "默认分组", DeviceID: "phone"})
	for _, g := range doc.DNS.Groups {
		if g.DeviceID == model.LocalDeviceID {
			g.ID = "dg-local"
		}
	}
	cn := doc.DNS.Servers[1]
	doc.DNS.Rules = []*model.DNSRule{{ID: "d-cn", GroupID: "dg-local", DeviceID: model.LocalDeviceID, Name: "国内域名", Enabled: true,
		Action: model.ActionRoute, Server: model.Some(cn.ID), Match: map[string]any{}, ConditionTree: leaf("domain_suffix", "cn")}}
	return doc
}

type fixture struct {
	t     *testing.T
	srv   *testkit.Server
	env   *env.Env
	store *store.Store
	w     *Workspace
	tt    *ui.Tester

	mu     sync.Mutex
	bodies map[string]json.RawMessage
	// ruleSets builds the Rule Sets pages in place of the workspace, as the
	// Settings workspace does; backs counts their returns to Settings.
	ruleSets bool
	backs    int
}

func newFixture(t *testing.T, routes func(f *fixture)) *fixture {
	t.Helper()
	return newFixtureDoc(t, fixtureDoc(), routes)
}

// newFixtureDoc is newFixture with the document given.
func newFixtureDoc(t *testing.T, doc *model.Doc, routes func(f *fixture)) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t), bodies: map[string]json.RawMessage{}}
	raw, _ := json.Marshal(doc)
	f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	if routes != nil {
		routes(f)
	}
	f.env, _ = testkit.NewEnv(f.srv)
	f.store = store.New(f.env)
	f.w = New(f.env, f.store)
	f.store.Load()
	f.tt = ui.NewTester(f.view, 1280, 820)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded })
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	return f
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

func (f *fixture) view(c *ui.Context) {
	kit.Apply(c)
	f.env.Drain(c)
	c.Root().Background(kit.T(c).Surface)
	if f.ruleSets {
		// As the Settings workspace: Escape leaves a text field, then goes back
		// a level.
		back := func() {
			f.backs++
			f.w.CloseRuleSets()
		}
		kit.BeginTextFocus(c)
		f.w.RuleSetsPage(c, back)
		if c.Shortcut(0, ui.KeyEscape) {
			switch {
			case kit.TextFocused(c):
				f.w.FocusRuleSetsBack()
			case f.w.RuleSetsEscape():
			default:
				back()
			}
		}
		return
	}
	f.w.View(c)
}

// choose chooses the item of the drawn menu shown at path, opening the
// submenus the others label.
func (f *fixture) choose(path ...string) {
	f.t.Helper()
	for _, label := range path {
		f.tt.Frame()
		if err := f.tt.Click(label); err != nil {
			f.t.Fatalf("menu %q: %v", path, err)
		}
	}
	f.tt.Frame()
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
}

// reveal scrolls the scroll view at x, y until text shows in the window.
func (f *fixture) reveal(text string, x, y float32) {
	f.t.Helper()
	for i := 0; i < 40; i++ {
		r, ok := f.tt.Find(text)
		if ok && r.Y+r.H < 800 && r.Y > 0 {
			return
		}
		f.tt.Scroll(x, y, 0, 120)
	}
	f.t.Fatalf("%q stays out of view", text)
}

// saved returns the document the server stores.
func (f *fixture) saved() *model.Doc {
	return model.DecodeDoc(f.srv.Store().Doc)
}

// waitSaved waits until the server's document satisfies cond.
func (f *fixture) waitSaved(cond func(doc *model.Doc) bool) {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool {
		doc := f.saved()
		return doc != nil && cond(doc) && !f.store.Saving()
	})
}

func findRule(doc *model.Doc, id string) *model.RouteRule {
	for _, r := range doc.Route.Rules {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (f *fixture) snapshot(name string, dark bool) {
	dir := os.Getenv("ROUTES_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f.tt.SetDark(dark)
	// The theme eases over a short time: the clock moves on until it has settled.
	f.tt.Advance(time.Second)
	f.tt.Frame()
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, f.tt.Image()); err != nil {
		f.t.Fatal(err)
	}
	f.tt.SetDark(false)
}

func TestDeviceAndTabSwitching(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	for _, text := range []string{"前置规则", "本机 / 网关", "iPhone", "其他设备", "系统服务", "应用分流", "默认出口", "目标出口", "3 条规则 · 2 个分组"} {
		if !tt.HasText(text) {
			t.Fatalf("missing %q: %q", text, tt.Texts())
		}
	}
	f.click("iPhone")
	testkit.UntilText(t, tt, "暂无规则，可从顶部添加规则或拖入已有规则")
	if f.w.owner != "phone" || tt.HasText("Apple 服务") {
		t.Fatalf("owner %q texts %q", f.w.owner, tt.Texts())
	}
	f.w.search = "x"
	f.click("DNS 分流")
	tt.Frame()
	if f.w.owner != "phone" || f.w.search != "" || f.w.sec != sectionDNS {
		t.Fatalf("tab switch: owner %q search %q sec %d", f.w.owner, f.w.search, f.w.sec)
	}
	for _, text := range []string{"解析服务器", "查询出口", "兜底 DNS", "添加 DNS 分流规则"} {
		if !tt.HasText(text) {
			t.Fatalf("DNS tab misses %q: %q", text, tt.Texts())
		}
	}
	f.click("本机 / 网关")
	testkit.UntilText(t, tt, "国内域名")
	if !tt.HasText("直连") {
		t.Fatalf("DNS query outbound: %q", tt.Texts())
	}
	f.snapshot("routes-dns-light.png", false)
	f.snapshot("routes-dns-dark.png", true)
}

func TestSearchFiltersRules(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.w.search = "github"
	f.tt.Frame()
	if f.tt.HasText("Apple 服务") || !f.tt.HasText("开发与协作") || !f.tt.HasText("1 / 3 条规则 · 2 个分组") {
		t.Fatalf("search: %q", f.tt.Texts())
	}
	f.w.search = "nothing-matches"
	f.tt.Frame()
	if !f.tt.HasText("没有匹配的规则") {
		t.Fatalf("empty search: %q", f.tt.Texts())
	}
	f.click("清除搜索")
	if f.w.search != "" {
		t.Fatal("清除搜索 keeps the query")
	}
}

func TestRuleEditorEditsConditions(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	if !tt.HasText("匹配条件") || !tt.HasText("apple.com") || !tt.HasText("匹配预览") {
		t.Fatalf("editor: %q", tt.Texts())
	}
	chip, ok := tt.Find("apple.com")
	if !ok {
		t.Fatal("no domain chip")
	}
	// The input follows the chips.
	tt.ClickAt(chip.X+chip.W+60, chip.Y+chip.H/2)
	tt.Type("icloud.com")
	testkit.UntilText(t, tt, "后缀 · 包含子域名")
	tt.Key(0, ui.KeyEnter)
	testkit.Until(t, tt, func() bool { return tt.HasText("icloud.com") && f.w.editor.cond.queries["root:domain"] == "" })
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		r := findRule(doc, "r-apple")
		raw, _ := json.Marshal(r.ConditionTree)
		return strings.Contains(string(raw), "icloud.com") && strings.Contains(string(raw), "apple.com")
	})
	if f.w.editor != nil {
		t.Fatal("editor stays open after saving")
	}
}

func TestEnableToggleDimsRow(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "开发与协作")
	f.click("编辑 开发与协作")
	testkit.UntilText(t, tt, "编辑路由规则")
	f.click("启用规则")
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool { return !findRule(doc, "r-dev").Enabled })
	table := buildTable(f.w.doc(), sectionTraffic, model.LocalDeviceID, "")
	for _, r := range table.rows {
		if r.kind == rowRule && r.rule.ID == "r-dev" && r.rule.Enabled {
			t.Fatal("row still enabled")
		}
	}
	f.snapshot("routes-traffic-light.png", false)
}

func TestDeleteRuleConfirms(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "开发与协作")
	f.click("编辑 开发与协作")
	testkit.UntilText(t, tt, "编辑路由规则")
	f.click("删除")
	testkit.UntilText(t, tt, "删除「开发与协作」后，其余规则按当前顺序继续匹配。")
	// The confirmation's button has the focus.
	tt.Key(0, ui.KeyEnter)
	f.waitSaved(func(doc *model.Doc) bool { return findRule(doc, "r-dev") == nil })
	testkit.Until(t, tt, func() bool { return f.w.editor == nil && !tt.HasText("开发与协作") })
}

func TestAddGroup(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("添加")
	f.choose("添加分组")
	testkit.UntilText(t, tt, "保存分组")
	tt.Type("流媒体")
	f.click("保存分组")
	f.waitSaved(func(doc *model.Doc) bool {
		return slices.ContainsFunc(doc.Route.Groups, func(g *model.RouteGroup) bool { return g.Name == "流媒体" && g.DeviceID == "" })
	})
	testkit.UntilText(t, tt, "流媒体")
}

func TestReorderMovesRulesAndGroups(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	// Rows: System Services, Ads & Tracking, App Routing, Apple Services,
	// Dev & Collaboration, Default Outbound.
	f.w.list.Reorder([]int{4}, 3)
	f.waitSaved(func(doc *model.Doc) bool {
		rules := model.RouteRulesOf(doc, model.LocalDeviceID)
		return len(rules) == 3 && rules[1].ID == "r-dev" && rules[2].ID == "r-apple"
	})
	tt.Frame()
	// The ad rule moves into App Routing's end.
	f.w.list.Reorder([]int{1}, 5)
	f.waitSaved(func(doc *model.Doc) bool {
		r := findRule(doc, "r-ads")
		return r.GroupID == "rg-app"
	})
	tt.Frame()
	// App Routing (now row 0 after System Services' empty row?) moves before
	// System Services.
	table := buildTable(f.w.doc(), sectionTraffic, model.LocalDeviceID, "")
	app := slices.IndexFunc(table.rows, func(r tableRow) bool { return r.kind == rowGroup && r.group.ID == "rg-app" })
	f.w.list.Reorder([]int{app}, 0)
	f.waitSaved(func(doc *model.Doc) bool {
		groups := model.RouteGroupsOf(doc, model.LocalDeviceID)
		return groups[0].ID == "rg-app"
	})
}

// TestDefaultOutboundEditor opens the Default Outbound row in the editor sidebar,
// numbered after the rules: its choice saves at once, and Done closes it.
func TestDefaultOutboundEditor(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	if !tt.HasText("4") || !tt.HasText("直连") {
		t.Fatalf("default row: %q", tt.Texts())
	}
	f.click("编辑 默认出口")
	testkit.Until(t, tt, func() bool { return f.w.fallback != nil && f.w.editor == nil })
	label, ok := tt.Find("出口")
	if !ok {
		t.Fatalf("no 出口 row: %q", tt.Texts())
	}
	// The select is right of the 80 DIP label.
	tt.ClickAt(label.X+80+12+60, label.Y+label.H/2)
	testkit.UntilText(t, tt, "核心默认")
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.LocalFinal.ID() == "proxy" })
	if f.w.fallback == nil {
		t.Fatal("choosing closed the sidebar")
	}
	f.click("完成")
	testkit.Until(t, tt, func() bool { return f.w.fallback == nil })
}

// TestEditorSidebarCloses switches the sidebar by pressing another row,
// and closes it, the rule editor and the default editor alike, with its
// close button and Escape.
func TestEditorSidebarCloses(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.Until(t, tt, func() bool { return f.w.editor != nil && f.w.editor.id() == "r-apple" })
	f.click("编辑 开发与协作")
	testkit.Until(t, tt, func() bool { return f.w.editor != nil && f.w.editor.id() == "r-dev" })
	f.click("关闭编辑器")
	testkit.Until(t, tt, func() bool { return f.w.editor == nil })
	tt.Frame()
	if f.w.editor != nil {
		t.Fatal("the rule editor opened again")
	}
	f.click("编辑 默认出口")
	testkit.Until(t, tt, func() bool { return f.w.fallback != nil })
	f.click("关闭编辑器")
	testkit.Until(t, tt, func() bool { return f.w.fallback == nil })
	f.click("编辑 默认出口")
	testkit.Until(t, tt, func() bool { return f.w.fallback != nil })
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return f.w.fallback == nil })
	// The "+" menu replaces the default editor with a new rule's.
	f.click("编辑 默认出口")
	testkit.Until(t, tt, func() bool { return f.w.fallback != nil })
	f.w.addRule(f.w.doc())
	tt.Frame()
	if f.w.fallback != nil || f.w.editor == nil {
		t.Fatalf("two sidebars: fallback %v editor %v", f.w.fallback, f.w.editor)
	}
}

func TestPolicyTestDialog(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		f.srv.Route("POST", "/policy/test", func(w http.ResponseWriter, r *http.Request) {
			f.record(r)
			testkit.JSON(api.PolicyTestResult{Action: "route", Rule: "Apple 服务", RuleIndex: 1, OutboundPath: []string{"直连"}, TestedAt: "1760000000000"})(w, r)
		})
	})
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("测试规则")
	testkit.UntilText(t, tt, "测试路由规则")
	f.click("运行测试")
	if !tt.HasText("请输入域名或目标 IP") {
		t.Fatalf("validation: %q", tt.Texts())
	}
	f.click("测试域名")
	tt.Type("www.apple.com")
	f.click("运行测试")
	testkit.UntilText(t, tt, "最终出口")
	var req api.PolicyTestRequest
	if err := json.Unmarshal(f.body("POST /api/v1/policy/test"), &req); err != nil {
		t.Fatal(err)
	}
	if req.Section != "route" || req.Domain != "www.apple.com" || req.DestinationPort != 443 || req.Inbound != "mixed-in" {
		t.Fatalf("request %+v", req)
	}
	if !tt.HasText("第 2 条 · Apple 服务") {
		t.Fatalf("result: %q", tt.Texts())
	}
}

// TestCopyToDevice copies a device's rules through its context menu's
// "Copy to…".
func TestCopyToDevice(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	if err := tt.RightClick("本机 / 网关"); err != nil {
		t.Fatal(err)
	}
	// The sidebar shows iPhone too: the keys choose the submenu's second
	// item (after Pre-rules), iPhone, as the owner check below confirms.
	for _, k := range []ui.Key{ui.KeyDown, ui.KeyRight, ui.KeyDown, ui.KeyEnter} {
		tt.Frame()
		tt.Key(0, k)
	}
	tt.Frame()
	testkit.UntilText(t, tt, "复制流量规则")
	if f.w.copy.selected != model.LocalDeviceID || f.w.copy.req.owner != "phone" {
		t.Fatalf("copy from %q to %q", f.w.copy.selected, f.w.copy.req.owner)
	}
	if !tt.HasText("从「本机 / 网关」复制到「iPhone」：2 个分组 · 3 条规则，按原顺序追加。") {
		t.Fatalf("summary: %q", tt.Texts())
	}
	f.click("复制")
	f.waitSaved(func(doc *model.Doc) bool { return len(model.RouteRulesOf(doc, "phone")) == 3 })
	testkit.UntilText(t, tt, "已复制 3 条规则至「iPhone」")
}

func TestMoveRuleToDevice(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.w.moveToDevice(f.w.doc(), "r-apple", "phone")
	f.waitSaved(func(doc *model.Doc) bool { r := findRule(doc, "r-apple"); return r != nil && r.DeviceID == "phone" })
	testkit.UntilText(t, f.tt, "已将“Apple 服务”移动到 iPhone")
}

func TestEditorSnapshot(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, f.tt, "编辑路由规则")
	f.snapshot("routes-editor-light.png", false)
	f.snapshot("routes-editor-dark.png", true)
}

func TestKeyboardReorder(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "开发与协作")
	f.click("编辑 开发与协作")
	testkit.UntilText(t, tt, "编辑路由规则")
	tt.Key(0, ui.KeyEscape)
	testkit.Until(t, tt, func() bool { return f.w.editor == nil })
	tt.Key(0, ui.KeySpace)
	if f.w.moving == nil || f.w.moving.key != "rule:r-dev" {
		t.Fatalf("space does not pick the rule up: %+v", f.w.moving)
	}
	tt.Key(0, ui.KeyUp)
	f.waitSaved(func(doc *model.Doc) bool {
		rules := model.RouteRulesOf(doc, model.LocalDeviceID)
		return rules[1].ID == "r-dev" && rules[2].ID == "r-apple"
	})
	tt.Key(0, ui.KeyEscape)
	f.waitSaved(func(doc *model.Doc) bool {
		rules := model.RouteRulesOf(doc, model.LocalDeviceID)
		return rules[1].ID == "r-apple" && rules[2].ID == "r-dev"
	})
	if f.w.moving != nil {
		t.Fatal("escape keeps moving")
	}
}

func TestDragRuleOntoDevice(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	from, _ := tt.Find("Apple 服务")
	to, _ := tt.Find("iPhone")
	tt.Press(from.X+from.W/2, from.Y+from.H/2)
	for i := 1; i <= 8; i++ {
		x := from.X + (to.X-from.X)*float32(i)/8
		y := from.Y + (to.Y-from.Y)*float32(i)/8
		tt.Move(x+from.W/2*(1-float32(i)/8), y+from.H/2)
	}
	tt.Move(to.X+to.W/2, to.Y+to.H/2)
	tt.Release(to.X+to.W/2, to.Y+to.H/2)
	f.waitSaved(func(doc *model.Doc) bool { r := findRule(doc, "r-apple"); return r != nil && r.DeviceID == "phone" })
}

func TestNewDNSRule(t *testing.T) {
	f := newFixture(t, nil)
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("DNS 分流")
	testkit.UntilText(t, tt, "国内域名")
	f.click("添加")
	f.choose("添加规则")
	testkit.UntilText(t, tt, "创建 DNS 分流规则")
	if !tt.HasText("查询出口") || !tt.HasText("查询类型") {
		t.Fatalf("DNS editor: %q", tt.Texts())
	}
	f.click("保存规则")
	if !tt.HasText("请配置至少一项条件") {
		t.Fatalf("validation: %q", tt.Texts())
	}
	f.click("A")
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.DNS.Rules) == 2 })
}

func TestDeviceDialogWithLANDevices(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		f.srv.Route("GET", "/info", testkit.JSON(api.ServerInfo{Version: "test", OS: "linux", Capabilities: map[string]bool{"lanDevices": true}}))
		f.srv.Route("GET", "/lan-devices", testkit.JSON(map[string]any{"devices": []api.LANDevice{
			{MAC: "02:11:22:33:44:55", Hostname: "Living-TV", Addresses: []string{"192.168.1.40"}, Sources: []string{"dhcp"}}}, "warnings": []string{}}))
	})
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("添加设备")
	testkit.UntilText(t, tt, "保存设备")
	f.click("从局域网设备选择")
	testkit.UntilText(t, tt, "Living-TV")
	f.click("选择 Living-TV")
	f.click("保存设备")
	f.waitSaved(func(doc *model.Doc) bool {
		return slices.ContainsFunc(doc.Route.Devices, func(d *model.RouteDevice) bool {
			return d.Name == "Living-TV" && slices.Equal(d.Addresses, []string{"192.168.1.40"})
		})
	})
	if f.w.owner == model.LocalDeviceID {
		t.Fatal("the new device is not chosen")
	}
}

func TestProcessPicker(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		f.srv.Route("GET", "/applications", testkit.JSON(api.Applications{Supported: true, OS: "darwin",
			Applications: []api.InstalledApplication{{ID: "safari", Name: "Safari", Path: "/Applications/Safari.app"}}}))
	})
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("编辑 Apple 服务")
	testkit.UntilText(t, tt, "编辑路由规则")
	tt.SetSize(1280, 1600)
	tt.Frame()
	f.click("添加进程条件")
	testkit.UntilText(t, tt, "选择 Safari")
	f.snapshot("routes-process-picker-light.png", false)
	f.click("选择 Safari")
	testkit.UntilText(t, tt, "已添加 1 项")
	f.click("完成")
	testkit.UntilText(t, tt, "Safari")
	if f.w.editor == nil {
		t.Fatal("the picker closed the editor")
	}
	// The chip edits the condition, showing its path regex.
	f.click(`^/Applications/Safari\.app/`)
	testkit.UntilText(t, tt, "修改进程条件")
	if !tt.HasText("当前条件") || !tt.HasText(`^/Applications/Safari\.app/`) {
		t.Fatalf("the original value is not shown: %q", tt.Texts())
	}
	f.snapshot("routes-process-edit-light.png", false)
	f.click("完成")
	tt.Frame()
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		raw, _ := json.Marshal(findRule(doc, "r-apple").ConditionTree)
		return strings.Contains(string(raw), "process_path_regex")
	})
}

// TestDNSRuleWithSeveralServers chooses a second server for a DNS rule,
// gives each its own Query Outbound, saves and shows both in the table.
func TestDNSRuleWithSeveralServers(t *testing.T) {
	f := newFixture(t, func(f *fixture) {
		doc := fixtureDoc()
		doc.DNS.Servers = append(doc.DNS.Servers, &model.DNSServer{ID: "dns-g", Tag: "dns-google", Enabled: true, Type: "udp", Server: "8.8.8.8"})
		raw, _ := json.Marshal(doc)
		f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	})
	tt := f.tt
	testkit.UntilText(t, tt, "Apple 服务")
	f.click("DNS 分流")
	testkit.UntilText(t, tt, "国内域名")
	f.click("编辑 国内域名")
	testkit.UntilText(t, tt, "编辑 DNS 分流规则")
	f.click("DNS 服务器：dns-cn")
	tt.Frame()
	f.click("dns-google")
	tt.Frame()
	f.click("DNS 服务器：dns-cn、dns-google")
	tt.Frame()
	if !tt.HasText("dns-google") || tt.HasText("没有匹配项") {
		t.Fatalf("editor: %q", tt.Texts())
	}
	f.click("dns-google 查询出口：直连（服务器拨号）")
	tt.Frame()
	tt.Type("美国")
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	f.snapshot("routes-dns-parallel-editor-light.png", false)
	f.snapshot("routes-dns-parallel-editor-dark.png", true)
	f.click("保存规则")
	f.waitSaved(func(doc *model.Doc) bool {
		for _, r := range doc.DNS.Rules {
			if r.ID == "d-cn" {
				targets := r.Targets()
				return len(targets) == 2 && targets[1].Server.ID() == "dns-g" && targets[1].Detour.ID() == "proxy" && !targets[0].Detour.Valid()
			}
		}
		return false
	})
	testkit.Until(t, tt, func() bool { return f.w.editor == nil })
	testkit.UntilText(t, tt, "dns-cn、dns-google")
	if !tt.HasText("直连、美国节点") {
		t.Fatalf("query outbound column: %q", tt.Texts())
	}
	f.snapshot("routes-dns-parallel-light.png", false)
	f.snapshot("routes-dns-parallel-dark.png", true)
}

func TestMITMOutcome(t *testing.T) {
	doc := model.EmptyDoc("linux")
	if _, ok := mitmOutcome(doc, "a.example.com", false, mitmpolicy.Source{}, "", false); ok {
		t.Error("feature off shown")
	}
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true,
		Hostnames: &model.MITMHostnames{Include: []string{"*.example.com"}, Exclude: []string{"pay.example.com"}}}}
	cases := []struct {
		host      string
		untrusted bool
		core      string
		answered  bool
		show      bool
		decision  string
		label     string
	}{
		{"a.example.com", false, "", false, true, mitmpolicy.Decrypted, "解密"},
		{"a.example.com", false, "decrypted", true, true, mitmpolicy.Decrypted, "解密"},
		{"a.example.com", false, "", true, true, mitmpolicy.None, "不解密"},
		{"pay.example.com", false, "decrypted", true, true, mitmpolicy.Decrypted, "解密"},
		{"pay.example.com", false, "skipped:excluded", true, true, mitmpolicy.Skipped, "已排除"},
		{"pay.example.com", false, "", false, true, mitmpolicy.Skipped, "已排除"},
		{"a.example.com", true, "", false, true, mitmpolicy.Skipped, "证书未信任"},
		{"other.org", false, "", false, true, mitmpolicy.Skipped, "不在包含列表"},
		{"a.example.com", false, "skipped:pinned", true, true, mitmpolicy.Skipped, "证书固定"},
		{"a.example.com", false, "skipped:builtin", true, true, mitmpolicy.Skipped, "内置排除"},
		{"a.example.com", false, "skipped:scope", true, true, mitmpolicy.Skipped, "设备未启用"},
		{"a.example.com", false, "blocked-quic", true, true, mitmpolicy.BlockedQUIC, "阻止 QUIC"},
		{"", false, "", false, false, "", ""},
	}
	for _, tc := range cases {
		o, ok := mitmOutcome(doc, tc.host, tc.untrusted, mitmpolicy.Source{}, tc.core, tc.answered)
		if ok != tc.show || ok && (o.Decision != tc.decision || o.Label() != tc.label) {
			t.Errorf("%+v: got %+v show %v", tc, o, ok)
		}
	}
	o, _ := mitmOutcome(doc, "a.example.com", false, mitmpolicy.Source{}, "skipped:weird", true)
	if got := mitmText(o); got != "跳过 · weird" {
		t.Errorf("text %q", got)
	}
}
