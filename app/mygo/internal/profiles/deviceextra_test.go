package profiles

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Backups, exports and the public share are the whole document: device
// MAC, picture and pending addresses go through SerializeBundle and
// ParseImport unchanged.
func TestBundleCarriesDeviceExtras(t *testing.T) {
	doc := mustImport(t, `{"inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890}],
		"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "direct"}}`, "linux")
	image := "data:image/jpeg;base64,/9j/4AAQ"
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Icon: "phone", Addresses: []string{"192.168.1.20/32"}, Final: "direct",
		MAC: "3c:22:fb:00:11:22", Image: image, PendingAddresses: []string{"192.168.1.77/32"}}}
	profile := &model.Profile{ID: "p1", Name: "Main", SavedAt: 1, Doc: doc}
	for _, all := range []bool{false, true} {
		data, err := SerializeBundle([]*model.Profile{profile}, nil, all)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), image) {
			t.Fatal("picture missing from the bundle")
		}
		list, _, err := ParseImport(data, "linux")
		if err != nil || len(list) != 1 {
			t.Fatalf("import %v", err)
		}
		d := list[0].Doc.Route.Devices[0]
		if d.MAC != "3c:22:fb:00:11:22" || d.Image != image || len(d.PendingAddresses) != 1 {
			t.Errorf("imported device %+v", d)
		}
	}
}

// An import whose pictures exceed the total quota is refused with a
// readable error instead of silently losing pictures.
func TestImportRefusesImagesOverQuota(t *testing.T) {
	doc := mustImport(t, `{"inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890}],
		"outbounds": [{"type": "direct", "tag": "direct"}], "route": {"final": "direct"}}`, "linux")
	raw := make([]byte, model.DeviceImageMaxBytes)
	raw[0], raw[1], raw[2] = 0xFF, 0xD8, 0xFF
	image := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw)
	for i := 0; i < model.DeviceImagesMaxBytes/model.DeviceImageMaxBytes+1; i++ {
		doc.Route.Devices = append(doc.Route.Devices, &model.RouteDevice{ID: model.NewID("d"), Name: "d", Addresses: []string{"10.0.0.1/32"}, Final: "direct", Image: image})
	}
	data, err := SerializeBundle([]*model.Profile{{ID: "p1", Name: "Main", SavedAt: 1, Doc: doc}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseImport(data, "linux"); !errors.Is(err, model.ErrDeviceImageQuota) || !strings.Contains(err.Error(), "Main") {
		t.Fatalf("import: %v", err)
	}
}
