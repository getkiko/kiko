package scripts

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// storeDetail is the whole value of the chosen entry, read when the list
// holds only a preview of it.
type storeDetail struct {
	id    string
	value string
	ok    bool
	err   string
}

// storeEdit is the edit dialog's draft.
type storeEdit struct {
	open, wasOpen bool
	isNew         bool
	ns, key, pick string
	code          kit.CodeState
	lang          string
	// orig is the value as loaded: the editor folds line endings, so a
	// CRLF value is written back with its endings.
	orig string
	// loading is while the whole value of a truncated entry is read;
	// unreadable when that failed. Neither lets the draft be saved: a
	// preview must never be written back.
	loading, unreadable bool
	saving              bool
	err                 string
}

func (e *storeEdit) load(text string) {
	e.lang = kit.LangText
	if _, ok := storeFormatJSON(text); ok {
		e.lang = kit.LangJSON
	}
	e.orig = text
	e.code.Reset(text)
}

// StorePage is the Storage page of the scripts workspace: the namespaces and
// keys of $persistentStore, with a detail inspector and editing.
type StorePage struct {
	env *env.Env

	active bool
	gen    int
	ctx    context.Context
	cancel context.CancelFunc

	loaded, loading, again bool
	unavailable            bool
	err                    string
	namespaces             []api.ScriptNamespace
	entries                []api.ScriptStoreItem
	entriesNS              string
	info                   *api.ScriptStoreInfo
	nsSel                  string

	search  string
	sidebar *bool
	facets  kit.Facets
	table   kit.TableState
	selKey  string
	refocus bool
	pretty  bool
	copied  string

	detail  storeDetail
	viewer  slViewer
	confirm kit.ConfirmState

	edit      storeEdit
	editToken int
}

// NewStorePage returns the store page of the server e talks to.
func NewStorePage(e *env.Env) *StorePage { return &StorePage{env: e} }

// SetActive starts reading the store while the page is visible, once and
// then every few seconds, and stops, cancelling what is in flight, when it
// is not.
func (p *StorePage) SetActive(active bool) {
	if active == p.active {
		return
	}
	p.active = active
	p.gen++
	if !active {
		if p.cancel != nil {
			p.cancel()
			p.cancel = nil
		}
		p.loading, p.again = false, false
		p.confirm.Open = false
		// A draft and the replies owed to it go with the page.
		p.edit = storeEdit{}
		p.editToken++
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx, p.cancel = ctx, cancel
	gen := p.gen
	p.refresh()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(storeRefresh):
			}
			// A draft in the dialog is not read over.
			p.env.Post(func() {
				if p.gen == gen && !p.edit.open {
					p.refresh()
				}
			})
		}
	}()
}

// refresh reads the namespaces and the entries of the chosen one; a read
// asked for while another runs follows it. UI thread.
func (p *StorePage) refresh() {
	if !p.active {
		return
	}
	if p.loading {
		p.again = true
		return
	}
	p.loading = true
	gen, ctx, ns := p.gen, p.ctx, p.nsSel
	go func() {
		list, err := p.env.API.ScriptStoreList(ctx, ns)
		p.env.Post(func() {
			if p.gen != gen {
				return
			}
			p.loading = false
			p.apply(ns, list, err)
		})
	}()
}

func (p *StorePage) apply(ns string, list api.ScriptStoreList, err error) {
	p.loaded = true
	if err != nil {
		p.unavailable = api.IsScriptUnavailable(err)
		p.err = ""
		if !p.unavailable {
			p.err = err.Error()
		}
	} else {
		p.unavailable, p.err = false, ""
		p.namespaces = list.Namespaces
		if list.Info != nil {
			p.info = list.Info
		}
		have := slices.ContainsFunc(p.namespaces, func(n api.ScriptNamespace) bool { return n.Name == p.nsSel })
		switch {
		case !have:
			p.nsSel, p.entries, p.entriesNS = "", nil, ""
			if len(p.namespaces) > 0 {
				p.nsSel = p.namespaces[0].Name
			}
		case ns == p.nsSel:
			p.entries, p.entriesNS = list.Entries, ns
		}
	}
	if p.again || (err == nil && p.nsSel != "" && p.entriesNS != p.nsSel) {
		p.again = false
		p.refresh()
	}
}

