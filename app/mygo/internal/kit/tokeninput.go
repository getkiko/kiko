package kit

import (
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// Token is a value of a TokenInput, shown as a chip: an optional badge
// (the kind, as "Suffix"), the label and a muted detail (as "Exclude").
type Token struct {
	ID, Label, Detail, Badge string
	// Title is the chip's tooltip, the label when empty.
	Title string
	// Editable chips call TokenInputOptions.TokenClick when clicked.
	Editable bool
	// Text is what copying the chip writes, the label when empty.
	Text string
	// Clip is the token as a paste into a field of the same ClipKind reads
	// it back, kind included (as "domain_suffix:apple.com"); Text when
	// empty.
	Clip string
}

// TokenSuggestion is an entry of a TokenInput's suggestions.
type TokenSuggestion struct {
	ID, Label, Detail string
}

// TokenInputOptions describe a TokenInput.
type TokenInputOptions struct {
	// Label names the field, as "Domain".
	Label       string
	Placeholder string
	// Error shows under the field.
	Error  string
	Tokens []Token
	// Suggestions list what the typed text could add, below the field.
	Suggestions []TokenSuggestion
	// Commit adds the typed text, as the suggestion chosen (its ID) or as
	// typed (""), returning whether it did: the text then clears.
	Commit func(suggestion string) bool
	// Remove takes a token out.
	Remove func(id string)
	// RemoveAll takes several tokens out at once, Remove for each when nil.
	RemoveAll func(ids []string)
	// TokenClick runs as an editable chip is clicked; anchor is the chip.
	TokenClick func(id string, anchor ui.Element)
	// Paste adds pasted values at once, each trimmed and the duplicates
	// left out: a paste of several values into the input (split by Split)
	// and the menu's Paste. Without it a paste goes into the input as
	// typed.
	Paste func(values []string)
	// Blur adds what was typed as the field loses focus, taken as typed
	// rather than as the first suggestion Enter would take; Commit("")
	// when nil.
	Blur func() bool
	// Whole reports whether a single pasted value adds at once, as one
	// naming its kind ("suffix:google.com"), rather than going into the
	// input as typed.
	Whole func(value string) bool
	// Split splits pasted text into values, SplitTokens when nil.
	Split func(text string) []string
	// ClipKind names the kind of the tokens' Clip: a paste into a field of
	// the same kind of what a TokenInput copied reads the Clips.
	ClipKind string
	// Browse makes the field a picker of a list: a click opens the
	// suggestions with nothing typed, they stay open after a choice, and a
	// chevron ends the field.
	Browse bool
}

// tokenAction is what a key or a paste, seen between frames, has the next
// build do.
type tokenAction int

const (
	tokenNone tokenAction = iota
	tokenCopy
	tokenSelectAll
	tokenRemoveSelected
)

type tokenInputState struct {
	focused  bool
	selected []string // the IDs of the chips chosen
	anchor   string   // the chip a Shift-click extends from
	action   tokenAction
	pasted   []string // values pasted, for the next build to add
	refocus  bool
	// wasOpen is whether the suggestions were open at the end of the
	// last build: a press on the field dismisses them before the build
	// sees it, so a Browse field toggles from what was shown.
	wasOpen bool
	// closing closes the suggestions on the click of a press that found
	// them open, as the combobox opens them on a click; opening opens
	// them in the next build, for a click beside the input.
	closing, opening bool
}

// tokenClipboard is what a TokenInput copied last, to paste it back
// losslessly: the text written, the kind and the tokens' Clips.
var tokenClipboard struct {
	text, kind string
	clips      []string
}

// SplitTokens splits pasted text into values at newlines, commas,
// semicolons, the ideographic ones and whitespace, each trimmed, the
// empty and repeated ones left out.
func SplitTokens(text string) []string {
	return uniqueTokens(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;，；、", r)
	}))
}

// SplitTokenLines is SplitTokens keeping spaces inside a value, as in a
// Wi-Fi name: it splits at newlines, commas and semicolons only.
func SplitTokenLines(text string) []string {
	return uniqueTokens(strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || strings.ContainsRune(",;，；、", r)
	}))
}

func uniqueTokens(parts []string) []string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// tokenText is what copying a token writes.
func tokenText(t Token) string {
	if t.Text != "" {
		return t.Text
	}
	return t.Label
}

