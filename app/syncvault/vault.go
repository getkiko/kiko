// Package syncvault implements the end-to-end encrypted envelope used by Kiko remote sync.
//
// A password is stretched with Argon2id (random salt, stored in clear in the envelope header so every
// device derives the same key), then HKDF-SHA256 derives purpose-separated sub keys. The payload is
// gzip-compressed and sealed with AES-256-GCM; the AAD binds the format, version, vault id and revision.
package syncvault

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

const (
	Format  = "kiko-vault"
	version = 1

	// LegacyFormat is the backup format created before the rename to Kiko. Its
	// subkeys are derived with the old HKDF info and it stays readable and
	// writable; when unlocking with the password, the caller upgrades it to
	// Format via Create.
	LegacyFormat = "sbcfg-vault"

	kdfArgon2id = "argon2id"
	kcvMessage  = "check"

	saltLen     = 16
	minSaltLen  = 16
	maxSaltLen  = 64
	maxTime     = 10
	maxMemory   = 512 * 1024 // KiB
	maxThreads  = 16
	keyLen      = 32
	nonceLen    = 12
	maxPlain    = 64 << 20 // limit on decompressed plaintext, guards against zip bombs
	minPassword = 8
	maxVaultID  = 64
)

var (
	ErrWrongPassword = errors.New("密码错误")
	ErrWrongKey      = errors.New("本机保存的密钥与远端备份不匹配（密码可能已在其他设备上修改），请重新输入密码")
	ErrCorrupt       = errors.New("备份数据已损坏或被篡改")
	ErrUnsupported   = errors.New("不支持的备份格式、版本或参数")
)

// Production parameters; tests may lower them. ParseHeader's upper-bound checks
// are unaffected.
var (
	defaultTime      uint32    = 3
	defaultMemoryKiB uint32    = 64 * 1024
	defaultThreads   uint8     = 4
	randReader       io.Reader = rand.Reader
)

type KDF struct {
	Name      string `json:"name"`
	Time      uint32 `json:"t"`
	MemoryKiB uint32 `json:"m"`
	Threads   uint8  `json:"p"`
	Salt      []byte `json:"salt"`
}

type Header struct {
	Format   string `json:"format"`
	V        int    `json:"v"`
	VaultID  string `json:"vaultId"`
	KDF      KDF    `json:"kdf"`
	KCV      []byte `json:"kcv"`
	Revision uint64 `json:"revision"`
}

type envelope struct {
	Header
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// hkdfInfo returns the HKDF info strings for a format (encryption subkey, KCV subkey).
func hkdfInfo(format string) (enc, kcv string) {
	if format == LegacyFormat {
		return "sbcfg/v1/enc", "sbcfg/v1/kcv"
	}
	return "kiko/v1/enc", "kiko/v1/kcv"
}

func knownFormat(format string) bool { return format == Format || format == LegacyFormat }

// Keys holds the subkeys derived from the password; it does not hold the password.
type Keys struct {
	format  string
	vaultID string
	kdf     KDF
	enc     [keyLen]byte
	kcv     [keyLen]byte
}

func normalize(password string) string { return norm.NFKC.String(password) }

// CheckPassword checks that the password meets the minimum requirement (at least
// 8 characters after NFKC normalization).
func CheckPassword(password string) error {
	if utf8.RuneCountInString(normalize(password)) < minPassword {
		return fmt.Errorf("密码至少需要 %d 个字符", minPassword)
	}
	return nil
}

var commonPasswords = map[string]bool{
	"password": true, "12345678": true, "123456789": true, "1234567890": true, "qwertyui": true,
	"qwerty123": true, "11111111": true, "00000000": true, "abc12345": true, "password1": true, "iloveyou": true,
}

// PasswordStrength returns a rough strength from 0 to 4. It is only a UI hint and
// not a security guarantee.
func PasswordStrength(password string) int {
	pw := normalize(password)
	runes := []rune(pw)
	n := len(runes)
	if n < minPassword || commonPasswords[pw] {
		return 0
	}
	distinct := map[rune]bool{}
	var lower, upper, digit, symbol bool
	for _, r := range runes {
		distinct[r] = true
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			symbol = true
		}
	}
	if len(distinct) < 4 {
		return 1
	}
	classes := 0
	for _, ok := range []bool{lower, upper, digit, symbol} {
		if ok {
			classes++
		}
	}
	score := 1
	if n >= 12 {
		score++
	}
	if classes >= 3 {
		score++
	}
	if n >= 16 && classes >= 2 {
		score++
	}
	return min(score, 4)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return nil, err
	}
	return b, nil
}

