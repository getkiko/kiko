// Package syncbackend provides the three storage backends used for config sync:
// WebDAV, S3-compatible storage, and a self-hosted Cloudflare KV Worker.
package syncbackend

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// Masked is the placeholder returned to the frontend in place of secret fields.
	Masked = "********"

	objectName = "kiko-vault.json"
	probeName  = "kiko-write-test"
	maxBody    = 32 << 20

	// legacyObjectName and legacyWebDAVDir are the backup file name and default WebDAV
	// directory from before the rename to Kiko; they are used only to migrate old backups to objectName.
	legacyObjectName = "sbcfg-vault.json"
	legacyWebDAVDir  = "sbcfg"
	// migrationBaseName holds the old backup as it was copied, kept while the
	// moved marker could not be written: the common ancestor for merging what
	// an old client writes afterwards.
	migrationBaseName = "kiko-vault.migration-base.json"
)

var (
	ErrNotFound     = errors.New("远端对象不存在")
	ErrConflict     = errors.New("远端对象已被其他设备修改")
	ErrUnauthorized = errors.New("认证失败或没有权限")
)

// Cond describes a conditional write: IfMatch is the expected ETag, and IfNoneMatch
// creates the object only if it does not exist yet.
type Cond struct {
	IfMatch     string
	IfNoneMatch bool
}

type Backend interface {
	Get(ctx context.Context) (data []byte, etag string, err error)
	Put(ctx context.Context, data []byte, cond Cond) (etag string, err error)
	Delete(ctx context.Context) error
	Test(ctx context.Context) error
}

