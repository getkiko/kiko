package subscription

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// explodeLink reads a share link as a proxy, as libsubconverter's
// explode does: ss, ssr, vmess (v2rayN and Shadowrocket), vless, trojan,
// hysteria, hysteria2/hy2, tuic, anytls, socks/socks5, http/https and
// wireguard/wg. recognized is false for a line that is no share link.
func explodeLink(line string) (p *proxy, recognized bool) {
	s := strings.TrimSpace(line)
	scheme, _, ok := strings.Cut(s, "://")
	if !ok {
		return nil, false
	}
	switch strings.ToLower(scheme) {
	case "ss":
		return explodeSS(s), true
	case "ssr":
		return explodeSSR(s), true
	case "vmess":
		return explodeVmess(s), true
	case "vless":
		if n, ok := ParseLink(s); ok {
			return nodeProxy(n), true
		}
		return nil, true
	case "trojan", "hysteria2", "hy2", "tuic", "anytls":
		return explodeURLStyle(s, strings.ToLower(scheme)), true
	case "hysteria":
		return explodeHysteria(s), true
	case "socks", "socks5", "socks5h":
		return explodeAuthProxy(s, "socks5"), true
	case "http", "https":
		return explodeAuthProxy(s, "http"), true
	case "wireguard", "wg":
		return explodeWireGuard(s), true
	}
	return nil, reLineScheme.MatchString(s)
}

// nodeProxy turns a node of ParseLink back into a proxy.
func nodeProxy(n Node) *proxy {
	f := n.Fields
	str := func(key string) string { return jsv.StringOr(f[key], "") }
	p := &proxy{Remark: n.Name, Host: str("server"), Port: int(jsv.Number(f["server_port"])),
		UUID: str("uuid"), Password: str("password"), Flow: str("flow"), Method: str("method"),
		Plugin: str("plugin"), PluginOpts: str("plugin_opts"),
		TLS: f["tls.enabled"] == true, SNI: str("tls.server_name"), Insecure: f["tls.insecure"] == true,
		RealityKey: str("tls.reality.public_key"), RealitySID: str("tls.reality.short_id"),
		CongestionControl: str("congestion_control"), UDPRelayMode: str("udp_relay_mode"),
		Obfs: str("obfs.type"), ObfsParam: str("obfs.password")}
	switch n.Type {
	case "shadowsocks":
		p.Type = "ss"
	default:
		p.Type = n.Type
	}
	if f["tls.utls.enabled"] == true {
		p.Fingerprint = str("tls.utls.fingerprint")
	}
	if ports, ok := f["server_ports"].([]any); ok {
		for _, r := range ports {
			p.ServerPorts = append(p.ServerPorts, jsv.String(r))
		}
	}
	if alpn, ok := f["tls.alpn"].([]any); ok {
		for _, a := range alpn {
			p.ALPN = append(p.ALPN, jsv.String(a))
		}
	}
	switch str("transport.type") {
	case "ws":
		p.Network, p.Path = "ws", str("transport.path")
		p.TransportHost = transportHeader(f, "Host")
		p.EarlyData = int(jsv.Number(f["transport.max_early_data"]))
		p.EarlyDataName = str("transport.early_data_header_name")
	case "grpc":
		p.Network, p.ServiceName = "grpc", str("transport.service_name")
	case "http":
		p.Network, p.Path = "http", str("transport.path_http")
		if hosts, ok := f["transport.host"].([]any); ok {
			p.TransportHost = joinValues(hosts)
		}
	case "httpupgrade":
		p.Network, p.Path, p.TransportHost = "httpupgrade", str("transport.path_hu"), str("transport.host_hu")
	}
	return p
}

func explodeSS(s string) *proxy {
	n, ok := parseSS(s)
	if !ok {
		return nil
	}
	p := nodeProxy(n)
	// SIP002 plugins: obfs-local and v2ray-plugin, as sing-box names them.
	if p.Plugin == "simple-obfs" {
		p.Plugin = "obfs-local"
	}
	return p
}

