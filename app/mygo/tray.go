package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/servers"
)

// prefTraySpeed is the app preference that shows the rates next to the
// menu bar icon (macOS); unset means shown.
const prefTraySpeed = "trayShowSpeed"

// traySpeedEvery is how often the rates are read while they show.
const traySpeedEvery = time.Second

// trayLatencyLimit is how many members a group test measures at once.
const trayLatencyLimit = 8

// trayController is the menu bar item of the local server: the rates
// next to its icon, and a menu with each policy group's members to
// choose from and test, without opening the console. It follows the
// server's event stream, so a choice made in the console shows here and
// one made here shows there.
//
// Menu item Click handlers run on the main thread; they hand their work
// to goroutines, and nothing that waits for the main thread runs while
// mu is held.
type trayController struct {
	shell *shell
	api   *api.Client
	tray  *mygo.Tray

	mu         sync.Mutex
	running    bool
	down, up   float64
	groups     []api.Selector
	showSpeed  bool
	delays     map[string]trayDelay
	testing    map[string]bool
	shape      string
	title      string
	statusItem *mygo.MenuItem
	// groupItems are the groups' submenu items, testItems their "Test"
	// items and memberItems the members' items, by group and member.
	groupItems  map[string]*mygo.MenuItem
	testItems   map[string]*mygo.MenuItem
	memberItems map[string]map[string]*mygo.MenuItem
}

// startTray adds the menu bar item and follows the local server until the
// app quits. A failure leaves the app without one.
func (s *shell) startTray() {
	t := &trayController{shell: s, api: api.New(s.paths.serverURL()), showSpeed: true,
		delays: map[string]trayDelay{}, testing: map[string]bool{}}
	var show bool
	if s.prefs.Load(prefTraySpeed, &show) {
		t.showSpeed = show
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:              "kiko",
		Icon:            trayIconPNG(),
		IconIsTemplate:  true,
		MonospacedTitle: true,
		ToolTip:         "Kiko",
	})
	if err != nil {
		log.Printf("[tray] %v", err)
		return
	}
	t.tray = tray
	t.render()
	go t.follow(context.Background())
	go t.pollSpeed(context.Background())
}

// follow keeps the core's state and groups from the event stream, loading
// the groups again when the core starts or the stream reconnects.
func (t *trayController) follow(ctx context.Context) {
	t.api.Events(ctx, func(up bool) {
		if up {
			go t.loadGroups(ctx)
		}
	}, func(event api.Event) {
		switch event.Type {
		case "runtime":
			var runtime api.Runtime
			if event.Decode(&runtime) != nil {
				return
			}
			t.mu.Lock()
			started := runtime.Running() && !t.running
			t.running = runtime.Running()
			if !t.running {
				t.groups, t.down, t.up = nil, 0, 0
			}
			t.mu.Unlock()
			if started {
				go t.loadGroups(ctx)
			}
			t.render()
		case "selectors":
			var payload struct {
				Selectors []api.Selector `json:"selectors"`
			}
			if event.Decode(&payload) == nil && payload.Selectors != nil {
				t.mu.Lock()
				t.groups = payload.Selectors
				t.mu.Unlock()
				t.render()
			}
		}
	})
}

