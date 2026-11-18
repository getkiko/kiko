package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestShareReason(t *testing.T) {
	ready := &api.MITMStatus{CA: api.MITMCA{State: caReady}}
	cases := []struct {
		s    *api.MITMStatus
		want string
	}{
		{nil, "正在读取"},
		{ready, ""},
		{&api.MITMStatus{CA: api.MITMCA{State: caAbsent}}, "没有根证书"},
		{&api.MITMStatus{}, "没有根证书"},
		{&api.MITMStatus{CA: api.MITMCA{State: caBroken}}, "证书已损坏"},
	}
	for _, tc := range cases {
		if got := shareReason(tc.s); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestShareFingerprintText(t *testing.T) {
	got := shareFingerprintText(testPrint)
	want := "0a1b 2c3d 4e5f 6071 8293 a4b5 c6d7 e8f9 0a1b 2c3d 4e5f 6071 8293 a4b5 c6d7 e8f9"
	if got != want {
		t.Errorf("grouped %q", got)
	}
	if got := shareFingerprintText("not a print"); got != "not a print" {
		t.Errorf("invalid input changed: %q", got)
	}
}

func TestShareRemaining(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		expires string
		want    string
		expired bool
	}{
		{"2026-11-04T10:09:05Z", "9:05", false},
		{"2026-11-04T10:00:00.2Z", "0:01", false},
		{"2026-11-04T10:00:00Z", "0:00", true},
		{"2026-11-04T09:59:00Z", "0:00", true},
		{"2026-11-04T11:00:00Z", "1:00:00", false},
		{"2026-11-04T12:03:09Z", "2:03:09", false},
		{"2026-11-04T18:00:05+08:00", "0:05", false},
		{"", "0:00", true},
		{"soon", "0:00", true},
	}
	for _, tc := range cases {
		got, expired := shareRemaining(tc.expires, now)
		if got != tc.want || expired != tc.expired {
			t.Errorf("%q: %q %v, want %q %v", tc.expires, got, expired, tc.want, tc.expired)
		}
	}
}

func TestShareAddress(t *testing.T) {
	list := []string{"192.168.1.5", "10.0.0.2"}
	if got := shareAddress(list, "10.0.0.2"); got != "10.0.0.2" {
		t.Errorf("kept choice: %q", got)
	}
	if got := shareAddress(list, "172.16.0.1"); got != "192.168.1.5" {
		t.Errorf("vanished choice: %q", got)
	}
	if got := shareAddress(nil, "x"); got != "" {
		t.Errorf("no candidates: %q", got)
	}
}

func TestExportFormats(t *testing.T) {
	ca := api.MITMCA{CommonName: "Kiko Root CA 1a2b3c4d"}
	want := map[string]string{
		"pem": "Kiko-Root-CA-1a2b3c4d.pem", "cer": "Kiko-Root-CA-1a2b3c4d.cer", "crt": "Kiko-Root-CA-1a2b3c4d.crt",
		"p12": "Kiko-Root-CA-1a2b3c4d.p12", "mobileconfig": "Kiko-Root-CA-1a2b3c4d.mobileconfig",
	}
	if len(exportFormats) != len(want) {
		t.Fatalf("%d formats", len(exportFormats))
	}
	labels := map[string]bool{}
	for _, e := range exportFormats {
		if got := mitmExportName(ca, e.Format); got != want[e.Format] {
			t.Errorf("%s: %q", e.Format, got)
		}
		if e.Label == "" || e.Filter == "" || labels[e.Label] {
			t.Errorf("format %+v", e)
		}
		labels[e.Label] = true
	}
	if got := mitmExportName(api.MITMCA{}, "crt"); got != "kiko-root-ca.crt" {
		t.Errorf("unnamed: %q", got)
	}
}

// routeShare registers the fake /mitm/share of kiko-server.
func (m *mitmServer) routeShare(f *fixture) {
	reply := func(w http.ResponseWriter, r *http.Request, fn func(s *api.MITMShare)) {
		m.mu.Lock()
		if fn != nil && m.shareFail == "" {
			fn(&m.share)
		}
		share, fail := m.share, m.shareFail
		m.mu.Unlock()
		if fail != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"` + fail + `"}`))
			return
		}
		testkit.JSON(share)(w, r)
	}
	f.srv.Route("GET", "/mitm/share", func(w http.ResponseWriter, r *http.Request) { reply(w, r, nil) })
	f.srv.Route("POST", "/mitm/share", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Address string `json:"address"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, r, func(s *api.MITMShare) {
			m.started = append(m.started, body.Address)
			addr := body.Address
			if addr == "" && len(s.Addresses) > 0 {
				addr = s.Addresses[0]
			}
			s.Active, s.Address = true, addr
			s.URL = "http://" + addr + ":47321/Zk3p9vQw2LxT/"
			s.Fingerprint = testPrint
			s.ExpiresAt = time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
			s.QR = testQR()
			s.Downloads = 0
		})
	})
	f.srv.Route("DELETE", "/mitm/share", func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, func(s *api.MITMShare) {
			*s = api.MITMShare{Addresses: s.Addresses}
		})
	})
}

