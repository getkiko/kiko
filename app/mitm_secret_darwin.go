//go:build darwin

package main

// On macOS the root CA private key is stored as a generic password item in the
// login keychain (not synced to iCloud).
//
// Access control uses the keychain's default ACL: the program that created the
// item (identified by its code signature) can read it directly. When the code
// signature of kiko-server changes (e.g. an ad-hoc signature after a rebuild),
// macOS shows an "allow keychain access" dialog and the key can only be read
// once the user allows it. Without a GUI session (e.g. over SSH), reads and
// writes return error codes such as errSecInteractionNotAllowed.

const (
	macSecretService = "Kiko HTTPS Decryption"
	macSecretAccount = "root-ca-key"
	macSecretLabel   = "Kiko Root CA Key"

	// Keychain item from before the rename to Kiko; migrated to
	// macSecretService on read.
	legacyMacSecretService = "Poppy HTTPS Decryption"
	legacyMacSecretLabel   = "Poppy Root CA Key"
)

type keychainSecretStore struct {
	service string
	account string
	label   string
}

func newSecretStore(string) secretStore {
	return &migratingSecretStore{
		current: &keychainSecretStore{service: macSecretService, account: macSecretAccount, label: macSecretLabel},
		legacy:  &keychainSecretStore{service: legacyMacSecretService, account: macSecretAccount, label: legacyMacSecretLabel},
	}
}

func (s *keychainSecretStore) query(b *cfBag, extra ...uintptr) uintptr {
	kv := []uintptr{
		macSec.kSecClass, macSec.kSecClassGenericPassword,
		macSec.kSecAttrService, b.str(s.service),
		macSec.kSecAttrAccount, b.str(s.account),
	}
	return b.dict(append(kv, extra...)...)
}

func (s *keychainSecretStore) Load() ([]byte, error) {
	if err := loadMacSecurity(); err != nil {
		return nil, err
	}
	var b cfBag
	defer b.release()
	q := s.query(&b,
		macSec.kSecReturnData, macSec.booleanTrue,
		macSec.kSecMatchLimit, macSec.kSecMatchLimitOne,
	)
	var result uintptr
	status := macSec.secItemCopyMatching(q, &result)
	if status == errSecItemNotFound {
		return nil, errSecretNotFound
	}
	if err := osStatus("读取钥匙串", status); err != nil {
		return nil, err
	}
	b.keep(result)
	key := cfDataBytes(result)
	if len(key) == 0 {
		return nil, errSecretNotFound
	}
	return key, nil
}

func (s *keychainSecretStore) Save(key []byte) error {
	if err := loadMacSecurity(); err != nil {
		return err
	}
	var b cfBag
	defer b.release()
	value := b.data(key)
	attrs := s.query(&b,
		macSec.kSecAttrLabel, b.str(s.label),
		macSec.kSecAttrAccessible, macSec.kSecAttrAccessibleAfterFirst,
		macSec.kSecValueData, value,
	)
	status := macSec.secItemAdd(attrs, nil)
	if status == errSecDuplicateItem {
		status = macSec.secItemUpdate(s.query(&b), b.dict(macSec.kSecValueData, value))
	}
	return osStatus("写入钥匙串", status)
}

func (s *keychainSecretStore) Delete() error {
	if err := loadMacSecurity(); err != nil {
		return err
	}
	var b cfBag
	defer b.release()
	status := macSec.secItemDelete(s.query(&b))
	if status == errSecItemNotFound {
		return nil
	}
	return osStatus("删除钥匙串项", status)
}
