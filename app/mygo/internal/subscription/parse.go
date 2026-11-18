// Package subscription parses and converts proxy subscriptions: share
// links and sing-box outbounds, traffic headers, and Convert, which turns
// a subscription body — sing-box JSON, a Clash YAML configuration, or
// share links, plain or Base64 — into sing-box outbounds, which
// ParseSingBoxConfig reads as nodes.
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// Node is a parsed node: an outbound type, its name, its fields in the
// document's flattened form ("tls.enabled", "transport.path"…), and a key
// identifying it across refreshes.
type Node struct {
	Type   string
	Name   string
	Fields map[string]any
	Key    string
}

// Result is the nodes of a subscription body and how many entries were
// skipped as unsupported.
type Result struct {
	Nodes   []Node
	Skipped int
}

var singBoxProxyTypes = map[string]bool{
	"socks": true, "http": true, "shadowsocks": true, "shadowsocksr": true, "vless": true, "vmess": true,
	"trojan": true, "hysteria": true, "hysteria2": true, "tuic": true, "anytls": true, "wireguard": true,
	"ssh": true, "shadowtls": true, "burrow": true, "naive": true,
}

var singBoxNonProxyTypes = map[string]bool{
	"direct": true, "block": true, "dns": true, "selector": true, "urltest": true, "fallback": true, "loadbalance": true, "smart": true, "interrupt": true, "tor": true,
}

func isScalarOrArray(v any) bool {
	switch v.(type) {
	case string, float64, bool, []any:
		return true
	}
	return false
}

func copyScalarFields(source, target map[string]any, excluded map[string]bool) {
	for key, value := range source {
		if excluded[key] {
			continue
		}
		if isScalarOrArray(value) {
			target[key] = value
		}
	}
}

func flattenOption(target map[string]any, prefix string, value any) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	for key, child := range obj {
		field := prefix + "." + key
		if _, isObj := child.(map[string]any); isObj {
			flattenOption(target, field, child)
		} else if isScalarOrArray(child) {
			target[field] = child
		}
	}
}

func applySingBoxTransport(fields map[string]any, transport any) {
	t, ok := transport.(map[string]any)
	if !ok {
		return
	}
	typ, _ := t["type"].(string)
	if typ == "" {
		return
	}
	fields["transport.type"] = typ
	if h, ok := t["headers"].(map[string]any); ok {
		fields["transport.headers"] = h
	}
	str := func(key, field string) {
		if v, ok := t[key].(string); ok {
			fields[field] = v
		}
	}
	switch typ {
	case "ws":
		str("path", "transport.path")
		if v, ok := t["max_early_data"].(float64); ok {
			fields["transport.max_early_data"] = v
		}
		str("early_data_header_name", "transport.early_data_header_name")
	case "grpc":
		str("service_name", "transport.service_name")
	case "http":
		if v, ok := t["host"].([]any); ok {
			fields["transport.host"] = v
		}
		str("path", "transport.path_http")
		str("method", "transport.method")
		str("idle_timeout", "transport.idle_timeout")
		str("ping_timeout", "transport.ping_timeout")
	case "httpupgrade":
		str("path", "transport.path_hu")
		str("host", "transport.host_hu")
	}
}

func transportHeader(fields map[string]any, name string) string {
	if headers, ok := fields["transport.headers"].(map[string]any); ok {
		for key, value := range headers {
			if strings.EqualFold(key, name) {
				if arr, ok := value.([]any); ok {
					parts := make([]string, len(arr))
					for i, v := range arr {
						parts[i] = jsv.String(v)
					}
					return strings.Join(parts, ",")
				}
				if value != nil {
					return jsv.String(value)
				}
			}
		}
	}
	return jsv.StringOr(fields["transport.headers."+name], "")
}

var reSignalServer = regexp.MustCompile(`(?i)^(wss?://)?([^/]+)(?:/.*)?$`)

func formatBurrowSignalAddress(server string, port float64, rawPath any) string {
	path := ""
	if p, ok := rawPath.(string); ok && p != "" {
		if strings.HasPrefix(p, "/") {
			path = p
		} else {
			path = "/" + p
		}
	}
	m := reSignalServer.FindStringSubmatch(strings.TrimSpace(server))
	if m == nil {
		return ""
	}
	host := m[2]
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return m[1] + host + ":" + jsv.FormatNumber(port) + path
}

