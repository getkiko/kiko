package lanscan

import (
	_ "embed"
	"net"
	"strings"
	"sync"
)

// oui.txt comes from the IEEE Registration Authority MA-L registry
// (https://standards-oui.ieee.org/oui/oui.txt), retrieved 2026-11-05. Only
// common vendor prefixes are kept; the format is described in its header.
//
//go:embed oui.txt
var ouiData string

// Vendor kinds carried by the OUI table.
const (
	kindApple   = "apple"
	kindRouter  = "router"
	kindPrinter = "printer"
	kindPhone   = "phone"
	kindPC      = "pc"
	kindIoT     = "iot"
	kindAny     = "any" // mixed product lines; not used to infer the device type
)

type vendorInfo struct{ Name, Kind string }

var (
	ouiOnce  sync.Once
	ouiTable map[string]vendorInfo
)

func loadOUI() {
	ouiTable = make(map[string]vendorInfo, 12000)
	for _, line := range strings.Split(ouiData, "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		info := vendorInfo{Name: parts[1], Kind: parts[0]}
		for _, prefix := range strings.Split(parts[2], ",") {
			if len(prefix) == 6 {
				ouiTable[prefix] = info
			}
		}
	}
}

// LookupVendor returns the vendor registered for the MAC's OUI. Locally
// administered (random) addresses have no registered vendor.
func LookupVendor(mac net.HardwareAddr) (name, kind string) {
	if len(mac) < 3 || IsRandomMAC(mac) {
		return "", ""
	}
	ouiOnce.Do(loadOUI)
	info := ouiTable[strings.ToLower(hex3(mac))]
	return info.Name, info.Kind
}

func hex3(mac net.HardwareAddr) string {
	const digits = "0123456789abcdef"
	var b [6]byte
	for i := 0; i < 3; i++ {
		b[i*2], b[i*2+1] = digits[mac[i]>>4], digits[mac[i]&15]
	}
	return string(b[:])
}

// IsRandomMAC reports a locally administered address (bit 1 of the first
// byte: second hex digit 2, 6, A or E), as used by private Wi-Fi addresses.
func IsRandomMAC(mac net.HardwareAddr) bool { return len(mac) > 0 && mac[0]&2 != 0 }
