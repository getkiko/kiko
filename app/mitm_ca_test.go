package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSecretStore struct {
	mu      sync.Mutex
	key     []byte
	loads   int
	saves   int
	deletes int
	loadErr error
	saveErr error
	// When block is non-nil, Load waits for it to close, simulating a keychain authorization dialog.
	block chan struct{}
}

func (f *fakeSecretStore) Load() ([]byte, error) {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.key == nil {
		return nil, errSecretNotFound
	}
	return append([]byte{}, f.key...), nil
}

func (f *fakeSecretStore) Save(key []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.key = append([]byte{}, key...)
	return nil
}

func (f *fakeSecretStore) Delete() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	f.key = nil
	return nil
}

func (f *fakeSecretStore) snapshot() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte{}, f.key...)
}

func (f *fakeSecretStore) setLoadErr(err error) {
	f.mu.Lock()
	f.loadErr = err
	f.mu.Unlock()
}

func (f *fakeSecretStore) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

// fakeTrustStore simulates system trust by swapping the trust roots used in tests.
type fakeTrustStore struct {
	s          *server
	supported  bool
	installErr error
	removeErr  error
	installs   int
	removes    int
}

func (f *fakeTrustStore) Supported() bool { return f.supported }

func (f *fakeTrustStore) Install(cert *x509.Certificate) error {
	if !f.supported {
		return errTrustUnsupported
	}
	f.installs++
	if f.installErr != nil {
		return f.installErr
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	f.s.mitm.mu.Lock()
	f.s.mitm.trustRoots = pool
	f.s.mitm.mu.Unlock()
	return nil
}

func (f *fakeTrustStore) Remove(*x509.Certificate) error {
	if !f.supported {
		return errTrustUnsupported
	}
	f.removes++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.s.mitm.mu.Lock()
	f.s.mitm.trustRoots = x509.NewCertPool()
	f.s.mitm.mu.Unlock()
	return nil
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newMITMTestServer(t *testing.T) (*server, *fakeSecretStore, *fakeTrustStore, *fakeClock) {
	t.Helper()
	s := newGRPCTestServer(t)
	secrets := &fakeSecretStore{}
	trust := &fakeTrustStore{s: s, supported: true}
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	s.mitm.secrets, s.mitm.trust, s.mitm.now = secrets, trust, clock.Now
	s.mitm.trustRoots = x509.NewCertPool()
	s.mitm.probeOnce.Do(func() {})
	s.mitm.supported = true
	s.mitm.policyProbeOnce.Do(func() {})
	s.mitm.policySupported = true
	return s, secrets, trust, clock
}

func mustRoot(t *testing.T, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	certDER, keyDER, err := generateMITMRoot(now)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := parseMITMRootKey(cert, keyDER)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, keyDER
}

func TestMITMRootCertificateFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root, _, keyDER := mustRoot(t, now)
	if !regexp.MustCompile(`^Kiko Root CA [0-9a-f]{8}$`).MatchString(root.Subject.CommonName) {
		t.Fatalf("CN %q", root.Subject.CommonName)
	}
	if len(root.Subject.Organization) != 1 || root.Subject.Organization[0] != "Kiko" {
		t.Fatalf("O %v", root.Subject.Organization)
	}
	if pub, ok := root.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("key %T", root.PublicKey)
	}
	if root.SignatureAlgorithm != x509.ECDSAWithSHA256 || root.CheckSignatureFrom(root) != nil {
		t.Fatalf("signature %v", root.SignatureAlgorithm)
	}
	if !root.NotBefore.Equal(now.Add(-24*time.Hour)) || !root.NotAfter.Equal(now.AddDate(10, 0, 0)) {
		t.Fatalf("validity %v %v", root.NotBefore, root.NotAfter)
	}
	if root.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign|x509.KeyUsageDigitalSignature || len(root.ExtKeyUsage) != 0 {
		t.Fatalf("usage %v %v", root.KeyUsage, root.ExtKeyUsage)
	}
	if !root.BasicConstraintsValid || !root.IsCA || root.MaxPathLen != 1 || root.MaxPathLenZero {
		t.Fatalf("constraints %v %v %d", root.BasicConstraintsValid, root.IsCA, root.MaxPathLen)
	}
	if len(root.SubjectKeyId) != 20 {
		t.Fatalf("SKI %x", root.SubjectKeyId)
	}
	if root.SerialNumber.Sign() <= 0 || root.SerialNumber.BitLen() > 128 {
		t.Fatalf("serial %v", root.SerialNumber)
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyDER); err != nil {
		t.Fatal(err)
	}
	other, _, _ := mustRoot(t, now)
	if other.Subject.CommonName == root.Subject.CommonName || other.SerialNumber.Cmp(root.SerialNumber) == 0 {
		t.Fatal("roots must have unique names and serials")
	}
	if _, err := parseMITMRootKey(other, keyDER); !errors.Is(err, errMITMRootMismatch) {
		t.Fatalf("mismatched key: %v", err)
	}
	if _, err := parseMITMRootKey(root, []byte("garbage")); err == nil || strings.Contains(err.Error(), "garbage") {
		t.Fatalf("garbage key: %v", err)
	}
}

