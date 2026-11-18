package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/common/mitm/capture"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	httpCaptureMaxMessage   = 16 << 20
	httpCaptureWriteTimeout = 5 * time.Second
	httpCaptureRPCTimeout   = 5 * time.Second
	httpCaptureDefaultLimit = 100
	httpCaptureMaxLimit     = 500
)

// httpCaptureProbeConfig carries the capture key; a core without it rejects
// the section while accepting the base mitm option. Every core option added
// for capture needs a probe key here and an entry in httpCaptureStripKeys.
const httpCaptureProbeConfig = `{"mitm":{"enabled":false,"capture":{"enabled":true}}}`

// httpCapturePersistProbeConfig carries capture.persist; a core without it
// rejects the key while accepting mitm.capture.
const httpCapturePersistProbeConfig = `{"mitm":{"enabled":false,"capture":{"persist":{"enabled":false}}}}`

// httpCapturePersistDirName is the folder of the stored exchanges under the
// data directory.
const httpCapturePersistDirName = "http-capture"

// httpCaptureStripKeys are the mitm keys an older core rejects.
var httpCaptureStripKeys = []string{"capture"}

// httpCaptureSupported probes once whether the core accepts mitm.capture.
func (s *server) httpCaptureSupported() bool {
	s.rpc.httpProbeOnce.Do(func() {
		if !s.mitmSupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "http-capture-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(httpCaptureProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.rpc.httpSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.rpc.httpSupported
}

// httpCapturePersistSupported probes once whether the core accepts
// mitm.capture.persist.
func (s *server) httpCapturePersistSupported() bool {
	s.rpc.persistProbeOnce.Do(func() {
		if !s.httpCaptureSupported() {
			return
		}
		path := filepath.Join(s.dataDir, "runtime", "http-capture-persist-probe.json")
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil || atomicWrite(path, []byte(httpCapturePersistProbeConfig), 0o600) != nil {
			return
		}
		defer os.Remove(path)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.rpc.persistSupported = hiddenCommandContext(ctx, s.singBox, "check", "-c", path).Run() == nil
	})
	return s.rpc.persistSupported
}

// httpCapturePersistDir is where the core keeps stored exchanges. It is
// injected into the runtime configuration only, never into the document or
// its exports.
func (s *server) httpCapturePersistDir() string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, httpCapturePersistDirName)
}

// httpCapturePersistPrepare completes or removes mitm.capture.persist in a
// runtime configuration: a core that does not know it loses the key, others
// get the directory. It reports whether the key was removed.
func (s *server) httpCapturePersistPrepare(section map[string]any) bool {
	captureSection, _ := section["capture"].(map[string]any)
	if captureSection == nil {
		return false
	}
	persist, _ := captureSection["persist"].(map[string]any)
	if persist == nil {
		return false
	}
	dir := s.httpCapturePersistDir()
	if dir == "" || !s.httpCapturePersistSupported() {
		delete(captureSection, "persist")
		return true
	}
	persist["path"] = dir
	return false
}

// httpCaptureStrip removes the capture keys from a mitm section for a core
// that does not know them; it reports whether any was removed.
func httpCaptureStrip(section map[string]any) bool {
	removed := false
	for _, key := range httpCaptureStripKeys {
		if _, ok := section[key]; ok {
			delete(section, key)
			removed = true
		}
	}
	return removed
}

func (s *server) httpCaptureClient() mitmapi.HTTPCaptureClient {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	return s.rpc.httpCapture
}

func (s *server) requireHTTPCapture(w http.ResponseWriter) mitmapi.HTTPCaptureClient {
	client := s.httpCaptureClient()
	if client == nil {
		writeJSON(w, 503, map[string]string{"error": "HTTP 抓取等待核心连接"})
	}
	return client
}

// The JSON shapes below are the REST contract. 64-bit cursors and counters
// are strings, as in the DNS monitor.

type httpHeaderJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type httpTimingJSON struct {
	Start          int64 `json:"start,omitempty"`
	RequestHeaders int64 `json:"request_headers,omitempty"`
	RequestBody    int64 `json:"request_body,omitempty"`
	FirstByte      int64 `json:"first_byte,omitempty"`
	ResponseHeader int64 `json:"response_headers,omitempty"`
	ResponseBody   int64 `json:"response_body,omitempty"`
	End            int64 `json:"end,omitempty"`
	DNS            int64 `json:"dns,omitempty"`
	Connect        int64 `json:"connect,omitempty"`
	TLS            int64 `json:"tls,omitempty"`
}

type httpConnectionJSON struct {
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

type httpRecordJSON struct {
	ID                   string              `json:"id"`
	Instance             string              `json:"instance"`
	Seq                  string              `json:"seq"`
	CreatedSeq           string              `json:"created_seq"`
	ConnectionID         string              `json:"connection_id,omitempty"`
	RequestIndex         uint32              `json:"request_index"`
	Connection           *httpConnectionJSON `json:"connection,omitempty"`
	Method               string              `json:"method"`
	Scheme               string              `json:"scheme,omitempty"`
	Authority            string              `json:"authority,omitempty"`
	Host                 string              `json:"host"`
	Path                 string              `json:"path"`
	URL                  string              `json:"url"`
	Proto                string              `json:"proto"`
	Status               int32               `json:"status"`
	Interim              []int32             `json:"interim,omitempty"`
	RequestHeaders       []httpHeaderJSON    `json:"request_headers,omitempty"`
	ResponseHeaders      []httpHeaderJSON    `json:"response_headers,omitempty"`
	RequestTrailers      []httpHeaderJSON    `json:"request_trailers,omitempty"`
	ResponseTrailers     []httpHeaderJSON    `json:"response_trailers,omitempty"`
	RequestBytes         int64               `json:"request_bytes"`
	ResponseBytes        int64               `json:"response_bytes"`
	RequestBodySaved     bool                `json:"request_body_saved,omitempty"`
	ResponseBodySaved    bool                `json:"response_body_saved,omitempty"`
	RequestBodyStored    int64               `json:"request_body_stored,omitempty"`
	ResponseBodyStored   int64               `json:"response_body_stored,omitempty"`
	RequestBodyTruncated bool                `json:"request_body_truncated,omitempty"`
	ResponseBodyTrunc    bool                `json:"response_body_truncated,omitempty"`
	BodyDropped          bool                `json:"body_dropped,omitempty"`
	RequestEncoding      string              `json:"request_encoding,omitempty"`
	ResponseEncoding     string              `json:"response_encoding,omitempty"`
	Timing               *httpTimingJSON     `json:"timing,omitempty"`
	State                string              `json:"state"`
	Error                string              `json:"error,omitempty"`
	Unverified           bool                `json:"unverified,omitempty"`
	Rewritten            bool                `json:"rewritten,omitempty"`
	Rewrites             []httpRewriteJSON   `json:"rewrites,omitempty"`
}

type httpRewriteChangeJSON struct {
	Op        string `json:"op"`
	Name      string `json:"name"`
	Before    string `json:"before,omitempty"`
	HadBefore bool   `json:"had_before,omitempty"`
	After     string `json:"after,omitempty"`
	HasAfter  bool   `json:"has_after,omitempty"`
}

type httpRewriteJSON struct {
	Kind    string                  `json:"kind"`
	Rule    string                  `json:"rule,omitempty"`
	Action  string                  `json:"action,omitempty"`
	Side    string                  `json:"side,omitempty"`
	Target  string                  `json:"target,omitempty"`
	Status  int32                   `json:"status,omitempty"`
	Changes []httpRewriteChangeJSON `json:"changes,omitempty"`
	Run     *httpScriptRunJSON      `json:"run,omitempty"`
}

// httpScriptRunJSON is the run of a script hit: duration, error, skip reason
// and console lines.
type httpScriptRunJSON struct {
	Tag        string              `json:"tag"`
	Phase      string              `json:"phase,omitempty"`
	DurationUS int64               `json:"duration_us,omitempty"`
	Error      string              `json:"error,omitempty"`
	Reason     string              `json:"reason,omitempty"`
	Logs       []httpScriptLogJSON `json:"logs,omitempty"`
}

type httpScriptLogJSON struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func httpRewrites(in []*mitmapi.HTTPRewriteHit) []httpRewriteJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]httpRewriteJSON, 0, len(in))
	for _, h := range in {
		if h == nil {
			continue
		}
		hit := httpRewriteJSON{Kind: h.Kind, Rule: h.Rule, Action: h.Action, Side: h.Side, Target: h.Target, Status: h.Status}
		for _, c := range h.Changes {
			if c == nil {
				continue
			}
			hit.Changes = append(hit.Changes, httpRewriteChangeJSON{Op: c.Op, Name: c.Name, Before: c.Before, HadBefore: c.HadBefore, After: c.After, HasAfter: c.HasAfter})
		}
		if run := h.Run; run != nil {
			hit.Run = &httpScriptRunJSON{Tag: run.Tag, Phase: run.Phase, DurationUS: run.DurationUs, Error: run.Error, Reason: run.Reason}
			for _, l := range run.Logs {
				hit.Run.Logs = append(hit.Run.Logs, httpScriptLogJSON{Level: l.GetLevel(), Message: l.GetMessage()})
			}
		}
		out = append(out, hit)
	}
	return out
}

