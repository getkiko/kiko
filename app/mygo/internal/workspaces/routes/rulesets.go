package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// ruleSetPage is Settings › Rule Sets: the catalog and custom rule sets, their
// updates and HTTP download clients, and the page of the rule set opened
// (rulesetdetail.go).
type ruleSetPage struct {
	// ruleSetState is the workspace's, outliving the page.
	*ruleSetState
	confirm kit.ConfirmState
	// confirmID is the rule set the confirmation deletes.
	confirmID string
	detail    *ruleSetDetail
	// focusBack moves the focus to the back button of the page shown.
	focusBack bool
}

// ruleSetState is the rule sets' update state: kept by the workspace, so
// that an update under way stays one (and its outcome shows) after the
// pages closed and opened again.
type ruleSetState struct {
	// busy is "Update all" under way; message its outcome.
	busy    bool
	message string
	// status is the core's state of each remote rule set it loaded, by
	// tag; statusLoaded tells that it was read.
	status       map[string]api.RuleSetStatus
	statusLoaded bool
	statusAt     time.Time
	// updating and updateErr follow each rule set's update, by ID.
	updating  map[string]bool
	updateErr map[string]string
	// counts are the entry counts of inline rules, by their copy.
	counts map[string]int
}

// ruleSetStatus is the workspace's rule set update state.
func (w *Workspace) ruleSetStatus() *ruleSetState {
	if w.ruleSetUpdates == nil {
		w.ruleSetUpdates = &ruleSetState{status: map[string]api.RuleSetStatus{}, updating: map[string]bool{}, updateErr: map[string]string{}}
	}
	return w.ruleSetUpdates
}

func (w *Workspace) newRuleSetPage() *ruleSetPage {
	st := w.ruleSetStatus()
	// The page asks the core again as it opens.
	st.statusAt = time.Time{}
	return &ruleSetPage{ruleSetState: st}
}

// countEntries counts an inline rule set's entries as its page's table
// does (a compound rule is one), remembering the count of each copy of its
// rules so that a frame does not count them again.
func (p *ruleSetState) countEntries(r *model.RuleSet) int {
	key := fmt.Sprintf("%p:%d", r.Rules, len(r.Rules))
	if n, ok := p.counts[key]; ok {
		return n
	}
	if p.counts == nil || len(p.counts) > 64 {
		p.counts = map[string]int{}
	}
	n := model.CountRuleSetEntries(r.Rules)
	p.counts[key] = n
	return n
}

// usedRuleSetTags are the rule sets enabled rules use.
func usedRuleSetTags(doc *model.Doc) []string {
	var tags []string
	add := func(tree *model.Condition, match map[string]any) {
		matches := []map[string]any{match}
		if tree != nil {
			matches = model.ConditionMatches(tree)
		}
		for _, m := range matches {
			tags = append(tags, model.RuleSetValues(m["rule_set"])...)
		}
	}
	for _, r := range doc.Route.Rules {
		if r.Enabled {
			add(r.ConditionTree, r.Match)
		}
	}
	for _, r := range doc.DNS.Rules {
		if r.Enabled {
			add(r.ConditionTree, r.Match)
		}
	}
	return tags
}

// displayRuleSets are the catalog's entries, stored or as defaults, then
// the custom rule sets.
func displayRuleSets(doc *model.Doc) []*model.RuleSet {
	var out []*model.RuleSet
	for _, b := range model.BuiltinRuleSets {
		var found *model.RuleSet
		for _, r := range doc.Route.RuleSets {
			if r.ID == model.BuiltinRuleSetID(b.Tag) || r.Tag == b.Tag {
				found = r
				break
			}
		}
		if found == nil {
			found = model.DefaultBuiltinRuleSet(b)
		}
		out = append(out, found)
	}
	for _, r := range doc.Route.RuleSets {
		if _, builtin := model.BuiltinRuleSetForEntry(r); !builtin {
			out = append(out, r)
		}
	}
	return out
}

