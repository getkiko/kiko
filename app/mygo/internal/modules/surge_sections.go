package modules

import (
	"encoding/base64"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func (c *converter) bad(section string, no int, reason, text string) {
	c.res.unsupported(section, no, reason, text)
}

// ---- [MITM]

// mitmIgnoredKeys are the global decryption settings a module may not
// change.
const mitmGlobalReason = "模块不能修改解密的全局设置"

func (c *converter) mitm(no int, line string) {
	key, value, found := strings.Cut(line, "=")
	if !found {
		c.bad(secMITM, no, "格式无法识别", line)
		return
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key != "hostname" {
		reason := mitmGlobalReason
		if key == "hostname-disabled" {
			reason = "已停用的主机名不转换"
		}
		c.bad(secMITM, no, reason, line)
		return
	}
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"%APPEND%", "%INSERT%"} {
		value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	if c.seenHost == nil {
		c.seenHost = map[string]bool{}
	}
	for _, part := range strings.Split(value, ",") {
		host := strings.TrimSpace(part)
		if host == "" {
			continue
		}
		exclude := false
		if strings.HasPrefix(host, "-") {
			exclude, host = true, strings.TrimSpace(host[1:])
		}
		host = strings.TrimSuffix(host, ":443")
		norm, ok := model.NormalizeMITMHostname(host)
		if !ok {
			c.bad(secMITM, no, "主机名「"+host+"」格式不被支持", line)
			continue
		}
		key := norm
		if exclude {
			key = "-" + norm
		}
		if c.seenHost[key] {
			continue
		}
		c.seenHost[key] = true
		if exclude {
			if len(c.exclude) < model.MaxModuleHosts {
				c.exclude = append(c.exclude, norm)
			}
		} else if len(c.include) < model.MaxModuleHosts {
			c.include = append(c.include, norm)
		}
	}
}

// ---- [Rule]

func (c *converter) rule(no int, line string) {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) < 3 {
		c.bad(secRule, no, "仅支持域名与 IP 段规则配合 DIRECT 或 REJECT", line)
		return
	}
	var action string
	switch strings.ToUpper(parts[2]) {
	case "DIRECT":
		action = model.ModuleActionDirect
	case "REJECT", "REJECT-DROP", "REJECT-NO-DROP", "REJECT-TINYGIF":
		action = model.ModuleActionReject
	default:
		c.bad(secRule, no, "策略「"+parts[2]+"」不支持，仅支持 DIRECT 与 REJECT", line)
		return
	}
	rule := &model.ModuleRule{Action: action, Value: parts[1]}
	if strings.EqualFold(parts[0], "SCRIPT") {
		// The script name becomes the script's tag once all lines are read.
		if len(parts) > 3 {
			c.bad(secRule, no, "选项「"+parts[3]+"」已忽略", line)
		}
		if len(c.res.Contents.Rules) >= model.MaxModuleRules {
			c.bad(secRule, no, "规则过多", line)
			return
		}
		rule.Type = model.ModuleRuleScript
		c.res.Contents.Rules = append(c.res.Contents.Rules, rule)
		c.scriptRules = append(c.scriptRules, pendingScriptRule{rule: rule, name: parts[1], line: no, text: line})
		return
	}
	switch strings.ToUpper(parts[0]) {
	case "DOMAIN":
		rule.Type, rule.Value = model.ModuleRuleDomain, strings.ToLower(parts[1])
	case "DOMAIN-SUFFIX":
		rule.Type, rule.Value = model.ModuleRuleDomainSuffix, strings.ToLower(strings.TrimPrefix(parts[1], "."))
	case "DOMAIN-KEYWORD":
		rule.Type = model.ModuleRuleDomainKeyword
	case "IP-CIDR", "IP-CIDR6":
		p, err := netip.ParsePrefix(parts[1])
		if err != nil {
			a, aerr := netip.ParseAddr(parts[1])
			if aerr != nil {
				c.bad(secRule, no, "IP 段无效", line)
				return
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		rule.Value = p.String()
		rule.Type = model.ModuleRuleIPCIDR
		if p.Addr().Is6() {
			rule.Type = model.ModuleRuleIPCIDR6
		}
	default:
		c.bad(secRule, no, "规则类型「"+parts[0]+"」不支持", line)
		return
	}
	for _, flag := range parts[3:] {
		switch strings.ToLower(flag) {
		case "no-resolve":
			rule.NoResolve = true
		case "":
		default:
			c.bad(secRule, no, "选项「"+flag+"」已忽略", line)
		}
	}
	if issue := rule.Issue(); issue != "" {
		c.bad(secRule, no, issue, line)
		return
	}
	if len(c.res.Contents.Rules) >= model.MaxModuleRules {
		c.bad(secRule, no, "规则过多", line)
		return
	}
	c.res.Contents.Rules = append(c.res.Contents.Rules, rule)
}

// ---- [URL Rewrite]

func (c *converter) urlRewrite(no int, line string) {
	toks, err := fields(line)
	if err != nil {
		c.bad(secURL, no, err.Error(), line)
		return
	}
	var pattern, target, kind string
	switch len(toks) {
	case 2:
		pattern, kind = toks[0], toks[1]
	case 3:
		pattern, target, kind = toks[0], toks[1], toks[2]
	default:
		c.bad(secURL, no, "格式应为「正则 目标 类型」", line)
		return
	}
	rule := &model.URLRewrite{ID: c.id("u"), Pattern: pattern}
	switch strings.ToLower(kind) {
	case "302":
		rule.Action, rule.Target = model.URLActionFound, target
	case "307":
		rule.Action, rule.Target = model.URLActionTemporary, target
	case "header":
		rule.Action, rule.Target = model.URLActionHeader, target
	case "reject":
		rule.Action = model.URLActionReject
	case "reject-200":
		rule.Action = model.URLActionReject200
	case "reject-img":
		rule.Action = model.URLActionRejectImg
	case "reject-dict":
		rule.Action = model.URLActionRejectDict
	case "reject-array":
		rule.Action = model.URLActionRejectArray
	case "reject-tinygif":
		rule.Action = model.URLActionTinyGIF
	case "reject-drop", "reject-no-drop":
		// The core has no connection drop: an empty 404 is the closest.
		rule.Action = model.URLActionReject
		c.bad(secURL, no, "近似映射为 reject（返回空响应）", line)
	default:
		c.bad(secURL, no, "类型「"+kind+"」不支持", line)
		return
	}
	if issue := rule.Issue(); issue != "" {
		c.bad(secURL, no, issue, line)
		return
	}
	if c.full(len(c.rewrites.URL)) {
		c.bad(secURL, no, "改写规则过多", line)
		return
	}
	c.rewrites.URL = append(c.rewrites.URL, rule)
}

// ---- [Header Rewrite]

func (c *converter) headerRewrite(no int, line string) {
	toks, err := fields(line)
	if err != nil {
		c.bad(secHeader, no, err.Error(), line)
		return
	}
	if len(toks) < 4 {
		c.bad(secHeader, no, "格式应为「作用范围 正则 操作 头名称 …」", line)
		return
	}
	rule := &model.HeaderRewrite{ID: c.id("h"), Pattern: toks[1]}
	switch strings.ToLower(toks[0]) {
	case "http-request":
		rule.Scope = model.HeaderScopeRequest
	case "http-response":
		rule.Scope = model.HeaderScopeResponse
	default:
		c.bad(secHeader, no, "作用范围「"+toks[0]+"」不支持", line)
		return
	}
	op := &model.HeaderEdit{Name: toks[3]}
	switch strings.ToLower(toks[2]) {
	case "header-add":
		op.Action = model.HeaderActionAdd
		if len(toks) < 5 {
			c.bad(secHeader, no, "缺少头的值", line)
			return
		}
		op.Value = strings.Join(toks[4:], " ")
	case "header-del":
		op.Action = model.HeaderActionDelete
	case "header-replace":
		op.Action = model.HeaderActionReplace
		if len(toks) < 5 {
			c.bad(secHeader, no, "缺少头的值", line)
			return
		}
		op.Value = strings.Join(toks[4:], " ")
	case "header-replace-regex":
		op.Action = model.HeaderActionReplaceRegex
		if len(toks) != 6 {
			c.bad(secHeader, no, "格式应为「header-replace-regex 头名称 正则 替换」", line)
			return
		}
		op.Regex, op.Value = toks[4], toks[5]
	default:
		c.bad(secHeader, no, "操作「"+toks[2]+"」不支持", line)
		return
	}
	rule.Ops = []*model.HeaderEdit{op}
	if issue := rule.Issue(); issue != "" {
		c.bad(secHeader, no, issue, line)
		return
	}
	if c.full(len(c.rewrites.Headers)) {
		c.bad(secHeader, no, "改写规则过多", line)
		return
	}
	c.rewrites.Headers = append(c.rewrites.Headers, rule)
}

// ---- [Body Rewrite]

func (c *converter) bodyRewrite(no int, line string) {
	toks, err := fields(line)
	if err != nil {
		c.bad(secBody, no, err.Error(), line)
		return
	}
	if len(toks) < 3 {
		c.bad(secBody, no, "格式应为「类型 正则 …」", line)
		return
	}
	rule := &model.BodyRewrite{ID: c.id("b"), Pattern: toks[1]}
	kind := strings.ToLower(toks[0])
	// Surge writes a regex replacement as "http-response <url> <regex>
	// <replacement> [<regex> <replacement> ...]"; the longer "-replace-regex"
	// spelling is accepted too.
	switch {
	case kind == "http-request" || kind == "http-response":
		rule.Scope = model.HeaderScopeRequest
		if kind == "http-response" {
			rule.Scope = model.HeaderScopeResponse
		}
		kind = "replace-regex"
	case strings.HasPrefix(kind, "http-request-"):
		rule.Scope = model.HeaderScopeRequest
		kind = strings.TrimPrefix(kind, "http-request-")
	case strings.HasPrefix(kind, "http-response-"):
		rule.Scope = model.HeaderScopeResponse
		kind = strings.TrimPrefix(kind, "http-response-")
	default:
		c.bad(secBody, no, "类型「"+toks[0]+"」不支持", line)
		return
	}
	switch kind {
	case "replace-regex":
		pairs := toks[2:]
		if len(pairs)%2 != 0 {
			c.bad(secBody, no, "正则与替换内容应成对出现", line)
			return
		}
		for i := 0; i < len(pairs); i += 2 {
			rule.Edits = append(rule.Edits, &model.BodyEdit{Action: model.BodyActionReplaceRegex, Regex: pairs[i], Value: pairs[i+1]})
		}
	case "jq":
		if len(toks) != 3 {
			c.bad(secBody, no, "格式应为「类型 正则 jq 表达式」", line)
			return
		}
		edits, reason := jqEdits(toks[2])
		if reason != "" {
			c.bad(secBody, no, reason, line)
			return
		}
		rule.Edits = edits
	default:
		c.bad(secBody, no, "类型「"+toks[0]+"」不支持", line)
		return
	}
	if issue := rule.Issue(); issue != "" {
		c.bad(secBody, no, issue, line)
		return
	}
	if c.full(len(c.rewrites.Body)) {
		c.bad(secBody, no, "改写规则过多", line)
		return
	}
	c.rewrites.Body = append(c.rewrites.Body, rule)
}

// ---- [Map Local]

// tinyGIF is the 1x1 transparent GIF of data-type=tiny-gif.
const tinyGIF = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"

func (c *converter) mapLocal(no int, line string) {
	pattern, rest, err := nextToken(line)
	if err != nil {
		c.bad(secMapLoc, no, err.Error(), line)
		return
	}
	params, err := scanParams(rest)
	if err != nil {
		c.bad(secMapLoc, no, err.Error(), line)
		return
	}
	rule := &model.MapLocal{ID: c.id("m"), Pattern: pattern, Status: 200}
	dataType, data, hasData, headerText := "", "", false, ""
	var notes []string
	for _, p := range params {
		switch p.key {
		case "data", "data-path":
			data, hasData = p.value, true
			if p.key == "data-path" && dataType == "" {
				dataType = "file"
			}
		case "data-type":
			dataType = strings.ToLower(p.value)
		case "status-code":
			n, err := strconv.Atoi(strings.TrimSpace(p.value))
			if err != nil {
				c.bad(secMapLoc, no, "状态码无效", line)
				return
			}
			rule.Status = n
		case "header":
			headerText = p.value
		case "mock-data-is-base64":
			if strings.EqualFold(p.value, "true") && dataType == "" {
				dataType = "base64"
			}
		default:
			notes = append(notes, p.key)
		}
	}
	for _, field := range strings.Split(headerText, "|") {
		if strings.TrimSpace(field) == "" {
			continue
		}
		name, value, ok := strings.Cut(field, ":")
		if !ok {
			c.bad(secMapLoc, no, "头「"+field+"」缺少冒号", line)
			return
		}
		rule.Headers = append(rule.Headers, model.HeaderField{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	fetch := ""
	switch dataType {
	case "", "text":
		if dataType == "" && isHTTPURL(data) {
			fetch = data
			break
		}
		rule.Body = data
	case "base64":
		raw := strings.Join(strings.Fields(data), "")
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(raw)
		}
		if err != nil {
			c.bad(secMapLoc, no, "base64 内容无效", line)
			return
		}
		rule.BodyBase64 = base64.StdEncoding.EncodeToString(decoded)
	case "tiny-gif":
		rule.BodyBase64 = tinyGIF
		if !slices.ContainsFunc(rule.Headers, func(f model.HeaderField) bool { return strings.EqualFold(f.Name, "content-type") }) {
			rule.Headers = append(rule.Headers, model.HeaderField{Name: "Content-Type", Value: "image/gif"})
		}
	case "file":
		if !hasData || !isHTTPURL(data) {
			c.bad(secMapLoc, no, "仅支持 http(s) 地址的映射数据，不支持本地文件", line)
			return
		}
		fetch = data
	default:
		c.bad(secMapLoc, no, "数据类型「"+dataType+"」不支持", line)
		return
	}
	if fetch == "" {
		if issue := rule.Issue(); issue != "" {
			c.bad(secMapLoc, no, issue, line)
			return
		}
	} else if rule.Status < 200 || rule.Status > 599 {
		c.bad(secMapLoc, no, "状态码须在 200–599 之间", line)
		return
	}
	if c.full(len(c.rewrites.MapLocal)) {
		c.bad(secMapLoc, no, "改写规则过多", line)
		return
	}
	c.rewrites.MapLocal = append(c.rewrites.MapLocal, rule)
	if fetch != "" {
		c.res.addFetch(fetch, FetchData)
		c.res.maps = append(c.res.maps, pendingMap{url: fetch, rule: rule, line: no, text: line})
	}
	for _, key := range notes {
		c.bad(secMapLoc, no, "参数「"+key+"」已忽略", line)
	}
}

func isHTTPURL(s string) bool {
	if len(s) > 2048 || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

// ---- [Script]

var cosmeticScriptKeys = map[string]bool{
	"script-update-interval": true, "update-interval": true, "debug": true, "tag": true,
}

// scriptTag derives a tag from the script name: letters, digits, dot,
// underscore and hyphen; other characters become underscores.
func scriptTag(name string) string {
	var b strings.Builder
	total, kept := 0, 0
	for _, r := range name {
		total++
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
			if r != '_' {
				kept++
			}
		default:
			b.WriteByte('_')
		}
		if b.Len() >= model.MaxModuleScriptTag-4 {
			break
		}
	}
	tag := strings.Trim(b.String(), "_")
	// A name mostly outside ASCII gives a meaningless tag: use "script".
	if kept*2 < total || strings.Trim(tag, "_-.") == "" {
		return "script"
	}
	return tag
}

func (c *converter) uniqueTag(base string) string {
	tag := base
	for n := 2; c.tags[tag]; n++ {
		tag = base + "-" + strconv.Itoa(n)
	}
	c.tags[tag] = true
	return tag
}

func (c *converter) script(no int, line string) {
	name, rest, found := strings.Cut(line, "=")
	name = strings.TrimSpace(name)
	if !found || name == "" {
		c.bad(secScript, no, "格式应为「名称 = type=…, script-path=…」", line)
		return
	}
	s := &model.HTTPScript{ID: c.id("s"), Name: name}
	var path string
	var notes []string
	disabled := false
	typ := ""
	params, perr := scanScriptParams(rest)
	if perr != nil {
		c.bad(secScript, no, perr.Error(), line)
		return
	}
	for _, p := range params {
		key, value := p.key, p.value
		if !p.hasValue {
			notes = append(notes, "参数「"+key+"」已忽略")
			continue
		}
		switch key {
		case "type":
			typ = strings.ToLower(value)
		case "pattern":
			s.Pattern = value
		case "script-path":
			path = value
		case "requires-body", "require-body":
			s.RequiresBody = strings.EqualFold(value, "true") || value == "1"
		case "binary-body-mode":
			s.BinaryBody = strings.EqualFold(value, "true") || value == "1"
		case "max-size":
			n, err := strconv.Atoi(value)
			if err != nil {
				c.bad(secScript, no, "max-size 无效", line)
				return
			}
			switch {
			case n == 0:
			case n < 0:
				// Surge: no limit. The core's largest cap is the closest.
				s.MaxSize = model.MaxBodySizeLimit
			case n > model.MaxBodySizeLimit:
				s.MaxSize = model.MaxBodySizeLimit
			default:
				s.MaxSize = (n + 1023) / 1024 * 1024
			}
		case "argument":
			s.Argument = value
		case "timeout":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				c.bad(secScript, no, "timeout 无效", line)
				return
			}
			s.Timeout = n
		case "cronexp":
			s.Cron = value
		case "enable":
			disabled = strings.EqualFold(value, "false") || value == "0"
		default:
			if !cosmeticScriptKeys[key] {
				notes = append(notes, "参数「"+key+"」已忽略")
			}
		}
	}
	switch typ {
	case "http-request":
		s.Type = model.ScriptHTTPRequest
	case "http-response":
		s.Type = model.ScriptHTTPResponse
	case "cron":
		s.Type = model.ScriptCron
	case "generic":
		s.Type = model.ScriptGeneric
	case "rule":
		s.Type = model.ScriptRule
	case "dns":
		s.Type = model.ScriptDNS
	case "":
		c.bad(secScript, no, "缺少 type", line)
		return
	default:
		c.bad(secScript, no, "脚本类型「"+typ+"」不支持", line)
		return
	}
	if s.IsHTTP() && strings.TrimSpace(s.Pattern) == "" {
		c.bad(secScript, no, "缺少 pattern", line)
		return
	}
	if !s.IsHTTP() {
		s.Pattern = ""
	}
	if s.Type != model.ScriptCron {
		s.Cron = ""
	}
	if s.BinaryBody && !s.RequiresBody {
		s.BinaryBody = false
		notes = append(notes, "未开启 requires-body，binary-body-mode 已忽略")
	}
	if !s.IsHTTP() {
		s.RequiresBody, s.BinaryBody, s.MaxSize = false, false, 0
	}
	if s.IsDecision() {
		// Surge counts the timeout in seconds; the model keeps rule and dns
		// scripts in milliseconds, at most one second.
		if s.Timeout > 0 {
			ms := s.Timeout * 1000
			if s.Timeout > model.MaxScriptTimeoutDecision/1000 {
				c.bad(secScript, no, "超时 "+strconv.Itoa(s.Timeout)+" 秒降为上限 "+strconv.Itoa(model.MaxScriptTimeoutDecision)+" 毫秒", line)
				ms = model.MaxScriptTimeoutDecision
			}
			s.Timeout = ms
		}
	} else {
		limit := model.MaxScriptTimeoutHTTP
		if !s.IsHTTP() {
			limit = model.MaxScriptTimeoutRun
		}
		if s.Timeout > limit {
			c.bad(secScript, no, "超时 "+strconv.Itoa(s.Timeout)+" 秒降为上限 "+strconv.Itoa(limit)+" 秒", line)
			s.Timeout = limit
		}
	}
	if path == "" {
		c.bad(secScript, no, "缺少 script-path", line)
		return
	}
	target, ok := c.resolveURL(path)
	if !ok {
		c.bad(secScript, no, "仅支持 http(s) 地址的脚本，不支持本地文件", line)
		return
	}
	s.Disabled = disabled
	s.Tag = c.uniqueTag(scriptTag(name))
	if s.Type == model.ScriptCron {
		if issue := model.CronIssue(s.Cron); issue != "" {
			c.bad(secScript, no, issue, line)
			return
		}
	}
	if len(c.res.Contents.Scripts) >= model.MaxModuleScripts {
		c.bad(secScript, no, "脚本过多", line)
		return
	}
	c.res.Contents.Scripts = append(c.res.Contents.Scripts, s)
	if c.scriptByName == nil {
		c.scriptByName = map[string]*model.HTTPScript{}
	}
	if _, dup := c.scriptByName[name]; !dup {
		c.scriptByName[name] = s
	}
	c.res.addFetch(target, FetchScript)
	c.res.scripts = append(c.res.scripts, pendingScript{url: target, script: s, line: no, text: line})
	for _, note := range notes {
		c.bad(secScript, no, note, line)
	}
}

func (c *converter) resolveURL(path string) (string, bool) {
	if isHTTPURL(path) {
		return path, true
	}
	if c.opts.BaseURL == "" || strings.Contains(path, "://") || strings.HasPrefix(path, "file:") ||
		strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") || strings.HasPrefix(path, "\\") {
		return "", false
	}
	base, err := url.Parse(c.opts.BaseURL)
	if err != nil || base.Scheme != "http" && base.Scheme != "https" {
		return "", false
	}
	ref, err := url.Parse(path)
	if err != nil || ref.IsAbs() && ref.Scheme != "http" && ref.Scheme != "https" {
		return "", false
	}
	full := base.ResolveReference(ref).String()
	return full, isHTTPURL(full)
}

// ---- Resolve

// Resolve fills the downloaded scripts and mapping data into the result and
// validates the content again. fetched maps a URL of Fetches to its body.
// Items whose download is missing or whose content the model refuses move to
// Unsupported; missing lists the URLs that were not supplied. The returned
// error is a limit of the module as a whole.
func (r *Result) Resolve(fetched map[string][]byte) (missing []string, err error) {
	defer r.dropOrphanScriptRules()
	failed := map[*model.HTTPScript]bool{}
	lineOf := map[*model.HTTPScript]pendingScript{}
	for _, p := range r.scripts {
		lineOf[p.script] = p
	}
	for _, p := range r.scripts {
		body, ok := fetched[p.url]
		if !ok {
			if !slices.Contains(missing, p.url) {
				missing = append(missing, p.url)
			}
			r.unsupported(secScript, p.line, "脚本未能下载", p.text)
			failed[p.script] = true
			continue
		}
		p.script.Source = string(body)
		if !utf8.ValidString(p.script.Source) {
			r.unsupported(secScript, p.line, "脚本含有无效字符", p.text)
			failed[p.script] = true
		}
	}
	r.scripts = nil
	var kept []*model.HTTPScript
	for _, s := range r.Contents.Scripts {
		if !failed[s] {
			if issue := s.Issue(); issue != "" {
				p := lineOf[s]
				r.unsupported(secScript, p.line, "脚本「"+s.EffectiveTag()+"」："+issue, p.text)
				continue
			}
			kept = append(kept, s)
		}
	}
	r.Contents.Scripts = kept

	badMap := map[*model.MapLocal]bool{}
	for _, p := range r.maps {
		body, ok := fetched[p.url]
		if !ok {
			if !slices.Contains(missing, p.url) {
				missing = append(missing, p.url)
			}
			r.unsupported(secMapLoc, p.line, "映射数据未能下载", p.text)
			badMap[p.rule] = true
			continue
		}
		if utf8.Valid(body) && !strings.ContainsRune(string(body), 0) {
			p.rule.Body = string(body)
		} else {
			p.rule.BodyBase64 = base64.StdEncoding.EncodeToString(body)
		}
		if issue := p.rule.Issue(); issue != "" {
			r.unsupported(secMapLoc, p.line, issue, p.text)
			badMap[p.rule] = true
		}
	}
	r.maps = nil
	if rw := r.Contents.Rewrites; rw != nil {
		rw.MapLocal = slices.DeleteFunc(rw.MapLocal, func(m *model.MapLocal) bool { return badMap[m] })
		if rw.Empty() {
			r.Contents.Rewrites = nil
		}
	}
	if issue := r.Contents.Issue(); issue != "" {
		return missing, &moduleError{issue}
	}
	return missing, nil
}

type moduleError struct{ msg string }

func (e *moduleError) Error() string { return e.msg }

// pendingScriptRule is a SCRIPT rule waiting for its script's tag.
type pendingScriptRule struct {
	rule *model.ModuleRule
	name string
	line int
	text string
}

// resolveScriptRules points each SCRIPT rule at the tag of the rule script
// it names; a rule naming no rule script moves to Unsupported.
func (c *converter) resolveScriptRules() {
	if len(c.scriptRules) == 0 {
		return
	}
	drop := map[*model.ModuleRule]bool{}
	for _, p := range c.scriptRules {
		script := c.scriptByName[p.name]
		switch {
		case script == nil:
			c.bad(secRule, p.line, "脚本「"+p.name+"」不存在", p.text)
			drop[p.rule] = true
		case script.Type != model.ScriptRule:
			c.bad(secRule, p.line, "脚本「"+p.name+"」不是 rule 类型", p.text)
			drop[p.rule] = true
		default:
			p.rule.Value = script.Tag
			if issue := p.rule.Issue(); issue != "" {
				c.bad(secRule, p.line, issue, p.text)
				drop[p.rule] = true
			}
		}
	}
	var kept []*model.ModuleRule
	for _, r := range c.res.Contents.Rules {
		if !drop[r] {
			kept = append(kept, r)
		}
	}
	c.res.Contents.Rules = kept
}

// dropOrphanScriptRules removes the SCRIPT rules whose script did not
// survive the download and validation.
func (r *Result) dropOrphanScriptRules() {
	tags := map[string]bool{}
	for _, s := range r.Contents.Scripts {
		if s.Type == model.ScriptRule {
			tags[s.Tag] = true
		}
	}
	var kept []*model.ModuleRule
	for _, rule := range r.Contents.Rules {
		if rule.Type == model.ModuleRuleScript && !tags[rule.Value] {
			r.unsupported(secRule, 0, "脚本「"+rule.Value+"」不可用", "SCRIPT,"+rule.Value)
			continue
		}
		kept = append(kept, rule)
	}
	r.Contents.Rules = kept
}
