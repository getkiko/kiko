package settings

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/timefmt"
)

// library is the page of the shared network ("Shared Network Configs") or
// TLS ("Advanced TLS Configs") configs, and the editor of one config.
type library struct {
	w *Workspace

	mode   string
	search string

	draft      *model.NetworkConfig
	editorOpen bool
	saving     bool
	err        string
	remove     kit.ConfirmState
}

func newLibrary(w *Workspace) *library { return &library{w: w} }

func (l *library) show(mode string) {
	l.mode, l.search = mode, ""
	l.close()
}

func (l *library) title() string {
	if l.mode == pageTLS {
		return "高级 TLS 配置"
	}
	return "通用网络配置"
}

func (l *library) profiles() []*model.Profile {
	s := l.w.store
	return usageProfiles(s.Profiles(), s.ActiveProfileID(), s.Doc())
}

func (l *library) open(config *model.NetworkConfig) {
	if !l.editorOpen {
		l.w.store.BeginEdit()
	}
	l.err, l.saving = "", false
	l.remove = kit.ConfirmState{}
	l.draft = model.Clone(config)
	if l.draft.Fields == nil {
		l.draft.Fields = model.Fields{}
	}
	l.editorOpen = true
}

func (l *library) close() {
	if l.editorOpen {
		l.w.store.EndEdit()
	}
	l.editorOpen, l.draft = false, nil
	l.remove.Open = false
}

// create opens a new config of a kind.
func (l *library) create(kind string) {
	base := "新建网络配置"
	switch kind {
	case model.NetworkConfigTLSClient:
		base = "新建客户端 TLS 配置"
	case model.NetworkConfigTLSServer:
		base = "新建服务端 TLS 配置"
	}
	l.open(&model.NetworkConfig{ID: model.NewID("net"), Name: uniqueConfigName(base, l.w.store.NetworkConfigs()),
		Kind: kind, Fields: model.Fields{}, UpdatedAt: time.Now().UnixMilli()})
}

func (l *library) duplicate(c *model.NetworkConfig) {
	copied := model.Clone(c)
	copied.ID = model.NewID("net")
	copied.Name = uniqueConfigName(c.Name+" 副本", l.w.store.NetworkConfigs())
	l.open(copied)
}

// commit checks and saves a new library.
func (l *library) commit(next []*model.NetworkConfig) error {
	s := l.w.store
	if err := checkLibrary(next, s.NetworkConfigs(), l.profiles()); err != nil {
		return err
	}
	s.SetNetworkConfigs(next)
	return nil
}

func (l *library) save(valid bool) {
	if l.draft == nil || l.saving {
		return
	}
	draft := l.draft
	if problem := model.ValidateNetworkConfig(draft); problem != "" || !valid {
		if problem == "" {
			problem = "请修正配置格式"
		}
		l.err = problem
		return
	}
	name := strings.TrimSpace(draft.Name)
	if name == "" {
		l.err = "请填写配置名称"
		return
	}
	library := l.w.store.NetworkConfigs()
	for _, c := range library {
		if c.ID != draft.ID && c.Name == name {
			l.err = "已存在同名配置"
			return
		}
	}
	next := *draft
	next.Name, next.Fields, next.UpdatedAt = name, cleanFields(draft.Fields), time.Now().UnixMilli()
	updated := make([]*model.NetworkConfig, 0, len(library)+1)
	replaced := false
	for _, c := range library {
		if c.ID == next.ID {
			updated = append(updated, &next)
			replaced = true
		} else {
			updated = append(updated, c)
		}
	}
	if !replaced {
		updated = append(updated, &next)
	}
	l.err = ""
	if err := l.commit(updated); err != nil {
		l.err = err.Error()
		return
	}
	l.close()
}

func (l *library) uses() []model.NetworkUse {
	if l.draft == nil {
		return nil
	}
	return model.NetworkUses(l.profiles(), l.draft.ID)
}

func (l *library) delete(done func(error)) {
	uses := l.uses()
	if len(uses) > 0 {
		l.err = fmt.Sprintf("此配置有 %d 处引用，请先更换引用", len(uses))
		done(fmt.Errorf("%s", l.err))
		return
	}
	var next []*model.NetworkConfig
	for _, c := range l.w.store.NetworkConfigs() {
		if c.ID != l.draft.ID {
			next = append(next, c)
		}
	}
	if next == nil {
		next = []*model.NetworkConfig{}
	}
	if err := l.commit(next); err != nil {
		done(fmt.Errorf("删除失败，请重试"))
		return
	}
	done(nil)
	l.close()
}

