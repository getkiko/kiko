package settings

import (
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// rect is the box of the element showing label.
func (f *fixture) rect(label string) ui.Rect {
	f.t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		f.t.Fatalf("no %q; texts %q", label, f.tt.Texts())
	}
	return r
}

// shareInBackupGroup checks that Config Share Address is the row under
// Encrypted Backup & Sync, in the group Backup & Sharing.
func (f *fixture) shareInBackupGroup() {
	f.t.Helper()
	title, backup, share := f.rect("备份与分享"), f.rect("加密备份与同步"), f.rect("配置分享地址")
	if !(title.Y < backup.Y && backup.Y < share.Y) {
		f.t.Fatalf("title %v, backup %v, share %v out of order", title, backup, share)
	}
	// The next row: no group title or other row between them.
	if gap := share.Y - (backup.Y + backup.H); gap < 0 || gap > 30 {
		f.t.Fatalf("share row %v is not the row under the backup row %v", share, backup)
	}
	if d := share.X - backup.X; d < -40 || d > 40 {
		f.t.Fatalf("share row %v is not in the backup row's column %v", share, backup)
	}
}

// Config Share Address is in the group of Encrypted Backup & Sync, wide and
// narrow; a jump to
// it (the overview's share item) focuses its field and scrolls it into
// view.
func TestShareOriginInBackupGroup(t *testing.T) {
	f := newFixture(t, nil)
	f.tt.Frame()
	f.shareInBackupGroup()
	for _, size := range [][2]int{{width, 640}, {640, 560}} {
		f.tt.SetSize(size[0], size[1])
		f.tt.Frame()
		// Away from the row first: the jump scrolls.
		f.w.Open("")
		f.tt.Frame()
		f.focusShareOrigin()
		f.tt.Frame()
		r := f.rect("配置分享地址")
		if r.Y < 0 || r.Y+r.H > float32(size[1]) {
			t.Fatalf("%v: the jump leaves the row at %v", size, r)
		}
		if !f.tt.Focused("配置分享地址") {
			t.Fatalf("%v: the field lost the focus", size)
		}
		f.shareInBackupGroup()
	}
}

// TestF17ShareGroupScreenshots writes the Backup & Sharing group: light, dark,
// the field focused by a jump, and narrow, where SETTINGS_PREVIEW_DIR says.
func TestF17ShareGroupScreenshots(t *testing.T) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SETTINGS_PREVIEW_DIR not set")
	}
	f := newFixture(t, nil)
	f.tt.Frame()
	f.shotRow(dir, "f17-share-group-light.png", "加密备份与同步", width)
	f.tt.SetDark(true)
	f.tt.Frame()
	f.shotRow(dir, "f17-share-group-dark.png", "加密备份与同步", width)
	f.tt.SetDark(false)
	f.w.Open(profiles.ShareOriginSetting)
	f.tt.Frame()
	f.tt.Frame()
	f.shotRow(dir, "f17-share-group-focused.png", "加密备份与同步", width)
	// Narrow, the group is under the first column's: the jump scrolls to it.
	f.tt.SetSize(640, height)
	f.tt.Frame()
	f.w.Open(profiles.ShareOriginSetting)
	f.tt.Frame()
	f.tt.Frame()
	// The jump brings the row in at the bottom: the band shows it whole.
	f.tt.Scroll(320, 600, 0, 240)
	f.tt.Frame()
	f.shotRow(dir, "f17-share-group-narrow.png", "加密备份与同步", 640)
	f.tt.SetDark(true)
	f.tt.Frame()
	f.shotRow(dir, "f17-share-group-narrow-dark.png", "加密备份与同步", 640)
}
