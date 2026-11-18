package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// Rule set kinds of the editor's Type.
const (
	kindRemote = "remote"
	kindInline = "inline"
	kindCustom = "custom"
)

// searchDelay is how long the content's search waits for typing to pause.
const searchDelay = 200 * time.Millisecond

// contentHeight is the content table's tallest height, contentMin its
// least (room for its empty state).
const (
	contentHeight = 440
	contentMin    = 200
)

// addRowNarrow is the window width at and below which the "Add rule" row
// stacks its field over its value.
const addRowNarrow = 600

// ruleSetDetail is the page of a rule set: its settings, and its content
// with the overrides (or, for a rule set the user builds, its entries).
type ruleSetDetail struct {
	// id is the rule set shown; a draft is not stored yet.
	id      string
	isDraft bool
	builtin bool
	// original and value are the settings as stored and as edited; kind
	// the Type chosen.
	original model.RuleSet
	value    model.RuleSet
	kind     string
	// clients are HTTP clients created for the rule set, saved with it.
	clients    []*model.HTTPClient
	validation string
	del        kit.ConfirmState

	content ruleSetContent
	search  string
	table   kit.TableState
	// add is the "Add rule" row; editing the entry of a custom rule set it
	// edits instead.
	add struct {
		open, focus  bool
		field, value string
		err          string
	}
	editing *model.RuleSetItem
}

func newRuleSetDetail(item *model.RuleSet, draft bool) *ruleSetDetail {
	_, builtin := model.BuiltinRuleSetForEntry(item)
	d := &ruleSetDetail{id: item.ID, isDraft: draft, builtin: builtin, original: basicSettings(item), value: basicSettings(item), kind: kindOf(item)}
	d.add.field = model.RuleSetFields[0].Field
	return d
}

// basicSettings are a rule set's settings the page edits, without its
// content.
func basicSettings(r *model.RuleSet) model.RuleSet {
	return model.RuleSet{ID: r.ID, Tag: r.Tag, Enabled: r.Enabled, Mode: r.Mode, Path: r.Path, URL: r.URL,
		UpdateInterval: r.UpdateInterval, HTTPClient: r.HTTPClient}
}

func kindOf(r *model.RuleSet) string {
	switch {
	case r.Mode == "remote":
		return kindRemote
	case model.IsCustomRuleSet(r):
		return kindCustom
	}
	return kindInline
}

// dirty reports settings edited and not saved.
func (d *ruleSetDetail) dirty() bool {
	a, b := d.value, d.original
	return d.isDraft || d.kind != kindOf(&b) || a.Tag != b.Tag || a.Mode != b.Mode || a.URL != b.URL ||
		a.UpdateInterval != b.UpdateInterval || a.HTTPClient != b.HTTPClient
}

// ruleSetContent is a rule set's content as loaded in the background:
// the source's entries, and the merged view the search filters.
type ruleSetContent struct {
	// key names the source loaded; nonce changes it to load again.
	key   string
	nonce int
	// loads counts the source's loads.
	loads   int
	gen     int
	loading bool
	err     string
	source  []model.RuleSetItem
	index   model.RuleSetSourceIndex
	srcGen  int
	// view is the merged view as wanted (want) and as built; rows the
	// indexes of the items the search shows.
	want    string
	viewGen int
	view    []model.RuleSetViewItem
	rows    []int
	ready   bool
	// typed is the search as typed, query as applied after a pause.
	typed   string
	typedAt time.Time
	query   string
}

// reload loads the source again, as after an update.
func (ct *ruleSetContent) reload() { ct.nonce++ }

// sourceKey names the source of r: the document's rules of an inline
// rule set, the core's (or the link's) of a remote one. Saving the
// document copies its rules: an inline JSON or catalog set is named by
// what changes with its content (its link, update and size), so that
// saving overrides does not read a large set again; the rules of a custom
// set, which its entries' edits change, by their copy.
func sourceKey(r *model.RuleSet, nonce int) string {
	switch {
	case model.IsCustomRuleSet(r):
		return fmt.Sprintf("custom:%p:%d", r.Rules, len(r.Rules))
	case r.Mode == "inline":
		return fmt.Sprintf("inline:%s|%d|%d|%d|%d", r.URL, r.LastUpdatedAt, r.SourceVersion, len(r.Rules), nonce)
	}
	return fmt.Sprintf("remote:%s|%s|%d", r.Tag, r.URL, nonce)
}

