package main

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// mitmUpstreamExtKeys are the upstream options of package R: the HTTP/2
// profile and the ClientHello mirror.
var mitmUpstreamExtKeys = []string{"h2_profile", "mirror_client_hello"}

// mitmUpstreamExtProbeConfig carries both keys; a core without them rejects
// it while still accepting the policy options.
const mitmUpstreamExtProbeConfig = `{"mitm":{"enabled":false,"upstream":{"h2_profile":"chrome","mirror_client_hello":true}}}`

// mitmUpstreamExtSupported probes once whether the core accepts
// mitm.upstream.h2_profile and mirror_client_hello. It needs the policy
// options.
func (s *server) mitmUpstreamExtSupported() bool {
	s.mitm.upstreamExtProbeOnce.Do(func() {
		if !s.mitmPolicySupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "mitm-upstream-ext-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(mitmUpstreamExtProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.upstreamExtSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.upstreamExtSupported
}

// mitmUpstreamExtPresent reports whether a mitm section sets one of the
// package R upstream options.
func mitmUpstreamExtPresent(section map[string]any) bool {
	upstream, _ := section["upstream"].(map[string]any)
	for _, key := range mitmUpstreamExtKeys {
		if _, ok := upstream[key]; ok {
			return true
		}
	}
	return false
}

// mitmUpstreamExtStrip removes the package R upstream options from a core
// that knows the policy options but not these; it reports whether any was
// removed.
func mitmUpstreamExtStrip(section map[string]any) bool {
	upstream, _ := section["upstream"].(map[string]any)
	removed := false
	for _, key := range mitmUpstreamExtKeys {
		if _, ok := upstream[key]; ok {
			delete(upstream, key)
			removed = true
		}
	}
	if removed && len(upstream) == 0 {
		delete(section, "upstream")
	}
	return removed
}
