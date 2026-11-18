package subscriptions

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/forms"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// The group editor's measures, in DIPs.
const (
	groupDialogW = 900
	groupDialogH = 680
	// groupNarrow is the window width below which the editor shows one
	// column at a time, switched by Sources / Final members.
	groupNarrow  = 760
	sourcesWidth = 380
)

// groupUI is the group editor's state apart from the draft: the
// popovers, the tag inputs' text, the final members' search and folds,
// and what it derives from the document, cached.
type groupUI struct {
	id string
	// opening are the members as the editor opened, the picker's order.
	opening []string

	regionMenu  bool
	regionQuery string

	nodeMenu  bool
	nodeQuery string
	// nodeToggled are the picker's sources folded other than by default;
	// nodeOpen, those open by default, taken as the picker opened.
	nodeToggled map[string]bool
	nodeOpen    map[string]bool

	keyword, excludeKeyword string
	patternMenu             bool

	query string
	// folded are the final members' groups folded; list is their list.
	folded map[string]bool
	list   ui.ListState
	// nodeList and picker are the node picker's list and its sources.
	nodeList ui.ListState
	picker   pickerCache
	// tab is the column shown in a narrow window: 0 Sources, 1 Final members.
	tab int

	index nodeIndex
	final finalCache
}

func (g *groupUI) reset(id string, members []string) {
	*g = groupUI{id: id, opening: slices.Clone(members), folded: map[string]bool{}, nodeToggled: map[string]bool{}}
}

// healthCheckKeys are the health check's fields, after a mode's own.
var healthCheckKeys = map[string]bool{"url": true, "interval": true, "timeout": true}

// groupFields are a mode's fields as the options section shows them,
// flat: the mode's own basic fields (default, tolerance, strategy), the health
// check, then the advanced ones.
func groupFields(typ string) []schema.Field {
	var own, check, adv []schema.Field
	for _, field := range schema.Fields(schema.KindOutbound, typ) {
		advanced := field.Adv
		field.Adv = false
		switch {
		case healthCheckKeys[field.Key]:
			check = append(check, field)
		case advanced:
			adv = append(adv, field)
		default:
			own = append(own, field)
		}
	}
	return append(append(own, check...), adv...)
}

// isGroupField reports whether key is a field of the mode's options.
func isGroupField(typ, key string) bool {
	return slices.ContainsFunc(groupFields(typ), func(f schema.Field) bool { return f.Key == key })
}

// groupModes are the modes the editor offers: all of them unless the
// server is known not to run the newer ones.
func (e *outEditor) groupModes() []string {
	if server := e.w.env.Server; server != nil && server.Info != nil && !server.Info.Capabilities["groupModes"] {
		return model.LegacyGroupModes
	}
	return model.GroupModes
}

// modeControl is the group's "Selection mode", a segment per mode filling the
// row; choosing one switches the draft's mode, keeping its members and
// shared fields.
func (e *outEditor) modeControl(c *ui.Context, o *model.Outbound) {
	modes := e.groupModes()
	if !slices.Contains(modes, o.Type) {
		modes = append(slices.Clone(modes), o.Type)
	}
	selected := slices.Index(modes, o.Type)
	segments := make([]kit.Segment, len(modes))
	for i, mode := range modes {
		segments[i] = kit.Segment{Label: model.GroupModeLabel(mode)}
	}
	if kit.SegmentedFill(c, "group-mode", &selected, segments...).Label("选择方式").Changed() {
		model.SwitchGroupMode(o, modes[selected])
	}
}

// groupView is the policy group editor: the header (title, name, mode,
// close), Sources on the left and Final members on the right, each scrolling on
// its own, and the footer. A narrow window shows one column at a time.
func (e *outEditor) groupView(c *ui.Context, doc *model.Doc, o *model.Outbound) {
	k := kit.T(c)
	title := "编辑策略组"
	if e.isNew {
		title = "添加策略组"
	}
	width, _ := c.Size()
	narrow := width < groupNarrow
	final := e.finalMembers(doc, o)
	// The options' form reports again as it builds; a narrow window
	// showing Final members does not build it.
	e.form = forms.Result{Valid: true}
	open := true
	kit.DialogFrame(c, &open, title, groupDialogW, groupDialogH, func() {
		e.groupHeader(c, o, title, narrow, &open)
		if narrow {
			ui.Row(c).Padding(10, 16).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft).Children(func() {
				kit.SegmentedFill(c, "group-tab", &e.group.tab,
					kit.Segment{Label: "来源"}, kit.Segment{Label: "最终成员", Count: fmt.Sprint(final.total)})
			})
			ui.Column(c).Grow(1).MinHeight(0).Children(func() {
				if e.group.tab == 0 {
					e.sourcesColumn(c, doc, o, true)
				} else {
					e.finalColumn(c, doc, o, final)
				}
			})
		} else {
			ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c.Key("sources")).Width(sourcesWidth).Shrink(0).Background(k.Surface2).
					BorderWidth(0, 1, 0, 0).BorderColor(k.HairlineSoft).Children(func() {
					e.sourcesColumn(c, doc, o, false)
				})
				ui.Column(c.Key("final")).Grow(1).MinWidth(0).Children(func() {
					e.finalColumn(c, doc, o, final)
				})
			})
		}
		e.groupFooter(c, o)
	})
	if !open {
		e.close()
	}
}

