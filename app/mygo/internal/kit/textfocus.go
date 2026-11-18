package kit

import "github.com/egoist/mygo/ui"

// textFocusKey keys the window's note of a focused text field.
type textFocusKey struct{}

func textFocus(c *ui.Context) *bool {
	return ui.Local(c.Root(), textFocusKey{}, func() bool { return false })
}

// noteTextFocus notes a text field that has the keyboard focus.
func noteTextFocus(c *ui.Context, in ui.Element) {
	if in.Focused() {
		*textFocus(c) = true
	}
}

// BeginTextFocus forgets the focus TextFocused reports: a view calls it
// before building the fields it then asks about.
func BeginTextFocus(c *ui.Context) { *textFocus(c) = false }

// TextFocused reports whether a text field of the kit (Input, Search,
// TextArea) built since BeginTextFocus has the keyboard focus, as a page
// whose Escape returns lets a first Escape leave the field instead.
func TextFocused(c *ui.Context) bool { return *textFocus(c) }
