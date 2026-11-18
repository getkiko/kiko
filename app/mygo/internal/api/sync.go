package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// Sync backend types.
const (
	SyncWebDAV = "webdav"
	SyncS3     = "s3"
	SyncKV     = "kv"
)

// SyncWebDAVConfig is a WebDAV backup location.
type SyncWebDAVConfig struct {
	URL           string `json:"url"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	Path          string `json:"path"`
	AllowInsecure bool   `json:"allowInsecure"`
}

// SyncS3Config is an S3-compatible backup location.
type SyncS3Config struct {
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	Prefix        string `json:"prefix"`
	AccessKey     string `json:"accessKey"`
	SecretKey     string `json:"secretKey"`
	PathStyle     bool   `json:"pathStyle"`
	AllowInsecure bool   `json:"allowInsecure"`
}

// SyncKVConfig is a Cloudflare KV worker backup location.
type SyncKVConfig struct {
	URL           string `json:"url"`
	Token         string `json:"token"`
	AllowInsecure bool   `json:"allowInsecure"`
}

// SyncBackendConfig is the backup location: Type picks one of the others.
type SyncBackendConfig struct {
	Type   string            `json:"type"`
	WebDAV *SyncWebDAVConfig `json:"webdav,omitempty"`
	S3     *SyncS3Config     `json:"s3,omitempty"`
	KV     *SyncKVConfig     `json:"kv,omitempty"`
}

// SyncResult counts what a sync moved.
type SyncResult struct {
	Pulled    int `json:"pulled"`
	Pushed    int `json:"pushed"`
	Conflicts int `json:"conflicts"`
	Deleted   int `json:"deleted"`
}

// Sync states.
const (
	SyncUnconfigured = "unconfigured"
	SyncLocked       = "locked"
	SyncNeedsSetup   = "needs-setup"
	SyncUnlocked     = "unlocked"
)

// SyncStatus is GET /sync/status and the "sync" event.
type SyncStatus struct {
	Configured     bool               `json:"configured"`
	State          string             `json:"state"`
	Backend        *SyncBackendConfig `json:"backend,omitempty"`
	VaultExists    *bool              `json:"vaultExists"`
	AutoSync       bool               `json:"autoSync"`
	Syncing        bool               `json:"syncing"`
	LastSyncedAt   int64              `json:"lastSyncedAt,omitempty"`
	LastError      string             `json:"lastError,omitempty"`
	RemoteRevision *uint64            `json:"remoteRevision,omitempty"`
	DeviceID       string             `json:"deviceId"`
	LastResult     *SyncResult        `json:"lastResult,omitempty"`
}

// SyncWorkerInfo is GET /sync/worker: how to deploy the KV worker.
type SyncWorkerInfo struct {
	Source   string   `json:"source"`
	Readme   string   `json:"readme"`
	Wrangler string   `json:"wrangler"`
	Commands []string `json:"commands"`
}

// SyncErrorCode returns the "code" of a sync endpoint's error body, as
// "wrong-password".
func SyncErrorCode(err error) string {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return ""
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(apiErr.Body, &body)
	return body.Code
}

func (c *Client) SyncStatus(ctx context.Context) (SyncStatus, error) {
	var v SyncStatus
	return v, c.Get(ctx, "/sync/status", &v)
}

// SyncSaveConfig sets the backup location; autoSync nil keeps the setting.
func (c *Client) SyncSaveConfig(ctx context.Context, backend SyncBackendConfig, autoSync *bool) (SyncStatus, error) {
	var v SyncStatus
	body := map[string]any{"backend": backend}
	if autoSync != nil {
		body["autoSync"] = *autoSync
	}
	return v, c.Send(ctx, http.MethodPut, "/sync/config", body, &v)
}

// SyncTest connects to a backup location, reporting whether it holds a
// vault.
func (c *Client) SyncTest(ctx context.Context, backend SyncBackendConfig) (bool, error) {
	var v struct {
		OK          bool `json:"ok"`
		VaultExists bool `json:"vaultExists"`
	}
	return v.VaultExists, c.Send(ctx, http.MethodPost, "/sync/test", map[string]any{"backend": backend}, &v)
}

// SyncUnlock unlocks the vault, or creates it with create and confirm.
func (c *Client) SyncUnlock(ctx context.Context, password, confirm string, create bool) (SyncStatus, error) {
	body := map[string]any{"password": password}
	if create {
		body["confirm"] = confirm
		body["create"] = true
	}
	var v SyncStatus
	return v, c.Send(ctx, http.MethodPost, "/sync/unlock", body, &v)
}

func (c *Client) SyncLock(ctx context.Context) (SyncStatus, error) {
	var v SyncStatus
	return v, c.Send(ctx, http.MethodPost, "/sync/lock", nil, &v)
}

// SyncNow syncs at once.
func (c *Client) SyncNow(ctx context.Context) (SyncStatus, SyncResult, error) {
	var v struct {
		Status SyncStatus `json:"status"`
		Result SyncResult `json:"result"`
	}
	err := c.Send(ctx, http.MethodPost, "/sync/now", nil, &v)
	return v.Status, v.Result, err
}

func (c *Client) SyncAuto(ctx context.Context, enabled bool) (SyncStatus, error) {
	var v SyncStatus
	return v, c.Send(ctx, http.MethodPost, "/sync/auto", map[string]bool{"enabled": enabled}, &v)
}

func (c *Client) SyncChangePassword(ctx context.Context, password, confirm string) (SyncStatus, error) {
	var v SyncStatus
	return v, c.Send(ctx, http.MethodPost, "/sync/change-password", map[string]string{"newPassword": password, "confirm": confirm}, &v)
}

// SyncReset deletes the remote vault and the local key.
func (c *Client) SyncReset(ctx context.Context) (SyncStatus, error) {
	var v SyncStatus
	return v, c.Send(ctx, http.MethodPost, "/sync/reset", map[string]bool{"confirm": true}, &v)
}

func (c *Client) SyncWorker(ctx context.Context) (SyncWorkerInfo, error) {
	var v SyncWorkerInfo
	return v, c.Get(ctx, "/sync/worker", &v)
}
