package routes

import (
	"os"
	"sync/atomic"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// TestF5Screenshots writes the acceptance frames of the device pictures,
// IP changed and the scan dialog when ROUTES_PREVIEW_DIR is set.
func TestF5Screenshots(t *testing.T) {
	if os.Getenv("ROUTES_PREVIEW_DIR") == "" {
		t.Skip("ROUTES_PREVIEW_DIR is not set")
	}
	f := scanFixture(t, api.ScanDone, new(atomic.Int32))
	doc := f.w.doc()
	custom := testImage(t, 7)
	f.w.update(func(doc *model.Doc) (*model.Doc, error) {
		next := model.Clone(doc)
		next.Route.Devices[0].ImageKey = "iphone"
		next.Route.Devices = append(next.Route.Devices,
			&model.RouteDevice{ID: "ipad", Name: "iPad", Icon: "phone", ImageKey: "ipad", Addresses: []string{"192.168.1.21/32"}, Final: "proxy"},
			&model.RouteDevice{ID: "mac", Name: "MacBook", Icon: "computer", ImageKey: "mac", Addresses: []string{"192.168.1.22/32"}, Final: "proxy"},
			&model.RouteDevice{ID: "nas", Name: "NAS", Icon: "server", Image: custom, Addresses: []string{"192.168.1.23/32"}, Final: "proxy"},
			&model.RouteDevice{ID: "gw", Name: "路由器", Icon: "router", ImageKey: "router", Addresses: []string{"192.168.1.1/32"}, Final: "direct"})
		return next, nil
	})
	_ = doc
	f.waitSaved(func(d *model.Doc) bool { return len(d.Route.Devices) == 5 })
	testkit.UntilText(t, f.tt, "MacBook")
	f.snapshot("f5-sidebar-light.png", false)
	f.snapshot("f5-sidebar-dark.png", true)
	f.click("扫描局域网")
	testkit.UntilText(t, f.tt, "Mia 的 iPad")
	f.snapshot("f5-scan-light.png", false)
	f.snapshot("f5-scan-dark.png", true)
	f.w.scan.open = false
	f.tt.Frame()
	f.w.editDevice(f.w.doc(), "nas")
	f.tt.Frame()
	f.snapshot("f5-device-image-light.png", false)
	f.snapshot("f5-device-image-dark.png", true)
}
