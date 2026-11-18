package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/common/mitm/hostmatch"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// REST API for the HTTPS decryption certificates. Responses never contain
// private key material.

type mitmCAStatus struct {
	State          string     `json:"state"`
	CommonName     string     `json:"commonName,omitempty"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	NotBefore      *time.Time `json:"notBefore,omitempty"`
	NotAfter       *time.Time `json:"notAfter,omitempty"`
	CreatedAt      *time.Time `json:"createdAt,omitempty"`
	Trusted        bool       `json:"trusted"`
	TrustUnknown   bool       `json:"trustUnknown,omitempty"`
	TrustSupported bool       `json:"trustSupported"`
}

type mitmCoreStatus struct {
	Connected            bool       `json:"connected"`
	Enabled              *bool      `json:"enabled,omitempty"`
	Pending              *bool      `json:"pending,omitempty"`
	AuthorityFingerprint string     `json:"authorityFingerprint,omitempty"`
	RootFingerprint      string     `json:"rootFingerprint,omitempty"`
	AuthorityNotAfter    *time.Time `json:"authorityNotAfter,omitempty"`
	LeafCacheSize        *uint32    `json:"leafCacheSize,omitempty"`
	CertificateSource    string     `json:"certificateSource,omitempty"`
}

// mitmSkipEntry is a host the core leaves undecrypted for a while.
type mitmSkipEntry struct {
	Host   string    `json:"host"`
	Source string    `json:"source,omitempty"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason,omitempty"`
}

// mitmFailure is a host whose interception failed.
type mitmFailure struct {
	Host      string    `json:"host"`
	Count     uint32    `json:"count"`
	LastError string    `json:"lastError,omitempty"`
	At        time.Time `json:"at"`
}

type mitmStatusResponse struct {
	Supported bool           `json:"supported"`
	CA        mitmCAStatus   `json:"ca"`
	Core      mitmCoreStatus `json:"core"`
	// BuiltinCount is the size of the core's built-in exclusion table; 0
	// (hidden by the UI) while the core status does not report it.
	BuiltinCount int             `json:"builtinCount,omitempty"`
	Skip         []mitmSkipEntry `json:"skip"`
	Failures     []mitmFailure   `json:"failures"`
}

// mitmBuiltinCount is the size of the built-in exclusion table compiled into
// this build; the connected core carries the same table. It is 0 (row
// hidden) when the core does not know the policy options: the app strips
// them from its config then, so the table does not apply.
func (s *server) mitmBuiltinCount() int {
	if !s.mitmPolicySupported() {
		return 0
	}
	return hostmatch.BuiltinCount()
}

func (s *server) registerMITMRoutes(m *routeMux) {
	m.get("GET /api/v1/mitm/status", s.mitmStatusHandler)
	m.get("GET /api/v1/mitm/export", s.mitmExport)
	m.get("GET /api/v1/mitm/share", s.mitmShareGet)
	m.handle("POST /api/v1/mitm/share", routeWrite, "开启证书下载页", s.mitmSharePost)
	m.handle("DELETE /api/v1/mitm/share", routeWrite, "关闭证书下载页", s.mitmShareDelete)
	m.handle("POST /api/v1/mitm/skip", routeWrite, "清除解密跳过记录", s.mitmClearSkip)
	m.handle("POST /api/v1/mitm/ca", routeWrite, "生成根证书", s.mitmCreateCA)
	m.handle("POST /api/v1/mitm/ca/regenerate", routeWrite, "重新生成根证书", s.mitmRegenerateCA)
	m.handle("DELETE /api/v1/mitm/ca", routeWrite, "删除根证书", s.mitmDeleteCA)
	m.handle("POST /api/v1/mitm/trust", routeWrite, "信任根证书", s.mitmInstallTrust)
	m.handle("DELETE /api/v1/mitm/trust", routeWrite, "取消信任根证书", s.mitmRemoveTrustHandler)
}

// mitmStatus summarizes the root certificate, trust and core state, fetching the
// latest state from the core when connected. Status polling uses only the cached
// intermediate CA and never reads the credential store; trustUnknown is true when
// trust cannot be determined.
func (s *server) mitmStatus(ctx context.Context) mitmStatusResponse {
	return s.mitmStatusRead(ctx, false)
}

