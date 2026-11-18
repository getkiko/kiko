package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

var protoJSON = protojson.MarshalOptions{EmitUnpopulated: true}

// endpointState holds the latest snapshot (protojson) of each endpoint status
// subscription, for new SSE connections and /endpoints/status.
type endpointState struct {
	mu       sync.Mutex
	snapshot map[string]json.RawMessage
	taildrop map[string]json.RawMessage
}

func (s *server) publishEndpointEvent(kind string, message proto.Message) {
	raw, err := protoJSON.Marshal(message)
	if err != nil {
		return
	}
	s.ep.mu.Lock()
	if s.ep.snapshot == nil {
		s.ep.snapshot = map[string]json.RawMessage{}
	}
	s.ep.snapshot[kind] = raw
	s.ep.mu.Unlock()
	s.emit(kind, json.RawMessage(raw))
}

func (s *server) publishTaildrop(tag string, inbox *daemon.TaildropInbox) {
	raw, err := protoJSON.Marshal(inbox)
	if err != nil {
		return
	}
	s.ep.mu.Lock()
	if s.ep.taildrop == nil {
		s.ep.taildrop = map[string]json.RawMessage{}
	}
	s.ep.taildrop[tag] = raw
	s.ep.mu.Unlock()
	s.emit("taildrop", json.RawMessage(raw))
}

func (s *server) resetEndpointSnapshots() {
	s.ep.mu.Lock()
	s.ep.snapshot, s.ep.taildrop = nil, nil
	s.ep.mu.Unlock()
}

// endpointSnapshotMessages returns the latest snapshot events, ready to write to SSE.
func (s *server) endpointSnapshotMessages() [][]byte {
	s.ep.mu.Lock()
	var messages [][]byte
	for kind, raw := range s.ep.snapshot {
		b, _ := json.Marshal(map[string]any{"type": kind, "data": raw})
		messages = append(messages, b)
	}
	for _, raw := range s.ep.taildrop {
		b, _ := json.Marshal(map[string]any{"type": "taildrop", "data": raw})
		messages = append(messages, b)
	}
	s.ep.mu.Unlock()
	if share := s.usbipShareState(); share.Running || share.Error != "" {
		b, _ := json.Marshal(map[string]any{"type": "usbipShare", "data": share})
		messages = append(messages, b)
	}
	return messages
}

type recvStream[T any] interface {
	Recv() (T, error)
}

