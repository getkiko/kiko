//go:build darwin

package main

import (
	"bytes"
	"crypto/x509"
	"errors"
)

// On macOS the root certificate is trusted in the user domain: the certificate
// is added to the login keychain, then marked as a trusted root for the SSL
// policy with SecTrustSettingsSetTrustSettings. Changing user-domain trust
// settings prompts for the login password; that interaction is expected.

const (
	secTrustSettingsDomainUser       = 0
	secTrustSettingsResultTrustRoot  = 1
	secTrustSettingsPolicyKey        = "kSecTrustSettingsPolicy"
	secTrustSettingsResultKey        = "kSecTrustSettingsResult"
	macTrustCertificateNotCreatedErr = "证书格式无效"
)

type keychainTrustStore struct{}

func newTrustStore() trustStore { return keychainTrustStore{} }

func (keychainTrustStore) Supported() bool { return true }

func (keychainTrustStore) Install(cert *x509.Certificate) error {
	if err := loadMacSecurity(); err != nil {
		return err
	}
	var b cfBag
	defer b.release()
	ref, err := macCertificate(&b, cert.Raw)
	if err != nil {
		return err
	}
	if err := macAddCertificate(&b, ref); err != nil {
		return err
	}
	status := macSec.secTrustSettingsSet(ref, secTrustSettingsDomainUser, macSSLRootTrustSettings(&b))
	return macTrustError("设置证书信任", status)
}

func (keychainTrustStore) Remove(cert *x509.Certificate) error {
	if err := loadMacSecurity(); err != nil {
		return err
	}
	var b cfBag
	defer b.release()
	ref, err := macCertificate(&b, cert.Raw)
	if err != nil {
		return err
	}
	status := macSec.secTrustSettingsRm(ref, secTrustSettingsDomainUser)
	if status != errSecItemNotFound {
		if err := macTrustError("移除证书信任", status); err != nil {
			return err
		}
	}
	return macDeleteCertificate(cert.Raw)
}

// macSSLRootTrustSettings builds trusted-root settings that apply only to the
// SSL policy: [{kSecTrustSettingsPolicy: SSL policy, kSecTrustSettingsResult: TrustRoot}].
func macSSLRootTrustSettings(b *cfBag) uintptr {
	policy := b.keep(macSec.secPolicyCreateSSL(true, 0))
	entry := b.dict(
		b.str(secTrustSettingsPolicyKey), policy,
		b.str(secTrustSettingsResultKey), b.int32(secTrustSettingsResultTrustRoot),
	)
	return b.array(entry)
}

func macCertificate(b *cfBag, der []byte) (uintptr, error) {
	ref := b.keep(macSec.secCertCreateWithData(0, b.data(der)))
	if ref == 0 {
		return 0, errors.New(macTrustCertificateNotCreatedErr)
	}
	return ref, nil
}

// macAddCertificate adds the certificate to the default (login) keychain; an
// already-present certificate counts as success.
func macAddCertificate(b *cfBag, ref uintptr) error {
	status := macSec.secItemAdd(b.dict(
		macSec.kSecClass, macSec.kSecClassCertificate,
		macSec.kSecValueRef, ref,
	), nil)
	if status == errSecDuplicateItem {
		return nil
	}
	return macTrustError("添加证书到钥匙串", status)
}

// macDeleteCertificate deletes only keychain certificates whose content matches
// der exactly. It lists all certificate references of the class, compares the
// DER of each, then deletes by reference.
func macDeleteCertificate(der []byte) error {
	var b cfBag
	defer b.release()
	var result uintptr
	status := macSec.secItemCopyMatching(b.dict(
		macSec.kSecClass, macSec.kSecClassCertificate,
		macSec.kSecReturnRef, macSec.booleanTrue,
		macSec.kSecMatchLimit, macSec.kSecMatchLimitAll,
	), &result)
	if status == errSecItemNotFound {
		return nil
	}
	if err := macTrustError("查找钥匙串证书", status); err != nil {
		return err
	}
	b.keep(result)
	for _, ref := range macCertificateRefs(result) {
		data := b.keep(macSec.secCertCopyData(ref))
		if !bytes.Equal(cfDataBytes(data), der) {
			continue
		}
		status := macSec.secItemDelete(b.dict(
			macSec.kSecClass, macSec.kSecClassCertificate,
			macSec.kSecValueRef, ref,
		))
		if status == errSecItemNotFound {
			continue
		}
		if err := macTrustError("删除钥匙串证书", status); err != nil {
			return err
		}
	}
	return nil
}

func macCertificateRefs(result uintptr) []uintptr {
	if result == 0 {
		return nil
	}
	if macSec.cfGetTypeID(result) != macSec.cfArrayGetTypeID() {
		return []uintptr{result}
	}
	n := macSec.cfArrayGetCount(result)
	refs := make([]uintptr, 0, n)
	for i := 0; i < n; i++ {
		if ref := macSec.cfArrayGetValueAtIndex(result, i); ref != 0 {
			refs = append(refs, ref)
		}
	}
	return refs
}

// macTrustError maps a user-canceled authorization to errTrustCanceled.
func macTrustError(op string, status int32) error {
	switch status {
	case errSecSuccess:
		return nil
	case errSecAuthFailed, errSecUserCanceled, errAuthorizationDeny, errAuthorizationCancl:
		return errTrustCanceled
	}
	return osStatus(op, status)
}
