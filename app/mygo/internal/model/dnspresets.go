package model

import (
	"strconv"
	"strings"
)

// DNSPresetsVersion is the version of the DNS preset list documents are
// completed with: a document whose DNS.PresetsVersion is lower gets the
// presets it lacks once (AddDNSPresets).
const DNSPresetsVersion = 1

// DNSPreset is a ready-made public resolver (DNS_PRESETS), the server
// split into type and host as sing-box writes it.
type DNSPreset struct {
	Vendor, Tag, Type, Server, Path string
}

// DNSPresets are the public resolvers every document lists.
var DNSPresets = []DNSPreset{
	// AliDNS officially supports DoT and DoH on IP addresses.
	{"阿里 AliDNS", "dns-ali-tls", "tls", "223.5.5.5", ""},
	{"阿里 AliDNS", "dns-ali-tls2", "tls", "223.6.6.6", ""},
	{"阿里 AliDNS", "dns-ali-udp", "udp", "223.5.5.5", ""},
	{"阿里 AliDNS", "dns-ali-udp2", "udp", "223.6.6.6", ""},
	{"阿里 AliDNS", "dns-ali-https", "https", "223.5.5.5", "/dns-query"},
	{"阿里 AliDNS", "dns-ali-https2", "https", "223.6.6.6", "/dns-query"},
	{"阿里 AliDNS", "dns-ali-v6-tls", "tls", "2400:3200::1", ""},
	{"阿里 AliDNS", "dns-ali-v6-tls2", "tls", "2400:3200:baba::1", ""},
	{"阿里 AliDNS", "dns-ali-v6-udp", "udp", "2400:3200::1", ""},
	{"阿里 AliDNS", "dns-ali-v6-udp2", "udp", "2400:3200:baba::1", ""},
	// Cloudflare's certificate SAN covers the resolver IPs, so direct dialing needs no bootstrap.
	{"Cloudflare", "dns-cf-tls", "tls", "1.1.1.1", ""},
	{"Cloudflare", "dns-cf-tls2", "tls", "1.0.0.1", ""},
	{"Cloudflare", "dns-cf-udp", "udp", "1.1.1.1", ""},
	{"Cloudflare", "dns-cf-udp2", "udp", "1.0.0.1", ""},
	{"Cloudflare", "dns-cf-https", "https", "1.1.1.1", "/dns-query"},
	{"Cloudflare", "dns-cf-https2", "https", "1.0.0.1", "/dns-query"},
	{"Cloudflare", "dns-cf-v6-tls", "tls", "2606:4700:4700::1111", ""},
	{"Cloudflare", "dns-cf-v6-tls2", "tls", "2606:4700:4700::1001", ""},
	{"Cloudflare", "dns-cf-v6-udp", "udp", "2606:4700:4700::1111", ""},
	{"Cloudflare", "dns-cf-v6-udp2", "udp", "2606:4700:4700::1001", ""},
	// Google
	{"Google", "dns-google-tls", "tls", "8.8.8.8", ""},
	{"Google", "dns-google-tls2", "tls", "8.8.4.4", ""},
	{"Google", "dns-google-udp", "udp", "8.8.8.8", ""},
	{"Google", "dns-google-udp2", "udp", "8.8.4.4", ""},
	{"Google", "dns-google-https", "https", "8.8.8.8", "/dns-query"},
	{"Google", "dns-google-https2", "https", "8.8.4.4", "/dns-query"},
	{"Google", "dns-google-v6-tls", "tls", "2001:4860:4860::8888", ""},
	{"Google", "dns-google-v6-tls2", "tls", "2001:4860:4860::8844", ""},
	{"Google", "dns-google-v6-udp", "udp", "2001:4860:4860::8888", ""},
	{"Google", "dns-google-v6-udp2", "udp", "2001:4860:4860::8844", ""},
	// DNSPod serves DoT and DoH on 1.12.12.12 and 120.53.53.53, a different range from 119.29.29.29.
	{"腾讯 DNSPod", "dns-pod-tls", "tls", "1.12.12.12", ""},
	{"腾讯 DNSPod", "dns-pod-tls2", "tls", "120.53.53.53", ""},
	{"腾讯 DNSPod", "dns-pod-udp", "udp", "119.29.29.29", ""},
	{"腾讯 DNSPod", "dns-pod-udp2", "udp", "119.28.28.28", ""},
	{"腾讯 DNSPod", "dns-pod-https", "https", "1.12.12.12", "/dns-query"},
	{"腾讯 DNSPod", "dns-pod-https2", "https", "120.53.53.53", "/dns-query"},
}

