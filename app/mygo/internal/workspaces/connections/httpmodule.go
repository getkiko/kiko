package connections

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// hitModule is the module a rewrite or script hit belongs to.
type hitModule struct {
	id, name string
	// item names the rule or script inside the module.
	item string
}

// title is the module name and the rule, joined by a middle dot.
func (o hitModule) title() string { return o.name + " · " + o.item }

// moduleOfHit finds the module behind a hit: a rewrite rule id
// "mod:<module>:<n>" or a script tag "<module>-<tag>" of an installed
// module. Ids that only look like module ids, or whose module is gone, are
// the user's own.
func moduleOfHit(doc *model.Doc, h api.HTTPRewriteHit) (hitModule, bool) {
	if doc == nil || doc.HTTPTools == nil {
		return hitModule{}, false
	}
	if h.Kind == "script" {
		tag := hitTag(h)
		id, ok := model.ModuleOfScriptTag(tag)
		if !ok {
			return hitModule{}, false
		}
		m := doc.HTTPTools.FindModule(id)
		if m == nil {
			return hitModule{}, false
		}
		for _, s := range m.Contents.Scripts {
			if s != nil && model.ModuleScriptTag(id, s.EffectiveTag()) == tag {
				item := s.Name
				if item == "" {
					item = s.EffectiveTag()
				}
				return hitModule{id: id, name: m.Name, item: item}, true
			}
		}
		return hitModule{}, false
	}
	id, n, ok := model.ModuleOf(h.Rule)
	if !ok {
		return hitModule{}, false
	}
	m := doc.HTTPTools.FindModule(id)
	if m == nil {
		return hitModule{}, false
	}
	item := "第 " + strconv.Itoa(n) + " 条"
	if rw := model.ModuleRewrites(model.ActiveModule{Module: m, Contents: m.Contents}); rw != nil {
		if pattern := moduleRulePattern(rw, h.Rule); pattern != "" {
			item = pattern
		}
	}
	return hitModule{id: id, name: m.Name, item: item}, true
}

// moduleRulePattern is the URL pattern of the module rewrite rule id.
func moduleRulePattern(rw *model.HTTPRewrites, id string) string {
	for _, r := range rw.URL {
		if r.ID == id {
			return r.Pattern
		}
	}
	for _, r := range rw.Headers {
		if r.ID == id {
			return r.Pattern
		}
	}
	for _, r := range rw.Body {
		if r.ID == id {
			return r.Pattern
		}
	}
	for _, r := range rw.MapLocal {
		if r.ID == id {
			return r.Pattern
		}
	}
	return ""
}

// moduleButton goes to the module a hit belongs to, in the Rewrites workspace.
func moduleButton(c *ui.Context, e *env.Env, o hitModule) {
	kit.Button(c, "前往模块", kit.ButtonOptions{Small: true, Variant: kit.TextButton, Icon: kit.SymArrowRight}).
		OnClick(func() { e.Navigate(env.PageRewrites, "module:"+o.id) })
}
