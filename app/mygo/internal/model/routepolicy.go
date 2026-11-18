package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the route policy: device scopes, condition trees, their
// summaries and validation.

// Device scopes of the route and DNS policies.
const (
	// PreDeviceID owns rules matched before every device.
	PreDeviceID = "pre"
	// LocalDeviceID is the host or gateway itself.
	LocalDeviceID = "local"
	// OtherDeviceID owns rules of devices without a scope of their own.
	OtherDeviceID = "other"
)

// IsFixedDevice reports the built-in scopes pre, local and other.
func IsFixedDevice(id string) bool {
	return id == PreDeviceID || id == LocalDeviceID || id == OtherDeviceID
}

// DeviceOwner is the scope a rule or group belongs to: its device id, or
// local when unset.
func DeviceOwner(deviceID string) string {
	if deviceID == "" {
		return LocalDeviceID
	}
	return deviceID
}

// SystemRouteRules are the built-in rules every configuration starts with.
func SystemRouteRules() []map[string]any {
	return []map[string]any{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}
}

// IsSystemRouteRule reports a stored copy of a built-in rule; only the
// canonical forms are absorbed, custom sniff conditions stay.
func IsSystemRouteRule(rule *RouteRule) bool {
	if rule.DeviceID != "" || rule.ConditionTree != nil || rule.Logic == "or" || rule.OverrideDestination {
		return false
	}
	keys := len(rule.Match)
	if rule.Action == ActionSniff && keys == 0 {
		return true
	}
	if rule.Action == ActionHijackDNS && keys == 1 {
		v, ok := rule.Match["protocol"]
		return ok && strings.Join(Values(v), ",") == "dns"
	}
	return false
}

// MigrateRoutePolicy completes a stored route section: default groups for
// every scope, rules in valid groups, built-in rules absorbed, and the
// local scope's default outbound.
func MigrateRoutePolicy(route Route) Route {
	devices := route.Devices
	if devices == nil {
		devices = []*RouteDevice{}
	}
	localEnabled := route.LocalEnabled == nil || *route.LocalEnabled
	groups := []*RouteGroup{}
	for _, g := range route.Groups {
		if localEnabled || !IsLocalScope(DeviceOwner(g.DeviceID)) {
			copied := *g
			groups = append(groups, &copied)
		}
	}
	owners := []string{PreDeviceID}
	if localEnabled {
		owners = append(owners, DeviceScopes(localDeviceOf(&route))...)
	}
	owners = append(owners, OtherDeviceID)
	for _, d := range devices {
		owners = append(owners, DeviceScopes(d)...)
	}
	for _, owner := range owners {
		if !slices.ContainsFunc(groups, func(g *RouteGroup) bool { return DeviceOwner(g.DeviceID) == owner }) {
			g := &RouteGroup{ID: NewID("rg"), Name: "默认分组"}
			if owner != LocalDeviceID {
				g.DeviceID = owner
			}
			groups = append(groups, g)
		}
	}
	rules := []*RouteRule{}
	for _, rule := range route.Rules {
		if !localEnabled && IsLocalScope(DeviceOwner(rule.DeviceID)) || IsSystemRouteRule(rule) {
			continue
		}
		owner := DeviceOwner(rule.DeviceID)
		valid := slices.ContainsFunc(groups, func(g *RouteGroup) bool { return g.ID == rule.GroupID && DeviceOwner(g.DeviceID) == owner })
		if !valid {
			copied := *rule
			copied.GroupID = ""
			for _, g := range groups {
				if DeviceOwner(g.DeviceID) == owner {
					copied.GroupID = g.ID
					break
				}
			}
			rule = &copied
		}
		rules = append(rules, rule)
	}
	next := route
	next.Devices, next.Groups, next.Rules = devices, groups, rules
	if localEnabled {
		next.LocalFinal = route.LocalFinal.Or(route.Final)
	} else {
		next.LocalFinal = Null()
		next.LocalAddresses = []string{}
		next.LocalProfiles, next.LocalActiveProfile = nil, ""
	}
	findProcess := true
	next.FindProcess = &findProcess
	return next
}

// EmptyConditionGroup is a new rule's condition: all of nothing.
func EmptyConditionGroup() *Condition {
	return &Condition{Kind: ConditionGroup, Mode: "and", Rules: []*Condition{}}
}

