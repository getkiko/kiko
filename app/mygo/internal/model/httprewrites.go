package model

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

// HTTPRewrites holds the rewrite rules of the document. Rules apply to
// decrypted HTTPS and to plain HTTP when HTTPS decryption is on. Within a
// list the first matching rule wins (URL and local mapping) or all matching
// rules apply in order (headers).
type HTTPRewrites struct {
	URL      []*URLRewrite    `json:"url,omitempty"`
	Headers  []*HeaderRewrite `json:"headers,omitempty"`
	MapLocal []*MapLocal      `json:"mapLocal,omitempty"`
	// Body edits buffered message bodies; MaxBodySize (bytes, 0 = 2 MiB)
	// bounds the buffering.
	Body        []*BodyRewrite `json:"body,omitempty"`
	MaxBodySize int            `json:"maxBodySize,omitempty"`
}

// Empty reports whether no rule is defined.
func (r *HTTPRewrites) Empty() bool {
	return r == nil || len(r.URL) == 0 && len(r.Headers) == 0 && len(r.MapLocal) == 0 && len(r.Body) == 0
}

// URL rewrite actions. The reject family answers locally; 302 and 307
// redirect to Target, in which $1..$9 refer to the pattern's capture groups.
const (
	URLActionReject      = "reject"
	URLActionReject200   = "reject-200"
	URLActionRejectImg   = "reject-img"
	URLActionRejectDict  = "reject-dict"
	URLActionRejectArray = "reject-array"
	URLActionTinyGIF     = "reject-tinygif"
	URLActionFound       = "302"
	URLActionTemporary   = "307"
)

// URLActionHeader (see httprewrites_body.go) rewrites the target internally.

// URLRewrite is one URL rewrite rule.
type URLRewrite struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Pattern is an RE2 expression matched against the full request URL
	// (scheme://authority/path?query).
	Pattern string `json:"pattern"`
	Action  string `json:"action"`
	// Target is the redirect location of 302 and 307.
	Target string `json:"target,omitempty"`
}

// Header rewrite scopes and actions.
const (
	HeaderScopeRequest  = "request"
	HeaderScopeResponse = "response"

	HeaderActionAdd          = "add"
	HeaderActionDelete       = "delete"
	HeaderActionReplace      = "replace"
	HeaderActionReplaceRegex = "replace-regex"
)

// HeaderRewrite changes the request or response headers of matching URLs.
type HeaderRewrite struct {
	ID       string        `json:"id"`
	Name     string        `json:"name,omitempty"`
	Disabled bool          `json:"disabled,omitempty"`
	Pattern  string        `json:"pattern"`
	Scope    string        `json:"scope"`
	Ops      []*HeaderEdit `json:"ops"`
}

// HeaderEdit is one header operation. add appends a field; delete removes
// every field of the name; replace sets the value (adds the field when
// absent); replace-regex rewrites the value of present fields with Regex and
// the replacement Value ($1 refers to a capture group).
type HeaderEdit struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Regex  string `json:"regex,omitempty"`
}

// MapLocal answers matching requests from the local rule without contacting
// the server. Body is text; BodyBase64 holds binary content and wins when set.
type MapLocal struct {
	ID         string        `json:"id"`
	Name       string        `json:"name,omitempty"`
	Disabled   bool          `json:"disabled,omitempty"`
	Pattern    string        `json:"pattern"`
	Status     int           `json:"status"`
	Headers    []HeaderField `json:"headers,omitempty"`
	Body       string        `json:"body,omitempty"`
	BodyBase64 string        `json:"bodyBase64,omitempty"`
}

// HeaderField is one header field.
type HeaderField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Limits of the stored rules. The bodies travel inline in the core config.
const (
	MaxMapLocalBody      = 2 << 20
	MaxMapLocalBodyTotal = 4 << 20
	// MaxPattern and MaxHeaderValue mirror the core's limits.
	MaxPattern     = 8 << 10
	MaxHeaderValue = 32 << 10
)

// ValidURLAction reports whether action is a URL rewrite action.
func ValidURLAction(action string) bool {
	switch action {
	case URLActionReject, URLActionReject200, URLActionRejectImg, URLActionRejectDict,
		URLActionRejectArray, URLActionTinyGIF, URLActionFound, URLActionTemporary, URLActionHeader:
		return true
	}
	return false
}

// ValidHeaderAction reports whether action is a header operation.
func ValidHeaderAction(action string) bool {
	switch action {
	case HeaderActionAdd, HeaderActionDelete, HeaderActionReplace, HeaderActionReplaceRegex:
		return true
	}
	return false
}

