package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// HTTPS decryption root certificate: generation, metadata, intermediate CA
// issuance and trust detection. The root private key only enters and leaves
// through secretStore and stays in memory briefly while signing.

const (
	mitmRootValidityYears   = 10
	mitmIntermediateLife    = 30 * 24 * time.Hour
	mitmIntermediateRenew   = 7 * 24 * time.Hour
	mitmTrustCheckInterval  = 5 * time.Second
	mitmKeyRetryInterval    = 10 * time.Minute
	mitmCAFileVersion       = 1
	mitmIntermediateCN      = "Kiko Intermediate CA"
	mitmOrganization        = "Kiko"
	mitmTrustCheckHost      = "kiko-trust-check.invalid"
	mitmCAStateAbsent       = "absent"
	mitmCAStateReady        = "ready"
	mitmCAStateBroken       = "broken"
	mitmRootCommonNamePrefx = "Kiko Root CA "
)

var (
	errMITMNoRoot       = errors.New("没有根证书")
	errMITMRootBroken   = errors.New("根证书不可用")
	errMITMRootExpired  = errors.New("根证书已过期")
	errMITMRootMismatch = errors.New("根证书与私钥不匹配")
	// errMITMKeyUnavailable means reading the credential store failed (e.g. access was denied); retries pause.
	errMITMKeyUnavailable = errors.New("读取根证书私钥失败")
	errMITMRootChanged    = errors.New("根证书已变更")
)

