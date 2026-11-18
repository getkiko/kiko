package model

import (
	"errors"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
)

// This file holds the parsing of what users type into port, address and
// domain condition inputs.

// PortEntry is a parsed port or port range.
type PortEntry struct {
	Field string `json:"field"` // port or port_range
	Value string `json:"value"`
}

var (
	rePortSplit = regexp.MustCompile(`[,，;；\s]+`)
	rePortSpan  = regexp.MustCompile(`^(\d+)\s*[-–:]\s*(\d+)$`)
)

// ParsePortInput parses ports and ranges such as "80, 443, 8000-9000";
// ok is false when any part is invalid.
func ParsePortInput(input string) ([]PortEntry, bool) {
	var parts []string
	for _, p := range rePortSplit.Split(strings.TrimSpace(input), -1) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	var result []PortEntry
	for _, part := range parts {
		if m := rePortSpan.FindStringSubmatch(part); m != nil {
			a, _ := strconv.ParseFloat(m[1], 64)
			b, _ := strconv.ParseFloat(m[2], 64)
			if a < 1 || b > 65535 || a > b {
				return nil, false
			}
			if a == b {
				result = append(result, PortEntry{"port", strconv.Itoa(int(a))})
			} else {
				result = append(result, PortEntry{"port_range", strconv.Itoa(int(a)) + ":" + strconv.Itoa(int(b))})
			}
			continue
		}
		n, err := strconv.ParseFloat(part, 64)
		if !isDigits(part) || err != nil || n < 1 || n > 65535 {
			return nil, false
		}
		result = append(result, PortEntry{"port", strconv.Itoa(int(n))})
	}
	// Keep the last of duplicates at the first one's position, as a Map
	// keyed by field and value does.
	var out []PortEntry
	for _, e := range result {
		if i := slices.IndexFunc(out, func(o PortEntry) bool { return o == e }); i >= 0 {
			out[i] = e
		} else {
			out = append(out, e)
		}
	}
	return out, true
}

// CanonicalNetwork returns input as a network with prefix bits (its own
// prefix when prefix < 0), host bits cleared; ok is false when invalid.
func CanonicalNetwork(input string, prefix int) (string, bool) {
	parsed, ok := ParseNetwork(input)
	if !ok {
		return "", false
	}
	bits := prefix
	if prefix < 0 {
		bits = parsed.Prefix
	}
	if bits > len(parsed.Bytes)*8 {
		return "", false
	}
	bytes := make([]byte, len(parsed.Bytes))
	for i, b := range parsed.Bytes {
		switch {
		case bits >= i*8+8:
			bytes[i] = b
		case bits <= i*8:
			bytes[i] = 0
		default:
			bytes[i] = b & byte(255<<(8-(bits-i*8)))
		}
	}
	var text string
	if len(bytes) == 4 {
		text = strconv.Itoa(int(bytes[0])) + "." + strconv.Itoa(int(bytes[1])) + "." + strconv.Itoa(int(bytes[2])) + "." + strconv.Itoa(int(bytes[3]))
	} else {
		text = formatIPv6(bytes)
	}
	return text + "/" + strconv.Itoa(bits), true
}

// AddressSuggestion is an address the IP input offers.
type AddressSuggestion struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// AddressSuggestions offers the networks around a typed address: the
// address itself and its common prefixes, or the typed network.
func AddressSuggestions(input string) []AddressSuggestion {
	parsed, ok := ParseNetwork(strings.TrimSpace(input))
	if !ok {
		return []AddressSuggestion{}
	}
	if strings.Contains(input, "/") {
		value, _ := CanonicalNetwork(input, -1)
		return []AddressSuggestion{{ID: value, Label: value, Detail: "/" + strconv.Itoa(parsed.Prefix) + " 网段"}}
	}
	max := len(parsed.Bytes) * 8
	prefixes := []int{128, 64, 48, 32}
	if max == 32 {
		prefixes = []int{32, 24, 16, 8}
	}
	trimmed := strings.TrimSpace(input)
	out := []AddressSuggestion{{ID: trimmed, Label: trimmed, Detail: "单个地址 · /" + strconv.Itoa(max)}}
	for _, prefix := range prefixes {
		value, _ := CanonicalNetwork(input, prefix)
		detail := "/" + strconv.Itoa(prefix) + " 网段"
		if prefix == max {
			detail = "主机地址"
		}
		out = append(out, AddressSuggestion{ID: value, Label: value, Detail: detail})
	}
	return out
}

// ---- domains ----

// DomainFields are the domain condition fields.
var DomainFields = []string{"domain_suffix", "domain", "domain_regex", "domain_keyword"}

