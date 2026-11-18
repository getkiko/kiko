package store

import (
	"slices"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func TestLatencyHistory(t *testing.T) {
	s := &Store{}
	ms := func(v float64) api.LatencyResult {
		return api.LatencyResult{ID: "a", MedianConnectMs: &v, SuccessSamples: 1}
	}
	if got := s.LatencyHistory("a"); got != nil {
		t.Fatalf("empty history %v", got)
	}
	s.recordLatency("a", ms(10))
	s.recordLatency("a", api.LatencyResult{ID: "a", Error: "timeout"})
	s.recordLatency("a", api.LatencyResult{ID: "a", SuccessSamples: 1})
	s.recordLatency("a", ms(20))
	s.recordLatency("b", ms(99))
	direct := ms(5)
	direct.Mode = api.LatencyModeDirect
	s.recordLatency("a", direct)
	if got := s.LatencyHistory("a"); !slices.Equal(got, []float64{10, 20}) {
		t.Fatalf("history %v, want failures skipped", got)
	}
	held := s.LatencyHistory("a")
	for i := range 30 {
		s.recordLatency("a", ms(float64(100+i)))
	}
	got := s.LatencyHistory("a")
	if len(got) != latencyHistoryLen || got[0] != 110 || got[len(got)-1] != 129 {
		t.Fatalf("ring %v, want the last %d", got, latencyHistoryLen)
	}
	if !slices.Equal(held, []float64{10, 20}) {
		t.Fatalf("a held history changed to %v", held)
	}
	if got := s.LatencyHistory("b"); !slices.Equal(got, []float64{99}) {
		t.Fatalf("other outbound %v", got)
	}
}

func TestLatencyHistoryNeverMixesModes(t *testing.T) {
	s := &Store{}
	ms := func(v float64, mode string) api.LatencyResult {
		return api.LatencyResult{ID: "a", MedianConnectMs: &v, SuccessSamples: 1, Mode: mode}
	}
	s.recordLatency("a", ms(100, ""))
	s.recordLatency("a", ms(110, ""))
	s.recordLatency("a", ms(30, api.LatencyModeUnified))
	if got := s.LatencyHistory("a"); !slices.Equal(got, []float64{30}) {
		t.Fatalf("history %v, want a restart on the unified mode", got)
	}
	s.recordLatency("a", ms(35, api.LatencyModeUnified))
	if got := s.LatencyHistory("a"); !slices.Equal(got, []float64{30, 35}) {
		t.Fatalf("history %v", got)
	}
	s.Latency = map[string]api.LatencyResult{"a": ms(30, api.LatencyModeUnified)}
	s.ResetLatency()
	if len(s.Latency) != 0 || s.LatencyHistory("a") != nil {
		t.Fatalf("reset left results %v history %v", s.Latency, s.LatencyHistory("a"))
	}
}

func TestLatencyTargetsFollowUnifiedSetting(t *testing.T) {
	doc := &model.Doc{Outbounds: []*model.Outbound{{ID: "a", Tag: "a", Fields: map[string]any{"server": "h", "server_port": 443.0}}}}
	if got := latencyTargets(doc, nil); len(got) != 1 || !got[0].Unified {
		t.Fatalf("default must be unified: %+v", got)
	}
	doc.LatencyPlain = true
	if got := latencyTargets(doc, nil); len(got) != 1 || got[0].Unified {
		t.Fatalf("plain setting must not be unified: %+v", got)
	}
}
