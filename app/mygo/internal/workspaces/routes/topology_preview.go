package routes

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// previewTopologyNode shows the rule of a node of the topology, id "final"
// for the default, in place of its editor.
func (w *Workspace) previewTopologyNode(id string) {
	w.leaveEditor(func() {
		w.err = ""
		w.topoPreview = id
	})
}

// topologyPreview is the rule of the node clicked: its conditions as set,
// its action and exit, with Edit opening its editor.
func (w *Workspace) topologyPreview(c *ui.Context, doc *model.Doc, g *topoGraph) {
	var node *topoRule
	for _, r := range g.rules() {
		if r.id == w.topoPreview {
			r := r
			node = &r
		}
	}
	if node == nil {
		w.topoPreview = ""
		return
	}
	var rule *model.RouteRule
	for _, r := range doc.Route.Rules {
		if r.ID == node.id {
			rule = r
		}
	}
	// A module's rule is read-only: no editor, a way to the module instead.
	moduleID, _, fromModule := model.ModuleOf(node.id)
	if fromModule {
		for _, r := range moduleRoutes(doc) {
			if r.ID == node.id {
				rule = r
			}
		}
	}
	k := kit.T(c)
	label, icon, tone := actionLook(c, node.action, false)
	if node.final {
		icon, tone = kit.SymRouter, k.AccentText
	}
	subtitle := node.number
	if group := ruleGroupName(doc, rule); group != "" {
		subtitle = strings.TrimSpace(subtitle + " · " + group)
	}
	editable := rule != nil && !fromModule || node.final && w.owner != model.PreDeviceID
	kit.Inspector(c, node.name, subtitle, func() {
		ui.Box(c).Size(32, 32).Radius(9).Center().Background(k.Fill).TextColor(tone).Shrink(0).
			Children(func() { kit.Icon(c, icon, 15) })
	}, func() {
		if fromModule {
			kit.Button(c, "前往模块", kit.ButtonOptions{Small: true, Icon: kit.SymArrowRight}).OnClick(func() {
				w.topoPreview = ""
				w.openModule(moduleID)
			})
		}
		if editable {
			kit.Button(c, "编辑", kit.ButtonOptions{Small: true, Icon: kit.SymPencil}).OnClick(func() {
				w.topoPreview = ""
				if node.final {
					w.fallback = &fallbackEditor{open: true}
					return
				}
				w.openRule(doc, node.id)
			})
		}
	}, func() { w.topoPreview = "" }, func() {
		kit.Section(c, "匹配条件", nil, func() {
			if rule == nil {
				kit.Property(c, "连接", node.summary)
				return
			}
			root := model.RuleCondition(rule.Match, rule.ConditionTree, rule.Logic)
			if len(root.Rules) == 0 {
				kit.Property(c, "连接", "任意连接")
				return
			}
			previewCondition(c, root, true)
		})
		kit.Section(c, "动作", nil, func() {
			if !node.final {
				kit.Property(c, "动作", label)
			}
			if e := g.exit(node.exit); e != nil {
				exit := e.label
				if e.detail != "" {
					exit += " → " + e.detail
				}
				kit.Property(c, "出口", exit)
			}
			if rule != nil && rule.Action == model.ActionSniff && rule.OverrideDestination {
				kit.Property(c, "覆盖目标地址", "是")
			}
		})
		if rule != nil && (!rule.Enabled || node.invalid) {
			kit.Section(c, "状态", nil, func() {
				if !rule.Enabled {
					kit.Property(c, "规则", "已停用")
				}
				if node.invalid {
					kit.Property(c, "配置", "请修正配置")
				}
			})
		}
		if t, ok := w.ruleConnTargetOf(node); ok {
			label := finalRuleLabel
			if !node.final {
				ordinal, _ := strconv.Atoi(strings.TrimPrefix(node.number, "P"))
				label = ruleLabel(ordinal, node.name)
			}
			w.ruleConnsSection(c, t, label)
		}
	})
}

// ruleGroupName is the name of the group of rule, "" for none.
func ruleGroupName(doc *model.Doc, rule *model.RouteRule) string {
	if rule == nil {
		return ""
	}
	for _, g := range doc.Route.Groups {
		if g.ID == rule.GroupID {
			return g.Name
		}
	}
	return ""
}

// previewCondition lists the conditions of node that are set: a leaf's
// fields with their values, a group's members under how many must match.
func previewCondition(c *ui.Context, node *model.Condition, root bool) {
	k := kit.T(c)
	if node.Kind == model.ConditionMatch {
		for _, key := range sortedMatchKeys(node.Match) {
			values := previewValues(node.Match[key])
			if len(values) == 0 {
				continue
			}
			label := model.ConditionLabels[key]
			if label == "" {
				label = key
			}
			if node.Invert {
				label = "排除 " + label
			}
			kit.Property(c, label, strings.Join(values, "、"))
		}
		return
	}
	children := func() {
		for _, child := range node.Rules {
			previewCondition(c, child, false)
		}
	}
	if root && !node.Invert && (node.Mode != "or" || len(node.Rules) < 2) {
		children()
		return
	}
	title := "全部满足"
	if node.Mode == "or" {
		title = "任一满足"
	}
	if node.Invert {
		title = "排除 · " + title
	}
	ui.Column(c).Gap(2).PaddingY(2).Children(func() {
		ui.Text(c, title).FontSize(kit.FontCaption).FontWeight(600).TextColor(k.Label3)
		ui.Column(c).Padding(0, 0, 0, 10).BorderWidth(0, 0, 0, 1).BorderColor(k.HairlineSoft).Children(children)
	})
}

// previewValues are a condition field's values as text, true as "Yes".
func previewValues(value any) []string {
	list, ok := jsv.Array(value)
	if !ok {
		list = []any{value}
	}
	var out []string
	for _, v := range list {
		switch v := v.(type) {
		case nil:
		case bool:
			if v {
				out = append(out, "是")
			} else {
				out = append(out, "否")
			}
		default:
			if s := strings.TrimSpace(jsv.String(v)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