func overridesKey(o *model.RuleSetOverrides) string {
	if o.Empty() {
		return ""
	}
	raw, _ := json.Marshal(o)
	return string(raw)
}

// syncContent starts the loads the rule set's content needs, in the
// background: the source when it changed, then the merged view and the
// search's rows. A frame only compares keys.
func (w *Workspace) syncContent(c *ui.Context, d *ruleSetDetail, r *model.RuleSet) {
	ct := &d.content
	if key := sourceKey(r, ct.nonce); key != ct.key {
		ct.key = key
		ct.loads++
		ct.gen++
		gen := ct.gen
		ct.loading, ct.err = true, ""
		remote, tag, url, rules := r.Mode != "inline", r.Tag, r.URL, r.Rules
		type loaded struct {
			items []model.RuleSetItem
			index model.RuleSetSourceIndex
		}
		env.Go(w.env, func(ctx context.Context) (loaded, error) {
			if remote {
				source, err := w.env.API.RuleSetSource(ctx, tag, url)
				if err != nil {
					return loaded{}, err
				}
				rules = source.Rules
			}
			items := model.FlattenRuleSet(rules)
			return loaded{items, model.IndexRuleSet(items)}, nil
		}, func(l loaded, err error) {
			if gen != ct.gen {
				return
			}
			ct.loading = false
			if err != nil {
				ct.err = err.Error()
				ct.source, ct.index, ct.ready = nil, nil, false
				return
			}
			ct.source, ct.index = l.items, l.index
			ct.srcGen++
		})
	}
	now := c.Now()
	if d.search != ct.typed {
		ct.typed, ct.typedAt = d.search, now
	}
	if ct.query != ct.typed {
		if wait := searchDelay - now.Sub(ct.typedAt); wait > 0 && ct.typed != "" {
			c.After(wait)
		} else {
			ct.query = ct.typed
		}
	}
	if ct.source == nil {
		return
	}
	ovKey := overridesKey(r.Overrides)
	want := fmt.Sprintf("%d|%s|%s", ct.srcGen, ovKey, ct.query)
	if want == ct.want {
		return
	}
	ct.want = want
	ct.viewGen++
	viewGen := ct.viewGen
	source, index, query := ct.source, ct.index, ct.query
	var overrides *model.RuleSetOverrides
	if ovKey != "" {
		overrides = model.Clone(r.Overrides)
	}
	type built struct {
		view []model.RuleSetViewItem
		rows []int
	}
	env.Go(w.env, func(context.Context) (built, error) {
		view := model.MergeRuleSetView(source, index, overrides)
		return built{view, model.FilterRuleSetView(view, query)}, nil
	}, func(b built, _ error) {
		if viewGen != ct.viewGen {
			return
		}
		// The choice follows the item it was on.
		var chosen *model.RuleSetViewItem
		if i, ok := d.table.Chosen(len(ct.rows)); ok {
			item := ct.view[ct.rows[i]]
			chosen = &item
		}
		ct.view, ct.rows, ct.ready = b.view, b.rows, true
		d.table.Clear()
		if chosen != nil {
			for i, at := range ct.rows {
				if ct.view[at].Entry() == chosen.Entry() {
					d.table.Selected = i
					break
				}
			}
		}
	})
}

// ruleSetDetailView is the page of a rule set.
func (w *Workspace) ruleSetDetailView(c *ui.Context, doc *model.Doc, p *ruleSetPage) {
	d := p.detail
	var current *model.RuleSet
	if !d.isDraft {
		if current = findDisplayRuleSet(doc, d.id); current == nil {
			p.detail = nil
			return
		}
		w.syncContent(c, d, current)
	}
	title := d.value.Tag
	switch {
	case d.isDraft:
		title = "新建规则集"
	case current != nil:
		title = current.Tag
	}
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backButton(c, "规则集", "返回规则集列表", &p.focusBack, func() { p.detail = nil })
			kit.PaneTitle(c, title, "", func() {
				if current != nil && !d.builtin {
					del := kit.Button(c, "删除规则集", kit.ButtonOptions{Variant: kit.Danger})
					if del.Clicked() {
						p.confirmID, d.del.Open = current.ID, true
					}
					w.ruleSetDeleteConfirm(c, doc, p, del, &d.del)
				}
			})
			w.ruleSetSettings(c, doc, p, d, current)
			if current != nil {
				w.ruleSetContentGroup(c, d, current)
			}
		})
	})
}

