package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// Code languages of CodeOptions and DiffOptions.
const (
	LangJavaScript = "javascript"
	LangJSON       = "json"
	LangText       = "text"
)

const (
	codeFontSize   = FontTable // code reads at the table's 12 DIPs
	codeLineHeight = 1.5       // 18 DIPs of line
	codeTabSize    = 2
	codeMinHeight  = 120
	// DiffNarrowWidth is the width below which CodeDiff shows one text
	// over the other.
	DiffNarrowWidth = 720
)

// CodeState is the state of a CodeEditor, kept by the view across frames.
// The zero value is an empty editor.
type CodeState struct {
	st      *editor.State
	lang    string
	seen    uint64
	changed bool
	focus   bool
}

func (s *CodeState) ensure() *editor.State {
	if s.st == nil {
		s.st = &editor.State{Buffer: editor.NewBuffer("")}
		s.seen = s.st.Buffer.Version()
	}
	return s.st
}

// Text returns the edited text.
func (s *CodeState) Text() string { return s.ensure().Buffer.String() }

// SetText replaces the text as one edit the user can undo. A programmatic
// change does not count for Changed.
func (s *CodeState) SetText(text string) {
	b := s.ensure().Buffer
	if b.String() != text {
		b.Set(text)
	}
	s.seen = b.Version()
}

// Reset loads text as a new document: the undo history, the selection and
// the scroll position start over.
func (s *CodeState) Reset(text string) {
	st := s.ensure()
	st.Buffer = editor.NewBuffer(text)
	s.lang = ""
	s.seen = st.Buffer.Version()
	s.changed = false
}

// Changed reports whether the user changed the text since the previous
// build of the editor: it holds for the frame after an edit, so the view
// reads it after CodeEditor has built.
func (s *CodeState) Changed() bool { return s.changed }

// Version counts the edits of the text, those of SetText included.
func (s *CodeState) Version() uint64 { return s.ensure().Buffer.Version() }

// ConsumesEscape reports whether a selection or several carets make the
// editor take the next Escape for itself (it collapses them). An open find
// bar, go-to-line bar or completion list takes it first and is not visible
// here. Otherwise Escape reaches the dialog around the editor.
func (s *CodeState) ConsumesEscape() bool {
	st := s.ensure()
	sel := st.Selections()
	return len(sel) > 1 || (len(sel) == 1 && !sel[0].Empty())
}

// Focus asks the editor to take the keyboard in its next build.
func (s *CodeState) Focus() { s.focus = true }

// CodeOptions configure CodeEditor.
type CodeOptions struct {
	// Language is LangJavaScript, LangJSON or LangText (the default).
	Language string
	ReadOnly bool
	// Label names the editor for assistive technology.
	Label string
	// MinHeight is the least height in DIPs; 120 when 0. The editor grows
	// to fill the space its parent gives it.
	MinHeight float32
	// NoLineNumbers hides the gutter's numbers.
	NoLineNumbers bool
	// Wrap wraps long lines at the editor's width.
	Wrap bool
	// Placeholder shows while the text is empty.
	Placeholder string
	// Fit makes the editor as high as its lines instead of scrolling them.
	Fit bool
}

func codeLanguage(name string) *editor.Language {
	switch name {
	case LangJavaScript:
		return editor.LanguageNamed("javascript")
	case LangJSON:
		return editor.LanguageNamed("json")
	}
	return nil
}

