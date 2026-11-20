package subscriptions

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
	"github.com/getkiko/kiko/app/mygo/internal/subscription"
)

// builtinTypes and groupTypes are the outbound types that are not nodes.
var (
	builtinTypes = []string{"direct", "block"}
	groupTypes   = model.GroupTypes
)

// manualNodeTypes are the node types the add menu offers: all but the
// built-in, group and WireGuard outbounds (an endpoint since sing-box 1.13).
func manualNodeTypes() []string {
	var out []string
	for _, t := range model.OutboundTypes {
		if !slices.Contains(builtinTypes, t) && !slices.Contains(groupTypes, t) && t != "wireguard" {
			out = append(out, t)
		}
	}
	return out
}

// outboundAddLabel is a type's name in the add menu: the first word of
// its label.
func outboundAddLabel(t string) string {
	if f := strings.Fields(typeLabel(t)); len(f) > 0 {
		return f[0]
	}
	return t
}

// addManual is the ＋ card opening the outbounds' type picker
// (outboundsSection.add).
const addManual = "manual"

// outboundsSection is the Outbounds section: the Selector and URLTest groups'
// cards (grouprow.go) with their member sheet (groupsheet.go), then the
// manual outbounds' cards, with the add menu and clipboard import;
// "Test all" is the page toolbar's.
type outboundsSection struct {
	w      *Workspace
	editor outEditor
	// add is the type picker of "Add outbound", at the heading's button or the
	// manual outbounds' ＋ card (addManual).
	add kit.MenuAnchor
	// remove is the confirmation of a card's Delete.
	remove cardConfirm
	// regionAt is "Auto-create by region" as last built.
	regionAt ui.Element
	// regionGroups confirms "Auto-create by region".
	regionGroups kit.ConfirmState
	// sheet is the side sheet of a group's members.
	sheet groupSheet
}

func (s *outboundsSection) init(w *Workspace) {
	s.w = w
	s.editor.w = w
	s.sheet.s = s
}

// items are the outbounds list's outbounds, in its order: all of them,
// the Selector and URLTest groups, and the others.
func (s *outboundsSection) items(doc *model.Doc) (all []model.TopLevelItem, groups []*model.Outbound, manual []model.TopLevelItem) {
	for _, item := range model.TopLevelOutboundItems(doc) {
		o := item.Outbound
		switch {
		case o == nil:
			continue
		case model.IsGroup(o.Type):
			groups = append(groups, o)
		default:
			manual = append(manual, item)
		}
		all = append(all, item)
	}
	return all, groups, manual
}

// heading is the section's heading: its count, "Import from clipboard"
// and "Auto-create by region", whose picker and confirmation overlays build.
func (s *outboundsSection) heading(c *ui.Context, doc *model.Doc, count int) {
	kit.SectionHeading(c, "出站", fmt.Sprintf("%d 个", count), func() {
		kit.ToolbarGroup(c, func() {
			kit.IconButton(c, kit.SymClipboardPaste, "从剪贴板导入", kit.IconButtonOptions{IconSize: 17}).
				OnClick(func() { s.importClipboard(c.ReadClipboard()) })
			s.regionAt = kit.IconButton(c, kit.SymGlobe, "按地区自动创建", kit.IconButtonOptions{IconSize: 17, Active: s.regionGroups.Open})
			if s.regionAt.Clicked() {
				if len(regionSummaries(doc)) == 0 {
					s.w.env.Toast("没有识别出地区的节点")
				} else {
					s.regionGroups = kit.ConfirmState{Open: true}
				}
			}
		})
	})
}

// grid is the cards of the manual outbounds, reordered by dragging, then
// "+ Add outbound", opening the type picker as the heading's "Add
// outbound" does.
func (s *outboundsSection) grid(c *ui.Context, doc *model.Doc, items []model.TopLevelItem) {
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.Key()
	}
	regions := s.w.infoOf(doc).matcher
	reorderGrid(c, "outbounds-other", keys, s.w.reorderTopLevel, func(i int) { s.card(c, doc, regions, items[i].Outbound) }, func() {
		s.add.Card(addManual, kit.AddCard(c.Key("add-outbound"), "添加出站"))
	})
}

// overlays are the type picker of "Add outbound", the confirmations of
// "Auto-create by region" and of a card's Delete, at their
// buttons when the list built them in this pass, the member sheet and
// the editor.
func (s *outboundsSection) overlays(c *ui.Context, doc *model.Doc) {
	if anchor, _, ok := s.add.At(); ok {
		groups := []pickerGroup{
			{label: "分组", items: []pickerItem{{id: model.ModeManual, label: "策略组", search: "selector urltest fallback loadbalance smart 手动 最快 故障转移 负载均衡 智能", icon: kit.SymLayers3}}},
		}
		{
			groups = append(groups, pickerGroup{label: "基础", items: []pickerItem{{id: "direct", label: "Direct", search: "direct 直连", icon: kit.SymLayers3}}})
			var nodes []pickerItem
			for _, t := range manualNodeTypes() {
				nodes = append(nodes, pickerItem{id: t, label: outboundAddLabel(t), search: t + " " + typeLabel(t), icon: kit.SymServer})
			}
			groups = append(groups, pickerGroup{label: "节点", items: nodes})
		}
		if t := typePicker(c, anchor, &s.add, "搜索出站类型", 248, groups); t != "" {
			s.editor.openAdd(t)
		}
	}
	if s.regionGroups.Open && s.regionAt.Valid() {
		s.regionConfirm(c, doc)
	}
	if at, ok := s.remove.anchor(); ok {
		id, title := s.remove.id, s.remove.title
		kit.ConfirmPopover(c, at, &s.remove.state, kit.ConfirmOptions{
			Title: "删除出站？", Message: "「" + title + "」将从出站列表中删除。", Confirm: "删除", Danger: true,
			Run: func(done func(error)) {
				done(s.w.store.Update(func(d *model.Doc) { removeOutbound(d, id) }))
			},
		})
	}
	s.sheet.view(c)
	s.editor.view(c)
}

