package subscription

import (
	"strings"
)

// proxy is a node read from a share link or a Clash proxy, the
// intermediate form libsubconverter explodes subscriptions into before
// writing sing-box outbounds.
type proxy struct {
	Type   string // ss, ssr, vmess, vless, trojan, hysteria, hysteria2, tuic, anytls, socks5, http, wireguard, or another (unsupported)
	Remark string
	Host   string
	Port   int

	Username, Password string
	Method             string // ss/ssr cipher, vmess security
	Plugin, PluginOpts string
	Protocol           string // ssr
	ProtocolParam      string
	Obfs, ObfsParam    string // ssr obfs; hysteria obfs (ObfsParam unused); hysteria2 obfs type and password
	UUID               string
	AlterID            int
	Flow               string

	// Network is the V2Ray transport: tcp, ws, grpc, http (h2), httpupgrade.
	Network       string
	TransportHost string
	Path          string
	ServiceName   string
	Headers       map[string]string
	EarlyData     int
	EarlyDataName string
	HTTPMethod    string

	TLS         bool
	SNI         string
	ALPN        []string
	Insecure    bool
	Fingerprint string
	DisableSNI  bool
	RealityKey  string
	RealitySID  string

	ServerPorts         []string // Hysteria port hopping, as sing-box "start:end" ranges
	UpMbps, DownMbps    int
	AuthStr             string
	CongestionControl   string
	UDPRelayMode        string
	ZeroRTT             bool
	PrivateKey          string
	PublicKey           string
	PreSharedKey        string
	LocalAddress        []string
	Reserved            []int
	MTU                 int
	TFO                 bool
	SocksVersion        string
	HTTPSProxy          bool
	HysteriaProtocol    string
	HysteriaRecvWindow  int
	HysteriaRecvWinConn int
}

// singBoxType is the outbound type of a proxy, "" when sing-box has none.
func (p *proxy) singBoxType() string {
	switch p.Type {
	case "ss":
		return "shadowsocks"
	case "ssr":
		return "shadowsocksr"
	case "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "anytls", "wireguard", "http":
		return p.Type
	case "socks5":
		return "socks"
	}
	return ""
}

// toSingBox writes the proxy as a sing-box outbound; ok is false for a
// type sing-box does not have or a node without an address.
func (p *proxy) toSingBox() (map[string]any, bool) {
	typ := p.singBoxType()
	if typ == "" || p.Host == "" || p.Port < 1 || p.Port > 65535 {
		return nil, false
	}
	o := map[string]any{"type": typ, "tag": p.Remark}
	if p.Remark == "" {
		o["tag"] = p.Host + ":" + itoa(p.Port)
	}
	if typ != "wireguard" {
		o["server"] = p.Host
		o["server_port"] = float64(p.Port)
	}
	setStr := func(key, value string) {
		if value != "" {
			o[key] = value
		}
	}
	switch typ {
	case "shadowsocks":
		o["method"] = p.Method
		o["password"] = p.Password
		setStr("plugin", p.Plugin)
		setStr("plugin_opts", p.PluginOpts)
	case "shadowsocksr":
		o["method"] = p.Method
		o["password"] = p.Password
		setStr("protocol", p.Protocol)
		setStr("protocol_param", p.ProtocolParam)
		setStr("obfs", p.Obfs)
		setStr("obfs_param", p.ObfsParam)
	case "vmess":
		o["uuid"] = p.UUID
		o["alter_id"] = float64(p.AlterID)
		o["security"] = firstNonEmpty(p.Method, "auto")
	case "vless":
		o["uuid"] = p.UUID
		setStr("flow", p.Flow)
	case "trojan", "anytls":
		o["password"] = p.Password
	case "http":
		setStr("username", p.Username)
		setStr("password", p.Password)
	case "socks":
		o["version"] = firstNonEmpty(p.SocksVersion, "5")
		setStr("username", p.Username)
		setStr("password", p.Password)
	case "hysteria":
		if p.UpMbps > 0 {
			o["up_mbps"] = float64(p.UpMbps)
		}
		if p.DownMbps > 0 {
			o["down_mbps"] = float64(p.DownMbps)
		}
		setStr("obfs", p.Obfs)
		setStr("auth_str", p.AuthStr)
		if p.HysteriaRecvWindow > 0 {
			o["recv_window"] = float64(p.HysteriaRecvWindow)
		}
		if p.HysteriaRecvWinConn > 0 {
			o["recv_window_conn"] = float64(p.HysteriaRecvWinConn)
		}
	case "hysteria2":
		o["password"] = p.Password
		if p.UpMbps > 0 {
			o["up_mbps"] = float64(p.UpMbps)
		}
		if p.DownMbps > 0 {
			o["down_mbps"] = float64(p.DownMbps)
		}
		if p.Obfs != "" {
			o["obfs"] = map[string]any{"type": p.Obfs, "password": p.ObfsParam}
		}
	case "tuic":
		o["uuid"] = p.UUID
		setStr("password", p.Password)
		setStr("congestion_control", p.CongestionControl)
		setStr("udp_relay_mode", p.UDPRelayMode)
		if p.ZeroRTT {
			o["zero_rtt_handshake"] = true
		}
	case "wireguard":
		addresses := make([]any, 0, len(p.LocalAddress))
		for _, a := range p.LocalAddress {
			if a = strings.TrimSpace(a); a != "" {
				if !strings.Contains(a, "/") {
					if strings.Contains(a, ":") {
						a += "/128"
					} else {
						a += "/32"
					}
				}
				addresses = append(addresses, a)
			}
		}
		o["local_address"] = addresses
		o["private_key"] = p.PrivateKey
		peer := map[string]any{"server": p.Host, "server_port": float64(p.Port), "public_key": p.PublicKey,
			"allowed_ips": []any{"0.0.0.0/0", "::/0"}}
		if p.PreSharedKey != "" {
			peer["pre_shared_key"] = p.PreSharedKey
		}
		if len(p.Reserved) > 0 {
			reserved := make([]any, len(p.Reserved))
			for i, r := range p.Reserved {
				reserved[i] = float64(r)
			}
			peer["reserved"] = reserved
		}
		o["peers"] = []any{peer}
		if p.MTU > 0 {
			o["mtu"] = float64(p.MTU)
		}
	}
	if len(p.ServerPorts) > 0 && (typ == "hysteria" || typ == "hysteria2") {
		ports := make([]any, len(p.ServerPorts))
		for i, r := range p.ServerPorts {
			ports[i] = r
		}
		o["server_ports"] = ports
	}
	if p.TFO {
		o["tcp_fast_open"] = true
	}
	if t := p.transport(typ); t != nil {
		o["transport"] = t
	}
	if tls := p.tlsOptions(typ); tls != nil {
		o["tls"] = tls
	}
	return o, true
}

