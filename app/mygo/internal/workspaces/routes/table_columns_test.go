package routes

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func ruleIDs(f *fixture) []string {
	var ids []string
	for _, r := range model.RouteRulesOf(f.w.doc(), model.LocalDeviceID) {
		ids = append(ids, r.ID)
	}
	return ids
}

// dragHeader drags the header title from over the header to, reporting
// whether the drop line showed on the way.
func (f *fixture) dragHeader(from, to string) (dropped bool) {
	f.t.Helper()
	a, ok := f.tt.Find(from)
	b, ok2 := f.tt.Find(to)
	if !ok || !ok2 {
		f.t.Fatalf("headers %q %q: %q", from, to, f.tt.Texts())
	}
	y := a.Y + a.H/2
	f.tt.Press(a.X+a.W/2, y)
	for i := 1; i <= 10; i++ {
		f.tt.Move(a.X+a.W/2+(b.X+b.W-a.X-a.W/2)*float32(i)/10, y)
		dropped = dropped || f.w.columnOrder().Dropping()
	}
	f.tt.Release(b.X+b.W, y)
	f.tt.Frame()
	return dropped
}

func x(f *fixture, text string) float32 {
	r, ok := f.tt.Find(text)
	if !ok {
		f.t.Fatalf("no %q: %q", text, f.tt.Texts())
	}
	return r.X
}

func TestDragColumnHeaderReordersCells(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	rules, requests := ruleIDs(f), len(f.srv.Requests())
	if !(x(f, "规则名称") < x(f, "匹配条件") && x(f, "匹配条件") < x(f, "目标出口")) {
		t.Fatal("default order is not name, match, target")
	}
	if !f.dragHeader("规则名称", "目标出口") {
		t.Error("no drop line while dragging")
	}
	if got := f.w.columnOrder().Order; !slices.Equal(got, []string{"index", "action", "match", "target", "name"}) {
		t.Fatalf("order %v", got)
	}
	if !(x(f, "匹配条件") < x(f, "目标出口") && x(f, "目标出口") < x(f, "规则名称")) {
		t.Fatal("headers did not move")
	}
	// The row's cells follow their headers: the target before the name.
	if tx := x(f, "美国节点"); tx > x(f, "目标出口")+40 || tx < x(f, "匹配条件") {
		t.Fatalf("cells stay in the old order: target cell %v, header %v", tx, x(f, "目标出口"))
	}
	// The rules keep their priority; nothing was saved.
	if !slices.Equal(ruleIDs(f), rules) || f.store.Saving() {
		t.Fatalf("rule order changed: %v", ruleIDs(f))
	}
	for _, r := range f.srv.Requests()[requests:] {
		if strings.HasPrefix(r, "PUT") {
			t.Fatalf("column drag saved the document: %q", f.srv.Requests())
		}
	}
	f.snapshot("4-columns-light.png", false)
	f.snapshot("4-columns-dark.png", true)
}

func TestColumnOrdersAreIndependentPerTable(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.dragHeader("规则名称", "目标出口")
	f.click("DNS 分流")
	f.tt.Frame()
	if len(f.w.columnOrder().Order) != 0 {
		t.Fatalf("DNS table takes the traffic order: %v", f.w.columnOrder().Order)
	}
	if !(x(f, "规则名称") < x(f, "匹配条件")) {
		t.Fatal("DNS table columns moved")
	}
	f.dragHeader("查询出口", "规则名称")
	dns := slices.Clone(f.w.columnOrder().Order)
	f.click("流量规则")
	f.tt.Frame()
	if got := f.w.columnOrder().Order; slices.Equal(got, dns) || !slices.Equal(got, []string{"index", "action", "match", "target", "name"}) {
		t.Fatalf("traffic order %v after DNS %v", got, dns)
	}
}

