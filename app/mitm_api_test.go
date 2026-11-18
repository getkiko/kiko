package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/common/mitm/hostmatch"
	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeMITM fakes the core's MITM service.
type fakeMITM struct {
	mitmapi.UnimplementedMITMServer
	mu            sync.Mutex
	unimplemented bool
	source        string
	status        *mitmapi.Status
	requests      []*mitmapi.SetAuthorityRequest
	calls         int
	setErr        error
	cleared       []string
}

func newFakeMITM() *fakeMITM {
	return &fakeMITM{source: "managed", status: &mitmapi.Status{Enabled: true, AuthorityPending: true, CertificateSource: "managed", LeafCacheSize: 3}}
}

func (f *fakeMITM) GetCapabilities(context.Context, *emptypb.Empty) (*mitmapi.Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.unimplemented {
		return nil, status.Error(codes.Unimplemented, "unknown service")
	}
	return &mitmapi.Capabilities{SchemaVersion: 1}, nil
}

func (f *fakeMITM) GetStatus(context.Context, *emptypb.Empty) (*mitmapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.unimplemented {
		return nil, status.Error(codes.Unimplemented, "unknown service")
	}
	return cloneStatus(f.status), nil
}

func (f *fakeMITM) SetAuthority(_ context.Context, request *mitmapi.SetAuthorityRequest) (*mitmapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.setErr != nil {
		return nil, f.setErr
	}
	f.requests = append(f.requests, request)
	next := cloneStatus(f.status)
	if len(request.CertChainPem) == 0 {
		next.AuthorityPending, next.AuthorityFingerprint, next.RootFingerprint, next.AuthorityNotAfter = true, "", "", 0
	} else {
		block, _ := pem.Decode(request.CertChainPem)
		next.AuthorityPending, next.AuthorityFingerprint = false, certFingerprint(block.Bytes)
		next.RootFingerprint, next.AuthorityNotAfter = request.RootFingerprint, request.NotAfter
	}
	f.status = next
	return cloneStatus(next), nil
}

func cloneStatus(s *mitmapi.Status) *mitmapi.Status {
	return &mitmapi.Status{Enabled: s.Enabled, AuthorityFingerprint: s.AuthorityFingerprint, AuthorityPending: s.AuthorityPending,
		LeafCacheSize: s.LeafCacheSize, RootFingerprint: s.RootFingerprint, AuthorityNotAfter: s.AuthorityNotAfter, CertificateSource: s.CertificateSource,
		Skip: append([]*mitmapi.SkipEntry{}, s.Skip...), Failures: append([]*mitmapi.Failure{}, s.Failures...)}
}

func (f *fakeMITM) ClearSkip(_ context.Context, request *mitmapi.ClearSkipRequest) (*mitmapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, request.GetHost())
	kept := f.status.Skip[:0:0]
	for _, entry := range f.status.Skip {
		if request.GetHost() != "" && entry.GetHost() != request.GetHost() {
			kept = append(kept, entry)
		}
	}
	f.status.Skip = kept
	return cloneStatus(f.status), nil
}

func (f *fakeMITM) sent() []*mitmapi.SetAuthorityRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*mitmapi.SetAuthorityRequest{}, f.requests...)
}

func (f *fakeMITM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeMITM) setStatus(update func(*mitmapi.Status)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f.status)
}

func startFakeMITM(t *testing.T, fake *fakeMITM, started daemon.StartedServiceServer) (string, mitmapi.MITMClient) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			if err := checkAuth(ctx); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			if err := checkAuth(stream.Context()); err != nil {
				return err
			}
			return handler(srv, stream)
		}),
	)
	if fake != nil {
		mitmapi.RegisterMITMServer(server, fake)
	}
	if started != nil {
		daemon.RegisterStartedServiceServer(server, started)
	}
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + listener.Addr().String(), Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return listener.Addr().String(), mitmapi.NewMITMClient(conn)
}

func startMITMSession(t *testing.T, s *server, client mitmapi.MITMClient, run int64) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.mitmSession(ctx, client, run)
	}()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return sync.OnceFunc(stop)
}

func logLines(s *server) string {
	s.logs.mu.Lock()
	defer s.logs.mu.Unlock()
	return strings.Join(s.logs.lines, "\n")
}