// ConditionMatches returns the leaves' matches of a tree, in order.
func ConditionMatches(node *Condition) []map[string]any {
	if node == nil {
		return nil
	}
	if node.Kind == ConditionMatch {
		return []map[string]any{node.Match}
	}
	var out []map[string]any
	for _, rule := range node.Rules {
		out = append(out, ConditionMatches(rule)...)
	}
	return out
}

// LegacyCondition converts a stored flat match into a condition tree.
// An "or" rule becomes one leaf per field; an "and" rule stays a single
// leaf, since sing-box combines some fields specially.
func LegacyCondition(match map[string]any, logic string) *Condition {
	raw := jsv.CloneMap(match)
	if raw == nil {
		raw = map[string]any{}
	}
	invert := raw["invert"] == true
	delete(raw, "invert")
	if logic == "or" {
		rules := []*Condition{}
		for _, key := range matchKeys(raw) {
			rules = append(rules, &Condition{Kind: ConditionMatch, Match: map[string]any{key: raw[key]}})
		}
		return &Condition{Kind: ConditionGroup, Mode: "or", Invert: invert, Rules: rules}
	}
	rules := []*Condition{}
	if len(raw) > 0 {
		rules = append(rules, &Condition{Kind: ConditionMatch, Match: raw})
	}
	return &Condition{Kind: ConditionGroup, Mode: "and", Invert: invert, Rules: rules}
}

// ConditionLabelKeys orders the condition fields; ConditionLabels names
// them.
var ConditionLabelKeys = []string{
	"source_port", "source_port_range", "ip_cidr", "port", "port_range", "domain", "domain_suffix",
	"domain_keyword", "domain_regex", "rule_set", "network", "protocol", "process_name", "process_path",
	"process_path_regex", "inbound", "query_type", "ip_is_private", "ip_version", "clash_mode",
	"package_name", "preferred_by", "source_ip_cidr", "source_mac_address", "source_hostname", "auth_user",
	"client", "user", "user_id", "network_type", "network_is_expensive", "network_is_constrained",
	"wifi_ssid", "wifi_bssid", "interface_address", "network_interface_address", "default_interface_address",
	"script",
}

// ConditionLabels names condition fields for display.
var ConditionLabels = map[string]string{
	"source_port":               "来源端口",
	"source_port_range":         "来源端口范围",
	"ip_cidr":                   "目标 IP",
	"port":                      "目标端口",
	"port_range":                "目标端口范围",
	"domain":                    "完整域名",
	"domain_suffix":             "域名后缀",
	"domain_keyword":            "域名关键词",
	"domain_regex":              "域名正则",
	"rule_set":                  "规则集",
	"network":                   "传输协议",
	"protocol":                  "应用协议",
	"process_name":              "进程名",
	"process_path":              "进程路径",
	"process_path_regex":        "进程路径正则",
	"inbound":                   "接入入口",
	"query_type":                "查询类型",
	"ip_is_private":             "私有目标",
	"ip_version":                "IP 版本",
	"clash_mode":                "Clash 模式",
	"package_name":              "Android 应用",
	"preferred_by":              "首选资源",
	"source_ip_cidr":            "旧版来源 IP",
	"source_mac_address":        "旧版来源 MAC",
	"source_hostname":           "旧版来源主机名",
	"auth_user":                 "认证用户",
	"client":                    "客户端类型",
	"user":                      "系统用户",
	"user_id":                   "用户 ID",
	"network_type":              "网络接口类型",
	"network_is_expensive":      "按流量计费网络",
	"network_is_constrained":    "低数据模式",
	"wifi_ssid":                 "Wi-Fi 名称",
	"wifi_bssid":                "Wi-Fi 接入点",
	"interface_address":         "接口地址",
	"network_interface_address": "接口类型地址",
	"default_interface_address": "默认接口地址",
	"script":                    "脚本条件",
}

func conditionLabel(key string) string {
	if label := ConditionLabels[key]; label != "" {
		return label
	}
	return key
}