// mitmStatusRead is like mitmStatus, but when allowRead is true (after a user
// action) it may read the credential store to check trust.
func (s *server) mitmStatusRead(ctx context.Context, allowRead bool) mitmStatusResponse {
	s.mitm.mu.Lock()
	client := s.mitm.client
	s.mitm.mu.Unlock()
	if client != nil {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		current, err := client.GetStatus(callCtx, &emptypb.Empty{})
		cancel()
		if err == nil {
			s.mitmStoreStatus(client, current)
		}
	}
	supported := s.mitmSupported()
	builtinCount := s.mitmBuiltinCount()
	s.mitmRefreshTrust(allowRead)
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	s.mitmLoadLocked()
	result := mitmStatusResponse{Supported: supported && !s.mitm.coreUnsupported, Skip: []mitmSkipEntry{}, Failures: []mitmFailure{}}
	result.CA.TrustSupported = s.mitmTrustLocked().Supported()
	state := s.mitm.state
	trusted := s.mitm.state == mitmCAStateReady && s.mitm.trusted
	result.CA.State = s.mitm.state
	if root := s.mitm.root; root != nil {
		notBefore, notAfter := root.NotBefore, root.NotAfter
		result.CA.CommonName = root.Subject.CommonName
		result.CA.Fingerprint = certFingerprint(root.Raw)
		result.CA.NotBefore, result.CA.NotAfter = &notBefore, &notAfter
		if !s.mitm.createdAt.IsZero() {
			createdAt := s.mitm.createdAt
			result.CA.CreatedAt = &createdAt
		}
		result.CA.Trusted = trusted
		result.CA.TrustUnknown = state == mitmCAStateReady && !s.mitm.trustKnown
	}
	if current := s.mitm.coreStatus; s.mitm.client != nil && current != nil {
		enabled, pending, cache := current.GetEnabled(), current.GetAuthorityPending(), current.GetLeafCacheSize()
		result.Core = mitmCoreStatus{
			Connected: true, Enabled: &enabled, Pending: &pending, LeafCacheSize: &cache,
			AuthorityFingerprint: current.GetAuthorityFingerprint(),
			RootFingerprint:      current.GetRootFingerprint(),
			CertificateSource:    current.GetCertificateSource(),
		}
		if seconds := current.GetAuthorityNotAfter(); seconds > 0 {
			notAfter := time.Unix(seconds, 0).UTC()
			result.Core.AuthorityNotAfter = &notAfter
		}
		result.BuiltinCount = builtinCount
		for _, entry := range current.GetSkip() {
			result.Skip = append(result.Skip, mitmSkipEntry{
				Host: entry.GetHost(), Source: entry.GetSource(), Reason: entry.GetReason(),
				Until: time.Unix(entry.GetUntil(), 0).UTC(),
			})
		}
		for _, entry := range current.GetFailures() {
			result.Failures = append(result.Failures, mitmFailure{
				Host: entry.GetHost(), Count: entry.GetCount(), LastError: entry.GetReason(),
				At: time.Unix(entry.GetTime(), 0).UTC(),
			})
		}
	}
	return result
}

func (s *server) mitmStatusHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mitmStatus(r.Context()))
}

