package settings

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// This file holds the backup sheet's validation and wording.

// secretMask is the server's placeholder for a saved secret; sending it
// back keeps the saved value.
const secretMask = "********"

// backendTypes are the backup locations in the picker's order.
var backendTypes = []string{api.SyncWebDAV, api.SyncS3, api.SyncKV}

// backendLabel names a backup location.
var backendLabel = map[string]string{
	api.SyncWebDAV: "WebDAV", api.SyncS3: "S3 及兼容存储", api.SyncKV: "Cloudflare KV",
}

// emptyBackend is a new location of a type, with its defaults.
func emptyBackend(typ string) api.SyncBackendConfig {
	switch typ {
	case api.SyncS3:
		return api.SyncBackendConfig{Type: typ, S3: &api.SyncS3Config{Region: "auto", Prefix: "kiko", PathStyle: true}}
	case api.SyncKV:
		return api.SyncBackendConfig{Type: typ, KV: &api.SyncKVConfig{}}
	}
	return api.SyncBackendConfig{Type: api.SyncWebDAV, WebDAV: &api.SyncWebDAVConfig{Path: "kiko"}}
}

const webdavVaultFile = "kiko-vault.json"

// legacyWebDAVVaultFile is the vault file's name before the rename to
// Kiko; the server moves it to webdavVaultFile in the same directory.
const legacyWebDAVVaultFile = "sbcfg-vault.json"

// webdavDirToPath turns the form's directory into the vault file path.
func webdavDirToPath(dir string) string {
	cleaned := strings.Trim(strings.TrimSpace(dir), "/")
	if cleaned == "" {
		return webdavVaultFile
	}
	return cleaned + "/" + webdavVaultFile
}

// webdavPathToDir turns the vault file path into the form's directory.
func webdavPathToDir(path string) string {
	for _, file := range []string{webdavVaultFile, legacyWebDAVVaultFile} {
		if path == file {
			return ""
		}
		if strings.HasSuffix(path, "/"+file) {
			return strings.TrimSuffix(path, "/"+file)
		}
	}
	return path
}

// withDefaults completes a saved location for the form of typ; another
// type starts empty. The WebDAV file path shows as its directory.
func withDefaults(config *api.SyncBackendConfig, typ string) api.SyncBackendConfig {
	base := emptyBackend(typ)
	if config == nil || config.Type != typ {
		return base
	}
	switch typ {
	case api.SyncWebDAV:
		if config.WebDAV != nil {
			c := *config.WebDAV
			c.Path = webdavPathToDir(c.Path)
			base.WebDAV = &c
		}
	case api.SyncS3:
		if config.S3 != nil {
			c := *config.S3
			if c.Region == "" {
				c.Region = base.S3.Region
			}
			base.S3 = &c
		}
	case api.SyncKV:
		if config.KV != nil {
			c := *config.KV
			base.KV = &c
		}
	}
	return base
}

// toWireBackend is the location as the server takes it: the WebDAV
// directory becomes the vault file path.
func toWireBackend(config api.SyncBackendConfig) api.SyncBackendConfig {
	if config.Type != api.SyncWebDAV || config.WebDAV == nil {
		return config
	}
	c := *config.WebDAV
	c.Path = webdavDirToPath(c.Path)
	config.WebDAV = &c
	return config
}

// parseHTTPURL parses an http or https URL, nil otherwise.
func parseHTTPURL(text string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(text))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	return u
}

// backendValidation is a location's problems: errors by field ("webdav.url")
// block saving, warnings only show.
type backendValidation struct {
	Errors   map[string]string
	Warnings []string
}

var (
	dotDotRE  = regexp.MustCompile(`(^|/)\.\.(/|$)`)
	bucketRE  = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	weakStart = regexp.MustCompile(`(?i)^(password|12345678|qwertyui|11111111|abcdefgh)`)
)

func checkURL(v *backendValidation, key, value string, allowInsecure bool, label string) {
	if strings.TrimSpace(value) == "" {
		v.Errors[key] = "请填写 " + label
		return
	}
	u := parseHTTPURL(value)
	if u == nil {
		v.Errors[key] = label + "需要以 https:// 开头的完整地址"
		return
	}
	if u.Scheme == "http" {
		if !allowInsecure {
			v.Errors[key] = "为保护凭据，默认只允许 https；确需使用 http（例如局域网 NAS）请勾选“允许不安全连接”"
		} else {
			v.Warnings = append(v.Warnings, label+"使用 http，账号密码会以明文在网络中传输；备份内容本身已加密。")
		}
	}
}

