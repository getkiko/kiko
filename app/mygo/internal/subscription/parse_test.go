package subscription

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// jsonValue normalizes a literal as JSON decoding does (numbers float64).
func jsonValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestParseSingBoxOutbounds(t *testing.T) {
	config, _ := json.Marshal(map[string]any{"outbounds": []any{
		map[string]any{"type": "direct", "tag": "DIRECT"},
		map[string]any{"type": "vless", "tag": "vless-node", "server": "vless.example", "server_port": 443,
			"uuid": "00000000-0000-0000-0000-000000000000", "network": "tcp",
			"tls": map[string]any{"enabled": true, "server_name": "sni.example", "insecure": true,
				"utls": map[string]any{"enabled": true, "fingerprint": "chrome"}},
			"transport": map[string]any{"type": "ws", "path": "/ws", "headers": map[string]any{"Host": "host.example"}}},
		map[string]any{"type": "shadowsocks", "tag": "ss-node", "server": "ss.example", "server_port": 8388,
			"method": "aes-256-gcm", "password": "secret", "network": "tcp"},
		map[string]any{"type": "hysteria2", "tag": "hy2-node", "server": "hy2.example", "server_port": 443,
			"password": "secret", "obfs": map[string]any{"type": "salamander", "password": "obfs-secret"}},
		map[string]any{"type": "wireguard", "tag": "wg-node", "local_address": []any{"10.0.0.2/32"},
			"private_key": "private-key", "mtu": 1408,
			"peers": []any{map[string]any{"server": "wg.example", "server_port": 51820, "public_key": "public-key",
				"pre_shared_key": "pre-shared", "reserved": []any{1, 2, 3}}}},
		map[string]any{"type": "selector", "tag": "PROXY", "outbounds": []any{"vless-node"}},
	}})
	result := ParseSubscription(string(config))
	if len(result.Nodes) != 4 || result.Skipped != 0 {
		t.Fatalf("nodes %d skipped %d", len(result.Nodes), result.Skipped)
	}
	want := Node{Type: "vless", Name: "vless-node", Key: "vless|vless.example|443|00000000-0000-0000-0000-000000000000",
		Fields: jsonValue(t, map[string]any{
			"server": "vless.example", "server_port": 443, "uuid": "00000000-0000-0000-0000-000000000000",
			"network": []any{"tcp"}, "tls.enabled": true, "tls.server_name": "sni.example", "tls.insecure": true,
			"tls.utls.enabled": true, "tls.utls.fingerprint": "chrome", "transport.type": "ws", "transport.path": "/ws",
			"transport.headers": map[string]any{"Host": "host.example"},
		}).(map[string]any)}
	if !reflect.DeepEqual(result.Nodes[0], want) {
		t.Errorf("vless\n got %#v\nwant %#v", result.Nodes[0], want)
	}
	if f := result.Nodes[2].Fields; f["obfs.type"] != "salamander" || f["obfs.password"] != "obfs-secret" {
		t.Errorf("hysteria2 obfs %v", f)
	}
	wantWG := Node{Type: "wireguard", Name: "wg-node", Key: "wireguard|wg.example|51820|private-key",
		Fields: jsonValue(t, map[string]any{
			"server": "wg.example", "server_port": 51820, "local_address": []any{"10.0.0.2/32"},
			"private_key": "private-key", "mtu": 1408, "peer_public_key": "public-key",
			"pre_shared_key": "pre-shared", "reserved": []any{1, 2, 3},
		}).(map[string]any)}
	if !reflect.DeepEqual(result.Nodes[3], wantWG) {
		t.Errorf("wireguard\n got %#v\nwant %#v", result.Nodes[3], wantWG)
	}
}

func TestParseConvertedSSRAndHysteria(t *testing.T) {
	config, _ := json.Marshal(map[string]any{"outbounds": []any{
		map[string]any{"type": "shadowsocksr", "tag": "ssr-node", "server": "ssr.example", "server_port": 8388,
			"method": "aes-256-cfb", "password": "secret", "protocol": "auth_aes128_md5",
			"protocol_param": "param", "obfs": "tls1.2_ticket_auth", "obfs_param": "cdn.example"},
		map[string]any{"type": "hysteria", "tag": "hy-node", "server": "hy.example", "server_port": 443,
			"up_mbps": 20, "down_mbps": 100, "obfs": "secret", "auth_str": "password",
			"tls": map[string]any{"enabled": true, "insecure": true}},
	}})
	result := ParseSubscription(string(config))
	if len(result.Nodes) != 2 || result.Skipped != 0 {
		t.Fatalf("nodes %d skipped %d", len(result.Nodes), result.Skipped)
	}
	if n := result.Nodes[0]; n.Type != "shadowsocksr" || n.Fields["protocol"] != "auth_aes128_md5" {
		t.Errorf("ssr %+v", n)
	}
	if n := result.Nodes[1]; n.Type != "hysteria" || n.Fields["auth_str"] != "password" || n.Fields["tls.insecure"] != true {
		t.Errorf("hysteria %+v", n)
	}
}

func TestParseFoldedBase64(t *testing.T) {
	uri := "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls#node"
	encoded := base64.StdEncoding.EncodeToString([]byte(uri))
	result := ParseSubscription(encoded[:32] + "\n" + encoded[32:])
	if len(result.Nodes) != 1 || result.Nodes[0].Type != "vless" {
		t.Fatalf("result %+v", result)
	}
}

