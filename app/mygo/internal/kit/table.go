package kit

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

// Column is a column of a Table.
type Column struct {
	// ID identifies the column for sorting; the title when empty.
	ID    string
	Title string
	// Width is fixed; 0 shares the room the fixed columns leave, at least
	// MinWidth.
	Width    float32
	MinWidth float32
	// Grow weighs a shared column's share, 1 when 0.
	Grow     float32
	Align    ui.Align
	Sortable bool
}

func (col Column) id() string {
	if col.ID != "" {
		return col.ID
	}
	return col.Title
}

// Density is a table's row height.
type Density int

const (
	// Compact rows are 32 DIPs, one line.
	Compact Density = iota
	// TwoLine rows are 48 DIPs, a primary and a secondary line.
	TwoLine
)

// TableState is what a table keeps between frames: its list's place and
// choice, and its sort order. Keep one in your state for each table.
type TableState struct {
	List ui.ListState
	// Selected is the chosen row, -1 for none.
	Selected int
	Sort     ui.SortOrder
	// ColumnWidths are the widths the user dragged the columns of a
	// Resizable table to (Widths, by column ID); save and restore Widths
	// across launches.
	ColumnWidths
	init bool
}

// TableOptions configure a Table.
type TableOptions struct {
	Label   string
	Density Density
	// Key identifies the item of a row, so the choice and the place follow
	// it as rows change.
	Key func(row int) any
	// Striped tints every other row.
	Striped bool
	// NoSelection lets clicks pass to the cells without choosing rows.
	NoSelection bool
	// Dimmed greys out a row, as a disabled rule.
	Dimmed func(row int) bool
	// Faded fades a row's cells as one, icons and badges included, as a
	// connection that has ended; the row's fills stay. A chosen row fades
	// less, to stay readable.
	Faded func(row int) bool
	// Menu builds a row's context menu.
	Menu func(m *ui.Menu, row int)
	// HasMenu tells that row has a context menu, for a Menu some rows
	// have nothing to add to; nil gives every row one.
	HasMenu func(row int) bool
	// Empty fills the table's area while it has no rows.
	Empty func()
	// Reorder lets rows be dragged, as ListState.Reorder.
	Reorder func(rows []int, to int)
	// KeepOffset keeps the scroll offset as rows re-sort, for live lists.
	KeepOffset bool
	// Flat draws hovered and chosen rows square, from edge to edge, the
	// chosen one in RowChosen with its text as it is.
	Flat bool
	// RowHeight sets a row's height, as a group title shorter than its
	// items; 0 keeps the density's.
	RowHeight func(row int) float32
	// Resizable lets each header's right edge be dragged to set its
	// column's width (ColumnGrip, kept in TableState.Widths; a double click
	// restores it). OnResize is told when a drag ends, to save the widths.
	Resizable bool
	OnResize  func()
	// ColumnOrder lets the headers be dragged (or moved with Alt+Left and
	// Alt+Right) to reorder the columns, keeping the order in it; a button
	// in the header restores the default order, and the default widths of
	// a Resizable table. cell and Widths keep the columns' default indexes
	// and IDs. OnReorder is told when the order changes, to save it.
	ColumnOrder *ColumnOrder
	OnReorder   func()
}

