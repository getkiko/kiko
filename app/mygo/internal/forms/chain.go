package forms

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/nodeview"
	"github.com/getkiko/kiko/app/mygo/internal/schema"
)

// chainDrag is a hop dragged within a chain.
type chainDrag struct{ id string }

// Chain editor metrics: a hop's chip, the widest its name shows, and the
// add chip.
const (
	chainChipH     = 26
	chainNameMaxW  = 180
	chainAddChipW  = 66
	chainAddPrompt = "＋ 添加"
)

// ChainEditor edits *chain, the proxy chain of owners (the outbound or
// endpoint edited, or every node of a subscription), as the path traffic
// takes, left to right and wrapping: this host, a chip for each hop,
// dragged onto another to take its place and removed by its ×, "+ Add"
// offering what may stand next, then ownerName. A hop at fault is marked
// and its problem, or a cycle, shows below. It reports whether the chain
// changed.
func ChainEditor(c *ui.Context, key any, doc *model.Doc, chain *[]string, owners []string, ownerName string) bool {
	k := kit.T(c)
	changed := false
	ui.Column(c.Key(key)).Gap(6).Shrink(0).Children(func() {
		ui.Text(c, "代理链").FontSize(kit.FontTable).TextColor(k.Label2).SingleLine()
		issue, ok := model.ChainError(doc, owners, *chain)
		options := ChainCandidates(doc, owners, *chain)
		ui.Row(c.Key("hops")).Wrap().GapX(4).GapY(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			end := func(text string) {
				ui.Text(c, text).FontSize(kit.FontTable).TextColor(k.Label3).SingleLine().Shrink(0)
			}
			arrow := func() {
				ui.Text(c, "→").FontSize(kit.FontTable).TextColor(k.Label4).SingleLine().Shrink(0)
			}
			end("本机")
			for i, id := range *chain {
				arrow()
				if chainChip(c, doc, chain, i, id, !ok && issue.Hop == i, k) {
					changed = true
				}
			}
			if len(options) > 0 {
				arrow()
				var add string
				trigger := kit.SearchSelect(c, "chain-add", &add, options, kit.SearchSelectOptions{Label: "添加一跳", Empty: chainAddPrompt,
					Placeholder: "搜索出站…", Width: chainAddChipW, NoChevron: true})
				trigger.Height(chainChipH).PaddingX(10).Radius(kit.RadiusCapsule).FontSize(kit.FontTable).Shrink(0).
					Border(1, k.Label4).BorderStyle(ui.BorderDashed).Transition(kit.HoverMotion)
				switch {
				case trigger.Pressed():
					trigger.Background(k.Fill2)
				case trigger.Hovered():
					trigger.Background(k.Fill)
				default:
					trigger.Background(k.Fill.Alpha(0))
				}
				if trigger.Changed() && add != "" {
					*chain = append(slices.Clone(*chain), add)
					changed = true
				}
			}
			arrow()
			end(ownerName)
		})
		if !ok {
			kit.FormError(c, issue.Message)
		}
		if hops := model.ChainWithoutUDP(doc, *chain); len(hops) > 0 {
			ui.Text(c, "经过「"+strings.Join(hops, "」「")+"」的链不转发 UDP").FontSize(kit.FontCaption).TextColor(k.Label3).Wrap()
		}
	})
	return changed
}

// chainChip is hop i of the chain, a capsule of its name and protocol
// with a ×, dragged onto another hop to take its place; fault marks the
// hop at fault. It reports whether the chain changed.
func chainChip(c *ui.Context, doc *model.Doc, chain *[]string, i int, id string, fault bool, k *kit.Tokens) bool {
	changed := false
	t := model.FindOutboundTarget(doc, id)
	name, detail := id, "已删除"
	if t.Valid() {
		name, detail = t.Tag(), nodeview.ProtocolTag(t.Type())
		if model.IsGroup(t.Type()) {
			detail = "策略组"
		}
	}
	chip := ui.Row(c.Key("hop-" + id)).Height(chainChipH).Gap(5).PaddingStart(10).PaddingEnd(4).Radius(kit.RadiusCapsule).
		Shrink(0).MinWidth(0).Transition(kit.HoverMotion).Label(fmt.Sprintf("第 %d 跳 %s", i+1, name))
	chip.Drag(chainDrag{id})
	if chip.Dragging() {
		chip.Opacity(0.45)
	}
	dragged, over := ui.DragOver[chainDrag](chip)
	switch {
	case over && dragged.id != id:
		chip.Background(k.AccentWeak).Border(1, k.Accent)
	case fault:
		chip.Background(k.DangerWeak).Border(1, k.Danger.Alpha(0.6))
	case chip.Hovered() && !chip.Dragging():
		chip.Background(k.Fill2).Border(1, k.Fill.Alpha(0))
	default:
		chip.Background(k.Fill).Border(1, k.Fill.Alpha(0))
	}
	if v, ok := ui.Drop[chainDrag](chip); ok && v.id != id {
		*chain = moveHop(*chain, v.id, id)
		changed = true
	}
	chip.Children(func() {
		text := ui.Text(c, name).FontSize(kit.FontBody).SingleLine().Ellipsis("…").MinWidth(0).MaxWidth(chainNameMaxW)
		if fault {
			text.TextColor(k.Danger)
		}
		ui.Text(c, detail).FontSize(kit.FontCaption).TextColor(k.Label3).SingleLine().Shrink(0)
		if kit.IconButton(c, kit.SymX, "移除 "+name, kit.IconButtonOptions{Size: 18, IconSize: 10}).Clicked() {
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