func TestColumnOrderPersists(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	path := filepath.Join(t.TempDir(), "prefs.json")
	f.env.Prefs = env.NewPrefs(path)
	f.dragHeader("规则名称", "目标出口")
	var saved []string
	if !env.NewPrefs(path).Load(prefTrafficColumns, &saved) || !slices.Equal(saved, []string{"index", "action", "match", "target", "name"}) {
		t.Fatalf("saved %v", saved)
	}
	if env.NewPrefs(path).Load(prefDNSColumns, &saved) {
		t.Fatal("the DNS table's key is written")
	}
	// A new launch reads it back, and ignores IDs it does not know.
	f.env.Prefs = env.NewPrefs(path)
	f.env.Prefs.Save(prefDNSColumns, []string{"gone", "query", "name"})
	w := New(f.env, f.store)
	if got := w.columnOrder().Indexes(columnIDs(sectionTraffic)); !slices.Equal(got, []int{0, 1, 3, 4, 2}) {
		t.Fatalf("restored %v", got)
	}
	w.sec = sectionDNS
	if got := w.columnOrder().Indexes(columnIDs(sectionDNS)); !slices.Equal(got, []int{5, 2, 0, 1, 3, 4}) {
		t.Fatalf("restored DNS %v", got)
	}
}

func TestColumnKeyboardMoveAndReset(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	if f.tt.HasText("恢复默认列") {
		t.Fatal("reset shows with the default order")
	}
	if err := f.tt.Click("移动规则名称列"); err != nil {
		t.Fatal(err)
	}
	f.tt.Key(ui.Alt, ui.KeyRight)
	f.tt.Frame()
	if got := f.w.columnOrder().Order; !slices.Equal(got, []string{"index", "action", "match", "name", "target"}) {
		t.Fatalf("alt+right: %v", got)
	}
	f.click("恢复默认列")
	f.tt.Frame()
	if !f.w.columnOrder().Confirm.Open {
		t.Fatal("reset does not ask")
	}
	f.click("恢复")
	f.tt.Frame()
	if len(f.w.columnOrder().Order) != 0 || x(f, "规则名称") > x(f, "匹配条件") {
		t.Fatalf("reset: %v", f.w.columnOrder().Order)
	}
}

func TestResetButtonCoversNoHeader(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	// Move the narrow action column last.
	f.dragHeader("动作", "目标出口")
	btn, ok := f.tt.Find("恢复默认列")
	if !ok {
		t.Fatalf("no reset button: %q", f.tt.Texts())
	}
	for _, title := range []string{"动作", "匹配条件", "目标出口", "规则名称"} {
		r, _ := f.tt.Find(title)
		if r.X+r.W > btn.X {
			t.Errorf("reset button at %v covers %q at %v..%v", btn.X, title, r.X, r.X+r.W)
		}
	}
	// A restored order gives the room back.
	f.click("恢复默认列")
	f.click("恢复")
	f.tt.Frame()
	if f.tt.HasText("恢复默认列") {
		t.Fatal("the button stays after restoring")
	}
}

// grip is the column edge of the header titled title.
func (f *fixture) grip(title string) ui.Rect {
	f.t.Helper()
	r, ok := f.tt.Find("调整" + title + "列宽")
	if !ok {
		f.t.Fatalf("no grip of %q: %q", title, f.tt.Texts())
	}
	return r
}

// dragGrip drags the right edge of the column titled title by dx.
func (f *fixture) dragGrip(title string, dx float32) {
	f.t.Helper()
	r := f.grip(title)
	x, y := r.X+r.W/2, r.Y+r.H/2
	f.tt.Press(x, y)
	for i := 1; i <= 8; i++ {
		f.tt.Move(x+dx*float32(i)/8, y)
	}
	f.tt.Release(x+dx, y)
	f.tt.Frame()
}

// textWith is the shown text containing part.
func (f *fixture) textWith(part string) string {
	f.t.Helper()
	for _, s := range f.tt.Texts() {
		if strings.Contains(s, part) {
			return s
		}
	}
	f.t.Fatalf("no text with %q: %q", part, f.tt.Texts())
	return ""
}

// columnWidth is the width the header box of the column titled title
// shows at: from its title, inside the cell's padding, to its edge.
func (f *fixture) columnWidth(title string) float32 {
	g := f.grip(title)
	return g.X + g.W/2 - (x(f, title) - kit.TableCellInset)
}

func near(a, b float32) bool { return a-b < 1.5 && b-a < 1.5 }

