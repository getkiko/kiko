package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Platform-independent storage logic for the freedesktop Secret Service. D-Bus
// calls go through the secretBusConn interface; the Linux implementation lives in
// mitm_secret_linux.go and tests use an in-memory fake.
//
// Sessions use the plain algorithm: data only travels over the private Unix
// socket of the local session bus, matching libsecret's behavior without dh
// support. Copies of the private key are zeroed after each call and never logged.

const (
	secretServiceCollection = "/org/freedesktop/secrets/aliases/default"
	secretServiceLabel      = "Kiko Root CA Key"
	secretServicePromptWait = 60 * time.Second
)

var secretServiceAttributes = map[string]string{
	"application": "kiko",
	"purpose":     "mitm-root-ca-key",
}

// legacySecretServiceAttributes are the attributes of items written before the
// rename to Kiko (application=poppy). Scoped legacy items of this data directory
// are migrated to the new attributes on read; unscoped legacy items are only read.
var legacySecretServiceAttributes = map[string]string{
	"application": "poppy",
	"purpose":     "mitm-root-ca-key",
}

var (
	// errSecretServiceUnavailable means there is no session bus or Secret Service.
	errSecretServiceUnavailable = errors.New("secret service unavailable")
	// errSecretNoCollection means the service is running but has no default
	// collection (aliases/default).
	errSecretNoCollection = errors.New("secret service has no default collection")
	// errSecretPromptDismissed means the user dismissed the unlock or authorization prompt.
	errSecretPromptDismissed = errors.New("secret service prompt dismissed")
	// errSecretLocked means the collection is still locked after unlocking.
	errSecretLocked = errors.New("secret service collection locked")
)

type secretBusConn interface {
	Close()
	// OpenSession opens a plain session and returns the session object path.
	OpenSession() (string, error)
	CollectionLocked(collection string) (bool, error)
	// Unlock requests unlocking the objects and returns the prompt object path
	// ("/" or empty means no prompt is needed).
	Unlock(objects []string) (string, error)
	SearchItems(collection string, attrs map[string]string) ([]string, error)
	// ItemAttributes reads an item's org.freedesktop.Secret.Item.Attributes.
	ItemAttributes(item string) (map[string]string, error)
	GetSecret(item, session string) ([]byte, error)
	// CreateItem creates an item with replace=true and returns the item and prompt paths.
	CreateItem(collection, label string, attrs map[string]string, value []byte, session string) (item, prompt string, err error)
	DeleteItem(item string) (prompt string, err error)
	// WaitPrompt shows the prompt and waits for the Completed signal.
	WaitPrompt(prompt string, timeout time.Duration) (dismissed bool, err error)
}

type secretServiceStore struct {
	dial func() (secretBusConn, error)
	// attrs are the attributes of this data directory's item (including
	// data_dir_hash); without a scope they equal the unscoped attributes.
	attrs map[string]string
	// legacyScopes are the data_dir_hash values this data directory had
	// before the rename to Kiko (see legacySecretScopeDirs).
	legacyScopes []string
	// keyMatches reports whether a key from a pre-rename item is the private
	// key of this data directory's root certificate; nil accepts any key.
	keyMatches func(key []byte) bool
	// When legacyAdopt returns true, Load falls back to the item with the old
	// unscoped attributes (no hash) if no scoped item exists and exactly one
	// item matches. Such items are only read, never overwritten or deleted,
	// since they may belong to another config directory.
	legacyAdopt func() bool
}

// secretServiceScope returns the scope hash of a data directory: the first 16 hex
// characters of the SHA-256 of its cleaned absolute path.
func secretServiceScope(dataDir string) string {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return hex.EncodeToString(sum[:])[:16]
}

// Names the default data directories had before the rename to Kiko: the
// server directory was sbcfg-server, and the desktop app, which keeps the
// server directory inside its own, was Poppy (earlier sbcfg-desktop).
const (
	secretServerDirName       = "kiko-server"
	secretLegacyServerDirName = "sbcfg-server"
	secretDesktopDirName      = "Kiko"
)

var secretLegacyDesktopDirNames = []string{"Poppy", "sbcfg-desktop"}