// view is the page: under the back row and the title, the configs as
// rows of a settings group (TLS: one group of client and one of server
// configs), each opening the editor, with the group's New row last.
func (l *library) view(c *ui.Context) {
	profiles := l.profiles()
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backRow(c, l.w)
			kit.PaneTitle(c, l.title(), "", nil)
			if l.mode == pageTLS {
				l.group(c, profiles, "客户端", model.NetworkConfigTLSClient, true)
				l.group(c, profiles, "服务端", model.NetworkConfigTLSServer, false)
			} else {
				l.group(c, profiles, "配置", model.NetworkConfigNetwork, true)
			}
		})
	})
	l.editor(c)
}

// matches reports whether a config's name or parameters contain the
// search.
func (l *library) matches(cfg *model.NetworkConfig) bool {
	query := strings.ToLower(strings.TrimSpace(l.search))
	return query == "" || strings.Contains(strings.ToLower(cfg.Name+" "+model.NetworkSummary(cfg)), query)
}

// group is the settings group of the configs of a kind, under its title
// with the count and, on the page's first group, the search.
func (l *library) group(c *ui.Context, profiles []*model.Profile, title, kind string, search bool) {
	k := kit.T(c)
	add := "新建配置"
	switch kind {
	case model.NetworkConfigTLSClient:
		add = "新建客户端配置"
	case model.NetworkConfigTLSServer:
		add = "新建服务端配置"
	}
	var all, shown []*model.NetworkConfig
	for _, cfg := range l.w.store.NetworkConfigs() {
		if model.ConfigKind(cfg) == kind {
			all = append(all, cfg)
			if l.matches(cfg) {
				shown = append(shown, cfg)
			}
		}
	}
	count := fmt.Sprintf("%d 个", len(all))
	if strings.TrimSpace(l.search) != "" {
		count = fmt.Sprintf("%d / %d 个", len(shown), len(all))
	}
	ui.Column(c.Key("group-" + kind)).Shrink(0).Children(func() {
		groupHeading(c, title, count, func() {
			if search {
				kit.Search(c, "search", &l.search, kit.SearchOptions{Placeholder: "搜索名称与参数", Width: 220}).Label("搜索配置")
			}
		})
		kit.SettingsGroup(c, "", "", func() {
			for _, cfg := range shown {
				ui.Column(c.Key(cfg.ID)).Children(func() { l.row(c, profiles, cfg) })
			}
			if len(shown) == 0 && len(all) > 0 {
				ui.Row(c.Key("none")).MinHeight(kit.SettingsRowH).Gap(12).Children(func() {
					ui.Text(c, "没有匹配的配置").FontSize(kit.FontBody).TextColor(k.Label3).Grow(1)
					kit.Button(c, "清除搜索", kit.ButtonOptions{Small: true}).OnClick(func() { l.search = "" })
				})
			}
			ui.Row(c.Key("add-" + kind)).MinHeight(44).Shrink(0).Children(func() {
				kit.Button(c, add, kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).
					OnClick(func() { l.create(kind) })
			})
		})
	})
}

// groupHeading is a settings group's title row with its count, and the
// controls on the right (a search that narrows as the page does).
func groupHeading(c *ui.Context, title, count string, controls func()) {
	k := kit.T(c)
	ui.Row(c).MinHeight(kit.SegmentHeight).Gap(8).Margin(0, 14, 9).Shrink(0).Children(func() {
		ui.Text(c, title).FontSize(kit.FontBody).FontWeight(600).SingleLine().Shrink(0)
		ui.Text(c, count).FontSize(kit.FontCaption).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Shrink(0)
		ui.Spacer(c)
		controls()
	})
}