func TestDragColumnEdgeResizesHeaderAndCells(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	requests := len(f.srv.Requests())
	summary := f.textWith("github.com")
	// The edge between headers is the column cursor's, as in Connections.
	g := f.grip("规则名称")
	if g.H < 30 {
		t.Errorf("grip %v high, want the header's height", g.H)
	}
	f.tt.Move(g.X+g.W/2, g.Y+g.H/2)
	f.tt.Frame()
	if got := f.tt.Cursor(); got != ui.CursorResizeColumn {
		t.Errorf("cursor %v over the edge", got)
	}
	match, target := x(f, "匹配条件"), x(f, "目标出口")
	cell, pinned, out := x(f, summary), x(f, "此设备的其余连接"), x(f, "美国节点")
	// aligned reports whether the Match Conditions and Target Outbound cells keep their
	// places in their columns' headers.
	aligned := func() bool {
		return near(x(f, summary)-x(f, "匹配条件"), cell-match) && near(x(f, "此设备的其余连接")-x(f, "匹配条件"), pinned-match) &&
			near(x(f, "美国节点")-x(f, "目标出口"), out-target)
	}
	f.dragGrip("规则名称", 80)
	if got := f.w.columnWidths().Widths["name"]; !near(got, 250) {
		t.Fatalf("规则名称 dragged to %v, want 250", got)
	}
	if got := f.columnWidth("规则名称"); !near(got, 250) {
		t.Errorf("规则名称 header %v wide", got)
	}
	// The header and every row move alike: Match Conditions starts 80 later and,
	// sharing the room left, ends where it did.
	if d := x(f, "匹配条件") - match; !near(d, 80) {
		t.Errorf("匹配条件 header moved %v", d)
	}
	if d := x(f, summary) - cell; !near(d, 80) {
		t.Errorf("rule's 匹配条件 cell moved %v", d)
	}
	if d := x(f, "此设备的其余连接") - pinned; !near(d, 80) {
		t.Errorf("默认出口's 匹配条件 cell moved %v", d)
	}
	if !aligned() {
		t.Error("cells left their headers")
	}
	// Target Outbound ends where it did, before the reset button's slot.
	if !near(x(f, "目标出口"), target-28) || !near(x(f, "美国节点"), out-28) {
		t.Errorf("目标出口 moved: header %v → %v, cell %v → %v", target, x(f, "目标出口"), out, x(f, "美国节点"))
	}
	// A drag of the shared column fixes its width, as in Connections.
	f.dragGrip("匹配条件", -60)
	m := f.w.columnWidths().Widths["match"]
	if m <= 0 || !near(f.columnWidth("匹配条件"), m) {
		t.Fatalf("匹配条件 dragged to %v, header %v", m, f.columnWidth("匹配条件"))
	}
	if !near(x(f, "目标出口")-x(f, "匹配条件"), m) || !aligned() {
		t.Errorf("目标出口 header %v after 匹配条件 %v; cells aligned %v", x(f, "目标出口")-x(f, "匹配条件"), m, aligned())
	}
	// Columns keep a least width: 60, or the default of a narrower one.
	f.dragGrip("规则名称", -400)
	if got := f.w.columnWidths().Widths["name"]; got != 60 {
		t.Errorf("规则名称 narrowed to %v, want 60", got)
	}
	f.dragGrip("动作", -30)
	if got := f.w.columnWidths().Widths["action"]; got != 44 {
		t.Errorf("动作 narrowed to %v, want its 44", got)
	}
	// Dragging edges neither moves columns nor saves the document.
	if f.w.columnOrder().Custom(columnIDs(sectionTraffic)) {
		t.Fatalf("an edge drag moved columns: %v", f.w.columnOrder().Order)
	}
	if f.w.editor != nil || f.w.fallback != nil {
		t.Fatal("an edge drag opened an editor")
	}
	for _, r := range f.srv.Requests()[requests:] {
		if strings.HasPrefix(r, "PUT") {
			t.Fatalf("an edge drag saved the document: %q", f.srv.Requests())
		}
	}
	// A double click restores the column's default.
	f.tt.Advance(time.Second)
	g = f.grip("规则名称")
	f.tt.ClickAt(g.X+g.W/2, g.Y+g.H/2)
	f.tt.ClickAt(g.X+g.W/2, g.Y+g.H/2)
	f.tt.Frame()
	if _, ok := f.w.columnWidths().Widths["name"]; ok {
		t.Errorf("double click kept %v", f.w.columnWidths().Widths)
	}
}