// checkPushed verifies the pushed authority: intermediate CA first, root
// second, the private key matches the intermediate CA, and expiry times agree.
func checkPushed(t *testing.T, s *server, request *mitmapi.SetAuthorityRequest) string {
	t.Helper()
	root, _ := s.mitmRootSnapshot()
	rest := request.CertChainPem
	var chain []*x509.Certificate
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, cert)
	}
	if len(chain) != 2 || !chain[1].Equal(root) || chain[0].CheckSignatureFrom(root) != nil {
		t.Fatalf("chain %d", len(chain))
	}
	if request.RootFingerprint != certFingerprint(root.Raw) || request.NotAfter != chain[0].NotAfter.Unix() {
		t.Fatalf("request %s %d", request.RootFingerprint, request.NotAfter)
	}
	key := mustParseKey(t, request.KeyPkcs8)
	if !key.PublicKey.Equal(chain[0].PublicKey) {
		t.Fatal("key does not match the intermediate")
	}
	return certFingerprint(chain[0].Raw)
}

func TestMITMSessionPushAndResync(t *testing.T) {
	s, secrets, _, clock := newMITMTestServer(t)
	s.mitm.interval = 20 * time.Millisecond
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeMITM()
	_, client := startFakeMITM(t, fake, nil)
	stop := startMITMSession(t, s, client, 7)
	eventually(t, "first push", func() bool { return len(fake.sent()) == 1 })
	first := checkPushed(t, s, fake.sent()[0])
	if !strings.Contains(logLines(s), "[kiko] HTTPS 解密证书已下发 "+first[:16]) {
		t.Fatalf("log %s", logLines(s))
	}
	time.Sleep(100 * time.Millisecond)
	if len(fake.sent()) != 1 {
		t.Fatal("matching fingerprint must not re-push")
	}

	fake.setStatus(func(st *mitmapi.Status) { st.AuthorityPending = true })
	eventually(t, "re-push on pending", func() bool { return len(fake.sent()) == 2 })
	fake.setStatus(func(st *mitmapi.Status) { st.AuthorityFingerprint = "other" })
	eventually(t, "re-push on mismatch", func() bool { return len(fake.sent()) == 3 })
	for _, request := range fake.sent() {
		if checkPushed(t, s, request) != first {
			t.Fatal("same run must keep the intermediate")
		}
	}

	clock.Advance(24 * 24 * time.Hour)
	eventually(t, "renewal push", func() bool { return len(fake.sent()) == 4 })
	renewed := checkPushed(t, s, fake.sent()[3])
	if renewed == first {
		t.Fatal("renewal must push a new intermediate")
	}

	// Reconnecting to the same run: no re-push if the core already has the
	// certificate; after a core restart the same one is pushed again.
	stop()
	stop = startMITMSession(t, s, client, 7)
	time.Sleep(100 * time.Millisecond)
	if len(fake.sent()) != 4 {
		t.Fatal("reconnect with loaded authority must not push")
	}
	stop()
	fresh := newFakeMITM()
	_, freshClient := startFakeMITM(t, fresh, nil)
	stop = startMITMSession(t, s, freshClient, 7)
	eventually(t, "reconnect push", func() bool { return len(fresh.sent()) == 1 })
	if checkPushed(t, s, fresh.sent()[0]) != renewed {
		t.Fatal("reconnect in the same run must reuse the intermediate")
	}
	stop()
	next := newFakeMITM()
	_, nextClient := startFakeMITM(t, next, nil)
	startMITMSession(t, s, nextClient, 8)
	eventually(t, "new run push", func() bool { return len(next.sent()) == 1 })
	if checkPushed(t, s, next.sent()[0]) == renewed {
		t.Fatal("new run must issue a new intermediate")
	}
	for _, request := range append(fake.sent(), next.sent()...) {
		assertNoKey(t, "chain", request.CertChainPem, secrets.snapshot(), request.KeyPkcs8)
		assertNoKey(t, "fingerprint", []byte(request.RootFingerprint), secrets.snapshot(), request.KeyPkcs8)
		if string(request.KeyPkcs8) == string(secrets.snapshot()) {
			t.Fatal("root key sent to the core")
		}
	}
	assertNoKey(t, "logs", []byte(logLines(s)), secrets.snapshot(), fake.sent()[0].KeyPkcs8, next.sent()[0].KeyPkcs8)
}

