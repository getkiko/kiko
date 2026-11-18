package routes

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Tabs of the process picker.
const (
	pickApplications = 0
	pickProcesses    = 1
	pickCustom       = 2
)

// processPicker is the dialog choosing applications, running processes
// or custom process conditions (ProcessPicker).
type processPicker struct {
	open    bool
	path    []int
	editing *model.ProcessTag
	tab     int
	query   string
	loading bool
	err     string
	apps    *api.Applications
	snap    *api.ProcessSnapshot
	// loaded is the tab the data was loaded for, -1 for none.
	loaded    int
	collapsed map[string]bool
	custom    int
	drafts    [3]string
	invalid   string
}

func newProcessPicker(path []int, editing *model.ProcessTag) *processPicker {
	p := &processPicker{open: true, path: path, editing: editing, loaded: -1, collapsed: map[string]bool{}}
	switch {
	case editing == nil:
		p.tab = pickApplications
	case editing.Source == model.ProcessSourceApplication:
		p.tab = pickApplications
	case editing.Source == model.ProcessSourceProcess:
		p.tab = pickProcesses
	default:
		p.tab = pickCustom
	}
	if editing != nil {
		p.custom = slices.Index([]string{model.ProcessTypeName, model.ProcessTypePath, model.ProcessTypeRegex}, editing.Type)
		if p.custom < 0 {
			p.custom = 0
		}
		if editing.Source != model.ProcessSourceApplication {
			p.drafts[p.custom] = editing.Value
		}
	}
	return p
}

// processGroup is the running processes of an application.
type processGroup struct {
	key, name string
	items     []processItem
}

// processItem is a running executable and how many processes run it.
type processItem struct {
	process api.MonitoredProcess
	count   int
}

// groupProcesses groups the running processes with a path by their
// application, matching search; "Other processes" last.
func groupProcesses(snap *api.ProcessSnapshot, search string) []processGroup {
	if snap == nil {
		return nil
	}
	type entry struct {
		key  string
		item processItem
	}
	var order []string
	paths := map[string]*processItem{}
	for _, p := range snap.Processes {
		if p.Path == "" {
			continue
		}
		key := p.Path
		if snap.OS == "windows" {
			key = strings.ToLower(key)
		}
		if item, ok := paths[key]; ok {
			item.count++
			continue
		}
		paths[key] = &processItem{process: p, count: 1}
		order = append(order, key)
	}
	groups := map[string]*processGroup{}
	var keys []string
	for _, k := range order {
		item := paths[k]
		p := item.process
		if !strings.Contains(strings.ToLower(fmt.Sprintf("%s %s %s %d", p.Name, p.AppName, p.Path, p.PID)), search) {
			continue
		}
		key := p.AppPath
		if key == "" {
			key = "other"
		}
		g, ok := groups[key]
		if !ok {
			name := p.AppName
			if name == "" {
				name = "其他进程"
			}
			g = &processGroup{key: key, name: name}
			groups[key] = g
			keys = append(keys, key)
		}
		g.items = append(g.items, *item)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		switch {
		case a == "other":
			return false
		case b == "other":
			return true
		}
		return groups[a].name < groups[b].name
	})
	out := make([]processGroup, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		sort.SliceStable(g.items, func(i, j int) bool { return g.items[i].process.Name < g.items[j].process.Name })
		out = append(out, *g)
	}
	return out
}

var reAbsolutePath = regexp.MustCompile(`^(/|[A-Za-z]:[\\/]|\\\\)`)