// transport is the V2Ray transport of vmess, vless and trojan nodes.
func (p *proxy) transport(typ string) map[string]any {
	if typ != "vmess" && typ != "vless" && typ != "trojan" {
		return nil
	}
	switch p.Network {
	case "ws":
		path, ed := splitEarlyData(firstNonEmpty(p.Path, "/"))
		if p.EarlyData > 0 {
			path, ed = firstNonEmpty(p.Path, "/"), p.EarlyData
		}
		t := map[string]any{"type": "ws", "path": path}
		headers := map[string]any{}
		for k, v := range p.Headers {
			headers[k] = v
		}
		if p.TransportHost != "" {
			headers["Host"] = p.TransportHost
		}
		if len(headers) > 0 {
			t["headers"] = headers
		}
		if ed > 0 {
			t["max_early_data"] = float64(ed)
			t["early_data_header_name"] = firstNonEmpty(p.EarlyDataName, "Sec-WebSocket-Protocol")
		}
		return t
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": p.ServiceName}
	case "http", "h2":
		t := map[string]any{"type": "http"}
		if p.TransportHost != "" {
			t["host"] = splitList(p.TransportHost)
		}
		if p.Path != "" {
			t["path"] = p.Path
		}
		if p.HTTPMethod != "" {
			t["method"] = p.HTTPMethod
		}
		return t
	case "httpupgrade":
		t := map[string]any{"type": "httpupgrade", "path": firstNonEmpty(p.Path, "/")}
		if p.TransportHost != "" {
			t["host"] = p.TransportHost
		}
		return t
	}
	return nil
}

// tlsOptions is the TLS block: always for the QUIC protocols, AnyTLS and
// Trojan, when enabled for the others.
func (p *proxy) tlsOptions(typ string) map[string]any {
	switch typ {
	case "hysteria", "hysteria2", "tuic", "anytls", "trojan":
	case "shadowsocks", "shadowsocksr", "wireguard":
		return nil
	case "http":
		if !p.TLS && !p.HTTPSProxy {
			return nil
		}
	default:
		if !p.TLS && p.RealityKey == "" {
			return nil
		}
	}
	tls := map[string]any{"enabled": true}
	if p.SNI != "" {
		tls["server_name"] = p.SNI
	} else if p.TransportHost != "" && (typ == "vmess" || typ == "vless" || typ == "trojan") {
		tls["server_name"] = strings.Split(p.TransportHost, ",")[0]
	}
	if p.Insecure {
		tls["insecure"] = true
	}
	if p.DisableSNI {
		tls["disable_sni"] = true
	}
	if len(p.ALPN) > 0 {
		alpn := make([]any, len(p.ALPN))
		for i, a := range p.ALPN {
			alpn[i] = a
		}
		tls["alpn"] = alpn
	}
	if p.Fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": p.Fingerprint}
	}
	if p.RealityKey != "" {
		reality := map[string]any{"enabled": true, "public_key": p.RealityKey}
		if p.RealitySID != "" {
			reality["short_id"] = p.RealitySID
		}
		tls["reality"] = reality
		if p.Fingerprint == "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	return tls
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
