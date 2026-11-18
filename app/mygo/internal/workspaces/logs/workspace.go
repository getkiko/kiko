// Package logs is the console's Logs workspace: the core's log stream
// (GET /logs/core) or kiko's own lines from the server's event stream,
// with level filter, search, pause, follow-tail, copy, save and clear.
package logs

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// Sources of the segmented control.
const (
	SourceCore = "core"
	SourceApp  = "app"
)

// maxVisible is how many matching lines the view shows, the latest.
const maxVisible = 1000

// Workspace is the logs page. Its fields are UI-thread state.
type Workspace struct {
	env   *env.Env
	store *store.Store

	active  bool
	source  string
	entries []Entry
	nextID  int
	cancel  context.CancelFunc

	defaultLevel int
	// level is the shown level, -1 for the core's default.
	level  int
	query  string
	wrap   bool
	paused bool
	away   bool
	frozen []Entry
	notice string
	err    string

	list ui.ListState
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	w := &Workspace{env: e, store: s, source: SourceCore, defaultLevel: 4, level: -1, wrap: true}
	w.list.FollowEnd = true
	return w
}

// SetActive follows the core's log stream while the page shows.
func (w *Workspace) SetActive(active bool) {
	w.active = active
	w.sync()
}

// sync runs the core stream exactly while it is needed, and has the
// server's lines wake the window only while they show.
func (w *Workspace) sync() {
	w.env.Server.WatchLogs(w.active && w.source == SourceApp)
	want := w.active && w.source == SourceCore
	if want && w.cancel == nil {
		w.connect()
	} else if !want && w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
}