// matchKeys orders a match's keys for display and validation: the
// labelled fields in their table order, then the rest by name (a decoded
// Go map has no order of its own).
func matchKeys(match map[string]any) []string {
	keys := make([]string, 0, len(match))
	for _, k := range ConditionLabelKeys {
		if _, ok := match[k]; ok {
			keys = append(keys, k)
		}
	}
	for _, k := range sortedKeys(match) {
		if _, labelled := ConditionLabels[k]; !labelled {
			keys = append(keys, k)
		}
	}
	return keys
}

var processFieldKeys = []string{"process_name", "process_path", "process_path_regex"}

// ConditionSummary describes a condition for display.
func ConditionSummary(node *Condition) string {
	if node.Kind == ConditionMatch && node.UIProcess != nil {
		for _, field := range processFieldKeys {
			if _, ok := node.Match[field]; !ok {
				continue
			}
			item := ProcessSelection{Source: node.UIProcess.Source, Type: processTypeOf(field), Label: node.UIProcess.Label}
			item.Value = node.UIProcess.AppPath
			if item.Value == "" {
				if v := Values(node.Match[field]); len(v) > 0 {
					item.Value = v[0]
				}
			}
			text := ProcessSelectionType(item) + " " + ProcessSelectionLabel(item)
			if node.Invert {
				return "排除（" + text + "）"
			}
			return text
		}
	}
	var text string
	if node.Kind == ConditionMatch {
		var parts []string
		for _, key := range matchKeys(node.Match) {
			if key == "package_apps" {
				continue
			}
			parts = append(parts, conditionLabel(key)+" "+matchValueDisplay(key, node.Match[key]))
		}
		text = strings.Join(parts, " · ")
	} else if len(node.Rules) > 0 {
		parts := make([]string, len(node.Rules))
		for i, rule := range node.Rules {
			parts[i] = ConditionSummary(rule)
		}
		sep := " 或 "
		if node.Mode == "and" {
			sep = " 且 "
		}
		text = "（" + strings.Join(parts, sep) + "）"
	} else {
		text = "全部连接"
	}
	if node.Invert {
		if node.Kind == ConditionMatch {
			return "排除（" + text + "）"
		}
		return "排除" + text
	}
	return text
}

func matchValueDisplay(key string, value any) string {
	var names map[string]string
	switch key {
	case "network_type":
		names = map[string]string{"wifi": "Wi-Fi", "cellular": "蜂窝网络", "ethernet": "有线网络", "other": "其他网络"}
	case "clash_mode":
		names = map[string]string{"Rule": "规则模式", "Global": "全局模式", "Direct": "直连模式"}
	}
	if b, ok := value.(bool); ok {
		if b {
			return "是"
		}
		return "否"
	}
	if m, ok := value.(map[string]any); ok {
		var parts []string
		for _, name := range sortedKeys(m) {
			parts = append(parts, name+": "+strings.Join(Values(m[name]), ", "))
		}
		return strings.Join(parts, "；")
	}
	var items []string
	for _, item := range Values(value) {
		switch {
		case names[item] != "":
			items = append(items, names[item])
		case key == "network" || key == "protocol":
			items = append(items, strings.ToUpper(item))
		case key == "ip_version":
			items = append(items, "IPv"+item)
		default:
			items = append(items, item)
		}
	}
	return strings.Join(items, ", ")
}

// RuleCondition is a rule's condition tree, converting a stored flat
// match when it has none.
func RuleCondition(match map[string]any, tree *Condition, logic string) *Condition {
	if tree != nil {
		return tree
	}
	return LegacyCondition(match, logic)
}

// RuleSummary describes a rule's conditions for a table cell; the cell
// already groups the expression, so the outer parentheses go.
func RuleSummary(match map[string]any, tree *Condition, logic string) string {
	root := RuleCondition(match, tree, logic)
	summary := ConditionSummary(root)
	if len(root.Rules) > 0 && !root.Invert {
		r := []rune(summary)
		if len(r) >= 2 {
			return string(r[1 : len(r)-1])
		}
		return ""
	}
	return summary
}

// Network is a parsed address or CIDR network: 4 or 16 bytes and a
// prefix length.
type Network struct {
	Bytes  []byte
	Prefix int
}

