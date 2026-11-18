package routes

import (
	"image"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// sidebarProfilesFixture is the item-16 sidebar: iPhone with Default, Work
// and Games (Work in effect) and IP changed, iPad with Default and Study, MacBook
// and NAS with one profile, each with its picture.
func sidebarProfilesFixture(t *testing.T) *fixture {
	t.Helper()
	doc, _ := profileDoc(t)
	next, err := model.AddProfile(doc, "phone", "游戏", "")
	if err != nil {
		t.Fatal(err)
	}
	phone := next.Route.Devices[0]
	phone.ImageKey = "iphone"
	phone.MAC = "00:11:22:33:44:55"
	phone.PendingAddresses = []string{"192.168.1.55/32"}
	phone.PendingFrom = map[string]string{"192.168.1.55/32": "192.168.1.20/32"}
	for _, p := range model.DeviceProfiles(phone) {
		if p.Name == "工作" {
			phone.ActiveProfile = p.ID
		}
	}
	next.Route.Devices = append(next.Route.Devices,
		&model.RouteDevice{ID: "ipad", Name: "iPad", Icon: "phone", ImageKey: "ipad", Addresses: []string{"192.168.1.21/32"}, Final: "proxy"},
		&model.RouteDevice{ID: "mac", Name: "MacBook", Icon: "computer", ImageKey: "mac", Addresses: []string{"192.168.1.22/32"}, Final: "proxy"},
		&model.RouteDevice{ID: "nas", Name: "NAS", Icon: "server", Image: testImage(t, 7), Addresses: []string{"192.168.1.23/32"}, Final: "proxy"})
	if next, err = model.AddProfile(next, "ipad", "学习", ""); err != nil {
		t.Fatal(err)
	}
	f := newFixtureDoc(t, next, nil)
	testkit.UntilText(t, f.tt, "MacBook")
	return f
}

// textStart is the x where the text of the sidebar row r starts: the first
// column after the avatar square whose pixels around the row's middle
// differ from the row's background.
func textStart(img *image.RGBA, r ui.Rect) float32 {
	x0, y := int(r.X)+sidebarTextInset-6, int(r.Y+r.H/2)
	bg := img.RGBAAt(x0, int(r.Y)+3)
	for x := x0; x < int(r.X+r.W); x++ {
		for dy := -6; dy <= 6; dy++ {
			p := img.RGBAAt(x, y+dy)
			d := math.Abs(float64(p.R)-float64(bg.R)) + math.Abs(float64(p.G)-float64(bg.G)) + math.Abs(float64(p.B)-float64(bg.B))
			if d > 90 {
				return float32(x) - r.X
			}
		}
	}
	return -1
}

// The rows of the sidebar, wide and in the narrow overlay: no expand
// control, every row at the same left edge and every name, the profiles'
// too, starting at the same x.
func TestSidebarRowsAlign(t *testing.T) {
	f := sidebarProfilesFixture(t)
	f.w.showActiveProfile("phone")
	for _, width := range []int{1280, 480} {
		f.tt = ui.NewTester(f.view, width, 820)
		f.w.sidebar = true
		f.tt.Frame()
		f.tt.Frame()
		for _, s := range f.tt.Texts() {
			if strings.HasPrefix(s, "展开配置") || strings.HasPrefix(s, "收起配置") {
				t.Fatalf("width %d: an expand control %q", width, s)
			}
		}
		img := f.tt.Image()
		var edge, text float32
		for i, name := range []string{"前置规则", "本机 / 网关", "iPhone", "默认", "工作", "游戏", "iPad", "MacBook", "NAS", "其他设备"} {
			r, ok := f.tt.Find(name)
			if !ok {
				t.Fatalf("width %d: no row %q: %q", width, name, f.tt.Texts())
			}
			x := textStart(img, r)
			t.Logf("width %d: %s row x %v text +%v", width, name, r.X, x)
			if i == 0 {
				edge, text = r.X, x
				continue
			}
			if r.X != edge {
				t.Errorf("width %d: row %q at x %v, 前置规则 at %v", width, name, r.X, edge)
			}
			if x < text-1 || x > text+1 {
				t.Errorf("width %d: the name of %q starts at +%v, 前置规则's at +%v", width, name, x, text)
			}
		}
	}
}

// sidebarSlice is the texts of the sidebar from the first one equal to
// from up to the first equal to to after it.
func sidebarSlice(t *testing.T, texts []string, from, to string) []string {
	t.Helper()
	i := slices.Index(texts, from)
	if i < 0 {
		t.Fatalf("no %q: %q", from, texts)
	}
	j := slices.Index(texts[i:], to)
	if j < 0 {
		t.Fatalf("no %q after %q: %q", to, from, texts)
	}
	return texts[i : i+j]
}

// No row shows a rule count; More shows once per device, on its row; the
// profile rows show their names only; a press on one switches to it.
// Local / Gateway with several profiles has its More and no count either.
func TestSidebarProfileRowsShowNamesOnly(t *testing.T) {
	f := sidebarProfilesFixture(t)
	f.w.showActiveProfile("phone")
	f.tt.Frame()
	texts := f.tt.Texts()
	device := sidebarSlice(t, texts, "iPhone", "默认")
	hasDigits := func(texts []string) bool {
		return slices.ContainsFunc(texts, func(s string) bool { _, err := strconv.Atoi(s); return err == nil })
	}
	if hasDigits(device) || !slices.Contains(device, "更多：iPhone") || slices.Contains(device, "工作") {
		t.Fatalf("device row %q", device)
	}
	if rows := sidebarSlice(t, texts, "默认", "iPad"); !slices.Equal(rows, []string{"默认", "默认", "工作", "工作", "游戏", "游戏"}) {
		t.Fatalf("profile rows %q", rows)
	}
	for _, name := range []string{"iPhone", "iPad", "MacBook", "NAS"} {
		n := 0
		for _, s := range texts {
			if s == "更多："+name {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d 更多 buttons of %s: %q", n, name, texts)
		}
	}
	for _, s := range texts {
		if s == "更多：默认" || s == "更多：工作" || s == "更多：游戏" {
			t.Fatalf("a profile row has %q", s)
		}
	}
	// Another device selected: the iPhone row shows Work beside its name and
	// no profile rows; iPad lists its own.
	f.click("iPad")
	testkit.UntilText(t, f.tt, "学习")
	texts = f.tt.Texts()
	if row := sidebarSlice(t, texts, "iPhone", "iPad"); !slices.Contains(row, "工作") || slices.Contains(row, "游戏") {
		t.Fatalf("unselected iPhone %q", row)
	}
	// A press shows a profile; a double click and Switch put it in effect.
	f.click("iPhone")
	testkit.UntilText(t, f.tt, "游戏")
	f.click("游戏")
	game := f.profileID("游戏")
	if f.w.owner != model.ProfileScope("phone", game) || profileActive(f) == game {
		t.Fatalf("preview: owner %q active %q", f.w.owner, profileActive(f))
	}
	f.tt.Advance(time.Second)
	f.doubleClick("游戏")
	testkit.UntilText(t, f.tt, "切换到「游戏」？")
	f.click("切换")
	f.waitSaved(func(doc *model.Doc) bool { return doc.Route.Devices[0].ActiveProfile == game })
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("切换到「游戏」？") })
	if f.w.owner != model.ProfileScope("phone", game) {
		t.Fatalf("owner %q", f.w.owner)
	}
	if rows := sidebarSlice(t, f.tt.Texts(), "默认", "iPad"); hasDigits(rows) {
		t.Fatalf("profile rows %q", rows)
	}
	if !f.w.update(func(doc *model.Doc) (*model.Doc, error) {
		return model.AddProfile(doc, model.LocalDeviceID, "旅行", "")
	}) {
		t.Fatal(f.w.err)
	}
	f.waitSaved(func(doc *model.Doc) bool { return len(doc.Route.LocalProfiles) == 2 })
	f.w.showActiveProfile(model.LocalDeviceID)
	testkit.UntilText(t, f.tt, "旅行")
	local := sidebarSlice(t, f.tt.Texts(), model.LocalDeviceName, "iPhone")
	if hasDigits(local) || !slices.Contains(local, "更多："+model.LocalDeviceName) || !slices.Contains(local, "旅行") {
		t.Fatalf("local rows %q", local)
	}
}

