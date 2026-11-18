package model

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the built-in rule set catalog, rule set references and
// fetching built-in sets. The native console is a desktop client of
// kiko-server, so the Android-only paths (APK catalog, local files) are
// not used; they are kept for the emitter's options.

// BuiltinRuleSet is a rule set of the meta-rules-dat catalog.
type BuiltinRuleSet struct {
	Tag      string
	Path     string
	FileName string
	Desc     string
}

// BuiltinRuleSets is the built-in catalog.
var BuiltinRuleSets = []BuiltinRuleSet{
	{Tag: "Gfw", Path: "geo/geosite/gfw", FileName: "gfw", Desc: "被墙域名"},
	{Tag: "Telegram", Path: "geo-lite/geoip/telegram", FileName: "telegram", Desc: "Telegram IP 段"},
	{Tag: "geosite-cn", Path: "geo/geosite/cn", FileName: "geosite-cn", Desc: "中国大陆域名"},
	{Tag: "geoip-cn", Path: "geo/geoip/cn", FileName: "geoip-cn", Desc: "中国大陆 IP"},
	{Tag: "geosite-ads", Path: "geo/geosite/category-ads-all", FileName: "geosite-ads", Desc: "广告域名"},
	{Tag: "openai", Path: "geo/geosite/openai", FileName: "openai", Desc: "OpenAI"},
	{Tag: "geosite:youtube", Path: "geo/geosite/youtube", FileName: "youtube", Desc: "YouTube"},
}

// RuleSetMirrors build a catalog file's URL on its mirrors; the first is
// used.
var RuleSetMirrors = []func(path string) string{
	func(path string) string {
		return "https://v4.gh-proxy.org/https://github.com/MetaCubeX/meta-rules-dat/raw/sing/" + path
	},
	func(path string) string {
		return "https://v4.gh-proxy.org/https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/" + path
	},
	func(path string) string { return "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/" + path },
}

// RuleSetOption is a rule set offered in pickers.
type RuleSetOption struct {
	Value  string
	Label  string
	Source string // builtin or imported
}

// RuleSetTemplate is a remote rule set offered when adding one.
type RuleSetTemplate struct {
	Tag string
	URL string
}

const metaRulesDat = "https://github.com/MetaCubeX/meta-rules-dat/raw/sing/"

// RuleSetTemplates are the remote rule sets offered when adding one.
var RuleSetTemplates = []RuleSetTemplate{
	{Tag: "Google", URL: metaRulesDat + "geo/geosite/google.srs"},
	{Tag: "Sony", URL: metaRulesDat + "geo/geosite/sony.srs"},
	{Tag: "Youtube", URL: metaRulesDat + "geo/geosite/youtube.srs"},
	{Tag: "Openai", URL: metaRulesDat + "geo/geosite/openai.srs"},
	{Tag: "Cursor", URL: metaRulesDat + "geo/geosite/cursor.srs"},
	{Tag: "Gfw", URL: metaRulesDat + "geo/geosite/gfw.srs"},
	{Tag: "Apple", URL: metaRulesDat + "geo/geosite/apple.srs"},
	{Tag: "CnSite", URL: metaRulesDat + "geo-lite/geosite/cn.srs"},
	{Tag: "CnIp", URL: metaRulesDat + "geo-lite/geoip/cn.srs"},
	{Tag: "Telegram", URL: metaRulesDat + "geo-lite/geoip/telegram.srs"},
}

// LocalRuleSetEntry is a rule set file an app bundles (Android).
type LocalRuleSetEntry struct {
	Path   string `json:"path"`
	Format string `json:"format"` // binary or source
}

// LocalRuleSetCatalog maps tags to bundled rule set files.
type LocalRuleSetCatalog = map[string]LocalRuleSetEntry

// FindBuiltinRuleSet returns the catalog entry of tag.
func FindBuiltinRuleSet(tag string) (BuiltinRuleSet, bool) {
	for _, r := range BuiltinRuleSets {
		if r.Tag == tag {
			return r, true
		}
	}
	return BuiltinRuleSet{}, false
}

// IsBuiltinRuleSet reports a catalog tag.
func IsBuiltinRuleSet(tag string) bool {
	_, ok := FindBuiltinRuleSet(tag)
	return ok
}

// BuiltinRuleSetID is the id of a materialized built-in rule set.
func BuiltinRuleSetID(tag string) string { return "builtin_" + tag }

