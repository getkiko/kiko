package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGsettings stands in for gsettings over a map of schema-key values.
func fakeGsettings(t *testing.T, values map[string]string) *[]string {
	t.Helper()
	var sets []string
	old := gsettingsRun
	t.Cleanup(func() { gsettingsRun = old })
	gsettingsRun = func(args ...string) ([]byte, error) {
		switch {
		case len(args) == 3 && args[0] == "get":
			v, ok := values[args[1]+" "+args[2]]
			if !ok {
				return nil, errors.New("no key " + args[2])
			}
			return []byte(v + "\n"), nil
		case len(args) == 4 && args[0] == "set":
			if values["fail"] == args[1]+" "+args[2] {
				delete(values, "fail")
				return nil, errors.New("refused")
			}
			values[args[1]+" "+args[2]] = args[3]
			sets = append(sets, args[2]+"="+args[3])
			return nil, nil
		}
		return nil, errors.New("unexpected " + strings.Join(args, " "))
	}
	return &sets
}

func gnomeDefaults() map[string]string {
	values := map[string]string{
		gnomeProxySchema + " mode": "'none'", gnomeProxySchema + " autoconfig-url": "''",
		gnomeProxySchema + " ignore-hosts": "['localhost', '127.0.0.0/8', '::1']",
	}
	for _, child := range []string{"http", "https", "socks"} {
		values[gnomeProxySchema+"."+child+" host"] = "''"
		values[gnomeProxySchema+"."+child+" port"] = "0"
	}
	return values
}

func TestGsettingsProxy(t *testing.T) {
	values := gnomeDefaults()
	sets := fakeGsettings(t, values)
	restore := filepath.Join(t.TempDir(), "system-proxy-restore.json")
	apply := sysProxyApply{Host: "127.0.0.1", Port: 7890, Bypass: []string{"localhost", "<local>", "*.local", "it's"}}
	if gsettingsProxyMatches(apply) {
		t.Fatal("matches before applying")
	}
	if err := setGsettingsProxy(apply, restore); err != nil {
		t.Fatal(err)
	}
	if got := (*sets)[len(*sets)-1]; got != "mode='manual'" {
		t.Errorf("mode set last: %q", *sets)
	}
	if values[gnomeProxySchema+".socks host"] != "'127.0.0.1'" || values[gnomeProxySchema+".https port"] != "7890" {
		t.Errorf("values %v", values)
	}
	if got := values[gnomeProxySchema+" ignore-hosts"]; got != `['localhost', '*.local', 'it\'s']` {
		t.Errorf("ignore-hosts %s", got)
	}
	if !gsettingsProxyMatches(apply) {
		t.Error("applied proxy does not match")
	}
	// Applying again keeps the first restore point.
	if err := setGsettingsProxy(sysProxyApply{PAC: "http://127.0.0.1:9099/pac"}, restore); err != nil {
		t.Fatal(err)
	}
	if values[gnomeProxySchema+" mode"] != "'auto'" || !gsettingsProxyMatches(sysProxyApply{PAC: "http://127.0.0.1:9099/pac"}) {
		t.Errorf("pac %v", values)
	}
	if err := clearGsettingsProxy(restore); err != nil {
		t.Fatal(err)
	}
	for key, want := range gnomeDefaults() {
		if values[key] != want {
			t.Errorf("restored %s = %s, want %s", key, values[key], want)
		}
	}
	if _, err := os.Stat(restore); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("restore file kept: %v", err)
	}
	if err := clearGsettingsProxy(restore); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
}

// A value refused once rolls back what applying changed and the new
// restore point.
func TestGsettingsProxyRollback(t *testing.T) {
	values := gnomeDefaults()
	values["fail"] = gnomeProxySchema + " ignore-hosts"
	fakeGsettings(t, values)
	restore := filepath.Join(t.TempDir(), "restore.json")
	if err := setGsettingsProxy(sysProxyApply{Host: "10.0.0.1", Port: 1080}, restore); err == nil {
		t.Fatal("applied past a refused key")
	}
	if values[gnomeProxySchema+".http host"] != "''" || values[gnomeProxySchema+" mode"] != "'none'" {
		t.Errorf("not rolled back: %v", values)
	}
	if _, err := os.Stat(restore); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("restore file kept: %v", err)
	}
}
