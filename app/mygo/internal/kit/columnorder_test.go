package kit

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestColumnOrderLogic(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	var o ColumnOrder
	if o.Custom(ids) || !slices.Equal(o.Indexes(ids), []int{0, 1, 2, 3}) {
		t.Fatal("zero value is not the default order")
	}
	if !o.Move(ids, "d", 0) || !slices.Equal(o.Order, []string{"d", "a", "b", "c"}) {
		t.Fatalf("move to front: %v", o.Order)
	}
	if !o.Custom(ids) {
		t.Fatal("moved order is not custom")
	}
	if o.Move(ids, "d", 0) || o.Move(ids, "zz", 1) {
		t.Fatal("no-op moves report a change")
	}
	// Past the end holds at the end.
	o.Move(ids, "d", 99)
	if !slices.Equal(o.Order, []string{"a", "b", "c", "d"}) {
		t.Fatalf("move to end: %v", o.Order)
	}
	o.Move(ids, "b", 2)
	o.Reset()
	if o.Custom(ids) || len(o.Order) != 0 {
		t.Fatal("reset keeps an order")
	}
}

func TestColumnOrderToleratesOldData(t *testing.T) {
	ids := []string{"a", "b", "c"}
	// Unknown, repeated and missing IDs.
	o := ColumnOrder{Order: []string{"x", "c", "c", "a", "gone"}}
	if got := o.Indexes(ids); !slices.Equal(got, []int{2, 0, 1}) {
		t.Fatalf("indexes %v", got)
	}
	o.Move(ids, "b", 0)
	if !slices.Equal(o.Order, []string{"b", "c", "a"}) {
		t.Fatalf("move on old data: %v", o.Order)
	}
	cols := (&ColumnOrder{Order: []string{"b"}}).Arrange([]Column{{Title: "a"}, {ID: "b", Title: "B"}, {Title: "c"}})
	if cols[0].Title != "B" || cols[1].Title != "a" || cols[2].Title != "c" {
		t.Fatalf("arrange %v", cols)
	}
}

type orderFixture struct {
	table   TableState
	order   ColumnOrder
	changes int
	tt      *ui.Tester
}

func newOrderFixture() *orderFixture {
	f := &orderFixture{}
	f.table.Selected = -1
	cols := []Column{{ID: "one", Title: "第一列", Width: 140}, {ID: "two", Title: "第二列", Width: 140, Sortable: true}, {ID: "three", Title: "第三列", Width: 140}}
	f.tt = ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface)
		Table(c, &f.table, cols, 2, TableOptions{Label: "表", ColumnOrder: &f.order, OnReorder: func() { f.changes++ }}, func(row, col int) {
			ui.Text(c, []string{"甲", "乙", "丙"}[col])
		})
	}, 600, 300)
	f.tt.Frame()
	return f
}

func (f *orderFixture) xs() []float32 {
	var xs []float32
	for _, s := range []string{"甲", "乙", "丙"} {
		r, _ := f.tt.Find(s)
		xs = append(xs, r.X)
	}
	return xs
}

func (f *orderFixture) drag(from, to string, dropping *bool) {
	a, _ := f.tt.Find(from)
	b, _ := f.tt.Find(to)
	y := a.Y + a.H/2
	f.tt.Press(a.X+a.W/2, y)
	steps := 10
	for i := 1; i <= steps; i++ {
		f.tt.Move(a.X+a.W/2+(b.X+b.W-a.X-a.W/2)*float32(i)/float32(steps), y)
		if dropping != nil && f.order.Dropping() {
			*dropping = true
		}
	}
	f.tt.Release(b.X+b.W, y)
	f.tt.Frame()
}

func TestTableDragHeaderReorders(t *testing.T) {
	f := newOrderFixture()
	dropped := false
	f.drag("第一列", "第三列", &dropped)
	if !dropped {
		t.Error("no drop line while dragging")
	}
	if f.changes != 1 || !slices.Equal(f.order.Order, []string{"two", "three", "one"}) {
		t.Fatalf("order %v, changes %d", f.order.Order, f.changes)
	}
	xs := f.xs()
	// Cells follow the headers: columns 1, 2, then 0.
	if !(xs[1] < xs[2] && xs[2] < xs[0]) {
		t.Fatalf("cell positions %v", xs)
	}
}

func TestTableDragCancelsOffTheHeader(t *testing.T) {
	f := newOrderFixture()
	a, _ := f.tt.Find("第一列")
	y := a.Y + a.H/2
	f.tt.Press(a.X+a.W/2, y)
	for i := 1; i <= 10; i++ {
		f.tt.Move(a.X+a.W/2+30*float32(i), y+10*float32(i))
	}
	f.tt.Release(a.X+300, y+100)
	f.tt.Frame()
	if f.changes != 0 || len(f.order.Order) != 0 {
		t.Fatalf("a drag off the header reorders: %v", f.order.Order)
	}
}

func TestTableDragDoesNotSort(t *testing.T) {
	f := newOrderFixture()
	f.drag("第二列", "第三列", nil)
	if f.table.Sort.Column != "" {
		t.Fatalf("a drag sorts: %q", f.table.Sort.Column)
	}
	if !slices.Equal(f.order.Order, []string{"one", "three", "two"}) {
		t.Fatalf("order %v", f.order.Order)
	}
	// A plain click still sorts.
	f.order.Reset()
	f.tt.Frame()
	if err := f.tt.Click("按第二列排序"); err != nil {
		t.Fatal(err)
	}
	if f.table.Sort.Column != "two" {
		t.Fatalf("click does not sort: %q", f.table.Sort.Column)
	}
}

func TestTableKeyboardMovesColumn(t *testing.T) {
	f := newOrderFixture()
	if err := f.tt.Click("移动第一列列"); err != nil {
		t.Fatal(err)
	}
	f.tt.Key(ui.Alt, ui.KeyRight)
	if !slices.Equal(f.order.Order, []string{"two", "one", "three"}) {
		t.Fatalf("alt+right: %v", f.order.Order)
	}
	f.tt.Key(ui.Alt, ui.KeyLeft)
	if f.order.Custom([]string{"one", "two", "three"}) {
		t.Fatalf("alt+left does not move back: %v", f.order.Order)
	}
}

func TestTableResetOrderConfirms(t *testing.T) {
	f := newOrderFixture()
	if f.tt.HasText("恢复默认列") {
		t.Fatal("reset shows with the default order")
	}
	f.order.Move([]string{"one", "two", "three"}, "one", 2)
	f.tt.Frame()
	if err := f.tt.Click("恢复默认列"); err != nil {
		t.Fatal(err)
	}
	if !f.order.Confirm.Open || !f.order.Custom([]string{"one", "two", "three"}) {
		t.Fatal("the click restores without asking")
	}
	if err := f.tt.Click("恢复"); err != nil {
		t.Fatal(err)
	}
	if f.order.Custom([]string{"one", "two", "three"}) || f.changes != 1 {
		t.Fatalf("confirm does not restore: %v, %d", f.order.Order, f.changes)
	}
}
