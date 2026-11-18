package kit

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// codeHarness is a CodeEditor in a dialog-like panel with a button
// elsewhere and an Escape shortcut on the panel.
type codeHarness struct {
	t       *testing.T
	state   CodeState
	opts    CodeOptions
	tt      *ui.Tester
	escapes int
	other   int
	changed int
	focus   bool
}

func newCodeHarness(t *testing.T, o CodeOptions, text string, w, h int) *codeHarness {
	t.Helper()
	ch := &codeHarness{t: t, opts: o, focus: true}
	ch.state.SetText(text)
	ch.tt = ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface)
		ui.Overlay(c, func() {
			panel := ui.Column(c.Key("panel")).Absolute().Top(0).Left(0).Width(float32(w)).Height(float32(h)).Gap(8).Padding(12).
				Background(T(c).Surface)
			panel.Children(func() {
				if ch.focus {
					ch.focus = false
					ch.state.Focus()
				}
				CodeEditor(c, &ch.state, ch.opts).Grow(1)
				if ch.state.Changed() {
					ch.changed++
				}
				if Button(c, "其他", ButtonOptions{}).Clicked() {
					ch.other++
				}
			})
			// The shortcut dialogs close with (Dialog, SideSheet).
			if panel.OverlayShortcut(0, ui.KeyEscape) {
				ch.escapes++
			}
		})
	}, w, h)
	ch.tt.Frame()
	ch.tt.Frame()
	return ch
}

func TestCodeEditorRendersBothThemes(t *testing.T) {
	src := "// 注释\nfunction f(a) {\n  return \"x\" + 12;\n}\n"
	for _, dark := range []bool{false, true} {
		ch := newCodeHarness(t, CodeOptions{Language: LangJavaScript, Label: "脚本"}, src, 640, 360)
		ch.tt.SetDark(dark)
		ch.tt.Frame()
		if ch.state.Text() != src {
			t.Fatalf("dark=%v: text %q", dark, ch.state.Text())
		}
	}
	for _, lang := range []string{LangJSON, LangText, ""} {
		ch := newCodeHarness(t, CodeOptions{Language: lang, Wrap: true, NoLineNumbers: true, Placeholder: "…"}, `{"a":[1,2]}`, 400, 200)
		ch.tt.Frame()
	}
}

func TestCodeThemeContrast(t *testing.T) {
	// Comments read at 4.5:1 or better on the surface in both themes.
	for _, k := range []*Tokens{&light, &dark} {
		ct := codeTheme(k)
		for kind, c := range map[string]ui.Color{
			"comment": ct.TokenColor(editor.TokenComment), "keyword": ct.TokenColor(editor.TokenKeyword), "string": ct.TokenColor(editor.TokenString),
			"number": ct.TokenColor(editor.TokenNumber), "type": ct.TokenColor(editor.TokenType), "function": ct.TokenColor(editor.TokenFunction),
			"property": ct.TokenColor(editor.TokenProperty), "escape": ct.TokenColor(editor.TokenEscape),
		} {
			if r := contrast(c, k.Surface); r < 4.5 {
				t.Errorf("dark=%v %s contrast %.2f", k.Dark, kind, r)
			}
		}
	}
}