// BuiltinRuleSetForEntry returns the catalog entry a stored rule set
// stands for, by tag or id.
func BuiltinRuleSetForEntry(ruleSet *RuleSet) (BuiltinRuleSet, bool) {
	for _, r := range BuiltinRuleSets {
		if r.Tag == ruleSet.Tag || BuiltinRuleSetID(r.Tag) == ruleSet.ID {
			return r, true
		}
	}
	return BuiltinRuleSet{}, false
}

// DefaultBuiltinRuleSet is a catalog entry as a remote rule set.
func DefaultBuiltinRuleSet(r BuiltinRuleSet) *RuleSet {
	return &RuleSet{ID: BuiltinRuleSetID(r.Tag), Tag: r.Tag, Enabled: true, Mode: "remote", Path: r.Path,
		URL: BuiltinRuleSetURL(r.Tag, "srs"), UpdateInterval: "24h0m0s"}
}

// BuiltinRuleSetURL is a catalog entry's file URL, format "json" or
// "srs"; "" when tag is not in the catalog.
func BuiltinRuleSetURL(tag, format string) string {
	r, ok := FindBuiltinRuleSet(tag)
	if !ok {
		return ""
	}
	if format == "" {
		format = "json"
	}
	return RuleSetMirrors[0](r.Path + "." + format)
}

// BuiltinLocalRuleSetPath is a catalog entry's bundled file path.
func BuiltinLocalRuleSetPath(tag string) string {
	r, ok := FindBuiltinRuleSet(tag)
	if !ok {
		return ""
	}
	return "rulesets/builtin/" + r.FileName + ".srs"
}

var reUnsafeFileName = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// CustomLocalRuleSetPath is the local file of a custom rule set.
func CustomLocalRuleSetPath(ruleSet *RuleSet) string {
	return "rulesets/custom/" + reUnsafeFileName.ReplaceAllString(ruleSet.ID, "_") + ".json"
}

// MaterializeBuiltinRuleSets adds inline entries for the catalog tags not
// yet in ruleSets.
func MaterializeBuiltinRuleSets(ruleSets []*RuleSet, tags []string) []*RuleSet {
	next := append([]*RuleSet{}, ruleSets...)
	existing := map[string]bool{}
	for _, r := range next {
		existing[r.Tag] = true
	}
	for _, tag := range tags {
		builtin, ok := FindBuiltinRuleSet(tag)
		if !ok || existing[tag] {
			continue
		}
		next = append(next, &RuleSet{ID: BuiltinRuleSetID(tag), Tag: tag, Enabled: true, Mode: "inline",
			Path: builtin.Path, URL: BuiltinRuleSetURL(tag, ""), UpdateInterval: "24h0m0s"})
		existing[tag] = true
	}
	return next
}

// Fetcher fetches a URL through kiko-server (POST /api/v1/fetch).
// *api.Client implements it.
type Fetcher interface {
	Fetch(ctx context.Context, url string, headers map[string]string) (api.FetchResult, error)
}

// FetchBuiltinRuleSet downloads a catalog entry's JSON source and returns
// its rules, nil when it fails.
func FetchBuiltinRuleSet(ctx context.Context, fetcher Fetcher, tag string) []any {
	u := BuiltinRuleSetURL(tag, "")
	if u == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := fetcher.Fetch(ctx, u, nil)
	if err != nil || result.Status < 200 || result.Status > 299 {
		return nil
	}
	var content struct {
		Rules []any `json:"rules"`
	}
	if json.Unmarshal([]byte(result.Body), &content) != nil {
		return nil
	}
	return content.Rules
}

