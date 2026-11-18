package main

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/skip2/go-qrcode"
	"howett.net/plist"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// ---- Export formats ----

func TestMitmExportFormats(t *testing.T) {
	root, _, _ := mustRoot(t, time.Now())
	cases := []struct{ format, contentType, name string }{
		{"", "application/x-pem-file", "Kiko Root CA.pem"},
		{"pem", "application/x-pem-file", "Kiko Root CA.pem"},
		{"cer", "application/pkix-cert", "Kiko Root CA.cer"},
		{"crt", "application/x-x509-ca-cert", "Kiko Root CA.crt"},
		{"p12", "application/x-pkcs12", "Kiko Root CA.p12"},
		{"mobileconfig", "application/x-apple-aspen-config", "Kiko Root CA.mobileconfig"},
	}
	for _, c := range cases {
		body, contentType, name, err := mitmExportBody(root, c.format)
		if err != nil || contentType != c.contentType || name != c.name || len(body) == 0 {
			t.Fatalf("%q: %v %q %q", c.format, err, contentType, name)
		}
	}
	if _, _, _, err := mitmExportBody(root, "zip"); err == nil || !strings.Contains(err.Error(), "mobileconfig") {
		t.Fatalf("unknown format error %v", err)
	}
	pemBody, _, _, _ := mitmExportBody(root, "pem")
	if block, _ := pem.Decode(pemBody); block == nil || !bytes.Equal(block.Bytes, root.Raw) {
		t.Fatal("pem must hold the root DER")
	}
	for _, f := range []string{"cer", "crt"} {
		der, _, _, _ := mitmExportBody(root, f)
		if !bytes.Equal(der, root.Raw) {
			t.Fatalf("%s must be the root DER", f)
		}
	}
}

