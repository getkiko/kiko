package syncbackend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type kvBackend struct {
	cfg    KV
	base   *url.URL
	client *http.Client
	// name is the vault object's name.
	name string
}

func newKV(c KV) (*kvBackend, error) {
	base, err := checkURL(c.URL, c.AllowInsecure, "Worker 地址")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, errors.New("请填写访问令牌 Token")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	return &kvBackend{cfg: c, base: base, client: newHTTPClient(), name: objectName}, nil
}

func (b *kvBackend) objectURL(name string) string {
	u := *b.base
	u.Path = b.base.Path + "/v1/objects/" + name
	return u.String()
}

func (b *kvBackend) do(ctx context.Context, method, name string, body []byte, header map[string]string) (*response, error) {
	if header == nil {
		header = map[string]string{}
	}
	header["Authorization"] = "Bearer " + b.cfg.Token
	req, err := newRequest(method, b.objectURL(name), body, header)
	if err != nil {
		return nil, err
	}
	return doRequest(ctx, b.client, req)
}

func kvStatusError(op string, r *response) error {
	if r.status == http.StatusUnauthorized {
		return fmt.Errorf("%w：Token 无效", ErrUnauthorized)
	}
	return genericStatusError(op, r)
}

func (b *kvBackend) Get(ctx context.Context) ([]byte, string, error) {
	r, err := b.do(ctx, http.MethodGet, b.name, nil, nil)
	if err != nil {
		return nil, "", err
	}
	if r.status == http.StatusOK {
		return r.body, normalizeETag(r.header.Get("ETag")), nil
	}
	return nil, "", kvStatusError("读取", r)
}

func (b *kvBackend) put(ctx context.Context, name string, data []byte, cond Cond) (string, error) {
	header := map[string]string{"Content-Type": "application/octet-stream"}
	condHeaders(header, cond)
	r, err := b.do(ctx, http.MethodPut, name, data, header)
	if err != nil {
		return "", err
	}
	if r.status == http.StatusOK || r.status == http.StatusCreated {
		return normalizeETag(r.header.Get("ETag")), nil
	}
	if r.status == http.StatusNotFound && cond.IfMatch != "" {
		return "", ErrConflict
	}
	return "", kvStatusError("写入", r)
}

func (b *kvBackend) Put(ctx context.Context, data []byte, cond Cond) (string, error) {
	return b.put(ctx, b.name, data, cond)
}

func (b *kvBackend) delete(ctx context.Context, name string) error {
	r, err := b.do(ctx, http.MethodDelete, name, nil, nil)
	if err != nil {
		return err
	}
	switch r.status {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return kvStatusError("删除", r)
}

func (b *kvBackend) Delete(ctx context.Context) error { return b.delete(ctx, b.name) }

// legacy is the vault object as named before the rename to Kiko.
func (b *kvBackend) legacy() Backend {
	old := *b
	old.name = legacyObjectName
	return &old
}

func (b *kvBackend) Test(ctx context.Context) error {
	if _, _, err := b.Get(ctx); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if _, err := b.put(ctx, probeName, []byte("ok"), Cond{}); err != nil {
		return err
	}
	return b.delete(ctx, probeName)
}

func (b *kvBackend) location() string { return "kv " + b.base.String() + " " + b.name }

// migrationBase is where the old backup is kept as it was copied.
func (b *kvBackend) migrationBase() Backend {
	base := *b
	base.name = migrationBaseName
	return &base
}