// PrepareBuiltinRuleSets fetches the built-in rule sets the document uses
// but has not cached, storing them inline. failed lists the tags that
// could not be fetched. Run it off the UI thread.
func PrepareBuiltinRuleSets(ctx context.Context, fetcher Fetcher, doc *Doc) (*Doc, []string) {
	var pending []string
	for _, tag := range UsedRuleSetTags(doc) {
		if !IsBuiltinRuleSet(tag) {
			continue
		}
		var cached *RuleSet
		for _, r := range doc.Route.RuleSets {
			if r.Tag == tag {
				cached = r
				break
			}
		}
		if cached != nil && (cached.Mode == "remote" || len(cached.Rules) > 0) {
			continue
		}
		pending = append(pending, tag)
	}
	failed := []string{}
	if len(pending) == 0 {
		return doc, failed
	}
	type loaded struct {
		rules []any
		at    int64
	}
	results := make([]loaded, len(pending))
	var wg sync.WaitGroup
	for i, tag := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = loaded{rules: FetchBuiltinRuleSet(ctx, fetcher, tag), at: time.Now().UnixMilli()}
		}()
	}
	wg.Wait()
	byTag := map[string]loaded{}
	var order []string
	for i, tag := range pending {
		if len(results[i].rules) > 0 {
			byTag[tag] = results[i]
			order = append(order, tag)
		} else {
			failed = append(failed, tag)
		}
	}
	if len(byTag) == 0 {
		return doc, failed
	}
	existing := map[string]bool{}
	ruleSets := make([]*RuleSet, 0, len(doc.Route.RuleSets)+len(order))
	for _, r := range doc.Route.RuleSets {
		existing[r.Tag] = true
		if result, ok := byTag[r.Tag]; ok {
			copied := *r
			copied.Enabled, copied.Mode, copied.Rules, copied.LastUpdatedAt = true, "inline", result.rules, result.at
			r = &copied
		}
		ruleSets = append(ruleSets, r)
	}
	for _, tag := range order {
		if existing[tag] {
			continue
		}
		result := byTag[tag]
		ruleSets = append(ruleSets, &RuleSet{ID: BuiltinRuleSetID(tag), Tag: tag, Enabled: true, Mode: "inline", Rules: result.rules, LastUpdatedAt: result.at})
	}
	next := *doc
	next.Route.RuleSets = ruleSets
	return &next, failed
}

// IsImportedRuleSet reports an enabled rule set with a tag.
func IsImportedRuleSet(ruleSet *RuleSet) bool {
	return ruleSet.Enabled && strings.TrimSpace(ruleSet.Tag) != ""
}

// LocalRuleSetFile is a custom rule set written to a local file (Android).
type LocalRuleSetFile struct {
	Path    string `json:"path"`
	Content struct {
		Version int   `json:"version"`
		Rules   []any `json:"rules"`
	} `json:"content"`
}

// LocalRuleSetPayload lists the custom rule set files of a document.
func LocalRuleSetPayload(doc *Doc) []LocalRuleSetFile {
	used := map[string]bool{}
	for _, tag := range UsedRuleSetTags(doc) {
		used[tag] = true
	}
	out := []LocalRuleSetFile{}
	for _, r := range doc.Route.RuleSets {
		if !used[r.Tag] || !IsImportedRuleSet(r) || r.Rules == nil {
			continue
		}
		var file LocalRuleSetFile
		file.Path = CustomLocalRuleSetPath(r)
		file.Content.Version = r.SourceVersion
		if file.Content.Version == 0 {
			file.Content.Version = 2
		}
		file.Content.Rules = r.Rules
		out = append(out, file)
	}
	return out
}

// RuleSetOptions lists the rule sets a picker offers: the catalog, then
// the imported ones.
func RuleSetOptions(ruleSets []*RuleSet) []RuleSetOption {
	var options []RuleSetOption
	used := map[string]bool{}
	for _, r := range BuiltinRuleSets {
		options = append(options, RuleSetOption{Value: r.Tag, Label: r.Tag + " · " + r.Desc, Source: "builtin"})
		used[r.Tag] = true
	}
	for _, r := range ruleSets {
		if !IsImportedRuleSet(r) || used[r.Tag] {
			continue
		}
		used[r.Tag] = true
		label := r.Tag + " · 用户导入"
		if IsCustomRuleSet(r) {
			label = r.Tag + " · 自建"
		}
		options = append(options, RuleSetOption{Value: r.Tag, Label: label, Source: "imported"})
	}
	return options
}

// RuleSetValues reads a rule_set value: an array, or a comma or newline
// separated string.
func RuleSetValues(value any) []string { return Values(value) }

// UsedRuleSetTags lists the rule set tags enabled rules and inbounds
// reference, in order of first use.
func UsedRuleSetTags(doc *Doc) []string {
	tags := []string{}
	seen := map[string]bool{}
	add := func(value any) {
		for _, tag := range RuleSetValues(value) {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	addRule := func(enabled bool, tree *Condition, match map[string]any) {
		if !enabled {
			return
		}
		if tree != nil {
			for _, m := range ConditionMatches(tree) {
				add(m["rule_set"])
			}
		} else {
			add(match["rule_set"])
		}
	}
	for _, r := range doc.Route.Rules {
		addRule(r.Enabled, r.ConditionTree, r.Match)
	}
	for _, r := range doc.DNS.Rules {
		addRule(r.Enabled, r.ConditionTree, r.Match)
	}
	for _, in := range doc.Inbounds {
		if in.Enabled {
			add(in.Fields["route_address_set"])
			add(in.Fields["route_exclude_address_set"])
		}
	}
	return tags
}

// ValidateRuleSetURL checks a rule set link for a mode ("inline" needs a
// JSON source).
func ValidateRuleSetURL(value, mode string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "必填"
	}
	invalid := "规则集链接必须是 HTTP(S) 的 .json 或 .srs 地址"
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return invalid
	}
	path := strings.ToLower(u.Path)
	binary, source := strings.HasSuffix(path, ".srs"), strings.HasSuffix(path, ".json")
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" || !binary && !source {
		return invalid
	}
	if mode == "inline" && !source {
		return "内联规则集需要 HTTP(S) 的 .json 地址"
	}
	return ""
}

