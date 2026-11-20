package connections

import (
	"image/png"
	"net/http"
	"os"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// A connection whose outbound dials through a detour shows the whole path,
// group first, in its row and in the inspector, with each hop's role.
func TestConnectionShowsDetourPath(t *testing.T) {
	h, _ := newSourceHarness(t, 1440, 820, func(srv *testkit.Server) {
		srv.Route("GET", "/config/applied", func(w http.ResponseWriter, r *http.Request) {
			testkit.JSON(map[string]any{"outbounds": []any{
				map[string]any{"tag": "马来西亚服务器", "type": "vless", "detour": "香港 01"},
				map[string]any{"tag": "香港 01", "type": "shadowsocks"},
			}})(w, r)
		})
	})
	h.conns.mu.Lock()
	h.conns.rows[0].Outbound = "马来西亚服务器"
	h.conns.rows[0].ChainList = []string{"马来西亚服务器", "自选"}
	h.conns.mu.Unlock()
	running := api.Runtime{State: "running", DesiredRunning: true, StartedAt: "t1"}
	h.srv.SetRuntime(running)
	h.srv.Emit("runtime", running)
	tt := h.tt
	testkit.UntilText(t, tt, "自选 → 马来西亚服务器 → 香港 01")

	h.click(t, "mtalk.google.com")
	testkit.UntilText(t, tt, "自选（策略组） → 马来西亚服务器 → 香港 01（前置代理）")
	if dir := os.Getenv("CONSOLE_PREVIEW_DIR"); dir != "" {
		f, err := os.Create(dir + "/connections-detour.png")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_ = png.Encode(f, tt.Image())
	}
}
