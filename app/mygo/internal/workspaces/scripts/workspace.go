package scripts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// pollEvery is how often the run counters are read while the list shows.
const pollEvery = 3 * time.Second

// columns are the list's columns.
var columns = []kit.Column{
	{ID: "index", Title: "序号", Width: 52, Align: ui.End},
	{ID: "enabled", Title: "启用", Width: 60},
	{ID: "name", Title: "名称", Width: 160},
	{ID: "type", Title: "类型", Width: 60},
	{ID: "pattern", Title: "匹配模式", MinWidth: 200, Grow: 3},
	{ID: "runs", Title: "运行", Width: 110},
	{ID: "status", Title: "状态", Width: 150},
	{ID: "ops", Title: "操作", Width: 152},
}

// NarrowWidth is the window width up to which the list drops columns and
// the host stacks its toolbar.
const NarrowWidth = 800

// TinyWidth is the window width up to which the list keeps only the switch,
// the name, the counters and the actions.
const TinyWidth = 480

// narrowColumns are the columns of a narrow window; its actions are edit
// and More, whose menu holds the rest.
var narrowColumns = []kit.Column{
	{ID: "enabled", Title: "启用", Width: 56},
	{ID: "name", Title: "名称", Width: 120},
	{ID: "pattern", Title: "匹配模式", MinWidth: 80, Grow: 3},
	{ID: "runs", Title: "运行", Width: 72},
	{ID: "ops", Title: "操作", Width: 60},
}

// tinyColumns are the columns of a window up to TinyWidth.
var tinyColumns = []kit.Column{
	{ID: "enabled", Title: "启用", Width: 52},
	{ID: "name", Title: "名称", MinWidth: 80, Grow: 1},
	{ID: "runs", Title: "运行", Width: 56},
	{ID: "ops", Title: "操作", Width: 60},
}

// Sub pages the toolbar opens.
const (
	subNone = iota
	subStore
	subLogs
	subNotes
)

// Workspace is the script list with the pages it opens. The Rewrites workspace
// shows it as its last tab: it draws Toolbar in its own toolbar and
// Body under it.
type Workspace struct {
	env     *env.Env
	doc     Doc
	editors *Editors
	store   *StorePage
	logs    *LogsPage
	notes   *NotesPage

	active bool
	// hidden is set while the host shows another tab: the sub pages stop
	// as when the page is away, and resume as the tab returns.
	hidden bool
	table  kit.TableState

	// confirm asks before a script is deleted, anchored to the row's More
	// button; target is the script it asks about.
	confirm kit.ConfirmState
	target  string
	// runs are the manual runs by script ID.
	runs map[string]*runState
	// reveal is a script to select and scroll to once it is listed.
	reveal string
	// refocus returns the keyboard focus to the table after the editor
	// closed.
	refocus bool

	// narrow is set while the list shows the columns of a narrow window.
	narrow bool

	// sub is the page that replaces the list; backTo is the toolbar
	// button that takes the focus back as the list returns.
	sub     int
	backTo  int
	focusUp bool

	// stats are the run counters by tag, valid while statsOK.
	stats    map[string]api.ScriptState
	statsOK  bool
	polling  bool
	lastPoll time.Time
	gen      int
}

// New returns the workspace over the document d.
func New(e *env.Env, d Doc) *Workspace {
	w := &Workspace{env: e, doc: d, store: NewStorePage(e), logs: NewLogsPage(e), notes: NewNotesPage(e)}
	w.editors = NewEditors(e, d)
	w.editors.OnSaved = func(id string) { w.reveal = id }
	w.table.Clear()
	return w
}

// Editors returns the script editors the workspace builds.
func (w *Workspace) Editors() *Editors { return w.editors }

// SetActive follows whether the page shows: the sub pages and the polling
// run only then, and what is open closes as the page stops.
func (w *Workspace) SetActive(active bool) {
	w.active = active
	if !active {
		w.editors.Close()
		w.confirm.Open = false
		w.stopRuns()
		w.sub = subNone
		w.gen++
		w.polling = false
	}
	w.syncSub()
}

