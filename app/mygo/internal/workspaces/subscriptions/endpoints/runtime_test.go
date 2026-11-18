package endpoints

import (
	"encoding/json"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

func event(t *testing.T, typ string, data any) api.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return api.Event{Type: typ, Data: raw}
}

// TestSnapshotKeepsNewerEvents: the snapshot requested as the section
// starts can arrive after events; it leaves the states they brought.
func TestSnapshotKeepsNewerEvents(t *testing.T) {
	r := &runtimeState{live: map[string]bool{}}
	r.reset()
	r.handle(event(t, "tailscale", api.TailscaleUpdate{Endpoints: []api.TailscaleStatus{{EndpointTag: "ts", BackendState: "Running"}}}))
	r.handle(event(t, "taildrop", api.TaildropInbox{EndpointTag: "ts", Files: []api.TaildropFile{{Name: "new.txt"}}}))
	r.applySnapshot(api.EndpointsSnapshot{
		Tailscale:   &api.TailscaleUpdate{Endpoints: []api.TailscaleStatus{{EndpointTag: "ts", BackendState: "Starting"}}},
		OpenConnect: &api.OpenConnectUpdate{Endpoints: []api.OpenConnectStatus{{EndpointTag: "oc", State: "connected"}}},
		Taildrop: map[string]api.TaildropInbox{
			"ts":    {EndpointTag: "ts", Files: []api.TaildropFile{{Name: "old.txt"}}},
			"other": {EndpointTag: "other", Files: []api.TaildropFile{{Name: "o.txt"}}},
		},
	})
	if st := r.tailscale["ts"]; st == nil || st.BackendState != "Running" {
		t.Errorf("tailscale %+v", st)
	}
	if r.openconnect["oc"] == nil {
		t.Error("snapshot state without events dropped")
	}
	if inbox := r.taildrop["ts"]; inbox == nil || inbox.Files[0].Name != "new.txt" {
		t.Errorf("inbox %+v", inbox)
	}
	if r.taildrop["other"] == nil {
		t.Error("other inbox dropped")
	}
	// The runtime stopped meanwhile: the snapshot's states are over.
	r.handle(event(t, "runtime", map[string]string{"state": "stopped"}))
	r.applySnapshot(api.EndpointsSnapshot{OpenConnect: &api.OpenConnectUpdate{Endpoints: []api.OpenConnectStatus{{EndpointTag: "oc"}}}})
	if len(r.openconnect)+len(r.tailscale) != 0 {
		t.Errorf("stale snapshot after stop: %v %v", r.openconnect, r.tailscale)
	}
}
