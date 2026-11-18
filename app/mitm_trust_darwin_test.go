//go:build darwin

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"os/exec"
	"testing"
	"time"
	"unsafe"
)

func testTrustCertificate(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestNewTrustStoreSupported(t *testing.T) {
	if !newTrustStore().Supported() {
		t.Fatal("unsupported")
	}
}

func TestMacCertificateRoundTrip(t *testing.T) {
	if err := loadMacSecurity(); err != nil {
		t.Fatal(err)
	}
	cert := testTrustCertificate(t, "Kiko Trust Round Trip")
	var b cfBag
	defer b.release()
	ref, err := macCertificate(&b, cert.Raw)
	if err != nil {
		t.Fatal(err)
	}
	data := b.keep(macSec.secCertCopyData(ref))
	if !bytes.Equal(cfDataBytes(data), cert.Raw) {
		t.Fatal("SecCertificateCopyData differs")
	}
	if _, err := macCertificate(&b, []byte("not a certificate")); err == nil {
		t.Fatal("invalid DER accepted")
	}
}

func TestMacSSLRootTrustSettings(t *testing.T) {
	if err := loadMacSecurity(); err != nil {
		t.Fatal(err)
	}
	var b cfBag
	defer b.release()
	settings := macSSLRootTrustSettings(&b)
	if macSec.cfGetTypeID(settings) != macSec.cfArrayGetTypeID() || macSec.cfArrayGetCount(settings) != 1 {
		t.Fatal("trust settings is not a one-element array")
	}
	entry := macSec.cfArrayGetValueAtIndex(settings, 0)
	if macSec.cfDictionaryGetValue(entry, b.str(secTrustSettingsPolicyKey)) == 0 {
		t.Fatal("missing policy")
	}
	result := macSec.cfDictionaryGetValue(entry, b.str(secTrustSettingsResultKey))
	var v int32
	if result == 0 || !macSec.cfNumberGetValue(result, cfNumberSInt32Type, unsafe.Pointer(&v)) || v != secTrustSettingsResultTrustRoot {
		t.Fatalf("result = %d", v)
	}
}

func TestMacTrustErrorMapsCancel(t *testing.T) {
	for _, status := range []int32{errSecAuthFailed, errSecUserCanceled, errAuthorizationDeny, errAuthorizationCancl} {
		if err := macTrustError("设置证书信任", status); !errors.Is(err, errTrustCanceled) {
			t.Fatalf("%d -> %v", status, err)
		}
	}
	if err := macTrustError("设置证书信任", errSecSuccess); err != nil {
		t.Fatal(err)
	}
	if code, ok := osStatusCode(macTrustError("设置证书信任", -25308)); !ok || code != -25308 {
		t.Fatal("status lost")
	}
}

// TestMacKeychainCertificateAddDelete covers only adding the certificate to the
// login keychain and deleting it exactly; trust settings are not changed (that
// would prompt for a password).
func TestMacKeychainCertificateAddDelete(t *testing.T) {
	requireKeychainTest(t)
	if err := loadMacSecurity(); err != nil {
		t.Fatal(err)
	}
	const cn = "Kiko Trust Store Test"
	a := testTrustCertificate(t, cn)
	other := testTrustCertificate(t, cn)
	t.Cleanup(func() {
		_ = macDeleteCertificate(a.Raw)
		_ = macDeleteCertificate(other.Raw)
	})
	for _, cert := range []*x509.Certificate{a, other} {
		var b cfBag
		ref, err := macCertificate(&b, cert.Raw)
		if err == nil {
			err = macAddCertificate(&b, ref)
		}
		if err == nil {
			// A repeated add counts as success.
			err = macAddCertificate(&b, ref)
		}
		b.release()
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := countKeychainCertificates(t, cn); n != 2 {
		t.Fatalf("certificates after add = %d", n)
	}
	if err := macDeleteCertificate(a.Raw); err != nil {
		t.Fatal(err)
	}
	if n := countKeychainCertificates(t, cn); n != 1 {
		t.Fatalf("certificates after exact delete = %d", n)
	}
	if err := macDeleteCertificate(a.Raw); err != nil {
		t.Fatalf("delete again: %v", err)
	}
	if err := macDeleteCertificate(other.Raw); err != nil {
		t.Fatal(err)
	}
	if n := countKeychainCertificates(t, cn); n != 0 {
		t.Fatalf("certificates after cleanup = %d", n)
	}
}

func countKeychainCertificates(t *testing.T, cn string) int {
	t.Helper()
	out, _ := exec.Command("security", "find-certificate", "-a", "-c", cn, "-Z").CombinedOutput()
	t.Logf("security find-certificate -c %q:\n%s", cn, out)
	return bytes.Count(out, []byte("SHA-1 hash:"))
}
