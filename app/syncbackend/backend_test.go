package syncbackend

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/syncvault"
)

// kvFake emulates the self-hosted Worker: /v1/objects/:name with Bearer auth and ETag conditional writes.
type kvFake struct {
	mu       sync.Mutex
	token    string
	objects  map[string][]byte
	etags    map[string]string
	n        int
	lastAuth string
	// beforePut runs before a PUT is handled, under the lock.
	beforePut func(name string, r *http.Request)
	// beforeGet runs before a GET is handled, under the lock.
	beforeGet func(name string)
	// putStatus, when it returns a status, answers a PUT with it.
	putStatus func(name string) int
	// noETag leaves ETag headers out, as some servers do.
	noETag bool
	gets   map[string]int
}

func newKVFake(token string) *kvFake {
	return &kvFake{token: token, objects: map[string][]byte{}, etags: map[string]string{}}
}

func (k *kvFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastAuth = r.Header.Get("Authorization")
	if k.lastAuth != "Bearer "+k.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	name, ok := strings.CutPrefix(r.URL.Path, "/v1/objects/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		if k.gets == nil {
			k.gets = map[string]int{}
		}
		k.gets[name]++
		if k.beforeGet != nil {
			k.beforeGet(name)
		}
	}
	data, exists := k.objects[name]
	switch r.Method {
	case http.MethodGet:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !k.noETag {
			w.Header().Set("ETag", `"`+k.etags[name]+`"`)
		}
		_, _ = w.Write(data)
	case http.MethodPut:
		if k.beforePut != nil {
			k.beforePut(name, r)
			data, exists = k.objects[name]
		}
		if k.putStatus != nil {
			if status := k.putStatus(name); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		if r.Header.Get("If-None-Match") == "*" && exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if match := r.Header.Get("If-Match"); match != "" && (!exists || match != `"`+k.etags[name]+`"`) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		k.n++
		k.objects[name] = body
		k.etags[name] = "e" + strconv.Itoa(k.n)
		if !k.noETag {
			w.Header().Set("ETag", `"`+k.etags[name]+`"`)
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(k.objects, name)
		delete(k.etags, name)
		w.WriteHeader(http.StatusNoContent)
	}
}

func newKVBackend(t *testing.T, url, token string) Backend {
	t.Helper()
	b, err := New(Config{Type: "kv", KV: &KV{URL: url, Token: token, AllowInsecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKVRoundTripAndConditionalWrites(t *testing.T) {
	fake := newKVFake("tok")
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	ctx := context.Background()

	if _, _, err := b.Get(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	e1, err := b.Put(ctx, []byte("one"), Cond{IfNoneMatch: true})
	if err != nil || e1 == "" {
		t.Fatalf("create: %q %v", e1, err)
	}
	if _, err = b.Put(ctx, []byte("again"), Cond{IfNoneMatch: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create twice should conflict: %v", err)
	}
	e2, err := b.Put(ctx, []byte("two"), Cond{IfMatch: e1})
	if err != nil || e2 == e1 {
		t.Fatalf("update: %q %v", e2, err)
	}
	if _, err = b.Put(ctx, []byte("stale"), Cond{IfMatch: e1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale etag should conflict: %v", err)
	}
	data, etag, err := b.Get(ctx)
	if err != nil || string(data) != "two" || etag != e2 {
		t.Fatalf("get: %q %q %v", data, etag, err)
	}
	if err = b.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err = b.Delete(ctx); err != nil {
		t.Fatalf("deleting a missing object must succeed: %v", err)
	}
	if _, _, err = b.Get(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err = b.Test(ctx); err != nil {
		t.Fatalf("test: %v", err)
	}
	if len(fake.objects) != 0 {
		t.Fatalf("probe object must be removed: %v", fake.objects)
	}
}

func TestUnauthorizedDoesNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(newKVFake("right"))
	defer server.Close()
	b := newKVBackend(t, server.URL, "wrong-token-123")
	_, _, err := b.Get(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want unauthorized, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token-123") {
		t.Fatalf("error leaks the token: %v", err)
	}
	if err = b.Test(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("test should report unauthorized: %v", err)
	}
}

func TestConnectionErrorsDoNotLeakSecrets(t *testing.T) {
	server := httptest.NewServer(newKVFake("t"))
	closedURL := server.URL
	server.Close()
	b := newKVBackend(t, closedURL, "super-secret-token")
	_, _, err := b.Get(context.Background())
	if err == nil || strings.Contains(err.Error(), "super-secret-token") || strings.Contains(err.Error(), closedURL) {
		t.Fatalf("unexpected error: %v", err)
	}

	tlsServer := httptest.NewTLSServer(newKVFake("t"))
	defer tlsServer.Close()
	b, err = New(Config{Type: "kv", KV: &KV{URL: tlsServer.URL, Token: "super-secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = b.Get(context.Background())
	if err == nil || !strings.Contains(err.Error(), "证书") || strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("expected a readable TLS error, got %v", err)
	}
}

func TestResponseOverLimitIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 33; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	b := newKVBackend(t, server.URL, "t")
	if _, _, err := b.Get(context.Background()); err == nil || !strings.Contains(err.Error(), "32MiB") {
		t.Fatalf("expected size limit error, got %v", err)
	}
}

func TestCrossHostRedirectIsNotFollowed(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte("leaked"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/objects/kiko-vault.json", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	b := newKVBackend(t, origin.URL, "bearer-secret")
	_, _, err := b.Get(context.Background())
	if err == nil || !strings.Contains(err.Error(), "重定向") {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("redirect target must not be contacted, got Authorization %q", hits)
	}
}

func TestSameHostRedirectDropsAuthorization(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/moved" {
			seen = append(seen, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	b := newKVBackend(t, server.URL, "bearer-secret")
	if data, _, err := b.Get(context.Background()); err != nil || string(data) != "ok" {
		t.Fatalf("same-host redirect: %q %v", data, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("Authorization must be dropped on redirect, got %q", seen)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	cases := map[string]Config{
		"kv http rejected":       {Type: "kv", KV: &KV{URL: "http://example.com", Token: "t"}},
		"kv token missing":       {Type: "kv", KV: &KV{URL: "https://example.com"}},
		"kv userinfo rejected":   {Type: "kv", KV: &KV{URL: "https://u:p@example.com", Token: "t"}},
		"webdav http rejected":   {Type: "webdav", WebDAV: &WebDAV{URL: "http://example.com/dav"}},
		"webdav bad path":        {Type: "webdav", WebDAV: &WebDAV{URL: "https://example.com/dav", Path: "a/../b"}},
		"s3 http rejected":       {Type: "s3", S3: &S3{Endpoint: "http://s3.example.com", Bucket: "bucket", AccessKey: "a", SecretKey: "s"}},
		"s3 secret missing":      {Type: "s3", S3: &S3{Endpoint: "https://s3.example.com", Bucket: "bucket", AccessKey: "a"}},
		"s3 ip needs path style": {Type: "s3", S3: &S3{Endpoint: "https://10.0.0.1:9000", Bucket: "bucket", AccessKey: "a", SecretKey: "s"}},
		"s3 endpoint path":       {Type: "s3", S3: &S3{Endpoint: "https://s3.example.com/x", Bucket: "bucket", AccessKey: "a", SecretKey: "s"}},
		"unknown type":           {Type: "ftp"},
		"missing section":        {Type: "webdav"},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := New(Config{Type: "s3", S3: &S3{Endpoint: "https://10.0.0.1:9000", PathStyle: true, Bucket: "bucket", AccessKey: "a", SecretKey: "s"}}); err != nil {
		t.Errorf("path-style with IP endpoint should be accepted: %v", err)
	}
}

func TestRedactedAndMergeSecrets(t *testing.T) {
	cfg := Config{Type: "webdav", WebDAV: &WebDAV{URL: "https://d", Username: "u", Password: "pw"}}
	red := cfg.Redacted()
	if red.WebDAV.Password != Masked || cfg.WebDAV.Password != "pw" {
		t.Fatalf("redaction must not touch the original: %+v %+v", red.WebDAV, cfg.WebDAV)
	}
	if !cfg.HasSecrets() || (Config{Type: "webdav", WebDAV: &WebDAV{}}).HasSecrets() {
		t.Fatal("HasSecrets mismatch")
	}
	if (Config{Type: "kv", KV: &KV{}}).Redacted().KV.Token != "" {
		t.Fatal("empty secrets must stay empty")
	}
	merged := red.MergeSecrets(cfg)
	if merged.WebDAV.Password != "pw" {
		t.Fatalf("masked password must be restored: %q", merged.WebDAV.Password)
	}
	edited := Config{Type: "webdav", WebDAV: &WebDAV{URL: "https://d", Password: "new"}}.MergeSecrets(cfg)
	if edited.WebDAV.Password != "new" {
		t.Fatalf("explicit password must win: %q", edited.WebDAV.Password)
	}
	s3 := Config{Type: "s3", S3: &S3{SecretKey: "sk"}}
	kv := Config{Type: "kv", KV: &KV{Token: "tk"}}
	if s3.Redacted().S3.SecretKey != Masked || kv.Redacted().KV.Token != Masked {
		t.Fatal("s3/kv secrets must be masked")
	}
	if s3.Redacted().MergeSecrets(s3).S3.SecretKey != "sk" || kv.Redacted().MergeSecrets(kv).KV.Token != "tk" {
		t.Fatal("s3/kv secrets must be restored")
	}
	if got := red.MergeSecrets(Config{}).WebDAV.Password; got != "" {
		t.Fatalf("no old value means empty, got %q", got)
	}
}

// The pre-rename backup sbcfg-vault.json is migrated to kiko-vault.json on the first
// read, and the old file is then replaced with the moved marker.
func TestKVMigratesLegacyVault(t *testing.T) {
	fake := newKVFake("tok")
	fake.objects["sbcfg-vault.json"] = []byte("legacy-cipher")
	fake.etags["sbcfg-vault.json"] = "old"
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	ctx := context.Background()

	data, etag, err := b.Get(ctx)
	if err != nil || string(data) != "legacy-cipher" || etag == "" || etag == "old" {
		t.Fatalf("migrating get: %q %q %v", data, etag, err)
	}
	if string(fake.objects["kiko-vault.json"]) != "legacy-cipher" {
		t.Fatalf("new object: %q", fake.objects["kiko-vault.json"])
	}
	if !isMovedMarker(fake.objects["sbcfg-vault.json"]) {
		t.Fatalf("legacy object not replaced by the marker: %q", fake.objects["sbcfg-vault.json"])
	}
	// After migration the ETag belongs to the new object and works for conditional writes.
	if _, err = b.Put(ctx, []byte("next"), Cond{IfMatch: etag}); err != nil {
		t.Fatalf("conditional write after migration: %v", err)
	}
	if data, _, err = b.Get(ctx); err != nil || string(data) != "next" {
		t.Fatalf("after write: %q %v", data, err)
	}
}

// When both files exist the new one wins and the old one is left untouched.
func TestKVPrefersCurrentVault(t *testing.T) {
	fake := newKVFake("tok")
	fake.objects["sbcfg-vault.json"], fake.etags["sbcfg-vault.json"] = []byte("old"), "o"
	fake.objects["kiko-vault.json"], fake.etags["kiko-vault.json"] = []byte("new"), "n"
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	if data, etag, err := b.Get(context.Background()); err != nil || string(data) != "new" || etag != "n" {
		t.Fatalf("get: %q %q %v", data, etag, err)
	}
	if string(fake.objects["sbcfg-vault.json"]) != "old" {
		t.Fatal("legacy object touched")
	}
}

// Deleting the backup replaces the old file with the moved marker, so a client from
// before the rename cannot create it again and a reset is not migrated back.
func TestKVDeleteRemovesLegacyVault(t *testing.T) {
	fake := newKVFake("tok")
	fake.objects["sbcfg-vault.json"], fake.etags["sbcfg-vault.json"] = []byte("old"), "o"
	fake.objects["kiko-vault.json"], fake.etags["kiko-vault.json"] = []byte("new"), "n"
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	if err := b.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.objects["kiko-vault.json"]; ok || !isMovedMarker(fake.objects["sbcfg-vault.json"]) {
		t.Fatalf("objects: %q", fake.objects)
	}
	if _, _, err := b.Get(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

// An old client that reads the marker gets an unsupported-format error, never
// "not found", so it stops instead of recreating its backup. Clients from before
// the rename validate the header the same way: only "sbcfg-vault" v1 is accepted.
func TestMovedMarkerFailsOldClients(t *testing.T) {
	_, err := syncvault.ParseHeader(movedMarker)
	if !errors.Is(err, syncvault.ErrUnsupported) || !strings.Contains(err.Error(), "请升级") {
		t.Fatalf("marker header: %v", err)
	}
	if !isMovedMarker(movedMarker) || isMovedMarker([]byte(`{"format":"sbcfg-vault","v":1}`)) || isMovedMarker([]byte("x")) {
		t.Fatal("marker detection")
	}
}

// An old client updates the old backup between this device's read and the marker
// write: the conditional marker write fails and the old backup stays, so the next
// check returns it for merging, and the marker is written only with its ETag.
func TestKVMigrationKeepsConcurrentLegacyUpdate(t *testing.T) {
	resetLegacyChecks()
	fake := newKVFake("tok")
	fake.objects["sbcfg-vault.json"], fake.etags["sbcfg-vault.json"] = []byte("v1"), "old"
	raced := false
	fake.beforePut = func(name string, r *http.Request) {
		if name == "sbcfg-vault.json" && !raced {
			raced = true
			fake.objects[name], fake.etags[name] = []byte("v2"), "old2"
		}
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	ctx := context.Background()
	data, _, err := b.Get(ctx)
	if err != nil || string(data) != "v1" || string(fake.objects["sbcfg-vault.json"]) != "v2" {
		t.Fatalf("get: %q %v; objects %q", data, err, fake.objects)
	}
	lb := b.(LegacyBackup)
	old, etag, err := lb.PendingLegacy(ctx, data)
	if err != nil || string(old) != "v2" || etag != "old2" {
		t.Fatalf("pending: %q %q %v", old, etag, err)
	}
	if err := lb.RetireLegacy(ctx, old, "stale"); !errors.Is(err, ErrConflict) || !bytes.Equal(fake.objects["sbcfg-vault.json"], []byte("v2")) {
		t.Fatalf("retire with a stale ETag: %v", err)
	}
	if err := lb.RetireLegacy(ctx, old, etag); err != nil || !isMovedMarker(fake.objects["sbcfg-vault.json"]) {
		t.Fatalf("retire: %v %q", err, fake.objects["sbcfg-vault.json"])
	}
	if old, _, err := lb.PendingLegacy(ctx, data); old != nil || err != nil {
		t.Fatalf("pending after retire: %q %v", old, err)
	}
}

// A marker write that fails for another reason leaves the old backup; the next
// check retires it when it equals the copy, and checks are throttled.
func TestKVLegacyCheckRetriesFailedMarker(t *testing.T) {
	resetLegacyChecks()
	now := time.Unix(1_800_000_000, 0)
	legacyCheckNow = func() time.Time { return now }
	t.Cleanup(func() { legacyCheckNow = time.Now })
	fake := newKVFake("tok")
	fake.objects["sbcfg-vault.json"], fake.etags["sbcfg-vault.json"] = []byte("v1"), "old"
	failing := true
	fake.putStatus = func(name string) int {
		if name == "sbcfg-vault.json" && failing {
			return http.StatusInternalServerError
		}
		return 0
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	ctx := context.Background()
	data, _, err := b.Get(ctx)
	if err != nil || string(data) != "v1" || string(fake.objects["sbcfg-vault.json"]) != "v1" {
		t.Fatalf("get: %q %v", data, err)
	}
	lb := b.(LegacyBackup)
	// The copied version is kept as the base for merging.
	if base, err := lb.LegacyBase(ctx); err != nil || string(base) != "v1" {
		t.Fatalf("base %q %v", base, err)
	}
	if old, _, err := lb.PendingLegacy(ctx, data); old != nil || err == nil {
		t.Fatalf("pending while the write fails: %q %v", old, err)
	}
	failing = false
	gets := fake.gets["sbcfg-vault.json"]
	if old, _, err := lb.PendingLegacy(ctx, data); old != nil || err != nil || fake.gets["sbcfg-vault.json"] != gets {
		t.Fatalf("check not throttled: %q %v", old, err)
	}
	now = now.Add(legacyCheckInterval - time.Second)
	if old, _, err := lb.PendingLegacy(ctx, data); old != nil || err != nil || fake.gets["sbcfg-vault.json"] != gets {
		t.Fatalf("checked within the interval: %q %v", old, err)
	}
	// A retried sync attempt checks again at once.
	lb.RecheckLegacy()
	if old, _, err := lb.PendingLegacy(ctx, data); old != nil || err != nil || !isMovedMarker(fake.objects["sbcfg-vault.json"]) {
		t.Fatalf("retry: %q %v %q", old, err, fake.objects["sbcfg-vault.json"])
	}
	// Retired, the base is no longer kept.
	if base, err := lb.LegacyBase(ctx); err != nil || base != nil {
		t.Fatalf("base after retire %q %v", base, err)
	}
}

// Without ETags the marker cannot be written conditionally: the old backup is read
// again first, and a change since the copy keeps it for merging.
func TestKVMigrationWithoutETag(t *testing.T) {
	fake := newKVFake("tok")
	fake.noETag = true
	fake.objects["sbcfg-vault.json"] = []byte("v1")
	fake.beforeGet = func(name string) {
		if name == "sbcfg-vault.json" && fake.gets[name] == 2 {
			fake.objects[name] = []byte("v2")
		}
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	data, _, err := b.Get(context.Background())
	if err != nil || string(data) != "v1" || string(fake.objects["sbcfg-vault.json"]) != "v2" {
		t.Fatalf("get: %q %v; objects %q", data, err, fake.objects)
	}

	// Unchanged, the marker is written after the second read.
	fake2 := newKVFake("tok")
	fake2.noETag = true
	fake2.objects["sbcfg-vault.json"] = []byte("v1")
	server2 := httptest.NewServer(fake2)
	defer server2.Close()
	if _, _, err := newKVBackend(t, server2.URL, "tok").Get(context.Background()); err != nil || !isMovedMarker(fake2.objects["sbcfg-vault.json"]) {
		t.Fatalf("without a change: %v %q", err, fake2.objects["sbcfg-vault.json"])
	}
}

// Deleting when there was never an old backup creates no marker.
func TestKVDeleteWithoutLegacy(t *testing.T) {
	fake := newKVFake("tok")
	fake.objects["kiko-vault.json"], fake.etags["kiko-vault.json"] = []byte("new"), "n"
	server := httptest.NewServer(fake)
	defer server.Close()
	if err := newKVBackend(t, server.URL, "tok").Delete(context.Background()); err != nil || len(fake.objects) != 0 {
		t.Fatalf("delete: %v %q", err, fake.objects)
	}
}

// A marker left after migration is not copied back when the new backup is gone.
func TestKVMovedMarkerIsNotMigrated(t *testing.T) {
	fake := newKVFake("tok")
	fake.objects["sbcfg-vault.json"], fake.etags["sbcfg-vault.json"] = movedMarker, "m"
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newKVBackend(t, server.URL, "tok")
	if _, _, err := b.Get(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, ok := fake.objects["kiko-vault.json"]; ok || !isMovedMarker(fake.objects["sbcfg-vault.json"]) {
		t.Fatalf("objects: %q", fake.objects)
	}
}

// Another device finished copying first: the new backup wins and this device does
// not write the marker; the next check retires an old backup equal to the copy and
// returns one that differs for merging.
func TestKVMigrationAfterOtherDeviceCopied(t *testing.T) {
	resetLegacyChecks()
	for _, tc := range []struct {
		copied  string
		pending bool
	}{{"v1", false}, {"other", true}} {
		fake := newKVFake("tok")
		fake.objects["sbcfg-vault.json"], fake.etags["sbcfg-vault.json"] = []byte("v1"), "old"
		fake.beforePut = func(name string, r *http.Request) {
			if name == "kiko-vault.json" && r.Header.Get("If-None-Match") == "*" {
				fake.objects[name], fake.etags[name] = []byte(tc.copied), "n"
			}
		}
		server := httptest.NewServer(fake)
		b := newKVBackend(t, server.URL, "tok")
		data, etag, err := b.Get(context.Background())
		if err != nil || string(data) != tc.copied || etag != "n" || isMovedMarker(fake.objects["sbcfg-vault.json"]) {
			t.Fatalf("%s: get %q %q %v", tc.copied, data, etag, err)
		}
		old, _, err := b.(LegacyBackup).PendingLegacy(context.Background(), data)
		server.Close()
		if err != nil || (old != nil) != tc.pending || isMovedMarker(fake.objects["sbcfg-vault.json"]) == tc.pending {
			t.Fatalf("%s: pending %q %v, old %q", tc.copied, old, err, fake.objects["sbcfg-vault.json"])
		}
	}
}

// resetLegacyChecks forgets the checks of earlier tests, whose servers may have
// had the same address.
func resetLegacyChecks() {
	legacyChecks.Lock()
	defer legacyChecks.Unlock()
	legacyChecks.retired, legacyChecks.last = map[string]bool{}, map[string]time.Time{}
}
