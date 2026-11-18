package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	dnsmonitorapi "github.com/sagernet/sing-box/common/dnsmonitor/api"
	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeDNSMonitor struct {
	dnsmonitorapi.UnimplementedDNSMonitorServer
	canceled chan struct{}
	cleared  atomic.Bool
}

func (f *fakeDNSMonitor) GetCapabilities(context.Context, *emptypb.Empty) (*dnsmonitorapi.Capabilities, error) {
	return &dnsmonitorapi.Capabilities{SchemaVersion: 1, Enabled: true}, nil
}
func (f *fakeDNSMonitor) ListQueries(context.Context, *dnsmonitorapi.ListRequest) (*dnsmonitorapi.Payload, error) {
	return &dnsmonitorapi.Payload{Json: []byte(`{"instance":"test","seq":"9007199254740993","queries":[]}`)}, nil
}
func (f *fakeDNSMonitor) GetQuery(_ context.Context, r *dnsmonitorapi.GetRequest) (*dnsmonitorapi.Payload, error) {
	if r.Id == "expired" {
		return nil, status.Error(codes.NotFound, "query expired")
	}
	return &dnsmonitorapi.Payload{Json: []byte(`{"id":"dns-query","questions":[]}`)}, nil
}
func (f *fakeDNSMonitor) ClearHistory(context.Context, *emptypb.Empty) (*dnsmonitorapi.Payload, error) {
	f.cleared.Store(true)
	return &dnsmonitorapi.Payload{Json: []byte(`{"reset":true}`)}, nil
}
func (f *fakeDNSMonitor) SubscribeEvents(r *dnsmonitorapi.SubscribeRequest, stream grpc.ServerStreamingServer[dnsmonitorapi.Payload]) error {
	if r.Instance != "resume" || r.After != 9007199254740993 {
		return status.Error(codes.InvalidArgument, "resume cursor lost")
	}
	if err := stream.Send(&dnsmonitorapi.Payload{Json: []byte(`{"instance":"resume","seq":"9007199254740994","queries":[]}`)}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(f.canceled)
	return stream.Context().Err()
}

func TestDNSMonitorHTTPBridgeAndCancel(t *testing.T) {
	fake := &fakeDNSMonitor{canceled: make(chan struct{})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := grpc.NewServer()
	defer rpcServer.Stop()
	dnsmonitorapi.RegisterDNSMonitorServer(rpcServer, fake)
	go rpcServer.Serve(listener)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.rpc.dns = dnsmonitorapi.NewDNSMonitorClient(conn)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/dns/capabilities", s.dnsCapabilities)
	mux.HandleFunc("GET /api/v1/dns/queries", s.dnsQueries)
	mux.HandleFunc("GET /api/v1/dns/queries/{id}", s.dnsQuery)
	mux.HandleFunc("GET /api/v1/dns/stream", s.streamDNS)
	mux.HandleFunc("DELETE /api/v1/dns/history", s.clearDNSHistory)
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/api/v1/dns/capabilities", 200}, {"GET", "/api/v1/dns/queries?before=bad", 400}, {"GET", "/api/v1/dns/queries?limit=501", 400}, {"GET", "/api/v1/dns/queries?limit=5", 200}, {"GET", "/api/v1/dns/queries/dns-query", 200}, {"GET", "/api/v1/dns/queries/expired", 404}, {"DELETE", "/api/v1/dns/history", 200}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if !fake.cleared.Load() {
		t.Fatal("clear did not reach core")
	}
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/v1/dns/stream?instance=resume&after=9007199254740993", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() {
		t.Fatal("missing stream frame")
	}
	var frame map[string]any
	if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame["seq"] != "9007199254740994" {
		t.Fatalf("frame: %s", scanner.Text())
	}
	cancel()
	select {
	case <-fake.canceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP cancel did not reach gRPC")
	}
}

func TestDNSMonitorOlderCoreCapability(t *testing.T) {
	_, address := startFakeStarted(t, "127.0.0.1:0", &fakeStarted{})
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + address, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.rpc.dns = dnsmonitorapi.NewDNSMonitorClient(conn)
	w := httptest.NewRecorder()
	s.dnsCapabilities(w, httptest.NewRequest("GET", "/api/v1/dns/capabilities", nil))
	var response map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 || response["supported"] != false {
		t.Fatalf("older core: %s", w.Body.String())
	}
}
