package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Linux desktops keep the system proxy in GSettings (GNOME and the
// desktops sharing its schema): org.gnome.system.proxy and its http,
// https and socks children. Applying saves the values it replaces to the
// restore file, as on Windows, and clearing writes them back.

const gnomeProxySchema = "org.gnome.system.proxy"

// gsettingsKeys are the keys applying touches, as schema and key.
var gsettingsKeys = [][2]string{
	{gnomeProxySchema, "mode"}, {gnomeProxySchema, "autoconfig-url"}, {gnomeProxySchema, "ignore-hosts"},
	{gnomeProxySchema + ".http", "host"}, {gnomeProxySchema + ".http", "port"},
	{gnomeProxySchema + ".https", "host"}, {gnomeProxySchema + ".https", "port"},
	{gnomeProxySchema + ".socks", "host"}, {gnomeProxySchema + ".socks", "port"},
}

// gsettingsRun runs gsettings with args.
var gsettingsRun = func(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("gsettings %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// gsettingsAvailable reports a desktop session with the GNOME proxy schema.
var gsettingsAvailable = sync.OnceValue(func() bool {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return false
	}
	_, err := gsettingsRun("list-keys", gnomeProxySchema)
	return err == nil
})

type gsettingsSnapshot struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

func gsettingsKey(schema, key string) string { return schema + " " + key }

func gsettingsGet(schema, key string) (string, error) {
	out, err := gsettingsRun("get", schema, key)
	return strings.TrimSpace(string(out)), err
}

func gsettingsSet(schema, key, value string) error {
	_, err := gsettingsRun("set", schema, key, value)
	return err
}

// gvariantString is s as a GVariant string literal.
func gvariantString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func gvariantStrings(list []string) string {
	if len(list) == 0 {
		return "@as []"
	}
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = gvariantString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// gnomeBypass drops Windows' <local>, which GNOME does not know.
func gnomeBypass(bypass []string) []string {
	out := make([]string, 0, len(bypass))
	for _, entry := range bypass {
		if entry != "<local>" {
			out = append(out, entry)
		}
	}
	return out
}

func readGsettingsSnapshot() (gsettingsSnapshot, error) {
	snapshot := gsettingsSnapshot{Version: 1, Values: map[string]string{}}
	for _, k := range gsettingsKeys {
		value, err := gsettingsGet(k[0], k[1])
		if err != nil {
			return gsettingsSnapshot{}, err
		}
		snapshot.Values[gsettingsKey(k[0], k[1])] = value
	}
	return snapshot, nil
}

func loadGsettingsSnapshot(path string) (gsettingsSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return gsettingsSnapshot{}, err
	}
	var snapshot gsettingsSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return gsettingsSnapshot{}, err
	}
	if snapshot.Version != 1 {
		return gsettingsSnapshot{}, fmt.Errorf("invalid system proxy restore file: %s", path)
	}
	for _, k := range gsettingsKeys {
		if _, ok := snapshot.Values[gsettingsKey(k[0], k[1])]; !ok {
			return gsettingsSnapshot{}, fmt.Errorf("missing %s in system proxy restore file: %s", gsettingsKey(k[0], k[1]), path)
		}
	}
	return snapshot, nil
}

// writeGsettingsSnapshot writes snapshot's values, the mode last so that
// the proxy turns on or off with its values in place.
func writeGsettingsSnapshot(snapshot gsettingsSnapshot) error {
	for _, k := range gsettingsKeys[1:] {
		if err := gsettingsSet(k[0], k[1], snapshot.Values[gsettingsKey(k[0], k[1])]); err != nil {
			return err
		}
	}
	return gsettingsSet(gnomeProxySchema, "mode", snapshot.Values[gsettingsKey(gnomeProxySchema, "mode")])
}

func applyGsettingsProxy(a sysProxyApply) error {
	if a.PAC != "" {
		if err := gsettingsSet(gnomeProxySchema, "autoconfig-url", gvariantString(a.PAC)); err != nil {
			return err
		}
		return gsettingsSet(gnomeProxySchema, "mode", "'auto'")
	}
	port := strconv.Itoa(a.Port)
	for _, child := range []string{"http", "https", "socks"} {
		if err := gsettingsSet(gnomeProxySchema+"."+child, "host", gvariantString(a.Host)); err != nil {
			return err
		}
		if err := gsettingsSet(gnomeProxySchema+"."+child, "port", port); err != nil {
			return err
		}
	}
	if err := gsettingsSet(gnomeProxySchema, "ignore-hosts", gvariantStrings(gnomeBypass(a.Bypass))); err != nil {
		return err
	}
	return gsettingsSet(gnomeProxySchema, "mode", "'manual'")
}

func setGsettingsProxy(a sysProxyApply, restorePath string) error {
	before, err := readGsettingsSnapshot()
	if err != nil {
		return err
	}
	first := false
	if _, err = loadGsettingsSnapshot(restorePath); errors.Is(err, os.ErrNotExist) {
		if err = atomicJSON(restorePath, before); err != nil {
			return err
		}
		first = true
	} else if err != nil {
		return err
	}
	if err = applyGsettingsProxy(a); err == nil {
		return nil
	}
	if rollbackErr := writeGsettingsSnapshot(before); rollbackErr != nil {
		return errors.Join(err, fmt.Errorf("rollback system proxy: %w", rollbackErr))
	}
	if first {
		err = errors.Join(err, os.Remove(restorePath))
	}
	return err
}

func clearGsettingsProxy(restorePath string) error {
	snapshot, err := loadGsettingsSnapshot(restorePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = writeGsettingsSnapshot(snapshot); err != nil {
		return err
	}
	return os.Remove(restorePath)
}

func gsettingsProxyMatches(a sysProxyApply) bool {
	mode, err := gsettingsGet(gnomeProxySchema, "mode")
	if err != nil {
		return true
	}
	if a.PAC != "" {
		url, err := gsettingsGet(gnomeProxySchema, "autoconfig-url")
		return mode == "'auto'" && err == nil && url == gvariantString(a.PAC)
	}
	if mode != "'manual'" {
		return false
	}
	port := strconv.Itoa(a.Port)
	for _, child := range []string{"http", "https", "socks"} {
		host, err1 := gsettingsGet(gnomeProxySchema+"."+child, "host")
		got, err2 := gsettingsGet(gnomeProxySchema+"."+child, "port")
		if err1 != nil || err2 != nil || host != gvariantString(a.Host) || got != port {
			return false
		}
	}
	return true
}
