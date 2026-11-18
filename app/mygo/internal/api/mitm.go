package api

import (
	"context"
	"net/http"
	"time"
)

// MITMCA is the root certificate of GET /mitm/status. State is "absent",
// "ready" or "broken"; an absent certificate carries no other field than
// Trusted and TrustSupported.
type MITMCA struct {
	State          string `json:"state"`
	CommonName     string `json:"commonName"`
	Fingerprint    string `json:"fingerprint"`
	NotBefore      string `json:"notBefore"`
	NotAfter       string `json:"notAfter"`
	CreatedAt      string `json:"createdAt"`
	Trusted        bool   `json:"trusted"`
	TrustUnknown   bool   `json:"trustUnknown"`
	TrustSupported bool   `json:"trustSupported"`
}

// MITMCore is what the core reports of HTTPS decryption.
type MITMCore struct {
	Connected            bool   `json:"connected"`
	Enabled              bool   `json:"enabled"`
	Pending              bool   `json:"pending"`
	AuthorityFingerprint string `json:"authorityFingerprint"`
	RootFingerprint      string `json:"rootFingerprint"`
	AuthorityNotAfter    string `json:"authorityNotAfter"`
	LeafCacheSize        int    `json:"leafCacheSize"`
	CertificateSource    string `json:"certificateSource"`
}

// MITMStatus is GET /mitm/status, and what every write of the root
// certificate answers.
type MITMStatus struct {
	Supported bool     `json:"supported"`
	CA        MITMCA   `json:"ca"`
	Core      MITMCore `json:"core"`
	// BuiltinCount is the size of the core's built-in exclusion table; 0
	// when the core does not report it.
	BuiltinCount int           `json:"builtinCount"`
	Skip         []MITMSkip    `json:"skip"`
	Failures     []MITMFailure `json:"failures"`
}

// MITMSkip is a host the core leaves undecrypted until Until.
type MITMSkip struct {
	Host   string    `json:"host"`
	Source string    `json:"source"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// MITMFailure is a host whose interception failed Count times.
type MITMFailure struct {
	Host      string    `json:"host"`
	Count     int       `json:"count"`
	LastError string    `json:"lastError"`
	At        time.Time `json:"at"`
}

// ClearMITMSkip forgets the skip entry of host; an empty host clears all.
func (c *Client) ClearMITMSkip(ctx context.Context, host string) error {
	return c.Send(ctx, http.MethodPost, "/mitm/skip", map[string]string{"host": host}, nil)
}

// MITMStatus reads the root certificate and the core's state.
func (c *Client) MITMStatus(ctx context.Context) (MITMStatus, error) {
	var out MITMStatus
	err := c.Get(ctx, "/mitm/status", &out)
	return out, err
}

func (c *Client) mitmWrite(ctx context.Context, method, path string) (MITMStatus, error) {
	var out MITMStatus
	err := c.Send(ctx, method, path, nil, &out)
	return out, err
}

// MITMGenerate creates the root certificate (409 when it exists).
func (c *Client) MITMGenerate(ctx context.Context) (MITMStatus, error) {
	return c.mitmWrite(ctx, http.MethodPost, "/mitm/ca")
}

// MITMRegenerate replaces the root certificate and clears its trust.
func (c *Client) MITMRegenerate(ctx context.Context) (MITMStatus, error) {
	return c.mitmWrite(ctx, http.MethodPost, "/mitm/ca/regenerate")
}

// MITMDelete removes the root certificate and its key.
func (c *Client) MITMDelete(ctx context.Context) (MITMStatus, error) {
	return c.mitmWrite(ctx, http.MethodDelete, "/mitm/ca")
}

// MITMInstallTrust trusts the root certificate on the server's system.
func (c *Client) MITMInstallTrust(ctx context.Context) (MITMStatus, error) {
	return c.mitmWrite(ctx, http.MethodPost, "/mitm/trust")
}

// MITMRemoveTrust withdraws the trust of the root certificate.
func (c *Client) MITMRemoveTrust(ctx context.Context) (MITMStatus, error) {
	return c.mitmWrite(ctx, http.MethodDelete, "/mitm/trust")
}

// MITMExport downloads the root certificate (never its key) in one of the
// MITMFormat* formats.
func (c *Client) MITMExport(ctx context.Context, format string) ([]byte, error) {
	return c.Raw(ctx, http.MethodGet, "/mitm/export"+Query("format", format))
}

// MITM export formats of GET /mitm/export.
const (
	MITMFormatPEM          = "pem"
	MITMFormatCER          = "cer"
	MITMFormatCRT          = "crt"
	MITMFormatP12          = "p12"
	MITMFormatMobileConfig = "mobileconfig"
)

// MITMShare is the LAN download page of the root certificate. While it is
// not Active only Addresses (the candidates to listen on) are set.
type MITMShare struct {
	Active      bool     `json:"active"`
	Addresses   []string `json:"addresses"`
	Address     string   `json:"address"`
	URL         string   `json:"url"`
	Fingerprint string   `json:"fingerprint"`
	ExpiresAt   string   `json:"expiresAt"`
	// QR is the URL's QR code: a string per row, '1' a dark module, without
	// the quiet zone.
	QR        []string `json:"qr"`
	Downloads int      `json:"downloads"`
}

// MITMShare reads the state of the LAN download page.
func (c *Client) MITMShare(ctx context.Context) (MITMShare, error) {
	var out MITMShare
	err := c.Get(ctx, "/mitm/share", &out)
	return out, err
}

// MITMShareStart starts the LAN download page on address, on the first
// candidate when empty; a running page restarts with a new link.
func (c *Client) MITMShareStart(ctx context.Context, address string) (MITMShare, error) {
	var out MITMShare
	err := c.Send(ctx, http.MethodPost, "/mitm/share", map[string]string{"address": address}, &out)
	return out, err
}

// MITMShareStop closes the LAN download page at once.
func (c *Client) MITMShareStop(ctx context.Context) (MITMShare, error) {
	var out MITMShare
	err := c.Send(ctx, http.MethodDelete, "/mitm/share", nil, &out)
	return out, err
}
