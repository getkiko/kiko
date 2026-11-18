package emit_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func tinyJPEG() string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3, 4})
}

// MAC, picture and pending addresses never change the emitted
// configuration, byte for byte.
func TestDeviceExtrasNotEmitted(t *testing.T) {
	plain := profileFixture()
	want := emit.Emit(plain, emit.Options{})
	if len(want.Errors) != 0 {
		t.Fatalf("errors %v", want.Errors)
	}
	extra := model.Clone(plain)
	for i, d := range extra.Route.Devices {
		d.MAC = []string{"3c:22:fb:00:11:22", "a2:00:00:00:00:01"}[i]
		d.Image = tinyJPEG()
		d.PendingAddresses = []string{"192.168.1.99/32"}
		d.PendingFrom = map[string]string{"192.168.1.99/32": "192.168.1.20/32"}
		d.ImageKey = "iphone"
	}
	got := emit.Emit(extra, emit.Options{})
	if string(got.JSON) != string(want.JSON) {
		t.Errorf("device extras changed the configuration:\n%s\n%s", want.JSON, got.JSON)
	}
	for _, s := range []string{"3c:22:fb", "a2:00:00", "data:image", "192.168.1.99", "imageKey"} {
		if strings.Contains(string(got.JSON), s) {
			t.Errorf("%q leaked into the configuration", s)
		}
	}
	if string(json.RawMessage(got.JSON)) != string(want.JSON) || len(got.Warnings) != len(want.Warnings) {
		t.Error("warnings or json differ")
	}
}

// A document saved before the fields existed loads and emits as before.
func TestDeviceExtrasMigration(t *testing.T) {
	plain := profileFixture()
	raw, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"mac"`, `"image"`, `"pendingAddresses"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("legacy document encodes %s", key)
		}
	}
	loaded := model.DecodeDoc(raw)
	if loaded == nil {
		t.Fatal("decode failed")
	}
	if a, b := emit.Emit(plain, emit.Options{}), emit.Emit(loaded, emit.Options{}); string(a.JSON) != string(b.JSON) {
		t.Errorf("upgrade changed the configuration:\n%s\n%s", a.JSON, b.JSON)
	}
}
