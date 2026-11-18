package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeLogs struct {
	daemon.UnimplementedStartedServiceServer
	canceled chan struct{}
	cleared  atomic.Bool
}

func (f *fakeLogs) GetDefaultLogLevel(context.Context, *emptypb.Empty) (*daemon.DefaultLogLevel, error) {
	return &daemon.DefaultLogLevel{Level: daemon.LogLevel_DEBUG}, nil
}
func (f *fakeLogs) SubscribeLog(_ *emptypb.Empty, stream grpc.ServerStreamingServer[daemon.Log]) error {
	for _, message := range []*daemon.Log{
		{Reset_: true, Messages: []*daemon.Log_Message{{Level: daemon.LogLevel_TRACE, Message: "history"}}},
		{Messages: []*daemon.Log_Message{{Level: daemon.LogLevel_WARN, Message: "live"}}},
		{Reset_: true},
	} {
		if err := stream.Send(message); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	close(f.canceled)
	return nil
}
func (f *fakeLogs) ClearLogs(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	f.cleared.Store(true)
	return &emptypb.Empty{}, nil
}

func TestCoreLogsUsesOfficialRPC(t *testing.T) {
	fake := &fakeLogs{canceled: make(chan struct{})}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	conn, err := daemon.NewRemoteClient(daemon.RemoteClientOptions{ServerURL: "http://" + address, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := newGRPCTestServer(t)
	s.rpc.client = daemon.NewStartedServiceClient(conn)
	httpServer := httptest.NewServer(http.HandlerFunc(s.streamCoreLogs))
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL, nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for i, expected := range []string{`{"defaultLevel":5}`, "history", "live", "reset"} {
		if !scanner.Scan() {
			t.Fatalf("frame %d: %v", i, scanner.Err())
		}
		var message map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		if i == 0 && message["defaultLevel"] != float64(5) {
			t.Fatalf("default=%v", message)
		}
		if i == 1 && message["reset"] != true {
			t.Fatalf("history reset=%v", message)
		}
		if i == 3 && message["reset"] != true {
			t.Fatalf("clear reset=%v", message)
		}
		_ = expected
	}
	cancel()
	select {
	case <-fake.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("closing viewer must cancel RPC")
	}
	recorder := httptest.NewRecorder()
	s.clearCoreLogs(recorder, httptest.NewRequest(http.MethodDelete, "/", nil))
	if recorder.Code != 204 || !fake.cleared.Load() {
		t.Fatalf("clear status=%d called=%v", recorder.Code, fake.cleared.Load())
	}
}
func TestCoreLogsRequiresRunningRPC(t *testing.T) {
	for _, handler := range []http.HandlerFunc{(&server{}).streamCoreLogs, (&server{}).clearCoreLogs} {
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != 503 {
			t.Fatalf("status=%d", recorder.Code)
		}
	}
}