type httpEventJSON struct {
	Kind   string          `json:"kind"`
	Record *httpRecordJSON `json:"record,omitempty"`
}

type httpStatusJSON struct {
	Instance     string `json:"instance"`
	Enabled      bool   `json:"enabled"`
	BodyEnabled  bool   `json:"body_enabled"`
	RecordCount  uint32 `json:"record_count"`
	InProgress   uint32 `json:"in_progress"`
	ByteEstimate int64  `json:"byte_estimate"`
	MaxMemory    int64  `json:"max_memory"`
	MaxRecords   uint32 `json:"max_records"`
	Total        string `json:"total"`
	Evicted      string `json:"evicted"`
	BodyDropped  string `json:"body_dropped"`

	PersistEnabled bool   `json:"persist_enabled,omitempty"`
	PersistFailed  bool   `json:"persist_failed,omitempty"`
	PersistRecords uint32 `json:"persist_records,omitempty"`
	PersistBytes   int64  `json:"persist_bytes,omitempty"`
	PersistWritten string `json:"persist_written,omitempty"`
	PersistDropped string `json:"persist_dropped,omitempty"`
	PersistErrors  string `json:"persist_errors,omitempty"`
}

type httpFrameJSON struct {
	Instance string          `json:"instance"`
	Seq      string          `json:"seq"`
	Floor    string          `json:"floor"`
	Reset    bool            `json:"reset,omitempty"`
	Gap      bool            `json:"gap,omitempty"`
	Events   []httpEventJSON `json:"events"`
	Status   *httpStatusJSON `json:"status,omitempty"`
}

type httpListJSON struct {
	Instance   string           `json:"instance"`
	Records    []httpRecordJSON `json:"records"`
	NextBefore string           `json:"next_before,omitempty"`
	Status     *httpStatusJSON  `json:"status,omitempty"`
}

func u64(v uint64) string { return strconv.FormatUint(v, 10) }

func httpHeaders(in []*mitmapi.HTTPHeader) []httpHeaderJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]httpHeaderJSON, len(in))
	for i, h := range in {
		out[i] = httpHeaderJSON{Name: h.GetName(), Value: h.GetValue()}
	}
	return out
}

func httpState(state mitmapi.RecordState) string {
	switch state {
	case mitmapi.RecordState_RECORD_STATE_COMPLETE:
		return "complete"
	case mitmapi.RecordState_RECORD_STATE_FAILED:
		return "failed"
	case mitmapi.RecordState_RECORD_STATE_UPGRADED:
		return "upgraded"
	}
	return "in_progress"
}

func httpEventKind(kind mitmapi.EventKind) string {
	switch kind {
	case mitmapi.EventKind_EVENT_KIND_NEW:
		return "new"
	case mitmapi.EventKind_EVENT_KIND_UPDATE:
		return "update"
	case mitmapi.EventKind_EVENT_KIND_DONE:
		return "done"
	}
	return "unspecified"
}

