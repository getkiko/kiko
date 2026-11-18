package model_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func jpegOf(size int) string {
	raw := make([]byte, size)
	raw[0], raw[1], raw[2] = 0xFF, 0xD8, 0xFF
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(raw)
}

func extraDoc(t *testing.T) *model.Doc {
	t.Helper()
	doc := profileDoc(t)
	device(doc, "phone").MAC = "3c:22:fb:00:11:22"
	return doc
}

func TestNormalizeDeviceMAC(t *testing.T) {
	good := map[string]string{
		"3C:22:FB:00:11:22":   "3c:22:fb:00:11:22",
		"3c-22-fb-00-11-22":   "3c:22:fb:00:11:22",
		"3c22.fb00.1122":      "3c:22:fb:00:11:22",
		"3c22fb001122":        "3c:22:fb:00:11:22",
		" 3c:22:fb:00:11:22 ": "3c:22:fb:00:11:22",
	}
	for in, want := range good {
		if got, ok := model.NormalizeDeviceMAC(in); !ok || got != want {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "3c:22:fb:00:11", "3c:22:fb:00:11:2", "3c:22:fb:00:11:zz", "3c:22-fb:00:11:22", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "3c:22:fb:00:11:22:33"} {
		if got, ok := model.NormalizeDeviceMAC(in); ok {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestIsRandomMAC(t *testing.T) {
	for mac, want := range map[string]bool{
		"02:00:00:00:00:01": true, "a6:00:00:00:00:01": true, "AA:00:00:00:00:01": true, "de:ad:be:ef:00:01": true,
		"3c:22:fb:00:11:22": false, "00:11:22:33:44:55": false, "b4:00:00:00:00:01": false, "garbage": false,
	} {
		if model.IsRandomMAC(mac) != want {
			t.Errorf("%s: want %v", mac, want)
		}
	}
}

func TestDeviceImageLimits(t *testing.T) {
	doc := extraDoc(t)
	if err := model.CheckDeviceImage(doc, "phone", jpegOf(model.DeviceImageMaxBytes)); err != nil {
		t.Errorf("max size rejected: %v", err)
	}
	if err := model.CheckDeviceImage(doc, "phone", jpegOf(model.DeviceImageMaxBytes+1)); !errors.Is(err, model.ErrDeviceImageSize) {
		t.Errorf("oversize: %v", err)
	}
	for _, bad := range []string{"data:image/gif;base64,R0lGODlh", "data:image/jpeg;base64,!!!", "http://x/y.png", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a png at all"))} {
		if err := model.CheckDeviceImage(doc, "phone", bad); !errors.Is(err, model.ErrDeviceImageFormat) {
			t.Errorf("%.30s: %v", bad, err)
		}
	}
	if model.CheckDeviceImage(doc, "phone", "") != nil {
		t.Error("clearing rejected")
	}
	// Webp and png with their signatures pass.
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), 1, 2, 3)
	if err := model.ValidDeviceImage("data:image/webp;base64," + base64.StdEncoding.EncodeToString(webp)); err != nil {
		t.Error(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3)
	if err := model.ValidDeviceImage("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)); err != nil {
		t.Error(err)
	}
}

func TestDeviceImageQuota(t *testing.T) {
	doc := extraDoc(t)
	count := model.DeviceImagesMaxBytes / model.DeviceImageMaxBytes // 21
	doc.Route.Devices = nil
	for i := 0; i < count; i++ {
		doc.Route.Devices = append(doc.Route.Devices, &model.RouteDevice{ID: model.NewID("d"), Name: "d", Addresses: []string{"10.0.0.1/32"}, Final: "proxy", Image: jpegOf(model.DeviceImageMaxBytes)})
	}
	if got := model.DeviceImageBytes(doc); got != count*model.DeviceImageMaxBytes {
		t.Fatalf("bytes %d", got)
	}
	// A new device does not fit; replacing an existing picture does.
	if err := model.CheckDeviceImage(doc, "new", jpegOf(9000)); !errors.Is(err, model.ErrDeviceImageQuota) {
		t.Errorf("quota: %v", err)
	}
	if err := model.CheckDeviceImage(doc, doc.Route.Devices[0].ID, jpegOf(model.DeviceImageMaxBytes)); err != nil {
		t.Errorf("replace: %v", err)
	}
	if err := model.CheckDeviceImage(doc, "new", ""); err != nil {
		t.Error(err)
	}
}

func TestSanitizeDeviceExtras(t *testing.T) {
	doc := extraDoc(t)
	d := device(doc, "phone")
	d.MAC = "3C-22-FB-00-11-22"
	d.Image = "data:image/gif;base64,AAAA"
	d.PendingAddresses = []string{"192.168.1.50/32", "192.168.1.50/32", "bad", "192.168.1.20/32"}
	// FillDefaults applies it.
	loaded := model.DecodeDoc(mustJSON(t, doc))
	got := device(loaded, "phone")
	if got.MAC != "3c:22:fb:00:11:22" || got.Image != "" || strings.Join(got.PendingAddresses, ",") != "192.168.1.50/32" {
		t.Errorf("sanitized %+v", got)
	}
	// Clean documents are returned as is.
	clean := model.FillDefaults(extraDoc(t))
	if model.SanitizeDeviceExtras(clean) != clean {
		t.Error("clean document copied")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The fields survive every document-wide copy: JSON, Clone, decode.
func TestDeviceExtrasRoundTrip(t *testing.T) {
	doc := extraDoc(t)
	d := device(doc, "phone")
	d.Image = jpegOf(100)
	d.PendingAddresses = []string{"192.168.1.50/32"}
	for name, got := range map[string]*model.Doc{"clone": model.Clone(doc), "decode": model.DecodeDoc(mustJSON(t, doc)), "fill": model.FillDefaults(doc)} {
		g := device(got, "phone")
		if g == nil || g.MAC != d.MAC || g.Image != d.Image || strings.Join(g.PendingAddresses, ",") != "192.168.1.50/32" {
			t.Errorf("%s lost fields: %+v", name, g)
		}
		if o := device(got, "tv"); o.MAC != "" || o.Image != "" || o.PendingAddresses != nil {
			t.Errorf("%s: other device gained fields", name)
		}
	}
	if s := string(mustJSON(t, device(profileDoc(t), "tv"))); strings.Contains(s, "mac") || strings.Contains(s, "image") || strings.Contains(s, "pending") {
		t.Errorf("empty fields encoded: %s", s)
	}
}

func TestDeleteDeviceDropsImage(t *testing.T) {
	doc := extraDoc(t)
	device(doc, "phone").Image = jpegOf(2000)
	if model.DeviceImageBytes(doc) == 0 {
		t.Fatal("no image bytes")
	}
	next := model.Clone(doc)
	next.Route.Devices = next.Route.Devices[1:]
	if model.DeviceImageBytes(next) != 0 || strings.Contains(string(mustJSON(t, next)), "data:image") {
		t.Error("image outlived its device")
	}
}

func TestMergeScanned(t *testing.T) {
	doc := extraDoc(t) // phone 192.168.1.20/32 with MAC, tv 192.168.1.30/32
	scan := []model.ScannedHost{
		{IP: "192.168.1.20", MAC: "3C:22:FB:00:11:22"}, // known
		{IP: "192.168.1.77", MAC: "3c:22:fb:00:11:22"}, // phone moved
		{IP: "192.168.1.30", MAC: "a2:11:22:33:44:55"}, // tv known, random MAC not learned
		{IP: "192.168.1.99", MAC: "b8:00:00:00:00:01"}, // new
		{IP: "192.168.1.98"},                           // new, no MAC
	}
	next, res := model.MergeScanned(doc, scan)
	want := []string{model.ScanKnown, model.ScanMoved, model.ScanKnown, model.ScanNew, model.ScanNew}
	for i, w := range want {
		if res[i].State != w {
			t.Errorf("host %d: %s, want %s", i, res[i].State, w)
		}
	}
	if res[1].DeviceID != "phone" || res[3].DeviceID != "" {
		t.Errorf("device ids %+v", res)
	}
	phone := device(next, "phone")
	if strings.Join(phone.Addresses, ",") != "192.168.1.20/32" || strings.Join(phone.PendingAddresses, ",") != "192.168.1.77/32" {
		t.Errorf("phone %+v", phone)
	}
	if device(next, "tv").MAC != "" {
		t.Error("random MAC learned")
	}
	if device(doc, "phone").PendingAddresses != nil {
		t.Error("input document mutated")
	}
	// A stable MAC is learned by IP; repeating the scan changes nothing.
	again, _ := model.MergeScanned(next, []model.ScannedHost{{IP: "192.168.1.30", MAC: "b8:aa:bb:cc:dd:ee"}, {IP: "192.168.1.77", MAC: "3c:22:fb:00:11:22"}})
	if device(again, "tv").MAC != "b8:aa:bb:cc:dd:ee" || len(device(again, "phone").PendingAddresses) != 1 {
		t.Errorf("learn/repeat: %+v %+v", device(again, "tv"), device(again, "phone"))
	}
	if same, _ := model.MergeScanned(doc, []model.ScannedHost{{IP: "192.168.1.20", MAC: "3c:22:fb:00:11:22"}}); same != doc {
		t.Error("no-op scan copied the document")
	}
	if model.DeviceForScanned(doc, model.ScannedHost{IP: "192.168.1.55", MAC: "3c:22:fb:00:11:22"}).ID != "phone" || model.DeviceForScanned(doc, model.ScannedHost{IP: "192.168.1.55"}) != nil {
		t.Error("DeviceForScanned")
	}
	if model.MatchDeviceByMAC(doc, "3C-22-FB-00-11-22").ID != "phone" || model.MatchDeviceByMAC(doc, "") != nil {
		t.Error("MatchDeviceByMAC")
	}
}

func TestConfirmPendingAddresses(t *testing.T) {
	doc := extraDoc(t)
	rulesBefore := mustJSON(t, doc.Route.Rules)
	moved, _ := model.MergeScanned(doc, []model.ScannedHost{{IP: "192.168.1.77", MAC: "3c:22:fb:00:11:22"}})
	done, err := model.ConfirmPendingAddresses(moved, "phone")
	if err != nil {
		t.Fatal(err)
	}
	phone := device(done, "phone")
	if strings.Join(phone.Addresses, ",") != "192.168.1.77/32" || phone.PendingAddresses != nil {
		t.Errorf("phone %+v", phone)
	}
	if string(mustJSON(t, done.Route.Rules)) != string(rulesBefore) || string(mustJSON(t, done.DNS.Rules)) != string(mustJSON(t, doc.DNS.Rules)) {
		t.Error("rules changed")
	}
	if device(moved, "phone").PendingAddresses == nil {
		t.Error("input mutated")
	}
	if _, err := model.ConfirmPendingAddresses(done, "phone"); !errors.Is(err, model.ErrNoPending) {
		t.Errorf("no pending: %v", err)
	}
	// An address another device uses cannot be confirmed.
	clash := model.Clone(moved)
	device(clash, "phone").PendingAddresses = []string{"192.168.1.30/32"}
	if _, err := model.ConfirmPendingAddresses(clash, "phone"); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Errorf("clash: %v", err)
	}
	// Wider networks stay, and a profile's scope is untouched.
	wide := model.Clone(moved)
	device(wide, "phone").Addresses = []string{"192.168.1.20/32", "10.1.0.0/16"}
	got, err := model.ConfirmPendingAddresses(wide, "phone")
	if err != nil || strings.Join(device(got, "phone").Addresses, ",") != "10.1.0.0/16,192.168.1.77/32" {
		t.Errorf("wide: %v %+v", err, device(got, "phone"))
	}
	dismissed, err := model.DismissPending(moved, "phone")
	if err != nil || device(dismissed, "phone").PendingAddresses != nil || strings.Join(device(dismissed, "phone").Addresses, ",") != "192.168.1.20/32" {
		t.Errorf("dismiss: %v", err)
	}
	if _, err := model.ConfirmPendingAddresses(moved, "nope"); err == nil {
		t.Error("unknown device")
	}
}

func TestSanitizeDropsUnknownImageKey(t *testing.T) {
	doc := extraDoc(t)
	device(doc, "phone").ImageKey = "iphone"
	if got := model.SanitizeDeviceExtras(doc); got != doc {
		t.Fatal("a valid key changed the document")
	}
	device(doc, "phone").ImageKey = "toaster"
	if got := model.SanitizeDeviceExtras(doc); device(got, "phone").ImageKey != "" || device(doc, "phone").ImageKey == "" {
		t.Fatal("unknown key kept or document mutated")
	}
}

func TestMergeScannedRangeDeviceLearnsNothing(t *testing.T) {
	doc := extraDoc(t)
	device(doc, "tv").Addresses = []string{"192.168.1.0/24"}
	device(doc, "phone").Addresses = []string{"192.168.1.20/32"}
	// The range holds 192.168.1.40; its host is known but teaches no MAC.
	next, res := model.MergeScanned(doc, []model.ScannedHost{{IP: "192.168.1.40", MAC: "b8:aa:bb:cc:dd:ee"}})
	if res[0].State != model.ScanKnown || res[0].DeviceID != "tv" {
		t.Errorf("range host: %+v", res[0])
	}
	if next != doc || device(next, "tv").MAC != "" {
		t.Error("range device learned a MAC")
	}
}

func TestMergeScannedMACConflict(t *testing.T) {
	doc := extraDoc(t) // phone 192.168.1.20/32 holds 3c:22:fb:00:11:22
	host := model.ScannedHost{IP: "192.168.1.20", MAC: "b8:aa:bb:cc:dd:ee"}
	next, res := model.MergeScanned(doc, []model.ScannedHost{host})
	if res[0].State != model.ScanConflict || res[0].DeviceID != "phone" {
		t.Fatalf("conflict: %+v", res[0])
	}
	if next != doc || device(next, "phone").MAC != "3c:22:fb:00:11:22" {
		t.Error("conflict changed the device")
	}
	if !model.ScanConflicts(doc, host) || model.ScannedKnown(doc, host) {
		t.Error("ScanConflicts / ScannedKnown")
	}
	same := model.ScannedHost{IP: "192.168.1.20", MAC: "3C:22:FB:00:11:22"}
	if model.ScanConflicts(doc, same) || !model.ScannedKnown(doc, same) {
		t.Error("same MAC is known")
	}
	if model.ScanConflicts(doc, model.ScannedHost{IP: "192.168.1.20"}) {
		t.Error("no scanned MAC cannot conflict")
	}
}

func TestSanitizeDuplicateMAC(t *testing.T) {
	doc := extraDoc(t)
	device(doc, "tv").MAC = "3C-22-FB-00-11-22"
	got := model.SanitizeDeviceExtras(doc)
	if device(got, "phone").MAC != "3c:22:fb:00:11:22" || device(got, "tv").MAC != "" {
		t.Errorf("phone %q tv %q", device(got, "phone").MAC, device(got, "tv").MAC)
	}
	if model.MatchDeviceByMAC(got, "3c:22:fb:00:11:22").ID != "phone" {
		t.Error("MAC not unique")
	}
}

// Loading keeps every valid picture, even beyond the total quota: dropping
// them silently on a load or sync merge would lose the user's data. The
// quota is enforced where pictures enter (CheckDeviceImage, imports and
// shares through CheckDeviceImagesTotal).
func TestSanitizeKeepsImagesBeyondQuota(t *testing.T) {
	doc := extraDoc(t)
	doc.Route.Devices = nil
	count := model.DeviceImagesMaxBytes/model.DeviceImageMaxBytes + 3
	for i := 0; i < count; i++ {
		doc.Route.Devices = append(doc.Route.Devices, &model.RouteDevice{ID: model.NewID("d"), Name: "d", Addresses: []string{"10.0.0.1/32"}, Final: "proxy", Image: jpegOf(model.DeviceImageMaxBytes)})
	}
	doc.Route.Devices[1].Image = "data:image/jpeg;base64,xxxx" // malformed: still dropped
	loaded := model.DecodeDoc(mustJSON(t, doc))
	kept := 0
	for _, d := range loaded.Route.Devices {
		if d.Image != "" {
			kept++
		}
	}
	if kept != count-1 {
		t.Errorf("kept %d of %d", kept, count)
	}
	if err := model.CheckDeviceImagesTotal(loaded); !errors.Is(err, model.ErrDeviceImageQuota) {
		t.Errorf("total check: %v", err)
	}
	if err := model.CheckDeviceImagesTotal(extraDoc(t)); err != nil {
		t.Errorf("small document: %v", err)
	}
	if err := model.CheckDeviceImagesTotal(nil); err != nil {
		t.Error(err)
	}
}

// A confirmed pending address replaces only the address it was paired
// with; the device's other host addresses stay.
func TestConfirmPendingKeepsOtherHostAddresses(t *testing.T) {
	doc := extraDoc(t)
	device(doc, "phone").Addresses = []string{"192.168.1.8/32", "192.168.1.9/32"}
	// Two host addresses: the replaced one is unknown, the new one is added.
	moved, _ := model.MergeScanned(doc, []model.ScannedHost{{IP: "192.168.1.50", MAC: "3c:22:fb:00:11:22"}})
	if p := device(moved, "phone"); len(p.PendingFrom) != 0 || len(p.PendingAddresses) != 1 {
		t.Fatalf("pending %+v", p)
	}
	done, err := model.ConfirmPendingAddresses(moved, "phone")
	if err != nil || strings.Join(device(done, "phone").Addresses, ",") != "192.168.1.8/32,192.168.1.9/32,192.168.1.50/32" {
		t.Fatalf("append: %v %+v", err, device(done, "phone"))
	}
	// A paired address replaces its partner only.
	paired := model.Clone(moved)
	p := device(paired, "phone")
	p.PendingFrom = map[string]string{"192.168.1.50/32": "192.168.1.8/32"}
	done, err = model.ConfirmPendingAddresses(paired, "phone")
	if err != nil || strings.Join(device(done, "phone").Addresses, ",") != "192.168.1.9/32,192.168.1.50/32" || device(done, "phone").PendingFrom != nil {
		t.Fatalf("paired: %v %+v", err, device(done, "phone"))
	}
	// The partner is gone: the new address is added.
	p.Addresses = []string{"192.168.1.9/32"}
	done, err = model.ConfirmPendingAddresses(paired, "phone")
	if err != nil || strings.Join(device(done, "phone").Addresses, ",") != "192.168.1.9/32,192.168.1.50/32" {
		t.Fatalf("gone: %v %+v", err, device(done, "phone"))
	}
	// One host address: the scan pairs it.
	single := extraDoc(t)
	moved, _ = model.MergeScanned(single, []model.ScannedHost{{IP: "192.168.1.77", MAC: "3c:22:fb:00:11:22"}})
	if got := device(moved, "phone").PendingFrom["192.168.1.77/32"]; got != "192.168.1.20/32" {
		t.Errorf("PendingFrom %q", got)
	}
	// JSON round trip and sanitizing keep valid pairs and drop stale ones.
	back := model.DecodeDoc(mustJSON(t, moved))
	if device(back, "phone").PendingFrom["192.168.1.77/32"] != "192.168.1.20/32" {
		t.Errorf("round trip %+v", device(back, "phone"))
	}
	stale := model.Clone(moved)
	device(stale, "phone").PendingFrom = map[string]string{"192.168.1.77/32": "10.9.9.9/32", "1.2.3.4/32": "192.168.1.20/32"}
	if got := model.SanitizeDeviceExtras(stale); device(got, "phone").PendingFrom != nil {
		t.Errorf("stale pairs kept: %+v", device(got, "phone").PendingFrom)
	}
	// Dismissing and an older document without pendingFrom both work.
	dismissed, _ := model.DismissPending(moved, "phone")
	if device(dismissed, "phone").PendingFrom != nil {
		t.Error("dismiss kept pairs")
	}
}

// A device seen at its own address again drops its pending change.
func TestMergeScannedKnownClearsPending(t *testing.T) {
	doc := extraDoc(t)
	moved, _ := model.MergeScanned(doc, []model.ScannedHost{{IP: "192.168.1.77", MAC: "3c:22:fb:00:11:22"}})
	if len(device(moved, "phone").PendingAddresses) != 1 {
		t.Fatal("no pending")
	}
	back, res := model.MergeScanned(moved, []model.ScannedHost{{IP: "192.168.1.20", MAC: "3c:22:fb:00:11:22"}})
	if res[0].State != model.ScanKnown {
		t.Fatalf("state %s", res[0].State)
	}
	if p := device(back, "phone"); p.PendingAddresses != nil || p.PendingFrom != nil {
		t.Errorf("pending kept: %+v", p)
	}
	if device(moved, "phone").PendingAddresses == nil {
		t.Error("input mutated")
	}
}

func TestValidateDeviceCountLimit(t *testing.T) {
	var others []*model.RouteDevice
	for i := 0; i < model.DevicesMax; i++ {
		others = append(others, &model.RouteDevice{ID: model.NewID("d"), Name: "d", Addresses: []string{"10.0.0.1/32"}, Final: "proxy"})
	}
	extra := &model.RouteDevice{ID: "extra", Name: "x", Addresses: []string{"10.9.9.9/32"}, Final: "proxy"}
	if problem := model.ValidateDevice(extra, others); problem != "设备数量已达上限" {
		t.Errorf("over the limit: %q", problem)
	}
	// Editing an existing device at the limit is fine.
	existing := others[0]
	existing.Addresses = []string{"10.8.8.8/32"}
	if problem := model.ValidateDevice(existing, others); problem != "" {
		t.Errorf("edit at the limit: %q", problem)
	}
}