var (
	reNetLength = regexp.MustCompile(`^\d+$`)
	reNetIPv6   = regexp.MustCompile(`(?i)^[\da-f:.]+$`)
	reNetWord   = regexp.MustCompile(`(?i)^[\da-f]{1,4}$`)
	reNetOctet  = regexp.MustCompile(`^\d{1,3}$`)
)

// ParseNetwork parses an IP address or CIDR network; ok is false when it
// is invalid. IPv4-mapped IPv6 networks are unmapped, as sing-box does.
func ParseNetwork(input string) (Network, bool) {
	parts := strings.Split(strings.TrimSpace(input), "/")
	host := parts[0]
	if host == "" || len(parts) > 2 || len(parts) == 2 && !reNetLength.MatchString(parts[1]) {
		return Network{}, false
	}
	var bytes []byte
	if strings.Contains(host, ":") {
		if strings.Contains(host, "%") || !reNetIPv6.MatchString(host) {
			return Network{}, false
		}
		expanded := host
		if strings.Contains(expanded, ".") {
			at := strings.LastIndex(expanded, ":")
			v4, ok := ParseNetwork(expanded[at+1:])
			if !ok || len(v4.Bytes) != 4 {
				return Network{}, false
			}
			expanded = expanded[:at+1] + strconv.FormatInt(int64(v4.Bytes[0])<<8|int64(v4.Bytes[1]), 16) + ":" +
				strconv.FormatInt(int64(v4.Bytes[2])<<8|int64(v4.Bytes[3]), 16)
		}
		halves := strings.Split(expanded, "::")
		if len(halves) > 2 {
			return Network{}, false
		}
		var left, right []string
		if halves[0] != "" {
			left = strings.Split(halves[0], ":")
		}
		if len(halves) == 2 && halves[1] != "" {
			right = strings.Split(halves[1], ":")
		}
		for _, w := range append(append([]string{}, left...), right...) {
			if !reNetWord.MatchString(w) {
				return Network{}, false
			}
		}
		count := len(left) + len(right)
		if len(halves) == 1 && count != 8 || len(halves) == 2 && count >= 8 {
			return Network{}, false
		}
		words := left
		if len(halves) == 2 {
			words = append(append([]string{}, left...), slices.Repeat([]string{"0"}, 8-count)...)
			words = append(words, right...)
		}
		for _, w := range words {
			n, _ := strconv.ParseUint(w, 16, 16)
			bytes = append(bytes, byte(n>>8), byte(n&255))
		}
	} else {
		octets := strings.Split(host, ".")
		if len(octets) != 4 {
			return Network{}, false
		}
		for _, o := range octets {
			n, _ := strconv.Atoi(o)
			if !reNetOctet.MatchString(o) || n > 255 {
				return Network{}, false
			}
			bytes = append(bytes, byte(n))
		}
	}
	prefix := len(bytes) * 8
	if len(parts) == 2 {
		n, err := strconv.Atoi(parts[1])
		if err != nil || n > len(bytes)*8 {
			return Network{}, false
		}
		prefix = n
	}
	if len(bytes) == 16 && prefix >= 96 && slices.Equal(bytes[:12], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255}) {
		return Network{Bytes: bytes[12:], Prefix: prefix - 96}, true
	}
	return Network{Bytes: bytes, Prefix: prefix}, true
}

// ValidNetwork reports whether ParseNetwork accepts input.
func ValidNetwork(input string) bool {
	_, ok := ParseNetwork(input)
	return ok
}

// NetworksOverlap reports whether two networks share addresses.
func NetworksOverlap(a, b string) bool {
	left, ok1 := ParseNetwork(a)
	right, ok2 := ParseNetwork(b)
	if !ok1 || !ok2 || len(left.Bytes) != len(right.Bytes) {
		return false
	}
	bits := min(left.Prefix, right.Prefix)
	for bit := 0; bit < bits; bit++ {
		mask := byte(128 >> (bit % 8))
		if left.Bytes[bit>>3]&mask != right.Bytes[bit>>3]&mask {
			return false
		}
	}
	return true
}

// DevicesMax bounds the LAN devices of a document.
const DevicesMax = 512

