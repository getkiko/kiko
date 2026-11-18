package model

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// This file is the node region table (after FlClash's defaultFilters): a
// user-editable list of regions, each a regular expression over node
// names, and the patterns of subscription info nodes (traffic left,
// expiry, notices). Selector and URLTest groups may take their members
// from it (Outbound.Filter), so membership follows subscription updates.
//
// Go's regexp (RE2) has no lookaround, so the defaults write FlClash's
// (?<![a-z])hk(?![a-z]) as (?:^|[^a-z])hk(?:[^a-z]|$); CompileNodePattern
// rewrites those two lookarounds the same way in patterns pasted from
// FlClash.

// Region is a named node region.
type Region struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Flag    string `json:"flag,omitempty"`
	Pattern string `json:"pattern"`
}

// RegionSettings is the document's region table and info node patterns.
type RegionSettings struct {
	Regions []Region `json:"regions"`
	Info    []Region `json:"info"`
}

// GroupFilter gives a Selector or URLTest group dynamic members: the
// enabled nodes whose region is one of Regions (when set) and whose name
// contains one of Keywords or matches Pattern (when either is set), minus
// those whose name contains one of ExcludeKeywords or matches Exclude
// and, unless KeepInfo, the info nodes. They follow the explicit members.
// Keywords are case-insensitive; Pattern and Exclude are the patterns
// KeywordsFromPattern cannot turn into keywords.
type GroupFilter struct {
	Regions         []string `json:"regions,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	ExcludeKeywords []string `json:"excludeKeywords,omitempty"`
	Pattern         string   `json:"pattern,omitempty"`
	Exclude         string   `json:"exclude,omitempty"`
	KeepInfo        bool     `json:"keepInfo,omitempty"`
}

// Active reports whether the filter selects anything.
func (f *GroupFilter) Active() bool {
	return f != nil && (len(f.Regions) > 0 || len(keywordList(f.Keywords)) > 0 || strings.TrimSpace(f.Pattern) != "")
}

// Error describes an invalid pattern of the filter, "" when valid.
func (f *GroupFilter) Error() string {
	if f == nil {
		return ""
	}
	if _, err := CompileNodePattern(f.Pattern); err != nil {
		return "筛选正则无效：" + err.Error()
	}
	if _, err := CompileNodePattern(f.Exclude); err != nil {
		return "排除正则无效：" + err.Error()
	}
	return ""
}

// keywordList are the non-empty keywords, trimmed.
func keywordList(keywords []string) []string {
	var out []string
	for _, k := range keywords {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// keywordPattern is the case-insensitive pattern of names containing any
// of keywords, "" for none. Keywords match through CompileNodePattern,
// the engine of patterns, so KeywordsFromPattern is lossless.
func keywordPattern(keywords []string) string {
	list := keywordList(keywords)
	if len(list) == 0 {
		return ""
	}
	for i, k := range list {
		list[i] = regexp.QuoteMeta(k)
	}
	return "(?i)" + strings.Join(list, "|")
}

// keywordMatchers compile each keyword, nil for an empty one.
func keywordMatchers(keywords []string) ([]string, []*regexp.Regexp) {
	list := keywordList(keywords)
	res := make([]*regexp.Regexp, len(list))
	for i, k := range list {
		res[i], _ = CompileNodePattern(keywordPattern([]string{k}))
	}
	return list, res
}

// KeywordsFromPattern turns a pattern into the keywords that select the
// same names, when it is only literal alternatives: an optional leading
// (?i), then alternatives split on unescaped |, each non-empty, without
// surrounding spaces and without metacharacters but for a backslash
// escaping ASCII punctuation. Without (?i) no alternative may hold a
// letter with case, as keywords ignore case. "" gives none.
func KeywordsFromPattern(pattern string) ([]string, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, true
	}
	fold := strings.HasPrefix(pattern, "(?i)")
	if fold {
		pattern = pattern[len("(?i)"):]
	}
	var out []string
	var cur strings.Builder
	finish := func() bool {
		k := cur.String()
		cur.Reset()
		if k == "" || strings.TrimSpace(k) != k {
			return false
		}
		if !fold && strings.ContainsFunc(k, func(r rune) bool { return unicode.SimpleFold(r) != r }) {
			return false
		}
		if !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, k) }) {
			out = append(out, k)
		}
		return true
	}
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\\':
			if i+1 >= len(runes) {
				return nil, false
			}
			next := runes[i+1]
			if next > unicode.MaxASCII || !unicode.IsPunct(next) && !unicode.IsSymbol(next) {
				return nil, false
			}
			cur.WriteRune(next)
			i++
		case r == '|':
			if !finish() {
				return nil, false
			}
		case strings.ContainsRune(`.+*?()[]{}^$`, r):
			return nil, false
		default:
			cur.WriteRune(r)
		}
	}
	if !finish() {
		return nil, false
	}
	return out, true
}

// MigrateFilterKeywords turns the filter's Pattern and Exclude into
// keywords when KeywordsFromPattern can, merging them into Keywords and
// ExcludeKeywords; the filter selects the same nodes. It reports whether
// it changed the filter.
func MigrateFilterKeywords(f *GroupFilter) bool {
	if f == nil {
		return false
	}
	changed := false
	migrate := func(pattern *string, keywords *[]string) {
		if strings.TrimSpace(*pattern) == "" {
			return
		}
		list, ok := KeywordsFromPattern(*pattern)
		if !ok {
			return
		}
		for _, k := range list {
			if !slices.ContainsFunc(*keywords, func(x string) bool { return strings.EqualFold(strings.TrimSpace(x), k) }) {
				*keywords = append(*keywords, k)
			}
		}
		*pattern = ""
		changed = true
	}
	migrate(&f.Pattern, &f.Keywords)
	migrate(&f.Exclude, &f.ExcludeKeywords)
	return changed
}

const (
	wordBefore = `(?:^|[^a-z])`
	wordAfter  = `(?:[^a-z]|$)`
)

func word(w string) string { return wordBefore + w + wordAfter }

// DefaultRegions is the region table of a document that has none.
func DefaultRegions() *RegionSettings {
	return &RegionSettings{
		Regions: []Region{
			{"hk", "香港", "🇭🇰", `(?i)🇭🇰|港|` + word("hk") + `|hong\s*kong`},
			{"tw", "台湾", "🇹🇼", `(?i)🇹🇼|台湾|台灣|台北|新北|彰化|` + word("tw") + `|taiwan`},
			{"jp", "日本", "🇯🇵", `(?i)🇯🇵|日本|东京|東京|大阪|埼玉|` + word("jp") + `|japan|tokyo|osaka`},
			{"sg", "新加坡", "🇸🇬", `(?i)🇸🇬|新加坡|狮城|獅城|` + word("sg") + `|singapore`},
			{"us", "美国", "🇺🇸", `(?i)🇺🇸|美国|美國|美西|美东|美東|洛杉矶|圣何塞|西雅图|硅谷|纽约|` + word("usa?") + `|united\s*states`},
			{"kr", "韩国", "🇰🇷", `(?i)🇰🇷|韩国|韓國|首尔|首爾|` + word("kr") + `|korea|seoul`},
			{"uk", "英国", "🇬🇧", `(?i)🇬🇧|英国|英國|伦敦|倫敦|` + word("(?:uk|gb)") + `|united\s*kingdom|britain|london`},
		},
		Info: []Region{
			{"traffic", "剩余流量", "", `(?i)剩余|剩餘|已用|流量\s*[:：]|traffic|remaining`},
			{"expire", "到期", "", `(?i)到期|过期|過期|有效期|expir`},
			{"reset", "重置", "", `(?i)重置|reset`},
			{"notice", "官网公告", "", `(?i)官网|官網|网址|網址|域名|公告|套餐|客服|群组|群組|频道|頻道|website|telegram|` + word("tg")},
		},
	}
}

// Regions is the document's region table, the defaults when it has none.
func Regions(doc *Doc) *RegionSettings {
	if doc != nil && doc.NodeRegions != nil {
		return doc.NodeRegions
	}
	return DefaultRegions()
}

var lookaround = strings.NewReplacer(`(?<![a-z])`, wordBefore, `(?![a-z])`, wordAfter,
	`(?<![A-Za-z])`, `(?:^|[^A-Za-z])`, `(?![A-Za-z])`, `(?:[^A-Za-z]|$)`)

// patternCache keeps compiled patterns: views match every frame. It is
// emptied when it grows past patternCacheSize, as while typing a pattern.
var (
	patternMu    sync.Mutex
	patternCache = map[string]compiled{}
)

const patternCacheSize = 256

type compiled struct {
	re  *regexp.Regexp
	err error
}

// CompileNodePattern compiles a node name pattern, nil for an empty one.
// FlClash's word boundaries (?<![a-z]) and (?![a-z]) are rewritten for RE2.
func CompileNodePattern(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	patternMu.Lock()
	defer patternMu.Unlock()
	if c, ok := patternCache[pattern]; ok {
		return c.re, c.err
	}
	re, err := regexp.Compile(lookaround.Replace(pattern))
	if len(patternCache) >= patternCacheSize {
		clear(patternCache)
	}
	patternCache[pattern] = compiled{re, err}
	return re, err
}

// RegionMatcher classifies node names by a region table.
type RegionMatcher struct {
	// table is every region of the table, for flags; regions those with
	// a valid pattern, res their patterns.
	table   []Region
	regions []Region
	res     []*regexp.Regexp
	info    []*regexp.Regexp
}

// NewRegionMatcher compiles a table; invalid patterns match nothing.
func NewRegionMatcher(table *RegionSettings) *RegionMatcher {
	m := &RegionMatcher{}
	if table == nil {
		return m
	}
	m.table = table.Regions
	for _, r := range table.Regions {
		if re, err := CompileNodePattern(r.Pattern); err == nil && re != nil {
			m.regions = append(m.regions, r)
			m.res = append(m.res, re)
		}
	}
	for _, r := range table.Info {
		if re, err := CompileNodePattern(r.Pattern); err == nil && re != nil {
			m.info = append(m.info, re)
		}
	}
	return m
}

// RegionOf is the region of name. A flag emoji in it decides (FlagIn):
// the table's region with that flag, else the first region whose pattern
// matches the flag, else the flag's country. A name whose only flag is
// 🇨🇳, a relay's entry, goes by the patterns first. Without a flag, the
// first region whose pattern matches name.
func (m *RegionMatcher) RegionOf(name string) (Region, bool) {
	flag, code, ok := FlagIn(name)
	if !ok {
		return m.byPattern(name)
	}
	for _, r := range m.table {
		if r.Flag == flag {
			return r, true
		}
	}
	if r, ok := m.byPattern(flag); ok {
		return r, true
	}
	if code == "CN" {
		if r, ok := m.byPattern(name); ok {
			return r, true
		}
	}
	return FlagRegion(flag, code), true
}

// byPattern is the first region whose pattern matches s.
func (m *RegionMatcher) byPattern(s string) (Region, bool) {
	for i, re := range m.res {
		if re.MatchString(s) {
			return m.regions[i], true
		}
	}
	return Region{}, false
}

// RegionsFound are the table's regions, then the countries of the flags
// outside it in names, in the order they appear.
func (m *RegionMatcher) RegionsFound(names []string) []Region {
	out := slices.Clone(m.table)
	for _, name := range names {
		if r, ok := m.RegionOf(name); ok && !slices.ContainsFunc(out, func(x Region) bool { return x.ID == r.ID }) {
			out = append(out, r)
		}
	}
	return out
}

// IsInfo reports whether name is a subscription info node.
func (m *RegionMatcher) IsInfo(name string) bool {
	return slices.ContainsFunc(m.info, func(re *regexp.Regexp) bool { return re.MatchString(name) })
}

// RegionOf is the region of a node name by the document's table.
func RegionOf(doc *Doc, name string) (Region, bool) {
	return NewRegionMatcher(Regions(doc)).RegionOf(name)
}

// IsFilterableNode reports whether an outbound may be a filtered member:
// an enabled proxy node (not a group, direct, block or bridge) whose
// subscription, if any, is enabled.
func IsFilterableNode(o *Outbound, subscriptions map[string]bool) bool {
	switch {
	case o == nil || !o.Enabled || IsGroup(o.Type):
		return false
	case o.Type == "direct" || o.Type == "block" || o.Type == "bridge":
		return false
	}
	return o.SubID == "" || subscriptions[o.SubID]
}

// FilterNodes are the nodes filters choose from, in configuration order.
func FilterNodes(doc *Doc) []*Outbound {
	subscriptions := map[string]bool{}
	for _, s := range doc.Subscriptions {
		if s.Enabled {
			subscriptions[s.ID] = true
		}
	}
	var out []*Outbound
	for _, o := range OrderedOutbounds(doc) {
		if IsFilterableNode(o, subscriptions) {
			out = append(out, o)
		}
	}
	return out
}

// MatchNodes returns the ids of the nodes a filter selects, in
// configuration order, skipping self. An invalid pattern selects none.
func MatchNodes(doc *Doc, filter *GroupFilter, self string) []string {
	if !filter.Active() || filter.Error() != "" {
		return nil
	}
	return matchNodes(FilterNodes(doc), NewRegionMatcher(Regions(doc)), filter, self)
}

func matchNodes(nodes []*Outbound, m *RegionMatcher, filter *GroupFilter, self string) []string {
	var out []string
	for _, h := range filterHits(nodes, m, filter, self) {
		if h.Excluded == "" {
			out = append(out, h.ID)
		}
	}
	return out
}

// FilterHit is a node the filter's regions and include condition select.
type FilterHit struct {
	ID string
	// Region is the region of the node's name, ID "" when unrecognized.
	Region Region
	// Keyword is the first of the filter's keywords the name contains,
	// "" when it passed by region only, "Custom" when only Pattern matched.
	Keyword string
	// Excluded is "" for a member, else why it is not one, in Chinese: the
	// name contains <kw> (the first exclude keyword it contains), a custom
	// exclusion (Exclude) or an info node (without KeepInfo).
	Excluded string
}

// FilterHits are the nodes a filter's regions and include condition
// select, in configuration order, skipping self, each with why it was
// selected or excluded; MatchNodes are those not excluded. None when the
// filter is inactive or has an invalid pattern, as MatchNodes, which emit
// then ignores with a warning.
func FilterHits(doc *Doc, filter *GroupFilter, self string) []FilterHit {
	if !filter.Active() || filter.Error() != "" {
		return nil
	}
	return filterHits(FilterNodes(doc), NewRegionMatcher(Regions(doc)), filter, self)
}

func filterHits(nodes []*Outbound, m *RegionMatcher, filter *GroupFilter, self string) []FilterHit {
	include, includeErr := CompileNodePattern(filter.Pattern)
	exclude, _ := CompileNodePattern(filter.Exclude)
	keywords, keywordREs := keywordMatchers(filter.Keywords)
	contains, _ := CompileNodePattern(keywordPattern(keywords))
	excludeKeywords, excludeREs := keywordMatchers(filter.ExcludeKeywords)
	conditioned := len(keywords) > 0 || strings.TrimSpace(filter.Pattern) != ""
	var out []FilterHit
	for _, o := range nodes {
		if o.ID == self {
			continue
		}
		r, ok := m.RegionOf(o.Tag)
		if len(filter.Regions) > 0 && (!ok || !slices.Contains(filter.Regions, r.ID)) {
			continue
		}
		hit := FilterHit{ID: o.ID, Region: r}
		if conditioned {
			switch {
			case contains != nil && contains.MatchString(o.Tag):
				for i, re := range keywordREs {
					if re != nil && re.MatchString(o.Tag) {
						hit.Keyword = keywords[i]
						break
					}
				}
			case include != nil && includeErr == nil && include.MatchString(o.Tag):
				hit.Keyword = "自定义"
			default:
				continue
			}
		}
		for i, re := range excludeREs {
			if re != nil && re.MatchString(o.Tag) {
				hit.Excluded = "名称含 " + excludeKeywords[i]
				break
			}
		}
		switch {
		case hit.Excluded != "":
		case exclude != nil && exclude.MatchString(o.Tag):
			hit.Excluded = "自定义排除"
		case !filter.KeepInfo && m.IsInfo(o.Tag):
			hit.Excluded = "信息节点"
		}
		out = append(out, hit)
	}
	return out
}

// GroupMembers are a group's members: the explicit ones, then those its
// filter selects, without duplicates. Without a filter it is o.Members.
func GroupMembers(doc *Doc, o *Outbound) []string {
	if o == nil || !o.Filter.Active() {
		if o == nil {
			return nil
		}
		return o.Members
	}
	out := slices.Clone(o.Members)
	for _, id := range MatchNodes(doc, o.Filter, o.ID) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// RegionNodes groups nodes by region in table order, then the countries
// of flags outside the table as they appear, the unrecognized last (region ID ""), skipping empty regions and, unless keepInfo, the
// info nodes.
type RegionNodes struct {
	Region Region
	Nodes  []*Outbound
}

// GroupByRegion groups nodes by the document's region table.
func GroupByRegion(doc *Doc, nodes []*Outbound, keepInfo bool) []RegionNodes {
	m := NewRegionMatcher(Regions(doc))
	byID := map[string][]*Outbound{}
	var other, kept []*Outbound
	for _, o := range nodes {
		if !keepInfo && m.IsInfo(o.Tag) {
			continue
		}
		kept = append(kept, o)
		if r, ok := m.RegionOf(o.Tag); ok {
			byID[r.ID] = append(byID[r.ID], o)
		} else {
			other = append(other, o)
		}
	}
	tags := make([]string, len(kept))
	for i, o := range kept {
		tags[i] = o.Tag
	}
	var out []RegionNodes
	for _, r := range m.RegionsFound(tags) {
		if list := byID[r.ID]; len(list) > 0 {
			out = append(out, RegionNodes{r, list})
			delete(byID, r.ID)
		}
	}
	if len(other) > 0 {
		out = append(out, RegionNodes{Region{Label: "未识别"}, other})
	}
	return out
}

// RegionGroupName is the name of a region's automatic group: flag and region name, as "🇭🇰 Hong Kong".
func RegionGroupName(r Region) string {
	return strings.TrimSpace(r.Flag + " " + RegionName(r))
}

// AddRegionGroups adds a URLTest group per region that has nodes, filtered
// by that region, and a Selector "Node selection" holding them, after the existing
// outbounds. Names taken already get a numeric suffix. It returns the new
// groups' ids, the Selector last; none when no region has nodes.
func AddRegionGroups(doc *Doc) []string {
	taken := map[string]bool{}
	for _, o := range doc.Outbounds {
		taken[o.Tag] = true
	}
	for _, e := range doc.Endpoints {
		taken[e.Tag] = true
	}
	unique := func(tag string) string {
		next := tag
		for i := 2; taken[next]; i++ {
			next = tag + " " + strconv.Itoa(i)
		}
		taken[next] = true
		return next
	}
	var ids []string
	for _, g := range GroupByRegion(doc, FilterNodes(doc), false) {
		if g.Region.ID == "" {
			continue
		}
		o := MakeOutbound("urltest", unique(RegionGroupName(g.Region)))
		o.Filter = &GroupFilter{Regions: []string{g.Region.ID}}
		doc.Outbounds = append(doc.Outbounds, o)
		if doc.OutboundOrder != nil {
			doc.OutboundOrder = append(doc.OutboundOrder, o.ID)
		}
		ids = append(ids, o.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	selector := MakeOutbound("selector", unique("节点选择"))
	selector.Members = slices.Clone(ids)
	doc.Outbounds = append(doc.Outbounds, selector)
	if doc.OutboundOrder != nil {
		doc.OutboundOrder = append(doc.OutboundOrder, selector.ID)
	}
	return append(ids, selector.ID)
}

// FlagIn is the flag emoji of name, a pair of regional indicator
// symbols, and its ISO 3166 country code ("HK"): the first one other
// than 🇨🇳, which marks a relay's entry ("🇨🇳→🇭🇰"), else 🇨🇳.
func FlagIn(name string) (flag, code string, ok bool) {
	runes := []rune(name)
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		if !isIndicator(a) || !isIndicator(b) {
			continue
		}
		f, c := string(runes[i:i+2]), string([]rune{'A' + a - 0x1F1E6, 'A' + b - 0x1F1E6})
		if c != "CN" {
			return f, c, true
		}
		if !ok {
			flag, code, ok = f, c, true
		}
		i++
	}
	return flag, code, ok
}

func isIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// FlagRegion is the region of a flag outside the table: its country code
// as the ID, lower case, and the country's Chinese name when known.
func FlagRegion(flag, code string) Region {
	label, _, ok := CountryName(code)
	if !ok {
		label = code
	}
	return Region{ID: strings.ToLower(code), Label: label, Flag: flag}
}