// followStream retries interrupted subscriptions and invalidates stale snapshots.
// Unsupported streams remain disabled until the next core session.
func followStream[T any](s *server, ctx context.Context, name string, open func(context.Context) (recvStream[T], error), handle func(T), disconnected ...func()) {
	for {
		stream, err := open(ctx)
		for err == nil {
			var message T
			if message, err = stream.Recv(); err == nil {
				handle(message)
			}
		}
		if ctx.Err() != nil {
			return
		}
		for _, notify := range disconnected {
			notify()
		}
		switch status.Code(err) {
		case codes.Unimplemented, codes.NotFound:
			if s.logs != nil {
				s.logs.add("[kiko] gRPC " + name + " 不可用: " + status.Convert(err).Message())
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// startEndpointSubscriptions subscribes to endpoint and USB/IP status once the gRPC
// session is established; the subscriptions end with the session ctx.
func (s *server) startEndpointSubscriptions(ctx context.Context, client daemon.StartedServiceClient) {
	inboxes := &taildropInboxes{cancels: map[string]context.CancelFunc{}}
	go func() {
		<-ctx.Done()
		inboxes.stopAll()
	}()
	go followStream(s, ctx, "Tailscale 状态", func(c context.Context) (recvStream[*daemon.TailscaleStatusUpdate], error) {
		return client.SubscribeTailscaleStatus(c, &emptypb.Empty{})
	}, func(update *daemon.TailscaleStatusUpdate) {
		s.publishEndpointEvent("tailscale", update)
		tags := make([]string, 0, len(update.GetEndpoints()))
		for _, endpoint := range update.GetEndpoints() {
			tags = append(tags, endpoint.GetEndpointTag())
		}
		inboxes.sync(ctx, tags, func(inboxCtx context.Context, tag string) {
			followStream(s, inboxCtx, "Taildrop 收件箱", func(c context.Context) (recvStream[*daemon.TaildropInbox], error) {
				return client.SubscribeTaildropInbox(c, &daemon.SubscribeTaildropInboxRequest{EndpointTag: tag})
			}, func(inbox *daemon.TaildropInbox) {
				if inbox.GetEndpointTag() == "" {
					inbox.EndpointTag = tag
				}
				s.publishTaildrop(tag, inbox)
			})
		})
	}, func() { s.publishEndpointEvent("tailscale", &daemon.TailscaleStatusUpdate{}) })
	go followStream(s, ctx, "OpenConnect 状态", func(c context.Context) (recvStream[*daemon.OpenConnectStatusUpdate], error) {
		return client.SubscribeOpenConnectStatus(c, &emptypb.Empty{})
	}, func(update *daemon.OpenConnectStatusUpdate) { s.publishEndpointEvent("openconnect", update) })
	go followStream(s, ctx, "OpenVPN 状态", func(c context.Context) (recvStream[*daemon.OpenVPNStatusUpdate], error) {
		return client.SubscribeOpenVPNStatus(c, &emptypb.Empty{})
	}, func(update *daemon.OpenVPNStatusUpdate) { s.publishEndpointEvent("openvpn", update) })
	go followStream(s, ctx, "USB/IP 状态", func(c context.Context) (recvStream[*daemon.USBIPServerStatusUpdate], error) {
		return client.SubscribeUSBIPServerStatus(c, &emptypb.Empty{})
	}, func(update *daemon.USBIPServerStatusUpdate) { s.publishEndpointEvent("usbip", update) })
}

type taildropInboxes struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (t *taildropInboxes) sync(ctx context.Context, tags []string, start func(context.Context, string)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	wanted := make(map[string]bool, len(tags))
	for _, tag := range tags {
		wanted[tag] = true
		if _, exists := t.cancels[tag]; !exists && ctx.Err() == nil {
			inboxCtx, cancel := context.WithCancel(ctx)
			t.cancels[tag] = cancel
			go start(inboxCtx, tag)
		}
	}
	for tag, cancel := range t.cancels {
		if !wanted[tag] {
			cancel()
			delete(t.cancels, tag)
		}
	}
}

func (t *taildropInboxes) stopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for tag, cancel := range t.cancels {
		cancel()
		delete(t.cancels, tag)
	}
}

// ---------- REST helpers ----------

func (s *server) requireClient(w http.ResponseWriter) daemon.StartedServiceClient {
	client := s.grpcClient()
	if client == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "gRPC API 未连接"})
	}
	return client
}

func writeRPCError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	switch status.Code(err) {
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.Unimplemented:
		code = http.StatusNotImplemented
	case codes.FailedPrecondition, codes.Unavailable:
		code = http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		code = http.StatusGatewayTimeout
	}
	writeJSON(w, code, map[string]any{"error": status.Convert(err).Message()})
}

// rpcCall decodes the JSON request body and runs a unary RPC, handling the 503 case
// and error-code mapping in one place.
func rpcCall[T any](s *server, w http.ResponseWriter, r *http.Request, call func(ctx context.Context, client daemon.StartedServiceClient, in *T) error) {
	var in T
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := call(ctx, client, &in); err != nil {
		writeRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type ndjsonWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	started bool
}

func (n *ndjsonWriter) line(b []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.started {
		n.w.Header().Set("Content-Type", "application/x-ndjson")
		n.w.Header().Set("Cache-Control", "no-cache")
		n.w.WriteHeader(http.StatusOK)
		n.started = true
	}
	_, _ = n.w.Write(append(append([]byte{}, b...), '\n'))
	_ = http.NewResponseController(n.w).Flush()
}

func (n *ndjsonWriter) errorLine(message string) {
	b, _ := json.Marshal(map[string]any{"error": message})
	n.line(b)
}

func (n *ndjsonWriter) begun() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.started
}