func TestColumnWidthsPerTableAndPersist(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	path := filepath.Join(t.TempDir(), "prefs.json")
	f.env.Prefs = env.NewPrefs(path)
	f.dragGrip("规则名称", 80)
	var saved map[string]float32
	if !env.NewPrefs(path).Load(prefTrafficWidths, &saved) || !near(saved["name"], 250) || len(saved) != 1 {
		t.Fatalf("saved %v", saved)
	}
	if env.NewPrefs(path).Load(prefDNSWidths, &saved) {
		t.Fatal("the DNS table's key is written")
	}
	f.click("DNS 分流")
	testkit.UntilText(t, f.tt, "查询出口")
	if len(f.w.columnWidths().Widths) != 0 || !near(f.columnWidth("规则名称"), 170) {
		t.Fatalf("DNS table takes the traffic widths: %v", f.w.columnWidths().Widths)
	}
	f.dragGrip("查询出口", 40)
	if got := f.w.columnWidths().Widths["query"]; !near(got, 200) {
		t.Fatalf("查询出口 dragged to %v", got)
	}
	f.click("流量规则")
	testkit.UntilText(t, f.tt, "Apple 服务")
	if got := f.w.columnWidths().Widths; len(got) != 1 || !near(got["name"], 250) {
		t.Fatalf("traffic widths %v after DNS", got)
	}
	// A new launch reads both back, the header at the saved width.
	f.env.Prefs = env.NewPrefs(path)
	f.w = New(f.env, f.store)
	f.tt.Frame()
	testkit.UntilText(t, f.tt, "Apple 服务")
	if got := f.columnWidth("规则名称"); !near(got, 250) {
		t.Fatalf("restored 规则名称 %v wide", got)
	}
	f.w.sec = sectionDNS
	if got := f.w.columnWidths().Widths; len(got) != 1 || !near(got["query"], 200) {
		t.Fatalf("restored DNS widths %v", got)
	}
}

func TestColumnWidthFollowsColumnOrder(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.dragGrip("规则名称", 80)
	off := x(f, "默认出口") - x(f, "规则名称")
	outOff := x(f, "美国节点") - x(f, "目标出口")
	// Moving the column keeps its width, and the widths keep the order.
	f.dragHeader("规则名称", "目标出口")
	if got := f.w.columnOrder().Order; !slices.Equal(got, []string{"index", "action", "match", "target", "name"}) {
		t.Fatalf("order %v", got)
	}
	if got := f.columnWidth("规则名称"); !near(got, 250) || !near(f.w.columnWidths().Widths["name"], 250) {
		t.Fatalf("moved 规则名称 %v wide, widths %v", got, f.w.columnWidths().Widths)
	}
	if !near(x(f, "默认出口")-x(f, "规则名称"), off) || !near(x(f, "美国节点")-x(f, "目标出口"), outOff) {
		t.Fatalf("cells stay behind: name %v under %v, target %v under %v", x(f, "默认出口"), x(f, "规则名称"), x(f, "美国节点"), x(f, "目标出口"))
	}
	// Widening another column keeps the order.
	f.dragGrip("目标出口", 30)
	if got := f.w.columnOrder().Order; !slices.Equal(got, []string{"index", "action", "match", "target", "name"}) {
		t.Fatalf("an edge drag moved columns: %v", got)
	}
	if got := f.columnWidth("目标出口"); !near(got, 180) {
		t.Fatalf("目标出口 %v wide", got)
	}
}

func TestColumnEdgeKeyboard(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	// Left and Right on the focused edge narrow and widen its column; the
	// order stays.
	g := f.grip("规则名称")
	f.tt.ClickAt(g.X+g.W/2, g.Y+g.H/2)
	f.tt.Frame()
	if !f.tt.Focused("调整规则名称列宽") {
		t.Fatal("a click does not focus the edge")
	}
	f.tt.Key(0, ui.KeyRight)
	f.tt.Key(0, ui.KeyRight)
	f.tt.Frame()
	if got := f.w.columnWidths().Widths["name"]; !near(got, 202) {
		t.Fatalf("right ×2: %v", got)
	}
	f.tt.Key(0, ui.KeyLeft)
	f.tt.Frame()
	if got := f.w.columnWidths().Widths["name"]; !near(got, 186) {
		t.Fatalf("left: %v", got)
	}
	f.tt.Key(ui.Alt, ui.KeyRight)
	f.tt.Frame()
	if len(f.w.columnOrder().Order) != 0 || !near(f.w.columnWidths().Widths["name"], 186) {
		t.Fatalf("alt+right on the edge: order %v, widths %v", f.w.columnOrder().Order, f.w.columnWidths().Widths)
	}
	// Alt+Right on the header moves the column and keeps the widths.
	if err := f.tt.Click("移动规则名称列"); err != nil {
		t.Fatal(err)
	}
	f.tt.Key(ui.Alt, ui.KeyRight)
	f.tt.Frame()
	if got := f.w.columnOrder().Order; !slices.Equal(got, []string{"index", "action", "match", "name", "target"}) {
		t.Fatalf("alt+right: %v", got)
	}
	if got := f.w.columnWidths().Widths; len(got) != 1 || !near(got["name"], 186) {
		t.Fatalf("moving changed widths %v", got)
	}
}

