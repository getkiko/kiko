package kit

import (
	"strings"

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
	// TokenClick runs as an editable chip is clicked; anchor is the chip.
	TokenClick func(id string, anchor ui.Element)
}

type tokenInputState struct{ focused bool }

// TokenInput is a whole-field tag input (NativeTokenInput): chips of the
// values, then an input editing *query. Typing shows the suggestions below
// the field; Up and Down move among them, Enter or a click commits one,
// Enter alone commits what was typed, and Escape closes the suggestions
// first. Backspace in the empty input removes the last chip, and leaving
// the field commits what was typed. It returns the input, whose Changed
// reports edits.
func TokenInput(c *ui.Context, key any, query *string, o TokenInputOptions) ui.Element {
	k := T(c)
	var input ui.Element
	field := ui.Row(c.Key(key)).Wrap().MinHeight(ControlHeight).Padding(3, 6).Gap(4).Radius(RadiusInput).
		Background(k.Fill).Cursor(ui.CursorText).MinWidth(0).FillWidth().Label(o.Label)
	var cb ui.ComboboxParts
	field.Children(func() {
		for _, token := range o.Tokens {
			tokenChip(c, token, o)
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
	})
	if input.Focused() {
		field.Border(1.5, k.Accent.Alpha(0.6))
	} else if o.Error != "" {
		field.Border(1, k.Danger.Alpha(0.6))
	}
	s := ui.Local(field, "token-input", func() tokenInputState { return tokenInputState{} })
	commit := func(suggestion string) {
		if o.Commit != nil && o.Commit(suggestion) {
			*query = ""
			cb.SetOpen(false)
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
	if *query == "" && len(o.Tokens) > 0 && input.Shortcut(0, ui.KeyBackspace) && o.Remove != nil {
		o.Remove(o.Tokens[len(o.Tokens)-1].ID)
	}
	focused := input.Focused()
	if s.focused && !focused && strings.TrimSpace(*query) != "" {
		commit("")
	}
	s.focused = focused
	if o.Error != "" {
		FormError(c, o.Error)
	}
	return input
}

// tokenChip is a token of a TokenInput with its remove button.
func tokenChip(c *ui.Context, token Token, o TokenInputOptions) {
	k := T(c)
	title := token.Title
	if title == "" {
		title = token.Label
	}
	chip := ui.Row(c.Key(token.ID)).Height(22).PaddingStart(7).PaddingEnd(3).Gap(4).Radius(6).
		Background(k.Surface).Border(0.5, k.Hairline.Alpha(0.6)).Shrink(0).MaxWidth(280)
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
			o.TokenClick(token.ID, chip)
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
}