type WebDAV struct {
	URL           string `json:"url"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	Path          string `json:"path"`
	AllowInsecure bool   `json:"allowInsecure"`
}

type S3 struct {
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	Prefix        string `json:"prefix"`
	AccessKey     string `json:"accessKey"`
	SecretKey     string `json:"secretKey"`
	PathStyle     bool   `json:"pathStyle"`
	AllowInsecure bool   `json:"allowInsecure"`
}

type KV struct {
	URL           string `json:"url"`
	Token         string `json:"token"`
	AllowInsecure bool   `json:"allowInsecure"`
}

type Config struct {
	Type   string  `json:"type"`
	WebDAV *WebDAV `json:"webdav,omitempty"`
	S3     *S3     `json:"s3,omitempty"`
	KV     *KV     `json:"kv,omitempty"`
}

// New validates cfg and creates a backend. If the remote only holds a pre-rename
// backup, the backend migrates it to the new file name on the first read.
func New(cfg Config) (Backend, error) {
	b, err := newBackend(cfg)
	if err != nil {
		return nil, err
	}
	return &migratingBackend{current: b}, nil
}

func newBackend(cfg Config) (legacyBackend, error) {
	switch cfg.Type {
	case "webdav":
		if cfg.WebDAV == nil {
			return nil, errors.New("缺少 WebDAV 配置")
		}
		return newWebDAV(*cfg.WebDAV)
	case "s3":
		if cfg.S3 == nil {
			return nil, errors.New("缺少 S3 配置")
		}
		return newS3(*cfg.S3)
	case "kv":
		if cfg.KV == nil {
			return nil, errors.New("缺少 Cloudflare KV 配置")
		}
		return newKV(*cfg.KV)
	default:
		return nil, fmt.Errorf("不支持的备份后端类型: %q", cfg.Type)
	}
}

// legacyBackend is a backend that can report its pre-rename backup location;
// legacy returns nil when there is no old location. location names the
// backup's place (server, bucket and path) without credentials.
type legacyBackend interface {
	Backend
	legacy() Backend
	location() string
	migrationBase() Backend
}

// errMigrationPending reports that another device copied the old backup to the new
// location but the copy cannot be read back yet (e.g. due to KV's eventual consistency).
var errMigrationPending = errors.New("旧备份已迁移到新位置，但暂时读取不到，请稍后重试")

// movedMarkerFormat is the format of the marker that replaces a migrated old backup.
// Clients from before the rename only accept the "sbcfg-vault" format: they report
// any other format as unsupported and stop, and they never take an existing object
// for a missing one, so they cannot recreate the old backup next to the new one.
// The format text is what those clients show in their error.
const movedMarkerFormat = "kiko-vault-moved: 备份已迁移到新版 Kiko，请升级"

// movedMarker is the content written over the old backup once it was migrated.
var movedMarker = []byte(`{"format":"` + movedMarkerFormat + `","v":0,"movedTo":"` + objectName + `"}`)

// isMovedMarker reports whether data is the marker of a migrated old backup.
func isMovedMarker(data []byte) bool {
	var head struct {
		Format string `json:"format"`
	}
	return json.Unmarshal(data, &head) == nil && head.Format == movedMarkerFormat
}

// LegacyBackup is implemented by backends that may still hold a backup written
// before the rename to Kiko next to the current one. A client from before the
// rename keeps writing the old backup until the moved marker replaces it; if
// writing the marker failed, its updates have to be merged into the current
// backup (which needs the vault keys) before the marker is written.
type LegacyBackup interface {
	// PendingLegacy returns the old backup when it holds data that differs
	// from the current backup current, and its ETag; nil data when there is
	// none. An old backup equal to current is retired right away. Checks are
	// throttled per backup location.
	PendingLegacy(ctx context.Context, current []byte) (data []byte, etag string, err error)
	// RetireLegacy writes the moved marker over the old backup, provided it
	// is still data (with ETag etag) as PendingLegacy returned it.
	RetireLegacy(ctx context.Context, data []byte, etag string) error
	// LegacyBase returns the old backup as it was copied to the current
	// location, nil when it was not kept.
	LegacyBase(ctx context.Context) ([]byte, error)
	// RecheckLegacy lets the next PendingLegacy check again at once, for a
	// sync attempt that is retried before its merge was written.
	RecheckLegacy()
}

// legacyCheckInterval is how often a backup location's old backup is checked
// while it has not been retired.
const legacyCheckInterval = 10 * time.Minute

// legacyChecks records per backup location whether its old backup is retired,
// and when it was last checked, for this process.
var legacyChecks = struct {
	sync.Mutex
	retired map[string]bool
	last    map[string]time.Time
}{retired: map[string]bool{}, last: map[string]time.Time{}}

// legacyCheckNow is the clock of the checks; tests replace it.
var legacyCheckNow = time.Now

// dueLegacyCheck reports whether the old backup at location should be checked
// now, recording the check.
func dueLegacyCheck(location string) bool {
	legacyChecks.Lock()
	defer legacyChecks.Unlock()
	now := legacyCheckNow()
	if legacyChecks.retired[location] {
		return false
	}
	if last, ok := legacyChecks.last[location]; ok && now.Sub(last) < legacyCheckInterval {
		return false
	}
	legacyChecks.last[location] = now
	return true
}

// setLegacyRetired records whether the old backup at location is retired.
func setLegacyRetired(location string, retired bool) {
	legacyChecks.Lock()
	defer legacyChecks.Unlock()
	if retired {
		legacyChecks.retired[location] = true
	} else {
		delete(legacyChecks.retired, location)
		delete(legacyChecks.last, location)
	}
}

// migratingBackend falls back to the old location when the new one does not exist:
// it copies the old backup to the new location and then replaces the old file with
// movedMarker, so clients from before the rename fail instead of recreating it.
// When the marker could not be written, PendingLegacy finds the old backup again
// so its updates are merged and the marker written later. Delete clears the new
// location and marks the old one, so a reset backup is not migrated back again.
type migratingBackend struct {
	current legacyBackend
}

func (m *migratingBackend) Get(ctx context.Context) ([]byte, string, error) {
	data, etag, err := m.current.Get(ctx)
	if !errors.Is(err, ErrNotFound) {
		return data, etag, err
	}
	legacy := m.current.legacy()
	if legacy == nil {
		return nil, "", err
	}
	return m.migrate(ctx, legacy, err)
}

// migrate copies the old backup to the new location and writes movedMarker over the
// old one. notFound is returned when there is nothing to migrate.
func (m *migratingBackend) migrate(ctx context.Context, legacy Backend, notFound error) ([]byte, string, error) {
	old, oldEtag, err := legacy.Get(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil, "", notFound
	}
	if err != nil {
		return nil, "", err
	}
	if isMovedMarker(old) {
		// Migrated before; the new backup has since been removed.
		return nil, "", notFound
	}
	etag, err := m.current.Put(ctx, old, Cond{IfNoneMatch: true})
	switch {
	case err == nil:
	case errors.Is(err, ErrConflict):
		// Another device copied the old backup first; the new location wins.
		// Whatever the old backup holds beyond that copy is found by
		// PendingLegacy and merged.
		data, currentEtag, err := m.current.Get(ctx)
		if errors.Is(err, ErrNotFound) {
			return nil, "", errMigrationPending
		}
		if err != nil {
			return nil, "", err
		}
		return data, currentEtag, nil
	default:
		return nil, "", err
	}
	// A marker that cannot be written now (an old client wrote in between,
	// or the write failed) is left to PendingLegacy; the copied version is
	// kept as the base for merging what the old backup holds by then.
	if m.retire(ctx, legacy, old, oldEtag) != nil {
		_, _ = m.current.migrationBase().Put(ctx, old, Cond{})
	}
	return old, etag, nil
}

// retire writes movedMarker over the old backup if it still holds data, read
// with etag. A backend that reports no ETag cannot write conditionally: the old
// backup is read again just before the marker is written, which narrows the
// window an old client could write in to that of the write itself.
func (m *migratingBackend) retire(ctx context.Context, legacy Backend, data []byte, etag string) error {
	location := m.current.location()
	if etag == "" {
		again, _, err := legacy.Get(ctx)
		if err != nil {
			return err
		}
		if !bytes.Equal(again, data) {
			return ErrConflict
		}
	}
	if _, err := legacy.Put(ctx, movedMarker, Cond{IfMatch: etag}); err != nil {
		return err
	}
	setLegacyRetired(location, true)
	// Nothing is merged from the old backup any more.
	_ = m.current.migrationBase().Delete(ctx)
	return nil
}

// PendingLegacy implements LegacyBackup. An old backup that cannot be read, or
// that the caller cannot open, stays in place and is checked again every
// legacyCheckInterval; nothing in it is lost meanwhile.
func (m *migratingBackend) PendingLegacy(ctx context.Context, current []byte) ([]byte, string, error) {
	legacy := m.current.legacy()
	if legacy == nil || !dueLegacyCheck(m.current.location()) {
		return nil, "", nil
	}
	old, etag, err := legacy.Get(ctx)
	if errors.Is(err, ErrNotFound) || (err == nil && isMovedMarker(old)) {
		setLegacyRetired(m.current.location(), true)
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if bytes.Equal(old, current) {
		// The copy is complete; only the marker is missing.
		return nil, "", m.retire(ctx, legacy, old, etag)
	}
	return old, etag, nil
}

// RetireLegacy implements LegacyBackup.
func (m *migratingBackend) RetireLegacy(ctx context.Context, data []byte, etag string) error {
	legacy := m.current.legacy()
	if legacy == nil {
		return nil
	}
	return m.retire(ctx, legacy, data, etag)
}

// LegacyBase implements LegacyBackup.
func (m *migratingBackend) LegacyBase(ctx context.Context) ([]byte, error) {
	data, _, err := m.current.migrationBase().Get(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return data, err
}

// RecheckLegacy implements LegacyBackup.
func (m *migratingBackend) RecheckLegacy() {
	legacyChecks.Lock()
	defer legacyChecks.Unlock()
	delete(legacyChecks.last, m.current.location())
}

func (m *migratingBackend) Put(ctx context.Context, data []byte, cond Cond) (string, error) {
	return m.current.Put(ctx, data, cond)
}

// Delete removes the backup. An old backup is replaced with the moved marker
// rather than deleted: clients from before the rename would otherwise create it
// again, and it would be migrated back.
func (m *migratingBackend) Delete(ctx context.Context) error {
	if err := m.current.Delete(ctx); err != nil {
		return err
	}
	_ = m.current.migrationBase().Delete(ctx)
	legacy := m.current.legacy()
	if legacy == nil {
		return nil
	}
	old, _, err := legacy.Get(ctx)
	if errors.Is(err, ErrNotFound) || (err == nil && isMovedMarker(old)) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := legacy.Put(ctx, movedMarker, Cond{}); err != nil {
		return err
	}
	setLegacyRetired(m.current.location(), true)
	return nil
}

func (m *migratingBackend) Test(ctx context.Context) error { return m.current.Test(ctx) }

func mask(value string) string {
	if value == "" {
		return ""
	}
	return Masked
}

// Redacted returns a copy with secret fields replaced by Masked, for returning to the frontend.
func (c Config) Redacted() Config {
	out := Config{Type: c.Type}
	if c.WebDAV != nil {
		v := *c.WebDAV
		v.Password = mask(v.Password)
		out.WebDAV = &v
	}
	if c.S3 != nil {
		v := *c.S3
		v.SecretKey = mask(v.SecretKey)
		out.S3 = &v
	}
	if c.KV != nil {
		v := *c.KV
		v.Token = mask(v.Token)
		out.KV = &v
	}
	return out
}

// MergeSecrets replaces secret fields that are still Masked in c with the real values from old.
func (c Config) MergeSecrets(old Config) Config {
	out := c
	if c.WebDAV != nil {
		v := *c.WebDAV
		if v.Password == Masked {
			v.Password = ""
			if old.WebDAV != nil {
				v.Password = old.WebDAV.Password
			}
		}
		out.WebDAV = &v
	}
	if c.S3 != nil {
		v := *c.S3
		if v.SecretKey == Masked {
			v.SecretKey = ""
			if old.S3 != nil {
				v.SecretKey = old.S3.SecretKey
			}
		}
		out.S3 = &v
	}
	if c.KV != nil {
		v := *c.KV
		if v.Token == Masked {
			v.Token = ""
			if old.KV != nil {
				v.Token = old.KV.Token
			}
		}
		out.KV = &v
	}
	return out
}

func (c Config) HasSecrets() bool {
	return (c.WebDAV != nil && c.WebDAV.Password != "") ||
		(c.S3 != nil && c.S3.SecretKey != "") ||
		(c.KV != nil && c.KV.Token != "")
}

func checkURL(raw string, allowInsecure bool, what string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("请填写%s", what)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%s格式不正确", what)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s中不要包含用户名和密码，请单独填写", what)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecure {
			return nil, fmt.Errorf("%s使用 http 不安全，请改用 https（局域网设备可勾选“允许 http”）", what)
		}
	default:
		return nil, fmt.Errorf("%s只支持 http 或 https", what)
	}
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return errors.New("重定向次数过多")
			}
			// Never cross hosts or downgrade to http; drop Authorization even on same-host
			// redirects so credentials are not forwarded.
			if !strings.EqualFold(req.URL.Host, via[0].URL.Host) ||
				(via[0].URL.Scheme == "https" && req.URL.Scheme != "https") {
				return http.ErrUseLastResponse
			}
			req.Header.Del("Authorization")
			return nil
		},
	}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func doRequest(ctx context.Context, client *http.Client, req *http.Request) (*response, error) {
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, describeNetError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, describeNetError(err)
	}
	if len(body) > maxBody {
		return nil, errors.New("远端响应超过 32MiB，已拒绝")
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

func newRequest(method, target string, body []byte, header map[string]string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return nil, errors.New("请求地址不合法")
	}
	for key, value := range header {
		req.Header.Set(key, value)
	}
	return req, nil
}

// describeNetError turns a network error into a Chinese, user-facing message that
// contains neither the URL nor credentials.
func describeNetError(err error) error {
	var (
		dnsErr      *net.DNSError
		unknownAuth x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		invalidCert x509.CertificateInvalidError
		recordErr   tls.RecordHeaderError
		netErr      net.Error
	)
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.As(err, &dnsErr):
		return fmt.Errorf("无法解析域名 %s，请检查地址和网络", dnsErr.Name)
	case errors.As(err, &unknownAuth):
		return errors.New("TLS 证书不受信任（可能是自签名证书）")
	case errors.As(err, &hostErr):
		return errors.New("TLS 证书与域名不匹配")
	case errors.As(err, &invalidCert):
		return errors.New("TLS 证书无效或已过期")
	case errors.As(err, &recordErr):
		return errors.New("服务器不是 https 服务，请检查地址的协议和端口")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return errors.New("连接超时，请检查网络和地址")
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("连接被拒绝，服务未运行或端口不对")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("网络错误：%s", err.Error())
}

func redirectError(r *response) error {
	host := ""
	if location, err := url.Parse(r.header.Get("Location")); err == nil {
		host = location.Host
	}
	if host != "" {
		return fmt.Errorf("服务器把请求重定向到了另一个地址（%s），已拒绝跟随；请检查填写的地址", host)
	}
	return errors.New("服务器返回了重定向，已拒绝跟随；请检查填写的地址")
}

func genericStatusError(op string, r *response) error {
	switch {
	case r.status == http.StatusUnauthorized:
		return fmt.Errorf("%w：凭据无效", ErrUnauthorized)
	case r.status == http.StatusForbidden:
		return fmt.Errorf("%w：没有%s权限", ErrUnauthorized, op)
	case r.status == http.StatusNotFound:
		return ErrNotFound
	case r.status == http.StatusConflict, r.status == http.StatusPreconditionFailed:
		return ErrConflict
	case r.status == http.StatusRequestEntityTooLarge:
		return errors.New("数据超过服务器允许的大小")
	case r.status >= 300 && r.status < 400:
		return redirectError(r)
	case r.status >= 500:
		return fmt.Errorf("服务器错误（HTTP %d）", r.status)
	default:
		return fmt.Errorf("服务器返回了意外的状态（HTTP %d）", r.status)
	}
}

// normalizeETag strips quotes and the weak-validator prefix W/. Apache's mod_deflate
// makes GET return W/"x" while If-Match only matches the same tag in strong form,
// so both are normalized to x.
func normalizeETag(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "W/")
	return strings.Trim(value, `"`)
}

func formatETag(value string) string {
	return `"` + value + `"`
}

func condHeaders(header map[string]string, cond Cond) {
	if cond.IfMatch != "" {
		header["If-Match"] = formatETag(cond.IfMatch)
	}
	if cond.IfNoneMatch {
		header["If-None-Match"] = "*"
	}
}