var singBoxExcluded = map[string]bool{
	"type": true, "tag": true, "server": true, "server_port": true, "tls": true, "transport": true,
	"multiplex": true, "detour": true, "outbounds": true, "default": true, "includes": true,
	"excludes": true, "peers": true,
}

func firstNetwork(v any) any {
	if arr, ok := v.([]any); ok {
		if len(arr) == 0 {
			return nil
		}
		return arr[0]
	}
	return v
}

func orDefault(v any, fallback string) string {
	if !jsv.Truthy(v) {
		return fallback
	}
	return jsv.String(v)
}

// parseSingBoxOutbound reads one sing-box proxy outbound as a node.
func parseSingBoxOutbound(outbound map[string]any) (Node, bool) {
	typ, ok := outbound["type"].(string)
	// holepunch is Burrow's type before the core renamed it.
	if typ == "holepunch" {
		typ = "burrow"
	}
	if !ok || !singBoxProxyTypes[typ] {
		return Node{}, false
	}
	var peer map[string]any
	if typ == "wireguard" {
		if peers, ok := outbound["peers"].([]any); ok && len(peers) > 0 {
			peer, _ = peers[0].(map[string]any)
		}
	}
	endpoint := outbound
	if typ == "wireguard" {
		endpoint = peer
	}
	server, _ := endpoint["server"].(string)
	port, _ := endpoint["server_port"].(float64)
	if server == "" || !jsv.IsInteger(port) || port < 1 || port > 65535 {
		return Node{}, false
	}
	fields := map[string]any{}
	if typ == "burrow" {
		fields["signal_address"] = formatBurrowSignalAddress(server, port, outbound["signal_path"])
	} else {
		fields["server"], fields["server_port"] = server, port
	}
	copyScalarFields(outbound, fields, singBoxExcluded)
	switch {
	case typ == "burrow":
		fields["network"] = orDefault(firstNetwork(outbound["network"]), "tcp") + "+" + orDefault(outbound["address_family"], "ipv4")
		delete(fields, "address_family")
		delete(fields, "signal_path")
	case typ == "wireguard" && peer != nil:
		if v, ok := peer["public_key"].(string); ok {
			fields["peer_public_key"] = v
		}
		if v, ok := peer["pre_shared_key"].(string); ok {
			fields["pre_shared_key"] = v
		}
		if v, ok := peer["reserved"].([]any); ok {
			fields["reserved"] = v
		}
	default:
		if n, ok := fields["network"].(string); ok {
			fields["network"] = []any{n}
		}
	}
	flattenOption(fields, "tls", outbound["tls"])
	flattenOption(fields, "multiplex", outbound["multiplex"])
	flattenOption(fields, "obfs", outbound["obfs"])
	flattenOption(fields, "handshake", outbound["handshake"])
	applySingBoxTransport(fields, outbound["transport"])
	if fields["tls.reality.enabled"] == true {
		fields["tls.utls.enabled"] = true
		if !jsv.Truthy(fields["tls.utls.fingerprint"]) {
			fields["tls.utls.fingerprint"] = "chrome"
		}
	}
	name, _ := outbound["tag"].(string)
	if name == "" {
		name = server + ":" + jsv.FormatNumber(port)
	}
	return Node{Type: typ, Name: name, Fields: fields, Key: identityKey(typ, server, jsv.FormatNumber(port), fields)}, true
}

// identityKey identifies a node across refreshes: its type, address and
// credential.
func identityKey(typ, server, port string, fields map[string]any) string {
	var identity any
	for _, key := range []string{"uuid", "password", "private_key", "peer_public_key", "method", "user"} {
		if v, ok := fields[key]; ok && v != nil {
			identity = v
			break
		}
	}
	return typ + "|" + server + "|" + port + "|" + jsv.StringOr(identity, "")
}

// nodeKey is the key of a share link's node, as ParseSingBoxConfig keys
// it: the former TypeScript parser left the credential out
// ("ss|host|port"), so nodes sharing an address collided.
func nodeKey(typ, host string, port int, fields map[string]any) string {
	return identityKey(typ, host, strconv.Itoa(port), fields)
}

