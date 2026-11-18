package overview

import (
	"slices"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// opened returns what the shell was asked to open.
func (f *fixture) opened() []string {
	return slices.Clone(f.shell.Opened)
}

// share runs "Share current profile" through the dialog to the upload page.
func (f *fixture) share(t *testing.T) {
	t.Helper()
	f.click(t, "管理配置")
	f.click(t, "分享当前配置")
	testkit.UntilText(t, f.tt, "上传 Kiko 配置源文件")
	f.w.profiles.share.confirmed = true
	f.click(t, "验证并上传")
	testkit.Until(t, f.tt, func() bool { return f.w.profiles.share.done || f.w.profiles.share.err != "" })
}

func TestShareUsesTheSetAddress(t *testing.T) {
	f := newFixture(t)
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "https://share.example.com")
	f.share(t)
	if got := f.opened(); !slices.Contains(got, "https://share.example.com/share-upload") {
		t.Fatalf("opened %q", got)
	}
}

// TestShareWithoutAnAddress: with no address set (there is no built-in
// share service), "Share current profile" shows unavailable with a hint,
// and leads to the setting; a cleared address does the same.
func TestShareWithoutAnAddress(t *testing.T) {
	f := newFixture(t)
	if f.w.shareAvailable() {
		t.Fatal("sharing is available without an address")
	}
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "https://share.example.com")
	if !f.w.shareAvailable() {
		t.Fatal("sharing is unavailable with an address")
	}
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "")
	var page string
	var arg any
	f.env.SetNavigator(func(p string, a any) { page, arg = p, a })
	f.click(t, "管理配置")
	r, ok := f.tt.Find("分享当前配置")
	if !ok {
		t.Fatalf("no share item: %q", f.tt.Texts())
	}
	f.tt.Move(r.X+r.W/2, r.Y+r.H/2)
	f.tt.Advance(2 * time.Second)
	if !f.tt.HasText("在设置中配置分享地址") {
		t.Fatalf("no hint: %q", f.tt.Texts())
	}
	f.click(t, "分享当前配置")
	f.tt.Frame()
	if page != env.PageSettings || arg != profiles.ShareOriginSetting {
		t.Fatalf("navigated to %q %v", page, arg)
	}
	if f.w.profiles.share.open || f.w.profiles.more {
		t.Fatalf("dialog %v menu %v", f.w.profiles.share.open, f.w.profiles.more)
	}
	// An address that turns invalid after the dialog opened stops the
	// upload in the dialog.
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "https://share.example.com")
	f.click(t, "管理配置")
	f.click(t, "分享当前配置")
	testkit.UntilText(t, f.tt, "上传 Kiko 配置源文件")
	f.env.AppPrefs.Save(profiles.PrefShareOrigin, "http://share.example.com")
	f.w.profiles.share.confirmed = true
	f.click(t, "验证并上传")
	testkit.UntilText(t, f.tt, "需要 https 地址")
	if len(f.opened()) != 0 || len(f.shell.SavedNames()) != 0 {
		t.Fatalf("opened %q saved %q", f.opened(), f.shell.SavedNames())
	}
}

// TestProfileMenuFitsAShortWindow opens "Manage profile" in windows of the
// least height and lower: the menu starts in the window, scrolls, and its
// last item, "Delete current profile", comes into the window and takes the
// click.
func TestProfileMenuFitsAShortWindow(t *testing.T) {
	for _, size := range [][2]int{{1320, 640}, {980, 640}, {1320, 420}} {
		f := newFixture(t)
		f.tt.SetSize(size[0], size[1])
		f.tt.Frame()
		f.click(t, "管理配置")
		f.tt.Frame()
		w, h := float32(size[0]), float32(size[1])
		first, ok := f.tt.Find("复制")
		if !ok || first.Y < 4 || first.X < 4 || first.X+first.W > w-4 || first.Y+first.H > h-4 {
			t.Fatalf("%v: first item at %+v (%v)", size, first, ok)
		}
		for range 10 {
			f.tt.Scroll(first.X+first.W/2, first.Y+first.H/2, 0, 200)
			f.tt.Frame()
		}
		last, ok := f.tt.Find("删除当前配置")
		if !ok || last.Y < 4 || last.Y+last.H > h-4 || last.X+last.W > w-4 {
			t.Fatalf("%v: last item at %+v (%v)", size, last, ok)
		}
		f.click(t, "删除当前配置")
		testkit.UntilText(t, f.tt, "删除「未命名配置」？")
	}
}
