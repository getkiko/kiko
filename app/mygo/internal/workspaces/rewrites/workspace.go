// Package rewrites is the console's Rewrites workspace: the URL rewrite, header
// rewrite and local mapping rules of HTTPS decryption, one table per kind
// with its toolbar, row actions and empty states. The rule editors
// (Editors) are shared with the HTTP inspector, which opens them from a
// record.
package rewrites

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/store"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/modules"
	"github.com/getkiko/kiko/app/mygo/internal/workspaces/scripts"
)

// columns are the table's columns.
var columns = []kit.Column{
	{ID: "index", Title: "序号", Width: 52, Align: ui.End},
	{ID: "enabled", Title: "启用", Width: 60},
	{ID: "name", Title: "名称", Width: 150},
	{ID: "pattern", Title: "匹配模式", MinWidth: 200, Grow: 3},
	{ID: "action", Title: "动作", MinWidth: 180, Grow: 2},
	{ID: "status", Title: "状态", Width: 150},
	{ID: "ops", Title: "操作", Width: 124},
}

// bodyColumns are the columns of the Body table.
var bodyColumns = []kit.Column{
	{ID: "index", Title: "序号", Width: 52, Align: ui.End},
	{ID: "enabled", Title: "启用", Width: 60},
	{ID: "name", Title: "规则", Width: 170},
	{ID: "scope", Title: "范围", Width: 64},
	{ID: "pattern", Title: "匹配模式", MinWidth: 200, Grow: 3},
	{ID: "edits", Title: "编辑", Width: 64, Align: ui.End},
	{ID: "status", Title: "状态", Width: 150},
	{ID: "ops", Title: "操作", Width: 124},
}

// columnsOf are the columns of a kind's table.
func columnsOf(kind Kind) []kit.Column {
	if kind == KindBody {
		return bodyColumns
	}
	return columns
}

// bodySizes are the buffer limits the toolbar offers, in KiB.
var bodySizes = []int{256, 512, 1024, 2048, 4096, 8192, 16384, 32768}

// shownRow is a row of the table with its place in the whole list.
type shownRow struct {
	ruleRow
	pos int
}

// Workspace is the Rewrites page.
type Workspace struct {
	env     *env.Env
	store   *store.Store
	editors *Editors
	// scripts is the Scripts tab, the page after the rule kinds.
	scripts *scripts.Workspace
	// modules is the Modules tab, the page after the scripts.
	modules *modules.Workspace

	tab    int
	search string
	tables [4]kit.TableState

	// confirm asks before a rule is deleted, anchored to the row's More
	// button; target is the rule it asks about.
	confirm kit.ConfirmState
	target  struct {
		kind Kind
		id   string
	}
	// reveal is a rule to select and scroll to once it is listed.
	reveal string
	// refocus returns the keyboard focus to the table after the editor
	// closed.
	refocus bool
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	w := &Workspace{env: e, store: s}
	var d Doc
	if s != nil {
		d = s
	}
	w.editors = NewEditors(e, d)
	w.scripts = scripts.New(e, d)
	w.modules = modules.New(e, d)
	w.editors.OnSaved = func(_ Kind, id string) { w.reveal = id }
	for i := range w.tables {
		w.tables[i].Clear()
	}
	return w
}

// Editors returns the rule editors the workspace builds.
func (w *Workspace) Editors() *Editors { return w.editors }

// Scripts returns the Scripts tab's workspace; its Editors() open scripts.
func (w *Workspace) Scripts() *scripts.Workspace { return w.scripts }

// scriptTab reports whether the Scripts tab shows.
func (w *Workspace) scriptTab() bool { return w.tab == len(Kinds) }

// moduleTab reports whether the Modules tab shows.
func (w *Workspace) moduleTab() bool { return w.tab == len(Kinds)+1 }

// Modules returns the Modules tab's workspace.
func (w *Workspace) Modules() *modules.Workspace { return w.modules }

// StartUpdates starts the schedule that updates the modules from their
// addresses; the console calls it as it starts.
func (w *Workspace) StartUpdates() { w.modules.StartUpdates() }

// StopUpdates stops the schedule.
func (w *Workspace) StopUpdates() { w.modules.StopUpdates() }

// SetActive closes what is open as the page stops showing.
func (w *Workspace) SetActive(active bool) {
	if !active {
		w.editors.Close()
		w.confirm.Open = false
	}
	w.scripts.SetActive(active)
	w.modules.SetActive(active)
}