// findDisplayRuleSet is the rule set of id as listed, nil when gone.
func findDisplayRuleSet(doc *model.Doc, id string) *model.RuleSet {
	for _, r := range displayRuleSets(doc) {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// formatRuleSetUpdate is a last update time, — without one.
func formatRuleSetUpdate(ms float64) string {
	if ms <= 0 {
		return "—"
	}
	return timefmt.FormatMilli(int64(ms), "2006/01/02 15:04")
}

// uniqueClientTag is base, numbered past the tags taken.
func uniqueClientTag(clients []*model.HTTPClient, base string) string {
	tag := base
	for n := 2; slices.ContainsFunc(clients, func(c *model.HTTPClient) bool { return c.Tag == tag }); n++ {
		tag = fmt.Sprintf("%s-%d", base, n)
	}
	return tag
}

// ruleSetUpdateKind is how a rule set updates: "remote" by the core,
// "inline" by downloading its JSON into the document, "" not at all.
func ruleSetUpdateKind(r *model.RuleSet) string {
	switch {
	case strings.TrimSpace(r.URL) == "":
		return ""
	case r.Mode == "remote":
		return "remote"
	case r.Mode == "inline":
		return "inline"
	}
	return ""
}

// lastUpdated is when a rule set last updated: by the core for a remote
// one.
func (p *ruleSetState) lastUpdated(r *model.RuleSet) float64 {
	if r.Mode == "remote" {
		return p.status[r.Tag].LastUpdatedAt
	}
	return float64(r.LastUpdatedAt)
}

// updateBlocker is why a rule set cannot update now, "" when it can.
func (p *ruleSetState) updateBlocker(r *model.RuleSet) string {
	if ruleSetUpdateKind(r) != "remote" {
		return ""
	}
	if _, ok := p.status[r.Tag]; !ok {
		return "核心未加载此规则集"
	}
	return ""
}

// updateError is a rule set's last update failure: this page's, or the
// core's.
func (p *ruleSetState) updateError(r *model.RuleSet) string {
	if err := p.updateErr[r.ID]; err != "" {
		return err
	}
	if r.Mode == "remote" {
		return p.status[r.Tag].LastError
	}
	return ""
}

// loadStatus reads the core's state of the remote rule sets.
func (w *Workspace) loadStatus(p *ruleSetState) {
	p.statusAt = time.Now()
	env.Go(w.env, func(ctx context.Context) ([]api.RuleSetStatus, error) {
		return w.env.API.RuleSetStatus(ctx)
	}, func(list []api.RuleSetStatus, err error) {
		if err != nil {
			p.status, p.statusLoaded = map[string]api.RuleSetStatus{}, false
			return
		}
		status := map[string]api.RuleSetStatus{}
		for _, s := range list {
			status[s.Tag] = s
		}
		p.status, p.statusLoaded = status, true
	})
}

// RuleSetsPage builds Settings › Rule Sets, or the rule set opened there; back
// returns to Settings. The Settings workspace handles Escape (RuleSetsEscape).
func (w *Workspace) RuleSetsPage(c *ui.Context, back func()) {
	if w.ruleSets == nil {
		w.ruleSets = w.newRuleSetPage()
	}
	p := w.ruleSets
	if time.Since(p.statusAt) >= time.Minute {
		w.loadStatus(p.ruleSetState)
	}
	c.After(time.Minute)
	doc := w.doc()
	if p.detail != nil {
		w.ruleSetDetailView(c, doc, p)
	} else {
		w.ruleSetListView(c, doc, p, back)
	}
	w.syncEditing()
}

// RuleSetsEscape goes back from a rule set's page to the list, for the
// Settings workspace's Escape; false on the list.
func (w *Workspace) RuleSetsEscape() bool {
	if w.ruleSets == nil || w.ruleSets.detail == nil {
		return false
	}
	w.ruleSets.detail = nil
	w.syncEditing()
	return true
}

// FocusRuleSetsBack moves the focus to the back button of the page shown,
// as a first Escape in a text field does.
func (w *Workspace) FocusRuleSetsBack() {
	if w.ruleSets != nil {
		w.ruleSets.focusBack = true
	}
}

// backButton is a subpage's way back, taking the focus when *focus is
// set.
func backButton(c *ui.Context, title, label string, focus *bool, back func()) {
	ui.Row(c).Margin(0, 0, 14).Shrink(0).Children(func() {
		button := kit.Button(c, title, kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymArrowLeft}).Label(label).OnClick(back)
		if *focus {
			button.Focus()
			*focus = false
		}
	})
}

// ruleSetListView lists the rule sets.
func (w *Workspace) ruleSetListView(c *ui.Context, doc *model.Doc, p *ruleSetPage, back func()) {
	rows := displayRuleSets(doc)
	var remote []*model.RuleSet
	for _, r := range doc.Route.RuleSets {
		if r.Mode == "remote" {
			remote = append(remote, r)
		}
	}
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backButton(c, "设置", "返回设置", &p.focusBack, back)
			kit.PaneTitle(c, "规则集", "", func() {
				label := "全部更新"
				if p.busy {
					label = "正在更新…"
				}
				kit.Button(c, label, kit.ButtonOptions{Icon: kit.SymRefreshCw, Disabled: p.busy}).OnClick(func() { w.updateAllRuleSets(doc) })
				kit.Button(c, "新建规则集", kit.ButtonOptions{Variant: kit.Primary, Icon: kit.SymPlus}).OnClick(func() { w.addRuleSet(doc) })
			})
			if p.message != "" {
				ui.Column(c).Margin(0, 0, 16).Shrink(0).Children(func() { kit.Notice(c, kit.Neutral, "", p.message, nil) })
			}
			kit.SettingsGroup(c, "", "", func() {
				for _, item := range rows {
					w.ruleSetRow(c, doc, p, item)
				}
			})
			if len(remote) > 0 {
				w.downloadClients(c, doc, remote)
			}
		})
	})
}

