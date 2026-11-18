//go:build windows

package main

import (
	"crypto/sha1"
	"crypto/x509"
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsTrustStore adds the root certificate to the current user's "Trusted
// Root Certification Authorities" store; Windows shows a confirmation dialog
// on both add and delete.
type windowsTrustStore struct{}

func newTrustStore() trustStore { return windowsTrustStore{} }

func (windowsTrustStore) Supported() bool { return true }

func openUserRootStore() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return 0, err
	}
	return windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, 0, 0,
		windows.CERT_SYSTEM_STORE_CURRENT_USER, uintptr(unsafe.Pointer(name)))
}

func (windowsTrustStore) Install(cert *x509.Certificate) error {
	if cert == nil || len(cert.Raw) == 0 {
		return errors.New("证书格式无效")
	}
	store, err := openUserRootStore()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0)
	ctx, err := windows.CertCreateCertificateContext(
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &cert.Raw[0], uint32(len(cert.Raw)))
	if err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(ctx)
	err = windows.CertAddCertificateContextToStore(store, ctx, windows.CERT_STORE_ADD_REPLACE_EXISTING, nil)
	return windowsTrustError(err)
}

func (windowsTrustStore) Remove(cert *x509.Certificate) error {
	store, err := openUserRootStore()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0)
	sum := sha1.Sum(cert.Raw)
	hash := windows.CryptHashBlob{Size: uint32(len(sum)), Data: &sum[0]}
	// At most one copy per hash; the cap avoids spinning if a delete has no effect.
	for range 8 {
		ctx, err := windows.CertFindCertificateInStore(store,
			windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, 0,
			windows.CERT_FIND_SHA1_HASH, unsafe.Pointer(&hash), nil)
		if ctx == nil {
			if err == nil || isWindowsNotFound(err) {
				return nil
			}
			return err
		}
		// CertDeleteCertificateFromStore always frees ctx.
		if err := windows.CertDeleteCertificateFromStore(ctx); err != nil {
			return windowsTrustError(err)
		}
	}
	return nil
}

func isWindowsNotFound(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && uint32(errno) == uint32(windows.CRYPT_E_NOT_FOUND)
}

// windowsTrustError maps the user canceling the system dialog to errTrustCanceled.
func windowsTrustError(err error) error {
	if err == nil {
		return nil
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch uint32(errno) {
		case uint32(windows.ERROR_CANCELLED), 0x800704C7, uint32(windows.SCARD_W_CANCELLED_BY_USER):
			return errTrustCanceled
		}
	}
	return err
}
