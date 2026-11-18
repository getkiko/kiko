//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

type tunDNSConfig struct {
	Type       string   `json:"type"`
	DNSMode    string   `json:"dns_mode"`
	Address    []string `json:"address"`
	DNSAddress []string `json:"dns_address"`
}

func tunDNSAddresses(inbound tunDNSConfig) []string {
	if inbound.Type != "tun" || inbound.DNSMode == "disabled" {
		return nil
	}
	if len(inbound.DNSAddress) > 0 {
		return uniqueDNSAddresses(inbound.DNSAddress)
	}
	var ipv4, ipv6 string
	for _, value := range inbound.Address {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			continue
		}
		address := prefix.Addr()
		if address.Is4() && ipv4 != "" || address.Is6() && ipv6 != "" {
			continue
		}
		if next := address.Next(); prefix.Contains(next) {
			if address.Is4() {
				ipv4 = next.String()
			} else {
				ipv6 = next.String()
			}
		}
	}
	return uniqueDNSAddresses([]string{ipv4, ipv6})
}

func applySystemDNS(configPath string) (func() error, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var config struct {
		Inbounds []tunDNSConfig `json:"inbounds"`
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	var addresses []string
	for _, inbound := range config.Inbounds {
		if inbound.Type != "tun" || inbound.DNSMode == "disabled" {
			continue
		}
		addresses = tunDNSAddresses(inbound)
		break
	}
	if len(addresses) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	services, err := networkServicesContext(ctx)
	if err != nil {
		return nil, err
	}
	original := make(map[string][]string, len(services))
	for _, service := range services {
		original[service], err = getDNSServers(ctx, service)
		if err != nil {
			return nil, err
		}
	}
	for _, service := range services {
		if err = networksetupContext(ctx, append([]string{"-setdnsservers", service}, addresses...)...); err != nil {
			restoreErr := restoreDNSServices(original)
			if restoreErr != nil {
				return func() error { return restoreDNSServices(original) }, errors.Join(err, restoreErr)
			}
			return nil, err
		}
	}
	return func() error { return restoreDNSServices(original) }, nil
}

// Restore services concurrently so one stalled service cannot delay all others.
func restoreDNSServices(original map[string][]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, len(original))
	var group sync.WaitGroup
	for service, servers := range original {
		group.Go(func() { errs <- restoreDNSService(ctx, service, servers) })
	}
	group.Wait()
	close(errs)
	var result error
	for err := range errs {
		result = errors.Join(result, err)
	}
	return result
}

func getDNSServers(ctx context.Context, service string) ([]string, error) {
	out, err := networksetupOutput(ctx, "-getdnsservers", service)
	if err != nil {
		return nil, fmt.Errorf("读取 %s DNS: %w: %s", service, err, strings.TrimSpace(string(out)))
	}
	text := strings.TrimSpace(string(out))
	if text == "" || strings.Contains(strings.ToLower(text), "aren't any") {
		return nil, nil
	}
	return strings.Fields(text), nil
}
func restoreDNSService(ctx context.Context, service string, servers []string) error {
	if len(servers) == 0 {
		return networksetupContext(ctx, "-setdnsservers", service, "Empty")
	}
	return networksetupContext(ctx, append([]string{"-setdnsservers", service}, servers...)...)
}
func uniqueDNSAddresses(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || address.IsUnspecified() {
			continue
		}
		value = address.String()
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
