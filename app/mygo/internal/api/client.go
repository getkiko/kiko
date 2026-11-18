// Package api is the native console's client for kiko-server's HTTP API
// (/api/v1): one method per endpoint, JSON in and out, plain-text errors
// surfaced as *Error.
//
// Methods are safe from any goroutine and never touch the UI; callers post
// results back to the view through env.Env.Post.
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client talks to one kiko-server origin, such as http://127.0.0.1:9099
// for the desktop's own server or https://nas:9443 for a paired remote one.
type Client struct {
	base  string
	state atomic.Pointer[transport]
	// info is the server's GET /info, cached for the capabilities applies
	// depend on; dropped when the event stream ends (the server may have
	// restarted as another version) or the client is reconfigured.
	info infoCache
}

// infoCache holds the last GET /info. Each drop starts a new generation; a
// response to a request sent before it is not stored.
type infoCache struct {
	mu   sync.Mutex
	gen  uint64
	info *ServerInfo
}

// generation is the current generation, taken before a request.
func (c *infoCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// store keeps info read in generation gen, if it is still current.
func (c *infoCache) store(gen uint64, info ServerInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen == c.gen {
		c.info = &info
	}
}

func (c *infoCache) load() *ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// drop forgets the info and any response still on its way.
func (c *infoCache) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.info = nil
}

// transport is what a client sends its requests with.
type transport struct {
	http *http.Client
	// stream has no overall timeout: event and NDJSON streams stay open.
	stream *http.Client
	tls    *tls.Config
	token  string
}

// Options configure a client of a remote server.
type Options struct {
	// Token is the device's pairing token, sent as a Bearer token with
	// every request.
	Token string
	// Fingerprint pins the server's certificate: the SHA-256 of its leaf
	// certificate's DER. A mismatch fails with *FingerprintError. Empty
	// verifies the certificate as usual.
	Fingerprint []byte
}

// requestTimeout is the overall limit of an ordinary request; a variable so
// tests can shorten it.
var requestTimeout = 60 * time.Second

// New returns a client for the trusted loopback server at origin.
func New(origin string) *Client { return NewWith(origin, Options{}) }

// NewWith returns a client for the server at origin with o's token and
// certificate pin. Every request of the client, its streams and uploads
// included, carries them.
func NewWith(origin string, o Options) *Client {
	c := &Client{base: strings.TrimRight(origin, "/")}
	c.Reconfigure(o)
	return c
}

// Reconfigure replaces the token and pin, as after pairing again.
// Requests already sent keep the old ones.
func (c *Client) Reconfigure(o Options) {
	t := &transport{token: o.Token}
	base := http.DefaultTransport.(*http.Transport).Clone()
	if len(o.Fingerprint) > 0 {
		t.tls = pinnedTLS(o.Fingerprint)
		base.TLSClientConfig = t.tls.Clone()
	}
	var rt http.RoundTripper = base
	if o.Token != "" {
		rt = bearer{base: base, token: o.Token}
	}
	t.http = &http.Client{Timeout: requestTimeout, Transport: rt}
	t.stream = &http.Client{Transport: rt}
	if old := c.state.Swap(t); old != nil {
		old.http.CloseIdleConnections()
	}
	c.info.drop()
}

func (c *Client) http() *http.Client   { return c.state.Load().http }
func (c *Client) stream() *http.Client { return c.state.Load().stream }

// bearer adds the device token to each request.
type bearer struct {
	base  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}

// TLSConfig is the pinned TLS configuration for connections the client
// does not make itself (the SSH WebSocket); nil when nothing is pinned.
func (c *Client) TLSConfig() *tls.Config {
	if t := c.state.Load(); t.tls != nil {
		return t.tls.Clone()
	}
	return nil
}

// Header is what such connections send: the Bearer token, if any.
func (c *Client) Header() http.Header {
	h := http.Header{}
	if token := c.state.Load().token; token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return h
}

// Origin is the server origin the client was created with.
func (c *Client) Origin() string { return c.base }

// URL returns the absolute URL of an API path such as "/info".
func (c *Client) URL(path string) string { return c.base + "/api/v1" + path }

// Error is a non-2xx response. Message is the server's text, or the
// "error" field of a JSON body.
type Error struct {
	Status  int
	Message string
	// Body is the raw response, for 409 responses that carry the current
	// state (PUT /store).
	Body []byte
	// Lock is who held the write gate, for 423 responses.
	Lock *LockState
}

func (e *Error) Error() string {
	if e.Message == "" {
		switch e.Status {
		case http.StatusUnauthorized:
			return "此设备已被吊销或令牌失效"
		case http.StatusLocked:
			if e.Lock != nil && e.Lock.Holder.Name != "" {
				return "「" + e.Lock.Holder.Name + "」正在" + e.Lock.Op + "，请稍后重试"
			}
			return "服务端正忙，请稍后重试"
		}
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return e.Message
}

// Is makes a 401 match ErrUnauthorized.
func (e *Error) Is(target error) bool {
	return target == ErrUnauthorized && e.Status == http.StatusUnauthorized
}

// ErrUnauthorized is a 401 of a remote server: the device was revoked or
// its token is wrong. errors.Is matches the *Error carrying it.
var ErrUnauthorized = errors.New("此设备已被吊销或令牌失效")

// IsLocked reports whether err is a 423 of the server's write gate,
// returning who holds it.
func IsLocked(err error) (*Error, bool) {
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusLocked {
		return apiErr, true
	}
	return nil, false
}

// IsStatus reports whether err is an *Error with the given status.
func IsStatus(err error, status int) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == status
}

func errorFrom(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	message := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		message = payload.Error
	}
	out := &Error{Status: resp.StatusCode, Message: message, Body: body}
	if resp.StatusCode == http.StatusLocked {
		var lock struct {
			Holder LockHolder `json:"holder"`
			Op     string     `json:"op"`
			Since  string     `json:"since"`
		}
		if json.Unmarshal(body, &lock) == nil {
			out.Lock = &LockState{Held: true, Holder: lock.Holder, Op: lock.Op, Since: lock.Since}
		}
	}
	return out
}

// do sends a request with an optional JSON body and decodes a JSON reply
// into out (nil to discard it).
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return c.doWith(c.http(), ctx, method, path, in, out)
}

// doWith is do over client: the same pinning, token and error
// classification, with the overall timeout of that client.
func (c *Client) doWith(client *http.Client, ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := client.Do(req)
	err = transportError(err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFrom(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Get decodes GET path into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Send performs method on path with a JSON body.
func (c *Client) Send(ctx context.Context, method, path string, in, out any) error {
	if in == nil && method != http.MethodGet {
		in = struct{}{}
	}
	return c.do(ctx, method, path, in, out)
}

// Raw performs a request and returns the response body unparsed, as for
// exports.
func (c *Client) Raw(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	err = transportError(err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errorFrom(resp)
	}
	return io.ReadAll(resp.Body)
}

func esc(s string) string { return url.QueryEscape(s) }

// Query builds "?a=b&c=d" from pairs, skipping empty values.
func Query(pairs ...string) string {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			values.Add(pairs[i], pairs[i+1])
		}
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}
