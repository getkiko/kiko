package routes

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// copyRequest is what to copy: every rule of a device's section to owner
// (from the device the dialog chooses, as a device's "Copy to…" preselects),
// or rule ruleID of owner to another device.
type copyRequest struct {
	sec    section
	owner  string
	ruleID string
}

// copyDialog copies rules between devices.
type copyDialog struct {
	open           bool
	req            copyRequest
	selected       string
	includeDefault bool
}

func newCopyDialog(doc *model.Doc, req copyRequest) *copyDialog {
	d := &copyDialog{open: true, req: req}
	if choices := d.choices(doc); len(choices) > 0 {
		d.selected = choices[0]
	}
	return d
}

// choices are the devices other than the request's.
func (d *copyDialog) choices(doc *model.Doc) []string {
	return slices.DeleteFunc(model.AllPolicyScopes(doc), func(id string) bool { return id == d.req.owner })
}

// copyPlan is what a copy covers, for the dialog's summary.
type copyPlan struct {
	source, target string
	single         bool
	groups, rules  int
	hasDefault     bool
	copyDefault    bool
	explicitSource bool
	defaultName    string
	detourName     string
}

// plan describes the copy the dialog would make.
func (d *copyDialog) plan(doc *model.Doc) copyPlan {
	p := copyPlan{single: d.req.ruleID != ""}
	p.source, p.target = d.selected, d.req.owner
	if p.single {
		p.source, p.target = d.req.owner, d.selected
	}
	p.hasDefault = model.CanCopyPolicyDefault(p.source, p.target)
	p.copyDefault = !p.single && p.hasDefault && d.includeDefault
	var trees []*model.Condition
	if d.req.sec == sectionDNS {
		groups := model.DNSGroupsOf(doc, p.source)
		p.groups = len(groups)
		for _, r := range model.OrderedDeviceRules(doc.DNS.Rules, groups, p.source) {
			if !p.single || r.ID == d.req.ruleID {
				p.rules++
				trees = append(trees, model.RuleCondition(r.Match, r.ConditionTree, ""))
			}
		}
	} else {
		groups := model.RouteGroupsOf(doc, p.source)
		p.groups = len(groups)
		for _, r := range model.OrderedDeviceRules(doc.Route.Rules, groups, p.source) {
			if !p.single || r.ID == d.req.ruleID {
				p.rules++
				trees = append(trees, model.RuleCondition(r.Match, r.ConditionTree, r.Logic))
			}
		}
	}
	for _, tree := range trees {
		for _, m := range model.ConditionMatches(tree) {
			for key := range m {
				if strings.HasPrefix(key, "source_ip") {
					p.explicitSource = true
				}
			}
		}
	}
	var defaultID string
	if d.req.sec == sectionDNS {
		defaultID = model.DNSFinalFor(doc, p.source).ID()
		if s := model.DNSServerByID(doc, defaultID); s != nil {
			p.defaultName = s.Tag
		}
		p.detourName = "协议默认"
		if detour := model.DNSFinalDetourFor(doc, p.source).ID(); detour != "" {
			p.detourName = refTag(doc, detour)
			if p.detourName == "" {
				p.detourName = "需修正引用"
			}
		}
	} else {
		defaultID = model.RouteFinalFor(doc, p.source).ID()
		if t := model.FindOutboundTarget(doc, defaultID); t.Valid() {
			p.defaultName = t.Tag()
		}
	}
	if p.defaultName == "" {
		p.defaultName = "核心默认"
		if defaultID != "" {
			p.defaultName = "需修正引用"
		}
	}
	return p
}

// copyDialogView is the "Copy rules" dialog.
func (w *Workspace) copyDialogView(c *ui.Context, doc *model.Doc) {
	d := w.copy
	p := d.plan(doc)
	choices := d.choices(doc)
	sectionName := "流量"
	if d.req.sec == sectionDNS {
		sectionName = " DNS"
	}
	title := "复制" + sectionName + "规则"
	label := "来源设备"
	if p.single {
		title, label = "复制规则到设备", "目标设备"
	}
	name := func(id string) string { return model.PolicyOwnerName(doc, id) }
	kit.Dialog(c, &d.open, title, 0, func() {
		dialogRow(c, label, func() {
			var options []kit.Option
			for _, id := range choices {
				options = append(options, kit.Option{Value: id, Label: name(id)})
			}
			kit.SearchSelect(c, "device", &d.selected, options, kit.SearchSelectOptions{Label: label, Placeholder: "搜索设备"})
		})
		scope := fmt.Sprintf("%d 个分组 · %d 条规则", p.groups, p.rules)
		if p.single {
			scope = "1 条规则"
		}
		help(c, fmt.Sprintf("从「%s」复制到「%s」：%s，按原顺序追加。", name(p.source), name(p.target), scope))
		if !p.single && p.hasDefault {
			dialogRow(c, "默认项", func() {
				text := "同时复制默认出口"
				if d.req.sec == sectionDNS {
					text = "同时复制兜底 DNS"
				}
				kit.Checkbox(c, &d.includeDefault, text)
			})
		}
		if !p.hasDefault {
			help(c, "前置规则仅复制规则与分组，剩余请求继续后续设备规则。")
		}
		if p.copyDefault {
			what := "默认出口"
			if d.req.sec == sectionDNS {
				what = "兜底 DNS"
			}
			text := fmt.Sprintf("「%s」的%s将更新为「%s」", name(p.target), what, p.defaultName)
			if d.req.sec == sectionDNS {
				text += fmt.Sprintf("，查询出口为「%s」", p.detourName)
			}
			help(c, text+"。")
		}
		if p.explicitSource {
			kit.Notice(c, kit.Neutral, "", "规则包含显式来源 IP 条件，副本会保留这些条件。", nil)
		}
		if w.err != "" {
			kit.Notice(c, kit.DangerTone, "", w.err, nil)
		}
	}, func() {
		kit.Button(c, "取消", kit.ButtonOptions{}).OnClick(func() { d.open = false })
		disabled := d.selected == "" || !slices.Contains(choices, d.selected) || p.rules == 0 && !p.copyDefault && p.groups == 0
		kit.Button(c, "复制", kit.ButtonOptions{Variant: kit.Primary, Disabled: disabled}).OnClick(func() {
			if disabled {
				return
			}
			options := model.CopyPolicyOptions{Section: d.req.sec.policy(), Source: p.source, Target: p.target, RuleID: d.req.ruleID, IncludeDefault: p.copyDefault}
			if w.save(func(doc *model.Doc) error {
				next, err := model.CopyDevicePolicy(doc, options)
				return replace(doc, next, err)
			}) {
				w.notice = fmt.Sprintf("已复制 %d 条规则至「%s」", p.rules, name(p.target))
				w.copy = nil
			}
		})
	})
	if w.copy == d && !d.open {
		w.copy = nil
		w.err = ""
	}
}