// ruleSetCaption is a rule set's kind, size, overrides and last update.
func (p *ruleSetPage) ruleSetCaption(r *model.RuleSet) string {
	parts := []string{model.RuleSetKind(r)}
	if r.Mode == "inline" && r.Rules != nil {
		parts = append(parts, kit.Count(p.countEntries(r))+" 条")
	}
	if s := model.OverrideSummary(r.Overrides); s != "" {
		parts = append(parts, s)
	}
	if at := p.lastUpdated(r); at > 0 {
		parts = append(parts, formatRuleSetUpdate(at))
	}
	return strings.Join(parts, " · ")
}

// ruleSetRow is a rule set: its name opens it, as its chevron does, and
// its controls (update, switch, delete) stand beside that button, as
// buttons of their own that assistive technology reaches and that hover
// alone. The name, its icon and the chevron fade together while the
// pointer is on either, as a settings navigation row's do.
func (w *Workspace) ruleSetRow(c *ui.Context, doc *model.Doc, p *ruleSetPage, item *model.RuleSet) {
	k := kit.T(c)
	_, isBuiltin := model.BuiltinRuleSetForEntry(item)
	var trash, update ui.Element
	ui.Row(c.Key(item.ID)).MinHeight(kit.SettingsRowH).FillWidth().Children(func() {
		// The button reaches the controls: a click just before them opens
		// the rule set too.
		open := ui.ButtonBase(c).MinHeight(kit.SettingsRowH).Padding(8, 12, 8, 0).Gap(12).Grow(1).MinWidth(0).Justify(ui.Start).
			Label("打开规则集 " + item.Tag).Transition(kit.HoverMotion)
		open.Children(func() {
			if model.HasInvalidRuleSetReferences(doc, item) {
				kit.Icon(c, kit.SymTriangleAlert, 16).TextColor(k.Warning)
			} else {
				kit.Icon(c, kit.SymFileSearch, 16).TextColor(k.AccentText)
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				kit.Clipped(c, item.Tag, kit.FontBody, 400)
				kit.Clipped(c, p.ruleSetCaption(item), kit.FontCaption, 400).TextColor(k.Label3).FontFeatures("tnum")
			})
		})
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			if err := p.updateError(item); err != "" && !p.updating[item.ID] {
				kit.Hint(c, kit.Icon(c, kit.SymCircleX, 15).TextColor(k.Danger).Label("更新失败："+err), err)
			}
			if ruleSetUpdateKind(item) != "" {
				blocker := p.updateBlocker(item)
				hint := "更新"
				if blocker != "" {
					hint = blocker
				}
				update = kit.IconButton(c, kit.SymRefreshCw, "更新规则集 "+item.Tag, kit.IconButtonOptions{Size: 26, IconSize: 14,
					Disabled: blocker != "", Busy: p.updating[item.ID], BusyHint: "正在更新", Hint: hint})
				if update.Clicked() {
					w.updateRuleSet(item)
				}
			}
			enabled := item.Enabled
			if kit.Switch(c, &enabled, "启用规则集 "+item.Tag).Changed() {
				w.toggleRuleSet(item, enabled)
			}
			// A catalog set keeps the place of the delete button, so that the
			// switches line up.
			if isBuiltin {
				ui.Box(c).Width(26).Height(26).Shrink(0)
			} else {
				trash = kit.IconButton(c, kit.SymTrash2, "删除规则集 "+item.Tag, kit.IconButtonOptions{Size: 26, IconSize: 14})
				if trash.Clicked() {
					p.confirmID, p.confirm.Open = item.ID, true
				}
			}
		})
		// The chevron opens it as the name does: a pointer target only,
		// neither focused nor read, the name's button standing for both.
		chevron := ui.Box(c).MinHeight(kit.SettingsRowH).PaddingStart(12).Center().Shrink(0).Role(ui.RoleNone).
			Transition(kit.HoverMotion).Children(func() {
			kit.Icon(c, kit.SymChevronRight, 14).TextColor(k.Label3)
		})
		if open.Hovered() || chevron.Hovered() {
			open.Opacity(0.85)
			chevron.Opacity(0.85)
		}
		if open.Clicked() || chevron.Clicked() {
			w.openRuleSet(item)
		}
	})
	if p.confirmID == item.ID && !isBuiltin {
		w.ruleSetDeleteConfirm(c, doc, p, trash, &p.confirm)
	}
}

