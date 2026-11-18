package syncbackend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// davFake is a minimal WebDAV server: directories must be created with MKCOL first,
// PUT requires the parent directory, and ETag conditional writes are supported.
type davFake struct {
	mu     sync.Mutex
	user   string
	pass   string
	dirs   map[string]bool
	files  map[string][]byte
	etags  map[string]string
	n      int
	mkcols []string
	noETag bool
	// Quirks of real servers.
	missingParentStatus int
	forbidPut           bool
	weakGET             bool
}

func newDavFake(user, pass string) *davFake {
	return &davFake{user: user, pass: pass, dirs: map[string]bool{"/dav": true}, files: map[string][]byte{}, etags: map[string]string{}}
}

func (d *davFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != d.user || pass != d.pass {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch r.Method {
	case "MKCOL":
		d.mkcols = append(d.mkcols, p)
		switch {
		case d.dirs[p]:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case !d.dirs[path.Dir(p)]:
			w.WriteHeader(http.StatusConflict)
		default:
			d.dirs[p] = true
			w.WriteHeader(http.StatusCreated)
		}
	case http.MethodPut:
		if d.forbidPut {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !d.dirs[path.Dir(p)] {
			status := http.StatusConflict
			if d.missingParentStatus != 0 {
				status = d.missingParentStatus
			}
			w.WriteHeader(status)
			return
		}
		_, exists := d.files[p]
		if r.Header.Get("If-None-Match") == "*" && exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if match := r.Header.Get("If-Match"); match != "" && (!exists || match != `"`+d.etags[p]+`"`) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		d.n++
		d.files[p] = body
		d.etags[p] = "v" + strconv.Itoa(d.n)
		if !d.noETag {
			w.Header().Set("ETag", `"`+d.etags[p]+`"`)
		}
		if exists {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
	case http.MethodGet:
		data, exists := d.files[p]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !d.noETag {
			w.Header().Set("ETag", `"`+d.etags[p]+`"`)
		}
		_, _ = w.Write(data)
	case http.MethodDelete:
		if _, exists := d.files[p]; !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(d.files, p)
		w.WriteHeader(http.StatusNoContent)
	}
}

func newDavBackend(t *testing.T, url, pass, filePath string) Backend {
	t.Helper()
	b, err := New(Config{Type: "webdav", WebDAV: &WebDAV{URL: url + "/dav", Username: "u", Password: pass, Path: filePath, AllowInsecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWebDAVRoundTripCreatesCollectionsInOrder(t *testing.T) {
	fake := newDavFake("u", "pw")
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "a b/c/vault.json")
	ctx := context.Background()

	if _, _, err := b.Get(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	e1, err := b.Put(ctx, []byte("one"), Cond{IfNoneMatch: true})
	if err != nil || e1 == "" {
		t.Fatalf("create: %q %v", e1, err)
	}
	if got := strings.Join(fake.mkcols, ","); got != "/dav/a b,/dav/a b/c" {
		t.Fatalf("collections must be created parent first, got %q", got)
	}
	if _, err = b.Put(ctx, []byte("dup"), Cond{IfNoneMatch: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second create must conflict: %v", err)
	}
	e2, err := b.Put(ctx, []byte("two"), Cond{IfMatch: e1})
	if err != nil || e2 == e1 {
		t.Fatalf("update: %q %v", e2, err)
	}
	if _, err = b.Put(ctx, []byte("stale"), Cond{IfMatch: e1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale etag must conflict: %v", err)
	}
	data, etag, err := b.Get(ctx)
	if err != nil || string(data) != "two" || etag != e2 {
		t.Fatalf("get: %q %q %v", data, etag, err)
	}
	if err = b.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err = b.Delete(ctx); err != nil {
		t.Fatalf("delete of a missing object must succeed: %v", err)
	}
	if err = b.Test(ctx); err != nil {
		t.Fatalf("test: %v", err)
	}
	for name := range fake.files {
		if strings.Contains(name, probeName) {
			t.Fatalf("probe file left behind: %s", name)
		}
	}
}

// Apache mod_dav returns 403 instead of 409 for a PUT when the parent directory is missing.
func TestWebDAVMissingParentReportedAsForbiddenStillCreatesCollections(t *testing.T) {
	fake := newDavFake("u", "pw")
	fake.missingParentStatus = http.StatusForbidden
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "x/y/vault.json")
	if _, err := b.Put(context.Background(), []byte("one"), Cond{IfNoneMatch: true}); err != nil {
		t.Fatalf("create with 403 on missing parent: %v", err)
	}
	if got := strings.Join(fake.mkcols, ","); got != "/dav/x,/dav/x/y" {
		t.Fatalf("collections must be created, got %q", got)
	}
}

func TestWebDAVForbiddenWithExistingParentIsPermissionError(t *testing.T) {
	fake := newDavFake("u", "pw")
	fake.forbidPut = true
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "")
	if _, err := b.Put(context.Background(), []byte("one"), Cond{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected a permission error, got %v", err)
	}
}

// Apache's mod_deflate makes GET return a weak ETag, while If-Match only matches the
// same tag in strong form.
func TestWebDAVWeakETagFromGetStillMatchesIfMatch(t *testing.T) {
	fake := newDavFake("u", "pw")
	fake.weakGET = true
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "")
	ctx := context.Background()
	e1, err := b.Put(ctx, []byte("one"), Cond{IfNoneMatch: true})
	if err != nil {
		t.Fatal(err)
	}
	_, etag, err := b.Get(ctx)
	if err != nil || strings.HasPrefix(etag, "W/") {
		t.Fatalf("weak prefix must be dropped, got %q %v", etag, err)
	}
	if etag != e1 {
		t.Fatalf("Get and Put must agree on the opaque tag: %q vs %q", etag, e1)
	}
	if _, err = b.Put(ctx, []byte("two"), Cond{IfMatch: etag}); err != nil {
		t.Fatalf("conditional update after a weak GET: %v", err)
	}
}

func TestWebDAVServerWithoutETagReturnsEmpty(t *testing.T) {
	fake := newDavFake("u", "pw")
	fake.noETag = true
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "")
	etag, err := b.Put(context.Background(), []byte("x"), Cond{})
	if err != nil || etag != "" {
		t.Fatalf("expected empty etag, got %q %v", etag, err)
	}
	data, etag, err := b.Get(context.Background())
	if err != nil || string(data) != "x" || etag != "" {
		t.Fatalf("get: %q %q %v", data, etag, err)
	}
}

func TestWebDAVUnauthorizedAndForbidden(t *testing.T) {
	server := httptest.NewServer(newDavFake("u", "pw"))
	defer server.Close()
	b := newDavBackend(t, server.URL, "bad-password-xyz", "")
	for name, err := range map[string]error{
		"get":  func() error { _, _, e := b.Get(context.Background()); return e }(),
		"put":  func() error { _, e := b.Put(context.Background(), []byte("x"), Cond{}); return e }(),
		"test": b.Test(context.Background()),
	} {
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s: want unauthorized, got %v", name, err)
		} else if strings.Contains(err.Error(), "bad-password-xyz") {
			t.Errorf("%s: error leaks the password: %v", name, err)
		}
	}
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer forbidden.Close()
	b = newDavBackend(t, forbidden.URL, "pw", "")
	if err := b.Test(context.Background()); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "权限") {
		t.Fatalf("forbidden test: %v", err)
	}
}

func TestWebDAVServerErrorsAreReadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "")
	if _, _, err := b.Get(context.Background()); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected a 502 error, got %v", err)
	}
}

// Both the default path and an old path ending in sbcfg-vault.json migrate the old
// backup to kiko-vault.json in the same directory and leave the moved marker.
func TestWebDAVMigratesLegacyVault(t *testing.T) {
	for _, tc := range []struct{ path, legacy, current string }{
		{"", "/dav/sbcfg/sbcfg-vault.json", "/dav/kiko/kiko-vault.json"},
		{"sbcfg/sbcfg-vault.json", "/dav/sbcfg/sbcfg-vault.json", "/dav/sbcfg/kiko-vault.json"},
		{"backup/kiko-vault.json", "/dav/backup/sbcfg-vault.json", "/dav/backup/kiko-vault.json"},
	} {
		fake := newDavFake("u", "pw")
		fake.dirs["/dav/sbcfg"], fake.dirs["/dav/kiko"], fake.dirs["/dav/backup"] = true, true, true
		fake.files[tc.legacy], fake.etags[tc.legacy] = []byte("legacy-cipher"), "old"
		server := httptest.NewServer(fake)
		b := newDavBackend(t, server.URL, "pw", tc.path)
		data, _, err := b.Get(context.Background())
		server.Close()
		if err != nil || string(data) != "legacy-cipher" {
			t.Fatalf("%q: get %q %v", tc.path, data, err)
		}
		if string(fake.files[tc.current]) != "legacy-cipher" {
			t.Fatalf("%q: new file %v", tc.path, fake.files)
		}
		if !isMovedMarker(fake.files[tc.legacy]) {
			t.Fatalf("%q: legacy file not replaced by the marker: %q", tc.path, fake.files[tc.legacy])
		}
	}
}

// A custom file name has no old location to migrate from.
func TestWebDAVCustomFileHasNoLegacy(t *testing.T) {
	fake := newDavFake("u", "pw")
	fake.dirs["/dav/x"] = true
	fake.files["/dav/x/sbcfg-vault.json"] = []byte("other")
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newDavBackend(t, server.URL, "pw", "x/vault.json")
	if _, _, err := b.Get(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("custom file: %v", err)
	}
	if _, ok := fake.files["/dav/x/sbcfg-vault.json"]; !ok {
		t.Fatal("unrelated file removed")
	}
}
