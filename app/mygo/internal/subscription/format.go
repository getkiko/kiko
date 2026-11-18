package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

func linkEndpoint(fields map[string]any) (string, int, bool) {
	host := jsv.Text(fields["server"])
	port := jsv.NumberOrNaN(fields["server_port"])
	if host == "" || !jsv.IsInteger(port) || port < 1 || port > 65535 {
		return "", 0, false
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host, int(port), true
}

func linkQuery(fields map[string]any, includeTransport bool) query {
	q := query{}
	set := func(key string, value any) {
		if value != nil && jsv.String(value) != "" {
			q.set(key, jsv.String(value))
		}
	}
	tls := fields["tls.enabled"] == true
	reality := fields["tls.reality.enabled"] == true
	if reality {
		q.set("security", "reality")
		set("pbk", fields["tls.reality.public_key"])
		set("sid", fields["tls.reality.short_id"])
	} else if tls {
		q.set("security", "tls")
	}
	if tls || reality {
		set("sni", fields["tls.server_name"])
		if fields["tls.insecure"] == true {
			q.set("insecure", "1")
		}
		if alpn, ok := fields["tls.alpn"].([]any); ok && len(alpn) > 0 {
			q.set("alpn", joinValues(alpn))
		}
		if fields["tls.utls.enabled"] == true {
			set("fp", fields["tls.utls.fingerprint"])
		}
	}
	if !includeTransport {
		return q
	}
	transport := jsv.StringOr(fields["transport.type"], "tcp")
	if transport != "" && transport != "tcp" {
		q.set("type", transport)
	}
	switch transport {
	case "ws":
		set("path", fields["transport.path"])
		set("host", transportHeader(fields, "Host"))
	case "grpc":
		set("serviceName", fields["transport.service_name"])
	case "http":
		if hosts, ok := fields["transport.host"].([]any); ok && len(hosts) > 0 {
			q.set("host", joinValues(hosts))
		}
		set("path", fields["transport.path_http"])
	case "httpupgrade":
		set("path", fields["transport.path_hu"])
		set("host", fields["transport.host_hu"])
	}
	return q
}

func joinValues(values []any) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = jsv.String(v)
	}
	return strings.Join(parts, ",")
}

func withQuery(base string, q query, tag string) string {
	search := q.String()
	if search != "" {
		search = "?" + search
	}
	return base + search + "#" + encodeURIComponent(tag)
}