// processField is the Process condition: TAGs of the selections and Add.
func (w *Workspace) processField(c *ui.Context, e *ruleEditor, group *model.Condition, path []int) {
	k := kit.T(c)
	at := slices.IndexFunc(group.Rules, func(n *model.Condition) bool { return n.Kind == model.ConditionGroup && n.UIField == "process" })
	var value *model.Condition
	if at >= 0 {
		value = group.Rules[at]
	}
	commit := func(next *model.Condition) {
		e.update(path, func(g *model.Condition) {
			i := slices.IndexFunc(g.Rules, func(n *model.Condition) bool { return n.Kind == model.ConditionGroup && n.UIField == "process" })
			if next != nil && len(next.Rules) == 0 {
				next = nil
			}
			switch {
			case i >= 0 && next != nil:
				g.Rules[i] = next
			case i >= 0:
				g.Rules = slices.Delete(g.Rules, i, i+1)
			case next != nil:
				g.Rules = append(g.Rules, next)
			}
		})
	}
	source := value
	if source == nil {
		source = model.EmptyProcessCondition()
	}
	tags := model.ProcessTags(source)
	fieldHead(c, "进程", value != nil, value != nil && value.Invert, false, func() {
		if value != nil {
			next := model.CloneCondition(value)
			next.Invert = !next.Invert
			commit(next)
		}
	}, func() { commit(nil) })
	ui.Row(c).Wrap().Gap(4).Children(func() {
		for _, tag := range tags {
			label := model.ProcessSelectionLabel(tag.ProcessSelection)
			chip := ui.Row(c.Key(fmt.Sprint(tag.Path, tag.Index, tag.Field))).Height(24).PaddingStart(8).PaddingEnd(3).Gap(5).Radius(7).
				Background(k.Fill).Shrink(0)
			chip.Children(func() {
				body := ui.ButtonBase(c).Gap(5).Label("修改进程 " + label)
				body.Children(func() {
					if tag.Source == model.ProcessSourceApplication {
						kit.Icon(c, kit.SymLaptop, 12).TextColor(k.Label2)
					}
					ui.Text(c, label).FontSize(kit.FontTable).SingleLine()
					kind := model.ProcessSelectionType(tag.ProcessSelection)
					if tag.Excluded {
						kind = "排除 · " + kind
					}
					ui.Text(c, kind).FontSize(10).TextColor(k.Label3)
				})
				if body.Clicked() {
					t := tag
					e.cond.process = newProcessPicker(path, &t)
				}
				x := ui.ButtonBase(c).Size(16, 16).Radius(8).Center().Label("移除进程 " + label).TextColor(k.Label3)
				x.Children(func() { kit.Icon(c, kit.SymX, 10) })
				if x.Clicked() && value != nil {
					commit(model.ChangeProcessTag(value, tag, nil))
				}
			})
		}
		kit.Button(c, "添加", kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).Label("添加进程条件").OnClick(func() {
			e.cond.process = newProcessPicker(path, nil)
		})
	})
	if value != nil && len(tags) > 1 {
		hint := "沿用既有条件组合；同一列表内匹配任意一个值"
		if value.Mode == "or" {
			hint = "匹配任意一个应用或进程"
		}
		ui.Text(c, hint).FontSize(kit.FontCaption).TextColor(k.Label3)
	}
	p := e.cond.process
	if p == nil || pathKey(p.path) != pathKey(path) {
		return
	}
	w.processPickerView(c, p, tags, func(item model.ProcessSelection) {
		group := value
		if group == nil {
			group = model.EmptyProcessCondition()
		}
		if p.editing != nil {
			commit(model.ChangeProcessTag(group, *p.editing, &item))
			p.open = false
			return
		}
		commit(model.AddProcessSelection(group, item))
	})
	if !p.open {
		e.cond.process = nil
	}
}

// load reads the picker's list for its tab from the server's host.
func (w *Workspace) loadPicker(p *processPicker, refresh bool) {
	if p.tab == pickCustom {
		return
	}
	p.loading, p.err, p.loaded = true, "", p.tab
	tab := p.tab
	if tab == pickApplications {
		env.Go(w.env, func(ctx context.Context) (api.Applications, error) {
			return w.env.API.Applications(ctx, refresh)
		}, func(apps api.Applications, err error) {
			if p.tab != tab {
				return
			}
			p.loading = false
			if err != nil {
				p.err = err.Error()
				return
			}
			p.apps = &apps
		})
		return
	}
	env.Go(w.env, func(ctx context.Context) (api.ProcessSnapshot, error) {
		return w.env.API.ProcessMonitor(ctx)
	}, func(snap api.ProcessSnapshot, err error) {
		if p.tab != tab {
			return
		}
		p.loading = false
		if err != nil {
			p.err = err.Error()
			return
		}
		p.snap = &snap
	})
}

