package syncbackend

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const awsExampleSecret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"

func TestSigV4AWSVectors(t *testing.T) {
	// Example from the AWS docs, "Deriving the signing key".
	if got := hex.EncodeToString(signingKey(awsExampleSecret, "20120215", "us-east-1", "iam")); got != "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d" {
		t.Fatalf("signing key: %s", got)
	}
	// get-vanilla from the AWS SigV4 test suite.
	got := sigV4Authorization("GET", "/", "", map[string]string{
		"host": "example.amazonaws.com", "x-amz-date": "20150830T123600Z",
	}, sha256Hex(nil), "AKIDEXAMPLE", awsExampleSecret, "us-east-1", "service", "20150830T123600Z")
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got != want {
		t.Fatalf("get-vanilla:\n got %s\nwant %s", got, want)
	}
}

func TestAWSEncode(t *testing.T) {
	if got := awsEncode("a b/é+~._-9", true); got != "a%20b/%C3%A9%2B~._-9" {
		t.Fatalf("keep slash: %s", got)
	}
	if got := awsEncode("a/b", false); got != "a%2Fb" {
		t.Fatalf("encode slash: %s", got)
	}
}

func TestS3ObjectURLStyles(t *testing.T) {
	newBackend := func(pathStyle bool, prefix string) *s3Backend {
		b, err := New(Config{Type: "s3", S3: &S3{Endpoint: "https://s3.example.com:9000", Bucket: "my-bucket", Prefix: prefix, AccessKey: "a", SecretKey: "s", PathStyle: pathStyle}})
		if err != nil {
			t.Fatal(err)
		}
		return b.(*migratingBackend).current.(*s3Backend)
	}
	if got := newBackend(true, "/my prefix/").objectURL(objectName).String(); got != "https://s3.example.com:9000/my-bucket/my%20prefix/kiko-vault.json" {
		t.Fatalf("path-style: %s", got)
	}
	if got := newBackend(false, "").objectURL(objectName).String(); got != "https://my-bucket.s3.example.com:9000/kiko-vault.json" {
		t.Fatalf("virtual-host: %s", got)
	}
}

// s3Fake checks each request's SigV4 signature and payload hash, then handles it with
// object storage semantics.
type s3Fake struct {
	mu      sync.Mutex
	bucket  string
	virtual string // non-empty means virtual-host; the endpoint host name without port
	ak, sk  string
	region  string
	objects map[string][]byte
	etags   map[string]string
	n       int
	hosts   []string
}

func newS3Fake(virtualHost string) *s3Fake {
	return &s3Fake{bucket: "bkt", virtual: virtualHost, ak: "AK", sk: "SK", region: "us-east-1", objects: map[string][]byte{}, etags: map[string]string{}}
}