func TestMitmExportP12HoldsOnlyTheCertificate(t *testing.T) {
	root, key, _ := mustRoot(t, time.Now())
	data, _, _, err := mitmExportBody(root, "p12")
	if err != nil {
		t.Fatal(err)
	}
	certs, err := pkcs12.DecodeTrustStore(data, "")
	if err != nil || len(certs) != 1 || !certs[0].Equal(root) {
		t.Fatalf("trust store %v %d", err, len(certs))
	}
	if _, _, err := pkcs12.Decode(data, ""); err == nil {
		t.Fatal("p12 must not carry a private key")
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	if bytes.Contains(data, keyDER) || bytes.Contains(data, key.D.Bytes()) {
		t.Fatal("p12 contains key material")
	}
}

func TestMitmExportMobileConfig(t *testing.T) {
	root, key, _ := mustRoot(t, time.Now())
	data, _, _, err := mitmExportBody(root, "mobileconfig")
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Type       string `plist:"PayloadType"`
		Version    int    `plist:"PayloadVersion"`
		Identifier string `plist:"PayloadIdentifier"`
		UUID       string `plist:"PayloadUUID"`
		Name       string `plist:"PayloadDisplayName"`
		Content    []struct {
			Type     string `plist:"PayloadType"`
			FileName string `plist:"PayloadCertificateFileName"`
			UUID     string `plist:"PayloadUUID"`
			Content  []byte `plist:"PayloadContent"`
		} `plist:"PayloadContent"`
	}
	if _, err := plist.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	fp := certFingerprint(root.Raw)
	if profile.Type != "Configuration" || profile.Version != 1 || profile.Name != "Kiko Root CA" ||
		profile.Identifier != "io.github.getkiko.kiko.mitm.root."+fp[:16] || len(profile.Content) != 1 {
		t.Fatalf("profile %+v", profile)
	}
	payload := profile.Content[0]
	if payload.Type != "com.apple.security.root" || payload.FileName != "Kiko Root CA.cer" || !bytes.Equal(payload.Content, root.Raw) ||
		payload.UUID == profile.UUID || len(profile.UUID) != 36 {
		t.Fatalf("payload %+v", payload)
	}
	again, _, _, _ := mitmExportBody(root, "mobileconfig")
	if !bytes.Equal(again, data) {
		t.Fatal("profile must be deterministic for one root")
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	if bytes.Contains(data, key.D.Bytes()) || bytes.Contains(data, keyDER) || strings.Contains(string(data), "PRIVATE") {
		t.Fatal("profile contains key material")
	}
}

func TestMitmExportHandlerHeaders(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	root, _ := s.mitmRootSnapshot()
	mux := mitmTestMux(s)
	for format, want := range map[string]string{
		"crt": "application/x-x509-ca-cert", "p12": "application/x-pkcs12", "mobileconfig": "application/x-apple-aspen-config",
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/mitm/export?format="+format, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != want ||
			w.Header().Get("Content-Disposition") != `attachment; filename="Kiko Root CA.`+format+`"` {
			t.Fatalf("%s: %d %v", format, w.Code, w.Header())
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/mitm/export?format=zip", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "mobileconfig") {
		t.Fatalf("unknown format %d %s", w.Code, w.Body.String())
	}
	_ = root
}

// ---- Download page ----

func newShareTestServer(t *testing.T, withRoot bool) *server {
	t.Helper()
	s, _, _, _ := newMITMTestServer(t)
	if withRoot {
		if err := s.mitmGenerate(); err != nil {
			t.Fatal(err)
		}
	}
	prevAddrs, prevLife := mitmShareLocalAddresses, mitmShareLifetime
	mitmShareLocalAddresses = func() []string { return []string{"127.0.0.1"} }
	t.Cleanup(func() {
		s.mitmShareStop()
		mitmShareLocalAddresses, mitmShareLifetime = prevAddrs, prevLife
	})
	return s
}

func shareGet(t *testing.T, rawURL string) (int, http.Header, []byte) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func startShare(t *testing.T, s *server) mitmShareResponse {
	t.Helper()
	if code, msg := s.mitmShareStart(""); code != 0 {
		t.Fatalf("start %d %s", code, msg)
	}
	resp := s.mitmShareSnapshot()
	if !resp.Active {
		t.Fatalf("not active %+v", resp)
	}
	return resp
}

func TestMitmShareServesTokenPaths(t *testing.T) {
	s := newShareTestServer(t, true)
	root, _ := s.mitmRootSnapshot()
	resp := startShare(t, s)
	u, err := url.Parse(resp.URL)
	if err != nil || u.Hostname() != "127.0.0.1" || resp.Address != "127.0.0.1" {
		t.Fatalf("url %q %v", resp.URL, err)
	}
	token := strings.Trim(u.Path, "/")
	if len(token) != 22 || resp.Fingerprint != certFingerprint(root.Raw) {
		t.Fatalf("token %q fp %q", token, resp.Fingerprint)
	}
	if resp.Downloads == nil || *resp.Downloads != 0 {
		t.Fatalf("downloads %v", resp.Downloads)
	}

	code, h, body := shareGet(t, resp.URL)
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
		t.Fatalf("page %d %v", code, h)
	}
	for k, v := range map[string]string{
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; img-src data:",
		"X-Content-Type-Options":  "nosniff", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer",
	} {
		if h.Get(k) != v {
			t.Fatalf("header %s=%q", k, h.Get(k))
		}
	}
	page := string(body)
	grouped := mitmShareFingerprint(certFingerprint(root.Raw))
	for _, want := range []string{grouped, root.Subject.CommonName, "ca.mobileconfig", "ca.crt", "intent:#Intent;action=android.settings.SECURITY_SETTINGS;end", "certutil", "security add-trusted-cert", "update-ca-certificates"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	for _, bad := range []string{"PRIVATE", "http://", "https://", "<script", "<img src=\"http"} {
		if strings.Contains(page, bad) {
			t.Fatalf("page contains %q", bad)
		}
	}

	// A token path without a trailing slash redirects to the page.
	if code, h, _ := shareGet(t, strings.TrimSuffix(resp.URL, "/")); code != 302 || h.Get("Location") != "/"+token+"/" {
		t.Fatalf("redirect %d %v", code, h)
	}

	files := map[string]string{
		"ca.cer": "application/pkix-cert", "ca.crt": "application/x-x509-ca-cert", "ca.pem": "application/x-pem-file",
		"ca.p12": "application/x-pkcs12", "ca.mobileconfig": "application/x-apple-aspen-config",
	}
	n := int64(0)
	for name, ct := range files {
		code, h, body := shareGet(t, resp.URL+name)
		if code != 200 || h.Get("Content-Type") != ct || len(body) == 0 {
			t.Fatalf("%s %d %v", name, code, h)
		}
		n++
		if name == "ca.cer" || name == "ca.crt" {
			if !bytes.Equal(body, root.Raw) {
				t.Fatalf("%s body", name)
			}
		}
	}
	if code, _, _ := shareGet(t, resp.URL+"ca.key"); code != 404 {
		t.Fatalf("unknown file %d", code)
	}
	if got := s.mitmShareSnapshot().Downloads; got == nil || *got != n {
		t.Fatalf("downloads %v want %d", got, n)
	}
	// The page itself and HEAD requests do not count as downloads.
	shareGet(t, resp.URL)
	if got := s.mitmShareSnapshot().Downloads; *got != n {
		t.Fatalf("page counted %d", *got)
	}
}

func TestMitmShareWrongTokenIdenticalAndRateLimited(t *testing.T) {
	s := newShareTestServer(t, true)
	resp := startShare(t, s)
	u, _ := url.Parse(resp.URL)
	base := "http://" + u.Host
	codeA, hA, bodyA := shareGet(t, base+"/AAAAAAAAAAAAAAAAAAAAAA/")
	codeB, hB, bodyB := shareGet(t, base+"/")
	codeC, _, bodyC := shareGet(t, base+"/"+strings.Trim(u.Path, "/")+"x/ca.cer")
	if codeA != 404 || codeB != 404 || codeC != 404 || !bytes.Equal(bodyA, bodyB) || !bytes.Equal(bodyA, bodyC) {
		t.Fatalf("not identical: %d %d %d", codeA, codeB, codeC)
	}
	if hA.Get("Content-Type") != hB.Get("Content-Type") || hA.Get("Content-Security-Policy") != hB.Get("Content-Security-Policy") {
		t.Fatalf("headers differ %v %v", hA, hB)
	}
	var limited bool
	for i := 0; i < 2*mitmShareBadPerIP; i++ {
		if code, _, _ := shareGet(t, base+"/wrong"); code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("wrong tokens must be rate limited")
	}
	// The correct token is not affected by rate limiting.
	if code, _, _ := shareGet(t, resp.URL); code != 200 {
		t.Fatalf("valid token %d", code)
	}
}

func TestMitmShareLimiterGlobal(t *testing.T) {
	var l mitmShareLimiter
	limited := false
	for i := 0; i < mitmShareBadGlobal+1; i++ {
		limited = l.bad("10.0.0." + strings.Repeat("1", i%3+1))
	}
	if !limited {
		t.Fatal("global limit")
	}
}

func TestMitmShareBoundOnlyToSelectedAddress(t *testing.T) {
	s := newShareTestServer(t, true)
	resp := startShare(t, s)
	u, _ := url.Parse(resp.URL)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+u.Port(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	// The listener binds only the chosen address; other addresses in the same loopback range are unreachable.
	if other, err := net.DialTimeout("tcp", "127.0.0.2:"+u.Port(), time.Second); err == nil {
		other.Close()
		t.Fatal("listener reachable on another address")
	}
}

func TestMitmShareStopsOnDeleteRestartAndRoot(t *testing.T) {
	s := newShareTestServer(t, true)
	mux := mitmTestMux(s)
	r := &mitmRecorder{t: t, mux: mux}
	dead := func(rawURL string) bool {
		u, _ := url.Parse(rawURL)
		c, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	}

	var inactive map[string]any
	if err := json.Unmarshal(r.do("GET", "/api/v1/mitm/share", 200).Body.Bytes(), &inactive); err != nil {
		t.Fatal(err)
	}
	if inactive["active"] != false || inactive["url"] != nil || inactive["qr"] != nil || len(inactive["addresses"].([]any)) != 1 {
		t.Fatalf("inactive %v", inactive)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mitm/share", strings.NewReader(`{"address":"8.8.8.8"}`)))
	if w.Code != 400 {
		t.Fatalf("bad address %d %s", w.Code, w.Body.String())
	}

	post := func(body string) mitmShareResponse {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mitm/share", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("post %d %s", w.Code, w.Body.String())
		}
		var resp mitmShareResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		// The token appears only in the url.
		token := strings.Trim(mustURL(t, resp.URL).Path, "/")
		if strings.Count(w.Body.String(), token) != 1 {
			t.Fatalf("token must appear once: %s", w.Body.String())
		}
		return resp
	}
	first := post(`{"address":"127.0.0.1"}`)
	second := post(``)
	if first.URL == second.URL || !dead(first.URL) || dead(second.URL) {
		t.Fatalf("restart must replace the listener: %q %q", first.URL, second.URL)
	}
	if code, _, _ := shareGet(t, second.URL); code != 200 {
		t.Fatalf("second %d", code)
	}

	// DELETE closes it immediately.
	var del mitmShareResponse
	if err := json.Unmarshal(r.do("DELETE", "/api/v1/mitm/share", 200).Body.Bytes(), &del); err != nil || del.Active || !dead(second.URL) {
		t.Fatalf("delete %+v %v", del, err)
	}
	r.do("DELETE", "/api/v1/mitm/share", 200)

	// Regenerating the root certificate closes the download page.
	third := post(``)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	if !dead(third.URL) || s.mitmShareSnapshot().Active {
		t.Fatal("regeneration must stop the share")
	}
	// Deleting the root certificate closes the download page, and sharing cannot start without one.
	fourth := post(``)
	if err := s.mitmRemoveFiles(); err != nil {
		t.Fatal(err)
	}
	if !dead(fourth.URL) {
		t.Fatal("deletion must stop the share")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/mitm/share", nil))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "没有根证书") {
		t.Fatalf("no root %d %s", w.Code, w.Body.String())
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestMitmShareAutoExpiryAndNoLeak(t *testing.T) {
	s := newShareTestServer(t, true)
	mitmShareLifetime = 80 * time.Millisecond
	before := runtime.NumGoroutine()
	resp := startShare(t, s)
	eventually(t, "share expires", func() bool { return !s.mitmShareSnapshot().Active })
	u := mustURL(t, resp.URL)
	if c, err := net.DialTimeout("tcp", u.Host, 300*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener must be closed after expiry")
	}
	s.mitmShareStop()
	s.mitmShareStop()
	eventually(t, "goroutines released", func() bool { return runtime.NumGoroutine() <= before+1 })
}

func TestMitmShareStartFailsWhenPortCannotBind(t *testing.T) {
	s := newShareTestServer(t, true)
	mitmShareLocalAddresses = func() []string { return []string{"192.0.2.1"} }
	code, msg := s.mitmShareStart("")
	if code != 500 || msg == "" {
		t.Fatalf("%d %q", code, msg)
	}
	if s.mitmShareSnapshot().Active {
		t.Fatal("failed start must not be active")
	}
}

func TestMitmShareConcurrentStartStop(t *testing.T) {
	s := newShareTestServer(t, true)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 5; j++ {
				if i%2 == 0 {
					s.mitmShareStart("")
				} else {
					s.mitmShareStop()
				}
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	s.mitmShareStop()
	if s.mitmShareSnapshot().Active {
		t.Fatal("must be stopped")
	}
}

func TestMitmShareQR(t *testing.T) {
	s := newShareTestServer(t, true)
	resp := startShare(t, s)
	q, err := qrcode.New(resp.URL, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	q.DisableBorder = true
	want := q.Bitmap()
	if len(resp.QR) != len(want) || len(want) < 21 || (len(want)-17)%4 != 0 {
		t.Fatalf("size %d", len(resp.QR))
	}
	for y, row := range resp.QR {
		if len(row) != len(want) {
			t.Fatalf("row %d width", y)
		}
		for x, c := range row {
			if (c == '1') != want[y][x] {
				t.Fatalf("module %d,%d", x, y)
			}
		}
	}
	// Three finder patterns: dark 7x7 outer ring and dark 3x3 center.
	n := len(resp.QR)
	for _, o := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		for d := 0; d < 7; d++ {
			if resp.QR[o[1]][o[0]+d] != '1' || resp.QR[o[1]+6][o[0]+d] != '1' || resp.QR[o[1]+d][o[0]] != '1' || resp.QR[o[1]+d][o[0]+6] != '1' {
				t.Fatalf("finder at %v", o)
			}
		}
		if resp.QR[o[1]+3][o[0]+3] != '1' || resp.QR[o[1]+1][o[0]+1] != '0' {
			t.Fatalf("finder core at %v", o)
		}
	}
}

func TestMitmShareCandidateFilter(t *testing.T) {
	// Real enumeration contains no loopback or public addresses.
	for _, a := range mitmLocalShareAddresses() {
		ip := net.ParseIP(a)
		if ip == nil || ip.IsLoopback() || (!ip.IsPrivate() && !ip.IsLinkLocalUnicast()) {
			t.Fatalf("candidate %q", a)
		}
	}
}
