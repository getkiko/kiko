package settings

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
)

func TestMITMBlocker(t *testing.T) {
	ready := api.MITMStatus{Supported: true, CA: api.MITMCA{State: caReady, Trusted: true, TrustSupported: true}}
	cases := []struct {
		name   string
		mutate func(s *api.MITMStatus)
		want   string
	}{
		{"ready and trusted", func(*api.MITMStatus) {}, ""},
		{"core unsupported", func(s *api.MITMStatus) { s.Supported = false }, "核心不支持"},
		{"absent", func(s *api.MITMStatus) { s.CA = api.MITMCA{State: caAbsent} }, "没有根证书"},
		{"broken", func(s *api.MITMStatus) { s.CA.State = caBroken }, "证书已损坏"},
		{"untrusted", func(s *api.MITMStatus) { s.CA.Trusted = false }, "根证书未信任"},
		{"trust unknown", func(s *api.MITMStatus) { s.CA.TrustUnknown = true }, "信任状态未知"},
	}
	for _, tc := range cases {
		s := ready
		tc.mutate(&s)
		if got := mitmBlocker(&s); got != tc.want {
			t.Errorf("%s: blocker %q, want %q", tc.name, got, tc.want)
		}
	}
	if mitmBlocker(nil) == "" {
		t.Error("an unread status must block")
	}
}

func TestMITMHostList(t *testing.T) {
	list, err := addHost(nil, nil, "  Example.COM. ")
	if err != nil || !slices.Equal(list, []string{"example.com"}) {
		t.Fatalf("add: %v %v", list, err)
	}
	if _, err := addHost(list, nil, "example.com"); err == nil {
		t.Error("duplicate accepted")
	}
	if _, err := addHost(nil, list, "example.com"); err == nil {
		t.Error("host of the other list accepted")
	}
	list, err = addHost(list, nil, "*.example.org")
	if err != nil || len(list) != 2 {
		t.Fatalf("wildcard: %v %v", list, err)
	}
	for _, bad := range []string{"", "*", "https://a.com", "a.com:443", "a..com", "a*.com", "a b.com", "a.com/x", "cloudflare-ech.com", "*.cloudflare-ech.com"} {
		if _, err := normalizeMITMHost(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := normalizeMITMHost("例え.jp"); err == nil {
		t.Error("non-ASCII accepted")
	}
	if _, err := addHost(nil, []string{"Example.com"}, "example.com"); err == nil {
		t.Error("unnormalised stored host not matched")
	}
	if got := removeHost(list, "example.com"); !slices.Equal(got, []string{"*.example.org"}) {
		t.Errorf("remove: %v", got)
	}
	if len(list) != 2 {
		t.Error("remove changed its input")
	}
}

func TestMITMSettingsEdit(t *testing.T) {
	doc := model.EmptyDoc("linux")
	if m := mitmSettingsOf(doc); m.QUIC != model.MITMQUICBlock || m.Enabled {
		t.Errorf("defaults %+v", m)
	}
	editMITM(doc, func(m *model.MITMSettings) {
		m.Enabled, m.QUIC = true, model.MITMQUICAllow
		setHostList(m, true, []string{"a.com"})
	})
	m := mitmSettingsOf(doc)
	if !m.Enabled || m.QUIC != model.MITMQUICAllow || !slices.Equal(hostList(m, true), []string{"a.com"}) || hostList(m, false) != nil {
		t.Errorf("edited %+v", m)
	}
}

// mitmServer is a fake /mitm/* of kiko-server.
type mitmServer struct {
	mu     sync.Mutex
	status api.MITMStatus
	fail   map[string]string
	calls  []string
	// cleared are the hosts of POST /mitm/skip, "" for all.
	cleared []string
	// share is the state of /mitm/share; shareFail is the error text of
	// its next answers while set; started are the addresses posted.
	share     api.MITMShare
	shareFail string
	started   []string
}

func (m *mitmServer) get() api.MITMStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mitmServer) set(fn func(s *api.MITMStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.status)
}

func (m *mitmServer) called(call string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.calls, call)
}

func (m *mitmServer) write(name string, fn func(s *api.MITMStatus)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.calls = append(m.calls, name)
		message := m.fail[name]
		if message == "" {
			fn(&m.status)
		}
		status := m.status
		m.mu.Unlock()
		if message != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
			return
		}
		testkit.JSON(status)(w, r)
	}
}