// TokenInput is a whole-field tag input (NativeTokenInput): chips of the
// values, then an input editing *query. Typing shows the suggestions below
// the field; Up and Down move among them, Enter or a click commits one,
// Enter alone commits what was typed, and Escape closes the suggestions
// first. Backspace in the empty input removes the last chip, and leaving
// the field commits what was typed.
//
// A click chooses a chip, Cmd-click adds or takes one from the choice and
// Shift-click extends it; in the empty input Cmd+A chooses them all, Cmd+C
// copies those chosen (all of them when none is), one a line, Backspace or
// Delete removes them and Escape lets them go. A paste of several values
// adds each (Paste). The field's context menu copies, pastes and clears.
// It returns the input, whose Changed reports edits.
func TokenInput(c *ui.Context, key any, query *string, o TokenInputOptions) ui.Element {
	k := T(c)
	var input ui.Element
	field := ui.Row(c.Key(key)).Wrap().MinHeight(ControlHeight).Padding(3, 6).Gap(4).Radius(RadiusInput).
		Background(k.Fill).Cursor(ui.CursorText).MinWidth(0).FillWidth().Label(o.Label)
	s := ui.Local(field, "token-input", func() tokenInputState { return tokenInputState{} })
	svc := c.Services()
	split := o.Split
	if split == nil {
		split = SplitTokens
	}

	// The choice keeps only the chips still shown.
	s.selected = slices.DeleteFunc(s.selected, func(id string) bool {
		return !slices.ContainsFunc(o.Tokens, func(t Token) bool { return t.ID == id })
	})
	chosen := func() []Token {
		var out []Token
		for _, t := range o.Tokens {
			if slices.Contains(s.selected, t.ID) {
				out = append(out, t)
			}
		}
		return out
	}
	copyTokens := func(tokens []Token) {
		if len(tokens) == 0 {
			return
		}
		texts := make([]string, len(tokens))
		clips := make([]string, len(tokens))
		for i, t := range tokens {
			texts[i] = tokenText(t)
			clips[i] = t.Clip
			if clips[i] == "" {
				clips[i] = texts[i]
			}
		}
		text := strings.Join(texts, "\n")
		tokenClipboard.text, tokenClipboard.kind, tokenClipboard.clips = text, o.ClipKind, clips
		c.WriteClipboard(text)
	}
	remove := func(ids []string) {
		if len(ids) == 0 {
			return
		}
		switch {
		case o.RemoveAll != nil:
			o.RemoveAll(ids)
		case o.Remove != nil:
			for _, id := range ids {
				o.Remove(id)
			}
		}
		s.selected, s.anchor = nil, ""
	}
	// pasteValues are the values a pasted text adds: what a TokenInput of
	// the same kind copied, or the text split.
	pasteValues := func(text string) []string {
		if text != "" && text == tokenClipboard.text && o.ClipKind == tokenClipboard.kind {
			return uniqueTokens(tokenClipboard.clips)
		}
		return split(text)
	}

	switch s.action {
	case tokenCopy:
		if picked := chosen(); len(picked) > 0 {
			copyTokens(picked)
		} else {
			copyTokens(o.Tokens)
		}
	case tokenSelectAll:
		s.selected = nil
		for _, t := range o.Tokens {
			s.selected = append(s.selected, t.ID)
		}
	case tokenRemoveSelected:
		remove(slices.Clone(s.selected))
	}
	s.action = tokenNone
	if s.pasted != nil {
		if o.Paste != nil {
			o.Paste(s.pasted)
		}
		s.pasted = nil
	}

	// menu is the context menu of the field, or of the chip target: a chip
	// right-clicked outside the choice becomes the choice. Copy takes the
	// chosen chips, all of them when none is.
	menu := func(m *ui.Menu, target string) {
		if target != "" && !slices.Contains(s.selected, target) {
			s.selected, s.anchor = []string{target}, target
		}
		if m.Item("复制").Shortcut(ui.Cmd, ui.KeyC).Disabled(len(o.Tokens) == 0).Chosen() {
			if picked := chosen(); len(picked) > 0 {
				copyTokens(picked)
			} else {
				copyTokens(o.Tokens)
			}
		}
		if m.Item("复制全部").Disabled(len(o.Tokens) == 0).Chosen() {
			copyTokens(o.Tokens)
		}
		if m.Item("粘贴").Shortcut(ui.Cmd, ui.KeyV).Disabled(o.Paste == nil).Chosen() {
			if values := pasteValues(c.ReadClipboard()); len(values) > 0 {
				o.Paste(values)
			}
		}
		if m.Item("全选").Shortcut(ui.Cmd, ui.KeyA).Disabled(len(o.Tokens) == 0).Chosen() {
			s.selected = nil
			for _, t := range o.Tokens {
				s.selected = append(s.selected, t.ID)
			}
			s.refocus = true
		}
		m.Separator()
		if m.Item("清空").Disabled(len(o.Tokens) == 0).Danger().Chosen() {
			ids := make([]string, len(o.Tokens))
			for i, t := range o.Tokens {
				ids[i] = t.ID
			}
			remove(ids)
		}
	}

	var cb ui.ComboboxParts
	field.Children(func() {
		for _, token := range o.Tokens {
			tokenChip(c, token, o, s, menu)
		}
		ui.Box(c.Key("input")).Grow(1).MinWidth(60).Children(func() {
			cb = ui.ComboboxBase(c, query)
			input = cb.Input
			placeholder := o.Placeholder
			if len(o.Tokens) > 0 {
				placeholder = ""
			}
			input.Height(22).FontSize(FontTable).TextColor(k.Label).SingleLine().Placeholder(placeholder).Label(o.Label)
		})
		if o.Browse {
			ui.Box(c).Height(22).PaddingX(4).Center().Shrink(0).Children(func() {
				Icon(c, SymChevronDown, 11).TextColor(k.Label3)
			})
		}
	})
	input.OnTextKey(func(key ui.TextKey) bool {
		empty := key.Text() == ""
		action := tokenNone
		switch {
		case key.Mods == ui.Cmd && key.Key == ui.KeyC && empty && len(o.Tokens) > 0,
			key.Mods == ui.Cmd && key.Key == ui.KeyC && len(s.selected) > 0:
			action = tokenCopy
		case key.Mods == ui.Cmd && key.Key == ui.KeyA && empty && len(o.Tokens) > 0:
			action = tokenSelectAll
		case key.Mods == 0 && (key.Key == ui.KeyBackspace || key.Key == ui.KeyDelete) && empty && len(s.selected) > 0:
			action = tokenRemoveSelected
		}
		if action == tokenNone {
			return false
		}
		s.action = action
		svc.Invalidate()
		return true
	})
	if o.Paste != nil {
		input.OnPaste(func(text string) bool {
			values := pasteValues(text)
			fromCopy := text != "" && text == tokenClipboard.text && o.ClipKind == tokenClipboard.kind
			whole := len(values) == 1 && o.Whole != nil && o.Whole(values[0])
			if len(values) < 2 && !fromCopy && !whole {
				return false // one value pastes as typed
			}
			s.pasted = values
			svc.Invalidate()
			return true
		})
	}
	if s.refocus {
		input.Focus()
		s.refocus = false
	}
	if input.Focused() {
		FocusRing(field, 1.5, k.Accent.Alpha(0.6))
	} else if o.Error != "" {
		FocusRing(field, 1, k.Danger.Alpha(0.6))
	}
	if len(s.selected) > 0 {
		// With chips chosen the suggestions stay closed, so that Escape
		// comes here rather than closing them.
		cb.SetOpen(false)
	}
	if len(s.selected) > 0 && input.Shortcut(0, ui.KeyEscape) {
		s.selected, s.anchor = nil, ""
	}
	if input.PressedInside() && len(s.selected) > 0 {
		// A press in the input lets the chosen chips go.
		s.selected, s.anchor = nil, ""
	} else if o.Browse {
		switch {
		case input.PressedInside():
			s.closing = s.wasOpen
		case input.Clicked():
			if s.closing {
				cb.SetOpen(false)
			}
			s.closing = false
		case field.PressedInside():
			// Beside the input (the chevron, the room after the chips):
			// the press may take the focus and close them first.
			s.closing = s.wasOpen
		case field.Clicked():
			if s.closing {
				cb.SetOpen(false)
			} else {
				s.opening, s.refocus = true, true
				svc.Invalidate()
			}
			s.closing = false
		case s.opening && input.Focused():
			cb.SetOpen(true)
			s.opening = false
		}
	}
	field.PopoverMenu(func(m *ui.Menu) { menu(m, "") })
	commit := func(suggestion string) {
		if o.Commit != nil && o.Commit(suggestion) {
			*query = ""
			cb.SetOpen(o.Browse && suggestion != "")
		}
	}
	if len(o.Suggestions) > 0 {
		cb.Popup(func(panel ui.Element) {
			Popup(c, panel).Padding(5).Gap(1).Label(o.Label + "建议")
			AttachPopover(c, panel, field, ui.AnchorBottomStart, ui.AnchorTopStart, 4, 260)
			if b := field.Bounds(); b.W > 0 {
				panel.MinWidth(b.W)
			}
			ui.Scroll(c).Grow(1).Shrink(1).MinHeight(0).Gap(1).Children(func() {
				for _, sug := range o.Suggestions {
					item := cb.Item(sug.ID).MinHeight(28).PaddingX(10).Gap(8).Radius(7).FontSize(FontTable).TextColor(k.Label)
					if item.Highlighted() {
						item.Background(k.Accent).TextColor(k.OnAccent)
					}
					item.Children(func() {
						ui.Text(c, sug.Label).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
						if sug.Detail != "" {
							d := ui.Text(c, sug.Detail).SingleLine().FontSize(FontCaption).Shrink(0)
							if !item.Highlighted() {
								d.TextColor(k.Label3)
							}
						}
					})
				}
			})
		})
	} else {
		cb.Popup(func(ui.Element) {})
	}
	if id, ok := cb.Chosen(); ok {
		commit(id)
	} else if input.Submitted() && strings.TrimSpace(*query) != "" {
		commit("")
	}
	if input.Changed() {
		// Typing lets the chosen chips go.
		s.selected, s.anchor = nil, ""
	}
	if *query == "" && len(o.Tokens) > 0 && len(s.selected) == 0 && input.Shortcut(0, ui.KeyBackspace) && o.Remove != nil {
		o.Remove(o.Tokens[len(o.Tokens)-1].ID)
	}
	s.wasOpen = cb.Open()
	focused := input.Focused()
	if s.focused && !focused && strings.TrimSpace(*query) != "" {
		if o.Blur == nil {
			commit("")
		} else if o.Blur() {
			*query = ""
		}
	}
	s.focused = focused
	if o.Error != "" {
		FormError(c, o.Error)
	}
	return input
}