// chooseNamespace shows the entries of name.
func (p *StorePage) chooseNamespace(name string) {
	if name == p.nsSel {
		return
	}
	p.nsSel, p.selKey = name, ""
	p.entries, p.entriesNS = nil, ""
	p.refresh()
}

// idOf names an entry for the detail read.
func (p *StorePage) idOf(item api.ScriptStoreItem) string {
	return fmt.Sprintf("%s\x00%s\x00%d", p.nsSel, item.Key, item.Size)
}

// loadDetail reads the whole value of a truncated entry.
func (p *StorePage) loadDetail(item api.ScriptStoreItem) {
	id, ns, key := p.idOf(item), p.nsSel, item.Key
	p.detail = storeDetail{id: id}
	gen, ctx := p.gen, p.ctx
	go func() {
		v, err := p.env.API.ScriptStoreGet(ctx, ns, key)
		p.env.Post(func() {
			if p.gen != gen || p.detail.id != id {
				return
			}
			switch {
			case err != nil:
				p.detail.err = err.Error()
			case !v.Found:
				p.detail.err = "条目不存在"
			default:
				p.detail.value, p.detail.ok = v.Value, true
			}
		})
	}()
}

// valueOf is the whole value of an entry; loading while a truncated one
// is read, err when that failed.
func (p *StorePage) valueOf(item api.ScriptStoreItem) (value string, loading bool, err string) {
	if !item.Truncated {
		return item.Value, false, ""
	}
	if p.detail.id == p.idOf(item) {
		switch {
		case p.detail.ok:
			return p.detail.value, false, ""
		case p.detail.err != "":
			return "", false, p.detail.err
		}
	}
	return "", true, ""
}

func (p *StorePage) openAdd() {
	p.editToken++
	p.edit = storeEdit{open: true, isNew: true, ns: p.nsSel}
	p.edit.load("")
}

func (p *StorePage) openEdit(item api.ScriptStoreItem) {
	p.editToken++
	token := p.editToken
	p.edit = storeEdit{open: true, ns: p.nsSel, key: item.Key}
	if !item.Truncated {
		p.edit.load(item.Value)
		p.edit.code.Focus()
		return
	}
	p.edit.loading = true
	ns, key := p.nsSel, item.Key
	go func() {
		v, err := p.env.API.ScriptStoreGet(context.Background(), ns, key)
		p.env.Post(func() {
			if token != p.editToken || !p.edit.open {
				return
			}
			p.edit.loading = false
			switch {
			case err != nil:
				p.edit.unreadable, p.edit.err = true, err.Error()
			case !v.Found:
				p.edit.unreadable, p.edit.err = true, "条目不存在"
			default:
				p.edit.load(v.Value)
				p.edit.code.Focus()
			}
		})
	}()
}

// exists reports whether the listed entries of the namespace hold key.
func (p *StorePage) exists(ns, key string) bool {
	ns = strings.TrimSpace(ns)
	if key == "" || ns == "" || ns != p.entriesNS {
		return false
	}
	return slices.ContainsFunc(p.entries, func(it api.ScriptStoreItem) bool { return it.Key == key })
}

// save writes the draft.
func (p *StorePage) save() {
	e := &p.edit
	if !e.open || e.loading || e.unreadable || e.saving {
		return
	}
	ns, key, value := strings.TrimSpace(e.ns), e.key, restoreEOL(e.orig, e.code.Text())
	if msg := storeValidate(ns, key, value); msg != "" {
		e.err = msg
		return
	}
	e.saving, e.err = true, ""
	token := p.editToken
	env.Go(p.env, func(ctx context.Context) (api.ScriptStoreInfo, error) {
		return p.env.API.ScriptStoreSet(ctx, ns, key, value)
	}, func(info api.ScriptStoreInfo, err error) {
		if token != p.editToken {
			// The dialog was closed or replaced while this ran: the write
			// stands, the list shows it, and the new draft is left alone.
			if err == nil {
				p.info = &info
				p.refresh()
			}
			return
		}
		e.saving = false
		if err != nil {
			e.err = err.Error()
			return
		}
		p.editToken++
		e.open = false
		p.refocus = true
		p.info = &info
		p.detail = storeDetail{}
		p.selKey = key
		if ns != p.nsSel {
			p.nsSel = ns
			p.entries, p.entriesNS = nil, ""
		}
		p.refresh()
	})
}