// ruleSetDeleteConfirm confirms deleting p.confirmID beside anchor.
func (w *Workspace) ruleSetDeleteConfirm(c *ui.Context, doc *model.Doc, p *ruleSetPage, anchor ui.Element, state *kit.ConfirmState) {
	tag := ""
	for _, r := range doc.Route.RuleSets {
		if r.ID == p.confirmID {
			tag = r.Tag
		}
	}
	id := p.confirmID
	kit.ConfirmPopover(c, anchor, state, kit.ConfirmOptions{Title: "删除规则集", Message: "删除「" + tag + "」后，关联匹配条件需要重新选择。",
		Confirm: "删除规则集", Danger: true, Run: func(done func(error)) {
			if w.save(func(doc *model.Doc) error {
				doc.Route.RuleSets = slices.DeleteFunc(slices.Clone(doc.Route.RuleSets), func(r *model.RuleSet) bool { return r.ID == id })
				return nil
			}) {
				done(nil)
				p.confirmID = ""
				if p.detail != nil && p.detail.id == id {
					p.detail = nil
				}
				return
			}
			done(errString("保存失败，请重试"))
		}})
}

// patchRuleSet changes a rule set as stored, storing a catalog entry
// shown by its defaults first.
func (w *Workspace) patchRuleSet(item *model.RuleSet, patch func(r *model.RuleSet)) bool {
	builtin, isBuiltin := model.BuiltinRuleSetForEntry(item)
	return w.save(func(doc *model.Doc) error {
		for _, r := range doc.Route.RuleSets {
			if r.ID == item.ID || isBuiltin && r.Tag == builtin.Tag {
				patch(r)
				return nil
			}
		}
		if !isBuiltin {
			return errors.New("规则集已删除")
		}
		next := model.Clone(item)
		patch(next)
		doc.Route.RuleSets = append(doc.Route.RuleSets, next)
		return nil
	})
}

