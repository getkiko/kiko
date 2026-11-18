package schema

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the compact signal address of Burrow forms and its
// conversion to sing-box options.

// BurrowSignalAddress is a parsed signal address.
type BurrowSignalAddress struct {
	Server   string
	Hostname string
	Port     int
	Path     string // "" when absent
	Scheme   string // "ws", "wss" or ""
	IsDomain bool
}

var (
	hpHexGroup    = regexp.MustCompile(`^[0-9a-fA-F]{1,4}$`)
	hpDomainLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?$`)
	hpScheme      = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*)://`)
	hpBracketed   = regexp.MustCompile(`^\[([^\]]+)]:(\d+)$`)
	hpHostPort    = regexp.MustCompile(`^([^:]+):(\d+)$`)
	hpDottedQuad  = regexp.MustCompile(`^\d+(?:\.\d+){3}$`)
	hpWSScheme    = regexp.MustCompile(`(?i)^(wss?)://`)
	hpSpace       = regexp.MustCompile(`\s`)
	hpTrailing    = regexp.MustCompile(`/+$`)
)

func hpIPv4(value string) bool { return IsIPv4(value) }

func hpIPv6(value string) bool {
	if !strings.Contains(value, ":") || strings.Count(value, "::") > 1 {
		return false
	}
	head, tail, hasTail := strings.Cut(value, "::")
	var groups []string
	if head != "" {
		groups = strings.Split(head, ":")
	}
	if hasTail && tail != "" {
		groups = append(groups, strings.Split(tail, ":")...)
	}
	count := len(groups)
	for _, g := range groups {
		if strings.Contains(g, ".") {
			// The IPv4 tail ends the address: not before "::".
			if g != groups[len(groups)-1] || strings.HasSuffix(value, ":") || !hpIPv4(g) {
				return false
			}
			count++
		} else if !hpHexGroup.MatchString(g) {
			return false
		}
	}
	if !hasTail {
		return count == 8
	}
	return count < 8
}

// IsIPAddress reports whether value is an IPv4 or IPv6 address.
func IsIPAddress(value string) bool { return hpIPv4(value) || hpIPv6(value) }

func hpDomainName(value string) bool {
	if len(value) > 253 || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || !hpDomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func hpHostForAddress(hostname string) string {
	if hpIPv6(hostname) {
		return "[" + hostname + "]"
	}
	return hostname
}

func hpNormalizePath(path string) string {
	value := hpTrailing.ReplaceAllString(strings.TrimSpace(path), "")
	if value == "" || value == "/" {
		return ""
	}
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

// ParseBurrowSignalAddress parses the compact signal address used by
// both Burrow forms; the error is the message to show.
func ParseBurrowSignalAddress(raw string) (BurrowSignalAddress, string) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return BurrowSignalAddress{}, "请填写信令地址"
	}
	if hpSpace.MatchString(input) {
		return BurrowSignalAddress{}, "信令地址不能包含空格"
	}
	scheme := ""
	rest := input
	if m := hpScheme.FindStringSubmatch(input); m != nil {
		scheme = strings.ToLower(m[1])
		if scheme != "ws" && scheme != "wss" {
			return BurrowSignalAddress{}, "协议仅支持 ws:// 或 wss://"
		}
		rest = input[len(m[0]):]
	}
	if strings.ContainsAny(rest, "?#") {
		return BurrowSignalAddress{}, "信令地址不能包含查询参数或片段"
	}
	authority, path := rest, ""
	if slash := strings.Index(rest, "/"); slash >= 0 {
		authority = rest[:slash]
		path = hpNormalizePath(rest[slash:])
	}
	if authority == "" || strings.Contains(authority, "@") {
		return BurrowSignalAddress{}, "信令服务器格式错误"
	}
	var hostname, portText string
	if strings.HasPrefix(authority, "[") {
		m := hpBracketed.FindStringSubmatch(authority)
		if m == nil {
			return BurrowSignalAddress{}, "IPv6 格式应为 [地址]:端口"
		}
		hostname, portText = m[1], m[2]
		if !hpIPv6(hostname) {
			return BurrowSignalAddress{}, "IPv6 地址格式错误"
		}
	} else {
		m := hpHostPort.FindStringSubmatch(authority)
		if m == nil {
			if strings.Contains(authority, ":") {
				return BurrowSignalAddress{}, "IPv6 地址需要使用方括号，例如 [2001:db8::1]:9960/path"
			}
			return BurrowSignalAddress{}, "信令地址格式应为 服务器:端口/路径"
		}
		hostname, portText = m[1], m[2]
	}
	port := jsv.StringToNumber(portText)
	if !jsv.IsInteger(port) || port < 1 || port > 65535 {
		return BurrowSignalAddress{}, "信令端口范围为 1-65535"
	}
	isIP := IsIPAddress(hostname)
	if !isIP && hpDottedQuad.MatchString(hostname) {
		return BurrowSignalAddress{}, "IPv4 地址格式错误"
	}
	if !isIP && !hpDomainName(hostname) {
		return BurrowSignalAddress{}, "信令服务器域名格式错误"
	}
	server := hostname
	if scheme != "" {
		server = scheme + "://" + hpHostForAddress(hostname)
	}
	return BurrowSignalAddress{Server: server, Hostname: hostname, Port: int(port), Path: path, Scheme: scheme, IsDomain: !isIP}, ""
}