func (w *Workspace) connect() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.paused, w.away = false, false
	w.list.ScrollToEnd()
	go func() {
		for ctx.Err() == nil {
			err := w.env.API.CoreLogs(ctx, func(u api.CoreLogUpdate) {
				w.env.Post(func() {
					if ctx.Err() != nil {
						return
					}
					w.err = ""
					if u.DefaultLevel != nil {
						w.defaultLevel = *u.DefaultLevel
					}
					if u.Messages != nil || u.Reset {
						w.entries = AppendCoreLogs(w.entries, u, func() int { w.nextID++; return w.nextID })
					}
				})
			})
			if ctx.Err() != nil {
				return
			}
			message := "日志流已断开，正在重连…"
			if err != nil && err != context.Canceled {
				message = err.Error()
			}
			w.env.Post(func() {
				if ctx.Err() == nil {
					w.err = message
				}
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
}

// current is the live history of the chosen source.
func (w *Workspace) current() []Entry {
	if w.source == SourceCore {
		return w.entries
	}
	var out []Entry
	for i, line := range w.env.Server.Logs {
		if IsApplicationLog(line, false) {
			out = append(out, Entry{ID: w.env.Server.LogsVersion - len(w.env.Server.Logs) + i, Level: appLogLevel(line), Text: line})
		}
	}
	return out
}

// filtered is what matches the level and the search, frozen while paused
// or scrolled away from the end.
func (w *Workspace) filtered(current []Entry) []Entry {
	source := current
	if w.paused || w.away {
		source = w.frozen
	}
	level := w.level
	if level < 0 {
		level = w.defaultLevel
	}
	query := strings.ToLower(strings.TrimSpace(w.query))
	var out []Entry
	for _, e := range source {
		if e.Level <= level && strings.Contains(strings.ToLower(Plain(e.Text)), query) {
			out = append(out, e)
		}
	}
	return out
}

func (w *Workspace) setSource(source string) {
	w.source = source
	w.paused, w.away, w.notice = false, false, ""
	w.list.ScrollToEnd()
	w.sync()
}

func (w *Workspace) text(entries []Entry) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = Plain(e.Text)
	}
	return strings.Join(lines, "\n")
}

func (w *Workspace) save(entries []Entry) {
	data := []byte(w.text(entries))
	name := "logs-" + timefmt.Format(time.Now(), "2006-01-02-150405.000") + ".txt"
	w.env.Run(func(context.Context) error {
		_, err := w.env.Shell.SaveFile(name, data, env.FileFilter{Name: "文本", Extensions: []string{"txt"}})
		return err
	}, func(err error) {
		if err != nil {
			w.notice = err.Error()
		}
	})
}

func (w *Workspace) clear() {
	source := w.source
	w.env.Run(func(ctx context.Context) error {
		if source == SourceCore {
			return w.env.API.ClearCoreLogs(ctx)
		}
		return w.env.API.ClearLogs(ctx)
	}, func(err error) {
		if err != nil {
			w.notice = err.Error()
			return
		}
		if source == SourceApp {
			w.env.Server.ClearLogs()
		}
		w.notice = ""
		w.frozen, w.paused, w.away = nil, false, false
		w.list.ScrollToEnd()
	})
}

// ---- view ----

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	k := kit.T(c)
	current := w.current()
	// Scrolling away from the end freezes the lines in view.
	if w.list.AtEnd() {
		w.away = false
	} else if !w.away && len(current) > 0 {
		if !w.paused {
			w.frozen = current
		}
		w.away = true
	}
	matching := w.filtered(current)
	visible := matching
	if len(visible) > maxVisible {
		visible = visible[len(visible)-maxVisible:]
	}
	kit.Workspace(c, func() {
		kit.Toolbar(c, "日志", "", func() {
			sources := []string{SourceCore, SourceApp}
			chosen := 0
			if w.source == SourceApp {
				chosen = 1
			}
			if kit.Segmented(c, "source", &chosen, kit.Segment{Label: "核心"}, kit.Segment{Label: "应用"}).Label("日志来源").Changed() {
				w.setSource(sources[chosen])
			}
			kit.Search(c, "search", &w.query, kit.SearchOptions{Placeholder: "搜索日志…", Toolbar: true, Width: 260})
			kit.ToolbarGroup(c, func() {
				label, icon := "暂停日志", kit.SymPause
				if w.paused {
					label, icon = "继续日志", kit.SymPlay
				}
				if kit.IconButton(c, icon, label, kit.IconButtonOptions{Active: w.paused}).Clicked() {
					if !w.paused && !w.away {
						w.frozen = current
					}
					w.paused = !w.paused
				}
				kit.IconButton(c, kit.SymEllipsis, "日志操作", kit.IconButtonOptions{}).PopoverMenuButton(func(m *ui.Menu) { w.menu(c, m, matching) })
			})
		})
		ui.Column(c).Grow(1).MinHeight(0).Padding(0, 20, 16).Gap(8).Children(func() {
			status := w.notice
			if w.source == SourceCore && w.err != "" {
				status = w.err
			}
			if status != "" {
				ui.Text(c, status).FontSize(kit.FontTable).TextColor(k.Label3).Shrink(0).Wrap()
			}
			ui.Box(c).Grow(1).MinHeight(0).Children(func() {
				w.lines(c, visible, len(current) > 0)
				if w.away {
					b := kit.IconButton(c, kit.SymArrowDown, "滚动到日志底部", kit.IconButtonOptions{Size: 32})
					b.Absolute().Right(12).Bottom(12).Background(k.Surface).Border(1, k.HairlineSoft).Shadow(0, 1, 3, 0, k.Shadow)
					if b.Clicked() {
						w.away = false
						w.list.ScrollToEnd()
					}
				}
			})
		})
	})
}

func (w *Workspace) menu(c *ui.Context, m *ui.Menu, matching []Entry) {
	m.Item("日志显示等级").Disabled(true)
	if m.Item("默认 (" + levelName(w.defaultLevel) + ")").Checked(w.level < 0).Chosen() {
		w.level = -1
	}
	for _, level := range []int{2, 3, 4, 5, 6} {
		if m.Item(Levels[level]).Checked(w.level == level).Chosen() {
			w.level = level
		}
	}
	m.Separator()
	m.Item("保存筛选后的日志").Disabled(true)
	if m.Item("复制到剪贴板").Chosen() {
		c.WriteClipboard(w.text(matching))
		w.notice = "日志已复制"
	}
	if m.Item("保存到文件").Chosen() {
		w.save(matching)
	}
	wrapLabel := "自动换行"
	if w.wrap {
		wrapLabel = "关闭自动换行"
	}
	if m.Item(wrapLabel).Chosen() {
		w.wrap = !w.wrap
	}
	m.Separator()
	if m.Item("清空日志").Chosen() {
		w.clear()
	}
}

