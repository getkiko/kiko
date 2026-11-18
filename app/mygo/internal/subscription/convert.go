package subscription

import (
	"encoding/json"
	"errors"
	"strings"
)

// Conversion is a subscription converted to sing-box: the configuration
// with its outbounds, how many nodes the body had, how many became
// outbounds, and how many were skipped.
type Conversion struct {
	Config    string
	Parsed    int
	Converted int
	Skipped   int
}

// Convert turns a subscription body into a sing-box configuration of
// outbounds. A sing-box configuration stays as it is; a Clash YAML
// configuration's proxies and share links (plain or Base64) are
// converted. Hosts mapped by the body's hosts section are replaced by
// their targets.
func Convert(content string) (Conversion, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Conversion{}, errors.New("订阅正文为空")
	}
	mappings := hostsMapping(content)
	if config, count, ok := singBoxConfig(content, mappings); ok {
		return Conversion{Config: config, Parsed: count, Converted: count}, nil
	}
	proxies := explodeContent(content)
	if len(proxies) == 0 {
		return Conversion{}, errors.New("没有识别到支持的订阅节点")
	}
	outbounds := []any{}
	for _, p := range proxies {
		if p == nil {
			continue
		}
		if o, ok := p.toSingBox(); ok {
			outbounds = append(outbounds, o)
		}
	}
	if len(outbounds) == 0 {
		return Conversion{}, errors.New("订阅节点无法转换为 sing-box 出站")
	}
	uniqueTags(outbounds)
	rewriteHosts(outbounds, mappings)
	raw, err := json.MarshalIndent(map[string]any{"outbounds": outbounds}, "", "  ")
	if err != nil {
		return Conversion{}, err
	}
	return Conversion{Config: string(raw), Parsed: len(proxies), Converted: len(outbounds),
		Skipped: len(proxies) - len(outbounds)}, nil
}

// uniqueTags numbers repeated tags, as the converter's node list does,
// past the tags already taken ("A", "A", "A 2" become "A", "A 2",
// "A 2 2", where counting each tag alone repeated "A 2").
func uniqueTags(outbounds []any) {
	used := map[string]bool{}
	for _, o := range outbounds {
		m := o.(map[string]any)
		base, _ := m["tag"].(string)
		tag := base
		for n := 2; used[tag]; n++ {
			tag = base + " " + itoa(n)
		}
		used[tag] = true
		m["tag"] = tag
	}
}

// explodeContent reads the nodes of a body: a Clash configuration, or
// share links, Base64 as a whole or not. Entries recognized as nodes
// that do not parse are nil, counting as skipped.
func explodeContent(content string) []*proxy {
	if list, ok := clashProxies(content); ok {
		var out []*proxy
		for _, item := range list {
			p, ok := clashProxy(item)
			if !ok {
				continue
			}
			out = append(out, p)
		}
		return out
	}
	if list, ok := sip008Proxies(content); ok {
		return list
	}
	text := content
	if !reHasScheme.MatchString(text) {
		if decoded, err := B64Decode(text); err == nil {
			if _, ok := clashProxies(decoded); ok {
				return explodeContent(decoded)
			}
			if list, ok := sip008Proxies(decoded); ok {
				return list
			}
			text = decoded
		}
	}
	var out []*proxy
	for _, line := range reNewline.Split(text, -1) {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}
		if p, recognized := explodeLink(s); recognized {
			out = append(out, p)
		}
	}
	return out
}

// sip008Proxies reads a SIP008 Shadowsocks configuration, {"servers":
// [{"server", "server_port", "method", "password", "remarks", "plugin",
// "plugin_opts"}]}, which libsubconverter reads among subscription
// bodies; ok is false when content is none.
func sip008Proxies(content string) ([]*proxy, bool) {
	var value map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &value) != nil {
		return nil, false
	}
	servers, ok := value["servers"].([]any)
	if !ok {
		return nil, false
	}
	var out []*proxy
	for _, item := range servers {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := clashMap(m)
		p := &proxy{Type: "ss", Remark: c.str("remarks"), Host: trimBrackets(c.str("server")), Port: c.num("server_port"),
			Method: c.str("method"), Password: c.str("password"), Plugin: c.str("plugin"), PluginOpts: c.str("plugin_opts")}
		if p.Plugin == "simple-obfs" {
			p.Plugin = "obfs-local"
		}
		out = append(out, p)
	}
	return out, true
}