// ruleSetSettings is the group of a rule set's settings, saved by its
// button.
func (w *Workspace) ruleSetSettings(c *ui.Context, doc *model.Doc, p *ruleSetPage, d *ruleSetDetail, current *model.RuleSet) {
	k := kit.T(c)
	r := &d.value
	urlError := ""
	if d.kind != kindCustom {
		urlError = model.ValidateRuleSetURL(r.URL, map[string]string{kindRemote: "remote", kindInline: "inline"}[d.kind])
	}
	intervalError := ""
	if d.kind == kindRemote {
		intervalError = model.ValidateRuleSetUpdateInterval(cmpOr(r.UpdateInterval, "24h0m0s"))
	}
	kit.SettingsGroup(c, "", "", func() {
		kit.SettingsRow(c, "名称", "", func() {
			ui.Column(c).Width(controlWidth(c)).Children(func() { kit.Input(c, "tag", &r.Tag, "").Label("规则集名称") })
		})
		if !d.builtin {
			kit.SettingsRow(c, "类型", "", func() {
				kind := d.kind
				if kit.Select(c, "mode", &kind, []kit.Option{{Value: kindRemote, Label: "远程"}, {Value: kindInline, Label: "内联 JSON"}, {Value: kindCustom, Label: "自建"}},
					kit.SelectOptions{Width: controlWidth(c), Label: "规则集类型"}).Changed() {
					d.kind = kind
					r.Mode = map[string]string{kindRemote: "remote", kindInline: "inline", kindCustom: "inline"}[kind]
				}
			})
		}
		if d.kind != kindCustom {
			kit.SettingsRow(c, "来源链接", urlError, func() {
				placeholder := "https://example.com/rules.json"
				if d.kind == kindRemote {
					placeholder = "https://example.com/rules.srs"
				}
				ui.Column(c).Width(controlWidth(c)).Children(func() { kit.Input(c, "url", &r.URL, placeholder).Label("规则集来源链接") })
			})
		}
		if d.kind == kindRemote {
			kit.SettingsRow(c, "更新间隔", intervalError, func() {
				interval := cmpOr(r.UpdateInterval, "24h0m0s")
				ui.Column(c).Width(controlWidth(c)).Children(func() {
					if kit.Input(c, "interval", &interval, "24h0m0s").Label("规则集更新间隔").Changed() {
						r.UpdateInterval = interval
					}
				})
			})
			kit.SettingsRow(c, "下载出口", "", func() { w.ruleSetClientSelect(c, doc, d) })
		}
		if current != nil && ruleSetUpdateKind(current) != "" && !d.dirty() {
			at := p.lastUpdated(current)
			kit.SettingsRow(c, "上次更新", "", func() {
				title := "尚无成功更新"
				if at > 0 {
					title = timefmt.FormatMilli(int64(at), "2006/1/2 15:04:05")
				}
				if err := p.updateError(current); err != "" && !p.updating[current.ID] {
					kit.Hint(c, kit.Icon(c, kit.SymCircleX, 15).TextColor(k.Danger).Label("更新失败："+err), err)
				}
				kit.Hint(c, ui.Text(c, formatRuleSetUpdate(at)).FontSize(kit.FontBody).TextColor(k.Label2).FontFeatures("tnum"), title)
				blocker := p.updateBlocker(current)
				label := "立即更新"
				if p.updating[current.ID] {
					label = "正在更新…"
				}
				kit.Button(c, label, kit.ButtonOptions{Small: true, Icon: kit.SymRefreshCw, Disabled: blocker != "" || p.updating[current.ID], Hint: blocker}).
					Label("更新规则集 " + current.Tag).OnClick(func() { w.updateRuleSet(current) })
			})
		}
	})
	if d.dirty() {
		ui.Row(c).Gap(8).Margin(-13, 0, 25).Shrink(0).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { kit.FormError(c, d.validation) })
			if !d.isDraft {
				kit.Button(c, "还原", kit.ButtonOptions{}).OnClick(func() {
					d.value, d.kind, d.clients, d.validation = d.original, kindOf(&d.original), nil, ""
				})
			}
			label := "保存"
			if d.isDraft {
				label = "添加规则集"
			}
			kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() { w.saveRuleSet(doc, d, urlError, intervalError) })
		})
	}
	if d.isDraft {
		kit.SettingsGroup(c, "快捷模板", "", func() {
			ui.Row(c).Wrap().Gap(8).PaddingY(12).Children(func() {
				for _, t := range model.RuleSetTemplates {
					kit.Button(c.Key(t.Tag), t.Tag, kit.ButtonOptions{Small: true}).OnClick(func() {
						r.Tag, r.Mode, r.URL, r.UpdateInterval = t.Tag, "remote", t.URL, "24h0m0s"
						d.kind = kindRemote
					})
				}
			})
		})
	}
}

func cmpOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// ruleSetClientSelect chooses a remote rule set's HTTP client, offering
// to create one through an outbound.
func (w *Workspace) ruleSetClientSelect(c *ui.Context, doc *model.Doc, d *ruleSetDetail) {
	clients := append(slices.Clone(doc.HTTPClients), d.clients...)
	options := []kit.Option{{Value: "", Label: "默认下载客户端"}}
	for _, cl := range clients {
		if cl.Enabled {
			options = append(options, kit.Option{Value: cl.ID, Label: fmt.Sprintf("%s（%s）", cl.Tag, clientDetourName(doc, cl))})
		}
	}
	for _, t := range model.AllOutboundTargets(doc) {
		if t.Enabled() && !slices.ContainsFunc(clients, func(cl *model.HTTPClient) bool { return cl.Enabled && cl.Detour.ID() == t.ID() }) {
			options = append(options, kit.Option{Value: "outbound:" + t.ID(), Label: "新建客户端：" + t.Tag()})
		}
	}
	value := d.value.HTTPClient.ID()
	if !kit.Select(c, "client", &value, withUnknown(options, value), kit.SelectOptions{Width: controlWidth(c), Label: "规则集下载出口"}).Changed() {
		return
	}
	if !strings.HasPrefix(value, "outbound:") {
		d.value.HTTPClient = model.RefOf(value)
		return
	}
	outbound := strings.TrimPrefix(value, "outbound:")
	for _, cl := range clients {
		if cl.Enabled && cl.Detour.ID() == outbound {
			d.value.HTTPClient = model.Some(cl.ID)
			return
		}
	}
	t := model.FindOutboundTarget(doc, outbound)
	if !t.Valid() {
		return
	}
	cl := &model.HTTPClient{ID: model.NewID("http"), Tag: uniqueClientTag(clients, "规则集下载-"+t.Tag()), Enabled: true, Detour: model.Some(outbound)}
	d.clients = append(d.clients, cl)
	d.value.HTTPClient = model.Some(cl.ID)
}

// saveRuleSet validates and stores the edited settings, keeping the rule
// set's content and overrides (its content goes when its source changes)
// and renaming its references when its tag changed.
func (w *Workspace) saveRuleSet(doc *model.Doc, d *ruleSetDetail, urlError, intervalError string) {
	value := d.value
	if d.kind == kindCustom {
		value.Mode, value.URL = "inline", ""
		urlError = ""
	}
	if d.kind != kindRemote {
		intervalError = ""
	}
	tag := strings.TrimSpace(value.Tag)
	duplicate := slices.ContainsFunc(displayRuleSets(doc), func(r *model.RuleSet) bool { return r.ID != value.ID && strings.TrimSpace(r.Tag) == tag })
	switch {
	case tag == "":
		d.validation = "请填写规则集名称"
		return
	case duplicate:
		d.validation = "规则集名称已存在"
		return
	case urlError != "":
		d.validation = "规则集链接：" + urlError
		return
	case intervalError != "":
		d.validation = "更新间隔：" + intervalError
		return
	}
	value.Tag = tag
	var clients []*model.HTTPClient
	for _, cl := range d.clients {
		if cl.ID == value.HTTPClient.ID() {
			clients = append(clients, cl)
		}
	}
	builtin, isBuiltin := model.BuiltinRuleSetForEntry(&d.original)
	if w.save(func(doc *model.Doc) error {
		doc.HTTPClients = append(doc.HTTPClients, clients...)
		previous := ""
		var stored *model.RuleSet
		for i, r := range doc.Route.RuleSets {
			if r.ID == value.ID || isBuiltin && r.Tag == builtin.Tag {
				stored = r
				doc.Route.RuleSets[i] = r
				break
			}
		}
		if stored == nil {
			saved := value
			stored = &saved
			doc.Route.RuleSets = append(doc.Route.RuleSets, stored)
			if isBuiltin {
				previous = builtin.Tag
			}
		} else {
			previous = stored.Tag
			// Becoming custom, a rule set keeps what it loads: its inline
			// rules with the overrides folded in; a remote one starts empty.
			toCustom := d.kind == kindCustom && !model.IsCustomRuleSet(stored)
			var folded []any
			if toCustom && stored.Mode == "inline" {
				folded = model.FoldOverrides(stored.Rules, stored.Overrides)
			}
			next := model.UpdateRuleSetSource(stored, func(r *model.RuleSet) {
				r.Tag, r.Mode, r.URL, r.UpdateInterval, r.HTTPClient = value.Tag, value.Mode, value.URL, value.UpdateInterval, value.HTTPClient
			})
			if toCustom {
				next.Rules, next.Overrides = folded, nil
			}
			*stored = *next
		}
		if previous != "" && previous != tag {
			*doc = *model.RenameRuleSetReferences(doc, previous, tag)
		}
		return nil
	}) {
		value.ID = d.value.ID
		d.isDraft, d.validation, d.clients = false, "", nil
		d.value = basicSettings(&value)
		d.original = d.value
		d.kind = kindOf(&d.value)
	} else {
		d.validation = w.err
	}
}

