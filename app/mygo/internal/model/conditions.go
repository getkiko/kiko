package model

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the condition editors' helpers: process conditions,
// process paths, typed matches and IP versions.

// ---- processes ----

// ProcessFields are the process condition fields.
var ProcessFields = []string{"process_name", "process_path", "process_path_regex"}

// Process selection sources and types.
const (
	ProcessSourceApplication = "application"
	ProcessSourceProcess     = "process"
	ProcessSourceCustom      = "custom"

	ProcessTypeName  = "name"
	ProcessTypePath  = "path"
	ProcessTypeRegex = "regex"
)

// ProcessSelection is a process chosen in the editor: an application, a
// running process or a custom value, matched by name, path or regex.
type ProcessSelection struct {
	Source string `json:"source"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Label  string `json:"label,omitempty"`
}

// ProcessTag is a selection found in a tree, with where it is.
type ProcessTag struct {
	ProcessSelection
	Field    string `json:"field"`
	Path     []int  `json:"path"`
	Index    int    `json:"index"`
	Excluded bool   `json:"excluded"`
}

func processEntries(value any) []string {
	list, ok := jsv.Array(value)
	if !ok {
		list = []any{value}
	}
	out := []string{}
	for _, v := range list {
		if v == nil {
			continue
		}
		if s := strings.TrimSpace(jsv.String(v)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func processFieldFor(typ string) string {
	switch typ {
	case ProcessTypeName:
		return "process_name"
	case ProcessTypePath:
		return "process_path"
	}
	return "process_path_regex"
}

func processTypeOf(field string) string {
	switch field {
	case "process_name":
		return ProcessTypeName
	case "process_path":
		return ProcessTypePath
	}
	return ProcessTypeRegex
}

// SelectionField is the condition field a selection matches.
func SelectionField(item ProcessSelection) string {
	if item.Source == ProcessSourceApplication {
		return "process_path_regex"
	}
	return processFieldFor(item.Type)
}

var reTrailingSlashes = regexp.MustCompile(`/+$`)

// SelectionValue is the value a selection matches; an application matches
// everything inside its bundle.
func SelectionValue(item ProcessSelection) string {
	if item.Source == ProcessSourceApplication {
		return ProcessPathRegex(reTrailingSlashes.ReplaceAllString(item.Value, ""), true)
	}
	return strings.TrimSpace(item.Value)
}

// ProcessSelectionKey identifies a selection by what it matches.
func ProcessSelectionKey(item ProcessSelection) string {
	return SelectionField(item) + ":" + SelectionValue(item)
}

var rePathSeparators = regexp.MustCompile(`[\\/]`)

// ProcessSelectionLabel names a selection.
func ProcessSelectionLabel(item ProcessSelection) string {
	if item.Label != "" {
		return item.Label
	}
	if item.Type == ProcessTypePath {
		parts := rePathSeparators.Split(item.Value, -1)
		if last := parts[len(parts)-1]; last != "" {
			return last
		}
	}
	return item.Value
}

// ProcessSelectionType names a selection's kind.
func ProcessSelectionType(item ProcessSelection) string {
	switch {
	case item.Source == ProcessSourceApplication:
		return "整个应用"
	case item.Type == ProcessTypeName:
		return "名称"
	case item.Type == ProcessTypePath:
		return "路径"
	}
	return "正则"
}

// ProcessSelectionLeaf is the condition of a selection.
func ProcessSelectionLeaf(item ProcessSelection) *Condition {
	ui := &UIProcess{Source: item.Source, Label: item.Label}
	if item.Source == ProcessSourceApplication {
		ui.AppPath = item.Value
	}
	return &Condition{Kind: ConditionMatch, Match: map[string]any{SelectionField(item): []any{SelectionValue(item)}}, UIProcess: ui}
}

// EmptyProcessCondition is a new process selector.
func EmptyProcessCondition() *Condition {
	return &Condition{Kind: ConditionGroup, UIField: "process", Mode: "or", Rules: []*Condition{}}
}

var reWindowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// ProcessPathRegex is the regex matching a path; directory matches
// everything inside it. Windows paths match case-insensitively with either
// separator.
func ProcessPathRegex(value string, directory bool) string {
	windows := reWindowsPath.MatchString(value) || strings.HasPrefix(value, `\\`)
	prefix, path, suffix := "^", regexp.QuoteMeta(value), "$"
	if windows {
		prefix = "(?i)^"
		parts := rePathSeparators.Split(value, -1)
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		path = strings.Join(parts, `[\\/]`)
	}
	if directory {
		suffix = "/"
		if windows {
			suffix = `[\\/]`
		}
	}
	return prefix + path + suffix
}

// NormalizeProcessConditions splits the independent process predicates of
// a tree into process selectors, keeping compound core fields and
// inversion.
func NormalizeProcessConditions(tree *Condition) *Condition {
	if tree.UIField != "" {
		return cloneCondition(tree)
	}
	next := cloneCondition(tree)
	next.Rules = []*Condition{}
	var processNodes []*Condition
	for _, node := range tree.Rules {
		if node.Kind == ConditionGroup {
			normalized := NormalizeProcessConditions(node)
			if normalized.UIField == "process" {
				processNodes = append(processNodes, normalized)
			} else {
				next.Rules = append(next.Rules, normalized)
			}
			continue
		}
		var fields []string
		for _, f := range ProcessFields {
			if _, ok := node.Match[f]; ok {
				fields = append(fields, f)
			}
		}
		if len(fields) == 0 {
			next.Rules = append(next.Rules, cloneCondition(node))
			continue
		}
		remaining := map[string]any{}
		for k, v := range node.Match {
			if !slices.Contains(fields, k) {
				remaining[k] = v
			}
		}
		mode := "or"
		if len(fields) > 1 {
			mode = "and"
		}
		process := &Condition{Kind: ConditionGroup, UIField: "process", Mode: mode}
		for _, f := range fields {
			leaf := &Condition{Kind: ConditionMatch, Match: map[string]any{f: jsv.Clone(node.Match[f])}}
			if node.UIProcess != nil {
				p := *node.UIProcess
				leaf.UIProcess = &p
			}
			process.Rules = append(process.Rules, leaf)
		}
		if len(remaining) > 0 {
			rest := cloneCondition(node)
			rest.Match = remaining
			rest.Invert = false
			rest.UIProcess = nil
			if tree.Mode == "and" && !node.Invert {
				next.Rules = append(next.Rules, rest)
				processNodes = append(processNodes, process)
			} else {
				next.Rules = append(next.Rules, &Condition{Kind: ConditionGroup, Mode: "and", Invert: node.Invert, Rules: []*Condition{rest, process}})
			}
		} else {
			process.Invert = node.Invert
			processNodes = append(processNodes, process)
		}
	}
	if len(processNodes) == 1 {
		next.Rules = append(next.Rules, processNodes[0])
	} else if len(processNodes) > 1 {
		next.Rules = append(next.Rules, &Condition{Kind: ConditionGroup, UIField: "process", Mode: tree.Mode, Rules: processNodes})
	}
	return next
}

// ProcessTags lists the process selections of a tree.
func ProcessTags(group *Condition) []ProcessTag {
	result := []ProcessTag{}
	var walk func(node *Condition, path []int, excluded bool)
	walk = func(node *Condition, path []int, excluded bool) {
		excluded = excluded != node.Invert
		if node.Kind == ConditionGroup {
			for i, child := range node.Rules {
				walk(child, append(slices.Clone(path), i), excluded)
			}
			return
		}
		for _, field := range ProcessFields {
			for index, value := range processEntries(node.Match[field]) {
				source := ProcessSourceCustom
				label := ""
				if node.UIProcess != nil {
					if node.UIProcess.Source != "" {
						source = node.UIProcess.Source
					}
					label = node.UIProcess.Label
				}
				if source == ProcessSourceApplication && node.UIProcess != nil && node.UIProcess.AppPath != "" {
					value = node.UIProcess.AppPath
				}
				result = append(result, ProcessTag{
					ProcessSelection: ProcessSelection{Source: source, Type: processTypeOf(field), Value: value, Label: label},
					Field:            field, Path: path, Index: index, Excluded: excluded,
				})
			}
		}
	}
	walk(group, []int{}, false)
	return result
}

// pruneEmpty drops empty leaves and groups below node.
func pruneEmpty(node *Condition) {
	kept := []*Condition{}
	for _, child := range node.Rules {
		if child.Kind == ConditionGroup {
			pruneEmpty(child)
			if len(child.Rules) > 0 {
				kept = append(kept, child)
			}
		} else if len(child.Match) > 0 {
			kept = append(kept, child)
		}
	}
	node.Rules = kept
}

// conditionAt returns the parent group and index of the node at path, or
// nil when the path does not lead to a node.
func conditionAt(root *Condition, path []int) (*Condition, int) {
	if len(path) == 0 {
		return nil, -1
	}
	parent := root
	for _, index := range path[:len(path)-1] {
		if index < 0 || index >= len(parent.Rules) || parent.Rules[index].Kind != ConditionGroup {
			return nil, -1
		}
		parent = parent.Rules[index]
	}
	at := path[len(path)-1]
	if at < 0 || at >= len(parent.Rules) {
		return nil, -1
	}
	return parent, at
}

// ChangeProcessTag removes a selection, or replaces it when replacement
// is set, editing the original leaf in place.
func ChangeProcessTag(group *Condition, tag ProcessTag, replacement *ProcessSelection) *Condition {
	next := cloneCondition(group)
	parent, at := conditionAt(next, tag.Path)
	if parent == nil || parent.Rules[at].Kind != ConditionMatch {
		return group
	}
	leaf := parent.Rules[at]
	values := processEntries(leaf.Match[tag.Field])
	several := len(values) > 1
	if tag.Index >= 0 && tag.Index < len(values) {
		values = slices.Delete(values, tag.Index, tag.Index+1)
	}
	if replacement != nil && several {
		kept := *leaf
		kept.Invert = false
		kept.Match = jsv.CloneMap(leaf.Match)
		kept.Match[tag.Field] = anyStrings(values)
		parent.Rules[at] = &Condition{Kind: ConditionGroup, Mode: "or", Invert: leaf.Invert, Rules: []*Condition{&kept, ProcessSelectionLeaf(*replacement)}}
	} else {
		if len(values) > 0 {
			leaf.Match[tag.Field] = anyStrings(values)
		} else if delete(leaf.Match, tag.Field); len(leaf.Match) == 0 {
			parent.Rules = slices.Delete(parent.Rules, at, at+1)
		}
		if replacement != nil {
			added := ProcessSelectionLeaf(*replacement)
			added.Invert = leaf.Invert
			parent.Rules = slices.Insert(parent.Rules, at, added)
		}
	}
	pruneEmpty(next)
	return next
}

// AddProcessSelection adds a selection to a process selector, keeping a
// legacy list's alternatives within the same field.
func AddProcessSelection(group *Condition, item ProcessSelection) *Condition {
	key := ProcessSelectionKey(item)
	for _, tag := range ProcessTags(group) {
		if ProcessSelectionKey(tag.ProcessSelection) == key {
			return group
		}
	}
	next := cloneCondition(group)
	if next.Mode == "and" {
		field := SelectionField(item)
		for i, node := range next.Rules {
			if _, ok := node.Match[field]; node.Kind == ConditionMatch && !node.Invert && ok {
				next.Rules[i] = &Condition{Kind: ConditionGroup, Mode: "or", Rules: []*Condition{node, ProcessSelectionLeaf(item)}}
				return next
			}
		}
	}
	next.Rules = append(next.Rules, ProcessSelectionLeaf(item))
	return next
}

// ---- typed matches ----

// Typed match kinds: the editors that own a group of fields.
const (
	TypedIP         = "ip"
	TypedWiFi       = "wifi"
	TypedPort       = "port"
	TypedSourcePort = "source_port"
	TypedDomain     = "domain"
)

// MatchValue is a value of a typed field: a string, or true for
// ip_is_private.
type MatchValue struct {
	Field string `json:"field"`
	Value any    `json:"value"`
	Label string `json:"label,omitempty"`
}

// MatchToken is a value found in a tree, with where it is.
type MatchToken struct {
	MatchValue
	Path     []int  `json:"path"`
	Index    int    `json:"index"`
	Excluded bool   `json:"excluded"`
	ID       string `json:"id"`
}

func typedList(value any) []string {
	list, ok := jsv.Array(value)
	if !ok {
		if value == nil {
			return []string{}
		}
		list = []any{value}
	}
	out := []string{}
	for _, v := range list {
		if s := strings.TrimSpace(jsv.String(v)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TypedFields are the fields a typed kind owns.
func TypedFields(kind string) []string {
	switch kind {
	case TypedIP:
		return []string{"ip_cidr", "ip_is_private", "rule_set"}
	case TypedWiFi:
		return []string{"wifi_ssid", "wifi_bssid"}
	case TypedDomain:
		return []string{"domain_suffix", "domain", "domain_regex", "domain_keyword"}
	}
	return []string{kind, kind + "_range"}
}

func ownedLeaf(leaf *Condition, kind string, inside bool) bool {
	for _, field := range TypedFields(kind) {
		if _, ok := leaf.Match[field]; ok && (field != "rule_set" || inside || leaf.UIField == "ip_cidr") {
			return true
		}
	}
	return false
}

func hasInnerExclusion(node *Condition) bool {
	return node.Invert || node.Kind == ConditionGroup && slices.ContainsFunc(node.Rules, hasInnerExclusion)
}

// MatchExclusion reports whether a kind's predicates are all excluded, and
// whether exclusion is mixed.
func MatchExclusion(group *Condition, kind string) (excluded, mixed bool) {
	var nodes []*Condition
	for _, node := range group.Rules {
		if node.Kind == ConditionGroup && node.UIField == kind || node.Kind != ConditionGroup && ownedLeaf(node, kind, false) {
			nodes = append(nodes, node)
		}
	}
	all, some := true, false
	for _, node := range nodes {
		all = all && node.Invert
		some = some || node.Invert
		if node.Kind == ConditionGroup && !node.Invert && slices.ContainsFunc(node.Rules, hasInnerExclusion) {
			mixed = true
		}
	}
	excluded = len(nodes) > 0 && all
	mixed = mixed || some && !all
	return excluded, mixed
}

// SetMatchExclusion excludes or includes a kind's predicates.
func SetMatchExclusion(group *Condition, kind string, excluded bool) *Condition {
	next := cloneCondition(group)
	fields := TypedFields(kind)
	var rules []*Condition
	for _, node := range next.Rules {
		if node.Kind == ConditionGroup {
			if node.UIField == kind {
				node.Invert = excluded
			}
			rules = append(rules, node)
			continue
		}
		if !ownedLeaf(node, kind, false) {
			rules = append(rules, node)
			continue
		}
		match, remaining := map[string]any{}, map[string]any{}
		for k, v := range node.Match {
			if slices.Contains(fields, k) {
				match[k] = v
			} else {
				remaining[k] = v
			}
		}
		if len(remaining) > 0 {
			rest, owned := *node, *node
			rest.Match = remaining
			owned.Match, owned.Invert = match, excluded
			rules = append(rules, &rest, &owned)
		} else {
			node.Invert = excluded
			rules = append(rules, node)
		}
	}
	if rules == nil {
		rules = []*Condition{}
	}
	next.Rules = rules
	return next
}

// NormalizeTypedMatches gathers simple legacy IP, Wi-Fi and domain leaves
// into their typed groups, keeping compound core predicates intact.
func NormalizeTypedMatches(tree *Condition) *Condition {
	if tree.UIField != "" {
		return cloneCondition(tree)
	}
	next := cloneCondition(tree)
	next.Rules = []*Condition{}
	kinds := []string{TypedIP, TypedWiFi, TypedDomain}
	owned := map[string][]*Condition{}
	for _, node := range tree.Rules {
		if node.Kind == ConditionGroup {
			next.Rules = append(next.Rules, NormalizeTypedMatches(node))
			continue
		}
		found := ""
		for _, kind := range kinds {
			if !ownedLeaf(node, kind, false) {
				continue
			}
			all := true
			for k := range node.Match {
				if !slices.Contains(TypedFields(kind), k) {
					all = false
				}
			}
			if all {
				found = kind
				break
			}
		}
		if found != "" {
			owned[found] = append(owned[found], cloneCondition(node))
		} else {
			next.Rules = append(next.Rules, cloneCondition(node))
		}
	}
	for _, kind := range kinds {
		list := owned[kind]
		if len(list) == 0 {
			continue
		}
		single := len(list) == 1
		invert := single && list[0].Invert
		mode := tree.Mode
		if single {
			mode = "or"
		}
		g := &Condition{Kind: ConditionGroup, UIField: kind, Mode: mode, Invert: invert, Rules: list}
		if invert {
			inner := *list[0]
			inner.Invert = false
			g.Rules = []*Condition{&inner}
		}
		next.Rules = append(next.Rules, g)
	}
	return next
}

// MatchTokens lists a kind's values in a tree.
func MatchTokens(group *Condition, kind string) []MatchToken {
	tokens := []MatchToken{}
	var walk func(node *Condition, path []int, inside, excluded bool)
	walk = func(node *Condition, path []int, inside, excluded bool) {
		excluded = excluded != node.Invert
		if node.Kind == ConditionGroup {
			for i, child := range node.Rules {
				if child.Kind == ConditionMatch || child.UIField == kind {
					walk(child, append(slices.Clone(path), i), inside || child.Kind == ConditionGroup, excluded)
				}
			}
			return
		}
		if !ownedLeaf(node, kind, inside) {
			return
		}
		for _, field := range TypedFields(kind) {
			if field == "rule_set" && !inside && node.UIField != "ip_cidr" {
				continue
			}
			var values []any
			if field == "ip_is_private" {
				if node.Match[field] == true {
					values = []any{true}
				}
			} else {
				values = anyStrings(typedList(node.Match[field]))
			}
			ids := make([]string, len(path))
			for i, p := range path {
				ids[i] = strconv.Itoa(p)
			}
			for index, value := range values {
				tokens = append(tokens, MatchToken{
					MatchValue: MatchValue{Field: field, Value: value},
					Path:       path, Index: index, Excluded: excluded,
					ID: strings.Join(ids, ".") + ":" + field + ":" + strconv.Itoa(index),
				})
			}
		}
	}
	walk(group, []int{}, false, false)
	return tokens
}

// RemoveMatchToken removes a value from its leaf, pruning what empties.
func RemoveMatchToken(group *Condition, token MatchToken) *Condition {
	next := cloneCondition(group)
	parent, at := conditionAt(next, token.Path)
	if parent == nil || parent.Rules[at].Kind != ConditionMatch {
		return group
	}
	leaf := parent.Rules[at]
	if token.Field == "ip_is_private" {
		delete(leaf.Match, token.Field)
	} else {
		values := typedList(leaf.Match[token.Field])
		if token.Index >= 0 && token.Index < len(values) {
			values = slices.Delete(values, token.Index, token.Index+1)
		}
		if len(values) > 0 {
			leaf.Match[token.Field] = anyStrings(values)
		} else {
			delete(leaf.Match, token.Field)
		}
	}
	if _, ok := leaf.Match["rule_set"]; token.Field == "rule_set" && !ok {
		leaf.UIField = ""
	}
	pruneEmpty(next)
	return next
}

func matchValueKey(v MatchValue) string { return v.Field + ":" + jsv.String(v.Value) }

// AddMatchValues adds values to a kind's predicates.
func AddMatchValues(group *Condition, kind string, values []MatchValue) *Condition {
	next := cloneCondition(group)
	existing := map[string]bool{}
	for _, t := range MatchTokens(next, kind) {
		existing[matchValueKey(t.MatchValue)] = true
	}
	for _, item := range values {
		key := matchValueKey(item)
		if existing[key] {
			continue
		}
		existing[key] = true
		_, isBool := item.Value.(bool)
		if kind == TypedPort || kind == TypedSourcePort {
			var leaf *Condition
			for _, node := range next.Rules {
				if node.Kind == ConditionMatch && ownedLeaf(node, kind, false) {
					leaf = node
					break
				}
			}
			if leaf == nil {
				leaf = &Condition{Kind: ConditionMatch, Match: map[string]any{}}
				next.Rules = append(next.Rules, leaf)
			}
			leaf.Match[item.Field] = append(anyStrings(typedList(leaf.Match[item.Field])), item.Value)
			continue
		}
		var target *Condition
		for _, node := range next.Rules {
			if node.Kind == ConditionGroup && node.UIField == kind {
				target = node
				break
			}
		}
		if target == nil {
			var previous *Condition
			for _, node := range next.Rules {
				if _, ok := node.Match[item.Field]; node.Kind == ConditionMatch && ok && ownedLeaf(node, kind, false) {
					previous = node
					break
				}
			}
			if previous != nil {
				if isBool {
					previous.Match[item.Field] = item.Value
				} else {
					previous.Match[item.Field] = append(anyStrings(typedList(previous.Match[item.Field])), item.Value)
				}
				continue
			}
			target = &Condition{Kind: ConditionGroup, UIField: kind, Mode: "or", Rules: []*Condition{}}
			next.Rules = append(next.Rules, target)
		}
		var value any = []any{item.Value}
		if isBool {
			value = item.Value
		}
		target.Rules = append(target.Rules, &Condition{Kind: ConditionMatch, Match: map[string]any{item.Field: value}})
	}
	return next
}

// ---- IP version ----

func isVersionNode(node *Condition) bool {
	if node.Kind == ConditionGroup {
		return node.UIField == "ip_version"
	}
	_, ok := node.Match["ip_version"]
	return ok
}

// IPVersionSelection returns the selected IP versions (4, 6) of a root
// group and whether they are excluded.
func IPVersionSelection(group *Condition) (versions []int, excluded bool) {
	i := slices.IndexFunc(group.Rules, isVersionNode)
	versions = []int{}
	if i < 0 {
		return versions, false
	}
	node := group.Rules[i]
	var raw []any
	if node.Kind == ConditionGroup {
		for _, child := range node.Rules {
			if child.Kind == ConditionMatch {
				raw = append(raw, child.Match["ip_version"])
			}
		}
	} else if list, ok := jsv.Array(node.Match["ip_version"]); ok {
		raw = list
	} else {
		raw = []any{node.Match["ip_version"]}
	}
	for _, v := range raw {
		n := jsv.NumberOrNaN(v)
		if (n == 4 || n == 6) && !slices.Contains(versions, int(n)) {
			versions = append(versions, int(n))
		}
	}
	return versions, node.Invert
}

// SetIPVersions selects IP versions in a root group; excluded inverts
// them (IPVersionSelection's excluded keeps the current state).
func SetIPVersions(group *Condition, selected []int, excluded bool) *Condition {
	next := cloneCondition(group)
	index := slices.IndexFunc(next.Rules, isVersionNode)
	var previous *Condition
	if index >= 0 {
		previous = next.Rules[index]
	}
	var versions []int
	for _, v := range selected {
		if !slices.Contains(versions, v) {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return strconv.Itoa(versions[i]) < strconv.Itoa(versions[j]) })
	remaining := map[string]any{}
	if previous != nil && previous.Kind == ConditionMatch {
		for k, v := range previous.Match {
			remaining[k] = v
		}
	}
	delete(remaining, "ip_version")
	predicate := &Condition{Kind: ConditionGroup, UIField: "ip_version", Mode: "or", Invert: excluded, Rules: []*Condition{}}
	for _, v := range versions {
		predicate.Rules = append(predicate.Rules, &Condition{Kind: ConditionMatch, Match: map[string]any{"ip_version": float64(v)}})
	}
	var replacement []*Condition
	if len(versions) > 0 {
		replacement = []*Condition{predicate}
	}
	if len(remaining) > 0 {
		previousInvert := previous != nil && previous.Invert
		switch {
		case len(versions) == 1 && excluded == previousInvert:
			leaf := *previous
			leaf.Match = remaining
			leaf.Match["ip_version"] = float64(versions[0])
			replacement = []*Condition{&leaf}
		case len(versions) > 1 && excluded == previousInvert:
			inner := *predicate
			inner.Invert = false
			replacement = []*Condition{{Kind: ConditionGroup, Mode: "and", Invert: previousInvert, Rules: []*Condition{{Kind: ConditionMatch, Match: remaining}, &inner}}}
		default:
			leaf := *previous
			leaf.Match = remaining
			replacement = append([]*Condition{&leaf}, replacement...)
		}
	}
	if index >= 0 {
		next.Rules = slices.Replace(next.Rules, index, index+1, replacement...)
	} else {
		next.Rules = append(next.Rules, replacement...)
	}
	return next
}
