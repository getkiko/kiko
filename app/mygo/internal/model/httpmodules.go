package model

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HTTP modules bundle MITM hostnames, rewrite rules, scripts and routing
// rules converted from a Surge module (.sgmodule) or a Kiko module
// (.kikomodule). The emitter reads Contents only; Raw is kept so the module
// converts again when its arguments change.

// Module limits.
const (
	MaxModules             = 64
	MaxModuleRaw           = 1 << 20
	MaxModuleName          = 128
	MaxModuleDescription   = 4096
	MaxModuleArguments     = 64
	MaxModuleArgumentValue = 4096
	MaxModuleHosts         = 1024
	MaxModuleRewrites      = 2048
	MaxModuleScripts       = 64
	MaxModuleRules         = 8192
	MaxModuleUnsupported   = 512
	// MaxModuleSources bounds the script texts and local-mapping bodies of
	// one module together.
	MaxModuleSources         = 4 << 20
	MaxModuleUpdateHours     = 24 * 365
	DefaultModuleUpdateHours = 24
	// MaxModuleScriptTag is the longest tag a module script may have: the
	// emitter prefixes it with the module id and a hyphen.
	MaxModuleScriptTag = 32
	MaxModuleIDLength  = 24
	ModuleIDPrefix     = "mod_"
)

// Module source kinds.
const (
	ModuleSourceURL    = "url"
	ModuleSourceFile   = "file"
	ModuleSourceInline = "inline"
)

// Module rule types and actions.
const (
	ModuleRuleDomain        = "domain"
	ModuleRuleDomainSuffix  = "domain-suffix"
	ModuleRuleDomainKeyword = "domain-keyword"
	ModuleRuleIPCIDR        = "ip-cidr"
	ModuleRuleIPCIDR6       = "ip-cidr6"
	// ModuleRuleScript routes by a rule script of the module; Value is the
	// script's tag inside the module.
	ModuleRuleScript = "script"

	ModuleActionDirect = "direct"
	ModuleActionReject = "reject"
)

// ModuleSource says where a module came from.
type ModuleSource struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

