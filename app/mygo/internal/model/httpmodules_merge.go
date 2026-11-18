package model

import (
	"strconv"
	"strings"
)

// The merge of module contents into the document's own entries. The emitter
// and the workspaces share these functions so both show the same result.

// ModuleRuleGroupPrefix starts the name of a module's routing rules.
const ModuleRuleGroupPrefix = "模块 · "

// ModuleItemID returns the id of the n-th (1-based) rewrite rule or routing
// rule of a module: "mod:<module id>:<n>".
func ModuleItemID(moduleID string, n int) string {
	return "mod:" + moduleID + ":" + strconv.Itoa(n)
}

// ModuleOf returns the module id and ordinal of an id made by ModuleItemID.
func ModuleOf(id string) (moduleID string, n int, ok bool) {
	rest, found := strings.CutPrefix(id, "mod:")
	if !found {
		return "", 0, false
	}
	i := strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[i+1:])
	if err != nil || n < 1 || !ValidModuleID(rest[:i]) {
		return "", 0, false
	}
	return rest[:i], n, true
}

// IsModuleRule reports whether id belongs to a routing rule synthesized from
// a module.
func IsModuleRule(id string) bool {
	_, _, ok := ModuleOf(id)
	return ok
}

// ModuleOfRewriteID returns the module id of a rewrite rule id made by the
// emitter.
func ModuleOfRewriteID(id string) (string, bool) {
	moduleID, _, ok := ModuleOf(id)
	return moduleID, ok
}

// ModuleScriptTag is the tag the emitter gives a module script.
func ModuleScriptTag(moduleID, tag string) string { return moduleID + "-" + tag }

// ModuleOfScriptTag returns the module id in a script tag made by
// ModuleScriptTag. The caller checks that the module exists: a user tag can
// look the same.
func ModuleOfScriptTag(tag string) (string, bool) {
	i := strings.IndexByte(tag, '-')
	if i <= 0 || !ValidModuleID(tag[:i]) {
		return "", false
	}
	return tag[:i], true
}

// ModuleHost is a hostname a module contributes to the HTTPS decryption
// lists.
type ModuleHost struct {
	Host       string
	Exclude    bool
	ModuleID   string
	ModuleName string
	// Approved is false for an include host the user has not confirmed;
	// such a host is not emitted.
	Approved bool
	Enabled  bool
}

