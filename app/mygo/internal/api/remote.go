package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// FingerprintError is a TLS handshake with a server whose certificate is
// not the one pinned when it was paired: the server's certificate changed,
// or something else answers at its address.
type FingerprintError struct {
	Want, Got []byte
}

func (e *FingerprintError) Error() string {
	return "服务器证书已变化（当前指纹 " + ShortFingerprint(e.Got) + "，配对时为 " + ShortFingerprint(e.Want) + "）"
}

// transportError is a failed request's error, the certificate pin's own
// when it failed: its message says what happened without the URL around
// it.
func transportError(err error) error {
	var fp *FingerprintError
	if errors.As(err, &fp) {
		return fp
	}
	return err
}

// Fatal reports whether err is one no retry mends: a revoked or wrong
// token, or a changed certificate. The device must pair again.
func Fatal(err error) bool {
	var fp *FingerprintError
	return errors.Is(err, ErrUnauthorized) || errors.As(err, &fp)
}

// Fingerprint is the SHA-256 of a certificate's DER.
func Fingerprint(der []byte) []byte {
	sum := sha256.Sum256(der)
	return sum[:]
}

// ParseFingerprint reads a fingerprint written in hex, with or without
// separators (spaces, colons, dashes), in any case.
func ParseFingerprint(text string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', ':', '-':
			return -1
		}
		return r
	}, strings.TrimSpace(text))
	fp, err := hex.DecodeString(strings.ToLower(clean))
	if err != nil || len(fp) != sha256.Size {
		return nil, errors.New("证书指纹格式无效")
	}
	return fp, nil
}

// FormatFingerprint writes a fingerprint as lowercase hex, the wire form.
func FormatFingerprint(fp []byte) string { return hex.EncodeToString(fp) }

// GroupFingerprint writes a fingerprint in groups of four characters, for
// people to compare.
func GroupFingerprint(fp []byte) string {
	text := hex.EncodeToString(fp)
	var b strings.Builder
	for i := 0; i < len(text); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(text[i:min(i+4, len(text))])
	}
	return b.String()
}

// ShortFingerprint is a fingerprint's first eight hex characters.
func ShortFingerprint(fp []byte) string {
	text := hex.EncodeToString(fp)
	return text[:min(8, len(text))]
}

// pinnedTLS accepts exactly the certificate whose fingerprint is want.
// The servers' certificates are self-signed, so the chain is not checked:
// the pin replaces it.
func pinnedTLS(want []byte) *tls.Config {
	want = bytes.Clone(want)
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // the pin below replaces chain verification
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return &FingerprintError{Want: want}
			}
			got := Fingerprint(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got, want) != 1 {
				return &FingerprintError{Want: want, Got: got}
			}
			return nil
		},
	}
}

// FetchFingerprint connects to hostport over TLS without trusting its
// certificate and returns the certificate's fingerprint, for the user to
// compare before pairing with a server added by hand.
func FetchFingerprint(ctx context.Context, hostport string) ([]byte, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, // only read, never trusted
	}
	conn, err := dialer.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("服务器没有提供证书")
	}
	return Fingerprint(certs[0].Raw), nil
}

// PairResult is POST /pair.
type PairResult struct {
	Token      string `json:"token"`
	DeviceID   string `json:"deviceId"`
	ServerName string `json:"serverName"`
}

// Pair trades a pairing code for this device's token at the server at
// origin, whose certificate must have the fingerprint fp.
func Pair(ctx context.Context, origin string, fp []byte, code, deviceName string) (PairResult, error) {
	var v PairResult
	err := NewWith(origin, Options{Fingerprint: fp}).Send(ctx, http.MethodPost, "/pair",
		map[string]string{"code": code, "deviceName": deviceName}, &v)
	if err == nil && v.Token == "" {
		err = errors.New("服务端没有返回令牌")
	}
	if IsStatus(err, http.StatusTooManyRequests) {
		err = fmt.Errorf("尝试次数过多，请稍后再试")
	}
	return v, err
}

// RemoteStatus is GET/PUT /remote: the server's remote access.
type RemoteStatus struct {
	Enabled     bool     `json:"enabled"`
	Listen      string   `json:"listen"`
	Running     bool     `json:"running"`
	Error       string   `json:"error"`
	Fingerprint string   `json:"fingerprint"`
	Addresses   []string `json:"addresses"`
}

// RemoteSettings is the body of PUT /remote.
type RemoteSettings struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
}

// Pairing is POST /remote/pairing: a one-time code for a new device.
type Pairing struct {
	Code        string `json:"code"`
	ExpiresAt   string `json:"expiresAt"`
	Fingerprint string `json:"fingerprint"`
	URI         string `json:"uri"`
}

// RemoteDevice is a paired device of GET /remote/devices.
type RemoteDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	LastAddr   string `json:"lastAddr"`
}

// LockHolder is who holds the write gate.
type LockHolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LockState is GET /lock and the "lock" event: the server's write gate.
type LockState struct {
	Held   bool       `json:"held"`
	Holder LockHolder `json:"holder"`
	Op     string     `json:"op"`
	Since  string     `json:"since"`
	Queued int        `json:"queued"`
}

func (c *Client) Remote(ctx context.Context) (RemoteStatus, error) {
	var v RemoteStatus
	return v, c.Get(ctx, "/remote", &v)
}

func (c *Client) SetRemote(ctx context.Context, settings RemoteSettings) (RemoteStatus, error) {
	var v RemoteStatus
	return v, c.Send(ctx, http.MethodPut, "/remote", settings, &v)
}

func (c *Client) CreatePairing(ctx context.Context) (Pairing, error) {
	var v Pairing
	return v, c.Send(ctx, http.MethodPost, "/remote/pairing", struct{}{}, &v)
}

func (c *Client) RemoteDevices(ctx context.Context) ([]RemoteDevice, error) {
	var v struct {
		Devices []RemoteDevice `json:"devices"`
	}
	return v.Devices, c.Get(ctx, "/remote/devices", &v)
}

func (c *Client) RevokeDevice(ctx context.Context, id string) error {
	return c.Send(ctx, http.MethodDelete, "/remote/devices/"+esc(id), nil, nil)
}

func (c *Client) Lock(ctx context.Context) (LockState, error) {
	var v LockState
	return v, c.Get(ctx, "/lock", &v)
}

// decodeLock reads a lock event's data.
func decodeLock(raw json.RawMessage) (LockState, bool) {
	var v LockState
	return v, json.Unmarshal(raw, &v) == nil
}

// Lock decodes a "lock" event; it reports false for any other event.
func (e Event) Lock() (LockState, bool) {
	if e.Type != "lock" {
		return LockState{}, false
	}
	return decodeLock(e.Data)
}
