package subscriptions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// editableFilter is the filter a group editor binds its fields to; it is
// never nil while editing (normalizeFilter drops an empty one on save).
func editableFilter(o *model.Outbound) *model.GroupFilter {
	if o.Filter == nil {
		o.Filter = &model.GroupFilter{}
	}
	return o.Filter
}

// normalizeFilter trims the filter's patterns and keywords, dropping
// empty and repeated keywords, and drops a filter that selects nothing,
// so a group edited without one saves as before.
func normalizeFilter(o *model.Outbound) {
	f := o.Filter
	if f == nil {
		return
	}
	f.Pattern, f.Exclude = strings.TrimSpace(f.Pattern), strings.TrimSpace(f.Exclude)
	f.Keywords = cleanKeywords(f.Keywords)
	f.ExcludeKeywords = cleanKeywords(f.ExcludeKeywords)
	if len(f.Regions) == 0 {
		f.Regions = nil
	}
	if !f.Active() {
		o.Filter = nil
	}
}

// cleanKeywords are keywords trimmed, without empty ones and repeats
// (ignoring case), nil for none.
func cleanKeywords(keywords []string) []string {
	var out []string
	for _, k := range keywords {
		k = strings.TrimSpace(k)
		if k != "" && !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, k) }) {
			out = append(out, k)
		}
	}
	return out
}

// addKeyword adds the trimmed text to keywords unless it is empty or
// there already, ignoring case.
func addKeyword(keywords []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" || slices.ContainsFunc(keywords, func(x string) bool { return strings.EqualFold(strings.TrimSpace(x), text) }) {
		return keywords
	}
	return append(slices.Clone(keywords), text)
}

// regionBadge is a region's code in a small outlined box; fixed, it is
// 22 DIPs wide with the code centered, as the picker's rows align them.
func regionBadge(c *ui.Context, code string, fixed bool) {
	k := kit.T(c)
	box := ui.Row(c).Height(17).PaddingX(4).Radius(5).Background(k.Surface).Border(1, k.HairlineSoft).
		Shrink(0).Justify(ui.Center)
	if fixed {
		box.Width(26).PaddingX(0)
	}
	box.Children(func() {
		ui.Text(c, code).FontSize(10).FontWeight(600).TextColor(k.Label2).SingleLine()
	})
}

// chipRemove is a chip's × button, removing what it shows.
func chipRemove(c *ui.Context, label string) bool {
	k := kit.T(c)
	x := ui.ButtonBase(c).Size(16, 16).Radius(8).Center().Label(label).TextColor(k.Label3).Transition(kit.HoverMotion)
	if x.Hovered() {
		x.Background(k.Fill2).TextColor(k.Label)
	}
	x.Children(func() { kit.Icon(c, kit.SymX, 10) })
	return x.Clicked()
}

// filterChip is a 24 DIP capsule on the control fill: content, then ×.
func filterChip(c *ui.Context, key, removeLabel string, content func()) bool {
	k := kit.T(c)
	removed := false
	ui.Row(c.Key(key)).Height(24).Radius(12).Background(k.Fill).PaddingStart(4).PaddingEnd(4).Gap(5).Shrink(0).
		MaxWidth(320).Children(func() {
		content()
		removed = chipRemove(c, removeLabel)
	})
	return removed
}

// linkButton is an accent text button of 12 DIPs, as "+ Choose regions…".
func linkButton(c *ui.Context, key, text string) ui.Element {
	k := kit.T(c)
	b := ui.ButtonBase(c.Key(key)).Height(24).PaddingX(6).Radius(7).Label(text).Transition(kit.HoverMotion).Shrink(0)
	if b.Hovered() {
		b.Background(k.Fill)
	}
	b.TextColor(k.AccentText).Children(func() {
		ui.Text(c, text).FontSize(kit.FontTable).SingleLine()
	})
	return b
}

