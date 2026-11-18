package syncbackend

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var s3BucketPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,254}$`)

type s3Backend struct {
	cfg    S3
	base   *url.URL
	prefix string
	client *http.Client
	now    func() time.Time
	// name is the vault object's name under prefix.
	name string
}

func newS3(c S3) (*s3Backend, error) {
	base, err := checkURL(c.Endpoint, c.AllowInsecure, "S3 Endpoint")
	if err != nil {
		return nil, err
	}
	if base.Path != "" && base.Path != "/" {
		return nil, errors.New("S3 Endpoint 不要带路径，例如 https://s3.amazonaws.com")
	}
	base.Path, base.RawPath = "", ""
	if !s3BucketPattern.MatchString(strings.TrimSpace(c.Bucket)) {
		return nil, errors.New("请填写正确的存储桶名称 Bucket")
	}
	c.Bucket = strings.TrimSpace(c.Bucket)
	if strings.TrimSpace(c.AccessKey) == "" || strings.TrimSpace(c.SecretKey) == "" {
		return nil, errors.New("请填写 Access Key 和 Secret Key")
	}
	if c.Region = strings.TrimSpace(c.Region); c.Region == "" {
		c.Region = "us-east-1"
	}
	host := base.Hostname()
	if !c.PathStyle && (net.ParseIP(host) != nil || strings.EqualFold(host, "localhost")) {
		return nil, errors.New("IP 地址或 localhost 形式的 Endpoint 需要启用 path-style")
	}
	prefix := strings.Trim(strings.TrimSpace(c.Prefix), "/")
	if prefix != "" {
		prefix += "/"
	}
	return &s3Backend{cfg: c, base: base, prefix: prefix, client: newHTTPClient(), now: time.Now, name: objectName}, nil
}

// awsEncode encodes s per SigV4 rules: only A-Za-z0-9-_.~ are kept, and optionally /.
func awsEncode(value string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && keepSlash:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// objectURL builds the object URL for path-style or virtual-host addressing and keeps
// the request-line path identical to the one used for signing.
func (b *s3Backend) objectURL(name string) *url.URL {
	u := *b.base
	key := b.prefix + name
	path := "/" + key
	if b.cfg.PathStyle {
		path = "/" + b.cfg.Bucket + "/" + key
	} else {
		u.Host = b.cfg.Bucket + "." + b.base.Host
	}
	u.Path = path
	u.RawPath = awsEncode(path, true)
	return &u
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func signingKey(secret, date, region, service string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	return hmacSHA256(key, "aws4_request")
}

// sigV4Authorization computes the Authorization header; header keys must be
// lowercase and all of them are signed.
func sigV4Authorization(method, canonicalURI, canonicalQuery string, headers map[string]string,
	payloadHash, accessKey, secretKey, region, service, amzDate string,
) string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + strings.Join(strings.Fields(headers[name]), " ") + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonical := method + "\n" + canonicalURI + "\n" + canonicalQuery + "\n" + canonicalHeaders.String() + "\n" + signedHeaders + "\n" + payloadHash
	scope := amzDate[:8] + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	signature := hex.EncodeToString(hmacSHA256(signingKey(secretKey, amzDate[:8], region, service), stringToSign))
	return "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

func (b *s3Backend) do(ctx context.Context, method, name string, body []byte, header map[string]string) (*response, error) {
	u := b.objectURL(name)
	payloadHash := sha256Hex(body)
	amzDate := b.now().UTC().Format("20060102T150405Z")
	if header == nil {
		header = map[string]string{}
	}
	header["X-Amz-Date"] = amzDate
	header["X-Amz-Content-Sha256"] = payloadHash
	req, err := newRequest(method, u.String(), body, header)
	if err != nil {
		return nil, err
	}
	req.Host = u.Host
	req.Header.Set("Authorization", sigV4Authorization(method, u.EscapedPath(), "", map[string]string{
		"host": u.Host, "x-amz-content-sha256": payloadHash, "x-amz-date": amzDate,
	}, payloadHash, b.cfg.AccessKey, b.cfg.SecretKey, b.cfg.Region, "s3", amzDate))
	return doRequest(ctx, b.client, req)
}

type s3ErrorBody struct {
	Code string `xml:"Code"`
}

func s3Error(op string, r *response) error {
	var body s3ErrorBody
	_ = xml.Unmarshal(r.body, &body)
	switch body.Code {
	case "NoSuchKey":
		return ErrNotFound
	case "NoSuchBucket":
		return errors.New("存储桶不存在，请检查 Bucket 名称、Region 和 Endpoint")
	case "InvalidAccessKeyId":
		return fmt.Errorf("%w：Access Key 无效", ErrUnauthorized)
	case "SignatureDoesNotMatch":
		return fmt.Errorf("%w：签名不匹配，请检查 Secret Key、Region 和 path-style 设置", ErrUnauthorized)
	case "AccessDenied", "AllAccessDisabled":
		return fmt.Errorf("%w：访问被拒绝，请确认密钥对该桶有读写权限", ErrUnauthorized)
	case "RequestTimeTooSkewed":
		return errors.New("本机时间与服务器相差过大，请校准系统时间")
	case "PreconditionFailed", "ConditionalRequestConflict":
		return ErrConflict
	case "AuthorizationHeaderMalformed", "PermanentRedirect", "IllegalLocationConstraintException":
		if region := r.header.Get("X-Amz-Bucket-Region"); region != "" {
			return fmt.Errorf("存储桶位于 %s 区域，请把 Region 改为 %s", region, region)
		}
		return errors.New("Region 或 Endpoint 不正确，请检查设置")
	}
	if region := r.header.Get("X-Amz-Bucket-Region"); region != "" && r.status >= 300 && r.status < 400 {
		return fmt.Errorf("存储桶位于 %s 区域，请把 Region 改为 %s", region, region)
	}
	return genericStatusError(op, r)
}

func (b *s3Backend) Get(ctx context.Context) ([]byte, string, error) {
	r, err := b.do(ctx, http.MethodGet, b.name, nil, nil)
	if err != nil {
		return nil, "", err
	}
	if r.status == http.StatusOK {
		return r.body, normalizeETag(r.header.Get("ETag")), nil
	}
	return nil, "", s3Error("读取", r)
}

func (b *s3Backend) put(ctx context.Context, name string, data []byte, cond Cond) (string, error) {
	header := map[string]string{"Content-Type": "application/octet-stream"}
	condHeaders(header, cond)
	r, err := b.do(ctx, http.MethodPut, name, data, header)
	if err != nil {
		return "", err
	}
	if r.status == http.StatusOK {
		return normalizeETag(r.header.Get("ETag")), nil
	}
	err = s3Error("写入", r)
	if errors.Is(err, ErrNotFound) && cond.IfMatch != "" {
		return "", ErrConflict
	}
	return "", err
}

func (b *s3Backend) Put(ctx context.Context, data []byte, cond Cond) (string, error) {
	return b.put(ctx, b.name, data, cond)
}

func (b *s3Backend) delete(ctx context.Context, name string) error {
	r, err := b.do(ctx, http.MethodDelete, name, nil, nil)
	if err != nil {
		return err
	}
	if r.status == http.StatusNoContent || r.status == http.StatusOK || r.status == http.StatusNotFound {
		if r.status == http.StatusNotFound && strings.Contains(string(r.body), "NoSuchBucket") {
			return s3Error("删除", r)
		}
		return nil
	}
	return s3Error("删除", r)
}

func (b *s3Backend) Delete(ctx context.Context) error { return b.delete(ctx, b.name) }

// legacy is the vault object as named before the rename to Kiko.
func (b *s3Backend) legacy() Backend {
	old := *b
	old.name = legacyObjectName
	return &old
}

// Test verifies the Endpoint, Region, bucket and read/write permissions by writing and
// then deleting a probe object (it does not require the ListBucket permission).
func (b *s3Backend) Test(ctx context.Context) error {
	if _, err := b.put(ctx, probeName, []byte("ok"), Cond{}); err != nil {
		return err
	}
	return b.delete(ctx, probeName)
}

func (b *s3Backend) location() string { return "s3 " + b.objectURL(b.name).String() }

// migrationBase is where the old backup is kept as it was copied.
func (b *s3Backend) migrationBase() Backend {
	base := *b
	base.name = migrationBaseName
	return &base
}
