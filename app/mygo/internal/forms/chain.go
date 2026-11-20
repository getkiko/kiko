package forms

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// chainDrag is a hop dragged within a chain.
type chainDrag struct{ id string }

// chainRowH is a hop's row.
const chainRowH = 30

// ChainEditor edits *chain, the proxy chain of owners (the outbound or
// endpoint edited, or every node of a subscription), in the order traffic
// takes it: a path from this host through every hop to ownerName, the
// hops numbered and dragged into order, each removable, then "+ Add hop"
// offering what may stand next. A hop at fault, or a cycle, shows its
// problem. It reports whether the chain changed.
func ChainEditor(c *ui.Context, key any, doc *model.Doc, chain *[]string, owners []string, ownerName string) bool {
	k := kit.T(c)
	changed := false
	ui.Column(c.Key(key)).Gap(5).Shrink(0).Children(func() {
		ui.Text(c, "代理链").FontSize(kit.FontTable).TextColor(k.Label2).SingleLine()
		ui.Text(c, chainPath(doc, *chain, ownerName)).FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		issue, ok := model.ChainError(doc, owners, *chain)
		if len(*chain) > 0 {
			ui.Column(c.Key("hops")).Shrink(0).Children(func() {
				for i, id := range *chain {
					if chainRow(c, doc, chain, i, id, k) {
						changed = true
					}
					if !ok && issue.Hop == i {
						ui.Column(c).PaddingStart(30).Shrink(0).Children(func() { kit.FormError(c, issue.Message) })
					}
				}
			})
		}
		if !ok && issue.Hop < 0 {
			kit.FormError(c, issue.Message)
		}
		if hops := model.ChainWithoutUDP(doc, *chain); len(hops) > 0 {
			ui.Text(c, "经过「"+strings.Join(hops, "」「")+"」的链不转发 UDP").FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
		options := ChainCandidates(doc, owners, *chain)
		if len(options) == 0 {
			return
		}
		var add string
		empty := "＋ 添加第 1 跳"
		if len(*chain) > 0 {
			empty = fmt.Sprintf("＋ 添加第 %d 跳", len(*chain)+1)
		}
		if kit.SearchSelect(c, "chain-add", &add, options, kit.SearchSelectOptions{Label: "添加一跳", Empty: empty, Placeholder: "搜索出站…"}).Changed() && add != "" {
			*chain = append(slices.Clone(*chain), add)
			changed = true
		}
	})
	return changed
}

// chainRow is hop i of the chain: its place, name and protocol, dragged to
// reorder, × on hover. It reports whether the chain changed.
func chainRow(c *ui.Context, doc *model.Doc, chain *[]string, i int, id string, k *kit.Tokens) bool {
	changed := false
	t := model.FindOutboundTarget(doc, id)
	name, detail := id, "已删除"
	if t.Valid() {
		name, detail = t.Tag(), protocolLabel(t.Type())
		if model.IsGroup(t.Type()) {
			detail = "策略组"
		}
	}
	row := ui.Row(c.Key("hop-" + id)).Height(chainRowH).Gap(6).PaddingX(6).Radius(7).Shrink(0).Transition(kit.HoverMotion).Label(fmt.Sprintf("第 %d 跳 %s", i+1, name))
	row.Drag(chainDrag{id})
	if row.Dragging() {
		row.Opacity(0.45)
	}
	hovered := row.Hovered()
	dragged, over := ui.DragOver[chainDrag](row)
	switch {
	case over && dragged.id != id:
		row.Background(k.AccentWeak)
	case hovered && !row.Dragging():
		row.Background(k.Fill)
	default:
		row.Background(k.Fill.Alpha(0))
	}
	if v, ok := ui.Drop[chainDrag](row); ok && v.id != id {
		*chain = moveHop(*chain, v.id, id)
		changed = true
	}
	row.Children(func() {
		ui.Text(c, fmt.Sprint(i+1)).FontSize(kit.FontTable).FontWeight(600).FontFeatures("tnum").TextColor(k.Label3).Width(18).Shrink(0).SingleLine()
		ui.Text(c, name).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
		ui.Text(c, detail).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
		if hovered && kit.IconButton(c, kit.SymX, "移除 "+name, kit.IconButtonOptions{Size: 20, IconSize: 11}).Clicked() {
			*chain = slices.DeleteFunc(slices.Clone(*chain), func(h string) bool { return h == id })
			changed = true
		}
	})
	return changed
}

// moveHop moves id to target's place in chain.
func moveHop(chain []string, id, target string) []string {
	from, to := slices.Index(chain, id), slices.Index(chain, target)
	if from < 0 || to < 0 || from == to {
		return chain
	}
	out := slices.Delete(slices.Clone(chain), from, from+1)
	return slices.Insert(out, to, id)
}

// chainPath is the way traffic takes, "本机 → A → B → ownerName".
func chainPath(doc *model.Doc, chain []string, ownerName string) string {
	parts := []string{"本机"}
	for _, id := range chain {
		if t := model.FindOutboundTarget(doc, id); t.Valid() {
			parts = append(parts, t.Tag())
		} else {
			parts = append(parts, "（已删除）")
		}
	}
	return strings.Join(append(parts, ownerName), " → ")
}

// ChainCandidates are the outbounds that may stand next in chain: for the
// first hop any usable outbound, group or endpoint, after it only usable
// servers (model.ChainMiddleHop); never an owner or a hop already in it.
func ChainCandidates(doc *model.Doc, owners []string, chain []string) []kit.Option {
	first := len(chain) == 0
	var out []kit.Option
	for _, option := range OutboundOptions(doc, append(slices.Clone(owners), chain...)...) {
		t := model.FindOutboundTarget(doc, option.Value)
		if !model.IsOutboundUsable(doc, option.Value) {
			continue
		}
		if first && model.ChainFirstHop(t) || !first && model.ChainMiddleHop(t) {
			if model.IsGroup(t.Type()) {
				option.Detail = "策略组"
			}
			out = append(out, option)
		}
	}
	return out
}

// WithoutDetour is table without the detour field, which the proxy chain
// replaces (ChainEditor).
func WithoutDetour(table []schema.Field) []schema.Field {
	return slices.DeleteFunc(slices.Clone(table), func(f schema.Field) bool { return f.Key == "detour" })
}
