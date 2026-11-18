package main

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/common/mitm/hostmatch"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	mitmSyncInterval = 30 * time.Second
	mitmRPCTimeout   = 10 * time.Second
)

// mitmState is the HTTPS decryption state of the server.
type mitmState struct {
	probeOnce sync.Once
	supported bool
	// policyProbeOnce/policySupported: the core also knows the interception
	// policy options (built-in exclusion, sources, upstream, quic, pinning).
	policyProbeOnce sync.Once
	policySupported bool
	// rewriteProbeOnce/rewriteSupported: the core knows mitm.http_rewrite.
	rewriteProbeOnce sync.Once
	rewriteSupported bool
	// bodyProbeOnce/bodySupported: the core also knows http_rewrite.body,
	// max_body_size and the "header" URL action (package K).
	bodyProbeOnce sync.Once
	bodySupported bool
	// upstreamExtProbeOnce/upstreamExtSupported: the core also knows
	// upstream.h2_profile and upstream.mirror_client_hello (package R).
	upstreamExtProbeOnce sync.Once
	upstreamExtSupported bool
	// scriptsProbeOnce/scriptsSupported: the core knows the top-level
	// scripts option (package L).
	scriptsProbeOnce sync.Once
	scriptsSupported bool
	// scriptsCronProbeOnce/scriptsCronSupported: the core also accepts
	// scripts of type cron (package N).
	scriptsCronProbeOnce sync.Once
	scriptsCronSupported bool
	// scriptsRuleProbeOnce/scriptsRuleSupported: the core also accepts
	// scripts of type rule and dns and the rules that use them (package P).
	scriptsRuleProbeOnce sync.Once
	scriptsRuleSupported bool
	// protectedPortsProbeOnce/protectedPortsSupported: the core also accepts
	// http_rewrite.protected_ports (package R).
	protectedPortsProbeOnce sync.Once
	protectedPortsSupported bool

	// opMu serializes changes to the root certificate and trust. It may wait
	// on a system authorization dialog, so it is not held together with mu
	// for long.
	opMu sync.Mutex
	// syncMu serializes synchronization with the core.
	syncMu sync.Mutex
	// signMu serializes reading the root private key and changing the root
	// certificate file, without blocking mu.
	signMu sync.Mutex
	// mu guards the fields below and is never held across gRPC calls.
	mu        sync.Mutex
	secrets   secretStore
	trust     trustStore
	now       func() time.Time
	interval  time.Duration
	loaded    bool
	state     string
	root      *x509.Certificate
	createdAt time.Time
	// authority is the intermediate CA for the current core run
	// (authorityRun); it exists only in memory.
	authority    *mitmAuthority
	authorityRun int64
	// checkAuthority is used only for trust checks.
	checkAuthority *mitmAuthority
	trusted        bool
	trustCheckedAt time.Time
	// trustKnown is false while no intermediate CA is available to check
	// trust; the status then reports unknown.
	trustKnown bool
	// trustGen is incremented when the user changes trust, discarding the
	// results of checks started earlier.
	trustGen   uint64
	trustRoots *x509.CertPool
	// Fingerprint of the root whose private key failed to load from the
	// credential store (other than not found), and when; no reload is
	// attempted in the meantime.
	keyFailedRoot string
	keyFailedAt   time.Time
	// prepareCA writes the temporary file for ca.json; tests may replace it.
	prepareCA func(path string, data []byte) (string, error)
	// renameCA replaces ca.json; tests may replace it.
	renameCA func(oldPath, newPath string) error
	// Core connection.
	client          mitmapi.MITMClient
	clientRun       int64
	coreStatus      *mitmapi.Status
	coreUnsupported bool
	unsupportedRun  int64
	lastFailure     string
	// share is the LAN certificate download page.
	share mitmShare
}

