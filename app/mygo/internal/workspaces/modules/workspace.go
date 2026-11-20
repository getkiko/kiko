package modules

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// The update schedule: the first check comes shortly after the console
// starts, the next ones every hour.
const (
	UpdateFirst = 3 * time.Second
	UpdateEvery = time.Hour
)

// Workspace is the Modules tab. The Rewrites workspace draws Toolbar in its own
// toolbar, Body under it and Overlays above everything.
type Workspace struct {
	env *env.Env
	doc Doc
	// fetch downloads what a module declares (public addresses only);
	// fetchMain its own address.
	fetch     Fetcher
	fetchMain Fetcher
	now       func() time.Time

	active, hidden bool

	install installer
	params  paramsEditor
	view    contentView
	// remove asks before a module is deleted, anchored to its card's More
	// button.
	remove cardConfirm

	// focus returns the keyboard focus to a card (its id) or the add card
	// after a dialog closed.
	focusID  string
	focusAdd bool

	updates updater

	timerMu sync.Mutex
	timer   *time.Timer
}

// New returns the workspace over the document d.
func New(e *env.Env, d Doc) *Workspace {
	w := &Workspace{env: e, doc: d, now: time.Now}
	if e != nil && e.API != nil {
		w.fetch, w.fetchMain = APIPublicFetcher(e.API), APIFetcher(e.API)
	}
	w.updates.init()
	return w
}

// SetFetcher replaces how downloads run (tests): f serves both the main
// address and the declared downloads.
func (w *Workspace) SetFetcher(f Fetcher) { w.fetch, w.fetchMain = f, f }

// SetFetchers replaces the two fetchers separately (tests).
func (w *Workspace) SetFetchers(main, declared Fetcher) { w.fetchMain, w.fetch = main, declared }

// SetClock replaces the clock (tests).
func (w *Workspace) SetClock(now func() time.Time) { w.now = now }

// SetActive follows whether the page shows: what is open closes as it
// stops.
func (w *Workspace) SetActive(active bool) {
	w.active = active
	if !active {
		w.closeAll()
	}
}

// SetHidden tells that the host shows another tab than the modules.
func (w *Workspace) SetHidden(hidden bool) {
	if hidden == w.hidden {
		return
	}
	w.hidden = hidden
	if hidden {
		w.closeAll()
	}
}

func (w *Workspace) closeAll() {
	w.closeInstall()
	w.params.close()
	w.view.open = false
	w.remove.state.Open = false
}

// Dialogs reports whether a dialog of the tab is open.
func (w *Workspace) Dialogs() bool { return w.install.open || w.params.open || w.view.open }

// Count is how many modules the document holds.
func Count(doc *model.Doc) int {
	if doc == nil || doc.HTTPTools == nil {
		return 0
	}
	return len(doc.HTTPTools.Modules)
}

func modulesOf(doc *model.Doc) []*model.HTTPModule {
	if doc == nil || doc.HTTPTools == nil {
		return nil
	}
	return doc.HTTPTools.Modules
}

// MITMEnabled reports whether HTTPS decryption is on.
func MITMEnabled(doc *model.Doc) bool {
	return doc != nil && doc.HTTPTools != nil && doc.HTTPTools.MITM != nil && doc.HTTPTools.MITM.Enabled
}

func ensure(d *model.Doc) *model.HTTPTools {
	if d.HTTPTools == nil {
		d.HTTPTools = &model.HTTPTools{}
	}
	return d.HTTPTools
}

func find(d *model.Doc, id string) *model.HTTPModule {
	if d == nil {
		return nil
	}
	return d.HTTPTools.FindModule(id)
}

// update changes the document, showing a refusal as a toast.
func (w *Workspace) update(fn func(d *model.Doc)) {
	if w.doc == nil {
		return
	}
	w.env.ToastError(w.doc.Update(fn))
}

// matches reports whether the search keeps the module.
func matches(m *model.HTTPModule, query string) bool {
	if query == "" {
		return true
	}
	for _, s := range []string{m.Name, m.Description, m.Source.URL, SourceLabel(m)} {
		if strings.Contains(strings.ToLower(s), query) {
			return true
		}
	}
	return false
}

// Toolbar is the tab's action group: add and update all.
func (w *Workspace) Toolbar(c *ui.Context) {
	kit.ToolbarGroup(c, func() {
		if kit.IconButton(c, kit.SymPlus, "添加模块", kit.IconButtonOptions{}).Clicked() {
			w.openAdd()
		}
		all := kit.IconButton(c, kit.SymRefreshCw, "更新全部", kit.IconButtonOptions{Disabled: !w.canUpdateAny()})
		if all.Clicked() {
			w.UpdateAll()
		}
	})
}

func (w *Workspace) canUpdateAny() bool {
	if w.doc == nil {
		return false
	}
	for _, m := range modulesOf(w.doc.Doc()) {
		if m.Source.Kind == model.ModuleSourceURL {
			return true
		}
	}
	return false
}

