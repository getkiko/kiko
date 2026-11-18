package subscription

import (
	"encoding/base64"
	"strings"
	"testing"
)

func convertNodes(t *testing.T, body string) (Conversion, Result) {
	t.Helper()
	conv, err := Convert(body)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	result, ok := ParseSingBoxConfig(conv.Config)
	if !ok {
		t.Fatalf("config does not parse: %s", conv.Config)
	}
	return conv, result
}

// The cases of the former web console's libsubconverter-based converter.
func TestConvertClashYAML(t *testing.T) {
	conv, result := convertNodes(t, "proxies:\n  - { name: test, type: ss, server: example.com, port: 8388, cipher: aes-128-gcm, password: secret }")
	if conv.Parsed != 1 || conv.Converted != 1 || !strings.Contains(conv.Config, `"type": "shadowsocks"`) {
		t.Fatalf("%+v", conv)
	}
	n := result.Nodes[0]
	if n.Name != "test" || n.Key != "shadowsocks|example.com|8388|secret" || n.Fields["method"] != "aes-128-gcm" {
		t.Errorf("node %+v", n)
	}
}

func TestConvertKeepsSingBoxJSON(t *testing.T) {
	conv, err := Convert(`{"outbounds":[{"type":"vless","tag":"test"}]}`)
	if err != nil || conv.Parsed != 1 || conv.Converted != 1 || conv.Skipped != 0 {
		t.Fatalf("%+v %v", conv, err)
	}
}

func TestConvertRewritesClashHosts(t *testing.T) {
	conv, _ := convertNodes(t, "hosts:\n  source.example: mapped.example\nproxies:\n  - { name: test, type: ss, server: source.example, port: 8388, cipher: aes-128-gcm, password: secret }")
	if !strings.Contains(conv.Config, "mapped.example") || strings.Contains(conv.Config, "source.example") {
		t.Errorf("config %s", conv.Config)
	}
}

func TestConvertRewritesSingBoxHosts(t *testing.T) {
	_, result := convertNodes(t, `{"hosts":{"source.example":"192.0.2.10"},"outbounds":[{"type":"shadowsocks","server":"source.example","server_port":8388}]}`)
	if len(result.Nodes) != 1 || result.Nodes[0].Fields["server"] != "192.0.2.10" {
		t.Errorf("nodes %+v", result.Nodes)
	}
}

func TestConvertEmptyAndUnknown(t *testing.T) {
	if _, err := Convert("  "); err == nil || err.Error() != "订阅正文为空" {
		t.Errorf("empty: %v", err)
	}
	if _, err := Convert("hello world"); err == nil || err.Error() != "没有识别到支持的订阅节点" {
		t.Errorf("unknown: %v", err)
	}
	if _, err := Convert("proxies:\n  - {name: s, type: snell, server: a.example, port: 1, psk: x}"); err == nil ||
		err.Error() != "订阅节点无法转换为 sing-box 出站" {
		t.Errorf("snell: %v", err)
	}
}

func TestConvertBlockClash(t *testing.T) {
	body := `mixed-port: 7890
proxies:
  - name: "香港 01"
    type: vmess
    server: hk.example
    port: 443
    uuid: 00000000-0000-0000-0000-000000000000
    alterId: 0
    cipher: auto
    tls: true
    servername: sni.example
    network: ws
    ws-opts:
      path: /ray # comment
      headers:
        Host: cdn.example
  - name: trojan
    type: trojan
    server: t.example
    port: 443
    password: '0123'
    sni: t.example
    skip-cert-verify: true
  - {name: hy2, type: hysteria2, server: h.example, port: 8443, password: pw, obfs: salamander, obfs-password: op, up: "30 Mbps"}
  - name: tuic
    type: tuic
    server: tuic.example
    port: 443
    uuid: 11111111-1111-1111-1111-111111111111
    password: tp
    alpn: [h3]
    congestion-controller: bbr
  - name: reality
    type: vless
    server: r.example
    port: 443
    uuid: 22222222-2222-2222-2222-222222222222
    flow: xtls-rprx-vision
    tls: true
    servername: www.apple.com
    client-fingerprint: safari
    reality-opts:
      public-key: PUBKEY
      short-id: abcd
  - name: snell
    type: snell
    server: s.example
    port: 1
proxy-groups:
  - name: PROXY
    type: select
    proxies: [香港 01, trojan]
`
	conv, result := convertNodes(t, body)
	if conv.Parsed != 6 || conv.Converted != 5 || conv.Skipped != 1 || len(result.Nodes) != 5 {
		t.Fatalf("conv %+v nodes %d", conv, len(result.Nodes))
	}
	vmess := result.Nodes[0]
	if vmess.Name != "香港 01" || vmess.Fields["transport.type"] != "ws" || vmess.Fields["transport.path"] != "/ray" ||
		vmess.Fields["tls.server_name"] != "sni.example" || vmess.Fields["security"] != "auto" {
		t.Errorf("vmess %+v", vmess.Fields)
	}
	if h, _ := vmess.Fields["transport.headers"].(map[string]any); h["Host"] != "cdn.example" {
		t.Errorf("vmess headers %v", vmess.Fields["transport.headers"])
	}
	trojan := result.Nodes[1]
	if trojan.Fields["password"] != "0123" || trojan.Fields["tls.insecure"] != true || trojan.Fields["tls.enabled"] != true {
		t.Errorf("trojan %+v", trojan.Fields)
	}
	hy2 := result.Nodes[2]
	if hy2.Fields["obfs.type"] != "salamander" || hy2.Fields["obfs.password"] != "op" || hy2.Fields["up_mbps"] != 30.0 {
		t.Errorf("hy2 %+v", hy2.Fields)
	}
	tuic := result.Nodes[3]
	if tuic.Fields["congestion_control"] != "bbr" || tuic.Fields["password"] != "tp" {
		t.Errorf("tuic %+v", tuic.Fields)
	}
	reality := result.Nodes[4]
	if reality.Fields["tls.reality.public_key"] != "PUBKEY" || reality.Fields["tls.utls.fingerprint"] != "safari" ||
		reality.Fields["flow"] != "xtls-rprx-vision" {
		t.Errorf("reality %+v", reality.Fields)
	}
}

