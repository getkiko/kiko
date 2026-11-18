package scripts

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// logsKeep is how many lines the page keeps, the newest.
const logsKeep = 5000

// Level filters of the page, in the segmented control's order.
var logsLevels = []struct{ label, level string }{
	{"全部", ""}, {"错误", api.ScriptLogLevelError}, {"警告", api.ScriptLogLevelWarn},
	{"信息", api.ScriptLogLevelInfo}, {"日志", api.ScriptLogLevelLog},
}

// logsLevelName is a level as the page shows it.
func logsLevelName(level string) string {
	switch level {
	case api.ScriptLogLevelError:
		return "错误"
	case api.ScriptLogLevelWarn:
		return "警告"
	case api.ScriptLogLevelInfo:
		return "信息"
	case api.ScriptLogLevelLog:
		return "日志"
	}
	return level
}

func logsTone(level string) kit.Tone {
	switch level {
	case api.ScriptLogLevelError:
		return kit.DangerTone
	case api.ScriptLogLevelWarn:
		return kit.WarningTone
	case api.ScriptLogLevelInfo:
		return kit.AccentTone
	}
	return kit.Neutral
}

// logsPhaseName is the phase of a run as the page shows it.
func logsPhaseName(phase string) string {
	switch phase {
	case "":
		return "—"
	case "http-request", "request":
		return "请求"
	case "http-response", "response":
		return "响应"
	case "generic":
		return "通用"
	case "cron":
		return "定时"
	case "rule":
		return "规则"
	case "dns":
		return "DNS"
	}
	return phase
}

// logRow is a line with an identity of its own, which a restart of the
// core does not reuse, and its text folded for the search.
type logRow struct {
	id    int
	entry api.ScriptLogEntry
	lower string
}

type logsDerived struct {
	rows  []*logRow
	total int
}

// LogsPage is the Logs page of the scripts workspace: the console lines
// of the installed scripts, followed live.
type LogsPage struct {
	env *env.Env

	feed[api.ScriptLogEntry]

	entries []*logRow
	nextID  int
	lastSeq api.Uint64
	tags    []string
	version int

	level   int
	tag     string
	search  string
	paused  bool
	pauseID int

	table   kit.TableState
	selID   int
	copied  bool
	viewer  slViewer
	derived *logsDerived
	key     string
}

// NewLogsPage returns the log page of the server e talks to.
func NewLogsPage(e *env.Env) *LogsPage {
	p := &LogsPage{env: e}
	p.feed = feed[api.ScriptLogEntry]{
		env: e,
		open: func(ctx context.Context, after api.Uint64, handle func(api.ScriptLogEntry), onState func(bool)) error {
			return e.API.StreamScriptLogsState(ctx, after, "", handle, onState)
		},
		seq:  func(l api.ScriptLogEntry) api.Uint64 { return l.Seq },
		take: p.take,
	}
	p.table.List.FollowEnd = true
	return p
}

// SetTag shows only the lines of the script tag; "" shows all.
func (p *LogsPage) SetTag(tag string) {
	p.addTag(tag)
	p.tag = tag
	p.version++
}

func (p *LogsPage) addTag(tag string) {
	if tag == "" || slices.Contains(p.tags, tag) {
		return
	}
	p.tags = append(p.tags, tag)
	slices.Sort(p.tags)
}

// SetActive follows the log while the page is visible. The lines kept stay
// when it is not.
func (p *LogsPage) SetActive(active bool) { p.setActive(active) }

// take adds the lines of one frame.
func (p *LogsPage) take(batch []api.ScriptLogEntry) {
	for _, e := range batch {
		p.add(e)
	}
	if len(p.entries) > logsKeep {
		p.entries = slices.Clone(p.entries[len(p.entries)-logsKeep:])
	}
	p.version++
}

// add appends a line. A line whose number is not above the one before
// comes from a core that restarted: what came from the one before goes.
func (p *LogsPage) add(e api.ScriptLogEntry) {
	if p.lastSeq != 0 && e.Seq <= p.lastSeq {
		p.entries, p.selID = nil, 0
		p.pauseID = p.nextID + 1
	}
	p.lastSeq = e.Seq
	p.nextID++
	p.addTag(e.Script)
	p.entries = append(p.entries, &logRow{id: p.nextID, entry: e, lower: strings.ToLower(e.Script + "\n" + e.Message)})
}