// ValidateDevice returns why a LAN device is invalid among others (which
// may include it), "" when valid.
func ValidateDevice(device *RouteDevice, others []*RouteDevice) string {
	if device.ID == "" || IsFixedDevice(device.ID) || IsLocalScope(device.ID) {
		return "设备标识无效"
	}
	same := 0
	for _, o := range others {
		if o.ID == device.ID {
			same++
		}
	}
	if same > 1 {
		return "设备标识重复"
	}
	if strings.TrimSpace(device.Name) == "" {
		return "请填写设备名称"
	}
	if n := len(others) - same; n >= DevicesMax {
		return "设备数量已达上限"
	}
	if len(device.Addresses) == 0 || slices.ContainsFunc(device.Addresses, func(a string) bool { return !ValidNetwork(a) }) {
		return "请填写有效的来源 IP 或 CIDR 网段"
	}
	if device.Final == "" {
		return "请选择设备默认出口"
	}
	if problem := validateProfiles(device); problem != "" {
		return problem
	}
	for _, other := range others {
		if other.ID == device.ID {
			continue
		}
		for _, a := range device.Addresses {
			for _, b := range other.Addresses {
				if NetworksOverlap(a, b) {
					return "来源范围与「" + other.Name + "」重叠，请调整"
				}
			}
		}
	}
	return ""
}

var rePortRange = regexp.MustCompile(`^(\d+):(\d+)$`)

func isDigits(s string) bool { return reNetLength.MatchString(s) }

// ValidateCondition returns the first problem of a condition, "" when it
// is valid. allowEmpty accepts an empty, non-inverted root group.
func ValidateCondition(node *Condition, allowEmpty bool) string {
	if problem := validateConditionShape(node, allowEmpty); problem != "" {
		return problem
	}
	return ScriptInvertProblem(node)
}

// ScriptInvertProblem is the error for a script condition below an
// exclusion, "" when there is none. A failing, slow or missing script
// counts as no match, and an exclusion would turn that into a match.
func ScriptInvertProblem(node *Condition) string {
	if node != nil && scriptUnderInvert(node, false) {
		return "脚本条件不能与「排除」同用"
	}
	return ""
}

func scriptUnderInvert(node *Condition, inverted bool) bool {
	inverted = inverted || node.Invert
	if node.Kind == ConditionGroup {
		return slices.ContainsFunc(node.Rules, func(rule *Condition) bool { return scriptUnderInvert(rule, inverted) })
	}
	_, hasScript := node.Match["script"]
	return inverted && hasScript
}

func validateConditionShape(node *Condition, allowEmpty bool) string {
	if node.Kind == ConditionGroup {
		if len(node.Rules) == 0 {
			if allowEmpty && !node.Invert {
				return ""
			}
			return "请配置至少一项条件"
		}
		for _, rule := range node.Rules {
			if err := validateConditionShape(rule, false); err != "" {
				return err
			}
		}
		return ""
	}
	if len(node.Match) == 0 {
		return "请填写匹配内容"
	}
	for _, key := range matchKeys(node.Match) {
		if key == "package_apps" {
			continue
		}
		value := node.Match[key]
		if value == nil {
			return "请填写" + conditionLabel(key)
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) == "" {
			return "请填写" + conditionLabel(key)
		}
		if list, ok := jsv.Array(value); ok && len(Values(list)) == 0 {
			return "请填写" + conditionLabel(key)
		}
		switch key {
		case "port", "source_port":
			if slices.ContainsFunc(Values(value), func(v string) bool {
				n := jsv.StringToNumber(v)
				return !isDigits(v) || n < 1 || n > 65535
			}) {
				return "端口应为 1–65535 的整数"
			}
		case "port_range", "source_port_range":
			if slices.ContainsFunc(Values(value), func(v string) bool {
				m := rePortRange.FindStringSubmatch(v)
				if m == nil {
					return true
				}
				a, b := jsv.StringToNumber(m[1]), jsv.StringToNumber(m[2])
				return a < 1 || b > 65535 || a > b
			}) {
				return "端口范围应为起始:结束，范围为 1–65535"
			}
		case "ip_cidr", "source_ip_cidr", "default_interface_address":
			if slices.ContainsFunc(Values(value), func(v string) bool { return !ValidNetwork(v) }) {
				return "请填写有效的 IP 地址或 CIDR 网段"
			}
		case "user_id":
			if slices.ContainsFunc(Values(value), func(v string) bool { return !isDigits(v) || jsv.StringToNumber(v) > 2147483647 }) {
				return "用户 ID 应为 0–2147483647 的整数"
			}
		case "interface_address", "network_interface_address":
			if err := validateInterfaceAddresses(key, value); err != "" {
				return err
			}
		}
	}
	return ""
}