// regionSection is "By region": the chosen regions as chips with their
// node counts, and "+ Choose regions…" opening the region picker.
func (e *outEditor) regionSection(c *ui.Context, doc *model.Doc, o *model.Outbound, f *model.GroupFilter) {
	idx := e.group.index.of(e.w.store.Version(), doc, o.ID)
	ui.Column(c.Key("regions")).Gap(8).Shrink(0).Children(func() {
		sourceCaption(c, "按地区")
		ui.Row(c).Wrap().Gap(6).Shrink(0).Children(func() {
			for _, id := range slices.Clone(f.Regions) {
				r := idx.region(id)
				name := model.RegionName(r)
				if filterChip(c, "region-"+id, "移除 "+name, func() {
					regionBadge(c, model.RegionCode(r), false)
					ui.Text(c, name).FontSize(kit.FontTable).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
					ui.Text(c, fmt.Sprint(idx.regionCounts(f.KeepInfo)[id])).FontSize(kit.FontTable).TextColor(kit.T(c).Label2).
						FontFeatures("tnum").SingleLine()
				}) {
					f.Regions = slices.DeleteFunc(slices.Clone(f.Regions), func(x string) bool { return x == id })
				}
			}
			add := linkButton(c, "add-region", "＋ 选择地区…")
			if add.Clicked() {
				e.group.regionMenu, e.group.regionQuery = true, ""
			}
			e.regionPicker(c, add, idx, f)
		})
	})
}

// keywordSection is "Name contains any": the keywords any of which a name
// contains, as a tag input.
func (e *outEditor) keywordSection(c *ui.Context, f *model.GroupFilter) {
	ui.Column(c.Key("keywords")).Gap(8).Shrink(0).Children(func() {
		sourceCaption(c, "名称包含任一")
		tagField(c, "keyword-input", "名称包含任一", &e.group.keyword, &f.Keywords)
	})
}

// excludeSection is "Name excludes", the keywords dropping a name, and whether
// the info nodes are skipped.
func (e *outEditor) excludeSection(c *ui.Context, f *model.GroupFilter) {
	ui.Column(c.Key("exclude")).Gap(8).Shrink(0).Children(func() {
		sourceCaption(c, "名称不含")
		tagField(c, "exclude-input", "名称不含", &e.group.excludeKeyword, &f.ExcludeKeywords)
		skip := !f.KeepInfo
		if kit.Checkbox(c, &skip, "跳过流量、到期等信息节点").Changed() {
			f.KeepInfo = !skip
		}
	})
}

// tagFieldState is whether a tag field's input had the focus.
type tagFieldState struct{ focused bool }

// tagField is a bordered field of keyword chips, each removable, and an
// input: Enter or leaving the field adds what was typed, Escape clears
// it, Backspace in the empty input removes the last chip.
func tagField(c *ui.Context, key, label string, text *string, keywords *[]string) {
	kt := kit.T(c)
	var input ui.Element
	field := ui.Row(c.Key(key)).Wrap().MinHeight(kit.ControlHeight).Padding(3, 6).Gap(6).Radius(kit.RadiusInput).
		Background(kt.Surface).Border(1, kt.HairlineSoft).Cursor(ui.CursorText).FillWidth().Label(label)
	field.Children(func() {
		for i, word := range slices.Clone(*keywords) {
			if filterChip(c, fmt.Sprintf("kw-%d-%s", i, word), "移除 "+word, func() {
				ui.Text(c, word).FontSize(kit.FontTable).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).MarginStart(4)
			}) {
				*keywords = slices.Delete(slices.Clone(*keywords), i, i+1)
			}
		}
		ui.Row(c.Key("input")).Grow(1).MinWidth(80).Height(22).Children(func() {
			placeholder := ""
			if len(*keywords) == 0 {
				placeholder = "输入后回车"
			}
			input = ui.TextInputBase(c, text).Grow(1).MinWidth(0).FontSize(kit.FontTable).SingleLine().
				Placeholder(placeholder).TextColor(kt.Label).Label(label)
			// The editor takes Backspace itself; seen first, Backspace in
			// the empty input takes out the last chip instead. macOS sends
			// the key before its text system's deleteBackward:, which
			// MyGo leaves alone, so this holds there too.
			input.OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyBackspace || k.Mods != 0 || k.Text() != "" || len(*keywords) == 0 {
					return false
				}
				*keywords = slices.Clone((*keywords)[:len(*keywords)-1])
				return true
			})
		})
	})
	if input.Focused() {
		field.Border(1.5, kt.Accent.Alpha(0.6))
	}
	commit := func() {
		*keywords = addKeyword(*keywords, *text)
		*text = ""
	}
	if input.Submitted() {
		commit()
	}
	if *text != "" && input.Shortcut(0, ui.KeyEscape) {
		*text = ""
	}
	s := ui.Local(field, "tag-field", func() tagFieldState { return tagFieldState{} })
	focused := input.Focused()
	if s.focused && !focused && strings.TrimSpace(*text) != "" {
		commit()
	}
	s.focused = focused
}

