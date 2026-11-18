package kit

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// preview is a monitor workspace built from the kit, as the connection
// monitor composes it.
type preview struct {
	segment  int
	query    string
	table    TableState
	selected map[string]bool
	open     bool
	confirm  ConfirmState
	cleared  bool
	density  int
	dialog   bool
	name     string
	enabled  bool
	choice   string
}

func (p *preview) view(c *ui.Context) {
	Apply(c)
	k := T(c)
	c.Root().Background(k.Surface)
	hosts := []string{"mtalk.google.com", "ab.chatgpt.com", "api.github.com", "x.com", "grok.com", "accounts.google.com"}
	Workspace(c, func() {
		Toolbar(c, "连接", "活动 21 · 已关闭 10", func() {
			Segmented(c, "state", &p.segment, Segment{Label: "活动", Count: "21"}, Segment{Label: "已关闭", Count: "10"})
			Search(c, "search", &p.query, SearchOptions{Placeholder: "搜索主机、进程、地址、链路…", Toolbar: true, Width: 260})
			ToolbarGroup(c, func() {
				IconButton(c, SymSlidersHorizontal, "筛选", IconButtonOptions{Active: true})
				IconButton(c, SymColumns3, "列", IconButtonOptions{})
				IconButton(c, SymDownload, "导出", IconButtonOptions{})
				clear := IconButton(c, SymTrash2, "清空", IconButtonOptions{})
				if clear.Clicked() {
					p.confirm.Open = true
				}
				ConfirmPopover(c, clear, &p.confirm, ConfirmOptions{Title: "关闭全部连接？", Confirm: "关闭", Danger: true,
					Run: func(done func(error)) { p.cleared = true; done(nil) }})
			})
		})
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			FilterPane(c, FilterConnection, len(p.selected) > 0, func() { p.selected = map[string]bool{} }, func() {
				FilterGroup(c, FilterConnection, "源地址", nil, func() {
					if FilterOption(c, FilterConnection, p.selected["172.18.0.1"], nil, "172.18.0.1", 21).Clicked() {
						p.selected["172.18.0.1"] = !p.selected["172.18.0.1"]
					}
				})
				FilterGroup(c, FilterConnection, "出站", nil, func() {
					FilterOption(c, FilterConnection, false, nil, "直连", 11)
					FilterOption(c, FilterConnection, false, nil, "美国板瓦", 10)
				})
			})
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				Table(c, &p.table, []Column{
					{Title: "主机 / 进程", Sortable: true, MinWidth: 200, Grow: 2},
					{Title: "规则 / 链路", MinWidth: 160},
					{Title: "上传", Width: 80, Align: ui.End, Sortable: true},
					{Title: "下载", Width: 80, Align: ui.End},
				}, len(hosts), TableOptions{Label: "连接", Density: TwoLine, Striped: true}, func(row, col int) {
					switch col {
					case 0:
						Badge(c, "TCP", AccentTone)
						Cell(c, hosts[row], "Google Chrome Helper")
					case 1:
						Cell(c, "RuleSet: geosite-cn", "直连 → 直连的")
					case 2:
						NumberCell(c, Bytes(float64(row*2300)))
					case 3:
						NumberCell(c, Bytes(float64(row*5400+90)))
					}
				})
				StatusBar(c, fmt.Sprintf("显示 1–%d / %d 条连接", len(hosts), len(hosts)), "实时更新", func() {
					page := 0
					Pagination(c, &page, 1)
				})
			})
			if row, ok := p.table.Chosen(len(hosts)); ok {
				Inspector(c, hosts[row], "Google Chrome Helper", nil, nil, func() { p.table.Clear() }, func() {
					Section(c, "连接", nil, func() {
						Property(c, "源地址", "172.18.0.1:61261")
						Property(c, "目标地址", "142.250.141.188:443")
					})
					DotChart(c, []*float64{Sample(1), nil, Sample(4), Sample(2), Sample(8)}, DotChartOptions{Label: "下载速度", Value: "1.2 MB/s", Columns: 30})
				})
			}
		})
	})
	Dialog(c, &p.dialog, "编辑出站", 0, func() {
		FormRow(c, "名称", "", func() { Input(c, "name", &p.name, "名称") })
		FormRow(c, "启用", "", func() { Switch(c, &p.enabled, "启用") })
		FormRow(c, "协议", "", func() {
			Select(c, "proto", &p.choice, []Option{{Value: "vless", Label: "VLESS"}, {Value: "trojan", Label: "Trojan"}}, SelectOptions{})
		})
	}, func() {
		Button(c, "取消", ButtonOptions{}).OnClick(func() { p.dialog = false })
		Button(c, "保存", ButtonOptions{Variant: Primary})
	})
}