// chooseToken updates the choice for a click on the chip id: alone, Cmd
// adding or taking it, Shift extending from the last chip clicked.
func chooseToken(s *tokenInputState, tokens []Token, id string, mods ui.Modifiers) {
	switch {
	case mods&ui.Shift != 0 && s.anchor != "":
		from := slices.IndexFunc(tokens, func(t Token) bool { return t.ID == s.anchor })
		to := slices.IndexFunc(tokens, func(t Token) bool { return t.ID == id })
		if from >= 0 && to >= 0 {
			if from > to {
				from, to = to, from
			}
			s.selected = nil
			for _, t := range tokens[from : to+1] {
				s.selected = append(s.selected, t.ID)
			}
			return
		}
		s.selected, s.anchor = []string{id}, id
	case mods&ui.Cmd != 0:
		if i := slices.Index(s.selected, id); i >= 0 {
			s.selected = slices.Delete(s.selected, i, i+1)
		} else {
			s.selected = append(s.selected, id)
		}
		s.anchor = id
	default:
		s.selected, s.anchor = []string{id}, id
	}
}

// tokenChip is a token of a TokenInput with its remove button.
func tokenChip(c *ui.Context, token Token, o TokenInputOptions, s *tokenInputState, menu func(*ui.Menu, string)) {
	k := T(c)
	title := token.Title
	if title == "" {
		title = token.Label
	}
	selected := slices.Contains(s.selected, token.ID)
	chip := ui.Row(c.Key(token.ID)).Height(22).PaddingStart(7).PaddingEnd(3).Gap(4).Radius(6).
		Shrink(0).MaxWidth(280).Cursor(ui.CursorDefault).Selected(selected).Transition(HoverMotion)
	if selected {
		chip.Background(k.Accent.Alpha(0.16)).Border(1, k.Accent.Alpha(0.75))
	} else {
		chip.Background(k.Surface).Border(0.5, k.Hairline.Alpha(0.6))
	}
	chip.Children(func() {
		body := ui.Row(c).Gap(4).MinWidth(0).Shrink(1)
		if token.Editable && o.TokenClick != nil {
			body = ui.ButtonBase(c).Gap(4).MinWidth(0).Shrink(1).Label(title)
		}
		body.Children(func() {
			if token.Badge != "" {
				ui.Text(c, token.Badge).FontSize(10).FontWeight(600).TextColor(k.AccentText).SingleLine().Shrink(0)
			}
			ui.Text(c, token.Label).FontSize(FontTable).TextColor(k.Label).SingleLine().Ellipsis("…").MinWidth(0)
			if token.Detail != "" {
				ui.Text(c, token.Detail).FontSize(10).TextColor(k.Label3).SingleLine().Shrink(0)
			}
		})
		if token.Editable && o.TokenClick != nil && body.Clicked() {
			// Cmd- and Shift-clicks change the choice; a plain click
			// edits the chip, leaving the choice alone, so that Delete
			// after the edit does not remove it.
			if mods := body.ClickModifiers(); mods&(ui.Cmd|ui.Shift) != 0 {
				chooseToken(s, o.Tokens, token.ID, mods)
				s.refocus = true
			} else {
				o.TokenClick(token.ID, chip)
			}
		}
		x := ui.ButtonBase(c).Size(16, 16).Radius(8).Center().Label("移除" + o.Label + " " + token.Label).TextColor(k.Label3).Transition(HoverMotion)
		if x.Hovered() {
			x.Background(k.Fill2)
		}
		x.Children(func() { Icon(c, SymX, 10) })
		if x.Clicked() && o.Remove != nil {
			o.Remove(token.ID)
		}
	})
	chip.PopoverMenu(func(m *ui.Menu) { menu(m, token.ID) })
	if chip.Clicked() {
		chooseToken(s, o.Tokens, token.ID, chip.ClickModifiers())
		s.refocus = true
	}
}