// SetHidden tells that the host shows another tab than the scripts: the sub
// pages stop reading until the tab shows again.
func (w *Workspace) SetHidden(hidden bool) {
	if hidden == w.hidden {
		return
	}
	w.hidden = hidden
	w.syncSub()
}

// Running reports whether a sub page is reading from the core.
func (w *Workspace) Running() bool { return w.store.active || w.logs.active || w.notes.active }

func (w *Workspace) syncSub() {
	on := w.active && !w.hidden
	w.store.SetActive(on && w.sub == subStore)
	w.logs.SetActive(on && w.sub == subLogs)
	w.notes.SetActive(on && w.sub == subNotes)
}

// Add opens the editor for a new script.
func (w *Workspace) Add(seed Seed) { w.editors.New(seed) }

// InSub reports whether a sub page replaces the list; the host then shows
// SubView instead of its toolbar and Body.
func (w *Workspace) InSub() bool { return w.sub != subNone }

// Back returns from a sub page to the list.
func (w *Workspace) Back() {
	if w.sub == subNone {
		return
	}
	w.backTo = w.sub
	w.sub = subNone
	w.focusUp = true
	w.syncSub()
}

func (w *Workspace) openSub(sub int) {
	w.sub, w.focusUp = sub, true
	w.confirm.Open = false
	w.syncSub()
}

// OpenLogs shows the log page limited to the script with tag.
func (w *Workspace) OpenLogs(tag string) {
	w.logs.SetTag(tag)
	w.openSub(subLogs)
}

// update changes the document, showing a refusal as a toast.
func (w *Workspace) update(fn func(d *model.Doc)) {
	if w.doc == nil {
		return
	}
	w.env.ToastError(w.doc.Update(fn))
}

// Toolbar is the list's action group: add, store and log.
func (w *Workspace) Toolbar(c *ui.Context) {
	kit.ToolbarGroup(c, func() {
		if kit.IconButton(c, kit.SymPlus, "添加脚本", kit.IconButtonOptions{}).Clicked() {
			w.Add(Seed{})
		}
		store := kit.IconButton(c, kit.SymDatabase, "存储", kit.IconButtonOptions{})
		if store.Clicked() {
			w.openSub(subStore)
		}
		logs := kit.IconButton(c, kit.SymTerminal, "日志", kit.IconButtonOptions{})
		if logs.Clicked() {
			w.logs.SetTag("")
			w.openSub(subLogs)
		}
		notes := kit.IconButton(c, kit.SymBell, "通知", kit.IconButtonOptions{})
		if notes.Clicked() {
			w.openSub(subNotes)
		}
		if w.focusUp && w.sub == subNone {
			switch w.backTo {
			case subStore:
				store.Focus()
			case subLogs:
				logs.Focus()
			case subNotes:
				notes.Focus()
			}
			w.focusUp, w.backTo = false, subNone
		}
	})
}

// SubView builds the open sub page under a row that returns to the list.
func (w *Workspace) SubView(c *ui.Context) {
	title := "存储"
	switch w.sub {
	case subLogs:
		title = "日志"
	case subNotes:
		title = "通知"
	}
	page := ui.Column(c).Grow(1).MinHeight(0)
	page.Children(func() {
		ui.Row(c).PaddingX(12).PaddingY(8).Gap(6).Shrink(0).DragWindow().Children(func() {
			back := kit.Button(c, "脚本", kit.ButtonOptions{Variant: kit.TextButton, Icon: kit.SymChevronLeft})
			if w.focusUp {
				back.Focus()
				w.focusUp = false
			}
			if back.Clicked() {
				w.Back()
			}
			ui.Text(c, title).FontSize(16).FontWeight(600).SingleLine()
		})
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			switch w.sub {
			case subLogs:
				w.logs.View(c)
			case subNotes:
				w.notes.View(c)
			default:
				w.store.View(c)
			}
		})
	})
	if page.Shortcut(0, ui.KeyEscape) {
		w.Back()
	}
}

// supported reports whether the core is known to lack scripts.
func (w *Workspace) unsupported() bool {
	info := w.env.Server.Info
	if info == nil {
		return false
	}
	on, ok := info.Capabilities["scripts"]
	return ok && !on
}

