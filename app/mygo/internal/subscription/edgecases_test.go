package subscription

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustLink(t *testing.T, link string) Node {
	t.Helper()
	n, ok := ParseLink(link)
	if !ok {
		t.Fatalf("%s does not parse", link)
	}
	return n
}

// convertLink converts one share link and reads it back as a node.
func convertLink(t *testing.T, link string) Node {
	t.Helper()
	_, result := convertNodes(t, link)
	if len(result.Nodes) != 1 {
		t.Fatalf("%s: nodes %+v", link, result.Nodes)
	}
	return result.Nodes[0]
}

func TestParseSSHostPort(t *testing.T) {
	for _, link := range []string{
		"ss://" + b64("aes-256-gcm:pw") + "@1.2.3.4#no-port",
		"ss://" + b64("aes-256-gcm:pw@1.2.3.4") + "#legacy-no-port",
		"ss://" + b64("aes-256-gcm:pw") + "@[::1]#v6-no-port",
		"ss://" + b64("aes-256-gcm:pw") + "@h.example:99999#big-port",
		"ss://" + b64("aes-256-gcm:pw") + "@h.example:443x#bad-port",
		"ss://" + b64("pw") + "@h.example:8388#no-method",
	} {
		if n, ok := ParseLink(link); ok {
			t.Errorf("%s parsed as %+v", link, n.Fields)
		}
		if p, _ := explodeLink(link); p != nil {
			if o, ok := p.toSingBox(); ok {
				t.Errorf("%s converted to %v", link, o)
			}
		}
	}
	n := mustLink(t, "ss://"+b64("aes-256-gcm:pw")+"@[2001:db8::1]:8388/?plugin=obfs-local%3Bobfs%3Dhttp#v6")
	if n.Fields["server"] != "2001:db8::1" || n.Fields["server_port"] != 8388.0 || n.Fields["plugin"] != "obfs-local" {
		t.Errorf("v6 %+v", n.Fields)
	}
}

func TestParseSSPercentEncodedUserinfo(t *testing.T) {
	n := mustLink(t, "ss://2022-blake3-aes-128-gcm:YWJj%2BZGVm%3D@h.example:8388#SIP022")
	if n.Fields["method"] != "2022-blake3-aes-128-gcm" || n.Fields["password"] != "YWJj+ZGVm=" {
		t.Errorf("SIP022 %+v", n.Fields)
	}
	n = mustLink(t, "ss://"+strings.ReplaceAll(b64("aes-128-gcm:p"), "=", "%3D")+"@h.example:8388#padding")
	if n.Fields["method"] != "aes-128-gcm" || n.Fields["password"] != "p" {
		t.Errorf("encoded padding %+v", n.Fields)
	}
}

func TestParseTrojanDefaultTLSOptions(t *testing.T) {
	for _, link := range []string{
		"trojan://pw@t.example:443?sni=s.example&allowInsecure=1&alpn=h2,http/1.1&fp=chrome#T",
		"trojan://pw@t.example:443?security=tls&sni=s.example&allowInsecure=true&alpn=h2,http/1.1&fp=chrome#T",
	} {
		for _, n := range []Node{mustLink(t, link), convertLink(t, link)} {
			f := n.Fields
			if f["tls.enabled"] != true || f["tls.server_name"] != "s.example" || f["tls.insecure"] != true ||
				!reflect.DeepEqual(f["tls.alpn"], []any{"h2", "http/1.1"}) || f["tls.utls.fingerprint"] != "chrome" {
				t.Errorf("%s → %+v", link, f)
			}
		}
	}
	if f := mustLink(t, "trojan://pw@t.example:80?security=none#plain").Fields; f["tls.enabled"] != nil {
		t.Errorf("security=none %+v", f)
	}
}

func TestParseInsecureSpellings(t *testing.T) {
	vmess := "vmess://" + b64(`{"v":"2","ps":"v","add":"v.example","port":"443","id":"u","net":"tcp","tls":"tls","allowInsecure":"true"}`)
	for _, link := range []string{
		"vless://u@v.example:443?security=tls&allowInsecure=true#a",
		"vless://u@v.example:443?security=tls&insecure=true#b",
		"hy2://pw@h.example:443?insecure=true#c",
		"tuic://u:p@h.example:443?allow_insecure=true#d",
		"anytls://pw@h.example:443?insecure=true#e",
		vmess,
	} {
		if f := mustLink(t, link).Fields; f["tls.insecure"] != true {
			t.Errorf("%s → %+v", link, f)
		}
	}
	if n := convertLink(t, "hysteria://h.example:443?auth=a&insecure=true&upmbps=10&downmbps=10#h"); n.Fields["tls.insecure"] != true {
		t.Errorf("hysteria %+v", n.Fields)
	}
}