// groupHeader is the editor's top row: the title, the name, the mode
// filling the rest, the close button. Narrow, the name and the mode take
// rows of their own.
func (e *outEditor) groupHeader(c *ui.Context, o *model.Outbound, title string, narrow bool, open *bool) {
	k := kit.T(c)
	name := func(width float32) {
		box := ui.Column(c).Shrink(0)
		if width > 0 {
			box.Width(width)
		} else {
			box.Grow(1).MinWidth(0)
		}
		box.Children(func() { kit.Input(c, "name", &o.Tag, "名称").Label("名称") })
	}
	closeButton := func() {
		// Closed while building, not by OnClick, which runs after it.
		if kit.IconButton(c, kit.SymX, "关闭", kit.IconButtonOptions{Size: 26, IconSize: 14}).Clicked() {
			*open = false
		}
	}
	heading := func() {
		ui.Text(c, title).FontSize(kit.FontToolbar).FontWeight(600).SingleLine().Shrink(0)
	}
	box := ui.Column(c.Key("group-header")).Padding(18, 24).Gap(10).Shrink(0).
		BorderWidth(0, 0, 1, 0).BorderColor(k.HairlineSoft)
	box.Children(func() {
		if narrow {
			ui.Row(c).Gap(12).Children(func() {
				heading()
				name(0)
				closeButton()
			})
			ui.Row(c).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() { e.modeControl(c, o) })
			})
			return
		}
		ui.Row(c).Gap(12).Children(func() {
			heading()
			name(200)
			ui.Column(c).Grow(1).MinWidth(0).Children(func() { e.modeControl(c, o) })
			closeButton()
		})
	})
}

// groupFooter is Delete (editing) on the left, Cancel and Save on the right.
func (e *outEditor) groupFooter(c *ui.Context, o *model.Outbound) {
	k := kit.T(c)
	ui.Row(c.Key("group-footer")).Padding(14, 24).Gap(8).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(k.HairlineSoft).
		Children(func() {
			if !e.isNew {
				del := kit.Button(c, "删除", kit.ButtonOptions{Variant: kit.Danger})
				if del.Clicked() {
					e.remove.Open = true
				}
				name := o.Tag
				if name == "" {
					name = typeLabel(o.Type)
				}
				kit.ConfirmPopover(c, del, &e.remove, kit.ConfirmOptions{
					Title: "删除策略组？", Message: "「" + name + "」将从出站列表中删除。", Confirm: "删除", Danger: true, Run: e.delete,
				})
			}
			ui.Spacer(c)
			kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(e.close)
			kit.Button(c, "保存", kit.ButtonOptions{Variant: kit.Primary}).OnClick(e.save)
		})
}

// sourcesColumn is Sources: "By region", "Name contains any", "Name
// excludes", the custom pattern when one is left, "Added manually", and at
// the bottom the mode's options ("Health check" or "Options"). It scrolls on its own.
func (e *outEditor) sourcesColumn(c *ui.Context, doc *model.Doc, o *model.Outbound, narrow bool) {
	scroll := ui.Scroll(c.Key("sources-scroll")).Grow(1).MinHeight(0).Padding(16, 20).Label("来源")
	if narrow {
		scroll.Background(kit.T(c).Surface2)
	}
	// The content fills the column at least, so the options sit at its
	// bottom while the sections leave room.
	view := scroll.Bounds().H - 32
	scroll.Children(func() {
		ui.Column(c).Gap(18).MinHeight(max(view, 0)).Children(func() {
			f := editableFilter(o)
			e.regionSection(c, doc, o, f)
			e.keywordSection(c, f)
			e.excludeSection(c, f)
			e.patternSection(c, f)
			e.manualSection(c, doc, o)
			ui.Spacer(c)
			e.form = e.optionsSection(c, doc, o)
		})
	})
}

