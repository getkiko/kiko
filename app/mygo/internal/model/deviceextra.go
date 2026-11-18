package model

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// This file holds the optional device fields that never reach the emitted
// configuration: the MAC address, the user's picture and the addresses
// found by a LAN scan that wait for the user's confirmation. It offers
// their validation, quota and the MAC-based merging of scan results.

// Device picture limits.
const (
	// DeviceImageMaxBytes bounds one picture (decoded bytes). The picture
	// is a cropped avatar compressed to about 20 KB; 24 KB leaves room.
	DeviceImageMaxBytes = 24 * 1024
	// DeviceImagesMaxBytes bounds all pictures of a document (decoded
	// bytes). The document is saved, synced and shared as one JSON value,
	// and a shared configuration may not exceed 2 MB (profiles.ShareMaxBytes);
	// 512 KB of pictures is about 700 KB of base64, which leaves most of
	// that budget to the configuration and is still 21 full-size pictures.
	DeviceImagesMaxBytes = 512 * 1024
)

// Device picture errors; the texts are shown to the user.
var (
	ErrDeviceImageFormat = errors.New("图片格式不支持")
	ErrDeviceImageSize   = errors.New("图片过大")
	ErrDeviceImageQuota  = errors.New("设备图片总量已达上限")
)

var imagePrefixes = []struct {
	prefix string
	magic  func(b []byte) bool
}{
	{"data:image/jpeg;base64,", func(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }},
	{"data:image/png;base64,", func(b []byte) bool { return len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" }},
	{"data:image/webp;base64,", func(b []byte) bool { return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" }},
}

// decodeDeviceImage returns the bytes of a picture data URL.
func decodeDeviceImage(image string) ([]byte, error) {
	for _, f := range imagePrefixes {
		if !strings.HasPrefix(image, f.prefix) {
			continue
		}
		payload := image[len(f.prefix):]
		// Reject before decoding what is certainly too large.
		if len(payload) > DeviceImageMaxBytes*4/3+4 {
			return nil, ErrDeviceImageSize
		}
		raw, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || !f.magic(raw) {
			return nil, ErrDeviceImageFormat
		}
		if len(raw) > DeviceImageMaxBytes {
			return nil, ErrDeviceImageSize
		}
		return raw, nil
	}
	return nil, ErrDeviceImageFormat
}

// ValidDeviceImage checks one picture: a jpeg, png or webp data URL of at
// most DeviceImageMaxBytes. The empty picture (none) is valid.
func ValidDeviceImage(image string) error {
	if image == "" {
		return nil
	}
	_, err := decodeDeviceImage(image)
	return err
}

// imageSize is the decoded size of a picture data URL, 0 when it has none
// or is malformed.
func imageSize(image string) int {
	i := strings.IndexByte(image, ',')
	if i < 0 {
		return 0
	}
	payload := image[i+1:]
	n := len(payload) / 4 * 3
	if strings.HasSuffix(payload, "==") {
		n -= 2
	} else if strings.HasSuffix(payload, "=") {
		n--
	}
	return max(n, 0)
}

// DeviceImageBytes is the decoded size of all device pictures of doc.
func DeviceImageBytes(doc *Doc) int {
	total := 0
	for _, d := range doc.Route.Devices {
		total += imageSize(d.Image)
	}
	return total
}

// CheckDeviceImagesTotal reports ErrDeviceImageQuota when the pictures of
// doc together exceed DeviceImagesMaxBytes. Loading a document keeps every
// valid picture; the import and share paths refuse an oversized one with
// this error instead of dropping pictures.
func CheckDeviceImagesTotal(doc *Doc) error {
	if doc == nil {
		return nil
	}
	if total := DeviceImageBytes(doc); total > DeviceImagesMaxBytes {
		return fmt.Errorf("%w（%d / %d KB）", ErrDeviceImageQuota, total/1024, DeviceImagesMaxBytes/1024)
	}
	return nil
}

// CheckDeviceImage reports why image cannot be set as the picture of the
// device: invalid, over the single limit, or over the total limit once it
// replaces the device's current picture. The empty picture (clearing)
// always passes. Unknown devices are checked as new ones.
func CheckDeviceImage(doc *Doc, deviceID, image string) error {
	if image == "" {
		return nil
	}
	raw, err := decodeDeviceImage(image)
	if err != nil {
		return err
	}
	total := len(raw)
	for _, d := range doc.Route.Devices {
		if d.ID != deviceID {
			total += imageSize(d.Image)
		}
	}
	if total > DeviceImagesMaxBytes {
		return fmt.Errorf("%w（%d / %d KB）", ErrDeviceImageQuota, total/1024, DeviceImagesMaxBytes/1024)
	}
	return nil
}

// NormalizeDeviceMAC (6-byte MACs only; NormalizeMAC in lan.go also accepts EUI-64) returns mac as lowercase colon-separated hex ("aa:bb:cc:dd:ee:ff"),
// accepting colon, dash and dot (aabb.ccdd.eeff) forms and bare hex.
// ok is false for anything else.
func NormalizeDeviceMAC(mac string) (string, bool) {
	mac = strings.ToLower(strings.TrimSpace(mac))
	var hex string
	switch {
	case strings.Count(mac, ":") == 5:
		hex = strings.ReplaceAll(mac, ":", "")
	case strings.Count(mac, "-") == 5:
		hex = strings.ReplaceAll(mac, "-", "")
	case strings.Count(mac, ".") == 2:
		hex = strings.ReplaceAll(mac, ".", "")
	default:
		hex = mac
	}
	if len(hex) != 12 {
		return "", false
	}
	for _, c := range hex {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	// Segments must have two digits each in the separated forms.
	if strings.ContainsAny(mac, ":-") {
		sep := ":"
		if strings.Contains(mac, "-") {
			sep = "-"
		}
		for _, part := range strings.Split(mac, sep) {
			if len(part) != 2 {
				return "", false
			}
		}
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	out := b.String()
	if out == "00:00:00:00:00:00" || out == "ff:ff:ff:ff:ff:ff" {
		return "", false
	}
	return out, true
}

// IsRandomMAC reports whether mac is locally administered (the second hex
// digit is 2, 6, a or e), as the private addresses of phones are. Invalid
// addresses are not random.
func IsRandomMAC(mac string) bool {
	norm, ok := NormalizeDeviceMAC(mac)
	if !ok {
		return false
	}
	switch norm[1] {
	case '2', '6', 'a', 'e':
		return true
	}
	return false
}

// SanitizeDeviceExtras normalizes the optional fields of the devices of a
// loaded document: MAC addresses are normalized or dropped, invalid or
// oversized pictures dropped, pending addresses reduced to valid, distinct
// ones the device does not use yet. A MAC already held by an earlier
// device is dropped from the later one, so MatchDeviceByMAC is unique.
// Pictures are dropped only when malformed or over the single limit; the
// total limit is never applied here, as silently dropping pictures on a
// load or sync merge would lose the user's data. CheckDeviceImage guards
// the UI and CheckDeviceImagesTotal the import and share paths.
// doc is returned itself when nothing changes, else a copy.
func SanitizeDeviceExtras(doc *Doc) *Doc {
	var devices []*RouteDevice
	seenMAC := map[string]bool{}
	for i, d := range doc.Route.Devices {
		c := *d
		c.MAC = ""
		if mac, ok := NormalizeDeviceMAC(d.MAC); ok && !seenMAC[mac] {
			c.MAC = mac
			seenMAC[mac] = true
		}
		if ValidDeviceImage(d.Image) != nil {
			c.Image = ""
		}
		if !ValidDeviceImageKey(d.ImageKey) {
			c.ImageKey = ""
		}
		c.PendingAddresses = nil
		for _, a := range d.PendingAddresses {
			if ValidNetwork(a) && !slices.Contains(c.PendingAddresses, a) && !slices.Contains(d.Addresses, a) {
				c.PendingAddresses = append(c.PendingAddresses, a)
			}
		}
		c.PendingFrom = nil
		for _, a := range c.PendingAddresses {
			if from, ok := d.PendingFrom[a]; ok && slices.Contains(d.Addresses, from) && isHostNetwork(from) && sameFamily(a, from) {
				if c.PendingFrom == nil {
					c.PendingFrom = map[string]string{}
				}
				c.PendingFrom[a] = from
			}
		}
		if c.MAC == d.MAC && c.Image == d.Image && c.ImageKey == d.ImageKey && slices.Equal(c.PendingAddresses, d.PendingAddresses) && maps.Equal(c.PendingFrom, d.PendingFrom) {
			continue
		}
		if devices == nil {
			devices = slices.Clone(doc.Route.Devices)
		}
		devices[i] = &c
	}
	if devices == nil {
		return doc
	}
	next := *doc
	next.Route.Devices = devices
	return &next
}

// DeviceImageKeys are the built-in device pictures, the keys of package
// deviceimg (a test there keeps both lists equal).
var DeviceImageKeys = []string{"iphone", "ipad", "watch", "mac", "appletv", "homepod", "airpods", "vision", "pc", "router"}

// ValidDeviceImageKey reports whether key is empty or a built-in picture.
func ValidDeviceImageKey(key string) bool { return key == "" || slices.Contains(DeviceImageKeys, key) }

// ScannedHost is one host a LAN scan found.
type ScannedHost struct {
	IP   string
	MAC  string
	Name string
}

// Scan states of a host relative to the configured devices.
const (
	// ScanNew: no device has the address or the MAC.
	ScanNew = "new"
	// ScanKnown: a device has the address, or the MAC at an address it
	// already uses.
	ScanKnown = "known"
	// ScanMoved: a device has the MAC at an address it does not use; the
	// new address is pending.
	ScanMoved = "moved"
	// ScanConflict: a device has the address but a different MAC; the host
	// is not the device. It is neither known nor learned, and the UI offers
	// it as a candidate marked as conflicting.
	ScanConflict = "conflict"
)

// ScanMatch is the outcome for one scanned host.
type ScanMatch struct {
	Host     ScannedHost
	State    string
	DeviceID string // the matching device; empty for ScanNew
}

// DeviceHasAddress reports whether ip lies in one of the device's
// addresses.
func DeviceHasAddress(d *RouteDevice, ip string) bool {
	return slices.ContainsFunc(d.Addresses, func(n string) bool { return AddressInNetwork(ip, n) })
}

// deviceHasHostAddress reports whether the device has ip as a single-host
// address (not merely inside a wider network).
func deviceHasHostAddress(d *RouteDevice, ip string) bool {
	return slices.ContainsFunc(d.Addresses, func(n string) bool { return isHostNetwork(n) && AddressInNetwork(ip, n) })
}

// ScanConflicts reports whether the device with the host's IP holds a
// different MAC, so the host is not that device.
func ScanConflicts(doc *Doc, h ScannedHost) bool {
	mac, ok := NormalizeDeviceMAC(h.MAC)
	if !ok {
		return false
	}
	for _, d := range doc.Route.Devices {
		if DeviceHasAddress(d, h.IP) {
			return deviceHasHostAddress(d, h.IP) && d.MAC != "" && d.MAC != mac
		}
	}
	return false
}

// ScannedKnown reports whether a scanned host is already added as a device:
// DeviceForScanned finds one and the host does not conflict with it.
func ScannedKnown(doc *Doc, h ScannedHost) bool {
	return DeviceForScanned(doc, h) != nil && !ScanConflicts(doc, h)
}

// MatchDeviceByMAC returns the device with the given MAC, nil when none.
func MatchDeviceByMAC(doc *Doc, mac string) *RouteDevice {
	norm, ok := NormalizeDeviceMAC(mac)
	if !ok {
		return nil
	}
	for _, d := range doc.Route.Devices {
		if d.MAC == norm {
			return d
		}
	}
	return nil
}

// DeviceForScanned finds the device a scanned host is already added as:
// the one whose addresses contain its IP, else the one with its MAC.
func DeviceForScanned(doc *Doc, h ScannedHost) *RouteDevice {
	for _, d := range doc.Route.Devices {
		if DeviceHasAddress(d, h.IP) {
			return d
		}
	}
	return MatchDeviceByMAC(doc, h.MAC)
}

// HostAddress is the device address form of a host IP ("192.168.1.8/32").
func HostAddress(ip string) (string, bool) {
	n, ok := ParseNetwork(ip)
	if !ok {
		return "", false
	}
	if !strings.Contains(ip, "/") {
		return fmt.Sprintf("%s/%d", strings.TrimSpace(ip), len(n.Bytes)*8), true
	}
	return strings.TrimSpace(ip), true
}

// MergeScanned relates scan results to the devices and returns the
// document with what the scan teaches:
//   - IP in a device's addresses: known; a single-host device without a MAC
//     learns the host's MAC unless it is a random one (those change). A
//     range device (192.168.1.0/24) learns nothing, as the host is only one
//     of its members. A single-host device holding another MAC is a
//     conflict: not known, nothing learned.
//   - MAC of a device at an IP it does not use: moved; the new address
//     becomes a pending address of the device. Addresses and rules are not
//     touched until ConfirmPendingAddresses.
//   - anything else: new.
//
// doc is returned itself when nothing changes. The results follow the
// order of scanned.
func MergeScanned(doc *Doc, scanned []ScannedHost) (*Doc, []ScanMatch) {
	out := make([]ScanMatch, 0, len(scanned))
	next := doc
	for _, h := range scanned {
		if mac, ok := NormalizeDeviceMAC(h.MAC); ok {
			h.MAC = mac
		} else {
			h.MAC = ""
		}
		var byIP *RouteDevice
		for _, d := range next.Route.Devices {
			if DeviceHasAddress(d, h.IP) {
				byIP = d
				break
			}
		}
		if byIP != nil {
			single := deviceHasHostAddress(byIP, h.IP)
			if single && byIP.MAC != "" && h.MAC != "" && byIP.MAC != h.MAC {
				out = append(out, ScanMatch{Host: h, State: ScanConflict, DeviceID: byIP.ID})
				continue
			}
			out = append(out, ScanMatch{Host: h, State: ScanKnown, DeviceID: byIP.ID})
			if single && len(byIP.PendingAddresses) > 0 {
				// Back at its address: the pending change is obsolete.
				if address, ok := HostAddress(h.IP); ok {
					next, _, _ = withDevice(next, byIP.ID, func(c *RouteDevice) { clearPendingFamily(c, address) })
				}
			}
			if single && byIP.MAC == "" && h.MAC != "" && !IsRandomMAC(h.MAC) && MatchDeviceByMAC(next, h.MAC) == nil {
				next, _, _ = withDevice(next, byIP.ID, func(c *RouteDevice) { c.MAC = h.MAC })
			}
			continue
		}
		byMAC := MatchDeviceByMAC(next, h.MAC)
		if byMAC == nil {
			out = append(out, ScanMatch{Host: h, State: ScanNew})
			continue
		}
		address, ok := HostAddress(h.IP)
		if !ok {
			out = append(out, ScanMatch{Host: h, State: ScanNew})
			continue
		}
		out = append(out, ScanMatch{Host: h, State: ScanMoved, DeviceID: byMAC.ID})
		if !slices.Contains(byMAC.PendingAddresses, address) {
			// One pending address per family keeps the latest sighting.
			next, _, _ = withDevice(next, byMAC.ID, func(c *RouteDevice) {
				clearPendingFamily(c, address)
				c.PendingAddresses = append(c.PendingAddresses, address)
				// The address it replaces is known only when the device has
				// a single host address of that family.
				var hosts []string
				for _, a := range c.Addresses {
					if isHostNetwork(a) && sameFamily(a, address) {
						hosts = append(hosts, a)
					}
				}
				if len(hosts) == 1 {
					if c.PendingFrom == nil {
						c.PendingFrom = map[string]string{}
					}
					c.PendingFrom[address] = hosts[0]
				}
			})
		}
	}
	return next, out
}

// clearPendingFamily drops the pending addresses of the family of address
// from c (a copy) together with their pairing.
func clearPendingFamily(c *RouteDevice, address string) {
	kept := slices.DeleteFunc(slices.Clone(c.PendingAddresses), func(a string) bool { return sameFamily(a, address) })
	from := map[string]string{}
	for a, old := range c.PendingFrom {
		if slices.Contains(kept, a) {
			from[a] = old
		}
	}
	c.PendingAddresses = kept
	if len(kept) == 0 {
		c.PendingAddresses = nil
	}
	c.PendingFrom = nil
	if len(from) > 0 {
		c.PendingFrom = from
	}
}

func sameFamily(a, b string) bool {
	x, ok1 := ParseNetwork(a)
	y, ok2 := ParseNetwork(b)
	return ok1 && ok2 && len(x.Bytes) == len(y.Bytes)
}

// ErrNoPending is returned when a device has no pending address.
var ErrNoPending = errors.New("没有待确认的地址")

// ConfirmPendingAddresses makes the pending addresses the device's
// addresses: each pending address replaces the one address it was paired
// with (PendingFrom) when the device still has it, else it is added; every
// other address stays. Profiles share the
// device addresses and rule scopes use the device id only, so no rule
// changes. A pending address overlapping another device is an error and
// nothing changes.
func ConfirmPendingAddresses(doc *Doc, deviceID string) (*Doc, error) {
	index := slices.IndexFunc(doc.Route.Devices, func(d *RouteDevice) bool { return d.ID == deviceID })
	if index < 0 {
		return nil, ErrProfileDevice
	}
	old := doc.Route.Devices[index]
	if len(old.PendingAddresses) == 0 {
		return nil, ErrNoPending
	}
	addresses := slices.Clone(old.Addresses)
	for _, p := range old.PendingAddresses {
		if from := old.PendingFrom[p]; from != "" {
			addresses = slices.DeleteFunc(addresses, func(a string) bool { return a == from })
		}
		if !slices.Contains(addresses, p) {
			addresses = append(addresses, p)
		}
	}
	candidate := *old
	candidate.Addresses = addresses
	candidate.PendingAddresses, candidate.PendingFrom = nil, nil
	devices := slices.Clone(doc.Route.Devices)
	devices[index] = &candidate
	if problem := ValidateDevice(&candidate, devices); problem != "" {
		return nil, errors.New(problem)
	}
	next := *doc
	next.Route.Devices = devices
	return &next, nil
}

func isHostNetwork(a string) bool {
	n, ok := ParseNetwork(a)
	return ok && n.Prefix == len(n.Bytes)*8
}

// DismissPending drops the device's pending addresses and keeps its
// addresses.
func DismissPending(doc *Doc, deviceID string) (*Doc, error) {
	next, _, err := withDevice(doc, deviceID, func(c *RouteDevice) { c.PendingAddresses, c.PendingFrom = nil, nil })
	return next, err
}