func TestConvertShareLinks(t *testing.T) {
	ssr := "ssr://" + base64.RawURLEncoding.EncodeToString([]byte("ssr.example:8388:auth_aes128_md5:aes-256-cfb:tls1.2_ticket_auth:"+
		base64.RawURLEncoding.EncodeToString([]byte("secret"))+"/?remarks="+base64.RawURLEncoding.EncodeToString([]byte("SSR 节点"))))
	links := strings.Join([]string{
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw")) + "@ss.example:8388#SS",
		ssr,
		"trojan://pw@trojan.example:443?sni=t.example#Trojan",
		"hy2://pw@hy2.example:443?sni=h.example&obfs=salamander&obfs-password=op#HY2",
		"hysteria://hy.example:443?auth=secret&peer=p.example&upmbps=10&downmbps=50#HY",
		"socks5://user:pass@socks.example:1080#Socks",
		"wireguard://privkey@wg.example:51820?publickey=pub&address=10.0.0.2/32&mtu=1280#WG",
		"unknown://whatever",
	}, "\n")
	body := base64.StdEncoding.EncodeToString([]byte(links))
	conv, result := convertNodes(t, body)
	if conv.Parsed != 8 || conv.Converted != 7 || len(result.Nodes) != 7 {
		t.Fatalf("conv %+v nodes %+v", conv, result.Nodes)
	}
	byName := map[string]Node{}
	for _, n := range result.Nodes {
		byName[n.Name] = n
	}
	if n := byName["SSR 节点"]; n.Type != "shadowsocksr" || n.Fields["password"] != "secret" || n.Fields["protocol"] != "auth_aes128_md5" {
		t.Errorf("ssr %+v", n)
	}
	if n := byName["HY"]; n.Type != "hysteria" || n.Fields["auth_str"] != "secret" || n.Fields["up_mbps"] != 10.0 || n.Fields["tls.server_name"] != "p.example" {
		t.Errorf("hysteria %+v", n)
	}
	if n := byName["Socks"]; n.Type != "socks" || n.Fields["username"] != "user" || n.Fields["password"] != "pass" {
		t.Errorf("socks %+v", n)
	}
	if n := byName["WG"]; n.Type != "wireguard" || n.Fields["private_key"] != "privkey" || n.Fields["peer_public_key"] != "pub" || n.Key != "wireguard|wg.example|51820|privkey" {
		t.Errorf("wireguard %+v", n)
	}
	if n := byName["SS"]; n.Key != "shadowsocks|ss.example|8388|pw" {
		t.Errorf("ss key %q", n.Key)
	}
}

func TestYAMLSubset(t *testing.T) {
	v, err := parseYAML("a:\n  - x\n  - {k: 'it''s', n: [1, 2]}\nb: |\n  line1\n  line2\nc: \"q\\u00e9\" # c\n")
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	list := m["a"].([]any)
	if list[0] != yamlPlain("x") || list[1].(map[string]any)["k"] != "it's" {
		t.Errorf("a %#v", list)
	}
	if m["b"] != "line1\nline2\n" || m["c"] != "qé" {
		t.Errorf("b %q c %q", m["b"], m["c"])
	}
}
