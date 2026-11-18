package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type lanDevice struct {
	MAC       string   `json:"mac,omitempty"`
	Hostname  string   `json:"hostname,omitempty"`
	Addresses []string `json:"addresses"`
	Sources   []string `json:"sources"`
}
type lanDevicesResponse struct {
	Devices  []lanDevice `json:"devices"`
	Warnings []string    `json:"warnings"`
}
type lanObservation struct {
	address          netip.Addr
	mac              net.HardwareAddr
	hostname, source string
}

func mergeLANDevices(observations []lanObservation) []lanDevice {
	// DHCPv6 leases can identify only a hostname. Join them to a known neighbor
	// by address before grouping by device identity.
	addressMAC := make(map[netip.Addr]net.HardwareAddr)
	for _, entry := range observations {
		if len(entry.mac) > 0 && entry.mac[0]&1 == 0 && !allZeroMAC(entry.mac) {
			addressMAC[entry.address.Unmap()] = entry.mac
		}
	}
	devices := make(map[string]*lanDevice)
	for _, entry := range observations {
		address := entry.address.Unmap()
		if !address.IsValid() || address.IsLoopback() || address.IsMulticast() || address.IsUnspecified() {
			continue
		}
		if len(entry.mac) == 0 {
			entry.mac = addressMAC[address]
		}
		mac := entry.mac.String()
		if len(entry.mac) > 0 && (entry.mac[0]&1 != 0 || allZeroMAC(entry.mac)) {
			continue
		}
		if mac == "" && entry.hostname == "" {
			continue
		}
		key := "mac:" + mac
		if mac == "" {
			key = "hostname:" + entry.hostname
		}
		device := devices[key]
		if device == nil {
			device = &lanDevice{MAC: mac, Addresses: []string{}, Sources: []string{}}
			devices[key] = device
		}
		if entry.hostname != "" {
			device.Hostname = entry.hostname
		}
		device.Addresses = appendUnique(device.Addresses, address.String())
		device.Sources = appendUnique(device.Sources, entry.source)
	}
	result := make([]lanDevice, 0, len(devices))
	for _, device := range devices {
		sort.Strings(device.Addresses)
		sort.Strings(device.Sources)
		result = append(result, *device)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MAC+"/"+result[i].Hostname < result[j].MAC+"/"+result[j].Hostname
	})
	return result
}
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
func allZeroMAC(mac net.HardwareAddr) bool {
	for _, value := range mac {
		if value != 0 {
			return false
		}
	}
	return true
}
func listLANDevices(w http.ResponseWriter, r *http.Request) {
	if !lanDevicesSupported() {
		http.Error(w, "设备发现适用于 Linux server", http.StatusNotImplemented)
		return
	}
	paths := r.URL.Query()["lease_file"]
	for _, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			http.Error(w, "DHCP 租约文件需要绝对路径", http.StatusBadRequest)
			return
		}
	}
	devices, warnings, err := discoverLANDevices(paths)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, lanDevicesResponse{devices, warnings})
}

// Discovery dependencies are injected so lease/neighbor failures can be exercised on every host.
func collectLANDevices(paths, defaultPaths []string, readNeighbors func() ([]lanObservation, error), reloadLeases func([]string) (map[netip.Addr]net.HardwareAddr, map[netip.Addr]string, map[string]string)) ([]lanDevice, []string, error) {
	observations := []lanObservation{}
	warnings := []string{}
	success := false
	neighbors, err := readNeighbors()
	if err != nil {
		warnings = append(warnings, "读取邻居表失败："+err.Error())
	} else {
		success = true
		for _, entry := range neighbors {
			observations = append(observations, entry)
		}
	}
	custom := len(paths) > 0
	if !custom {
		paths = defaultPaths
	}
	readable := []string{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && !info.Mode().IsRegular() {
			warnings = append(warnings, "DHCP 租约需要普通文件："+path)
			continue
		}
		if err != nil {
			if custom || !os.IsNotExist(err) {
				warnings = append(warnings, "读取 DHCP 租约失败："+path+"："+err.Error())
			}
			continue
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			warnings = append(warnings, "读取 DHCP 租约失败："+path+"："+openErr.Error())
			continue
		}
		_ = file.Close()
		success = true
		readable = append(readable, path)
	}
	macs, names, macNames := reloadLeases(readable)
	// Enrich neighbor records with the same MAC-to-hostname fallback as the core.
	for index := range observations {
		entry := &observations[index]
		entry.hostname = names[entry.address.Unmap()]
		if entry.hostname == "" {
			entry.hostname = macNames[entry.mac.String()]
		}
	}
	addresses := make([]string, 0, len(macs)+len(names))
	for address := range macs {
		addresses = appendUnique(addresses, address.String())
	}
	for address := range names {
		addresses = appendUnique(addresses, address.String())
	}
	sort.Strings(addresses)
	for _, text := range addresses {
		address := netip.MustParseAddr(text)
		hostname := names[address]
		if hostname == "" {
			hostname = macNames[macs[address].String()]
		}
		observations = append(observations, lanObservation{address: address, mac: macs[address], hostname: hostname, source: "dhcp"})
	}
	if !success {
		return nil, warnings, fmt.Errorf("设备发现失败：%v", warnings)
	}
	return mergeLANDevices(observations), warnings, nil
}
