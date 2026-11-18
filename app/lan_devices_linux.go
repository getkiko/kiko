//go:build linux

package main

import "github.com/sagernet/sing-box/route"

// Default lease locations used by the app's LAN discovery.
func defaultLANLeaseFiles() []string {
	return []string{
		"/tmp/dhcp.leases",
		"/var/lib/dhcp/dhcpd.leases",
		"/var/lib/dhcpd/dhcpd.leases",
		"/var/lib/kea/kea-leases4.csv",
		"/var/lib/kea/kea-leases6.csv",
	}
}

func lanDevicesSupported() bool { return true }
func discoverLANDevices(paths []string) ([]lanDevice, []string, error) {
	return collectLANDevices(paths, defaultLANLeaseFiles(), func() ([]lanObservation, error) {
		entries, err := route.ReadNeighborEntries()
		if err != nil {
			return nil, err
		}
		observations := make([]lanObservation, 0, len(entries))
		for _, entry := range entries {
			observations = append(observations, lanObservation{address: entry.Address, mac: entry.MACAddress, source: "neighbor"})
		}
		return observations, nil
	}, route.ReloadLeaseFiles)
}
