package model

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Script types. rule scripts are the `script` condition of route and DNS
// rules, dns scripts the `script` action of DNS rules; both run synchronously
// with a timeout in milliseconds.
const (
	ScriptHTTPRequest  = "http-request"
	ScriptHTTPResponse = "http-response"
	ScriptGeneric      = "generic"
	ScriptCron         = "cron"
	ScriptRule         = "rule"
	ScriptDNS          = "dns"
)

// Script limits; they mirror the core's compile step.
const (
	MaxScriptTag         = 64
	MaxScriptSource      = 1 << 20
	MaxScriptArgument    = 16 << 10
	MaxScriptPattern     = 4096
	MaxScriptDepth       = 2000
	MaxScripts           = 256
	MaxScriptTimeoutHTTP = 30
	MaxScriptTimeoutRun  = 60
	// MaxScriptTimeoutDecision and DefaultScriptTimeoutDecision are in
	// milliseconds: rule and dns scripts keep Timeout in milliseconds.
	MaxScriptTimeoutDecision     = 1000
	DefaultScriptTimeoutDecision = 100
	// DefaultScriptTimeoutHTTP and DefaultScriptTimeoutRun are in seconds.
	DefaultScriptTimeoutHTTP = 10
	DefaultScriptTimeoutRun  = 60
)

// HTTPScript is one user script. http-request and http-response scripts run
// on decrypted messages whose URL matches Pattern, after the rewrite rules;
// generic scripts run on demand. Source is the script text; the emitter
// writes it to a content-addressed file in the core's working directory.
type HTTPScript struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Tag names the script in the core and in hit records: letters, digits,
	// dot, underscore and hyphen, at most 64, unique. Empty means the ID.
	Tag  string `json:"tag,omitempty"`
	Type string `json:"type"`
	// Pattern is an RE2 expression matched against the full request URL;
	// required for http-request and http-response, unused by generic and cron.
	Pattern string `json:"pattern,omitempty"`
	// Cron is the five-field schedule of a cron script (see CronIssue).
	Cron   string `json:"cron,omitempty"`
	Source string `json:"source"`
	// RequiresBody makes the script run only on messages whose body is
	// buffered; BinaryBody passes the body as a Uint8Array.
	RequiresBody bool `json:"requiresBody,omitempty"`
	BinaryBody   bool `json:"binaryBody,omitempty"`
	// MaxSize is the body cap in bytes (multiple of 1 KiB, 1 KiB to
	// 32 MiB); 0 uses the rewrite buffer limit.
	MaxSize int `json:"maxSize,omitempty"`
	// Timeout is in seconds; 0 selects the default of the type. rule and dns
	// scripts keep it in milliseconds (1 to 1000, default 100).
	Timeout  int    `json:"timeout,omitempty"`
	Argument string `json:"argument,omitempty"`
}

// ValidScriptType reports whether t is a supported script type.
func ValidScriptType(t string) bool {
	switch t {
	case ScriptHTTPRequest, ScriptHTTPResponse, ScriptGeneric, ScriptCron, ScriptRule, ScriptDNS:
		return true
	}
	return false
}

// IsHTTP reports whether the script runs on HTTP messages.
func (s *HTTPScript) IsHTTP() bool {
	return s.Type == ScriptHTTPRequest || s.Type == ScriptHTTPResponse
}

// IsRun reports whether the script runs outside HTTP messages: generic
// scripts on demand, cron scripts on a schedule and on demand.
func (s *HTTPScript) IsRun() bool {
	return s.Type == ScriptGeneric || s.Type == ScriptCron
}

// IsDecision reports whether the script decides a rule condition (rule) or a
// DNS answer (dns) synchronously.
func (s *HTTPScript) IsDecision() bool { return s.Type == ScriptRule || s.Type == ScriptDNS }

// EffectiveTag returns the tag the core knows the script by.
func (s *HTTPScript) EffectiveTag() string {
	if s.Tag != "" {
		return s.Tag
	}
	return s.ID
}