// codeTheme maps the console's tokens to an editor theme.
func codeTheme(k *Tokens) *editor.Theme {
	ct := &editor.Theme{
		Background:        k.Surface,
		Text:              k.Label,
		LineNumber:        k.Label3,
		ActiveLineNumber:  k.Label,
		ActiveLine:        k.Label.Alpha(0.04),
		Selection:         k.Accent.Alpha(0.28),
		InactiveSelection: k.Label3.Alpha(0.28),
		Cursor:            k.Accent,
		MatchingBracket:   k.Label3.Alpha(0.5),
		Match:             k.Warning.Alpha(0.3),
		CurrentMatch:      k.Accent.Alpha(0.4),
		Occurrence:        k.Label.Alpha(0.08),
		IndentGuide:       k.HairlineSoft,
		ActiveIndentGuide: k.Hairline,
		WrapGuide:         k.HairlineSoft,
		Whitespace:        k.Label4,
		Error:             k.Danger,
		Warning:           k.Warning,
		Info:              k.AccentText,
		Hint:              k.Label3,
		Added:             k.Success,
		Deleted:           k.Danger,
		Modified:          k.Accent,
	}
	// Apple system colors for the syntax; the comment is the muted text
	// color, darkened in the light theme to keep its contrast.
	var keyword, str, number, typ, fn, prop, comment, escape ui.Color
	if k.Dark {
		keyword, str, number = ui.Hex("#FF7AB2"), ui.Hex("#FF8170"), ui.Hex("#D9C97C")
		typ, fn, prop = ui.Hex("#5DD8FF"), ui.Hex("#67B7A4"), ui.Hex("#4EB0CC")
		comment, escape = ui.Hex("#8E8E93"), ui.Hex("#D0A8FF")
	} else {
		keyword, str, number = ui.Hex("#AD3DA4"), ui.Hex("#C41A16"), ui.Hex("#1C00CF")
		typ, fn, prop = ui.Hex("#0B4F79"), ui.Hex("#326D74"), ui.Hex("#0066CC")
		comment, escape = ui.Hex("#6E6E73"), ui.Hex("#6C36A9")
	}
	set := func(c ui.Color, kinds ...editor.TokenKind) {
		for _, kind := range kinds {
			ct.Tokens[kind].Color = c
		}
	}
	set(keyword, editor.TokenKeyword, editor.TokenOperator)
	set(str, editor.TokenString, editor.TokenRegexp, editor.TokenLink, editor.TokenCode)
	set(number, editor.TokenNumber, editor.TokenConstant)
	set(typ, editor.TokenType, editor.TokenTag)
	set(fn, editor.TokenFunction, editor.TokenMeta)
	set(prop, editor.TokenProperty, editor.TokenAttribute, editor.TokenHeading)
	set(comment, editor.TokenComment)
	set(escape, editor.TokenEscape)
	set(k.Success, editor.TokenInserted)
	set(k.Danger, editor.TokenDeleted, editor.TokenInvalid)
	ct.Tokens[editor.TokenHeading].Bold = true
	ct.Tokens[editor.TokenStrong].Bold = true
	ct.Tokens[editor.TokenEmphasis].Italic = true
	return ct
}

func codeFont() ui.Font { return ui.Font{Family: MonoFont, Size: codeFontSize} }

// CodeEditor is an editable code editor on a solid surface: a 9 DIP frame
// with a hairline, an accent outline while it has the keyboard, syntax
// colors of the console's palette, line numbers, search (Cmd/Ctrl+F),
// multiple carets and undo. The returned element is the frame; give it a
// size, such as Grow(1) or a Height.
//
// Keys: Tab indents (a selection indents its lines) and Shift+Tab
// outdents, so Tab does not leave an editable editor; click outside or use
// the window's own focus keys. Escape closes the editor's bars and
// completions, then collapses a selection or extra carets, and only
// otherwise reaches the dialog around it (ConsumesEscape). Input methods
// compose in place.
func CodeEditor(c *ui.Context, s *CodeState, o CodeOptions) ui.Element {
	k := T(c)
	st := s.ensure()
	s.changed = st.Buffer.Version() != s.seen
	s.seen = st.Buffer.Version()
	if s.lang != o.Language || st.Buffer.Language() == nil && codeLanguage(o.Language) != nil {
		s.lang = o.Language
		st.Buffer.SetLanguage(codeLanguage(o.Language))
	}
	st.Font = codeFont()
	st.LineHeight = codeLineHeight
	st.TabSize = codeTabSize
	st.Indent = editor.IndentSpaces
	st.SoftWrap = o.Wrap
	st.ReadOnly = o.ReadOnly
	st.Label = o.Label
	st.Placeholder = o.Placeholder
	st.FitContent = o.Fit
	st.NoScrollPastEnd = true
	st.NoFolding = true
	st.LineNumbers = editor.LineNumbersOn
	if o.NoLineNumbers {
		st.LineNumbers = editor.LineNumbersOff
	}
	st.Theme = codeTheme(k)

	least := o.MinHeight
	if least <= 0 {
		least = codeMinHeight
	}
	frame := ui.Column(c).Radius(RadiusInput).Background(k.Surface).Clip().MinWidth(0).MinHeight(least)
	var ed ui.Element
	frame.Children(func() {
		ed = editor.View(c.Key("code"), st).Grow(1).MinHeight(0).MinWidth(0)
		if s.focus {
			s.focus = false
			ed.Focus()
		}
	})
	frame.Border(0.5, k.Hairline)
	if ed.Focused() {
		FocusRing(frame, 1.5, k.Accent.Alpha(0.6))
	}
	return frame
}