// Body builds the cards, or the empty state that takes their place.
func (w *Workspace) Body(c *ui.Context, search string, openSettings func(c *ui.Context)) {
	doc := w.doc.Doc()
	all := modulesOf(doc)
	query := strings.ToLower(strings.TrimSpace(search))
	var shown []*model.HTTPModule
	for _, m := range all {
		if matches(m, query) {
			shown = append(shown, m)
		}
	}
	if !MITMEnabled(doc) && len(all) > 0 {
		ui.Row(c).PaddingX(16).PaddingY(6).Shrink(0).Children(func() {
			kit.Notice(c, kit.WarningTone, "未开启 HTTPS 解密", "", func() { openSettings(c) }).Grow(1)
		})
	}
	switch {
	case len(all) == 0:
		w.empty(c, doc, openSettings)
		return
	case len(shown) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			kit.EmptyState(c, kit.SymFileSearch, "没有符合条件的模块", "", nil)
		})
	default:
		w.grid(c, shown, query == "")
	}
	text := fmt.Sprintf("%s 个模块", kit.Count(len(all)))
	if len(shown) != len(all) {
		text = fmt.Sprintf("%s / %s 个模块", kit.Count(len(shown)), kit.Count(len(all)))
	}
	kit.StatusBar(c, text, "", nil)
}

// empty fills the area without modules.
func (w *Workspace) empty(c *ui.Context, doc *model.Doc, openSettings func(c *ui.Context)) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		add := func() {
			b := kit.Button(c, "添加模块", kit.ButtonOptions{Small: true, Variant: kit.Primary, Icon: kit.SymPlus})
			if w.focusAdd {
				w.focusAdd = false
				b.Focus()
			}
			b.OnClick(w.openAdd)
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
		kit.EmptyState(c, kit.SymLayers, "暂无模块", "", add)
	})
}

// grid is the cards in a grid that gains columns with the window's
// width, then the ＋ card when the list is not filtered.
func (w *Workspace) grid(c *ui.Context, mods []*model.HTTPModule, withAdd bool) {
	ui.Scroll(c.Key("modules-scroll")).Grow(1).MinHeight(0).Padding(4, 16, 16).Children(func() {
		ui.Grid(c.Key("modules")).Columns(kit.HeroColumns(c)).Gap(12).FillWidth().Shrink(0).Children(func() {
			seen := map[string]bool{}
			for _, m := range mods {
				// A damaged document can repeat an id: only the first card
				// shows, so an action lands on the module the card shows.
				if seen[m.ID] {
					continue
				}
				seen[m.ID] = true
				// A one-column grid lets the card fill the cell.
				ui.Grid(c.Key(m.ID)).Columns(1).MinWidth(0).Radius(kit.HeroRadius + 2).Children(func() { w.card(c, m) })
			}
			if withAdd {
				add := kit.AddCard(c.Key("add-module"), "添加模块")
				if w.focusAdd {
					w.focusAdd = false
					add.Focus()
				}
				if add.Clicked() {
					w.openAdd()
				}
			}
		})
	})
}

// cardConfirm is a confirmation started from a card's more menu, shown at
// that card's more button as last built.
type cardConfirm struct {
	state kit.ConfirmState
	id    string
	at    ui.Element
	title string
}

func (p *cardConfirm) start(id string) { p.id, p.state = id, kit.ConfirmState{Open: true} }

func (p *cardConfirm) showing(id string) bool { return p.id == id && p.state.Open }

func (p *cardConfirm) track(id, title string, more ui.Element) {
	if p.id == id {
		p.at, p.title = more, title
	}
}

// statusBadge is the card's state tag: "Updating", "Unconfirmed", "Update
// failed" or "Updated".
func (w *Workspace) statusBadge(c *ui.Context, m *model.HTTPModule) {
	switch {
	case w.updates.busy(m.ID):
		kit.Badge(c, "更新中", kit.Neutral)
	case m.Pending != nil:
		kit.Badge(c, "待确认", kit.WarningTone)
	case m.LastError != "":
		kit.Hint(c, kit.Badge(c, "更新失败", kit.DangerTone), m.LastError)
	case m.Updated:
		kit.Badge(c, "已更新", kit.SuccessTone)
	}
}