// validateBackend checks a location before testing or saving it.
func validateBackend(config api.SyncBackendConfig) backendValidation {
	v := backendValidation{Errors: map[string]string{}}
	filled := func(s string) bool { return strings.TrimSpace(s) != "" }
	switch config.Type {
	case api.SyncWebDAV:
		c := config.WebDAV
		if c == nil {
			c = emptyBackend(api.SyncWebDAV).WebDAV
		}
		checkURL(&v, "webdav.url", c.URL, c.AllowInsecure, "WebDAV 地址")
		if filled(c.Username) && !filled(c.Password) {
			v.Errors["webdav.password"] = "填写了用户名，请同时填写密码"
		}
		if dotDotRE.MatchString(c.Path) {
			v.Errors["webdav.path"] = "目录不能包含 .."
		}
	case api.SyncS3:
		c := config.S3
		if c == nil {
			c = emptyBackend(api.SyncS3).S3
		}
		checkURL(&v, "s3.endpoint", c.Endpoint, c.AllowInsecure, "服务地址（Endpoint）")
		if !filled(c.Region) {
			v.Errors["s3.region"] = "请填写区域（不确定时填 auto 或 us-east-1）"
		}
		if !filled(c.Bucket) {
			v.Errors["s3.bucket"] = "请填写存储桶名称"
		} else if !bucketRE.MatchString(strings.TrimSpace(c.Bucket)) {
			v.Errors["s3.bucket"] = "存储桶名称只能包含字母、数字、点和连字符"
		}
		if !filled(c.AccessKey) {
			v.Errors["s3.accessKey"] = "请填写 Access Key"
		}
		if !filled(c.SecretKey) {
			v.Errors["s3.secretKey"] = "请填写 Secret Key"
		}
		if dotDotRE.MatchString(c.Prefix) {
			v.Errors["s3.prefix"] = "前缀不能包含 .."
		}
	default:
		c := config.KV
		if c == nil {
			c = emptyBackend(api.SyncKV).KV
		}
		checkURL(&v, "kv.url", c.URL, c.AllowInsecure, "Worker 地址")
		if !filled(c.Token) {
			v.Errors["kv.token"] = "请填写 Worker 的访问令牌（SYNC_TOKEN）"
		}
	}
	return v
}

// passwordTone colors the strength bar.
type passwordTone int

const (
	toneWeak passwordTone = iota
	toneFair
	toneGood
	toneStrong
)

// minPasswordLength is the shortest backup password.
const minPasswordLength = 8

// passwordStrength rates a password from 0 to 4 for the strength bar: by
// length and character classes, less for weak patterns. A hint only.
func passwordStrength(password string) (score int, label string, tone passwordTone) {
	if password == "" {
		return 0, "", toneWeak
	}
	runes := []rune(password)
	classes := 0
	for _, re := range []string{`[a-z]`, `[A-Z]`, `\d`, `[^a-zA-Z0-9]`} {
		if regexp.MustCompile(re).MatchString(password) {
			classes++
		}
	}
	points := 0
	if len(runes) >= minPasswordLength {
		points++
	}
	if len(runes) >= 12 {
		points++
	}
	if len(runes) >= 16 {
		points++
	}
	if classes >= 3 {
		points++
	}
	distinct := map[rune]bool{}
	for _, r := range runes {
		distinct[r] = true
	}
	if float64(len(distinct)) <= max(3, float64(len(runes))/4) {
		points -= 2
	}
	if weakStart.MatchString(password) {
		points = 0
	}
	if len(runes) < minPasswordLength {
		points = min(points, 0)
	}
	score = max(0, min(4, points))
	labels := []string{"太弱", "较弱", "一般", "较强", "很强"}
	tones := []passwordTone{toneWeak, toneWeak, toneFair, toneGood, toneStrong}
	return score, labels[score], tones[score]
}

// validateNewPassword checks a new password and its confirmation; "" when
// they pass.
func validateNewPassword(password, confirm string) string {
	if utf8.RuneCountInString(password) < minPasswordLength {
		return fmt.Sprintf("密码至少需要 %d 位", minPasswordLength)
	}
	if password != confirm {
		return "两次输入的密码不一致"
	}
	return ""
}