func (s *server) registerEndpointRoutes(m *routeMux) {
	m.handle("POST /api/v1/policy/test", routeRead, "", s.testPolicy)
	m.get("GET /api/v1/endpoints/status", s.endpointsStatus)
	m.handle("POST /api/v1/tailscale/exit-node", routeWrite, "切换出口节点", s.tailscaleExitNode)
	m.handle("POST /api/v1/tailscale/logout", routeWrite, "退出 Tailscale", s.tailscaleLogout)
	m.handle("POST /api/v1/tailscale/ping", routeRead, "", s.tailscalePing)
	m.get("GET /api/v1/tailscale/diagnostics", s.tailscaleDiagnostics)
	m.get("GET /api/v1/tailscale/ssh", s.tailscaleSSH)
	m.handle("POST /api/v1/openconnect/auth", routeWrite, "OpenConnect 认证", s.openConnectAuth)
	m.handle("POST /api/v1/openconnect/cancel", routeWrite, "取消 OpenConnect 认证", s.openConnectCancel)
	m.handle("POST /api/v1/openvpn/challenge", routeWrite, "OpenVPN 验证", s.openVPNChallenge)
	m.handle("POST /api/v1/openvpn/cancel", routeWrite, "取消 OpenVPN 验证", s.openVPNCancel)
	m.handle("POST /api/v1/taildrop/read", routeWrite, "标记 Taildrop 已读", s.taildropRead)
	m.get("GET /api/v1/taildrop/file", s.taildropDownload)
	m.handle("DELETE /api/v1/taildrop/file", routeWrite, "删除 Taildrop 文件", s.taildropDelete)
	m.handle("POST /api/v1/taildrop/cancel", routeWrite, "取消 Taildrop 接收", s.taildropCancel)
	m.handle("POST /api/v1/taildrop/send", routeOwn, "发送 Taildrop 文件", s.taildropSend)
	m.get("GET /api/v1/taildrop/sends", s.taildropSends)
	m.handle("POST /api/v1/taildrop/send/cancel", routeWrite, "取消 Taildrop 发送", s.taildropSendCancel)
	m.get("GET /api/v1/taildrop/directory", s.taildropDirectoryInfo)
	m.handle("POST /api/v1/taildrop/directory/open", routeWrite, "打开接收目录", s.taildropOpenDirectory)
	m.get("GET /api/v1/usbip/status", s.usbipStatus)
	m.get("GET /api/v1/usbip/preflight", s.usbipPreflight)
	m.get("GET /api/v1/usb/devices", s.usbDevices)
	m.get("GET /api/v1/usbip/share", s.getUSBIPShare)
	m.handle("POST /api/v1/usbip/share", routeWrite, "共享 USB 设备", s.startUSBIPShare)
	m.handle("DELETE /api/v1/usbip/share", routeWrite, "停止共享 USB 设备", s.stopUSBIPShareHandler)
}

func (s *server) testPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Section         string `json:"section"`
		Inbound         string `json:"inbound"`
		SourceIP        string `json:"sourceIp"`
		Domain          string `json:"domain"`
		DestinationIP   string `json:"destinationIp"`
		Network         string `json:"network"`
		Protocol        string `json:"protocol"`
		SourcePort      uint32 `json:"sourcePort"`
		DestinationPort uint32 `json:"destinationPort"`
	}
	if !decode(w, r, &body) {
		return
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := client.TestPolicy(ctx, &daemon.PolicyTestRequest{
		Section: body.Section, Inbound: body.Inbound, SourceIp: body.SourceIP,
		Domain: body.Domain, DestinationIp: body.DestinationIP,
		Network: body.Network, Protocol: body.Protocol,
		SourcePort: body.SourcePort, DestinationPort: body.DestinationPort,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "当前核心版本需要更新"})
			return
		}
		writeRPCError(w, err)
		return
	}
	data, err := protoJSON.Marshal(result)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "测试结果编码失败"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *server) endpointsStatus(w http.ResponseWriter, r *http.Request) {
	s.ep.mu.Lock()
	result := map[string]any{
		"tailscale":   s.ep.snapshot["tailscale"],
		"openconnect": s.ep.snapshot["openconnect"],
		"openvpn":     s.ep.snapshot["openvpn"],
		"usbip":       s.ep.snapshot["usbip"],
		"taildrop":    s.ep.taildrop,
	}
	s.ep.mu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

func (s *server) usbipStatus(w http.ResponseWriter, r *http.Request) {
	s.ep.mu.Lock()
	raw := s.ep.snapshot["usbip"]
	s.ep.mu.Unlock()
	writeJSON(w, http.StatusOK, raw)
}

// ---------- Tailscale ----------

type endpointTagBody struct {
	EndpointTag string `json:"endpointTag"`
}

func (s *server) tailscaleExitNode(w http.ResponseWriter, r *http.Request) {
	type body struct {
		EndpointTag string `json:"endpointTag"`
		StableID    string `json:"stableID"`
	}
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *body) error {
		_, err := client.SetTailscaleExitNode(ctx, &daemon.SetTailscaleExitNodeRequest{EndpointTag: in.EndpointTag, StableID: in.StableID})
		return err
	})
}