// splitEarlyData takes Xray's early data parameter out of a WebSocket
// path ("/ws?ed=2048"), which sing-box would otherwise request
// literally; ed is 0 when the path has none.
func splitEarlyData(path string) (string, int) {
	base, rawQuery, ok := strings.Cut(path, "?")
	if !ok {
		return path, 0
	}
	ed := 0
	var rest []string
	for _, part := range strings.Split(rawQuery, "&") {
		name, value, _ := strings.Cut(part, "=")
		if name == "ed" && ed == 0 {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				ed = n
				continue
			}
		}
		if part != "" {
			rest = append(rest, part)
		}
	}
	if ed == 0 {
		return path, 0
	}
	if len(rest) > 0 {
		base += "?" + strings.Join(rest, "&")
	}
	return base, ed
}

// setWSPath sets a WebSocket path, its early data parameter as
// max_early_data in the Sec-WebSocket-Protocol header.
func setWSPath(f map[string]any, path string) {
	path, ed := splitEarlyData(path)
	f["transport.path"] = path
	if ed > 0 {
		f["transport.max_early_data"] = float64(ed)
		f["transport.early_data_header_name"] = "Sec-WebSocket-Protocol"
	}
}

// ParseSingBoxConfig reads the proxy outbounds of a sing-box
// configuration; ok is false when text is not one.
func ParseSingBoxConfig(text string) (Result, bool) {
	var config any
	if err := json.Unmarshal([]byte(text), &config); err != nil {
		return Result{}, false
	}
	obj, ok := config.(map[string]any)
	if !ok {
		return Result{}, false
	}
	outbounds, ok := obj["outbounds"].([]any)
	if !ok {
		return Result{}, false
	}
	var result Result
	for _, value := range outbounds {
		o, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if node, ok := parseSingBoxOutbound(o); ok {
			result.Nodes = append(result.Nodes, node)
		} else if t, ok := o["type"].(string); ok && !singBoxNonProxyTypes[t] {
			result.Skipped++
		}
	}
	return result, true
}

var reSpace = regexp.MustCompile(`[\r\n\s]`)

// B64Decode decodes Base64 or URL-safe Base64, with or without padding
// and line breaks, as UTF-8 text.
func B64Decode(s string) (string, error) {
	t := reSpace.ReplaceAllString(strings.TrimSpace(s), "")
	t = strings.NewReplacer("-", "+", "_", "/").Replace(t)
	for len(t)%4 != 0 {
		t += "="
	}
	raw, err := base64.StdEncoding.DecodeString(t)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return strings.ToValidUTF8(string(raw), "�"), nil
	}
	return string(raw), nil
}

var reBase64Body = regexp.MustCompile(`^[A-Za-z0-9+/\-_=]+$`)

func looksBase64(s string) bool {
	t := reSpace.ReplaceAllString(s, "")
	return len(t) > 24 && reBase64Body.MatchString(t)
}

// dec is decodeURIComponent, keeping s when it does not decode.
func dec(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return s
			}
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(v))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	if !utf8.Valid(b) {
		return s
	}
	return string(b)
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func parseIntPrefix(s string) int {
	f := jsv.ParseInt(s)
	if !jsv.IsFinite(f) {
		return 0
	}
	return int(f)
}

func splitList(s string) []any {
	var out []any
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	if out == nil {
		out = []any{}
	}
	return out
}