// explodeSSR reads ssr://Base64(host:port:protocol:method:obfs:Base64(password)/?params).
func explodeSSR(s string) *proxy {
	decoded, err := B64Decode(strings.TrimPrefix(s[len("ssr://"):], "/"))
	if err != nil {
		return nil
	}
	main, params, _ := strings.Cut(decoded, "/?")
	if !strings.Contains(decoded, "/?") {
		main, params, _ = strings.Cut(decoded, "?")
	}
	parts := strings.Split(main, ":")
	if len(parts) < 6 {
		return nil
	}
	n := len(parts)
	host := strings.Join(parts[:n-5], ":")
	port, _ := strconv.Atoi(parts[n-5])
	password, err := B64Decode(parts[n-1])
	if err != nil {
		return nil
	}
	q := parseQuery(params)
	b64 := func(key string) string {
		v, err := B64Decode(q.get(key))
		if err != nil {
			return ""
		}
		return v
	}
	return &proxy{Type: "ssr", Host: trimBrackets(host), Port: port, Protocol: parts[n-4], Method: parts[n-3],
		Obfs: parts[n-2], Password: password, ObfsParam: b64("obfsparam"), ProtocolParam: b64("protoparam"),
		Remark: b64("remarks")}
}

var reRocketVmess = regexp.MustCompile(`^([^:]+):(.+)@(\[[^\]]+\]|[^:]+):(\d+)$`)

func explodeVmess(s string) *proxy {
	rest := s[len("vmess://"):]
	if n, ok := parseVmess(s); ok {
		p := nodeProxy(n)
		// v2rayN keeps the transport host for TLS SNI fallbacks and the
		// aid; parseVmess already filled both.
		decoded, _ := B64Decode(rest)
		var j map[string]any
		if json.Unmarshal([]byte(decoded), &j) == nil {
			if aid := jsv.ParseInt(jsv.StringOr(j["aid"], "0")); jsv.IsFinite(aid) {
				p.AlterID = int(aid)
			}
			p.Method = orDefault(j["scy"], "auto")
		}
		return p
	}
	// Shadowrocket: vmess://Base64(method:uuid@host:port)?remarks=…&obfs=websocket&path=…&obfsParam=…&tls=1
	payload, params, _ := strings.Cut(rest, "?")
	payload, frag, _ := strings.Cut(payload, "#")
	decoded, err := B64Decode(payload)
	if err != nil {
		return nil
	}
	m := reRocketVmess.FindStringSubmatch(decoded)
	if m == nil {
		return nil
	}
	params, frag2, _ := strings.Cut(params, "#")
	q := parseQuery(params)
	port, _ := strconv.Atoi(m[4])
	p := &proxy{Type: "vmess", Method: m[1], UUID: m[2], Host: trimBrackets(m[3]), Port: port,
		Remark: firstNonEmpty(q.get("remarks"), dec(frag), dec(frag2)), AlterID: parseIntPrefix(q.get("alterId")),
		TLS: linkTrue(q.get("tls")), SNI: q.get("peer"), Insecure: insecureParam(q)}
	switch q.get("obfs") {
	case "websocket":
		p.Network, p.Path, p.TransportHost = "ws", q.get("path"), q.get("obfsParam")
	case "http":
		p.Network, p.Path, p.TransportHost = "http", q.get("path"), q.get("obfsParam")
	case "grpc":
		p.Network, p.ServiceName = "grpc", q.get("path")
	}
	return p
}

func explodeURLStyle(s, scheme string) *proxy {
	typ := scheme
	if typ == "hy2" {
		typ, s = "hysteria2", "hysteria2:"+s[len("hy2:"):]
	}
	n, ok := parseURLStyle(s, typ)
	if !ok {
		return nil
	}
	p := nodeProxy(n)
	s, _ = splitHopPorts(s)
	u, _ := parseLinkURL(s)
	q := u.query
	switch typ {
	case "hysteria2":
		p.UpMbps, p.DownMbps = mbps(q.get("upmbps")), mbps(q.get("downmbps"))
	case "tuic":
		p.DisableSNI = linkTrue(q.get("disable_sni"))
	}
	return p
}

