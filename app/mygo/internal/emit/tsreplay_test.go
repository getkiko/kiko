package emit_test

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/model/tsgolden"
)

// emitted is the part of an emit result compared with the recorded
// TypeScript result.
type emitted struct {
	Config           map[string]any `json:"config"`
	Warnings         []string       `json:"warnings"`
	Errors           []string       `json:"errors"`
	DependencyErrors []string       `json:"dependencyErrors"`
}

var emitAdapters = map[string]tsgolden.Adapter{
	"emit.emit": func(c tsgolden.Call) (any, error) {
		var doc *model.Doc
		c.Arg(0, &doc)
		var options struct {
			NetworkConfigs      []*model.NetworkConfig    `json:"networkConfigs"`
			LocalAddresses      []string                  `json:"localAddresses"`
			LocalRuleSetCatalog model.LocalRuleSetCatalog `json:"localRuleSetCatalog"`
			LocalBasePath       string                    `json:"localBasePath"`
		}
		c.Arg(1, &options)
		r := emit.Emit(doc, emit.Options{NetworkConfigs: options.NetworkConfigs, LocalAddresses: options.LocalAddresses,
			LocalRuleSetCatalog: options.LocalRuleSetCatalog, LocalBasePath: options.LocalBasePath})
		return emitted{r.Config, r.Warnings, r.Errors, r.DependencyErrors}, nil
	},
	"emit.removeIPRange": func(c tsgolden.Call) (any, error) {
		var outer, inner string
		c.Arg(0, &outer)
		c.Arg(1, &inner)
		return model.RemoveIPRange(outer, inner), nil
	},
	"emit.tunDefaultExclude": func(c tsgolden.Call) (any, error) {
		var addresses []string
		var ipv6 bool
		c.Arg(0, &addresses)
		c.Arg(1, &ipv6)
		return model.TunDefaultExclude(addresses, ipv6), nil
	},
	"emit.tunDefaultAddress": func(c tsgolden.Call) (any, error) {
		var platform string
		var ipv6 bool
		c.Arg(0, &platform)
		c.Arg(1, &ipv6)
		return model.TunDefaultAddress(platform, ipv6), nil
	},
	"config-dependencies.configDependencyErrors": func(c tsgolden.Call) (any, error) {
		var config map[string]any
		c.Arg(0, &config)
		return emit.ConfigDependencyErrors(config), nil
	},
}

// TestTypeScriptParity replays the emitter calls recorded from the former
// TypeScript console (see package tsgolden).
func TestTypeScriptParity(t *testing.T) {
	calls := tsgolden.Load(t, "testdata/ts-calls.jsonl.gz")
	ran := tsgolden.Replay(t, calls, emitAdapters, tsgolden.Options{
		Recorded:  func(fn string, v any) any { return tsgolden.CoreWording(v) },
		Normalize: func(fn string, v any) any { return tsgolden.SortLogicalSingles(v) },
		Fixed:     emitFixes,
	})
	tsgolden.Summary(t, ran, emitAdapters)
}

// danglingOutbound returns an outbound tag the configuration references
// (group members, detours, route rules and final) without emitting it.
func danglingOutbound(config map[string]any) string {
	emitted := map[string]bool{}
	var outbounds []map[string]any
	for _, key := range []string{"outbounds", "endpoints"} {
		list, _ := config[key].([]any)
		for _, item := range list {
			if o, ok := item.(map[string]any); ok {
				tag, _ := o["tag"].(string)
				emitted[tag] = true
				outbounds = append(outbounds, o)
			}
		}
	}
	var refs []any
	for _, o := range outbounds {
		members, _ := o["outbounds"].([]any)
		refs = append(refs, members...)
		refs = append(refs, o["detour"])
	}
	route, _ := config["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	for _, rule := range rules {
		if r, ok := rule.(map[string]any); ok {
			refs = append(refs, r["outbound"])
		}
	}
	refs = append(refs, route["final"])
	for _, ref := range refs {
		if tag, ok := ref.(string); ok && !emitted[tag] {
			return tag
		}
	}
	return ""
}

func configOf(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var result struct {
		Config map[string]any `json:"config"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.Config
}

var emitFixes = []tsgolden.Fix{{
	Bug: "emit.ts skips a group without usable members but still references its tag from other groups, " +
		"detours, route rules and the default outbound; Go unresolves such groups (also through nested empty groups) " +
		"and reports the references",
	Fn: "emit.emit",
	Match: func(c tsgolden.Call) bool {
		var recorded any
		_ = json.Unmarshal(c.R, &recorded)
		return danglingOutbound(configOf(recorded)) != ""
	},
	Check: func(c tsgolden.Call, got any, err error) string {
		if err != nil {
			return err.Error()
		}
		if tag := danglingOutbound(configOf(got)); tag != "" {
			return "references the skipped outbound " + tag
		}
		return ""
	},
}, {
	Bug: "emit.ts clears a detour that points at a disabled or deleted target with a warning, so the chain " +
		"silently falls back to direct; Go keeps the configuration but reports an error that blocks applying",
	Fn: "emit.emit",
	Match: func(c tsgolden.Call) bool {
		var recorded struct {
			Warnings []string `json:"warnings"`
		}
		_ = json.Unmarshal(c.R, &recorded)
		for _, w := range recorded.Warnings {
			if strings.Contains(w, "的前置代理") && strings.HasSuffix(w, "已清除") {
				return true
			}
		}
		return false
	},
	Check: func(c tsgolden.Call, got any, err error) string {
		if err != nil {
			return err.Error()
		}
		var recorded any
		_ = json.Unmarshal(c.R, &recorded)
		if !reflect.DeepEqual(configOf(recorded), configOf(got)) {
			return "configuration changed"
		}
		result := got.(emitted)
		for _, w := range result.Warnings {
			if strings.Contains(w, "的前置代理") && strings.HasSuffix(w, "已清除") {
				return "detour still cleared with a warning: " + w
			}
		}
		for _, e := range result.Errors {
			// Detours became proxy chains: the error names the hop.
			if strings.Contains(e, "的前置代理") || strings.Contains(e, "的代理链第") {
				return ""
			}
		}
		return "no detour error"
	},
}, {
	Bug: "emit.ts names the Tailscale endpoint in its warnings (Tailscale「tag」…); Go keeps one Tailscale " +
		"endpoint with a fixed tag (2d21d9c) and words the warnings without it",
	Fn:    "emit.emit",
	Match: func(c tsgolden.Call) bool { return tailscaleNamed.Match(c.R) },
	Want: func(c tsgolden.Call, got any) json.RawMessage {
		return tailscaleNamed.ReplaceAll(c.R, []byte("Tailscale "))
	},
}}

// tailscaleNamed is the endpoint's tag in the Tailscale warnings the
// former TypeScript emitter recorded.
var tailscaleNamed = regexp.MustCompile(`Tailscale「[^」]*」`)