// mitmSupported probes only once: upstream and older cores have no top-level
// mitm option, so check fails outright. Config generation uses the result to
// decide whether to write mitm.
func (s *server) mitmSupported() bool {
	s.mitm.probeOnce.Do(func() {
		if s.singBox == "" || s.dataDir == "" {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "mitm-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(`{"mitm":{"enabled":false}}`), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.supported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.supported
}

// mitmPolicyProbeConfig carries one of the package F keys; a core without
// them rejects it while still accepting the base mitm section.
const mitmPolicyProbeConfig = `{"mitm":{"enabled":false,"hostnames":{"builtin_exclude":true}}}`

// mitmPolicySupported reports whether the core recognizes the interception
// policy options (package F). It probes only once and requires the base mitm
// option to be available.
func (s *server) mitmPolicySupported() bool {
	s.mitm.policyProbeOnce.Do(func() {
		if !s.mitmSupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "mitm-policy-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(mitmPolicyProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.policySupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.policySupported
}

// httpRewriteProbeConfig carries the rewrite key; a core without it rejects
// it while still accepting the base mitm section.
const httpRewriteProbeConfig = `{"mitm":{"enabled":false,"http_rewrite":{"url":[]}}}`

// httpRewriteSupported probes once whether the core accepts
// mitm.http_rewrite. It needs the base mitm option.
func (s *server) httpRewriteSupported() bool {
	s.mitm.rewriteProbeOnce.Do(func() {
		if !s.mitmSupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "http-rewrite-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(httpRewriteProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.rewriteSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.rewriteSupported
}

// httpRewriteBodyProbeConfig carries the package K key; a core without it
// rejects it while still accepting http_rewrite.
const httpRewriteBodyProbeConfig = `{"mitm":{"enabled":false,"http_rewrite":{"body":[]}}}`

// httpRewriteBodySupported probes once whether the core accepts
// http_rewrite.body (and with it max_body_size and the header URL action).
func (s *server) httpRewriteBodySupported() bool {
	s.mitm.bodyProbeOnce.Do(func() {
		if !s.httpRewriteSupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "http-rewrite-body-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(httpRewriteBodyProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.mitm.bodySupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.mitm.bodySupported
}

// httpRewriteBodyStrip removes the package K options (body, max_body_size and
// URL rules with the "header" action) from a core that knows http_rewrite but
// not these. It returns a short description of what was removed; empty when
// nothing was. An http_rewrite left without rules is removed too.
func httpRewriteBodyStrip(section map[string]any) []string {
	rewrite, _ := section["http_rewrite"].(map[string]any)
	if rewrite == nil {
		return nil
	}
	var removed []string
	if _, ok := rewrite["body"]; ok {
		delete(rewrite, "body")
		removed = append(removed, "正文改写")
	}
	if _, ok := rewrite["max_body_size"]; ok {
		delete(rewrite, "max_body_size")
	}
	var kept []any
	dropped := false
	switch list := rewrite["url"].(type) {
	case []any:
		for _, item := range list {
			if rule, ok := item.(map[string]any); ok && rule["action"] == "header" {
				dropped = true
				continue
			}
			kept = append(kept, item)
		}
	case []map[string]any:
		for _, rule := range list {
			if rule["action"] == "header" {
				dropped = true
				continue
			}
			kept = append(kept, rule)
		}
	}
	if dropped {
		removed = append(removed, "内部改写")
		if len(kept) == 0 {
			delete(rewrite, "url")
		} else {
			rewrite["url"] = kept
		}
	}
	if len(removed) > 0 && len(rewrite) == 0 {
		delete(section, "http_rewrite")
	}
	return removed
}

// httpRewriteStrip removes http_rewrite from a mitm section for a core that
// does not know it; it reports whether it was present.
func httpRewriteStrip(section map[string]any) bool {
	if _, ok := section["http_rewrite"]; !ok {
		return false
	}
	delete(section, "http_rewrite")
	return true
}

// mitmDropUnknownBuiltin removes disabled_builtin names the compiled
// built-in table does not know (an id or a single pattern); the core
// rejects the whole config for one. It returns the dropped names.
func mitmDropUnknownBuiltin(section map[string]any) []string {
	hostnames, _ := section["hostnames"].(map[string]any)
	if hostnames == nil {
		return nil
	}
	var names []string
	switch list := hostnames["disabled_builtin"].(type) {
	case []string:
		names = list
	case []any:
		for _, item := range list {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
	default:
		return nil
	}
	known := map[string]bool{}
	for _, entry := range hostmatch.Builtin() {
		known[entry.ID] = true
		for _, pattern := range entry.Patterns {
			known[pattern] = true
		}
	}
	var kept, dropped []string
	for _, name := range names {
		if known[strings.ToLower(strings.TrimSpace(strings.TrimSuffix(name, ".")))] {
			kept = append(kept, name)
		} else {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	if len(kept) == 0 {
		delete(hostnames, "disabled_builtin")
	} else {
		hostnames["disabled_builtin"] = kept
	}
	return dropped
}

// mitmStripPolicy removes the package F keys from a mitm section for a
// core that only knows the base options; it reports whether any was removed.
func mitmStripPolicy(section map[string]any) bool {
	removed := false
	for _, key := range []string{"sources", "upstream", "quic", "pinning_skip_duration"} {
		if _, ok := section[key]; ok {
			delete(section, key)
			removed = true
		}
	}
	if hostnames, ok := section["hostnames"].(map[string]any); ok {
		for _, key := range []string{"builtin_exclude", "disabled_builtin"} {
			if _, ok := hostnames[key]; ok {
				delete(hostnames, key)
				removed = true
			}
		}
		if len(hostnames) == 0 {
			delete(section, "hostnames")
		}
	}
	return removed
}

var errMITMUnimplemented = errors.New("core has no MITM service")

// mitmSession pushes the intermediate CA to the core within one gRPC session
// and reconciles every 30 seconds. If the core has no MITM service, it is not
// called again for this run.
func (s *server) mitmSession(ctx context.Context, client mitmapi.MITMClient, runID int64) {
	s.mitm.mu.Lock()
	if s.mitm.coreUnsupported && s.mitm.unsupportedRun == runID {
		s.mitm.mu.Unlock()
		return
	}
	interval := s.mitm.interval
	s.mitm.mu.Unlock()
	if interval <= 0 {
		interval = mitmSyncInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := context.WithTimeout(ctx, mitmRPCTimeout)
		_, err := client.GetCapabilities(callCtx, &emptypb.Empty{})
		cancel()
		if err == nil {
			break
		}
		if status.Code(err) == codes.Unimplemented {
			s.mitmMarkUnsupported(runID)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	s.mitm.mu.Lock()
	// A newer session may have replaced this one: register only while this
	// session is still valid.
	if ctx.Err() != nil {
		s.mitm.mu.Unlock()
		return
	}
	s.mitm.client, s.mitm.clientRun = client, runID
	s.mitm.coreUnsupported = false
	s.mitm.mu.Unlock()
	defer func() {
		s.mitm.mu.Lock()
		if s.mitm.client == client {
			s.mitm.client, s.mitm.coreStatus = nil, nil
		}
		s.mitm.mu.Unlock()
	}()
	for {
		if errors.Is(s.mitmSync(ctx, client, runID), errMITMUnimplemented) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *server) mitmMarkUnsupported(runID int64) {
	s.mitm.mu.Lock()
	s.mitm.coreUnsupported, s.mitm.unsupportedRun = true, runID
	if s.mitm.clientRun == runID {
		s.mitm.client, s.mitm.coreStatus = nil, nil
	}
	s.mitm.mu.Unlock()
}

// mitmSyncNow syncs the currently connected core right after the root
// certificate changes.
func (s *server) mitmSyncNow(ctx context.Context) {
	s.mitm.mu.Lock()
	client, runID := s.mitm.client, s.mitm.clientRun
	s.mitm.mu.Unlock()
	if client != nil {
		_ = s.mitmSync(ctx, client, runID)
	}
}

// mitmSync reads the core status, pushes or clears the intermediate CA as
// needed, and records the latest status.
func (s *server) mitmSync(ctx context.Context, client mitmapi.MITMClient, runID int64) error {
	s.mitm.syncMu.Lock()
	defer s.mitm.syncMu.Unlock()
	callCtx, cancel := context.WithTimeout(ctx, mitmRPCTimeout)
	current, err := client.GetStatus(callCtx, &emptypb.Empty{})
	cancel()
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			s.mitmMarkUnsupported(runID)
			return errMITMUnimplemented
		}
		return err
	}
	s.mitm.mu.Lock()
	s.mitmLoadLocked()
	absent := s.mitm.state == mitmCAStateAbsent
	s.mitm.mu.Unlock()
	// When the core has decryption disabled or uses a file certificate, do not
	// sign or read the credential store.
	root, rootState := s.mitmRootSnapshot()
	// When the core's root differs from the current root (deleted, corrupted
	// or replaced), it must be replaced or cleared whether or not decryption
	// is enabled.
	stale := current.GetRootFingerprint() != "" &&
		(rootState != mitmCAStateReady || root == nil || current.GetRootFingerprint() != certFingerprint(root.Raw))
	managed := (current.GetEnabled() || stale) && current.GetCertificateSource() != "file"
	var authority *mitmAuthority
	var authorityErr error
	if managed {
		authority, err = s.mitmAuthority(runID)
		// Reading the keychain may wait for user authorization; if the session
		// ended meanwhile, do not push or record a failure.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.mitmPushFailed(err.Error())
			if !errors.Is(err, errMITMRootBroken) {
				s.mitmStoreStatus(client, current)
				return err
			}
			authorityErr, authority = err, nil
		}
	}
	var request *mitmapi.SetAuthorityRequest
	switch {
	case current.GetCertificateSource() == "file":
	case authority != nil && (current.GetAuthorityPending() || current.GetAuthorityFingerprint() != authority.fingerprint):
		request = &mitmapi.SetAuthorityRequest{
			CertChainPem:    authority.chainPEM,
			KeyPkcs8:        authority.key,
			RootFingerprint: authority.rootFP,
			NotAfter:        authority.cert.NotAfter.Unix(),
		}
	case authority == nil && (stale || (absent && current.GetAuthorityFingerprint() != "")):
		request = &mitmapi.SetAuthorityRequest{}
	}
	if request != nil {
		callCtx, cancel := context.WithTimeout(ctx, mitmRPCTimeout)
		updated, err := client.SetAuthority(callCtx, request)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if status.Code(err) == codes.Unimplemented {
				s.mitmMarkUnsupported(runID)
				return errMITMUnimplemented
			}
			s.mitmPushFailed("（" + status.Code(err).String() + "）")
			s.mitmStoreStatus(client, current)
			return err
		}
		s.mitm.mu.Lock()
		s.mitm.lastFailure = ""
		s.mitm.mu.Unlock()
		if s.logs != nil {
			if authority != nil {
				s.logs.add("[kiko] HTTPS 解密证书已下发 " + authority.fingerprint[:16])
			} else {
				s.logs.add("[kiko] HTTPS 解密证书已清除")
			}
		}
		current = updated
	}
	s.mitmStoreStatus(client, current)
	return authorityErr
}

func (s *server) mitmStoreStatus(client mitmapi.MITMClient, current *mitmapi.Status) {
	s.mitm.mu.Lock()
	if s.mitm.client == client {
		s.mitm.coreStatus = current
	}
	s.mitm.mu.Unlock()
}

// mitmPushFailed records a push failure. reason is either a local,
// user-facing reason or "(gRPC code)" in full-width parentheses; it never
// contains text returned by the core. Each reason is logged only once.
func (s *server) mitmPushFailed(reason string) {
	s.mitm.mu.Lock()
	repeated := s.mitm.lastFailure == reason
	s.mitm.lastFailure = reason
	s.mitm.mu.Unlock()
	if repeated || s.logs == nil {
		return
	}
	if strings.HasPrefix(reason, "（") {
		s.logs.add("[kiko] HTTPS 解密证书下发失败" + reason)
	} else {
		s.logs.add("[kiko] HTTPS 解密证书下发失败: " + reason)
	}
}

// mitmSyncAfterApply syncs right after a config is applied successfully, so
// enabling decryption does not wait for the next cycle.
func (s *server) mitmSyncAfterApply() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*mitmRPCTimeout)
		defer cancel()
		s.mitmSyncNow(ctx)
	}()
}
