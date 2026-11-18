// Package routes is the console's Routing workspace: a persistent device
// sidebar (Pre-rules, Local / Gateway, the LAN devices, Other Devices)
// beside the fixed Traffic Rules / DNS Routing tabs, whose tables list each
// device's rule groups, rules and default. Rules open in a floating editor
// sidebar with a condition editor, as do each device's default outbound
// and fallback DNS. The toolbar's "+"
// menu adds rules and groups; a device's context menu copies its rules to
// another device. Devices, groups, route settings, copying and policy tests
// use dialogs; the Rule Sets pages show in the Settings workspace (OpenRuleSets,
// RuleSetsPage).
//
// Document logic is package model's (route, device and DNS policy,
// conditions, typed matches, rule sets, references); this package lays it
// out and edits it through store.Store.Update.
package routes

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/store"
)

// narrowWidth is the window width at and below which the device sidebar
// starts closed and closes as a device is chosen.
const narrowWidth = 800

// Workspace is the routes page.
type Workspace struct {
	env   *env.Env
	store *store.Store
	// deviceWidth is the width the user gave the device pane.
	deviceWidth kit.PaneWidth

	// hits are the traffic rules' hit lights, followed while the page
	// shows (followHits).
	hits            ruleHits
	unsubscribeHits func()
	// conns is the Current Connections list of the topology's rule preview.
	conns ruleConns

	// choices are the running core's group choices by group tag, for
	// the table's outbound chains; nil while the core is stopped.
	choices map[string]string
	// chains caches outboundChain by outbound ID for the frame, byTag
	// its tag index.
	chains, byTag map[string]string
	// exits bring the rules' exits down to servers for their speed tests.
	exits nodeview.ExitsCache

	sec    section
	owner  string
	search string
	// sidebar shows the device sidebar; sidebarInit records its first
	// choice by the window's width.
	sidebar, sidebarInit bool
	// err is the last change's failure (usePolicyCommit's error).
	err    string
	notice string

	list ui.ListState
	// pressed is the rule row pressed last, the one a drag onto a device
	// carries.
	pressed string
	// rowHeld is whether a table row is pressed this frame: a press
	// outside the editor sidebar keeps it open for the row's click to
	// switch it.
	rowHeld bool
	// press is where the row being pressed was pressed, telling a drag
	// from a click; pressSeen, that a row reported its press this frame.
	press     pressPoint
	pressSeen bool
	// fallback is the open editor of the default outbound or fallback DNS.
	fallback *fallbackEditor
	// moving is the row moving with the keyboard.
	moving *moveState
	// orders are the column orders of the two tables (columnOrder), widths
	// their column widths (columnWidths).
	orders [2]*kit.ColumnOrder
	widths [2]*kit.ColumnWidths

	// avatars are the decoded device pictures; picking is that a file dialog
	// for a device picture is open.
	avatars map[string]*ui.Bitmap
	// avatarUsed is the last use of each cached picture (LRU eviction).
	avatarUsed map[string]uint64
	// avatarKeys remembers the cache key of each device's picture, so a
	// frame hashes it only when the picture changed.
	avatarKeys map[string]avatarKey
	avatarTick uint64
	picking    bool
	// lastScan is the id of the scan this window started last.
	lastScan atomic.Pointer[string]
	// narrow is whether the window is narrow, as the sidebar overlay needs.
	narrow bool
	// profile is the open dialog naming a new profile or renaming one;
	// profileDelete is the confirmation of deleting one.
	profile       *profileDialog
	profileDelete profileDelete
	// imageRemove is the confirmation of removing a device picture.
	imageRemove imageRemove
	// profileQ is the confirmation of a profile switch.
	profileQ *profileAsk

	editor   *ruleEditor
	device   *deviceDialog
	scan     *scanDialog
	group    *groupDialog
	settings *settingsDialog
	ruleSets *ruleSetPage
	// ruleSetUpdates outlives the Rule Sets pages (ruleSetStatus).
	ruleSetUpdates *ruleSetState
	copy           *copyDialog
	test           *testDialog

	// untrusted is whether the root certificate is known not to be
	// trusted; the tests judge HTTPS decryption by it.
	untrusted    bool
	trustLoading bool

	// topology shows Traffic Rules as the topology; topoTest is its test, nil
	// while closed.
	topology bool
	topoTest *topoTest
	// topoPreview is the rule a click on a node of the topology shows,
	// "final" for the default, "" for none.
	topoPreview string
	// topoCam pans and zooms the topology.
	topoCam topoCamera
	// topoFlash is the node Open pointed at in the topology.
	topoFlash topoFlash

	// editing records that an editor or dialog holds the store's edit.
	editing bool
	// reveal is the rule the table scrolls to next, as Open shows it.
	reveal string
	// highlight is the rule Open pointed at, marked until highlightUntil.
	highlight      string
	highlightUntil time.Time
}

