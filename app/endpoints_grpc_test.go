package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeEndpoints struct {
	fakeStarted
	rec         sync.Mutex
	calls       []string
	sendStart   *daemon.TaildropSendStart
	sendData    []byte
	sendGate    <-chan struct{}
	sendStarted chan<- struct{}
	sshInputs   []string
}

func (f *fakeEndpoints) record(call string) {
	f.rec.Lock()
	f.calls = append(f.calls, call)
	f.rec.Unlock()
}

func (f *fakeEndpoints) recorded() []string {
	f.rec.Lock()
	defer f.rec.Unlock()
	return append([]string{}, f.calls...)
}

func blockUntilDone(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (f *fakeEndpoints) SubscribeTailscaleStatus(_ *emptypb.Empty, stream grpc.ServerStreamingServer[daemon.TailscaleStatusUpdate]) error {
	if err := stream.Send(&daemon.TailscaleStatusUpdate{Endpoints: []*daemon.TailscaleEndpointStatus{{EndpointTag: "ts", BackendState: "Running"}}}); err != nil {
		return err
	}
	return blockUntilDone(stream.Context())
}

func (f *fakeEndpoints) SubscribeTaildropInbox(request *daemon.SubscribeTaildropInboxRequest, stream grpc.ServerStreamingServer[daemon.TaildropInbox]) error {
	if err := stream.Send(&daemon.TaildropInbox{EndpointTag: request.EndpointTag, Files: []*daemon.TaildropFile{{Name: "a.txt", Size: 3}}}); err != nil {
		return err
	}
	return blockUntilDone(stream.Context())
}

func (f *fakeEndpoints) SubscribeOpenConnectStatus(_ *emptypb.Empty, stream grpc.ServerStreamingServer[daemon.OpenConnectStatusUpdate]) error {
	if err := stream.Send(&daemon.OpenConnectStatusUpdate{Endpoints: []*daemon.OpenConnectEndpointStatus{{EndpointTag: "oc", State: "connecting"}}}); err != nil {
		return err
	}
	return blockUntilDone(stream.Context())
}

func (f *fakeEndpoints) SubscribeOpenVPNStatus(_ *emptypb.Empty, stream grpc.ServerStreamingServer[daemon.OpenVPNStatusUpdate]) error {
	if err := stream.Send(&daemon.OpenVPNStatusUpdate{Endpoints: []*daemon.OpenVPNEndpointStatus{{EndpointTag: "ov", State: "connected"}}}); err != nil {
		return err
	}
	return blockUntilDone(stream.Context())
}

func (f *fakeEndpoints) SubscribeUSBIPServerStatus(_ *emptypb.Empty, stream grpc.ServerStreamingServer[daemon.USBIPServerStatusUpdate]) error {
	if err := stream.Send(&daemon.USBIPServerStatusUpdate{Servers: []*daemon.USBIPServerStatus{{ServerTag: "usb"}}}); err != nil {
		return err
	}
	return blockUntilDone(stream.Context())
}

func (f *fakeEndpoints) SetTailscaleExitNode(_ context.Context, request *daemon.SetTailscaleExitNodeRequest) (*emptypb.Empty, error) {
	if request.EndpointTag == "missing" {
		return nil, status.Error(codes.NotFound, "no such endpoint")
	}
	f.record("exit:" + request.EndpointTag + ":" + request.StableID)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) TailscaleLogout(_ context.Context, request *daemon.TailscaleLogoutRequest) (*emptypb.Empty, error) {
	f.record("logout:" + request.EndpointTag)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) GetTailscaleDiagnostics(_ context.Context, request *daemon.TailscaleDiagnosticsRequest) (*daemon.TailscaleDiagnostics, error) {
	if request.EndpointTag == "bad" {
		return nil, status.Error(codes.FailedPrecondition, "endpoint does not support tailscale diagnostics")
	}
	f.record(fmt.Sprintf("diagnostics:%s:%v", request.EndpointTag, request.Refresh))
	return &daemon.TailscaleDiagnostics{
		Netcheck: &daemon.TailscaleNetcheck{CheckedAt: 1790000000000, Udp: true, GlobalV4: "203.0.113.7:41641", PreferredDERP: 2,
			Regions: []*daemon.TailscaleDERPRegion{{Id: 2, Code: "tok", Name: "Tokyo", LatencyMs: 30}}},
		Peers: []*daemon.TailscalePeerDiagnostics{{StableID: "n1", LastHandshake: 1790000000000,
			Endpoints: []*daemon.TailscalePeerEndpoint{{Address: "198.51.100.2:41641", Path: "detour"}}}},
	}, nil
}

func (f *fakeEndpoints) StartTailscalePing(request *daemon.TailscalePingRequest, stream grpc.ServerStreamingServer[daemon.TailscalePingResponse]) error {
	if request.PeerIP == "100.64.0.98" {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	if request.PeerIP == "100.64.0.99" {
		deadline, ok := stream.Context().Deadline()
		if !ok || time.Until(deadline) > 11*time.Second {
			return status.Error(codes.FailedPrecondition, "ping deadline missing")
		}
		return status.Error(codes.DeadlineExceeded, "timed out")
	}
	if request.EndpointTag == "bad" {
		return status.Error(codes.InvalidArgument, "bad endpoint")
	}
	for _, latency := range []float64{12, 15} {
		if err := stream.Send(&daemon.TailscalePingResponse{LatencyMs: latency, IsDirect: true}); err != nil {
			return err
		}
	}
	if request.PeerIP == "100.64.0.97" {
		<-stream.Context().Done()
		f.record("ping-canceled")
	}
	return nil
}

func (f *fakeEndpoints) SubmitOpenConnectAuthResponse(_ context.Context, request *daemon.OpenConnectAuthResponseSubmission) (*emptypb.Empty, error) {
	f.record(fmt.Sprintf("oc-auth:%s:%s:%s", request.EndpointTag, request.ChallengeID, request.GetForm().GetValues()["password"]))
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) CancelOpenConnectAuthChallenge(_ context.Context, request *daemon.OpenConnectAuthChallengeCancel) (*emptypb.Empty, error) {
	f.record("oc-cancel:" + request.EndpointTag + ":" + request.ChallengeID)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) SubmitOpenVPNChallengeResponse(_ context.Context, request *daemon.OpenVPNChallengeSubmission) (*emptypb.Empty, error) {
	f.record(fmt.Sprintf("ov-challenge:%s:%s:%s:%s:%s", request.EndpointTag, request.ChallengeID, request.Username, request.Password, request.Secret))
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) CancelOpenVPNChallenge(_ context.Context, request *daemon.OpenVPNChallengeCancel) (*emptypb.Empty, error) {
	f.record("ov-cancel:" + request.EndpointTag + ":" + request.ChallengeID)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) MarkTaildropInboxRead(_ context.Context, request *daemon.MarkTaildropInboxReadRequest) (*emptypb.Empty, error) {
	f.record("td-read:" + request.EndpointTag)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) DeleteTaildropFile(_ context.Context, request *daemon.DeleteTaildropFileRequest) (*emptypb.Empty, error) {
	f.record("td-delete:" + request.EndpointTag + ":" + request.Name)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) CancelTaildropReceiving(_ context.Context, request *daemon.CancelTaildropReceivingRequest) (*emptypb.Empty, error) {
	f.record("td-cancel:" + request.EndpointTag + ":" + request.SenderID + ":" + request.Name)
	return &emptypb.Empty{}, nil
}

func (f *fakeEndpoints) DownloadTaildropFile(request *daemon.DownloadTaildropFileRequest, stream grpc.ServerStreamingServer[daemon.DownloadTaildropFileChunk]) error {
	if request.Name == "gone" {
		return status.Error(codes.NotFound, "file not found")
	}
	for _, chunk := range []*daemon.DownloadTaildropFileChunk{{Size: 5}, {Data: []byte("he")}, {Data: []byte("llo")}} {
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeEndpoints) SendTaildropFiles(stream grpc.BidiStreamingServer[daemon.TaildropSendClientMessage, daemon.TaildropSendServerMessage]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	f.rec.Lock()
	f.sendStart = start
	gate, started := f.sendGate, f.sendStarted
	f.rec.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
	var received int64
	index := int32(0)
	for int(index) < len(start.GetFiles()) {
		message, recvErr := stream.Recv()
		if recvErr != nil {
			return recvErr
		}
		switch {
		case message.GetChunk() != nil:
			f.rec.Lock()
			f.sendData = append(f.sendData, message.GetChunk().GetData()...)
			f.rec.Unlock()
			received += int64(len(message.GetChunk().GetData()))
		case message.GetFileDone() != nil:
			if err = stream.Send(&daemon.TaildropSendServerMessage{Message: &daemon.TaildropSendServerMessage_Progress{
				Progress: &daemon.TaildropSendProgress{FileIndex: index, SentBytes: received, FileCompleted: true},
			}}); err != nil {
				return err
			}
			index++
			received = 0
		}
	}
	return nil
}

func (f *fakeEndpoints) StartTailscaleSSHSession(stream grpc.BidiStreamingServer[daemon.TailscaleSSHClientMessage, daemon.TailscaleSSHServerMessage]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.record("ssh-start:" + first.GetStart().GetEndpointTag() + ":" + first.GetStart().GetPeerAddress() + ":" + first.GetStart().GetUsername())
	if err = stream.Send(&daemon.TailscaleSSHServerMessage{Message: &daemon.TailscaleSSHServerMessage_Ready{Ready: &daemon.TailscaleSSHReady{}}}); err != nil {
		return err
	}
	for {
		message, recvErr := stream.Recv()
		if recvErr != nil {
			return recvErr
		}
		if resize := message.GetResize(); resize != nil {
			f.record(fmt.Sprintf("ssh-resize:%dx%d", resize.Columns, resize.Rows))
			continue
		}
		data := message.GetInput().GetData()
		f.rec.Lock()
		f.sshInputs = append(f.sshInputs, string(data))
		f.rec.Unlock()
		if err = stream.Send(&daemon.TailscaleSSHServerMessage{Message: &daemon.TailscaleSSHServerMessage_Output{Output: &daemon.TailscaleSSHOutput{Data: data}}}); err != nil {
			return err
		}
		if string(data) == "q" {
			return stream.Send(&daemon.TailscaleSSHServerMessage{Message: &daemon.TailscaleSSHServerMessage_Exit{Exit: &daemon.TailscaleSSHExit{ExitCode: 3}}})
		}
	}
}

func connectedEndpointServer(t *testing.T) (*server, *fakeEndpoints, chan string) {
	t.Helper()
	fake := &fakeEndpoints{}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	events := make(chan string, 64)
	s.events[events] = struct{}{}
	s.state.State = "running"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.grpcWorker(ctx, "http://"+address, testSecret)
	eventually(t, "grpc active", s.grpcActive)
	return s, fake, events
}

func postJSON(handler http.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(method, "/x", strings.NewReader(body)))
	return rec
}

func allTrue(seen map[string]bool, kinds []string) bool {
	for _, kind := range kinds {
		if !seen[kind] {
			return false
		}
	}
	return true
}

func setUSBIPGOOS(t *testing.T, goos string) {
	t.Helper()
	old := usbipGOOS
	usbipGOOS = goos
	t.Cleanup(func() { usbipGOOS = old })
}

func TestEndpointSubscriptionsPublishSnapshots(t *testing.T) {
	s, _, events := connectedEndpointServer(t)
	eventually(t, "all snapshots", func() bool {
		s.ep.mu.Lock()
		defer s.ep.mu.Unlock()
		return len(s.ep.snapshot) == 4 && s.ep.taildrop["ts"] != nil
	})

	rec := httptest.NewRecorder()
	s.endpointsStatus(rec, httptest.NewRequest("GET", "/api/v1/endpoints/status", nil))
	var status struct {
		Tailscale struct {
			Endpoints []struct{ EndpointTag, BackendState string }
		}
		Openconnect struct {
			Endpoints []struct{ EndpointTag string }
		}
		Openvpn struct {
			Endpoints []struct{ EndpointTag string }
		}
		Usbip    struct{ Servers []struct{ ServerTag string } }
		Taildrop map[string]struct{ Files []struct{ Name string } }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if status.Tailscale.Endpoints[0].EndpointTag != "ts" || status.Tailscale.Endpoints[0].BackendState != "Running" ||
		status.Openconnect.Endpoints[0].EndpointTag != "oc" || status.Openvpn.Endpoints[0].EndpointTag != "ov" ||
		status.Usbip.Servers[0].ServerTag != "usb" || status.Taildrop["ts"].Files[0].Name != "a.txt" {
		t.Fatalf("unexpected status: %s", rec.Body)
	}

	seen := map[string]bool{}
	deadline := time.After(5 * time.Second)
	wanted := []string{"tailscale", "taildrop", "openconnect", "openvpn", "usbip"}
	for !allTrue(seen, wanted) {
		select {
		case line := <-events:
			var event struct{ Type string }
			_ = json.Unmarshal([]byte(line), &event)
			seen[event.Type] = true
		case <-deadline:
			t.Fatalf("missing SSE events, saw %v", seen)
		}
	}
	types := map[string]bool{}
	for _, message := range s.endpointSnapshotMessages() {
		var event struct{ Type string }
		_ = json.Unmarshal(message, &event)
		types[event.Type] = true
	}
	if len(types) != 5 {
		t.Fatalf("snapshot messages types=%v", types)
	}

	s.stopGRPC()
	if len(s.endpointSnapshotMessages()) != 0 {
		t.Fatal("snapshots must be cleared when the gRPC session stops")
	}
}

func TestTailscaleRoutes(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	if rec := postJSON(s.tailscaleExitNode, "POST", `{"endpointTag":"ts","stableID":"n123"}`); rec.Code != 200 {
		t.Fatalf("exit-node code=%d %s", rec.Code, rec.Body)
	}
	if rec := postJSON(s.tailscaleExitNode, "POST", `{"endpointTag":"ts","stableID":""}`); rec.Code != 200 {
		t.Fatalf("clear exit-node code=%d", rec.Code)
	}
	if rec := postJSON(s.tailscaleExitNode, "POST", `{"endpointTag":"missing"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing endpoint code=%d", rec.Code)
	}
	if rec := postJSON(s.tailscaleLogout, "POST", `{"endpointTag":"ts"}`); rec.Code != 200 {
		t.Fatalf("logout code=%d", rec.Code)
	}
	if got := strings.Join(fake.recorded(), ","); got != "exit:ts:n123,exit:ts:,logout:ts" {
		t.Fatalf("calls=%s", got)
	}

	rec := postJSON(s.tailscalePing, "POST", `{"endpointTag":"ts","peerIP":"100.64.0.2"}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("ping code=%d type=%s", rec.Code, rec.Header().Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"latencyMs":12`) || !strings.Contains(lines[0], `"isDirect":true`) {
		t.Fatalf("ping lines=%q", lines)
	}
	if rec = postJSON(s.tailscalePing, "POST", `{"endpointTag":"bad"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ping code=%d %s", rec.Code, rec.Body)
	}
	rec = postJSON(s.tailscalePing, "POST", `{"endpointTag":"ts","peerIP":"100.64.0.2","continuous":true}`)
	lines = strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if rec.Code != 200 || len(lines) != 2 || !strings.Contains(lines[1], `"latencyMs":15`) {
		t.Fatalf("continuous ping code=%d lines=%q", rec.Code, lines)
	}
}

func TestTailscaleDiagnostics(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.tailscaleDiagnostics(rec, httptest.NewRequest("GET", "/api/v1/tailscale/diagnostics?"+query, nil))
		return rec
	}
	rec := get("endpointTag=ts&refresh=1")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"globalV4":"203.0.113.7:41641"`) || !strings.Contains(body, `"checkedAt":"1790000000000"`) ||
		!strings.Contains(body, `"path":"detour"`) || !strings.Contains(body, `"ipv6":false`) {
		t.Fatalf("code=%d body=%s", rec.Code, body)
	}
	if rec = get("endpointTag=ts"); rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if got := strings.Join(fake.recorded(), ","); got != "diagnostics:ts:true,diagnostics:ts:false" {
		t.Errorf("calls=%s", got)
	}
	if rec = get("endpointTag=bad"); rec.Code == 200 {
		t.Errorf("unsupported endpoint code=%d", rec.Code)
	}
}

func TestContinuousTailscalePingCancellation(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	web := httptest.NewServer(http.HandlerFunc(s.tailscalePing))
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", web.URL, strings.NewReader(`{"endpointTag":"ts","peerIP":"100.64.0.97","continuous":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for range 2 {
		if !scanner.Scan() {
			t.Fatalf("missing continuous sample: %v", scanner.Err())
		}
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(fake.recorded(), ","), "ping-canceled") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pausing HTTP Ping did not cancel the official RPC")
}

func TestTailscalePingTimeout(t *testing.T) {
	s, _, _ := connectedEndpointServer(t)
	rec := postJSON(s.tailscalePing, "POST", `{"endpointTag":"ts","peerIP":"100.64.0.99"}`)
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "Ping 超时") {
		t.Fatalf("timeout code=%d body=%s", rec.Code, rec.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest("POST", "/x", strings.NewReader(`{"endpointTag":"ts","peerIP":"100.64.0.98"}`)).WithContext(ctx)
	rec = httptest.NewRecorder()
	s.tailscalePing(rec, request)
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), "Ping 超时") {
		t.Fatalf("stalled ping code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestOpenConnectAndOpenVPNRoutes(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	if rec := postJSON(s.openConnectAuth, "POST", `{"endpointTag":"oc","challengeID":"c1","form":{"values":{"password":"pw"}}}`); rec.Code != 200 {
		t.Fatalf("auth code=%d %s", rec.Code, rec.Body)
	}
	if rec := postJSON(s.openConnectAuth, "POST", `{"endpointTag":"oc","challengeID":"c1","browser":{"finalURL":"x"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("browser auth must be rejected, code=%d", rec.Code)
	}
	if rec := postJSON(s.openConnectAuth, "POST", `{"endpointTag":"oc","challengeID":"c1"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing form must be rejected, code=%d", rec.Code)
	}
	postJSON(s.openConnectCancel, "POST", `{"endpointTag":"oc","challengeID":"c1"}`)
	postJSON(s.openVPNChallenge, "POST", `{"endpointTag":"ov","challengeID":"c2","username":"u","password":"p","secret":"123456"}`)
	postJSON(s.openVPNCancel, "POST", `{"endpointTag":"ov","challengeID":"c2"}`)
	want := "oc-auth:oc:c1:pw,oc-cancel:oc:c1,ov-challenge:ov:c2:u:p:123456,ov-cancel:ov:c2"
	if got := strings.Join(fake.recorded(), ","); got != want {
		t.Fatalf("calls=%s\nwant %s", got, want)
	}
}

func multipartBody(t *testing.T, manifest string, files [][2]string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if manifest != "" {
		field, _ := writer.CreateFormField("manifest")
		_, _ = field.Write([]byte(manifest))
	}
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(file[1]))
	}
	_ = writer.Close()
	return &body, writer.FormDataContentType()
}

func TestTaildropRoutes(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	postJSON(s.taildropRead, "POST", `{"endpointTag":"ts"}`)
	postJSON(s.taildropDelete, "DELETE", `{"endpointTag":"ts","name":"a.txt"}`)
	postJSON(s.taildropCancel, "POST", `{"endpointTag":"ts","senderID":"s1","name":"b.bin"}`)
	if got := strings.Join(fake.recorded(), ","); got != "td-read:ts,td-delete:ts:a.txt,td-cancel:ts:s1:b.bin" {
		t.Fatalf("calls=%s", got)
	}

	rec := httptest.NewRecorder()
	s.taildropDownload(rec, httptest.NewRequest("GET", "/api/v1/taildrop/file?endpointTag=ts&name=a%20b.txt", nil))
	if rec.Code != 200 || rec.Body.String() != "hello" || rec.Header().Get("Content-Length") != "5" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `filename="a b.txt"`) {
		t.Fatalf("download code=%d body=%q headers=%v", rec.Code, rec.Body, rec.Header())
	}
	rec = httptest.NewRecorder()
	s.taildropDownload(rec, httptest.NewRequest("GET", "/api/v1/taildrop/file?endpointTag=ts&name=gone", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing file code=%d", rec.Code)
	}

	body, contentType := multipartBody(t, `[{"name":"a.txt","size":3},{"name":"b.txt","size":2}]`, [][2]string{{"a.txt", "abc"}, {"b.txt", "de"}})
	request := httptest.NewRequest("POST", "/api/v1/taildrop/send?endpointTag=ts&peer=node1", body)
	request.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	s.taildropSend(rec, request)
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if rec.Code != 200 || len(lines) != 2 || !strings.Contains(lines[0], `"fileCompleted":true`) || !strings.Contains(lines[1], `"fileIndex":1`) {
		t.Fatalf("send code=%d body=%q", rec.Code, rec.Body)
	}
	fake.rec.Lock()
	start, data := fake.sendStart, string(fake.sendData)
	fake.rec.Unlock()
	if start.GetEndpointTag() != "ts" || start.GetPeerStableID() != "node1" || len(start.GetFiles()) != 2 ||
		start.GetFiles()[0].GetSize() != 3 || data != "abcde" {
		t.Fatalf("start=%v data=%q", start, data)
	}

	body, contentType = multipartBody(t, `[{"name":"a.txt","size":5}]`, [][2]string{{"a.txt", "abc"}})
	request = httptest.NewRequest("POST", "/api/v1/taildrop/send?endpointTag=ts&peer=node1", body)
	request.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	s.taildropSend(rec, request)
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("size mismatch must report an error line, got %q", rec.Body)
	}

	body, contentType = multipartBody(t, "", [][2]string{{"a.txt", "abc"}})
	request = httptest.NewRequest("POST", "/api/v1/taildrop/send?endpointTag=ts&peer=node1", body)
	request.Header.Set("Content-Type", contentType)
	rec = httptest.NewRecorder()
	s.taildropSend(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing manifest code=%d", rec.Code)
	}
}

func TestTaildropSendThroughRoutes(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	handler := s.routes()
	body, contentType := multipartBody(t, `[{"name":"a.txt","size":3}]`, [][2]string{{"a.txt", "abc"}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/taildrop/send?endpointTag=ts&peer=node1", body)
	request.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fileCompleted":true`) {
		t.Fatalf("send code=%d body=%q", rec.Code, rec.Body.String())
	}
	fake.rec.Lock()
	data := string(fake.sendData)
	fake.rec.Unlock()
	if data != "abc" {
		t.Fatalf("sent data=%q", data)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/taildrop/send?endpointTag=ts&peer=node1", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON upload code=%d body=%q", rec.Code, rec.Body.String())
	}

	body, contentType = multipartBody(t, `[{"name":"a.txt","size":3}]`, [][2]string{{"a.txt", "abc"}})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/taildrop/send?endpointTag=ts&peer=node1", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Origin", "https://other.example")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upload code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestTaildropDirectoryRoutes(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(t.TempDir(), "Received")
	config := fmt.Sprintf(`{"endpoints":[{"type":"tailscale","tag":"default"},{"type":"tailscale","tag":"relative","taildrop_directory":"files/inbox"},{"type":"tailscale","tag":"absolute","taildrop_directory":%q},{"type":"openvpn","tag":"other"}]}`, custom)
	if err := os.WriteFile(filepath.Join(dataDir, "runtime", "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := (&server{dataDir: dataDir}).routes()
	for _, test := range []struct{ tag, want string }{
		{"default", filepath.Join(dataDir, "Taildrop")},
		{"relative", filepath.Join(dataDir, "files", "inbox")},
		{"absolute", custom},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/taildrop/directory?endpointTag="+test.tag, nil))
		var response struct {
			Path string `json:"path"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Path != test.want {
			t.Fatalf("tag=%s code=%d body=%q, want %q", test.tag, rec.Code, rec.Body, test.want)
		}
	}
	for _, tag := range []string{"missing", "other"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/taildrop/directory?endpointTag="+tag, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("tag=%s code=%d body=%q", tag, rec.Code, rec.Body)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/taildrop/directory/open", strings.NewReader(`{"endpointTag":"missing"}`))
	request.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown directory open code=%d body=%q", rec.Code, rec.Body)
	}
}

func TestFileManagerCommands(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		goos    string
		reveal  bool
		path    string
		program []string
		last    string
	}{
		{"darwin", false, "/directory with spaces", []string{"open"}, "/directory with spaces"},
		{"darwin", true, "/a b/c", []string{"open"}, "/a b/c"},
		{"windows", false, `C:\Program Files\App`, []string{"explorer.exe"}, `"C:\Program Files\App"`},
		{"windows", true, `C:\Program Files\App\app.exe`, []string{"explorer.exe"}, `/select,"C:\Program Files\App\app.exe"`},
		{"linux", false, "/srv/taildrop", []string{"xdg-open"}, "/srv/taildrop"},
		{"linux", true, "/usr/bin/a b", []string{"dbus-send", "xdg-open"}, "/usr/bin"},
	} {
		cmds, err := fileManagerCommands(ctx, test.goos, test.path, test.reveal)
		if err != nil || len(cmds) != len(test.program) {
			t.Fatalf("%s reveal=%v: %v %v", test.goos, test.reveal, cmds, err)
		}
		for i, cmd := range cmds {
			if filepath.Base(cmd.Args[0]) != test.program[i] {
				t.Errorf("%s reveal=%v: program %q", test.goos, test.reveal, cmd.Args[0])
			}
		}
		last := cmds[len(cmds)-1].Args
		if last[len(last)-1] != test.last {
			t.Errorf("%s reveal=%v: args %q", test.goos, test.reveal, last)
		}
	}
	cmds, _ := fileManagerCommands(ctx, "linux", "/usr/bin/a b", true)
	if !strings.Contains(strings.Join(cmds[0].Args, " "), "array:string:file:///usr/bin/a%20b") {
		t.Errorf("dbus uri: %q", cmds[0].Args)
	}
	if _, err := fileManagerCommands(ctx, "plan9", "/", false); err == nil {
		t.Error("plan9 has a file manager")
	}
}

func TestTailscaleSSHBridge(t *testing.T) {
	s, fake, _ := connectedEndpointServer(t)
	httpServer := httptest.NewServer(http.HandlerFunc(s.tailscaleSSH))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sshURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	for _, test := range []struct {
		origin string
		allow  bool
	}{
		{origin: "kiko://localhost", allow: true},
		{origin: "http://kiko.localhost", allow: true},
		{origin: "https://other.example", allow: false},
	} {
		conn, response, err := websocket.Dial(ctx, sshURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": {test.origin}},
		})
		if test.allow {
			if err != nil {
				t.Fatalf("origin %s: %v", test.origin, err)
			}
			conn.CloseNow()
		} else if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
			if conn != nil {
				conn.CloseNow()
			}
			t.Fatalf("origin %s: response=%v err=%v", test.origin, response, err)
		}
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	write := func(frame map[string]any) {
		b, _ := json.Marshal(frame)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		_ = json.Unmarshal(data, &frame)
		return frame
	}
	write(map[string]any{"type": "start", "endpointTag": "ts", "peerAddress": "100.64.0.2", "username": "root", "columns": 80, "rows": 24})
	if frame := read(); frame["type"] != "ready" {
		t.Fatalf("frame=%v", frame)
	}
	write(map[string]any{"type": "resize", "columns": 100, "rows": 30})
	write(map[string]any{"type": "input", "data": base64.StdEncoding.EncodeToString([]byte("hi"))})
	frame := read()
	if decoded, _ := base64.StdEncoding.DecodeString(fmt.Sprint(frame["data"])); frame["type"] != "output" || string(decoded) != "hi" {
		t.Fatalf("frame=%v", frame)
	}
	write(map[string]any{"type": "input", "data": base64.StdEncoding.EncodeToString([]byte("q"))})
	read()
	if frame = read(); frame["type"] != "exit" || frame["exitCode"] != float64(3) {
		t.Fatalf("frame=%v", frame)
	}
	if got := strings.Join(fake.recorded(), ","); got != "ssh-start:ts:100.64.0.2:root,ssh-resize:100x30" {
		t.Fatalf("calls=%s", got)
	}
}

func TestEndpointRoutesReturn503WhenGRPCDisconnected(t *testing.T) {
	setUSBIPGOOS(t, "linux")
	s := newGRPCTestServer(t)
	body, contentType := multipartBody(t, `[{"name":"a","size":1}]`, [][2]string{{"a", "x"}})
	sendRequest := httptest.NewRequest("POST", "/api/v1/taildrop/send?endpointTag=ts&peer=n", body)
	sendRequest.Header.Set("Content-Type", contentType)
	for name, call := range map[string]func(*httptest.ResponseRecorder){
		"exit-node": func(rec *httptest.ResponseRecorder) {
			s.tailscaleExitNode(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{}`)))
		},
		"logout": func(rec *httptest.ResponseRecorder) {
			s.tailscaleLogout(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{}`)))
		},
		"ping": func(rec *httptest.ResponseRecorder) {
			s.tailscalePing(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{}`)))
		},
		"ssh": func(rec *httptest.ResponseRecorder) { s.tailscaleSSH(rec, httptest.NewRequest("GET", "/x", nil)) },
		"download": func(rec *httptest.ResponseRecorder) {
			s.taildropDownload(rec, httptest.NewRequest("GET", "/x?name=a", nil))
		},
		"send": func(rec *httptest.ResponseRecorder) { s.taildropSend(rec, sendRequest) },
		"share": func(rec *httptest.ResponseRecorder) {
			s.startUSBIPShare(rec, httptest.NewRequest("POST", "/x", strings.NewReader(`{}`)))
		},
	} {
		rec := httptest.NewRecorder()
		call(rec)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: code=%d body=%s", name, rec.Code, rec.Body)
		}
	}
}

