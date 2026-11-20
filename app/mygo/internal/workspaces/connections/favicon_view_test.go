package connections

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/favicon"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// With an icon outbound set, a connection to a domain shows its site's
// icon, asked of the server by the root domain through the outbound's
// tag; without one, nothing is asked.
func TestConnectionsShowSiteIcons(t *testing.T) {
	for _, on := range []bool{true, false} {
		srv := testkit.NewServer(t)
		doc := model.EmptyDoc(model.PlatformLinux)
		out := &model.Outbound{Entity: model.Entity{ID: "out_hk", Tag: "香港 01", Enabled: true}, Kind: "outbound", Type: "direct", Fields: model.Fields{}}
		doc.Outbounds = append(doc.Outbounds, out)
		if on {
			doc.FaviconOutbound = model.Some("out_hk")
		}
		raw, _ := json.Marshal(doc)
		srv.SetStore(api.ServerStore{Revision: 1, Doc: raw, Profiles: json.RawMessage("[]")})
		conns := newFakeConnections(fixtureRows(time.Now()))
		srv.Route("GET", "/connections", conns.ServeHTTP)
		srv.Route("GET", "/dns/capabilities", testkit.JSON(api.DNSCapabilities{Supported: true, Enabled: true, SchemaVersion: 1}))
		srv.Route("GET", "/dns/stream", ndjson(fixtureDNS()))
		var mu sync.Mutex
		var asked []string
		icon := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for i := 0; i < len(icon.Pix); i += 4 {
			icon.Pix[i], icon.Pix[i+1], icon.Pix[i+2], icon.Pix[i+3] = 66, 133, 244, 255
		}
		var pngBytes bytes.Buffer
		_ = png.Encode(&pngBytes, icon)
		srv.Route("GET", "/favicon", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asked = append(asked, r.URL.Query().Get("domain")+"|"+r.URL.Query().Get("outbound"))
			mu.Unlock()
			if r.URL.Query().Get("domain") != "google.com" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes.Bytes())
		})
		running := api.Runtime{State: "running", DesiredRunning: true}
		srv.SetRuntime(running)
		e, _ := testkit.NewEnv(srv)
		e.Server.Start()
		t.Cleanup(e.Server.Stop)
		st := store.New(e)
		st.Load()
		w := New(e, st)
		tt := ui.NewTester(func(c *ui.Context) {
			kit.Apply(c)
			e.Drain(c)
			w.View(c)
		}, 1440, 820)
		w.SetActive(true)
		srv.Emit("runtime", running)
		testkit.Until(t, tt, func() bool { return st.Loaded && e.Server.Runtime.Running() })
		w.conn.setMode(groupNone)
		testkit.UntilText(t, tt, "mtalk.google.com")
		if !on {
			tt.Advance(500 * time.Millisecond)
			mu.Lock()
			n := len(asked)
			mu.Unlock()
			if n != 0 {
				t.Fatalf("icons asked while off: %v", asked)
			}
			w.SetActive(false)
			continue
		}
		testkit.Until(t, tt, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return strings.Contains(strings.Join(asked, " "), "google.com|香港 01")
		})
		mu.Lock()
		seen := append([]string(nil), asked...)
		mu.Unlock()
		for _, a := range seen {
			if strings.HasPrefix(a, "8.8.8.8") || strings.Contains(a, "mtalk.") {
				t.Errorf("asked %q: IPs are not asked, domains by their root", a)
			}
		}
		// The icon arrives and shows.
		testkit.Until(t, tt, func() bool {
			return favicon.For(e).Bitmap("mtalk.google.com", st.Doc(), st.Version()) != nil
		})
		tt.Advance(300 * time.Millisecond)
		if dir := os.Getenv("CONN_PREVIEW_DIR"); dir != "" {
			f, err := os.Create(dir + "/connections-favicons.png")
			if err == nil {
				_ = png.Encode(f, tt.Image())
				f.Close()
			}
		}
		w.SetActive(false)
	}
}