func TestParseHysteria2PortHopping(t *testing.T) {
	want := []any{"443:443", "20000:30000"}
	for _, link := range []string{
		"hysteria2://pw@h.example:443,20000-30000/?sni=s.example&obfs=salamander&obfs-password=op#hop",
		"hy2://pw@h.example:443?mport=443,20000-30000&sni=s.example&obfs=salamander&obfs-password=op#hop",
	} {
		for _, n := range []Node{mustLink(t, link), convertLink(t, link)} {
			f := n.Fields
			if f["server_port"] != 443.0 || !reflect.DeepEqual(f["server_ports"], want) || f["tls.server_name"] != "s.example" ||
				f["obfs.password"] != "op" || n.Name != "hop" {
				t.Errorf("%s → %+v", link, f)
			}
		}
	}
	_, result := convertNodes(t, "proxies:\n  - {name: h, type: hysteria2, server: h.example, port: 443, ports: '443,20000-30000', password: pw}\n")
	if f := result.Nodes[0].Fields; !reflect.DeepEqual(f["server_ports"], want) {
		t.Errorf("clash ports %+v", f)
	}
	link, _ := FormatOutboundLink(outbound("hysteria2", map[string]any{"server": "h.example", "server_port": 443.0,
		"password": "pw", "server_ports": want}))
	if f := mustLink(t, link).Fields; !reflect.DeepEqual(f["server_ports"], want) {
		t.Errorf("%s → %+v", link, f)
	}
}

func TestParseQUICLinkTLS(t *testing.T) {
	for _, link := range []string{
		"hy2://pw@h.example:443?alpn=h3#a",
		"tuic://u:p@h.example:443?alpn=h3&congestion_control=bbr#b",
		"anytls://pw@h.example:443?alpn=h3&fp=chrome#c",
	} {
		f := mustLink(t, link).Fields
		if !reflect.DeepEqual(f["tls.alpn"], []any{"h3"}) {
			t.Errorf("%s → %+v", link, f)
		}
	}
	if f := mustLink(t, "anytls://pw@h.example:443?fp=chrome#c").Fields; f["tls.utls.fingerprint"] != "chrome" {
		t.Errorf("anytls fp %+v", f)
	}
}

func TestParseVmessHTTPUpgrade(t *testing.T) {
	link := "vmess://" + b64(`{"v":"2","ps":"hu","add":"v.example","port":443,"id":"u","net":"httpupgrade","host":"cdn.example","path":"/up","tls":""}`)
	for _, n := range []Node{mustLink(t, link), convertLink(t, link)} {
		if f := n.Fields; f["transport.type"] != "httpupgrade" || f["transport.path_hu"] != "/up" || f["transport.host_hu"] != "cdn.example" {
			t.Errorf("%+v", f)
		}
	}
}

func TestConvertHostsRewriteServersOnly(t *testing.T) {
	body := `hosts:
  a.example: 192.0.2.1
  '+.wild.example': 192.0.2.2
proxies:
  - {name: a.example node, type: trojan, server: a.example, port: 443, password: a.example, sni: a.example}
  - {name: cdn, type: trojan, server: cdn.a.example, port: 443, password: pw}
  - {name: wild, type: trojan, server: x.wild.example, port: 443, password: pw}
`
	_, result := convertNodes(t, body)
	a, cdn, wild := result.Nodes[0], result.Nodes[1], result.Nodes[2]
	if a.Fields["server"] != "192.0.2.1" || a.Fields["tls.server_name"] != "a.example" || a.Fields["password"] != "a.example" ||
		a.Name != "a.example node" {
		t.Errorf("a %s %+v", a.Name, a.Fields)
	}
	if cdn.Fields["server"] != "cdn.a.example" {
		t.Errorf("cdn %+v", cdn.Fields)
	}
	if wild.Fields["server"] != "192.0.2.2" {
		t.Errorf("wild %+v", wild.Fields)
	}
	conv, err := Convert(`{"hosts":{"q.example":"a\"b"},"outbounds":[{"type":"trojan","tag":"q","server":"q.example","server_port":443,"password":"p"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := ParseSingBoxConfig(conv.Config); !ok || r.Nodes[0].Fields["server"] != `a"b` {
		t.Errorf("quoted target %s", conv.Config)
	}
}

func TestUniqueTagsPastTakenTags(t *testing.T) {
	outbounds := []any{map[string]any{"tag": "A"}, map[string]any{"tag": "A"}, map[string]any{"tag": "A 2"}}
	uniqueTags(outbounds)
	var tags []string
	for _, o := range outbounds {
		tags = append(tags, o.(map[string]any)["tag"].(string))
	}
	if !reflect.DeepEqual(tags, []string{"A", "A 2", "A 2 2"}) {
		t.Errorf("tags %v", tags)
	}
}

