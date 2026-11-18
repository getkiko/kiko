package main

import (
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// fileSecretStore stores the private key, encoded, as a single file under <dataDir>/mitm:
// directory 0700, file 0600, replaced atomically. encode / decode set the on-disk format
// (PEM by default, DPAPI ciphertext on Windows). Errors include only the path and the
// system error, never the content.
type fileSecretStore struct {
	path   string
	encode func([]byte) ([]byte, error)
	decode func([]byte) ([]byte, error)
	// When legacyDecode is non-nil and decode fails, it reads files written before the rename to
	// Kiko, which are then rewritten in the current format (the Windows DPAPI entropy changed
	// with the rename).
	legacyDecode func([]byte) ([]byte, error)
}

const secretPEMType = "PRIVATE KEY"

var errSecretCorrupt = errors.New("根证书私钥文件格式无效")

func newPEMSecretStore(dataDir string) *fileSecretStore {
	return &fileSecretStore{
		path:   filepath.Join(dataDir, "mitm", "ca.key"),
		encode: encodeSecretPEM,
		decode: decodeSecretPEM,
	}
}

func encodeSecretPEM(key []byte) ([]byte, error) {
	return pem.EncodeToMemory(&pem.Block{Type: secretPEMType, Bytes: key}), nil
}

func decodeSecretPEM(data []byte) ([]byte, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != secretPEMType || len(block.Bytes) == 0 {
		return nil, errSecretCorrupt
	}
	return block.Bytes, nil
}

func (s *fileSecretStore) Load() ([]byte, error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errSecretNotFound
	}
	if err != nil {
		return nil, err
	}
	// If permissions were loosened, tighten them back to 0600 and continue.
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(s.path, 0o600); err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errSecretNotFound
	}
	if err != nil {
		return nil, err
	}
	defer clear(data)
	key, err := s.decode(data)
	if err == nil || s.legacyDecode == nil {
		return key, err
	}
	key, legacyErr := s.legacyDecode(data)
	if legacyErr != nil {
		return nil, err
	}
	_ = s.Save(key)
	return key, nil
}

func (s *fileSecretStore) Save(key []byte) error {
	data, err := s.encode(key)
	if err != nil {
		return err
	}
	defer clear(data)
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return atomicWrite(s.path, data, 0o600)
}

func (s *fileSecretStore) Delete() error {
	err := os.Remove(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