// DomainEntry is a parsed domain condition value.
type DomainEntry struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// DomainKindLabel names a domain field.
func DomainKindLabel(field string) string {
	switch field {
	case "domain_suffix":
		return "后缀"
	case "domain":
		return "全匹配"
	case "domain_regex":
		return "正则"
	case "domain_keyword":
		return "关键词"
	}
	return "域名"
}

var (
	reDomainForbidden = regexp.MustCompile(`[\s/:?#@\\%]`)
	reDomainLabel     = regexp.MustCompile(`(?i)^[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?$`)
	reRegexPrefix     = regexp.MustCompile(`(?i)^(?:regexp|regex):`)
	reWildcardPrefix  = regexp.MustCompile(`^(?:\*\.|\.)`)
	reRegexChars      = regexp.MustCompile(`[\\^$|()[\]{}?+*]`)
	reRegexFlags      = regexp.MustCompile(`^[ims]*$`)
)

// NormalizeDomainName returns the canonical lower-case IDNA host name the
// router sees, "" when input is not a domain.
func NormalizeDomainName(input string) string {
	value := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if value == "" || reDomainForbidden.MatchString(value) {
		return ""
	}
	// A fixed TLD keeps numeric labels from being read as IPv4 shorthand.
	host, ok := whatwgHostname(value + ".invalid")
	if !ok || !strings.HasSuffix(host, ".invalid") {
		return ""
	}
	host = strings.TrimSuffix(host, ".invalid")
	if len(host) > 253 {
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if !reDomainLabel.MatchString(label) {
			return ""
		}
	}
	return host
}

// IsDomainRegex reports whether input reads as a regular expression.
func IsDomainRegex(input string) bool {
	value := strings.TrimSpace(input)
	if reRegexPrefix.MatchString(value) || strings.HasPrefix(value, "/") {
		return true
	}
	if reWildcardPrefix.MatchString(value) && NormalizeDomainName(reWildcardPrefix.ReplaceAllString(value, "")) != "" {
		return false
	}
	return reRegexChars.MatchString(value)
}

// DomainRegexSource returns the RE2 source of a regex input: a regex:
// prefix is dropped and /source/flags becomes (?flags)source.
func DomainRegexSource(input string) (string, error) {
	value := strings.TrimSpace(reRegexPrefix.ReplaceAllString(strings.TrimSpace(input), ""))
	if !strings.HasPrefix(value, "/") {
		return value, nil
	}
	end := strings.LastIndex(value, "/")
	if end <= 0 {
		return value, nil
	}
	flags := value[end+1:]
	if !reRegexFlags.MatchString(flags) {
		return "", errors.New("正则修饰符支持 i、m、s")
	}
	prefix := ""
	if flags != "" {
		var seen []rune
		for _, r := range flags {
			if !slices.Contains(seen, r) {
				seen = append(seen, r)
			}
		}
		prefix = "(?" + string(seen) + ")"
	}
	return prefix + value[1:end], nil
}

// CompileDomainRegex compiles a regex input as the core's RE2; error is
// the message to show.
func CompileDomainRegex(input string) (pattern *regexp.Regexp, source, message string) {
	source = strings.TrimSpace(input)
	s, err := DomainRegexSource(input)
	if err != nil {
		return nil, source, "正则表达式有误：" + err.Error()
	}
	source = s
	if source == "" {
		return nil, source, "请输入正则表达式"
	}
	pattern, err = regexp.Compile(source)
	if err != nil {
		description := err.Error()
		var syntaxErr *syntax.Error
		if errors.As(err, &syntaxErr) {
			description = syntaxErr.Code.String()
		}
		return nil, source, "正则表达式有误：" + description
	}
	return pattern, source, ""
}

// ParseDomainEntry parses a domain input for field ("" detects full match
// or regex); message is the error to show.
func ParseDomainEntry(input, field string) (DomainEntry, string) {
	typ := field
	if typ == "" {
		typ = "domain"
		if IsDomainRegex(input) {
			typ = "domain_regex"
		}
	}
	if typ == "domain_regex" {
		_, source, message := CompileDomainRegex(input)
		if message != "" {
			return DomainEntry{}, message
		}
		return DomainEntry{Field: typ, Value: source}, ""
	}
	raw := strings.TrimSpace(input)
	if typ == "domain_keyword" {
		if raw == "" {
			return DomainEntry{}, "请输入域名关键词"
		}
		return DomainEntry{Field: typ, Value: raw}, ""
	}
	host := NormalizeDomainName(reWildcardPrefix.ReplaceAllString(raw, ""))
	if host == "" {
		return DomainEntry{}, "请输入域名，例如 example.com"
	}
	return DomainEntry{Field: typ, Value: host}, ""
}