// fillTLSTransport fills the TLS and transport fields from a link's
// query (vless, trojan).
// defaultTLS is whether the protocol uses TLS when the link names no
// security (Trojan).
func fillTLSTransport(p query, host string, f map[string]any, defaultTLS bool) {
	sec := p.get("security")
	pbk := p.get("pbk")
	tls := sec == "tls" || sec == "reality" || sec == "xtls" || linkTrue(p.get("tls")) || pbk != "" ||
		defaultTLS && sec == ""
	if tls {
		f["tls.enabled"] = true
		f["tls.server_name"] = firstNonEmpty(p.get("sni"), p.get("peer"), p.get("host"), host)
		if alpn := p.get("alpn"); alpn != "" {
			f["tls.alpn"] = splitList(alpn)
		}
		if fp := p.get("fp"); fp != "" {
			f["tls.utls.enabled"] = true
			f["tls.utls.fingerprint"] = fp
		}
		if insecureParam(p) {
			f["tls.insecure"] = true
		}
		if sec == "reality" || pbk != "" {
			f["tls.reality.enabled"] = true
			f["tls.reality.public_key"] = pbk
			if sid := p.get("sid"); sid != "" {
				f["tls.reality.short_id"] = sid
			}
			f["tls.utls.enabled"] = true
			f["tls.utls.fingerprint"] = firstNonEmpty(p.get("fp"), "chrome")
		}
	}
	if p.has("udp") && p.get("udp") != "0" {
		f["network"] = []any{"tcp", "udp"}
	}
	switch net := firstNonEmpty(p.get("type"), p.get("obfs"), "tcp"); net {
	case "ws":
		f["transport.type"] = "ws"
		setWSPath(f, firstNonEmpty(p.get("path"), "/"))
		if h := p.get("host"); h != "" {
			f["transport.headers"] = map[string]any{"Host": h}
		}
	case "grpc":
		f["transport.type"] = "grpc"
		f["transport.service_name"] = p.get("serviceName")
	case "http", "h2":
		f["transport.type"] = "http"
		if h := p.get("host"); h != "" {
			f["transport.host"] = splitList(h)
		}
		if path := p.get("path"); path != "" {
			f["transport.path_http"] = path
		}
	case "httpupgrade":
		f["transport.type"] = "httpupgrade"
		f["transport.path_hu"] = firstNonEmpty(p.get("path"), "/")
		if h := p.get("host"); h != "" {
			f["transport.host_hu"] = h
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func trimBrackets(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// splitHostPort splits host:port or [IPv6]:port, a trailing slash
// allowed (SIP002's "host:port/?plugin"). The port is 0 when it is
// missing, not a number or out of range: the former TypeScript parser
// sliced at the last colon even when there was none ("1.2.3.4" became
// host "1.2.3." port 1, "[::1]" host "[:" port 1) and took any numeric
// prefix.
func splitHostPort(hostport string) (string, int) {
	hostport = strings.TrimSuffix(hostport, "/")
	var host, port string
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 || !strings.HasPrefix(hostport[end+1:], ":") {
			return "", 0
		}
		host, port = hostport[:end+1], hostport[end+2:]
	} else {
		pi := strings.LastIndex(hostport, ":")
		if pi < 0 {
			return hostport, 0
		}
		host, port = hostport[:pi], hostport[pi+1:]
	}
	return host, validPort(port)
}

// validPort reads a port of digits only, 0 when it is none of 1–65535.
func validPort(s string) int {
	if s == "" || len(s) > 5 || strings.Trim(s, "0123456789") != "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	if n < 1 || n > 65535 {
		return 0
	}
	return n
}

// splitUserinfo splits method:password at the first colon; without one
// there is no method (the former TypeScript parser took all but the last
// character).
func splitUserinfo(userinfo string) (string, string) {
	ci := strings.Index(userinfo, ":")
	if ci < 0 {
		return "", userinfo
	}
	return userinfo[:ci], userinfo[ci+1:]
}

// linkTrue reads a boolean query parameter: "1", "true", "yes" or "on".
func linkTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// insecureParam reports whether a link skips certificate verification,
// under any of the names clients write.
func insecureParam(p query) bool {
	return linkTrue(p.get("allowInsecure")) || linkTrue(p.get("insecure")) || linkTrue(p.get("allow_insecure")) ||
		linkTrue(p.get("skip-cert-verify"))
}

func parseSS(uri string) (Node, bool) {
	rest := uri[len("ss://"):]
	name := ""
	if h := strings.Index(rest, "#"); h >= 0 {
		name, rest = dec(rest[h+1:]), rest[:h]
	}
	q := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		q, rest = rest[i+1:], rest[:i]
	}
	var method, password, host string
	var port int
	if strings.Contains(rest, "@") {
		at := strings.LastIndex(rest, "@")
		userinfo, hostport := rest[:at], rest[at+1:]
		// SIP002 writes method:password percent-encoded (SIP022 for the
		// 2022 ciphers) or Base64, whose padding may be percent-encoded.
		if strings.Contains(userinfo, ":") {
			m, pw := splitUserinfo(userinfo)
			method, password = dec(m), dec(pw)
		} else if decoded := dec(userinfo); strings.Contains(decoded, ":") {
			method, password = splitUserinfo(decoded)
		} else {
			decoded, err := B64Decode(decoded)
			if err != nil {
				return Node{}, false
			}
			method, password = splitUserinfo(decoded)
		}
		host, port = splitHostPort(hostport)
	} else {
		d, err := B64Decode(rest)
		if err != nil {
			return Node{}, false
		}
		at := strings.LastIndex(d, "@")
		if at < 0 {
			return Node{}, false
		}
		userinfo, hostport := d[:at], d[at+1:]
		method, password = splitUserinfo(userinfo)
		host, port = splitHostPort(hostport)
	}
	if host == "" || port == 0 || method == "" {
		return Node{}, false
	}
	host = trimBrackets(host)
	f := map[string]any{"server": host, "server_port": float64(port), "method": method, "password": password}
	if q != "" {
		sp := parseQuery(q)
		if pl := sp.get("plugin"); pl != "" {
			parts := strings.Split(pl, ";")
			f["plugin"] = parts[0]
			if len(parts) > 1 {
				f["plugin_opts"] = strings.Join(parts[1:], ";")
			}
		}
	}
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return Node{Type: "shadowsocks", Name: name, Fields: f, Key: nodeKey("shadowsocks", host, port, f)}, true
}

func parseVmess(uri string) (Node, bool) {
	decoded, err := B64Decode(uri[len("vmess://"):])
	if err != nil {
		return Node{}, false
	}
	var j map[string]any
	if json.Unmarshal([]byte(decoded), &j) != nil || j == nil {
		return Node{}, false
	}
	host := orDefault(j["add"], "")
	port := parseIntPrefix(jsv.StringOr(j["port"], "undefined"))
	if host == "" || port == 0 || !jsv.Truthy(j["id"]) {
		return Node{}, false
	}
	f := map[string]any{
		"server": host, "server_port": float64(port), "uuid": jsv.String(j["id"]),
		"security": orDefault(j["scy"], "auto"),
	}
	if aid := jsv.ParseInt(jsv.StringOr(j["aid"], "undefined")); jsv.IsFinite(aid) && aid > 0 {
		f["alter_id"] = aid
	}
	if j["tls"] == "tls" || j["tls"] == "xtls" {
		f["tls.enabled"] = true
		f["tls.server_name"] = orDefault(firstTruthy(j["sni"], j["host"]), host)
		if jsv.Truthy(j["alpn"]) {
			f["tls.alpn"] = splitList(jsv.String(j["alpn"]))
		}
		if jsv.Truthy(j["fp"]) {
			f["tls.utls.enabled"] = true
			f["tls.utls.fingerprint"] = jsv.String(j["fp"])
		}
		if jsonTrue(j["allowInsecure"]) || jsonTrue(j["scv"]) || jsonTrue(j["insecure"]) {
			f["tls.insecure"] = true
		}
	}
	switch net := orDefault(j["net"], "tcp"); net {
	case "ws":
		f["transport.type"] = "ws"
		setWSPath(f, orDefault(j["path"], "/"))
		if jsv.Truthy(j["host"]) {
			f["transport.headers"] = map[string]any{"Host": jsv.String(j["host"])}
		}
	case "grpc":
		f["transport.type"] = "grpc"
		f["transport.service_name"] = orDefault(j["path"], "")
	case "h2", "http":
		f["transport.type"] = "http"
		if jsv.Truthy(j["host"]) {
			f["transport.host"] = splitList(jsv.String(j["host"]))
		}
		if jsv.Truthy(j["path"]) {
			f["transport.path_http"] = jsv.String(j["path"])
		}
	case "httpupgrade":
		f["transport.type"] = "httpupgrade"
		f["transport.path_hu"] = orDefault(j["path"], "/")
		if jsv.Truthy(j["host"]) {
			f["transport.host_hu"] = jsv.String(j["host"])
		}
	}
	name := dec(orDefault(j["ps"], ""))
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return Node{Type: "vmess", Name: name, Fields: f, Key: nodeKey("vmess", host, port, f)}, true
}

// jsonTrue reads a v2rayN boolean: true, 1, "1" or "true".
func jsonTrue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x == 1
	case string:
		return linkTrue(x)
	}
	return false
}

