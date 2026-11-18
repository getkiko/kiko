package kit

import "github.com/egoist/mygo/ui"

// MenuFreeze holds what a list shows still while a context menu of one
// of its rows shows, so that its rows, the filter pane's counts and the
// segment numbers stay where the menu opened; the data goes on arriving
// and shows as the menu closes. T is the list's derived view.
//
// Each pass, call Begin before reading the view, read it through Hold,
// wrap the table's Menu with Menu, and call End after building the
// table:
//
//	m.freeze.Begin()
//	d := m.freeze.Hold(m.derive)
//	kit.Table(c, …, kit.TableOptions{Menu: m.freeze.Menu(m.rowMenu), …}, …)
//	m.freeze.End(c)
//
// A popover menu builds its items only in the passes it shows, and as
// one is chosen: a pass that built none after one that did is the menu
// closed.
type MenuFreeze[T any] struct {
	// frozen tells that the menu showed in the last pass; seen that it
	// showed in this one.
	frozen, seen bool
	held         T
	ok           bool
}

// Begin starts a pass of the view.
func (f *MenuFreeze[T]) Begin() {
	f.frozen, f.seen = f.seen, false
}

// Frozen reports whether a row's menu shows: the view holds still. A
// list whose data arrives as whole answers can keep them aside while
// frozen, and take the last one in once it is not.
func (f *MenuFreeze[T]) Frozen() bool { return f.frozen }

// Hold is the view to show: live's while no menu shows, else the one it
// last gave. live is not called while held.
func (f *MenuFreeze[T]) Hold(live func() T) T {
	if f.frozen && f.ok {
		return f.held
	}
	f.held, f.ok = live(), true
	return f.held
}

// Menu wraps a table's row menu, noting the passes it shows in.
func (f *MenuFreeze[T]) Menu(build func(m *ui.Menu, row int)) func(m *ui.Menu, row int) {
	return func(m *ui.Menu, row int) {
		f.seen = true
		build(m, row)
	}
}

// End ends a pass: a menu that closed gets another pass at once, to show
// the list as it is now.
func (f *MenuFreeze[T]) End(c *ui.Context) {
	if f.frozen && !f.seen {
		f.frozen = false
		c.AnimationFrame()
	}
}
