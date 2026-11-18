package model

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Body rewrite edit actions.
const (
	BodyActionReplaceRegex = "replace-regex"
	BodyActionJSONSet      = "json-set"
	BodyActionJSONDelete   = "json-delete"
	BodyActionJSONAppend   = "json-append"

	// URLActionHeader rewrites the request target internally (the client
	// sees no change); Target is the new URL.
	URLActionHeader = "header"
)

// Body rewrite limits; they mirror the core's compile step.
const (
	MaxBodyEdits           = 32
	MaxBodyContentTypes    = 32
	MaxBodyContentTypeLen  = 256
	MinBodySize            = 1 << 10
	MaxBodySizeLimit       = 32 << 20
	DefaultBodySize        = 2 << 20
	MaxRewriteTargetLength = 8 << 10
)

// BodyRewrite edits the buffered request or response body of matching URLs.
// ContentTypes are case-insensitive substrings; empty selects the default
// set of the edits.
type BodyRewrite struct {
	ID           string      `json:"id"`
	Name         string      `json:"name,omitempty"`
	Disabled     bool        `json:"disabled,omitempty"`
	Pattern      string      `json:"pattern"`
	Scope        string      `json:"scope"`
	ContentTypes []string    `json:"contentTypes,omitempty"`
	Edits        []*BodyEdit `json:"edits"`
}

// BodyEdit is one body edit, applied to the result of the previous one.
// replace-regex rewrites the decoded body bytes with Regex and the template
// Value ($1, ${name}); the json actions address nodes with a JSONPath subset
// and take a JSON text as Value (json-set, json-append).
type BodyEdit struct {
	Action string `json:"action"`
	Regex  string `json:"regex,omitempty"`
	Path   string `json:"path,omitempty"`
	Value  string `json:"value,omitempty"`
}

// ValidBodyAction reports whether action is a body edit action.
func ValidBodyAction(action string) bool {
	switch action {
	case BodyActionReplaceRegex, BodyActionJSONSet, BodyActionJSONDelete, BodyActionJSONAppend:
		return true
	}
	return false
}

// IsJSONBodyAction reports whether action addresses a JSON path.
func IsJSONBodyAction(action string) bool {
	return action == BodyActionJSONSet || action == BodyActionJSONDelete || action == BodyActionJSONAppend
}

// MaxBodySizeIssue explains why n (bytes) is not a usable buffer limit;
// zero selects the default.
func MaxBodySizeIssue(n int) string {
	if n == 0 {
		return ""
	}
	if n < MinBodySize || n > MaxBodySizeLimit || n%1024 != 0 {
		return "缓冲上限须在 1 KiB 到 32 MiB 之间"
	}
	return ""
}

// MaxBodySizeOrDefault returns the effective buffer limit in bytes.
func (r *HTTPRewrites) MaxBodySizeOrDefault() int {
	if r == nil || r.MaxBodySize == 0 {
		return DefaultBodySize
	}
	return r.MaxBodySize
}

// RegexIssue explains why expr is not a usable RE2 expression.
func RegexIssue(expr string) string {
	if expr == "" {
		return "请填写正则表达式"
	}
	if len(expr) > MaxPattern {
		return "正则表达式过长"
	}
	if _, err := regexp.Compile(expr); err != nil {
		return "正则表达式无效"
	}
	return ""
}

// JSONValueIssue explains why text is not a JSON value.
func JSONValueIssue(text string) string {
	if strings.TrimSpace(text) == "" {
		return "请填写 JSON 值"
	}
	if len(text) > MaxHeaderValue {
		return "JSON 值过长"
	}
	if !json.Valid([]byte(text)) {
		return "JSON 值无效"
	}
	depth, inString := 0, false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case inString:
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '[' || c == '{':
			if depth++; depth > maxJSONDepth {
				return "JSON 值嵌套过深"
			}
		case c == ']' || c == '}':
			depth--
		}
	}
	return ""
}

// Issue returns the first problem of the edit; empty when it is valid.
func (e *BodyEdit) Issue() string {
	if e == nil || !ValidBodyAction(e.Action) {
		return "未知的编辑动作"
	}
	switch e.Action {
	case BodyActionReplaceRegex:
		if issue := RegexIssue(e.Regex); issue != "" {
			return issue
		}
		if len(e.Value) > MaxHeaderValue {
			return "替换内容过长"
		}
		return ""
	}
	if issue := JSONPathIssue(e.Path); issue != "" {
		return issue
	}
	if e.Action != BodyActionJSONDelete {
		return JSONValueIssue(e.Value)
	}
	return ""
}

// Issue returns the first problem of the rule; empty when it is valid.
func (r *BodyRewrite) Issue() string {
	if issue := PatternIssue(r.Pattern); issue != "" {
		return issue
	}
	if r.Scope != HeaderScopeRequest && r.Scope != HeaderScopeResponse {
		return "未知的作用范围"
	}
	if len(r.ContentTypes) > MaxBodyContentTypes {
		return "内容类型过多"
	}
	for _, ct := range r.ContentTypes {
		if ct = strings.TrimSpace(ct); ct == "" || len(ct) > MaxBodyContentTypeLen {
			return "内容类型无效"
		}
		if HeaderValueIssue(ct) != "" {
			return "内容类型无效"
		}
	}
	if len(r.Edits) == 0 {
		return "请添加至少一项编辑"
	}
	if len(r.Edits) > MaxBodyEdits {
		return "编辑项过多"
	}
	for _, e := range r.Edits {
		if issue := e.Issue(); issue != "" {
			return issue
		}
	}
	return ""
}

