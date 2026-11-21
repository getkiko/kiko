package kit

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// clickTwice clicks the trigger to open a popover, then again to close it:
// the second click must not close it and open it again.
func clickTwice(t *testing.T, tt *ui.Tester, x, y float32, shown string) {
	t.Helper()
	tt.ClickAt(x, y)
	tt.Frame()
	if !tt.HasText(shown) {
		t.Fatalf("first click did not open: %q", tt.Texts())
	}
	tt.ClickAt(x, y)
	tt.Frame()
	tt.Frame()
	if tt.HasText(shown) {
		t.Fatalf("second click left it open: %q", tt.Texts())
	}
}

func TestSearchSelectTriggerToggles(t *testing.T) {
	value := "a"
	options := []Option{{Value: "a", Label: "Alpha"}, {Value: "b", Label: "Beta"}, {Value: "c", Label: "Gamma"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "选项", Width: 200})
	}, 640, 600)
	tt.Frame()
	r, _ := tt.Find("选项：Alpha")
	clickTwice(t, tt, r.X+r.W/2, r.Y+r.H/2, "Gamma")
}

func TestBrowseTokenInputToggles(t *testing.T) {
	query := ""
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		TokenInput(c, "pick", &query, TokenInputOptions{Label: "选项", Placeholder: "选择", Browse: true,
			Suggestions: []TokenSuggestion{{ID: "a", Label: "Alpha"}, {ID: "g", Label: "Gamma"}}})
	}, 640, 600)
	tt.Frame()
	clickTwice(t, tt, 120, 35, "Gamma")
}

func TestActionMenuAnchorToggles(t *testing.T) {
	open := false
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		b := ui.ButtonBase(c).Size(80, 30).Label("菜单")
		if b.Clicked() {
			open = !open
		}
		ActionMenu(c, b, &open, []ActionGroup{{Items: []ActionItem{{Label: "Gamma"}}}}, ActionMenuOptions{})
	}, 640, 600)
	tt.Frame()
	r, _ := tt.Find("菜单")
	clickTwice(t, tt, r.X+r.W/2, r.Y+r.H/2, "Gamma")
}

// The chevron beside the input opens and closes the list as well.
func TestBrowseTokenInputChevronToggles(t *testing.T) {
	query := ""
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		ui.Column(c).Width(300).Children(func() {
			TokenInput(c, "pick", &query, TokenInputOptions{Label: "选项", Placeholder: "选择", Browse: true,
				Suggestions: []TokenSuggestion{{ID: "a", Label: "Alpha"}, {ID: "g", Label: "Gamma"}}})
		})
	}, 640, 600)
	tt.Frame()
	clickTwice(t, tt, 20+300-12, 35, "Gamma")
}

// A long detail (an address) shortens before the name: the name stays.
func TestSearchSelectKeepsNameBesideLongDetail(t *testing.T) {
	value := ""
	options := []Option{{Value: "a", Label: "香港 01 · 家宽 IEPL 专线", Tag: "VLESS", Detail: "a-very-long-server-name.example-provider.com:443"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "出口", Width: 220})
	}, 640, 600)
	tt.Frame()
	if err := tt.Click("出口：请选择"); err != nil {
		t.Fatalf("open: %v; %q", err, tt.Texts())
	}
	tt.Frame()
	name, ok := tt.Find("香港 01 · 家宽 IEPL 专线")
	tag, _ := tt.Find("VLESS")
	// Find gives the row (named by the label): the tag comes after the
	// whole name, about 150 DIPs in.
	if !ok || tag.X-name.X < 140 {
		t.Fatalf("name %+v tag %+v", name, tag)
	}
}

func TestSearchSelectTagShot(t *testing.T) {
	dir := os.Getenv("KIT_PREVIEW_DIR")
	if dir == "" {
		t.Skip()
	}
	value := "b"
	options := []Option{{Value: "", Label: "不加载"},
		{Value: "a", Label: "香港 01 · 家宽 IEPL 专线", Tag: "VLESS", Detail: "a-very-long-server-name.example-provider.com:443"},
		{Value: "b", Label: "日本 02", Tag: "SS", Detail: "jp.example:8388"},
		{Value: "c", Label: "直连", Tag: "直连"}}
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		SearchSelect(c, "pick", &value, options, SearchSelectOptions{Label: "出口", Width: 220})
	}, 420, 320)
	tt.Frame()
	_ = tt.Click("出口：日本 02")
	tt.Frame()
	f, _ := os.Create(filepath.Join(dir, "searchselect-tags.png"))
	_ = png.Encode(f, tt.Image())
	f.Close()
}