func (w *Workspace) lines(c *ui.Context, visible []Entry, received bool) {
	k := kit.T(c)
	box := ui.Column(c).Fill().Radius(kit.RadiusGroup).Border(1, k.HairlineSoft).Background(k.Surface).Clip()
	box.Children(func() {
		if len(visible) == 0 {
			text := "等待日志…"
			if received {
				text = "没有匹配的日志"
			}
			ui.Text(c, text).Font(kit.MonoFont).FontSize(11).TextColor(k.Label3).Padding(12, 14)
			return
		}
		w.list.Key = func(row int) any { return visible[row].ID }
		query := strings.TrimSpace(w.query)
		// The list is one Selectable container, so a drag selects across
		// lines and Cmd+C copies them joined by newlines.
		list := ui.List(c, &w.list, len(visible)).Grow(1).MinHeight(0).Padding(12, 14).Label("日志").Selectable()
		if !w.wrap {
			list.RowWidth(2400)
		}
		list.Rows(func(r ui.ListRow) {
			e := visible[r.Index]
			text := ui.RichText(r.Context, spans(k, ParseLine(e.Text), query)...).Font(kit.MonoFont).FontSize(11).
				LineHeight(1.65).TextColor(k.Label)
			if w.wrap {
				text.Wrap()
			} else {
				text.SingleLine()
			}
		})
	})
}

// spans renders segments, mixing terminal colors with the label color so
// that they keep their contrast in both themes, and marking the search.
func spans(k *kit.Tokens, segments []Segment, query string) []ui.Span {
	var out []ui.Span
	var pattern *regexp.Regexp
	if query != "" {
		pattern = regexp.MustCompile("(?i)" + regexp.QuoteMeta(query))
	}
	for _, s := range segments {
		base := ui.Span{Weight: s.Style.Weight, Italic: s.Style.Italic,
			Underline: s.Style.Decoration == "underline", Strikethrough: s.Style.Decoration == "line-through"}
		if color, ok := cssColor(s.Style.Color); ok {
			base.Color = color.Mix(k.Label, 0.35)
		}
		if bg, ok := cssColor(s.Style.Background); ok {
			base.Background = bg
		}
		if s.Style.Opacity > 0 {
			fg := base.Color
			if fg == (ui.Color{}) {
				fg = k.Label
			}
			base.Color = fg.Alpha(float32(s.Style.Opacity))
		}
		if pattern == nil {
			span := base
			span.Text = s.Text
			out = append(out, span)
			continue
		}
		cursor := 0
		for _, m := range pattern.FindAllStringIndex(s.Text, -1) {
			if m[0] > cursor {
				span := base
				span.Text = s.Text[cursor:m[0]]
				out = append(out, span)
			}
			mark := base
			mark.Text, mark.Background, mark.Radius = s.Text[m[0]:m[1]], k.AccentWeak, 2
			out = append(out, mark)
			cursor = m[1]
		}
		if cursor < len(s.Text) {
			span := base
			span.Text = s.Text[cursor:]
			out = append(out, span)
		}
	}
	return out
}

var rgbPattern = regexp.MustCompile(`^rgb\((\d+), (\d+), (\d+)\)$`)

// cssColor parses the colors ParseLine produces.
func cssColor(s string) (ui.Color, bool) {
	if strings.HasPrefix(s, "#") {
		return ui.Hex(s), true
	}
	if m := rgbPattern.FindStringSubmatch(s); m != nil {
		r, _ := strconv.Atoi(m[1])
		g, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		return ui.RGBA(uint8(r), uint8(g), uint8(b), 1), true
	}
	return ui.Color{}, false
}

// levelName names a level index from the server, which may be out of range.
func levelName(level int) string {
	if level < 0 || level >= len(Levels) {
		return "未知"
	}
	return Levels[level]
}
