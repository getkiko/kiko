package main

import (
	"errors"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/route"
)

func macForTest(t *testing.T, value string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(value)
	if err != nil {
		t.Fatal(err)
	}
	return mac
}
func TestMergeLANDevices(t *testing.T) {
	mac := macForTest(t, "02:11:22:33:44:55")
	input := []lanObservation{
		{address: netip.MustParseAddr("192.168.1.20"), mac: mac, source: "neighbor"},
		{address: netip.MustParseAddr("fd00::20"), mac: mac, source: "neighbor"},
		{address: netip.MustParseAddr("192.168.1.20"), mac: mac, hostname: "TV", source: "dhcp"},
		{address: netip.MustParseAddr("fd00::30"), hostname: "Phone", source: "dhcp"},
		{address: netip.MustParseAddr("192.168.1.21"), mac: macForTest(t, "02:11:22:33:44:66"), hostname: "TV", source: "dhcp"},
		{address: netip.MustParseAddr("127.0.0.1"), mac: mac, source: "neighbor"},
		{address: netip.MustParseAddr("224.0.0.1"), mac: mac, source: "neighbor"},
		{address: netip.MustParseAddr("192.168.1.1"), mac: macForTest(t, "00:00:00:00:00:00"), source: "neighbor"},
		{address: netip.MustParseAddr("192.168.1.2"), mac: macForTest(t, "ff:ff:ff:ff:ff:ff"), source: "neighbor"},
	}
	devices := mergeLANDevices(input)
	if len(devices) != 3 {
		t.Fatalf("expected two MAC devices and a hostname-only device: %+v", devices)
	}
	var tv *lanDevice
	for index := range devices {
		if devices[index].MAC == mac.String() {
			tv = &devices[index]
		}
	}
	if tv == nil || tv.Hostname != "TV" || !reflect.DeepEqual(tv.Addresses, []string{"192.168.1.20", "fd00::20"}) || !reflect.DeepEqual(tv.Sources, []string{"dhcp", "neighbor"}) {
		t.Fatalf("unexpected merged TV: %+v", tv)
	}
}
func TestCollectLANDevicesLeaseAndFailureSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dhcp.leases")
	text := "0 02:11:22:33:44:55 192.168.1.20 TV *\n1 02:11:22:33:44:66 192.168.1.21 expired *\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	mac := macForTest(t, "02:11:22:33:44:55")
	neighbors := func() ([]lanObservation, error) {
		return []lanObservation{{address: netip.MustParseAddr("fd00::20"), mac: mac, source: "neighbor"}}, nil
	}
	devices, warnings, err := collectLANDevices(nil, []string{path}, neighbors, route.ReloadLeaseFiles)
	if err != nil || len(warnings) != 0 || len(devices) != 1 || devices[0].Hostname != "TV" || len(devices[0].Addresses) != 2 {
		t.Fatalf("unexpected result: %+v %v %v", devices, warnings, err)
	}
	fail := func() ([]lanObservation, error) { return nil, errors.New("neighbor permission denied") }
	devices, warnings, err = collectLANDevices([]string{path}, nil, fail, route.ReloadLeaseFiles)
	if err != nil || len(devices) != 1 || len(warnings) != 1 {
		t.Fatalf("expected DHCP fallback: %+v %v %v", devices, warnings, err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	devices, warnings, err = collectLANDevices([]string{missing}, nil, neighbors, route.ReloadLeaseFiles)
	if err != nil || len(devices) != 1 || len(warnings) != 1 {
		t.Fatalf("expected neighbor fallback: %+v %v %v", devices, warnings, err)
	}
	_, _, err = collectLANDevices([]string{missing}, nil, fail, route.ReloadLeaseFiles)
	if err == nil {
		t.Fatal("expected total failure")
	}
	_, warnings, err = collectLANDevices(nil, []string{missing}, neighbors, route.ReloadLeaseFiles)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("absent default path should be normal: %v %v", warnings, err)
	}
	_, warnings, err = collectLANDevices([]string{filepath.Dir(path)}, nil, neighbors, route.ReloadLeaseFiles)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("directory must yield a warning: %v %v", warnings, err)
	}
}
func TestLANDevicesCapabilityAndInvalidPaths(t *testing.T) {
	if !lanDevicesSupported() {
		w := httptest.NewRecorder()
		listLANDevices(w, httptest.NewRequest("GET", "/api/v1/lan-devices", nil))
		if w.Code != 501 {
			t.Fatalf("expected unsupported: %d", w.Code)
		}
		return
	}
	for _, query := range []string{"lease_file=relative", "lease_file=%00"} {
		w := httptest.NewRecorder()
		listLANDevices(w, httptest.NewRequest("GET", "/api/v1/lan-devices?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("expected invalid path: %s %d", query, w.Code)
		}
	}
}
func TestLeaseHostnameCaseAndHostnameOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dhcp.leases")
	lease := strings.Join([]string{"0 02:11:22:33:44:55 192.168.1.20 TV *", "0 1234 fd00::21 Phone *", "1 02:11:22:33:44:66 192.168.1.22 expired *"}, "\n")
	if err := os.WriteFile(path, []byte(lease), 0600); err != nil {
		t.Fatal(err)
	}
	devices, _, err := collectLANDevices([]string{path}, nil, func() ([]lanObservation, error) { return nil, nil }, route.ReloadLeaseFiles)
	if err != nil || len(devices) != 2 {
		t.Fatalf("unexpected leases at %v: %+v %v", time.Now(), devices, err)
	}
	if devices[0].MAC != "" || devices[0].Hostname != "Phone" || devices[1].Hostname != "TV" {
		t.Fatalf("hostname and identity should be preserved: %+v", devices)
	}
}

func TestMergeLANDevicesJoinsHostnameOnlyLeaseByAddress(t *testing.T) {
	mac := macForTest(t, "02:11:22:33:44:55")
	observations := []lanObservation{
		{address: netip.MustParseAddr("fd00::20"), hostname: "Phone", source: "dhcp"},
		{address: netip.MustParseAddr("fd00::20"), mac: mac, hostname: "Phone", source: "neighbor"},
		{address: netip.MustParseAddr("fd00::21"), hostname: "Phone", source: "dhcp"},
	}
	devices := mergeLANDevices(observations)
	if len(devices) != 2 {
		t.Fatalf("same-IP lease should join its neighbor while distinct-IP lease remains separate: %+v", devices)
	}
	var joined *lanDevice
	for index := range devices {
		if devices[index].MAC == mac.String() {
			joined = &devices[index]
		}
	}
	if joined == nil || joined.Hostname != "Phone" || !reflect.DeepEqual(joined.Addresses, []string{"fd00::20"}) || !reflect.DeepEqual(joined.Sources, []string{"dhcp", "neighbor"}) {
		t.Fatalf("unexpected merged device: %+v", joined)
	}
}