// mitmClearSkip clears the core's skip entry of one host, or all of them
// when host is empty (or the body is).
func (s *server) mitmClearSkip(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host string `json:"host"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			mitmError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
	}
	s.mitm.mu.Lock()
	client := s.mitm.client
	s.mitm.mu.Unlock()
	if client == nil {
		mitmError(w, http.StatusConflict, "核心未连接")
		return
	}
	callCtx, cancel := context.WithTimeout(r.Context(), mitmRPCTimeout)
	current, err := client.ClearSkip(callCtx, &mitmapi.ClearSkipRequest{Host: strings.ToLower(strings.TrimSpace(body.Host))})
	cancel()
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			mitmError(w, http.StatusNotImplemented, "当前核心版本需要更新")
			return
		}
		mitmError(w, http.StatusBadGateway, "清除失败")
		return
	}
	s.mitmStoreStatus(client, current)
	writeJSON(w, http.StatusOK, s.mitmStatus(r.Context()))
}

func mitmError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// mitmTrustError maps a failed trust operation to an HTTP response.
func mitmTrustError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, errTrustCanceled):
		mitmError(w, http.StatusConflict, "已取消授权")
	case errors.Is(err, errTrustUnsupported):
		mitmError(w, http.StatusNotImplemented, "当前平台不支持安装信任")
	default:
		mitmError(w, http.StatusInternalServerError, action+"失败")
	}
}

func (s *server) mitmCreateCA(w http.ResponseWriter, r *http.Request) {
	s.mitm.opMu.Lock()
	defer s.mitm.opMu.Unlock()
	if _, state := s.mitmRootSnapshot(); state != mitmCAStateAbsent {
		mitmError(w, http.StatusConflict, "根证书已存在")
		return
	}
	if err := s.mitmGenerate(); err != nil {
		mitmError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mitmSyncNow(r.Context())
	writeJSON(w, http.StatusOK, s.mitmStatusRead(r.Context(), true))
}

// mitmRegenerateCA removes trust in the old root certificate first and keeps the
// old root if that fails.
func (s *server) mitmRegenerateCA(w http.ResponseWriter, r *http.Request) {
	s.mitm.opMu.Lock()
	defer s.mitm.opMu.Unlock()
	root, _ := s.mitmRootSnapshot()
	wasTrusted := s.mitmWasTrusted()
	if err := s.mitmRemoveTrust(root); err != nil {
		mitmTrustError(w, err, "移除信任")
		return
	}
	if err := s.mitmGenerate(); err != nil {
		s.mitmRestoreTrust(root, wasTrusted)
		mitmError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mitmSyncNow(r.Context())
	writeJSON(w, http.StatusOK, s.mitmStatusRead(r.Context(), true))
}

// mitmDeleteCA removes trust, then deletes the private key and certificate and
// clears the intermediate CA from the core.
func (s *server) mitmDeleteCA(w http.ResponseWriter, r *http.Request) {
	s.mitm.opMu.Lock()
	defer s.mitm.opMu.Unlock()
	root, _ := s.mitmRootSnapshot()
	wasTrusted := s.mitmWasTrusted()
	if err := s.mitmRemoveTrust(root); err != nil {
		mitmTrustError(w, err, "移除信任")
		return
	}
	if err := s.mitmRemoveFiles(); err != nil {
		s.mitmRestoreTrust(root, wasTrusted)
		mitmError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.mitmSyncNow(r.Context())
	writeJSON(w, http.StatusOK, s.mitmStatusRead(r.Context(), true))
}

// mitmWasTrusted reports, before a user operation, whether the current root
// certificate is trusted. It may read the credential store.
func (s *server) mitmWasTrusted() bool {
	s.mitmInvalidateTrust()
	s.mitmRefreshTrust(true)
	s.mitm.mu.Lock()
	defer s.mitm.mu.Unlock()
	return s.mitm.state == mitmCAStateReady && s.mitm.trustKnown && s.mitm.trusted
}

// mitmRestoreTrust restores the previous trust when an operation fails and the
// old root certificate is kept.
func (s *server) mitmRestoreTrust(root *x509.Certificate, wasTrusted bool) {
	if root == nil || !wasTrusted {
		return
	}
	trust := s.mitmTrustStore()
	if trust.Supported() {
		_ = trust.Install(root)
	}
	s.mitmInvalidateTrust()
}

func (s *server) mitmInstallTrust(w http.ResponseWriter, r *http.Request) {
	s.mitm.opMu.Lock()
	defer s.mitm.opMu.Unlock()
	trust := s.mitmTrustStore()
	if !trust.Supported() {
		mitmError(w, http.StatusNotImplemented, "当前平台不支持安装信任")
		return
	}
	root, state := s.mitmRootSnapshot()
	if state != mitmCAStateReady {
		mitmError(w, http.StatusConflict, "没有可用的根证书")
		return
	}
	err := trust.Install(root)
	s.mitmInvalidateTrust()
	if err != nil {
		mitmTrustError(w, err, "安装信任")
		return
	}
	writeJSON(w, http.StatusOK, s.mitmStatusRead(r.Context(), true))
}

func (s *server) mitmRemoveTrustHandler(w http.ResponseWriter, r *http.Request) {
	s.mitm.opMu.Lock()
	defer s.mitm.opMu.Unlock()
	if !s.mitmTrustStore().Supported() {
		mitmError(w, http.StatusNotImplemented, "当前平台不支持安装信任")
		return
	}
	root, _ := s.mitmRootSnapshot()
	if root == nil {
		mitmError(w, http.StatusConflict, "没有根证书")
		return
	}
	if err := s.mitmRemoveTrust(root); err != nil {
		mitmTrustError(w, err, "移除信任")
		return
	}
	writeJSON(w, http.StatusOK, s.mitmStatusRead(r.Context(), true))
}

// mitmExport exports only the root certificate.
func (s *server) mitmExport(w http.ResponseWriter, r *http.Request) {
	root, _ := s.mitmRootSnapshot()
	if root == nil {
		mitmError(w, http.StatusNotFound, "没有根证书")
		return
	}
	body, contentType, name, err := mitmExportBody(root, r.URL.Query().Get("format"))
	if err != nil {
		mitmError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