// commitTagInputs adds what the tag fields hold typed but not entered to
// their keywords, as saving does.
func (e *outEditor) commitTagInputs(f *model.GroupFilter) {
	g := &e.group
	f.Keywords = addKeyword(f.Keywords, g.keyword)
	f.ExcludeKeywords = addKeyword(f.ExcludeKeywords, g.excludeKeyword)
	g.keyword, g.excludeKeyword = "", ""
}

// patternSection is "Advanced: Custom match", shown while the filter keeps a
// pattern keywords cannot express: the patterns in a read-only chip, an
// edit popover and a clear button.
func (e *outEditor) patternSection(c *ui.Context, f *model.GroupFilter) {
	g := &e.group
	if f.Pattern == "" && f.Exclude == "" && !g.patternMenu {
		return
	}
	kt := kit.T(c)
	var parts []string
	if s := strings.TrimSpace(f.Pattern); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(f.Exclude); s != "" {
		parts = append(parts, "不含 "+s)
	}
	full := strings.Join(parts, "  ·  ")
	ui.Column(c.Key("pattern")).Gap(8).Shrink(0).Children(func() {
		sourceCaption(c, "高级：自定义匹配")
		ui.Row(c).Gap(4).Shrink(0).Children(func() {
			chip := ui.ButtonBase(c.Key("pattern-chip")).Height(24).Radius(12).PaddingX(10).Background(kt.Fill).
				MinWidth(0).Shrink(1).Justify(ui.Start).Label("编辑自定义匹配").Tooltip(full).Transition(kit.HoverMotion)
			if chip.Hovered() {
				chip.Background(kt.Fill2)
			}
			chip.Children(func() {
				ui.Text(c, full).Font(kit.MonoFont).FontSize(kit.FontCaption).TextColor(kt.Label).
					SingleLine().Ellipsis("…").MinWidth(0)
			})
			if chip.Clicked() {
				g.patternMenu = true
			}
			if kit.IconButton(c, kit.SymX, "清除自定义匹配", kit.IconButtonOptions{Size: 22, IconSize: 12}).Clicked() {
				f.Pattern, f.Exclude, g.patternMenu = "", "", false
			}
			ui.PopoverBase(c, chip, &g.patternMenu, func(panel ui.Element) {
				kit.Popup(c, panel).Width(320).Padding(12).Radius(16).Label("自定义匹配")
				kit.AttachPopover(c, panel, chip, ui.AnchorBottomStart, ui.AnchorTopStart, 6, 0)
				kit.PopoverBody(c, 8, func() {
					patternField(c, "pattern-include", "名称匹配", &f.Pattern)
					patternField(c, "pattern-exclude", "名称排除", &f.Exclude)
				})
			})
		})
	})
}

// patternField edits a pattern of the filter, its problem under it.
func patternField(c *ui.Context, key, label string, value *string) {
	ui.Column(c.Key(key)).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, label).FontSize(kit.FontCaption).FontWeight(600).TextColor(kit.T(c).Label2)
		kit.Input(c, key, value, "").Label(label).Font(kit.MonoFont)
		if _, err := model.CompileNodePattern(*value); err != nil {
			kit.FormError(c, "正则无效："+err.Error())
		}
	})
}