// DiffMode is how CodeDiff lays out the two texts.
type DiffMode uint8

const (
	// DiffSideBySide shows the old text left of the new one; below
	// DiffNarrowWidth it falls back to DiffUnified.
	DiffSideBySide DiffMode = iota
	// DiffUnifiedMode shows the new text with the removed lines above.
	DiffUnifiedMode
)

// DiffOptions configure CodeDiff.
type DiffOptions struct {
	Mode DiffMode
	// Language is LangJavaScript, LangJSON or LangText (the default).
	Language string
	// Context is how many unchanged lines show around a change; 3 when 0.
	// Longer runs collapse into a separator that expands on a click.
	Context int
	// NoWords turns off the highlight of the words changed within lines.
	NoWords bool
	// Wrap wraps long lines.
	Wrap bool
	// MinHeight is the least height in DIPs; 120 when 0.
	MinHeight float32
	// Fit makes the viewer as high as its lines.
	Fit bool
	// Key distinguishes several diffs built from one place.
	Key any
}

type diffHolder struct {
	st       *editor.DiffState
	old, new string
	lang     string
}

// CodeDiff shows the changes from before to after, read-only: lines
// tinted, words highlighted, unchanged runs collapsed. It keeps its own
// state on the returned element, so call it every frame with the current
// texts; the texts may change between frames.
func CodeDiff(c *ui.Context, before, after string, o DiffOptions) ui.Element {
	k := T(c)
	least := o.MinHeight
	if least <= 0 {
		least = codeMinHeight
	}
	key := o.Key
	if key == nil {
		key = "code-diff"
	}
	frame := ui.Column(c.Key(key)).Radius(RadiusInput).Background(k.Surface).Clip().MinWidth(0).MinHeight(least).
		Border(0.5, k.Hairline)
	h := ui.Local(frame, "code-diff", func() diffHolder {
		return diffHolder{st: &editor.DiffState{Old: editor.NewBuffer(""), New: editor.NewBuffer("")}}
	})
	st := h.st
	if h.old != before {
		st.Old.Set(before)
		h.old = before
	}
	if h.new != after {
		st.New.Set(after)
		h.new = after
	}
	if h.lang != o.Language {
		h.lang = o.Language
		lang := codeLanguage(o.Language)
		st.Old.SetLanguage(lang)
		st.New.SetLanguage(lang)
	}
	// The width is the last frame's; the window's before the first.
	width, _ := c.Size()
	if w := frame.Bounds().W; w > 0 {
		width = w
	}
	st.Layout = editor.DiffSplit
	if o.Mode == DiffUnifiedMode || width < DiffNarrowWidth {
		st.Layout = editor.DiffUnified
	}
	st.Inline = editor.DiffInlineWordAlt
	if o.NoWords {
		st.Inline = editor.DiffInlineNone
	}
	st.Indicators = editor.DiffBars
	st.ContextLines = o.Context
	st.Wrap = o.Wrap
	st.FitContent = o.Fit
	st.Editable = false
	st.Font = codeFont()
	st.LineHeight = codeLineHeight
	st.TabSize = codeTabSize
	st.Theme = codeTheme(k)
	frame.Children(func() {
		editor.DiffView(c.Key("diff"), st).Grow(1).MinHeight(0).MinWidth(0)
	})
	return frame
}