func validVaultID(id string) bool {
	if id == "" || len(id) > maxVaultID {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validateKDF(k KDF) error {
	switch {
	case k.Name != kdfArgon2id:
		return fmt.Errorf("%w: kdf %q", ErrUnsupported, k.Name)
	case k.Time < 1 || k.Time > maxTime:
		return fmt.Errorf("%w: kdf.t=%d", ErrUnsupported, k.Time)
	case k.Threads < 1 || k.Threads > maxThreads:
		return fmt.Errorf("%w: kdf.p=%d", ErrUnsupported, k.Threads)
	case k.MemoryKiB < 8*uint32(k.Threads) || k.MemoryKiB > maxMemory:
		return fmt.Errorf("%w: kdf.m=%d", ErrUnsupported, k.MemoryKiB)
	case len(k.Salt) < minSaltLen || len(k.Salt) > maxSaltLen:
		return fmt.Errorf("%w: kdf.salt length %d", ErrUnsupported, len(k.Salt))
	}
	return nil
}

func validateHeader(h Header) error {
	if !knownFormat(h.Format) || h.V != version {
		return fmt.Errorf("%w: %q v%d", ErrUnsupported, h.Format, h.V)
	}
	if !validVaultID(h.VaultID) {
		return fmt.Errorf("%w: vaultId", ErrUnsupported)
	}
	if len(h.KCV) != keyLen {
		return fmt.Errorf("%w: kcv length", ErrUnsupported)
	}
	return validateKDF(h.KDF)
}

// ParseHeader parses and validates only the envelope header; it needs no key.
func ParseHeader(data []byte) (Header, error) {
	var h Header
	if err := json.Unmarshal(data, &h); err != nil {
		return Header{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := validateHeader(h); err != nil {
		return Header{}, err
	}
	return h, nil
}

func derive(format, password, vaultID string, kdf KDF) (*Keys, error) {
	master := argon2.IDKey([]byte(normalize(password)), kdf.Salt, kdf.Time, kdf.MemoryKiB, kdf.Threads, keyLen)
	defer clear(master)
	salt := []byte(vaultID)
	infoEnc, infoKCV := hkdfInfo(format)
	enc, err := hkdf.Key(sha256.New, master, salt, infoEnc, keyLen)
	if err != nil {
		return nil, err
	}
	kcvKey, err := hkdf.Key(sha256.New, master, salt, infoKCV, keyLen)
	if err != nil {
		return nil, err
	}
	defer clear(kcvKey)
	mac := hmac.New(sha256.New, kcvKey)
	mac.Write([]byte(kcvMessage))
	k := &Keys{format: format, vaultID: vaultID, kdf: kdf}
	k.kdf.Salt = bytes.Clone(kdf.Salt)
	copy(k.enc[:], enc)
	copy(k.kcv[:], mac.Sum(nil))
	clear(enc)
	return k, nil
}

// Create generates a new vault with a random salt (and a random vaultID if empty)
// and returns its keys and header (Revision=0).
func Create(password, vaultID string) (*Keys, Header, error) {
	if password == "" {
		return nil, Header{}, ErrWrongPassword
	}
	if vaultID == "" {
		id, err := randomBytes(16)
		if err != nil {
			return nil, Header{}, err
		}
		vaultID = hex.EncodeToString(id)
	}
	if !validVaultID(vaultID) {
		return nil, Header{}, fmt.Errorf("%w: vaultId", ErrUnsupported)
	}
	salt, err := randomBytes(saltLen)
	if err != nil {
		return nil, Header{}, err
	}
	kdf := KDF{Name: kdfArgon2id, Time: defaultTime, MemoryKiB: defaultMemoryKiB, Threads: defaultThreads, Salt: salt}
	keys, err := derive(Format, password, vaultID, kdf)
	if err != nil {
		return nil, Header{}, err
	}
	return keys, Header{Format: Format, V: version, VaultID: vaultID, KDF: kdf, KCV: bytes.Clone(keys.kcv[:])}, nil
}

// Unlock derives keys from the password and envelope header and verifies the KCV.
// A wrong password returns ErrWrongPassword.
func Unlock(password string, h Header) (*Keys, error) {
	if err := validateHeader(h); err != nil {
		return nil, err
	}
	keys, err := derive(h.Format, password, h.VaultID, h.KDF)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(keys.kcv[:], h.KCV) {
		return nil, ErrWrongPassword
	}
	return keys, nil
}

func (k *Keys) VaultID() string { return k.vaultID }

// Format returns the format of the backup the keys belong to: Format, or
// LegacyFormat for backups created before the rename.
func (k *Keys) Format() string { return k.format }

// Matches reports whether the header belongs to these keys (same format, vaultId,
// KDF parameters, salt and KCV).
func (k *Keys) Matches(h Header) bool {
	return h.Format == k.format && h.VaultID == k.vaultID &&
		h.KDF.Name == k.kdf.Name && h.KDF.Time == k.kdf.Time && h.KDF.MemoryKiB == k.kdf.MemoryKiB &&
		h.KDF.Threads == k.kdf.Threads && bytes.Equal(h.KDF.Salt, k.kdf.Salt) &&
		hmac.Equal(h.KCV, k.kcv[:])
}

func aad(format, vaultID string, revision uint64) []byte {
	buf := make([]byte, 0, len(format)+len(vaultID)+12)
	buf = append(buf, format...)
	buf = append(buf, 0)
	buf = append(buf, byte(version))
	buf = append(buf, vaultID...)
	buf = append(buf, 0)
	return binary.BigEndian.AppendUint64(buf, revision)
}

func (k *Keys) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.enc[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal compresses and encrypts payload and returns the envelope JSON. h supplies
// VaultID/KDF/KCV; the envelope's Revision is set to revision.
func (k *Keys) Seal(h Header, revision uint64, payload []byte) ([]byte, error) {
	if len(payload) > maxPlain {
		return nil, fmt.Errorf("备份数据过大（超过 %d MiB）", maxPlain>>20)
	}
	var compressed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err = zw.Write(payload); err != nil {
		return nil, err
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	return k.sealCompressed(h, revision, compressed.Bytes())
}

func (k *Keys) sealCompressed(h Header, revision uint64, compressed []byte) ([]byte, error) {
	if !k.Matches(h) {
		return nil, ErrWrongKey
	}
	aead, err := k.gcm()
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(nonceLen)
	if err != nil {
		return nil, err
	}
	out := h
	out.Format, out.V, out.Revision = k.format, version, revision
	return json.Marshal(envelope{
		Header: out, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, compressed, aad(k.format, h.VaultID, revision)),
	})
}

// Open checks that the envelope matches the keys, then AEAD-decrypts and
// decompresses it.
func (k *Keys) Open(data []byte) ([]byte, Header, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, Header{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := validateHeader(env.Header); err != nil {
		return nil, Header{}, err
	}
	if !k.Matches(env.Header) {
		return nil, Header{}, ErrWrongKey
	}
	if len(env.Nonce) != nonceLen {
		return nil, Header{}, fmt.Errorf("%w: nonce", ErrCorrupt)
	}
	aead, err := k.gcm()
	if err != nil {
		return nil, Header{}, err
	}
	compressed, err := aead.Open(nil, env.Nonce, env.Ciphertext, aad(env.Format, env.VaultID, env.Revision))
	if err != nil {
		return nil, Header{}, ErrCorrupt
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, Header{}, fmt.Errorf("%w: gzip", ErrCorrupt)
	}
	defer zr.Close()
	payload, err := io.ReadAll(io.LimitReader(zr, maxPlain+1))
	if err != nil {
		return nil, Header{}, fmt.Errorf("%w: gzip", ErrCorrupt)
	}
	if len(payload) > maxPlain {
		return nil, Header{}, fmt.Errorf("%w: 解压后数据过大", ErrCorrupt)
	}
	return payload, env.Header, nil
}

type keyFile struct {
	// An empty Format means a key file written before the rename (LegacyFormat).
	Format  string `json:"format,omitempty"`
	V       int    `json:"v"`
	VaultID string `json:"vaultId"`
	KDF     KDF    `json:"kdf"`
	KCV     []byte `json:"kcv"`
	Enc     []byte `json:"enc"`
}

// Marshal returns the local key file contents. They include the encryption key,
// so the caller must write them with mode 0600.
func (k *Keys) Marshal() []byte {
	b, _ := json.Marshal(keyFile{Format: k.format, V: version, VaultID: k.vaultID, KDF: k.kdf, KCV: k.kcv[:], Enc: k.enc[:]})
	return b
}

func UnmarshalKeys(data []byte) (*Keys, error) {
	var f keyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if f.Format == "" {
		f.Format = LegacyFormat
	}
	if !knownFormat(f.Format) || f.V != version || !validVaultID(f.VaultID) || len(f.KCV) != keyLen || len(f.Enc) != keyLen {
		return nil, ErrUnsupported
	}
	if err := validateKDF(f.KDF); err != nil {
		return nil, err
	}
	k := &Keys{format: f.Format, vaultID: f.VaultID, kdf: f.KDF}
	k.kdf.Salt = bytes.Clone(f.KDF.Salt)
	copy(k.enc[:], f.Enc)
	copy(k.kcv[:], f.KCV)
	return k, nil
}
