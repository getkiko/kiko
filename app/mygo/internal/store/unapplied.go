package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/env"
)

// Unapplied are the parts of the document that differ from the
// configuration the core last applied.
type Unapplied struct {
	// Parts are the changed parts' names (routing rules, outbounds…), in a fixed order.
	Parts []string
	// Pages are the workspaces the parts belong to.
	Pages map[string]bool
}

// Count is the number of changed parts.
func (u Unapplied) Count() int { return len(u.Parts) }

// appliedState is the applied configuration by top-level part, and the
// classification cached for a document version.
type appliedState struct {
	known      bool
	generation int
	config     map[string]any
	// run is the core run (Runtime.StartedAt) the configuration was read
	// in, and requested the run a read was last asked for by Unapplied.
	run, requested string
	// loading a read is in flight; again another was asked for meanwhile.
	loading, again bool

	cachedVersion, cachedGeneration int
	cached                          Unapplied

	// detours are AppliedDetours of the configuration of generation
	// detoursGeneration.
	detours           map[string]string
	detoursGeneration int
}

// unappliedPart names a part of the configuration and the workspace that
// edits it. Parts of route and dns are keyed "route.rules" and so on.
type unappliedPart struct{ name, page string }

var unappliedParts = map[string]unappliedPart{
	"route.rules":    {"路由规则", env.PageRoute},
	"route.rule_set": {"规则集", env.PageRoute},
	"route.final":    {"默认出口", env.PageRoute},
	"dns.rules":      {"DNS 分流", env.PageRoute},
	"dns.final":      {"DNS 分流", env.PageRoute},
	"outbounds":      {"出站", env.PageSubscriptions},
	"endpoints":      {"端点", env.PageSubscriptions},
	"inbounds":       {"入站", env.PageSubscriptions},
	"dns.servers":    {"DNS 服务器", env.PageSettings},
	// The scripts (user and module) belong to the Rewrites workspace.
	"scripts": {"改写", env.PageRewrites},
}

// partOrder is the order parts are listed in.
var partOrder = []string{"路由规则", "规则集", "默认出口", "DNS 分流", "出站", "端点", "入站", "DNS 服务器", "改写", "设置"}

// splitParts flattens a configuration into its parts: route and dns by
// their keys, everything else by its top-level key. The rule sets leave
// out the local source set, the host's interface addresses that emitting
// detects: they change as the core brings up its TUN interface, without
// an edit, and every apply refreshes them.
func splitParts(config map[string]any) map[string]any {
	parts := map[string]any{}
	for key, value := range config {
		if object, ok := value.(map[string]any); ok && (key == "route" || key == "dns") {
			for sub, v := range object {
				if key == "route" && sub == "rule_set" {
					v = withoutLocalSource(v)
				}
				parts[key+"."+sub] = v
			}
			continue
		}
		parts[key] = value
	}
	return parts
}

// localSourcePrefix starts the tag of the rule set of the host's
// addresses (emit's kiko-local-source, kiko-local-source-2…).
const localSourcePrefix = "kiko-local-source"

// withoutLocalSource is a rule set list without the local source set.
func withoutLocalSource(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(list))
	for _, item := range list {
		if set, ok := item.(map[string]any); ok {
			if tag, _ := set["tag"].(string); strings.HasPrefix(tag, localSourcePrefix) {
				continue
			}
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// legacyTagPrefixes are the tags emit generated before the rename to Kiko
// (sbcfg-local-source, sbcfg-default-dns and their numbered forms); a
// configuration applied by an earlier release still carries them.
var legacyTagPrefixes = []string{"sbcfg-local-source", "sbcfg-default-dns"}

// renameLegacyTags returns v with every pre-rename generated tag given its
// current kiko- name, wherever the tag or a reference to it appears.
func renameLegacyTags(v any) any {
	switch v := v.(type) {
	case string:
		for _, prefix := range legacyTagPrefixes {
			if v == prefix || strings.HasPrefix(v, prefix+"-") {
				return "kiko" + strings.TrimPrefix(v, "sbcfg")
			}
		}
		return v
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = renameLegacyTags(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = renameLegacyTags(item)
		}
		return out
	}
	return v
}

// classifyUnapplied compares two configurations part by part. The applied
// one may come from a release before the rename to Kiko; its generated tags
// are normalized first.
func classifyUnapplied(current, applied map[string]any) Unapplied {
	normalized, _ := renameLegacyTags(applied).(map[string]any)
	a, b := splitParts(current), splitParts(normalized)
	names := map[string]bool{}
	u := Unapplied{Pages: map[string]bool{}}
	visit := func(key string) {
		if reflect.DeepEqual(a[key], b[key]) {
			return
		}
		part, ok := unappliedParts[key]
		if !ok {
			part = unappliedPart{"设置", env.PageSettings}
		}
		names[part.name] = true
		u.Pages[part.page] = true
	}
	for key := range a {
		visit(key)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			visit(key)
		}
	}
	for _, name := range partOrder {
		if names[name] {
			u.Parts = append(u.Parts, name)
		}
	}
	return u
}

// Unapplied classifies the document's changes against the applied
// configuration by workspace. Before the applied configuration is known,
// and while the core is not running, it reports none: a stopped core takes
// the document as it is when it starts.
func (s *Store) Unapplied() Unapplied {
	st := &s.applied
	server := s.env.Server
	if !st.known || !s.Loaded || server == nil || !server.HasRuntime || !server.Runtime.Running() {
		return Unapplied{Pages: map[string]bool{}}
	}
	// A configuration read in an earlier run is the last run's: a core
	// just started compares against it only once read again, or the
	// changes card flashes on and off as it starts.
	if run := server.Runtime.StartedAt; st.run != run {
		if st.requested != run {
			st.requested = run
			s.RefreshApplied()
		}
		return Unapplied{Pages: map[string]bool{}}
	}
	if st.cachedVersion == s.version+1 && st.cachedGeneration == st.generation {
		return st.cached
	}
	var current map[string]any
	if result := s.Emit(); len(result.Errors) == 0 {
		_ = json.Unmarshal(result.JSON, &current)
	}
	st.cached = classifyUnapplied(current, st.config)
	st.cachedVersion, st.cachedGeneration = s.version+1, st.generation
	return st.cached
}

// AppliedGen counts the reads of the applied configuration: it changes
// after each apply, for views of what the core runs to load again.
func (s *Store) AppliedGen() int { return s.applied.generation }

// RefreshApplied reads the configuration the core last applied.
func (s *Store) RefreshApplied() {
	if s.applied.loading {
		s.applied.again = true
		return
	}
	s.applied.loading = true
	run := ""
	if s.env.Server != nil {
		run = s.env.Server.Runtime.StartedAt
	}
	env.Go(s.env, func(ctx context.Context) (map[string]any, error) {
		raw, err := s.env.API.AppliedConfig(ctx)
		if err != nil {
			return nil, err
		}
		config := map[string]any{}
		return config, json.Unmarshal(raw, &config)
	}, func(config map[string]any, err error) {
		s.applied.loading = false
		if err == nil {
			s.applied.known, s.applied.config, s.applied.run = true, config, run
			s.applied.generation++
		}
		if s.applied.again {
			s.applied.again = false
			s.RefreshApplied()
		}
	})
}