// poll reads the run counters every pollEvery while the list shows with
// decryption on.
func (w *Workspace) poll(c *ui.Context, doc *model.Doc) {
	if !w.active || w.env == nil || w.env.API == nil || !MITMEnabled(doc) || w.unsupported() || Count(doc) == 0 {
		// Nothing runs: the numbers of before are stale, and a poll in
		// flight must not bring them back.
		if w.statsOK || w.stats != nil || w.polling {
			w.stats, w.statsOK = nil, false
			w.polling = false
			w.lastPoll = time.Time{}
			w.gen++
		}
		return
	}
	now := c.Now()
	c.After(pollEvery)
	if w.polling || (!w.lastPoll.IsZero() && now.Sub(w.lastPoll) < pollEvery) {
		return
	}
	w.polling, w.lastPoll = true, now
	gen := w.gen
	env.Go(w.env, func(ctx context.Context) (api.ScriptStatus, error) {
		return w.env.API.ScriptStatus(ctx)
	}, func(st api.ScriptStatus, err error) {
		if gen != w.gen {
			return
		}
		w.polling = false
		if err != nil {
			w.stats, w.statsOK = nil, false
			return
		}
		w.stats = make(map[string]api.ScriptState, len(st.Scripts))
		for _, s := range st.Scripts {
			w.stats[s.Tag] = s
		}
		w.statsOK = true
	})
}

// Body builds the list under the host's toolbar: the notices, the table or
// the empty state that takes its place, and the status bar. search is the
// text of the host's search field.
func (w *Workspace) Body(c *ui.Context, search string, openSettings func(c *ui.Context)) {
	if w.editors.TakeClosed() {
		w.refocus = true
	}
	doc := w.doc.Doc()
	w.poll(c, doc)
	all := rowsOf(doc)
	query := strings.ToLower(strings.TrimSpace(search))
	var rows []scriptRow
	for _, r := range all {
		if r.matches(query) {
			rows = append(rows, r)
		}
	}
	if w.unsupported() {
		ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
			kit.Notice(c, kit.WarningTone, "核心不支持脚本", "", nil).Grow(1)
		})
	}
	if !MITMEnabled(doc) && len(all) > 0 {
		ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
			kit.Notice(c, kit.WarningTone, "未开启 HTTPS 解密", "", func() { openSettings(c) }).Grow(1)
		})
	}
	switch {
	case len(all) == 0:
		w.empty(c, doc, openSettings)
	case len(rows) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			kit.EmptyState(c, kit.SymFileSearch, "没有符合条件的脚本", "", nil)
		})
		w.statusBar(c, len(rows), len(all))
	default:
		w.list(c, rows, strings.TrimSpace(search) != "")
		w.statusBar(c, len(rows), len(all))
	}
}

func (w *Workspace) statusBar(c *ui.Context, shown, all int) {
	text := fmt.Sprintf("%s 个脚本", kit.Count(all))
	if shown != all {
		text = fmt.Sprintf("%s / %s 个脚本", kit.Count(shown), kit.Count(all))
	}
	kit.StatusBar(c, text, "", nil)
}

// empty fills the area without scripts.
func (w *Workspace) empty(c *ui.Context, doc *model.Doc, openSettings func(c *ui.Context)) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		add := func() {
			kit.Button(c, "添加脚本", kit.ButtonOptions{Small: true, Variant: kit.Primary, Icon: kit.SymPlus}).
				OnClick(func() { w.Add(Seed{}) })
		}
		if !MITMEnabled(doc) {
			kit.EmptyState(c, kit.SymLock, "未开启 HTTPS 解密", "", func() {
				ui.Row(c).Gap(8).Children(func() {
					openSettings(c)
					add()
				})
			})
			return
		}
		kit.EmptyState(c, kit.SymFileCode, "暂无脚本", "", add)
	})
}

