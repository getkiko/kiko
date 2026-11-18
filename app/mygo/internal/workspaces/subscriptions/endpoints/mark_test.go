package endpoints

import (
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
)

// TestSummaryMarkStates: the logo's tone and motion for the card's states.
func TestSummaryMarkStates(t *testing.T) {
	now := time.UnixMilli(22000)
	online := &api.TailscalePeer{Online: true}
	cases := []struct {
		name   string
		live   *api.TailscaleStatus
		tone   stateTone
		motion kit.MarkMotion
	}{
		{"stopped", nil, toneIdle, kit.MarkStill},
		{"online", &api.TailscaleStatus{BackendState: "Running", Self: online, ObservedAt: "21000"}, toneOK, kit.MarkBreathe},
		{"connecting", &api.TailscaleStatus{BackendState: "Starting"}, toneWarn, kit.MarkFlow},
		{"needs login", &api.TailscaleStatus{BackendState: "NeedsLogin"}, toneWarn, kit.MarkStill},
		{"expired", &api.TailscaleStatus{BackendState: "Running", Self: online, ObservedAt: "1000"}, toneWarn, kit.MarkStill},
		{"control lost", &api.TailscaleStatus{BackendState: "Running", Self: &api.TailscalePeer{}}, toneWarn, kit.MarkStill},
		{"error", &api.TailscaleStatus{BackendState: "Failed"}, toneError, kit.MarkStill},
	}
	for _, tc := range cases {
		m := summarize(tc.live, nil, now)
		if m.tone != tc.tone || m.Motion != tc.motion {
			t.Errorf("%s: tone %v motion %v, want %v %v", tc.name, m.tone, m.Motion, tc.tone, tc.motion)
		}
	}
}