// formatAgo is a past time in milliseconds relative to now, as "5 minutes
// ago".
func formatAgo(timestamp int64, now time.Time) string {
	if timestamp == 0 {
		return ""
	}
	seconds := max(0, roundDiv(now.UnixMilli()-timestamp, 1000))
	if seconds < 45 {
		return "刚刚"
	}
	minutes := roundDiv(seconds, 60)
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟前", minutes)
	}
	hours := roundDiv(minutes, 60)
	if hours < 24 {
		return fmt.Sprintf("%d 小时前", hours)
	}
	return fmt.Sprintf("%d 天前", roundDiv(hours, 24))
}

// roundDiv divides rounding half up, as Math.round.
func roundDiv(a, b int64) int64 {
	q := a / b
	if r := a % b; r*2 >= b {
		q++
	}
	return q
}

// formatSyncResult summarizes what a sync moved.
func formatSyncResult(result *api.SyncResult) string {
	if result == nil {
		return "已是最新"
	}
	var parts []string
	if result.Pulled > 0 {
		parts = append(parts, fmt.Sprintf("拉取 %d 个", result.Pulled))
	}
	if result.Pushed > 0 {
		parts = append(parts, fmt.Sprintf("推送 %d 个", result.Pushed))
	}
	if result.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("删除 %d 个", result.Deleted))
	}
	if result.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("冲突副本 %d 个", result.Conflicts))
	}
	if len(parts) == 0 {
		return "已是最新"
	}
	return strings.Join(parts, "、")
}

// syncTone colors the summary.
type syncTone int

const (
	syncIdle syncTone = iota
	syncOK
	syncWarn
	syncError
)

// syncSummary is the subtitle of the settings row of backups.
func syncSummary(status *api.SyncStatus, now time.Time) (string, syncTone) {
	if status == nil {
		return "端到端加密备份到 WebDAV / S3 / Cloudflare KV", syncIdle
	}
	switch status.State {
	case api.SyncLocked:
		return "已锁定 · 输入密码后即可同步", syncWarn
	case api.SyncNeedsSetup:
		return "还没有云端备份 · 设置密码后创建", syncWarn
	case api.SyncUnlocked:
		switch {
		case status.Syncing:
			return "正在同步…", syncIdle
		case status.LastError != "":
			return "同步出错：" + status.LastError, syncError
		case status.LastSyncedAt != 0:
			text := "上次同步 " + formatAgo(status.LastSyncedAt, now)
			if status.AutoSync {
				text += " · 自动同步已开启"
			}
			return text, syncOK
		}
		return "已解锁 · 尚未同步", syncIdle
	}
	return "未配置 · 端到端加密备份到 WebDAV / S3 / Cloudflare KV", syncIdle
}

var syncErrorCodes = map[string]string{
	"wrong-password": "密码错误，请重新输入",
	"vault-missing":  "云端还没有备份，请先创建",
}

// syncErrorMessage is the message to show for a sync request's failure:
// a known code's wording, else the server's text.
func syncErrorMessage(err error) string {
	const fallback = "操作失败，请稍后重试"
	if err == nil {
		return fallback
	}
	if message, ok := syncErrorCodes[api.SyncErrorCode(err)]; ok {
		return message
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	if message := strings.TrimSpace(err.Error()); message != "" {
		return message
	}
	return fallback
}

// backendHost is the host of a location's address, "" when unknown.
func backendHost(backend *api.SyncBackendConfig) string {
	if backend == nil {
		return ""
	}
	var address string
	switch backend.Type {
	case api.SyncWebDAV:
		if backend.WebDAV != nil {
			address = backend.WebDAV.URL
		}
	case api.SyncS3:
		if backend.S3 != nil {
			address = backend.S3.Endpoint
		}
	default:
		if backend.KV != nil {
			address = backend.KV.URL
		}
	}
	if u := parseHTTPURL(address); u != nil {
		return u.Host
	}
	return ""
}

// backendName names a location, "Cloud" when there is none.
func backendName(backend *api.SyncBackendConfig) string {
	if backend == nil {
		return "云端"
	}
	return backendLabel[backend.Type]
}