func TestMITMIntermediateFieldsAndChain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root, rootKey, _ := mustRoot(t, now)
	authority, err := issueMITMIntermediate(root, rootKey, now)
	if err != nil {
		t.Fatal(err)
	}
	cert := authority.cert
	if cert.Subject.CommonName != "Kiko Intermediate CA" || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != "Kiko" {
		t.Fatalf("subject %v", cert.Subject)
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() || cert.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		t.Fatalf("key %T %v", cert.PublicKey, cert.SignatureAlgorithm)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.MaxPathLen != 0 || !cert.MaxPathLenZero {
		t.Fatalf("constraints %v %d %v", cert.IsCA, cert.MaxPathLen, cert.MaxPathLenZero)
	}
	if cert.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign|x509.KeyUsageDigitalSignature ||
		len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("usage %v %v", cert.KeyUsage, cert.ExtKeyUsage)
	}
	if !cert.NotBefore.Equal(now.Add(-time.Hour)) || !cert.NotAfter.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("validity %v %v", cert.NotBefore, cert.NotAfter)
	}
	if cert.CheckSignatureFrom(root) != nil || string(cert.AuthorityKeyId) != string(root.SubjectKeyId) || len(cert.SubjectKeyId) == 0 {
		t.Fatal("intermediate is not issued by root")
	}
	if authority.fingerprint != certFingerprint(cert.Raw) || authority.rootFP != certFingerprint(root.Raw) {
		t.Fatal("fingerprints")
	}
	if !strings.HasPrefix(string(authority.chainPEM), string(authority.certPEM)) || strings.Count(string(authority.chainPEM), "BEGIN CERTIFICATE") != 2 {
		t.Fatalf("chain %s", authority.chainPEM)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if !verifyMITMTrust(authority, roots, now) {
		t.Fatal("leaf must verify to root")
	}
	if verifyMITMTrust(authority, x509.NewCertPool(), now) {
		t.Fatal("leaf must not verify without root")
	}

	// The intermediate CA may only sign server certificates and may not sign subordinate CAs.
	intermediateKey := mustParseKey(t, authority.key)
	sub := &x509.Certificate{SerialNumber: root.SerialNumber, Subject: pkix.Name{CommonName: "sub"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	subKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	subDER, err := x509.CreateCertificate(rand.Reader, sub, cert, &subKey.PublicKey, intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	subCert, _ := x509.ParseCertificate(subDER)
	leaf := &x509.Certificate{SerialNumber: root.SerialNumber, Subject: pkix.Name{CommonName: "a.test"}, DNSNames: []string{"a.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, _ := x509.CreateCertificate(rand.Reader, leaf, subCert, &leafKey.PublicKey, subKey)
	leafCert, _ := x509.ParseCertificate(leafDER)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	pool.AddCert(subCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, DNSName: "a.test", CurrentTime: now}); err == nil {
		t.Fatal("path length 0 must reject a sub CA")
	}
	client := &x509.Certificate{SerialNumber: root.SerialNumber, Subject: pkix.Name{CommonName: "c"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientDER, _ := x509.CreateCertificate(rand.Reader, client, cert, &leafKey.PublicKey, intermediateKey)
	clientCert, _ := x509.ParseCertificate(clientDER)
	only := x509.NewCertPool()
	only.AddCert(cert)
	if _, err := clientCert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: only, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err == nil {
		t.Fatal("intermediate EKU must constrain leaves to server auth")
	}
}

func mustParseKey(t *testing.T, der []byte) *ecdsa.PrivateKey {
	t.Helper()
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	return key.(*ecdsa.PrivateKey)
}

func TestMITMIntermediateClampsToRoot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root, rootKey, _ := mustRoot(t, now)
	early, err := issueMITMIntermediate(root, rootKey, root.NotBefore.Add(10*time.Minute))
	if err != nil || !early.cert.NotBefore.Equal(root.NotBefore) {
		t.Fatalf("not before %v %v", early, err)
	}
	late, err := issueMITMIntermediate(root, rootKey, root.NotAfter.Add(-10*24*time.Hour))
	if err != nil || !late.cert.NotAfter.Equal(root.NotAfter) {
		t.Fatalf("not after %v %v", late, err)
	}
	if _, err := issueMITMIntermediate(root, rootKey, root.NotAfter.Add(time.Minute)); !errors.Is(err, errMITMRootExpired) {
		t.Fatalf("expired root: %v", err)
	}
}

func TestMITMAuthorityPerRunAndRenewal(t *testing.T) {
	s, secrets, _, clock := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	authority := func(run int64) *mitmAuthority {
		t.Helper()
		a, err := s.mitmAuthority(run)
		if err != nil || a == nil {
			t.Fatalf("authority %v %v", a, err)
		}
		return a
	}
	first := authority(1)
	if authority(1) != first || secrets.loadCount() != 1 {
		t.Fatalf("same run must reuse the intermediate, loads=%d", secrets.loadCount())
	}
	clock.Advance(22 * 24 * time.Hour)
	if authority(1) != first {
		t.Fatal("8 days left must not renew")
	}
	clock.Advance(2 * 24 * time.Hour)
	renewed := authority(1)
	if renewed == first || renewed.fingerprint == first.fingerprint || !renewed.cert.NotAfter.After(first.cert.NotAfter) {
		t.Fatal("6 days left must renew")
	}
	next := authority(2)
	if next == renewed || next.fingerprint == renewed.fingerprint {
		t.Fatal("new run must issue a new intermediate")
	}
	if secrets.loadCount() != 3 {
		t.Fatalf("loads=%d", secrets.loadCount())
	}
	// Once the root certificate is about to expire, renewal no longer repeats.
	root, _ := s.mitmRootSnapshot()
	clock.Advance(root.NotAfter.Sub(clock.Now()) - 3*24*time.Hour)
	nearEnd := authority(3)
	if authority(3) != nearEnd {
		t.Fatal("intermediate clamped to root expiry must not renew every call")
	}
}

func TestMITMCAFileAndStates(t *testing.T) {
	s, secrets, _, _ := newMITMTestServer(t)
	if root, state := s.mitmRootSnapshot(); root != nil || state != mitmCAStateAbsent {
		t.Fatalf("initial %v %s", root, state)
	}
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dataDir, "mitm", "ca.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ca.json %v %v", info, err)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dir.Mode())
	}
	data, _ := os.ReadFile(path)
	var file map[string]any
	if json.Unmarshal(data, &file) != nil || len(file) != 4 || file["version"] != float64(1) {
		t.Fatalf("ca.json %s", data)
	}
	root, _ := s.mitmRootSnapshot()
	if file["fingerprint"] != certFingerprint(root.Raw) || !strings.Contains(file["certificate"].(string), "BEGIN CERTIFICATE") {
		t.Fatalf("ca.json %s", data)
	}
	assertNoKey(t, "ca.json", data, secrets.snapshot())

	reload := func(mutate func()) *server {
		mutate()
		fresh, _, _, _ := newMITMTestServer(t)
		fresh.dataDir = s.dataDir
		fresh.mitm.secrets = secrets
		return fresh
	}
	fresh := reload(func() {})
	if _, state := fresh.mitmRootSnapshot(); state != mitmCAStateReady {
		t.Fatalf("reload %s", state)
	}
	if fresh.mitmStatus(t.Context()).CA.State != mitmCAStateReady {
		t.Fatal("matching key must stay ready")
	}

	key := secrets.snapshot()
	_, _, otherKey := mustRoot(t, time.Now())
	fresh = reload(func() { _ = secrets.Save(otherKey) })
	if fresh.mitmStatusRead(t.Context(), true).CA.State != mitmCAStateBroken {
		t.Fatal("mismatched key must report broken")
	}
	fresh = reload(func() { _ = secrets.Delete() })
	if status := fresh.mitmStatusRead(t.Context(), true); status.CA.State != mitmCAStateBroken || status.CA.Fingerprint != certFingerprint(root.Raw) {
		t.Fatalf("missing key %+v", status.CA)
	}
	_ = secrets.Save(key)
	secrets.setLoadErr(errors.New("locked"))
	fresh = reload(func() {})
	if fresh.mitmStatus(t.Context()).CA.State != mitmCAStateReady {
		t.Fatal("transient store error must not mark broken")
	}
	secrets.setLoadErr(nil)
	for _, corrupt := range []string{"{", `{"version":2}`, `{"version":1,"certificate":"x"}`, strings.Replace(string(data), certFingerprint(root.Raw), strings.Repeat("0", 64), 1)} {
		fresh = reload(func() { _ = os.WriteFile(path, []byte(corrupt), 0o600) })
		if _, state := fresh.mitmRootSnapshot(); state != mitmCAStateBroken {
			t.Fatalf("corrupt %q: %s", corrupt, state)
		}
	}
}

func TestMITMTrustDetectionUsesVerification(t *testing.T) {
	s, secrets, trust, clock := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	if s.mitmStatusRead(t.Context(), true).CA.Trusted {
		t.Fatal("not yet trusted")
	}
	root, _ := s.mitmRootSnapshot()
	_ = trust.Install(root)
	if s.mitmStatusRead(t.Context(), true).CA.Trusted {
		t.Fatal("result must be cached for 5 s")
	}
	clock.Advance(6 * time.Second)
	if !s.mitmStatusRead(t.Context(), true).CA.Trusted {
		t.Fatal("trusted root must verify")
	}
	loads := secrets.loadCount()
	clock.Advance(6 * time.Second)
	s.mitmStatusRead(t.Context(), true)
	if secrets.loadCount() != loads {
		t.Fatal("trust checks must reuse the check intermediate")
	}
}

// assertNoKey checks that data contains the private key in no DER, PEM, Base64 or hex form.
func assertNoKey(t *testing.T, what string, data []byte, keys ...[]byte) {
	t.Helper()
	text := string(data)
	if strings.Contains(text, "PRIVATE KEY") {
		t.Fatalf("%s contains a PEM private key", what)
	}
	for _, key := range keys {
		if len(key) == 0 {
			continue
		}
		for _, form := range keyForms(key) {
			if strings.Contains(text, form) {
				t.Fatalf("%s contains private key material", what)
			}
		}
	}
}

// After a credential store read fails (e.g. access was denied), reads pause until a user action or 10 minutes pass.
func TestMITMKeyReadBackoff(t *testing.T) {
	s, secrets, trust, clock := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	secrets.setLoadErr(errors.New("user denied"))
	for range 5 {
		if st := s.mitmStatusRead(t.Context(), true); st.CA.State != mitmCAStateReady || st.CA.Trusted {
			t.Fatalf("status %+v", st.CA)
		}
		if _, err := s.mitmAuthority(1); !errors.Is(err, errMITMKeyUnavailable) {
			t.Fatalf("authority %v", err)
		}
		clock.Advance(6 * time.Second)
	}
	if secrets.loadCount() != 1 {
		t.Fatalf("loads=%d", secrets.loadCount())
	}
	root, _ := s.mitmRootSnapshot()
	_ = trust.Install(root)
	s.mitmInvalidateTrust()
	s.mitmStatusRead(t.Context(), true)
	if secrets.loadCount() != 2 {
		t.Fatalf("user action must allow a retry, loads=%d", secrets.loadCount())
	}
	clock.Advance(9 * time.Minute)
	_, _ = s.mitmAuthority(1)
	if secrets.loadCount() != 2 {
		t.Fatal("retry within 10 minutes")
	}
	secrets.setLoadErr(nil)
	clock.Advance(2 * time.Minute)
	if a, err := s.mitmAuthority(1); err != nil || a == nil {
		t.Fatalf("retry after 10 minutes: %v", err)
	}
	if !s.mitmStatusRead(t.Context(), true).CA.Trusted {
		t.Fatal("trust must be detected after the key is readable")
	}
}

// mu is not held while reading the credential store: the status endpoint returns the cached result immediately.
func TestMITMKeyReadDoesNotHoldStateLock(t *testing.T) {
	s, secrets, _, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	secrets.mu.Lock()
	secrets.block = block
	secrets.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.mitmAuthority(1)
	}()
	time.Sleep(50 * time.Millisecond)
	answered := make(chan mitmStatusResponse, 1)
	go func() { answered <- s.mitmStatus(t.Context()) }()
	select {
	case st := <-answered:
		if st.CA.State != mitmCAStateReady {
			t.Fatalf("status %+v", st.CA)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status blocked by a pending keychain read")
	}
	close(block)
	<-done
}

// A failed generation keeps the original root certificate and private key.
func TestMITMGenerateKeepsOldRootOnFailure(t *testing.T) {
	s, secrets, _, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	root, _ := s.mitmRootSnapshot()
	key := secrets.snapshot()
	path := filepath.Join(s.dataDir, "mitm", "ca.json")
	data, _ := os.ReadFile(path)
	check := func(what string) {
		t.Helper()
		if current, state := s.mitmRootSnapshot(); state != mitmCAStateReady || !current.Equal(root) {
			t.Fatalf("%s: root changed (%s)", what, state)
		}
		if string(secrets.snapshot()) != string(key) {
			t.Fatalf("%s: key changed", what)
		}
		if now, _ := os.ReadFile(path); string(now) != string(data) {
			t.Fatalf("%s: ca.json changed", what)
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 1 {
			t.Fatalf("%s: leftover files %v", what, entries)
		}
		fresh, _, _, _ := newMITMTestServer(t)
		fresh.dataDir, fresh.mitm.secrets = s.dataDir, secrets
		if fresh.mitmStatus(t.Context()).CA.State != mitmCAStateReady {
			t.Fatalf("%s: reload not ready", what)
		}
	}
	s.mitm.prepareCA = func(string, []byte) (string, error) { return "", errors.New("disk full") }
	if err := s.mitmGenerate(); err == nil {
		t.Fatal("failing writer must fail")
	}
	check("writer")
	s.mitm.prepareCA = nil
	secrets.saveErr = errors.New("keychain")
	if err := s.mitmGenerate(); err == nil {
		t.Fatal("failing store must fail")
	}
	check("store")
}

// An unreadable root private key (foreign DPAPI data, a corrupt ca.key) marks the root broken.
func TestMITMCorruptKeyMarksBroken(t *testing.T) {
	s, secrets, _, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	secrets.setLoadErr(errSecretCorrupt)
	if _, err := s.mitmAuthority(1); !errors.Is(err, errMITMRootBroken) {
		t.Fatalf("authority %v", err)
	}
	if st := s.mitmStatus(t.Context()); st.CA.State != mitmCAStateBroken {
		t.Fatalf("state %s", st.CA.State)
	}
}

// Status polling does not read the credential store and trust stays unknown; it is read only after a user action.
func TestMITMStatusDoesNotReadSecrets(t *testing.T) {
	s, secrets, trust, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	root, _ := s.mitmRootSnapshot()
	_ = trust.Install(root)
	s.mitmInvalidateTrust()
	for range 3 {
		if st := s.mitmStatus(t.Context()); !st.CA.TrustUnknown || st.CA.Trusted {
			t.Fatalf("status %+v", st.CA)
		}
	}
	if secrets.loadCount() != 0 {
		t.Fatalf("loads=%d", secrets.loadCount())
	}
	if st := s.mitmStatusRead(t.Context(), true); st.CA.TrustUnknown || !st.CA.Trusted {
		t.Fatalf("explicit %+v", st.CA)
	}
	if st := s.mitmStatus(t.Context()); st.CA.TrustUnknown || !st.CA.Trusted {
		t.Fatalf("cached %+v", st.CA)
	}
}

// A check started during a trust operation must not write back a stale result.
func TestMITMTrustCheckDiscardedAfterInvalidate(t *testing.T) {
	s, secrets, trust, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	secrets.mu.Lock()
	secrets.block = block
	secrets.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.mitmRefreshTrust(true)
	}()
	time.Sleep(50 * time.Millisecond)
	root, _ := s.mitmRootSnapshot()
	_ = trust.Install(root)
	s.mitmInvalidateTrust()
	close(block)
	<-done
	s.mitm.mu.Lock()
	stale := !s.mitm.trustCheckedAt.IsZero() || s.mitm.trustKnown
	s.mitm.mu.Unlock()
	if stale {
		t.Fatal("stale check result was written back")
	}
	if !s.mitmStatusRead(t.Context(), true).CA.Trusted {
		t.Fatal("trust must be detected after the install")
	}
}

// When replacing ca.json fails, the old key is written back and the old root stays usable.
func TestMITMGenerateRenameFailureRestoresKey(t *testing.T) {
	s, secrets, _, _ := newMITMTestServer(t)
	if err := s.mitmGenerate(); err != nil {
		t.Fatal(err)
	}
	root, _ := s.mitmRootSnapshot()
	key := secrets.snapshot()
	s.mitm.renameCA = func(string, string) error { return errors.New("rename") }
	if err := s.mitmGenerate(); err == nil {
		t.Fatal("rename failure must fail")
	}
	if string(secrets.snapshot()) != string(key) {
		t.Fatal("old key not restored")
	}
	current, state := s.mitmRootSnapshot()
	if state != mitmCAStateReady || !current.Equal(root) {
		t.Fatalf("state %s", state)
	}
	if _, err := s.mitmAuthority(1); err != nil {
		t.Fatalf("old root must still sign: %v", err)
	}
	// A rename failure on first generation leaves no private key behind.
	s2, secrets2, _, _ := newMITMTestServer(t)
	s2.mitm.renameCA = func(string, string) error { return errors.New("rename") }
	if err := s2.mitmGenerate(); err == nil || len(secrets2.snapshot()) != 0 {
		t.Fatalf("first generate leaves key: %v", err)
	}
}
