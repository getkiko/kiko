package endpoints

import (
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
	"github.com/getkiko/kiko/app/mygo/internal/testkit"
	"golang.org/x/net/websocket"
)

// TestPingStopsWhenPeerLeaves: the ping stream of a device's sheet ends
// when the device leaves the tailnet and its sheet goes away.
func TestPingStopsWhenPeerLeaves(t *testing.T) {
	f := newFixture(t, testDoc())
	var active atomic.Int32
	f.srv.Route("POST", "/tailscale/ping", func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"latencyMs":12,"isDirect":true}` + "\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	f.srv.Emit("tailscale", tailscaleEvent())
	testkit.Until(t, f.tt, func() bool { return f.s.rt.tailscale["Tailscale"] != nil })
	f.openTailscale()
	testkit.UntilText(t, f.tt, "Alice")
	f.click("nas")
	f.click("开始 Ping")
	testkit.Until(t, f.tt, func() bool { return active.Load() == 1 && f.tt.HasText("12.0 ms") })
	gone := tailscaleEvent()
	gone["endpoints"].([]map[string]any)[0]["userGroups"] = []map[string]any{}
	f.srv.Emit("tailscale", gone)
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("控制面在线") })
	testkit.Until(t, f.tt, func() bool { return active.Load() == 0 })
}

// TestDeleteEndpointPrunesGroups: a deleted endpoint leaves no group
// member, default or order entry.
func TestDeleteEndpointPrunesGroups(t *testing.T) {
	doc := testDoc()
	group := model.MakeOutbound("selector", "G")
	group.Members = []string{"ep-ts", doc.Outbounds[0].ID}
	group.Fields["default_ref"] = "ep-ts"
	doc.Outbounds = append(doc.Outbounds, group)
	f := newFixture(t, doc)
	f.s.askConfirm("delete", kindEndpoint, "ep-ts", "ts", false)
	if err := f.s.runConfirm(); err != nil {
		t.Fatal(err)
	}
	testkit.Until(t, f.tt, func() bool {
		saved := model.DecodeDoc(f.srv.Store().Doc)
		if f.store.Saving() || saved == nil || len(saved.Endpoints) != 1 {
			return false
		}
		for _, o := range saved.Outbounds {
			if o.ID == group.ID {
				return !slices.Contains(o.Members, "ep-ts") && o.Fields["default_ref"] == nil
			}
		}
		return false
	})
}

// TestTunnelFormForgetsOnClose: what was typed into a challenge's form
// is gone when the sheet closes and opens again.
func TestTunnelFormForgetsOnClose(t *testing.T) {
	f := newFixture(t, testDoc())
	f.srv.Emit("openconnect", map[string]any{"endpoints": []map[string]any{{
		"endpointTag": "oc", "state": "AUTHENTICATING",
		"authChallenge": map[string]any{"id": "ch1", "message": "Please log in", "form": map[string]any{"fields": []map[string]any{
			{"submissionKey": "password", "label": "Password", "kind": "password"},
		}}},
	}}})
	testkit.UntilText(t, f.tt, "AUTHENTICATING")
	f.click("oc 运行状态")
	testkit.UntilText(t, f.tt, "Please log in")
	f.clickBelow("Password")
	f.tt.Type("secret")
	f.tt.Frame()
	if f.s.tunnel.values["password"] != "secret" {
		t.Fatalf("typed %v", f.s.tunnel.values)
	}
	f.tt.Key(0, ui.KeyEscape)
	f.tt.Frame()
	testkit.Until(t, f.tt, func() bool { return !f.tt.HasText("Please log in") })
	f.click("oc 运行状态")
	testkit.UntilText(t, f.tt, "Please log in")
	if v, ok := f.s.tunnel.values["password"]; ok {
		t.Fatalf("password kept: %q", v)
	}
}

// TestSSHDisconnectWhileDialing: a session ended before its WebSocket
// connected closes the socket once it does.
func TestSSHDisconnectWhileDialing(t *testing.T) {
	f := newFixture(t, testDoc())
	var open, seen atomic.Int32
	f.srv.Route("GET", "/tailscale/ssh", func(w http.ResponseWriter, r *http.Request) {
		websocket.Server{Handshake: func(*websocket.Config, *http.Request) error {
			time.Sleep(150 * time.Millisecond)
			return nil
		}, Handler: func(ws *websocket.Conn) {
			open.Add(1)
			seen.Add(1)
			defer open.Add(-1)
			var message string
			for websocket.Message.Receive(ws, &message) == nil {
			}
		}}.ServeHTTP(w, r)
	})
	h := &f.s.tailnet.sheet.ssh
	h.open("ts", api.TailscalePeer{HostName: "nas", TailscaleIPs: []string{"100.64.0.2"}})
	h.username = "root"
	h.connect(f.s)
	h.disconnect()
	time.Sleep(300 * time.Millisecond)
	testkit.Until(t, f.tt, func() bool { return seen.Load() == 1 && open.Load() == 0 })
	if h.conn != nil || h.term != nil {
		t.Fatal("session kept")
	}
}

// TestStoppedDownloadKeepsNewer: a download stopped with its sheet ends
// later; its end leaves the download started after it running.
func TestStoppedDownloadKeepsNewer(t *testing.T) {
	f := newFixture(t, testDoc())
	var started, ended atomic.Int32
	release := make(chan struct{})
	f.srv.Route("GET", "/taildrop/file", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") == "a.txt" {
			started.Add(1)
			<-r.Context().Done()
			ended.Add(1)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	// The sheet stays open: closing it would stop downloads itself.
	f.openTailscale()
	ts := &f.s.tailnet.sheet
	ts.downloadFile("a.txt", 1)
	testkit.Until(t, f.tt, func() bool { return started.Load() == 1 })
	ts.stop()
	ts.downloadFile("b.txt", 1)
	testkit.Until(t, f.tt, func() bool { return ended.Load() == 1 })
	for range 10 {
		time.Sleep(10 * time.Millisecond)
		f.tt.Frame()
	}
	if ts.download != "b.txt" || ts.downloadCancel == nil {
		t.Fatalf("download %q cancel %v", ts.download, ts.downloadCancel != nil)
	}
	ts.stop()
}