func firstTruthy(values ...any) any {
	for _, v := range values {
		if jsv.Truthy(v) {
			return v
		}
	}
	return nil
}

func parseURLStyle(uri, typ string) (Node, bool) {
	var serverPorts []any
	if typ == "hysteria2" {
		uri, serverPorts = splitHopPorts(uri)
	}
	u, ok := parseLinkURL(uri)
	if !ok {
		return Node{}, false
	}
	host := trimBrackets(u.host)
	port := parseIntPrefix(u.port)
	if host == "" || port == 0 {
		return Node{}, false
	}
	p := u.query
	f := map[string]any{"server": host, "server_port": float64(port)}
	cred := dec(u.username)
	// The QUIC protocols and AnyTLS always use TLS; their links name the
	// server name, verification and ALPN directly.
	quicTLS := func() {
		f["tls.enabled"] = true
		if sni := p.get("sni"); sni != "" {
			f["tls.server_name"] = sni
		}
		if insecureParam(p) {
			f["tls.insecure"] = true
		}
		if alpn := p.get("alpn"); alpn != "" {
			f["tls.alpn"] = splitList(alpn)
		}
	}
	switch typ {
	case "vless":
		if cred == "" {
			return Node{}, false
		}
		f["uuid"] = cred
		if flow := p.get("flow"); flow != "" {
			f["flow"] = flow
		}
	case "trojan":
		if cred == "" {
			return Node{}, false
		}
		f["password"] = cred
	case "hysteria2":
		f["password"] = firstNonEmpty(cred, p.get("auth"))
		quicTLS()
		if obfs := p.get("obfs"); obfs != "" {
			f["obfs.type"] = obfs
			f["obfs.password"] = p.get("obfs-password")
		}
		if mport := p.get("mport"); mport != "" && serverPorts == nil {
			serverPorts = portRanges(mport)
		}
		if len(serverPorts) > 0 {
			f["server_ports"] = serverPorts
		}
	case "tuic":
		f["uuid"] = cred
		f["password"] = dec(u.password)
		quicTLS()
		if cc := p.get("congestion_control"); cc != "" {
			f["congestion_control"] = cc
		}
		if urm := p.get("udp_relay_mode"); urm != "" {
			f["udp_relay_mode"] = urm
		}
	case "anytls":
		f["password"] = cred
		quicTLS()
		if fp := p.get("fp"); fp != "" {
			f["tls.utls.enabled"] = true
			f["tls.utls.fingerprint"] = fp
		}
	}
	if typ == "vless" || typ == "trojan" {
		fillTLSTransport(p, host, f, typ == "trojan")
	}
	name := host + ":" + strconv.Itoa(port)
	if u.hash != "" {
		name = dec(u.hash)
	}
	return Node{Type: typ, Name: name, Fields: f, Key: nodeKey(typ, host, port, f)}, true
}

