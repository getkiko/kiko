package overview

import (
	"os"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// TestF15Screenshots writes the "Manage profile" menu in a window of the
// least height, light and dark, and in a narrow, shorter window with
// "Share current profile" unavailable and its hint, where
// OVERVIEW_PREVIEW_DIR says.
func TestF15Screenshots(t *testing.T) {
	if os.Getenv("OVERVIEW_PREVIEW_DIR") == "" {
		t.Skip("OVERVIEW_PREVIEW_DIR is not set")
	}
	f := newFixture(t)
	f.tt.SetSize(1320, 640)
	f.tt.Frame()
	f.click(t, "管理配置")
	f.tt.Advance(time.Second)
	savePreview(t, f.tt, "f15-menu-short-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	savePreview(t, f.tt, "f15-menu-short-dark.png")
	f.tt.SetDark(false)
	f.click(t, "管理配置")
	f.tt.SetSize(980, 520)
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "")
	f.tt.Frame()
	f.click(t, "管理配置")
	f.tt.Advance(time.Second)
	r, _ := f.tt.Find("分享当前配置")
	f.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	f.tt.Advance(2 * time.Second)
	savePreview(t, f.tt, "f15-menu-narrow-share-unavailable.png")
}