// regionSummaries are the regions "Auto-create by region" would make a group of,
// each with its count of nodes.
func regionSummaries(doc *model.Doc) []string {
	var regions []string
	for _, g := range model.GroupByRegion(doc, model.FilterNodes(doc), false) {
		if g.Region.ID != "" {
			regions = append(regions, fmt.Sprintf("%s %d", model.RegionGroupName(g.Region), len(g.Nodes)))
		}
	}
	return regions
}

// regionConfirm confirms "Auto-create by region": a URLTest group per
// region that has nodes, filtered by that region, and a Selector "Node
// selection" holding them
// (model.AddRegionGroups), listing them.
func (s *outboundsSection) regionConfirm(c *ui.Context, doc *model.Doc) {
	regions := regionSummaries(doc)
	kit.ConfirmPopover(c, s.regionAt, &s.regionGroups, kit.ConfirmOptions{
		Title: "按地区自动创建分组？",
		Message: fmt.Sprintf("将为 %d 个地区各创建一个 URLTest 分组（%s），并创建 Selector「节点选择」包含它们。",
			len(regions), strings.Join(regions, "、")),
		Confirm: "创建",
		Run: func(done func(error)) {
			created := 0
			err := s.w.store.Update(func(d *model.Doc) { created = len(model.AddRegionGroups(d)) })
			done(err)
			if err == nil && created > 0 {
				s.w.env.Toast(fmt.Sprintf("已创建 %d 个分组", created))
			}
		},
	})
}

// importClipboard adds the outbounds of share links on the clipboard:
// one opens in the editor to confirm, several are added at once.
func (s *outboundsSection) importClipboard(text string) {
	if strings.TrimSpace(text) == "" {
		s.w.env.Toast("剪贴板没有可导入的链接")
		return
	}
	result := subscription.ParseSubscription(text)
	if len(result.Nodes) == 0 {
		s.w.env.Toast("未识别到支持的出站链接")
		return
	}
	imported := importedOutbounds(s.w.store.Doc(), result.Nodes)
	if len(imported) == 1 {
		s.editor.openDraft(imported[0])
		s.w.env.Toast("已识别链接，请确认后保存")
		return
	}
	if err := s.w.store.Update(func(doc *model.Doc) { doc.Outbounds = append(doc.Outbounds, imported...) }); err != nil {
		s.w.env.Toast(err.Error())
		return
	}
	msg := fmt.Sprintf("已导入 %d 个出站", len(imported))
	if result.Skipped > 0 {
		msg += fmt.Sprintf("，跳过 %d 个", result.Skipped)
	}
	s.w.env.Toast(msg)
}

// copyLink puts an outbound's share link on the clipboard.
func (s *outboundsSection) copyLink(c *ui.Context, o *model.Outbound) {
	link, ok := subscription.FormatOutboundLink(o)
	if !ok {
		s.w.env.Toast("当前出站缺少生成链接所需的字段")
		return
	}
	c.WriteClipboard(link)
	s.w.env.Toast("分享链接已复制")
}