// New returns the workspace.
func New(e *env.Env, s *store.Store) *Workspace {
	w := &Workspace{env: e, store: s, sidebar: true}
	e.Prefs.Load(prefDeviceWidth, &w.deviceWidth.Width)
	e.Prefs.Load(prefTopology, &w.topology)
	return w
}

// prefDeviceWidth keeps the width of the device pane.
const prefDeviceWidth = "kiko-routes-device-width"

// deviceWidthOf is the width to lay the device pane out at and the bounds
// a drag keeps it in, as a filter pane's.
func (w *Workspace) deviceWidthOf(c *ui.Context) (shown, lo, hi float32) {
	width, _ := c.Size()
	lo = min(kit.FilterPaneMinW, kit.FilterWidth)
	hi = max(lo, min(kit.FilterPaneMaxW, width/3))
	return w.deviceWidth.Shown(kit.FilterWidth, lo, hi), lo, hi
}

// doc is the document being shown.
func (w *Workspace) doc() *model.Doc { return w.store.Doc() }

// save changes the document through fn, which edits a copy; an error
// from fn or the store's checks shows as w.err and leaves it unchanged.
func (w *Workspace) save(fn func(doc *model.Doc) error) bool {
	next := model.Clone(w.doc())
	if err := fn(next); err != nil {
		w.err = err.Error()
		return false
	}
	if err := w.store.Update(func(doc *model.Doc) { *doc = *next }); err != nil {
		w.err = err.Error()
		return false
	}
	w.err = ""
	return true
}

// replace is save for model functions returning the next document.
func replace(doc *model.Doc, next *model.Doc, err error) error {
	if err != nil {
		return err
	}
	if next == nil {
		return errors.New("保存失败，请重试")
	}
	*doc = *next
	return nil
}

// validOwner keeps the chosen device in the document: a removed device or
// a removed local scope falls back to Local / Gateway or Other Devices.
func (w *Workspace) validOwner(doc *model.Doc) {
	fallback := model.OtherDeviceID
	if model.LocalEnabled(doc) {
		fallback = model.LocalActiveScope(doc)
	}
	switch w.owner {
	case "":
		w.owner = fallback
	case model.PreDeviceID, model.OtherDeviceID:
	default:
		if _, _, ok := model.ScopeDevice(doc, w.owner); ok {
			return
		}
		// A removed profile falls back to the profile in effect.
		for _, d := range model.ProfileDevices(doc) {
			if w.owner == d.ID || strings.HasPrefix(w.owner, d.ID+"@") {
				w.owner = model.ActiveScope(d)
				return
			}
		}
		w.owner = fallback
	}
}

// findDevice finds the LAN device with id, or the LAN device owning scope
// id (a profile's scope).
func findDevice(doc *model.Doc, id string) *model.RouteDevice {
	if model.IsLocalScope(id) {
		return nil
	}
	for _, d := range doc.Route.Devices {
		if d.ID == id {
			return d
		}
	}
	if d, _, ok := model.ScopeDevice(doc, id); ok {
		return d
	}
	return nil
}

// profileOwner finds the owner of profiles with id, or owning scope id: a
// LAN device, or the local scope (model.LocalDevice) while it exists.
func profileOwner(doc *model.Doc, id string) *model.RouteDevice {
	if model.IsLocalScope(id) {
		if d, _, ok := model.ScopeDevice(doc, id); ok {
			return d
		}
		if id == model.LocalDeviceID && model.LocalEnabled(doc) {
			return model.LocalDevice(doc)
		}
		return nil
	}
	return findDevice(doc, id)
}

