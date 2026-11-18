package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Escape in a search field empties it; in an empty field it goes on to
// the window's shortcut, as a subpage returning. TextFocused tells the
// field has the focus.
func TestSearchEscapeEmptiesThenPasses(t *testing.T) {
	query, escaped, focused := "", 0, false
	tt := ui.NewTester(func(c *ui.Context) {
		Apply(c)
		BeginTextFocus(c)
		Search(c, "search", &query, SearchOptions{Placeholder: "搜索", Width: 220})
		focused = TextFocused(c)
		if c.Shortcut(0, ui.KeyEscape) {
			escaped++
		}
	}, 400, 200)
	tt.Frame()
	if focused {
		t.Fatal("text focus before a click")
	}
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !focused {
		t.Fatal("no text focus in the field")
	}
	tt.Type("abc")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if query != "" || escaped != 0 {
		t.Fatalf("first Escape: query %q, window got %d", query, escaped)
	}
	if !tt.Focused("搜索") {
		t.Fatal("the field lost the focus")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if escaped != 1 {
		t.Fatalf("second Escape: window got %d", escaped)
	}
}