// ValidateBurrowSignalAddress is the signal address field's validator.
func ValidateBurrowSignalAddress(value string, _ map[string]any) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	_, err := ParseBurrowSignalAddress(value)
	return err
}

// ValidateSignalServerIP is the signal server IP field's validator.
func ValidateSignalServerIP(value string, _ map[string]any) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if IsIPAddress(strings.TrimSpace(value)) {
		return ""
	}
	return "请输入正确的 IPv4 或 IPv6 地址"
}

// IsBurrowDomainAddress reports a signal address naming a domain.
func IsBurrowDomainAddress(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	parsed, err := ParseBurrowSignalAddress(s)
	return err == "" && parsed.IsDomain
}

// FormatBurrowSignalAddress builds the compact form value from the
// legacy server, port and path fields.
func FormatBurrowSignalAddress(rawServer, rawPort, rawPath any) string {
	server := jsv.Text(rawServer)
	if server == "" {
		return ""
	}
	if value, err := ParseBurrowSignalAddress(server); err == "" {
		prefix := ""
		if value.Scheme != "" {
			prefix = value.Scheme + "://"
		}
		path := value.Path
		if path == "" {
			path = hpNormalizePath(jsv.StringOr(rawPath, ""))
		}
		return prefix + hpHostForAddress(value.Hostname) + ":" + strconv.Itoa(value.Port) + path
	}
	scheme, hostname, embeddedPath, hasEmbedded := "", server, "", false
	if m := hpWSScheme.FindStringSubmatch(server); m != nil {
		scheme = strings.ToLower(m[1]) + "://"
		rest := server[len(m[0]):]
		hostname = rest
		if slash := strings.Index(rest, "/"); slash >= 0 {
			hostname = rest[:slash]
			embeddedPath = hpNormalizePath(rest[slash:])
			hasEmbedded = embeddedPath != ""
		}
	}
	hostname = strings.TrimSuffix(strings.TrimPrefix(hostname, "["), "]")
	port := jsv.NumberOrNaN(rawPort)
	portPart := ""
	if jsv.IsInteger(port) && port > 0 && port <= 65535 {
		portPart = ":" + jsv.FormatNumber(port)
	}
	path := embeddedPath
	if !hasEmbedded {
		path = hpNormalizePath(jsv.StringOr(rawPath, ""))
	}
	return scheme + hpHostForAddress(hostname) + portPart + path
}

// BurrowConfigFromFields converts form fields to the native sing-box
// Burrow options, as an ordered list of key/value pairs.
func BurrowConfigFromFields(fields map[string]any) [][2]any {
	config := [][2]any{{"mode", "header"}}
	if parsed, err := ParseBurrowSignalAddress(jsv.StringOr(fields["signal_address"], "")); err == "" {
		config = append(config, [2]any{"server", parsed.Server}, [2]any{"server_port", float64(parsed.Port)})
		if parsed.Path != "" {
			config = append(config, [2]any{"signal_path", parsed.Path})
		}
		ip := jsv.Text(fields["signal_server_ip"])
		if parsed.IsDomain && IsIPAddress(ip) {
			config = append(config, [2]any{"hosts", map[string]any{parsed.Hostname: ip}})
		}
	}
	parts := strings.Split(jsv.StringOr(fields["network"], ""), "+")
	network, hasFamily := parts[0], len(parts) > 1
	family := ""
	if hasFamily {
		family = parts[1]
	}
	if network != "" {
		config = append(config, [2]any{"network", []any{network}})
	}
	var addressFamily any = family
	if !hasFamily {
		addressFamily = fields["address_family"]
	}
	if addressFamily == "ipv4" || addressFamily == "ipv6" {
		config = append(config, [2]any{"address_family", addressFamily})
	}
	return config
}