func TestMITMSessionSkipsFileSourceAndAbsentRoot(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	s.mitm.interval = 20 * time.Millisecond
	fake := newFakeMITM()
	_, client := startFakeMITM(t, fake, nil)
	startMITMSession(t, s, client, 1)
	eventually(t, "status polled", func() bool { return fake.callCount() >= 4 })
	if len(fake.sent()) != 0 {
		t.Fatal("no root must not push")
	}
	fake.setStatus(func(st *mitmapi.Status) { st.CertificateSource = "file"; st.AuthorityFingerprint = "file-ca" })
	// A poll that read the managed status before the switch could still see
	// the root generated next; wait until a poll has started after it.
	switched := fake.callCount()
	eventually(t, "poll after the switch", func() bool { return fake.callCount() >= switched+2 })
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	calls := fake.callCount()
	eventually(t, "more polls", func() bool { return fake.callCount() >= calls+3 })
	if len(fake.sent()) != 0 {
		t.Fatal("file source must not receive a managed authority")
	}
}

// When the core has decryption disabled or uses file certificates, nothing is
// issued and the credential store is not read; once enabled and applied, the
// certificate is pushed immediately.
func TestMITMSessionSkipsDisabledCore(t *testing.T) {
	s, secrets, _, _ := newMITMTestServer(t)
	s.mitm.interval = time.Hour
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeMITM()
	fake.setStatus(func(st *mitmapi.Status) { st.Enabled = false })
	_, client := startFakeMITM(t, fake, nil)
	startMITMSession(t, s, client, 1)
	eventually(t, "connected", func() bool { return fake.callCount() >= 2 })
	s.mitmSyncNow(t.Context())
	fake.setStatus(func(st *mitmapi.Status) { st.Enabled = true; st.CertificateSource = "file" })
	s.mitmSyncNow(t.Context())
	if len(fake.sent()) != 0 || secrets.loadCount() != 0 || strings.Contains(logLines(s), "下发失败") {
		t.Fatalf("sent=%d loads=%d log=%s", len(fake.sent()), secrets.loadCount(), logLines(s))
	}
	fake.setStatus(func(st *mitmapi.Status) { st.CertificateSource = "managed" })
	s.mitmSyncAfterApply()
	eventually(t, "push after apply", func() bool { return len(fake.sent()) == 1 })
	// Deleting the root certificate still clears it from the core after
	// decryption is disabled.
	fake.setStatus(func(st *mitmapi.Status) { st.Enabled = false })
	if err := s.mitmRemoveFiles(); err != nil {
		t.Fatal(err)
	}
	s.mitmSyncNow(t.Context())
	if sent := fake.sent(); len(sent) != 2 || len(sent[1].CertChainPem) != 0 {
		t.Fatalf("clear not pushed: %d", len(sent))
	}
}

func TestMITMSessionPushFailureLoggedOnce(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	s.mitm.interval = 20 * time.Millisecond
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeMITM()
	fake.setErr = status.Error(codes.InvalidArgument, "chain rejected")
	_, client := startFakeMITM(t, fake, nil)
	startMITMSession(t, s, client, 1)
	eventually(t, "retries", func() bool { return fake.callCount() >= 6 })
	if got := strings.Count(logLines(s), "[kiko] HTTPS 解密证书下发失败（InvalidArgument）"); got != 1 || strings.Contains(logLines(s), "chain rejected") {
		t.Fatalf("failure logged %d times: %s", got, logLines(s))
	}
}

