package kit

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

type widthFixture struct {
	table            TableState
	order            ColumnOrder
	resizes, reorder int
	tt               *ui.Tester
}

func newWidthFixture() *widthFixture {
	f := &widthFixture{}
	f.table.Selected = -1
	cols := []Column{{ID: "one", Title: "第一列", Width: 140}, {ID: "icon", Title: "图标", Width: 44}, {ID: "rest", Title: "其余", MinWidth: 120}}
	f.tt = ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface)
		Table(c, &f.table, cols, 2, TableOptions{Label: "表", Resizable: true, OnResize: func() { f.resizes++ },
			ColumnOrder: &f.order, OnReorder: func() { f.reorder++ }}, func(row, col int) {
			ui.Text(c, []string{"甲", "乙", "丙"}[col])
		})
	}, 600, 300)
	f.tt.Frame()
	return f
}

func (f *widthFixture) drag(t *testing.T, label string, dx float32) {
	t.Helper()
	r, ok := f.tt.Find(label)
	if !ok {
		t.Fatalf("no %s: %q", label, f.tt.Texts())
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	f.tt.Press(x, y)
	f.tt.Move(x+dx/2, y)
	f.tt.Move(x+dx, y)
	f.tt.Release(x+dx, y)
	f.tt.Frame()
}

func TestColumnWidthsWidthAndCustom(t *testing.T) {
	var w ColumnWidths
	col := Column{ID: "a", Width: 90}
	if w.Width(col) != 90 || w.Custom([]string{"a"}) {
		t.Fatal("empty widths change the column")
	}
	w.Widths = map[string]float32{"a": 120, "gone": 300}
	if w.Width(col) != 120 || !w.Custom([]string{"a"}) || w.Custom([]string{"b"}) {
		t.Fatalf("widths %v", w.Widths)
	}
}

// A column narrower than the least drag width keeps its own as its least;
// the others keep minColumnWidth.
func TestColumnGripLeastWidth(t *testing.T) {
	f := newWidthFixture()
	f.drag(t, "调整图标列宽", -30)
	if got := f.table.Widths["icon"]; got != 44 {
		t.Fatalf("图标 narrowed to %v, want its 44", got)
	}
	f.drag(t, "调整第一列列宽", -200)
	if got := f.table.Widths["one"]; got != minColumnWidth {
		t.Fatalf("第一列 narrowed to %v", got)
	}
	f.drag(t, "调整图标列宽", 20)
	if got := f.table.Widths["icon"]; got != 64 {
		t.Fatalf("图标 widened to %v", got)
	}
	if f.resizes != 3 || f.order.Custom([]string{"one", "icon", "rest"}) {
		t.Fatalf("resizes %d, order %v", f.resizes, f.order.Order)
	}
}

// With widths bound, the order's reset shows for a width alone and
// restores both.
func TestResetRestoresWidthsToo(t *testing.T) {
	f := newWidthFixture()
	if f.tt.HasText("恢复默认列") {
		t.Fatal("reset shows with the default columns")
	}
	f.drag(t, "调整第一列列宽", 40)
	if err := f.tt.Click("恢复默认列"); err != nil {
		t.Fatalf("no reset after a width change: %v", err)
	}
	if !f.tt.HasText("恢复默认列顺序和宽度？") {
		t.Fatalf("confirmation %q", f.tt.Texts())
	}
	f.order.Move([]string{"one", "icon", "rest"}, "one", 2)
	if err := f.tt.Click("恢复"); err != nil {
		t.Fatal(err)
	}
	f.tt.Frame()
	if len(f.table.Widths) != 0 || len(f.order.Order) != 0 || f.resizes != 2 || f.reorder != 1 {
		t.Fatalf("widths %v, order %v, resizes %d, reorders %d", f.table.Widths, f.order.Order, f.resizes, f.reorder)
	}
	if f.tt.HasText("恢复默认列") {
		t.Fatal("the button stays after restoring")
	}
	// Widths follow their columns' IDs as they move.
	f.drag(t, "调整第一列列宽", 40)
	f.order.Move([]string{"one", "icon", "rest"}, "one", 2)
	f.tt.Frame()
	if !slices.Equal(f.order.Order, []string{"icon", "rest", "one"}) || f.table.Widths["one"] != 180 {
		t.Fatalf("order %v, widths %v", f.order.Order, f.table.Widths)
	}
	r, _ := f.tt.Find("调整第一列列宽")
	head, _ := f.tt.Find("第一列")
	if got := r.X + r.W/2 - (head.X - TableCellInset); got < 178 || got > 182 {
		t.Fatalf("moved 第一列 shows %v wide", got)
	}
}

// The last column's edge takes the pointer beside the reset button.
func TestLastGripBesideResetButton(t *testing.T) {
	f := newWidthFixture()
	f.drag(t, "调整第一列列宽", 20)
	g, _ := f.tt.Find("调整其余列宽")
	btn, ok := f.tt.Find("恢复默认列")
	if !ok || btn.X < g.X+g.W {
		t.Fatalf("reset button %v overlaps the edge %v", btn, g)
	}
	f.tt.Move(g.X+g.W/2, g.Y+g.H/2)
	f.tt.Frame()
	if got := f.tt.Cursor(); got != ui.CursorResizeColumn {
		t.Fatalf("cursor %v on the last edge", got)
	}
}