// Table shows n rows in columns under a sticky 32 DIP header, building
// only the rows in view. cell builds the content of a row's column,
// usually with Cell. Sortable headers set s.Sort; sort the rows by it.
// Read the choice from s.Selected; the element returned is the list, whose
// Changed reports a new choice and Submitted a double click or Enter.
func Table(c *ui.Context, s *TableState, columns []Column, n int, o TableOptions, cell func(row, col int)) ui.Element {
	k := T(c)
	if !s.init {
		s.Selected, s.init = -1, true
	}
	// Without a Key, a choice past the end is gone; with one, the list
	// follows the chosen item to where it moved first.
	if o.Key == nil && s.Selected >= n {
		s.Selected = -1
	}
	// declared are the columns as given, for the grips' limits.
	declared := columns
	if o.Resizable && len(s.Widths) > 0 {
		columns = slices.Clone(columns)
		for i := range columns {
			columns[i].Width = s.Width(columns[i])
		}
	}
	// The columns in the order shown, and each one's index for cell.
	var ord OrderView
	shown := make([]int, len(columns))
	for i := range shown {
		shown[i] = i
	}
	if o.ColumnOrder != nil {
		ids := make([]string, len(columns))
		for i, col := range columns {
			ids[i] = col.id()
		}
		ord = o.ColumnOrder.View(ids, o.OnReorder)
		if o.Resizable {
			ord = ord.WithWidths(&s.ColumnWidths, o.OnResize)
		}
		shown = ord.Order()
		arranged := make([]Column, len(columns))
		for i, at := range shown {
			arranged[i] = columns[at]
		}
		columns = arranged
	}
	rowHeight := float32(RowHeight)
	if o.Density == TwoLine {
		rowHeight = TwoLineRowH
	}
	var minRow float32
	for _, col := range columns {
		if col.Width > 0 {
			minRow += col.Width
		} else {
			minRow += max(col.MinWidth, 80)
		}
	}
	minRow += 2*TableInset + ord.Reserved()

	s.List.Key = o.Key
	s.List.KeepOffset = o.KeepOffset
	s.List.Reorder = o.Reorder
	if o.NoSelection {
		s.List.Selected = nil
	} else {
		s.List.Selected = &s.Selected
	}

	var list ui.Element
	ui.Column(c).Grow(1).MinHeight(0).MinWidth(0).Label(o.Label).Children(func() {
		head := ui.Row(c).Height(RowHeight).Shrink(0).Clip().BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).
			Background(k.Surface)
		head.Children(func() {
			// The cells fill the header's height: the grips at their right
			// edges span it, and their hairlines show between headers.
			ui.Row(c).MinWidth(minRow).FillWidth().FillHeight().PaddingX(TableInset).Children(func() {
				for i, col := range columns {
					var grip func(box ui.Element)
					if o.Resizable {
						base := declared[shown[i]]
						grip = func(box ui.Element) { ColumnGrip(c, &s.ColumnWidths, base, box, o.OnResize) }
					}
					headerCell(c, s, col, grip, ord, i)
				}
				ord.Reset(c)
			})
		})
		list = ui.List(c, &s.List, n).Grow(1).MinHeight(0).RowWidth(minRow).Label(o.Label)
		head.ScrollWith(list)
		if o.Flat {
			// The list rounds and fills its rows from the theme as they
			// build: a square, light one for this table's.
			theme := c.Theme()
			flat := *theme
			flat.Radius, flat.Accent, flat.AccentText = 0, k.RowChosen, k.Label
			c.SetTheme(&flat)
			defer c.SetTheme(theme)
		}
		list.Rows(func(r ui.ListRow) {
			row := r.Index
			height := rowHeight
			if o.RowHeight != nil {
				if h := o.RowHeight(row); h > 0 {
					height = h
				}
			}
			e := ui.Row(r.Context).Height(height).PaddingX(TableInset).FontSize(FontTable).TextColor(k.Label)
			// A hovered row shows the list's hover fill under it.
			if o.Striped && row%2 == 1 && !r.Selected() && !e.Hovered() {
				e.Background(k.Stripe)
			}
			if r.Selected() && o.Flat {
				e.Background(k.RowChosen)
			} else if r.Selected() {
				if r.ListFocused() {
					e.TextColor(k.OnAccent)
				} else {
					e.Background(k.AccentWeak)
				}
			} else if o.Dimmed != nil && o.Dimmed(row) {
				e.TextColor(k.Label4)
			}
			if o.Menu != nil && (o.HasMenu == nil || o.HasMenu(row)) {
				e.PopoverMenu(func(m *ui.Menu) { o.Menu(m, row) })
			}
			onAccent := r.Selected() && r.ListFocused() && !o.Flat
			opacity := float32(1)
			if o.Faded != nil && o.Faded(row) {
				opacity = FadedRowOpacity
				if r.Selected() {
					opacity = FadedChosenRowOpacity
				}
			}
			e.Children(func() {
				previous := rowOnAccent
				rowOnAccent = onAccent
				defer func() { rowOnAccent = previous }()
				for i, col := range columns {
					box := ui.Row(c).MinWidth(0).PaddingX(TableCellInset).Gap(6).FillHeight()
					if col.Width > 0 {
						box.Width(col.Width).Shrink(0)
					} else {
						grow := col.Grow
						if grow == 0 {
							grow = 1
						}
						box.Grow(grow).Basis(0).MinWidth(max(col.MinWidth, 80))
					}
					if opacity < 1 {
						box.Opacity(opacity)
					}
					box.Justify(col.Align).Children(func() { cell(row, shown[i]) })
				}
				ord.Spacer(c)
			})
		})
		// Rows has moved a keyed choice with its item: one still past the
		// end is gone.
		if s.Selected >= n {
			s.Selected = -1
		}
		if n == 0 && o.Empty != nil {
			list.Children(o.Empty)
		}
	})
	return list
}

