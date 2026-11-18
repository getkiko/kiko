package model

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the DNS server helpers: server types, server addresses
// and the default DNS servers.

// DNS server type groups.
var (
	DialerDNSTypes = []string{"local", "udp", "tcp", "tls", "https", "quic", "h3", "dhcp"}
	TLSDNSTypes    = []string{"tls", "https", "quic", "h3"}
	HTTPDNSTypes   = []string{"https", "h3"}
	RemoteDNSTypes = []string{"udp", "tcp", "tls", "https", "quic", "h3"}
)

// IsDialerDNS reports a DNS type that dials (has dialer options).
func IsDialerDNS(typ string) bool { return slices.Contains(DialerDNSTypes, typ) }

// IsTLSDNS reports a DNS type over TLS.
func IsTLSDNS(typ string) bool { return slices.Contains(TLSDNSTypes, typ) }

// IsHTTPDNS reports a DNS type over HTTP.
func IsHTTPDNS(typ string) bool { return slices.Contains(HTTPDNSTypes, typ) }

// IsRemoteDNS reports a DNS type with a server address.
func IsRemoteDNS(typ string) bool { return slices.Contains(RemoteDNSTypes, typ) }

// FakeIPDefaultRanges are a new FakeIP server's ranges.
var FakeIPDefaultRanges = map[string]string{"inet4_range": "198.18.0.0/15", "inet6_range": "fc00::/18"}

var dnsProtocolKeys = map[string][]string{
	"local": {"prefer_go", "neighbor_domain"}, "udp": {}, "tcp": {},
	"tls": {"tls"}, "quic": {"tls"},
	"https": {"tls", "headers", "method"}, "h3": {"tls", "headers", "method"},
	"hosts": {"path", "predefined"}, "fakeip": {"inet4_range", "inet6_range"},
	"dhcp":      {"prefer_go", "neighbor_domain", "interface"},
	"tailscale": {"endpoint", "accept_default_resolvers", "accept_search_domain"},
}

func dnsDialerKeys() []string {
	keys := make([]string, 0, len(schema.DNSDialerSchema)+3)
	for _, f := range schema.DNSDialerSchema {
		keys = append(keys, f.Key)
	}
	return append(keys, "domain_resolver_disable_cache", "domain_resolver_rewrite_ttl", "domain_resolver_client_subnet")
}

// DNSServerForType applies a protocol's capabilities to a server: fields,
// shared configs and properties the type does not have are dropped.
func DNSServerForType(server *DNSServer, typ string) *DNSServer {
	dialer, tls := IsDialerDNS(typ), IsTLSDNS(typ)
	allowed := slices.Clone(dnsProtocolKeys[typ])
	if dialer {
		allowed = append(allowed, dnsDialerKeys()...)
	}
	fields := Fields{}
	for k, v := range server.Fields {
		if slices.Contains(allowed, k) {
			fields[k] = v
		}
	}
	if typ == "fakeip" {
		for _, key := range []string{"inet4_range", "inet6_range"} {
			if s, ok := fields[key].(string); ok {
				fields[key] = strings.TrimSpace(s)
			}
		}
	}
	refs := NetworkConfigRefs{}
	for slot, id := range server.NetworkConfigRefs {
		if slot == "default" && dialer || slot == "tls" && tls {
			refs[slot] = id
		}
	}
	remote := !slices.Contains([]string{"local", "dhcp", "hosts", "fakeip", "tailscale"}, typ)
	next := *server
	next.Type, next.Fields, next.NetworkConfigRefs = typ, fields, refs
	if !remote {
		next.Server, next.ServerPort = "", nil
	}
	next.Path = ""
	if IsHTTPDNS(typ) {
		next.Path = server.Path
		if next.Path == "" {
			if p, ok := server.Fields["path"].(string); ok {
				next.Path = p
			}
		}
	}
	if !dialer {
		next.Detour, next.DomainResolver, next.DomainResolverDetour, next.DomainStrategy = Ref{}, Ref{}, Ref{}, ""
	}
	return &next
}

// WithFakeIPDefaults fills a FakeIP server's empty ranges with defaults.
func WithFakeIPDefaults(server *DNSServer) *DNSServer {
	if server.Type != "fakeip" {
		return server
	}
	if jsv.Text(server.Fields["inet4_range"]) != "" || jsv.Text(server.Fields["inet6_range"]) != "" {
		return server
	}
	next := *server
	next.Fields = cloneFields(server.Fields)
	for k, v := range FakeIPDefaultRanges {
		next.Fields[k] = v
	}
	return &next
}

