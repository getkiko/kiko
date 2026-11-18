package connections

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestSourceFilterShowsDeviceNames(t *testing.T) {
	srv := testkit.NewServer(t)
	conns := newFakeConnections(fixtureRows(time.Now()))
	srv.Route("GET", "/connections", conns.ServeHTTP)
	srv.Route("GET", "/dns/capabilities", testkit.JSON(api.DNSCapabilities{Supported: true, Enabled: true, SchemaVersion: 1}))
	srv.Route("GET", "/dns/stream", ndjson(fixtureDNS()))
	doc := model.EmptyDoc("linux")
	doc.Route.Devices = []*model.RouteDevice{{ID: "d1", Name: "客厅电视", Addresses: []string{"172.18.0.2", "172.18.0.8"}}}
	raw, _ := json.Marshal(doc)
	srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	e, _ := testkit.NewEnv(srv)
	s := store.New(e)
	w := New(e, s)
	tt := ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		e.Drain(c)
		w.View(c)
	}, 1440, 820)
	w.SetActive(true)
	t.Cleanup(func() { w.SetActive(false) })
	s.Load()
	testkit.Until(t, tt, func() bool { return s.Loaded })
	s.LocalAddresses = []string{"172.18.0.1/32"}
	for _, text := range []string{"172.18.0.1", "本机", "172.18.0.2", "客厅电视"} {
		testkit.UntilText(t, tt, text)
	}
	// The filter value stays the IP.
	if err := tt.Click("172.18.0.2 客厅电视"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	if !w.conn.filter.Has(facetSource, "172.18.0.2") {
		t.Errorf("filter %v", w.conn.filter)
	}
	// A rename shows at once.
	if err := s.Update(func(d *model.Doc) { d.Route.Devices[0].Name = "卧室电视" }); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, tt, "卧室电视")
	if tt.HasText("客厅电视") {
		t.Errorf("old name still shown: %q", tt.Texts())
	}
	if err := tt.Click("清除"); err != nil {
		t.Fatal(err)
	}
	if len(w.conn.filter) != 0 {
		t.Errorf("filter not cleared: %v", w.conn.filter)
	}
}