var rePortList = regexp.MustCompile(`^\d+(?:-\d+)?(?:[,/]\d+(?:-\d+)?)*$`)

// portRanges reads a Hysteria 2 port list, "443,20000-30000", as
// sing-box server_ports ("443:443", "20000:30000"); nil when it is not
// one.
func portRanges(list string) []any {
	list = strings.ReplaceAll(strings.TrimSpace(list), " ", "")
	if !rePortList.MatchString(list) {
		return nil
	}
	var out []any
	for _, item := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == '/' }) {
		start, end, isRange := strings.Cut(item, "-")
		if !isRange {
			end = start
		}
		a, b := validPort(start), validPort(end)
		if a == 0 || b == 0 || a > b {
			return nil
		}
		out = append(out, strconv.Itoa(a)+":"+strconv.Itoa(b))
	}
	return out
}

// splitHopPorts takes the port hopping list out of a Hysteria 2 link's
// authority (hysteria2://auth@host:443,20000-30000/), which no URL
// parser reads: the link keeps the first port, and the list becomes
// server_ports.
func splitHopPorts(uri string) (string, []any) {
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri, nil
	}
	start := i + 3
	end := len(uri)
	if j := strings.IndexAny(uri[start:], "/?#"); j >= 0 {
		end = start + j
	}
	authority := uri[start:end]
	hostStart := strings.LastIndex(authority, "@") + 1
	colon := strings.LastIndex(authority, ":")
	if colon < hostStart || strings.Contains(authority[colon:], "]") {
		return uri, nil
	}
	list := authority[colon+1:]
	if !strings.ContainsAny(list, ",-") {
		return uri, nil
	}
	ranges := portRanges(list)
	if ranges == nil {
		return uri, nil
	}
	first, _, _ := strings.Cut(ranges[0].(string), ":")
	return uri[:start+colon+1] + first + uri[end:], ranges
}