func httpRecord(r *mitmapi.HTTPRecord) *httpRecordJSON {
	if r == nil {
		return nil
	}
	out := &httpRecordJSON{
		ID: r.Id, Instance: r.Instance, Seq: u64(r.Seq), CreatedSeq: u64(r.CreatedSeq),
		ConnectionID: r.ConnectionId, RequestIndex: r.RequestIndex,
		Method: r.Method, Scheme: r.Scheme, Authority: r.Authority, Host: r.Host, Path: r.Path, URL: r.Url, Proto: r.Proto,
		Status: r.Status, Interim: r.Interim,
		RequestHeaders: httpHeaders(r.RequestHeaders), ResponseHeaders: httpHeaders(r.ResponseHeaders),
		RequestTrailers: httpHeaders(r.RequestTrailers), ResponseTrailers: httpHeaders(r.ResponseTrailers),
		RequestBytes: r.RequestBytes, ResponseBytes: r.ResponseBytes,
		RequestBodySaved: r.RequestBodySaved, ResponseBodySaved: r.ResponseBodySaved,
		RequestBodyStored: r.RequestBodyStored, ResponseBodyStored: r.ResponseBodyStored,
		RequestBodyTruncated: r.RequestBodyTruncated, ResponseBodyTrunc: r.ResponseBodyTruncated,
		BodyDropped:     r.BodyDropped,
		RequestEncoding: r.RequestEncoding, ResponseEncoding: r.ResponseEncoding,
		State: httpState(r.State), Error: r.Error, Unverified: r.Unverified, Rewritten: r.Rewritten,
		Rewrites: httpRewrites(r.Rewrites),
	}
	if c := r.Connection; c != nil {
		out.Connection = &httpConnectionJSON{
			ID: c.Id, Source: c.Source, Destination: c.Destination, Host: c.Host,
			Inbound: c.Inbound, InboundType: c.InboundType, Outbound: c.Outbound, OutboundType: c.OutboundType,
			Rule: c.Rule, ProcessName: c.ProcessName, ProcessPath: c.ProcessPath, ProcessID: c.ProcessId, User: c.User,
		}
	}
	if t := r.Timing; t != nil {
		out.Timing = &httpTimingJSON{
			Start: t.Start, RequestHeaders: t.RequestHeaders, RequestBody: t.RequestBody, FirstByte: t.FirstByte,
			ResponseHeader: t.ResponseHeaders, ResponseBody: t.ResponseBody, End: t.End,
			DNS: t.Dns, Connect: t.Connect, TLS: t.Tls,
		}
	}
	return out
}

func httpStatus(s *mitmapi.CaptureStatus) *httpStatusJSON {
	if s == nil {
		return nil
	}
	return &httpStatusJSON{
		Instance: s.Instance, Enabled: s.Enabled, BodyEnabled: s.BodyEnabled,
		RecordCount: s.RecordCount, InProgress: s.InProgress, ByteEstimate: s.ByteEstimate,
		MaxMemory: s.MaxMemory, MaxRecords: s.MaxRecords,
		Total: u64(s.Total), Evicted: u64(s.Evicted), BodyDropped: u64(s.BodyDropped),
		PersistEnabled: s.PersistEnabled, PersistFailed: s.PersistFailed, PersistRecords: s.PersistRecords,
		PersistBytes: s.PersistBytes, PersistWritten: u64(s.PersistWritten), PersistDropped: u64(s.PersistDropped),
		PersistErrors: u64(s.PersistErrors),
	}
}

func httpFrame(p *mitmapi.CapturePayload) httpFrameJSON {
	frame := httpFrameJSON{
		Instance: p.Instance, Seq: u64(p.Seq), Floor: u64(p.Floor), Reset: p.GetReset_(), Gap: p.Gap,
		Events: make([]httpEventJSON, 0, len(p.Events)), Status: httpStatus(p.Status),
	}
	for _, e := range p.Events {
		frame.Events = append(frame.Events, httpEventJSON{Kind: httpEventKind(e.Kind), Record: httpRecord(e.Record)})
	}
	return frame
}