func TestResetRestoresOrderAndWidths(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	path := filepath.Join(t.TempDir(), "prefs.json")
	f.env.Prefs = env.NewPrefs(path)
	if f.tt.HasText("恢复默认列") {
		t.Fatal("reset shows with the default columns")
	}
	// A width alone brings the button.
	f.dragGrip("规则名称", 80)
	btn, ok := f.tt.Find("恢复默认列")
	if !ok {
		t.Fatalf("no reset button after a width change: %q", f.tt.Texts())
	}
	for _, title := range []string{"动作", "规则名称", "匹配条件", "目标出口"} {
		r, _ := f.tt.Find(title)
		if r.X+r.W > btn.X {
			t.Errorf("reset button at %v covers %q at %v..%v", btn.X, title, r.X, r.X+r.W)
		}
	}
	f.click("恢复默认列")
	f.tt.Frame()
	if !f.tt.HasText("恢复默认列顺序和宽度？") {
		t.Fatalf("no confirmation: %q", f.tt.Texts())
	}
	f.click("恢复")
	f.tt.Frame()
	if len(f.w.columnWidths().Widths) != 0 || !near(f.columnWidth("规则名称"), 170) || f.tt.HasText("恢复默认列") {
		t.Fatalf("reset kept widths %v", f.w.columnWidths().Widths)
	}
	var saved map[string]float32
	if env.NewPrefs(path).Load(prefTrafficWidths, &saved) && len(saved) != 0 {
		t.Fatalf("saved widths after reset %v", saved)
	}
	// An order and a width restore together.
	f.dragGrip("目标出口", 40)
	f.dragHeader("规则名称", "目标出口")
	f.click("恢复默认列")
	f.click("恢复")
	f.tt.Frame()
	if len(f.w.columnOrder().Order) != 0 || len(f.w.columnWidths().Widths) != 0 {
		t.Fatalf("reset kept order %v, widths %v", f.w.columnOrder().Order, f.w.columnWidths().Widths)
	}
	var order []string
	if env.NewPrefs(path).Load(prefTrafficColumns, &order) && len(order) != 0 {
		t.Fatalf("saved order after reset %v", order)
	}
	if x(f, "规则名称") > x(f, "匹配条件") || !near(f.columnWidth("目标出口"), 150) {
		t.Fatal("columns not restored")
	}
	// The DNS table's own reset leaves the traffic table alone.
	f.dragGrip("规则名称", 30)
	f.click("DNS 分流")
	testkit.UntilText(t, f.tt, "查询出口")
	if f.tt.HasText("恢复默认列") {
		t.Fatal("the DNS table shows the traffic table's reset")
	}
}

// The last column's edge, beside the reset button, takes the pointer
// across its width and drags again once the button shows.
func TestLastColumnEdgeBesideResetButton(t *testing.T) {
	f := newFixture(t, nil)
	testkit.UntilText(t, f.tt, "Apple 服务")
	f.dragGrip("目标出口", -40)
	g := f.grip("目标出口")
	btn, ok := f.tt.Find("恢复默认列")
	if !ok || btn.X < g.X+g.W {
		t.Fatalf("reset button %v overlaps the edge %v", btn, g)
	}
	for _, dx := range []float32{-5, 0, 5} {
		f.tt.Move(g.X+g.W/2+dx, g.Y+g.H/2)
		f.tt.Frame()
		if got := f.tt.Cursor(); got != ui.CursorResizeColumn {
			t.Errorf("cursor %v at %v from the edge", got, dx)
		}
	}
	f.dragGrip("目标出口", -20)
	f.dragGrip("目标出口", 30)
	if got := f.w.columnWidths().Widths["target"]; got != 120 {
		t.Fatalf("目标出口 %v wide, want 120", got)
	}
}
