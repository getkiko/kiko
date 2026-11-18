package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// subpageDoc has configs of every kind and a few DNS servers.
func subpageDoc() *model.Doc {
	doc := mixedDoc()
	doc.DNS.Servers = []*model.DNSServer{
		{ID: "dns-local", Tag: "dns-local", Enabled: true, Type: "local"},
		{ID: "dns-cn", Tag: "dns-cn", Enabled: true, Type: "https", Server: "223.5.5.5", Path: "/dns-query"},
		{ID: "dns-cf", Tag: "dns-cf-udp", Enabled: false, Type: "udp", Server: "1.1.1.1"},
	}
	doc.DNS.Final = model.Some("dns-cn")
	doc.Route.DefaultDomainResolver = model.Some("dns-local")
	doc.Route.DefaultDomainResolverDetour = model.Null()
	doc.DNS.PresetsVersion = model.DNSPresetsVersion
	return doc
}

func subpageLibrary() []*model.NetworkConfig {
	return []*model.NetworkConfig{
		{ID: "net-a", Name: "低延迟", Kind: model.NetworkConfigNetwork, UpdatedAt: 1762400000000,
			Fields: model.Fields{"tcp_fast_open": true, "connect_timeout": "5s"}},
		{ID: "net-b", Name: "绑定 en0", Kind: model.NetworkConfigNetwork, UpdatedAt: 1762410000000,
			Fields: model.Fields{"bind_interface": "en0"}},
		{ID: "tls-a", Name: "TLS 1.3", Kind: model.NetworkConfigTLSClient, UpdatedAt: 1762420000000,
			Fields: model.Fields{"tls.min_version": "1.3", "tls.alpn": []any{"h2"}}},
		{ID: "tls-b", Name: "服务端证书", Kind: model.NetworkConfigTLSServer, UpdatedAt: 1762430000000,
			Fields: model.Fields{}},
	}
}

func (f *fixture) escape() {
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
}

// The three rows open their subpages, which return by the back button
// and by Escape, the row taking the focus again.
func TestSubpagesOpenAndReturn(t *testing.T) {
	f := newFixture(t, subpageDoc())
	for _, p := range []struct{ row, page, shows string }{
		{"通用网络配置", pageNetwork, "新建配置"},
		{"高级 TLS 配置", pageTLS, "新建服务端配置"},
		{"DNS 服务器", pageDNS, "DNS 查询策略"},
	} {
		f.click(p.row)
		testkit.UntilText(t, f.tt, p.shows)
		if f.w.page != p.page || !f.tt.HasText("返回设置") {
			t.Fatalf("%s: page %q, texts %q", p.row, f.w.page, f.tt.Texts())
		}
		f.click("返回设置")
		f.tt.Frame()
		if f.w.page != pageMain || !f.tt.Focused(p.row) {
			t.Errorf("%s: back to %q, row focused %v", p.row, f.w.page, f.tt.Focused(p.row))
		}
		f.click(p.row)
		testkit.UntilText(t, f.tt, p.shows)
		f.escape()
		if f.w.page != pageMain || !f.tt.Focused(p.row) {
			t.Errorf("%s: Escape to %q, row focused %v", p.row, f.w.page, f.tt.Focused(p.row))
		}
	}
}

// Escape returns from every subpage.
func TestEscapeReturnsFromEverySubpage(t *testing.T) {
	f := newFixture(t, subpageDoc())
	for _, page := range []string{pageNetwork, pageTLS, pageServers, pageRemote, pageDNS, pageRegions, pageHTTPS} {
		f.w.Open(page)
		f.tt.Frame()
		f.escape()
		if f.w.page != pageMain {
			t.Errorf("Escape on %q left page %q", page, f.w.page)
		}
	}
}

