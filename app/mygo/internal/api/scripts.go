package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Script types: the phase a script runs in.
const (
	ScriptTypeRequest  = "http-request"
	ScriptTypeResponse = "http-response"
	ScriptTypeGeneric  = "generic"
	ScriptTypeCron     = "cron"
	ScriptTypeRule     = "rule"
	ScriptTypeDNS      = "dns"
)

// Script actions: the outcome of a run.
const (
	ScriptActionNone    = "none"
	ScriptActionModify  = "modify"
	ScriptActionRespond = "respond"
	ScriptActionAbort   = "abort"
	ScriptActionError   = "error"
	ScriptActionTimeout = "timeout"
	ScriptActionLimit   = "limit"
	ScriptActionPaused  = "paused"
	ScriptActionSkipped = "skipped"
)

// Script log levels.
const (
	ScriptLogLevelLog   = "log"
	ScriptLogLevelInfo  = "info"
	ScriptLogLevelWarn  = "warn"
	ScriptLogLevelError = "error"
)

// ScriptHeader is one header field of a message a script sees.
type ScriptHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ScriptRequestMessage is the request a script runs on. Body is raw bytes
// (base64 on the wire); HasBody tells an empty body from none.
type ScriptRequestMessage struct {
	URL     string         `json:"url"`
	Method  string         `json:"method,omitempty"`
	Headers []ScriptHeader `json:"headers,omitempty"`
	Body    []byte         `json:"body,omitempty"`
	HasBody bool           `json:"has_body,omitempty"`
}

// ScriptResponseMessage is a response a script sees or produces.
type ScriptResponseMessage struct {
	Status  int32          `json:"status,omitempty"`
	Headers []ScriptHeader `json:"headers,omitempty"`
	Body    []byte         `json:"body,omitempty"`
	HasBody bool           `json:"has_body,omitempty"`
}

// ScriptStoreEntry seeds the store of a test run.
type ScriptStoreEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ScriptEvalRequest is the body of POST /scripts/evaluate: Source runs as
// Type on Request or Response without being installed. TimeoutMS and
// MaxSize 0 take the core's defaults.
type ScriptEvalRequest struct {
	Source       string                 `json:"source"`
	Type         string                 `json:"type"`
	Pattern      string                 `json:"pattern,omitempty"`
	Argument     string                 `json:"argument,omitempty"`
	BinaryBody   bool                   `json:"binary_body,omitempty"`
	RequiresBody bool                   `json:"requires_body,omitempty"`
	TimeoutMS    int64                  `json:"timeout_ms,omitempty"`
	MaxSize      int64                  `json:"max_size,omitempty"`
	Request      *ScriptRequestMessage  `json:"request,omitempty"`
	Response     *ScriptResponseMessage `json:"response,omitempty"`
	Tag          string                 `json:"tag,omitempty"`
	Store        []ScriptStoreEntry     `json:"store,omitempty"`
	// Rule is the $request of a rule or dns script.
	Rule *ScriptRuleInput `json:"rule,omitempty"`
}

// ScriptRuleInput is the $request of a rule or dns script trial run.
type ScriptRuleInput struct {
	Domain      string `json:"domain,omitempty"`
	IP          string `json:"ip,omitempty"`
	Port        uint32 `json:"port,omitempty"`
	Network     string `json:"network,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Inbound     string `json:"inbound,omitempty"`
	InboundType string `json:"inbound_type,omitempty"`
	SourceIP    string `json:"source_ip,omitempty"`
	ProcessName string `json:"process_name,omitempty"`
	ProcessPath string `json:"process_path,omitempty"`
	User        string `json:"user,omitempty"`
	QueryType   string `json:"query_type,omitempty"`
}

// ScriptLogLine is a console line of a run; TimeMS is a Unix time in
// milliseconds.
type ScriptLogLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	TimeMS  int64  `json:"time_ms"`
}

// ScriptNotification is a notification a script posted. Seq is 0 for the
// notifications of a test run.
type ScriptNotification struct {
	Seq      Uint64 `json:"seq,omitempty"`
	Script   string `json:"script,omitempty"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body,omitempty"`
	URL      string `json:"url,omitempty"`
	TimeMS   int64  `json:"time_ms"`
}

