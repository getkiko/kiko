package connections

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

// mitmFailure is a long failure of the core, which the inspector wraps.
const mitmFailure = "handshake with client: remote error: tls: unknown certificate authority; the client rejected the certificate issued for api.github.com by Kiko HTTPS Decryption CA, it may pin its certificates"

// mitmRows are the fixture with mtalk.google.com decrypted, api.github.com
// failed, ime.doubao.com left alone and cdn.doubao.com skipped before the
// certificate was ready.
func mitmRows(now time.Time) []api.ConnectionRow {
	rows := fixtureRows(now)
	rows[0].Mitm, rows[0].HTTPRequests = api.MitmDecrypted, 12
	rows[1].Mitm, rows[1].HTTPRequests, rows[1].MitmError = api.MitmFailed, 0, mitmFailure
	rows[1].Destination = "140.82.112.6:8443"
	skipped := rows[2]
	skipped.ID, skipped.Domain, skipped.Source = "c5", "cdn.doubao.com", "172.18.0.2:61805"
	skipped.Mitm, skipped.MitmError = api.MitmSkipped, "authority_pending"
	return append(rows, skipped)
}

func newMitmHarness(t *testing.T, width, height int) *harness {
	h := newSizedHarness(t, width, height, func(srv *testkit.Server) {
		srv.Route("GET", "/connections", newFakeConnections(mitmRows(time.Now())).ServeHTTP)
	})
	h.w.conn.setMode(groupNone)
	testkit.UntilText(t, h.tt, "mtalk.google.com")
	return h
}

func TestConnectionMitmProtocolLabel(t *testing.T) {
	c := sample
	c.Destination = "1.2.3.4:8443"
	for mitm, want := range map[string]string{"": "TLS", api.MitmPending: "TLS", api.MitmSkipped: "TLS", api.MitmDecrypted: "HTTPS", api.MitmFailed: "HTTPS"} {
		c.Mitm = mitm
		if got := connectionProtocolLabel(&c); got != want {
			t.Errorf("%q: %q", mitm, got)
		}
	}
	for mitm, want := range map[string]string{"": "TCP · tls", api.MitmPending: "TCP · tls", api.MitmDecrypted: "TCP · HTTPS · 已解密", api.MitmFailed: "TCP · HTTPS · 解密失败"} {
		c.Mitm = mitm
		if got := connectionNetworkText(&c); got != want {
			t.Errorf("%q: %q", mitm, got)
		}
	}
}

// A decrypted connection shows its state and requests, a failed one its
// reason, wrapped and selectable; a connection left alone neither.
func TestConnectionMitmInspector(t *testing.T) {
	h := newMitmHarness(t, 1440, 820)
	tt := h.tt
	h.click(t, "mtalk.google.com")
	testkit.UntilText(t, tt, "TCP · HTTPS · 已解密")
	if !hasLinkedProperty(tt, "HTTP 请求", "查看此连接的 HTTP 请求", "12") || tt.HasText("失败原因") {
		t.Fatalf("decrypted: %q", tt.Texts())
	}
	savePreview(t, tt, "mitm-decrypted-light.png")

	h.click(t, "api.github.com")
	testkit.UntilText(t, tt, "TCP · HTTPS · 解密失败")
	if !hasProperty(tt, "失败原因", mitmFailure) || !hasProperty(tt, "HTTP 请求", "0") {
		t.Fatalf("failed: %q", tt.Texts())
	}
	// The badge says HTTPS on a port other than 443.
	if n := slices.Index(tt.Texts(), "TLS"); n >= 0 || countText(tt.Texts(), "HTTPS") != 4 {
		t.Fatalf("badges: %d in %q", n, tt.Texts())
	}
	savePreview(t, tt, "mitm-failed-light.png")
	tt.SetDark(true)
	tt.Frame()
	savePreview(t, tt, "mitm-failed-dark.png")
	tt.SetDark(false)

	h.click(t, "ime.doubao.com")
	testkit.UntilText(t, tt, "TCP · tls")
	if countText(tt.Texts(), "HTTP 请求") != 1 || tt.HasText("失败原因") || tt.HasText("TCP · HTTPS · 已解密") {
		t.Fatalf("normal: %q", tt.Texts())
	}
	savePreview(t, tt, "mitm-normal-light.png")

	// A skipped connection keeps its protocol and names the reason.
	h.click(t, "cdn.doubao.com")
	testkit.UntilText(t, tt, "跳过原因")
	if !hasProperty(tt, "网络", "TCP · tls") || !hasProperty(tt, "跳过原因", "证书未就绪") || tt.HasText("authority_pending") || countText(tt.Texts(), "HTTP 请求") != 1 {
		t.Fatalf("skipped: %q", tt.Texts())
	}
	savePreview(t, tt, "mitm-skipped-light.png")
}

// The narrow inspector wraps the reason within its overlay.
func TestConnectionMitmNarrowInspector(t *testing.T) {
	h := newMitmHarness(t, 760, 640)
	tt := h.tt
	h.click(t, "api.github.com")
	testkit.UntilText(t, tt, "失败原因")
	savePreview(t, tt, "mitm-narrow-inspector.png")
	h.click(t, "关闭详情")
	testkit.Until(t, tt, func() bool { return !tt.HasText("失败原因") })
}

func countText(texts []string, text string) int {
	n := 0
	for _, t := range texts {
		if t == text {
			n++
		}
	}
	return n
}

// hasProperty reports whether the inspector shows the row label with value.
func hasProperty(tt *ui.Tester, label, value string) bool {
	texts := tt.Texts()
	for i := 0; i+1 < len(texts); i++ {
		if texts[i] == label && texts[i+1] == value {
			return true
		}
	}
	return false
}

// hasLinkedProperty reports whether the inspector shows a row whose value
// is a link: its label, the link's hint, then the value.
func hasLinkedProperty(tt *ui.Tester, label, hint, value string) bool {
	texts := tt.Texts()
	for i := 0; i+2 < len(texts); i++ {
		if texts[i] == label && texts[i+1] == hint && texts[i+2] == value {
			return true
		}
	}
	return false
}