func TestFormatShadowsocksUserinfo(t *testing.T) {
	// "aes-128-gcm:>>>?" encodes to Base64 with "+" and "/".
	link, _ := FormatOutboundLink(outbound("shadowsocks", map[string]any{
		"server": "ss.example", "server_port": 8388.0, "method": "aes-128-gcm", "password": ">>>??"}))
	userinfo := strings.TrimPrefix(link[:strings.Index(link, "@")], "ss://")
	if strings.ContainsAny(userinfo, "+/=") {
		t.Errorf("userinfo %q is not URL-safe", userinfo)
	}
	if f := mustLink(t, link).Fields; f["password"] != ">>>??" {
		t.Errorf("%s → %+v", link, f)
	}
	link, _ = FormatOutboundLink(outbound("shadowsocks", map[string]any{
		"server": "ss.example", "server_port": 8388.0, "method": "2022-blake3-aes-128-gcm", "password": "YWJj+ZGVm/w=="}))
	if !strings.HasPrefix(link, "ss://2022-blake3-aes-128-gcm:YWJj%2BZGVm%2Fw%3D%3D@") {
		t.Errorf("SIP022 link %s", link)
	}
	if f := mustLink(t, link).Fields; f["password"] != "YWJj+ZGVm/w==" {
		t.Errorf("%s → %+v", link, f)
	}
}

func TestClashWireGuardReservedBase64(t *testing.T) {
	_, result := convertNodes(t, "proxies:\n  - {name: wg, type: wireguard, server: wg.example, port: 51820, ip: 10.0.0.2, private-key: pk, public-key: pub, reserved: AQID}\n")
	if f := result.Nodes[0].Fields; !reflect.DeepEqual(f["reserved"], []any{1.0, 2.0, 3.0}) {
		t.Errorf("reserved %+v", f)
	}
}

func TestConvertSIP008(t *testing.T) {
	body := `{"version": 1, "servers": [
		{"id": "1", "remarks": "SIP008", "server": "ss.example", "server_port": 8388, "password": "pw", "method": "aes-256-gcm",
			"plugin": "obfs-local", "plugin_opts": "obfs=http"},
		{"server": "ss2.example", "server_port": 8389, "password": "pw2", "method": "chacha20-ietf-poly1305"}]}`
	for _, content := range []string{body, base64.RawURLEncoding.EncodeToString([]byte(body))} {
		conv, result := convertNodes(t, content)
		if conv.Converted != 2 || len(result.Nodes) != 2 {
			t.Fatalf("conv %+v", conv)
		}
		n := result.Nodes[0]
		if n.Name != "SIP008" || n.Type != "shadowsocks" || n.Fields["server_port"] != 8388.0 || n.Fields["plugin_opts"] != "obfs=http" {
			t.Errorf("node %+v", n)
		}
		if result.Nodes[1].Name != "ss2.example:8389" {
			t.Errorf("second %+v", result.Nodes[1])
		}
	}
}

func TestParsePathsDecodeOnce(t *testing.T) {
	f := mustLink(t, "vless://u@v.example:443?type=ws&path=%2Fa%2525b&security=tls#a").Fields
	if f["transport.path"] != "/a%25b" {
		t.Errorf("ws path %v", f["transport.path"])
	}
	f = mustLink(t, "vless://u@v.example:443?type=grpc&serviceName=svc%2541&security=tls#g").Fields
	if f["transport.service_name"] != "svc%41" {
		t.Errorf("grpc %v", f["transport.service_name"])
	}
}

func TestParseLinkKeysIncludeCredential(t *testing.T) {
	a := mustLink(t, "ss://"+b64("aes-256-gcm:one")+"@h.example:8388#a")
	b := mustLink(t, "ss://"+b64("aes-256-gcm:two")+"@h.example:8388#b")
	if a.Key == b.Key || a.Key != "shadowsocks|h.example|8388|one" {
		t.Errorf("keys %q %q", a.Key, b.Key)
	}
	v1 := mustLink(t, "vless://u1@v.example:443#a")
	v2 := mustLink(t, "vless://u2@v.example:443#b")
	if v1.Key == v2.Key || v1.Key != "vless|v.example|443|u1" {
		t.Errorf("vless keys %q %q", v1.Key, v2.Key)
	}
}

func TestParseWebSocketEarlyData(t *testing.T) {
	vmess := "vmess://" + b64(`{"v":"2","ps":"v","add":"v.example","port":443,"id":"u","net":"ws","path":"/ws?ed=2048"}`)
	for _, link := range []string{
		"vless://u@v.example:443?type=ws&path=%2Fws%3Fed%3D2048&security=tls#a",
		vmess,
	} {
		for _, n := range []Node{mustLink(t, link), convertLink(t, link)} {
			f := n.Fields
			if f["transport.path"] != "/ws" || f["transport.max_early_data"] != 2048.0 ||
				f["transport.early_data_header_name"] != "Sec-WebSocket-Protocol" {
				t.Errorf("%s → %+v", link, f)
			}
		}
	}
	_, result := convertNodes(t, "proxies:\n  - {name: w, type: vmess, server: w.example, port: 443, uuid: u, alterId: 0, cipher: auto, network: ws, ws-opts: {path: \"/p?x=1&ed=2560\"}}\n")
	if f := result.Nodes[0].Fields; f["transport.path"] != "/p?x=1" || f["transport.max_early_data"] != 2560.0 {
		t.Errorf("clash %+v", f)
	}
}