// row is a config: its name and parameters, the references and the
// last change, opening the editor; the copy button opens a copy.
func (l *library) row(c *ui.Context, profiles []*model.Profile, cfg *model.NetworkConfig) {
	k := kit.T(c)
	ui.Row(c).Gap(10).Children(func() {
		b := ui.ButtonBase(c).Grow(1).MinWidth(0).MinHeight(kit.SettingsRowH).PaddingY(10).Gap(12).
			Justify(ui.Start).Label("编辑 " + cfg.Name).Transition(kit.HoverMotion)
		if b.Hovered() {
			b.Opacity(0.85)
		}
		b.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, cfg.Name).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0)
				ui.Text(c, model.NetworkSummary(cfg)).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Ellipsis("…").MinWidth(0)
			})
			ui.Text(c, fmt.Sprintf("%d 处引用", len(model.NetworkUses(profiles, cfg.ID)))).
				FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Shrink(0)
			ui.Text(c, timefmt.FormatMilli(cfg.UpdatedAt, "01/02 15:04")).
				FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").SingleLine().Shrink(0)
		})
		if b.Clicked() {
			l.open(cfg)
		}
		kit.IconButton(c, kit.SymCopy, "复制 "+cfg.Name, kit.IconButtonOptions{Size: kit.SmallControl, IconSize: 13}).
			OnClick(func() { l.duplicate(cfg) })
	})
}

// editor is the dialog editing the draft config.
func (l *library) editor(c *ui.Context) {
	if l.draft == nil {
		return
	}
	k := kit.T(c)
	open := l.editorOpen
	kind := model.ConfigKind(l.draft)
	all := model.NetworkSchemaForKind(kind)
	sections := networkSections
	if kind != model.NetworkConfigNetwork {
		sections = tlsSections(all)
	}
	nameLabel := "网络配置名称"
	if l.mode == pageTLS {
		nameLabel = "TLS 配置名称"
	}
	valid := true
	uses := l.uses()
	existing := findConfigByID(l.w.store.NetworkConfigs(), l.draft.ID) != nil
	kit.Dialog(c, &open, l.title(), 560, func() {
		ui.Column(c).Gap(5).Shrink(0).Children(func() {
			ui.Text(c, "配置名称").FontSize(kit.FontTable).TextColor(k.Label2)
			kit.Input(c, "name", &l.draft.Name, "").Label(nameLabel)
		})
		for _, section := range sections {
			fields := editorFields(all, section.keys)
			if len(fields) == 0 {
				continue
			}
			ui.Column(c.Key(section.name)).Gap(8).Shrink(0).Children(func() {
				ui.Text(c, section.name).FontSize(kit.FontSection).FontWeight(600).Margin(6, 0, 0)
				result := forms.Form(c, "form", fields, l.draft.Fields, forms.Context{
					Doc: l.w.store.Doc(), NetworkConfigs: l.w.store.NetworkConfigs(), EditNetworkConfig: true,
				})
				valid = valid && result.Valid
			})
		}
		ui.Column(c).Gap(4).Padding(10, 12).Radius(12).Background(k.Surface2).Shrink(0).Children(func() {
			ui.Text(c, fmt.Sprintf("使用位置 · %d", len(uses))).FontSize(kit.FontBody).FontWeight(600)
			for _, u := range uses {
				ui.Text(c.Key(u.ProfileID+u.EntityID+u.Label), u.Label).FontSize(kit.FontTable).TextColor(k.Label2).Wrap()
			}
			ui.Text(c, "平台限定参数按目标平台生效；每个项目使用其协议支持的字段。").FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		})
	}, func() {
		if l.err != "" {
			ui.Text(c, l.err).FontSize(kit.FontTable).TextColor(k.Danger).Grow(1).MinWidth(0).Wrap()
		}
		if existing {
			del := kit.Button(c, "删除", kit.ButtonOptions{Variant: kit.Danger, Disabled: l.saving})
			if del.Clicked() {
				if len(uses) > 0 {
					l.err = fmt.Sprintf("此配置有 %d 处引用，请先更换引用", len(uses))
				} else {
					l.err = ""
					l.remove.Open = true
				}
			}
			kit.ConfirmPopover(c, del, &l.remove, kit.ConfirmOptions{Title: "删除「" + l.draft.Name + "」？",
				Message: "此配置将从配置库移除。", Confirm: "删除配置", Danger: true, Run: l.delete})
		}
		if l.err == "" {
			ui.Spacer(c)
		}
		kit.Button(c, "取消", kit.ButtonOptions{Disabled: l.saving}).OnClick(l.close)
		label := "保存配置"
		if l.saving {
			label = "正在保存…"
		}
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.Primary, Disabled: l.saving}).OnClick(func() { l.save(valid) })
	})
	if !open && l.editorOpen && !l.saving {
		l.close()
	}
}
