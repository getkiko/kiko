package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/daemon"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeLive struct {
	protocol int32
	result   *daemon.LiveConfigResult
	calls    []*daemon.LiveConfigRequest
}

func (f *fakeLive) GetStartedAt(context.Context, *emptypb.Empty) (*daemon.StartedAt, error) {
	return &daemon.StartedAt{Ready: true, LiveApplyProtocol: f.protocol}, nil
}
func (f *fakeLive) ApplyLiveConfig(_ context.Context, in *daemon.LiveConfigRequest) (*daemon.LiveConfigResult, error) {
	f.calls = append(f.calls, in)
	return f.result, nil
}

func liveFixture(t *testing.T, fake *fakeLive) (*server, string) {
	s, marker, _ := reloadFixture(t)
	s.liveDial = func() (liveApplier, func(), error) { return fake, func() {}, nil }
	return s, marker
}

func liveBody(res *httptest.ResponseRecorder) (body struct {
	Error           string `json:"error"`
	RestartRequired bool   `json:"restartRequired"`
	Applied         bool   `json:"applied"`
}) {
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	return
}

const liveConfig = `{"outbounds":[{"type":"direct","tag":"live"}],"route":{"final":"live"}}`

func TestApplyLiveWritesConfigWithoutSignal(t *testing.T) {
	fake := &fakeLive{protocol: 1, result: &daemon.LiveConfigResult{Applied: true}}
	s, marker := liveFixture(t, fake)
	before := s.snapshot()
	res := postJSON(s.applyLive, "POST", liveConfig)
	if res.Code != 200 || !liveBody(res).Applied {
		t.Fatalf("live apply: %d %s", res.Code, res.Body)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls=%d", len(fake.calls))
	}
	runtimeConfig, _ := os.ReadFile(filepath.Join(s.dataDir, "runtime", "config.json"))
	if fake.calls[0].ConfigJson != string(runtimeConfig) || configRevision(runtimeConfig) == "" {
		t.Fatal("core received a configuration other than the saved runtime one")
	}
	clean, _ := os.ReadFile(filepath.Join(s.dataDir, "config.json"))
	if !strings.Contains(string(clean), `"live"`) || strings.Contains(string(clean), apiServiceTag) {
		t.Fatalf("config.json=%s", clean)
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Fatalf("live apply signaled core: %q", data)
	}
	if after := s.snapshot(); after.PID != before.PID {
		t.Fatal("live apply restarted core")
	}
	applied := httptest.NewRecorder()
	s.appliedConfig(applied, httptest.NewRequest("GET", "/x", nil))
	if applied.Body.String() != string(clean) {
		t.Fatalf("applied=%s", applied.Body)
	}
	// The same configuration again does not reach the core.
	if res := postJSON(s.applyLive, "POST", liveConfig); res.Code != 200 || len(fake.calls) != 1 {
		t.Fatalf("unchanged: %d calls=%d", res.Code, len(fake.calls))
	}
}

func TestApplyLiveRestartRequiredKeepsFiles(t *testing.T) {
	for name, fake := range map[string]*fakeLive{
		"restart":     {protocol: 1, result: &daemon.LiveConfigResult{RestartRequired: true, Reason: "inbounds or TUN changed, restart required"}},
		"unsupported": {protocol: 0},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := liveFixture(t, fake)
			before, _ := os.ReadFile(filepath.Join(s.dataDir, "config.json"))
			res := postJSON(s.applyLive, "POST", liveConfig)
			body := liveBody(res)
			if res.Code != 409 || !body.RestartRequired || body.Error == "" {
				t.Fatalf("live apply: %d %s", res.Code, res.Body)
			}
			if name == "restart" && body.Error != "入站或 TUN 有改动，需要重启核心" {
				t.Fatalf("error=%q", body.Error)
			}
			if name == "unsupported" && body.Error != liveUnsupportedMessage {
				t.Fatalf("error=%q", body.Error)
			}
			if after, _ := os.ReadFile(filepath.Join(s.dataDir, "config.json")); string(after) != string(before) {
				t.Fatal("restart-required apply wrote config.json")
			}
		})
	}
}

func TestApplyLiveFailureAndStoppedCore(t *testing.T) {
	fake := &fakeLive{protocol: 1, result: &daemon.LiveConfigResult{Reason: "outbound live: bad address"}}
	s, _ := liveFixture(t, fake)
	before, _ := os.ReadFile(filepath.Join(s.dataDir, "config.json"))
	res := postJSON(s.applyLive, "POST", liveConfig)
	if body := liveBody(res); res.Code != 500 || body.RestartRequired || !strings.Contains(body.Error, "bad address") {
		t.Fatalf("failed live apply: %d %s", res.Code, res.Body)
	}
	if after, _ := os.ReadFile(filepath.Join(s.dataDir, "config.json")); string(after) != string(before) {
		t.Fatal("failed live apply wrote config.json")
	}
	s.stop(true)
	res = postJSON(s.applyLive, "POST", liveConfig)
	if body := liveBody(res); res.Code != 409 || !body.RestartRequired || body.Error != "核心未运行" {
		t.Fatalf("stopped live apply: %d %s", res.Code, res.Body)
	}
}
