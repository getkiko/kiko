package routes

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// services names well-known ports.
var services = map[string]string{"22": "SSH", "53": "DNS", "80": "HTTP", "443": "HTTPS", "853": "DoT", "123": "NTP"}

var (
	reWiFiMAC     = regexp.MustCompile(`(?i)^(?:[\da-f]{2}[:-]){5}[\da-f]{2}$`)
	rePortSpaces  = regexp.MustCompile(`\s*([-–:])\s*`)
	reSetPrefix   = regexp.MustCompile(`(?i)^set\s*:?\s*`)
	reWantsSets   = regexp.MustCompile(`(?i)^set(?:\s|:|$)`)
	rePrivateSet  = regexp.MustCompile(`(?i)^set:private_ip$`)
	reSetColon    = regexp.MustCompile(`(?i)^set:`)
	errPortFormat = "输入 1–65535 的端口号或范围，例如 80,443 或 1-123"
)

// tokenSuggestions are what a typed value of a typed field could add
// (PolicyTokenField): the parsed ports, the networks around an address
// or the rule sets, a Wi-Fi name or access point.
func tokenSuggestions(doc *model.Doc, kind, draft string) []kit.TokenSuggestion {
	var out []kit.TokenSuggestion
	if kind == model.TypedPort || kind == model.TypedSourcePort {
		if entries, ok := model.ParsePortInput(rePortSpaces.ReplaceAllString(draft, "$1")); ok {
			labels := make([]string, len(entries))
			for i, e := range entries {
				labels[i] = strings.Replace(e.Value, ":", "-", 1)
			}
			detail := "端口"
			switch {
			case len(entries) > 1:
				detail = fmt.Sprintf("%d 个条件", len(entries))
			case entries[0].Field == "port_range":
				detail = "端口范围"
			case services[entries[0].Value] != "":
				detail = services[entries[0].Value]
			}
			out = append(out, kit.TokenSuggestion{ID: "ports", Label: strings.Join(labels, ", "), Detail: detail})
		}
		return out
	}
	if draft == "" {
		return nil
	}
	switch kind {
	case model.TypedIP:
		for _, s := range model.AddressSuggestions(draft) {
			out = append(out, kit.TokenSuggestion{ID: s.ID, Label: s.Label, Detail: s.Detail})
		}
		filter := strings.ToLower(reSetPrefix.ReplaceAllString(draft, ""))
		if reWantsSets.MatchString(draft) || len(out) == 0 {
			if filter == "" || strings.Contains("private_ip 私有目标地址 局域网", filter) {
				out = append(out, kit.TokenSuggestion{ID: "private", Label: "set:private_ip", Detail: "私有目标地址"})
			}
			for _, o := range model.RuleSetOptions(doc.Route.RuleSets) {
				if !strings.Contains(strings.ToLower(o.Value+" "+o.Label), filter) {
					continue
				}
				detail := "规则集"
				if parts := strings.Split(o.Label, " · "); len(parts) > 1 {
					detail = strings.Join(parts[1:], " · ")
				}
				out = append(out, kit.TokenSuggestion{ID: "set:" + o.Value, Label: "set:" + o.Value, Detail: detail})
			}
		}
	case model.TypedWiFi:
		if mac := wifiMAC(draft); mac != "" {
			out = append(out, kit.TokenSuggestion{ID: "wifi", Label: mac, Detail: "Wi-Fi 接入点"})
		} else {
			out = append(out, kit.TokenSuggestion{ID: "wifi", Label: draft, Detail: "Wi-Fi 名称"})
		}
	}
	return out
}

// wifiMAC is a typed access point MAC, normalized, "" for a name.
func wifiMAC(draft string) string {
	if !reWiFiMAC.MatchString(draft) {
		return ""
	}
	return strings.ReplaceAll(strings.ToLower(draft), "-", ":")
}