// Open shows the tab of a kind ("url", "header", "map_local", "body"), the
// Scripts tab ("script") or a Kind.
func (w *Workspace) Open(arg any) {
	switch v := arg.(type) {
	case Kind:
		w.tab = int(v)
	case string:
		switch v {
		case "header":
			w.tab = int(KindHeader)
		case "map_local":
			w.tab = int(KindMapLocal)
		case "body":
			w.tab = int(KindBody)
		case "script":
			w.tab = len(Kinds)
			w.scripts.Back()
		case "module":
			w.tab = len(Kinds) + 1
		default:
			if id, ok := strings.CutPrefix(v, "module:"); ok {
				w.tab = len(Kinds) + 1
				w.modules.Reveal(id)
				break
			}
			w.tab = int(KindURL)
		}
	}
}

func (w *Workspace) kind() Kind { return Kinds[min(max(w.tab, 0), len(Kinds)-1)] }

// update changes the document, showing a refusal as a toast.
func (w *Workspace) update(fn func(d *model.Doc)) {
	w.env.ToastError(w.store.Update(fn))
}

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	defer w.editors.View(c)
	defer w.scripts.Editors().View(c)
	defer w.modules.Overlays(c)
	w.scripts.SetHidden(!w.scriptTab())
	w.modules.SetHidden(!w.moduleTab())
	if w.store == nil || !w.store.Loaded {
		kit.Workspace(c, func() {
			text := "正在载入配置…"
			if w.store != nil && w.store.LoadError != "" {
				text = w.store.LoadError
			}
			kit.EmptyState(c, "", text, "", nil)
		})
		return
	}
	doc := w.store.Doc()
	kind := w.kind()
	if w.editors.TakeClosed() {
		w.refocus = true
	}
	kit.Workspace(c, func() {
		if w.scriptTab() && w.scripts.InSub() {
			w.scripts.SubView(c)
			return
		}
		w.header(c, doc, kind)
		if w.scriptTab() {
			w.scripts.Body(c, w.search, w.openSettings)
			return
		}
		if w.moduleTab() {
			w.modules.Body(c, w.search, w.openSettings)
			return
		}
		if kind == KindBody {
			ui.Row(c).PaddingX(16).PaddingY(4).Shrink(0).Children(func() { w.bufferSelect(c, doc) })
		}
		if !MITMEnabled(doc) && w.total(doc) > 0 {
			ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
				kit.Notice(c, kit.WarningTone, "未开启 HTTPS 解密", "", func() { w.openSettings(c) }).Grow(1)
			})
		}
		w.content(c, doc, kind)
	})
}

func (w *Workspace) total(doc *model.Doc) int {
	n := 0
	for _, k := range Kinds {
		n += count(doc, k)
	}
	return n
}

func (w *Workspace) openSettings(c *ui.Context) {
	kit.Button(c, "前往 HTTPS 解密设置", kit.ButtonOptions{Small: true}).OnClick(func() { w.env.Navigate(env.PageSettings, "https") })
}

// header is the toolbar row: the kind tabs, search and the add button. On
// the Scripts tab of a narrow window the search and the buttons take a second
// row.
func (w *Workspace) header(c *ui.Context, doc *model.Doc, kind Kind) {
	width, _ := c.Size()
	stacked := w.scriptTab() && width <= scripts.NarrowWidth
	tabs := func() {
		segments := make([]kit.Segment, len(Kinds)+2)
		for i, k := range Kinds {
			segments[i] = kit.Segment{Label: k.Label(), Count: kit.Count(count(doc, k))}
		}
		segments[len(Kinds)] = kit.Segment{Label: "脚本", Count: kit.Count(scripts.Count(doc))}
		segments[len(Kinds)+1] = kit.Segment{Label: "模块", Count: kit.Count(modules.Count(doc))}
		tabs := kit.Segmented(c, "rewrites-tab", &w.tab, segments...)
		tabs.Label("改写类型")
		if tabs.Changed() {
			w.confirm.Open = false
			w.scripts.CloseConfirm()
			w.modules.CloseConfirm()
		}
	}
	tools := func() {
		placeholder := "搜索名称、模式、动作"
		if w.scriptTab() {
			placeholder = "搜索名称、标识、模式"
		}
		if w.moduleTab() {
			placeholder = "搜索名称、来源"
		}
		searchWidth := float32(260)
		if stacked {
			searchWidth = max(width-32-10-120, 120)
		}
		kit.Search(c, "rewrites-search", &w.search, kit.SearchOptions{Placeholder: placeholder, Toolbar: true, Width: searchWidth})
		if w.scriptTab() {
			w.scripts.Toolbar(c)
			return
		}
		if w.moduleTab() {
			w.modules.Toolbar(c)
			return
		}
		kit.ToolbarGroup(c, func() {
			add := kit.IconButton(c, kit.SymPlus, "添加规则", kit.IconButtonOptions{})
			if add.Clicked() {
				w.editors.New(kind, Seed{})
			}
		})
	}
	if stacked {
		ui.Column(c).PaddingX(16).PaddingY(8).Gap(8).Shrink(0).DragWindow().Children(func() {
			ui.Row(c).Children(tabs)
			ui.Row(c).Gap(10).Children(tools)
		})
		return
	}
	ui.Row(c).PaddingX(16).PaddingY(8).Gap(10).Shrink(0).DragWindow().Children(func() {
		tabs()
		ui.Spacer(c)
		tools()
	})
}

