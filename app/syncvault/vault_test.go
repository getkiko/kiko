package syncvault

import (
	"bytes"
	"compress/gzip"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fastKDF switches to cheap parameters for tests; ParseHeader's upper bounds keep
// their production values.
func fastKDF(t *testing.T) {
	t.Helper()
	oldT, oldM, oldP := defaultTime, defaultMemoryKiB, defaultThreads
	defaultTime, defaultMemoryKiB, defaultThreads = 1, 64, 1
	t.Cleanup(func() { defaultTime, defaultMemoryKiB, defaultThreads = oldT, oldM, oldP })
}

func newVault(t *testing.T, password string) (*Keys, Header) {
	t.Helper()
	fastKDF(t)
	keys, header, err := Create(password, "")
	if err != nil {
		t.Fatal(err)
	}
	return keys, header
}

func TestRoundTripPayloads(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	for name, payload := range map[string][]byte{
		"empty":   {},
		"1 byte":  {0x42},
		"chinese": []byte(`{"name":"家里的配置","密码":"你好，世界"}`),
		"1MiB+":   bytes.Repeat([]byte("sing-box "), 130000),
	} {
		t.Run(name, func(t *testing.T) {
			sealed, err := keys.Seal(header, 7, payload)
			if err != nil {
				t.Fatal(err)
			}
			got, h, err := keys.Open(sealed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) || h.Revision != 7 || h.VaultID != header.VaultID {
				t.Fatalf("mismatch: revision=%d vault=%q payload equal=%v", h.Revision, h.VaultID, bytes.Equal(got, payload))
			}
		})
	}
}

func TestUnlockOnSecondDeviceAndWrongPassword(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	sealed, err := keys.Seal(header, 1, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHeader(sealed)
	if err != nil {
		t.Fatal(err)
	}
	other, err := Unlock("correct horse battery", parsed)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := other.Open(sealed); err != nil || string(got) != "secret" {
		t.Fatalf("second device open: %q %v", got, err)
	}
	_, err = Unlock("correct horse batterY", parsed)
	if !errors.Is(err, ErrWrongPassword) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrWrongPassword only, got %v", err)
	}
}

func TestPasswordIsNormalizedToNFKC(t *testing.T) {
	fastKDF(t)
	// U+FF21 (fullwidth A) and "A" are equal after NFKC normalization.
	keys, header, err := Create("ＡＢＣＤＥＦＧＨ-pass", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Unlock("ABCDEFGH-pass", header); err != nil {
		t.Fatalf("normalized password should unlock: %v", err)
	}
	_ = keys
}

func TestOpenRejectsTamperingOfEveryHeaderField(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	sealed, err := keys.Seal(header, 5, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(edit func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(sealed, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	kdf := func(m map[string]any) map[string]any { return m["kdf"].(map[string]any) }
	cases := map[string]func(m map[string]any){
		"format":   func(m map[string]any) { m["format"] = "other" },
		"v":        func(m map[string]any) { m["v"] = 2 },
		"vaultId":  func(m map[string]any) { m["vaultId"] = "deadbeef" },
		"revision": func(m map[string]any) { m["revision"] = 6 },
		"kdf.name": func(m map[string]any) { kdf(m)["name"] = "scrypt" },
		"kdf.t":    func(m map[string]any) { kdf(m)["t"] = 2 },
		"kdf.m":    func(m map[string]any) { kdf(m)["m"] = 128 },
		"kdf.p":    func(m map[string]any) { kdf(m)["p"] = 2 },
		"kdf.salt": func(m map[string]any) { kdf(m)["salt"] = "AAAAAAAAAAAAAAAAAAAAAA==" },
		"kcv":      func(m map[string]any) { m["kcv"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" },
		"nonce":    func(m map[string]any) { m["nonce"] = "AAAAAAAAAAAAAAAA" },
	}
	for name, edit := range cases {
		if _, _, err := keys.Open(mutate(edit)); err == nil {
			t.Errorf("tampering %s was accepted", name)
		}
	}
	// Flip some bytes of the nonce, ciphertext and authentication tag.
	var env envelope
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatal(err)
	}
	for _, pos := range []int{0, 5, len(env.Ciphertext) / 2, len(env.Ciphertext) - 1} {
		flipped := env
		flipped.Ciphertext = bytes.Clone(env.Ciphertext)
		flipped.Ciphertext[pos] ^= 1
		data, _ := json.Marshal(flipped)
		if _, _, err := keys.Open(data); !errors.Is(err, ErrCorrupt) {
			t.Errorf("ciphertext flip at %d: %v", pos, err)
		}
	}
	for pos := range env.Nonce {
		flipped := env
		flipped.Nonce = bytes.Clone(env.Nonce)
		flipped.Nonce[pos] ^= 0x80
		data, _ := json.Marshal(flipped)
		if _, _, err := keys.Open(data); !errors.Is(err, ErrCorrupt) {
			t.Errorf("nonce flip at %d: %v", pos, err)
		}
	}
}

func TestAADBindsRevisionAndVault(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	sealed, err := keys.Seal(header, 1, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err = json.Unmarshal(sealed, &env); err != nil {
		t.Fatal(err)
	}
	env.Revision = 2 // pass old ciphertext off as a newer revision
	data, _ := json.Marshal(env)
	if _, _, err = keys.Open(data); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("revision replay: %v", err)
	}
	// Another vault's keys cannot open it.
	other, _ := newVault(t, "correct horse battery")
	if _, _, err = other.Open(sealed); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("foreign vault: %v", err)
	}
	// The password was changed elsewhere (new salt): the old local keys do not
	// match the new envelope.
	fastKDF(t)
	_, changed, err := Create("correct horse battery", header.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if keys.Matches(changed) {
		t.Fatal("Matches must be false after the salt changed")
	}
	if _, err = keys.Seal(changed, 3, nil); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("seal with mismatching header: %v", err)
	}
}

func TestParseHeaderLimitsAndMalformedInput(t *testing.T) {
	_, header := newVault(t, "correct horse battery")
	check := func(name string, edit func(h *Header), want error) {
		t.Helper()
		h := header
		h.KDF.Salt = bytes.Clone(header.KDF.Salt)
		edit(&h)
		data, _ := json.Marshal(h)
		if _, err := ParseHeader(data); !errors.Is(err, want) {
			t.Errorf("%s: got %v want %v", name, err, want)
		}
	}
	check("valid", func(h *Header) {}, nil)
	check("memory over limit", func(h *Header) { h.KDF.MemoryKiB = maxMemory + 1 }, ErrUnsupported)
	check("time over limit", func(h *Header) { h.KDF.Time = maxTime + 1 }, ErrUnsupported)
	check("threads over limit", func(h *Header) { h.KDF.Threads = maxThreads + 1 }, ErrUnsupported)
	check("zero time", func(h *Header) { h.KDF.Time = 0 }, ErrUnsupported)
	check("memory below 8*p", func(h *Header) { h.KDF.MemoryKiB = 4 }, ErrUnsupported)
	check("short salt", func(h *Header) { h.KDF.Salt = h.KDF.Salt[:8] }, ErrUnsupported)
	check("kdf name", func(h *Header) { h.KDF.Name = "pbkdf2" }, ErrUnsupported)
	check("format", func(h *Header) { h.Format = "x" }, ErrUnsupported)
	check("version", func(h *Header) { h.V = 9 }, ErrUnsupported)
	check("vault id chars", func(h *Header) { h.VaultID = "a/b" }, ErrUnsupported)
	check("kcv length", func(h *Header) { h.KCV = h.KCV[:3] }, ErrUnsupported)
	for _, bad := range []string{"", "not json", "{", `{"format":`, "[]"} {
		if _, err := ParseHeader([]byte(bad)); err == nil {
			t.Errorf("ParseHeader(%q) accepted", bad)
		}
	}
}

func TestKeysMarshalRoundTrip(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	restored, err := UnmarshalKeys(keys.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Matches(header) || restored.VaultID() != keys.VaultID() {
		t.Fatal("restored keys do not match the header")
	}
	sealed, err := keys.Seal(header, 1, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := restored.Open(sealed); err != nil || string(got) != "hello" {
		t.Fatalf("restored open: %q %v", got, err)
	}
	if bytes.Contains(keys.Marshal(), []byte("correct horse")) {
		t.Fatal("key file must not contain the password")
	}
	for _, bad := range [][]byte{nil, []byte("{}"), []byte(`{"v":1}`), []byte("garbage")} {
		if _, err := UnmarshalKeys(bad); err == nil {
			t.Errorf("UnmarshalKeys(%q) accepted", bad)
		}
	}
}

func TestNonceIsNeverReused(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	seen := map[string]bool{}
	ciphertexts := map[string]bool{}
	for range 1000 {
		sealed, err := keys.Seal(header, 1, []byte("same payload"))
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		if err = json.Unmarshal(sealed, &env); err != nil {
			t.Fatal(err)
		}
		if seen[string(env.Nonce)] || ciphertexts[string(env.Ciphertext)] {
			t.Fatal("nonce or ciphertext repeated")
		}
		seen[string(env.Nonce)], ciphertexts[string(env.Ciphertext)] = true, true
	}
}

func TestCompressionBombIsRejected(t *testing.T) {
	keys, header := newVault(t, "correct horse battery")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zeros := make([]byte, 1<<20)
	for range maxPlain>>20 + 8 {
		if _, err := zw.Write(zeros); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sealed, err := keys.sealCompressed(header, 1, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = keys.Open(sealed); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("bomb: %v", err)
	}
	if _, err = keys.Seal(header, 1, make([]byte, maxPlain+1)); err == nil {
		t.Fatal("oversized plaintext accepted by Seal")
	}
}

// RFC 5869 Appendix A.1: confirms the standard library HKDF we rely on behaves
// correctly.
func TestHKDFRFC5869Vector(t *testing.T) {
	ikm, _ := hex.DecodeString("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")
	salt, _ := hex.DecodeString("000102030405060708090a0b0c")
	info, _ := hex.DecodeString("f0f1f2f3f4f5f6f7f8f9")
	okm, err := hkdf.Key(sha256.New, ikm, salt, string(info), 42)
	if err != nil {
		t.Fatal(err)
	}
	const want = "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865"
	if got := hex.EncodeToString(okm); got != want {
		t.Fatalf("hkdf: %s", got)
	}
}

// Known derivation results of the pre-rename format for a fixed password, salt
// and parameters (cross-checked with an independent Python argon2-cffi plus a
// hand-written HKDF). They guard against accidental changes to the info, salt or
// KDF usage, so backups created before the rename can still be unlocked.
func TestDeriveKnownAnswer(t *testing.T) {
	kdf := KDF{Name: kdfArgon2id, Time: 1, MemoryKiB: 64, Threads: 1, Salt: []byte("0123456789abcdef")}
	keys, err := derive(LegacyFormat, "correct horse battery", "vault-1", kdf)
	if err != nil {
		t.Fatal(err)
	}
	const wantEnc = "7208759fe1c707c4d21eab55b497fae15aa765322d7f633344b9ec1ed4316147"
	const wantKCV = "349ebdf1dc259a1837d6a0f9ca2fa7f7ea16aef38cd214dfb4fe183ef0a9ae9c"
	if got := hex.EncodeToString(keys.enc[:]); got != wantEnc {
		t.Errorf("enc = %s", got)
	}
	if got := hex.EncodeToString(keys.kcv[:]); got != wantKCV {
		t.Errorf("kcv = %s", got)
	}
	if keys.enc == keys.kcv {
		t.Fatal("sub keys must be separated")
	}
}

// Known derivation results of the current format. It differs from the pre-rename
// format only in the HKDF info (the derivation itself is covered by the
// cross-check above), so the subkeys differ; this also guards the info strings.
func TestDeriveKnownAnswerCurrentFormat(t *testing.T) {
	kdf := KDF{Name: kdfArgon2id, Time: 1, MemoryKiB: 64, Threads: 1, Salt: []byte("0123456789abcdef")}
	keys, err := derive(Format, "correct horse battery", "vault-1", kdf)
	if err != nil {
		t.Fatal(err)
	}
	const wantEnc = "8dd63e68346ca329f660b66a473a03f1e031a97d5c0ff1a356c197489a8ab1b0"
	const wantKCV = "f89531c6b35ae93bd0bdf588d1fb6a88ebb91fc4ae89cea581a98e430fd463bb"
	if got := hex.EncodeToString(keys.enc[:]); got != wantEnc {
		t.Errorf("enc = %s", got)
	}
	if got := hex.EncodeToString(keys.kcv[:]); got != wantKCV {
		t.Errorf("kcv = %s", got)
	}
	legacy, err := derive(LegacyFormat, "correct horse battery", "vault-1", kdf)
	if err != nil {
		t.Fatal(err)
	}
	if keys.enc == legacy.enc || keys.kcv == legacy.kcv {
		t.Fatal("formats must derive separate keys")
	}
}

// legacyVault builds a backup in the pre-rename format (sbcfg-vault) and its keys.
func legacyVault(t *testing.T, password string) (*Keys, Header) {
	t.Helper()
	fastKDF(t)
	kdf := KDF{Name: kdfArgon2id, Time: defaultTime, MemoryKiB: defaultMemoryKiB, Threads: defaultThreads, Salt: []byte("0123456789abcdef")}
	keys, err := derive(LegacyFormat, password, "legacy-vault", kdf)
	if err != nil {
		t.Fatal(err)
	}
	return keys, Header{Format: LegacyFormat, V: version, VaultID: "legacy-vault", KDF: kdf, KCV: bytes.Clone(keys.kcv[:])}
}

// Backups created before the rename can still be unlocked, read and written back,
// and stay in the original format after writing.
func TestLegacyVaultStillWorks(t *testing.T) {
	legacyKeys, header := legacyVault(t, "correct horse battery")
	sealed, err := legacyKeys.Seal(header, 3, []byte("old payload"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sealed), `"format":"sbcfg-vault"`) {
		t.Fatalf("legacy envelope: %s", sealed)
	}
	parsed, err := ParseHeader(sealed)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := Unlock("correct horse battery", parsed)
	if err != nil {
		t.Fatal(err)
	}
	if keys.Format() != LegacyFormat {
		t.Fatalf("format %q", keys.Format())
	}
	if _, err := Unlock("wrong password!", parsed); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	plain, h, err := keys.Open(sealed)
	if err != nil || string(plain) != "old payload" || h.Revision != 3 {
		t.Fatalf("open: %q %+v %v", plain, h, err)
	}
	resealed, err := keys.Seal(h, 4, []byte("new payload"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, h, err := legacyKeys.Open(resealed); err != nil || string(plain) != "new payload" || h.Format != LegacyFormat {
		t.Fatalf("reopen: %q %+v %v", plain, h, err)
	}
	// Re-creating with the same vaultId upgrades to the current format; the old
	// keys do not recognize the new backup.
	upgraded, upHeader, err := Create("correct horse battery", keys.VaultID())
	if err != nil {
		t.Fatal(err)
	}
	next, err := upgraded.Seal(upHeader, 5, plain)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Format() != Format || !strings.Contains(string(next), `"format":"kiko-vault"`) {
		t.Fatalf("upgraded envelope: %s", next)
	}
	if _, _, err := keys.Open(next); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("legacy keys on the upgraded vault: %v", err)
	}
}

// A key file written before the rename has no format field and is read as the
// pre-rename format.
func TestLegacyKeyFile(t *testing.T) {
	legacyKeys, header := legacyVault(t, "correct horse battery")
	var file map[string]any
	if err := json.Unmarshal(legacyKeys.Marshal(), &file); err != nil {
		t.Fatal(err)
	}
	delete(file, "format")
	old, _ := json.Marshal(file)
	restored, err := UnmarshalKeys(old)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Format() != LegacyFormat || !restored.Matches(header) {
		t.Fatalf("restored format %q", restored.Format())
	}
	keys, _ := newVault(t, "correct horse battery")
	if again, err := UnmarshalKeys(keys.Marshal()); err != nil || again.Format() != Format {
		t.Fatalf("current key file: %v", err)
	}
}

func TestCheckPasswordAndStrength(t *testing.T) {
	if err := CheckPassword("short"); err == nil || !strings.Contains(err.Error(), "8") {
		t.Fatalf("short password: %v", err)
	}
	if err := CheckPassword("长度刚好八个字符呀"); err != nil {
		t.Fatalf("8+ characters should pass (counted in characters, not bytes): %v", err)
	}
	if err := CheckPassword("12345678"); err != nil {
		t.Fatalf("length check only: %v", err)
	}
	prev := -1
	for _, pw := range []string{"short", "aaaaaaaaaaaa", "password", "abcdefgh1", "Abcdefgh1234", "Abcdefgh1234!xyz9"} {
		s := PasswordStrength(pw)
		if s < 0 || s > 4 {
			t.Fatalf("%q out of range: %d", pw, s)
		}
		_ = prev
	}
	if PasswordStrength("short") != 0 || PasswordStrength("password") != 0 {
		t.Fatal("weak passwords must score 0")
	}
	if PasswordStrength("aaaaaaaaaaaa") > 1 {
		t.Fatal("repeated characters must stay weak")
	}
	if PasswordStrength("Abcdefgh1234!xyz9") != 4 {
		t.Fatalf("strong password: %d", PasswordStrength("Abcdefgh1234!xyz9"))
	}
}

// BenchmarkCreateProductionParams reports the time of one derivation with
// production parameters: go test -run ^$ -bench Production ./syncvault/
func BenchmarkCreateProductionParams(b *testing.B) {
	for b.Loop() {
		start := time.Now()
		if _, _, err := Create("correct horse battery", ""); err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(time.Since(start).Milliseconds()), "ms/derive")
	}
}
