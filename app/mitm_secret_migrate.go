package main

import "errors"

// migratingSecretStore migrates a root CA private key saved before the rename to
// Kiko to the current location. When the current location has no key it reads
// the old location; if a key is found it is written to the current location and
// the old entry is deleted. If that write fails the old entry is kept, the key is
// still returned, and migration is retried on the next read.
type migratingSecretStore struct {
	current secretStore
	legacy  secretStore
}

func (s *migratingSecretStore) Load() ([]byte, error) {
	key, err := s.current.Load()
	if !errors.Is(err, errSecretNotFound) {
		return key, err
	}
	old, err := s.legacy.Load()
	if err != nil {
		return nil, err
	}
	if s.current.Save(old) == nil {
		_ = s.legacy.Delete()
	}
	return old, nil
}

func (s *migratingSecretStore) Save(key []byte) error { return s.current.Save(key) }

// Delete removes the private key from both the current and the old location.
func (s *migratingSecretStore) Delete() error {
	err := s.current.Delete()
	if legacyErr := s.legacy.Delete(); err == nil {
		err = legacyErr
	}
	return err
}