// bufferSelect is the body buffer limit: the largest message the proxy
// holds in memory to edit it.
func (w *Workspace) bufferSelect(c *ui.Context, doc *model.Doc) {
	size := model.DefaultBodySize
	if r := rulesOf(doc); r != nil {
		size = r.MaxBodySizeOrDefault()
	}
	options := make([]kit.Option, 0, len(bodySizes)+1)
	known := false
	for _, kib := range bodySizes {
		known = known || kib*1024 == size
		options = append(options, kit.Option{Value: strconv.Itoa(kib * 1024), Label: "缓冲 " + kit.Bytes(float64(kib*1024))})
	}
	if !known {
		options = append(options, kit.Option{Value: strconv.Itoa(size), Label: "缓冲 " + kit.Bytes(float64(size))})
	}
	value := strconv.Itoa(size)
	if kit.Select(c, "rewrites-buffer", &value, options, kit.SelectOptions{Width: 128, Label: "最大缓冲"}).Changed() {
		n, _ := strconv.Atoi(value)
		if n == model.DefaultBodySize {
			n = 0
		}
		w.update(func(d *model.Doc) { ensure(d).MaxBodySize = n })
	}
}

// shown lists the rows of kind that the search keeps.
func (w *Workspace) shown(doc *model.Doc, kind Kind) (all []ruleRow, rows []shownRow) {
	all = rowsOf(doc, kind)
	query := strings.ToLower(strings.TrimSpace(w.search))
	for i, r := range all {
		if r.matches(query) {
			rows = append(rows, shownRow{r, i})
		}
	}
	return all, rows
}

// content is the table, or the empty state that takes its place.
func (w *Workspace) content(c *ui.Context, doc *model.Doc, kind Kind) {
	all, rows := w.shown(doc, kind)
	switch {
	case len(all) == 0:
		w.empty(c, doc, kind)
	case len(rows) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			kit.EmptyState(c, kit.SymFileSearch, "没有符合条件的规则", "", func() {
				kit.Button(c, "清除搜索", kit.ButtonOptions{Small: true}).OnClick(func() { w.search = "" })
			})
		})
		w.statusBar(c, len(rows), len(all))
	default:
		w.table(c, doc, kind, rows)
		w.statusBar(c, len(rows), len(all))
	}
}

func (w *Workspace) statusBar(c *ui.Context, shown, all int) {
	text := fmt.Sprintf("%s 条规则", kit.Count(all))
	if shown != all {
		text = fmt.Sprintf("%s / %s 条规则", kit.Count(shown), kit.Count(all))
	}
	kit.StatusBar(c, text, "", nil)
}

// empty fills the area of a kind without rules.
func (w *Workspace) empty(c *ui.Context, doc *model.Doc, kind Kind) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		add := func() {
			kit.Button(c, "添加 "+kind.Label(), kit.ButtonOptions{Small: true, Variant: kit.Primary, Icon: kit.SymPlus}).
				OnClick(func() { w.editors.New(kind, Seed{}) })
		}
		if !MITMEnabled(doc) && w.total(doc) == 0 {
			kit.EmptyState(c, kit.SymLock, "未开启 HTTPS 解密", "", func() {
				ui.Row(c).Gap(8).Children(func() {
					w.openSettings(c)
					add()
				})
			})
			return
		}
		kit.EmptyState(c, kit.SymWandSparkles, "暂无改写", "", add)
	})
}

// table lists the rows of one kind.
func (w *Workspace) table(c *ui.Context, doc *model.Doc, kind Kind, rows []shownRow) {
	state := &w.tables[kind]
	if w.reveal != "" {
		for i, r := range rows {
			if r.ID == w.reveal {
				state.Selected = i
				state.List.ScrollTo(i, ui.Center)
				w.reveal = ""
			}
		}
	}
	filtered := strings.TrimSpace(w.search) != ""
	cols := columnsOf(kind)
	list := kit.Table(c, state, cols, len(rows), kit.TableOptions{
		Label: kind.Label() + "规则", Density: kit.Compact, Striped: true,
		Key:    func(row int) any { return rows[row].ID },
		Dimmed: func(row int) bool { return rows[row].Disabled },
		Menu: func(menu *ui.Menu, row int) {
			w.menuItems(menu, kind, rows[row], len(rows), filtered, true)
		},
	}, func(row, col int) { w.cell(c, kind, rows[row], len(rows), filtered, cols[col].ID) })
	if w.refocus {
		list.Focus()
		w.refocus = false
	}
	if list.Submitted() {
		if i, ok := state.Chosen(len(rows)); ok {
			w.editors.Edit(kind, rows[i].ID)
		}
	}
}

