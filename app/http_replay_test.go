package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mitmapi "github.com/sagernet/sing-box/common/mitm/api"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func startHTTPCaptureServer(t *testing.T, impl mitmapi.HTTPCaptureServer) *server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := grpc.NewServer(grpc.MaxRecvMsgSize(daemon.HTTPReplayMaxMessage))
	t.Cleanup(rpcServer.Stop)
	mitmapi.RegisterHTTPCaptureServer(rpcServer, impl)
	go rpcServer.Serve(listener)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := newGRPCTestServer(t)
	s.rpc.httpCapture = mitmapi.NewHTTPCaptureClient(conn)
	return s
}

type fakeReplay struct {
	mitmapi.UnimplementedHTTPCaptureServer
	last atomic.Pointer[mitmapi.ReplayRequest]
	// disabled makes Replay answer like a core whose capture is off.
	disabled bool
}

func (f *fakeReplay) GetStatus(context.Context, *emptypb.Empty) (*mitmapi.CaptureStatus, error) {
	return &mitmapi.CaptureStatus{Instance: "inst-1", Enabled: true}, nil
}

func (f *fakeReplay) Replay(_ context.Context, request *mitmapi.ReplayRequest) (*mitmapi.ReplayResponse, error) {
	if f.disabled {
		return nil, status.Error(codes.FailedPrecondition, "HTTP capture is disabled")
	}
	if request.Url == "" {
		return nil, status.Error(codes.InvalidArgument, "url scheme must be http or https")
	}
	f.last.Store(request)
	record := fakeRecord("11", 11)
	record.Connection.Inbound = "kiko-internal"
	if strings.Contains(request.Url, "down.test") {
		record.State = mitmapi.RecordState_RECORD_STATE_FAILED
		record.Error = "connection refused"
		record.Status = 0
		return &mitmapi.ReplayResponse{Record: record, Error: "connection refused"}, nil
	}
	return &mitmapi.ReplayResponse{Record: record}, nil
}

func replayMux(s *server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/http/capabilities", s.httpCaptureCapabilities)
	mux.HandleFunc("POST /api/v1/http/replay", s.replayHTTPCapture)
	return mux
}

func muxPost(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return w
}

func TestHTTPReplayREST(t *testing.T) {
	fake := &fakeReplay{}
	s := startHTTPCaptureServer(t, fake)
	mux := replayMux(s)

	w := muxPost(mux, "/api/v1/http/replay", `{"source_id":"7","method":"post","url":"https://example.com/a?b=1","headers":[{"name":"X-A","value":"1"},{"name":"X-A","value":"2"}],"body":"`+base64.StdEncoding.EncodeToString([]byte("hi"))+`","outbound":"proxy","timeout_ms":999999}`)
	if w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Record map[string]any `json:"record"`
		Error  *string        `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Error == nil || *out.Error != "" || out.Record["id"] != "11" || out.Record["state"] != "complete" {
		t.Fatalf("response: %v %s", err, w.Body.String())
	}
	request := fake.last.Load()
	if request.SourceId != "7" || request.Method != "POST" || request.Url != "https://example.com/a?b=1" || string(request.Body) != "hi" ||
		request.Outbound != "proxy" || request.TimeoutMs != 120000 || !request.VerifyTls || len(request.Headers) != 2 || request.Headers[1].Value != "2" {
		t.Fatalf("rpc request: %v", request)
	}

	muxPost(mux, "/api/v1/http/replay", `{"url":"http://example.com/","verify_tls":false}`)
	request = fake.last.Load()
	if request.VerifyTls || request.Method != "GET" {
		t.Fatalf("defaults: %v", request)
	}

	w = muxPost(mux, "/api/v1/http/replay", `{"method":"GET","url":"http://down.test/"}`)
	if w.Code != 200 {
		t.Fatalf("upstream failure: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Record["state"] != "failed" || *out.Error != "connection refused" {
		t.Fatalf("failure body: %s", w.Body.String())
	}

	for name, body := range map[string]string{
		"scheme":      `{"url":"ftp://example.com/"}`,
		"no host":     `{"url":"http:///x"}`,
		"empty url":   `{"url":""}`,
		"bad base64":  `{"url":"http://example.com/","body":"!!!"}`,
		"big body":    `{"url":"http://example.com/","body":"` + base64.StdEncoding.EncodeToString(make([]byte, httpReplayMaxBody+1)) + `"}`,
		"bad json":    `{`,
		"wrong field": `{"url":5}`,
	} {
		if w := muxPost(mux, "/api/v1/http/replay", body); w.Code != 400 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestHTTPReplayUnavailable(t *testing.T) {
	s := newGRPCTestServer(t)
	w := muxPost(replayMux(s), "/api/v1/http/replay", `{"url":"http://example.com/"}`)
	if w.Code != 503 {
		t.Fatalf("no core: %d %s", w.Code, w.Body.String())
	}
	disabled := startHTTPCaptureServer(t, &fakeReplay{disabled: true})
	w = muxPost(replayMux(disabled), "/api/v1/http/replay", `{"url":"http://example.com/"}`)
	if w.Code != 503 {
		t.Fatalf("capture off: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPReplayCapability(t *testing.T) {
	capabilities := func(s *server) map[string]any {
		w := httptest.NewRecorder()
		replayMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/http/capabilities", nil))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 {
			t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	if got := capabilities(startHTTPCaptureServer(t, &fakeReplay{})); got["replay"] != true || got["supported"] != true {
		t.Fatalf("with replay: %v", got)
	}
	if got := capabilities(startHTTPCaptureServer(t, &fakeReplay{disabled: true})); got["replay"] != true {
		t.Fatalf("capture off still replay capable: %v", got)
	}
	if got := capabilities(startHTTPCaptureServer(t, &fakeHTTPCapture{canceled: make(chan struct{})})); got["replay"] != false {
		t.Fatalf("older core: %v", got)
	}
	if got := capabilities(newGRPCTestServer(t)); got["replay"] != false {
		t.Fatalf("no core: %v", got)
	}
}