// ModuleHosts lists the hostnames of the document's modules, enabled or not,
// in module order, include hosts before exclude hosts.
func ModuleHosts(doc *Doc) []ModuleHost {
	if doc == nil || doc.HTTPTools == nil {
		return nil
	}
	var out []ModuleHost
	seen := map[string]bool{}
	for _, m := range doc.HTTPTools.Modules {
		// A repeated id is skipped like the emitter does: the first stays.
		if m == nil || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		pending := m.NeedsApproval(m.Contents)
		for _, h := range NormalizeMITMHostnames(m.Contents.Hostnames.includes()).Hosts {
			out = append(out, ModuleHost{Host: h, ModuleID: m.ID, ModuleName: m.Name, Enabled: m.Enabled,
				Approved: !containsString(pending.Hostnames, h)})
		}
		for _, h := range NormalizeMITMHostnames(m.Contents.Hostnames.excludes()).Hosts {
			out = append(out, ModuleHost{Host: h, Exclude: true, ModuleID: m.ID, ModuleName: m.Name, Enabled: m.Enabled, Approved: true})
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ActiveModule is an enabled module with the contents that take effect.
type ActiveModule struct {
	Module   *HTTPModule
	Contents ModuleContents
	// SkippedHosts and SkippedScripts are the unconfirmed include hosts and
	// scripts left out of Contents.
	SkippedHosts   []string
	SkippedScripts []string
	SkippedDirect  []string
}

// ActiveModules returns the enabled modules in list order. Pending updates
// are not part of it.
func ActiveModules(tools *HTTPTools) []ActiveModule {
	if tools == nil {
		return nil
	}
	var out []ActiveModule
	seen := map[string]bool{}
	for _, m := range tools.Modules {
		// A repeated id is skipped like the emitter does: the first stays.
		if m == nil || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if !m.Enabled {
			continue
		}
		contents, hosts, scripts, direct := m.ActiveContents()
		out = append(out, ActiveModule{Module: m, Contents: contents, SkippedHosts: hosts, SkippedScripts: scripts, SkippedDirect: direct})
	}
	return out
}

// ModuleRewrites returns the rewrite rules of the active module a: copies
// with the ids "mod:<module id>:<n>" (URL, header, body, then local mapping
// rules, numbered together). Names say which module a rule comes from.
func ModuleRewrites(a ActiveModule) *HTTPRewrites {
	rw := a.Contents.Rewrites
	if rw.Empty() {
		return nil
	}
	out := &HTTPRewrites{}
	n := 0
	label := func() (string, string) {
		n++
		return ModuleItemID(a.Module.ID, n), "模块「" + a.Module.Name + "」第 " + strconv.Itoa(n) + " 条"
	}
	for _, r := range rw.URL {
		if r == nil {
			continue
		}
		c := *r
		c.ID, c.Name = label()
		out.URL = append(out.URL, &c)
	}
	for _, r := range rw.Headers {
		if r == nil {
			continue
		}
		c := *r
		c.ID, c.Name = label()
		out.Headers = append(out.Headers, &c)
	}
	for _, r := range rw.Body {
		if r == nil {
			continue
		}
		c := *r
		c.ID, c.Name = label()
		out.Body = append(out.Body, &c)
	}
	for _, r := range rw.MapLocal {
		if r == nil {
			continue
		}
		c := *r
		c.ID, c.Name = label()
		out.MapLocal = append(out.MapLocal, &c)
	}
	return out
}

// ModuleScripts returns the scripts of the active module a as copies tagged
// "<module id>-<tag>".
func ModuleScripts(a ActiveModule) []*HTTPScript {
	var out []*HTTPScript
	for _, s := range a.Contents.Scripts {
		if s == nil {
			continue
		}
		c := *s
		c.Tag = ModuleScriptTag(a.Module.ID, s.EffectiveTag())
		c.ID = c.Tag
		if c.Name == "" {
			c.Name = a.Module.Name
		}
		out = append(out, &c)
	}
	return out
}

// moduleMatchKey is the route match key of a module rule type.
func moduleMatchKey(t string) string {
	switch t {
	case ModuleRuleDomain:
		return "domain"
	case ModuleRuleDomainSuffix:
		return "domain_suffix"
	case ModuleRuleDomainKeyword:
		return "domain_keyword"
	case ModuleRuleScript:
		return "script"
	}
	return "ip_cidr"
}

// ModuleMatchKey returns the route match key of a module rule.
func ModuleMatchKey(r *ModuleRule) string { return moduleMatchKey(r.Type) }

// ModuleRouteRules returns the read-only routing rules of the enabled
// modules, in module and rule order: pre-device rules named "Module · <name>",
// ids "mod:<module id>:<n>". A reject rule has action reject; a direct rule
// has action route with an empty outbound, which the emitter replaces with
// the direct outbound. Rules the model refuses are left out (the numbering
// still counts them). DIRECT rules the user has not confirmed are left out
// as well.
func ModuleRouteRules(doc *Doc) []*RouteRule {
	if doc == nil || doc.HTTPTools == nil {
		return nil
	}
	var out []*RouteRule
	seen := map[string]bool{}
	for _, m := range doc.HTTPTools.Modules {
		// A repeated id is skipped like the emitter does: the first stays.
		if m == nil || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if !m.Enabled {
			continue
		}
		held := stringSet(m.unapprovedDirect(m.Contents))
		// A script rule needs its script to take effect: confirmed, enabled
		// and valid.
		active, _, _, _ := m.ActiveContents()
		usable := map[string]bool{}
		for _, sc := range active.Scripts {
			if sc != nil && sc.Type == ScriptRule && !sc.Disabled && sc.Issue() == "" {
				usable[sc.EffectiveTag()] = true
			}
		}
		for i, r := range m.Contents.Rules {
			if r == nil || r.Issue() != "" || r.Action == ModuleActionDirect && held[ModuleRuleKey(r)] {
				continue
			}
			value := r.Value
			if r.Type == ModuleRuleScript {
				if !usable[r.Value] {
					continue
				}
				value = ModuleScriptTag(m.ID, r.Value)
			}
			rule := &RouteRule{
				ID: ModuleItemID(m.ID, i+1), Name: ModuleRuleGroupPrefix + m.Name, Enabled: true, DeviceID: PreDeviceID,
				Match: map[string]any{moduleMatchKey(r.Type): []any{value}},
			}
			if r.Action == ModuleActionReject {
				rule.Action = ActionReject
			} else {
				rule.Action = ActionRoute
			}
			out = append(out, rule)
		}
	}
	return out
}
