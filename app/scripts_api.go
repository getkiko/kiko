package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	scriptRPCTimeout    = 5 * time.Second
	scriptEvalTimeout   = 70 * time.Second
	scriptMaxMessage    = 16 << 20
	scriptStreamTimeout = 5 * time.Second
)

func (s *server) scriptClient() mitmapi.ScriptClient {
	s.rpc.mu.Lock()
	defer s.rpc.mu.Unlock()
	return s.rpc.script
}

func (s *server) requireScript(w http.ResponseWriter) mitmapi.ScriptClient {
	client := s.scriptClient()
	if client == nil {
		writeJSON(w, 503, map[string]string{"error": "脚本等待核心连接"})
	}
	return client
}

// The JSON shapes below are the REST contract of /api/v1/scripts. Bytes are
// base64; 64-bit cursors and counters are strings.

type scriptHeaderJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type scriptRequestJSON struct {
	URL     string             `json:"url"`
	Method  string             `json:"method,omitempty"`
	Headers []scriptHeaderJSON `json:"headers,omitempty"`
	Body    string             `json:"body,omitempty"`
	HasBody bool               `json:"has_body,omitempty"`
}

type scriptResponseJSON struct {
	Status  int32              `json:"status,omitempty"`
	Headers []scriptHeaderJSON `json:"headers,omitempty"`
	Body    string             `json:"body,omitempty"`
	HasBody bool               `json:"has_body,omitempty"`
}

type scriptStoreEntryJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type scriptEvaluateRequestJSON struct {
	Source       string                 `json:"source"`
	Type         string                 `json:"type"`
	Pattern      string                 `json:"pattern,omitempty"`
	Argument     string                 `json:"argument,omitempty"`
	BinaryBody   bool                   `json:"binary_body,omitempty"`
	RequiresBody bool                   `json:"requires_body,omitempty"`
	TimeoutMS    int64                  `json:"timeout_ms,omitempty"`
	MaxSize      int64                  `json:"max_size,omitempty"`
	Request      *scriptRequestJSON     `json:"request,omitempty"`
	Response     *scriptResponseJSON    `json:"response,omitempty"`
	Tag          string                 `json:"tag,omitempty"`
	Store        []scriptStoreEntryJSON `json:"store,omitempty"`
	Rule         *scriptRuleInputJSON   `json:"rule,omitempty"`
}

// scriptRuleInputJSON is the $request of a rule or dns script trial run.
type scriptRuleInputJSON struct {
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

type scriptLogLineJSON struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	TimeMS  int64  `json:"time_ms"`
}

type scriptNotificationJSON struct {
	Seq      string `json:"seq,omitempty"`
	Script   string `json:"script,omitempty"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body,omitempty"`
	URL      string `json:"url,omitempty"`
	TimeMS   int64  `json:"time_ms"`
}

type scriptChangeJSON struct {
	Kind      string `json:"kind"`
	Op        string `json:"op,omitempty"`
	Name      string `json:"name,omitempty"`
	Before    string `json:"before,omitempty"`
	HadBefore bool   `json:"had_before,omitempty"`
	After     string `json:"after,omitempty"`
	HasAfter  bool   `json:"has_after,omitempty"`
}

type scriptEvaluateResponseJSON struct {
	Action         string                   `json:"action"`
	Reason         string                   `json:"reason,omitempty"`
	Error          string                   `json:"error,omitempty"`
	ErrorKind      string                   `json:"error_kind,omitempty"`
	DurationUS     int64                    `json:"duration_us"`
	Logs           []scriptLogLineJSON      `json:"logs,omitempty"`
	Notifications  []scriptNotificationJSON `json:"notifications,omitempty"`
	Changes        []scriptChangeJSON       `json:"changes,omitempty"`
	URL            string                   `json:"url,omitempty"`
	HeadersChanged bool                     `json:"headers_changed,omitempty"`
	Headers        []scriptHeaderJSON       `json:"headers,omitempty"`
	BodyChanged    bool                     `json:"body_changed,omitempty"`
	Body           string                   `json:"body,omitempty"`
	Status         int32                    `json:"status,omitempty"`
	Response       *scriptResponseJSON      `json:"response,omitempty"`
	Output         string                   `json:"output,omitempty"`
	Decision       string                   `json:"decision,omitempty"`
}

type scriptRunRequestJSON struct {
	Tag      string `json:"tag"`
	Argument string `json:"argument,omitempty"`
}

type scriptRunResponseJSON struct {
	Action     string              `json:"action"`
	Error      string              `json:"error,omitempty"`
	DurationUS int64               `json:"duration_us"`
	Logs       []scriptLogLineJSON `json:"logs,omitempty"`
	Output     string              `json:"output,omitempty"`
}