// explodeHysteria reads hysteria://host:port?auth=…&peer=…&upmbps=…&downmbps=…&obfs=…#name.
func explodeHysteria(s string) *proxy {
	s, hop := splitHopPorts(s)
	u, ok := parseLinkURL(s)
	if !ok {
		return nil
	}
	q := u.query
	if hop == nil {
		hop = portRanges(q.get("mport"))
	}
	port := parseIntPrefix(u.port)
	p := &proxy{Type: "hysteria", Host: trimBrackets(u.host), Port: port, Remark: dec(u.hash),
		AuthStr: firstNonEmpty(q.get("auth"), q.get("auth_str")), SNI: firstNonEmpty(q.get("peer"), q.get("sni")),
		Insecure: insecureParam(q), UpMbps: mbps(firstNonEmpty(q.get("upmbps"), q.get("up"))),
		DownMbps: mbps(firstNonEmpty(q.get("downmbps"), q.get("down"))), Obfs: firstNonEmpty(q.get("obfsParam"), q.get("obfs-password")),
		HysteriaProtocol: q.get("protocol")}
	if q.get("obfs") != "" && q.get("obfs") != "xplus" && p.Obfs == "" {
		p.Obfs = q.get("obfs")
	}
	if alpn := q.get("alpn"); alpn != "" {
		p.ALPN = strings.Split(alpn, ",")
	}
	for _, r := range hop {
		p.ServerPorts = append(p.ServerPorts, r.(string))
	}
	return p
}

// explodeAuthProxy reads socks5:// and http(s):// proxies with optional
// user info, as user:pass@host:port or Base64(user:pass)@host:port.
func explodeAuthProxy(s, typ string) *proxy {
	u, ok := parseLinkURL(s)
	if !ok || u.port == "" || (u.path != "" && u.path != "/") {
		return nil
	}
	user, pass := dec(u.username), dec(u.password)
	if user != "" && u.password == "" {
		if decoded, err := B64Decode(user); err == nil && strings.Contains(decoded, ":") {
			user, pass, _ = strings.Cut(decoded, ":")
		}
	}
	p := &proxy{Type: typ, Host: trimBrackets(u.host), Port: parseIntPrefix(u.port), Username: user, Password: pass,
		Remark: firstNonEmpty(dec(u.hash), u.query.get("remarks"))}
	if strings.HasPrefix(strings.ToLower(s), "https://") {
		p.HTTPSProxy, p.TLS = true, true
		p.SNI = u.query.get("sni")
	}
	return p
}

// explodeWireGuard reads wireguard://privatekey@host:port?publickey=…&address=…&mtu=…&reserved=…#name.
func explodeWireGuard(s string) *proxy {
	u, ok := parseLinkURL(s)
	if !ok {
		return nil
	}
	q := u.query
	p := &proxy{Type: "wireguard", Host: trimBrackets(u.host), Port: parseIntPrefix(u.port), Remark: dec(u.hash),
		PrivateKey: dec(u.username), PublicKey: firstNonEmpty(q.get("publickey"), q.get("public_key"), q.get("peer")),
		PreSharedKey: firstNonEmpty(q.get("presharedkey"), q.get("pre_shared_key")), MTU: parseIntPrefix(q.get("mtu"))}
	for _, a := range strings.Split(firstNonEmpty(q.get("address"), q.get("ip")), ",") {
		if a = strings.TrimSpace(a); a != "" {
			p.LocalAddress = append(p.LocalAddress, a)
		}
	}
	p.Reserved = reservedBytes(strings.Split(q.get("reserved"), ","))
	return p
}

// reservedBytes reads WireGuard reserved bytes, written as numbers
// ("1,2,3") or, as Clash also accepts, one Base64 string ("AQID").
func reservedBytes(parts []string) []int {
	var out []int
	if len(parts) == 1 {
		if s := strings.TrimSpace(parts[0]); s != "" && strings.Trim(s, "0123456789") != "" {
			if raw, err := base64.StdEncoding.DecodeString(s); err == nil && len(raw) == 3 {
				for _, b := range raw {
					out = append(out, int(b))
				}
			}
			return out
		}
	}
	for _, r := range parts {
		if n, err := strconv.Atoi(strings.TrimSpace(r)); err == nil && n >= 0 && n <= 255 {
			out = append(out, n)
		}
	}
	return out
}

var reMbps = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)`)

// mbps reads a bandwidth as "100", "100 Mbps" or "1 Gbps" in Mbit/s.
func mbps(s string) int {
	m := reMbps.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "g"):
		v *= 1000
	case strings.Contains(lower, "k"):
		v /= 1000
	}
	return int(v)
}