func (s *server) httpCaptureCapabilities(w http.ResponseWriter, r *http.Request) {
	client := s.httpCaptureClient()
	if client == nil {
		writeJSON(w, 200, map[string]any{"supported": s.httpCaptureSupported(), "enabled": false, "replay": false, "rewrite": s.httpRewriteSupported(), "persist": s.httpCapturePersistSupported(), "reason": "等待核心运行并建立控制连接"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpCaptureRPCTimeout)
	defer cancel()
	st, err := client.GetStatus(ctx, &emptypb.Empty{})
	switch status.Code(err) {
	case codes.OK:
		writeJSON(w, 200, map[string]any{"supported": true, "enabled": st.Enabled, "body_enabled": st.BodyEnabled, "status": httpStatus(st), "replay": httpReplaySupported(ctx, client), "rewrite": s.httpRewriteSupported(), "persist": s.httpCapturePersistSupported()})
	case codes.Unimplemented:
		writeJSON(w, 200, map[string]any{"supported": false, "enabled": false, "replay": false, "rewrite": false, "reason": "当前核心需要 HTTP 抓取扩展"})
	case codes.FailedPrecondition:
		writeJSON(w, 200, map[string]any{"supported": true, "enabled": false, "replay": false, "rewrite": s.httpRewriteSupported(), "reason": "HTTPS 解密未运行"})
	default:
		writeRPCError(w, err)
	}
}

func (s *server) httpCaptureRecords(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	query := r.URL.Query()
	before, ok := cursorParam(w, r, "before")
	if !ok {
		return
	}
	limit := uint64(httpCaptureDefaultLimit)
	if v := query.Get("limit"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 || n > httpCaptureMaxLimit {
			writeJSON(w, 400, map[string]string{"error": "limit 范围为 1–500"})
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpCaptureRPCTimeout)
	defer cancel()
	list, err := client.ListRecords(ctx, &mitmapi.ListRecordsRequest{
		Search: query.Get("search"), Host: query.Get("host"), ConnectionId: query.Get("connection_id"),
		Limit: uint32(limit), Before: before,
	}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	out := httpListJSON{Instance: list.Instance, Records: make([]httpRecordJSON, 0, len(list.Records)), Status: httpStatus(list.Status)}
	if list.NextBefore != 0 {
		out.NextBefore = u64(list.NextBefore)
	}
	for _, record := range list.Records {
		out.Records = append(out.Records, *httpRecord(record))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (s *server) httpCaptureRecord(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpCaptureRPCTimeout)
	defer cancel()
	record, err := client.GetRecord(ctx, &mitmapi.GetRecordRequest{Id: r.PathValue("id"), Instance: r.URL.Query().Get("instance")}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, httpRecord(record))
}

// httpCaptureBody streams the bytes of one body. The first chunk carries the
// view's facts, which become headers: X-Total (size of the view), X-Original-Size
// (bytes that passed), X-Truncated, X-Encoding, X-Saved, X-Complete,
// X-Offset and X-Decode-Error.
func (s *server) httpCaptureBody(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	query := r.URL.Query()
	side := mitmapi.BodySide_BODY_SIDE_REQUEST
	switch query.Get("side") {
	case "", "request":
	case "response":
		side = mitmapi.BodySide_BODY_SIDE_RESPONSE
	default:
		writeJSON(w, 400, map[string]string{"error": "side 为 request 或 response"})
		return
	}
	var offset, length int64
	for key, target := range map[string]*int64{"offset": &offset, "length": &length} {
		if v := query.Get(key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				writeJSON(w, 400, map[string]string{"error": "无效参数: " + key})
				return
			}
			*target = n
		}
	}
	decoded := false
	if v := query.Get("decoded"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "无效参数: decoded"})
			return
		}
		decoded = parsed
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := client.GetBody(ctx, &mitmapi.GetBodyRequest{
		Id: r.PathValue("id"), Instance: query.Get("instance"), Side: side, Offset: offset, Length: length, Decoded: decoded,
	}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	started := false
	for {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			if !started && recvErr != io.EOF {
				writeRPCError(w, recvErr)
			}
			return
		}
		if !started {
			header := w.Header()
			header.Set("Content-Type", "application/octet-stream")
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("Cache-Control", "no-store")
			header.Set("X-Total", strconv.FormatInt(chunk.Total, 10))
			header.Set("X-Original-Size", strconv.FormatInt(chunk.OriginalSize, 10))
			header.Set("X-Offset", strconv.FormatInt(chunk.Offset, 10))
			header.Set("X-Truncated", strconv.FormatBool(chunk.Truncated))
			header.Set("X-Saved", strconv.FormatBool(chunk.Saved))
			header.Set("X-Complete", strconv.FormatBool(chunk.Complete))
			header.Set("X-Encoding", chunk.Encoding)
			if chunk.DecodeError != "" {
				header.Set("X-Decode-Error", sanitizeHeader(chunk.DecodeError))
			}
			started = true
		}
		_ = controller.SetWriteDeadline(time.Now().Add(httpCaptureWriteTimeout))
		if _, err = w.Write(chunk.Data); err != nil {
			return
		}
	}
}

// sanitizeHeader makes a message safe as a header value.
func sanitizeHeader(text string) string {
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text) && len(out) < 256; i++ {
		c := text[i]
		if c < 0x20 || c > 0x7e {
			c = ' '
		}
		out = append(out, c)
	}
	return string(out)
}

func (s *server) clearHTTPCapture(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpCaptureRPCTimeout)
	defer cancel()
	st, err := client.Clear(ctx, &emptypb.Empty{})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": httpStatus(st)})
}

