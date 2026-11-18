//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTunDNSAddresses(t *testing.T) {
	for _, test := range []struct {
		name    string
		inbound tunDNSConfig
		want    []string
	}{
		{
			name: "derive from TUN addresses",
			inbound: tunDNSConfig{
				Type: "tun", DNSMode: "hijack",
				Address: []string{"172.18.0.1/30", "fdfe:dcba:9876::1/126"},
			},
			want: []string{"172.18.0.2", "fdfe:dcba:9876::2"},
		},
		{
			name: "explicit DNS takes precedence",
			inbound: tunDNSConfig{
				Type: "tun", DNSMode: "native", Address: []string{"172.18.0.1/30"},
				DNSAddress: []string{"8.8.8.8", "8.8.8.8"},
			},
			want: []string{"8.8.8.8"},
		},
		{
			name: "first address in each family wins",
			inbound: tunDNSConfig{
				Type: "tun", Address: []string{
					"fdfe:dcba:9876::1/126", "172.18.0.1/30", "172.19.0.1/30",
				},
			},
			want: []string{"172.18.0.2", "fdfe:dcba:9876::2"},
		},
		{
			name: "disabled skips system DNS", inbound: tunDNSConfig{
				Type: "tun", DNSMode: "disabled", Address: []string{"172.18.0.1/30"},
			},
		},
		{
			name: "host-only address cannot derive DNS", inbound: tunDNSConfig{
				Type: "tun", DNSMode: "hijack", Address: []string{"172.18.0.1/32"},
			},
			want: []string{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tunDNSAddresses(test.inbound); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("addresses = %v, want %v", got, test.want)
			}
		})
	}
}

func fakeNetworksetup(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "networksetup"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

func TestNetworksetupCancellationBoundsCommandAndDescendants(t *testing.T) {
	fakeNetworksetup(t, "trap '' TERM INT\nsleep 30 &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := networksetupOutput(ctx, "-test")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > time.Second {
		t.Fatalf("networksetup cancellation: %v, elapsed=%v", err, time.Since(began))
	}
}

func TestDNSRestoreAttemptsAllServicesWithinCommandBudget(t *testing.T) {
	dir := fakeNetworksetup(t, "echo \"$2:$3\" >> \"$KIKO_DNS_RECORD\"\ncase \"$2\" in slow*) exec sleep 30;; esac\n")
	record := filepath.Join(dir, "record")
	t.Setenv("KIKO_DNS_RECORD", record)
	began := time.Now()
	err := restoreDNSServices(map[string][]string{"slow-a": nil, "slow-b": nil, "working": {"1.1.1.1"}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > 4*time.Second {
		t.Fatalf("restore command budgets: %v, elapsed=%v", err, time.Since(began))
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slow-a:Empty", "slow-b:Empty", "working:1.1.1.1"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing service %q in %q", want, data)
		}
	}
}

func TestFailedDNSApplyRetainsFailedRollbackForRetry(t *testing.T) {
	dir := fakeNetworksetup(t, `case "$1" in
-listallnetworkservices) printf 'Network services\nWi-Fi\n';;
-getdnsservers) echo 1.1.1.1;;
-setdnsservers) [ "$3" = 1.1.1.1 ] && [ -f "$KIKO_DNS_RECOVER" ];;
esac
`)
	recoverPath := filepath.Join(dir, "recover")
	t.Setenv("KIKO_DNS_RECOVER", recoverPath)
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"inbounds":[{"type":"tun","address":["172.19.0.1/30"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := newGRPCTestServer(t)
	s.cmd = &exec.Cmd{}
	s.applySystemDNS(configPath)
	if s.restoreSystemDNS == nil {
		t.Fatal("failed apply and rollback discarded original DNS")
	}
	if err := os.WriteFile(recoverPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.restoreSystemDNSConfig()
	if s.restoreSystemDNS != nil {
		t.Fatal("DNS restoration retry did not finish")
	}
}