// ownFinal is the default outbound a scope chooses itself, and whether it
// has one: a LAN device profile's, or a local profile's other than the
// original (which may inherit route.final or the core's default).
func ownFinal(doc *model.Doc, scope string) (string, bool) {
	if scope == model.LocalDeviceID {
		return "", false
	}
	if d, _, ok := model.ScopeDevice(doc, scope); ok {
		return model.ProfileFinal(d, scope), true
	}
	return "", false
}

// selectDevice shows a device's rules, closing the editor and the search.
func (w *Workspace) selectDevice(id string, narrow bool) {
	w.leaveEditor(func() { w.showDevice(id, narrow) })
}

// showDevice is selectDevice with the editor already left.
func (w *Workspace) showDevice(id string, narrow bool) {
	w.owner, w.search, w.notice, w.moving, w.pressed = id, "", "", nil, ""
	w.test, w.topoFlash = nil, topoFlash{}
	if narrow {
		w.sidebar = false
	}
}

// Open shows a rule another page links to (env.RouteRule, as the
// connection, DNS and usage inspectors name the matched rule): its device
// and tab (Traffic Rules or DNS Routing Rules) without a search, the rule or the
// device's default row (Default Outbound / Fallback DNS) scrolled to and highlighted.
// The topology, when Traffic Rules shows as one, stays: its node of the rule
// scrolls into view and flashes.
//
// env.NewRouteRule opens the editor of a new traffic rule of its device
// with its conditions, unsaved, as "Add to rule" of Connections, Usage and Processes asks.
func (w *Workspace) Open(arg any) {
	if w.store == nil || !w.store.Loaded {
		return
	}
	if draft, ok := arg.(env.NewRouteRule); ok {
		w.openNewRule(draft)
		return
	}
	target, ok := arg.(env.RouteRule)
	if !ok {
		return
	}
	sec := sectionTraffic
	switch target.Section {
	case "", "route":
	case "dns":
		sec = sectionDNS
	default:
		return
	}
	if target.Final {
		if target.Owner == "" || target.Owner == model.PreDeviceID {
			return
		}
		if !ownerExists(w.doc(), target.Owner) {
			w.env.Toast("该规则已删除")
			return
		}
		w.mark(target.Owner, sec, pinnedKey)
		return
	}
	if target.ID == "" {
		return
	}
	doc := w.doc()
	owner, found := "", false
	if sec == sectionDNS {
		for _, r := range doc.DNS.Rules {
			if r.ID == target.ID {
				owner, found = r.PolicyOwner(), true
				break
			}
		}
	} else {
		var rule *model.RouteRule
		for _, r := range doc.Route.Rules {
			if r.ID == target.ID {
				rule = r
				break
			}
		}
		if rule == nil && model.IsModuleRule(target.ID) {
			// A module's rule: read-only, shown among Pre-rules.
			for _, r := range model.ModuleRouteRules(doc) {
				if r.ID == target.ID {
					rule = r
					break
				}
			}
		}
		if rule != nil {
			owner, found = rule.PolicyOwner(), true
		}
	}
	if !found {
		w.env.Toast("该规则已删除")
		return
	}
	w.mark(owner, sec, target.ID)
}

// ownerExists reports whether owner's default row is still there: Other
// Devices, the local server while enabled, or a device of the document (a removed profile's
// scope shows the device's profile in effect).
func ownerExists(doc *model.Doc, owner string) bool {
	if owner == model.OtherDeviceID {
		return true
	}
	return slices.ContainsFunc(model.ProfileDevices(doc), func(d *model.RouteDevice) bool {
		return owner == d.ID || strings.HasPrefix(owner, d.ID+"@")
	})
}

// pinnedKey marks the default row (rowPinned) for reveal and highlight.
const pinnedKey = "\x00pinned"

// mark shows owner's tab sec with the rule id (or the default row,
// pinnedKey) marked where it stands rather than opened: the table
// scrolls to it and highlights it for a moment, the topology its node.
func (w *Workspace) mark(owner string, sec section, id string) {
	w.leaveEditor(func() {
		w.showDevice(owner, false)
		w.validOwner(w.doc())
		w.showSection(sec)
		w.err, w.fallback = "", nil
		if w.topology && sec == sectionTraffic {
			node := id
			if id == pinnedKey {
				node = "final"
			}
			w.topoFlash = topoFlash{id: node, owner: w.owner, pending: true}
			return
		}
		w.reveal = id
		w.highlight, w.highlightUntil = id, time.Now().Add(highlightFor)
	})
}