// ScriptChange is one change a script made: Kind says what (such as
// "header"), Op the operation and Name the header; Before and After are the
// values, valid when HadBefore and HasAfter.
type ScriptChange struct {
	Kind      string `json:"kind"`
	Op        string `json:"op,omitempty"`
	Name      string `json:"name,omitempty"`
	Before    string `json:"before,omitempty"`
	HadBefore bool   `json:"had_before,omitempty"`
	After     string `json:"after,omitempty"`
	HasAfter  bool   `json:"has_after,omitempty"`
}

// ScriptEvalResult is what a test run produced. Action says what the
// script did (one of the ScriptAction values; Reason says why a run was
// skipped, ErrorKind "compile" marks a script that did not compile). URL is the
// rewritten URL when it changed; Headers hold the whole header list when
// HeadersChanged; Body the whole body when BodyChanged; Response the
// response a respond decision produced. DurationUS is in microseconds.
type ScriptEvalResult struct {
	Action         string                 `json:"action"`
	Reason         string                 `json:"reason,omitempty"`
	Error          string                 `json:"error,omitempty"`
	ErrorKind      string                 `json:"error_kind,omitempty"`
	DurationUS     int64                  `json:"duration_us"`
	Logs           []ScriptLogLine        `json:"logs,omitempty"`
	Notifications  []ScriptNotification   `json:"notifications,omitempty"`
	Changes        []ScriptChange         `json:"changes,omitempty"`
	URL            string                 `json:"url,omitempty"`
	HeadersChanged bool                   `json:"headers_changed,omitempty"`
	Headers        []ScriptHeader         `json:"headers,omitempty"`
	BodyChanged    bool                   `json:"body_changed,omitempty"`
	Body           []byte                 `json:"body,omitempty"`
	Status         int32                  `json:"status,omitempty"`
	Response       *ScriptResponseMessage `json:"response,omitempty"`
	Output         string                 `json:"output,omitempty"`
	// Decision is what the host makes of the output of a rule or dns
	// script: "true" or "false", "pass", "server <tag>", "empty answer",
	// an rcode or the answer addresses. Empty on error.
	Decision string `json:"decision,omitempty"`
}

// ScriptRunResult is what POST /scripts/run answers for a generic script
// run by hand.
type ScriptRunResult struct {
	Action     string          `json:"action"`
	Error      string          `json:"error,omitempty"`
	DurationUS int64           `json:"duration_us"`
	Logs       []ScriptLogLine `json:"logs,omitempty"`
	Output     string          `json:"output,omitempty"`
}

// ScriptState is the counters of one installed script. PausedUntil and
// LastRunMS are Unix times in milliseconds, 0 when unset; LastDurationUS
// is in microseconds.
type ScriptState struct {
	Tag            string `json:"tag"`
	Type           string `json:"type"`
	Runs           Uint64 `json:"runs"`
	Errors         Uint64 `json:"errors"`
	Timeouts       Uint64 `json:"timeouts"`
	Limits         Uint64 `json:"limits"`
	Skipped        Uint64 `json:"skipped"`
	Paused         bool   `json:"paused,omitempty"`
	PausedUntil    int64  `json:"paused_until,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	LastRunMS      int64  `json:"last_run_ms,omitempty"`
	LastDurationUS int64  `json:"last_duration_us,omitempty"`
	// NextRunMS is the next scheduled run of a cron script, Unix ms.
	NextRunMS int64 `json:"next_run_ms,omitempty"`
}

// ScriptStatus is GET /scripts/status. Bytes and limit are of the
// persistent store.
type ScriptStatus struct {
	Scripts         []ScriptState `json:"scripts"`
	StorePersistent bool          `json:"store_persistent"`
	StoreBytes      Uint64        `json:"store_bytes"`
	StoreLimit      Uint64        `json:"store_limit"`
}

// ScriptLogEntry is a console line of an installed script on the log
// stream; Seq is the cursor.
type ScriptLogEntry struct {
	Seq     Uint64 `json:"seq"`
	Script  string `json:"script"`
	Phase   string `json:"phase,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
	TimeMS  int64  `json:"time_ms"`
}