// UpdateRuleSetSource applies a patch; changing the mode or URL drops the
// cached rules.
func UpdateRuleSetSource(ruleSet *RuleSet, patch func(*RuleSet)) *RuleSet {
	next := *ruleSet
	patch(&next)
	if next.Mode != ruleSet.Mode || next.URL != ruleSet.URL {
		next.Rules, next.SourceVersion, next.LastUpdatedAt = nil, 0, 0
	}
	return &next
}

var (
	reDurationPart  = regexp.MustCompile(`\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)`)
	reDurationParse = regexp.MustCompile(`^(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)$`)
)

// ValidateRuleSetUpdateInterval checks an update interval as a Go
// duration within sing-box's range.
func ValidateRuleSetUpdateInterval(value string) string {
	raw := strings.TrimSpace(value)
	parts := reDurationPart.FindAllString(raw, -1)
	if raw == "" || len([]rune(raw)) > 128 || parts == nil || strings.Join(parts, "") != raw {
		return "请填写有效时长，例如 24h 或 12h30m"
	}
	units := map[string]float64{"ns": 1, "us": 1e3, "µs": 1e3, "ms": 1e6, "s": 1e9, "m": 6e10, "h": 3.6e12}
	total := 0.0
	for _, part := range parts {
		m := reDurationParse.FindStringSubmatch(part)
		n, _ := strconv.ParseFloat(m[1], 64)
		total += math.Trunc(n * units[m[2]])
	}
	if jsv.IsFinite(total) && total > 0 && total <= 9223372036854775807 {
		return ""
	}
	return "更新间隔需要大于 0，且须在核心支持范围内"
}

// RenameRuleSetReferences returns doc with every reference to the rule
// set tag previous renamed to next: the rule_set conditions of traffic and
// DNS rules (flat matches and condition trees) and the TUN inbounds'
// route_address_set and route_exclude_address_set. It does not modify doc.
func RenameRuleSetReferences(doc *Doc, previous, next string) *Doc {
	if previous == "" || previous == next {
		return doc
	}
	rename := func(value any) (any, bool) {
		values := RuleSetValues(value)
		if !slices.Contains(values, previous) {
			return value, false
		}
		renamed := make([]any, len(values))
		for i, v := range values {
			if v == previous {
				v = next
			}
			renamed[i] = v
		}
		return renamed, true
	}
	renameMatch := func(match map[string]any) map[string]any {
		value, changed := rename(match["rule_set"])
		if !changed {
			return match
		}
		out := make(map[string]any, len(match))
		for k, v := range match {
			out[k] = v
		}
		out["rule_set"] = value
		return out
	}
	out := *doc
	out.Route.Rules = make([]*RouteRule, len(doc.Route.Rules))
	for i, r := range doc.Route.Rules {
		c := *r
		c.Match = renameMatch(r.Match)
		if r.ConditionTree != nil {
			c.ConditionTree = MapCondition(r.ConditionTree, renameMatch)
		}
		out.Route.Rules[i] = &c
	}
	out.DNS.Rules = make([]*DNSRule, len(doc.DNS.Rules))
	for i, r := range doc.DNS.Rules {
		c := *r
		c.Match = renameMatch(r.Match)
		if r.ConditionTree != nil {
			c.ConditionTree = MapCondition(r.ConditionTree, renameMatch)
		}
		out.DNS.Rules[i] = &c
	}
	out.Inbounds = make([]*Inbound, len(doc.Inbounds))
	for i, in := range doc.Inbounds {
		out.Inbounds[i] = in
		var fields Fields
		for _, key := range []string{"route_address_set", "route_exclude_address_set"} {
			value, changed := rename(in.Fields[key])
			if !changed {
				continue
			}
			if fields == nil {
				fields = cloneFields(in.Fields)
			}
			fields[key] = value
		}
		if fields != nil {
			c := *in
			c.Fields = fields
			out.Inbounds[i] = &c
		}
	}
	return &out
}
