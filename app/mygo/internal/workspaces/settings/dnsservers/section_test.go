package dnsservers

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
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
	s     *Section
	tt    *ui.Tester
}

func testDoc() *model.Doc {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.DNS.Servers = []*model.DNSServer{
		{ID: "dns-local", Tag: "dns-local", Enabled: true, Type: "local"},
		{ID: "dns-cn", Tag: "dns-cn", Enabled: true, Type: "https", Server: "223.5.5.5", Path: "/dns-query",
			Fields: model.Fields{"tls": map[string]any{"server_name": "dns.alidns.com"}}},
		{ID: "dns-cf", Tag: "dns-cf-udp", Enabled: false, Type: "udp", Server: "1.1.1.1"},
	}
	doc.DNS.Final = model.Some("dns-cn")
	// The presets would join the three servers on load.
	doc.DNS.PresetsVersion = model.DNSPresetsVersion
	return doc
}

func newFixture(t *testing.T, doc *model.Doc) *fixture {
	t.Helper()
	f := &fixture{t: t, srv: testkit.NewServer(t)}
	raw, _ := json.Marshal(doc)
	f.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	f.env, _ = testkit.NewEnv(f.srv)
	f.store = store.New(f.env)
	f.s = New(f.env, f.store)
	f.store.Load()
	f.tt = ui.NewTester(f.view, 1100, 900)
	testkit.Until(t, f.tt, func() bool { return f.store.Loaded && !f.store.Saving() })
	return f
}

func (f *fixture) view(c *ui.Context) {
	kit.Apply(c)
	f.env.Drain(c)
	c.Root().Background(kit.T(c).Surface)
	ui.Scroll(c).Fill().Padding(0, 20, 24).Children(func() {
		f.s.Settings(c)
		f.s.View(c)
	})
}

// clickField clicks the control on the right of a form row's label.
func (f *fixture) clickField(label string) {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q in %q", label, f.tt.Texts())
	}
	f.tt.ClickAt(r.X+190, r.Y+r.H/2)
	f.tt.Frame()
}

func (f *fixture) click(label string) {
	f.t.Helper()
	if err := f.tt.Click(label); err != nil {
		f.t.Fatalf("click %q: %v; texts %q", label, err, f.tt.Texts())
	}
	f.tt.Frame()
}

// stored is the document the fake server holds.
func (f *fixture) stored() *model.Doc {
	return model.DecodeDoc(f.srv.Store().Doc)
}

func (f *fixture) saved(cond func(doc *model.Doc) bool) {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return !f.store.Saving() && cond(f.stored()) })
}