// With a dialog, a popover or a search to clear, Escape closes that and
// stays on the page.
func TestEscapeClosesWhatIsOpenFirst(t *testing.T) {
	f := newFixture(t, subpageDoc())
	f.store.SetNetworkConfigs(subpageLibrary())
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })

	f.click("通用网络配置")
	f.click("新建配置")
	testkit.UntilText(t, f.tt, "使用位置 · 0")
	f.escape()
	if f.w.library.editorOpen || f.w.page != pageNetwork {
		t.Fatalf("editor open %v, page %q", f.w.library.editorOpen, f.w.page)
	}
	f.click("低延迟")
	testkit.UntilText(t, f.tt, "使用位置 · 0")
	f.click("删除")
	f.tt.Frame()
	if !f.w.library.remove.Open {
		t.Fatalf("no confirmation: %q", f.tt.Texts())
	}
	f.escape()
	if f.w.library.remove.Open || !f.w.library.editorOpen {
		t.Fatalf("Escape on the confirmation: open %v, editor %v", f.w.library.remove.Open, f.w.library.editorOpen)
	}
	f.escape()
	f.escape()
	if f.w.page != pageMain || len(f.store.NetworkConfigs()) != 4 {
		t.Fatalf("page %q, %d configs", f.w.page, len(f.store.NetworkConfigs()))
	}

	f.click("DNS 服务器")
	f.click("添加服务器")
	testkit.UntilText(t, f.tt, "添加 DNS 服务器")
	f.escape()
	if f.tt.HasText("添加 DNS 服务器") || f.w.page != pageDNS {
		t.Fatalf("DNS editor: page %q, texts %q", f.w.page, f.tt.Texts())
	}
	f.click("搜索 DNS 服务器")
	f.tt.Type("cf")
	f.tt.Frame()
	if f.tt.HasText("编辑 dns-local") {
		t.Fatalf("search: %q", f.tt.Texts())
	}
	f.escape()
	if !f.tt.HasText("编辑 dns-local") || f.w.page != pageDNS {
		t.Fatalf("Escape on the search: page %q, texts %q", f.w.page, f.tt.Texts())
	}
	// The empty field gives the focus to the back button first.
	f.escape()
	if f.w.page != pageDNS || !f.tt.Focused("返回设置") {
		t.Fatalf("Escape in the empty search: page %q, back focused %v", f.w.page, f.tt.Focused("返回设置"))
	}
	f.escape()
	if f.w.page != pageMain {
		t.Fatalf("page %q", f.w.page)
	}
}

// In a text field a first Escape leaves the field for the back button, a
// second returns.
func TestEscapeLeavesTextFieldFirst(t *testing.T) {
	f := newFixture(t, subpageDoc())
	f.click("DNS 服务器")
	testkit.UntilText(t, f.tt, "EDNS Client Subnet")
	f.clickRowControl("EDNS Client Subnet", 100)
	f.tt.Frame()
	if !f.tt.Focused("EDNS Client Subnet") {
		t.Fatal("the field has no focus")
	}
	f.escape()
	if f.w.page != pageDNS || f.tt.Focused("EDNS Client Subnet") || !f.tt.Focused("返回设置") {
		t.Fatalf("first Escape: page %q, field focused %v", f.w.page, f.tt.Focused("EDNS Client Subnet"))
	}
	f.escape()
	if f.w.page != pageMain {
		t.Fatalf("second Escape: page %q", f.w.page)
	}
}

// The library pages search names and parameters; the TLS page's search
// narrows both groups.
func TestLibrarySearch(t *testing.T) {
	f := newFixture(t, subpageDoc())
	f.store.SetNetworkConfigs(subpageLibrary())
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	f.click("通用网络配置")
	testkit.UntilText(t, f.tt, "2 个")
	f.click("搜索配置")
	f.tt.Type("en0")
	f.tt.Frame()
	if f.tt.HasText("低延迟") || !f.tt.HasText("绑定 en0") || !f.tt.HasText("1 / 2 个") {
		t.Fatalf("name search: %q", f.tt.Texts())
	}
	f.w.library.search = "fast open"
	f.tt.Frame()
	if !f.tt.HasText("低延迟") || f.tt.HasText("绑定 en0") {
		t.Fatalf("parameter search: %q", f.tt.Texts())
	}
	f.w.library.search = "nothing"
	f.tt.Frame()
	if !f.tt.HasText("没有匹配的配置") || !f.tt.HasText("0 / 2 个") {
		t.Fatalf("no match: %q", f.tt.Texts())
	}
	f.click("清除搜索")
	if !f.tt.HasText("低延迟") || !f.tt.HasText("绑定 en0") || !f.tt.HasText("2 个") {
		t.Fatalf("cleared: %q", f.tt.Texts())
	}
	f.click("返回设置")
	f.click("高级 TLS 配置")
	if f.w.library.search != "" {
		t.Fatalf("search kept: %q", f.w.library.search)
	}
	f.w.library.search = "1.3"
	f.tt.Frame()
	if !f.tt.HasText("TLS 1.3") || f.tt.HasText("服务端证书") || !f.tt.HasText("1 / 1 个") || !f.tt.HasText("0 / 1 个") ||
		!f.tt.HasText("没有匹配的配置") {
		t.Fatalf("TLS search: %q", f.tt.Texts())
	}
}

