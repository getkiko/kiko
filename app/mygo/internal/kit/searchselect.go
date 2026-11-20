package kit

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// SearchSelectOptions shape a SearchSelect.
type SearchSelectOptions struct {
	// Label names the control, as "Target outbound".
	Label string
	// Placeholder is the search field's hint.
	Placeholder string
	// Width is the trigger's width; it fills its row when 0.
	Width float32
	// PopoverWidth is the panel's width, 320 DIPs by default, bounded by
	// the window.
	PopoverWidth float32
	Disabled     bool
	// Empty is the trigger's text without a choice, "Select" in Chinese by default.
	Empty string
	// OnQuery, when set, is told the search text as the panel opens (empty)
	// and each time it changes, for options that come from a server.
	OnQuery func(query string)
	// OnOpen, when set, is called each time the panel opens, before the
	// first OnQuery, for options that must be read again on every open.
	OnOpen func()
	// NoChevron leaves the down arrow out, for a trigger that reads as an
	// action, as "+ Add".
	NoChevron bool
}

type searchSelectState struct {
	open   bool
	query  string
	active int
	// shown is the query the active option was set for.
	shown string
	// told is the query OnQuery last heard.
	told string
	// announced is set once OnOpen ran for this opening.
	announced bool
	// reveal scrolls the active option into view, after a key moved it.
	reveal bool
	// pointer is where the pointer was over the panel as it last made an
	// option active: one resting there leaves the keys' choice.
	pointer [2]float32
	// list is the options' virtual list: every match is listed, only the
	// rows in view are built.
	list ui.ListState
}

// normalizeSearch folds text for matching: lower case, without marks,
// spaces, underscores, hyphens and minus signs (UTC−05:00 as UTC-05:00).
func normalizeSearch(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) || unicode.IsSpace(r) || r == '_' || r == '-' || r == '−' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// SearchSelectMatches returns the options matching query by label, detail
// and value, all of them, as the panel lists them.
func SearchSelectMatches(options []Option, value, query string) (visible []Option, total int) {
	needle := normalizeSearch(query)
	var matches []Option
	for _, o := range options {
		if strings.Contains(normalizeSearch(o.Label+" "+o.Detail+" "+o.Value), needle) {
			matches = append(matches, o)
		}
	}
	return matches, len(matches)
}

// SearchSelect is a single choice with local search (NativeSearchSelect,
// variant "popover"): a 30 DIP trigger showing the current choice opens a
// panel, 320 DIPs wide, whose search field takes the focus and filters
// the options by name and detail; the current option is highlighted, Up
// and Down move, Enter or a click chooses. *value changes only on a
// choice, which Changed reports on the trigger it returns.
func SearchSelect(c *ui.Context, key any, value *string, options []Option, o SearchSelectOptions) ui.Element {
	current, muted := searchSelectEmpty(o), true
	for _, option := range options {
		if option.Value == *value {
			current, muted = option.Label, false
		}
	}
	trigger, s := searchSelectTrigger(c, key, current, muted, o)
	choose := func(option Option) {
		s.open = false
		s.query = ""
		if option.Value != *value {
			*value = option.Value
			trigger.SetChanged()
		}
		trigger.Focus()
	}
	searchSelectPanel(c, trigger, s, options, *value, func(v string) bool { return v == *value }, choose, o)
	return trigger
}

// SearchMultiSelect is SearchSelect choosing several values: the panel
// stays open, a click or Enter adds or removes an option, chosen options
// carry a check. *values keeps the order of choice; the trigger lists the
// chosen labels and reports each change by Changed.
func SearchMultiSelect(c *ui.Context, key any, values *[]string, options []Option, o SearchSelectOptions) ui.Element {
	var labels []string
	for _, v := range *values {
		label := v
		for _, option := range options {
			if option.Value == v {
				label = option.Label
				break
			}
		}
		labels = append(labels, label)
	}
	current, muted := searchSelectEmpty(o), len(labels) == 0
	if !muted {
		current = strings.Join(labels, "、")
	}
	trigger, s := searchSelectTrigger(c, key, current, muted, o)
	selected := func(v string) bool { return slices.Contains(*values, v) }
	toggle := func(option Option) {
		if i := slices.Index(*values, option.Value); i >= 0 {
			*values = slices.Delete(slices.Clone(*values), i, i+1)
		} else {
			*values = append(slices.Clone(*values), option.Value)
		}
		trigger.SetChanged()
	}
	first := ""
	if len(*values) > 0 {
		first = (*values)[0]
	}
	searchSelectPanel(c, trigger, s, options, first, selected, toggle, o)
	return trigger
}

func searchSelectEmpty(o SearchSelectOptions) string {
	if o.Empty == "" {
		return "请选择"
	}
	return o.Empty
}

