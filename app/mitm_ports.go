package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// httpRewriteProtectedProbeConfig carries the package R key; a core without it rejects it
// while still accepting http_rewrite.
const httpRewriteProtectedProbeConfig = `{"mitm":{"enabled":false,"http_rewrite":{"protected_ports":[]}}}`

// httpRewriteProtectedPortsSupported probes once whether the core accepts
// http_rewrite.protected_ports.
func (s *server) httpRewriteProtectedPortsSupported() bool {
	s.mitm.protectedPortsProbeOnce.Do(func() {
		if !s.httpRewriteSupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "http-rewrite-protected-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(httpRewriteProtectedProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.protectedPortsSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.protectedPortsSupported
}

// appListenPorts are the ports this server listens on: the local API and, when running, the
// remote access listener. Internal rewrites of the core must not be aimed at them.
func (s *server) appListenPorts() []int {
	var ports []int
	if _, text, err := net.SplitHostPort(s.listenAddr); err == nil {
		if port, err := strconv.Atoi(text); err == nil && port > 0 && port < 65536 {
			ports = append(ports, port)
		}
	}
	if s.remote != nil {
		if status := s.remote.status(); status.Running {
			if _, text, err := net.SplitHostPort(status.Listen); err == nil {
				if port, err := strconv.Atoi(text); err == nil && port > 0 && port < 65536 {
					ports = append(ports, port)
				}
			}
		}
	}
	// The certificate download page listens on an ephemeral port; a config
	// generated while it runs protects that port too.
	if port := s.mitmSharePort(); port > 0 {
		ports = append(ports, port)
	}
	return ports
}

// mitmSharePort returns the port of the running download page, or 0.
func (s *server) mitmSharePort() int {
	sh := &s.mitm.share
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.cur == nil {
		return 0
	}
	return sh.cur.port
}

// mitmProtectPorts adds http_rewrite.protected_ports to a mitm section that has rewrite
// rules, for a core that knows the option.
func (s *server) mitmProtectPorts(section map[string]any) {
	rewrite, _ := section["http_rewrite"].(map[string]any)
	if rewrite == nil {
		return
	}
	ports := s.appListenPorts()
	if len(ports) == 0 || !s.httpRewriteProtectedPortsSupported() {
		return
	}
	rewrite["protected_ports"] = ports
}