// remove deletes the chosen entry.
func (p *StorePage) remove(key string, done func(error)) {
	ns := p.nsSel
	env.Go(p.env, func(ctx context.Context) (api.ScriptStoreInfo, error) {
		return p.env.API.ScriptStoreDelete(ctx, ns, key)
	}, func(info api.ScriptStoreInfo, err error) {
		if err == nil {
			p.info = &info
			if p.selKey == key && p.nsSel == ns {
				p.selKey = ""
			}
			p.refocus = true
			p.refresh()
		}
		done(err)
	})
}

// View builds the page: the namespaces, the entries and the inspector of
// the chosen one, filling the area it is given.
func (p *StorePage) View(c *ui.Context) {
	width, _ := c.Size()
	narrow := width <= slNarrow
	if p.sidebar == nil {
		open := width >= 1000
		p.sidebar = &open
	}
	// The dialog closes while it builds, by Escape or its buttons, or on a
	// reply: the frame after, the table takes the keyboard back.
	if p.edit.wasOpen && !p.edit.open {
		p.refocus = true
		p.editToken++
	}
	p.edit.wasOpen = p.edit.open
	rows := storeFilter(p.entries, p.search)
	selected := storeSelectedIndex(rows, p.selKey)
	var item *api.ScriptStoreItem
	if selected >= 0 {
		item = &rows[selected]
	}
	if item != nil && item.Truncated && p.active && p.detail.id != p.idOf(*item) {
		p.loadDetail(*item)
	}
	showPane := len(p.namespaces) > 0 && !p.unavailable && *p.sidebar
	if narrow && showPane && item != nil {
		*p.sidebar, showPane = false, false
	}

	kit.Workspace(c, func() {
		p.toolbar(c)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			if showPane && !narrow {
				p.pane(c)
			}
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				if p.err != "" {
					ui.Row(c).Padding(8, 12).Shrink(0).Children(func() { kit.Notice(c, kit.DangerTone, "", p.err, nil).Grow(1) })
				}
				p.body(c, rows, selected)
			})
			if item != nil && !narrow {
				p.inspector(c, *item)
			}
		})
		p.statusBar(c, rows)
	})
	if narrow {
		if showPane {
			slSidePanel(c, false, func() { *p.sidebar = false }, func() { p.pane(c) })
		}
		if item != nil {
			slSidePanel(c, true, func() { p.selKey = "" }, func() { p.inspector(c, *item) })
		}
	}
	p.editDialog(c)
}

func (p *StorePage) toolbar(c *ui.Context) {
	ui.Row(c).PaddingX(16).PaddingY(8).Gap(10).Shrink(0).DragWindow().Children(func() {
		kit.Search(c, "store-search", &p.search, kit.SearchOptions{Placeholder: "搜索键、值", Toolbar: true, Width: 260})
		ui.Spacer(c)
		kit.ToolbarGroup(c, func() {
			hasPane := len(p.namespaces) > 0 && !p.unavailable
			kit.IconButton(c, kit.SymSlidersHorizontal, "切换命名空间筛选", kit.IconButtonOptions{Active: *p.sidebar && hasPane, Disabled: !hasPane}).
				OnClick(func() { *p.sidebar = !*p.sidebar })
			kit.IconButton(c, kit.SymRefreshCw, "刷新存储", kit.IconButtonOptions{}).OnClick(p.refresh)
		})
		kit.Button(c, "添加", kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymPlus}).OnClick(p.openAdd)
	})
}

// pane lists the namespaces.
func (p *StorePage) pane(c *ui.Context) {
	kit.SizedFilterPane(c, kit.FilterConnection, 0, false, nil, func() {
		n := len(p.namespaces)
		kit.FacetGroup(c, "命名空间", p.facets.Of("ns"), n,
			func(i int) string { return p.namespaces[i].Name },
			func(i int) bool { return p.namespaces[i].Name == p.nsSel },
			func(i int) {
				ns := p.namespaces[i]
				o := kit.FilterOptionDetail(c.Key("ns\x00"+ns.Name), kit.FilterConnection, ns.Name == p.nsSel, ns.Name, storeNamespaceDetail(ns))
				if o.Clicked() {
					p.chooseNamespace(ns.Name)
					if w, _ := c.Size(); w <= slNarrow {
						*p.sidebar = false
					}
				}
			})
	})
}

var storeColumns = []kit.Column{
	{ID: "key", Title: "键", MinWidth: 160, Grow: 1},
	{ID: "value", Title: "值", MinWidth: 220, Grow: 2},
	{ID: "size", Title: "大小", Width: 90, Align: ui.End},
}