// ruleSetContentGroup is a rule set's content: a search, the "Add rule" row
// and the table of entries.
func (w *Workspace) ruleSetContentGroup(c *ui.Context, d *ruleSetDetail, r *model.RuleSet) {
	k := kit.T(c)
	ct := &d.content
	custom := model.IsCustomRuleSet(r)
	ui.Column(c).Margin(0, 0, 25).Shrink(0).Children(func() {
		ui.Text(c, "规则").FontSize(kit.FontBody).FontWeight(600).Margin(0, 14, 9)
		ui.Column(c).Radius(kit.RadiusGroup).Background(k.Surface).Border(1, k.Label.Alpha(0.04)).Clip().Children(func() {
			ui.Row(c).Height(48).PaddingX(12).Gap(10).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					kit.Search(c, "content-search", &d.search, kit.SearchOptions{Placeholder: "搜索规则"}).Label("搜索规则")
				})
				if ct.ready {
					count := kit.Count(len(ct.rows))
					if ct.query != "" {
						count += " / " + kit.Count(len(ct.view))
					}
					ui.Text(c, count+" 条").FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Shrink(0)
				}
				kit.Button(c, "添加规则", kit.ButtonOptions{Small: true, Icon: kit.SymPlus, Disabled: !ct.ready}).OnClick(func() {
					d.add.open, d.add.focus, d.add.err, d.editing = true, true, "", nil
				})
			})
			if d.add.open {
				w.ruleSetAddRow(c, d, r, custom)
			}
			height := float32(contentHeight)
			if ct.ready {
				height = min(contentHeight, max(contentMin, float32(len(ct.rows)+1)*kit.RowHeight+8))
			}
			ui.Column(c).Height(height).Shrink(0).Children(func() {
				switch {
				case ct.loading && ct.source == nil:
					ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
						kit.Spinner(c, 18)
						ui.Text(c, "正在读取…").FontSize(kit.FontBody).TextColor(k.Label3)
					})
				case ct.err != "":
					kit.EmptyState(c, kit.SymTriangleAlert, "无法读取规则", ct.err, func() {
						kit.Button(c, "重试", kit.ButtonOptions{}).OnClick(ct.reload)
					})
				case ct.ready:
					w.ruleSetTable(c, d, r, custom)
				}
			})
		})
	})
}

