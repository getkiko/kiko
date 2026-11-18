package usage

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestSourceFilterShowsDeviceNames(t *testing.T) {
	srv := testkit.NewServer(t)
	doc := model.EmptyDoc("linux")
	doc.Route.Devices = []*model.RouteDevice{
		{ID: "d1", Name: "客厅 Apple TV", Addresses: []string{"10.0.0.2", "10.0.0.9"}},
		{ID: "d2", Name: "一台名字特别特别特别特别特别特别特别特别长的设备", Addresses: []string{"10.0.0.4"}},
	}
	raw, _ := json.Marshal(doc)
	srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
	srv.Route("GET", "/usage", func(w http.ResponseWriter, r *http.Request) {
		resp := api.UsageResponse{From: 1, To: 2, Limit: 50, Connected: true, Facets: map[string][]api.UsageFacet{
			"source": {
				{Value: "10.0.0.2", UsageCounters: counters(1, 2048)},
				{Value: "10.0.0.3", UsageCounters: counters(1, 1024)},
				{Value: "10.0.0.4", UsageCounters: counters(1, 512)},
				{Value: "192.168.1.5", UsageCounters: counters(1, 256)},
			},
		}}
		testkit.JSON(resp)(w, r)
	})
	e, _ := testkit.NewEnv(srv)
	s := store.New(e)
	w := New(e, s)
	w.SetActive(true)
	t.Cleanup(func() { w.SetActive(false) })
	s.Load()
	tt := ui.NewTester(func(c *ui.Context) {
		kit.Apply(c)
		e.Drain(c)
		c.Root().Background(kit.T(c).Surface)
		w.View(c)
	}, 1320, 760)
	testkit.Until(t, tt, func() bool { return s.Loaded && tt.HasText("10.0.0.3") })
	s.LocalAddresses = []string{"192.168.1.5/32"}
	for _, text := range []string{"10.0.0.2", "客厅 Apple TV", "10.0.0.3", "本机"} {
		testkit.UntilText(t, tt, text)
	}
	// The filter value stays the IP.
	if err := tt.Click("10.0.0.2 客厅 Apple TV"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	if !w.filters.Has("source", "10.0.0.2") {
		t.Errorf("filters %v", w.filters)
	}
	// A rename shows at once.
	if err := s.Update(func(d *model.Doc) { d.Route.Devices[0].Name = "卧室" }); err != nil {
		t.Fatal(err)
	}
	testkit.UntilText(t, tt, "卧室")
	for _, text := range tt.Texts() {
		if strings.Contains(text, "客厅") {
			t.Errorf("old name still shown: %q", text)
		}
	}
}