func TestParseUSBDeviceList(t *testing.T) {
	devices := parseUSBDeviceList("1-2\t046d:c31c\tUSB Keyboard\tSN1\n3-1\t1a2b:00ff\t-\t-\nbroken line\n2-4\tzzzz:0001\tx\ty\n")
	if len(devices) != 2 {
		t.Fatalf("devices=%+v", devices)
	}
	if devices[0].BusID != "1-2" || devices[0].VendorID != 0x046d || devices[0].ProductID != 0xc31c || devices[0].Product != "USB Keyboard" || devices[0].Serial != "SN1" {
		t.Fatalf("device0=%+v", devices[0])
	}
	if devices[1].Product != "" || devices[1].Serial != "" || devices[1].VendorID != 0x1a2b {
		t.Fatalf("device1=%+v", devices[1])
	}
	if empty, _ := json.Marshal(parseUSBDeviceList("")); string(empty) != "[]" {
		t.Fatalf("empty list must marshal as [], got %s", empty)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	path := filepath.Join(t.TempDir(), "fake-kiko-core")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUSBDevicesEndpoint(t *testing.T) {
	s := newGRPCTestServer(t)
	s.singBox = writeScript(t, "printf '1-2\\t046d:c31c\\tKeyboard\\t-\\n'\n")
	rec := httptest.NewRecorder()
	s.usbDevices(rec, httptest.NewRequest("GET", "/api/v1/usb/devices", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"busId":"1-2"`) || !strings.Contains(rec.Body.String(), `"vendorId":1133`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	s.singBox = writeScript(t, "echo 'USB/IP is not included in this build' >&2\nexit 1\n")
	rec = httptest.NewRecorder()
	s.usbDevices(rec, httptest.NewRequest("GET", "/api/v1/usb/devices", nil))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "not included") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestUSBIPShareLifecycle(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args.txt")
	script := writeScript(t, "echo \"$@\" > "+out+"\necho \"$BOX_API_URL $BOX_API_SECRET\" >> "+out+
		"\ntrap 'exit 0' TERM INT\nwhile true; do sleep 0.05; done\n")

	setUSBIPGOOS(t, "darwin")
	s := newGRPCTestServer(t)
	if rec := postJSON(s.startUSBIPShare, "POST", `{"serviceTag":"usb"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SIP") {
		t.Fatalf("macOS must be rejected, code=%d body=%s", rec.Code, rec.Body)
	}
	usbipGOOS = "linux"

	s, _, _ = connectedEndpointServer(t)
	s.singBox = script
	s.rpc.mu.Lock()
	s.rpc.port, s.rpc.secret = 4321, "sec"
	s.rpc.mu.Unlock()
	if rec := postJSON(s.startUSBIPShare, "POST", `{"serviceTag":"-x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("flag-like tag code=%d", rec.Code)
	}
	rec := postJSON(s.startUSBIPShare, "POST", `{"serviceTag":"usb","devices":["1-2","046d:c31c"],"capture":true}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"running":true`) {
		t.Fatalf("start code=%d body=%s", rec.Code, rec.Body)
	}
	if rec = postJSON(s.startUSBIPShare, "POST", `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("second start code=%d", rec.Code)
	}
	eventually(t, "script output", func() bool {
		b, _ := os.ReadFile(out)
		return strings.Count(string(b), "\n") >= 2
	})
	b, _ := os.ReadFile(out)
	if want := "api usbip share --service usb --capture -- 1-2 046d:c31c\nhttp://127.0.0.1:4321 sec\n"; string(b) != want {
		t.Fatalf("args/env=%q\nwant %q", b, want)
	}
	rec = httptest.NewRecorder()
	s.getUSBIPShare(rec, httptest.NewRequest("GET", "/api/v1/usbip/share", nil))
	if !strings.Contains(rec.Body.String(), `"serviceTag":"usb"`) || !strings.Contains(rec.Body.String(), `"pid"`) {
		t.Fatalf("state=%s", rec.Body)
	}
	rec = httptest.NewRecorder()
	s.stopUSBIPShareHandler(rec, httptest.NewRequest("DELETE", "/api/v1/usbip/share", nil))
	if state := s.usbipShareState(); state.Running || state.Error != "" {
		t.Fatalf("after stop: %+v", state)
	}

	if rec = postJSON(s.startUSBIPShare, "POST", `{}`); rec.Code != 200 {
		t.Fatalf("restart code=%d body=%s", rec.Code, rec.Body)
	}
	if b, _ = os.ReadFile(out); !strings.HasPrefix(string(b), "api usbip share") {
		t.Fatalf("args=%q", b)
	}
	s.stopGRPC()
	eventually(t, "share stopped with gRPC", func() bool { return !s.usbipShareState().Running })
}

func TestUSBIPShareReportsFailure(t *testing.T) {
	setUSBIPGOOS(t, "linux")
	s, _, _ := connectedEndpointServer(t)
	s.singBox = writeScript(t, "echo 'permission denied' >&2\nexit 2\n")
	if rec := postJSON(s.startUSBIPShare, "POST", `{}`); rec.Code != 200 {
		t.Fatalf("start code=%d body=%s", rec.Code, rec.Body)
	}
	eventually(t, "failure recorded", func() bool { return s.usbipShareState().Error == "permission denied" })
	if s.usbipShareState().Running {
		t.Fatal("share must not be running after the process exits")
	}
}
