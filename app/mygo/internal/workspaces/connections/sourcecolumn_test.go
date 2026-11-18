package connections

import (
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

const longDevice = "书房里那台名字特别特别长的 Mac mini 设备"

// sourceRows are the fixture's connections from the host's TUN address
// (172.18.0.1) and the living-room TV (172.18.0.2), and one each from a
// device with a long name and from an unnamed address.
func sourceRows(now time.Time) []api.ConnectionRow {
	rows := fixtureRows(now)
	for i, s := range []struct{ id, host, source string }{
		{"c5", "updates.example.com", "192.168.1.23:50123"},
		{"c6", "printer.example.com", "10.0.0.99:50001"},
	} {
		rows = append(rows, api.ConnectionRow{Connection: api.Connection{
			ID: s.id, Inbound: "tun-in", InboundType: "tun", IPVersion: 4, Network: "tcp", Source: s.source,
			Destination: fmt.Sprintf("17.253.%d.1:443", i), Domain: s.host, Protocol: "tls",
			CreatedAt:   api.Int64(now.Add(-time.Duration(4+i) * time.Minute).UnixMilli()),
			UplinkTotal: api.Int64(1200), DownlinkTotal: api.Int64(3400),
			Rule: "final", Outbound: "直连", OutboundType: "direct", ChainList: []string{"直连"},
			ProcessInfo: &api.ProcessInfo{ProcessID: 300 + i, ProcessPath: "/usr/sbin/softwareupdated"},
		}})
	}
	return rows
}

// sourceDoc is a document with a TUN inbound at 172.18.0.1/30 and the
// devices "living-room TV" (172.18.0.2) and longDevice (192.168.1.23).
func sourceDoc() *model.Doc {
	doc := model.EmptyDoc("darwin")
	tun := model.MakeInbound("tun", "tun-in", "darwin", true)
	tun.Enabled = true
	doc.Inbounds = append(doc.Inbounds, tun)
	doc.Route.Devices = []*model.RouteDevice{
		{ID: "d1", Name: "客厅电视", Addresses: []string{"172.18.0.2"}},
		{ID: "d2", Name: longDevice, Addresses: []string{"192.168.1.23/32"}},
	}
	return doc
}

// newSourceHarness is newSizedHarness over a store with sourceDoc, the
// connections being sourceRows. No core runs and no interface address is
// detected: the local server comes from the document alone.
func newSourceHarness(t *testing.T, width, height int, routes func(*testkit.Server)) (*harness, *store.Store) {
	t.Helper()
	h := &harness{srv: testkit.NewServer(t)}
	h.conns = newFakeConnections(sourceRows(time.Now()))
	h.srv.Route("GET", "/connections", h.conns.ServeHTTP)
	h.srv.Route("GET", "/dns/capabilities", testkit.JSON(api.DNSCapabilities{Supported: true, Enabled: true, SchemaVersion: 1}))
	h.srv.Route("GET", "/dns/stream", ndjson(fixtureDNS()))
	h.srv.Route("GET", "/dns/queries/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		for _, q := range fixtureDNS().Queries {
			if q.ID == id {
				testkit.JSON(q)(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
	if routes != nil {
		routes(h.srv)
	}
	raw, _ := json.Marshal(sourceDoc())
	h.srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	h.env, h.shell = testkit.NewEnv(h.srv)
	s := store.New(h.env)
	h.w = New(h.env, s)
	h.tt = ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		h.env.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		h.w.View(c)
	}, width, height)
	h.w.SetActive(true)
	t.Cleanup(func() { h.w.SetActive(false) })
	s.Load()
	testkit.Until(t, h.tt, func() bool { return s.Loaded })
	h.w.conn.setMode(groupNone)
	return h, s
}

func TestSourceColumnShowsDeviceNames(t *testing.T) {
	h, s := newSourceHarness(t, 1440, 820, nil)
	tt := h.tt
	testkit.UntilText(t, tt, "172.18.0.2:61804")
	// The filter and the list both name the addresses: the living-room TV
	// once in the filter and once in its row, the long name likewise; the
	// TUN address is Local in the filter and in its three rows.
	testkit.Until(t, tt, func() bool {
		texts := tt.Texts()
		return countText(texts, "客厅电视") == 2 && countText(texts, longDevice) == 2 && countText(texts, "本机") == 4
	})
	for _, want := range []string{"172.18.0.1:61261", "172.18.0.1:5353", "192.168.1.23:50123", "10.0.0.99:50001"} {
		if countText(tt.Texts(), want) != 1 {
			t.Errorf("source %q shown %d times", want, countText(tt.Texts(), want))
		}
	}

	// The inspector names the source; the value stays the address.
	h.w.conn.selectedID = "c3"
	testkit.UntilText(t, tt, "关闭此连接")
	testkit.Until(t, tt, func() bool {
		texts := tt.Texts()
		return countText(texts, "客厅电视") == 3 && countText(texts, "172.18.0.2:61804") == 2
	})
	row := &Row{Conn: api.Connection{Source: "172.18.0.2:61804"}}
	if got := columnText(row, colSource); got != "172.18.0.2:61804" {
		t.Errorf("source text for search, CSV and sorting: %q", got)
	}

	// Sorting by the source column asks the server for the address order.
	h.w.conn.table.Sort.Column = string(colSource)
	testkit.Until(t, tt, func() bool {
		for _, q := range h.conns.queried() {
			if strings.Contains(q, "sort=source") {
				return true
			}
		}
		return false
	})

	// A rename shows in the filter, the row and the inspector at once.
	names := h.w.conn.sourceNames()
	tt.Frame()
	if h.w.conn.sourceNames() != names {
		t.Error("names rebuilt without a change")
	}
	if err := s.Update(func(d *model.Doc) { d.Route.Devices[0].Name = "卧室电视" }); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), "卧室电视") == 3 })
	if tt.HasText("客厅电视") {
		t.Errorf("old name still shown: %q", tt.Texts())
	}

	// A search is the address's text, not the device's name.
	h.click(t, "搜索主机、进程、地址、链路…")
	tt.Type("172.18.0.2")
	testkit.Until(t, tt, func() bool { return !tt.HasText("mtalk.google.com") })
	if !tt.HasText("ime.doubao.com") {
		t.Fatalf("search: %q", tt.Texts())
	}
}