func validateInterfaceAddresses(key string, value any) string {
	v := value
	if s, ok := value.(string); ok {
		if json.Unmarshal([]byte(s), &v) != nil {
			return "请填写有效 JSON 对象"
		}
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return "请添加接口及其地址"
	}
	for _, name := range sortedKeys(m) {
		if strings.TrimSpace(name) == "" {
			return "请填写接口名称或选择接口类型"
		}
		if key == "network_interface_address" && !slices.Contains([]string{"wifi", "cellular", "ethernet", "other"}, name) {
			return "请选择有效的网络接口类型"
		}
		addresses := Values(m[name])
		if len(addresses) == 0 || slices.ContainsFunc(addresses, func(a string) bool { return !ValidNetwork(a) }) {
			return "请为每个接口填写有效的 IP 地址或 CIDR 网段"
		}
	}
	return ""
}

// MapCondition returns a copy of a tree with every leaf's match converted.
func MapCondition(node *Condition, convert func(map[string]any) map[string]any) *Condition {
	next := *node
	next.Rules = make([]*Condition, len(node.Rules))
	for i, rule := range node.Rules {
		if rule.Kind == ConditionGroup {
			next.Rules[i] = MapCondition(rule, convert)
		} else {
			leaf := *rule
			leaf.Match = convert(rule.Match)
			next.Rules[i] = &leaf
		}
	}
	return &next
}

// PolicyRule is a route or DNS rule, for the helpers both policies share.
type PolicyRule interface {
	*RouteRule | *DNSRule
	PolicyID() string
	PolicyGroup() string
	PolicyOwner() string
}

// PolicyID is the rule's id.
func (r *RouteRule) PolicyID() string { return r.ID }

// PolicyGroup is the rule's group id.
func (r *RouteRule) PolicyGroup() string { return r.GroupID }

// PolicyOwner is the rule's scope.
func (r *RouteRule) PolicyOwner() string { return DeviceOwner(r.DeviceID) }

// PolicyID is the rule's id.
func (r *DNSRule) PolicyID() string { return r.ID }

// PolicyGroup is the rule's group id.
func (r *DNSRule) PolicyGroup() string { return r.GroupID }

// PolicyOwner is the rule's scope.
func (r *DNSRule) PolicyOwner() string { return DeviceOwner(r.DeviceID) }

// withGroup returns a copy of rule in group.
func withGroup[T PolicyRule](rule T, group string) T {
	switch r := any(rule).(type) {
	case *RouteRule:
		c := *r
		c.GroupID = group
		return any(&c).(T)
	case *DNSRule:
		c := *r
		c.GroupID = group
		return any(&c).(T)
	}
	return rule
}

// Insertion positions of MovePolicyRule.
const (
	PositionBefore = "before"
	PositionAfter  = "after"
)

// MovePolicyRule moves rule id into groupID, before or after targetID
// (position "before"/"after"); without a position the legacy index-based
// placement applies, and without a target it goes last. The rules are
// copies; the input is returned unchanged when nothing moves.
func MovePolicyRule[T PolicyRule](rules []T, id, groupID, targetID, position string) []T {
	next := make([]T, len(rules))
	copy(next, rules)
	from := slices.IndexFunc(next, func(r T) bool { return r.PolicyID() == id })
	target := slices.IndexFunc(next, func(r T) bool { return targetID != "" && r.PolicyID() == targetID })
	if from < 0 || id == targetID {
		return rules
	}
	originalGroup := next[from].PolicyGroup()
	rule := withGroup(next[from], groupID)
	next = slices.Delete(next, from, from+1)
	targetIndex := func() int {
		return slices.IndexFunc(next, func(r T) bool { return r.PolicyID() == targetID })
	}
	var at int
	switch {
	case target < 0:
		at = len(next)
	case position != "":
		at = targetIndex()
		if position == PositionAfter {
			at++
		}
	case originalGroup == groupID:
		at = target
	default:
		at = targetIndex()
	}
	return slices.Insert(next, at, rule)
}