// contrast is the WCAG contrast ratio of two opaque colors.
func contrast(a, b ui.Color) float64 {
	lum := func(c ui.Color) float64 {
		f := func(v uint8) float64 {
			x := float64(v) / 255
			if x <= 0.03928 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*f(c.R) + 0.7152*f(c.G) + 0.0722*f(c.B)
	}
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestCodeStateTextRoundTrip(t *testing.T) {
	var s CodeState
	if s.Text() != "" {
		t.Fatal("zero state not empty")
	}
	s.SetText("a\nb")
	if s.Text() != "a\nb" {
		t.Fatalf("text %q", s.Text())
	}
	s.SetText("变量 = 1\r\nx")
	if got := s.Text(); got != "变量 = 1\nx" {
		t.Fatalf("text %q", got)
	}
}

func TestCodeStateResetClearsHistory(t *testing.T) {
	var s CodeState
	s.SetText("one")
	s.SetText("two")
	if !s.st.Buffer.CanUndo() {
		t.Fatal("SetText must be undoable")
	}
	s.Reset("three")
	if s.st.Buffer.CanUndo() || s.Text() != "three" {
		t.Fatalf("after Reset: undo=%v text=%q", s.st.Buffer.CanUndo(), s.Text())
	}
}

func TestCodeEditorChangedOnTypingOnly(t *testing.T) {
	ch := newCodeHarness(t, CodeOptions{Language: LangJavaScript}, "let a", 640, 300)
	if ch.changed != 0 {
		t.Fatalf("changed %d before typing", ch.changed)
	}
	ch.tt.Type("!")
	ch.tt.Frame()
	if ch.changed != 1 || !strings.Contains(ch.state.Text(), "!") {
		t.Fatalf("changed=%d text=%q", ch.changed, ch.state.Text())
	}
	ch.tt.Frame()
	if ch.changed != 1 {
		t.Fatalf("Changed held a second frame: %d", ch.changed)
	}
	ch.state.SetText("other")
	ch.tt.Frame()
	if ch.changed != 1 {
		t.Fatalf("SetText counted as a change: %d", ch.changed)
	}
}

func TestCodeEditorReadOnly(t *testing.T) {
	ch := newCodeHarness(t, CodeOptions{ReadOnly: true}, "fixed", 400, 200)
	ch.tt.Type("x")
	ch.tt.Key(0, ui.KeyBackspace)
	ch.tt.Key(0, ui.KeyTab)
	ch.tt.Frame()
	if ch.state.Text() != "fixed" || ch.changed != 0 {
		t.Fatalf("read-only text %q changed %d", ch.state.Text(), ch.changed)
	}
}

func TestCodeEditorTabIndents(t *testing.T) {
	ch := newCodeHarness(t, CodeOptions{Language: LangJavaScript}, "", 500, 200)
	ch.tt.Key(0, ui.KeyTab)
	ch.tt.Type("x")
	ch.tt.Frame()
	if got := ch.state.Text(); got != "  x" {
		t.Fatalf("Tab gave %q", got)
	}
	ch.tt.Key(ui.Shift, ui.KeyTab)
	ch.tt.Frame()
	if got := ch.state.Text(); got != "x" {
		t.Fatalf("Shift+Tab gave %q", got)
	}
	if ch.other != 0 || !ch.state.st.Focused() {
		t.Fatal("Tab left the editor")
	}
}

func TestCodeEditorEscapeReachesTheDialog(t *testing.T) {
	ch := newCodeHarness(t, CodeOptions{}, "abc def", 500, 200)
	// A caret alone does not take Escape.
	ch.tt.Key(0, ui.KeyEscape)
	if ch.escapes != 1 {
		t.Fatalf("Escape with a caret reached the dialog %d times", ch.escapes)
	}
	// A selection takes the first Escape, the second reaches the dialog.
	ch.state.st.Select(0, 3)
	ch.tt.Frame()
	if !ch.state.ConsumesEscape() {
		t.Fatal("ConsumesEscape false with a selection")
	}
	ch.tt.Key(0, ui.KeyEscape)
	if ch.escapes != 1 {
		t.Fatalf("Escape with a selection reached the dialog (%d)", ch.escapes)
	}
	if ch.state.ConsumesEscape() {
		t.Fatal("selection survived Escape")
	}
	ch.tt.Key(0, ui.KeyEscape)
	if ch.escapes != 2 {
		t.Fatalf("Escape after the selection went did not reach the dialog: %d", ch.escapes)
	}
	// The find bar takes Escape and closes alone.
	ch.tt.Key(ui.Cmd, ui.KeyF)
	ch.tt.Frame()
	ch.tt.Frame()
	ch.tt.Key(0, ui.KeyEscape)
	ch.tt.Frame()
	t.Logf("Escape in the find bar: dialog saw %d (total)", ch.escapes)
	if ch.escapes != 2 {
		t.Fatalf("Escape in the find bar reached the dialog (%d)", ch.escapes)
	}
	ch.tt.Frame()
	ch.tt.Frame()
	for presses := 1; ch.escapes != 3 && presses <= 3; presses++ {
		t.Logf("selection after the find bar: %v", ch.state.ConsumesEscape())
		ch.tt.Key(0, ui.KeyEscape)
	}
	if ch.escapes != 3 {
		t.Fatalf("Escape after the find bar closed did not reach the dialog: %d", ch.escapes)
	}
}

func TestCodeEditorFocusLeavesByClick(t *testing.T) {
	ch := newCodeHarness(t, CodeOptions{}, "abc", 500, 200)
	if !ch.state.st.Focused() {
		t.Fatal("editor not focused")
	}
	if err := ch.tt.Click("其他"); err != nil {
		t.Fatal(err)
	}
	ch.tt.Frame()
	if ch.other != 1 || ch.state.st.Focused() {
		t.Fatalf("other=%d focused=%v", ch.other, ch.state.st.Focused())
	}
	ch.tt.Type("z")
	ch.tt.Frame()
	if ch.state.Text() != "abc" {
		t.Fatalf("unfocused editor took text: %q", ch.state.Text())
	}
}

func TestCodeEditorInputMethod(t *testing.T) {
	ch := newCodeHarness(t, CodeOptions{}, "", 500, 200)
	if active, off := ch.tt.TextInputActive(); !active || off {
		t.Fatalf("input method active=%v off=%v", active, off)
	}
	ch.tt.Compose("ni", 2)
	ch.tt.Frame()
	t.Logf("while composing the text is %q", ch.state.Text())
	ch.tt.Type("你")
	ch.tt.Frame()
	if got := ch.state.Text(); got != "你" {
		t.Fatalf("committed text %q", got)
	}
	if _, ok := ch.tt.TextCaret(); !ok {
		t.Fatal("no caret for the input method window")
	}
}

func TestCodeEditorLargeText(t *testing.T) {
	var b strings.Builder
	line := "  const value = request.headers[\"x-token\"] + 12345; // comment line\n"
	for b.Len() < 1<<20 {
		b.WriteString(line)
	}
	start := time.Now()
	ch := newCodeHarness(t, CodeOptions{Language: LangJavaScript}, b.String(), 800, 500)
	ch.tt.Frame()
	_ = ch.tt.Image()
	built := time.Since(start)
	start = time.Now()
	ch.tt.Key(0, ui.KeyPageDown)
	ch.tt.Frame()
	t.Logf("1 MiB (%d lines): set up and first frames %v, a page down frame %v", strings.Count(b.String(), "\n"), built, time.Since(start))
	if built > 5*time.Second {
		t.Fatalf("building 1 MiB took %v", built)
	}
	start = time.Now()
	if got := ch.state.Text(); len(got) != b.Len() {
		t.Fatalf("text %d bytes, want %d", len(got), b.Len())
	}
	t.Logf("Text() of 1 MiB: %v", time.Since(start))
}

type diffHarness struct {
	tt     *ui.Tester
	before string
	after  string
	opts   DiffOptions
	holder *diffHolder
}

func newDiffHarness(t *testing.T, o DiffOptions, w, h int) *diffHarness {
	dh := &diffHarness{
		before: "GET /a HTTP/1.1\nHost: x.com\nAccept: */*\n\n{\"a\":1}\n",
		after:  "GET /b HTTP/1.1\nHost: x.com\nX-Added: 1\nAccept: */*\n\n{\"a\":2}\n",
		opts:   o,
	}
	dh.tt = ui.NewTester(func(c *ui.Context) {
		Apply(c)
		c.Root().Background(T(c).Surface)
		ui.Column(c).Grow(1).Padding(12).Children(func() {
			frame := CodeDiff(c, dh.before, dh.after, dh.opts).Grow(1)
			dh.holder = ui.Local(frame, "code-diff", func() diffHolder { return diffHolder{} })
		})
	}, w, h)
	dh.tt.Frame()
	dh.tt.Frame()
	return dh
}

func (dh *diffHarness) layout() string {
	if dh.holder.st.Layout == 0 {
		return "split"
	}
	return "unified"
}

func TestCodeDiffModes(t *testing.T) {
	wide := newDiffHarness(t, DiffOptions{Language: LangText}, 1000, 400)
	if got := wide.layout(); got != "split" {
		t.Fatalf("wide default is %s", got)
	}
	wide.tt.SetDark(true)
	wide.tt.Frame()
	added, deleted := newDiffStats(wide)
	if added == 0 || deleted == 0 {
		t.Fatalf("stats +%d -%d", added, deleted)
	}
	uni := newDiffHarness(t, DiffOptions{Mode: DiffUnifiedMode, Language: LangJSON, Context: 1}, 1000, 400)
	if got := uni.layout(); got != "unified" {
		t.Fatalf("unified mode is %s", got)
	}
	uni.after = "GET /b HTTP/1.1\n"
	uni.tt.Frame()
	uni.tt.Frame()
}

func newDiffStats(dh *diffHarness) (int, int) {
	return dh.holder.st.Stats()
}

func TestCodeDiffNarrowFallsBackToUnified(t *testing.T) {
	narrow := newDiffHarness(t, DiffOptions{}, 500, 400)
	if got := narrow.layout(); got != "unified" {
		t.Fatalf("narrow side-by-side is %s", got)
	}
	// Widening the window goes back to two columns.
	narrow.tt.SetSize(1000, 400)
	narrow.tt.Frame()
	narrow.tt.Frame()
	if got := narrow.layout(); got != "split" {
		t.Fatalf("widened is %s", got)
	}
}