// The TLS page lists client and server configs in their groups, each
// creating its kind; a copy opens in the editor; references and the last
// change show on each row.
func TestTLSLibraryGroups(t *testing.T) {
	f := newFixture(t, subpageDoc())
	f.store.SetNetworkConfigs(subpageLibrary())
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	f.click("高级 TLS 配置")
	for _, text := range []string{"客户端", "服务端", "TLS 1.3", "服务端证书", "0 处引用", "新建客户端配置"} {
		if !f.tt.HasText(text) {
			t.Fatalf("missing %q in %q", text, f.tt.Texts())
		}
	}
	if f.tt.HasText("低延迟") {
		t.Error("a network config on the TLS page")
	}
	client, _ := f.tt.Find("TLS 1.3")
	server, _ := f.tt.Find("服务端证书")
	heading, _ := f.tt.Find("服务端")
	if !(client.Y < heading.Y && heading.Y < server.Y) {
		t.Errorf("client %v, 服务端 %v, server %v", client, heading, server)
	}
	f.click("新建服务端配置")
	testkit.UntilText(t, f.tt, "使用位置 · 0")
	revision := f.srv.Store().Revision
	f.click("保存配置")
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision > revision && !f.store.Saving() })
	added := f.store.NetworkConfigs()[4]
	if added.Kind != model.NetworkConfigTLSServer || added.Name != "新建服务端 TLS 配置" {
		t.Fatalf("added %+v", added)
	}
	f.click("复制 TLS 1.3")
	testkit.UntilText(t, f.tt, "使用位置 · 0")
	if f.w.library.draft == nil || f.w.library.draft.Name != "TLS 1.3 副本" || f.w.library.draft.Kind != model.NetworkConfigTLSClient {
		t.Fatalf("copy %+v", f.w.library.draft)
	}
	f.click("取消")
}

// The DNS page toggles a server by its switch and opens its details from
// its menu.
func TestDNSPageRows(t *testing.T) {
	f := newFixture(t, subpageDoc())
	f.click("DNS 服务器")
	testkit.UntilText(t, f.tt, "dns-cf-udp")
	for _, text := range []string{"DNS 设置", "服务器", "3 个", "默认", "系统解析器", "udp://1.1.1.1"} {
		if !f.tt.HasText(text) {
			t.Fatalf("missing %q in %q", text, f.tt.Texts())
		}
	}
	if f.tt.HasText("查询策略、默认域名解析器与缓存。") {
		t.Error("the title's description shows")
	}
	revision := f.srv.Store().Revision
	f.click("启用 dns-cf-udp")
	testkit.Until(t, f.tt, func() bool { return f.srv.Store().Revision > revision && !f.store.Saving() })
	if !model.DNSServerByID(f.store.Doc(), "dns-cf").Enabled {
		t.Fatal("switch did not enable the server")
	}
	f.click("dns-cn 操作")
	f.tt.Frame()
	f.click("查看详情")
	testkit.UntilText(t, f.tt, "当前服务器")
	f.escape()
	if f.w.page != pageDNS || f.w.dns == nil || f.tt.HasText("当前服务器") {
		t.Fatalf("details: page %q, texts %q", f.w.page, f.tt.Texts())
	}
}

// Screenshots of the three pages: light, dark and narrow.
func TestSubpageShots(t *testing.T) {
	f := newFixture(t, subpageDoc())
	f.store.SetNetworkConfigs(subpageLibrary())
	testkit.Until(t, f.tt, func() bool { return !f.store.Saving() })
	for _, p := range []struct{ name, page string }{{"network", pageNetwork}, {"tls", pageTLS}, {"dns", pageDNS}} {
		f.w.Open(p.page)
		f.tt.SetSize(width, 900)
		f.tt.SetDark(false)
		f.tt.Frame()
		f.save(p.name + "-light.png")
		f.tt.SetDark(true)
		f.tt.Frame()
		f.save(p.name + "-dark.png")
		f.tt.SetDark(false)
		f.tt.SetSize(760, 900)
		f.tt.Frame()
		f.save(p.name + "-narrow.png")
		f.tt.SetSize(480, 900)
		f.tt.Frame()
		f.save(p.name + "-480-light.png")
		f.tt.SetDark(true)
		f.tt.Frame()
		f.save(p.name + "-480-dark.png")
		f.tt.SetDark(false)
		f.escape()
	}
	f.tt.SetSize(width, height)
	f.tt.Frame()
}