// sourceCaption is a section's caption, 11 DIPs in Label2.
func sourceCaption(c *ui.Context, text string) ui.Element {
	return ui.Text(c, text).FontSize(kit.FontCaption).FontWeight(600).TextColor(kit.T(c).Label2).SingleLine()
}

// optionsSection is the mode's fields behind a summary row: "Health check"
// for the modes testing their members, summarized by interval and timeout,
// and "Options" for manual, summarized by its default member. Folded until opened
// or a save finds a problem in them.
func (e *outEditor) optionsSection(c *ui.Context, doc *model.Doc, o *model.Outbound) forms.Result {
	k := kit.T(c)
	result := forms.Result{Valid: true}
	fields := groupFields(o.Type)
	if len(fields) == 0 {
		return result
	}
	check := model.HasHealthCheck(o.Type)
	title, summary := "选项", ""
	if check {
		title = "健康检查"
		var parts []string
		value := func(key string) (string, bool) {
			i := slices.IndexFunc(fields, func(f schema.Field) bool { return f.Key == key })
			if i < 0 {
				return "", false
			}
			s, _ := o.Fields[key].(string)
			if strings.TrimSpace(s) == "" {
				s, _ = fields[i].Def.(string)
			}
			s = strings.TrimSpace(s)
			return s, s != ""
		}
		if s, ok := value("url"); ok {
			parts = append(parts, urlHost(s))
		}
		for _, key := range []string{"interval", "timeout"} {
			if s, ok := value(key); ok {
				parts = append(parts, durationText(s))
			}
		}
		summary = strings.Join(parts, " · ")
	} else if id, _ := o.Fields["default_ref"].(string); id != "" {
		if t := model.FindOutboundTarget(doc, id); t.Valid() {
			summary = t.Tag()
		}
	}
	ui.Column(c.Key("options")).Gap(12).Shrink(0).Children(func() {
		label := "展开" + title
		if e.advOpen {
			label = "收起" + title
		}
		head := ui.ButtonBase(c).Padding(9, 12).Gap(8).Radius(12).FillWidth().Justify(ui.Start).Label(label).
			Transition(kit.HoverMotion)
		switch {
		case head.Pressed():
			head.Background(k.Fill2)
		case head.Hovered():
			head.Background(k.Fill.Mix(k.Fill2, 0.6))
		default:
			head.Background(k.Fill)
		}
		head.TextColor(k.Label).Children(func() {
			ui.Text(c, title).FontSize(kit.FontBody).SingleLine().Grow(1)
			if summary != "" {
				ui.Text(c, summary).FontSize(kit.FontTable).TextColor(k.Label2).FontFeatures("tnum").
					SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
			}
			chev := kit.Icon(c, kit.SymChevronDown, 13).TextColor(k.Label3)
			if !e.advOpen {
				chev.Rotate(-90)
			}
		})
		if head.Clicked() {
			e.advOpen = !e.advOpen
		}
		if !e.advOpen {
			return
		}
		fc := forms.Context{
			Doc: doc, NetworkConfigs: e.w.store.NetworkConfigs(), ExcludeRef: o.ID, ShowErrors: e.tried,
			OnChange: e.onChange, Inline: true,
		}
		// A reference field (the default member) offers the group's members.
		if slices.ContainsFunc(fields, func(f schema.Field) bool { return f.Type == schema.Ref }) {
			byID := map[string]forms.RefOption{}
			for _, r := range e.group.index.of(e.w.store.Version(), doc, o.ID).refOptions(doc) {
				if _, ok := byID[r.ID]; !ok {
					byID[r.ID] = r
				}
			}
			fc.RefOptions = []forms.RefOption{}
			for _, id := range e.group.final.list.ids {
				if r, ok := byID[id]; ok {
					fc.RefOptions = append(fc.RefOptions, r)
				}
			}
		}
		result = forms.Form(c, []any{"group-options", o.Type, o.ID, e.generation}, fields, o.Fields, fc)
	})
	return result
}

// urlHost is the host of a test address, the address itself when it has
// none.
func urlHost(s string) string {
	if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return s
}

// durationText is a duration field's value in words where it is a whole
// number of one unit: 3m is "3 minutes", 5s "5 seconds"; otherwise as
// written.
func durationText(s string) string {
	units := []struct{ suffix, word string }{{"ms", " 毫秒"}, {"s", " 秒"}, {"m", " 分钟"}, {"h", " 小时"}}
	for _, u := range units {
		n, ok := strings.CutSuffix(s, u.suffix)
		if !ok || n == "" || strings.Trim(n, "0123456789") != "" {
			continue
		}
		return n + u.word
	}
	return s
}