// processPickerView is the dialog adding applications, processes or
// custom conditions.
func (w *Workspace) processPickerView(c *ui.Context, p *processPicker, tags []model.ProcessTag, add func(model.ProcessSelection)) {
	k := kit.T(c)
	if p.loaded != p.tab {
		w.loadPicker(p, false)
	}
	selected := func(item model.ProcessSelection) bool {
		key := model.ProcessSelectionKey(item)
		for _, t := range tags {
			if model.ProcessSelectionKey(t.ProcessSelection) == key &&
				!(p.editing != nil && t.Index == p.editing.Index && fmt.Sprint(t.Path) == fmt.Sprint(p.editing.Path)) {
				return true
			}
		}
		return false
	}
	title := "选择应用或进程"
	if p.editing != nil {
		title = "修改进程条件"
	}
	changeTab := func(tab int) {
		p.tab, p.query, p.invalid = tab, "", ""
	}
	resultRow := func(item model.ProcessSelection, icon kit.Symbol, detail, trailing string) {
		added := selected(item)
		name := item.Label
		if name == "" {
			name = item.Value
		}
		state := "选择 "
		if added {
			state = "已添加 "
		}
		row := ui.ButtonBase(c.Key(model.ProcessSelectionKey(item))).MinHeight(40).PaddingX(10).Gap(10).Radius(8).FillWidth().
			Justify(ui.Start).Label(state + name).Transition(kit.HoverMotion)
		kit.Hint(c, row, detail)
		if row.Hovered() && !added {
			row.Background(k.Fill)
		}
		row.Children(func() {
			kit.Icon(c, icon, 16).TextColor(k.Label2)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, name).FontSize(kit.FontBody).SingleLine().Ellipsis("…")
				ui.Text(c, detail).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…")
			})
			if added {
				kit.Icon(c, kit.SymCircleCheck, 15).TextColor(k.AccentText).Label("已添加")
			} else if trailing != "" {
				ui.Text(c, trailing).FontSize(kit.FontCaption).TextColor(k.Label3).Shrink(0)
			} else {
				kit.Icon(c, kit.SymPlus, 14).TextColor(k.Label3)
			}
		})
		if row.Clicked() && !added {
			add(item)
		}
	}
	kit.Dialog(c, &p.open, title, 560, func() {
		ui.Row(c).Gap(8).Shrink(0).Children(func() {
			tab := p.tab
			if kit.Segmented(c, "source", &tab, kit.Segment{Label: "已安装应用"}, kit.Segment{Label: "运行中进程"}, kit.Segment{Label: "自定义"}).Label("进程条件来源").Changed() {
				changeTab(tab)
			}
			ui.Spacer(c)
			if p.tab != pickCustom {
				kit.IconButton(c, kit.SymRotateCcw, "刷新选择列表", kit.IconButtonOptions{Disabled: p.loading}).OnClick(func() { w.loadPicker(p, true) })
			}
		})
		if p.tab == pickCustom {
			w.customProcess(c, p, selected, add)
			return
		}
		placeholder, label := "搜索应用名称或目录", "搜索已安装应用"
		if p.tab == pickProcesses {
			placeholder, label = "搜索进程名称、应用或路径", "搜索运行中进程"
		}
		kit.Search(c, "search", &p.query, kit.SearchOptions{Placeholder: placeholder}).Label(label)
		os := ""
		if p.apps != nil {
			os = p.apps.OS
		} else if p.snap != nil {
			os = p.snap.OS
		}
		host := map[string]string{"darwin": " macOS", "windows": " Windows", "linux": " Linux"}[os]
		caption := "选择具体进程，按完整路径匹配"
		if p.tab == pickApplications {
			caption = "选择整个应用及其目录内的进程"
		}
		ui.Text(c, "来自运行 Kiko 的"+host+" 主机 · "+caption).FontSize(kit.FontCaption).TextColor(k.Label3)
		search := strings.ToLower(strings.TrimSpace(p.query))
		ui.Column(c).MinHeight(240).Gap(2).Children(func() {
			switch {
			case p.loading:
				kit.EmptyState(c, "", "正在读取…", "", nil)
			case p.err != "":
				kit.EmptyState(c, kit.SymTriangleAlert, "读取失败", p.err, func() {
					kit.Button(c, "重试", kit.ButtonOptions{}).OnClick(func() { w.loadPicker(p, true) })
				})
			case p.tab == pickApplications && p.apps != nil && !p.apps.Supported:
				kit.EmptyState(c, kit.SymLaptop, "当前系统提供进程与自定义选择", "已安装应用选择优先支持 macOS。", func() {
					kit.Button(c, "选择运行中进程", kit.ButtonOptions{}).OnClick(func() { changeTab(pickProcesses) })
				})
			case p.tab == pickApplications:
				shown := 0
				if p.apps != nil {
					for _, app := range p.apps.Applications {
						if !strings.Contains(strings.ToLower(app.Name+" "+app.Path), search) {
							continue
						}
						shown++
						resultRow(model.ProcessSelection{Source: model.ProcessSourceApplication, Type: model.ProcessTypeRegex, Value: app.Path, Label: app.Name},
							kit.SymLaptop, app.Path, "")
					}
				}
				if shown == 0 {
					kit.EmptyState(c, "", "没有匹配的应用", "", func() {
						kit.Button(c, "自定义条件", kit.ButtonOptions{}).OnClick(func() { changeTab(pickCustom) })
					})
				}
				if p.apps != nil && len(p.apps.Warnings) > 0 {
					kit.Notice(c, kit.Neutral, "", strings.Join(p.apps.Warnings, "；"), nil)
				}
			default:
				groups := groupProcesses(p.snap, search)
				for _, g := range groups {
					ui.Column(c.Key(g.key)).Shrink(0).Children(func() {
						head := ui.ButtonBase(c).Height(28).Gap(6).FillWidth().Justify(ui.Start).Label(g.name)
						head.Children(func() {
							sym := kit.SymChevronDown
							if p.collapsed[g.key] {
								sym = kit.SymChevronRight
							}
							kit.Icon(c, sym, 12).TextColor(k.Label3)
							ui.Text(c, g.name).FontSize(kit.FontTable).FontWeight(600).TextColor(k.Label2)
							ui.Text(c, fmt.Sprint(len(g.items))).FontSize(10).TextColor(k.Label3)
						})
						if head.Clicked() {
							p.collapsed[g.key] = !p.collapsed[g.key]
						}
						if p.collapsed[g.key] {
							return
						}
						for _, it := range g.items {
							trailing := fmt.Sprintf("PID %d", it.process.PID)
							if it.count > 1 {
								trailing = fmt.Sprintf("%d 个实例", it.count)
							}
							resultRow(model.ProcessSelection{Source: model.ProcessSourceProcess, Type: model.ProcessTypePath, Value: it.process.Path, Label: it.process.Name},
								kit.SymCpu, it.process.Path, trailing)
						}
					})
				}
				if len(groups) == 0 {
					kit.EmptyState(c, "", "没有匹配的进程", "", func() {
						kit.Button(c, "自定义条件", kit.ButtonOptions{}).OnClick(func() { changeTab(pickCustom) })
					})
				}
			}
		})
	}, func() {
		ui.Text(c, fmt.Sprintf("已添加 %d 项", len(tags))).FontSize(kit.FontTable).TextColor(k.Label3).Grow(1)
		kit.Button(c, "完成", kit.ButtonOptions{Variant: kit.Primary}).OnClick(func() { p.open = false })
	})
}

