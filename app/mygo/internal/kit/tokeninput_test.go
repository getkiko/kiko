package kit

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// tokenHarness is a TokenInput over a list of values: Paste adds the new
// ones, refusing "bad".
type tokenHarness struct {
	tt      *ui.Tester
	values  []string
	query   string
	problem string
	kind    string
}

func newTokenHarness(t *testing.T, values ...string) *tokenHarness {
	t.Helper()
	h := &tokenHarness{values: values}
	h.tt = ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface).Padding(20)
		tokens := make([]Token, len(h.values))
		for i, v := range h.values {
			tokens[i] = Token{ID: v, Label: v}
		}
		ui.Column(c).Width(420).Children(func() {
			TokenInput(c, "tokens", &h.query, TokenInputOptions{
				Label: "域名", Placeholder: "例如 example.com", Tokens: tokens, Error: h.problem, ClipKind: h.kind,
				Commit: func(string) bool {
					h.values = append(h.values, strings.TrimSpace(h.query))
					return true
				},
				Remove: func(id string) { h.values = slices.DeleteFunc(h.values, func(v string) bool { return v == id }) },
				RemoveAll: func(ids []string) {
					h.values = slices.DeleteFunc(h.values, func(v string) bool { return slices.Contains(ids, v) })
				},
				Paste: func(values []string) {
					h.problem = ""
					for _, v := range values {
						switch {
						case v == "bad":
							h.problem = "bad"
						case !slices.Contains(h.values, v):
							h.values = append(h.values, v)
						}
					}
				},
			})
		})
	}, 640, 400)
	h.tt.Frame()
	return h
}

// focus clicks the input after the chips.
func (h *tokenHarness) focus(t *testing.T) {
	t.Helper()
	if len(h.values) == 0 {
		if err := h.tt.Click("域名"); err != nil {
			t.Fatalf("focus: %v", err)
		}
	} else {
		r, ok := h.tt.Find(h.values[len(h.values)-1])
		if !ok {
			t.Fatalf("no chips: %q", h.tt.Texts())
		}
		h.tt.ClickAt(r.X+r.W+60, r.Y+r.H/2)
	}
	h.tt.Frame()
}

func (h *tokenHarness) clickChip(t *testing.T, mods ui.Modifiers, label string) {
	t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		t.Fatalf("no chip %q: %q", label, h.tt.Texts())
	}
	h.tt.ClickAtWith(mods, r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
}

func TestSplitTokens(t *testing.T) {
	got := SplitTokens(" a.com,b.com；c.com\n\n a.com\td.com 、e.com;")
	if want := []string{"a.com", "b.com", "c.com", "d.com", "e.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SplitTokens %q", got)
	}
	if got := SplitTokenLines("Home WiFi\nOffice 5G, Cafe"); !reflect.DeepEqual(got, []string{"Home WiFi", "Office 5G", "Cafe"}) {
		t.Errorf("SplitTokenLines %q", got)
	}
}

// TestTokenInputPastesSeveral pastes a list: each value becomes a chip,
// the repeated one once; one value pastes into the input as typed.
func TestTokenInputPastesSeveral(t *testing.T) {
	h := newTokenHarness(t, "a.com")
	h.focus(t)
	h.tt.SetClipboard("b.com, c.com\na.com; c.com")
	h.tt.Key(ui.Cmd, ui.KeyV)
	h.tt.Frame()
	h.tt.Frame()
	if want := []string{"a.com", "b.com", "c.com"}; !reflect.DeepEqual(h.values, want) || h.query != "" {
		t.Fatalf("values %q query %q", h.values, h.query)
	}
	h.tt.SetClipboard("d.com")
	h.tt.Key(ui.Cmd, ui.KeyV)
	h.tt.Frame()
	if h.query != "d.com" || len(h.values) != 3 {
		t.Fatalf("single paste: values %q query %q", h.values, h.query)
	}
}

// TestTokenInputCopies copies the chips chosen, all of them when none is,
// and pastes a copy back through its Clips.
func TestTokenInputCopies(t *testing.T) {
	h := newTokenHarness(t, "a.com", "b.com", "c.com")
	h.focus(t)
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.tt.Frame()
	if got := h.tt.Clipboard(); got != "a.com\nb.com\nc.com" {
		t.Fatalf("copy all %q", got)
	}
	h.clickChip(t, 0, "b.com")
	h.clickChip(t, ui.Cmd, "c.com")
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.tt.Frame()
	if got := h.tt.Clipboard(); got != "b.com\nc.com" {
		t.Fatalf("copy chosen %q", got)
	}
	h.clickChip(t, 0, "a.com")
	h.clickChip(t, ui.Shift, "c.com")
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.tt.Frame()
	if got := h.tt.Clipboard(); got != "a.com\nb.com\nc.com" {
		t.Fatalf("copy range %q", got)
	}
}

// TestTokenInputRemovesChosen chooses chips by click and Cmd+A, and
// Backspace removes them; Escape lets them go.
func TestTokenInputRemovesChosen(t *testing.T) {
	h := newTokenHarness(t, "a.com", "b.com", "c.com", "d.com")
	h.clickChip(t, 0, "b.com")
	h.clickChip(t, ui.Cmd, "d.com")
	h.tt.Key(0, ui.KeyBackspace)
	h.tt.Frame()
	h.tt.Frame()
	if want := []string{"a.com", "c.com"}; !reflect.DeepEqual(h.values, want) {
		t.Fatalf("after Backspace %q", h.values)
	}
	h.focus(t)
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDelete)
	h.tt.Frame()
	if len(h.values) != 2 {
		t.Fatalf("Delete after Escape removed %q", h.values)
	}
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Frame()
	h.tt.Key(0, ui.KeyDelete)
	h.tt.Frame()
	h.tt.Frame()
	if len(h.values) != 0 {
		t.Fatalf("Cmd+A, Delete left %q", h.values)
	}
}

// TestTokenInputMenu copies, pastes and clears from the context menu.
func TestTokenInputMenu(t *testing.T) {
	h := newTokenHarness(t, "a.com", "b.com")
	r, _ := h.tt.Find("b.com")
	h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	if err := h.tt.Click("复制"); err != nil {
		t.Fatalf("menu: %v; %q", err, h.tt.Texts())
	}
	h.tt.Frame()
	if got := h.tt.Clipboard(); got != "b.com" {
		t.Fatalf("menu copy %q", got)
	}
	h.tt.SetClipboard("x.com bad y.com")
	r, _ = h.tt.Find("a.com")
	h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	if err := h.tt.Click("粘贴"); err != nil {
		t.Fatalf("menu: %v; %q", err, h.tt.Texts())
	}
	h.tt.Frame()
	if want := []string{"a.com", "b.com", "x.com", "y.com"}; !reflect.DeepEqual(h.values, want) || h.problem != "bad" {
		t.Fatalf("menu paste %q problem %q", h.values, h.problem)
	}
	h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.tt.Frame()
	if err := h.tt.Click("清空"); err != nil {
		t.Fatalf("menu: %v; %q", err, h.tt.Texts())
	}
	h.tt.Frame()
	if len(h.values) != 0 {
		t.Fatalf("menu clear left %q", h.values)
	}
}