type scriptStateJSON struct {
	Tag            string `json:"tag"`
	Type           string `json:"type"`
	Runs           string `json:"runs"`
	Errors         string `json:"errors"`
	Timeouts       string `json:"timeouts"`
	Limits         string `json:"limits"`
	Skipped        string `json:"skipped"`
	Paused         bool   `json:"paused,omitempty"`
	PausedUntil    int64  `json:"paused_until,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	LastRunMS      int64  `json:"last_run_ms,omitempty"`
	LastDurationUS int64  `json:"last_duration_us,omitempty"`
	NextRunMS      int64  `json:"next_run_ms,omitempty"`
}

type scriptStatusJSON struct {
	Scripts         []scriptStateJSON `json:"scripts"`
	StorePersistent bool              `json:"store_persistent"`
	StoreBytes      string            `json:"store_bytes"`
	StoreLimit      string            `json:"store_limit"`
}

type scriptLogEntryJSON struct {
	Seq     string `json:"seq"`
	Script  string `json:"script"`
	Phase   string `json:"phase,omitempty"`
	Level   string `json:"level"`
	Message string `json:"message"`
	TimeMS  int64  `json:"time_ms"`
}

type scriptStoreInfoJSON struct {
	Persistent bool   `json:"persistent"`
	Bytes      string `json:"bytes"`
	Limit      string `json:"limit"`
}

type scriptStoreListJSON struct {
	Namespaces []struct {
		Name    string `json:"name"`
		Entries uint32 `json:"entries"`
		Bytes   string `json:"bytes"`
	} `json:"namespaces"`
	Entries []struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Size      uint32 `json:"size"`
		Truncated bool   `json:"truncated,omitempty"`
	} `json:"entries"`
	Info *scriptStoreInfoJSON `json:"info,omitempty"`
}

type scriptStoreValueJSON struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Found     bool   `json:"found"`
}

func scriptHeadersIn(in []scriptHeaderJSON) []*mitmapi.ScriptHeader {
	out := make([]*mitmapi.ScriptHeader, len(in))
	for i, h := range in {
		out[i] = &mitmapi.ScriptHeader{Name: h.Name, Value: h.Value}
	}
	return out
}

func scriptHeadersOut(in []*mitmapi.ScriptHeader) []scriptHeaderJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]scriptHeaderJSON, len(in))
	for i, h := range in {
		out[i] = scriptHeaderJSON{Name: h.GetName(), Value: h.GetValue()}
	}
	return out
}

func scriptBody(text string) ([]byte, bool) {
	if text == "" {
		return nil, true
	}
	body, err := base64.StdEncoding.DecodeString(text)
	return body, err == nil
}

func scriptLogsOut(in []*mitmapi.ScriptLogLine) []scriptLogLineJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]scriptLogLineJSON, len(in))
	for i, l := range in {
		out[i] = scriptLogLineJSON{Level: l.GetLevel(), Message: l.GetMessage(), TimeMS: l.GetTimeMs()}
	}
	return out
}

func scriptNotificationOut(n *mitmapi.ScriptNotification) scriptNotificationJSON {
	out := scriptNotificationJSON{Script: n.GetScript(), Title: n.GetTitle(), Subtitle: n.GetSubtitle(), Body: n.GetBody(), URL: n.GetUrl(), TimeMS: n.GetTimeMs()}
	if n.GetSeq() != 0 {
		out.Seq = u64(n.GetSeq())
	}
	return out
}

func scriptBadRequest(w http.ResponseWriter, message string) {
	writeJSON(w, 400, map[string]string{"error": message})
}

func (s *server) scriptEvaluate(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	var in scriptEvaluateRequestJSON
	if !decode(w, r, &in) {
		return
	}
	req := &mitmapi.ScriptEvaluateRequest{
		Source: in.Source, Type: in.Type, Pattern: in.Pattern, Argument: in.Argument, BinaryBody: in.BinaryBody,
		RequiresBody: in.RequiresBody, TimeoutMs: in.TimeoutMS, MaxSize: in.MaxSize, Tag: in.Tag,
	}
	if m := in.Request; m != nil {
		body, ok := scriptBody(m.Body)
		if !ok {
			scriptBadRequest(w, "请求正文不是有效的 base64")
			return
		}
		req.Request = &mitmapi.ScriptRequestMessage{Url: m.URL, Method: m.Method, Headers: scriptHeadersIn(m.Headers), Body: body, HasBody: m.HasBody}
	}
	if m := in.Response; m != nil {
		body, ok := scriptBody(m.Body)
		if !ok {
			scriptBadRequest(w, "响应正文不是有效的 base64")
			return
		}
		req.Response = &mitmapi.ScriptResponseMessage{Status: m.Status, Headers: scriptHeadersIn(m.Headers), Body: body, HasBody: m.HasBody}
	}
	for _, e := range in.Store {
		req.Store = append(req.Store, &mitmapi.ScriptStoreEntry{Key: e.Key, Value: e.Value})
	}
	if m := in.Rule; m != nil {
		req.Rule = &mitmapi.ScriptRuleInput{
			Domain: m.Domain, Ip: m.IP, Port: m.Port, Network: m.Network, Protocol: m.Protocol, Inbound: m.Inbound,
			InboundType: m.InboundType, SourceIp: m.SourceIP, ProcessName: m.ProcessName, ProcessPath: m.ProcessPath,
			User: m.User, QueryType: m.QueryType,
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptEvalTimeout)
	defer cancel()
	res, err := client.Evaluate(ctx, req, grpc.MaxCallRecvMsgSize(scriptMaxMessage), grpc.MaxCallSendMsgSize(scriptMaxMessage))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	out := scriptEvaluateResponseJSON{
		Action: res.Action, Reason: res.Reason, Error: res.Error, ErrorKind: res.ErrorKind, DurationUS: res.DurationUs,
		Logs: scriptLogsOut(res.Logs), URL: res.Url, HeadersChanged: res.HeadersChanged, Headers: scriptHeadersOut(res.Headers),
		BodyChanged: res.BodyChanged, Status: res.Status, Output: res.Output, Decision: res.Decision,
	}
	if res.BodyChanged {
		out.Body = base64.StdEncoding.EncodeToString(res.Body)
	}
	for _, n := range res.Notifications {
		out.Notifications = append(out.Notifications, scriptNotificationOut(n))
	}
	for _, c := range res.Changes {
		out.Changes = append(out.Changes, scriptChangeJSON{Kind: c.Kind, Op: c.Op, Name: c.Name, Before: c.Before, HadBefore: c.HadBefore, After: c.After, HasAfter: c.HasAfter})
	}
	if m := res.Response; m != nil {
		out.Response = &scriptResponseJSON{Status: m.Status, Headers: scriptHeadersOut(m.Headers), HasBody: m.HasBody}
		if m.HasBody {
			out.Response.Body = base64.StdEncoding.EncodeToString(m.Body)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func (s *server) scriptRun(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	var in scriptRunRequestJSON
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Tag) == "" {
		scriptBadRequest(w, "请指定脚本标识")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptEvalTimeout)
	defer cancel()
	res, err := client.Run(ctx, &mitmapi.ScriptRunRequest{Tag: in.Tag, Argument: in.Argument})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, 200, scriptRunResponseJSON{Action: res.Action, Error: res.Error, DurationUS: res.DurationUs, Logs: scriptLogsOut(res.Logs), Output: res.Output})
}

func (s *server) scriptStatus(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptRPCTimeout)
	defer cancel()
	list, err := client.GetStatus(ctx, &emptypb.Empty{})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	out := scriptStatusJSON{Scripts: make([]scriptStateJSON, 0, len(list.Scripts)), StorePersistent: list.StorePersistent, StoreBytes: u64(list.StoreBytes), StoreLimit: u64(list.StoreLimit)}
	for _, st := range list.Scripts {
		out.Scripts = append(out.Scripts, scriptStateJSON{
			Tag: st.Tag, Type: st.Type, Runs: u64(st.Runs), Errors: u64(st.Errors), Timeouts: u64(st.Timeouts), Limits: u64(st.Limits),
			Skipped: u64(st.Skipped), Paused: st.Paused, PausedUntil: st.PausedUntil, LastError: st.LastError, LastRunMS: st.LastRunMs, LastDurationUS: st.LastDurationUs, NextRunMS: st.NextRunMs,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// scriptStream copies a gRPC stream to the response as NDJSON, one object
// per entry.
func scriptStream[T any](w http.ResponseWriter, r *http.Request, open func(ctx context.Context) (grpc.ServerStreamingClient[T], error), convert func(*T) any) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := open(ctx)
	if err != nil {
		writeRPCError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	encoder := json.NewEncoder(w)
	started := false
	for {
		entry, recvErr := stream.Recv()
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
		_ = controller.SetWriteDeadline(time.Now().Add(scriptStreamTimeout))
		if encoder.Encode(convert(entry)) != nil || controller.Flush() != nil {
			return
		}
	}
}

func (s *server) scriptLogs(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	after, ok := cursorParam(w, r, "after")
	if !ok {
		return
	}
	tag := r.URL.Query().Get("tag")
	scriptStream(w, r, func(ctx context.Context) (grpc.ServerStreamingClient[mitmapi.ScriptLogEntry], error) {
		return client.SubscribeLogs(ctx, &mitmapi.ScriptSubscribeRequest{After: after, Tag: tag})
	}, func(e *mitmapi.ScriptLogEntry) any {
		return scriptLogEntryJSON{Seq: u64(e.Seq), Script: e.Script, Phase: e.Phase, Level: e.Level, Message: e.Message, TimeMS: e.TimeMs}
	})
}

func (s *server) scriptNotifications(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	after, ok := cursorParam(w, r, "after")
	if !ok {
		return
	}
	scriptStream(w, r, func(ctx context.Context) (grpc.ServerStreamingClient[mitmapi.ScriptNotification], error) {
		return client.SubscribeNotifications(ctx, &mitmapi.ScriptSubscribeRequest{After: after})
	}, func(n *mitmapi.ScriptNotification) any { return scriptNotificationOut(n) })
}

func scriptInfoOut(i *mitmapi.ScriptStoreInfo) *scriptStoreInfoJSON {
	if i == nil {
		return nil
	}
	return &scriptStoreInfoJSON{Persistent: i.Persistent, Bytes: u64(i.Bytes), Limit: u64(i.Limit)}
}

func (s *server) scriptStoreList(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptRPCTimeout)
	defer cancel()
	list, err := client.StoreList(ctx, &mitmapi.ScriptStoreListRequest{Namespace: r.URL.Query().Get("namespace")}, grpc.MaxCallRecvMsgSize(scriptMaxMessage))
	if err != nil {
		writeRPCError(w, err)
		return
	}
	out := scriptStoreListJSON{Info: scriptInfoOut(list.Info)}
	out.Namespaces = make([]struct {
		Name    string `json:"name"`
		Entries uint32 `json:"entries"`
		Bytes   string `json:"bytes"`
	}, 0, len(list.Namespaces))
	for _, n := range list.Namespaces {
		out.Namespaces = append(out.Namespaces, struct {
			Name    string `json:"name"`
			Entries uint32 `json:"entries"`
			Bytes   string `json:"bytes"`
		}{n.Name, n.Entries, u64(n.Bytes)})
	}
	out.Entries = make([]struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Size      uint32 `json:"size"`
		Truncated bool   `json:"truncated,omitempty"`
	}, 0, len(list.Entries))
	for _, e := range list.Entries {
		out.Entries = append(out.Entries, struct {
			Key       string `json:"key"`
			Value     string `json:"value"`
			Size      uint32 `json:"size"`
			Truncated bool   `json:"truncated,omitempty"`
		}{e.Key, e.Value, e.Size, e.Truncated})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func scriptStoreKeyParams(w http.ResponseWriter, r *http.Request) (namespace, key string, ok bool) {
	namespace, key = r.URL.Query().Get("namespace"), r.URL.Query().Get("key")
	if namespace == "" || key == "" {
		scriptBadRequest(w, "请指定 namespace 与 key")
		return "", "", false
	}
	return namespace, key, true
}

func (s *server) scriptStoreGet(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	namespace, key, ok := scriptStoreKeyParams(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptRPCTimeout)
	defer cancel()
	value, err := client.StoreGet(ctx, &mitmapi.ScriptStoreKey{Namespace: namespace, Key: key})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, scriptStoreValueJSON{Namespace: namespace, Key: key, Value: value.Value, Found: value.Found})
}

func (s *server) scriptStoreSet(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	var in scriptStoreValueJSON
	if !decode(w, r, &in) {
		return
	}
	if in.Namespace == "" || in.Key == "" {
		scriptBadRequest(w, "请指定 namespace 与 key")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptRPCTimeout)
	defer cancel()
	info, err := client.StoreSet(ctx, &mitmapi.ScriptStoreSetRequest{Namespace: in.Namespace, Key: in.Key, Value: in.Value})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, 200, scriptInfoOut(info))
}

func (s *server) scriptStoreDelete(w http.ResponseWriter, r *http.Request) {
	client := s.requireScript(w)
	if client == nil {
		return
	}
	namespace, key, ok := scriptStoreKeyParams(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scriptRPCTimeout)
	defer cancel()
	info, err := client.StoreDelete(ctx, &mitmapi.ScriptStoreKey{Namespace: namespace, Key: key})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, 200, scriptInfoOut(info))
}
