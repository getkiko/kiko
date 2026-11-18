package settings

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// This file holds the system proxy's logic: the bypass list, the proxy
// endpoint of the first mixed inbound, and the shell commands that set it.

// proxyShell is a shell a proxy command is copied for.
type proxyShell struct {
	Value, Label string
}

var proxyShells = []proxyShell{
	{"bash", "Bash"}, {"fish", "Fish"}, {"cmd", "CMD"}, {"powershell", "PowerShell"},
}

// defaultProxyBypass is what the system proxy always bypasses unless
// turned off.
var defaultProxyBypass = []string{
	"127.0.0.1", "192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "localhost", "*.local", "*.crashlytics.com", "<local>",
}

// effectiveBypass merges the default and custom bypass without
// duplicates, in order.
func effectiveBypass(defaultBypass bool, custom []string) []string {
	var all []string
	if defaultBypass {
		all = append(all, defaultProxyBypass...)
	}
	all = append(all, custom...)
	seen := map[string]bool{}
	out := []string{}
	for _, entry := range all {
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out
}

// splitBypass splits the custom bypass text at newlines, commas and
// semicolons.
func splitBypass(text string) []string {
	out := []string{}
	for _, entry := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// proxyEndpoint is where the proxy listens and its credentials.
type proxyEndpoint struct {
	Host, Port         string
	Username, Password string
	HasAuth            bool
}

// firstMixedInbound is the first enabled mixed inbound.
func firstMixedInbound(doc *model.Doc) *model.Inbound {
	for _, in := range doc.Inbounds {
		if in.Enabled && in.Type == "mixed" {
			return in
		}
	}
	return nil
}

// hasEnabledTun reports an enabled TUN inbound.
func hasEnabledTun(doc *model.Doc) bool {
	for _, in := range doc.Inbounds {
		if in.Enabled && in.Type == "tun" {
			return true
		}
	}
	return false
}

func connectHost(listen any) string {
	value, _ := listen.(string)
	value = strings.TrimSpace(value)
	if value == "" || value == "0.0.0.0" || value == "::" {
		return "127.0.0.1"
	}
	return value
}

// portOf reads a port field stored as a number or a numeric string.
func portOf(v any) (int, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int:
		f = float64(x)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil || strings.TrimSpace(x) == "" {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f <= 0 || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// proxyEndpointOf is the first mixed inbound's address; hostOverride,
// when set, replaces its host. ok is false without a usable inbound.
func proxyEndpointOf(doc *model.Doc, hostOverride string) (proxyEndpoint, bool) {
	in := firstMixedInbound(doc)
	if in == nil {
		return proxyEndpoint{}, false
	}
	port, ok := portOf(in.Fields["listen_port"])
	if !ok {
		return proxyEndpoint{}, false
	}
	e := proxyEndpoint{Host: strings.TrimSpace(hostOverride), Port: strconv.Itoa(port)}
	if e.Host == "" {
		e.Host = connectHost(in.Fields["listen"])
	}
	if auth, _ := in.Fields["auth_enabled"].(bool); auth {
		if username, _ := in.Fields["username"].(string); username != "" {
			e.HasAuth, e.Username = true, username
			if password := in.Fields["password"]; password != nil {
				e.Password = fmt.Sprint(password)
			}
		}
	}
	return e, true
}

// formatHostPort joins host and port, bracketing IPv6 hosts.
func formatHostPort(e proxyEndpoint) string {
	host := e.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + e.Port
}

// encodeCredential escapes as encodeURIComponent, also escaping !'()*.
func encodeCredential(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	for _, r := range []string{"!", "'", "(", ")", "*"} {
		escaped = strings.ReplaceAll(escaped, r, fmt.Sprintf("%%%02X", r[0]))
	}
	// QueryEscape escapes ~ which encodeURIComponent keeps.
	return strings.ReplaceAll(escaped, "%7E", "~")
}

func proxyURL(scheme string, e proxyEndpoint) string {
	auth := ""
	if e.HasAuth {
		auth = encodeCredential(e.Username) + ":" + encodeCredential(e.Password) + "@"
	}
	return scheme + "://" + auth + formatHostPort(e)
}

// shellCommand sets the proxy variables in a shell.
func shellCommand(shell string, e proxyEndpoint) string {
	http := proxyURL("http", e)
	socks := proxyURL("socks5", e)
	switch shell {
	case "fish":
		return fmt.Sprintf("set -gx http_proxy %s; set -gx https_proxy %s; set -gx all_proxy %s", http, http, socks)
	case "cmd":
		return fmt.Sprintf("set http_proxy=%s&& set https_proxy=%s&& set all_proxy=%s", http, http, socks)
	case "powershell":
		return fmt.Sprintf(`$env:http_proxy="%s"; $env:https_proxy="%s"; $env:all_proxy="%s"`, http, http, socks)
	}
	return fmt.Sprintf("export http_proxy=%s https_proxy=%s all_proxy=%s", http, http, socks)
}
