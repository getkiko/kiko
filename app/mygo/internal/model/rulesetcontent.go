package model

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// This file reads a rule set's rules as entries, one value of one field
// each, for the rule set page: the source's entries, the overrides that
// add entries to it or remove entries from it, and the entries of a
// rule set the user builds.

// RuleSetEntry is a value of a headless rule's field.
type RuleSetEntry struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// RuleSetOverrides change a rule set's source: Add rules are appended,
// Remove values are taken out of its default rules, each time the core
// loads the source, so they outlive its updates.
type RuleSetOverrides struct {
	Add    []RuleSetEntry `json:"add,omitempty"`
	Remove []RuleSetEntry `json:"remove,omitempty"`
}

// Empty reports overrides that change nothing.
func (o *RuleSetOverrides) Empty() bool { return o == nil || len(o.Add) == 0 && len(o.Remove) == 0 }

// RuleSetFieldInfo is a field entries can have.
type RuleSetFieldInfo struct {
	Field string
	Label string
	// group is the field's match group: sing-box ORs the fields of a group
	// and ANDs the groups of a rule. ip_cidr is a group of its own, as the
	// core's overrides take it (rule_set_ip_cidr_match_source can make it
	// match the source).
	group int
}

// RuleSetFields are the fields of entries, in the order shown.
var RuleSetFields = []RuleSetFieldInfo{
	{"domain", "域名", 0},
	{"domain_suffix", "域名后缀", 0},
	{"domain_keyword", "域名关键词", 0},
	{"domain_regex", "域名正则", 0},
	{"ip_cidr", "IP 段", 1},
	{"source_ip_cidr", "源 IP 段", 2},
	{"process_name", "进程名", 3},
	{"process_path", "进程路径", 4},
	{"package_name", "应用包名", 5},
}

func ruleSetField(field string) (RuleSetFieldInfo, bool) {
	for _, f := range RuleSetFields {
		if f.Field == field {
			return f, true
		}
	}
	return RuleSetFieldInfo{}, false
}

// RuleSetFieldLabel is a field's name as shown, the field itself when
// unknown and "Compound rule" for a compound rule.
func RuleSetFieldLabel(field string) string {
	if field == "" {
		return "组合规则"
	}
	if f, ok := ruleSetField(field); ok {
		return f.Label
	}
	return field
}

// RuleSetItem is an entry of a rule set's rules: a value of a field, or a
// compound rule (Field "") whose Value is the rule's compact JSON.
type RuleSetItem struct {
	Field string
	Value string
	// Rule is a compound rule as read.
	Rule any
}

// Compound reports a rule shown whole.
func (i RuleSetItem) Compound() bool { return i.Field == "" }

// Entry is the item's field and value.
func (i RuleSetItem) Entry() RuleSetEntry { return RuleSetEntry{i.Field, i.Value} }

