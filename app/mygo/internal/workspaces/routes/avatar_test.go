package routes

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/deviceimg"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// testPNG is a noisy picture, so its compressed size is not tiny.
func testPNG(t *testing.T, seed int64) []byte {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testImage(t *testing.T, seed int64) string {
	t.Helper()
	s, err := deviceimg.Compress(testPNG(t, seed))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAvatarPriority(t *testing.T) {
	f := newFixture(t, nil)
	w := f.w
	custom := &model.RouteDevice{ID: "a", Icon: "computer", Image: testImage(t, 1)}
	builtin := &model.RouteDevice{ID: "b", Icon: "computer", ImageKey: "pc"}
	plain := &model.RouteDevice{ID: "c", Icon: "computer"}
	broken := &model.RouteDevice{ID: "d", Icon: "router", ImageKey: "router", Image: "data:image/jpeg;base64,AAAA"}
	own := w.avatarBitmap(false, custom)
	pc := w.avatarBitmap(false, builtin)
	if own == nil || pc == nil || own == pc {
		t.Fatalf("custom %v builtin %v", own, pc)
	}
	if w.avatarBitmap(false, custom) != own || w.avatarBitmap(true, builtin) == nil {
		t.Fatal("pictures are not cached or lack a dark variant")
	}
	if w.avatarBitmap(false, plain) != nil || w.avatarBitmap(false, nil) != nil {
		t.Fatal("without a key and for fixed scopes the type icon shows")
	}
	// An undecodable image falls back to the built-in one.
	if got := w.avatarBitmap(false, broken); got == nil || got == own {
		t.Fatalf("broken image: %v", got)
	}
	// Frames render the pictures without error.
	f.w.update(func(doc *model.Doc) (*model.Doc, error) {
		next := model.Clone(doc)
		next.Route.Devices[0].Image = custom.Image
		return next, nil
	})
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Image != "" })
	f.tt.Frame()
	if len(w.avatars) == 0 {
		t.Fatal("sidebar did not decode the picture")
	}
}

func TestChooseAndRemoveImage(t *testing.T) {
	f := newFixture(t, nil)
	shell := f.env.Shell.(*testkit.Shell)
	shell.Open = testPNG(t, 2)
	shell.OpenName = "me.png"
	f.w.chooseImage("phone")
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Image != "" })
	if err := model.ValidDeviceImage(f.saved().Route.Devices[0].Image); err != nil {
		t.Fatal(err)
	}
	// "Remove image" asks beside the More button; cancelling keeps the picture.
	f.tt.Frame()
	f.w.requestRemoveImage("device:phone", "phone")
	testkit.UntilText(t, f.tt, "移除「iPhone」的图片？")
	f.click("取消")
	testkit.Until(t, f.tt, func() bool { return !f.w.imageRemove.state.Open })
	if f.saved().Route.Devices[0].Image == "" {
		t.Fatal("cancel removed the picture")
	}
	f.w.requestRemoveImage("device:phone", "phone")
	testkit.UntilText(t, f.tt, "移除「iPhone」的图片？")
	f.click("移除")
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Image == "" })
	// A file that is no picture is refused with a short message.
	shell.Open = []byte("not an image")
	f.w.chooseImage("phone")
	testkit.Until(t, f.tt, func() bool { return f.w.err != "" })
	if f.saved().Route.Devices[0].Image != "" || len(f.w.err) > 40 {
		t.Fatalf("err %q", f.w.err)
	}
	// A cancelled dialog changes nothing.
	f.w.err = ""
	shell.Open = nil
	f.w.chooseImage("phone")
	f.tt.Frame()
	testkit.Until(t, f.tt, func() bool { return !f.w.picking })
	if f.w.err != "" {
		t.Fatalf("err %q", f.w.err)
	}
}

func TestImageQuotaRefused(t *testing.T) {
	doc := fixtureDoc()
	// Pictures that leave less than 1 KB of the quota (they only need the
	// image header to be valid).
	fake := func(size int) string {
		raw := make([]byte, size)
		raw[0], raw[1], raw[2] = 0xFF, 0xD8, 0xFF
		return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw)
	}
	full := model.DeviceImagesMaxBytes / model.DeviceImageMaxBytes
	for i := 0; i < full; i++ {
		doc.Route.Devices = append(doc.Route.Devices, &model.RouteDevice{ID: model.NewID("x"), Name: "x", Icon: "phone",
			Addresses: []string{}, Final: "proxy", Image: fake(model.DeviceImageMaxBytes)})
	}
	f := newFixtureDoc(t, doc, nil)
	shell := f.env.Shell.(*testkit.Shell)
	shell.Open = testPNG(t, 4)
	f.w.chooseImage("phone")
	testkit.Until(t, f.tt, func() bool { return f.w.err != "" })
	if !strings.Contains(f.w.err, "上限") || findDevice(f.saved(), "phone").Image != "" {
		t.Fatalf("err %q", f.w.err)
	}
}