// searchSelectTrigger is the 30 DIP button showing current; it opens and
// closes the panel.
func searchSelectTrigger(c *ui.Context, key any, current string, muted bool, o SearchSelectOptions) (ui.Element, *searchSelectState) {
	k := T(c)
	trigger := ui.ButtonBase(c.Key(key)).Height(ControlHeight).PaddingX(10).Gap(6).Radius(RadiusInput).
		FontSize(FontBody).TextColor(k.Label).Background(k.Fill).MinWidth(0).Justify(ui.Start).Disabled(o.Disabled)
	if o.Width > 0 {
		trigger.Width(o.Width)
	} else {
		trigger.FillWidth()
	}
	if o.Disabled {
		trigger.Opacity(0.5)
	}
	s := ui.Local(trigger, "search-select", func() searchSelectState { return searchSelectState{active: -1} })
	trigger.Label(o.Label + "：" + current)
	trigger.Children(func() {
		text := ui.Text(c, current).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		if muted {
			text.TextColor(k.Label3)
		}
		if !o.NoChevron {
			Icon(c, SymChevronDown, 11).TextColor(k.Label3)
		}
	})
	if o.Disabled {
		s.open = false
	}
	if trigger.Clicked() || !s.open && (trigger.Shortcut(0, ui.KeyDown) || trigger.Shortcut(0, ui.KeyUp)) {
		s.open = !s.open
		s.query, s.active, s.shown, s.told, s.announced = "", -1, "\x00", "\x00", false
	}
	return trigger, s
}

// searchSelectPanel is the popover of a search select: the search field,
// the matching options (current first), Up / Down, Enter and clicks
// calling choose.
func searchSelectPanel(c *ui.Context, trigger ui.Element, s *searchSelectState, options []Option, current string, selected func(string) bool, choose func(Option), o SearchSelectOptions) {
	k := T(c)
	width := o.PopoverWidth
	if width == 0 {
		width = 320
	}
	placeholder := o.Placeholder
	if placeholder == "" {
		placeholder = "输入名称或代码筛选…"
	}
	ui.PopoverBase(c, trigger, &s.open, func(panel ui.Element) {
		w, h := c.Size()
		Popup(c, panel).Width(min(width, max(w-32, 200))).Padding(6).Gap(4).Label("选择" + o.Label)
		AttachPopover(c, panel, trigger, ui.AnchorBottomStart, ui.AnchorTopStart, 4, max(min(h*0.6, 420), 200))
		if o.OnOpen != nil && !s.announced {
			s.announced = true
			o.OnOpen()
		}
		if o.OnQuery != nil && s.told != s.query {
			s.told = s.query
			o.OnQuery(s.query)
		}
		visible, total := SearchSelectMatches(options, current, s.query)
		if s.shown != s.query {
			s.shown = s.query
			s.active, s.reveal = -1, true
			for i, option := range visible {
				if option.Value == current {
					s.active = i
				}
			}
			if s.active < 0 && len(visible) > 0 {
				s.active = 0
			}
		}
		var input ui.Element
		ui.Row(c).Height(30).PaddingX(9).Gap(6).Radius(8).Background(k.Fill).TextColor(k.Label3).Shrink(0).Children(func() {
			Icon(c, SymSearch, 14)
			input = ui.TextInputBase(c, &s.query).Grow(1).MinWidth(0).FontSize(FontBody).TextColor(k.Label).
				Placeholder(placeholder).SingleLine().Label("筛选" + o.Label).AutoFocus()
		})
		switch {
		case input.Shortcut(0, ui.KeyDown):
			s.active, s.reveal = min(s.active+1, len(visible)-1), true
		case input.Shortcut(0, ui.KeyUp):
			s.active, s.reveal = max(s.active-1, 0), true
		}
		if input.Submitted() && s.active >= 0 && s.active < len(visible) {
			choose(visible[s.active])
			return
		}
		if len(visible) == 0 {
			ui.Text(c, "没有匹配项，试试其他名称或代码").FontSize(FontTable).TextColor(k.Label3).Padding(10)
		} else {
			ui.List(c, &s.list, len(visible), func(i int) {
				option := visible[i]
				item := ui.ButtonBase(c.Key(option.Value)).MinHeight(30).PaddingX(9).Gap(8).Radius(7).FillWidth().
					Justify(ui.Start).FontSize(FontBody).TextColor(k.Label).Label(option.Label).Transition(HoverMotion)
				if item.Hovered() {
					if x, y, _ := panel.PointerPosition(); s.pointer != [2]float32{x, y} {
						s.pointer, s.active = [2]float32{x, y}, i
					}
				}
				chosen := selected(option.Value)
				switch {
				case i == s.active:
					item.Background(k.Accent).TextColor(k.OnAccent)
				case chosen:
					item.Background(k.AccentWeak).TextColor(k.AccentText)
				}
				item.Children(func() {
					mark := Icon(c, SymCheck, 13)
					if !chosen {
						mark.Invisible()
					}
					ui.Text(c, option.Label).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
					if option.Detail != "" {
						d := ui.Text(c, option.Detail).SingleLine().FontSize(FontCaption).Shrink(0)
						if i != s.active {
							d.TextColor(k.Label3)
						}
					}
				})
				if item.Clicked() {
					choose(option)
				}
			}).Grow(1).Shrink(1).MinHeight(0).Label(o.Label + "选项")
			if s.reveal && s.active >= 0 {
				s.list.ScrollIntoView(s.active)
				s.reveal = false
			}
		}
		ui.Text(c, fmt.Sprintf("%d 项匹配", total)).FontSize(FontCaption).TextColor(k.Label3).Padding(2, 6, 0).Shrink(0)
	})
}