// menuItems are the actions of a rule, for its context menu and its More
// menu.
func (w *Workspace) menuItems(menu *ui.Menu, kind Kind, r shownRow, n int, filtered, context bool) {
	id := r.ID
	if menu.Item("编辑").Chosen() {
		w.editors.Edit(kind, id)
	}
	if menu.Item("复制").Chosen() {
		w.update(func(d *model.Doc) {
			if copied := duplicateRule(d, kind, id); copied != "" {
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
		w.update(func(d *model.Doc) { disableRule(d, kind, id, disabled) })
	}
	if context {
		if menu.Item("上移").Disabled(filtered || r.pos == 0).Chosen() {
			w.move(kind, id, -1)
		}
		if menu.Item("下移").Disabled(filtered || r.pos >= totalOf(w, kind)-1).Chosen() {
			w.move(kind, id, 1)
		}
	}
	menu.Separator()
	if menu.Item("删除").Chosen() {
		w.target.kind, w.target.id, w.confirm.Open = kind, id, true
	}
}

// totalOf is how many rules the document holds of kind.
func totalOf(w *Workspace, kind Kind) int { return count(w.store.Doc(), kind) }

// move shifts a rule one place and keeps it chosen.
func (w *Workspace) move(kind Kind, id string, delta int) {
	w.update(func(d *model.Doc) { moveRule(d, kind, id, delta) })
	w.reveal = id
}

// cell builds one cell of a row.
func (w *Workspace) cell(c *ui.Context, kind Kind, r shownRow, n int, filtered bool, col string) {
	k := kit.T(c)
	switch col {
	case "index":
		kit.NumberCell(c, fmt.Sprint(r.pos+1)).TextColor(kit.Muted(c))
	case "enabled":
		on := !r.Disabled
		sw := kit.Switch(c, &on, "启用 "+r.title())
		if sw.Changed() {
			id := r.ID
			w.update(func(d *model.Doc) { disableRule(d, kind, id, !on) })
		}
	case "name":
		if kind == KindBody && r.Name == "" {
			kit.Clipped(c, r.ID, kit.FontTable, 400).TextColor(kit.Muted(c))
			break
		}
		kit.Cell(c, r.Name, "")
	case "scope":
		ui.Text(c, ScopeLabel(r.Scope)).SingleLine()
	case "edits":
		kit.NumberCell(c, fmt.Sprint(r.Edits))
	case "pattern":
		kit.Clipped(c, r.Pattern, 11, 400).Font(kit.MonoFont)
	case "action":
		kit.Cell(c, r.Summary, "")
	case "status":
		switch {
		case r.Issue != "":
			ui.Row(c).Gap(6).MinWidth(0).Children(func() {
				kit.Icon(c, kit.SymTriangleAlert, 13).TextColor(k.Danger)
				kit.Clipped(c, r.Issue, kit.FontTable, 400).TextColor(k.Danger)
			})
		case r.Disabled:
			ui.Text(c, "已停用").SingleLine().TextColor(kit.Muted(c))
		}
	case "ops":
		w.rowActions(c, kind, r, n, filtered)
	}
}

// rowActions are the buttons at the end of a row.
func (w *Workspace) rowActions(c *ui.Context, kind Kind, r shownRow, n int, filtered bool) {
	title := r.title()
	small := kit.IconButtonOptions{Size: 24, IconSize: 14}
	ui.Row(c).Gap(2).Shrink(0).Children(func() {
		if kit.IconButton(c, kit.SymPencil, "编辑 "+title, small).Clicked() {
			w.editors.Edit(kind, r.ID)
		}
		up := small
		up.Disabled = filtered || r.pos == 0
		if kit.IconButton(c, kit.SymArrowUp, "上移 "+title, up).Clicked() {
			w.move(kind, r.ID, -1)
		}
		down := small
		down.Disabled = filtered || r.pos >= totalOf(w, kind)-1
		if kit.IconButton(c, kit.SymArrowDown, "下移 "+title, down).Clicked() {
			w.move(kind, r.ID, 1)
		}
		more := kit.IconButton(c, kit.SymEllipsis, "更多 "+title, small)
		more.PopoverMenuButton(func(menu *ui.Menu) { w.menuItems(menu, kind, r, n, filtered, false) })
		if w.confirm.Open && w.target.kind == kind && w.target.id == r.ID {
			kit.ConfirmPopover(c, more, &w.confirm, kit.ConfirmOptions{
				Title: "删除「" + title + "」？", Confirm: "删除", Danger: true,
				Run: func(done func(error)) {
					id := r.ID
					done(w.store.Update(func(d *model.Doc) { removeRule(d, kind, id) }))
				},
			})
		}
	})
}