// highlightFor is how long a rule Open pointed at stays highlighted.
const highlightFor = 2500 * time.Millisecond

// openNewRule shows draft's device and the Traffic Rules tab with a new rule of
// its name and conditions open in the editor; saving it stays the user's.
func (w *Workspace) openNewRule(draft env.NewRouteRule) {
	w.leaveEditor(func() {
		doc := w.doc()
		w.showDevice(draft.Owner, false)
		w.validOwner(doc)
		w.showSection(sectionTraffic)
		w.err, w.fallback = "", nil
		rule := w.newRuleDraft(doc)
		rule.Name = draft.Name
		tree := model.EmptyConditionGroup()
		if draft.Tree != nil {
			tree = editorTree(draft.Tree, nil, "")
		}
		w.showEditor(&ruleEditor{open: true, isNew: true, route: rule, tree: tree})
	})
}

// setSection switches the tab, keeping the device and the sidebar.
func (w *Workspace) setSection(s section) {
	w.leaveEditor(func() { w.showSection(s) })
}

// showSection is setSection with the editor already left.
func (w *Workspace) showSection(s section) {
	w.sec, w.search, w.notice, w.moving, w.pressed = s, "", "", nil, ""
	w.test, w.topoFlash = nil, topoFlash{}
}

// syncEditing holds the store's edit while an editor or dialog is open.
func (w *Workspace) syncEditing() {
	open := w.editor != nil || w.device != nil || w.group != nil || w.settings != nil || w.copy != nil || w.profile != nil ||
		w.ruleSets != nil && w.ruleSets.detail != nil && w.ruleSets.detail.dirty()
	if open != w.editing {
		w.editing = open
		if open {
			w.store.BeginEdit()
		} else {
			w.store.EndEdit()
		}
	}
}

// View builds the workspace.
func (w *Workspace) View(c *ui.Context) {
	k := kit.T(c)
	width, _ := c.Size()
	narrow := width <= narrowWidth
	w.narrow = narrow
	if !w.pressSeen {
		w.press = pressPoint{}
	}
	w.rowHeld, w.pressSeen = false, false
	if !w.sidebarInit {
		w.sidebarInit, w.sidebar = true, !narrow
	}
	if w.store == nil || !w.store.Loaded {
		kit.Workspace(c, func() {
			text := "正在载入配置…"
			if w.store != nil && w.store.LoadError != "" {
				text = w.store.LoadError
			}
			kit.EmptyState(c, kit.SymRoute, text, "", nil)
		})
		return
	}
	doc := w.doc()
	w.validOwner(doc)
	w.forgetWaitingSwitch()
	kit.Workspace(c, func() {
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			width, lo, hi := w.deviceWidthOf(c)
			if w.sidebar && !narrow {
				w.sidebarView(c, doc, narrow, width)
			}
			ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).Background(k.Surface).Children(func() {
				w.toolbar(c, doc)
				if w.notice != "" {
					ui.Row(c).PaddingX(20).Height(28).Shrink(0).Children(func() {
						kit.Icon(c, kit.SymCircleCheck, 13).TextColor(k.Success)
						ui.Text(c, w.notice).FontSize(kit.FontTable).TextColor(k.Label2).Margin(0, 0, 0, 6).SingleLine()
					})
				}
				if w.topology && w.sec == sectionTraffic {
					w.topologyView(c, doc)
				} else {
					w.tableView(c, doc)
				}
			})
			if w.sidebar && !narrow && kit.ResizeHandle(c, &w.deviceWidth, width, lo, hi, "调整设备栏宽度") {
				w.env.Prefs.Save(prefDeviceWidth, w.deviceWidth.Width)
			}
		})
		if w.sidebar && narrow {
			w.sidebarOverlay(c, doc)
		}
	})
	w.editorView(c, doc)
	w.fallbackEditorView(c, doc)
	w.dialogs(c, doc)
	w.syncEditing()
}

