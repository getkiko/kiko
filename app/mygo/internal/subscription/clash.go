package subscription

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// clashValue reads a YAML value as Go's JSON-like values do.
type clashMap map[string]any

func (m clashMap) str(keys ...string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			return v
		case yamlPlain:
			if v == "~" || v == "null" {
				return ""
			}
			return string(v)
		case float64:
			return jsv.FormatNumber(v)
		case bool:
			return strconv.FormatBool(v)
		}
	}
	return ""
}

func (m clashMap) num(keys ...string) int {
	s := m.str(keys...)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return mbps(s)
}

func (m clashMap) bool(keys ...string) bool {
	switch strings.ToLower(m.str(keys...)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

func (m clashMap) sub(key string) clashMap {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return clashMap{}
}

func (m clashMap) list(key string) []string {
	switch v := m[key].(type) {
	case []any:
		var out []string
		for _, item := range v {
			if s := (clashMap{"v": item}).str("v"); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string, yamlPlain:
		var out []string
		for _, part := range strings.Split(m.str(key), ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

// first returns the first of a header or host value that may be a list.
func (m clashMap) first(key string) string {
	if l := m.list(key); len(l) > 0 {
		return l[0]
	}
	return ""
}

// clashProxies reads the proxies of a Clash configuration (proxies, or
// the old Proxy key); ok is false when content is none.
func clashProxies(content string) ([]any, bool) {
	if !reClashProxies.MatchString(content) {
		return nil, false
	}
	doc, err := parseYAML(content)
	if err != nil {
		// Read the proxies block alone when another section does not
		// parse.
		block := reClashBlock.FindString(content)
		if block == "" {
			return nil, false
		}
		if doc, err = parseYAML(block); err != nil {
			return nil, false
		}
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, false
	}
	for _, key := range []string{"proxies", "Proxy"} {
		if list, ok := root[key].([]any); ok {
			return list, true
		}
	}
	return nil, false
}

var (
	reClashProxies = regexp.MustCompile(`(?m)^(proxies|Proxy):`)
	reClashBlock   = regexp.MustCompile(`(?ms)^(?:proxies|Proxy):.*?(?:\n[^\s#\-][^\n]*:|\z)`)
)

// clashProxy reads a Clash proxy entry.
func clashProxy(v any) (*proxy, bool) {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	m := clashMap(raw)
	p := &proxy{Remark: m.str("name"), Host: m.str("server"), Port: m.num("port"), TFO: m.bool("tfo"),
		Insecure: m.bool("skip-cert-verify"), Fingerprint: m.str("client-fingerprint"), ALPN: m.list("alpn")}
	p.Host = trimBrackets(p.Host)
	typ := strings.ToLower(m.str("type"))
	switch typ {
	case "ss":
		p.Type, p.Method, p.Password = "ss", m.str("cipher"), m.str("password")
		clashSSPlugin(p, m)
	case "ssr":
		p.Type, p.Method, p.Password = "ssr", m.str("cipher"), m.str("password")
		p.Protocol, p.ProtocolParam = m.str("protocol"), m.str("protocol-param", "protocolparam")
		p.Obfs, p.ObfsParam = m.str("obfs"), m.str("obfs-param", "obfsparam")
	case "vmess":
		p.Type, p.UUID, p.AlterID, p.Method = "vmess", m.str("uuid"), m.num("alterId", "alter-id"), m.str("cipher")
		p.TLS, p.SNI = m.bool("tls"), m.str("servername", "sni")
		clashTransport(p, m)
	case "vless":
		p.Type, p.UUID, p.Flow = "vless", m.str("uuid"), m.str("flow")
		p.TLS, p.SNI = m.bool("tls"), m.str("servername", "sni")
		reality := m.sub("reality-opts")
		p.RealityKey, p.RealitySID = reality.str("public-key"), reality.str("short-id")
		if p.RealityKey != "" {
			p.TLS = true
		}
		clashTransport(p, m)
	case "trojan":
		p.Type, p.Password, p.SNI = "trojan", m.str("password"), m.str("sni", "servername")
		p.TLS = true
		reality := m.sub("reality-opts")
		p.RealityKey, p.RealitySID = reality.str("public-key"), reality.str("short-id")
		clashTransport(p, m)
	case "hysteria":
		p.Type, p.SNI = "hysteria", m.str("sni", "servername")
		p.AuthStr = m.str("auth-str", "auth_str", "auth")
		p.UpMbps, p.DownMbps = mbps(m.str("up", "up-mbps")), mbps(m.str("down", "down-mbps"))
		p.Obfs, p.HysteriaProtocol = m.str("obfs"), m.str("protocol")
		p.HysteriaRecvWindow, p.HysteriaRecvWinConn = m.num("recv-window"), m.num("recv-window-conn")
		p.ServerPorts = clashPorts(m)
	case "hysteria2", "hy2":
		p.Type, p.Password, p.SNI = "hysteria2", m.str("password", "auth"), m.str("sni", "servername")
		p.UpMbps, p.DownMbps = mbps(m.str("up")), mbps(m.str("down"))
		p.Obfs, p.ObfsParam = m.str("obfs"), m.str("obfs-password")
		p.ServerPorts = clashPorts(m)
	case "tuic":
		p.Type, p.UUID, p.Password, p.SNI = "tuic", m.str("uuid"), m.str("password"), m.str("sni", "servername")
		p.CongestionControl = m.str("congestion-controller", "congestion-control")
		p.UDPRelayMode, p.ZeroRTT, p.DisableSNI = m.str("udp-relay-mode"), m.bool("reduce-rtt"), m.bool("disable-sni")
	case "anytls":
		p.Type, p.Password, p.SNI = "anytls", m.str("password"), m.str("sni", "servername")
	case "socks5", "socks":
		p.Type, p.Username, p.Password = "socks5", m.str("username"), m.str("password")
		p.TLS = m.bool("tls")
	case "http":
		p.Type, p.Username, p.Password, p.SNI = "http", m.str("username"), m.str("password"), m.str("sni")
		p.TLS = m.bool("tls")
	case "wireguard", "wg":
		p.Type, p.PrivateKey, p.PublicKey = "wireguard", m.str("private-key"), m.str("public-key")
		p.PreSharedKey, p.MTU = m.str("pre-shared-key", "preshared-key"), m.num("mtu")
		for _, key := range []string{"ip", "ipv6"} {
			if a := m.str(key); a != "" {
				p.LocalAddress = append(p.LocalAddress, a)
			}
		}
		if peers, ok := m["peers"].([]any); ok && len(peers) > 0 {
			if peer, ok := peers[0].(map[string]any); ok {
				pm := clashMap(peer)
				p.Host, p.Port = firstNonEmpty(pm.str("server"), p.Host), max(pm.num("port"), p.Port)
				p.PublicKey = firstNonEmpty(pm.str("public-key"), p.PublicKey)
				p.PreSharedKey = firstNonEmpty(pm.str("pre-shared-key"), p.PreSharedKey)
				if r := pm.list("reserved"); len(r) > 0 {
					m["reserved"] = peer["reserved"]
				}
			}
		}
		p.Reserved = reservedBytes(m.list("reserved"))
	default:
		p.Type = typ
	}
	return p, true
}

// clashPorts reads the port hopping list of a Hysteria proxy, "ports:
// 443,20000-30000", as sing-box server_ports.
func clashPorts(m clashMap) []string {
	var out []string
	for _, r := range portRanges(m.str("ports")) {
		out = append(out, r.(string))
	}
	return out
}

// clashSSPlugin maps Clash's obfs and v2ray-plugin options to SIP003
// plugin options.
func clashSSPlugin(p *proxy, m clashMap) {
	opts := m.sub("plugin-opts")
	switch m.str("plugin") {
	case "obfs":
		p.Plugin = "obfs-local"
		parts := []string{"obfs=" + firstNonEmpty(opts.str("mode"), "http")}
		if host := opts.str("host"); host != "" {
			parts = append(parts, "obfs-host="+host)
		}
		p.PluginOpts = strings.Join(parts, ";")
	case "v2ray-plugin":
		p.Plugin = "v2ray-plugin"
		parts := []string{"mode=" + firstNonEmpty(opts.str("mode"), "websocket")}
		if opts.bool("tls") {
			parts = append(parts, "tls")
		}
		if host := opts.str("host"); host != "" {
			parts = append(parts, "host="+host)
		}
		if path := opts.str("path"); path != "" {
			parts = append(parts, "path="+path)
		}
		if opts.bool("mux") {
			parts = append(parts, "mux=4")
		}
		p.PluginOpts = strings.Join(parts, ";")
	case "":
	default:
		p.Plugin = m.str("plugin")
	}
}

// clashTransport reads the network options of vmess, vless and trojan.
func clashTransport(p *proxy, m clashMap) {
	network := strings.ToLower(m.str("network"))
	switch network {
	case "ws":
		ws := m.sub("ws-opts")
		p.Network = "ws"
		p.Path = firstNonEmpty(ws.str("path"), m.str("ws-path"))
		headers := ws.sub("headers")
		if len(headers) == 0 {
			headers = m.sub("ws-headers")
		}
		p.TransportHost = headers.first("Host")
		if p.TransportHost == "" {
			p.TransportHost = headers.first("host")
		}
		p.EarlyData, p.EarlyDataName = ws.num("max-early-data"), ws.str("early-data-header-name")
	case "grpc":
		p.Network, p.ServiceName = "grpc", m.sub("grpc-opts").str("grpc-service-name")
	case "h2":
		h2 := m.sub("h2-opts")
		p.Network, p.Path = "http", h2.str("path")
		p.TransportHost = strings.Join(h2.list("host"), ",")
	case "http":
		opts := m.sub("http-opts")
		p.Network, p.HTTPMethod = "http", opts.str("method")
		p.Path = opts.first("path")
		p.TransportHost = strings.Join(opts.sub("headers").list("Host"), ",")
	case "httpupgrade":
		opts := m.sub("http-upgrade-opts")
		if len(opts) == 0 {
			opts = m.sub("ws-opts")
		}
		p.Network, p.Path = "httpupgrade", opts.str("path")
		p.TransportHost = opts.sub("headers").first("Host")
	}
}