// loadGroups reads the groups, retrying every half second for a core
// that has just started and reports none yet.
func (t *trayController) loadGroups(ctx context.Context) {
	for attempt := 0; attempt <= 10 && ctx.Err() == nil; attempt++ {
		request, cancel := context.WithTimeout(ctx, 5*time.Second)
		runtime, err := t.api.Runtime(request)
		var groups []api.Selector
		if err == nil && runtime.Running() {
			groups, err = t.api.Selectors(request)
		}
		cancel()
		if err == nil {
			t.mu.Lock()
			t.running = runtime.Running()
			if t.running {
				t.groups = groups
			} else {
				t.groups = nil
			}
			t.mu.Unlock()
			t.render()
			if !runtime.Running() || len(groups) > 0 {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pollSpeed reads the rates every second while they show.
func (t *trayController) pollSpeed(ctx context.Context) {
	ticker := time.NewTicker(traySpeedEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		t.mu.Lock()
		wanted := t.showSpeed && t.running
		t.mu.Unlock()
		if !wanted {
			continue
		}
		request, cancel := context.WithTimeout(ctx, 2*time.Second)
		runtime, err := t.api.Runtime(request)
		cancel()
		if err != nil {
			continue
		}
		t.mu.Lock()
		t.running = runtime.Running()
		t.down, t.up = rateOf(runtime.DownloadRate), rateOf(runtime.UploadRate)
		t.mu.Unlock()
		t.renderTitle()
	}
}

func rateOf(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// render rebuilds the menu when its shape changed, else updates its
// items in place, so a menu kept open follows the choices and tests.
func (t *trayController) render() {
	t.mu.Lock()
	shape := trayShape(t.running, t.groups, runtimeIsDarwin())
	var menu *mygo.Menu
	if shape != t.shape {
		t.shape = shape
		menu = t.buildLocked()
	} else {
		t.updateLocked()
	}
	t.mu.Unlock()
	if menu != nil {
		t.tray.SetMenu(menu)
	}
	t.renderTitle()
}

// renderTitle shows the rates next to the icon, or nothing, and in the
// tooltip elsewhere than macOS.
func (t *trayController) renderTitle() {
	t.mu.Lock()
	title := ""
	if t.running && t.showSpeed {
		title = speedTitle(t.down, t.up)
	}
	changed := title != t.title
	t.title = title
	t.mu.Unlock()
	if !changed {
		return
	}
	if runtimeIsDarwin() {
		t.tray.SetTitle(title)
	} else if title != "" {
		t.tray.SetToolTip("Kiko  " + title)
	} else {
		t.tray.SetToolTip("Kiko")
	}
}

// statusLabelLocked is the first line of the menu.
func (t *trayController) statusLabelLocked() string {
	if t.running {
		return "Kiko 正在运行"
	}
	return "Kiko 未运行"
}

// buildLocked builds the menu: the state, the console, a submenu per
// group, the rates option and Quit. t.mu is held.
func (t *trayController) buildLocked() *mygo.Menu {
	t.groupItems = map[string]*mygo.MenuItem{}
	t.testItems = map[string]*mygo.MenuItem{}
	t.memberItems = map[string]map[string]*mygo.MenuItem{}
	t.statusItem = &mygo.MenuItem{Label: t.statusLabelLocked(), Disabled: true}
	items := []*mygo.MenuItem{
		t.statusItem,
		{Label: "打开 Kiko 控制台", Click: func(*mygo.MenuItem, *mygo.Window) {
			go t.shell.openServer(servers.LocalID)
		}},
		mygo.Separator(),
	}
	switch {
	case !t.running:
		items = append(items, &mygo.MenuItem{Label: "启动内核后可在这里切换节点", Disabled: true})
	case len(t.groups) == 0:
		items = append(items, &mygo.MenuItem{Label: "没有策略组", Disabled: true})
	}
	if t.running {
		for _, g := range t.groups {
			items = append(items, t.groupItemLocked(g))
		}
	}
	items = append(items, mygo.Separator())
	if runtimeIsDarwin() {
		items = append(items, &mygo.MenuItem{Label: "在菜单栏显示网速", Type: mygo.MenuItemCheckbox, Checked: t.showSpeed,
			Click: func(item *mygo.MenuItem, _ *mygo.Window) {
				show := item.Checked
				go t.setShowSpeed(show)
			}})
		items = append(items, mygo.Separator())
	}
	items = append(items, &mygo.MenuItem{Label: "退出 Kiko", Click: func(*mygo.MenuItem, *mygo.Window) {
		mygo.App.Quit()
	}})
	return mygo.NewMenu(items)
}

// groupItemLocked is a group's submenu: "Test", then its members, the
// chosen one checked, each with its latency. The members of a group the
// core chooses for itself (fastest, fallback) show, but cannot be chosen.
func (t *trayController) groupItemLocked(g api.Selector) *mygo.MenuItem {
	name := g.Name
	test := &mygo.MenuItem{Label: "测速", KeepOpen: true, Click: func(*mygo.MenuItem, *mygo.Window) {
		go t.testGroup(name)
	}}
	t.testItems[name] = test
	sub := []*mygo.MenuItem{test, mygo.Separator()}
	members := map[string]*mygo.MenuItem{}
	choose := switchable(g)
	for _, member := range g.All {
		item := &mygo.MenuItem{Label: member, Type: mygo.MenuItemRadio, KeepOpen: true, Disabled: !choose,
			BadgeShape: mygo.BadgeShapePill}
		if choose {
			item.Click = func(*mygo.MenuItem, *mygo.Window) { go t.choose(name, member) }
		}
		members[member] = item
		sub = append(sub, item)
	}
	t.memberItems[name] = members
	group := &mygo.MenuItem{Label: name, Submenu: sub}
	t.groupItems[name] = group
	t.applyGroupLocked(g)
	return group
}

// updateLocked brings the items to the groups' choices and latencies.
func (t *trayController) updateLocked() {
	if t.statusItem != nil {
		t.statusItem.SetLabel(t.statusLabelLocked())
	}
	for _, g := range t.groups {
		t.applyGroupLocked(g)
	}
}

// applyGroupLocked shows g's chosen member and its members' latencies.
// Setters on items not yet in a menu only change their fields.
func (t *trayController) applyGroupLocked(g api.Selector) {
	if group := t.groupItems[g.Name]; group != nil && group.Subtitle != g.Now {
		group.SetSubtitle(g.Now)
	}
	if test := t.testItems[g.Name]; test != nil {
		label := "测速"
		if t.testing[g.Name] {
			label = "正在测速…"
		}
		if test.Label != label {
			test.SetLabel(label)
		}
		if test.IsEnabled() == t.testing[g.Name] {
			test.SetEnabled(!t.testing[g.Name])
		}
	}
	for member, item := range t.memberItems[g.Name] {
		if checked := member == g.Now; item.IsChecked() != checked && checked {
			item.SetChecked(true)
		}
		text, color := memberDelay(g, member, t.delays).badge()
		if item.Badge != text {
			item.SetBadge(text)
		}
		if item.BadgeColor != color {
			item.SetBadgeColor(color)
		}
	}
}

// choose switches group to member in the core. The radio item checked
// itself; a failure puts the check back on the member the core has.
func (t *trayController) choose(group, member string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := t.api.Select(ctx, group, member)
	cancel()
	t.mu.Lock()
	if err == nil {
		for i := range t.groups {
			if t.groups[i].Name == group {
				t.groups[i].Now = member
			}
		}
	} else {
		for _, g := range t.groups {
			if item := t.memberItems[group][g.Now]; g.Name == group && item != nil {
				item.SetChecked(true)
			}
		}
	}
	t.mu.Unlock()
	if err != nil {
		mygo.RunOnMain(func() { mygo.Dialog.Error("切换节点失败", err.Error()) })
		return
	}
	t.render()
}

// testGroup measures the latency of each member of group, eight at a
// time, the badges filling in as the results come.
func (t *trayController) testGroup(group string) {
	t.mu.Lock()
	var members []string
	for _, g := range t.groups {
		if g.Name == group {
			members = append(members, g.All...)
		}
	}
	if t.testing[group] || len(members) == 0 {
		t.mu.Unlock()
		return
	}
	t.testing[group] = true
	for _, m := range members {
		t.delays[m] = trayDelay{Testing: true}
	}
	t.mu.Unlock()
	t.render()

	testURL, unified := t.latencySettings()
	sem := make(chan struct{}, trayLatencyLimit)
	var wg sync.WaitGroup
	for _, member := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			results, err := t.api.Latency(ctx, []api.LatencyTarget{{ID: member, Tag: member, URL: testURL, Unified: unified}})
			cancel()
			delay := trayDelay{Failed: true}
			if err == nil && len(results) > 0 {
				delay = delayOf(results[0])
			}
			t.mu.Lock()
			t.delays[member] = delay
			t.mu.Unlock()
			t.render()
		}()
	}
	wg.Wait()
	t.mu.Lock()
	delete(t.testing, group)
	t.mu.Unlock()
	t.render()
}

// latencySettings are the test address and measure the configuration
// sets, as the console tests with; the defaults when it cannot be read.
func (t *trayController) latencySettings() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := t.api.Store(ctx)
	var doc struct {
		LatencyTestURL string `json:"latencyTestURL"`
		LatencyPlain   bool   `json:"latencyPlain"`
	}
	if err != nil || json.Unmarshal(store.Doc, &doc) != nil {
		return model.DefaultLatencyTestURL, true
	}
	if doc.LatencyTestURL == "" {
		doc.LatencyTestURL = model.DefaultLatencyTestURL
	}
	return doc.LatencyTestURL, !doc.LatencyPlain
}

// setShowSpeed shows or hides the rates and remembers the choice.
func (t *trayController) setShowSpeed(show bool) {
	t.shell.prefs.Save(prefTraySpeed, show)
	t.mu.Lock()
	t.showSpeed = show
	t.mu.Unlock()
	t.renderTitle()
}
