package model

import (
	"net/url"
	"regexp"
	"strings"
)

// This file holds the parsing of pasted proxy addresses.

type proxyScheme struct {
	port    int
	version string
}

var proxySchemes = map[string]proxyScheme{
	"http": {port: 80}, "https": {port: 443},
	"socks": {1080, "5"}, "socks5": {1080, "5"}, "socks5h": {1080, "5"}, "socks4": {1080, "4"}, "socks4a": {1080, "4a"},
}

var (
	reProxyInvalid  = regexp.MustCompile(`[\s\\]`)
	reProxyBracket  = regexp.MustCompile(`^\[[^\]]+\](?::(\d+))?$`)
	reProxyHostPort = regexp.MustCompile(`^[^:]+(?::(\d+))?$`)
)

func decodeCredential(value string) string {
	if decoded, err := url.PathUnescape(value); err == nil {
		return decoded
	}
	return value
}

// ProxyAddressPatch splits a pasted proxy address such as
// socks5://user:pass@host:1080 into an HTTP or SOCKS outbound's fields,
// keeping its other settings. ok is false when input is not an address.
func ProxyAddressPatch(outbound *Outbound, input string) (Fields, bool) {
	if outbound.Type != "http" && outbound.Type != "socks" {
		return nil, false
	}
	text := strings.TrimSpace(input)
	if text == "" || reProxyInvalid.MatchString(text) {
		return nil, false
	}
	scheme, rest := "", text
	if m := reURLScheme.FindStringSubmatch(text); m != nil {
		scheme = strings.ToLower(m[1])
		if _, ok := proxySchemes[scheme]; !ok {
			return nil, false
		}
		rest = text[len(m[0]):]
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	hostPort := authority[strings.LastIndex(authority, "@")+1:]
	re := reProxyHostPort
	if strings.HasPrefix(hostPort, "[") {
		re = reProxyBracket
	}
	parts := re.FindStringSubmatch(hostPort)
	if parts == nil || parts[1] == "" && scheme == "" {
		return nil, false
	}
	port := proxySchemes[scheme].port
	if parts[1] != "" {
		port = atoiOr(parts[1], -1)
	}
	if port < 1 || port > 65535 {
		return nil, false
	}
	// new URL("http://" + rest) must have the root path and no query; a
	// fragment does not matter.
	suffix, _, _ := strings.Cut(rest[len(authority):], "#")
	if suffix != "" && suffix != "/" && suffix != "?" && suffix != "/?" {
		return nil, false
	}
	parsed, ok := parseWHATWGAuthority(authority)
	if !ok || parsed.Hostname == "" {
		return nil, false
	}
	fields := cloneFields(outbound.Fields)
	fields["server"] = strings.TrimSuffix(strings.TrimPrefix(parsed.Hostname, "["), "]")
	fields["server_port"] = float64(port)
	if scheme != "" || strings.Contains(authority, "@") {
		fields["username"] = decodeCredential(parsed.Username)
		fields["password"] = decodeCredential(parsed.Password)
	}
	if outbound.Type == "http" && (scheme == "http" || scheme == "https") {
		fields["tls.enabled"] = scheme == "https"
	}
	if outbound.Type == "socks" && scheme != "" && proxySchemes[scheme].version != "" {
		fields["version"] = proxySchemes[scheme].version
	}
	return fields, true
}

func atoiOr(s string, fallback int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return fallback
		}
	}
	return n
}
