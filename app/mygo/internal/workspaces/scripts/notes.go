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
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// notesKeep is how many notifications the page keeps, the newest.
const notesKeep = 1000

// noteRow is a notification with an identity of its own, which a restart of
// the core does not reuse, its one-line text and its text folded for the
// search.
type noteRow struct {
	id      int
	n       api.ScriptNotification
	summary string
	lower   string
}

// noteSummary is the text of a notification under its title: the subtitle
// and the body on one line.
func noteSummary(n api.ScriptNotification) string {
	parts := make([]string, 0, 2)
	for _, p := range []string{n.Subtitle, n.Body} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, strings.Join(strings.Fields(p), " "))
		}
	}
	return strings.Join(parts, " · ")
}

// noteText is a notification as text, for copying.
func noteText(n api.ScriptNotification) string {
	lines := make([]string, 0, 4)
	for _, p := range []string{n.Title, n.Subtitle, n.Body, n.URL} {
		if p != "" {
			lines = append(lines, p)
		}
	}
	return strings.Join(lines, "\n")
}

// noteTime is a time of day, "12:00:01".
func noteTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return timefmt.FormatMilli(ms, "15:04:05")
}

// noteDateTime is a date and time of day, "2026-11-03 12:00:01".
func noteDateTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return timefmt.FormatMilli(ms, "2006-01-02 15:04:05")
}

type notesDerived struct {
	rows  []*noteRow
	total int
}

// NotesPage is the Notifications page of the scripts workspace: the notifications
// the installed scripts posted, followed live. The app has no channel for
// system notifications; they are listed here only.
type NotesPage struct {
	env *env.Env

	feed[api.ScriptNotification]

	entries []*noteRow
	nextID  int
	lastSeq api.Uint64
	tags    []string
	version int

	tag     string
	search  string
	paused  bool
	pauseID int

	table   kit.TableState
	selID   int
	viewer  slViewer
	derived *notesDerived
	key     string
}

// NewNotesPage returns the notification page of the server e talks to.
func NewNotesPage(e *env.Env) *NotesPage {
	p := &NotesPage{env: e}
	p.feed = feed[api.ScriptNotification]{
		env: e,
		open: func(ctx context.Context, after api.Uint64, handle func(api.ScriptNotification), onState func(bool)) error {
			return e.API.StreamScriptNotificationsState(ctx, after, handle, onState)
		},
		seq:  func(n api.ScriptNotification) api.Uint64 { return n.Seq },
		take: p.take,
	}
	p.table.List.FollowEnd = true
	return p
}

// SetActive follows the notifications while the page is visible. Those
// kept stay when it is not.
func (p *NotesPage) SetActive(active bool) { p.setActive(active) }

func (p *NotesPage) addTag(tag string) {
	if tag == "" || slices.Contains(p.tags, tag) {
		return
	}
	p.tags = append(p.tags, tag)
	slices.Sort(p.tags)
}

// take adds the notifications of one frame.
func (p *NotesPage) take(batch []api.ScriptNotification) {
	for _, n := range batch {
		p.add(n)
	}
	if len(p.entries) > notesKeep {
		p.entries = slices.Clone(p.entries[len(p.entries)-notesKeep:])
	}
	p.version++
}

// add appends a notification. One whose number is not above the one before
// comes from a core that restarted: what came from the one before goes.
func (p *NotesPage) add(n api.ScriptNotification) {
	if p.lastSeq != 0 && n.Seq <= p.lastSeq {
		p.entries, p.selID = nil, 0
		p.pauseID = p.nextID + 1
	}
	p.lastSeq = n.Seq
	p.nextID++
	p.addTag(n.Script)
	p.entries = append(p.entries, &noteRow{
		id: p.nextID, n: n, summary: noteSummary(n),
		lower: strings.ToLower(n.Script + "\n" + n.Title + "\n" + n.Subtitle + "\n" + n.Body + "\n" + n.URL),
	})
}

func (p *NotesPage) togglePause() {
	p.paused = !p.paused
	p.pauseID = p.nextID + 1
	p.version++
}

func (p *NotesPage) clear() {
	p.entries, p.selID = nil, 0
	p.version++
}

func (p *NotesPage) reset() {
	p.tag, p.search = "", ""
	p.version++
}

