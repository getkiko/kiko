//go:build windows

package main

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestWindowsProxyRestoresOriginalRegistryValues(t *testing.T) {
	for _, original := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "existing"}[original], func(t *testing.T) {
			name := `Software\Kiko-Tests\` + rand.Text()
			key, _, err := registry.CreateKey(registry.CURRENT_USER, name, registry.ALL_ACCESS)
			if err != nil {
				t.Fatal(err)
			}
			defer registry.DeleteKey(registry.CURRENT_USER, name)
			defer key.Close()
			restorePath := filepath.Join(t.TempDir(), "proxy-restore.json")
			if original {
				for field, value := range map[string]string{"ProxyServer": "old:8080", "ProxyOverride": "*.example", "AutoConfigURL": "https://example/pac"} {
					if err = key.SetStringValue(field, value); err != nil {
						t.Fatal(err)
					}
				}
				if err = key.SetDWordValue("ProxyEnable", 1); err != nil {
					t.Fatal(err)
				}
			}
			if err = setProxySettings(key, restorePath, sysProxyApply{Host: "127.0.0.1", Port: 2080}); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(restorePath); err != nil {
				t.Fatal("restore snapshot was not persisted:", err)
			}
			// Reapplying and switching to PAC must retain the original snapshot across restarts.
			if err = setProxySettings(key, restorePath, sysProxyApply{PAC: "http://127.0.0.1/pac"}); err != nil {
				t.Fatal(err)
			}
			if err = restoreProxySettings(key, restorePath); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(restorePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore snapshot remains: %v", err)
			}
			for field, want := range map[string]string{"ProxyServer": "old:8080", "ProxyOverride": "*.example", "AutoConfigURL": "https://example/pac"} {
				got, _, err := key.GetStringValue(field)
				if original && (err != nil || got != want) {
					t.Fatalf("%s=%q err=%v", field, got, err)
				}
				if !original && !errors.Is(err, registry.ErrNotExist) {
					t.Fatalf("%s remains: %v", field, err)
				}
			}
			got, _, err := key.GetIntegerValue("ProxyEnable")
			if original && (err != nil || got != 1) {
				t.Fatalf("ProxyEnable=%d err=%v", got, err)
			}
			if !original && !errors.Is(err, registry.ErrNotExist) {
				t.Fatalf("ProxyEnable remains: %v", err)
			}
		})
	}
}

func TestWindowsProxyFailedReapplyPreservesActiveProxy(t *testing.T) {
	name := `Software\Kiko-Tests\` + rand.Text()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, name, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.DeleteKey(registry.CURRENT_USER, name)
	defer key.Close()
	restorePath := filepath.Join(t.TempDir(), "proxy-restore.json")
	active := sysProxyApply{Host: "127.0.0.1", Port: 2080}
	if err = setProxySettings(key, restorePath, active); err != nil {
		t.Fatal(err)
	}
	if err = setProxySettings(key, restorePath, sysProxyApply{Host: "127.0.0.1", Port: 3080, Bypass: []string{"invalid\x00value"}}); err == nil {
		t.Fatal("expected partial registry write to fail")
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil || server != "http=127.0.0.1:2080;https=127.0.0.1:2080;socks=127.0.0.1:2080" {
		t.Fatalf("active proxy changed after failed reapply: %q, %v", server, err)
	}
	if _, err = os.Stat(restorePath); err != nil {
		t.Fatal("original snapshot was lost:", err)
	}
	if err = restoreProxySettings(key, restorePath); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsProxyFailedFirstApplyRemovesSnapshot(t *testing.T) {
	name := `Software\Kiko-Tests\` + rand.Text()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, name, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.DeleteKey(registry.CURRENT_USER, name)
	defer key.Close()
	restorePath := filepath.Join(t.TempDir(), "proxy-restore.json")
	if err = setProxySettings(key, restorePath, sysProxyApply{Host: "127.0.0.1", Port: 3080, Bypass: []string{"invalid\x00value"}}); err == nil {
		t.Fatal("expected partial registry write to fail")
	}
	if _, err = os.Stat(restorePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed first apply retained snapshot: %v", err)
	}
	if _, _, err = key.GetStringValue("ProxyServer"); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("failed first apply retained proxy server: %v", err)
	}
}

func TestWindowsProxyInvalidRestoreFileBlocksRegistryWrites(t *testing.T) {
	name := `Software\Kiko-Tests\` + rand.Text()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, name, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.DeleteKey(registry.CURRENT_USER, name)
	defer key.Close()
	restorePath := filepath.Join(t.TempDir(), "proxy-restore.json")
	if err = os.WriteFile(restorePath, []byte(`{"version":1,"values":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = setProxySettings(key, restorePath, sysProxyApply{Host: "127.0.0.1", Port: 2080}); err == nil {
		t.Fatal("invalid restore file was accepted")
	}
	if _, _, err = key.GetStringValue("ProxyServer"); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("registry was modified despite invalid restore file: %v", err)
	}
}
