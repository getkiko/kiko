package model_test

import (
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// A source belongs to the local server / gateway by the local networks, then to the first
// device listing it, else to the other devices.
func TestDeviceForSource(t *testing.T) {
	doc := model.EmptyDoc(model.PlatformLinux)
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Addresses: []string{"192.168.1.20", "10.0.0.0/24"}}}
	local := []string{"192.168.1.2"}
	for ip, want := range map[string]string{
		"127.0.0.1": model.LocalDeviceID, "192.168.1.2": model.LocalDeviceID, "192.168.1.20": "phone",
		"10.0.0.9": "phone", "192.168.1.30": model.OtherDeviceID, "": model.OtherDeviceID, "::1": model.LocalDeviceID,
	} {
		if got := model.DeviceForSource(doc, ip, local); got != want {
			t.Errorf("%q: %s, want %s", ip, got, want)
		}
	}
	if !model.AddressInNetwork("10.0.0.9", "10.0.0.0/24") || model.AddressInNetwork("10.0.1.9", "10.0.0.0/24") {
		t.Error("AddressInNetwork")
	}
}