const testPrint = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"

func readyCA(trusted bool) api.MITMCA {
	return api.MITMCA{State: caReady, CommonName: "Kiko Root CA 1a2b3c4d", Fingerprint: testPrint[:64],
		NotBefore: "2026-11-01T00:00:00Z", NotAfter: "2036-11-01T00:00:00Z", Trusted: trusted, TrustSupported: true}
}

func newMITMFixture(t *testing.T, initial api.MITMStatus, doc *model.Doc) (*fixture, *mitmServer) {
	t.Helper()
	f := newFixture(t, doc)
	m := &mitmServer{status: initial, fail: map[string]string{}}
	f.srv.Route("GET", "/mitm/status", func(w http.ResponseWriter, r *http.Request) { testkit.JSON(m.get())(w, r) })
	f.srv.Route("POST", "/mitm/ca/regenerate", m.write("regenerate", func(s *api.MITMStatus) { s.CA = readyCA(false) }))
	f.srv.Route("POST", "/mitm/ca", m.write("generate", func(s *api.MITMStatus) { s.CA = readyCA(false) }))
	f.srv.Route("DELETE", "/mitm/ca", m.write("delete", func(s *api.MITMStatus) { s.CA = api.MITMCA{State: caAbsent, TrustSupported: true} }))
	f.srv.Route("POST", "/mitm/trust", m.write("trust", func(s *api.MITMStatus) { s.CA.Trusted = true }))
	f.srv.Route("DELETE", "/mitm/trust", m.write("untrust", func(s *api.MITMStatus) { s.CA.Trusted = false }))
	m.routeShare(f)
	f.srv.Route("POST", "/mitm/skip", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Host string `json:"host"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.cleared = append(m.cleared, body.Host)
		m.status.Skip = slices.DeleteFunc(m.status.Skip, func(e api.MITMSkip) bool { return body.Host == "" || e.Host == body.Host })
		m.status.Failures = slices.DeleteFunc(m.status.Failures, func(e api.MITMFailure) bool { return body.Host == "" || e.Host == body.Host })
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv.Route("GET", "/mitm/export", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("-----BEGIN CERTIFICATE-----\n" + r.URL.Query().Get("format") + "\n-----END CERTIFICATE-----\n"))
	})
	f.w.Open(pageHTTPS)
	f.tt.SetSize(width, 2600)
	testkit.Until(t, f.tt, func() bool { return f.w.https.status != nil })
	return f, m
}

func (f *fixture) mitmSettings() model.MITMSettings { return mitmSettingsOf(f.store.Doc()) }

func (f *fixture) settled() {
	f.t.Helper()
	testkit.Until(f.t, f.tt, func() bool { return !f.store.Saving() && !f.w.https.busy })
}

func TestHTTPSGenerateTrustAndEnable(t *testing.T) {
	f, m := newMITMFixture(t, api.MITMStatus{Supported: true, CA: api.MITMCA{State: caAbsent, TrustSupported: true}}, mixedDoc())
	for _, text := range []string{"解密 HTTPS", "没有根证书", "未生成"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q in %q", text, f.tt.Texts())
		}
	}
	// Without a trusted certificate the switch stays off.
	f.clickRowControl("解密 HTTPS", 16)
	f.tt.Frame()
	if f.mitmSettings().Enabled || f.w.https.enableConfirm.Open {
		t.Fatal("switch turned on without a certificate")
	}
	f.click("生成根证书")
	testkit.UntilText(t, f.tt, "Kiko Root CA 1a2b3c4d")
	if !m.called("generate") || !f.tt.HasText("未信任") || !f.tt.HasText("根证书未信任") {
		t.Errorf("after generate: %q", f.tt.Texts())
	}
	f.clickRowControl("解密 HTTPS", 16)
	f.tt.Frame()
	if f.mitmSettings().Enabled {
		t.Fatal("switch turned on with an untrusted certificate")
	}
	f.click("安装信任")
	testkit.UntilText(t, f.tt, "已信任")
	f.settled()

	// The first enable asks; nothing is stored until it is confirmed.
	f.clickRowControl("解密 HTTPS", 16)
	f.tt.Frame()
	if !f.w.https.enableConfirm.Open || f.mitmSettings().Enabled {
		t.Fatalf("no confirmation: open %v, %+v", f.w.https.enableConfirm.Open, f.mitmSettings())
	}
	f.click("取消")
	f.tt.Frame()
	if f.mitmSettings().Enabled || f.mitmSettings().Acknowledged {
		t.Fatal("cancel stored the switch")
	}
	f.clickRowControl("解密 HTTPS", 16)
	f.tt.Frame()
	f.click("开启")
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().Enabled && !f.store.Saving() })
	if !f.mitmSettings().Acknowledged {
		t.Error("not acknowledged")
	}
	var saved model.Doc
	testkit.Until(t, f.tt, func() bool {
		_ = json.Unmarshal(f.srv.Store().Doc, &saved)
		return saved.HTTPTools != nil && saved.HTTPTools.MITM != nil && saved.HTTPTools.MITM.Enabled
	})

	// Turning off needs no confirmation, and on again none either.
	f.clickRowControl("解密 HTTPS", 16)
	testkit.Until(t, f.tt, func() bool { return !f.mitmSettings().Enabled && !f.store.Saving() })
	f.clickRowControl("解密 HTTPS", 16)
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().Enabled && !f.store.Saving() })
	if f.w.https.enableConfirm.Open {
		t.Error("second enable asked again")
	}

	if dir := os.Getenv("SETTINGS_PREVIEW_DIR"); dir != "" {
		f.save("https-light.png")
		f.tt.SetDark(true)
		f.tt.Frame()
		f.save("https-dark.png")
		f.tt.SetDark(false)
	}
}

func TestHTTPSCoreUnsupportedBlocksEnable(t *testing.T) {
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: false, CA: readyCA(true)}, mixedDoc())
	if !f.tt.HasText("核心不支持") {
		t.Errorf("no reason: %q", f.tt.Texts())
	}
	f.clickRowControl("解密 HTTPS", 16)
	f.tt.Frame()
	if f.mitmSettings().Enabled || f.w.https.enableConfirm.Open {
		t.Error("enabled on an unsupported core")
	}
}

func TestHTTPSEnabledSwitchTurnsOffWhenBlocked(t *testing.T) {
	doc := mixedDoc()
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true}}
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: api.MITMCA{State: caBroken, TrustSupported: true}}, doc)
	f.clickRowControl("解密 HTTPS", 16)
	testkit.Until(t, f.tt, func() bool { return !f.mitmSettings().Enabled && !f.store.Saving() })
}

func TestHTTPSHostLists(t *testing.T) {
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	f.w.https.include.text = "Example.com"
	f.tt.Frame()
	f.click("添加包含主机名")
	testkit.Until(t, f.tt, func() bool { return len(hostList(f.mitmSettings(), false)) == 1 && !f.store.Saving() })
	if hostList(f.mitmSettings(), false)[0] != "example.com" {
		t.Errorf("stored %v", hostList(f.mitmSettings(), false))
	}
	if !f.tt.HasText("example.com") {
		t.Errorf("not listed: %q", f.tt.Texts())
	}
	f.w.https.include.text = "bad host"
	f.tt.Frame()
	f.click("添加包含主机名")
	f.tt.Frame()
	if !f.tt.HasText("只填写主机名") {
		t.Errorf("no reason: %q", f.tt.Texts())
	}
	f.click("移除 example.com")
	testkit.Until(t, f.tt, func() bool { return len(hostList(f.mitmSettings(), false)) == 0 && !f.store.Saving() })
}

func TestHTTPSOptions(t *testing.T) {
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	f.click("放行")
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().QUIC == model.MITMQUICAllow && !f.store.Saving() })
	f.clickRowControl("保存正文", 16)
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().SaveBody && !f.store.Saving() })
	f.click("阻止")
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().QUIC == model.MITMQUICBlock && !f.store.Saving() })
}

func TestHTTPSRegenerateAndDeleteConfirm(t *testing.T) {
	f, m := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	m.mu.Lock()
	m.fail["regenerate"] = "已取消授权"
	m.mu.Unlock()
	f.click("重新生成根证书")
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEnter)
	testkit.UntilText(t, f.tt, "已取消授权")
	if !f.w.https.regenConfirm.Open || !f.tt.HasText("重试") {
		t.Fatalf("no retry: %q", f.tt.Texts())
	}
	m.mu.Lock()
	delete(m.fail, "regenerate")
	m.mu.Unlock()
	f.click("重试")
	testkit.Until(t, f.tt, func() bool { return !f.w.https.regenConfirm.Open && m.called("regenerate") && !f.w.https.busy })
	if m.get().CA.Trusted || !f.tt.HasText("未信任") {
		t.Errorf("regenerate kept the trust: %q", f.tt.Texts())
	}
	f.click("删除根证书")
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEnter)
	testkit.Until(t, f.tt, func() bool { return m.get().CA.State == caAbsent && f.tt.HasText("未生成") })
}

func TestHTTPSExport(t *testing.T) {
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	f.click("导出根证书")
	f.tt.Frame()
	if err := f.tt.Click("CER"); err != nil {
		t.Fatalf("menu %q: %v", f.tt.Texts(), err)
	}
	shell := f.env.Shell.(*testkit.Shell)
	testkit.Until(t, f.tt, func() bool { return len(shell.SavedNames()) == 1 })
	data, ok := shell.SavedFile("Kiko-Root-CA-1a2b3c4d.cer")
	if !ok || !strings.Contains(string(data), "cer") {
		t.Errorf("saved %q (%v)", data, shell.SavedNames())
	}
	if !f.requested("GET /mitm/export?format=cer") && !f.requested("GET /api/v1/mitm/export?format=cer") {
		t.Errorf("requests %q", f.srv.Requests())
	}
}

func TestHTTPSNarrowScreenshot(t *testing.T) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SETTINGS_PREVIEW_DIR not set")
	}
	doc := mixedDoc()
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true, SaveBody: true,
		Hostnames: &model.MITMHostnames{Include: []string{"example.com", "*.api.example.org"}, Exclude: []string{"pay.example.com"}}}}
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true), Core: api.MITMCore{Connected: true, Enabled: true, AuthorityFingerprint: "ab"}}, doc)
	f.save("https-hosts-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("https-hosts-dark.png")
	f.tt.SetDark(false)
	f.tt.SetSize(480, 1000)
	f.tt.Frame()
	f.save("https-narrow-light.png")
}

func TestHTTPSEnabledShowsBlockerAndRegenerateTurnsOff(t *testing.T) {
	doc := mixedDoc()
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true}}
	broken := api.MITMStatus{Supported: true, CA: api.MITMCA{State: caBroken, TrustSupported: true}}
	f, _ := newMITMFixture(t, broken, doc)
	if reason := mitmBlocker(&broken); reason == "" || !f.tt.HasText(reason) {
		t.Errorf("blocker %q not shown while on: %q", reason, f.tt.Texts())
	}

	doc2 := mixedDoc()
	doc2.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true}}
	g, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, doc2)
	g.click("重新生成根证书")
	g.tt.Frame()
	g.tt.Key(0, ui.KeyEnter)
	testkit.Until(t, g.tt, func() bool { return !g.mitmSettings().Enabled && !g.store.Saving() })
}

func TestHTTPSRemoveTrustAndDeleteTurnOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(f *fixture)
	}{
		{"remove trust", func(f *fixture) { f.click("移除信任") }},
		{"delete", func(f *fixture) {
			f.click("删除根证书")
			f.tt.Frame()
			f.tt.Key(0, ui.KeyEnter)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mixedDoc()
			doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true}}
			f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, doc)
			tc.run(f)
			testkit.Until(t, f.tt, func() bool { return !f.mitmSettings().Enabled && !f.store.Saving() })
		})
	}
}

func TestSkipRows(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	s := &api.MITMStatus{
		Skip:     []api.MITMSkip{{Host: "Pin.example.com", Reason: "pinned", Until: now.Add(12*time.Minute + 5*time.Second)}, {Host: "b.example.com", Source: "weird", Until: now.Add(20 * time.Second)}},
		Failures: []api.MITMFailure{{Host: "pin.example.com", Count: 3}, {Host: "c.example.com", Count: 2, LastError: "bad certificate"}},
	}
	rows := skipRows(s, now)
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	want := []skipRow{
		{Host: "Pin.example.com", Detail: "剩余 13 分钟 · 证书固定", Skipped: true},
		{Host: "b.example.com", Detail: "剩余不足 1 分钟 · weird", Skipped: true},
		{Host: "c.example.com", Detail: "2 次 · bad certificate"},
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, rows[i], want[i])
		}
	}
	if skipRows(nil, now) != nil || skipRows(&api.MITMStatus{}, now) != nil {
		t.Error("rows from nothing")
	}
	if got := skipRemaining(-time.Second); got != "即将恢复" {
		t.Error(got)
	}
	if got := skipRemaining(90 * time.Minute); got != "剩余 2 小时" {
		t.Error(got)
	}
}

func TestExcludeHostLogic(t *testing.T) {
	m := model.MITMSettings{Hostnames: &model.MITMHostnames{Include: []string{"a.example.com", "keep.com"}}}
	if err := excludeHost(&m, "A.Example.com."); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Hostnames.Exclude, []string{"a.example.com"}) || !slices.Equal(m.Hostnames.Include, []string{"keep.com"}) {
		t.Errorf("lists %+v", m.Hostnames)
	}
	if err := excludeHost(&m, "a.example.com"); err != nil || len(m.Hostnames.Exclude) != 1 {
		t.Errorf("repeat: %v %+v", err, m.Hostnames)
	}
	if err := excludeHost(&m, "bad host"); err == nil {
		t.Error("bad host accepted")
	}
}

func TestUpstreamAndDeviceEdit(t *testing.T) {
	var m model.MITMSettings
	editUpstream(&m, func(u *model.MITMUpstream) { u.Fingerprint = model.MITMFingerprintNone })
	if m.Upstream == nil || m.Upstream.Fingerprint != "none" {
		t.Fatalf("none must be kept: %+v", m.Upstream)
	}
	editUpstream(&m, func(u *model.MITMUpstream) { u.Fingerprint = model.MITMFingerprintChrome })
	if m.Upstream != nil {
		t.Errorf("chrome is the default and is not stored: %+v", m.Upstream)
	}
	setDeviceScope(&m, "phone", true)
	m.NoLocal = true
	setDeviceScope(&m, "phone", false)
	if m.Devices != nil || m.NoLocal {
		t.Errorf("devices %v noLocal %v", m.Devices, m.NoLocal)
	}
}

func policyDoc() *model.Doc {
	doc := mixedDoc()
	doc.Route.Devices = []*model.RouteDevice{{ID: "phone", Name: "手机", Addresses: []string{"192.168.1.20"}, Final: "direct"}}
	doc.HTTPTools = &model.HTTPTools{MITM: &model.MITMSettings{Enabled: true, Acknowledged: true,
		Hostnames: &model.MITMHostnames{Include: []string{"bank.example.com"}}}}
	return doc
}

func policyStatus() api.MITMStatus {
	return api.MITMStatus{Supported: true, CA: readyCA(true), Core: api.MITMCore{Connected: true, Enabled: true, AuthorityFingerprint: "ab"}, BuiltinCount: 24,
		Skip:     []api.MITMSkip{{Host: "bank.example.com", Reason: "pinned", Until: time.Now().Add(25 * time.Minute)}},
		Failures: []api.MITMFailure{{Host: "bank.example.com", Count: 3}, {Host: "cdn.example.net", Count: 2, LastError: "tls: bad certificate"}}}
}

func TestHTTPSPolicyGroups(t *testing.T) {
	f, m := newMITMFixture(t, policyStatus(), policyDoc())
	for _, text := range []string{"内置排除", "24", "解密失败的主机", "bank.example.com", "cdn.example.net", "证书固定", "2 次 · tls: bad certificate", "设备", "手机", "指纹", "跳过证书校验"} {
		if !f.tt.HasText(text) {
			t.Errorf("missing %q in %q", text, f.tt.Texts())
		}
	}
	// The Built-in Exclusions switch writes builtinExclude.
	f.clickRowControl("内置排除", 16)
	testkit.Until(t, f.tt, func() bool { return !f.builtinOn() && !f.store.Saving() })
	f.clickRowControl("内置排除", 16)
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().BuiltinExclude == nil && !f.store.Saving() })
	// "Add to exclusions" moves the host out of include and clears its record.
	f.click("加入排除 bank.example.com")
	testkit.Until(t, f.tt, func() bool {
		return slices.Contains(hostList(f.mitmSettings(), true), "bank.example.com") && !f.store.Saving() && !f.w.https.skipBusy
	})
	if len(hostList(f.mitmSettings(), false)) != 0 || !slices.Contains(m.cleared, "bank.example.com") {
		t.Errorf("include %v cleared %v", hostList(f.mitmSettings(), false), m.cleared)
	}
	testkit.Until(t, f.tt, func() bool { return f.tt.HasText("cdn.example.net") && !f.tt.HasText("证书固定") })
	// "Clear" forgets one host.
	f.click("清除 cdn.example.net")
	testkit.Until(t, f.tt, func() bool { return !f.w.https.skipBusy && slices.Contains(m.cleared, "cdn.example.net") })
	f.tt.Frame()
	if f.tt.HasText("解密失败的主机") {
		t.Errorf("empty group shown: %q", f.tt.Texts())
	}
}

func TestHTTPSClearAllConfirm(t *testing.T) {
	f, m := newMITMFixture(t, policyStatus(), policyDoc())
	f.click("清除全部跳过记录")
	f.tt.Frame()
	f.tt.Key(0, ui.KeyEnter)
	testkit.Until(t, f.tt, func() bool { return !f.w.https.clearConfirm.Open && slices.Contains(m.cleared, "") })
	testkit.Until(t, f.tt, func() bool { return len(f.w.https.status.Skip) == 0 && len(f.w.https.status.Failures) == 0 })
}

func TestHTTPSPolicyGroupsHidden(t *testing.T) {
	f, _ := newMITMFixture(t, api.MITMStatus{Supported: true, CA: readyCA(true)}, mixedDoc())
	for _, text := range []string{"内置排除", "解密失败的主机", "设备"} {
		if f.tt.HasText(text) {
			t.Errorf("%q shown without data: %q", text, f.tt.Texts())
		}
	}
}

func TestHTTPSUpstreamAndDevices(t *testing.T) {
	f, _ := newMITMFixture(t, policyStatus(), policyDoc())
	f.click("Safari")
	testkit.Until(t, f.tt, func() bool { return upstreamOf(f.mitmSettings()).Fingerprint == "safari" && !f.store.Saving() })
	f.w.https.skipVerify.text = "internal.example.com"
	f.tt.Frame()
	f.click("添加跳过证书校验主机名")
	testkit.Until(t, f.tt, func() bool {
		return slices.Equal(upstreamOf(f.mitmSettings()).SkipVerify, []string{"internal.example.com"}) && !f.store.Saving()
	})
	f.clickRowControl("手机", 16)
	testkit.Until(t, f.tt, func() bool { return slices.Equal(f.mitmSettings().Devices, []string{"phone"}) && !f.store.Saving() })
	f.clickRowControl("本机", 16)
	testkit.Until(t, f.tt, func() bool { return f.mitmSettings().NoLocal && !f.store.Saving() })
}

func TestHTTPSPolicyScreenshot(t *testing.T) {
	dir := os.Getenv("SETTINGS_PREVIEW_DIR")
	if dir == "" {
		t.Skip("SETTINGS_PREVIEW_DIR not set")
	}
	doc := policyDoc()
	doc.HTTPTools.MITM.Hostnames.Exclude = []string{"pay.example.com"}
	doc.HTTPTools.MITM.Upstream = &model.MITMUpstream{Fingerprint: "chrome", SkipVerify: []string{"internal.example.com"}}
	doc.HTTPTools.MITM.Devices = []string{"phone"}
	f, _ := newMITMFixture(t, policyStatus(), doc)
	f.tt.SetSize(1100, 2200)
	f.tt.Frame()
	f.save("https-policy-light.png")
	f.tt.SetDark(true)
	f.tt.Frame()
	f.save("https-policy-dark.png")
	f.tt.SetDark(false)
	f.tt.SetSize(480, 2800)
	f.tt.Frame()
	f.save("https-policy-narrow-light.png")
}

func (f *fixture) builtinOn() bool {
	m := f.mitmSettings()
	return m.BuiltinExcludeOn()
}

func TestBuiltinShown(t *testing.T) {
	m := model.MITMSettings{DisabledBuiltin: []string{"a", "b"}}
	if got := builtinShown(24, m, true); got != 22 {
		t.Errorf("enabled count %d", got)
	}
	if got := builtinShown(24, m, false); got != 0 {
		t.Errorf("count shown while the table is off: %d", got)
	}
	if got := builtinShown(1, m, true); got != 0 {
		t.Errorf("count below zero: %d", got)
	}
}
