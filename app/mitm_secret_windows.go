//go:build windows

package main

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiEntropy is the additional entropy for DPAPI. On Windows the private key
// is encrypted with DPAPI (current-user scope) and the ciphertext is stored in
// <dataDir>/mitm/ca.key.dpapi.
var dpapiEntropy = []byte("Kiko HTTPS Decryption root CA key v1")

// legacyDPAPIEntropy is the additional entropy used before the rename to Kiko;
// files encrypted with it are rewritten with dpapiEntropy on read.
var legacyDPAPIEntropy = []byte("Poppy HTTPS Decryption root CA key v1")

func newSecretStore(dataDir string) secretStore {
	return &fileSecretStore{
		path:   filepath.Join(dataDir, "mitm", "ca.key.dpapi"),
		encode: dpapiProtect,
		decode: dpapiUnprotect,
		legacyDecode: func(blob []byte) ([]byte, error) {
			return dpapiUnprotectWith(blob, legacyDPAPIEntropy)
		},
	}
}

func dpapiBlob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func dpapiProtect(key []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptProtectData(dpapiBlob(key), nil, dpapiBlob(dpapiEntropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return dpapiTake(&out, false), nil
}

func dpapiUnprotect(blob []byte) ([]byte, error) {
	return dpapiUnprotectWith(blob, dpapiEntropy)
}

func dpapiUnprotectWith(blob, entropy []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptUnprotectData(dpapiBlob(blob), nil, dpapiBlob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, errSecretCorrupt
	}
	return dpapiTake(&out, true), nil
}

// dpapiTake copies the output into Go memory and calls LocalFree; plaintext
// output is zeroed before it is freed.
func dpapiTake(out *windows.DataBlob, wipe bool) []byte {
	if out.Data == nil {
		return nil
	}
	src := unsafe.Slice(out.Data, out.Size)
	b := make([]byte, len(src))
	copy(b, src)
	if wipe {
		clear(src)
	}
	windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return b
}
