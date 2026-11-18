package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func newSysProxyTestServer(t *testing.T, runtimeConfig string) *server {
	t.Helper()
	s := &server{dataDir: t.TempDir(), listenAddr: "0.0.0.0:9099", logs: newLogBuffer(t.TempDir())}
	s.loadSysProxy()
	if runtimeConfig != "" {
		if err := os.MkdirAll(filepath.Join(s.dataDir, "runtime"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dataDir, "runtime", "config.json"), []byte(runtimeConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func putSysProxyRequest(s *server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.putSysProxy(rec, httptest.NewRequest(http.MethodPut, "/api/v1/system-proxy", strings.NewReader(body)))
	return rec
}

func TestProxyTargetUsesFirstMixedAndDetectsTun(t *testing.T) {
	s := newSysProxyTestServer(t, `{"inbounds":[{"type":"socks","listen_port":1},{"type":"mixed","listen":"192.168.1.5","listen_port":7890},{"type":"mixed","listen_port":7891}]}`)
	if port, listen, tun := s.proxyTarget(); port != 7890 || listen != "192.168.1.5" || tun {
		t.Fatalf("port=%d listen=%q tun=%v", port, listen, tun)
	}
	s = newSysProxyTestServer(t, `{"inbounds":[{"type":"tun"},{"type":"mixed","listen_port":7890}]}`)
	if port, listen, tun := s.proxyTarget(); port != 7890 || listen != "" || !tun {
		t.Fatalf("port=%d listen=%q tun=%v", port, listen, tun)
	}
}

func TestAutoProxyHostFollowsConcreteListenAddress(t *testing.T) {
	for listen, want := range map[string]string{
		"": "127.0.0.1", "0.0.0.0": "127.0.0.1", "::": "127.0.0.1", "[::]": "127.0.0.1",
		"127.0.0.1": "127.0.0.1", "192.168.1.5": "192.168.1.5", "::1": "::1", " 10.0.0.2 ": "10.0.0.2",
	} {
		if got := autoProxyHost(listen); got != want {
			t.Errorf("autoProxyHost(%q)=%q want %q", listen, got, want)
		}
	}
}

func TestBuildSysProxyApplyHostAutoOrExplicit(t *testing.T) {
	s := newSysProxyTestServer(t, `{"inbounds":[{"type":"mixed","listen":"192.168.1.5","listen_port":7890}]}`)
	st := defaultSysProxySettings()
	if st.Host != "" {
		t.Fatalf("default host should be auto (empty), got %q", st.Host)
	}
	apply, err := s.buildSysProxyApply(st)
	if err != nil || apply.Host != "192.168.1.5" {
		t.Fatalf("auto apply=%+v err=%v", apply, err)
	}
	st.Host = "example.lan"
	if apply, err = s.buildSysProxyApply(st); err != nil || apply.Host != "example.lan" {
		t.Fatalf("explicit apply=%+v err=%v", apply, err)
	}
	if got := s.sysProxyStatus().EffectiveHost; got != "192.168.1.5" {
		t.Fatalf("effective host=%q", got)
	}
}

func TestLoadSysProxyTreatsLegacyLoopbackHostAsAuto(t *testing.T) {
	s := newSysProxyTestServer(t, "")
	if err := os.WriteFile(s.sysProxyPath(), []byte(`{"host":"127.0.0.1","guardInterval":30,"defaultBypass":true,"validateBypass":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.loadSysProxy()
	if s.sys.settings.Host != "" {
		t.Fatalf("legacy default host should become auto, got %q", s.sys.settings.Host)
	}
	if err := os.WriteFile(s.sysProxyPath(), []byte(`{"host":"proxy.lan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.loadSysProxy()
	if s.sys.settings.Host != "proxy.lan" {
		t.Fatalf("explicit host must be kept, got %q", s.sys.settings.Host)
	}
}

func TestSysProxySettingsAllowEmptyHost(t *testing.T) {
	st := defaultSysProxySettings()
	st.normalize()
	if st.Host != "" || st.validate() != nil {
		t.Fatalf("empty host must stay valid and empty: %+v err=%v", st, st.validate())
	}
	st.Host = "bad host!"
	if st.validate() == nil {
		t.Fatal("invalid host should still be rejected")
	}
}

func TestBuildSysProxyApplyRejectsTunAndMissingMixed(t *testing.T) {
	st := defaultSysProxySettings()
	tunServer := newSysProxyTestServer(t, `{"inbounds":[{"type":"tun"},{"type":"mixed","listen_port":7890}]}`)
	if _, err := tunServer.buildSysProxyApply(st); err != errSysProxyTun {
		t.Fatalf("err=%v", err)
	}
	noMixed := newSysProxyTestServer(t, `{"inbounds":[{"type":"socks","listen_port":1080}]}`)
	if _, err := noMixed.buildSysProxyApply(st); err != errSysProxyNoMixed {
		t.Fatalf("err=%v", err)
	}
	ok := newSysProxyTestServer(t, `{"inbounds":[{"type":"mixed","listen_port":7890}]}`)
	st.PAC = true
	apply, err := ok.buildSysProxyApply(st)
	if err != nil || apply.Port != 7890 || apply.PAC != "http://127.0.0.1:9099/api/v1/system-proxy/pac" {
		t.Fatalf("apply=%+v err=%v", apply, err)
	}
}

func TestPutSysProxyValidationAndConflicts(t *testing.T) {
	if !sysProxySupported() {
		t.Skip("system proxy unsupported")
	}
	s := newSysProxyTestServer(t, `{"inbounds":[{"type":"tun"},{"type":"mixed","listen_port":7890}]}`)
	if rec := putSysProxyRequest(s, `{"enabled":true,"host":"127.0.0.1","defaultBypass":true,"validateBypass":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("tun code=%d", rec.Code)
	}
	if rec := putSysProxyRequest(s, `{"enabled":false,"validateBypass":true,"bypass":["bad entry!"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid bypass code=%d", rec.Code)
	}
	if rec := putSysProxyRequest(s, `{"enabled":false,"validateBypass":false,"bypass":["bad entry!"]}`); rec.Code != http.StatusOK {
		t.Fatalf("unvalidated bypass code=%d body=%s", rec.Code, rec.Body)
	}
	if s.sys.settings.Enabled || s.sys.applied {
		t.Fatalf("settings=%+v applied=%v", s.sys.settings, s.sys.applied)
	}
}

func TestEffectiveBypass(t *testing.T) {
	st := defaultSysProxySettings()
	st.Bypass = []string{"localhost", "example.com"}
	got := st.effectiveBypass()
	if len(got) != len(defaultProxyBypass)+1 || got[len(got)-1] != "example.com" {
		t.Fatalf("got=%v", got)
	}
	st.DefaultBypass = false
	if got = st.effectiveBypass(); !slices.Equal(got, []string{"localhost", "example.com"}) {
		t.Fatalf("got=%v", got)
	}
}

func TestValidBypassEntry(t *testing.T) {
	for _, entry := range []string{"<local>", "127.0.0.1", "10.0.0.0/8", "::1", "localhost", "*.local", ".example.com", "192.168.*"} {
		if !validBypassEntry(entry) {
			t.Errorf("%q should be valid", entry)
		}
	}
	for _, entry := range []string{"bad entry", "a/b", "-x", "http://x"} {
		if validBypassEntry(entry) {
			t.Errorf("%q should be invalid", entry)
		}
	}
}

func TestWinBypassExpandsCIDR(t *testing.T) {
	got := winBypass([]string{"10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12", "1.2.3.4/32", "localhost", "<local>"})
	if len(got) != 1+1+16+1+2 {
		t.Fatalf("got=%v", got)
	}
	for _, want := range []string{"10.*", "192.168.*", "172.16.*", "172.31.*", "1.2.3.4", "localhost", "<local>"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func TestBuildPAC(t *testing.T) {
	pac := buildPAC(sysProxyApply{Host: "127.0.0.1", Port: 7890, Bypass: []string{"<local>", "10.0.0.0/8", "*.local", ".example.com"}})
	for _, want := range []string{
		`isPlainHostName(host)`,
		`isInNet(host, "10.0.0.0", "255.0.0.0")`,
		`shExpMatch(host, "*.local")`,
		`shExpMatch(host, "*.example.com")`,
		`return "PROXY 127.0.0.1:7890; SOCKS5 127.0.0.1:7890; SOCKS 127.0.0.1:7890; DIRECT";`,
	} {
		if !strings.Contains(pac, want) {
			t.Errorf("PAC missing %q:\n%s", want, pac)
		}
	}
}