// cloneCondition deep-copies a tree.
func cloneCondition(node *Condition) *Condition {
	if node == nil {
		return nil
	}
	next := *node
	next.Match = jsv.CloneMap(node.Match)
	if node.UIProcess != nil {
		p := *node.UIProcess
		next.UIProcess = &p
	}
	if node.Rules != nil {
		next.Rules = make([]*Condition, len(node.Rules))
		for i, r := range node.Rules {
			next.Rules[i] = cloneCondition(r)
		}
	}
	return &next
}

// CloneCondition deep-copies a condition tree.
func CloneCondition(node *Condition) *Condition { return cloneCondition(node) }

// SetConditionField edits field key (and its range field) of a root
// group, keeping legacy compound predicates together. An empty field
// clears it; field "rule_set" for another key records the owning key in
// uiField.
func SetConditionField(group *Condition, key, rangeKey, field string, value any, invert bool) *Condition {
	next := cloneCondition(group)
	index := slices.IndexFunc(next.Rules, func(node *Condition) bool {
		if node.Kind != ConditionMatch {
			return false
		}
		if node.UIField == key {
			return true
		}
		if node.UIField != "" {
			return false
		}
		_, hasKey := node.Match[key]
		_, hasRange := node.Match[rangeKey]
		return hasKey || rangeKey != "" && hasRange
	})
	var previous *Condition
	remaining := map[string]any{}
	if index >= 0 {
		previous = next.Rules[index]
		for k, v := range previous.Match {
			remaining[k] = v
		}
	}
	delete(remaining, key)
	if rangeKey != "" {
		delete(remaining, rangeKey)
	}
	if previous != nil && previous.UIField == key {
		delete(remaining, "rule_set")
	}
	if key == "package_name" {
		delete(remaining, "package_apps")
	}
	if previous != nil && len(remaining) > 0 && (field == "" || field != "rule_set" && invert == previous.Invert) {
		leaf := *previous
		leaf.Match = remaining
		if field != "" {
			leaf.Match[field] = value
		}
		next.Rules[index] = &leaf
		return next
	}
	var replacement []*Condition
	if previous != nil && len(remaining) > 0 {
		leaf := *previous
		leaf.Match = remaining
		replacement = append(replacement, &leaf)
	}
	if field != "" {
		leaf := &Condition{Kind: ConditionMatch, Match: map[string]any{field: value}, Invert: invert}
		if field == "rule_set" && key != "rule_set" {
			leaf.UIField = key
		}
		replacement = append(replacement, leaf)
	}
	if index >= 0 {
		next.Rules = slices.Replace(next.Rules, index, index+1, replacement...)
	} else {
		next.Rules = append(next.Rules, replacement...)
	}
	return next
}

// SetPortConditionField edits a port field; a port list and its ranges
// form alternatives inside one core predicate.
func SetPortConditionField(group *Condition, key, field string, value any, invert bool) *Condition {
	next := SetConditionField(group, key, "", field, value, invert)
	if field == "" {
		return next
	}
	pair := []string{"port", "port_range"}
	if strings.HasPrefix(key, "source_") {
		pair = []string{"source_port", "source_port_range"}
	}
	onlyPair := func(match map[string]any) bool {
		for k := range match {
			if !slices.Contains(pair, k) {
				return false
			}
		}
		return true
	}
	index := slices.IndexFunc(next.Rules, func(node *Condition) bool {
		_, ok := node.Match[key]
		return node.Kind == ConditionMatch && ok
	})
	if index < 0 {
		return next
	}
	current := next.Rules[index]
	if !onlyPair(current.Match) || current.Invert && group.Mode == "or" {
		return next
	}
	other := -1
	for i, node := range next.Rules {
		_, has := node.Match[key]
		if i != index && node.Kind == ConditionMatch && !has && node.Invert == current.Invert && onlyPair(node.Match) {
			other = i
			break
		}
	}
	if other >= 0 {
		merged := *current
		merged.Match = map[string]any{}
		for k, v := range next.Rules[other].Match {
			merged.Match[k] = v
		}
		for k, v := range current.Match {
			merged.Match[k] = v
		}
		next.Rules[index] = &merged
		next.Rules = slices.Delete(next.Rules, other, other+1)
	}
	return next
}