func (f *fixture) snapshot(name string) {
	dir := os.Getenv("DNS_PREVIEW_DIR")
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

func TestRowsRender(t *testing.T) {
	f := newFixture(t, testDoc())
	for _, text := range []string{"服务器", "3 个", "dns-local", "dns-cn", "dns-cf-udp", "系统解析器",
		"https://223.5.5.5/dns-query", "udp://1.1.1.1", "DoH", "默认"} {
		if !f.tt.HasText(text) {
			t.Fatalf("missing %q in %q", text, f.tt.Texts())
		}
	}
	f.snapshot("dns-rows")
}

func TestSearchSortFilter(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("搜索 DNS 服务器")
	f.tt.Type("cf")
	f.tt.Frame()
	if f.tt.HasText("dns-local") || !f.tt.HasText("dns-cf-udp") || !f.tt.HasText("1 / 3 个") {
		t.Fatalf("search: %q", f.tt.Texts())
	}
	f.s.filter.search = ""
	f.tt.Frame()

	// The filter menu is gone (c0394d1); the filters still narrow the
	// rows.
	f.s.filter.status = "disabled"
	f.tt.Frame()
	if f.tt.HasText("dns-local") || !f.tt.HasText("dns-cf-udp") || !f.tt.HasText("1 / 3 个") {
		t.Fatalf("status filter: %q", f.tt.Texts())
	}
	f.s.filter.status = "all"
	f.s.filter.protocols = []string{"https"}
	f.tt.Frame()
	if !f.tt.HasText("dns-cn") || f.tt.HasText("dns-cf-udp") {
		t.Fatalf("protocol filter: %q", f.tt.Texts())
	}
	f.s.clearFilters()
	f.tt.Frame()

	f.s.filter.sort = parseSort("name:desc")
	visible := f.s.filter.visible(f.store.Doc().DNS.Servers)
	if visible[0].Tag != "dns-local" || visible[2].Tag != "dns-cf-udp" {
		t.Fatalf("sort: %v %v %v", visible[0].Tag, visible[1].Tag, visible[2].Tag)
	}
	f.s.filter.sort = parseSort("protocol:asc")
	visible = f.s.filter.visible(f.store.Doc().DNS.Servers)
	if visible[0].Type != "https" || visible[2].Type != "udp" {
		t.Fatalf("protocol sort: %v %v %v", visible[0].Type, visible[1].Type, visible[2].Type)
	}
}

func TestNoMatchEmptyState(t *testing.T) {
	f := newFixture(t, testDoc())
	f.s.filter.search = "nothing-here"
	f.tt.Frame()
	if !f.tt.HasText("没有匹配的服务器") || !f.tt.HasText("0 / 3 个") {
		t.Fatalf("empty: %q", f.tt.Texts())
	}
	f.s.filter.search = ""
	// No server is a disabled DoH one: the empty state's button clears
	// the filters too.
	f.s.filter.status, f.s.filter.protocols = "disabled", []string{"https"}
	f.tt.Frame()
	if !f.tt.HasText("没有匹配的服务器") {
		t.Fatalf("empty: %q", f.tt.Texts())
	}
	f.click("清除搜索")
	if !f.tt.HasText("dns-local") || f.s.filter.filtered() {
		t.Fatalf("cleared: %q", f.tt.Texts())
	}
}

func TestAddCustomServer(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("添加服务器")
	if !f.tt.HasText("添加 DNS 服务器") {
		t.Fatalf("editor: %q", f.tt.Texts())
	}
	f.clickField("服务器地址")
	f.tt.Type("tls://dns.google")
	f.tt.Frame()
	f.click("保存服务器")
	f.saved(func(doc *model.Doc) bool { return len(doc.DNS.Servers) == 4 })
	added := f.stored().DNS.Servers[3]
	if added.Type != "tls" || added.Server != "dns.google" || added.Tag != "dns-udp-4" {
		t.Fatalf("added %+v", added)
	}
	if f.tt.HasText("添加 DNS 服务器") {
		t.Fatal("editor stayed open")
	}
}

func TestAddressError(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("添加服务器")
	f.click("保存服务器")
	if !f.tt.HasText("请填写服务器地址") {
		t.Fatalf("no address error: %q", f.tt.Texts())
	}
}

func TestEditServer(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("编辑 dns-cn")
	if !f.tt.HasText("编辑 DNS 服务器") {
		t.Fatalf("editor: %q", f.tt.Texts())
	}
	f.snapshot("dns-editor")
	f.click("DNS 服务器名称")
	f.tt.Key(ui.Cmd, ui.KeyA)
	f.tt.Type("ali-doh")
	f.tt.Frame()
	f.click("保存服务器")
	f.saved(func(doc *model.Doc) bool { return doc.DNS.Servers[1].Tag == "ali-doh" })
	s := f.stored().DNS.Servers[1]
	if s.Type != "https" || s.Server != "223.5.5.5" || s.Path != "/dns-query" {
		t.Fatalf("edited %+v", s)
	}
}

func TestDeleteServer(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("编辑 dns-cf-udp")
	f.click("删除")
	f.click("删除服务器")
	f.saved(func(doc *model.Doc) bool { return len(doc.DNS.Servers) == 2 })
}

func TestToggleEnable(t *testing.T) {
	f := newFixture(t, testDoc())
	f.click("启用 dns-local")
	f.saved(func(doc *model.Doc) bool { return !doc.DNS.Servers[0].Enabled })
	f.click("启用 dns-cf-udp")
	f.saved(func(doc *model.Doc) bool { return doc.DNS.Servers[2].Enabled })
}

func TestDetailsDialog(t *testing.T) {
	f := newFixture(t, testDoc())
	f.s.details, f.s.detailsOpen = "dns-cn", true
	f.tt.Frame()
	for _, text := range []string{"服务器", "已启用", "DoH", "引用", "当前服务器", "核心默认", "安全连接", "dns.alidns.com", "启用"} {
		if !f.tt.HasText(text) {
			t.Fatalf("details missing %q: %q", text, f.tt.Texts())
		}
	}
	f.snapshot("dns-details")
	f.click("复制地址")
	if f.tt.Clipboard() != "https://223.5.5.5/dns-query" {
		t.Fatalf("clipboard %q", f.tt.Clipboard())
	}
	f.click("编辑")
	if !f.tt.HasText("编辑 DNS 服务器") {
		t.Fatalf("details edit: %q", f.tt.Texts())
	}
}

func TestSettingsRows(t *testing.T) {
	f := newFixture(t, testDoc())
	for _, text := range []string{"DNS 设置", "DNS 查询策略", "默认域名解析器", "默认解析器策略", "缓存容量", "EDNS Client Subnet", "禁用缓存", "禁用过期", "反向映射"} {
		if !f.tt.HasText(text) {
			t.Fatalf("settings missing %q: %q", text, f.tt.Texts())
		}
	}
	f.snapshot("dns-settings")
	for text, want := range map[string]int{"": 0, " 4096 ": 4096} {
		if n, ok := parseCapacity(text); !ok || n != want {
			t.Fatalf("capacity %q: %d %v", text, n, ok)
		}
	}
	for _, text := range []string{"-1", "1.5", "x"} {
		if _, ok := parseCapacity(text); ok {
			t.Fatalf("capacity %q accepted", text)
		}
	}
}

func TestAddPresetsOnLoad(t *testing.T) {
	doc := testDoc()
	doc.DNS.PresetsVersion = 0
	f := newFixture(t, doc)
	f.saved(func(doc *model.Doc) bool { return doc.DNS.PresetsVersion == model.DNSPresetsVersion })
	servers := f.stored().DNS.Servers
	// dns-cn is AliDNS's DoH on 223.5.5.5 and dns-cf-udp Cloudflare's UDP
	// on 1.1.1.1: both presets are listed already.
	if len(servers) != 3+len(model.DNSPresets)-2 {
		t.Fatalf("%d servers", len(servers))
	}
	for _, s := range servers[3:] {
		if s.Enabled || s.Tag == "dns-ali-https" || s.Tag == "dns-cf-udp" {
			t.Fatalf("preset %+v", s)
		}
	}
	if !servers[0].Enabled || !servers[1].Enabled {
		t.Fatal("the document's servers changed")
	}
}