// ScriptTagIssue explains why tag is not a usable script tag.
func ScriptTagIssue(tag string) string {
	if tag == "" {
		return "请填写标识"
	}
	if len(tag) > MaxScriptTag {
		return "标识过长"
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return "标识只能含字母、数字、点、下划线和连字符"
		}
	}
	return ""
}

// ScriptMaxSizeIssue explains why n (bytes) is not a usable script body
// cap; zero selects the default.
func ScriptMaxSizeIssue(n int) string {
	if n == 0 {
		return ""
	}
	if n < MinBodySize || n > MaxBodySizeLimit || n%1024 != 0 {
		return "正文上限须在 1 KiB 到 32 MiB 之间"
	}
	return ""
}

// ScriptTimeoutIssue explains why seconds is not a usable timeout for the
// script type; zero selects the default. The value of a rule or dns script
// is in milliseconds.
func ScriptTimeoutIssue(typ string, seconds int) string {
	if seconds == 0 {
		return ""
	}
	if typ == ScriptRule || typ == ScriptDNS {
		if seconds < 0 || seconds > MaxScriptTimeoutDecision {
			return "超时须在 1 到 " + strconv.Itoa(MaxScriptTimeoutDecision) + " 毫秒之间"
		}
		return ""
	}
	limit := MaxScriptTimeoutHTTP
	if typ == ScriptGeneric || typ == ScriptCron {
		limit = MaxScriptTimeoutRun
	}
	if seconds < 0 || seconds > limit {
		return "超时须在 1 到 " + strconv.Itoa(limit) + " 秒之间"
	}
	return ""
}

// ScriptArgumentIssue explains why arg cannot be a script argument.
func ScriptArgumentIssue(arg string) string {
	if len(arg) > MaxScriptArgument {
		return "参数过长"
	}
	if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
		return "参数含有无效字符"
	}
	return ""
}

// Issue returns the first problem of the script; empty when it is valid.
func (s *HTTPScript) Issue() string {
	if issue := ScriptTagIssue(s.EffectiveTag()); issue != "" {
		return issue
	}
	if !ValidScriptType(s.Type) {
		return "未知的脚本类型"
	}
	if s.IsHTTP() {
		if issue := PatternIssue(s.Pattern); issue != "" {
			return issue
		}
		if len(s.Pattern) > MaxScriptPattern {
			return "匹配模式过长"
		}
		if s.BinaryBody && !s.RequiresBody {
			return "二进制正文需要同时开启读取正文"
		}
	}
	if s.IsDecision() && (s.Pattern != "" || s.RequiresBody || s.BinaryBody || s.MaxSize != 0 || s.Cron != "") {
		return "规则脚本与 DNS 脚本不带匹配模式、正文或计划"
	}
	if s.Type == ScriptCron {
		if issue := CronIssue(s.Cron); issue != "" {
			return issue
		}
	}
	if strings.TrimSpace(s.Source) == "" {
		return "请填写脚本内容"
	}
	if len(s.Source) > MaxScriptSource {
		return "脚本超过 1 MiB"
	}
	if err := checkScriptSource(s.Source, scriptSourceLimits); err != nil {
		return err.Error()
	}
	if !utf8.ValidString(s.Source) || strings.ContainsRune(s.Source, 0) {
		return "脚本含有无效字符"
	}
	if s.IsHTTP() {
		if issue := ScriptMaxSizeIssue(s.MaxSize); issue != "" {
			return issue
		}
	}
	if issue := ScriptTimeoutIssue(s.Type, s.Timeout); issue != "" {
		return issue
	}
	return ScriptArgumentIssue(s.Argument)
}

// EnabledScripts reports whether any script is enabled and valid.
func (t *HTTPTools) EnabledScripts() bool {
	if t == nil {
		return false
	}
	for _, s := range t.Scripts {
		if s != nil && !s.Disabled && s.Issue() == "" {
			return true
		}
	}
	return false
}