// headerCell is col's header, with grip, when set, built in it last.
func headerCell(c *ui.Context, s *TableState, col Column, grip func(box ui.Element), ord OrderView, visual int) {
	k := T(c)
	reorder := ord.o != nil
	box := ui.Row(c.Key(col.id())).MinWidth(0).PaddingX(TableCellInset).Gap(3).FillHeight().FontSize(FontCaption).TextColor(k.Label3).Justify(col.Align).
		PassThrough()
	if col.Width > 0 {
		box.Width(col.Width).Shrink(0)
	} else {
		grow := col.Grow
		if grow == 0 {
			grow = 1
		}
		box.Grow(grow).Basis(0).MinWidth(max(col.MinWidth, 80))
	}
	box.Children(func() {
		if grip != nil {
			defer grip(box)
		}
		if !col.Sortable {
			if !reorder {
				ui.Text(c, col.Title).SingleLine().FontWeight(500)
				return
			}
			h := ui.ButtonBase(c).Label("移动" + col.Title + "列").MinWidth(24).Cursor(ui.CursorGrab)
			h.Children(func() { ui.Text(c, col.Title).SingleLine().FontWeight(500) })
			ord.Header(c, box, h, visual, col.Title)
			return
		}
		sorted := s.Sort.Column == col.id()
		b := ui.ButtonBase(c).Gap(3).Label("按" + col.Title + "排序").MinWidth(0).Transition(HoverMotion)
		if sorted {
			b.TextColor(k.Label).FontWeight(600)
		} else if b.Hovered() {
			b.TextColor(k.Label2)
		}
		b.Children(func() {
			ui.Text(c, col.Title).SingleLine()
			if sorted {
				arrow := SymChevronUp
				if s.Sort.Descending {
					arrow = SymChevronDown
				}
				Icon(c, arrow, 11)
			}
		})
		swallow := false
		if reorder {
			swallow = ord.Header(c, box, b, visual, col.Title)
		}
		if b.Clicked() && !swallow {
			if sorted {
				s.Sort.Descending = !s.Sort.Descending
			} else {
				s.Sort = ui.SortOrder{Column: col.id()}
			}
		}
	})
}

// rowOnAccent is set while the cells of a selected row of a focused table
// build, so their secondary text turns to the accent's text color. Views
// build on the UI thread only: every window, of whichever console, builds
// its frames on the main thread one after another.
var rowOnAccent bool

// Muted is the color of secondary text in a cell: the tertiary label, or
// the accent's text color on a selected row of a focused table.
func Muted(c *ui.Context) ui.Color {
	if rowOnAccent {
		return T(c).OnAccent.Alpha(0.8)
	}
	return T(c).Label3
}

// The opacities of a faded row's cells (TableOptions.Faded), and of a
// chosen one.
const (
	FadedRowOpacity       = 0.42
	FadedChosenRowOpacity = 0.7
)

// Cell is a table cell's text: a primary line and, in two-line tables, a
// secondary one below it. Long text ends with an ellipsis and shows whole
// in a hint (FullText).
func Cell(c *ui.Context, primary, secondary string) ui.Element {
	return ui.Column(c).MinWidth(0).Grow(1).Gap(1).Children(func() {
		Clipped(c, primary, FontTable, 400)
		if secondary != "" {
			Clipped(c, secondary, FontCaption, 400).TextColor(Muted(c))
		}
	})
}

// NumberCell is a right-aligned number in tabular digits.
func NumberCell(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).SingleLine().FontFeatures("tnum")
}

// Selected returns the chosen row of s when it is within n rows.
func (s *TableState) Chosen(n int) (int, bool) {
	if s.init && s.Selected >= 0 && s.Selected < n {
		return s.Selected, true
	}
	return -1, false
}

// Clear clears the choice.
func (s *TableState) Clear() { s.Selected = -1; s.init = true }
