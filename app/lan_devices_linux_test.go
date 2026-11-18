//go:build linux

package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLANDevicesLinuxAPIWithRealNeighborReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dhcp.leases")
	if err := os.WriteFile(path, []byte("0 02:11:22:33:44:55 192.168.10.20 TV *\n0 02:11:22:33:44:66 192.168.10.21 Phone *\n"), 0600); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"lease_file": []string{path}}
	w := httptest.NewRecorder()
	listLANDevices(w, httptest.NewRequest("GET", "/api/v1/lan-devices?"+query.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("discovery API failed: %d %s", w.Code, w.Body.String())
	}
	var response lanDevicesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, device := range response.Devices {
		found[device.MAC] = device.Hostname
	}
	if found["02:11:22:33:44:55"] != "TV" || found["02:11:22:33:44:66"] != "Phone" {
		t.Fatalf("expected both clients: %+v", response)
	}
	paths := defaultLANLeaseFiles()
	if len(paths) == 0 || paths[0] != "/tmp/dhcp.leases" {
		t.Fatalf("expected OpenWrt lease path: %v", paths)
	}
	paths[0] = "changed"
	if defaultLANLeaseFiles()[0] != "/tmp/dhcp.leases" {
		t.Fatal("default lease paths must be independent copies")
	}
}