// toolbar is the 64 DIP toolbar: the tabs, the search and the actions.
func (w *Workspace) toolbar(c *ui.Context, doc *model.Doc) {
	ui.Row(c).Height(kit.ToolbarHeight).PaddingX(20).Gap(10).Shrink(0).DragWindow().Children(func() {
		selected := int(w.sec)
		tabs := kit.Segmented(c, "section", &selected, kit.Segment{Label: "流量规则"}, kit.Segment{Label: "DNS 分流"})
		tabs.Label("路由分区")
		if tabs.Changed() {
			w.setSection(section(selected))
		}
		if w.sec == sectionTraffic {
			view := 0
			if w.topology {
				view = 1
			}
			views := kit.Segmented(c, "view", &view, kit.Segment{Icon: kit.SymList, Name: "列表"}, kit.Segment{Icon: kit.SymNetwork, Name: "拓扑"})
			views.Label("规则视图")
			if views.Changed() {
				w.topology = view == 1
				w.topoFlash = topoFlash{}
				w.env.Prefs.Save(prefTopology, w.topology)
			}
		}
		ui.Spacer(c)
		placeholder, label := "搜索规则与条件", "搜索路由规则"
		if w.sec == sectionDNS {
			placeholder, label = "搜索规则与解析器", "搜索 DNS 分流规则"
		}
		kit.Search(c, "search", &w.search, kit.SearchOptions{Placeholder: placeholder, Toolbar: true, Width: 270}).Label(label)
		kit.ToolbarGroup(c, func() {
			kit.IconButton(c, kit.SymPanelLeft, "设备分栏", kit.IconButtonOptions{Active: w.sidebar}).OnClick(func() { w.sidebar = !w.sidebar })
			ui.Box(c).Size(1, 18).Background(kit.T(c).HairlineSoft).Margin(0, 3).Shrink(0)
			kit.IconButton(c, kit.SymPlus, "添加", kit.IconButtonOptions{IconSize: 17}).PopoverMenuButton(func(menu *ui.Menu) {
				if menu.Item("添加规则").Chosen() {
					if w.sec == sectionDNS {
						w.addDNSRule(doc)
					} else {
						w.addRule(doc)
					}
				}
				if menu.Item("添加分组").Chosen() {
					w.addGroup()
				}
			})
			if w.topology && w.sec == sectionTraffic {
				kit.IconButton(c, kit.SymPlay, "测试路径", kit.IconButtonOptions{Active: w.topoTest != nil}).OnClick(w.toggleTopologyTest)
			} else {
				kit.IconButton(c, kit.SymPlay, "测试规则", kit.IconButtonOptions{}).OnClick(func() { w.test = newTestDialog(w, doc) })
			}
			kit.IconButton(c, kit.SymSettings, "路由设置", kit.IconButtonOptions{}).OnClick(func() { w.openSettings(doc) })
		})
	})
}

// editorKey is the key of the editor sidebar, the rule editor's and the
// default editor's alike: switching between them keeps it in place.
const editorKey = "editor"

// dragSlop is how far, in DIPs, a pressed row moves before its press is a
// drag.
const dragSlop = 4

// pressPoint is where a row's press began, by the row's key.
type pressPoint struct {
	key  string
	x, y float32
}

// trackPress follows the press on row key's button b: once it moves as a
// drag, the editor sidebar closes, so that the drag reorders the rows or
// moves the rule to a device as without it.
func (w *Workspace) trackPress(b ui.Element, key string) {
	w.pressSeen = true
	x, y, _ := b.PointerPosition()
	if w.press.key != key {
		w.press = pressPoint{key: key, x: x, y: y}
		return
	}
	// A rule editor with unsaved changes stays: the drop asks about it.
	if (w.editor != nil && !w.editor.dirty() || w.editor == nil && w.fallback != nil) && (abs32(x-w.press.x) > dragSlop || abs32(y-w.press.y) > dragSlop) {
		w.closeEditor()
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// addGroup opens the dialog creating a group of the current device.
func (w *Workspace) addGroup() {
	prefix := "rg"
	if w.sec == sectionDNS {
		prefix = "dg"
	}
	device := w.owner
	if device == model.LocalDeviceID {
		device = ""
	}
	w.err = ""
	w.group = &groupDialog{open: true, isNew: true, draft: model.RouteGroup{ID: model.NewID(prefix), DeviceID: device}}
}

// openSettings opens the route settings.
func (w *Workspace) openSettings(doc *model.Doc) {
	w.err = ""
	w.settings = newSettingsDialog(doc)
}
