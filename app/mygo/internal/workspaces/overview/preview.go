package overview

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// configViewer shows a configuration's JSON, searchable, with its matches
// highlighted, and copies it whole or redacted.
type configViewer struct {
	open    bool
	title   string
	content string

	lines  []string
	starts []int
	width  float32

	query   string
	index   int
	matches []int
	matched string // the content and query matches were found for
	// found is the query the matches were found for: the highlights use
	// its length, as the search field may change the query while the
	// frame is built.
	found  string
	reveal bool
	list   ui.ListState

	copied   string
	copiedAt time.Time
}

// show opens the viewer's dialog on content.
func (v *configViewer) show(title, content string) {
	v.open, v.title = true, title
	v.query, v.index, v.copied = "", 0, ""
	v.setContent(content)
}

// setContent changes the text shown, splitting it into lines.
func (v *configViewer) setContent(content string) {
	if content == v.content && v.lines != nil {
		return
	}
	v.content = content
	v.lines = strings.Split(content, "\n")
	v.starts = make([]int, len(v.lines))
	longest, offset := 0, 0
	for i, line := range v.lines {
		v.starts[i] = offset
		offset += len(line) + 1
		longest = max(longest, utf8.RuneCountInString(line))
	}
	// Monospaced 12 DIP text is about 7.3 DIPs a character.
	v.width = float32(longest)*7.3 + 32
	v.matched = "\x00"
}

// findMatches finds the query's matches in the content, once for each
// content and query.
func (v *configViewer) findMatches() {
	if v.matched == v.content+"\x00"+v.query {
		return
	}
	v.matched = v.content + "\x00" + v.query
	v.found = v.query
	v.matches = profiles.FindConfigMatches(v.content, v.query)
	v.reveal = len(v.matches) > 0
}

// active is the index of the current match, -1 without matches.
func (v *configViewer) active() int {
	if len(v.matches) == 0 {
		return -1
	}
	return min(v.index, len(v.matches)-1)
}

// step moves to the next (1) or previous (-1) match, wrapping around.
func (v *configViewer) step(delta int) {
	if n := len(v.matches); n > 0 {
		v.index = ((v.active()+delta)%n + n) % n
		v.reveal = true
	}
}

// lineOf is the line holding a byte offset.
func (v *configViewer) lineOf(offset int) int {
	lo, hi := 0, len(v.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if v.starts[mid] <= offset {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// copy writes the content, or its redacted form, to the clipboard.
func (v *configViewer) copy(c *ui.Context, e *env.Env, redacted bool) {
	text := v.content
	if redacted {
		var err error
		if text, err = profiles.RedactConfigForCopy(v.content); err != nil {
			e.Toast("脱敏失败，配置未复制")
			return
		}
	}
	c.WriteClipboard(text)
	v.copied, v.copiedAt = "raw", c.Now()
	message := "已复制配置"
	if redacted {
		v.copied, message = "redacted", "已复制脱敏配置"
	}
	e.Toast(message)
	c.After(1500 * time.Millisecond)
}

// view builds the search row, the copy actions and the text; disabled
// turns off copying, as for a configuration with errors.
func (v *configViewer) view(c *ui.Context, e *env.Env, height float32, disabled bool) {
	k := kit.T(c)
	v.findMatches()
	if v.copied != "" && c.Now().Sub(v.copiedAt) >= 1500*time.Millisecond {
		v.copied = ""
	}
	active := v.active()
	ui.Row(c).Gap(8).Shrink(0).Children(func() {
		search := kit.Search(c, "config-search", &v.query, kit.SearchOptions{Placeholder: "搜索 config.json", Width: 360})
		search.Label("搜索配置文件")
		if search.Changed() {
			v.index = 0
		}
		if search.Shortcut(ui.Shift, ui.KeyEnter) {
			v.step(-1)
		} else if search.Submitted() {
			v.step(1)
		}
		counter := ""
		if v.query != "" {
			counter = fmt.Sprintf("%d / %d", active+1, len(v.matches))
		}
		ui.Text(c, counter).FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").MinWidth(48).TextAlign(ui.End).Role(ui.RoleStatus)
		kit.ToolbarGroup(c, func() {
			kit.IconButton(c, kit.SymChevronLeft, "上一个匹配", kit.IconButtonOptions{Disabled: len(v.matches) == 0, IconSize: 15}).OnClick(func() { v.step(-1) })
			kit.IconButton(c, kit.SymChevronRight, "下一个匹配", kit.IconButtonOptions{Disabled: len(v.matches) == 0, IconSize: 15}).OnClick(func() { v.step(1) })
		}).Label("搜索结果导航")
		kit.ToolbarGroup(c, func() {
			rawLabel, rawIcon := "复制原始配置", kit.SymCopy
			if v.copied == "raw" {
				rawLabel, rawIcon = "已复制配置", kit.SymCheck
			}
			kit.IconButton(c, rawIcon, rawLabel, kit.IconButtonOptions{Disabled: disabled}).OnClick(func() { v.copy(c, e, false) })
			redactedLabel, redactedIcon := "复制脱敏配置（屏蔽地址、端口和认证信息）", kit.SymShieldCheck
			if v.copied == "redacted" {
				redactedLabel, redactedIcon = "已复制脱敏配置", kit.SymCheck
			}
			kit.IconButton(c, redactedIcon, redactedLabel, kit.IconButtonOptions{Disabled: disabled}).OnClick(func() { v.copy(c, e, true) })
		}).Label("复制配置")
	})
	// The search field may have changed the query: find its matches
	// before the lines highlight them.
	v.findMatches()
	active = v.active()
	if v.reveal && active >= 0 {
		v.list.ScrollIntoView(v.lineOf(v.matches[active]))
	}
	v.reveal = false
	byLine := map[int][]int{}
	for i, start := range v.matches {
		line := v.lineOf(start)
		byLine[line] = append(byLine[line], i)
	}
	ui.List(c, &v.list, len(v.lines), func(i int) {
		line := v.lines[i]
		hits := byLine[i]
		if len(hits) == 0 {
			ui.Text(c, line+" ").Font(kit.MonoFont).FontSize(kit.FontTable).NoWrap().TextColor(k.Label).PaddingX(12)
			return
		}
		var spans []ui.Span
		from := 0
		for _, m := range hits {
			start := v.matches[m] - v.starts[i]
			end := start + len(v.found)
			if start < from || end > len(line) {
				continue // a match the line cannot hold, never expected
			}
			spans = append(spans, ui.Span{Text: line[from:start]})
			highlight := ui.Span{Text: line[start:end], Background: k.WarningWeak, Color: k.Label, Radius: 2}
			if m == active {
				highlight.Background, highlight.Color = k.Accent, k.OnAccent
			}
			spans = append(spans, highlight)
			from = end
		}
		spans = append(spans, ui.Span{Text: line[from:] + " "})
		ui.RichText(c, spans...).Font(kit.MonoFont).FontSize(kit.FontTable).NoWrap().TextColor(k.Label).PaddingX(12)
	}).Height(height).RowWidth(v.width).PaddingY(10).Radius(kit.RadiusInput).Border(1, k.HairlineSoft).
		Background(k.Surface2).Label("配置文件内容")
}

// dialog shows the viewer in a wide dialog.
func (v *configViewer) dialog(c *ui.Context, e *env.Env) {
	_, h := c.Size()
	kit.Dialog(c, &v.open, v.title, 880, func() {
		v.view(c, e, max(200, min(h-200, 600)), false)
	}, nil)
}