// ModuleArgument is one module argument. Value empty means Default.
type ModuleArgument struct {
	Name        string `json:"name"`
	Default     string `json:"default,omitempty"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
}

// Effective returns the value the module converts with.
func (a ModuleArgument) Effective() string {
	if a.Value != "" {
		return a.Value
	}
	return a.Default
}

// UnsupportedItem is a line or section of the module that has no
// counterpart; it is listed, never silently dropped.
type UnsupportedItem struct {
	Section string `json:"section"`
	Line    int    `json:"line,omitempty"`
	Reason  string `json:"reason"`
	// Text is the (shortened) original line.
	Text string `json:"text,omitempty"`
}

// ModuleRule is a routing rule of a module: DOMAIN*, IP-CIDR* with a
// DIRECT or REJECT action.
type ModuleRule struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Action    string `json:"action"`
	NoResolve bool   `json:"noResolve,omitempty"`
}

// ModuleContents is the converted content of a module.
type ModuleContents struct {
	Hostnames *MITMHostnames `json:"hostnames,omitempty"`
	Rewrites  *HTTPRewrites  `json:"rewrites,omitempty"`
	Scripts   []*HTTPScript  `json:"scripts,omitempty"`
	Rules     []*ModuleRule  `json:"rules,omitempty"`
}

// ModuleApproved lists what the user confirmed: MITM include hostnames,
// scripts (ScriptApprovalKey: text and triggers) and DIRECT routing rules
// ("type,value").
type ModuleApproved struct {
	Hostnames []string `json:"hostnames,omitempty"`
	Scripts   []string `json:"scripts,omitempty"`
	Direct    []string `json:"direct,omitempty"`
}

// ModulePending is an update waiting for the user's confirmation because it
// adds hostnames or scripts; the old Contents stay in force meanwhile.
type ModulePending struct {
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Version     string            `json:"version,omitempty"`
	Arguments   []ModuleArgument  `json:"arguments,omitempty"`
	Raw         string            `json:"raw,omitempty"`
	Hash        string            `json:"hash,omitempty"`
	Contents    ModuleContents    `json:"contents"`
	Unsupported []UnsupportedItem `json:"unsupported,omitempty"`
	// Added previews the new hostnames and scripts.
	Added ModuleDiff `json:"added"`
}

// HTTPModule is an installed module.
type HTTPModule struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Source      ModuleSource `json:"source"`
	Version     string       `json:"version,omitempty"`
	// UpdatedAt is the time (ms) the content last changed.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
	// UpdateInterval is in hours; 0 updates only by hand.
	UpdateInterval int              `json:"updateInterval"`
	Enabled        bool             `json:"enabled"`
	Arguments      []ModuleArgument `json:"arguments,omitempty"`
	// Raw is the module text; Hash its SHA-256.
	Raw         string            `json:"raw,omitempty"`
	Hash        string            `json:"hash,omitempty"`
	Contents    ModuleContents    `json:"contents"`
	Unsupported []UnsupportedItem `json:"unsupported,omitempty"`
	Approved    ModuleApproved    `json:"approved"`
	Pending     *ModulePending    `json:"pending,omitempty"`
	// Updated marks a content change the user has not looked at yet.
	Updated       bool   `json:"updated,omitempty"`
	LastCheckedAt int64  `json:"lastCheckedAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// NewHTTPModule returns an empty module with a fresh id and the default
// update interval.
func NewHTTPModule() *HTTPModule {
	return &HTTPModule{ID: NewID("mod"), UpdateInterval: DefaultModuleUpdateHours}
}

// HashModuleRaw returns the hex SHA-256 of a module text.
func HashModuleRaw(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ScriptSHA256 returns the hex SHA-256 of the script text.
func ScriptSHA256(s *HTTPScript) string { return HashModuleRaw(s.Source) }

// ScriptApprovalKey identifies what the user confirms about a script: its
// text together with what makes it run (type, pattern, cron, body options,
// argument, size cap and timeout). A module that keeps a script's text and
// widens its pattern needs a new confirmation.
func ScriptApprovalKey(s *HTTPScript) string {
	var b strings.Builder
	for _, part := range []string{ScriptSHA256(s), s.Type, s.Pattern, s.Cron, strconv.FormatBool(s.RequiresBody),
		strconv.FormatBool(s.BinaryBody), s.Argument, strconv.Itoa(s.MaxSize), strconv.Itoa(s.Timeout)} {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return HashModuleRaw(b.String())
}

// ModuleRuleKey is the confirmation key of a routing rule.
func ModuleRuleKey(r *ModuleRule) string { return r.Type + "," + r.Value }

// Empty reports whether the contents hold nothing.
func (c *ModuleContents) Empty() bool {
	return len(c.Hostnames.includes()) == 0 && len(c.Hostnames.excludes()) == 0 &&
		c.Rewrites.Empty() && len(c.Scripts) == 0 && len(c.Rules) == 0
}

func (h *MITMHostnames) includes() []string {
	if h == nil {
		return nil
	}
	return h.Include
}

func (h *MITMHostnames) excludes() []string {
	if h == nil {
		return nil
	}
	return h.Exclude
}

// ModuleCounts are the item counts of a module's contents.
type ModuleCounts struct {
	Hostnames, Rewrites, Scripts, Rules int
}

// Counts returns the item counts.
func (c *ModuleContents) Counts() ModuleCounts {
	n := ModuleCounts{
		Hostnames: len(c.Hostnames.includes()) + len(c.Hostnames.excludes()),
		Scripts:   len(c.Scripts),
		Rules:     len(c.Rules),
	}
	if c.Rewrites != nil {
		n.Rewrites = len(c.Rewrites.URL) + len(c.Rewrites.Headers) + len(c.Rewrites.MapLocal) + len(c.Rewrites.Body)
	}
	return n
}

// ValidModuleRuleType reports whether t is a module rule type.
func ValidModuleRuleType(t string) bool {
	switch t {
	case ModuleRuleDomain, ModuleRuleDomainSuffix, ModuleRuleDomainKeyword, ModuleRuleIPCIDR, ModuleRuleIPCIDR6, ModuleRuleScript:
		return true
	}
	return false
}

// Issue returns the first problem of the rule; empty when it is valid.
func (r *ModuleRule) Issue() string {
	if r == nil || !ValidModuleRuleType(r.Type) {
		return "未知的规则类型"
	}
	if r.Action != ModuleActionDirect && r.Action != ModuleActionReject {
		return "未知的动作"
	}
	switch r.Type {
	case ModuleRuleScript:
		if ScriptTagIssue(r.Value) != "" || len(r.Value) > MaxModuleScriptTag {
			return "脚本标识无效"
		}
	case ModuleRuleDomain, ModuleRuleDomainSuffix:
		if !validRuleDomain(r.Value) {
			return "域名无效"
		}
	case ModuleRuleDomainKeyword:
		if r.Value == "" || len(r.Value) > 253 || strings.ContainsAny(r.Value, " ,\t\r\n\x00") || !utf8.ValidString(r.Value) {
			return "关键字无效"
		}
	case ModuleRuleIPCIDR, ModuleRuleIPCIDR6:
		p, err := netip.ParsePrefix(r.Value)
		if err != nil || p.Addr().Zone() != "" {
			return "IP 段无效"
		}
		if r.Type == ModuleRuleIPCIDR && !p.Addr().Is4() || r.Type == ModuleRuleIPCIDR6 && !p.Addr().Is6() || p.Addr().Is4In6() {
			return "IP 段与类型不符"
		}
		if p.Addr().Is4() && p.Bits() < MinModuleIPv4Bits || p.Addr().Is6() && p.Bits() < MinModuleIPv6Bits {
			return ModuleRuleTooBroad
		}
	}
	if moduleRuleTooBroad(r) {
		return ModuleRuleTooBroad
	}
	return ""
}

// Module rules that reach too far are refused: they would send a large share
// of the traffic past the proxy (DIRECT) or break it (REJECT).
const (
	MinModuleIPv4Bits  = 8
	MinModuleIPv6Bits  = 32
	MinModuleKeyword   = 3
	ModuleRuleTooBroad = "范围过大"
)

func moduleRuleTooBroad(r *ModuleRule) bool {
	switch r.Type {
	case ModuleRuleDomainSuffix:
		return !strings.Contains(r.Value, ".")
	case ModuleRuleDomainKeyword:
		return len(r.Value) < MinModuleKeyword
	}
	return false
}

func validRuleDomain(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}

// Issue returns the first problem of the contents; empty when they are
// valid. Items are checked with their own Issue methods.
func (c *ModuleContents) Issue() string {
	for _, list := range [][]string{c.Hostnames.includes(), c.Hostnames.excludes()} {
		if len(list) > MaxModuleHosts {
			return "主机名过多"
		}
		if r := NormalizeMITMHostnames(list); len(r.Invalid) > 0 {
			return "主机名「" + r.Invalid[0] + "」无效"
		}
	}
	if rw := c.Rewrites; rw != nil {
		if len(rw.URL) > MaxModuleRewrites || len(rw.Headers) > MaxModuleRewrites || len(rw.MapLocal) > MaxModuleRewrites || len(rw.Body) > MaxModuleRewrites {
			return "改写规则过多"
		}
		if rw.MaxBodySize != 0 {
			return "模块不能设置正文缓冲上限"
		}
		for i, r := range rw.URL {
			if r == nil {
				return "URL 改写第 " + strconv.Itoa(i+1) + " 条为空"
			}
			if issue := r.Issue(); issue != "" {
				return "URL 改写第 " + strconv.Itoa(i+1) + " 条：" + issue
			}
		}
		for i, r := range rw.Headers {
			if r == nil {
				return "头改写第 " + strconv.Itoa(i+1) + " 条为空"
			}
			if issue := r.Issue(); issue != "" {
				return "头改写第 " + strconv.Itoa(i+1) + " 条：" + issue
			}
		}
		for i, r := range rw.Body {
			if r == nil {
				return "正文改写第 " + strconv.Itoa(i+1) + " 条为空"
			}
			if issue := r.Issue(); issue != "" {
				return "正文改写第 " + strconv.Itoa(i+1) + " 条：" + issue
			}
		}
		for i, r := range rw.MapLocal {
			if r == nil {
				return "本地映射第 " + strconv.Itoa(i+1) + " 条为空"
			}
			if issue := r.Issue(); issue != "" {
				return "本地映射第 " + strconv.Itoa(i+1) + " 条：" + issue
			}
		}
	}
	if len(c.Scripts) > MaxModuleScripts {
		return "脚本过多"
	}
	seen := map[string]bool{}
	for i, s := range c.Scripts {
		if s == nil {
			return "脚本第 " + strconv.Itoa(i+1) + " 个为空"
		}
		if issue := s.Issue(); issue != "" {
			return "脚本「" + s.EffectiveTag() + "」：" + issue
		}
		tag := s.EffectiveTag()
		if len(tag) > MaxModuleScriptTag {
			return "脚本标识「" + tag + "」过长"
		}
		if seen[tag] {
			return "脚本标识「" + tag + "」重复"
		}
		seen[tag] = true
	}
	if len(c.Rules) > MaxModuleRules {
		return "规则过多"
	}
	for i, r := range c.Rules {
		if issue := r.Issue(); issue != "" {
			return "规则第 " + strconv.Itoa(i+1) + " 条：" + issue
		}
	}
	if c.SourceBytes() > MaxModuleSources {
		return "脚本与映射内容超过 4 MiB"
	}
	return ""
}

// SourceBytes is the size of the script texts and local-mapping bodies.
func (c *ModuleContents) SourceBytes() int {
	n := 0
	for _, s := range c.Scripts {
		if s != nil {
			n += len(s.Source)
		}
	}
	if c.Rewrites != nil {
		for _, r := range c.Rewrites.MapLocal {
			if r != nil {
				n += r.BodyBytes()
			}
		}
	}
	return n
}

// ValidModuleID reports whether id is a module id: "mod_" and lowercase
// letters or digits. The shape keeps script tags (id + "-" + tag)
// unambiguous.
func ValidModuleID(id string) bool {
	rest, ok := strings.CutPrefix(id, ModuleIDPrefix)
	if !ok || rest == "" || len(id) > MaxModuleIDLength {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func plainText(s string, limit int) bool {
	if len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' || r == 0x7f {
			return false
		}
	}
	return true
}

// ModuleArgumentNameIssue explains why name is not an argument name.
func ModuleArgumentNameIssue(name string) string {
	if name == "" || len(name) > 64 || !utf8.ValidString(name) {
		return "参数名无效"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("{}:,=\"'", r) || r == ' ' {
			return "参数名含有无效字符"
		}
	}
	return ""
}

// Issue returns the first problem of the module; empty when it is valid.
func (m *HTTPModule) Issue() string {
	if !ValidModuleID(m.ID) {
		return "模块标识无效"
	}
	if strings.TrimSpace(m.Name) == "" {
		return "请填写模块名称"
	}
	if !plainText(m.Name, MaxModuleName) || strings.ContainsRune(m.Name, '\n') {
		return "模块名称无效"
	}
	if !plainText(m.Description, MaxModuleDescription) {
		return "模块说明无效"
	}
	if !plainText(m.Version, 64) || strings.ContainsRune(m.Version, '\n') {
		return "模块版本无效"
	}
	switch m.Source.Kind {
	case ModuleSourceURL:
		if m.Source.URL == "" || len(m.Source.URL) > 2048 {
			return "模块地址无效"
		}
	case ModuleSourceFile, ModuleSourceInline:
	default:
		return "未知的模块来源"
	}
	if m.UpdateInterval < 0 || m.UpdateInterval > MaxModuleUpdateHours {
		return "更新间隔须在 0 到 8760 小时之间"
	}
	if len(m.Raw) > MaxModuleRaw {
		return "模块内容超过 1 MiB"
	}
	if len(m.Arguments) > MaxModuleArguments {
		return "参数过多"
	}
	names := map[string]bool{}
	for _, a := range m.Arguments {
		if issue := ModuleArgumentNameIssue(a.Name); issue != "" {
			return issue
		}
		if names[a.Name] {
			return "参数「" + a.Name + "」重复"
		}
		names[a.Name] = true
		if len(a.Default) > MaxModuleArgumentValue || len(a.Value) > MaxModuleArgumentValue ||
			strings.ContainsAny(a.Value, "\r\n\x00") || !utf8.ValidString(a.Value) {
			return "参数「" + a.Name + "」的值无效"
		}
	}
	if issue := m.Contents.Issue(); issue != "" {
		return issue
	}
	for _, s := range m.Contents.Scripts {
		if len(m.ID)+1+len(s.EffectiveTag()) > MaxScriptTag {
			return "脚本标识「" + s.EffectiveTag() + "」过长"
		}
	}
	if m.Pending != nil {
		if issue := m.Pending.Contents.Issue(); issue != "" {
			return "待确认的更新：" + issue
		}
	}
	return ""
}

// ModulesIssue checks the module list: count, unique ids and each module.
func (t *HTTPTools) ModulesIssue() string {
	if t == nil {
		return ""
	}
	if len(t.Modules) > MaxModules {
		return "模块过多"
	}
	seen := map[string]bool{}
	for _, m := range t.Modules {
		if m == nil {
			return "模块为空"
		}
		if seen[m.ID] {
			return "模块标识重复"
		}
		seen[m.ID] = true
		if issue := m.Issue(); issue != "" {
			return "模块「" + m.Name + "」：" + issue
		}
	}
	return ""
}

// EnabledModules reports whether any module is enabled.
func (t *HTTPTools) EnabledModules() bool {
	if t == nil {
		return false
	}
	return slices.ContainsFunc(t.Modules, func(m *HTTPModule) bool { return m != nil && m.Enabled })
}

// FindModule returns the module with the id; nil when absent.
func (t *HTTPTools) FindModule(id string) *HTTPModule {
	if t == nil {
		return nil
	}
	for _, m := range t.Modules {
		if m != nil && m.ID == id {
			return m
		}
	}
	return nil
}

// ModuleScriptChange is a script a module adds or changes.
type ModuleScriptChange struct {
	Tag  string `json:"tag"`
	Name string `json:"name,omitempty"`
	// SHA256 is the text's digest; Key the confirmation key (ScriptApprovalKey).
	SHA256 string `json:"sha256"`
	Key    string `json:"key"`
	// Type, Pattern and Cron say when the script runs.
	Type    string `json:"type,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Cron    string `json:"cron,omitempty"`
	// Changed marks a script whose tag existed with another text or other
	// triggers.
	Changed bool `json:"changed,omitempty"`
}

// ModuleDiff lists the sensitive additions of module contents: MITM
// hostnames to decrypt, scripts to run and DIRECT rules that bypass the
// proxy (as "type,value").
type ModuleDiff struct {
	Hostnames []string             `json:"hostnames,omitempty"`
	Scripts   []ModuleScriptChange `json:"scripts,omitempty"`
	Direct    []string             `json:"direct,omitempty"`
}

// Empty reports whether nothing needs confirmation.
func (d ModuleDiff) Empty() bool {
	return len(d.Hostnames) == 0 && len(d.Scripts) == 0 && len(d.Direct) == 0
}

// moduleIncludes returns the normalised include hosts.
func moduleIncludes(c ModuleContents) []string {
	return NormalizeMITMHostnames(c.Hostnames.includes()).Hosts
}

func scriptChanges(scripts []*HTTPScript, known func(key string) bool, oldByTag map[string]string) []ModuleScriptChange {
	var out []ModuleScriptChange
	seen := map[string]bool{}
	for _, s := range scripts {
		if s == nil {
			continue
		}
		key := ScriptApprovalKey(s)
		if known(key) || seen[key] {
			continue
		}
		seen[key] = true
		tag := s.EffectiveTag()
		_, existed := oldByTag[tag]
		out = append(out, ModuleScriptChange{Tag: tag, Name: s.Name, SHA256: ScriptSHA256(s), Key: key,
			Type: s.Type, Pattern: s.Pattern, Cron: s.Cron, Changed: existed})
	}
	return out
}

// directRules returns the keys of the DIRECT rules of contents that known
// does not hold, once each.
func directRules(rules []*ModuleRule, known func(key string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rules {
		if r == nil || r.Action != ModuleActionDirect {
			continue
		}
		key := ModuleRuleKey(r)
		if known(key) || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// NewModuleChange lists the hostnames, scripts and DIRECT rules next adds to
// old: include hostnames not in old, scripts whose text and triggers are not
// in old (Changed when the tag existed otherwise) and DIRECT rules not in old.
func NewModuleChange(old, next ModuleContents) ModuleDiff {
	var diff ModuleDiff
	have := map[string]bool{}
	for _, h := range moduleIncludes(old) {
		have[h] = true
	}
	for _, h := range moduleIncludes(next) {
		if !have[h] {
			diff.Hostnames = append(diff.Hostnames, h)
		}
	}
	keys := map[string]bool{}
	byTag := map[string]string{}
	for _, s := range old.Scripts {
		if s != nil {
			key := ScriptApprovalKey(s)
			keys[key] = true
			byTag[s.EffectiveTag()] = key
		}
	}
	diff.Scripts = scriptChanges(next.Scripts, func(key string) bool { return keys[key] }, byTag)
	oldDirect := map[string]bool{}
	for _, r := range old.Rules {
		if r != nil && r.Action == ModuleActionDirect {
			oldDirect[ModuleRuleKey(r)] = true
		}
	}
	diff.Direct = directRules(next.Rules, func(key string) bool { return oldDirect[key] })
	return diff
}

func stringSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	return set
}

// NeedsApproval lists what contents add beyond what the user already
// approved: include hostnames, scripts (text and triggers) and DIRECT rules
// missing from Approved. An empty result means the contents may take effect
// without asking.
func (m *HTTPModule) NeedsApproval(contents ModuleContents) ModuleDiff {
	var diff ModuleDiff
	hosts := stringSet(m.Approved.Hostnames)
	for _, h := range moduleIncludes(contents) {
		if !hosts[h] {
			diff.Hostnames = append(diff.Hostnames, h)
		}
	}
	keys := stringSet(m.Approved.Scripts)
	byTag := map[string]string{}
	for _, s := range m.Contents.Scripts {
		if s != nil {
			byTag[s.EffectiveTag()] = ScriptApprovalKey(s)
		}
	}
	diff.Scripts = scriptChanges(contents.Scripts, func(key string) bool { return keys[key] }, byTag)
	diff.Direct = m.unapprovedDirect(contents)
	return diff
}

// unapprovedDirect lists the DIRECT rules of contents the user has not
// confirmed.
func (m *HTTPModule) unapprovedDirect(contents ModuleContents) []string {
	if len(contents.Rules) == 0 {
		return nil
	}
	approved := stringSet(m.Approved.Direct)
	return directRules(contents.Rules, func(key string) bool { return approved[key] })
}

// Approve records the hostnames, scripts and DIRECT rules of diff as
// confirmed.
func (m *HTTPModule) Approve(diff ModuleDiff) {
	for _, h := range diff.Hostnames {
		if !slices.Contains(m.Approved.Hostnames, h) {
			m.Approved.Hostnames = append(m.Approved.Hostnames, h)
		}
	}
	for _, s := range diff.Scripts {
		if !slices.Contains(m.Approved.Scripts, s.Key) {
			m.Approved.Scripts = append(m.Approved.Scripts, s.Key)
		}
	}
	have := stringSet(m.Approved.Direct)
	for _, key := range diff.Direct {
		if !have[key] {
			have[key] = true
			m.Approved.Direct = append(m.Approved.Direct, key)
		}
	}
	sort.Strings(m.Approved.Hostnames)
	sort.Strings(m.Approved.Scripts)
	sort.Strings(m.Approved.Direct)
}

// ApproveContents confirms everything contents adds.
func (m *HTTPModule) ApproveContents(contents ModuleContents) {
	m.Approve(m.NeedsApproval(contents))
}

// ModuleUpdate is a converted module text, ready to install or update.
type ModuleUpdate struct {
	Name        string
	Description string
	Version     string
	Arguments   []ModuleArgument
	Raw         string
	Contents    ModuleContents
	Unsupported []UnsupportedItem
}

func (m *HTTPModule) apply(u ModuleUpdate, now int64) {
	m.Name, m.Description, m.Version = u.Name, u.Description, u.Version
	m.Arguments = u.Arguments
	m.Raw, m.Hash = u.Raw, HashModuleRaw(u.Raw)
	m.Contents = u.Contents
	m.Unsupported = u.Unsupported
	m.UpdatedAt = now
	m.Pending = nil
}

// UpdateResult is what AcceptUpdate did.
type UpdateResult struct {
	// Pending: the contents add hostnames, scripts or DIRECT rules and wait
	// for the user; the old contents stay in force.
	Pending bool
	// Applied: the update took effect (a text, argument or content change).
	Applied bool
}

// AcceptUpdate installs a freshly converted text. A text that converts to the
// same content and arguments only refreshes LastCheckedAt; the same text with
// other arguments or other downloaded scripts is an update like a new text.
// A change that adds nothing sensitive takes effect at once and sets Updated;
// one that adds hostnames, scripts or DIRECT rules waits in Pending (the old
// contents stay) and AcceptUpdate returns true.
func (m *HTTPModule) AcceptUpdate(u ModuleUpdate, now int64) (pending bool) {
	return m.Accept(u, now).Pending
}

// Accept is AcceptUpdate with the full result.
func (m *HTTPModule) Accept(u ModuleUpdate, now int64) UpdateResult {
	m.LastCheckedAt = now
	m.LastError = ""
	hash := HashModuleRaw(u.Raw)
	if m.Pending != nil && m.Pending.Hash == hash && reflect.DeepEqual(m.Pending.Contents, u.Contents) {
		return UpdateResult{Pending: true}
	}
	// The same text converts to other content when the arguments change or
	// a downloaded script changed, so the contents are compared too.
	if m.Pending == nil && hash == m.Hash && reflect.DeepEqual(m.Contents, u.Contents) && reflect.DeepEqual(m.Arguments, u.Arguments) {
		return UpdateResult{}
	}
	if diff := m.NeedsApproval(u.Contents); !diff.Empty() {
		m.Pending = &ModulePending{
			Name: u.Name, Description: u.Description, Version: u.Version, Arguments: u.Arguments,
			Raw: u.Raw, Hash: hash, Contents: u.Contents, Unsupported: u.Unsupported,
			Added: NewModuleChange(m.Contents, u.Contents),
		}
		return UpdateResult{Pending: true}
	}
	m.apply(u, now)
	m.Updated = true
	return UpdateResult{Applied: true}
}

// Fingerprint identifies the pending update's content: the dialog that asks
// for the decision records it and the decision is refused when it changed.
func (p *ModulePending) Fingerprint() string {
	var b strings.Builder
	b.WriteString(p.Hash)
	for _, a := range p.Arguments {
		b.WriteString("\na:" + a.Name + "=" + a.Effective())
	}
	for _, h := range moduleIncludes(p.Contents) {
		b.WriteString("\nh:" + h)
	}
	for _, s := range p.Contents.Scripts {
		if s != nil {
			b.WriteString("\ns:" + ScriptApprovalKey(s))
		}
	}
	for _, r := range p.Contents.Rules {
		if r != nil && r.Action == ModuleActionDirect {
			b.WriteString("\nd:" + ModuleRuleKey(r))
		}
	}
	return HashModuleRaw(b.String())
}

// ApprovePending confirms the pending update and makes it take effect.
func (m *HTTPModule) ApprovePending(now int64) bool {
	p := m.Pending
	if p == nil {
		return false
	}
	m.ApproveContents(p.Contents)
	m.apply(ModuleUpdate{Name: p.Name, Description: p.Description, Version: p.Version, Arguments: p.Arguments,
		Raw: p.Raw, Contents: p.Contents, Unsupported: p.Unsupported}, now)
	m.Updated = true
	return true
}

// DiscardPending drops the pending update.
func (m *HTTPModule) DiscardPending() { m.Pending = nil }

// ActiveContents returns the contents that take effect: unapproved include
// hostnames, unapproved scripts and unapproved DIRECT rules are left out and
// named in skipped.
func (m *HTTPModule) ActiveContents() (active ModuleContents, skippedHosts []string, skippedScripts []string, skippedDirect []string) {
	active = m.Contents
	diff := m.NeedsApproval(m.Contents)
	if diff.Empty() {
		return active, nil, nil, nil
	}
	if len(diff.Direct) > 0 {
		skippedDirect = diff.Direct
		block := stringSet(diff.Direct)
		active.Rules = nil
		for _, r := range m.Contents.Rules {
			if r != nil && r.Action == ModuleActionDirect && block[ModuleRuleKey(r)] {
				continue
			}
			active.Rules = append(active.Rules, r)
		}
	}
	if len(diff.Hostnames) > 0 && active.Hostnames != nil {
		skippedHosts = diff.Hostnames
		hosts := *active.Hostnames
		hosts.Include = nil
		for _, h := range active.Hostnames.Include {
			if n, ok := NormalizeMITMHostname(h); ok && slices.Contains(diff.Hostnames, n) {
				continue
			}
			hosts.Include = append(hosts.Include, h)
		}
		active.Hostnames = &hosts
	}
	if len(diff.Scripts) > 0 {
		block := map[string]bool{}
		for _, s := range diff.Scripts {
			block[s.Key] = true
		}
		active.Scripts = nil
		for _, s := range m.Contents.Scripts {
			if s != nil && block[ScriptApprovalKey(s)] {
				skippedScripts = append(skippedScripts, s.EffectiveTag())
				continue
			}
			active.Scripts = append(active.Scripts, s)
		}
	}
	return active, skippedHosts, skippedScripts, skippedDirect
}