func (p *LogsPage) togglePause() {
	p.paused = !p.paused
	p.pauseID = p.nextID + 1
	p.version++
}

func (p *LogsPage) clear() {
	p.entries, p.selID = nil, 0
	p.version++
}

func (p *LogsPage) reset() {
	p.level, p.tag, p.search = 0, "", ""
	p.version++
}

func (p *LogsPage) derive() *logsDerived {
	key := fmt.Sprint(p.version, p.level, p.tag, p.search, p.paused, p.pauseID)
	if p.derived != nil && key == p.key {
		return p.derived
	}
	d := &logsDerived{}
	level := logsLevels[p.level].level
	query := strings.ToLower(strings.TrimSpace(p.search))
	for _, r := range p.entries {
		if p.paused && r.id >= p.pauseID {
			break
		}
		d.total++
		if level != "" && r.entry.Level != level {
			continue
		}
		if p.tag != "" && r.entry.Script != p.tag {
			continue
		}
		if query != "" && !strings.Contains(r.lower, query) {
			continue
		}
		d.rows = append(d.rows, r)
	}
	p.derived, p.key = d, key
	return d
}

var logsColumns = []kit.Column{
	{ID: "time", Title: "时间", Width: 110},
	{ID: "script", Title: "脚本", Width: 150},
	{ID: "phase", Title: "阶段", Width: 70},
	{ID: "level", Title: "级别", Width: 84},
	{ID: "message", Title: "消息", MinWidth: 260, Grow: 1},
}

// View builds the page, filling the area it is given.
func (p *LogsPage) View(c *ui.Context) {
	width, _ := c.Size()
	narrow := width <= slNarrow
	d := p.derive()
	selected := -1
	var row *logRow
	for i, r := range d.rows {
		if r.id == p.selID {
			selected, row = i, r
			break
		}
	}
	filtered := p.level != 0 || p.tag != "" || strings.TrimSpace(p.search) != ""
	kit.Workspace(c, func() {
		p.toolbar(c, width, len(d.rows) > 0)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { p.list(c, d, selected, filtered) })
			if row != nil && !narrow {
				p.inspector(c, row)
			}
		})
		p.statusBar(c, d)
	})
	if narrow && row != nil {
		slSidePanel(c, true, func() { p.selID = 0 }, func() { p.inspector(c, row) })
	}
}

func (p *LogsPage) toolbar(c *ui.Context, width float32, hasRows bool) {
	segments := make([]kit.Segment, len(logsLevels))
	for i, l := range logsLevels {
		segments[i] = kit.Segment{Label: l.label}
	}
	levels := func() {
		kit.Segmented(c, "logs-level", &p.level, segments...).Label("日志级别")
	}
	controls := func() {
		kit.ToolbarGroup(c, func() {
			options := []kit.Option{{Value: "", Label: "全部脚本"}}
			for _, tag := range p.tags {
				options = append(options, kit.Option{Value: tag, Label: tag})
			}
			kit.Select(c, "logs-tag", &p.tag, options, kit.SelectOptions{Toolbar: true, Width: 150, Label: "脚本"})
			if hasRows && !p.table.List.AtEnd() {
				kit.IconButton(c, kit.SymArrowDown, "回到底部", kit.IconButtonOptions{}).OnClick(p.table.List.ScrollToEnd)
			}
			pause, label := kit.SymPause, "暂停滚动"
			if p.paused {
				pause, label = kit.SymPlay, "继续"
			}
			kit.IconButton(c, pause, label, kit.IconButtonOptions{Active: p.paused}).OnClick(p.togglePause)
			kit.IconButton(c, kit.SymTrash2, "清空日志", kit.IconButtonOptions{Disabled: len(p.entries) == 0}).OnClick(p.clear)
		})
	}
	search := func() {
		kit.Search(c, "logs-search", &p.search, kit.SearchOptions{Placeholder: "搜索消息、脚本", Toolbar: true, Width: 260})
	}
	if width < 900 {
		ui.Column(c).PaddingX(16).PaddingY(8).Gap(8).Shrink(0).Children(func() {
			ui.Row(c).Gap(10).Children(func() {
				search()
				ui.Spacer(c)
				controls()
			})
			ui.Row(c).Children(levels)
		})
		return
	}
	ui.Row(c).PaddingX(16).PaddingY(8).Gap(10).Shrink(0).DragWindow().Children(func() {
		search()
		levels()
		ui.Spacer(c)
		controls()
	})
}

