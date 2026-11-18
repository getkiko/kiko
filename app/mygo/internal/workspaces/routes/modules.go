package routes

import (
	"strconv"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// moduleRoutes are the routing rules the enabled modules bring, in the
// order the core applies them: after the user's own Pre-rules. Direct rules
// point to the outbound the emitter gives them; without one they take no
// effect (the emitter skips them) and are left out, as the table's rows
// still show them.
func moduleRoutes(doc *model.Doc) []*model.RouteRule {
	rules := model.ModuleRouteRules(doc)
	if len(rules) == 0 {
		return nil
	}
	direct := moduleDirectOutbound(doc)
	out := rules[:0:0]
	for _, r := range rules {
		if r.Action == model.ActionRoute {
			if direct == "" {
				continue
			}
			r.Outbound = model.RefOf(direct)
		}
		out = append(out, r)
	}
	return out
}

// moduleDirectOutbound is the outbound a module's direct rules use: an
// enabled direct outbound without settings, else any enabled direct one
// (the emitter's choice), "" for none.
func moduleDirectOutbound(doc *model.Doc) string {
	fallback := ""
	for _, o := range doc.Outbounds {
		if o.Type != "direct" || !o.Enabled || refTag(doc, o.ID) == "" {
			continue
		}
		empty := true
		for _, v := range o.Fields {
			if v != nil && v != "" {
				empty = false
				break
			}
		}
		if empty {
			return o.ID
		}
		if fallback == "" {
			fallback = o.ID
		}
	}
	return fallback
}

// moduleBands are the bands of the topology for the module rules: one per
// module, its rules numbered on from the user's Pre-rules. pre marks them as
// Pre-rules of another device's topology.
func moduleBands(doc *model.Doc, pre bool, ruleExit func(*model.RouteRule) string) []topoBand {
	rules := moduleRoutes(doc)
	if len(rules) == 0 {
		return nil
	}
	ordinal := model.ModuleRuleOrdinals(doc)
	var bands []topoBand
	current := ""
	for _, r := range rules {
		id, _, _ := model.ModuleOf(r.ID)
		if id != current {
			current = id
			title := r.Name
			if pre {
				title = "前置规则 · " + title
			}
			bands = append(bands, topoBand{title: title})
		}
		number := strconv.Itoa(ordinal[r.ID])
		if pre {
			number = "P" + number
		}
		band := &bands[len(bands)-1]
		band.rules = append(band.rules, topoRule{
			id: r.ID, name: r.Name, summary: model.RuleSummary(r.Match, nil, ""), number: number, pre: pre, enabled: true,
			action: r.Action, exit: ruleExit(r), hitKey: r.ID,
		})
	}
	return bands
}