// tokenAdditions parses what a typed field commits: the values to add, or
// the message why it cannot.
func tokenAdditions(doc *model.Doc, kind, draft, suggestion string) ([]model.MatchValue, string) {
	switch kind {
	case model.TypedPort, model.TypedSourcePort:
		entries, ok := model.ParsePortInput(rePortSpaces.ReplaceAllString(draft, "$1"))
		if !ok {
			return nil, errPortFormat
		}
		var out []model.MatchValue
		for _, e := range entries {
			field := e.Field
			if kind == model.TypedSourcePort {
				field = "source_" + field
			}
			out = append(out, model.MatchValue{Field: field, Value: e.Value})
		}
		return out, ""
	case model.TypedWiFi:
		if mac := wifiMAC(draft); mac != "" {
			return []model.MatchValue{{Field: "wifi_bssid", Value: mac}}, ""
		}
		return []model.MatchValue{{Field: "wifi_ssid", Value: draft}}, ""
	}
	if suggestion == "private" || rePrivateSet.MatchString(draft) {
		return []model.MatchValue{{Field: "ip_is_private", Value: true}}, ""
	}
	if strings.HasPrefix(suggestion, "set:") {
		return []model.MatchValue{{Field: "rule_set", Value: suggestion[4:]}}, ""
	}
	raw := suggestion
	if raw == "" {
		raw = draft
	}
	if network, ok := model.CanonicalNetwork(raw, -1); ok {
		value := raw
		if strings.Contains(raw, "/") {
			value = network
		}
		return []model.MatchValue{{Field: "ip_cidr", Value: value}}, ""
	}
	name := strings.ToLower(reSetColon.ReplaceAllString(raw, ""))
	for _, o := range model.RuleSetOptions(doc.Route.RuleSets) {
		if strings.ToLower(o.Value) == name {
			return []model.MatchValue{{Field: "rule_set", Value: o.Value}}, ""
		}
	}
	return nil, "填写 IP 或 CIDR，或从下拉建议中选择规则集"
}

// tokenLabel is how a typed value shows on its chip.
func tokenLabel(field string, value any) string {
	switch {
	case field == "ip_is_private":
		return "set:private_ip"
	case field == "rule_set":
		return "set:" + fmt.Sprint(value)
	case strings.HasSuffix(field, "_range"):
		return strings.Replace(fmt.Sprint(value), ":", "-", 1)
	}
	return fmt.Sprint(value)
}

// clearTokens removes every value of a kind.
func clearTokens(group *model.Condition, kind string) *model.Condition {
	next := group
	for {
		tokens := model.MatchTokens(next, kind)
		if len(tokens) == 0 {
			return next
		}
		next = model.RemoveMatchToken(next, tokens[len(tokens)-1])
	}
}

// tokenField is the TAG input of ports, IP addresses (with rule sets) and
// Wi-Fi.
func (w *Workspace) tokenField(c *ui.Context, doc *model.Doc, e *ruleEditor, kind, label string, group *model.Condition, path []int) {
	s := &e.cond
	key := pathKey(path) + ":" + kind
	entries := model.MatchTokens(group, kind)
	excluded, mixed := model.MatchExclusion(group, kind)
	port := kind == model.TypedPort || kind == model.TypedSourcePort
	fieldHead(c, label, len(entries) > 0, excluded, mixed,
		func() { e.replaceRules(path, model.SetMatchExclusion(group, kind, !excluded)) },
		func() { e.replaceRules(path, clearTokens(group, kind)) })
	query := s.queries[key]
	draft := strings.TrimSpace(query)
	var tokens []kit.Token
	for _, t := range entries {
		text := tokenLabel(t.Field, t.Value)
		detail := ""
		switch {
		case t.Excluded:
			detail = "排除"
		case kind == model.TypedWiFi && t.Field == "wifi_bssid":
			detail = "接入点"
		case kind == model.TypedWiFi:
			detail = "名称"
		case t.Field == "ip_is_private":
			detail = "私有地址"
		case port && !strings.HasSuffix(t.Field, "range"):
			detail = services[fmt.Sprint(t.Value)]
		}
		tokens = append(tokens, kit.Token{ID: t.ID, Label: text, Title: text, Detail: detail})
	}
	placeholder := "Wi-Fi 名称或接入点 MAC，回车添加"
	switch {
	case port:
		placeholder = "例如 80, 30-500"
	case kind == model.TypedIP:
		placeholder = "IP、网段或规则集名称，输入 set 查看地址集"
	}
	input := kit.TokenInput(c, "tokens", &query, kit.TokenInputOptions{
		Label: label, Placeholder: placeholder, Error: s.errors[key], Tokens: tokens,
		Suggestions: tokenSuggestions(doc, kind, draft),
		Commit: func(suggestion string) bool {
			if draft == "" && suggestion == "" {
				return false
			}
			additions, problem := tokenAdditions(doc, kind, draft, suggestion)
			if problem != "" {
				s.errors[key] = problem
				return false
			}
			e.replaceRules(path, model.AddMatchValues(group, kind, additions))
			s.errors[key] = ""
			return true
		},
		Remove: func(id string) {
			for _, t := range entries {
				if t.ID == id {
					e.replaceRules(path, model.RemoveMatchToken(group, t))
				}
			}
		},
	})
	if input.Changed() {
		s.errors[key] = ""
	}
	s.queries[key] = query
}