// customProcess adds a process by name, path or path regex.
func (w *Workspace) customProcess(c *ui.Context, p *processPicker, selected func(model.ProcessSelection) bool, add func(model.ProcessSelection)) {
	k := kit.T(c)
	types := []string{model.ProcessTypeName, model.ProcessTypePath, model.ProcessTypeRegex}
	labels := []string{"进程名称", "可执行文件路径", "进程路径正则"}
	placeholders := []string{"例如 curl 或 msedge.exe", "例如 /usr/bin/curl", `例如 ^/Applications/.*\.app/`}
	hints := []string{"匹配同名进程，适合命令行工具。", "精确匹配这个可执行文件，进程重新启动后继续生效。", "使用 Go / RE2 正则表达式匹配可执行文件路径。"}
	if kit.Segmented(c, "custom-type", &p.custom, kit.Segment{Label: "名称"}, kit.Segment{Label: "路径"}, kit.Segment{Label: "正则"}).Label("自定义进程类型").Changed() {
		p.invalid = ""
	}
	submit := func() {
		value := strings.TrimSpace(p.drafts[p.custom])
		switch {
		case value == "":
			p.invalid = "请填写" + []string{"进程名称", "完整路径", "路径正则"}[p.custom]
			return
		case types[p.custom] == model.ProcessTypePath && !reAbsolutePath.MatchString(value):
			p.invalid = "请填写完整的可执行文件路径"
			return
		}
		item := model.ProcessSelection{Source: model.ProcessSourceCustom, Type: types[p.custom], Value: value}
		if selected(item) {
			p.invalid = "这个条件已添加"
			return
		}
		add(item)
		p.drafts[p.custom], p.invalid = "", ""
	}
	ui.Text(c, labels[p.custom]).FontSize(kit.FontTable).TextColor(k.Label2)
	ui.Row(c).Gap(8).Children(func() {
		var in ui.Element
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			in = kit.Input(c, fmt.Sprint("custom", p.custom), &p.drafts[p.custom], placeholders[p.custom]).Label(labels[p.custom])
		})
		if in.Changed() {
			p.invalid = ""
		}
		if in.Submitted() {
			submit()
		}
		label := "添加"
		if p.editing != nil {
			label = "更新"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary}).OnClick(submit)
	})
	ui.Text(c, hints[p.custom]).FontSize(kit.FontCaption).TextColor(k.Label3)
	kit.FormError(c, p.invalid)
}