func TestKitPreview(t *testing.T) {
	p := &preview{selected: map[string]bool{}, choice: "vless"}
	tt := ui.NewTester(p.view, 1280, 640)
	if err := tt.Click("ab.chatgpt.com"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("142.250.141.188:443") {
		t.Fatalf("inspector not shown: %q", tt.Texts())
	}
	if err := tt.Click("172.18.0.1"); err != nil {
		t.Fatal(err)
	}
	if !p.selected["172.18.0.1"] || !tt.HasText("清除") {
		t.Errorf("filter option not chosen: %v", p.selected)
	}
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if !p.cleared || p.confirm.Open {
		t.Errorf("confirm popover: cleared %v open %v", p.cleared, p.confirm.Open)
	}
	if err := tt.Click("按上传排序"); err != nil {
		t.Fatal(err)
	}
	if p.table.Sort.Column != "上传" {
		t.Errorf("sort %+v", p.table.Sort)
	}
	savePreview(t, tt, "kit-light.png")
	tt.SetDark(true)
	p.dialog = true
	tt.Frame()
	if !tt.HasText("编辑出站") {
		t.Errorf("dialog not shown")
	}
	savePreview(t, tt, "kit-dark.png")
}

// savePreview writes the frame where KIT_PREVIEW_DIR says, for reviewing
// the look.
func savePreview(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("KIT_PREVIEW_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// A keyed table's choice follows its item when rows before it go, even
// when its old index is past the new end.
func TestTableChoiceFollowsKeyAsListShrinks(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	var s TableState
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		Table(c, &s, []Column{{Title: "名称"}}, len(items), TableOptions{Label: "表", Key: func(row int) any { return items[row] }},
			func(row, col int) { Cell(c, items[row], "") })
	}, 400, 400)
	if err := tt.Click("e"); err != nil {
		t.Fatal(err)
	}
	if s.Selected != 4 {
		t.Fatalf("selected %d after click", s.Selected)
	}
	items = []string{"d", "e"}
	tt.Frame()
	if row, ok := s.Chosen(len(items)); !ok || items[row] != "e" {
		t.Errorf("choice %d (%v) after the list shrank, want e", s.Selected, ok)
	}
	items = []string{"x"}
	tt.Frame()
	if _, ok := s.Chosen(len(items)); ok || s.Selected != -1 {
		t.Errorf("choice %d kept after its item went", s.Selected)
	}
}

// The selected indicator of a segmented control slides to a new choice over
// about 220 ms, and is there at once while the desktop asks for less motion.
func TestSegmentedIndicatorSlides(t *testing.T) {
	selected := 0
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(20).Children(func() {
			Segmented(c, "seg", &selected, Segment{Label: "第一项"}, Segment{Label: "第二项"}, Segment{Label: "第三项"})
		})
	}, 400, 100)
	tt.Frame()
	// lit reports whether the indicator's white shows in the padding before
	// a segment's label.
	lit := func(label string) bool {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("no %s in %q", label, tt.Texts())
		}
		p := tt.Image().RGBAAt(int(r.X-6), int(r.Y+r.H/2))
		return p.R > 250 && p.G > 250 && p.B > 250
	}
	if !lit("第一项") || lit("第三项") {
		t.Fatalf("indicator not on the first segment")
	}
	if err := tt.Click("第三项"); err != nil {
		t.Fatal(err)
	}
	if selected != 2 {
		t.Fatalf("selected %d", selected)
	}
	tt.Advance(110 * time.Millisecond)
	savePreview(t, tt, "segmented-halfway.png")
	if lit("第三项") || !lit("第二项") {
		t.Errorf("halfway, the indicator is not passing the second segment")
	}
	tt.Advance(300 * time.Millisecond)
	if !lit("第三项") || lit("第一项") {
		t.Errorf("once over, the indicator is not on the third segment")
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	if err := tt.Click("第一项"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !lit("第一项") || lit("第二项") {
		t.Errorf("with less motion, the indicator does not move at once")
	}
}

func TestHintWaitsForALongHover(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(40).Gap(20).Children(func() {
			IconButton(c, SymTrash2, "清空", IconButtonOptions{Hint: "清空连接记录"})
			IconButton(c, SymDownload, "导出", IconButtonOptions{Hint: "导出全部明细"})
		})
	}, 300, 200)
	tt.Frame()
	// The buttons are 30 DIPs, at 40,40 and 40,90.
	tt.Move(55, 55)
	tt.Advance(1000 * time.Millisecond)
	if tt.HasText("清空连接记录") {
		t.Fatal("the hint shows after a second")
	}
	tt.Advance(700 * time.Millisecond)
	if !tt.HasText("清空连接记录") {
		t.Fatalf("no hint after 1.7 seconds: %q", tt.Texts())
	}
	savePreview(t, tt, "hint.png")
	// Moving on to the next button starts the wait again.
	tt.Move(55, 105)
	tt.Advance(300 * time.Millisecond)
	if tt.HasText("导出全部明细") || tt.HasText("清空连接记录") {
		t.Fatalf("a hint trails the pointer: %q", tt.Texts())
	}
	tt.Advance(1500 * time.Millisecond)
	if !tt.HasText("导出全部明细") {
		t.Fatalf("no hint on the second button: %q", tt.Texts())
	}
}

