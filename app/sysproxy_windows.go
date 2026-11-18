//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

var internetSetOption = windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")

type proxyRegistryValue struct {
	Present      bool   `json:"present"`
	StringValue  string `json:"stringValue,omitempty"`
	IntegerValue uint64 `json:"integerValue,omitempty"`
	Type         uint32 `json:"type,omitempty"`
}

type proxyRegistrySnapshot struct {
	Version int                           `json:"version"`
	Values  map[string]proxyRegistryValue `json:"values"`
}

var proxyRegistryNames = []string{"ProxyEnable", "ProxyServer", "ProxyOverride", "AutoConfigURL"}

func readProxySettings(key registry.Key) (proxyRegistrySnapshot, error) {
	values := make(map[string]proxyRegistryValue, 4)
	for _, name := range proxyRegistryNames {
		var value proxyRegistryValue
		if name == "ProxyEnable" {
			n, kind, err := key.GetIntegerValue(name)
			if err != nil && !errors.Is(err, registry.ErrNotExist) {
				return proxyRegistrySnapshot{}, err
			}
			value.Present, value.IntegerValue, value.Type = err == nil, n, kind
		} else {
			str, kind, err := key.GetStringValue(name)
			if err != nil && !errors.Is(err, registry.ErrNotExist) {
				return proxyRegistrySnapshot{}, err
			}
			value.Present, value.StringValue, value.Type = err == nil, str, kind
		}
		values[name] = value
	}
	return proxyRegistrySnapshot{Version: 1, Values: values}, nil
}

func loadProxySnapshot(path string) (proxyRegistrySnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return proxyRegistrySnapshot{}, err
	}
	var snapshot proxyRegistrySnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return proxyRegistrySnapshot{}, err
	}
	if snapshot.Version != 1 || len(snapshot.Values) != len(proxyRegistryNames) {
		return proxyRegistrySnapshot{}, fmt.Errorf("invalid system proxy restore file: %s", path)
	}
	for _, name := range proxyRegistryNames {
		if _, ok := snapshot.Values[name]; !ok {
			return proxyRegistrySnapshot{}, fmt.Errorf("missing %s in system proxy restore file: %s", name, path)
		}
	}
	return snapshot, nil
}

func notifyProxyChanged() {
	// INTERNET_OPTION_SETTINGS_CHANGED, INTERNET_OPTION_REFRESH
	_, _, _ = internetSetOption.Call(0, 39, 0, 0)
	_, _, _ = internetSetOption.Call(0, 37, 0, 0)
}

func setSystemProxy(a sysProxyApply, restorePath string) (err error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return setProxySettings(key, restorePath, a)
}

func setProxySettings(key registry.Key, restorePath string, a sysProxyApply) error {
	before, err := readProxySettings(key)
	if err != nil {
		return err
	}
	first := false
	if _, err = loadProxySnapshot(restorePath); errors.Is(err, os.ErrNotExist) {
		if err = atomicJSON(restorePath, before); err != nil {
			return err
		}
		first = true
	} else if err != nil {
		return err
	}
	if err = applyProxySettings(key, a); err == nil {
		return nil
	}
	if rollbackErr := writeProxySettings(key, before); rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("rollback system proxy: %w", rollbackErr))
	}
	if first {
		err = errors.Join(err, os.Remove(restorePath))
	}
	return err
}

func applyProxySettings(key registry.Key, a sysProxyApply) (err error) {
	if a.PAC != "" {
		if err = key.SetStringValue("AutoConfigURL", a.PAC); err != nil {
			return err
		}
		if err = key.SetDWordValue("ProxyEnable", 0); err != nil {
			return err
		}
	} else {
		target := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
		server := "http=" + target + ";https=" + target + ";socks=" + target
		if err = key.SetStringValue("ProxyServer", server); err != nil {
			return err
		}
		if err = key.SetStringValue("ProxyOverride", strings.Join(winBypass(a.Bypass), ";")); err != nil {
			return err
		}
		if err = key.SetDWordValue("ProxyEnable", 1); err != nil {
			return err
		}
		if err = key.DeleteValue("AutoConfigURL"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
	}
	notifyProxyChanged()
	return nil
}

func clearSystemProxy(restorePath string) error {
	if _, err := os.Stat(restorePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return restoreProxySettings(key, restorePath)
}

func recoverSystemProxy(restorePath string) error {
	return clearSystemProxy(restorePath)
}

func restoreProxySettings(key registry.Key, restorePath string) error {
	snapshot, err := loadProxySnapshot(restorePath)
	if err != nil {
		return err
	}
	if err = writeProxySettings(key, snapshot); err != nil {
		return err
	}
	return os.Remove(restorePath)
}

func writeProxySettings(key registry.Key, snapshot proxyRegistrySnapshot) error {
	for _, name := range []string{"ProxyServer", "ProxyOverride", "AutoConfigURL", "ProxyEnable"} {
		value := snapshot.Values[name]
		var err error
		if !value.Present {
			err = key.DeleteValue(name)
			if errors.Is(err, registry.ErrNotExist) {
				err = nil
			}
		} else if name == "ProxyEnable" {
			if value.Type == registry.QWORD {
				err = key.SetQWordValue(name, value.IntegerValue)
			} else {
				err = key.SetDWordValue(name, uint32(value.IntegerValue))
			}
		} else if value.Type == registry.EXPAND_SZ {
			err = key.SetExpandStringValue(name, value.StringValue)
		} else {
			err = key.SetStringValue(name, value.StringValue)
		}
		if err != nil {
			return err
		}
	}
	notifyProxyChanged()
	return nil
}

func systemProxyMatches(a sysProxyApply) bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer key.Close()
	if a.PAC != "" {
		url, _, err := key.GetStringValue("AutoConfigURL")
		return err == nil && url == a.PAC
	}
	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled != 1 {
		return false
	}
	target := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
	server, _, err := key.GetStringValue("ProxyServer")
	return err == nil && server == "http="+target+";https="+target+";socks="+target
}
