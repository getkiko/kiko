package settings

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// regionsPage is Settings › Region Detection: the document's region table (name,
// flag and the pattern over node names, model.Regions) and the patterns
// of subscription info nodes, each with its match count over the current
// nodes. Rows edit a draft taken as the page opens; a valid change saves
// at once, an invalid pattern shows its error and waits.
type regionsPage struct {
	w     *Workspace
	draft *model.RegionSettings
	// remove confirms deleting removeID (a region or an info pattern).
	remove   kit.ConfirmState
	removeID string
	restore  kit.ConfirmState
}

func (p *regionsPage) open() {
	p.draft = model.Clone(model.Regions(p.w.store.Doc()))
	p.remove, p.restore, p.removeID = kit.ConfirmState{}, kit.ConfirmState{}, ""
}

// save stores the draft when every pattern compiles.
func (p *regionsPage) save() {
	for _, list := range [][]model.Region{p.draft.Regions, p.draft.Info} {
		for _, r := range list {
			if _, err := model.CompileNodePattern(r.Pattern); err != nil {
				return
			}
		}
	}
	saved := model.Clone(p.draft)
	p.w.update(func(d *model.Doc) { d.NodeRegions = saved })
}

// regionsSummary describes the row opening the page.
func regionsSummary(doc *model.Doc) string {
	return fmt.Sprintf("按节点名称识别地区，用于分组筛选与策略组选择 · %d 个地区", len(model.Regions(doc).Regions))
}

func (p *regionsPage) view(c *ui.Context) {
	if p.draft == nil {
		p.open()
	}
	doc := p.w.store.Doc()
	nodes := model.FilterNodes(doc)
	matcher := model.NewRegionMatcher(p.draft)
	recognized := 0
	for _, n := range nodes {
		if _, ok := matcher.RegionOf(n.Tag); ok {
			recognized++
		}
	}
	kit.Workspace(c, func() {
		kit.PreferencesForm(c, func() {
			backRow(c, p.w)
			kit.PaneTitle(c, "地区识别", "按节点名称中的旗帜、地名或缩写识别地区。分组的按地区筛选、按地区自动创建和概览的策略组选择共用此表。名称含旗帜时按旗帜归入同旗帜的地区或旗帜所属国家，否则按顺序取第一个匹配的地区。", func() {
				restore := kit.Button(c, "恢复默认", kit.ButtonOptions{Small: true, Icon: kit.SymRotateCcw})
				if restore.Clicked() {
					p.restore.Open = true
				}
				kit.ConfirmPopover(c, restore, &p.restore, kit.ConfirmOptions{
					Title: "恢复默认地区识别？", Message: "地区与信息节点规则将恢复为默认列表，自定义的修改会丢失。", Confirm: "恢复", Danger: true,
					Run: func(done func(error)) {
						err := p.w.store.Update(func(d *model.Doc) { d.NodeRegions = nil })
						if err == nil {
							p.open()
						}
						done(err)
					},
				})
			})
			footer := fmt.Sprintf("已识别 %d / %d 个节点；正则区分大小写，可用 (?i) 忽略大小写，(?<![a-z]) 等 FlClash 写法会自动转换。", recognized, len(nodes))
			kit.SettingsGroup(c, "地区", footer, func() {
				for i := range p.draft.Regions {
					p.row(c, nodes, &p.draft.Regions[i], true)
				}
				p.addRow(c, "添加地区", func() {
					p.draft.Regions = append(p.draft.Regions, model.Region{ID: model.NewID("region"), Label: "新地区"})
				})
			})
			kit.SettingsGroup(c, "信息节点", "订阅中展示剩余流量、到期时间或公告的节点；分组筛选默认排除它们。", func() {
				for i := range p.draft.Info {
					p.row(c, nodes, &p.draft.Info[i], false)
				}
				p.addRow(c, "添加规则", func() {
					p.draft.Info = append(p.draft.Info, model.Region{ID: model.NewID("info"), Label: "新规则"})
				})
			})
		})
	})
}

// row edits one region (with a flag) or info pattern.
func (p *regionsPage) row(c *ui.Context, nodes []*model.Outbound, r *model.Region, region bool) {
	k := kit.T(c)
	re, err := model.CompileNodePattern(r.Pattern)
	// A region counts the nodes it is the region of, flags deciding
	// first; an info rule the names its pattern matches.
	count := 0
	matcher := model.NewRegionMatcher(p.draft)
	for _, n := range nodes {
		switch {
		case region:
			if got, ok := matcher.RegionOf(n.Tag); ok && got.ID == r.ID {
				count++
			}
		case re != nil && re.MatchString(n.Tag):
			count++
		}
	}
	id := r.ID
	ui.Column(c.Key("region-" + id)).PaddingY(10).Gap(6).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			if region {
				ui.Column(c).Width(56).Shrink(0).Children(func() {
					if kit.Input(c, "flag", &r.Flag, "旗帜").Label("旗帜").Changed() {
						p.save()
					}
				})
			}
			ui.Column(c).Width(140).Shrink(0).Children(func() {
				if kit.Input(c, "label", &r.Label, "名称").Label("名称").Changed() {
					p.save()
				}
			})
			text := fmt.Sprintf("匹配 %d 个", count)
			if err != nil {
				text = "正则无效"
			}
			ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label3).FontFeatures("tnum").Grow(1).TextAlign(ui.End)
			del := kit.IconButton(c, kit.SymTrash2, "删除 "+r.Label, kit.IconButtonOptions{Size: kit.SmallControl, Danger: true})
			if del.Clicked() {
				p.removeID, p.remove = id, kit.ConfirmState{Open: true}
			}
			if p.removeID == id {
				kit.ConfirmPopover(c, del, &p.remove, kit.ConfirmOptions{
					Title: "删除「" + r.Label + "」？", Message: "使用它的分组筛选将不再匹配这一项。", Confirm: "删除", Danger: true,
					Run: func(done func(error)) {
						drop := func(list []model.Region) []model.Region {
							return slices.DeleteFunc(slices.Clone(list), func(x model.Region) bool { return x.ID == id })
						}
						p.draft.Regions, p.draft.Info = drop(p.draft.Regions), drop(p.draft.Info)
						p.save()
						done(nil)
					},
				})
			}
		})
		if kit.Input(c, "pattern", &r.Pattern, "正则表达式").Label(r.Label + " 正则").Font(kit.MonoFont).Changed() {
			p.save()
		}
		if err != nil {
			kit.FormError(c, err.Error())
		}
	})
}

func (p *regionsPage) addRow(c *ui.Context, label string, add func()) {
	ui.Row(c).MinHeight(44).Shrink(0).Children(func() {
		kit.Button(c, label, kit.ButtonOptions{Variant: kit.TextButton, Small: true, Icon: kit.SymPlus}).OnClick(func() {
			add()
			p.save()
		})
	})
}
