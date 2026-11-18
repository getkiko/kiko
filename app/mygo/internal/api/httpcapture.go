package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// HTTP record states.
const (
	HTTPStateInProgress = "in_progress"
	HTTPStateComplete   = "complete"
	HTTPStateFailed     = "failed"
	HTTPStateUpgraded   = "upgraded"
)

// HTTP event kinds.
const (
	HTTPEventNew    = "new"
	HTTPEventUpdate = "update"
	HTTPEventDone   = "done"
)

// The two sides of an exchange a body belongs to.
const (
	HTTPSideRequest  = "request"
	HTTPSideResponse = "response"
)

// HTTPHeader is one header field in wire order.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPTiming holds Unix nanoseconds, zero when the moment was not reached;
// DNS, Connect and TLS are durations in nanoseconds.
type HTTPTiming struct {
	Start           int64 `json:"start,omitempty"`
	RequestHeaders  int64 `json:"request_headers,omitempty"`
	RequestBody     int64 `json:"request_body,omitempty"`
	FirstByte       int64 `json:"first_byte,omitempty"`
	ResponseHeaders int64 `json:"response_headers,omitempty"`
	ResponseBody    int64 `json:"response_body,omitempty"`
	End             int64 `json:"end,omitempty"`
	DNS             int64 `json:"dns,omitempty"`
	Connect         int64 `json:"connect,omitempty"`
	TLS             int64 `json:"tls,omitempty"`
}

// HTTPConnection is the connection an exchange travelled over.
type HTTPConnection struct {
	ID           string `json:"id"`
	Source       string `json:"source,omitempty"`
	Destination  string `json:"destination,omitempty"`
	Host         string `json:"host,omitempty"`
	Inbound      string `json:"inbound,omitempty"`
	InboundType  string `json:"inbound_type,omitempty"`
	Outbound     string `json:"outbound,omitempty"`
	OutboundType string `json:"outbound_type,omitempty"`
	Rule         string `json:"rule,omitempty"`
	ProcessName  string `json:"process_name,omitempty"`
	ProcessPath  string `json:"process_path,omitempty"`
	ProcessID    uint32 `json:"process_id,omitempty"`
	User         string `json:"user,omitempty"`
}

// HTTPRecord is one decrypted HTTP exchange; bodies are fetched with
// HTTPBody.
type HTTPRecord struct {
	ID                   string          `json:"id"`
	Instance             string          `json:"instance"`
	Seq                  Uint64          `json:"seq"`
	CreatedSeq           Uint64          `json:"created_seq"`
	ConnectionID         string          `json:"connection_id,omitempty"`
	RequestIndex         uint32          `json:"request_index"`
	Connection           *HTTPConnection `json:"connection,omitempty"`
	Method               string          `json:"method"`
	Scheme               string          `json:"scheme,omitempty"`
	Authority            string          `json:"authority,omitempty"`
	Host                 string          `json:"host"`
	Path                 string          `json:"path"`
	URL                  string          `json:"url"`
	Proto                string          `json:"proto"`
	Status               int             `json:"status"`
	Interim              []int           `json:"interim,omitempty"`
	RequestHeaders       []HTTPHeader    `json:"request_headers,omitempty"`
	ResponseHeaders      []HTTPHeader    `json:"response_headers,omitempty"`
	RequestTrailers      []HTTPHeader    `json:"request_trailers,omitempty"`
	ResponseTrailers     []HTTPHeader    `json:"response_trailers,omitempty"`
	RequestBytes         int64           `json:"request_bytes"`
	ResponseBytes        int64           `json:"response_bytes"`
	RequestBodySaved     bool            `json:"request_body_saved,omitempty"`
	ResponseBodySaved    bool            `json:"response_body_saved,omitempty"`
	RequestBodyStored    int64           `json:"request_body_stored,omitempty"`
	ResponseBodyStored   int64           `json:"response_body_stored,omitempty"`
	RequestBodyTruncated bool            `json:"request_body_truncated,omitempty"`
	ResponseBodyTrunc    bool            `json:"response_body_truncated,omitempty"`
	BodyDropped          bool            `json:"body_dropped,omitempty"`
	RequestEncoding      string          `json:"request_encoding,omitempty"`
	ResponseEncoding     string          `json:"response_encoding,omitempty"`
	Timing               *HTTPTiming     `json:"timing,omitempty"`
	State                string          `json:"state"`
	Error                string          `json:"error,omitempty"`
	// Unverified: the upstream certificate was not verified.
	Unverified bool `json:"unverified,omitempty"`
	// Rewritten: a rewrite rule changed the exchange.
	Rewritten bool `json:"rewritten,omitempty"`
	// Rewrites lists what rewrite rules did, in the order they ran.
	Rewrites []HTTPRewriteHit `json:"rewrites,omitempty"`
}