// body is the table, or the state that takes its place.
func (p *StorePage) body(c *ui.Context, rows []api.ScriptStoreItem, selected int) {
	switch {
	case p.unavailable:
		p.refocus = false
		kit.EmptyState(c, kit.SymPowerOff, "核心未运行", "", func() {
			kit.Button(c, "重试", kit.ButtonOptions{Small: true}).OnClick(p.refresh)
		})
		return
	case !p.loaded:
		return
	case len(p.namespaces) == 0:
		p.refocus = false
		kit.EmptyState(c, kit.SymDatabase, "暂无存储", "", func() {
			kit.Button(c, "添加", kit.ButtonOptions{Small: true, Icon: kit.SymPlus}).OnClick(p.openAdd)
		})
		return
	case p.entriesNS != p.nsSel:
		return
	case len(p.entries) == 0:
		p.refocus = false
		kit.EmptyState(c, kit.SymDatabase, "此命名空间暂无条目", "", nil)
		return
	case len(rows) == 0:
		p.refocus = false
		kit.EmptyState(c, kit.SymFileSearch, "没有匹配的条目", "", func() {
			kit.Button(c, "清除搜索", kit.ButtonOptions{Small: true}).OnClick(func() { p.search = "" })
		})
		return
	}
	p.table.Selected = selected
	list := kit.Table(c, &p.table, storeColumns, len(rows), kit.TableOptions{
		Label: "存储条目", Striped: true, KeepOffset: true,
		Key: func(row int) any { return rows[row].Key },
	}, func(row, col int) {
		item := rows[row]
		switch storeColumns[col].ID {
		case "key":
			kit.Clipped(c, item.Key, kit.FontTable, 500).Font(kit.MonoFont)
		case "value":
			kit.Clipped(c, storePreview(item.Value, item.Truncated), kit.FontTable, 400).Font(kit.MonoFont)
		case "size":
			kit.NumberCell(c, kit.Bytes(float64(item.Size))).TextColor(kit.Muted(c))
		}
	})
	if list.Changed() {
		if i, ok := p.table.Chosen(len(rows)); ok && rows[i].Key != p.selKey {
			p.selKey, p.copied = rows[i].Key, ""
		}
	}
	if i, ok := p.table.Chosen(len(rows)); ok && rows[i].Key == p.selKey {
		switch {
		case list.Submitted():
			p.openEdit(rows[i])
		case list.Shortcut(0, ui.KeyDelete), list.Shortcut(0, ui.KeyBackspace):
			p.confirm.Open = true
		}
	}
	if list.Shortcut(0, ui.KeyEscape) {
		p.selKey = ""
	}
	if p.refocus {
		p.refocus = false
		list.Focus()
	}
}

// inspector shows the chosen entry whole.
func (p *StorePage) inspector(c *ui.Context, item api.ScriptStoreItem) {
	k := kit.T(c)
	value, loading, failure := p.valueOf(item)
	text, language := value, kit.LangText
	formatted, isJSON := storeFormatJSON(value)
	if isJSON {
		language = kit.LangJSON
		if p.pretty {
			text = formatted
		}
	}
	subtitle := p.nsSel + " · " + kit.Bytes(float64(item.Size))
	kit.ScrollingInspector(c.Key(p.nsSel+"\x00"+item.Key), item.Key, subtitle, nil, nil, func() { p.selKey = "" }, func() {
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			kit.Button(c, "编辑", kit.ButtonOptions{Small: true, Icon: kit.SymPencil}).OnClick(func() { p.openEdit(item) })
			del := kit.Button(c, "删除", kit.ButtonOptions{Small: true, Icon: kit.SymTrash2})
			del.OnClick(func() { p.confirm.Open = true })
			kit.ConfirmPopover(c, del, &p.confirm, kit.ConfirmOptions{
				Title: "删除此条目？", Message: item.Key, Confirm: "确认删除", Danger: true,
				Run: func(done func(error)) { p.remove(item.Key, done) },
			})
		})
		trailing := func() {
			ui.Row(c).Gap(2).Children(func() {
				if isJSON {
					kit.IconButton(c, kit.SymWrapText, "格式化显示", kit.IconButtonOptions{Size: 24, IconSize: 14, Active: p.pretty}).
						OnClick(func() { p.pretty = !p.pretty })
				}
				kit.IconButton(c, kit.SymCopy, "复制值", kit.IconButtonOptions{Size: 24, IconSize: 14, Disabled: loading || failure != ""}).
					OnClick(func() { c.WriteClipboard(text) })
			})
		}
		kit.Section(c, "值", trailing, func() {
			switch {
			case loading:
				ui.Text(c, "加载中…").FontSize(kit.FontTable).TextColor(k.Label3)
			case failure != "":
				kit.Notice(c, kit.DangerTone, "", failure, nil)
			default:
				p.viewer.show(c, fmt.Sprint(p.idOf(item), p.pretty), text, language, "值", slViewerHeight(text))
			}
		})
		kit.Section(c, "", nil, func() {
			kit.Property(c, "命名空间", p.nsSel)
			kit.Property(c, "大小", fmt.Sprintf("%s（%d 字节）", kit.Bytes(float64(item.Size)), item.Size))
		})
	})
}