func (p *LogsPage) list(c *ui.Context, d *logsDerived, selected int, filtered bool) {
	switch {
	case len(p.entries) == 0:
		kit.EmptyState(c, kit.SymTerminal, "暂无日志", "", nil)
		return
	case len(d.rows) == 0:
		title := "没有匹配的日志"
		if !filtered {
			title = "暂无日志"
		}
		var action func()
		if filtered {
			action = func() { kit.Button(c, "清除筛选", kit.ButtonOptions{Small: true}).OnClick(p.reset) }
		}
		kit.EmptyState(c, kit.SymFileSearch, title, "", action)
		return
	}
	rows := d.rows
	p.table.Selected = selected
	list := kit.Table(c, &p.table, logsColumns, len(rows), kit.TableOptions{
		Label: "脚本日志", Striped: true, KeepOffset: true,
		Key: func(i int) any { return rows[i].id },
	}, func(i, col int) {
		r := rows[i]
		switch logsColumns[col].ID {
		case "time":
			ui.Text(c, slTime(r.entry.TimeMS)).Font(kit.MonoFont).SingleLine().FontFeatures("tnum")
		case "script":
			kit.Clipped(c, r.entry.Script, kit.FontTable, 400)
		case "phase":
			ui.Text(c, logsPhaseName(r.entry.Phase)).SingleLine().TextColor(kit.Muted(c))
		case "level":
			kit.Status(c, logsLevelName(r.entry.Level), logsTone(r.entry.Level))
		case "message":
			kit.Clipped(c, storePreview(r.entry.Message, false), kit.FontTable, 400).Font(kit.MonoFont)
		}
	})
	if list.Changed() {
		if i, ok := p.table.Chosen(len(rows)); ok && rows[i].id != p.selID {
			p.selID, p.copied = rows[i].id, false
		}
	}
	if list.Shortcut(0, ui.KeyEscape) {
		p.selID = 0
	}
}

func (p *LogsPage) inspector(c *ui.Context, r *logRow) {
	e := r.entry
	text := e.Message
	height := slViewerHeight(text)
	kit.ScrollingInspector(c.Key(r.id), e.Script, slTime(e.TimeMS), nil, nil, func() { p.selID = 0 }, func() {
		kit.Section(c, "", nil, func() {
			kit.Property(c, "时间", slTime(e.TimeMS))
			kit.Property(c, "脚本", e.Script)
			kit.Property(c, "阶段", logsPhaseName(e.Phase))
			kit.PropertyWith(c, "级别", func() { kit.Status(c, logsLevelName(e.Level), logsTone(e.Level)) })
		})
		kit.Section(c, "消息", func() {
			kit.IconButton(c, kit.SymCopy, "复制消息", kit.IconButtonOptions{Size: 24, IconSize: 14}).OnClick(func() { c.WriteClipboard(text) })
		}, func() {
			p.viewer.show(c, fmt.Sprint(r.id), text, kit.LangText, "消息", height)
		})
	})
}

func (p *LogsPage) statusBar(c *ui.Context, d *logsDerived) {
	k := kit.T(c)
	left := fmt.Sprintf("%s 条", kit.Count(len(d.rows)))
	if len(d.rows) != d.total {
		left = fmt.Sprintf("%s / %s 条", kit.Count(len(d.rows)), kit.Count(d.total))
	}
	center := ""
	if p.paused {
		center = "已暂停"
	}
	kit.StatusBar(c, left, center, func() {
		if p.disconnected {
			ui.Row(c).Gap(4).Children(func() {
				kit.Icon(c, kit.SymUnplug, 13).TextColor(k.Warning)
				ui.Text(c, "已断开").SingleLine()
			})
		}
	})
}