// card is a module's hero card: its name and state, the counts of what it
// holds, its source with version and age, and its switch. Clicking it
// shows its content, or the update waiting for a decision.
func (w *Workspace) card(c *ui.Context, m *model.HTTPModule) {
	k := kit.T(c)
	id, title := m.ID, m.Name
	counts := m.Contents.Counts()
	now := w.now()
	_, glow := kit.TypeLook(c, "module", "")
	var menu []kit.EntityMenuItem
	if m.Source.Kind == model.ModuleSourceURL {
		menu = append(menu, kit.EntityMenuItem{Label: "更新", Icon: kit.SymRefreshCw, Run: func(ui.Element) { w.Update(id) }})
	}
	if len(m.Arguments) > 0 || m.Source.Kind == model.ModuleSourceURL {
		menu = append(menu, kit.EntityMenuItem{Label: "参数", Icon: kit.SymSlidersHorizontal, Run: func(ui.Element) { w.openParams(id) }})
	}
	menu = append(menu,
		kit.EntityMenuItem{Label: "查看内容", Icon: kit.SymEye, Run: func(ui.Element) { w.openContent(id) }},
		kit.EntityMenuItem{Label: "导出", Icon: kit.SymDownload, Run: func(ui.Element) { w.export(id) }},
		kit.EntityMenuItem{Label: "删除", Icon: kit.SymTrash2, Danger: true, Run: func(ui.Element) { w.remove.start(id) }})
	focus := w.focusID == id
	if focus {
		w.focusID = ""
	}
	stat := func(n int, label string) {
		if n == 0 {
			return
		}
		ui.Row(c).Gap(3).AlignItems(ui.Baseline).Shrink(0).Children(func() {
			ui.Text(c, kit.Count(n)).FontSize(20).FontWeight(600).FontFeatures("tnum").SingleLine()
			ui.Text(c, label).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine()
		})
	}
	more := kit.HeroCard(c, id, kit.HeroCardOptions{
		Label: "查看 " + title,
		Glow:  glow,
		Muted: !m.Enabled,
		Header: func() {
			kit.HeroDot(c, m.Enabled)
			kit.HeroName(c, title)
		},
		HeaderEnd: func() { w.statusBadge(c, m) },
		Main: func() {
			ui.Row(c).Gap(12).MinWidth(0).AlignItems(ui.Baseline).Children(func() {
				stat(counts.Hostnames, "主机名")
				stat(counts.Rewrites, "改写")
				stat(counts.Scripts, "脚本")
				stat(counts.Rules, "规则")
				if counts == (model.ModuleCounts{}) {
					ui.Text(c, "—").FontSize(20).FontWeight(600)
				}
			})
		},
		Footer: func() {
			parts := []string{SourceLabel(m)}
			if m.Version != "" {
				parts = append(parts, m.Version)
			}
			text := ui.Text(c, strings.Join(parts, " · ")).FontFeatures("tnum").SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			if m.UpdatedAt > 0 {
				kit.Hint(c, text, "更新于 "+kit.Ago(time.UnixMilli(m.UpdatedAt), now))
			}
			ui.Spacer(c)
			on := m.Enabled
			sw := kit.Switch(c, &on, "启用 "+title)
			if sw.Changed() {
				w.setEnabled(id, on)
			}
		},
		Enabled:         m.Enabled,
		OnEnabledChange: func(enabled bool) { w.setEnabled(id, enabled) },
		Menu:            menu,
		KeepMore:        w.remove.showing(id),
		Focus:           focus,
		OnOpen: func() {
			if m.Pending != nil {
				w.openPending(id)
				return
			}
			w.openContent(id)
		},
	})
	w.remove.track(id, title, more)
}

// setEnabled flips a module's switch in the document as it is now.
func (w *Workspace) setEnabled(id string, on bool) {
	w.update(func(d *model.Doc) {
		if m := find(d, id); m != nil {
			m.Enabled = on
		}
	})
}

// Overlays builds the tab's dialogs and the delete confirmation; the host
// calls it every frame.
func (w *Workspace) Overlays(c *ui.Context) {
	w.installDialog(c)
	w.paramsDialog(c)
	w.contentDialog(c)
	if w.remove.id == "" {
		return
	}
	doc := w.doc.Doc()
	if m := find(doc, w.remove.id); m != nil && w.remove.at.Valid() {
		id := m.ID
		kit.ConfirmPopover(c, w.remove.at, &w.remove.state, kit.ConfirmOptions{
			Title: "删除「" + m.Name + "」？", Confirm: "删除", Danger: true,
			Run: func(done func(error)) {
				done(w.doc.Update(func(d *model.Doc) {
					t := ensure(d)
					kept := t.Modules[:0:0]
					for _, x := range t.Modules {
						if x != nil && x.ID != id {
							kept = append(kept, x)
						}
					}
					t.Modules = kept
				}))
			},
		})
	}
	if !w.remove.state.Open {
		// The confirmation closed: its card (when it is still there)
		// takes the focus back, else the add card.
		if find(w.doc.Doc(), w.remove.id) != nil {
			w.focusID = w.remove.id
		} else {
			w.focusAdd = true
		}
		w.remove.id = ""
	}
}

// ---- updates

// StartUpdates starts the schedule: a check shortly after the console
// starts, then one every hour. Safe to call again.
func (w *Workspace) StartUpdates() {
	w.timerMu.Lock()
	defer w.timerMu.Unlock()
	if w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(UpdateFirst, w.tick)
}

// StopUpdates stops the schedule.
func (w *Workspace) StopUpdates() {
	w.timerMu.Lock()
	defer w.timerMu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *Workspace) tick() {
	w.env.Post(func() { w.CheckDue() })
	w.timerMu.Lock()
	defer w.timerMu.Unlock()
	if w.timer != nil {
		w.timer.Reset(UpdateEvery)
	}
}

// CloseConfirm closes the delete confirmation, as the host leaves the
// tab.
func (w *Workspace) CloseConfirm() { w.remove.state.Open = false }

// Reveal moves the keyboard focus to the card of module id once the tab
// shows it.
func (w *Workspace) Reveal(id string) { w.focusID = id }