// Text an ellipsis ends shows whole in a hint after a long hover; text
// that fits shows none.
func TestClippedShowsWholeText(t *testing.T) {
	const long = "very-long-host-name.subdomain.example.com"
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Canvas).Padding(40).Gap(20).Children(func() {
			ui.Row(c).Width(80).Height(24).Children(func() { Clipped(c, long, FontTable, 400) })
			ui.Row(c).Width(200).Height(24).Children(func() { Clipped(c, "short", FontTable, 400) })
		})
	}, 400, 200)
	tt.Frame()
	count := func(s string) int {
		n := 0
		for _, text := range tt.Texts() {
			if text == s {
				n++
			}
		}
		return n
	}
	// The rows are 24 DIPs, at 40,40 and 40,84.
	tt.Move(60, 52)
	tt.Advance(1700 * time.Millisecond)
	if count(long) != 2 {
		t.Fatalf("no hint for clipped text: %q", tt.Texts())
	}
	tt.Move(60, 96)
	tt.Advance(1700 * time.Millisecond)
	if count(long) != 1 || count("short") != 1 {
		t.Fatalf("a hint for text that fits: %q", tt.Texts())
	}
}

// guardView builds an editor sidebar guarded by g, with a footer action.
func guardView(open *bool, g *EditorGuard) func(c *ui.Context) {
	return func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface)
		EditorSidebarGuarded(c, open, false, g, "编辑", nil, func() { ui.Text(c, "表单") }, func() {
			Button(c, "保存", ButtonOptions{Variant: Primary})
		})
	}
}

// The close button and Escape are a discard: they close at once, changes
// or not, and even while the question shows.
func TestEditorGuardExplicitWaysCloseAtOnce(t *testing.T) {
	for name, way := range map[string]func(tt *ui.Tester){
		"close":  func(tt *ui.Tester) { _ = tt.Click("关闭编辑器") },
		"escape": func(tt *ui.Tester) { tt.Key(0, ui.KeyEscape) },
	} {
		open := true
		g := &EditorGuard{Dirty: true}
		tt := ui.NewTester(guardView(&open, g), 900, 600)
		tt.Frame()
		way(tt)
		tt.Frame()
		if open || g.Pending() || tt.HasText("放弃更改？") {
			t.Fatalf("%s: open %v asking %v", name, open, g.Pending())
		}
	}
	open := true
	g := &EditorGuard{Dirty: true}
	tt := ui.NewTester(guardView(&open, g), 900, 600)
	tt.Frame()
	tt.ClickAt(4, 4)
	tt.Frame()
	if !g.Pending() {
		t.Fatal("no question")
	}
	_ = tt.Click("关闭编辑器")
	tt.Frame()
	if open || g.Pending() {
		t.Fatalf("close while asking: open %v asking %v", open, g.Pending())
	}
}