// toggleRuleSet turns a rule set on or off, storing a catalog entry.
func (w *Workspace) toggleRuleSet(item *model.RuleSet, enabled bool) {
	w.patchRuleSet(item, func(r *model.RuleSet) { r.Enabled = enabled })
}

// fetchRuleSetJSON downloads a JSON source rule set through the server.
func fetchRuleSetJSON(ctx context.Context, client *api.Client, url string) (rules []any, version int, err error) {
	result, err := client.Fetch(ctx, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if result.Status < 200 || result.Status > 299 {
		return nil, 0, fmt.Errorf("下载失败：HTTP %d", result.Status)
	}
	var content struct {
		Version float64 `json:"version"`
		Rules   []any   `json:"rules"`
	}
	if json.Unmarshal([]byte(result.Body), &content) != nil || content.Rules == nil || !slices.Contains([]float64{1, 2, 3, 4}, content.Version) {
		return nil, 0, errors.New("不是有效的规则集 JSON")
	}
	return content.Rules, int(content.Version), nil
}

// updateRuleSet updates one rule set: a remote one by the core, an
// inline one from its JSON link into the document.
func (w *Workspace) updateRuleSet(item *model.RuleSet) {
	p := w.ruleSetStatus()
	id, tag, url := item.ID, item.Tag, item.URL
	if p.updating[id] {
		return
	}
	// The page open as it ends shows the new source.
	reload := func() {
		if w.ruleSets != nil && w.ruleSets.detail != nil && w.ruleSets.detail.id == id {
			w.ruleSets.detail.content.reload()
		}
	}
	switch ruleSetUpdateKind(item) {
	case "remote":
		if p.updateBlocker(item) != "" {
			return
		}
		p.updating[id] = true
		delete(p.updateErr, id)
		env.Go(w.env, func(ctx context.Context) (api.RuleSetStatus, error) {
			return w.env.API.UpdateRuleSet(ctx, tag)
		}, func(status api.RuleSetStatus, err error) {
			delete(p.updating, id)
			switch {
			case err != nil:
				p.updateErr[id] = err.Error()
			case status.LastError != "":
				p.updateErr[id] = status.LastError
			default:
				if status.Tag == "" {
					status.Tag = tag
				}
				p.status[tag] = status
				reload()
			}
			w.loadStatus(p)
		})
	case "inline":
		p.updating[id] = true
		delete(p.updateErr, id)
		type loaded struct {
			rules   []any
			version int
		}
		env.Go(w.env, func(ctx context.Context) (loaded, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			rules, version, err := fetchRuleSetJSON(ctx, w.env.API, url)
			return loaded{rules, version}, err
		}, func(l loaded, err error) {
			delete(p.updating, id)
			if err != nil {
				p.updateErr[id] = err.Error()
				return
			}
			// A rule set changed meanwhile (its link, or to remote or
			// custom) keeps what it has.
			current := findDisplayRuleSet(w.doc(), id)
			if current == nil || current.Mode != "inline" || current.URL != url {
				p.updateErr[id] = "配置已变化，请重新更新"
				return
			}
			at := time.Now().UnixMilli()
			if !w.patchRuleSet(current, func(r *model.RuleSet) {
				r.Rules, r.SourceVersion, r.LastUpdatedAt = l.rules, l.version, at
			}) {
				p.updateErr[id] = w.err
			} else {
				reload()
			}
		})
	}
}

// updateAllRuleSets updates every remote rule set the core loaded and
// downloads the inline JSON rule sets in use.
func (w *Workspace) updateAllRuleSets(doc *model.Doc) {
	p := w.ruleSetStatus()
	for _, r := range displayRuleSets(doc) {
		if r.Enabled && ruleSetUpdateKind(r) == "remote" && p.updateBlocker(r) == "" {
			w.updateRuleSet(r)
		}
	}
	w.fetchRuleSets(doc, p)
}

// addRuleSet opens the page of a new remote rule set.
func (w *Workspace) addRuleSet(doc *model.Doc) {
	used := map[string]bool{}
	for _, r := range doc.Route.RuleSets {
		used[r.Tag] = true
	}
	index := len(doc.Route.RuleSets) + 1
	for used[fmt.Sprintf("自定义规则集 %d", index)] {
		index++
	}
	draft := model.RuleSet{ID: model.NewID("rs"), Tag: fmt.Sprintf("自定义规则集 %d", index), Enabled: true, Mode: "remote", UpdateInterval: "24h0m0s"}
	w.ruleSets.detail = newRuleSetDetail(&draft, true)
}

// openRuleSet opens a rule set's page.
func (w *Workspace) openRuleSet(item *model.RuleSet) {
	w.ruleSets.detail = newRuleSetDetail(item, false)
}

// downloadClients is the Download group: the HTTP client of every remote
// rule set, and removing clients.
func (w *Workspace) downloadClients(c *ui.Context, doc *model.Doc, remote []*model.RuleSet) {
	shared := remote[0].HTTPClient.ID()
	for _, r := range remote {
		if r.HTTPClient.ID() != shared {
			shared = "__mixed__"
		}
	}
	kit.SettingsGroup(c, "下载", "", func() {
		kit.SettingsRow(c, "下载客户端", "", func() {
			options := []kit.Option{{Value: "", Label: "默认网络"}}
			if shared == "__mixed__" {
				options = append(options, kit.Option{Value: "__mixed__", Label: "当前设置不同"})
			}
			for _, cl := range doc.HTTPClients {
				if cl.Enabled {
					options = append(options, kit.Option{Value: cl.ID, Label: fmt.Sprintf("%s（%s）", cl.Tag, clientDetourName(doc, cl))})
				}
			}
			for _, o := range doc.Outbounds {
				if o.Enabled {
					options = append(options, kit.Option{Value: "outbound:" + o.ID, Label: "新建客户端：" + o.Tag})
				}
			}
			value := shared
			if kit.Select(c, "client", &value, options, kit.SelectOptions{Width: controlWidth(c), Label: "规则集下载客户端"}).Changed() && value != "__mixed__" {
				w.setAllHTTPClients(value)
			}
		})
		if len(doc.HTTPClients) > 0 {
			ui.Row(c).Wrap().Gap(8).PaddingY(12).Children(func() {
				for _, cl := range doc.HTTPClients {
					id := cl.ID
					kit.Button(c.Key(id), "删除 "+cl.Tag, kit.ButtonOptions{Small: true, Variant: kit.Danger}).OnClick(func() {
						w.save(func(doc *model.Doc) error {
							doc.HTTPClients = slices.DeleteFunc(slices.Clone(doc.HTTPClients), func(x *model.HTTPClient) bool { return x.ID == id })
							return nil
						})
					})
				}
			})
		}
	})
}

// clientDetourName is the outbound an HTTP client dials through.
func clientDetourName(doc *model.Doc, cl *model.HTTPClient) string {
	if t := model.FindOutboundTarget(doc, cl.Detour.ID()); t.Valid() {
		return t.Tag()
	}
	return "默认出口"
}

// setAllHTTPClients sets every remote rule set's HTTP client, creating
// one through an outbound when asked.
func (w *Workspace) setAllHTTPClients(value string) {
	w.save(func(doc *model.Doc) error {
		outbound := strings.TrimPrefix(value, "outbound:")
		var clientID string
		switch {
		case value == "":
		case strings.HasPrefix(value, "outbound:"):
			for _, cl := range doc.HTTPClients {
				if cl.Detour.ID() == outbound {
					clientID = cl.ID
				}
			}
			if clientID == "" {
				base := "规则集下载"
				for _, o := range doc.Outbounds {
					if o.ID == outbound {
						base = "规则集下载-" + o.Tag
					}
				}
				cl := &model.HTTPClient{ID: model.NewID("http"), Tag: uniqueClientTag(doc.HTTPClients, base), Enabled: true, Detour: model.Some(outbound)}
				doc.HTTPClients = append(doc.HTTPClients, cl)
				clientID = cl.ID
			}
		default:
			clientID = value
		}
		doc.Route.DefaultHTTPClient = model.RefOf(clientID)
		for _, r := range doc.Route.RuleSets {
			if r.Mode == "remote" {
				r.HTTPClient = model.RefOf(clientID)
			}
		}
		return nil
	})
}

// fetchRuleSets downloads the inline JSON rule sets in use, through the
// server, and stores their rules.
func (w *Workspace) fetchRuleSets(doc *model.Doc, p *ruleSetState) {
	if w.editing || p.busy {
		return
	}
	used := usedRuleSetTags(doc)
	materialized := model.MaterializeBuiltinRuleSets(doc.Route.RuleSets, used)
	changed := len(materialized) != len(doc.Route.RuleSets)
	var targets []*model.RuleSet
	for _, r := range materialized {
		if r.Enabled && r.Mode == "inline" && r.URL != "" && (len(r.Rules) == 0 || slices.Contains(used, r.Tag)) {
			targets = append(targets, r)
		}
	}
	if len(targets) == 0 {
		p.message = ""
		return
	}
	version := w.store.Version()
	p.busy, p.message = true, ""
	type loaded struct {
		id, url       string
		rules         []any
		sourceVersion int
		at            int64
	}
	type outcome struct {
		loaded []loaded
		failed []string
	}
	env.Go(w.env, func(ctx context.Context) (outcome, error) {
		var out outcome
		for _, r := range targets {
			rules, sourceVersion, err := fetchRuleSetJSON(ctx, w.env.API, r.URL)
			if err != nil {
				out.failed = append(out.failed, r.Tag)
				continue
			}
			out.loaded = append(out.loaded, loaded{r.ID, r.URL, rules, sourceVersion, time.Now().UnixMilli()})
		}
		return out, nil
	}, func(out outcome, _ error) {
		p.busy = false
		if w.store.Version() != version {
			p.message = "配置已变化，请重新更新"
			return
		}
		byID := map[string]loaded{}
		for _, l := range out.loaded {
			byID[l.id] = l
		}
		saved := w.save(func(doc *model.Doc) error {
			list := doc.Route.RuleSets
			if changed {
				// A catalog set stored for the first time is stored only
				// once downloaded: one that failed keeps its default (the
				// core's remote srs).
				stored := map[string]bool{}
				for _, r := range doc.Route.RuleSets {
					stored[r.ID] = true
				}
				list = nil
				for _, r := range model.Clone(materialized) {
					if _, ok := byID[r.ID]; ok || stored[r.ID] {
						list = append(list, r)
					}
				}
			}
			for _, r := range list {
				if l, ok := byID[r.ID]; ok && r.URL == l.url {
					r.Rules, r.SourceVersion, r.LastUpdatedAt, r.Mode = l.rules, l.sourceVersion, l.at, "inline"
				}
			}
			doc.Route.RuleSets = list
			return nil
		})
		switch {
		case !saved:
			p.message = w.err
		case len(out.failed) > 0:
			p.message = "更新失败：" + strings.Join(out.failed, "、")
		default:
			p.message = fmt.Sprintf("已导入 %d 个规则集", len(targets))
		}
	})
}

// controlWidth is the width of a settings row's control on the rule set
// pages: narrower in a narrow window, so that the row's label keeps one
// line.
func controlWidth(c *ui.Context) float32 {
	if width, _ := c.Size(); width <= 560 {
		return 180
	}
	return 260
}