// ruleSetAddRow adds an entry (or edits one of a custom rule set): a
// field, a value, and Enter to add the next.
func (w *Workspace) ruleSetAddRow(c *ui.Context, d *ruleSetDetail, r *model.RuleSet, custom bool) {
	k := kit.T(c)
	options := make([]kit.Option, len(model.RuleSetFields))
	for i, f := range model.RuleSetFields {
		options[i] = kit.Option{Value: f.Field, Label: f.Label}
	}
	width, _ := c.Size()
	narrow := width <= addRowNarrow
	ui.Column(c).PaddingX(12).PaddingY(8).Gap(6).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
		var line ui.Element
		if narrow {
			line = ui.Column(c).Gap(8)
		} else {
			line = ui.Row(c).Gap(8)
		}
		line.Children(func() {
			fieldWidth := float32(130)
			kit.Select(c, "add-field", &d.add.field, options, kit.SelectOptions{Width: fieldWidth, Label: "规则字段"})
			var input ui.Element
			ui.Row(c).Gap(8).Grow(1).MinWidth(0).FillWidth().Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					input = kit.Input(c, "add-value", &d.add.value, "值").Label("规则值")
				})
				label := "添加"
				if d.editing != nil {
					label = "保存"
				}
				submit := kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Small: true})
				closeButton := kit.IconButton(c, kit.SymX, "关闭添加规则", kit.IconButtonOptions{Size: 26, IconSize: 14})
				if submit.Clicked() || input.Submitted() {
					w.submitEntry(d, r, custom)
					d.add.focus = true
				}
				if closeButton.Clicked() {
					d.add.open, d.editing, d.add.err, d.add.value = false, nil, "", ""
				}
			})
			if d.add.focus {
				input.Focus()
				d.add.focus = false
			}
			if input.Shortcut(0, ui.KeyEscape) {
				d.add.open, d.editing, d.add.err, d.add.value = false, nil, "", ""
			}
		})
		if d.add.err != "" {
			kit.FormError(c, d.add.err)
		}
	})
}

// submitEntry adds the entry of the "Add rule" row: to a custom rule set's
// rules, or to the overrides of another.
func (w *Workspace) submitEntry(d *ruleSetDetail, r *model.RuleSet, custom bool) {
	e := model.NormalizeRuleSetEntry(model.RuleSetEntry{Field: d.add.field, Value: d.add.value})
	if msg := model.ValidateRuleSetEntry(e); msg != "" {
		d.add.err = msg
		return
	}
	exists := false
	saved := w.patchRuleSet(r, func(stored *model.RuleSet) {
		if custom {
			var ok bool
			if d.editing != nil {
				stored.Rules, ok = model.ReplaceRuleSetEntry(stored.Rules, d.editing.Entry(), e)
			} else {
				stored.Rules, ok = model.AddRuleSetEntry(stored.Rules, e)
			}
			exists = !ok
			return
		}
		var changed bool
		stored.Overrides, changed = model.AddOverride(stored.Overrides, d.content.index, e)
		exists = !changed
	})
	switch {
	case exists:
		d.add.err = "规则已存在"
	case !saved:
		d.add.err = w.err
	default:
		d.add.err, d.add.value = "", ""
		if d.editing != nil {
			d.editing, d.add.open = nil, false
		}
	}
}

// entryAction changes the entry of the merged view item: removes it,
// takes back its override, or deletes it from a custom rule set.
func (w *Workspace) entryAction(r *model.RuleSet, index model.RuleSetSourceIndex, item model.RuleSetViewItem, custom bool) {
	w.patchRuleSet(r, func(stored *model.RuleSet) {
		switch {
		case item.State != model.EntrySource:
			stored.Overrides, _ = model.UndoOverride(stored.Overrides, item)
		case custom:
			stored.Rules = model.RemoveRuleSetEntry(stored.Rules, item.RuleSetItem)
		case !item.Compound():
			stored.Overrides, _ = model.RemoveOverride(stored.Overrides, index, item.Entry())
		}
	})
}

// entryActionLabel names entryAction for an item, "" when there is none.
func entryActionLabel(item model.RuleSetViewItem, custom bool) (kit.Symbol, string) {
	switch {
	case item.State == model.EntryAdded:
		return kit.SymRotateCcw, "撤销添加"
	case item.State == model.EntryRemoved || item.State == model.EntryPartlyRemoved:
		return kit.SymRotateCcw, "撤销移除"
	case custom:
		return kit.SymTrash2, "删除"
	case item.Compound():
		return "", ""
	}
	return kit.SymBan, "移除"
}

// entryActionHint is the hint of an item's action: an address range's
// removal names the removed range it undoes.
func entryActionHint(item model.RuleSetViewItem, label string) string {
	if item.By != (model.RuleSetEntry{}) {
		return label + " " + item.By.Value
	}
	return label
}

