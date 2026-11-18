package profiles

import (
	"errors"
	"net/url"
	"strings"
)

// PrefShareOrigin is the app preference (Env.AppPrefs) holding the share
// address set in Settings, the user's own configuration share service.
// There is no built-in service: unset or "", sharing is unavailable until
// the user enters an address.
const PrefShareOrigin = "configShareOrigin"

// ShareOriginSetting is the Settings page's Open argument that focuses the
// share address.
const ShareOriginSetting = "share-origin"

// PrefLoader reads preferences, as env.Prefs.
type PrefLoader interface {
	Load(key string, v any) bool
}

// ShareOrigin is the share address prefs hold, "" while unset.
func ShareOrigin(prefs PrefLoader) string {
	var origin string
	if prefs == nil || !prefs.Load(PrefShareOrigin, &origin) {
		return ""
	}
	return origin
}

// errNoShareOrigin is the refusal to share without an address.
var errNoShareOrigin = errors.New("未配置分享地址")

// NormalizeShareOrigin checks a share address, returning it trimmed and
// without a trailing slash: an https URL with a host and optional port,
// and no user, path, query or fragment.
func NormalizeShareOrigin(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return "", errNoShareOrigin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.Opaque != "" {
		return "", errors.New("需要 https 地址")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(origin, "#") {
		return "", errors.New("地址不能包含用户名、查询或片段")
	}
	// The share service routes from the root of its origin.
	if strings.Trim(u.Path, "/") != "" {
		return "", errors.New("只填写域名，不要包含路径")
	}
	return u.Scheme + "://" + u.Host, nil
}

// ValidShareOrigin reports what is wrong with a share address, nil when
// NormalizeShareOrigin takes it.
func ValidShareOrigin(origin string) error {
	_, err := NormalizeShareOrigin(origin)
	return err
}

// ShareMaxBytes bounds a shared configuration.
const ShareMaxBytes = 2_000_000

// ShareUploadPage is the upload page of the share service at origin, a
// share address NormalizeShareOrigin takes, where a browser passes the
// Turnstile check the native console cannot show.
func ShareUploadPage(origin string) (string, error) {
	origin, err := NormalizeShareOrigin(origin)
	if err != nil {
		return "", err
	}
	return origin + "/share-upload", nil
}