// list is the table of the rows.
func (w *Workspace) list(c *ui.Context, rows []scriptRow, filtered bool) {
	state := &w.table
	cols := columns
	w.narrow = false
	if width, _ := c.Size(); width <= NarrowWidth {
		cols, w.narrow = narrowColumns, true
		if width <= TinyWidth {
			cols = tinyColumns
		}
	}
	if w.reveal != "" {
		for i, r := range rows {
			if r.ID == w.reveal {
				state.Selected = i
				state.List.ScrollTo(i, ui.Center)
				w.reveal = ""
			}
		}
	}
	list := kit.Table(c, state, cols, len(rows), kit.TableOptions{
		Label: "脚本", Density: kit.Compact, Striped: true,
		Key:    func(row int) any { return rows[row].ID },
		Dimmed: func(row int) bool { return rows[row].Disabled },
		Menu: func(menu *ui.Menu, row int) {
			w.menuItems(menu, rows[row], filtered, true)
		},
	}, func(row, col int) { w.cell(c, rows[row], filtered, cols[col].ID) })
	if w.refocus {
		list.Focus()
		w.refocus = false
	}
	if list.Submitted() {
		if i, ok := state.Chosen(len(rows)); ok {
			w.editors.Edit(rows[i].ID)
		}
	}
}

func (w *Workspace) total() int { return Count(w.doc.Doc()) }

// menuItems are the actions of a script, for its context menu and its More
// menu.
func (w *Workspace) menuItems(menu *ui.Menu, r scriptRow, filtered, context bool) {
	id := r.ID
	if menu.Item("编辑").Chosen() {
		w.editors.Edit(id)
	}
	if menu.Item("复制").Disabled(w.total() >= model.MaxScripts).Chosen() {
		w.update(func(d *model.Doc) {
			if copied := duplicateScript(d, id); copied != "" {
				w.reveal = copied
			}
		})
	}
	toggle := "停用"
	if r.Disabled {
		toggle = "启用"
	}
	if menu.Item(toggle).Chosen() {
		disabled := !r.Disabled
		w.update(func(d *model.Doc) { disableScript(d, id, disabled) })
	}
	if r.IsRun() {
		reason := w.runBlock(r)
		if menu.Item("立即运行").Disabled(reason != "" || w.runOf(id).running).Chosen() {
			w.startRun(r, true)
		}
	}
	if menu.Item("查看日志").Chosen() {
		w.OpenLogs(r.EffectiveTag())
	}
	if context {
		if menu.Item("上移").Disabled(filtered || r.pos == 0).Chosen() {
			w.move(id, -1)
		}
		if menu.Item("下移").Disabled(filtered || r.pos >= w.total()-1).Chosen() {
			w.move(id, 1)
		}
	}
	menu.Separator()
	if menu.Item("删除").Chosen() {
		w.target, w.confirm.Open = id, true
	}
}

// move shifts a script one place and keeps it chosen.
func (w *Workspace) move(id string, delta int) {
	w.update(func(d *model.Doc) { moveScript(d, id, delta) })
	w.reveal = id
}

// cell builds one cell of a row.
func (w *Workspace) cell(c *ui.Context, r scriptRow, filtered bool, col string) {
	k := kit.T(c)
	switch col {
	case "index":
		kit.NumberCell(c, strconv.Itoa(r.pos+1)).TextColor(kit.Muted(c))
	case "enabled":
		on := !r.Disabled
		sw := kit.Switch(c, &on, "启用 "+r.title())
		if sw.Changed() {
			id := r.ID
			w.update(func(d *model.Doc) { disableScript(d, id, !on) })
		}
	case "name":
		if w.narrow && r.issue != "" {
			ui.Row(c).Gap(6).MinWidth(0).Children(func() {
				kit.Icon(c, kit.SymTriangleAlert, 13).TextColor(k.Danger)
				kit.Clipped(c, r.title(), kit.FontTable, 400)
			})
			break
		}
		kit.Clipped(c, r.title(), kit.FontTable, 400)
	case "type":
		ui.Text(c, TypeLabel(r.Type)).SingleLine()
	case "pattern":
		switch {
		case r.IsHTTP():
			kit.Clipped(c, r.Pattern, 11, 400).Font(kit.MonoFont)
		case r.Type == model.ScriptCron:
			kit.Clipped(c, r.Cron, 11, 400).Font(kit.MonoFont)
		default:
			ui.Text(c, "–").SingleLine().TextColor(kit.Muted(c))
		}
	case "runs":
		w.runsCell(c, r)
	case "status":
		switch {
		case r.issue != "":
			ui.Row(c).Gap(6).MinWidth(0).Children(func() {
				kit.Icon(c, kit.SymTriangleAlert, 13).TextColor(k.Danger)
				kit.Clipped(c, r.issue, kit.FontTable, 400).TextColor(k.Danger)
			})
		case r.Disabled:
			ui.Text(c, "已停用").SingleLine().TextColor(kit.Muted(c))
		default:
			w.nextRun(c, r)
		}
	case "ops":
		w.rowActions(c, r, filtered)
	}
}