// legacySecretScopeDirs returns the paths a data directory had before the
// rename to Kiko, whose items were scoped by those paths' hashes:
// .../kiko-server was .../sbcfg-server, and .../Kiko/kiko-server of the
// desktop app was .../Poppy/sbcfg-server (or .../Poppy/kiko-server, or
// under sbcfg-desktop). Other data directories were not renamed.
func legacySecretScopeDirs(dataDir string) []string {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	dataDir = filepath.Clean(dataDir)
	if filepath.Base(dataDir) != secretServerDirName {
		return nil
	}
	parent := filepath.Dir(dataDir)
	parents := []string{parent}
	if filepath.Base(parent) == secretDesktopDirName {
		for _, name := range secretLegacyDesktopDirNames {
			parents = append(parents, filepath.Join(filepath.Dir(parent), name))
		}
	}
	var dirs []string
	for _, p := range parents {
		for _, name := range []string{secretLegacyServerDirName, secretServerDirName} {
			if dir := filepath.Join(p, name); dir != dataDir {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

// scoped scopes the store to a data directory; legacyAdopt decides whether old
// unscoped items are read.
func (s *secretServiceStore) scoped(dataDir string, legacyAdopt func() bool) *secretServiceStore {
	s.attrs, s.legacyAdopt = scopedSecretAttributes(secretServiceAttributes, dataDir), legacyAdopt
	s.legacyScopes = nil
	for _, dir := range legacySecretScopeDirs(dataDir) {
		s.legacyScopes = append(s.legacyScopes, secretServiceScope(dir))
	}
	return s
}

func scopedSecretAttributes(base map[string]string, dataDir string) map[string]string {
	return scopedSecretAttributesHash(base, secretServiceScope(dataDir))
}

func scopedSecretAttributesHash(base map[string]string, hash string) map[string]string {
	attrs := map[string]string{"data_dir_hash": hash}
	for k, v := range base {
		attrs[k] = v
	}
	return attrs
}

// legacyScopedAttrs returns the attribute sets of this data directory's items
// from before the rename: application=poppy under its current hash, and both
// application values under the hashes of its pre-rename paths. It is nil when
// the store is not scoped.
func (s *secretServiceStore) legacyScopedAttrs() []map[string]string {
	if s.attrs == nil {
		return nil
	}
	sets := []map[string]string{scopedSecretAttributesHash(legacySecretServiceAttributes, s.attrs["data_dir_hash"])}
	for _, hash := range s.legacyScopes {
		sets = append(sets,
			scopedSecretAttributesHash(secretServiceAttributes, hash),
			scopedSecretAttributesHash(legacySecretServiceAttributes, hash))
	}
	return sets
}

// searchLegacyScoped returns this data directory's pre-rename items.
func (s *secretServiceStore) searchLegacyScoped(conn secretBusConn) ([]string, error) {
	var found []string
	seen := map[string]bool{}
	for _, attrs := range s.legacyScopedAttrs() {
		items, err := conn.SearchItems(secretServiceCollection, attrs)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if !seen[item] {
				seen[item] = true
				found = append(found, item)
			}
		}
	}
	return found, nil
}

func (s *secretServiceStore) itemAttrs() map[string]string {
	if s.attrs != nil {
		return s.attrs
	}
	return secretServiceAttributes
}

func newSecretServiceStore(dial func() (secretBusConn, error)) *secretServiceStore {
	return &secretServiceStore{dial: dial}
}

func secretNoPrompt(path string) bool { return path == "" || path == "/" }

func (s *secretServiceStore) runPrompt(conn secretBusConn, prompt string) error {
	if secretNoPrompt(prompt) {
		return nil
	}
	dismissed, err := conn.WaitPrompt(prompt, secretServicePromptWait)
	if err != nil {
		return err
	}
	if dismissed {
		return errSecretPromptDismissed
	}
	return nil
}

// open connects and makes sure the default collection is unlocked.
func (s *secretServiceStore) open() (secretBusConn, string, error) {
	conn, err := s.dial()
	if err != nil {
		return nil, "", err
	}
	session, err := conn.OpenSession()
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	locked, err := conn.CollectionLocked(secretServiceCollection)
	if err != nil {
		conn.Close()
		return nil, "", err
	}
	if locked {
		prompt, err := conn.Unlock([]string{secretServiceCollection})
		if err == nil {
			err = s.runPrompt(conn, prompt)
		}
		if err == nil {
			var still bool
			if still, err = conn.CollectionLocked(secretServiceCollection); err == nil && still {
				err = errSecretLocked
			}
		}
		if err != nil {
			conn.Close()
			return nil, "", err
		}
	}
	return conn, session, nil
}

func (s *secretServiceStore) Load() ([]byte, error) {
	conn, session, err := s.open()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	items, err := conn.SearchItems(secretServiceCollection, s.itemAttrs())
	if err != nil {
		return nil, err
	}
	if len(items) == 0 && s.attrs != nil {
		old, err := s.searchLegacyScoped(conn)
		if err != nil {
			return nil, err
		}
		if len(old) > 0 {
			value, ok, err := s.migrate(conn, session, old)
			if err != nil || ok {
				return value, err
			}
		}
	}
	if len(items) == 0 && s.attrs != nil && s.legacyAdopt != nil && s.legacyAdopt() {
		if items, err = s.unscopedLegacy(conn); err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, errSecretNotFound
	}
	value, err := conn.GetSecret(items[0], session)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, errSecretCorrupt
	}
	return value, nil
}

// unscopedLegacy returns the unscoped pre-rename item if there is exactly one.
// The Secret Service matches attribute subsets, so the search also finds other
// data directories' scoped items; those carry data_dir_hash and are skipped.
func (s *secretServiceStore) unscopedLegacy(conn secretBusConn) ([]string, error) {
	found, err := conn.SearchItems(secretServiceCollection, legacySecretServiceAttributes)
	if err != nil {
		return nil, err
	}
	var unscoped []string
	for _, item := range found {
		attrs, err := conn.ItemAttributes(item)
		if err != nil {
			return nil, err
		}
		if _, scoped := attrs["data_dir_hash"]; !scoped {
			unscoped = append(unscoped, item)
		}
	}
	if len(unscoped) != 1 {
		return nil, nil
	}
	return unscoped, nil
}

// keyFits reports whether key is the private key of this data directory's root
// certificate; without a check every key fits.
func (s *secretServiceStore) keyFits(key []byte) bool {
	return s.keyMatches == nil || s.keyMatches(key)
}

// migrate adopts the first of the pre-rename items that holds the private key
// of this data directory's root certificate: it writes the key under the new
// attributes and deletes that item. Items holding other keys belong to other
// data directories and are left to their own migration. ok is false when no
// item fits; nothing is written or deleted then. If the write fails the item
// is kept and the key is still returned; migration is retried on the next read.
func (s *secretServiceStore) migrate(conn secretBusConn, session string, old []string) (value []byte, ok bool, err error) {
	for _, item := range old {
		value, err := conn.GetSecret(item, session)
		if err != nil {
			return nil, false, err
		}
		if len(value) == 0 || !s.keyFits(value) {
			clear(value)
			continue
		}
		copied := append([]byte(nil), value...)
		_, prompt, err := conn.CreateItem(secretServiceCollection, secretServiceLabel, s.itemAttrs(), copied, session)
		clear(copied)
		if err == nil {
			err = s.runPrompt(conn, prompt)
		}
		if err == nil {
			_ = s.deleteItems(conn, []string{item})
		}
		return value, true, nil
	}
	return nil, false, nil
}

func (s *secretServiceStore) deleteItems(conn secretBusConn, items []string) error {
	for _, item := range items {
		prompt, err := conn.DeleteItem(item)
		if err != nil {
			return err
		}
		if err := s.runPrompt(conn, prompt); err != nil {
			return err
		}
	}
	return nil
}

func (s *secretServiceStore) Save(key []byte) error {
	conn, session, err := s.open()
	if err != nil {
		return err
	}
	defer conn.Close()
	value := append([]byte(nil), key...)
	defer clear(value)
	_, prompt, err := conn.CreateItem(secretServiceCollection, secretServiceLabel, s.itemAttrs(), value, session)
	if err != nil {
		return err
	}
	return s.runPrompt(conn, prompt)
}

func (s *secretServiceStore) Delete() error {
	conn, session, err := s.open()
	if err != nil {
		return err
	}
	defer conn.Close()
	items, err := conn.SearchItems(secretServiceCollection, s.itemAttrs())
	if err != nil {
		return err
	}
	// Also delete this data directory's pre-rename items: those holding the
	// key of its root certificate. Others belong to other data directories.
	if s.attrs != nil {
		old, err := s.searchLegacyScoped(conn)
		if err != nil {
			return err
		}
		for _, item := range old {
			value, err := conn.GetSecret(item, session)
			if err != nil {
				return err
			}
			if s.keyFits(value) {
				items = append(items, item)
			}
			clear(value)
		}
	}
	return s.deleteItems(conn, items)
}

// errSecretPromptTimeout means the prompt did not complete within the wait time.
var errSecretPromptTimeout = errors.New("secret service prompt timed out")

// secretBusAddressUsable reports whether the session bus address can be dialed
// directly: it is non-empty and contains no autolaunch: entry (for which godbus
// would run dbus-launch).
func secretBusAddressUsable(addr string) bool {
	if addr == "" {
		return false
	}
	for _, part := range strings.Split(addr, ";") {
		if strings.HasPrefix(strings.TrimSpace(part), "autolaunch:") {
			return false
		}
	}
	return true
}

// secretErrorUnavailable reports whether a D-Bus error text means the service is
// unavailable, cannot be started or does not respond.
func secretErrorUnavailable(text string) bool {
	for _, name := range []string{
		"org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.Spawn.",
		"org.freedesktop.DBus.Error.NoReply",
		"org.freedesktop.DBus.Error.Timeout",
		"org.freedesktop.DBus.Error.TimedOut",
		"context deadline exceeded",
	} {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
}

// mitmRootKeyMatcher returns a check of whether a key is the private key of the
// root certificate saved in dataDir; with no readable certificate no key matches.
func mitmRootKeyMatcher(dataDir string) func(key []byte) bool {
	return func(key []byte) bool {
		root := readMITMRootCert(dataDir)
		if root == nil {
			return false
		}
		_, err := parseMITMRootKey(root, key)
		return err == nil
	}
}