func (m *mitmServer) startedAddresses() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.started...)
}

// testQR is a 29-module pattern with finder squares in three corners.
func testQR() []string {
	const n = 29
	rows := make([]string, n)
	finder := func(x, y int) bool {
		in := func(ox, oy int) bool {
			dx, dy := x-ox, y-oy
			if dx < 0 || dy < 0 || dx > 6 || dy > 6 {
				return false
			}
			return dx == 0 || dy == 0 || dx == 6 || dy == 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4)
		}
		return in(0, 0) || in(n-7, 0) || in(0, n-7)
	}
	for y := range n {
		var b strings.Builder
		for x := range n {
			dark := finder(x, y) || (x > 8 && x < n-8 && y > 8 && (x*7+y*3+x*y)%5 < 2)
			if dark {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[y] = b.String()
	}
	return rows
}

func shareFixture(t *testing.T, addresses ...string) (*fixture, *mitmServer) {
	t.Helper()
	f, m := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	m.mu.Lock()
	m.share = api.MITMShare{Addresses: addresses}
	m.mu.Unlock()
	f.w.https.loadShare()
	testkit.Until(t, f.tt, func() bool {
		return f.w.https.share.info != nil && len(f.w.https.share.info.Addresses) == len(addresses)
	})
	return f, m
}

func TestShareStartStop(t *testing.T) {
	f, m := shareFixture(t, "192.168.1.5")
	if !f.tt.HasText("局域网分发") || !f.tt.HasText("192.168.1.5") || f.tt.HasText("二维码") {
		t.Fatalf("stopped state: %q", f.tt.Texts())
	}
	f.click("开始局域网分发")
	testkit.Until(t, f.tt, func() bool {
		return f.w.https.share.info != nil && f.w.https.share.info.Active && !f.w.https.share.busy
	})
	if got := m.startedAddresses(); len(got) != 1 || got[0] != "192.168.1.5" {
		t.Errorf("started on %q", got)
	}
	f.tt.Frame()
	for _, text := range []string{"http://192.168.1.5:47321/Zk3p9vQw2LxT/", "二维码", "停止", "下载", "剩余"} {
		if !f.tt.HasText(text) && text != "二维码" {
			t.Errorf("missing %q in %q", text, f.tt.Texts())
		}
	}
	if !f.tt.HasText("0a1b 2c3d 4e5f 6071 8293 a4b5 c6d7 e8f9 0a1b 2c3d 4e5f 6071 8293 a4b5 c6d7 e8f9") {
		t.Errorf("fingerprint not grouped: %q", f.tt.Texts())
	}
	if !f.tt.HasText("9:5") && !f.tt.HasText("10:00") && !f.tt.HasText("9:59") {
		t.Errorf("no time left: %q", f.tt.Texts())
	}
	f.click("停止局域网分发")
	testkit.Until(t, f.tt, func() bool { return !f.w.https.share.info.Active && !f.w.https.share.busy })
	f.tt.Frame()
	if f.tt.HasText("http://192.168.1.5:47321/Zk3p9vQw2LxT/") || !f.tt.HasText("开始") {
		t.Errorf("not stopped: %q", f.tt.Texts())
	}
}

func TestShareAddressPicker(t *testing.T) {
	f, m := shareFixture(t, "192.168.1.5", "10.0.0.2")
	if !f.tt.HasText("192.168.1.5") {
		t.Fatalf("no picker: %q", f.tt.Texts())
	}
	f.click("分发地址")
	f.tt.Frame()
	f.click("10.0.0.2")
	f.tt.Frame()
	if f.w.https.share.addr != "10.0.0.2" {
		t.Fatalf("picked %q", f.w.https.share.addr)
	}
	f.click("开始局域网分发")
	testkit.Until(t, f.tt, func() bool { return len(m.startedAddresses()) == 1 })
	if got := m.startedAddresses()[0]; got != "10.0.0.2" {
		t.Errorf("started on %q", got)
	}
}

func TestShareNoRootDisabled(t *testing.T) {
	f, m := newMITMFixture(t, api.MITMStatus{Supported: true, CA: api.MITMCA{State: caAbsent, TrustSupported: true}}, mixedDoc())
	m.mu.Lock()
	m.share = api.MITMShare{Addresses: []string{"192.168.1.5"}}
	m.mu.Unlock()
	f.w.https.loadShare()
	testkit.Until(t, f.tt, func() bool { return f.w.https.share.info != nil })
	f.tt.Frame()
	if !f.tt.HasText("局域网分发") || !f.tt.HasText("没有根证书") {
		t.Fatalf("no reason: %q", f.tt.Texts())
	}
	if err := f.tt.Click("开始局域网分发"); err == nil {
		f.tt.Frame()
	}
	if len(m.startedAddresses()) != 0 || f.w.https.share.busy {
		t.Error("started without a root certificate")
	}
}

func TestShareErrorsShowInline(t *testing.T) {
	f, m := shareFixture(t, "192.168.1.5")
	m.mu.Lock()
	m.shareFail = "没有根证书"
	m.mu.Unlock()
	f.click("开始局域网分发")
	testkit.UntilText(t, f.tt, "没有根证书")
	if f.w.https.share.info == nil || f.w.https.share.info.Active {
		t.Error("failed start showed active")
	}
	if !f.tt.HasText("开始") {
		t.Errorf("no retry: %q", f.tt.Texts())
	}
	m.mu.Lock()
	m.shareFail = ""
	m.mu.Unlock()
	f.click("开始局域网分发")
	testkit.Until(t, f.tt, func() bool { return f.w.https.share.info.Active })
	f.tt.Frame()
	if f.w.https.share.err != "" {
		t.Errorf("error kept: %q", f.w.https.share.err)
	}
}

func TestShareExpiresOnServer(t *testing.T) {
	f, m := shareFixture(t, "192.168.1.5")
	f.click("开始局域网分发")
	testkit.Until(t, f.tt, func() bool { return f.w.https.share.info.Active && !f.w.https.share.busy })
	m.mu.Lock()
	m.share = api.MITMShare{Addresses: []string{"192.168.1.5"}}
	m.mu.Unlock()
	f.w.https.loadShare()
	testkit.Until(t, f.tt, func() bool { return !f.w.https.share.info.Active })
	f.tt.Frame()
	if f.tt.HasText("http://192.168.1.5:47321/Zk3p9vQw2LxT/") || !f.tt.HasText("开始") {
		t.Errorf("still active: %q", f.tt.Texts())
	}
}

func TestShareStaleReadDoesNotOverwrite(t *testing.T) {
	f, m := shareFixture(t, "192.168.1.5")
	reads := func() (n int) {
		for _, r := range f.srv.Requests() {
			if strings.HasSuffix(r, "GET /mitm/share") || strings.HasSuffix(r, "GET /api/v1/mitm/share") {
				n++
			}
		}
		return n
	}
	p := f.w.https
	before, seen := p.share.info, reads()
	m.mu.Lock()
	m.share = api.MITMShare{Active: true, Address: "10.9.9.9", URL: "http://10.9.9.9/x/"}
	m.mu.Unlock()
	p.loadShare()
	p.share.writes++ // an action overtook the read
	testkit.Until(t, f.tt, func() bool { return reads() > seen })
	f.tt.Advance(200 * time.Millisecond)
	f.tt.Frame()
	if p.share.info != before {
		t.Error("an older read replaced the state")
	}
}

func TestShareExportFormatsMenu(t *testing.T) {
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	shell := f.env.Shell.(*testkit.Shell)
	for i, e := range exportFormats {
		f.click("导出根证书")
		f.tt.Frame()
		if err := f.tt.Click(e.Label); err != nil {
			t.Fatalf("menu %q: %v", f.tt.Texts(), err)
		}
		testkit.Until(t, f.tt, func() bool { return len(shell.SavedNames()) == i+1 })
		if _, ok := shell.SavedFile("Kiko-Root-CA-1a2b3c4d." + e.Format); !ok {
			t.Errorf("%s: saved %q", e.Format, shell.SavedNames())
		}
		if !f.requested("GET /mitm/export?format="+e.Format) && !f.requested("GET /api/v1/mitm/export?format="+e.Format) {
			t.Errorf("%s: requests %q", e.Format, f.srv.Requests())
		}
	}
}

func TestShareScreenshots(t *testing.T) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SETTINGS_PREVIEW_DIR not set")
	}
	shot := func(f *fixture, name string) {
		f.tt.SetDark(false)
		f.tt.Frame()
		f.save(name + "-light.png")
		f.tt.SetDark(true)
		f.tt.Frame()
		f.save(name + "-dark.png")
		f.tt.SetDark(false)
		f.tt.Frame()
	}
	f, _ := shareFixture(t, "192.168.1.5", "10.0.0.2")
	f.click("导出根证书")
	f.tt.Frame()
	shot(f, "export-menu")
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	shot(f, "share-stopped")
	f.click("开始局域网分发")
	testkit.Until(t, f.tt, func() bool { return f.w.https.share.info.Active && !f.w.https.share.busy })
	f.tt.Frame()
	shot(f, "share-active")
	f.tt.SetSize(480, 2600)
	f.tt.Frame()
	shot(f, "share-narrow")
}

// Once the root certificate is deleted, the old download link is withdrawn
// at once, without waiting for the server's answer.
func TestShareClearedWhenRootDeleted(t *testing.T) {
	f, m := shareFixture(t, "192.168.1.5")
	f.click("开始局域网分发")
	testkit.Until(t, f.tt, func() bool {
		return f.w.https.share.info != nil && f.w.https.share.info.Active && !f.w.https.share.busy
	})
	// Later reads fail: only an explicit clear makes the old state go away.
	m.mu.Lock()
	m.shareFail = "x"
	m.mu.Unlock()
	f.w.https.act(func(ctx context.Context, c *api.Client) (api.MITMStatus, error) { return c.MITMDelete(ctx) }, nil)
	testkit.Until(t, f.tt, func() bool {
		return !f.w.https.busy && f.w.https.status != nil && f.w.https.status.CA.State == caAbsent
	})
	testkit.Until(t, f.tt, func() bool { return f.w.https.share.err != "" })
	if f.w.https.share.info != nil {
		t.Fatal("old share still shown")
	}
}
