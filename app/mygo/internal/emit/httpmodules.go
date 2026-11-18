package emit

import (
	"slices"
	"strconv"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Module merge: the enabled modules' content follows the user's own entries.
// Hostnames, rewrite rules and scripts reach the core only while HTTPS
// decryption is on (their callers return early otherwise); the routing rules
// do not depend on it. Pending updates never reach the core, and hostnames
// and scripts the user has not confirmed are skipped with a warning.

// Limits of all enabled modules together; a module that would pass them is
// skipped as a whole.
const (
	maxActiveModuleRules   = 50000
	maxActiveModuleSources = 16 << 20
)

// sanitizeModules returns doc without the modules the emitter must not use:
// empty or repeated ids (the ids name merged rules and script tags), modules
// past the count limit, enabled modules the model refuses, and enabled
// modules past the combined rule and source limits. Each skip is a warning.
// A document whose modules are all usable is returned as it is.
func sanitizeModules(doc *model.Doc) (*model.Doc, []string) {
	if doc == nil || doc.HTTPTools == nil || len(doc.HTTPTools.Modules) == 0 {
		return doc, nil
	}
	var warnings []string
	var kept []*model.HTTPModule
	seen := map[string]bool{}
	rules, sources := 0, 0
	for i, m := range doc.HTTPTools.Modules {
		switch {
		case m == nil:
			warnings = append(warnings, "模块列表第 "+strconv.Itoa(i+1)+" 项为空，已跳过")
			continue
		case seen[m.ID]:
			warnings = append(warnings, "模块「"+m.Name+"」的标识重复，已跳过")
			continue
		case i >= model.MaxModules:
			warnings = append(warnings, "模块超过 "+strconv.Itoa(model.MaxModules)+" 个，「"+m.Name+"」已跳过")
			continue
		}
		seen[m.ID] = true
		if m.Enabled {
			// An item the model refuses is skipped on its own where it is
			// emitted; the module as a whole is only reported.
			if issue := m.Issue(); issue != "" {
				warnings = append(warnings, "模块「"+m.Name+"」有问题："+issue)
			}
			n, size := len(m.Contents.Rules), m.Contents.SourceBytes()
			if rules+n > maxActiveModuleRules || sources+size > maxActiveModuleSources {
				warnings = append(warnings, "启用的模块规则或脚本总量过大，「"+m.Name+"」已跳过")
				continue
			}
			rules, sources = rules+n, sources+size
		}
		kept = append(kept, m)
	}
	if len(kept) == len(doc.HTTPTools.Modules) {
		return doc, warnings
	}
	tools := *doc.HTTPTools
	tools.Modules = kept
	out := *doc
	out.HTTPTools = &tools
	return &out, warnings
}

// moduleHostnames returns the include and exclude hostnames of the enabled
// modules, and a warning for each unconfirmed include host.
func moduleHostnames(tools *model.HTTPTools) (include, exclude, warnings []string) {
	for _, a := range model.ActiveModules(tools) {
		for _, host := range a.SkippedHosts {
			warnings = append(warnings, "模块「"+a.Module.Name+"」的主机名「"+host+"」尚未确认，已跳过")
		}
		if h := a.Contents.Hostnames; h != nil {
			include = append(include, h.Include...)
			exclude = append(exclude, h.Exclude...)
		}
	}
	return include, exclude, warnings
}

// mergedRewrites returns the user's rewrite rules followed by those of the
// enabled modules (ids "mod:<module id>:<n>"). Without modules it returns rw
// itself.
func mergedRewrites(tools *model.HTTPTools) *model.HTTPRewrites {
	var user *model.HTTPRewrites
	if tools != nil {
		user = tools.Rewrites
	}
	var extra []*model.HTTPRewrites
	for _, a := range model.ActiveModules(tools) {
		if rw := model.ModuleRewrites(a); rw != nil {
			extra = append(extra, rw)
		}
	}
	if len(extra) == 0 {
		return user
	}
	merged := &model.HTTPRewrites{}
	if user != nil {
		merged.URL = slices.Clone(user.URL)
		merged.Headers = slices.Clone(user.Headers)
		merged.Body = slices.Clone(user.Body)
		merged.MapLocal = slices.Clone(user.MapLocal)
		merged.MaxBodySize = user.MaxBodySize
	}
	for _, rw := range extra {
		merged.URL = append(merged.URL, rw.URL...)
		merged.Headers = append(merged.Headers, rw.Headers...)
		merged.Body = append(merged.Body, rw.Body...)
		merged.MapLocal = append(merged.MapLocal, rw.MapLocal...)
	}
	return merged
}

// withModuleScripts returns tools with the scripts of the enabled modules
// appended (tags "<module id>-<tag>"); the HTTP scripts only while decryption
// is on. tools itself when no module contributes. It also returns a warning per unconfirmed
// script.
func withModuleScripts(tools *model.HTTPTools) (*model.HTTPTools, []string) {
	if tools == nil {
		return tools, nil
	}
	// rule and dns scripts act on routing and DNS and do not need HTTPS
	// decryption; the HTTP scripts do.
	mitm := tools.MITM != nil && tools.MITM.Enabled
	var extra []*model.HTTPScript
	var warnings []string
	for _, a := range model.ActiveModules(tools) {
		for _, tag := range a.SkippedScripts {
			if mitm || moduleScriptIsDecision(a.Module, tag) {
				warnings = append(warnings, "模块「"+a.Module.Name+"」的脚本「"+tag+"」尚未确认，已跳过")
			}
		}
		for _, s := range model.ModuleScripts(a) {
			if mitm || s.IsDecision() {
				extra = append(extra, s)
			}
		}
	}
	if len(extra) == 0 {
		return tools, warnings
	}
	merged := *tools
	merged.Scripts = append(slices.Clone(tools.Scripts), extra...)
	return &merged, warnings
}

func moduleScriptIsDecision(m *model.HTTPModule, tag string) bool {
	for _, s := range m.Contents.Scripts {
		if s != nil && s.EffectiveTag() == tag {
			return s.IsDecision()
		}
	}
	return false
}

// moduleDNSScriptRules returns the catch-all DNS rules of the dns scripts of
// the enabled modules: every query runs through each confirmed script, a
// failing script passes the query on. They come before the user's DNS rules.
func (e *emitter) moduleDNSScriptRules() []any {
	var out []any
	for _, a := range model.ActiveModules(e.doc.HTTPTools) {
		for _, s := range model.ModuleScripts(a) {
			if s.Type != model.ScriptDNS || e.scriptTypes[s.Tag] != model.ScriptDNS {
				continue
			}
			out = append(out, obj("action", "script", "dns_script", s.Tag))
		}
	}
	return out
}

// maxModuleRuleValues bounds the values one emitted module rule carries.
const maxModuleRuleValues = 512

// directOutboundID returns the id of the outbound module direct rules use:
// an enabled direct outbound without settings, else any direct outbound.
func (e *emitter) directOutboundID() string {
	var fallback string
	for _, o := range e.enabledOut {
		if o.Type != "direct" {
			continue
		}
		if _, ok := e.tagOf[o.ID]; !ok {
			continue
		}
		empty := true
		for _, v := range o.Fields {
			if !isEmpty(v) {
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

// moduleRouteRules returns the routing rules of the enabled modules, ready
// for emitRoute: consecutive rules of one module with the same match key and
// action share one route rule (the first rule's id); direct rules point to
// the direct outbound.
func (e *emitter) moduleRouteRules() []*model.RouteRule {
	for _, a := range model.ActiveModules(e.doc.HTTPTools) {
		if n := len(a.SkippedDirect); n > 0 {
			e.warnings = append(e.warnings, "模块「"+a.Module.Name+"」的 "+strconv.Itoa(n)+" 条直连规则尚未确认，已跳过")
		}
	}
	rules := model.ModuleRouteRules(e.doc)
	if len(rules) == 0 {
		return nil
	}
	direct := e.directOutboundID()
	warned := false
	var out []*model.RouteRule
	var last *model.RouteRule
	var lastModule, lastKey string
	for _, r := range rules {
		if r.Action == model.ActionRoute {
			if direct == "" {
				if !warned {
					e.warnings = append(e.warnings, "模块的直连规则需要一个直连出站，已跳过")
					warned = true
				}
				last = nil
				continue
			}
			r.Outbound = model.RefOf(direct)
		}
		module, _, _ := model.ModuleOf(r.ID)
		var key string
		for k := range r.Match {
			key = k
		}
		if last != nil && module == lastModule && key == lastKey && last.Action == r.Action && key != "script" {
			if values, _ := last.Match[key].([]any); len(values) < maxModuleRuleValues {
				last.Match[key] = append(values, r.Match[key].([]any)...)
				continue
			}
		}
		out = append(out, r)
		last, lastModule, lastKey = r, module, key
	}
	return out
}

// moduleOrdinalsKey keys the module rules' numbers among e.ordinals.
const moduleOrdinalsKey = "\x00modules"

// moduleRuleRef is the route table reference of a module routing rule,
// numbered as the route table shows it (model.ModuleRuleOrdinals).
func (e *emitter) moduleRuleRef(r *model.RouteRule) api.RuleRef {
	ordinals, ok := e.ordinals[moduleOrdinalsKey]
	if !ok {
		ordinals = model.ModuleRuleOrdinals(e.source)
		e.ordinals[moduleOrdinalsKey] = ordinals
	}
	return api.RuleRef{ID: r.ID, Owner: model.PreDeviceID, Section: "route", Ordinal: ordinals[r.ID], Name: r.Name}
}
