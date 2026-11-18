package connections

import (
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

var sample = api.Connection{
	ID: "one", Inbound: "mixed-in", InboundType: "mixed", IPVersion: 4, Network: "tcp",
	Source: "127.0.0.1:5050", Destination: "1.2.3.4:443", Domain: "example.com", Protocol: "tls",
	CreatedAt: 1700000000000, UplinkTotal: 9007199254740993, DownlinkTotal: 2,
	Rule: "domain", Outbound: "proxy", OutboundType: "selector", ChainList: []string{"proxy", "node"},
}

func TestConnectionProtocolLabel(t *testing.T) {
	cases := []struct{ protocol, network, destination, want string }{
		{"tls", "tcp", "example.com:443", "HTTPS"},
		{"tls", "tcp", "[2001:db8::1]:853", "DoT"},
		{"tls", "tcp", "example.com:8443", "TLS"},
		{"http", "tcp", "1.2.3.4:443", "HTTP"},
		{"quic", "udp", "1.2.3.4:443", "QUIC"},
		{"dns", "udp", "1.2.3.4:443", "DNS"},
		{"stun", "udp", "1.2.3.4:443", "STUN"},
		{"ssh", "tcp", "1.2.3.4:443", "SSH"},
		{"bittorrent", "tcp", "1.2.3.4:443", "BitTorrent"},
		{"", "udp", "1.2.3.4:443", "UDP"},
	}
	for _, tc := range cases {
		c := sample
		c.Protocol, c.Network, c.Destination = tc.protocol, tc.network, tc.destination
		if got := connectionProtocolLabel(&c); got != tc.want {
			t.Errorf("%+v: %q", tc, got)
		}
	}
}

func TestConnectionTimes(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	ms := func(t time.Time) int64 { return t.UnixMilli() }
	cases := map[string]string{
		formatConnectionTime(ms(now.Add(-20*time.Second)), now):                          "刚刚",
		formatConnectionTime(ms(now.Add(-10*time.Minute)), now):                          "10分钟前",
		formatConnectionTime(ms(now.Add(-2*time.Hour)), now):                             "10:00:00",
		formatConnectionTime(ms(time.Date(2026, 10, 13, 23, 59, 0, 0, time.Local)), now): "10月13日 23:59:00",
		formatConnectionDetailTime(ms(now.Add(-20*time.Second)), 0, now):                 "10月14日 11:59:40（20秒）",
		formatConnectionDetailTime(ms(now.Add(-90*time.Minute)), 0, now):                 "10月14日 10:30:00（1小时30分钟）",
		formatConnectionDetailTime(ms(now.Add(-26*time.Hour)), 0, now):                   "10月13日 10:00:00（1天2小时）",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestFormatRule(t *testing.T) {
	cases := map[string]string{
		"rule_set=[Gfw Telegram openai geosite:youtube]":             "RuleSet: Gfw, Telegram, openai, geosite:youtube",
		"!(network=tcp port=[80 443]) || domain_suffix=example.com":  "!(Network: tcp Port: 80, 443) || DomainSuffix: example.com",
		"process_path=[/Applications/My App.app/Contents/MacOS/App]": "ProcessPath: [/Applications/My App.app/Contents/MacOS/App]",
		"":               "",
		"final":          "final",
		"wifi_ssid=home": "WifiSsid: home",
	}
	for in, want := range cases {
		if got := formatRule(in); got != want {
			t.Errorf("formatRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMitmReasonText(t *testing.T) {
	if got := mitmReasonText("authority_pending"); got != "证书未就绪" {
		t.Fatal(got)
	}
	for reason, want := range map[string]string{"pinned": "证书固定", "builtin": "内置排除", "excluded": "已排除"} {
		if got := mitmReasonText(reason); got != want {
			t.Fatalf("%s: %q", reason, got)
		}
	}
	if got := mitmReasonText("remote error: tls: bad certificate"); got != "remote error: tls: bad certificate" {
		t.Fatal(got)
	}
}