func (s *server) tailscaleLogout(w http.ResponseWriter, r *http.Request) {
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *endpointTagBody) error {
		_, err := client.TailscaleLogout(ctx, &daemon.TailscaleLogoutRequest{EndpointTag: in.EndpointTag})
		return err
	})
}

// tailscaleDiagnostics is the endpoint's network check and peer paths;
// refresh=1 runs a new network check first.
func (s *server) tailscaleDiagnostics(w http.ResponseWriter, r *http.Request) {
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := client.GetTailscaleDiagnostics(ctx, &daemon.TailscaleDiagnosticsRequest{
		EndpointTag: r.URL.Query().Get("endpointTag"), Refresh: r.URL.Query().Get("refresh") == "1",
	})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	data, err := protoJSON.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *server) tailscalePing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EndpointTag string `json:"endpointTag"`
		PeerIP      string `json:"peerIP"`
		Continuous  bool   `json:"continuous"`
	}
	if !decode(w, r, &in) {
		return
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	if !in.Continuous {
		cancel()
		ctx, cancel = context.WithTimeout(r.Context(), 10*time.Second)
	}
	defer cancel()
	stream, err := client.StartTailscalePing(ctx, &daemon.TailscalePingRequest{EndpointTag: in.EndpointTag, PeerIP: in.PeerIP})
	if err != nil {
		if status.Code(err) == codes.DeadlineExceeded {
			writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "Ping 超时，请重试"})
			return
		}
		writeRPCError(w, err)
		return
	}
	out := &ndjsonWriter{w: w}
	for {
		message, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			if !out.begun() {
				out.errorLine("Ping 未返回结果")
			}
			return
		}
		if recvErr != nil {
			if status.Code(recvErr) == codes.DeadlineExceeded {
				if !out.begun() {
					writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "Ping 超时，请重试"})
				} else {
					out.errorLine("Ping 超时，请重试")
				}
				return
			}
			if !out.begun() {
				writeRPCError(w, recvErr)
			} else if r.Context().Err() == nil {
				out.errorLine(status.Convert(recvErr).Message())
			}
			return
		}
		b, _ := protoJSON.Marshal(message)
		out.line(b)
		if !in.Continuous {
			return // One-shot actions cancel after the first result.
		}
	}
}

// ---------- OpenConnect / OpenVPN ----------

func (s *server) openConnectAuth(w http.ResponseWriter, r *http.Request) {
	type body struct {
		EndpointTag string `json:"endpointTag"`
		ChallengeID string `json:"challengeID"`
		Form        *struct {
			Values map[string]string `json:"values"`
		} `json:"form"`
		Browser json.RawMessage `json:"browser"`
	}
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *body) error {
		if len(in.Browser) > 0 || in.Form == nil {
			return status.Error(codes.InvalidArgument, "仅支持表单认证，浏览器(SSO)认证暂不支持")
		}
		_, err := client.SubmitOpenConnectAuthResponse(ctx, &daemon.OpenConnectAuthResponseSubmission{
			EndpointTag: in.EndpointTag, ChallengeID: in.ChallengeID,
			Response: &daemon.OpenConnectAuthResponseSubmission_Form{Form: &daemon.OpenConnectAuthFormResponse{Values: in.Form.Values}},
		})
		return err
	})
}

type challengeBody struct {
	EndpointTag string `json:"endpointTag"`
	ChallengeID string `json:"challengeID"`
}

func (s *server) openConnectCancel(w http.ResponseWriter, r *http.Request) {
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *challengeBody) error {
		_, err := client.CancelOpenConnectAuthChallenge(ctx, &daemon.OpenConnectAuthChallengeCancel{EndpointTag: in.EndpointTag, ChallengeID: in.ChallengeID})
		return err
	})
}