// HTTPRewriteHit is one rewrite rule or script that matched an exchange. Kind is
// "url", "header", "body", "map_local", "connect" or "script"; Rule is the rule id (a script hit names the script by Target, its tag); Action the
// rule's action (reject, 302, add, ...); Side "request" or "response" for
// header rules; Target the redirect location of a redirect.
type HTTPRewriteHit struct {
	Kind    string              `json:"kind"`
	Rule    string              `json:"rule,omitempty"`
	Action  string              `json:"action,omitempty"`
	Side    string              `json:"side,omitempty"`
	Target  string              `json:"target,omitempty"`
	Status  int                 `json:"status,omitempty"`
	Changes []HTTPRewriteChange `json:"changes,omitempty"`
	// Run is set for script hits.
	Run *HTTPScriptRun `json:"run,omitempty"`
}

// HTTPScriptRun is one script run in an exchange: duration in
// microseconds, the error, why it was skipped and its console lines.
type HTTPScriptRun struct {
	Tag        string          `json:"tag"`
	Phase      string          `json:"phase,omitempty"`
	DurationUS int64           `json:"duration_us,omitempty"`
	Error      string          `json:"error,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Logs       []HTTPScriptLog `json:"logs,omitempty"`
}

// HTTPScriptLog is a console line of a script run; Level is log, info, warn
// or error.
type HTTPScriptLog struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// HTTPRewriteChange is one header operation of a header rule. Before is the
// value the header had (all values joined by ", ") when HadBefore; After is
// the value it has now when HasAfter. The recorded headers of the exchange
// already show the rewritten state.
type HTTPRewriteChange struct {
	Op        string `json:"op"`
	Name      string `json:"name"`
	Before    string `json:"before,omitempty"`
	HadBefore bool   `json:"had_before,omitempty"`
	After     string `json:"after,omitempty"`
	HasAfter  bool   `json:"has_after,omitempty"`
}

// HTTPEvent is a change of a record.
type HTTPEvent struct {
	Kind   string      `json:"kind"`
	Record *HTTPRecord `json:"record,omitempty"`
}

// HTTPStatus are the capture store's counters.
type HTTPStatus struct {
	Instance     string `json:"instance"`
	Enabled      bool   `json:"enabled"`
	BodyEnabled  bool   `json:"body_enabled"`
	RecordCount  int    `json:"record_count"`
	InProgress   int    `json:"in_progress"`
	ByteEstimate int64  `json:"byte_estimate"`
	MaxMemory    int64  `json:"max_memory"`
	MaxRecords   int    `json:"max_records"`
	Total        Uint64 `json:"total"`
	Evicted      Uint64 `json:"evicted"`
	BodyDropped  Uint64 `json:"body_dropped"`

	// PersistEnabled: finished exchanges are being written to disk;
	// PersistFailed: the directory could not be opened. Records and bytes
	// are what the files hold now; Dropped counts records skipped under back
	// pressure and Errors failed writes.
	PersistEnabled bool   `json:"persist_enabled,omitempty"`
	PersistFailed  bool   `json:"persist_failed,omitempty"`
	PersistRecords int    `json:"persist_records,omitempty"`
	PersistBytes   int64  `json:"persist_bytes,omitempty"`
	PersistWritten Uint64 `json:"persist_written,omitempty"`
	PersistDropped Uint64 `json:"persist_dropped,omitempty"`
	PersistErrors  Uint64 `json:"persist_errors,omitempty"`
}

// HTTPFrame is one update of the stream. With Reset the events replace the
// whole list; Gap says the reset came from eviction or a clear within the
// same instance. Seq is the cursor to resume from and Floor the oldest
// cursor the core still serves.
//
// New, Update and Done events must be applied by record id as an upsert: a
// reconnect can resend records already received. A Reset snapshot carries
// only the newest 500 records and is split into batches whose intermediate
// frames have Seq 0, so the cursor is only meaningful on the last frame;
// older records come from ListHTTPRecords pages.
type HTTPFrame struct {
	Instance string      `json:"instance"`
	Seq      Uint64      `json:"seq"`
	Floor    Uint64      `json:"floor"`
	Reset    bool        `json:"reset,omitempty"`
	Gap      bool        `json:"gap,omitempty"`
	Events   []HTTPEvent `json:"events"`
	Status   *HTTPStatus `json:"status,omitempty"`
}

// HTTPList is a page of records, newest first. NextBefore is the cursor of
// the next older page, empty at the end.
type HTTPList struct {
	Instance   string       `json:"instance"`
	Records    []HTTPRecord `json:"records"`
	NextBefore string       `json:"next_before,omitempty"`
	Status     *HTTPStatus  `json:"status,omitempty"`
}

// HTTPCapabilities is GET /http/capabilities.
type HTTPCapabilities struct {
	Supported   bool `json:"supported"`
	Enabled     bool `json:"enabled"`
	BodyEnabled bool `json:"body_enabled,omitempty"`
	// Replay: the core can send a request again (POST /http/replay).
	Replay bool `json:"replay,omitempty"`
	// Rewrite: the core applies rewrite rules (mitm.http_rewrite).
	Rewrite bool `json:"rewrite,omitempty"`
	// Persist: the core can keep exchanges on disk (mitm.capture.persist).
	Persist bool        `json:"persist,omitempty"`
	Reason  string      `json:"reason,omitempty"`
	Status  *HTTPStatus `json:"status,omitempty"`
}

// HTTPQuery filters ListHTTPRecords.
type HTTPQuery struct {
	Search       string
	Host         string
	ConnectionID string
	Limit        int
	// Before is the NextBefore of the previous page.
	Before string
}

// HTTPBody is a range of a body with the facts of its view.
type HTTPBody struct {
	Data []byte
	// Total is the size of the whole view (saved or decoded bytes);
	// OriginalSize the bytes that passed, saved or not.
	Total        int64
	OriginalSize int64
	// Offset is where Data starts within the view.
	Offset int64
	// Truncated: the view lacks bytes that passed.
	Truncated bool
	// Saved: body capture was on for the exchange.
	Saved bool
	// Complete: the body ended.
	Complete    bool
	Encoding    string
	DecodeError string
}

// ReplayInbound is the inbound of the connections a replay makes.
const ReplayInbound = "kiko-internal"

// ReplayRequest is the request POST /http/replay sends on: SourceID names
// the record it came from, Outbound the tag to leave through (empty
// follows the routing rules), TimeoutMS 0 the core's default.
type ReplayRequest struct {
	SourceID  string       `json:"source_id,omitempty"`
	Method    string       `json:"method"`
	URL       string       `json:"url"`
	Headers   []HTTPHeader `json:"headers"`
	Body      []byte       `json:"body,omitempty"`
	Outbound  string       `json:"outbound,omitempty"`
	TimeoutMS int          `json:"timeout_ms,omitempty"`
	VerifyTLS bool         `json:"verify_tls"`
}

// HARSanitize says which secrets a HAR export replaces.
type HARSanitize struct {
	AuthHeaders  bool `json:"auth_headers"`
	Cookies      bool `json:"cookies"`
	QuerySecrets bool `json:"query_secrets"`
	Bodies       bool `json:"bodies"`
}

// HARFilter picks the records of an export by the list's filters.
type HARFilter struct {
	Search       string `json:"search"`
	Host         string `json:"host"`
	ConnectionID string `json:"connection_id"`
}

// HARRequest is the body of POST /http/har: IDs when non-empty, else every
// record Filter matches.
type HARRequest struct {
	IDs      []string    `json:"ids"`
	Filter   HARFilter   `json:"filter"`
	Bodies   bool        `json:"bodies"`
	Sanitize HARSanitize `json:"sanitize"`
}

// HTTPCapabilities reports whether the core captures HTTP exchanges.
func (c *Client) HTTPCapabilities(ctx context.Context) (HTTPCapabilities, error) {
	var v HTTPCapabilities
	return v, c.Get(ctx, "/http/capabilities", &v)
}

// ListHTTPRecords reads a page of records, newest first.
func (c *Client) ListHTTPRecords(ctx context.Context, q HTTPQuery) (HTTPList, error) {
	limit := ""
	if q.Limit > 0 {
		limit = strconv.Itoa(q.Limit)
	}
	var v HTTPList
	return v, c.Get(ctx, "/http/records"+Query("search", q.Search, "host", q.Host, "connection_id", q.ConnectionID, "limit", limit, "before", q.Before), &v)
}

// GetHTTPRecord reads one record. A non-empty instance must match the
// core's, else the call fails with 503.
func (c *Client) GetHTTPRecord(ctx context.Context, id, instance string) (HTTPRecord, error) {
	var v HTTPRecord
	return v, c.Get(ctx, "/http/records/"+pathEscape(id)+Query("instance", instance), &v)
}

// HTTPBody reads length bytes (0 for the rest) of a record's body from
// offset. side is HTTPSideRequest or HTTPSideResponse; decoded undoes the
// Content-Encoding.
func (c *Client) HTTPBody(ctx context.Context, id, side string, offset, length int64, decoded bool) (HTTPBody, error) {
	pairs := []string{"side", side}
	if offset > 0 {
		pairs = append(pairs, "offset", strconv.FormatInt(offset, 10))
	}
	if length > 0 {
		pairs = append(pairs, "length", strconv.FormatInt(length, 10))
	}
	if decoded {
		pairs = append(pairs, "decoded", "true")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL("/http/records/"+pathEscape(id)+"/body"+Query(pairs...)), nil)
	if err != nil {
		return HTTPBody{}, err
	}
	resp, err := c.http().Do(req)
	if err = transportError(err); err != nil {
		return HTTPBody{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return HTTPBody{}, errorFrom(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPBody{}, err
	}
	number := func(key string) int64 {
		n, _ := strconv.ParseInt(resp.Header.Get(key), 10, 64)
		return n
	}
	flag := func(key string) bool { return resp.Header.Get(key) == "true" }
	return HTTPBody{
		Data: data, Total: number("X-Total"), OriginalSize: number("X-Original-Size"), Offset: number("X-Offset"),
		Truncated: flag("X-Truncated"), Saved: flag("X-Saved"), Complete: flag("X-Complete"),
		Encoding: resp.Header.Get("X-Encoding"), DecodeError: resp.Header.Get("X-Decode-Error"),
	}, nil
}

// ClearHTTP drops the finished records; those in progress stay.
func (c *Client) ClearHTTP(ctx context.Context) (HTTPStatus, error) {
	var v struct {
		Status HTTPStatus `json:"status"`
	}
	err := c.Send(ctx, http.MethodPost, "/http/clear", struct{}{}, &v)
	return v.Status, err
}

// DeleteHTTPRecord removes one finished record from the list and from disk.
// A non-empty instance must match the core's.
func (c *Client) DeleteHTTPRecord(ctx context.Context, id, instance string) (HTTPStatus, error) {
	var v struct {
		Status HTTPStatus `json:"status"`
	}
	err := c.Send(ctx, http.MethodDelete, "/http/records/"+pathEscape(id)+Query("instance", instance), nil, &v)
	return v.Status, err
}

// PurgeHTTPPersisted deletes every stored exchange, also while the core or
// decryption is off, and the list rows that came from or went to disk.
func (c *Client) PurgeHTTPPersisted(ctx context.Context) (HTTPStatus, error) {
	var v struct {
		Status HTTPStatus `json:"status"`
	}
	err := c.Send(ctx, http.MethodPost, "/http/persist/purge", struct{}{}, &v)
	return v.Status, err
}

// StreamHTTP follows GET /http/stream from the cursor after of instance
// until ctx ends, calling handle with each frame. It reconnects after
// failures and broken connections with a short backoff, resuming from the
// last frame's instance and seq; the core answers an unknown instance or an
// evicted cursor with a Reset frame. Only a failure no retry can mend (a
// revoked token, a changed certificate) ends it before ctx, returning that
// error; otherwise it returns ctx's error.
func (c *Client) StreamHTTP(ctx context.Context, after Uint64, instance string, handle func(HTTPFrame)) error {
	backoff := eventsBackoff
	for ctx.Err() == nil {
		received := false
		path := "/http/stream" + Query("instance", instance, "after", after.String())
		err := c.NDJSON(ctx, http.MethodGet, path, nil, false, func(line json.RawMessage) error {
			var frame HTTPFrame
			if err := json.Unmarshal(line, &frame); err != nil {
				return err
			}
			received = true
			instance, after = frame.Instance, frame.Seq
			handle(frame)
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if Fatal(err) {
			return err
		}
		if IsStatus(err, http.StatusBadRequest) {
			// The cursor was refused: start over from a snapshot.
			instance, after = "", 0
		}
		if received {
			backoff = eventsBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, eventsMaxBackoff)
	}
	return ctx.Err()
}

// ReplayHTTP sends a request again through the core. The record of the
// exchange comes back even when the upstream failed (State failed); then
// the error is not nil as well, with the reason, so that a caller shows it
// and still holds the record.
func (c *Client) ReplayHTTP(ctx context.Context, req ReplayRequest) (HTTPRecord, error) {
	if req.Headers == nil {
		req.Headers = []HTTPHeader{}
	}
	var v struct {
		Record HTTPRecord `json:"record"`
		Error  string     `json:"error"`
	}
	if err := c.Send(ctx, http.MethodPost, "/http/replay", req, &v); err != nil {
		return HTTPRecord{}, err
	}
	if v.Error == "" && v.Record.State == HTTPStateFailed {
		v.Error = v.Record.Error
	}
	if v.Error != "" {
		return v.Record, errors.New(v.Error)
	}
	return v.Record, nil
}

// ExportHAR writes the HAR document of POST /http/har to w as it arrives;
// the client itself holds none of it, the caller's w does. It stops when ctx
// ends or w fails.
func (c *Client) ExportHAR(ctx context.Context, req HARRequest, w io.Writer) error {
	if req.IDs == nil {
		req.IDs = []string{}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL("/http/har"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cache-Control", "no-store")
	// The stream client has no overall timeout: an export may take long.
	resp, err := c.stream().Do(r)
	if err = transportError(err); err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFrom(resp)
	}
	_, err = io.Copy(w, resp.Body)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