func b64encode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// FormatOutboundLink returns the share link of an outbound that other
// clients read (Shadowsocks, VMess, VLESS, Trojan, Hysteria2, TUIC,
// AnyTLS); ok is false when its type has none or fields are missing.
func FormatOutboundLink(o *model.Outbound) (string, bool) {
	host, port, ok := linkEndpoint(o.Fields)
	if !ok {
		return "", false
	}
	fields := o.Fields
	required := func(key string) string { return jsv.Text(fields[key]) }
	hp := host + ":" + strconv.Itoa(port)
	switch o.Type {
	case "shadowsocks":
		method, password := required("method"), required("password")
		if method == "" || password == "" {
			return "", false
		}
		q := query{}
		if plugin := required("plugin"); plugin != "" {
			parts := []string{plugin}
			if opts := required("plugin_opts"); opts != "" {
				parts = append(parts, opts)
			}
			q.set("plugin", strings.Join(parts, ";"))
		}
		// SIP002 userinfo: URL-safe Base64, or percent-encoded for the
		// 2022 ciphers (SIP022). The former TypeScript parser wrote
		// standard Base64, whose "/" and "+" URL parsers read as a path
		// and a space.
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
		if strings.HasPrefix(method, "2022-") {
			userinfo = encodeURIComponent(method) + ":" + encodeURIComponent(password)
		}
		return withQuery("ss://"+userinfo+"@"+hp, q, o.Tag), true
	case "vmess":
		uuid := required("uuid")
		if uuid == "" {
			return "", false
		}
		q := linkQuery(fields, true)
		transport := jsv.StringOr(fields["transport.type"], "tcp")
		transportHost, transportPath := "", ""
		switch transport {
		case "ws":
			transportHost, transportPath = transportHeader(fields, "Host"), required("transport.path")
		case "grpc":
			transportPath = required("transport.service_name")
		case "http":
			if hosts, ok := fields["transport.host"].([]any); ok {
				transportHost = joinValues(hosts)
			}
			transportPath = required("transport.path_http")
		}
		tls := ""
		if q.get("security") == "tls" {
			tls = "tls"
		}
		pairs := [][2]string{
			{"v", "2"}, {"ps", o.Tag}, {"add", trimBrackets(host)}, {"port", strconv.Itoa(port)}, {"id", uuid},
			{"aid", jsv.StringOr(fields["alter_id"], "0")}, {"scy", firstNonEmpty(required("security"), "auto")},
			{"net", transport}, {"type", "none"}, {"host", transportHost}, {"path", transportPath},
			{"tls", tls}, {"sni", q.get("sni")}, {"alpn", q.get("alpn")}, {"fp", q.get("fp")},
		}
		if q.get("insecure") == "1" {
			pairs = append(pairs, [2]string{"allowInsecure", "1"})
		}
		return "vmess://" + b64encode(orderedJSON(pairs)), true
	case "vless":
		uuid := required("uuid")
		if uuid == "" {
			return "", false
		}
		q := linkQuery(fields, true)
		q.set("encryption", "none")
		if !q.has("security") {
			q.set("security", "none")
		}
		if flow := required("flow"); flow != "" {
			q.set("flow", flow)
		}
		return withQuery("vless://"+encodeURIComponent(uuid)+"@"+hp, q, o.Tag), true
	case "trojan":
		password := required("password")
		if password == "" {
			return "", false
		}
		q := linkQuery(fields, true)
		if !q.has("security") {
			q.set("security", "none")
		}
		return withQuery("trojan://"+encodeURIComponent(password)+"@"+hp, q, o.Tag), true
	case "hysteria2":
		password := required("password")
		if password == "" {
			return "", false
		}
		q := linkQuery(fields, false)
		q.del("security")
		if obfs := required("obfs.type"); obfs != "" {
			q.set("obfs", obfs)
		}
		if pw := required("obfs.password"); pw != "" {
			q.set("obfs-password", pw)
		}
		// Port hopping, as v2rayN writes it ("mport=20000-30000").
		if ports, ok := fields["server_ports"].([]any); ok && len(ports) > 0 {
			hops := make([]string, 0, len(ports))
			for _, r := range ports {
				start, end, _ := strings.Cut(jsv.String(r), ":")
				if end == "" || end == start {
					hops = append(hops, start)
				} else {
					hops = append(hops, start+"-"+end)
				}
			}
			q.set("mport", strings.Join(hops, ","))
		}
		return withQuery("hy2://"+encodeURIComponent(password)+"@"+hp, q, o.Tag), true
	case "tuic":
		uuid, password := required("uuid"), required("password")
		if uuid == "" || password == "" {
			return "", false
		}
		q := linkQuery(fields, false)
		q.del("security")
		if cc := required("congestion_control"); cc != "" {
			q.set("congestion_control", cc)
		}
		if urm := required("udp_relay_mode"); urm != "" {
			q.set("udp_relay_mode", urm)
		}
		return withQuery("tuic://"+encodeURIComponent(uuid)+":"+encodeURIComponent(password)+"@"+hp, q, o.Tag), true
	case "anytls":
		password := required("password")
		if password == "" {
			return "", false
		}
		q := linkQuery(fields, false)
		q.del("security")
		return withQuery("anytls://"+encodeURIComponent(password)+"@"+hp, q, o.Tag), true
	}
	return "", false
}

// orderedJSON writes an object of strings in the pairs' order, as
// JSON.stringify writes an object literal.
func orderedJSON(pairs [][2]string) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, p[0])
		b.WriteByte(':')
		writeJSONString(&b, p[1])
	}
	b.WriteByte('}')
	return b.String()
}

func writeJSONString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Truncate(b.Len() - 1) // the encoder's newline
}