func TestParseShadowrocketReality(t *testing.T) {
	n, ok := ParseLink("vless://00000000-0000-0000-0000-000000000000@vless.example:443?tls=1&peer=music.apple.com&udp=3&xtls=2&pbk=V7AAP7PV-VnbuUujjTIjFd4a7OJodWfN5yb_uco2uX0&sid=4d334e32e512babd")
	if !ok || n.Type != "vless" {
		t.Fatalf("parsed %v %+v", ok, n)
	}
	f := n.Fields
	checks := map[string]any{
		"tls.enabled": true, "tls.server_name": "music.apple.com", "tls.reality.enabled": true,
		"tls.reality.public_key": "V7AAP7PV-VnbuUujjTIjFd4a7OJodWfN5yb_uco2uX0", "tls.reality.short_id": "4d334e32e512babd",
		"tls.utls.enabled": true, "tls.utls.fingerprint": "chrome",
	}
	for k, v := range checks {
		if f[k] != v {
			t.Errorf("%s = %v, want %v", k, f[k], v)
		}
	}
	if !reflect.DeepEqual(f["network"], []any{"tcp", "udp"}) {
		t.Errorf("network %v", f["network"])
	}
}

func TestParseShadowrocketBase64Vless(t *testing.T) {
	n, ok := ParseLink("vless://YXV0bzoxNDc3NTQwNC1iMzgxLTQxOTYtOWVmZS1mM2FjNTk3N2ExZGZAMTQ0LjM0LjIyOS4yMTc6NDQz")
	if !ok || n.Type != "vless" || n.Fields["uuid"] != "14775404-b381-4196-9efe-f3ac5977a1df" ||
		n.Fields["server"] != "144.34.229.217" || n.Fields["server_port"] != 443.0 {
		t.Fatalf("parsed %v %+v", ok, n)
	}
}

func outbound(typ string, fields map[string]any) *model.Outbound {
	return &model.Outbound{Entity: model.Entity{ID: "out_test", Tag: "测试节点", Enabled: true}, Kind: "outbound", Type: typ, Fields: fields}
}

func TestFormatVlessRoundTrip(t *testing.T) {
	source := outbound("vless", map[string]any{
		"server": "vless.example", "server_port": 443.0, "uuid": "00000000-0000-0000-0000-000000000000",
		"flow": "xtls-rprx-vision", "tls.enabled": true, "tls.server_name": "sni.example",
		"tls.utls.enabled": true, "tls.utls.fingerprint": "chrome", "transport.type": "ws", "transport.path": "/edge",
		"transport.headers": map[string]any{"Host": "ws.example", "X-Test": "enabled"},
	})
	link, ok := FormatOutboundLink(source)
	if !ok {
		t.Fatal("no link")
	}
	n, ok := ParseLink(link)
	if !ok || n.Type != "vless" || n.Name != "测试节点" || n.Fields["uuid"] != source.Fields["uuid"] ||
		n.Fields["tls.server_name"] != "sni.example" || n.Fields["transport.path"] != "/edge" {
		t.Fatalf("%s → %+v", link, n)
	}
	if !reflect.DeepEqual(n.Fields["transport.headers"], map[string]any{"Host": "ws.example"}) {
		t.Errorf("headers %v", n.Fields["transport.headers"])
	}
}

func TestFormatShadowsocksAndVmess(t *testing.T) {
	link, _ := FormatOutboundLink(outbound("shadowsocks", map[string]any{
		"server": "ss.example", "server_port": 8388.0, "method": "aes-256-gcm", "password": "secret"}))
	ss, ok := ParseLink(link)
	if !ok || ss.Type != "shadowsocks" || ss.Fields["method"] != "aes-256-gcm" || ss.Fields["password"] != "secret" {
		t.Errorf("%s → %+v", link, ss)
	}
	link, _ = FormatOutboundLink(outbound("vmess", map[string]any{
		"server": "vmess.example", "server_port": 443.0, "uuid": "00000000-0000-0000-0000-000000000000",
		"security": "auto", "tls.enabled": true, "tls.server_name": "sni.example",
		"transport.type": "grpc", "transport.service_name": "grpc-service"}))
	vmess, ok := ParseLink(link)
	if !ok || vmess.Type != "vmess" || vmess.Fields["uuid"] != "00000000-0000-0000-0000-000000000000" ||
		vmess.Fields["transport.service_name"] != "grpc-service" {
		t.Errorf("%s → %+v", link, vmess)
	}
}

func TestFormatRemainingProtocols(t *testing.T) {
	sources := []*model.Outbound{
		outbound("trojan", map[string]any{"server": "trojan.example", "server_port": 443.0, "password": "secret", "tls.enabled": true}),
		outbound("hysteria2", map[string]any{"server": "hy2.example", "server_port": 443.0, "password": "secret", "obfs.type": "salamander", "obfs.password": "obfs"}),
		outbound("tuic", map[string]any{"server": "tuic.example", "server_port": 443.0, "uuid": "00000000-0000-0000-0000-000000000000", "password": "secret"}),
		outbound("anytls", map[string]any{"server": "anytls.example", "server_port": 443.0, "password": "secret"}),
	}
	for _, source := range sources {
		link, ok := FormatOutboundLink(source)
		if !ok {
			t.Errorf("%s: no link", source.Type)
			continue
		}
		n, ok := ParseLink(link)
		if !ok || n.Type != source.Type || n.Fields["server"] != source.Fields["server"] || n.Fields["server_port"] != source.Fields["server_port"] {
			t.Errorf("%s → %+v", link, n)
		}
	}
	if _, ok := FormatOutboundLink(outbound("direct", map[string]any{})); ok {
		t.Error("direct has a link")
	}
}
