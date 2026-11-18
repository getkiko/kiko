package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stickySecretStore chooses the private key backend, file or Secret Service, and
// records the choice in <dataDir>/mitm/ca.key.backend. A recorded backend is never
// silently replaced: if secretservice is recorded but the service is unavailable,
// Load returns a non-NotFound error so sync backs off instead of regenerating the
// root CA.
//
// Headless gateways (no graphical session or no Secret Service) use the 0600 file
// backend.
type stickySecretStore struct {
	dir     string
	file    secretStore
	service secretStore
	// preferService decides whether a new key tries the Secret Service (which
	// needs a graphical session to show the unlock prompt).
	preferService func() bool
}

const (
	secretBackendFile    = "file"
	secretBackendService = "secretservice"
)

func newStickySecretStore(dataDir string, file, service secretStore, preferService func() bool) *stickySecretStore {
	return &stickySecretStore{dir: filepath.Join(dataDir, "mitm"), file: file, service: service, preferService: preferService}
}

func (s *stickySecretStore) markerPath() string { return filepath.Join(s.dir, "ca.key.backend") }

// backend reads the recorded backend, or returns "" when none is recorded.
// Unrecognized content is treated as corrupt.
func (s *stickySecretStore) backend() (string, error) {
	data, err := os.ReadFile(s.markerPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch value := strings.TrimSpace(string(data)); value {
	case secretBackendFile, secretBackendService:
		return value, nil
	}
	return "", errSecretCorrupt
}

func (s *stickySecretStore) writeMarker(backend string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return atomicWrite(s.markerPath(), []byte(backend+"\n"), 0o600)
}

func (s *stickySecretStore) store(backend string) secretStore {
	if backend == secretBackendService {
		return s.service
	}
	return s.file
}

func (s *stickySecretStore) Load() ([]byte, error) {
	backend, err := s.backend()
	if err != nil {
		return nil, err
	}
	if backend != "" {
		return s.store(backend).Load()
	}
	// Nothing recorded: check the file first (installs from before the upgrade),
	// then the Secret Service.
	key, err := s.file.Load()
	if err == nil {
		if err := s.writeMarker(secretBackendFile); err != nil {
			clear(key)
			return nil, err
		}
		return key, nil
	}
	if !errors.Is(err, errSecretNotFound) {
		return nil, err
	}
	if !s.preferService() {
		return nil, errSecretNotFound
	}
	key, err = s.service.Load()
	switch {
	case err == nil:
		if err := s.writeMarker(secretBackendService); err != nil {
			clear(key)
			return nil, err
		}
		return key, nil
	case serviceAbsent(err):
		return nil, errSecretNotFound
	}
	return nil, err
}

// serviceAbsent reports whether the Secret Service is missing or has no default
// collection (only used while no backend is recorded).
func serviceAbsent(err error) bool {
	return errors.Is(err, errSecretServiceUnavailable) || errors.Is(err, errSecretNoCollection) || errors.Is(err, errSecretPromptTimeout)
}

func (s *stickySecretStore) Save(key []byte) error {
	backend, err := s.backend()
	if errors.Is(err, errSecretCorrupt) && s.noKeyInAnyBackend() {
		// Corrupt marker and no readable key in either backend: treat it as no
		// marker and overwrite it after writing.
		backend, err = "", nil
	}
	if err != nil {
		return err
	}
	if backend == "" {
		backend = secretBackendFile
		if s.preferService() {
			err := s.service.Save(key)
			switch {
			case err == nil:
				backend = secretBackendService
			case serviceAbsent(err):
			default:
				return err
			}
		}
		if backend == secretBackendFile {
			if err := s.file.Save(key); err != nil {
				return err
			}
		}
		if err := s.writeMarker(backend); err != nil {
			_ = s.store(backend).Delete()
			return err
		}
		return nil
	}
	return s.store(backend).Save(key)
}

// noKeyInAnyBackend confirms that Load cannot find any key: the file does not
// exist and the Secret Service has no item, is absent or would not be used. Any
// other error counts as a possible key (fail closed).
func (s *stickySecretStore) noKeyInAnyBackend() bool {
	key, err := s.file.Load()
	if err == nil {
		clear(key)
		return false
	}
	if !errors.Is(err, errSecretNotFound) {
		return false
	}
	if !s.preferService() {
		return true
	}
	key, err = s.service.Load()
	if err == nil {
		clear(key)
		return false
	}
	return errors.Is(err, errSecretNotFound) || serviceAbsent(err)
}

func (s *stickySecretStore) Delete() error {
	backend, err := s.backend()
	if err != nil && !errors.Is(err, errSecretCorrupt) {
		return err
	}
	if backend == "" {
		// No record or a corrupt record: clear the file backend and the record.
		if err := s.file.Delete(); err != nil {
			return err
		}
	} else if err := s.store(backend).Delete(); err != nil {
		return err
	}
	if err := os.Remove(s.markerPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