// domainSuggestions are the kinds a typed domain could be added as: a
// regex, or a full match and a suffix, and always a keyword.
func domainSuggestions(draft string) []kit.TokenSuggestion {
	if draft == "" {
		return nil
	}
	var out []kit.TokenSuggestion
	parsed, problem := model.ParseDomainEntry(draft, "")
	switch {
	case model.IsDomainRegex(draft):
		label := draft
		if problem == "" {
			label = parsed.Value
		}
		out = append(out, kit.TokenSuggestion{ID: "domain_regex", Label: label, Detail: "正则"})
	case problem == "":
		out = append(out, kit.TokenSuggestion{ID: "domain", Label: parsed.Value, Detail: "全匹配"},
			kit.TokenSuggestion{ID: "domain_suffix", Label: parsed.Value, Detail: "后缀 · 包含子域名"})
	}
	return append(out, kit.TokenSuggestion{ID: "domain_keyword", Label: draft, Detail: "关键词 · 包含此文本"})
}

// regexResult tests a domain against a regex input: the message, and
// whether it matches (nil without a result).
func regexResult(expression, domain string) (string, *bool) {
	pattern, _, problem := model.CompileDomainRegex(expression)
	host := model.NormalizeDomainName(domain)
	if problem != "" {
		return problem, nil
	}
	if strings.TrimSpace(domain) != "" && host == "" {
		return "请输入有效的测试域名", nil
	}
	if host == "" {
		return "输入域名即可查看匹配结果", nil
	}
	ok := pattern.MatchString(host)
	if ok {
		return "匹配", &ok
	}
	return "不匹配", &ok
}

// regexTest is the "Test domain" input with its result.
func regexTest(c *ui.Context, key any, expression string, domain *string, focus bool) {
	k := kit.T(c)
	ui.Column(c.Key(key)).Gap(5).Shrink(0).Children(func() {
		ui.Text(c, "测试域名").FontSize(kit.FontCaption).TextColor(k.Label3)
		in := kit.Input(c, "domain", domain, "例如 api.example.com").Label("测试域名")
		if focus {
			in.AutoFocus()
		}
		message, matched := regexResult(expression, *domain)
		ui.Row(c).Gap(5).Children(func() {
			tone := k.Label3
			if matched != nil {
				icon := kit.SymCircleX
				tone = k.Danger
				if *matched {
					icon, tone = kit.SymCircleCheck, k.Success
				}
				kit.Icon(c, icon, 13).TextColor(tone)
			} else if message != "输入域名即可查看匹配结果" {
				tone = k.Danger
			}
			ui.Text(c, message).FontSize(kit.FontCaption).TextColor(tone)
		})
	})
}