// SetConditionMode changes a root group's operator; an explicit change
// expands legacy compound predicates into one leaf per field.
func SetConditionMode(group *Condition, mode string) *Condition {
	if group.Mode == mode {
		return group
	}
	next := cloneCondition(group)
	next.Mode = mode
	var rules []*Condition
	for _, node := range next.Rules {
		if node.Kind == ConditionGroup || node.UIField != "" {
			rules = append(rules, node)
			continue
		}
		var keys []string
		for _, k := range matchKeys(node.Match) {
			if k == "package_apps" || k == "rule_set_ip_cidr_match_source" {
				continue
			}
			if _, ok := node.Match["port"]; k == "port_range" && ok {
				continue
			}
			if _, ok := node.Match["source_port"]; k == "source_port_range" && ok {
				continue
			}
			keys = append(keys, k)
		}
		if len(keys) < 2 {
			rules = append(rules, node)
			continue
		}
		var leaves []*Condition
		for _, k := range keys {
			match := map[string]any{k: node.Match[k]}
			if k == "port" && jsv.Truthy(node.Match["port_range"]) {
				match["port_range"] = node.Match["port_range"]
			}
			if k == "source_port" && jsv.Truthy(node.Match["source_port_range"]) {
				match["source_port_range"] = node.Match["source_port_range"]
			}
			if k == "package_name" && jsv.Truthy(node.Match["package_apps"]) {
				match["package_apps"] = node.Match["package_apps"]
			}
			if v, ok := node.Match["rule_set_ip_cidr_match_source"]; k == "rule_set" && ok {
				match["rule_set_ip_cidr_match_source"] = v
			}
			leaves = append(leaves, &Condition{Kind: ConditionMatch, Match: match})
		}
		if node.Invert {
			rules = append(rules, &Condition{Kind: ConditionGroup, Mode: mode, Invert: true, Rules: leaves})
		} else {
			rules = append(rules, leaves...)
		}
	}
	if rules == nil {
		rules = []*Condition{}
	}
	next.Rules = rules
	return next
}

// LocalSourceAddresses are the source networks of the local scope: the
// loopback networks, the host's detected addresses, TUN interface
// addresses (host prefixes) and the configured extra ranges.
func LocalSourceAddresses(doc *Doc, detected []string) []string {
	out := []string{"127.0.0.0/8", "::1/128"}
	out = append(out, detected...)
	for _, in := range doc.Inbounds {
		if !in.Enabled || in.Type != "tun" {
			continue
		}
		addresses := Values(in.Fields["address"])
		if !slices.ContainsFunc(addresses, func(a string) bool { return !strings.Contains(a, ":") }) {
			addresses = append(addresses, "172.18.0.1")
		}
		if in.Fields["ipv6"] == true && !slices.ContainsFunc(addresses, func(a string) bool { return strings.Contains(a, ":") }) {
			if doc.Platform == PlatformAndroid {
				addresses = append(addresses, "fd00::1")
			} else {
				addresses = append(addresses, "fdfe:dcba:9876::1")
			}
		}
		for _, a := range addresses {
			out = append(out, strings.Split(a, "/")[0])
		}
	}
	out = append(out, doc.Route.LocalAddresses...)
	return unique(out)
}

// RouteSourceScope is the predicate that limits a scope's rules to its
// sources: the generated local source rule set, or a device's networks.
// Pre and other rely on rule order and have none.
func RouteSourceScope(doc *Doc, owner, localRuleSetTag string) map[string]any {
	switch owner {
	case PreDeviceID, OtherDeviceID:
		return map[string]any{}
	}
	if IsLocalScope(owner) {
		return map[string]any{"rule_set": []any{localRuleSetTag}}
	}
	addresses := []any{}
	if d, _, ok := ScopeDevice(doc, owner); ok {
		addresses = anyStrings(d.Addresses)
	}
	return map[string]any{"source_ip_cidr": addresses}
}

// String formats a network as CIDR, for debugging.
func (n Network) String() string { return fmt.Sprintf("%v/%d", n.Bytes, n.Prefix) }