// mitmCAFile is the content of <dataDir>/mitm/ca.json; it holds neither the private key nor the intermediate CA.
type mitmCAFile struct {
	Version     int       `json:"version"`
	Certificate string    `json:"certificate"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// mitmAuthority is an intermediate CA; its key is handed only to the core.
type mitmAuthority struct {
	cert        *x509.Certificate
	certPEM     []byte
	key         []byte // PKCS#8 DER
	fingerprint string
	rootFP      string
	chainPEM    []byte
}

func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	for {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return nil, err
		}
		if n.Sign() > 0 {
			return n, nil
		}
	}
}

func subjectKeyID(key *ecdsa.PublicKey) []byte {
	pub, err := key.ECDH()
	if err != nil {
		return nil
	}
	sum := sha1.Sum(pub.Bytes())
	return sum[:]
}

// generateMITMRoot generates a self-signed root certificate and returns the certificate DER and the PKCS#8 private key DER.
func generateMITMRoot(now time.Time) (certDER, keyDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	suffix := make([]byte, 4)
	if _, err = rand.Read(suffix); err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: mitmRootCommonNamePrefx + hex.EncodeToString(suffix), Organization: []string{mitmOrganization}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.AddDate(mitmRootValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
		SubjectKeyId:          subjectKeyID(&key.PublicKey),
		SignatureAlgorithm:    x509.ECDSAWithSHA256,
	}
	certDER, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err = x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return certDER, keyDER, nil
}

// parseMITMRootKey parses the root private key and checks it matches the certificate; errors never include key material.
func parseMITMRootKey(root *x509.Certificate, keyDER []byte) (*ecdsa.PrivateKey, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, errMITMRootMismatch
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errMITMRootMismatch
	}
	pub, ok := root.PublicKey.(*ecdsa.PublicKey)
	if !ok || !key.PublicKey.Equal(pub) {
		return nil, errMITMRootMismatch
	}
	return key, nil
}

// issueMITMIntermediate signs an intermediate CA with the root certificate; its validity never exceeds the root's.
func issueMITMIntermediate(root *x509.Certificate, rootKey *ecdsa.PrivateKey, now time.Time) (*mitmAuthority, error) {
	notBefore := now.Add(-time.Hour)
	if notBefore.Before(root.NotBefore) {
		notBefore = root.NotBefore
	}
	notAfter := now.Add(mitmIntermediateLife)
	if notAfter.After(root.NotAfter) {
		notAfter = root.NotAfter
	}
	if !notAfter.After(now) {
		return nil, errMITMRootExpired
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: mitmIntermediateCN, Organization: []string{mitmOrganization}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          subjectKeyID(&key.PublicKey),
		SignatureAlgorithm:    x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw})
	return &mitmAuthority{
		cert: cert, certPEM: certPEM, key: keyDER,
		fingerprint: certFingerprint(der), rootFP: certFingerprint(root.Raw),
		chainPEM: append(append([]byte{}, certPEM...), rootPEM...),
	}, nil
}

// signMITMTrustLeaf signs, with the intermediate CA, a leaf certificate used only for trust detection.
func signMITMTrustLeaf(authority *mitmAuthority, now time.Time) (*x509.Certificate, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(authority.key)
	if err != nil {
		return nil, err
	}
	signer, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errMITMRootMismatch
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	notBefore := now.Add(-5 * time.Minute)
	if notBefore.Before(authority.cert.NotBefore) {
		notBefore = authority.cert.NotBefore
	}
	notAfter := now.Add(time.Hour)
	if notAfter.After(authority.cert.NotAfter) {
		notAfter = authority.cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: mitmTrustCheckHost},
		DNSNames:     []string{mitmTrustCheckHost},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority.cert, &key.PublicKey, signer)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// verifyMITMTrust uses the system verifier to check whether the root certificate is trusted; roots is passed only in tests.
func verifyMITMTrust(authority *mitmAuthority, roots *x509.CertPool, now time.Time) bool {
	leaf, err := signMITMTrustLeaf(authority, now)
	if err != nil {
		return false
	}
	intermediates := x509.NewCertPool()
	intermediates.AddCert(authority.cert)
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		DNSName:       mitmTrustCheckHost,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err == nil
}

func (s *server) mitmCAPath() string { return filepath.Join(s.dataDir, "mitm", "ca.json") }

// readMITMRootCert reads the root certificate saved in dataDir, nil when there
// is none or it cannot be parsed.
func readMITMRootCert(dataDir string) *x509.Certificate {
	data, err := os.ReadFile(filepath.Join(dataDir, "mitm", "ca.json"))
	if err != nil {
		return nil
	}
	var file mitmCAFile
	if json.Unmarshal(data, &file) != nil {
		return nil
	}
	block, _ := pem.Decode([]byte(file.Certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}

func (s *server) mitmNow() time.Time {
	if s.mitm.now != nil {
		return s.mitm.now()
	}
	return time.Now()
}

func (s *server) mitmSecretsLocked() secretStore {
	if s.mitm.secrets == nil {
		s.mitm.secrets = newSecretStore(s.dataDir)
	}
	return s.mitm.secrets
}

func (s *server) mitmTrustStore() trustStore {
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	return s.mitmTrustLocked()
}

func (s *server) mitmTrustLocked() trustStore {
	if s.mitm.trust == nil {
		s.mitm.trust = newTrustStore()
	}
	return s.mitm.trust
}

// mitmLoadLocked reads ca.json on first use; when reading fails it reports broken.
func (s *server) mitmLoadLocked() {
	if s.mitm.loaded {
		return
	}
	s.mitm.loaded = true
	s.mitm.root, s.mitm.state = nil, mitmCAStateAbsent
	s.mitm.createdAt = time.Time{}
	data, err := os.ReadFile(s.mitmCAPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	s.mitm.state = mitmCAStateBroken
	if err != nil {
		return
	}
	var file mitmCAFile
	if json.Unmarshal(data, &file) != nil || file.Version != mitmCAFileVersion {
		return
	}
	block, _ := pem.Decode([]byte(file.Certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return
	}
	s.mitm.root, s.mitm.createdAt = cert, file.CreatedAt
	if file.Fingerprint != certFingerprint(cert.Raw) || !cert.IsCA {
		return
	}
	s.mitm.state = mitmCAStateReady
}

func (s *server) mitmResetLocked() {
	s.mitm.authority, s.mitm.authorityRun = nil, 0
	s.mitm.checkAuthority = nil
	s.mitm.trustCheckedAt = time.Time{}
	s.mitm.trusted, s.mitm.trustKnown = false, false
	s.mitm.trustGen++
	s.mitm.keyFailedRoot = ""
}

// mitmSign takes the root private key from the credential store, signs an
// intermediate CA and discards the key right after use. The caller holds
// signMu; mu is not held while reading the credential store (macOS may show
// a keychain authorization prompt). A missing key or one that does not match
// the certificate marks the root broken; after any other read failure the
// store is not read again for 10 minutes or until a user action.
func (s *server) mitmSign() (*mitmAuthority, error) {
	s.mitm.mu.Lock()
	s.mitmLoadLocked()
	root, state := s.mitm.root, s.mitm.state
	now := s.mitmNow()
	switch {
	case state == mitmCAStateAbsent:
		s.mitm.mu.Unlock()
		return nil, errMITMNoRoot
	case state == mitmCAStateBroken:
		s.mitm.mu.Unlock()
		return nil, errMITMRootBroken
	case s.mitm.keyFailedRoot == certFingerprint(root.Raw) && now.Sub(s.mitm.keyFailedAt) < mitmKeyRetryInterval:
		s.mitm.mu.Unlock()
		return nil, errMITMKeyUnavailable
	}
	secrets := s.mitmSecretsLocked()
	s.mitm.mu.Unlock()

	keyDER, loadErr := secrets.Load()
	var authority *mitmAuthority
	var err error
	if loadErr == nil {
		var key *ecdsa.PrivateKey
		key, err = parseMITMRootKey(root, keyDER)
		clear(keyDER)
		if err == nil {
			authority, err = issueMITMIntermediate(root, key, now)
		}
	}

	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	if s.mitm.root != root {
		return nil, errMITMRootChanged
	}
	switch {
	case errors.Is(loadErr, errSecretNotFound), errors.Is(loadErr, errSecretCorrupt):
		s.mitm.state = mitmCAStateBroken
		return nil, errMITMRootBroken
	case loadErr != nil:
		s.mitm.keyFailedRoot, s.mitm.keyFailedAt = certFingerprint(root.Raw), now
		return nil, errMITMKeyUnavailable
	case errors.Is(err, errMITMRootMismatch):
		s.mitm.state = mitmCAStateBroken
		return nil, err
	case err != nil:
		return nil, err
	}
	s.mitm.keyFailedRoot = ""
	return authority, nil
}

// mitmAuthority returns the intermediate CA for the current core run: it is
// reused within a run, renewed when fewer than 7 days remain and the root
// allows extending it, and issued anew for a new run. It returns nil when no
// usable root certificate exists.
func (s *server) mitmAuthority(runID int64) (*mitmAuthority, error) {
	s.mitm.signMu.Lock()
	defer s.mitm.signMu.Unlock()
	s.mitm.mu.Lock()
	s.mitmLoadLocked()
	if s.mitm.state != mitmCAStateReady {
		s.mitm.mu.Unlock()
		return nil, nil
	}
	current := s.mitm.authority
	if current != nil && s.mitm.authorityRun == runID {
		remaining := current.cert.NotAfter.Sub(s.mitmNow())
		if remaining >= mitmIntermediateRenew || !s.mitm.root.NotAfter.After(current.cert.NotAfter) {
			s.mitm.mu.Unlock()
			return current, nil
		}
	}
	s.mitm.mu.Unlock()
	authority, err := s.mitmSign()
	if err != nil {
		return nil, err
	}
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	if s.mitm.root == nil || certFingerprint(s.mitm.root.Raw) != authority.rootFP {
		return nil, errMITMRootChanged
	}
	s.mitm.authority, s.mitm.authorityRun = authority, runID
	return authority, nil
}

// mitmRefreshTrust detects trust by actually verifying, caching the result for
// at most 5 seconds; neither verification nor the credential store read holds
// mu. When allowRead is false it only uses an already cached intermediate CA;
// without one the trust state is unknown, and it neither reads the credential
// store nor takes signMu. When allowRead is true (a user action) and the store
// is already being read (possibly waiting for authorization), the cached
// result is kept.
func (s *server) mitmRefreshTrust(allowRead bool) {
	if allowRead {
		if !s.mitm.signMu.TryLock() {
			return
		}
		defer s.mitm.signMu.Unlock()
	}
	s.mitm.mu.Lock()
	s.mitmLoadLocked()
	now := s.mitmNow()
	if s.mitm.state != mitmCAStateReady {
		s.mitm.trusted = false
		s.mitm.mu.Unlock()
		return
	}
	if !s.mitm.trustCheckedAt.IsZero() && now.Sub(s.mitm.trustCheckedAt) < mitmTrustCheckInterval && !now.Before(s.mitm.trustCheckedAt) {
		s.mitm.mu.Unlock()
		return
	}
	root := s.mitm.root
	rootFP := certFingerprint(root.Raw)
	gen := s.mitm.trustGen
	authority := s.mitm.authority
	if authority == nil || authority.rootFP != rootFP {
		authority = s.mitm.checkAuthority
	}
	if authority != nil && (authority.rootFP != rootFP || !now.Before(authority.cert.NotAfter)) {
		authority = nil
	}
	roots := s.mitm.trustRoots
	if authority == nil && !allowRead {
		s.mitm.trusted, s.mitm.trustKnown = false, false
		s.mitm.mu.Unlock()
		return
	}
	s.mitm.mu.Unlock()

	if authority == nil {
		issued, err := s.mitmSign()
		if err != nil {
			s.mitm.mu.Lock()
			if s.mitm.root == root && s.mitm.trustGen == gen {
				s.mitm.trusted, s.mitm.trustKnown, s.mitm.trustCheckedAt = false, true, now
			}
			s.mitm.mu.Unlock()
			return
		}
		authority = issued
	}
	if roots == nil {
		roots = mitmSystemRoots()
	}
	trusted := verifyMITMTrust(authority, roots, now)
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	if s.mitm.root != root || s.mitm.trustGen != gen {
		return
	}
	if s.mitm.authority != authority {
		s.mitm.checkAuthority = authority
	}
	s.mitm.trusted, s.mitm.trustKnown, s.mitm.trustCheckedAt = trusted, true, now
}

// mitmGenerate generates a new root certificate and replaces the existing one:
// ca.json is first written to a temporary file and replaced only after the
// private key is written to the credential store; if any step fails, the
// original root certificate and key are kept.
func (s *server) mitmGenerate() error {
	s.mitmShareStop()
	certDER, keyDER, err := generateMITMRoot(s.mitmNow())
	if err != nil {
		return errors.New("生成根证书失败")
	}
	defer clear(keyDER)
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return errors.New("生成根证书失败")
	}
	s.mitm.signMu.Lock()
	defer s.mitm.signMu.Unlock()
	createdAt := s.mitmNow().UTC().Truncate(time.Second)
	data, err := json.MarshalIndent(mitmCAFile{
		Version:     mitmCAFileVersion,
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		Fingerprint: certFingerprint(certDER),
		CreatedAt:   createdAt,
	}, "", "  ")
	if err != nil {
		return errors.New("保存根证书失败")
	}
	s.mitm.mu.Lock()
	secrets, prepare := s.mitmSecretsLocked(), s.mitm.prepareCA
	s.mitm.mu.Unlock()
	if prepare == nil {
		prepare = mitmPrepareCAFile
	}
	tmp, err := prepare(s.mitmCAPath(), data)
	if err != nil {
		return errors.New("保存根证书失败")
	}
	defer os.Remove(tmp)
	// Back up the old key before replacing it and write it back if replacing the certificate fails. An unreadable old key cannot be restored.
	var oldKey []byte
	hadRoot := false
	if _, state := s.mitmRootSnapshot(); state != mitmCAStateAbsent {
		hadRoot = true
		if key, loadErr := secrets.Load(); loadErr == nil {
			oldKey = key
			defer clear(oldKey)
		}
	}
	if err := secrets.Save(keyDER); err != nil {
		return errors.New("保存根证书私钥失败")
	}
	s.mitm.mu.Lock()
	rename := s.mitm.renameCA
	s.mitm.mu.Unlock()
	if rename == nil {
		rename = os.Rename
	}
	renameErr := rename(tmp, s.mitmCAPath())
	if renameErr != nil {
		switch {
		case oldKey != nil:
			_ = secrets.Save(oldKey)
		case !hadRoot:
			_ = secrets.Delete()
		}
	}
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	s.mitmResetLocked()
	if renameErr != nil {
		// Re-read ca.json; if the key could not be restored, the signing path reports broken.
		s.mitm.loaded = false
		return errors.New("保存根证书失败")
	}
	s.mitm.loaded, s.mitm.root, s.mitm.state, s.mitm.createdAt = true, cert, mitmCAStateReady, createdAt
	return nil
}

// mitmPrepareCAFile writes a 0600 temporary file next to ca.json and returns its path.
func mitmPrepareCAFile(path string, data []byte) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	f, err := os.CreateTemp(dir, ".ca-*.json")
	if err != nil {
		return "", err
	}
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// mitmRemoveFiles deletes the root private key and ca.json.
func (s *server) mitmRemoveFiles() error {
	s.mitmShareStop()
	s.mitm.signMu.Lock()
	defer s.mitm.signMu.Unlock()
	s.mitm.mu.Lock()
	secrets := s.mitmSecretsLocked()
	s.mitm.mu.Unlock()
	if err := secrets.Delete(); err != nil {
		return errors.New("删除根证书私钥失败")
	}
	removeErr := os.Remove(s.mitmCAPath())
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	s.mitmResetLocked()
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		s.mitm.loaded = false
		return errors.New("删除根证书失败")
	}
	s.mitm.loaded, s.mitm.root, s.mitm.state, s.mitm.createdAt = true, nil, mitmCAStateAbsent, time.Time{}
	return nil
}

// mitmRootSnapshot returns the current root certificate (possibly nil) and its state.
func (s *server) mitmRootSnapshot() (*x509.Certificate, string) {
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	s.mitmLoadLocked()
	return s.mitm.root, s.mitm.state
}

// mitmRemoveTrust removes trust in the root certificate; platforms that cannot install trust skip it.
func (s *server) mitmRemoveTrust(root *x509.Certificate) error {
	if root == nil {
		return nil
	}
	trust := s.mitmTrustStore()
	if !trust.Supported() {
		return nil
	}
	err := trust.Remove(root)
	s.mitmInvalidateTrust()
	return err
}

// mitmInvalidateTrust re-runs detection after the user changes trust and allows reading the credential store again.
func (s *server) mitmInvalidateTrust() {
	s.mitm.mu.Lock()
	s.mitm.trustCheckedAt = time.Time{}
	s.mitm.trustKnown = false
	s.mitm.trustGen++
	s.mitm.keyFailedRoot = ""
	s.mitm.mu.Unlock()
}