// domainField is the Domain TAG input: full match, suffix, keyword and
// regex, with a regex test.
func (w *Workspace) domainField(c *ui.Context, e *ruleEditor, group *model.Condition, path []int) {
	k := kit.T(c)
	s := &e.cond
	key := pathKey(path) + ":domain"
	entries := model.MatchTokens(group, model.TypedDomain)
	excluded, mixed := model.MatchExclusion(group, model.TypedDomain)
	fieldHead(c, "域名", len(entries) > 0, excluded, mixed,
		func() { e.replaceRules(path, model.SetMatchExclusion(group, model.TypedDomain, !excluded)) },
		func() {
			e.replaceRules(path, clearTokens(group, model.TypedDomain))
			delete(s.regex, key)
		})
	query := s.queries[key]
	draft := strings.TrimSpace(query)
	var tokens []kit.Token
	for _, t := range entries {
		kindLabel := model.DomainKindLabel(t.Field)
		title := kindLabel + " · " + fmt.Sprint(t.Value)
		if t.Field == "domain_regex" {
			title += " · 点击测试域名"
		}
		detail := ""
		if t.Excluded {
			detail = "排除"
		}
		tokens = append(tokens, kit.Token{ID: t.ID, Label: fmt.Sprint(t.Value), Badge: kindLabel, Detail: detail,
			Editable: t.Field == "domain_regex", Title: title})
	}
	input := kit.TokenInput(c, "tokens", &query, kit.TokenInputOptions{
		Label: "域名", Placeholder: "例如 example.com，回车添加", Error: s.errors[key], Tokens: tokens,
		Suggestions: domainSuggestions(draft),
		Commit: func(suggestion string) bool {
			entry, problem := model.ParseDomainEntry(draft, suggestion)
			if problem != "" {
				s.errors[key] = problem
				return false
			}
			e.replaceRules(path, model.AddMatchValues(group, model.TypedDomain, []model.MatchValue{{Field: entry.Field, Value: entry.Value}}))
			s.errors[key] = ""
			return true
		},
		Remove: func(id string) {
			for _, t := range entries {
				if t.ID == id {
					e.replaceRules(path, model.RemoveMatchToken(group, t))
				}
			}
		},
		TokenClick: func(id string, _ ui.Element) {
			s.regex[key], s.regexOpen, s.regexText = id, true, ""
		},
	})
	if input.Changed() {
		s.errors[key] = ""
	}
	s.queries[key] = query
	// A regex being typed shows its test below the field.
	if draft != "" && model.IsDomainRegex(draft) {
		ui.Column(c).Padding(8, 10).Radius(9).Background(k.Surface2).Shrink(0).Children(func() {
			text := s.drafts[key+":test"]
			regexTest(c, "typed-test", draft, &text, false)
			s.drafts[key+":test"] = text
		})
	}
	// A regex chip clicked shows its test beside it.
	var tested *model.MatchToken
	for i, t := range entries {
		if t.ID == s.regex[key] && t.Field == "domain_regex" {
			tested = &entries[i]
		}
	}
	if tested == nil || !s.regexOpen {
		return
	}
	ui.PopoverBase(c, input, &s.regexOpen, func(panel ui.Element) {
		kit.Popup(c, panel).Width(320).Padding(12).Label("正则测试")
		kit.AttachPopover(c, panel, input, ui.AnchorBottomStart, ui.AnchorTopStart, 6, 0)
		kit.PopoverBody(c, 8, func() {
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, "正则测试").FontSize(kit.FontBody).FontWeight(600).Grow(1)
				kit.IconButton(c, kit.SymX, "关闭正则测试", kit.IconButtonOptions{Size: 24, IconSize: 13}).OnClick(func() { s.regexOpen = false })
			})
			kit.CodeBlock(c, fmt.Sprint(tested.Value))
			regexTest(c, tested.ID, fmt.Sprint(tested.Value), &s.regexText, true)
		})
	})
}