func TestDeviceDialogImage(t *testing.T) {
	f := newFixture(t, nil)
	shell := f.env.Shell.(*testkit.Shell)
	shell.Open = testPNG(t, 5)
	f.w.editDevice(f.w.doc(), "phone")
	f.tt.Frame()
	f.click("选择图片")
	testkit.Until(t, f.tt, func() bool { return f.w.device.draft.Image != "" })
	f.tt.Frame()
	if !f.tt.HasText("更换") || !f.tt.HasText("移除") {
		t.Fatalf("texts %q", f.tt.Texts())
	}
	// The picture is part of the draft until "Save device".
	if findDevice(f.saved(), "phone").Image != "" {
		t.Fatal("saved early")
	}
	f.w.saveDevice(f.w.doc(), f.w.device)
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Image != "" })
	f.w.editDevice(f.w.doc(), "phone")
	f.tt.Frame()
	f.click("移除")
	f.w.saveDevice(f.w.doc(), f.w.device)
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].Image == "" })
}

func pendingDoc() *model.Doc {
	doc := fixtureDoc()
	d := doc.Route.Devices[0]
	d.MAC = "00:11:22:33:44:55"
	d.PendingAddresses = []string{"192.168.1.55/32"}
	d.PendingFrom = map[string]string{"192.168.1.55/32": "192.168.1.20/32"}
	return doc
}

func TestPendingAddressesConfirmAndDismiss(t *testing.T) {
	f := newFixtureDoc(t, pendingDoc(), nil)
	testkit.UntilText(t, f.tt, "iPhone")
	f.click("IP 已变更：192.168.1.20 → 192.168.1.55")
	f.w.confirmPending("phone")
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].PendingAddresses) == 0 })
	if got := f.saved().Route.Devices[0].Addresses; len(got) != 1 || got[0] != "192.168.1.55/32" {
		t.Fatalf("addresses %v", got)
	}
	f.tt.Frame()
	if f.tt.HasText("IP 已变更") {
		t.Fatal("marker stays")
	}

	g := newFixtureDoc(t, pendingDoc(), nil)
	g.w.dismissPending("phone")
	g.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.Devices[0].PendingAddresses) == 0 })
	if got := g.saved().Route.Devices[0].Addresses; got[0] != "192.168.1.20/32" {
		t.Fatalf("addresses %v", got)
	}
}

func TestDeviceDialogShowsMAC(t *testing.T) {
	f := newFixtureDoc(t, pendingDoc(), nil)
	f.w.editDevice(f.w.doc(), "phone")
	f.tt.Frame()
	if !f.tt.HasText("00:11:22:33:44:55") {
		t.Fatalf("texts %q", f.tt.Texts())
	}
	f.w.saveDevice(f.w.doc(), f.w.device)
	f.tt.Frame()
	f.waitSaved(func(doc *model.Doc) bool { return !f.store.Saving() })
	d := f.saved().Route.Devices[0]
	if d.MAC != "00:11:22:33:44:55" || len(d.PendingAddresses) != 1 {
		t.Fatalf("device %+v", d)
	}
}

// A full cache drops the least recently used picture, not everything.
func TestAvatarCacheEvictsLeastRecentlyUsed(t *testing.T) {
	f := newFixture(t, nil)
	w := f.w
	w.avatarPut("first", nil)
	for i := 1; i < avatarCacheMax; i++ {
		w.avatarPut(fmt.Sprintf("k%d", i), nil)
	}
	if len(w.avatars) != avatarCacheMax {
		t.Fatalf("size %d", len(w.avatars))
	}
	// Using "first" makes k1 the oldest.
	if _, ok := w.avatarGet("first"); !ok {
		t.Fatal("first missing")
	}
	w.avatarPut("extra", nil)
	if len(w.avatars) != avatarCacheMax {
		t.Fatalf("size after eviction %d", len(w.avatars))
	}
	if _, ok := w.avatars["k1"]; ok {
		t.Error("least recently used picture kept")
	}
	for _, key := range []string{"first", "k2", "extra"} {
		if _, ok := w.avatars[key]; !ok {
			t.Errorf("%s evicted", key)
		}
	}
}

func TestPendingTextPairs(t *testing.T) {
	d := &model.RouteDevice{Addresses: []string{"192.168.1.8/32", "192.168.1.9/32"}, PendingAddresses: []string{"192.168.1.50/32"}}
	if got := pendingText(d); got != "IP 已变更：新增 192.168.1.50" {
		t.Errorf("unpaired: %q", got)
	}
	d.PendingFrom = map[string]string{"192.168.1.50/32": "192.168.1.8/32"}
	if got := pendingText(d); got != "IP 已变更：192.168.1.8 → 192.168.1.50" {
		t.Errorf("paired: %q", got)
	}
}

// The picture cache is keyed by a digest, not the data URL, and the
// digest is computed again only when the picture changes.
func TestAvatarKeyIsDigest(t *testing.T) {
	w := &Workspace{}
	d := &model.RouteDevice{ID: "a", Image: "data:image/jpeg;base64," + strings.Repeat("A", 20000)}
	k1 := w.customAvatarKey(d)
	if len(k1) != 2+32 || strings.Contains(k1, "base64") {
		t.Fatalf("key %q", k1)
	}
	if w.customAvatarKey(d) != k1 {
		t.Error("key not stable")
	}
	other := &model.RouteDevice{ID: "a", Image: "data:image/jpeg;base64," + strings.Repeat("B", 20000)}
	if w.customAvatarKey(other) == k1 {
		t.Error("different picture, same key")
	}
	same := &model.RouteDevice{ID: "b", Image: d.Image}
	if w.customAvatarKey(same) != k1 {
		t.Error("same picture, different key")
	}
}