// runsCell is the run counter of a script: its runs and, when some failed,
// the failures in red.
func (w *Workspace) runsCell(c *ui.Context, r scriptRow) {
	k := kit.T(c)
	st, ok := w.stats[r.EffectiveTag()]
	switch {
	case !w.statsOK || !ok:
		ui.Text(c, "–").SingleLine().TextColor(kit.Muted(c))
	case st.Paused:
		ui.Text(c, "已暂停").SingleLine().TextColor(k.Warning)
	default:
		ui.Row(c).Gap(8).Children(func() {
			kit.NumberCell(c, strconv.FormatUint(uint64(st.Runs), 10))
			if st.Errors > 0 {
				ui.Row(c).Gap(3).Children(func() {
					kit.Icon(c, kit.SymTriangleAlert, 12).TextColor(k.Danger)
					kit.NumberCell(c, strconv.FormatUint(uint64(st.Errors), 10)).TextColor(k.Danger)
				})
			}
		})
	}
}

// rowActions are the buttons at the end of a row.
func (w *Workspace) rowActions(c *ui.Context, r scriptRow, filtered bool) {
	title := r.title()
	small := kit.IconButtonOptions{Size: 24, IconSize: 14}
	ui.Row(c).Gap(2).Shrink(0).Children(func() {
		if kit.IconButton(c, kit.SymPencil, "编辑 "+title, small).Clicked() {
			w.editors.Edit(r.ID)
		}
		var run ui.Element
		switch {
		case w.narrow:
		case r.IsRun():
			run = w.runButton(c, r, small)
		default:
			// Keeps the buttons of every row in line.
			ui.Box(c).Size(24, 24).Shrink(0)
		}
		if !w.narrow {
			up := small
			up.Disabled = filtered || r.pos == 0
			if kit.IconButton(c, kit.SymArrowUp, "上移 "+title, up).Clicked() {
				w.move(r.ID, -1)
			}
			down := small
			down.Disabled = filtered || r.pos >= w.total()-1
			if kit.IconButton(c, kit.SymArrowDown, "下移 "+title, down).Clicked() {
				w.move(r.ID, 1)
			}
		}
		more := kit.IconButton(c, kit.SymEllipsis, "更多 "+title, small)
		more.PopoverMenuButton(func(menu *ui.Menu) { w.menuItems(menu, r, filtered, w.narrow) })
		if r.IsRun() {
			if w.narrow || w.runOf(r.ID).more {
				w.runPopover(c, more, r)
			} else {
				w.runPopover(c, run, r)
			}
		}
		if w.confirm.Open && w.target == r.ID {
			kit.ConfirmPopover(c, more, &w.confirm, kit.ConfirmOptions{
				Title: "删除「" + title + "」？", Confirm: "删除", Danger: true,
				Run: func(done func(error)) {
					id := r.ID
					done(w.doc.Update(func(d *model.Doc) { removeScript(d, id) }))
				},
			})
		}
	})
}

// nextRun shows when a scheduled script runs next, from the counters.
func (w *Workspace) nextRun(c *ui.Context, r scriptRow) {
	if r.Type != model.ScriptCron || !w.statsOK {
		return
	}
	st, ok := w.stats[r.EffectiveTag()]
	if !ok || st.Paused || st.NextRunMS <= 0 {
		return
	}
	text := NextRunText(st.NextRunMS, c.Now())
	ui.Row(c).Gap(5).Children(func() {
		kit.Icon(c, kit.SymClock, 12).TextColor(kit.Muted(c))
		ui.Text(c, text).SingleLine().FontFeatures("tnum").TextColor(kit.Muted(c))
	}).Label("下次运行 " + text)
}

// CloseConfirm closes the delete confirmation, as the host leaves the list.
func (w *Workspace) CloseConfirm() { w.confirm.Open = false }
