package syncbackend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type webdavBackend struct {
	cfg      WebDAV
	base     *url.URL
	segments []string
	client   *http.Client
	// legacySegments is the vault file's path before the rename to Kiko,
	// nil for a custom file name.
	legacySegments []string
}

func newWebDAV(c WebDAV) (*webdavBackend, error) {
	base, err := checkURL(c.URL, c.AllowInsecure, "WebDAV 地址")
	if err != nil {
		return nil, err
	}
	path := strings.Trim(strings.TrimSpace(c.Path), "/")
	var legacySegments []string
	if path == "" {
		path = "kiko/" + objectName
		legacySegments = []string{legacyWebDAVDir, legacyObjectName}
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, "\x00\\") {
			return nil, errors.New("WebDAV 文件路径不合法")
		}
	}
	// A path saved before the rename ends in sbcfg-vault.json: the vault
	// moves to kiko-vault.json in the same directory.
	switch last := len(segments) - 1; segments[last] {
	case legacyObjectName:
		legacySegments = slices.Clone(segments)
		segments[last] = objectName
	case objectName:
		if legacySegments == nil {
			legacySegments = append(slices.Clone(segments[:last]), legacyObjectName)
		}
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	return &webdavBackend{cfg: c, base: base, segments: segments, client: newHTTPClient(), legacySegments: legacySegments}, nil
}

// target returns the URL for the first n path segments; when dir is true it ends with /.
func (b *webdavBackend) target(segments []string, dir bool) string {
	u := *b.base
	u.Path = b.base.Path + "/" + strings.Join(segments, "/")
	if dir {
		u.Path += "/"
	}
	return u.String()
}

func (b *webdavBackend) do(ctx context.Context, method, target string, body []byte, header map[string]string) (*response, error) {
	req, err := newRequest(method, target, body, header)
	if err != nil {
		return nil, err
	}
	if b.cfg.Username != "" || b.cfg.Password != "" {
		req.SetBasicAuth(b.cfg.Username, b.cfg.Password)
	}
	return doRequest(ctx, b.client, req)
}

func (b *webdavBackend) objectURL() string { return b.target(b.segments, false) }

func (b *webdavBackend) Get(ctx context.Context) ([]byte, string, error) {
	r, err := b.do(ctx, http.MethodGet, b.objectURL(), nil, nil)
	if err != nil {
		return nil, "", err
	}
	if r.status == http.StatusOK {
		return r.body, normalizeETag(r.header.Get("ETag")), nil
	}
	return nil, "", genericStatusError("读取", r)
}

func (b *webdavBackend) ensureDirs(ctx context.Context) error {
	for i := 1; i < len(b.segments); i++ {
		r, err := b.do(ctx, "MKCOL", b.target(b.segments[:i], true), nil, nil)
		if err != nil {
			return err
		}
		switch {
		case r.status == http.StatusCreated, r.status == http.StatusOK, r.status == http.StatusMethodNotAllowed:
		case r.status == http.StatusConflict:
			return errors.New("WebDAV 上级目录不存在，请检查地址")
		case r.status == http.StatusNotFound:
			return errors.New("WebDAV 地址不存在，请检查地址和路径")
		default:
			return genericStatusError("创建目录", r)
		}
	}
	return nil
}

func (b *webdavBackend) Put(ctx context.Context, data []byte, cond Cond) (string, error) {
	header := map[string]string{"Content-Type": "application/octet-stream"}
	condHeaders(header, cond)
	for attempt := 0; ; attempt++ {
		r, err := b.do(ctx, http.MethodPut, b.objectURL(), data, header)
		if err != nil {
			return "", err
		}
		switch {
		case r.status == http.StatusOK, r.status == http.StatusCreated, r.status == http.StatusNoContent:
			return normalizeETag(r.header.Get("ETag")), nil
		case r.status == http.StatusPreconditionFailed:
			return "", ErrConflict
		// Apache mod_dav returns 403 when the parent directory is missing, so on the first
		// 403 also create the directories and retry once.
		case (r.status == http.StatusConflict || r.status == http.StatusNotFound || r.status == http.StatusForbidden) && attempt == 0:
			if err := b.ensureDirs(ctx); err != nil {
				return "", err
			}
		case r.status == http.StatusConflict:
			return "", errors.New("WebDAV 上级目录不存在，请检查地址")
		default:
			return "", genericStatusError("写入", r)
		}
	}
}

func (b *webdavBackend) Delete(ctx context.Context) error {
	r, err := b.do(ctx, http.MethodDelete, b.objectURL(), nil, nil)
	if err != nil {
		return err
	}
	switch r.status {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return genericStatusError("删除", r)
}

// legacy is the vault file as named before the rename to Kiko, nil for a
// custom file name.
func (b *webdavBackend) legacy() Backend {
	if b.legacySegments == nil {
		return nil
	}
	old := *b
	old.segments, old.legacySegments = b.legacySegments, nil
	return &old
}

func (b *webdavBackend) Test(ctx context.Context) error {
	if err := b.ensureDirs(ctx); err != nil {
		return err
	}
	probe := b.target(append(append([]string{}, b.segments[:len(b.segments)-1]...), probeName), false)
	r, err := b.do(ctx, http.MethodPut, probe, []byte("ok"), map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	switch {
	case r.status == http.StatusOK, r.status == http.StatusCreated, r.status == http.StatusNoContent:
	case r.status == http.StatusUnauthorized:
		return fmt.Errorf("%w：用户名或密码错误", ErrUnauthorized)
	case r.status == http.StatusForbidden:
		return fmt.Errorf("%w：没有写入权限", ErrUnauthorized)
	case r.status == http.StatusNotFound || r.status == http.StatusConflict:
		return errors.New("WebDAV 地址或目录不存在，请检查地址和路径")
	default:
		return genericStatusError("写入", r)
	}
	if r, err = b.do(ctx, http.MethodDelete, probe, nil, nil); err != nil {
		return err
	}
	switch r.status {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return genericStatusError("删除", r)
}

func (b *webdavBackend) location() string {
	return "webdav " + b.base.String() + " " + strings.Join(b.segments, "/")
}

// migrationBase is where the old backup is kept as it was copied: next to
// the backup file.
func (b *webdavBackend) migrationBase() Backend {
	base := *b
	base.segments = append(slices.Clone(b.segments[:len(b.segments)-1]), migrationBaseName)
	base.legacySegments = nil
	return &base
}
