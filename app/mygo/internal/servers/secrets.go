package servers

import (
	"encoding/json"
	"errors"
	"os"
)

// The tokens go to the credential store; where the system has none
// (errors.ErrUnsupported), to server-tokens.json, readable by the user
// only. Once the store proved missing, the file serves for the rest of the
// run.

func (d *Directory) useFile(err error) bool {
	if err == nil || !errors.Is(err, errors.ErrUnsupported) {
		return false
	}
	d.mu.Lock()
	d.fallback = true
	d.mu.Unlock()
	return true
}

func (d *Directory) onFile() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fallback
}

func (d *Directory) setSecret(key string, value []byte) error {
	if !d.onFile() {
		err := d.secrets.Set(key, value)
		if !d.useFile(err) {
			return err
		}
	}
	return d.editTokens(func(tokens map[string]string) { tokens[key] = string(value) })
}

func (d *Directory) getSecret(key string) ([]byte, error) {
	if !d.onFile() {
		value, err := d.secrets.Get(key)
		if !d.useFile(err) {
			return value, err
		}
	}
	tokens, err := d.readTokens()
	if err != nil {
		return nil, err
	}
	value, ok := tokens[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return []byte(value), nil
}

func (d *Directory) deleteSecret(key string) error {
	if !d.onFile() {
		err := d.secrets.Delete(key)
		if !d.useFile(err) {
			return err
		}
	}
	return d.editTokens(func(tokens map[string]string) { delete(tokens, key) })
}

func (d *Directory) readTokens() (map[string]string, error) {
	tokens := map[string]string{}
	data, err := os.ReadFile(d.tokenPath)
	if errors.Is(err, os.ErrNotExist) {
		return tokens, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}
	return tokens, nil
}

func (d *Directory) editTokens(fn func(map[string]string)) error {
	d.tokens.Lock()
	defer d.tokens.Unlock()
	tokens, err := d.readTokens()
	if err != nil {
		return err
	}
	fn(tokens)
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(d.tokenPath, data)
}

// MigratingSecrets is a SecretStore that moves tokens from legacy, the
// credential store service used before the app was renamed Kiko, into
// current: a token found only in legacy is copied to current and then
// removed from legacy. Deleting removes the key from both. An unsupported
// current store (errors.ErrUnsupported) passes through, so the directory
// falls back to its token file as before.
func MigratingSecrets(current, legacy SecretStore) SecretStore {
	return migratingSecrets{current: current, legacy: legacy}
}

type migratingSecrets struct{ current, legacy SecretStore }

func (m migratingSecrets) Set(key string, value []byte) error { return m.current.Set(key, value) }

func (m migratingSecrets) Get(key string) ([]byte, error) {
	value, err := m.current.Get(key)
	if !errors.Is(err, ErrSecretNotFound) {
		return value, err
	}
	old, legacyErr := m.legacy.Get(key)
	if legacyErr != nil {
		return value, err
	}
	if setErr := m.current.Set(key, old); setErr != nil {
		return nil, setErr
	}
	_ = m.legacy.Delete(key)
	return old, nil
}

// Delete removes the key from current; removing a leftover legacy copy
// is best effort.
func (m migratingSecrets) Delete(key string) error {
	err := m.current.Delete(key)
	_ = m.legacy.Delete(key)
	return err
}
