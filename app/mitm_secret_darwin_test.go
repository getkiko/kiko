//go:build darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireKeychainTest skips unless KIKO_KEYCHAIN_TEST=1; keychain integration
// tests need an unlocked login keychain (a GUI session).
func requireKeychainTest(t *testing.T) {
	t.Helper()
	if os.Getenv("KIKO_KEYCHAIN_TEST") != "1" {
		t.Skip("set KIKO_KEYCHAIN_TEST=1 to use the login keychain")
	}
}

func TestNewSecretStoreUsesKeychain(t *testing.T) {
	migrating, ok := newSecretStore(t.TempDir()).(*migratingSecretStore)
	if !ok {
		t.Fatalf("store = %#v", migrating)
	}
	store, ok := migrating.current.(*keychainSecretStore)
	if !ok || store.service != "Kiko HTTPS Decryption" || store.account != "root-ca-key" || store.label != "Kiko Root CA Key" {
		t.Fatalf("current = %#v", store)
	}
	legacy, ok := migrating.legacy.(*keychainSecretStore)
	if !ok || legacy.service != "Poppy HTTPS Decryption" || legacy.account != "root-ca-key" {
		t.Fatalf("legacy = %#v", legacy)
	}
}

// A pre-rename keychain item is migrated to the new service name on read and the
// old item is deleted.
func TestKeychainMigratesLegacyItem(t *testing.T) {
	requireKeychainTest(t)
	account := fmt.Sprintf("test-%d", time.Now().UnixNano())
	legacy := &keychainSecretStore{service: "Poppy HTTPS Decryption Test", account: account, label: "Poppy Root CA Key Test"}
	current := &keychainSecretStore{service: "Kiko HTTPS Decryption Test", account: account, label: "Kiko Root CA Key Test"}
	t.Cleanup(func() { _ = legacy.Delete(); _ = current.Delete() })
	key := testSecretKey(0x63)
	if err := legacy.Save(key); err != nil {
		t.Fatal(err)
	}
	store := &migratingSecretStore{current: current, legacy: legacy}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("load = %x, %v", got, err)
	}
	if got, err := current.Load(); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("current = %x, %v", got, err)
	}
	if _, err := legacy.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("legacy item remains: %v", err)
	}
}

func TestKeychainSecretStoreRoundTrip(t *testing.T) {
	requireKeychainTest(t)
	store := &keychainSecretStore{
		service: "Kiko HTTPS Decryption Test",
		account: fmt.Sprintf("test-%d", time.Now().UnixNano()),
		label:   "Kiko Root CA Key Test",
	}
	t.Cleanup(func() { _ = store.Delete() })

	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load empty: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("delete empty: %v", err)
	}
	first := testSecretKey(0x61)
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("load = %x, %v", got, err)
	}
	// The generic password item's label shows up as attribute 0x00000007 in the
	// security output.
	out, err := exec.Command("security", "find-generic-password", "-s", store.service, "-a", store.account).CombinedOutput()
	if err != nil || !strings.Contains(string(out), `0x00000007 <blob>="`+store.label+`"`) || !strings.Contains(string(out), `"svce"<blob>="`+store.service+`"`) {
		t.Fatalf("security find-generic-password: %v\n%s", err, out)
	}
	t.Logf("security find-generic-password:\n%s", out)

	second := testSecretKey(0x62)
	if err := store.Save(second); err != nil {
		t.Fatalf("save replace: %v", err)
	}
	if got, err := store.Load(); err != nil || !bytes.Equal(got, second) {
		t.Fatalf("load after replace = %x, %v", got, err)
	}

	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, errSecretNotFound) {
		t.Fatalf("load deleted: %v", err)
	}
	if out, err := exec.Command("security", "find-generic-password", "-s", store.service, "-a", store.account).CombinedOutput(); err == nil {
		t.Fatalf("item still present:\n%s", out)
	}
}

func TestKeychainErrorsCarryOnlyStatus(t *testing.T) {
	err := osStatus("写入钥匙串", -25308)
	if code, ok := osStatusCode(err); !ok || code != -25308 || err.Error() != "写入钥匙串失败（OSStatus -25308）" {
		t.Fatalf("err = %v", err)
	}
}