// ruleValues reads a field's values, a list of strings or a string; ok is
// false for anything else.
func ruleValues(value any) ([]string, bool) {
	switch v := value.(type) {
	case string:
		return []string{v}, true
	case []string:
		return v, true
	case []any:
		out := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

// expandable reads a default rule whose fields all are entry fields of
// one match group, so that its values are entries of their own: nil for a
// compound rule. An inverted rule is compound: the core leaves it as it
// is.
func expandable(rule any) map[string][]string {
	m, ok := rule.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	group := -1
	out := make(map[string][]string, len(m))
	for k, v := range m {
		switch k {
		case "type":
			if v != "default" {
				return nil
			}
			continue
		case "invert":
			if v != false {
				return nil
			}
			continue
		}
		f, ok := ruleSetField(k)
		if !ok || group >= 0 && f.group != group {
			return nil
		}
		group = f.group
		values, ok := ruleValues(v)
		if !ok {
			return nil
		}
		out[k] = values
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FlattenRuleSet reads headless rules (the source format) as entries: each
// value of a default rule whose fields are entry fields of one match
// group, in the order of RuleSetFields within a rule, and every other rule
// whole. It reads hundreds of thousands of values; run it off the UI
// thread for a large rule set.
func FlattenRuleSet(rules []any) []RuleSetItem {
	n := 0
	for _, rule := range rules {
		if m, ok := rule.(map[string]any); ok {
			for _, v := range m {
				if list, ok := v.([]any); ok {
					n += len(list)
				}
			}
		}
		n++
	}
	out := make([]RuleSetItem, 0, n)
	for _, rule := range rules {
		fields := expandable(rule)
		if fields == nil {
			raw, _ := json.Marshal(rule)
			out = append(out, RuleSetItem{Value: string(raw), Rule: rule})
			continue
		}
		for _, f := range RuleSetFields {
			for _, v := range fields[f.Field] {
				out = append(out, RuleSetItem{Field: f.Field, Value: v})
			}
		}
	}
	return out
}

// RuleSetEntryState marks an entry of the merged view.
type RuleSetEntryState int

const (
	// EntrySource is a source entry the overrides keep.
	EntrySource RuleSetEntryState = iota
	// EntryRemoved is a source entry the overrides remove, or a removal
	// the source no longer has.
	EntryRemoved
	// EntryAdded is an entry the overrides add.
	EntryAdded
	// EntryPartlyRemoved is a source address range a removed range
	// overlaps, which the core narrows.
	EntryPartlyRemoved
)

// RuleSetViewItem is an entry of the merged view.
type RuleSetViewItem struct {
	RuleSetItem
	State RuleSetEntryState
	// By is the removal that marks a source address range removed or
	// partly removed, when it is another range; zero otherwise.
	By RuleSetEntry
}

// isAddressField reports the fields the core takes as address sets.
func isAddressField(field string) bool { return field == "ip_cidr" || field == "source_ip_cidr" }

// parseRange reads an address range or a single address.
func parseRange(value string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(value); err == nil {
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(value); err == nil {
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

type rangeRemoval struct {
	entry  RuleSetEntry
	prefix netip.Prefix
}

// RuleSetSourceIndex holds a source's entries for lookups.
type RuleSetSourceIndex map[RuleSetEntry]struct{}

// IndexRuleSet indexes the entries of a source (compound rules aside).
func IndexRuleSet(items []RuleSetItem) RuleSetSourceIndex {
	index := make(RuleSetSourceIndex, len(items))
	for _, item := range items {
		if !item.Compound() {
			index[item.Entry()] = struct{}{}
		}
	}
	return index
}

// Has reports an entry of the source.
func (x RuleSetSourceIndex) Has(e RuleSetEntry) bool {
	_, ok := x[e]
	return ok
}

// MergeRuleSetView is what the core loads, entry by entry: the entries the
// overrides add, the source's entries with those they remove marked, then
// the removals the source does not have (kept for a later source), marked.
// An added entry the source has shows once, as the source's. Address
// ranges go as the core subtracts them: a source range a removed one
// covers shows removed, one it overlaps partly removed (each by that
// removal), and such a removal does not show on its own.
func MergeRuleSetView(source []RuleSetItem, index RuleSetSourceIndex, o *RuleSetOverrides) []RuleSetViewItem {
	if index == nil {
		index = IndexRuleSet(source)
	}
	var removed map[RuleSetEntry]bool
	var add, remove []RuleSetEntry
	if o != nil {
		add, remove = o.Add, o.Remove
		removed = make(map[RuleSetEntry]bool, len(remove))
		for _, e := range remove {
			removed[e] = true
		}
	}
	var ranges []rangeRemoval
	for _, e := range remove {
		if isAddressField(e.Field) {
			if p, ok := parseRange(e.Value); ok {
				ranges = append(ranges, rangeRemoval{e, p})
			}
		}
	}
	used := map[RuleSetEntry]bool{}
	out := make([]RuleSetViewItem, 0, len(add)+len(source)+len(remove))
	for _, e := range add {
		if !index.Has(e) {
			out = append(out, RuleSetViewItem{RuleSetItem: RuleSetItem{Field: e.Field, Value: e.Value}, State: EntryAdded})
		}
	}
	for _, item := range source {
		view := RuleSetViewItem{RuleSetItem: item}
		switch {
		case item.Compound():
		case removed[item.Entry()]:
			view.State = EntryRemoved
		case len(ranges) > 0 && isAddressField(item.Field):
			markRange(&view, ranges, used)
		}
		out = append(out, view)
	}
	for _, e := range remove {
		if !index.Has(e) && !used[e] {
			out = append(out, RuleSetViewItem{RuleSetItem: RuleSetItem{Field: e.Field, Value: e.Value}, State: EntryRemoved})
		}
	}
	return out
}

// markRange marks a source address range the removed ranges cover or
// overlap, noting the removals that apply in used.
func markRange(view *RuleSetViewItem, ranges []rangeRemoval, used map[RuleSetEntry]bool) {
	p, ok := parseRange(view.Value)
	if !ok {
		return
	}
	for _, r := range ranges {
		if r.entry.Field != view.Field || !r.prefix.Overlaps(p) {
			continue
		}
		used[r.entry] = true
		if r.prefix.Bits() <= p.Bits() {
			view.State, view.By = EntryRemoved, r.entry
			return
		}
		if view.State == EntrySource {
			view.State, view.By = EntryPartlyRemoved, r.entry
		}
	}
}

// Search tiers of FilterRuleSetView, best first.
const (
	// matchExact is a value equal to the query.
	matchExact = iota
	// matchPrefix is a value starting with the query.
	matchPrefix
	// matchLabels is a value whose dot-separated labels the query matches
	// whole: google.com in www.google.com, as a domain suffix.
	matchLabels
	// matchContains is any other value, or field name, containing it.
	matchContains
	matchNone
)

// FilterRuleSetView lists the indexes of the items whose value or field
// name contains query, ignoring case, the best matches first: a value
// equal to the query (a suffix's leading dot aside), then starting with it, then matching it as whole
// dot-separated labels (a domain suffix), then containing it; in their
// order within each. All of them, in order, for an empty query.
func FilterRuleSetView(items []RuleSetViewItem, query string) []int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		out := make([]int, len(items))
		for i := range out {
			out[i] = i
		}
		return out
	}
	var tiers [matchNone][]int
	for i, item := range items {
		if tier := ruleSetMatch(item.RuleSetItem, query); tier < matchNone {
			tiers[tier] = append(tiers[tier], i)
		}
	}
	out := make([]int, 0, len(tiers[0])+len(tiers[1])+len(tiers[2])+len(tiers[3]))
	for _, t := range tiers {
		out = append(out, t...)
	}
	return out
}

// ruleSetMatch is how well item matches query, a lowercase, trimmed,
// nonempty search: a tier from exact (0) to contained (3), or 4 for no
// match.
func ruleSetMatch(item RuleSetItem, query string) int {
	value := strings.ToLower(item.Value)
	switch {
	// A domain suffix written with its leading dot is the domain too.
	case value == query, strings.TrimPrefix(value, ".") == query:
		return matchExact
	case strings.HasPrefix(value, query):
		return matchPrefix
	case matchesLabels(value, query):
		return matchLabels
	case strings.Contains(value, query) || strings.Contains(item.Field, query) ||
		strings.Contains(strings.ToLower(RuleSetFieldLabel(item.Field)), query):
		return matchContains
	}
	return matchNone
}

// matchesLabels reports whether query occurs in value as whole
// dot-separated labels: bounded by dots or the value's ends, as google.com
// in www.google.com (a query's own leading or trailing dot is a bound).
func matchesLabels(value, query string) bool {
	open, closed := strings.HasPrefix(query, "."), strings.HasSuffix(query, ".")
	for from := 0; from < len(value); {
		at := strings.Index(value[from:], query)
		if at < 0 {
			return false
		}
		at += from
		end := at + len(query)
		if (open || at == 0 || value[at-1] == '.') && (closed || end == len(value) || value[end] == '.') {
			return true
		}
		from = at + 1
	}
	return false
}

// NormalizeRuleSetEntry trims an entry's value.
func NormalizeRuleSetEntry(e RuleSetEntry) RuleSetEntry {
	e.Value = strings.TrimSpace(e.Value)
	return e
}

// ValidateRuleSetEntry checks an entry: a known field and a value it
// takes; "" when valid.
func ValidateRuleSetEntry(e RuleSetEntry) string {
	if _, ok := ruleSetField(e.Field); !ok {
		return "不支持的字段"
	}
	value := strings.TrimSpace(e.Value)
	switch {
	case value == "":
		return "请填写值"
	case e.Field == "ip_cidr" || e.Field == "source_ip_cidr":
		if _, err := netip.ParsePrefix(value); err != nil {
			if _, err := netip.ParseAddr(value); err != nil {
				return "请填写 IP 或 CIDR"
			}
		}
	case e.Field == "domain_regex":
		if _, err := regexp.Compile(value); err != nil {
			return "正则表达式无效"
		}
	}
	return ""
}

func cleanOverrides(o *RuleSetOverrides) *RuleSetOverrides {
	if o.Empty() {
		return nil
	}
	return o
}

func copyOverrides(o *RuleSetOverrides) *RuleSetOverrides {
	if o == nil {
		return &RuleSetOverrides{}
	}
	return &RuleSetOverrides{Add: slices.Clone(o.Add), Remove: slices.Clone(o.Remove)}
}

// AddOverride adds an entry to a rule set: an entry it removes is kept
// again, one the source or the overrides have already is not added twice
// (changed false). It returns new overrides, nil when empty.
func AddOverride(o *RuleSetOverrides, index RuleSetSourceIndex, e RuleSetEntry) (next *RuleSetOverrides, changed bool) {
	e = NormalizeRuleSetEntry(e)
	next = copyOverrides(o)
	if i := slices.Index(next.Remove, e); i >= 0 {
		next.Remove = slices.Delete(next.Remove, i, i+1)
		return cleanOverrides(next), true
	}
	if index.Has(e) || slices.Contains(next.Add, e) {
		return o, false
	}
	next.Add = append(next.Add, e)
	return cleanOverrides(next), true
}

// RemoveOverride removes an entry from a rule set: an added one is no
// longer added, and one the source has (index) is removed once, both when
// the source came to have an added entry.
func RemoveOverride(o *RuleSetOverrides, index RuleSetSourceIndex, e RuleSetEntry) (next *RuleSetOverrides, changed bool) {
	next = copyOverrides(o)
	if i := slices.Index(next.Add, e); i >= 0 {
		next.Add = slices.Delete(next.Add, i, i+1)
		changed = true
		if !index.Has(e) {
			return cleanOverrides(next), true
		}
	}
	if slices.Contains(next.Remove, e) {
		if changed {
			return cleanOverrides(next), true
		}
		return o, false
	}
	next.Remove = append(next.Remove, e)
	return cleanOverrides(next), true
}

// UndoOverride takes back the override marking an entry of the merged
// view: the addition of an added entry, the removal of a removed one.
func UndoOverride(o *RuleSetOverrides, item RuleSetViewItem) (next *RuleSetOverrides, changed bool) {
	next = copyOverrides(o)
	e := item.Entry()
	if item.By != (RuleSetEntry{}) {
		e = item.By
	}
	switch item.State {
	case EntryAdded:
		next.Add = slices.DeleteFunc(next.Add, func(x RuleSetEntry) bool { return x == e })
		changed = len(next.Add) != len(o.addList())
	case EntryRemoved, EntryPartlyRemoved:
		next.Remove = slices.DeleteFunc(next.Remove, func(x RuleSetEntry) bool { return x == e })
		changed = len(next.Remove) != len(o.removeList())
	}
	if !changed {
		return o, false
	}
	return cleanOverrides(next), true
}

func (o *RuleSetOverrides) addList() []RuleSetEntry {
	if o == nil {
		return nil
	}
	return o.Add
}

func (o *RuleSetOverrides) removeList() []RuleSetEntry {
	if o == nil {
		return nil
	}
	return o.Remove
}

// OverrideSummary is +added −removed, "" without overrides.
func OverrideSummary(o *RuleSetOverrides) string {
	if o.Empty() {
		return ""
	}
	var parts []string
	if len(o.Add) > 0 {
		parts = append(parts, "+"+strconv.Itoa(len(o.Add)))
	}
	if len(o.Remove) > 0 {
		parts = append(parts, "−"+strconv.Itoa(len(o.Remove)))
	}
	return strings.Join(parts, " ")
}

// RuleSetFieldValues are the values of a field.
type RuleSetFieldValues struct {
	Field  string
	Values []string
}

// group lists entries' values by field: in the order fields first appear,
// or in the order of RuleSetFields when canonical.
func groupEntries(entries []RuleSetEntry, canonical bool) []RuleSetFieldValues {
	var out []RuleSetFieldValues
	at := map[string]int{}
	for _, e := range entries {
		if _, ok := ruleSetField(e.Field); !ok || strings.TrimSpace(e.Value) == "" {
			continue
		}
		i, ok := at[e.Field]
		if !ok {
			i = len(out)
			at[e.Field] = i
			out = append(out, RuleSetFieldValues{Field: e.Field})
		}
		if !slices.Contains(out[i].Values, e.Value) {
			out[i].Values = append(out[i].Values, e.Value)
		}
	}
	if canonical {
		order := map[string]int{}
		for i, f := range RuleSetFields {
			order[f.Field] = i
		}
		slices.SortStableFunc(out, func(a, b RuleSetFieldValues) int { return order[a.Field] - order[b.Field] })
	}
	return out
}

// AddGroups are the added entries as headless rules, one per field in the
// order the fields first appear.
func (o *RuleSetOverrides) AddGroups() []RuleSetFieldValues {
	if o == nil {
		return nil
	}
	return groupEntries(o.Add, false)
}

// RemoveGroups are the removed values by field, in the order of
// RuleSetFields.
func (o *RuleSetOverrides) RemoveGroups() []RuleSetFieldValues {
	if o == nil {
		return nil
	}
	return groupEntries(o.Remove, true)
}

// IsCustomRuleSet reports a rule set the user builds: inline, without a
// source link, not of the catalog.
func IsCustomRuleSet(r *RuleSet) bool {
	if r.Mode != "inline" || strings.TrimSpace(r.URL) != "" {
		return false
	}
	_, builtin := BuiltinRuleSetForEntry(r)
	return !builtin
}

// RuleSetKind is a rule set's kind as shown: built-in, remote srs, remote json,
// inline or self-built.
func RuleSetKind(r *RuleSet) string {
	if _, builtin := BuiltinRuleSetForEntry(r); builtin {
		return "内置"
	}
	switch {
	case r.Mode == "remote" && strings.HasSuffix(strings.ToLower(urlPath(r.URL)), ".json"):
		return "远程 json"
	case r.Mode == "remote":
		return "远程 srs"
	case strings.TrimSpace(r.URL) == "":
		return "自建"
	}
	return "内联"
}

func urlPath(raw string) string {
	raw, _, _ = strings.Cut(raw, "?")
	raw, _, _ = strings.Cut(raw, "#")
	return raw
}

// singleField reports a default rule of field alone, with its values.
func singleField(rule any, field string) ([]string, bool) {
	fields := expandable(rule)
	if len(fields) != 1 {
		return nil, false
	}
	values, ok := fields[field]
	return values, ok
}

func anyValues(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// HasRuleSetEntry reports an entry of rules.
func HasRuleSetEntry(rules []any, e RuleSetEntry) bool {
	for _, rule := range rules {
		if slices.Contains(expandable(rule)[e.Field], e.Value) {
			return true
		}
	}
	return false
}

// AddRuleSetEntry adds an entry to a custom rule set's rules: to the
// first rule of its field alone, or as a new rule. An entry it has is not
// added again (ok false). rules is not modified.
func AddRuleSetEntry(rules []any, e RuleSetEntry) (next []any, ok bool) {
	e = NormalizeRuleSetEntry(e)
	if HasRuleSetEntry(rules, e) {
		return rules, false
	}
	next = slices.Clone(rules)
	for i, rule := range next {
		if values, ok := singleField(rule, e.Field); ok {
			next[i] = map[string]any{e.Field: anyValues(append(slices.Clone(values), e.Value))}
			return next, true
		}
	}
	return append(next, map[string]any{e.Field: []any{e.Value}}), true
}

// RemoveRuleSetEntry takes an entry out of rules, dropping a rule it
// leaves empty, or removes a compound rule (Field "") by its JSON. rules is
// not modified.
func RemoveRuleSetEntry(rules []any, item RuleSetItem) []any {
	next := make([]any, 0, len(rules))
	for _, rule := range rules {
		fields := expandable(rule)
		if fields == nil {
			if item.Compound() {
				if raw, _ := json.Marshal(rule); string(raw) == item.Value {
					continue
				}
			}
			next = append(next, rule)
			continue
		}
		values := fields[item.Field]
		if item.Compound() || !slices.Contains(values, item.Value) {
			next = append(next, rule)
			continue
		}
		rest := map[string]any{}
		for k, v := range rule.(map[string]any) {
			rest[k] = v
		}
		kept := slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == item.Value })
		if len(kept) == 0 {
			delete(rest, item.Field)
		} else {
			rest[item.Field] = anyValues(kept)
		}
		if expandable(rest) != nil {
			next = append(next, rest)
		}
	}
	return next
}

// ReplaceRuleSetEntry changes an entry of rules to e, in place when the
// field stays; ok is false when rules have e already.
func ReplaceRuleSetEntry(rules []any, previous RuleSetEntry, e RuleSetEntry) (next []any, ok bool) {
	e = NormalizeRuleSetEntry(e)
	if e == previous {
		return rules, true
	}
	if HasRuleSetEntry(rules, e) {
		return rules, false
	}
	if e.Field == previous.Field {
		next = slices.Clone(rules)
		for i, rule := range next {
			values := expandable(rule)[e.Field]
			at := slices.Index(values, previous.Value)
			if at < 0 {
				continue
			}
			replaced := slices.Clone(values)
			replaced[at] = e.Value
			m := map[string]any{}
			for k, v := range rule.(map[string]any) {
				m[k] = v
			}
			m[e.Field] = anyValues(replaced)
			next[i] = m
			return next, true
		}
	}
	return AddRuleSetEntry(RemoveRuleSetEntry(rules, RuleSetItem{Field: previous.Field, Value: previous.Value}), e)
}

// FoldOverrides is rules with overrides applied, as the rules of a rule
// set that stops having a source: the removed values leave the rules that
// show them as entries (a rule left empty goes; compound rules stay, as an
// address range is not split), then the added entries join.
func FoldOverrides(rules []any, o *RuleSetOverrides) []any {
	next := slices.Clone(rules)
	if o == nil {
		return next
	}
	for _, e := range o.Remove {
		next = RemoveRuleSetEntry(next, RuleSetItem{Field: e.Field, Value: e.Value})
	}
	for _, e := range o.Add {
		next, _ = AddRuleSetEntry(next, e)
	}
	return next
}

// CountRuleSetEntries counts the entries FlattenRuleSet reads from rules:
// a compound rule is one.
func CountRuleSetEntries(rules []any) int {
	n := 0
	for _, rule := range rules {
		if fields := expandable(rule); fields != nil {
			for _, values := range fields {
				n += len(values)
			}
		} else {
			n++
		}
	}
	return n
}