func TestDNSAndHTTPSourcesShowDeviceNames(t *testing.T) {
	feed := newHTTPFeed(inspectorFrame())
	h, _ := newSourceHarness(t, 1800, 860, func(srv *testkit.Server) {
		srv.Route("GET", "/http/capabilities", testkit.JSON(enabled()))
		srv.Route("GET", "/http/stream", feed.ServeHTTP)
	})
	tt := h.tt

	// DNS Queries: the Source IP filter, the Source column and the inspector.
	h.w.view = viewDNS
	h.w.SetActive(true)
	testkit.UntilText(t, tt, "chatgpt.com")
	// One filter option and three rows from the TUN address.
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), "本机") == 4 })
	h.w.dns.selectQuery("2")
	testkit.UntilText(t, tt, "源 IP")
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), "本机") == 5 })

	// HTTP Requests: the Source filter and the inspector's Source Address;
	// the list has no source column.
	h.w.view = viewHTTP
	h.w.SetActive(true)
	testkit.UntilText(t, tt, "/json")
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), "本机") == 1 })
	h.w.http.selectRow("21")
	testkit.UntilText(t, tt, "172.18.0.1:5000")
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), "本机") == 2 })
}

// shot writes the frame as name under CONN_PREVIEW_DIR, the theme settled.
func shot(t *testing.T, tt *ui.Tester, name string, dark bool) {
	t.Helper()
	dir := os.Getenv("CONN_PREVIEW_DIR")
	if dir == "" {
		return
	}
	tt.SetDark(dark)
	tt.Advance(time.Second)
	tt.Frame()
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, tt.Image()); err != nil {
		t.Fatal(err)
	}
	tt.SetDark(false)
	tt.Advance(time.Second)
}

// TestSourceColumnScreens writes the acceptance frames of review item 18
// where CONN_PREVIEW_DIR says.
func TestSourceColumnScreens(t *testing.T) {
	if os.Getenv("CONN_PREVIEW_DIR") == "" {
		t.Skip("CONN_PREVIEW_DIR is not set")
	}
	feed := newHTTPFeed(inspectorFrame())
	h, _ := newSourceHarness(t, 1440, 820, func(srv *testkit.Server) {
		srv.Route("GET", "/http/capabilities", testkit.JSON(enabled()))
		srv.Route("GET", "/http/stream", feed.ServeHTTP)
	})
	tt := h.tt
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), "本机") == 4 })
	shot(t, tt, "18-tun-local-light.png", false)
	shot(t, tt, "18-tun-local-dark.png", true)
	// Wide enough for the Source Address column beside the inspector; the
	// row is chosen as a click does, the list keeping the focus.
	tt.SetSize(1760, 820)
	h.click(t, "ime.doubao.com")
	testkit.UntilText(t, tt, "关闭此连接")
	shot(t, tt, "18-connections-light.png", false)
	shot(t, tt, "18-connections-dark.png", true)
	h.click(t, "updates.example.com")
	testkit.Until(t, tt, func() bool { return countText(tt.Texts(), longDevice) == 3 })
	shot(t, tt, "18-connections-long-light.png", false)
	// The clipped name shows whole, with the address, in a hint.
	h.w.conn.selectedID = ""
	testkit.Until(t, tt, func() bool { return !tt.HasText("关闭此连接") })
	cell, ok := tt.Find("192.168.1.23:50123")
	if !ok {
		t.Fatalf("no source cell: %q", tt.Texts())
	}
	tt.Move(cell.X+4, cell.Y+cell.H/2)
	tt.Advance(1700 * time.Millisecond)
	if !tt.HasText("192.168.1.23:50123 · " + longDevice) {
		t.Errorf("no hint: %q", tt.Texts())
	}
	shot(t, tt, "18-connections-hint-light.png", false)
	tt.Move(1, 1)

	tt.SetSize(1920, 820)
	h.w.view = viewDNS
	h.w.SetActive(true)
	testkit.UntilText(t, tt, "chatgpt.com")
	h.click(t, "chatgpt.com.")
	testkit.UntilText(t, tt, "源 IP")
	testkit.UntilText(t, tt, "上游尝试")
	shot(t, tt, "18-dns-light.png", false)
	shot(t, tt, "18-dns-dark.png", true)

	tt.SetSize(1440, 820)
	h.w.view = viewHTTP
	h.w.SetActive(true)
	testkit.UntilText(t, tt, "/json")
	h.w.http.selectRow("21")
	testkit.UntilText(t, tt, "172.18.0.1:5000")
	shot(t, tt, "18-http-light.png", false)

	n, _ := newSourceHarness(t, 760, 820, nil)
	testkit.UntilText(t, n.tt, "172.18.0.2:61804")
	n.w.conn.selectedID = "c3"
	testkit.UntilText(t, n.tt, "关闭此连接")
	shot(t, n.tt, "18-narrow-light.png", false)
	shot(t, n.tt, "18-narrow-dark.png", true)
}