// The device menu manages the profile shown, else the one in effect; the
// delete confirmation shows at the device's More button, wide and narrow.
func TestSidebarProfileMenuAndDeleteAnchor(t *testing.T) {
	f := sidebarProfilesFixture(t)
	phone := findDevice(f.w.doc(), "phone")
	if p := f.w.menuProfile(phone); p.Name != "工作" {
		t.Fatalf("unselected: %q", p.Name)
	}
	f.w.showDevice("phone", false)
	if p := f.w.menuProfile(phone); p.Name != "默认" {
		t.Fatalf("showing 默认: %q", p.Name)
	}
	for _, width := range []int{1280, 480} {
		f.tt = ui.NewTester(f.view, width, 820)
		f.w.sidebar = true
		f.tt.Frame()
		f.w.requestDeleteProfile("device:phone", "phone", f.profileID("游戏"))
		f.tt.Frame()
		testkit.UntilText(t, f.tt, "删除配置「游戏」？")
		more, _ := f.tt.Find("更多：iPhone")
		title, _ := f.tt.Find("删除配置「游戏」？")
		t.Logf("width %d: 更多 %+v, title %+v", width, more, title)
		if title.Y < more.Y-240 || title.Y > more.Y+more.H+80 {
			t.Fatalf("width %d: the confirmation at %+v, 更多 at %+v", width, title, more)
		}
		f.click("取消")
		testkit.Until(t, f.tt, func() bool { return !f.w.profileDelete.state.Open })
	}
}