var dnsPresetServerNames = map[string]string{
	"阿里 AliDNS":  "dns.alidns.com",
	"Cloudflare": "cloudflare-dns.com",
	"Google":     "dns.google",
	"腾讯 DNSPod":  "dot.pub",
}

// ServerName is the certificate name of a TLS preset, empty otherwise.
func (p DNSPreset) ServerName() string {
	if !IsTLSDNS(p.Type) {
		return ""
	}
	return dnsPresetServerNames[p.Vendor]
}

// Address is the preset as an address: tls://dns.alidns.com.
func (p DNSPreset) Address() string {
	host := p.ServerName()
	if host == "" {
		host = p.Server
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
	}
	return p.Type + "://" + host + p.Path
}

// DNSPresetServer is the server of a preset, enabled or not, its tag
// unique among taken.
func DNSPresetServer(p DNSPreset, taken map[string]bool, enabled bool) *DNSServer {
	tag := p.Tag
	for i := 2; taken[tag]; i++ {
		tag = p.Tag + "-" + strconv.Itoa(i)
	}
	server := &DNSServer{ID: NewID("dns"), Tag: tag, Enabled: enabled, Type: p.Type, Server: p.Server}
	if IsHTTPDNS(p.Type) {
		server.Path = p.Path
	}
	if IsTLSDNS(p.Type) {
		tls := map[string]any{}
		if name := p.ServerName(); name != "" {
			tls["server_name"] = name
		}
		server.Fields = Fields{"tls": tls}
	}
	return server
}

// dnsPresetKey identifies a resolver by type, host and HTTP path, so a
// preset already listed under another name is not added again.
func dnsPresetKey(typ, server, path string) string {
	host := strings.ToLower(strings.Trim(strings.TrimSpace(server), "[]"))
	if IsHTTPDNS(typ) {
		if path == "" {
			path = "/dns-query"
		}
	} else {
		path = ""
	}
	return typ + "|" + host + "|" + path
}

// AddDNSPresets completes a document with the presets it lacks, once per
// DNSPresetsVersion: they are added disabled after the document's own
// servers, so the emitted configuration does not change until one is
// enabled. A preset listed already (same type, host and path) is skipped.
// It returns doc itself when it is current, a modified copy otherwise.
func AddDNSPresets(doc *Doc) *Doc {
	if doc == nil || doc.DNS.PresetsVersion >= DNSPresetsVersion {
		return doc
	}
	next := Clone(doc)
	taken := map[string]bool{}
	listed := map[string]bool{}
	for _, s := range next.DNS.Servers {
		taken[s.Tag] = true
		path := s.Path
		if p, ok := s.Fields["path"].(string); ok && path == "" {
			path = p
		}
		listed[dnsPresetKey(s.Type, s.Server, path)] = true
	}
	for _, p := range DNSPresets {
		key := dnsPresetKey(p.Type, p.Server, p.Path)
		if listed[key] {
			continue
		}
		server := DNSPresetServer(p, taken, false)
		taken[server.Tag], listed[key] = true, true
		next.DNS.Servers = append(next.DNS.Servers, server)
	}
	next.DNS.PresetsVersion = DNSPresetsVersion
	return next
}
