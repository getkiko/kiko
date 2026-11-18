package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"

	"howett.net/plist"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// Root certificate export: the certificate (public part) only, never the
// private key.

const (
	mitmExportName         = "Kiko Root CA"
	mitmExportFormatsError = "format 必须是 pem、cer、crt、p12 或 mobileconfig"
)

// mitmExportBody returns the exported root certificate body, content type and
// file name.
func mitmExportBody(root *x509.Certificate, format string) ([]byte, string, string, error) {
	switch format {
	case "", "pem":
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), "application/x-pem-file", mitmExportName + ".pem", nil
	case "cer":
		return bytes.Clone(root.Raw), "application/pkix-cert", mitmExportName + ".cer", nil
	case "crt":
		return bytes.Clone(root.Raw), "application/x-x509-ca-cert", mitmExportName + ".crt", nil
	case "p12":
		// A certificate-only PKCS#12 trust store with an empty password; 3DES
		// encoding stays compatible with imports on Windows, macOS and Android.
		data, err := pkcs12.LegacyDES.EncodeTrustStore([]*x509.Certificate{root}, "")
		if err != nil {
			return nil, "", "", fmt.Errorf("生成 p12 失败")
		}
		return data, "application/x-pkcs12", mitmExportName + ".p12", nil
	case "mobileconfig":
		data, err := mitmMobileConfig(root)
		if err != nil {
			return nil, "", "", fmt.Errorf("生成描述文件失败")
		}
		return data, "application/x-apple-aspen-config", mitmExportName + ".mobileconfig", nil
	}
	return nil, "", "", fmt.Errorf("%s", mitmExportFormatsError)
}

// mitmMobileConfig builds an unsigned Apple configuration profile containing
// only the root certificate payload. Its identifier and UUID derive from the
// SHA-256 fingerprint, so reinstalling the same root yields the same profile.
func mitmMobileConfig(root *x509.Certificate) ([]byte, error) {
	fp := certFingerprint(root.Raw)
	profile := map[string]any{
		"PayloadType":        "Configuration",
		"PayloadVersion":     1,
		"PayloadIdentifier":  "io.github.getkiko.kiko.mitm.root." + fp[:16],
		"PayloadUUID":        mitmDerivedUUID("profile", fp),
		"PayloadDisplayName": mitmExportName,
		"PayloadContent": []any{
			map[string]any{
				"PayloadType":                "com.apple.security.root",
				"PayloadVersion":             1,
				"PayloadIdentifier":          "io.github.getkiko.kiko.mitm.root.cert." + fp[:16],
				"PayloadUUID":                mitmDerivedUUID("cert", fp),
				"PayloadDisplayName":         mitmExportName,
				"PayloadCertificateFileName": mitmExportName + ".cer",
				"PayloadContent":             bytes.Clone(root.Raw),
			},
		},
	}
	return plist.Marshal(profile, plist.XMLFormat)
}

// mitmDerivedUUID deterministically derives a UUID (version 5 format) from
// the fingerprint.
func mitmDerivedUUID(scope, fingerprint string) string {
	h := sha1.Sum([]byte("kiko-mitm-root/" + scope + "/" + fingerprint))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b)
	return strings.ToUpper(s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32])
}