func (s *server) openVPNChallenge(w http.ResponseWriter, r *http.Request) {
	type body struct {
		EndpointTag string `json:"endpointTag"`
		ChallengeID string `json:"challengeID"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		Secret      string `json:"secret"`
	}
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *body) error {
		_, err := client.SubmitOpenVPNChallengeResponse(ctx, &daemon.OpenVPNChallengeSubmission{
			EndpointTag: in.EndpointTag, ChallengeID: in.ChallengeID, Username: in.Username, Password: in.Password, Secret: in.Secret,
		})
		return err
	})
}

func (s *server) openVPNCancel(w http.ResponseWriter, r *http.Request) {
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *challengeBody) error {
		_, err := client.CancelOpenVPNChallenge(ctx, &daemon.OpenVPNChallengeCancel{EndpointTag: in.EndpointTag, ChallengeID: in.ChallengeID})
		return err
	})
}

// ---------- Taildrop ----------

func (s *server) taildropRead(w http.ResponseWriter, r *http.Request) {
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *endpointTagBody) error {
		_, err := client.MarkTaildropInboxRead(ctx, &daemon.MarkTaildropInboxReadRequest{EndpointTag: in.EndpointTag})
		return err
	})
}

func (s *server) taildropDelete(w http.ResponseWriter, r *http.Request) {
	type body struct {
		EndpointTag string `json:"endpointTag"`
		Name        string `json:"name"`
	}
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *body) error {
		_, err := client.DeleteTaildropFile(ctx, &daemon.DeleteTaildropFileRequest{EndpointTag: in.EndpointTag, Name: in.Name})
		return err
	})
}

func (s *server) taildropCancel(w http.ResponseWriter, r *http.Request) {
	type body struct {
		EndpointTag string `json:"endpointTag"`
		SenderID    string `json:"senderID"`
		Name        string `json:"name"`
	}
	rpcCall(s, w, r, func(ctx context.Context, client daemon.StartedServiceClient, in *body) error {
		_, err := client.CancelTaildropReceiving(ctx, &daemon.CancelTaildropReceivingRequest{EndpointTag: in.EndpointTag, SenderID: in.SenderID, Name: in.Name})
		return err
	})
}

func (s *server) taildropDownload(w http.ResponseWriter, r *http.Request) {
	tag, name := r.URL.Query().Get("endpointTag"), r.URL.Query().Get("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 name"})
		return
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	stream, err := client.DownloadTaildropFile(r.Context(), &daemon.DownloadTaildropFileRequest{EndpointTag: tag, Name: name})
	if err != nil {
		writeRPCError(w, err)
		return
	}
	first, err := stream.Recv()
	if err != nil {
		writeRPCError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	if first.GetSize() >= 0 {
		w.Header().Set("Content-Length", fmt.Sprint(first.GetSize()))
	}
	_, _ = w.Write(first.GetData())
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return
		}
		if recvErr != nil {
			panic(http.ErrAbortHandler)
		}
		if _, writeErr := w.Write(chunk.GetData()); writeErr != nil {
			return
		}
	}
}

type taildropManifestEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

var errTaildropEndpointNotFound = errors.New("Tailscale endpoint not found")

func (s *server) taildropDirectoryFor(tag string) (string, error) {
	var config struct {
		Endpoints []struct {
			Type              string `json:"type"`
			Tag               string `json:"tag"`
			TaildropDirectory string `json:"taildrop_directory"`
		} `json:"endpoints"`
	}
	if err := readJSON(filepath.Join(s.dataDir, "runtime", "config.json"), &config); err != nil {
		return "", err
	}
	for _, endpoint := range config.Endpoints {
		if endpoint.Type != "tailscale" || endpoint.Tag != tag {
			continue
		}
		directory := os.ExpandEnv(endpoint.TaildropDirectory)
		if directory == "" {
			directory = "Taildrop"
		}
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(s.dataDir, directory)
		}
		return filepath.Abs(directory)
	}
	return "", errTaildropEndpointNotFound
}

func (s *server) taildropDirectoryInfo(w http.ResponseWriter, r *http.Request) {
	directory, err := s.taildropDirectoryFor(r.URL.Query().Get("endpointTag"))
	if err != nil {
		writeTaildropDirectoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": directory})
}

func (s *server) taildropOpenDirectory(w http.ResponseWriter, r *http.Request) {
	var body endpointTagBody
	if !decode(w, r, &body) {
		return
	}
	directory, err := s.taildropDirectoryFor(body.EndpointTag)
	if err != nil {
		writeTaildropDirectoryError(w, err)
		return
	}
	if s.usePrivilegedHelper && runtime.GOOS == "darwin" {
		base, baseErr := filepath.Abs(s.dataDir)
		relative, relativeErr := filepath.Rel(base, directory)
		if baseErr == nil && relativeErr == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			if err = repairTaildropAccess(base, body.EndpointTag); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		http.Error(w, "Taildrop directory is unavailable", http.StatusNotFound)
		return
	}
	if err = showInFileManager(r.Context(), directory, false); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errNoFileManager) {
			code = http.StatusNotImplemented
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeTaildropDirectoryError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, errTaildropEndpointNotFound) {
		code = http.StatusNotFound
	}
	http.Error(w, err.Error(), code)
}

func validTaildropName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}

// taildropSend's multipart contract: the first part must be a form field named
// manifest containing JSON [{"name","size"}] (the SendTaildropFiles start message
// must declare every file size up front), followed by one file part per manifest
// entry in manifest order (filename matching the manifest).
func (s *server) taildropSend(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("async") == "true" {
		s.taildropSendAsync(w, r)
		return
	}
	tag, peer := r.URL.Query().Get("endpointTag"), r.URL.Query().Get("peer")
	if peer == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 peer"})
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "manifest" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "第一个 part 必须是 manifest"})
		return
	}
	var manifest []taildropManifestEntry
	if err = json.NewDecoder(io.LimitReader(part, 1<<20)).Decode(&manifest); err != nil || len(manifest) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "manifest 无效"})
		return
	}
	files := make([]*daemon.TaildropOutgoingFile, 0, len(manifest))
	for _, entry := range manifest {
		if !validTaildropName(entry.Name) || entry.Size < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "manifest 文件名或大小无效: " + entry.Name})
			return
		}
		files = append(files, &daemon.TaildropOutgoingFile{Name: entry.Name, Size: entry.Size})
	}
	client := s.requireClient(w)
	if client == nil {
		return
	}
	_ = http.NewResponseController(w).EnableFullDuplex()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream, err := client.SendTaildropFiles(ctx)
	if err != nil {
		writeRPCError(w, err)
		return
	}
	out := &ndjsonWriter{w: w}
	done := make(chan error, 1)
	go func() {
		for {
			message, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				done <- nil
				return
			}
			if recvErr != nil {
				done <- recvErr
				return
			}
			if progress := message.GetProgress(); progress != nil {
				b, _ := protoJSON.Marshal(progress)
				out.line(b)
			}
		}
	}()
	fail := func(message string) {
		cancel()
		out.errorLine(message)
	}
	streamError := func() string {
		select {
		case recvErr := <-done:
			if recvErr != nil {
				return status.Convert(recvErr).Message()
			}
		case <-time.After(2 * time.Second):
		}
		return "发送中断"
	}
	if err = stream.Send(&daemon.TaildropSendClientMessage{Message: &daemon.TaildropSendClientMessage_Start{
		Start: &daemon.TaildropSendStart{EndpointTag: tag, PeerStableID: peer, Files: files},
	}}); err != nil {
		fail(streamError())
		return
	}
	buffer := make([]byte, daemon.TaildropChunkSize)
	for _, entry := range manifest {
		file, partErr := reader.NextPart()
		if partErr != nil || file.FileName() != entry.Name {
			fail("缺少文件 part: " + entry.Name)
			return
		}
		var sent int64
		for {
			n, readErr := file.Read(buffer)
			if n > 0 {
				sent += int64(n)
				if err = stream.Send(&daemon.TaildropSendClientMessage{Message: &daemon.TaildropSendClientMessage_Chunk{
					Chunk: &daemon.TaildropFileChunk{Data: buffer[:n]},
				}}); err != nil {
					fail(streamError())
					return
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				fail("读取上传失败: " + readErr.Error())
				return
			}
		}
		if sent != entry.Size {
			fail(fmt.Sprintf("文件 %s 大小与 manifest 不符", entry.Name))
			return
		}
		if err = stream.Send(&daemon.TaildropSendClientMessage{Message: &daemon.TaildropSendClientMessage_FileDone{
			FileDone: &daemon.TaildropFileDone{},
		}}); err != nil {
			fail(streamError())
			return
		}
	}
	_ = stream.CloseSend()
	if recvErr := <-done; recvErr != nil {
		out.errorLine(status.Convert(recvErr).Message())
		return
	}
	if !out.begun() {
		out.line([]byte(`{}`))
	}
}

// ---------- Tailscale SSH (WebSocket) ----------

type sshFrame struct {
	Type         string   `json:"type"`
	EndpointTag  string   `json:"endpointTag"`
	PeerAddress  string   `json:"peerAddress"`
	Username     string   `json:"username"`
	TerminalType string   `json:"terminalType"`
	Columns      int32    `json:"columns"`
	Rows         int32    `json:"rows"`
	WidthPixels  int32    `json:"widthPixels"`
	HeightPixels int32    `json:"heightPixels"`
	HostKeys     []string `json:"hostKeys"`
	ForwardAgent bool     `json:"forwardAgent"`
	Data         string   `json:"data"`
}

func (s *server) tailscaleSSH(w http.ResponseWriter, r *http.Request) {
	client := s.requireClient(w)
	if client == nil {
		return
	}
	options := &websocket.AcceptOptions{OriginPatterns: []string{"kiko://localhost", "http://kiko.localhost"}}
	if requestClient(r).Remote {
		// Remote callers already proved a Bearer token; browsers cannot attach it.
		options = &websocket.AcceptOptions{InsecureSkipVerify: true}
	}
	conn, err := websocket.Accept(w, r, options)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	s.serveTailscaleSSH(r.Context(), conn, client)
}

type sshConnection interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
}

func (s *server) serveTailscaleSSH(parent context.Context, conn sshConnection, client daemon.StartedServiceClient) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	send := func(frame map[string]any) {
		b, _ := json.Marshal(frame)
		_ = conn.Write(ctx, websocket.MessageText, b)
	}
	fail := func(message string) {
		send(map[string]any{"type": "error", "message": message})
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	var first sshFrame
	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}
	if json.Unmarshal(data, &first) != nil || first.Type != "start" || first.PeerAddress == "" {
		fail("首帧必须是包含 peerAddress 的 start")
		return
	}
	stream, err := client.StartTailscaleSSHSession(ctx)
	if err != nil {
		fail(status.Convert(err).Message())
		return
	}
	if err = stream.Send(&daemon.TailscaleSSHClientMessage{Message: &daemon.TailscaleSSHClientMessage_Start{Start: &daemon.TailscaleSSHStart{
		EndpointTag: first.EndpointTag, PeerAddress: first.PeerAddress, Username: first.Username, TerminalType: first.TerminalType,
		Columns: first.Columns, Rows: first.Rows, WidthPixels: first.WidthPixels, HeightPixels: first.HeightPixels,
		HostKeys: first.HostKeys, ForwardAgent: first.ForwardAgent,
	}}}); err != nil {
		fail(status.Convert(err).Message())
		return
	}
	go func() {
		defer cancel()
		for {
			message, recvErr := stream.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, io.EOF) && ctx.Err() == nil {
					send(map[string]any{"type": "error", "message": status.Convert(recvErr).Message()})
				}
				_ = conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			switch m := message.GetMessage().(type) {
			case *daemon.TailscaleSSHServerMessage_AuthBanner:
				send(map[string]any{"type": "authBanner", "message": m.AuthBanner.GetMessage()})
			case *daemon.TailscaleSSHServerMessage_Ready:
				send(map[string]any{"type": "ready"})
			case *daemon.TailscaleSSHServerMessage_Output:
				send(map[string]any{"type": "output", "data": base64.StdEncoding.EncodeToString(m.Output.GetData())})
			case *daemon.TailscaleSSHServerMessage_Exit:
				send(map[string]any{"type": "exit", "exitCode": m.Exit.GetExitCode(), "signal": m.Exit.GetSignal(), "errorMessage": m.Exit.GetErrorMessage()})
			case *daemon.TailscaleSSHServerMessage_Error:
				send(map[string]any{"type": "error", "message": m.Error.GetMessage()})
			}
		}
	}()
	for {
		_, data, err = conn.Read(ctx)
		if err != nil {
			return
		}
		var frame sshFrame
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		switch frame.Type {
		case "input":
			decoded, decodeErr := base64.StdEncoding.DecodeString(frame.Data)
			if decodeErr != nil {
				continue
			}
			err = stream.Send(&daemon.TailscaleSSHClientMessage{Message: &daemon.TailscaleSSHClientMessage_Input{Input: &daemon.TailscaleSSHInput{Data: decoded}}})
		case "resize":
			err = stream.Send(&daemon.TailscaleSSHClientMessage{Message: &daemon.TailscaleSSHClientMessage_Resize{Resize: &daemon.TailscaleSSHResize{
				Columns: frame.Columns, Rows: frame.Rows, WidthPixels: frame.WidthPixels, HeightPixels: frame.HeightPixels,
			}}})
		}
		if err != nil {
			return
		}
	}
}