func s3XMLError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>` + code + `</Code><Message>x</Message></Error>`))
}

func (s *s3Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if hash != sha256Hex(body) {
		s3XMLError(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch")
		return
	}
	uri := r.RequestURI
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		uri = uri[:i]
	}
	amzDate := r.Header.Get("X-Amz-Date")
	want := sigV4Authorization(r.Method, uri, "", map[string]string{
		"host": r.Host, "x-amz-content-sha256": hash, "x-amz-date": amzDate,
	}, hash, s.ak, s.sk, s.region, "s3", amzDate)
	auth := r.Header.Get("Authorization")
	if !strings.Contains(auth, "Credential="+s.ak+"/") {
		s3XMLError(w, http.StatusForbidden, "InvalidAccessKeyId")
		return
	}
	if auth != want {
		s3XMLError(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	s.hosts = append(s.hosts, r.Host)
	var bucket, key string
	if s.virtual != "" {
		host, _, _ := net.SplitHostPort(r.Host)
		bucket = strings.TrimSuffix(host, "."+s.virtual)
		key = strings.TrimPrefix(r.URL.Path, "/")
	} else {
		bucket, key, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	}
	if bucket != s.bucket {
		s3XMLError(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	data, exists := s.objects[key]
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") == "*" && exists {
			s3XMLError(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if match := r.Header.Get("If-Match"); match != "" {
			if !exists {
				s3XMLError(w, http.StatusNotFound, "NoSuchKey")
				return
			}
			if match != `"`+s.etags[key]+`"` {
				s3XMLError(w, http.StatusPreconditionFailed, "PreconditionFailed")
				return
			}
		}
		s.n++
		s.objects[key] = body
		s.etags[key] = "etag" + strconv.Itoa(s.n)
		w.Header().Set("ETag", `"`+s.etags[key]+`"`)
	case http.MethodGet:
		if !exists {
			s3XMLError(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", `"`+s.etags[key]+`"`)
		_, _ = w.Write(data)
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

func newS3Backend(t *testing.T, endpoint string, mutate func(*S3)) Backend {
	t.Helper()
	cfg := S3{Endpoint: endpoint, Bucket: "bkt", Prefix: "my prefix/é", AccessKey: "AK", SecretKey: "SK", PathStyle: true, AllowInsecure: true}
	if mutate != nil {
		mutate(&cfg)
	}
	b, err := New(Config{Type: "s3", S3: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestS3PathStyleRoundTripWithSignatureCheck(t *testing.T) {
	fake := newS3Fake("")
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newS3Backend(t, server.URL, nil)
	ctx := context.Background()

	if _, _, err := b.Get(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	e1, err := b.Put(ctx, []byte("one"), Cond{IfNoneMatch: true})
	if err != nil || e1 != "etag1" {
		t.Fatalf("create: %q %v", e1, err)
	}
	if _, ok := fake.objects["my prefix/é/kiko-vault.json"]; !ok {
		t.Fatalf("object key with non-ASCII prefix not stored correctly: %v", fake.objects)
	}
	if _, err = b.Put(ctx, []byte("dup"), Cond{IfNoneMatch: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create twice must conflict: %v", err)
	}
	e2, err := b.Put(ctx, []byte("two"), Cond{IfMatch: e1})
	if err != nil || e2 == e1 {
		t.Fatalf("update: %q %v", e2, err)
	}
	if _, err = b.Put(ctx, []byte("stale"), Cond{IfMatch: e1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale etag must conflict: %v", err)
	}
	if err = b.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Put(ctx, []byte("x"), Cond{IfMatch: e2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("If-Match on a missing object must be a conflict: %v", err)
	}
	if err = b.Delete(ctx); err != nil {
		t.Fatalf("delete of a missing object must succeed: %v", err)
	}
	if err = b.Test(ctx); err != nil {
		t.Fatalf("test: %v", err)
	}
	if len(fake.objects) != 0 {
		t.Fatalf("probe object left behind: %v", fake.objects)
	}
}

func TestS3VirtualHostAddressing(t *testing.T) {
	fake := newS3Fake("s3.test")
	server := httptest.NewServer(fake)
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	b := newS3Backend(t, "http://s3.test:"+port, func(c *S3) { c.PathStyle = false })
	// Dial every host name to the test server so the Host header and signature can be verified.
	b.(*migratingBackend).current.(*s3Backend).client.Transport = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	if _, err := b.Put(context.Background(), []byte("v"), Cond{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.hosts[0]; got != "bkt.s3.test:"+port {
		t.Fatalf("host header: %s", got)
	}
	data, _, err := b.Get(context.Background())
	if err != nil || string(data) != "v" {
		t.Fatalf("get: %q %v", data, err)
	}
}

func TestS3ErrorsAreReadable(t *testing.T) {
	server := httptest.NewServer(newS3Fake(""))
	defer server.Close()
	ctx := context.Background()

	if err := newS3Backend(t, server.URL, func(c *S3) { c.SecretKey = "wrong-secret-abc" }).Test(ctx); !errors.Is(err, ErrUnauthorized) ||
		!strings.Contains(err.Error(), "签名") || strings.Contains(err.Error(), "wrong-secret-abc") {
		t.Fatalf("bad secret: %v", err)
	}
	if err := newS3Backend(t, server.URL, func(c *S3) { c.AccessKey = "OTHER" }).Test(ctx); !errors.Is(err, ErrUnauthorized) ||
		!strings.Contains(err.Error(), "Access Key") {
		t.Fatalf("bad access key: %v", err)
	}
	err := newS3Backend(t, server.URL, func(c *S3) { c.Bucket = "missing" }).Test(ctx)
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "存储桶") {
		t.Fatalf("missing bucket must not look like a missing object: %v", err)
	}
	// Region mismatch: 301 with x-amz-bucket-region.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amz-Bucket-Region", "eu-west-1")
		s3XMLError(w, http.StatusMovedPermanently, "PermanentRedirect")
	}))
	defer redirect.Close()
	if err := newS3Backend(t, redirect.URL, nil).Test(ctx); err == nil || !strings.Contains(err.Error(), "eu-west-1") {
		t.Fatalf("region hint: %v", err)
	}
}

func TestS3RequestUsesInjectedClock(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Amz-Date")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	b := newS3Backend(t, server.URL, nil)
	b.(*migratingBackend).current.(*s3Backend).now = func() time.Time { return time.Date(2026, 9, 30, 8, 1, 2, 0, time.FixedZone("x", 8*3600)) }
	_, _, _ = b.Get(context.Background())
	if got != "20260930T000102Z" {
		t.Fatalf("X-Amz-Date must be UTC: %s", got)
	}
}