// card is a manual outbound's hero card, opening its editor: a proxy's
// flag (or initial), name and protocol over its server, its port,
// region and last latency, which a click tests again; direct and block
// what they do and how many routes use them. Its menu, the more button's
// and the context menu, tests a proxy, copies its share link and deletes
// it after a confirmation at the more button (heroCard).
func (s *outboundsSection) card(c *ui.Context, doc *model.Doc, regions *model.RegionMatcher, o *model.Outbound) {
	id := o.ID
	invalid := model.HasInvalidOutboundReferences(doc, o, s.w.store.NetworkConfigs())
	title := o.Tag
	if title == "" {
		title = typeLabel(o.Type)
	}
	opts := s.cardOptions(c, o, title)
	if o.Type == "direct" || o.Type == "block" {
		initial, what := "直", "本机网络"
		if o.Type == "block" {
			initial, what = "拒", "丢弃连接"
		}
		uses := model.OutboundUseCount(doc, id)
		opts.Header = func() {
			heroHeader(c, func() { kit.HeroInitial(c, initial) }, title, invalid, strings.ToUpper(o.Type))
		}
		opts.Main = func() { kit.HeroValue(c, what).FontSize(18) }
		opts.Footer = func() {
			ui.Text(c, fmt.Sprintf("被 %d 处使用", uses)).FontFeatures("tnum").SingleLine().Ellipsis("…").
				Shrink(1).MinWidth(0)
		}
	} else {
		region, found := regions.RegionOf(o.Tag)
		server, port := jsv.StringOr(o.Fields["server"], ""), jsv.StringOr(o.Fields["server_port"], "")
		if o.Type == "burrow" {
			server, port = jsv.StringOr(o.Fields["signal_address"], ""), ""
		}
		testable := server != "" && o.Type != "burrow"
		if server == "" {
			server = "—"
		}
		var note []string
		if port != "" {
			note = append(note, ":"+port)
		}
		if name := model.RegionName(region); found && name != "" {
			note = append(note, name)
		}
		flag := ""
		if found {
			flag = region.Flag
		}
		name := nodeview.BesideFlag(flag, title)
		lead := func() {
			if flag != "" {
				ui.Text(c, flag).FontSize(15).SingleLine().Shrink(0)
			} else {
				kit.HeroInitial(c, nodeview.GroupInitial(title))
			}
		}
		opts.Header = func() { heroHeader(c, lead, name, invalid, nodeview.ProtocolTag(o.Type)) }
		opts.Main = func() { kit.HeroMono(c, server) }
		opts.Footer = func() {
			ui.Text(c, strings.Join(note, " · ")).FontFeatures("tnum").SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			ui.Spacer(c)
			if testable {
				s.latencyButton(c, id, title)
			}
		}
		if testable {
			opts.Menu = append(opts.Menu, kit.EntityMenuItem{Label: "测速", Icon: kit.SymGauge, Disabled: s.w.store.Testing(id),
				Run: func(ui.Element) { s.w.measureLatency([]string{id}) }})
		}
	}
	s.heroCard(c, o, title, opts)
}

// cardOptions are what the cards of outbound o share: they open its
// editor, turn it on or off, keep the more button shown while its Delete
// confirms, and list the more menu in their context menu too.
func (s *outboundsSection) cardOptions(c *ui.Context, o *model.Outbound, title string) kit.HeroCardOptions {
	id := o.ID
	return kit.HeroCardOptions{
		Label:           "编辑 " + title,
		Muted:           !o.Enabled,
		Enabled:         o.Enabled,
		OnEnabledChange: func(enabled bool) { s.setEnabled(id, enabled) },
		OnOpen:          func() { s.editor.openEdit(s.w.store.Doc(), id) },
		KeepMore:        s.remove.showing(id),
		ContextMenu:     true,
	}
}

// heroCard is outbound o's card, its menu ending in "Copy link", when o
// has a share link, and Delete, confirmed at the more button.
func (s *outboundsSection) heroCard(c *ui.Context, o *model.Outbound, title string, opts kit.HeroCardOptions) {
	id := o.ID
	opts.Menu = append(opts.Menu, s.browserItems(o)...)
	if _, ok := subscription.FormatOutboundLink(o); ok {
		opts.Menu = append(opts.Menu, kit.EntityMenuItem{Label: "复制链接", Icon: kit.SymCopy, Run: func(ui.Element) { s.copyLink(c, o) }})
	}
	opts.Menu = append(opts.Menu, kit.EntityMenuItem{Label: "删除", Icon: kit.SymTrash2, Danger: true, Run: func(ui.Element) {
		s.remove.start(id, "")
	}})
	s.remove.track(id, title, kit.HeroCard(c, id, opts))
}

// latencyButton is a proxy card's latency, testing outbound id again
// as clicked, "Test" before its first test; it takes the click from the
// card around it and shows the turning spinner (nodeview.TestingMark)
// while a test runs.
func (s *outboundsSection) latencyButton(c *ui.Context, id, title string) {
	k := kit.T(c)
	running := s.w.store.Testing(id)
	result := s.w.store.Latency[id]
	text, fg, bg, ok := nodeview.LatencyTag(k, result)
	if running {
		// The card takes no click on the spinner.
		ui.Row(c).Height(19).AlignItems(ui.Center).Shrink(0).Children(func() {
			nodeview.TestingMark(c)
			kit.BusyCover(c, "测速 "+title)
		})
		return
	}
	if !ok {
		if heroLink(c, "测速", "测速 "+title, false).Clicked() {
			s.w.measureLatency([]string{id})
		}
		return
	}
	b := ui.ButtonBase(c).Height(19).Radius(6).AlignItems(ui.Center).Shrink(0).Label("测速 " + title).
		Transition(kit.HoverMotion)
	if b.Pressed() {
		b.Opacity(0.6)
	}
	b.Children(func() { kit.HeroLatency(c, text, fg, bg) })
	kit.Hint(c, b, nodeview.LatencyHint(result))
	if b.Clicked() {
		s.w.measureLatency([]string{id})
	}
}

// setEnabled turns outbound id on or off.
func (s *outboundsSection) setEnabled(id string, enabled bool) {
	s.w.update(func(d *model.Doc) {
		for _, x := range d.Outbounds {
			if x.ID == id {
				x.Enabled = enabled
			}
		}
	})
}