// FakeIPRangeError returns why a FakeIP server's ranges are invalid.
func FakeIPRangeError(server *DNSServer) string {
	if server.Type != "fakeip" {
		return ""
	}
	ipv4 := jsv.Text(server.Fields["inet4_range"])
	ipv6 := jsv.Text(server.Fields["inet6_range"])
	switch {
	case ipv4 == "" && ipv6 == "":
		return "请至少填写一个 FakeIP 网段（IPv4 或 IPv6）"
	case ipv4 != "" && (!schema.IsCIDR(ipv4) || strings.Contains(ipv4, ":")):
		return "FakeIP IPv4 网段请填写有效的 IPv4 CIDR"
	case ipv6 != "" && (!schema.IsCIDR(ipv6) || !strings.Contains(ipv6, ":")):
		return "FakeIP IPv6 网段请填写有效的 IPv6 CIDR"
	}
	return ""
}

// FakeIPServerError returns why the servers' FakeIP use is invalid: more
// than one, or FakeIP as the fallback.
func FakeIPServerError(servers []*DNSServer, final Ref) string {
	var enabled []*DNSServer
	fakes := 0
	for _, s := range servers {
		if s.Enabled {
			enabled = append(enabled, s)
			if s.Type == "fakeip" {
				fakes++
			}
		}
	}
	if fakes > 1 {
		return "仅支持启用一个 FakeIP 服务器"
	}
	var fallback *DNSServer
	for _, s := range enabled {
		if final.Valid() && s.ID == final.ID() {
			fallback = s
			break
		}
	}
	if fallback == nil && len(enabled) > 0 {
		fallback = enabled[0]
	}
	if fallback != nil && fallback.Type == "fakeip" {
		return "FakeIP 用于 DNS 分流规则；请在路由中选择其他服务器作为兜底 DNS"
	}
	return ""
}

// ---- addresses ----

var dnsSchemes = map[string]string{"udp": "udp", "tcp": "tcp", "tls": "tls", "dot": "tls", "https": "https", "doh": "https", "quic": "quic", "doq": "quic", "h3": "h3", "http3": "h3"}

// DNSAddressPlaceholder is the address input's example for a type.
func DNSAddressPlaceholder(typ string) string {
	return map[string]string{
		"udp": "udp://223.5.5.5:53", "tcp": "tcp://223.5.5.5:53", "tls": "tls://dns.alidns.com:853",
		"https": "https://dns.alidns.com/dns-query", "quic": "quic://dns.adguard-dns.com:853", "h3": "h3://dns.alidns.com/dns-query",
	}[typ]
}

// ParsedDNSAddress is a server address split into its parts.
type ParsedDNSAddress struct {
	Type       string `json:"type"`
	Server     string `json:"server"`
	ServerPort *int   `json:"serverPort,omitempty"`
	Path       string `json:"path,omitempty"`
}