func TestMITMSessionUnimplementedCore(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	s.mitm.interval = 20 * time.Millisecond
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeMITM()
	fake.unimplemented = true
	_, client := startFakeMITM(t, fake, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.mitmSession(ctx, client, 3)
	s.mitmSession(ctx, client, 3)
	if fake.callCount() != 1 {
		t.Fatalf("calls=%d", fake.callCount())
	}
	if st := s.mitmStatus(ctx); st.Supported || st.Core.Connected {
		t.Fatalf("status %+v", st)
	}
	// A missing service (older core) is likewise called only once.
	_, missing := startFakeMITM(t, nil, nil)
	s.mitmSession(ctx, missing, 4)
	if !s.mitm.coreUnsupported {
		t.Fatal("missing service must mark unsupported")
	}
	fake.mu.Lock()
	fake.unimplemented = false
	fake.mu.Unlock()
	startMITMSession(t, s, client, 5)
	eventually(t, "new run retries", func() bool { return len(fake.sent()) == 1 })
	if !s.mitmStatus(ctx).Supported {
		t.Fatal("supported core must clear the flag")
	}
}

// grpcSession starts pushing after the first status frame.
func TestGRPCWorkerStartsMITMSession(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeMITM()
	address, _ := startFakeMITM(t, fake, &fakeStarted{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.grpcWorker(ctx, "http://"+address, testSecret) }()
	eventually(t, "push through grpc worker", func() bool { return len(fake.sent()) == 1 })
	checkPushed(t, s, fake.sent()[0])
	if st := s.mitmStatus(ctx); !st.Core.Connected || *st.Core.Pending || st.Core.AuthorityFingerprint == "" {
		t.Fatalf("core %+v", st.Core)
	}
	cancel()
	<-done
	// mitmSession runs detached from the session's collectors.
	eventually(t, "session end disconnects", func() bool { return !s.mitmStatus(context.Background()).Core.Connected })
}

func mitmTestMux(s *server) *routeMux {
	m := newRouteMux(nil)
	s.registerMITMRoutes(m)
	return m
}

type mitmRecorder struct {
	t      *testing.T
	mux    *routeMux
	bodies []string
}

func (r *mitmRecorder) do(method, path string, code int) *httptest.ResponseRecorder {
	r.t.Helper()
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	if w.Code != code {
		r.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	r.bodies = append(r.bodies, w.Body.String())
	return w
}

func (r *mitmRecorder) status(method, path string, code int) mitmStatusResponse {
	r.t.Helper()
	w := r.do(method, path, code)
	var result mitmStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		r.t.Fatalf("%s: %v", w.Body.String(), err)
	}
	return result
}

func TestMITMRESTLifecycle(t *testing.T) {
	s, secrets, trust, clock := newMITMTestServer(t)
	fake := newFakeMITM()
	_, client := startFakeMITM(t, fake, nil)
	s.mitm.interval = time.Hour
	startMITMSession(t, s, client, 1)
	eventually(t, "connected", func() bool { return s.mitmStatus(t.Context()).Core.Connected })
	r := &mitmRecorder{t: t, mux: mitmTestMux(s)}

	st := r.status("GET", "/api/v1/mitm/status", 200)
	if !st.Supported || st.CA.State != "absent" || st.CA.Fingerprint != "" || st.CA.Trusted || !st.CA.TrustSupported || !st.Core.Connected || st.Core.CertificateSource != "managed" {
		t.Fatalf("absent %+v", st)
	}
	r.do("GET", "/api/v1/mitm/export", 404)
	r.do("POST", "/api/v1/mitm/trust", 409)
	r.do("DELETE", "/api/v1/mitm/trust", 409)

	st = r.status("POST", "/api/v1/mitm/ca", 200)
	root, _ := s.mitmRootSnapshot()
	rootKeys := [][]byte{secrets.snapshot()}
	if st.CA.State != "ready" || st.CA.Fingerprint != certFingerprint(root.Raw) || st.CA.CommonName != root.Subject.CommonName ||
		!st.CA.NotBefore.Equal(root.NotBefore) || !st.CA.NotAfter.Equal(root.NotAfter) || st.CA.Trusted {
		t.Fatalf("ready %+v", st.CA)
	}
	if len(fake.sent()) != 1 || st.Core.Pending == nil || *st.Core.Pending || st.Core.RootFingerprint != st.CA.Fingerprint || st.Core.AuthorityNotAfter == nil {
		t.Fatalf("generate must push immediately: %d %+v", len(fake.sent()), st.Core)
	}
	r.do("POST", "/api/v1/mitm/ca", 409)

	pemBody := r.do("GET", "/api/v1/mitm/export", 200)
	block, _ := pem.Decode(pemBody.Body.Bytes())
	if block == nil || string(block.Bytes) != string(root.Raw) || pemBody.Header().Get("Content-Type") != "application/x-pem-file" ||
		pemBody.Header().Get("Content-Disposition") != `attachment; filename="Kiko Root CA.pem"` {
		t.Fatalf("pem export %v", pemBody.Header())
	}
	cer := r.do("GET", "/api/v1/mitm/export?format=cer", 200)
	if cer.Body.String() != string(root.Raw) || cer.Header().Get("Content-Type") != "application/pkix-cert" ||
		cer.Header().Get("Content-Disposition") != `attachment; filename="Kiko Root CA.cer"` {
		t.Fatalf("cer export %v", cer.Header())
	}
	r.do("GET", "/api/v1/mitm/export?format=bogus", 400)

	if st = r.status("POST", "/api/v1/mitm/trust", 200); !st.CA.Trusted || trust.installs != 1 {
		t.Fatalf("install %+v", st.CA)
	}
	if st = r.status("DELETE", "/api/v1/mitm/trust", 200); st.CA.Trusted || trust.removes != 1 {
		t.Fatalf("remove %+v", st.CA)
	}
	trust.installErr = errTrustCanceled
	if body := r.do("POST", "/api/v1/mitm/trust", 409).Body.String(); !strings.Contains(body, "已取消授权") {
		t.Fatal(body)
	}
	trust.installErr = nil
	r.status("POST", "/api/v1/mitm/trust", 200)

	// The old root certificate is kept when removing the old trust fails.
	trust.removeErr = errTrustCanceled
	r.do("POST", "/api/v1/mitm/ca/regenerate", 409)
	if current, _ := s.mitmRootSnapshot(); !current.Equal(root) || len(fake.sent()) != 1 {
		t.Fatal("failed trust removal must keep the old root")
	}
	trust.removeErr = nil
	clock.Advance(time.Second)
	st = r.status("POST", "/api/v1/mitm/ca/regenerate", 200)
	if st.CA.Fingerprint == certFingerprint(root.Raw) || st.CA.Trusted || st.CA.State != "ready" {
		t.Fatalf("regenerate %+v", st.CA)
	}
	if len(fake.sent()) != 2 || fake.sent()[1].RootFingerprint != st.CA.Fingerprint || st.Core.RootFingerprint != st.CA.Fingerprint {
		t.Fatal("regenerate must push the new root's intermediate")
	}
	rootKeys = append(rootKeys, secrets.snapshot())
	caFile, _ := os.ReadFile(filepath.Join(s.dataDir, "mitm", "ca.json"))

	// broken: with the private key lost, the public certificate can still be
	// exported and regenerated.
	_ = secrets.Delete()
	s.mitm.mu.Lock()
	s.mitmResetLocked()
	s.mitm.mu.Unlock()
	s.mitmRefreshTrust(true)
	if st = r.status("GET", "/api/v1/mitm/status", 200); st.CA.State != "broken" || st.CA.Fingerprint == "" {
		t.Fatalf("broken %+v", st.CA)
	}
	r.do("POST", "/api/v1/mitm/ca", 409)
	r.do("POST", "/api/v1/mitm/trust", 409)
	r.do("GET", "/api/v1/mitm/export", 200)
	if st = r.status("POST", "/api/v1/mitm/ca/regenerate", 200); st.CA.State != "ready" {
		t.Fatalf("regenerate broken %+v", st.CA)
	}
	rootKeys = append(rootKeys, secrets.snapshot())

	st = r.status("DELETE", "/api/v1/mitm/ca", 200)
	if st.CA.State != "absent" || st.CA.Fingerprint != "" || len(secrets.snapshot()) != 0 {
		t.Fatalf("delete %+v", st.CA)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "mitm", "ca.json")); !os.IsNotExist(err) {
		t.Fatal("ca.json must be removed")
	}
	last := fake.sent()[len(fake.sent())-1]
	if len(last.CertChainPem) != 0 || len(last.KeyPkcs8) != 0 || last.RootFingerprint != "" || st.Core.AuthorityFingerprint != "" || !*st.Core.Pending {
		t.Fatalf("delete must clear the core: %+v", st.Core)
	}
	if !strings.Contains(logLines(s), "[kiko] HTTPS 解密证书已清除") {
		t.Fatal(logLines(s))
	}
	r.do("POST", "/api/v1/mitm/ca", 200)
	rootKeys = append(rootKeys, secrets.snapshot())
	keys := append([][]byte{}, rootKeys...)
	for _, request := range fake.sent() {
		if len(request.KeyPkcs8) > 0 {
			keys = append(keys, request.KeyPkcs8)
		}
	}
	if len(rootKeys) != 4 || len(keys) != 8 {
		t.Fatalf("keys %d %d", len(rootKeys), len(keys))
	}

	for _, body := range r.bodies {
		assertNoKey(t, "REST", []byte(body), keys...)
	}
	assertNoKey(t, "ca.json", caFile, keys...)
	current, _ := os.ReadFile(filepath.Join(s.dataDir, "mitm", "ca.json"))
	assertNoKey(t, "ca.json", current, keys...)
	assertNoKey(t, "logs", []byte(logLines(s)), keys...)
	for _, request := range fake.sent() {
		assertNoKey(t, "core chain", append(append([]byte{}, request.CertChainPem...), request.RootFingerprint...), keys...)
		assertNoKey(t, "core key", request.KeyPkcs8, rootKeys...)
	}
}

func TestMITMRESTTrustUnsupported(t *testing.T) {
	s, _, trust, _ := newMITMTestServer(t)
	trust.supported = false
	r := &mitmRecorder{t: t, mux: mitmTestMux(s)}
	if st := r.status("POST", "/api/v1/mitm/ca", 200); st.CA.TrustSupported || st.Core.Connected || st.Core.Enabled != nil {
		t.Fatalf("status %+v", st)
	}
	r.do("POST", "/api/v1/mitm/trust", 501)
	r.do("DELETE", "/api/v1/mitm/trust", 501)
	r.status("POST", "/api/v1/mitm/ca/regenerate", 200)
	r.status("DELETE", "/api/v1/mitm/ca", 200)
	if trust.installs != 0 || trust.removes != 0 {
		t.Fatal("unsupported trust store must not be called")
	}
	// Core fields are omitted while disconnected.
	body := r.do("GET", "/api/v1/mitm/status", 200).Body.String()
	if !strings.Contains(body, `"core":{"connected":false}`) {
		t.Fatal(body)
	}
}

func keyForms(key []byte) []string {
	forms := []string{string(key), hex.EncodeToString(key), base64.StdEncoding.EncodeToString(key)[:40]}
	if parsed, err := x509.ParsePKCS8PrivateKey(key); err == nil {
		if k, ok := parsed.(*ecdsa.PrivateKey); ok {
			d, _ := k.Bytes()
			forms = append(forms, string(d), hex.EncodeToString(d), base64.StdEncoding.EncodeToString(d)[:40], base64.RawURLEncoding.EncodeToString(d)[:40])
		}
	}
	return forms
}

// Previous trust is restored when generation or deletion fails and the old
// root certificate is kept.
func TestMITMRESTRestoresTrustOnFailure(t *testing.T) {
	s, secrets, trust, _ := newMITMTestServer(t)
	r := &mitmRecorder{t: t, mux: mitmTestMux(s)}
	r.status("POST", "/api/v1/mitm/ca", 200)
	if st := r.status("POST", "/api/v1/mitm/trust", 200); !st.CA.Trusted {
		t.Fatal("trusted")
	}
	s.mitm.prepareCA = func(string, []byte) (string, error) { return "", errors.New("disk full") }
	r.do("POST", "/api/v1/mitm/ca/regenerate", 500)
	if st := r.status("GET", "/api/v1/mitm/status", 200); !st.CA.Trusted || trust.installs != 2 {
		t.Fatalf("regenerate failure: %+v installs=%d", st.CA, trust.installs)
	}
	s.mitm.prepareCA = nil
	_ = secrets
	s.mitm.renameCA = func(string, string) error { return errors.New("rename") }
	r.do("POST", "/api/v1/mitm/ca/regenerate", 500)
	if st := r.status("POST", "/api/v1/mitm/trust", 200); !st.CA.Trusted {
		t.Fatalf("rename failure: %+v", st.CA)
	}
}

// A stale root in the core is replaced or cleared even when decryption is
// disabled.
func TestMITMSessionReplacesStaleRootWhenDisabled(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	s.mitm.interval = time.Hour
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	fake := newFakeMITM()
	fake.setStatus(func(st *mitmapi.Status) { st.Enabled = true })
	_, client := startFakeMITM(t, fake, nil)
	startMITMSession(t, s, client, 1)
	eventually(t, "initial push", func() bool { return len(fake.sent()) == 1 })
	fake.setStatus(func(st *mitmapi.Status) { st.Enabled = false })
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	s.mitmSyncNow(t.Context())
	sent := fake.sent()
	if len(sent) != 2 || len(sent[1].CertChainPem) == 0 {
		t.Fatalf("replacement not pushed: %d", len(sent))
	}
	fake.setStatus(func(st *mitmapi.Status) { st.Enabled = false })
	if err := s.mitmRemoveFiles(); err != nil {
		t.Fatal(err)
	}
	s.mitmSyncNow(t.Context())
	if sent = fake.sent(); len(sent) != 3 || len(sent[2].CertChainPem) != 0 {
		t.Fatalf("clear not pushed: %d", len(sent))
	}
}

func TestMITMRESTSkipFailuresAndClear(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	fake := newFakeMITM()
	until := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	failedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	fake.setStatus(func(st *mitmapi.Status) {
		st.Skip = []*mitmapi.SkipEntry{
			{Host: "pin.example.com", Source: "local", Until: until.Unix(), Reason: "certificate pinning"},
			{Host: "other.example.com", Until: until.Unix(), Reason: "x"},
		}
		st.Failures = []*mitmapi.Failure{{Host: "pin.example.com", Reason: "tls: bad certificate", Time: failedAt.Unix(), Count: 3}}
	})
	_, client := startFakeMITM(t, fake, nil)
	s.mitm.interval = time.Hour
	startMITMSession(t, s, client, 1)
	eventually(t, "connected", func() bool { return s.mitmStatus(t.Context()).Core.Connected })
	r := &mitmRecorder{t: t, mux: mitmTestMux(s)}

	st := r.status("GET", "/api/v1/mitm/status", 200)
	if len(st.Skip) != 2 || st.Skip[0].Host != "pin.example.com" || st.Skip[0].Source != "local" || !st.Skip[0].Until.Equal(until) || st.Skip[0].Reason != "certificate pinning" {
		t.Fatalf("skip %+v", st.Skip)
	}
	if len(st.Failures) != 1 || st.Failures[0].Count != 3 || st.Failures[0].LastError != "tls: bad certificate" || !st.Failures[0].At.Equal(failedAt) {
		t.Fatalf("failures %+v", st.Failures)
	}
	if st.BuiltinCount <= 0 || st.BuiltinCount != hostmatch.BuiltinCount() {
		t.Fatalf("builtin count is the compiled table size: %d", st.BuiltinCount)
	}

	s.mitm.policySupported = false
	if st = r.status("GET", "/api/v1/mitm/status", 200); st.BuiltinCount != 0 {
		t.Fatalf("builtin row stays for a core without policy options: %d", st.BuiltinCount)
	}
	s.mitm.policySupported = true

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/mitm/skip", strings.NewReader(`{"host":" Pin.Example.com "}`))
	r.mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("clear one: %d %s", w.Code, w.Body.String())
	}
	var after mitmStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil || len(after.Skip) != 1 || after.Skip[0].Host != "other.example.com" {
		t.Fatalf("after clear one: %v %+v", err, after.Skip)
	}
	r.do("POST", "/api/v1/mitm/skip", 200)
	if got := strings.Join(fake.cleared, ","); got != "pin.example.com," {
		t.Fatalf("cleared %q", got)
	}
	if st = r.status("GET", "/api/v1/mitm/status", 200); len(st.Skip) != 0 {
		t.Fatalf("skip left: %+v", st.Skip)
	}
}

func TestMITMRESTClearSkipWithoutCore(t *testing.T) {
	s, _, _, _ := newMITMTestServer(t)
	r := &mitmRecorder{t: t, mux: mitmTestMux(s)}
	r.do("POST", "/api/v1/mitm/skip", 409)
	if st := r.status("GET", "/api/v1/mitm/status", 200); st.Skip == nil || st.Failures == nil {
		t.Fatalf("lists must be non-nil: %+v", st)
	}
}