func (p *StorePage) statusBar(c *ui.Context, rows []api.ScriptStoreItem) {
	k := kit.T(c)
	left := ""
	if p.loaded && !p.unavailable {
		left = fmt.Sprintf("%s 条", kit.Count(len(rows)))
		if len(rows) != len(p.entries) {
			left = fmt.Sprintf("%s / %s 条", kit.Count(len(rows)), kit.Count(len(p.entries)))
		}
	}
	center := ""
	if p.info != nil && !p.unavailable {
		center = fmt.Sprintf("已用 %s / %s", kit.Bytes(float64(p.info.Bytes)), kit.Bytes(float64(p.info.Limit)))
	}
	kit.StatusBar(c, left, center, func() {
		if p.info != nil && !p.unavailable && !p.info.Persistent {
			ui.Row(c).Gap(4).Children(func() {
				kit.Icon(c, kit.SymTriangleAlert, 13).TextColor(k.Warning)
				ui.Text(c, "未持久化").SingleLine()
			})
		}
	})
}

// editDialog builds the add and edit dialog.
func (p *StorePage) editDialog(c *ui.Context) {
	e := &p.edit
	k := kit.T(c)
	title := "编辑条目"
	if e.isNew {
		title = "添加条目"
	}
	kit.Dialog(c, &e.open, title, 560, func() {
		kit.FormRow(c, "命名空间", "", func() {
			if !e.isNew {
				ui.Text(c, e.ns).Font(kit.MonoFont).FontSize(kit.FontBody).Selectable().Wrap().Margin(0, 0, 0)
				return
			}
			in := kit.Input(c, "store-ns", &e.ns, "命名空间").Label("命名空间").Font(kit.MonoFont)
			if e.ns == "" {
				in.AutoFocus()
			}
			if len(p.namespaces) > 0 {
				options := make([]kit.Option, len(p.namespaces))
				for i, ns := range p.namespaces {
					options[i] = kit.Option{Value: ns.Name, Label: ns.Name}
				}
				sel := kit.Select(c, "store-ns-pick", &e.pick, options, kit.SelectOptions{Placeholder: "选择已有命名空间", Label: "已有命名空间"})
				if sel.Changed() {
					e.ns, e.pick = e.pick, ""
				}
			}
		})
		kit.FormRow(c, "键", "", func() {
			if !e.isNew {
				ui.Text(c, e.key).Font(kit.MonoFont).FontSize(kit.FontBody).Selectable().Wrap()
				return
			}
			in := kit.Input(c, "store-key", &e.key, "键").Label("键").Font(kit.MonoFont)
			if e.ns != "" {
				in.AutoFocus()
			}
			if p.exists(e.ns, e.key) {
				ui.Text(c, "已存在，保存将覆盖").FontSize(kit.FontCaption).TextColor(k.Warning)
			}
		})
		kit.FormRow(c, "值", "", func() {
			if e.loading {
				ui.Text(c, "加载中…").FontSize(kit.FontBody).TextColor(k.Label3)
				return
			}
			kit.CodeEditor(c, &e.code, kit.CodeOptions{Language: e.lang, Label: "值", Wrap: true, MinHeight: 240, Placeholder: "值"}).Height(240)
		})
		kit.FormError(c, e.err)
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { e.open = false })
		kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary, Disabled: e.loading || e.unreadable || e.saving}).OnClick(p.save)
	})
}