// A press outside with changes shows the question in the sidebar, over
// the footer actions, focused on "Keep editing".
func TestEditorGuardAsksOnPressOutside(t *testing.T) {
	open := true
	g := &EditorGuard{Dirty: true}
	tt := ui.NewTester(guardView(&open, g), 900, 600)
	tt.Frame()
	tt.ClickAt(4, 4)
	tt.Frame()
	tt.Advance(time.Second)
	if !open || !g.Pending() || !tt.HasText("放弃更改？") || !tt.HasText("继续编辑") {
		t.Fatalf("open %v asking %v texts %q", open, g.Pending(), tt.Texts())
	}
	if !tt.Focused("继续编辑") {
		t.Fatal("继续编辑 is not focused")
	}
	bar, _ := tt.Find("放弃更改？")
	save, _ := tt.Find("保存")
	if bar.Y+bar.H > save.Y || bar.X < 900-12-EditorWidth || bar.X+bar.W > 900-12 || bar.H == 0 {
		t.Fatalf("question at %+v, save at %+v", bar, save)
	}
	// Presses in the question are not outside the sidebar, and another
	// press outside keeps it.
	tt.ClickAt(4, 4)
	tt.Frame()
	if !open || !g.Pending() {
		t.Fatal("a second press outside dropped the question")
	}
	if err := tt.Click("继续编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !open || g.Pending() || tt.HasText("放弃更改？") {
		t.Fatalf("继续编辑: open %v asking %v", open, g.Pending())
	}
	// Escape answers the question, keeping the draft.
	tt.ClickAt(4, 4)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !open || g.Pending() {
		t.Fatalf("escape in the question: open %v asking %v", open, g.Pending())
	}
	tt.ClickAt(4, 4)
	tt.Frame()
	if err := tt.Click("放弃更改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if open {
		t.Fatal("discard kept the editor open")
	}
}

func TestEditorGuardCleanAndNilCloseAtOnce(t *testing.T) {
	open := true
	tt := ui.NewTester(guardView(&open, &EditorGuard{}), 900, 600)
	tt.Frame()
	tt.ClickAt(4, 4)
	tt.Frame()
	if open || tt.HasText("放弃更改？") {
		t.Fatal("a clean editor asked")
	}
	open = true
	tt = ui.NewTester(guardView(&open, nil), 900, 600)
	tt.Frame()
	tt.ClickAt(4, 4)
	tt.Frame()
	if open {
		t.Fatal("an unguarded editor stayed open")
	}
}

// A switch asks the same way and runs after the discard; asked again
// while the question shows, the latest switch waits instead.
func TestEditorGuardRunsTheSwitchAfterDiscard(t *testing.T) {
	open, first, second := true, 0, 0
	g := &EditorGuard{Dirty: true}
	tt := ui.NewTester(guardView(&open, g), 900, 600)
	tt.Frame()
	if g.Ask(&open, func() { first++ }) || first != 0 || !g.Pending() {
		t.Fatal("a switch with changes did not wait")
	}
	tt.Frame()
	if g.Ask(&open, func() { second++ }) || !g.Pending() {
		t.Fatal("a second switch did not wait")
	}
	tt.Frame()
	if err := tt.Click("放弃更改"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if first != 0 || second != 1 || !open || g.Pending() {
		t.Fatalf("first %d second %d open %v asking %v", first, second, open, g.Pending())
	}
	// Discard drops a waiting switch.
	g.Dirty = true
	g.Ask(&open, func() { first++ })
	g.Discard(&open)
	if open || g.Pending() || first != 0 {
		t.Fatalf("discard: open %v asking %v first %d", open, g.Pending(), first)
	}
}

// The question keeps its retry and its single run while busy.
func TestConfirmPopoverRetriesAndRunsOnce(t *testing.T) {
	var s ConfirmState
	var finish func(error)
	runs := 0
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(40)
		a := Button(c, "删除", ButtonOptions{})
		ConfirmPopover(c, a, &s, ConfirmOptions{Title: "放弃更改？", Confirm: "放弃更改", Danger: true,
			Run: func(done func(error)) { runs++; finish = done }})
	}, 400, 300)
	s.Open = true
	tt.Frame()
	_ = tt.Click("放弃更改")
	_ = tt.Click("放弃更改")
	tt.Frame()
	if runs != 1 || !s.Busy {
		t.Fatalf("runs %d busy %v", runs, s.Busy)
	}
	finish(errTest("失败了"))
	tt.Frame()
	if s.Busy || !s.Open || !tt.HasText("失败了") || !tt.HasText("重试") {
		t.Fatalf("failure: busy %v open %v texts %q", s.Busy, s.Open, tt.Texts())
	}
	_ = tt.Click("重试")
	finish(nil)
	tt.Frame()
	if runs != 2 || s.Open {
		t.Fatalf("retry: runs %d open %v", runs, s.Open)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
