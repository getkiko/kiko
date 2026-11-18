package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakePolicyService struct {
	fakeStarted
	request *daemon.PolicyTestRequest
	err     error
}

func (f *fakePolicyService) TestPolicy(_ context.Context, request *daemon.PolicyTestRequest) (*daemon.PolicyTestResult, error) {
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	return &daemon.PolicyTestResult{
		Action: "route", RuleIndex: 2, OutboundPath: []string{"selector", "node-a"},
		SelectedIp: "203.0.113.8", ConfigRevision: "revision-1", Mitm: "skipped:builtin",
		ScriptMaybe: []string{"geo", "ads"},
	}, nil
}

func TestPolicyAPIForwardsRuntimeResult(t *testing.T) {
	fake := &fakePolicyService{}
	_, address := startFakeStarted(t, "127.0.0.1:0", fake)
	s := newGRPCTestServer(t)
	s.state.State = "running"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.grpcWorker(ctx, "http://"+address, testSecret)
	eventually(t, "policy grpc active", s.grpcActive)
	body := `{"section":"route","inbound":"tun-in","sourceIp":"192.168.1.2","domain":"www.example.com","network":"tcp","protocol":"tls","sourcePort":53111,"destinationPort":443}`
	rec := httptest.NewRecorder()
	s.testPolicy(rec, httptest.NewRequest(http.MethodPost, "/api/v1/policy/test", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.request.GetSourceIp() != "192.168.1.2" || fake.request.GetDestinationPort() != 443 {
		t.Fatalf("forwarded request=%+v", fake.request)
	}
	var result struct {
		OutboundPath []string `json:"outboundPath"`
		SelectedIP   string   `json:"selectedIp"`
		MITM         string   `json:"mitm"`
		ScriptMaybe  []string `json:"scriptMaybe"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || len(result.ScriptMaybe) != 2 || result.ScriptMaybe[0] != "geo" || len(result.OutboundPath) != 2 || result.OutboundPath[1] != "node-a" || result.SelectedIP != "203.0.113.8" || result.MITM != "skipped:builtin" {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	fake.err = status.Error(codes.Unimplemented, "unknown method")
	rec = httptest.NewRecorder()
	s.testPolicy(rec, httptest.NewRequest(http.MethodPost, "/api/v1/policy/test", strings.NewReader(body)))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "核心版本需要更新") {
		t.Fatalf("old core status=%d body=%s", rec.Code, rec.Body.String())
	}
	fake.err = status.Error(codes.FailedPrecondition, "core is not running")
	rec = httptest.NewRecorder()
	s.testPolicy(rec, httptest.NewRequest(http.MethodPost, "/api/v1/policy/test", strings.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPolicyAPIOffline(t *testing.T) {
	s := newGRPCTestServer(t)
	rec := httptest.NewRecorder()
	s.testPolicy(rec, httptest.NewRequest(http.MethodPost, "/api/v1/policy/test", strings.NewReader(`{"section":"route"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