// reservedHeaders cannot be rewritten: they frame the message or select the
// connection.
var reservedHeaders = map[string]bool{
	"content-length": true, "transfer-encoding": true, "connection": true, "keep-alive": true,
	"upgrade": true, "te": true, "trailer": true, "host": true, "proxy-connection": true,
	"proxy-authenticate": true, "proxy-authorization": true, "http2-settings": true, "expect": true,
}

// HeaderNameIssue explains why name cannot be rewritten; empty when it can.
func HeaderNameIssue(name string) string {
	if name == "" {
		return "请填写头名称"
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return "头名称含有无效字符"
		}
	}
	if reservedHeaders[strings.ReplaceAll(strings.ToLower(name), "_", "-")] {
		return "该头由代理管理，不能改写"
	}
	return ""
}

// HeaderValueIssue explains why value cannot be a header value.
func HeaderValueIssue(value string) string {
	if len(value) > MaxHeaderValue {
		return "头的值过长"
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return "头的值不能含有控制字符"
		}
	}
	return ""
}

// PatternIssue explains why pattern is not a usable RE2 expression.
func PatternIssue(pattern string) string {
	if strings.TrimSpace(pattern) == "" {
		return "请填写匹配模式"
	}
	if len(pattern) > MaxPattern {
		return "匹配模式过长"
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return "匹配模式无效"
	}
	return ""
}

// Issue returns the first problem of the rule; empty when it is valid.
func (r *URLRewrite) Issue() string {
	if issue := PatternIssue(r.Pattern); issue != "" {
		return issue
	}
	if !ValidURLAction(r.Action) {
		return "未知的动作"
	}
	if r.Action == URLActionFound || r.Action == URLActionTemporary {
		target := strings.TrimSpace(r.Target)
		if target == "" {
			return "请填写跳转地址"
		}
		if HeaderValueIssue(target) != "" {
			return "跳转地址含有换行"
		}
		// Without a capture reference the core needs an absolute http(s)
		// URL with a host.
		if !strings.Contains(target, "$") {
			if u, err := url.Parse(target); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return "跳转地址须为 http 或 https 地址"
			}
		}
	}
	if r.Action == URLActionHeader {
		return HeaderTargetIssue(r.Target)
	}
	return ""
}

// Issue returns the first problem of the rule; empty when it is valid.
func (r *HeaderRewrite) Issue() string {
	if issue := PatternIssue(r.Pattern); issue != "" {
		return issue
	}
	if r.Scope != HeaderScopeRequest && r.Scope != HeaderScopeResponse {
		return "未知的作用范围"
	}
	if len(r.Ops) == 0 {
		return "请添加至少一项操作"
	}
	for _, op := range r.Ops {
		if op == nil || !ValidHeaderAction(op.Action) {
			return "未知的操作"
		}
		if issue := HeaderNameIssue(op.Name); issue != "" {
			return issue
		}
		if issue := HeaderValueIssue(op.Value); issue != "" {
			return issue
		}
		switch op.Action {
		case HeaderActionAdd, HeaderActionReplace:
			if op.Value == "" && op.Action == HeaderActionAdd {
				return "请填写值"
			}
		case HeaderActionReplaceRegex:
			if _, err := regexp.Compile(op.Regex); err != nil || op.Regex == "" || len(op.Regex) > MaxPattern {
				return "替换表达式无效"
			}
		}
	}
	return ""
}

// BodyBytes returns the size of the stored body.
func (r *MapLocal) BodyBytes() int {
	if r.BodyBase64 != "" {
		return base64.StdEncoding.DecodedLen(len(r.BodyBase64))
	}
	return len(r.Body)
}

// Issue returns the first problem of the rule; empty when it is valid.
func (r *MapLocal) Issue() string {
	if issue := PatternIssue(r.Pattern); issue != "" {
		return issue
	}
	if r.Status < 200 || r.Status > 599 {
		return "状态码须在 200–599 之间"
	}
	for _, field := range r.Headers {
		if issue := HeaderNameIssue(field.Name); issue != "" {
			return issue
		}
		if issue := HeaderValueIssue(field.Value); issue != "" {
			return issue
		}
	}
	if r.BodyBase64 != "" {
		if _, err := base64.StdEncoding.DecodeString(r.BodyBase64); err != nil {
			return "正文内容无效"
		}
	}
	if r.BodyBytes() > MaxMapLocalBody {
		return "正文超过 2 MiB"
	}
	if r.BodyBytes() > 0 && (r.Status == 204 || r.Status == 205 || r.Status == 304) {
		return "该状态码不能带正文"
	}
	return ""
}