// deleteHTTPRecord removes one finished record from the list and from the
// stored copy.
func (s *server) deleteHTTPRecord(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpCaptureRPCTimeout)
	defer cancel()
	st, err := client.DeleteRecord(ctx, &mitmapi.DeleteRecordRequest{Id: r.PathValue("id"), Instance: r.URL.Query().Get("instance")})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": httpStatus(st)})
}

// purgeHTTPPersisted deletes every stored exchange. The core removes its
// files and list rows; the directory is also cleared here so that the call
// works while the core is stopped or HTTPS decryption is off.
func (s *server) purgeHTTPPersisted(w http.ResponseWriter, r *http.Request) {
	var st *mitmapi.CaptureStatus
	if client := s.httpCaptureClient(); client != nil {
		ctx, cancel := context.WithTimeout(r.Context(), httpCaptureRPCTimeout)
		defer cancel()
		var err error
		st, err = client.PurgePersisted(ctx, &emptypb.Empty{})
		switch status.Code(err) {
		case codes.OK, codes.Unimplemented, codes.FailedPrecondition, codes.Unavailable:
		default:
			writeRPCError(w, err)
			return
		}
	}
	if err := capture.PurgeDir(s.httpCapturePersistDir()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "无法删除已保存的数据"})
		return
	}
	writeJSON(w, 200, map[string]any{"status": httpStatus(st)})
}

// streamHTTPCapture bridges SubscribeEvents as NDJSON. Each line is one
// frame (instance, seq, floor, reset, gap, events, status). The core
// restarting ends the gRPC stream and so this response; the client resumes
// with its last instance and seq, and the new core answers with a reset
// frame under a new instance.
func (s *server) streamHTTPCapture(w http.ResponseWriter, r *http.Request) {
	client := s.requireHTTPCapture(w)
	if client == nil {
		return
	}
	after, ok := cursorParam(w, r, "after")
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := client.SubscribeEvents(ctx, &mitmapi.SubscribeEventsRequest{Instance: r.URL.Query().Get("instance"), After: after}, grpc.MaxCallRecvMsgSize(httpCaptureMaxMessage))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	encoder := json.NewEncoder(w)
	started := false
	for {
		payload, recvErr := stream.Recv()
		if recvErr != nil {
			if !started && ctx.Err() == nil && recvErr != io.EOF {
				writeRPCError(w, recvErr)
			}
			return
		}
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		// A client that stops reading fails the write at the deadline and
		// the handler returns, which cancels the gRPC stream.
		_ = controller.SetWriteDeadline(time.Now().Add(httpCaptureWriteTimeout))
		if encoder.Encode(httpFrame(payload)) != nil || controller.Flush() != nil {
			return
		}
	}
}