// singBoxConfig returns content when it is a sing-box configuration with
// at least one typed outbound, with the hosts mappings applied, and how
// many outbounds it has.
func singBoxConfig(content string, mappings []hostMapping) (string, int, bool) {
	var value map[string]any
	if json.Unmarshal([]byte(content), &value) != nil {
		return "", 0, false
	}
	outbounds, ok := value["outbounds"].([]any)
	if !ok {
		return "", 0, false
	}
	typed := false
	for _, o := range outbounds {
		if m, ok := o.(map[string]any); ok {
			if _, ok := m["type"].(string); ok {
				typed = true
				break
			}
		}
	}
	if !typed {
		return "", 0, false
	}
	rewriteHosts(outbounds, mappings)
	if endpoints, ok := value["endpoints"].([]any); ok {
		rewriteHosts(endpoints, mappings)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", 0, false
	}
	return string(raw), len(outbounds), true
}

type hostMapping struct{ source, target string }

func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}

// hostsMapping reads the hosts of a body: a JSON hosts object, or the
// entries of a top-level YAML hosts: block.
func hostsMapping(content string) []hostMapping {
	var mappings []hostMapping
	var value map[string]any
	if err := json.Unmarshal([]byte(content), &value); err == nil {
		if hosts, ok := value["hosts"].(map[string]any); ok {
			for source, target := range hosts {
				switch t := target.(type) {
				case string:
					mappings = append(mappings, hostMapping{source, t})
				case []any:
					if len(t) > 0 {
						if s, ok := t[0].(string); ok {
							mappings = append(mappings, hostMapping{source, s})
						}
					}
				}
			}
		}
		return mappings
	}
	hostsIndent := -1
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if hostsIndent < 0 {
			if trimmed == "hosts:" {
				hostsIndent = indent
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indent <= hostsIndent {
			break
		}
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = unquote(strings.TrimSpace(key))
		val, _, _ = strings.Cut(val, "#")
		val = unquote(strings.TrimSpace(val))
		val = strings.Trim(val, "[]")
		val, _, _ = strings.Cut(val, ",")
		val = unquote(strings.TrimSpace(val))
		if key != "" && val != "" {
			mappings = append(mappings, hostMapping{key, val})
		}
	}
	return mappings
}

// rewriteHosts replaces the server addresses of outbounds (and of their
// WireGuard peers) that a hosts mapping names. lib.rs replaced every
// case-insensitive occurrence of a mapped name anywhere in the JSON
// text, which also rewrote TLS server names, Host headers, tags,
// passwords and longer names containing it ("a.example" in
// "cdn.a.example"), and could break the JSON with a quote in a target.
func rewriteHosts(entries []any, mappings []hostMapping) {
	if len(mappings) == 0 {
		return
	}
	rewrite := func(m map[string]any) {
		if server, ok := m["server"].(string); ok {
			if target, ok := mappedHost(server, mappings); ok {
				m["server"] = target
			}
		}
	}
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rewrite(m)
		if peers, ok := m["peers"].([]any); ok {
			for _, peer := range peers {
				if pm, ok := peer.(map[string]any); ok {
					rewrite(pm)
				}
			}
		}
	}
}

// mappedHost is the target of host: an exact mapping first, ignoring
// case, then Clash's wildcards ("+.example.com" for the domain and its
// subdomains, "*.example.com" for one level of subdomain, ".example.com"
// for any subdomain).
func mappedHost(host string, mappings []hostMapping) (string, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, m := range mappings {
		if strings.EqualFold(m.source, host) {
			return m.target, true
		}
	}
	for _, m := range mappings {
		source := strings.ToLower(m.source)
		switch {
		case strings.HasPrefix(source, "+."):
			if d := source[2:]; host == d || strings.HasSuffix(host, "."+d) {
				return m.target, true
			}
		case strings.HasPrefix(source, "*."):
			if d := source[1:]; strings.HasSuffix(host, d) && !strings.Contains(host[:len(host)-len(d)], ".") && len(host) > len(d) {
				return m.target, true
			}
		case strings.HasPrefix(source, "."):
			if strings.HasSuffix(host, source) && len(host) > len(source) {
				return m.target, true
			}
		}
	}
	return "", false
}
