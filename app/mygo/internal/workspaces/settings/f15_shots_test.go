package settings

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// shotRow writes the band of the frame around label, 320 DIPs high, where
// SETTINGS_PREVIEW_DIR says.
func (f *fixture) shotRow(dir, name, label string, w float32) {
	f.t.Helper()
	f.tt.Advance(time.Second)
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q", label)
	}
	img := f.tt.Image()
	scale := float32(img.Bounds().Dx()) / w
	top := max(int((r.Y-160)*scale), 0)
	band := img.SubImage(image.Rect(0, top, img.Bounds().Dx(), min(top+int(320*scale), img.Bounds().Dy())))
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		f.t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, band); err != nil {
		f.t.Fatal(err)
	}
}

// TestF15Screenshots writes Settings › Config Share Address: default, custom
// with "Restore default", a wrong address, cleared, dark and narrow.
func TestF15Screenshots(t *testing.T) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SETTINGS_PREVIEW_DIR not set")
	}
	f := newFixture(t, nil)
	f.tt.Frame()
	f.shotRow(dir, "f15-share-unset.png", "配置分享地址", width)
	f.focusShareOrigin()
	f.typeKeys("https://share.example.com", "")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()
	f.shotRow(dir, "f15-share-custom.png", "配置分享地址", width)
	f.clearField("https://share.example.com")
	f.typeKeys("share.example.com", "https://share.example.com")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.Frame()
	f.shotRow(dir, "f15-share-invalid.png", "配置分享地址", width)
	f.clearField("https://share.example.com")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.SetDark(true)
	f.tt.Frame()
	f.shotRow(dir, "f15-share-empty-dark.png", "配置分享地址", width)
	f.tt.SetDark(false)
	f.typeKeys("https://share.example.com", "")
	f.tt.Key(0, ui.KeyEnter)
	f.tt.SetSize(640, height)
	f.tt.Frame()
	// Narrow, the row is under the first column's groups (F17).
	f.w.Open(profiles.ShareOriginSetting)
	f.tt.Frame()
	f.tt.Frame()
	f.tt.Scroll(320, 600, 0, 240)
	f.tt.Frame()
	f.shotRow(dir, "f15-share-narrow.png", "配置分享地址", 640)
}