// ScriptStoreInfo is the store's size: Persistent tells whether it
// survives a restart of the core.
type ScriptStoreInfo struct {
	Persistent bool   `json:"persistent"`
	Bytes      Uint64 `json:"bytes"`
	Limit      Uint64 `json:"limit"`
}

// ScriptNamespace is a namespace of the store with its entry count and
// bytes.
type ScriptNamespace struct {
	Name    string `json:"name"`
	Entries uint32 `json:"entries"`
	Bytes   Uint64 `json:"bytes"`
}

// ScriptStoreItem is a key of a namespace. Value is cut when Truncated;
// Size is the whole value's bytes.
type ScriptStoreItem struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Size      uint32 `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ScriptStoreList is GET /scripts/store: the namespaces always, the
// entries of the namespace asked for.
type ScriptStoreList struct {
	Namespaces []ScriptNamespace `json:"namespaces"`
	Entries    []ScriptStoreItem `json:"entries"`
	Info       *ScriptStoreInfo  `json:"info,omitempty"`
}

// ScriptStoreValue is one value of the store; Found false when the key
// does not exist.
type ScriptStoreValue struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Found     bool   `json:"found"`
}

// IsScriptUnavailable reports whether err says the scripts are not
// reachable: the core is not running or not connected (503).
func IsScriptUnavailable(err error) bool { return IsStatus(err, http.StatusServiceUnavailable) }

// ScriptStatus reads the installed scripts' counters and the store's size.
func (c *Client) ScriptStatus(ctx context.Context) (ScriptStatus, error) {
	var v ScriptStatus
	return v, c.Get(ctx, "/scripts/status", &v)
}

// ScriptEvaluate runs a script on a message without installing it. A
// script's own failure is in the result (Error, ErrorKind), not the
// returned error. The request has no overall timeout of its own (a script
// may run long): ctx limits it.
func (c *Client) ScriptEvaluate(ctx context.Context, req ScriptEvalRequest) (ScriptEvalResult, error) {
	var v ScriptEvalResult
	return v, c.doWith(c.stream(), ctx, http.MethodPost, "/scripts/evaluate", req, &v)
}

// ScriptRun runs the installed script tag by hand with argument; ctx limits
// it, as for ScriptEvaluate.
func (c *Client) ScriptRun(ctx context.Context, tag, argument string) (ScriptRunResult, error) {
	var v ScriptRunResult
	in := struct {
		Tag      string `json:"tag"`
		Argument string `json:"argument,omitempty"`
	}{tag, argument}
	return v, c.doWith(c.stream(), ctx, http.MethodPost, "/scripts/run", in, &v)
}

// ScriptStoreList reads the namespaces and the entries of namespace
// (empty: only the namespaces).
func (c *Client) ScriptStoreList(ctx context.Context, namespace string) (ScriptStoreList, error) {
	var v ScriptStoreList
	return v, c.Get(ctx, "/scripts/store"+Query("namespace", namespace), &v)
}

// ScriptStoreGet reads one value.
func (c *Client) ScriptStoreGet(ctx context.Context, namespace, key string) (ScriptStoreValue, error) {
	var v ScriptStoreValue
	return v, c.Get(ctx, "/scripts/store/value"+Query("namespace", namespace, "key", key), &v)
}

// ScriptStoreSet writes one value and returns the store's size.
func (c *Client) ScriptStoreSet(ctx context.Context, namespace, key, value string) (ScriptStoreInfo, error) {
	var v *ScriptStoreInfo
	in := struct {
		Namespace string `json:"namespace"`
		Key       string `json:"key"`
		Value     string `json:"value"`
	}{namespace, key, value}
	err := c.Send(ctx, http.MethodPut, "/scripts/store/value", in, &v)
	if v == nil {
		return ScriptStoreInfo{}, err
	}
	return *v, err
}

// ScriptStoreDelete removes one key and returns the store's size.
func (c *Client) ScriptStoreDelete(ctx context.Context, namespace, key string) (ScriptStoreInfo, error) {
	var v *ScriptStoreInfo
	err := c.Send(ctx, http.MethodDelete, "/scripts/store/value"+Query("namespace", namespace, "key", key), nil, &v)
	if v == nil {
		return ScriptStoreInfo{}, err
	}
	return *v, err
}

// scriptSettle is how long a request of a script stream may stay unanswered
// and unfailed before the link counts as up; a variable for tests.
var scriptSettle = time.Second

// streamScripts follows an NDJSON endpoint of the scripts until ctx ends.
// seq reads an entry's cursor. It reconnects after failures and broken
// connections with a short backoff, resuming from the last entry. The core
// numbers entries from 1 again after it restarts: a 503 (the core is gone
// or restarting) therefore drops the cursor to 0, and the next connection
// replays what the new run holds. A caller sees a restart as an entry whose
// Seq is not above the one before. Only a failure no retry can mend ends
// it before ctx, returning that error.
func streamScripts[T any](c *Client, ctx context.Context, path string, after Uint64, query []string, seq func(*T) Uint64, handle func(T), onState func(connected bool)) error {
	state := func(connected bool) {
		if onState != nil {
			onState(connected)
		}
	}
	backoff := eventsBackoff
	for ctx.Err() == nil {
		received := false
		// A server may send nothing, not even its headers, until the first
		// entry: a request that has not failed after scriptSettle counts as
		// connected, as does the first answer.
		var gate sync.Mutex
		ended := false
		up := func() {
			gate.Lock()
			defer gate.Unlock()
			if !ended {
				state(true)
			}
		}
		settled := time.AfterFunc(scriptSettle, up)
		pairs := append([]string{"after", after.String()}, query...)
		err := c.ndjson(ctx, http.MethodGet, path+Query(pairs...), nil, false, up, func(line json.RawMessage) error {
			var entry T
			if err := json.Unmarshal(line, &entry); err != nil {
				return err
			}
			received = true
			after = seq(&entry)
			handle(entry)
			return nil
		})
		settled.Stop()
		gate.Lock()
		ended = true
		gate.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if Fatal(err) {
			return err
		}
		state(false)
		if IsScriptUnavailable(err) || IsStatus(err, http.StatusBadRequest) {
			after = 0
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

// StreamScriptLogs follows the console lines of the installed scripts
// (GET /scripts/logs) after cursor after, of script tag only when tag is
// not empty, calling handle with each entry until ctx ends. See
// streamScripts for reconnection and restarts.
func (c *Client) StreamScriptLogs(ctx context.Context, after Uint64, tag string, handle func(ScriptLogEntry)) error {
	return c.StreamScriptLogsState(ctx, after, tag, handle, nil)
}

// StreamScriptLogsState is StreamScriptLogs with onState, called from the
// streaming goroutine: true when a connection is established, false when
// it fails or breaks and a retry follows.
func (c *Client) StreamScriptLogsState(ctx context.Context, after Uint64, tag string, handle func(ScriptLogEntry), onState func(connected bool)) error {
	return streamScripts(c, ctx, "/scripts/logs", after, []string{"tag", tag}, func(e *ScriptLogEntry) Uint64 { return e.Seq }, handle, onState)
}

// StreamScriptNotifications follows the notifications scripts post
// (GET /scripts/notifications) after cursor after, as StreamScriptLogs.
func (c *Client) StreamScriptNotifications(ctx context.Context, after Uint64, handle func(ScriptNotification)) error {
	return c.StreamScriptNotificationsState(ctx, after, handle, nil)
}

// StreamScriptNotificationsState is StreamScriptNotifications with the
// connection state callback of StreamScriptLogsState.
func (c *Client) StreamScriptNotificationsState(ctx context.Context, after Uint64, handle func(ScriptNotification), onState func(connected bool)) error {
	return streamScripts(c, ctx, "/scripts/notifications", after, nil, func(n *ScriptNotification) Uint64 { return n.Seq }, handle, onState)
}
