package model

import (
	"regexp"
	"slices"
	"strings"

	"github.com/getkiko/kiko/app/mygo/internal/schema/jsv"
)

// This file holds the LAN device helpers: legacy source MAC / hostname
// conditions and device labels.

// Device identification modes of legacy LAN device conditions.
const (
	DeviceModeMAC      = "source_mac_address"
	DeviceModeHostname = "source_hostname"
)

// DiscoveredDevice is a LAN device the server found.
type DiscoveredDevice struct {
	MAC       string   `json:"mac,omitempty"`
	Hostname  string   `json:"hostname,omitempty"`
	Addresses []string `json:"addresses"`
	Sources   []string `json:"sources"`
}

// DeviceValues reads a device list value: unique trimmed items.
func DeviceValues(value any) []string {
	var items []string
	if list, ok := jsv.Array(value); ok {
		for _, v := range list {
			items = append(items, jsv.String(v))
		}
	} else if s, ok := value.(string); ok {
		items = reListSeparator.Split(s, -1)
	}
	out := []string{}
	for _, item := range items {
		if t := strings.TrimSpace(item); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

var (
	reDottedMAC = regexp.MustCompile(`(?i)^(?:[\da-f]{4}\.)+[\da-f]{4}$`)
	reColonMAC  = regexp.MustCompile(`(?i)^(?:[\da-f]{2}:)+[\da-f]{2}$|^(?:[\da-f]{2}-)+[\da-f]{2}$`)
	reMACSep    = regexp.MustCompile(`[:-]`)
)

// NormalizeMAC returns a MAC address in lower-case colon form, accepting
// the forms net.ParseMAC does; "" when invalid.
func NormalizeMAC(value string) string {
	text := strings.TrimSpace(value)
	var bytes []string
	switch {
	case reDottedMAC.MatchString(text):
		hex := strings.ReplaceAll(text, ".", "")
		for i := 0; i+2 <= len(hex); i += 2 {
			bytes = append(bytes, hex[i:i+2])
		}
	case reColonMAC.MatchString(text):
		bytes = reMACSep.Split(text, -1)
	default:
		return ""
	}
	if n := len(bytes); n != 6 && n != 8 && n != 20 {
		return ""
	}
	return strings.ToLower(strings.Join(bytes, ":"))
}

// DeviceKey is a device label's key.
func DeviceKey(mode, value string) string {
	if mode == DeviceModeMAC {
		if mac := NormalizeMAC(value); mac != "" {
			return "mac:" + mac
		}
		return "mac:" + value
	}
	return "hostname:" + value
}

// DeviceIdentity is a discovered device's value in a mode.
func DeviceIdentity(device DiscoveredDevice, mode string) string {
	if mode == DeviceModeMAC {
		return device.MAC
	}
	return device.Hostname
}

// DeviceName names a device value: its alias, discovered hostname, saved
// hostname, or the value.
func DeviceName(mode, value string, labels map[string]DeviceLabel, devices []DiscoveredDevice) string {
	label := labels[DeviceKey(mode, value)]
	if alias := strings.TrimSpace(label.Alias); alias != "" {
		return alias
	}
	for _, d := range devices {
		if DeviceIdentity(d, mode) == value {
			if d.Hostname != "" {
				return d.Hostname
			}
			break
		}
	}
	if label.Hostname != "" {
		return label.Hostname
	}
	return value
}

// ConvertDeviceMode converts values between modes through the discovered
// devices; missing lists the values without a counterpart.
func ConvertDeviceMode(values []string, from, to string, devices []DiscoveredDevice) (next, missing []string) {
	next, missing = []string{}, []string{}
	for _, value := range values {
		var matches []DiscoveredDevice
		for _, d := range devices {
			if DeviceIdentity(d, from) == value {
				matches = append(matches, d)
			}
		}
		if len(matches) == 0 || slices.ContainsFunc(matches, func(d DiscoveredDevice) bool { return DeviceIdentity(d, to) == "" }) {
			missing = append(missing, value)
			continue
		}
		for _, d := range matches {
			next = append(next, DeviceIdentity(d, to))
		}
	}
	return unique(next), missing
}

// SnapshotDeviceLabels records the discovered hostnames of values.
func SnapshotDeviceLabels(mode string, values []string, labels map[string]DeviceLabel, devices []DiscoveredDevice) map[string]DeviceLabel {
	next := make(map[string]DeviceLabel, len(labels))
	for k, v := range labels {
		next[k] = v
	}
	for _, value := range values {
		for _, d := range devices {
			if DeviceIdentity(d, mode) == value {
				if d.Hostname != "" {
					key := DeviceKey(mode, value)
					label := next[key]
					label.Hostname = d.Hostname
					next[key] = label
				}
				break
			}
		}
	}
	return next
}