var (
	reSpace       = regexp.MustCompile(`\s`)
	reURLScheme   = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*)://`)
	reBracketHost = regexp.MustCompile(`^\[([^\]]+)\](?::([^:]*))?$`)
	reDNSHost     = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.?$`)
)

// ParseDNSAddress parses a server address such as https://host/dns-query
// for the selected type; the error is the message to show.
func ParseDNSAddress(input, selectedType string) (ParsedDNSAddress, string) {
	raw := strings.TrimSpace(input)
	switch {
	case raw == "":
		return ParsedDNSAddress{}, "请填写服务器地址"
	case reSpace.MatchString(raw):
		return ParsedDNSAddress{}, "地址中的空格请使用 URL 编码"
	case strings.Contains(raw, "#"):
		return ParsedDNSAddress{}, "DNS 地址中请移除 # 片段"
	}
	typ, rest := "", raw
	if m := reURLScheme.FindStringSubmatch(raw); m != nil {
		typ = dnsSchemes[strings.ToLower(m[1])]
		rest = raw[len(m[0]):]
	} else if IsRemoteDNS(selectedType) {
		typ = selectedType
	}
	if typ == "" {
		return ParsedDNSAddress{}, "支持 udp://、tcp://、tls://、https://、quic:// 和 h3://"
	}
	authority, suffix := rest, ""
	if split := strings.IndexAny(rest, "/?"); split >= 0 {
		authority, suffix = rest[:split], rest[split:]
	}
	if authority == "" || strings.Contains(authority, "@") {
		return ParsedDNSAddress{}, "请填写有效域名或 IP；认证信息可填写在 HTTP 请求头中"
	}
	host, port, hasPort := authority, "", false
	if strings.HasPrefix(authority, "[") {
		m := reBracketHost.FindStringSubmatch(authority)
		if m == nil {
			return ParsedDNSAddress{}, "IPv6 地址请使用 [地址]:端口 格式"
		}
		host = m[1]
		port, hasPort = m[2], strings.Contains(authority, "]:")
	} else if strings.Count(authority, ":") == 1 {
		host, port, _ = strings.Cut(authority, ":")
		hasPort = true
	}
	if hasPort {
		n := jsv.StringToNumber(port)
		if !isDigits(port) || n < 1 || n > 65535 {
			return ParsedDNSAddress{}, "端口范围为 1–65535"
		}
	}
	bracket := host
	if strings.Contains(host, ":") {
		bracket = "[" + host + "]"
	}
	invalid := "请填写有效域名或 IP；IPv6 的端口写作 [地址]:端口"
	if host == "" || strings.ContainsAny(host, `%\`) {
		return ParsedDNSAddress{}, invalid
	}
	authorityText := bracket
	if hasPort {
		authorityText += ":" + port
	}
	parsed, ok := parseWHATWGAuthority(authorityText)
	if !ok || parsed.Hostname == "" || strings.ContainsAny(bracket, "/?#") {
		return ParsedDNSAddress{}, invalid
	}
	host = strings.TrimSuffix(strings.TrimPrefix(parsed.Hostname, "["), "]")
	if !strings.Contains(host, ":") && !reDNSHost.MatchString(host) {
		return ParsedDNSAddress{}, invalid
	}
	value := ParsedDNSAddress{Type: typ, Server: host}
	if hasPort {
		n, _ := strconv.Atoi(port)
		value.ServerPort = &n
	}
	if typ == "https" || typ == "h3" {
		pathInput := suffix
		switch {
		case suffix == "":
			pathInput = "/dns-query"
		case strings.HasPrefix(suffix, "?"):
			pathInput = "/dns-query" + suffix
		}
		value.Path = normalizeURLPath(pathInput)
	} else if suffix != "" && suffix != "/" {
		return ParsedDNSAddress{}, "当前协议使用主机和端口，请移除路径与查询参数"
	}
	return value, ""
}

// normalizeURLPath returns pathname+search of https://dns.invalid+path, as
// new URL normalizes them.
func normalizeURLPath(path string) string {
	base, _ := url.Parse("https://dns.invalid/")
	ref, err := url.Parse(path)
	if err != nil {
		return path
	}
	u := base.ResolveReference(ref)
	out := u.EscapedPath()
	if out == "" {
		out = "/"
	}
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// FormatDNSAddress writes a remote server's address as one URL.
func FormatDNSAddress(server *DNSServer) string {
	if !IsRemoteDNS(server.Type) || server.Server == "" {
		return ""
	}
	if strings.Contains(server.Server, "://") {
		return server.Server
	}
	host := server.Server
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	out := server.Type + "://" + host
	if server.ServerPort != nil {
		out += ":" + strconv.Itoa(*server.ServerPort)
	}
	if server.Type == "https" || server.Type == "h3" {
		if server.Path != "" {
			out += server.Path
		} else {
			out += "/dns-query"
		}
	}
	return out
}

// DNSAddressPatch applies a parsed address to a server, keeping its
// references and settings while changing the protocol.
func DNSAddressPatch(server *DNSServer, address ParsedDNSAddress) *DNSServer {
	next := DNSServerForType(server, address.Type)
	if IsTLSDNS(address.Type) && !jsv.Truthy(next.Fields["tls"]) {
		next.Fields["tls"] = map[string]any{}
	}
	patched := *server
	patched.Type, patched.Server, patched.ServerPort, patched.Path = address.Type, address.Server, address.ServerPort, address.Path
	patched.Fields, patched.NetworkConfigRefs = next.Fields, next.NetworkConfigRefs
	return &patched
}

// DefaultDNSServers are the DNS servers of a new document: the system
// resolver and AliDNS over HTTPS.
func DefaultDNSServers(localID, aliID string) (local, ali *DNSServer) {
	local = &DNSServer{ID: localID, Tag: "dns-local", Enabled: true, Type: "local"}
	ali = &DNSServer{
		ID: aliID, Tag: "dns-cn", Enabled: true, Type: "https", Server: "223.5.5.5", Path: "/dns-query",
		Fields: Fields{"tls": map[string]any{"server_name": "dns.alidns.com"}},
	}
	return local, ali
}