func (p *NotesPage) derive() *notesDerived {
	key := fmt.Sprint(p.version, p.tag, p.search, p.paused, p.pauseID)
	if p.derived != nil && key == p.key {
		return p.derived
	}
	d := &notesDerived{}
	query := strings.ToLower(strings.TrimSpace(p.search))
	for _, r := range p.entries {
		if p.paused && r.id >= p.pauseID {
			break
		}
		d.total++
		if p.tag != "" && r.n.Script != p.tag {
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

var notesColumns = []kit.Column{
	{ID: "time", Title: "时间", Width: 90},
	{ID: "script", Title: "脚本", Width: 140},
	{ID: "title", Title: "标题", MinWidth: 140, Grow: 1},
	{ID: "body", Title: "内容", MinWidth: 200, Grow: 2},
}

// notesNarrowColumns are the columns of a narrow window.
var notesNarrowColumns = []kit.Column{
	{ID: "time", Title: "时间", Width: 80},
	{ID: "title", Title: "标题", MinWidth: 120, Grow: 1},
	{ID: "body", Title: "内容", MinWidth: 120, Grow: 2},
}

// View builds the page, filling the area it is given.
func (p *NotesPage) View(c *ui.Context) {
	width, _ := c.Size()
	narrow := width <= slNarrow
	d := p.derive()
	selected := -1
	var row *noteRow
	for i, r := range d.rows {
		if r.id == p.selID {
			selected, row = i, r
			break
		}
	}
	filtered := p.tag != "" || strings.TrimSpace(p.search) != ""
	kit.Workspace(c, func() {
		p.toolbar(c, len(d.rows) > 0)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { p.list(c, d, selected, filtered, narrow) })
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

func (p *NotesPage) toolbar(c *ui.Context, hasRows bool) {
	ui.Row(c).PaddingX(16).PaddingY(8).Gap(10).Shrink(0).DragWindow().Children(func() {
		kit.Search(c, "notes-search", &p.search, kit.SearchOptions{Placeholder: "搜索标题、内容、脚本", Toolbar: true, Width: 260})
		ui.Spacer(c)
		kit.ToolbarGroup(c, func() {
			options := []kit.Option{{Value: "", Label: "全部脚本"}}
			for _, tag := range p.tags {
				options = append(options, kit.Option{Value: tag, Label: tag})
			}
			kit.Select(c, "notes-tag", &p.tag, options, kit.SelectOptions{Toolbar: true, Width: 150, Label: "脚本"})
			if hasRows && !p.table.List.AtEnd() {
				kit.IconButton(c, kit.SymArrowDown, "回到底部", kit.IconButtonOptions{}).OnClick(p.table.List.ScrollToEnd)
			}
			pause, label := kit.SymPause, "暂停滚动"
			if p.paused {
				pause, label = kit.SymPlay, "继续"
			}
			kit.IconButton(c, pause, label, kit.IconButtonOptions{Active: p.paused}).OnClick(p.togglePause)
			kit.IconButton(c, kit.SymTrash2, "清空通知", kit.IconButtonOptions{Disabled: len(p.entries) == 0}).OnClick(p.clear)
		})
	})
}

func (p *NotesPage) list(c *ui.Context, d *notesDerived, selected int, filtered, narrow bool) {
	switch {
	case len(p.entries) == 0:
		kit.EmptyState(c, kit.SymBell, "暂无通知", "", nil)
		return
	case len(d.rows) == 0:
		title := "没有匹配的通知"
		if !filtered {
			title = "暂无通知"
		}
		var action func()
		if filtered {
			action = func() { kit.Button(c, "清除筛选", kit.ButtonOptions{Small: true}).OnClick(p.reset) }
		}
		kit.EmptyState(c, kit.SymFileSearch, title, "", action)
		return
	}
	rows := d.rows
	cols := notesColumns
	if narrow {
		cols = notesNarrowColumns
	}
	p.table.Selected = selected
	list := kit.Table(c, &p.table, cols, len(rows), kit.TableOptions{
		Label: "脚本通知", Striped: true, KeepOffset: true,
		Key: func(i int) any { return rows[i].id },
	}, func(i, col int) {
		r := rows[i]
		switch cols[col].ID {
		case "time":
			ui.Text(c, noteTime(r.n.TimeMS)).Font(kit.MonoFont).SingleLine().FontFeatures("tnum")
		case "script":
			kit.Clipped(c, r.n.Script, kit.FontTable, 400)
		case "title":
			kit.Clipped(c, r.n.Title, kit.FontTable, 400)
		case "body":
			kit.Clipped(c, r.summary, kit.FontTable, 400).TextColor(kit.Muted(c))
		}
	})
	if list.Changed() {
		if i, ok := p.table.Chosen(len(rows)); ok && rows[i].id != p.selID {
			p.selID = rows[i].id
		}
	}
	if list.Shortcut(0, ui.KeyEscape) {
		p.selID = 0
	}
}

func (p *NotesPage) inspector(c *ui.Context, r *noteRow) {
	n := r.n
	kit.ScrollingInspector(c.Key(r.id), firstText(n.Title, n.Script), noteDateTime(n.TimeMS), nil, nil, func() { p.selID = 0 }, func() {
		kit.Section(c, "", func() {
			kit.IconButton(c, kit.SymCopy, "复制通知", kit.IconButtonOptions{Size: 24, IconSize: 14}).OnClick(func() { c.WriteClipboard(noteText(n)) })
		}, func() {
			kit.Property(c, "时间", noteDateTime(n.TimeMS))
			kit.Property(c, "脚本", firstText(n.Script, "—"))
			kit.Property(c, "标题", firstText(n.Title, "—"))
			if n.Subtitle != "" {
				kit.Property(c, "副标题", n.Subtitle)
			}
			if n.URL != "" {
				kit.Property(c, "链接", n.URL)
			}
		})
		if n.Body != "" {
			kit.Section(c, "内容", nil, func() {
				p.viewer.show(c, fmt.Sprint(r.id), n.Body, kit.LangText, "内容", slViewerHeight(n.Body))
			})
		}
	})
}

func (p *NotesPage) statusBar(c *ui.Context, d *notesDerived) {
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