var reRocketVless = regexp.MustCompile(`^(?:[^:@\s]+:)?([^@:\s]+)@(\[[^\]]+\]|[^:\s]+):(\d+)$`)

// parseShadowrocketBase64Vless reads vless://Base64(auto:UUID@host:port).
func parseShadowrocketBase64Vless(uri string) (Node, bool) {
	payload := uri[len("vless://"):]
	name := ""
	if h := strings.Index(payload, "#"); h >= 0 {
		name, payload = dec(payload[h+1:]), payload[:h]
	}
	q := ""
	if i := strings.Index(payload, "?"); i >= 0 {
		q, payload = payload[i+1:], payload[:i]
	}
	if payload == "" || strings.Contains(payload, "@") {
		return Node{}, false
	}
	decoded, err := B64Decode(payload)
	if err != nil {
		return Node{}, false
	}
	m := reRocketVless.FindStringSubmatch(decoded)
	if m == nil {
		return Node{}, false
	}
	uuid, host := m[1], trimBrackets(m[2])
	port, _ := strconv.Atoi(m[3])
	if uuid == "" || host == "" || port < 1 || port > 65535 {
		return Node{}, false
	}
	fields := map[string]any{"server": host, "server_port": float64(port), "uuid": uuid}
	if q != "" {
		fillTLSTransport(parseQuery(q), host, fields, false)
	}
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return Node{Type: "vless", Name: name, Fields: fields, Key: nodeKey("vless", host, port, fields)}, true
}

// ParseLink reads a share link: ss, vmess, vless (also Shadowrocket's
// Base64 form), trojan, hysteria2 (hy2), tuic and anytls.
func ParseLink(line string) (Node, bool) {
	s := strings.TrimSpace(line)
	switch {
	case s == "":
		return Node{}, false
	case strings.HasPrefix(s, "ss://"):
		return parseSS(s)
	case strings.HasPrefix(s, "vmess://"):
		return parseVmess(s)
	case strings.HasPrefix(s, "vless://"):
		if n, ok := parseURLStyle(s, "vless"); ok {
			return n, true
		}
		return parseShadowrocketBase64Vless(s)
	case strings.HasPrefix(s, "trojan://"):
		return parseURLStyle(s, "trojan")
	case strings.HasPrefix(s, "hysteria2://"):
		return parseURLStyle(s, "hysteria2")
	case strings.HasPrefix(s, "hy2://"):
		return parseURLStyle("hysteria2:"+s[len("hy2:"):], "hysteria2")
	case strings.HasPrefix(s, "tuic://"):
		return parseURLStyle(s, "tuic")
	case strings.HasPrefix(s, "anytls://"):
		return parseURLStyle(s, "anytls")
	}
	return Node{}, false
}

var (
	reHasScheme  = regexp.MustCompile(`(?i)(^|\n)\s*[a-z0-9]+://`)
	reLineScheme = regexp.MustCompile(`(?i)^[a-z0-9]+://`)
	reNewline    = regexp.MustCompile(`\r?\n`)
)

// ParseSubscription parses a whole body of share links, possibly Base64
// as a whole, or a sing-box configuration (parseSubscription).
func ParseSubscription(text string) Result {
	t := strings.TrimSpace(text)
	if t == "" {
		return Result{}
	}
	if !reHasScheme.MatchString(t) && looksBase64(t) {
		if decoded, err := B64Decode(t); err == nil {
			t = decoded
		}
	}
	if r, ok := ParseSingBoxConfig(t); ok {
		return r
	}
	var result Result
	for _, line := range reNewline.Split(t, -1) {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}
		if n, ok := ParseLink(s); ok {
			result.Nodes = append(result.Nodes, n)
		} else if reLineScheme.MatchString(s) {
			result.Skipped++
		}
	}
	return result
}
