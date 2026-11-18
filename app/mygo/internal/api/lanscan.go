package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// ScanDevice is one LAN scan result.
type ScanDevice struct {
	IP        string   `json:"ip"`
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type"`              // phone tablet watch computer tv speaker router printer iot unknown
	Model     string   `json:"model,omitempty"`   // readable model name, or the raw identifier
	ModelID   string   `json:"modelId,omitempty"` // Apple model identifier behind Model
	OS        string   `json:"os,omitempty"`      // system version, e.g. "iOS 27.0.1"
	MAC       string   `json:"mac,omitempty"`
	MACRandom bool     `json:"macRandom,omitempty"`
	Vendor    string   `json:"vendor,omitempty"`
	Image     string   `json:"image,omitempty"` // iphone ipad watch mac appletv homepod airpods vision pc router, or empty
	Sources   []string `json:"sources"`
	Gateway   bool     `json:"gateway,omitempty"`
	Self      bool     `json:"self,omitempty"`
}

// Scan states.
const (
	ScanRunning  = "running"
	ScanDone     = "done"
	ScanCanceled = "canceled"
	ScanFailed   = "failed"
)

// ScanSnapshot is the state of a LAN scan; Devices holds only the entries
// new or changed since the cursor the poll passed.
type ScanSnapshot struct {
	ID      string       `json:"id"`
	State   string       `json:"state"`
	Phase   string       `json:"phase"`
	Step    int          `json:"step"`
	Steps   int          `json:"steps"`
	Cursor  int          `json:"cursor"`
	Devices []ScanDevice `json:"devices"`
	Error   string       `json:"error,omitempty"`
}

// StartScan begins a LAN scan on the machine of the local server and returns
// its id. A scan already running answers 409; a remote server answers 403.
func (c *Client) StartScan(ctx context.Context) (string, error) {
	var v struct {
		ID string `json:"id"`
	}
	return v.ID, c.Send(ctx, http.MethodPost, "/lan-scan", struct{}{}, &v)
}

// PollScan returns the scan's progress and the devices changed after since
// (pass the previous snapshot's Cursor; 0 returns everything).
func (c *Client) PollScan(ctx context.Context, id string, since int) (ScanSnapshot, error) {
	var v ScanSnapshot
	return v, c.Get(ctx, "/lan-scan?id="+esc(id)+"&since="+strconv.Itoa(since), &v)
}

// CancelScan stops the scan at once; devices found so far stay pollable.
func (c *Client) CancelScan(ctx context.Context, id string) error {
	return c.Send(ctx, http.MethodPost, "/lan-scan/cancel?id="+esc(id), nil, nil)
}

// RunningScanID is the id of the scan in progress that a start answered 409
// with; empty for any other error or an older server.
func RunningScanID(err error) string {
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		return ""
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(apiErr.Body, &v) != nil {
		return ""
	}
	return v.ID
}