// HeaderTargetIssue explains why target is not a valid internal-rewrite URL:
// http or https, a host, no userinfo, no control characters, at most 8 KiB.
// A target with a capture reference ($1) is checked on its fixed parts.
func HeaderTargetIssue(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "请填写目标地址"
	}
	if len(target) > MaxRewriteTargetLength {
		return "目标地址过长"
	}
	for i := 0; i < len(target); i++ {
		if c := target[i]; c <= 0x20 || c == 0x7f {
			return "目标地址含有空白或控制字符"
		}
	}
	if strings.Contains(target, "$") {
		// The core checks the expanded URL at run time.
		return ""
	}
	lower := strings.ToLower(target)
	var rest string
	switch {
	case strings.HasPrefix(lower, "https://"):
		rest = target[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		rest = target[len("http://"):]
	default:
		return "目标地址须为 http 或 https 地址"
	}
	authority := rest
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if authority == "" {
		return "目标地址缺少主机"
	}
	if strings.Contains(authority, "@") {
		return "目标地址不能含有用户信息"
	}
	if !strings.Contains(target, "$") {
		u, err := url.Parse(target)
		if err != nil {
			return "目标地址无效"
		}
		if u.User != nil {
			return "目标地址不能含有用户信息"
		}
		host := u.Hostname()
		if host == "" || strings.ContainsAny(host, "%\\/@") {
			return "目标地址缺少主机"
		}
		if text := u.Port(); text != "" {
			if n, err := strconv.Atoi(text); err != nil || n < 1 || n > 65535 {
				return "目标地址的端口无效"
			}
		} else if strings.HasSuffix(u.Host, ":") {
			return "目标地址的端口无效"
		}
	}
	return ""
}

const (
	maxPathSegments = 64
	maxPathUnion    = 64
	maxJSONDepth    = 128
	pathForbidden   = ".[]'\"*,()?@<>=! \t\r\n:"
)

// JSONPathIssue validates the JSONPath subset of the core (a port of its
// parser): $, .name, .*, ['name'], [n], [*], ..name, ..['name'] and unions
// of names or indexes. Filters and slices are not supported.
func JSONPathIssue(path string) string {
	if path == "" {
		return "请填写路径"
	}
	if len(path) > MaxPattern {
		return "路径过长"
	}
	if path[0] != '$' {
		return "路径须以 $ 开头"
	}
	segments := 0
	i := 1
	for i < len(path) {
		if segments >= maxPathSegments {
			return "路径层级过多"
		}
		segments++
		switch path[i] {
		case '.':
			if strings.HasPrefix(path[i:], "..") {
				i += 2
				if i < len(path) && path[i] == '[' {
					end, kind, issue := jsonPathBracket(path, i)
					if issue != "" {
						return issue
					}
					if kind != 'n' {
						return "递归下降只能跟一个名称"
					}
					i = end
					continue
				}
				end, issue := jsonPathName(path, i)
				if issue != "" {
					return issue
				}
				i = end
				continue
			}
			i++
			if i < len(path) && path[i] == '*' {
				i++
				continue
			}
			end, issue := jsonPathName(path, i)
			if issue != "" {
				return issue
			}
			i = end
		case '[':
			end, _, issue := jsonPathBracket(path, i)
			if issue != "" {
				return issue
			}
			i = end
		default:
			return "路径语法无效"
		}
	}
	return ""
}

func jsonPathName(path string, i int) (int, string) {
	start := i
	for i < len(path) && path[i] != '.' && path[i] != '[' {
		if strings.IndexByte(pathForbidden, path[i]) >= 0 || path[i] < ' ' || path[i] == 0x7f {
			return 0, "路径含有不支持的字符"
		}
		i++
	}
	if i == start {
		return 0, "路径中的名称不能为空"
	}
	return i, ""
}

// jsonPathBracket parses [..] at path[i] == '[' and returns the index after
// it and the kind: 'n' a single name, 'm' anything else.
func jsonPathBracket(path string, i int) (int, byte, string) {
	i++
	skip := func() {
		for i < len(path) && (path[i] == ' ' || path[i] == '\t') {
			i++
		}
	}
	names, indexes, count := 0, 0, 0
	wild := false
	for {
		skip()
		if i >= len(path) {
			return 0, 0, "路径缺少 ]"
		}
		count++
		if count > maxPathUnion {
			return 0, 0, "并集项过多"
		}
		switch c := path[i]; {
		case c == '*':
			wild = true
			i++
		case c == '\'' || c == '"':
			i++
			closed := false
			for i < len(path) {
				if path[i] == '\\' {
					i += 2
					continue
				}
				if path[i] == c {
					closed = true
					i++
					break
				}
				i++
			}
			if !closed {
				return 0, 0, "路径中的引号未闭合"
			}
			names++
		case c == '-' || c >= '0' && c <= '9':
			start := i
			i++
			for i < len(path) && path[i] >= '0' && path[i] <= '9' {
				i++
			}
			if _, err := strconv.ParseInt(path[start:i], 10, 64); err != nil {
				return 0, 0, "路径下标无效"
			}
			indexes++
		default:
			return 0, 0, "路径语法无效"
		}
		skip()
		if i >= len(path) {
			return 0, 0, "路径缺少 ]"
		}
		switch path[i] {
		case ':':
			return 0, 0, "路径语法无效"
		case ',':
			i++
			continue
		case ']':
			i++
		default:
			return 0, 0, "路径语法无效"
		}
		break
	}
	kinds := 0
	for _, present := range []bool{wild, names > 0, indexes > 0} {
		if present {
			kinds++
		}
	}
	if kinds != 1 || wild && count != 1 {
		return 0, 0, "并集不能混用名称与下标"
	}
	if names == 1 {
		return i, 'n', ""
	}
	return i, 'm', ""
}