// editEntry opens the "Add rule" row on an entry of a custom rule set.
func (d *ruleSetDetail) editEntry(item model.RuleSetItem) {
	if item.Compound() {
		return
	}
	d.editing = &item
	d.add.open, d.add.focus, d.add.err = true, true, ""
	d.add.field, d.add.value = item.Field, item.Value
}

// ruleSetTable shows the merged view's entries the search keeps, building
// only the rows in view.
func (w *Workspace) ruleSetTable(c *ui.Context, d *ruleSetDetail, r *model.RuleSet, custom bool) {
	k := kit.T(c)
	ct := &d.content
	width, _ := c.Size()
	narrow := width <= narrowWidth
	fieldWidth, stateWidth := float32(104), float32(72)
	if narrow {
		fieldWidth, stateWidth = 76, 60
	}
	columns := []kit.Column{{ID: "field", Title: "字段", Width: fieldWidth}, {ID: "value", Title: "值", MinWidth: 140}}
	// The marks show while there are overrides.
	if !r.Overrides.Empty() {
		columns = append(columns, kit.Column{ID: "state", Title: "状态", Width: stateWidth})
	}
	columns = append(columns, kit.Column{ID: "action", Title: "", Width: 40})
	rows, view := ct.rows, ct.view
	list := kit.Table(c, &d.table, columns, len(rows), kit.TableOptions{
		// Flat: a chosen row is the light RowChosen fill, not the accent,
		// so that its buttons keep their look and kit's hover.
		Label: "规则集内容", Striped: true, Flat: true,
		Menu: func(m *ui.Menu, row int) {
			item := view[rows[row]]
			if custom && item.State == model.EntrySource && !item.Compound() && m.Item("编辑").Chosen() {
				d.editEntry(item.RuleSetItem)
			}
			if _, label := entryActionLabel(item, custom); label != "" && m.Item(label).Chosen() {
				w.entryAction(r, d.content.index, item, custom)
			}
			// A range partly removed can go whole.
			if item.State == model.EntryPartlyRemoved && !custom && m.Item("移除").Chosen() {
				w.patchRuleSet(r, func(stored *model.RuleSet) {
					stored.Overrides, _ = model.RemoveOverride(stored.Overrides, d.content.index, item.Entry())
				})
			}
			if m.Item("复制值").Chosen() {
				c.WriteClipboard(item.Value)
			}
		},
		Empty: func() {
			title := "没有规则"
			if ct.query != "" {
				title = "没有匹配的规则"
			}
			kit.EmptyState(c, kit.SymFileSearch, title, "", func() {
				if ct.query != "" {
					kit.Button(c, "清除搜索", kit.ButtonOptions{}).OnClick(func() { d.search = "" })
				} else {
					kit.Button(c, "添加规则", kit.ButtonOptions{Icon: kit.SymPlus}).OnClick(func() { d.add.open, d.add.focus = true, true })
				}
			})
		},
	}, func(row, col int) {
		item := view[rows[row]]
		muted := item.State == model.EntryRemoved
		switch columns[col].ID {
		case "field":
			kit.Clipped(c, model.RuleSetFieldLabel(item.Field), kit.FontTable, 400).TextColor(k.Label2)
		case "value":
			v := kit.Clipped(c, item.Value, kit.FontTable, 400)
			if item.Compound() {
				v.Font(kit.MonoFont)
			}
			if muted {
				v.TextColor(k.Label3)
			}
		case "state":
			switch item.State {
			case model.EntryAdded:
				kit.Badge(c, "已添加", kit.AccentTone)
			case model.EntryRemoved:
				kit.Badge(c, "已移除", kit.Neutral)
			case model.EntryPartlyRemoved:
				kit.Badge(c, "部分移除", kit.Neutral)
			}
		case "action":
			if icon, label := entryActionLabel(item, custom); label != "" {
				if kit.IconButton(c.Key(row), icon, label+" "+item.Value, kit.IconButtonOptions{Size: 24, IconSize: 13, Hint: entryActionHint(item, label)}).Clicked() {
					w.entryAction(r, d.content.index, item, custom)
				}
			}
		}
	})
	if list.Submitted() && custom {
		if i, ok := d.table.Chosen(len(rows)); ok && view[rows[i]].State == model.EntrySource {
			d.editEntry(view[rows[i]].RuleSetItem)
		}
	}
}
