package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// ---- endpoint-format ----

func TestTailscaleConnectionState(t *testing.T) {
	now := time.UnixMilli(22000)
	cases := []struct {
		status api.TailscaleStatus
		want   stateLine
	}{
		{api.TailscaleStatus{BackendState: "Running", Self: &api.TailscalePeer{Online: false}}, stateLine{"控制连接已断开", toneWarn}},
		{api.TailscaleStatus{BackendState: "Running", Self: &api.TailscalePeer{Online: true}, Health: []string{"relay unavailable"}}, stateLine{"控制连接在线", toneOK}},
		{api.TailscaleStatus{BackendState: "Running", Self: &api.TailscalePeer{Online: true}, ObservedAt: "1000"}, stateLine{"状态已过期", toneWarn}},
		{api.TailscaleStatus{BackendState: "Running", Self: &api.TailscalePeer{Online: true}, ObservedAt: "21000"}, stateLine{"控制连接在线", toneOK}},
		{api.TailscaleStatus{BackendState: "NeedsLogin"}, stateLine{"需要登录", toneWarn}},
	}
	for i, tc := range cases {
		if got := tailscaleConnectionState(&tc.status, now); got != tc.want {
			t.Errorf("%d: %+v, want %+v", i, got, tc.want)
		}
	}
	ssh := "Tailscale SSH enabled, but access controls don't allow anyone to access this device. Ask your admin to update your tailnet's ACLs to allow access."
	d := tailscaleHealthDiagnostic(ssh)
	if d.label != "SSH 访问策略" || d.connectionIssue || !strings.HasPrefix(d.text, "Tailscale SSH 已开启") {
		t.Fatalf("ssh diagnostic %+v", d)
	}
	if d := tailscaleHealthDiagnostic("relay unavailable"); d.label != "运行提示" || d.text != "relay unavailable" {
		t.Fatalf("plain diagnostic %+v", d)
	}
	online := api.TailscaleStatus{BackendState: "Running", Self: &api.TailscalePeer{Online: true}, Health: []string{ssh, "relay unavailable"}}
	if got := tailscaleConnectionState(&online, time.Now()); got.text != "控制连接在线" {
		t.Fatalf("ssh warning marks fault: %+v", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{512: "512 B", 1536: "1.5 KB", 5 << 30: "5.0 GB", 0: "0 B", 150 << 20: "150 MB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStateTone(t *testing.T) {
	cases := []struct {
		state, err string
		want       stateTone
	}{
		{"Running", "", toneOK}, {"NeedsLogin", "", toneWarn}, {"OPEN_CONNECT_STATE_CONNECTING", "", toneWarn},
		{"connected", "boom", toneError}, {"Stopped", "", toneIdle}, {"", "", toneIdle},
	}
	for _, tc := range cases {
		if got := toneOf(tc.state, tc.err); got != tc.want {
			t.Errorf("toneOf(%q, %q) = %v, want %v", tc.state, tc.err, got, tc.want)
		}
	}
}

func TestTailscaleStateLabel(t *testing.T) {
	cases := [][3]string{{"Running", "", "运行中"}, {"", "NeedsLogin", "需要登录"}, {"Connecting", "", "连接中"},
		{"custom-state", "", "custom-state"}, {"", "", "未知"}}
	for _, tc := range cases {
		if got := tailscaleStateLabel(tc[0], tc[1]); got != tc[2] {
			t.Errorf("label(%q,%q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}

func TestUSBDeviceToRow(t *testing.T) {
	var a, b api.USBLocalDevice
	_ = json.Unmarshal([]byte(`{"busId":"1-2","vendorId":7531,"productId":2,"serial":"ABC"}`), &a)
	_ = json.Unmarshal([]byte(`{"busId":"3-1.4","vendorId":"0x046d","productId":"c52b"}`), &b)
	if got := usbDeviceToRow(a); got != (deviceRow{"", "7531", "2", "ABC"}) {
		t.Fatalf("row a %+v", got)
	}
	if got := usbDeviceToRow(b); got != (deviceRow{"3-1.4", "1133", "50475", ""}) {
		t.Fatalf("row b %+v", got)
	}
	if _, ok := idNumber(api.USBID{Text: "zz"}); ok {
		t.Fatal("zz parsed")
	}
	if hexID(api.USBID{Text: "7531", Numeric: true}) != "1d6b" {
		t.Fatal("hex id")
	}
}

// ---- tailscale-setup ----

func TestTailscaleSetup(t *testing.T) {
	if tailscaleLoginMode(map[string]any{}) != "link" || tailscaleLoginMode(map[string]any{"auth_key": "   "}) != "link" ||
		tailscaleLoginMode(map[string]any{"auth_key": "tskey-auth-abc"}) != "key" {
		t.Fatal("login mode")
	}
	for in, want := range map[string]string{"Mac-mini.local": "mac-mini", "MacBook Pro (2)": "macbook-pro--2", "nas.home.arpa": "nas", "": ""} {
		if got := tailnetHostname(in); got != want {
			t.Fatalf("tailnetHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- tailscale-ping ----

func TestPingOnce(t *testing.T) {
	ctx := context.Background()
	canceled := false
	_, err := pingOnce(ctx, func(ctx context.Context, _ func(int)) error {
		<-ctx.Done()
		canceled = true
		return ctx.Err()
	}, 20*time.Millisecond)
	if !errors.Is(err, errPingTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if !canceled {
		t.Fatal("transport not canceled")
	}

	v, err := pingOnce(ctx, func(ctx context.Context, result func(int)) error {
		result(12)
		result(15)
		<-ctx.Done()
		return nil
	}, time.Second)
	if err != nil || v != 12 {
		t.Fatalf("first result %v %v", v, err)
	}

	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, err := pingOnce(cctx, func(ctx context.Context, _ func(int)) error { <-ctx.Done(); return nil }, time.Second); !errors.Is(err, errPingCanceled) {
		t.Fatalf("cancel: %v", err)
	}

	if _, err := pingOnce(ctx, func(context.Context, func(int)) error { return nil }, time.Second); !errors.Is(err, errPingNoResult) {
		t.Fatalf("empty: %v", err)
	}
	failure := errors.New("transport failed")
	if _, err := pingOnce(ctx, func(context.Context, func(int)) error { return failure }, time.Second); !errors.Is(err, failure) {
		t.Fatalf("failed: %v", err)
	}
}

// ---- taildrop-tasks ----

func task(patch func(*api.TaildropSendTask)) api.TaildropSendTask {
	t := api.TaildropSendTask{ID: "one", EndpointTag: "ts", Peer: "node", Files: []api.TaildropSendFile{{Name: "file.txt", Size: 10}},
		SentBytes: 4, TotalBytes: 10, State: "sending", UpdatedAt: "2026-10-09T00:00:00Z", Revision: 1}
	if patch != nil {
		patch(&t)
	}
	return t
}

func TestTaildropTracker(t *testing.T) {
	var outcomes []string
	tracker := newTaildropTracker(func(t api.TaildropSendTask) { outcomes = append(outcomes, t.ID) })
	tracker.merge(task(nil), false)
	if tracker.snapshot()[0].SentBytes != 4 {
		t.Fatal("progress")
	}
	tracker.merge(task(func(t *api.TaildropSendTask) { t.SentBytes, t.Revision = 8, 2 }), true)
	if tracker.snapshot()[0].SentBytes != 8 {
		t.Fatal("latest progress")
	}
	done := func(rev uint64) api.TaildropSendTask {
		return task(func(t *api.TaildropSendTask) { t.State, t.SentBytes, t.Revision = "completed", 10, rev })
	}
	tracker.merge(done(3), true)
	tracker.merge(done(3), false)
	tracker.merge(done(4), true)
	if !slices.Equal(outcomes, []string{"one"}) {
		t.Fatalf("outcomes %v", outcomes)
	}
	if tracker.merge(task(nil), false) || tracker.snapshot()[0].State != "completed" {
		t.Fatal("older revision overwrote")
	}

	outcomes = nil
	tracker = newTaildropTracker(func(t api.TaildropSendTask) { outcomes = append(outcomes, t.ID) })
	tracker.merge(task(func(t *api.TaildropSendTask) { t.ID, t.State = "history", "completed" }), false)
	tracker.merge(task(func(t *api.TaildropSendTask) { t.ID = "new" }), false)
	tracker.merge(task(func(t *api.TaildropSendTask) { t.ID, t.State, t.Revision = "new", "completed", 2 }), false)
	tracker.merge(task(func(t *api.TaildropSendTask) { t.ID = "canceled" }), false)
	tracker.merge(task(func(t *api.TaildropSendTask) { t.ID, t.State, t.Revision = "canceled", "canceled", 2 }), true)
	tracker.merge(task(func(t *api.TaildropSendTask) {
		t.ID, t.EndpointTag, t.State, t.Error = "failed", "other", "failed", "offline"
	}), true)
	if !slices.Equal(outcomes, []string{"new", "failed"}) || len(tracker.snapshot()) != 4 {
		t.Fatalf("outcomes %v, %d tasks", outcomes, len(tracker.snapshot()))
	}
	if got := taildropOutcome(task(func(t *api.TaildropSendTask) { t.State = "completed" })); got != "文件已发送：file.txt" {
		t.Fatal(got)
	}
	if got := taildropOutcome(task(func(t *api.TaildropSendTask) { t.State, t.Error = "failed", "offline" })); got != "文件发送失败：offline" {
		t.Fatal(got)
	}
}

// ---- socket-download ----

func TestDownloadReceived(t *testing.T) {
	requests := 0
	srv := newHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/taildrop/file" || r.URL.Query().Get("name") != "a b.txt" {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte{0, 255, 128})
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte{5})
	})
	client := api.New(srv)
	var progress []int64
	data, err := downloadReceived(context.Background(), client, "ts", "a b.txt", 4, downloadLimit, func(n int64) { progress = append(progress, n) })
	if err != nil || string(data) != string([]byte{0, 255, 128, 5}) || progress[len(progress)-1] != 4 {
		t.Fatalf("download %v %v %v", data, err, progress)
	}
	if _, err := downloadReceived(context.Background(), client, "ts", "a b.txt", downloadLimit+1, downloadLimit, nil); !errors.Is(err, errTooLarge) || requests != 1 {
		t.Fatalf("size check: %v, %d requests", err, requests)
	}
	if _, err := downloadReceived(context.Background(), client, "ts", "a b.txt", 0, 2, nil); !errors.Is(err, errTooLarge) {
		t.Fatalf("stream limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := downloadReceived(ctx, client, "ts", "a b.txt", 0, downloadLimit, nil); err == nil {
		t.Fatal("canceled download succeeded")
	}
}

// ---- usbip-guide ----

func guideDoc(platform string) *model.Doc {
	doc := model.EmptyDoc(platform)
	doc.Endpoints, doc.Services = nil, nil
	return doc
}

func tsEndpoint(id, tag string) *model.Endpoint {
	return &model.Endpoint{Entity: model.Entity{ID: id, Tag: tag, Enabled: true}, Kind: "endpoint", Type: "tailscale", Fields: model.Fields{}}
}

func svc(id, tag, typ string, fields model.Fields) *model.Service {
	return &model.Service{Entity: model.Entity{ID: id, Tag: tag, Enabled: true}, Kind: "service", Type: typ, Fields: fields}
}

func tailscaleGuideDoc() *model.Doc {
	doc := guideDoc("linux")
	doc.Endpoints = []*model.Endpoint{tsEndpoint("ts", "ts")}
	doc.Services = []*model.Service{svc("s", "usb", "usbip-server", model.Fields{"provider": "dynamic", "listen": "127.0.0.1", "listen_port": 3240.0})}
	return doc
}

func ids(items []guideItem, level guideLevel) []string {
	var out []string
	for _, item := range items {
		if item.Level == level {
			out = append(out, item.ID)
		}
	}
	return out
}

func find(items []guideItem, id string) guideItem {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return guideItem{}
}

func TestDetectAndApplyPath(t *testing.T) {
	doc := tailscaleGuideDoc()
	server := svc("s2", "x", "usbip-server", model.Fields{"listen": "127.0.0.1"})
	client := svc("c", "c", "usbip-client", model.Fields{"server": "100.64.0.2", "detour": "ts"})
	cases := []struct {
		service *model.Service
		want    string
	}{
		{server, pathTailscale}, {client, pathTailscale},
		{svc("c2", "c", "usbip-client", model.Fields{"server": "100.64.0.2"}), pathVPN},
		{svc("c3", "c", "usbip-client", model.Fields{"server": "192.168.1.9"}), pathLAN},
		{svc("c4", "c", "usbip-client", model.Fields{"server": "1.2.3.4", "guide_path": "lan"}), pathLAN},
	}
	for _, tc := range cases {
		if got := detectPath(doc, tc.service); got != tc.want {
			t.Errorf("detectPath(%s) = %s, want %s", tc.service.ID, got, tc.want)
		}
	}
	eq := func(got, want map[string]any) {
		t.Helper()
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Errorf("patch %s, want %s", a, b)
		}
	}
	eq(applyPath(doc, "usbip-server", pathTailscale, ""), map[string]any{"guide_path": "tailscale", "listen": "127.0.0.1"})
	eq(applyPath(doc, "usbip-server", pathLAN, ""), map[string]any{"guide_path": "lan", "listen": "0.0.0.0"})
	eq(applyPath(doc, "usbip-client", pathTailscale, ""), map[string]any{"guide_path": "tailscale", "detour": "ts"})
	eq(applyPath(doc, "usbip-client", pathLAN, ""), map[string]any{"guide_path": "lan", "detour": ""})
	eq(applyPath(guideDoc("linux"), "usbip-client", pathTailscale, ""), map[string]any{"guide_path": "tailscale", "detour": ""})
}

func preflight(patch func(*api.USBIPPreflight)) *api.USBIPPreflight {
	p := &api.USBIPPreflight{OS: "linux", Arch: "amd64", User: "me", Elevated: true, ServerSupported: true, SingBoxUSBIP: true}
	p.Kernel = &struct {
		USBIPHost api.USBIPKernelModule `json:"usbipHost"`
		VHCIHCD   api.USBIPKernelModule `json:"vhciHcd"`
	}{api.USBIPKernelModule{Loaded: true, Available: true}, api.USBIPKernelModule{Loaded: true, Available: true}}
	if patch != nil {
		patch(p)
	}
	return p
}

func TestChecklistPlatformBuildPrivilegeKernel(t *testing.T) {
	server := svc("s", "s", "usbip-server", model.Fields{"provider": "dynamic", "listen": "0.0.0.0"})
	if !slices.Contains(ids(analyzeService(guideDoc("darwin"), server, guideContext{}), levelError), "platform") {
		t.Fatal("darwin server allowed")
	}
	linux := guideDoc("linux")
	ok := analyzeService(linux, server, guideContext{preflight: preflight(nil), serverRuntime: true})
	if len(ids(ok, levelError)) != 0 || !slices.Contains(ids(ok, levelOK), "privilege") || !slices.Contains(ids(ok, levelOK), "kernel") {
		t.Fatalf("ok checklist %+v", ok)
	}
	bad := analyzeService(linux, server, guideContext{serverRuntime: true, preflight: preflight(func(p *api.USBIPPreflight) {
		p.Elevated, p.SingBoxUSBIP = false, false
		p.Kernel.USBIPHost = api.USBIPKernelModule{Available: true}
		p.Kernel.VHCIHCD = api.USBIPKernelModule{}
	})})
	if !slices.Contains(ids(bad, levelError), "build") || !slices.Contains(ids(bad, levelWarn), "privilege") {
		t.Fatalf("bad checklist %+v", bad)
	}
	if k := find(bad, "kernel"); k.Level != levelWarn || k.Command != "sudo modprobe usbip-host" {
		t.Fatalf("kernel %+v", k)
	}
	client := svc("c", "c", "usbip-client", model.Fields{"server": "192.168.1.2"})
	missing := analyzeService(linux, client, guideContext{preflight: preflight(func(p *api.USBIPPreflight) { p.Kernel.VHCIHCD = api.USBIPKernelModule{} })})
	if find(missing, "kernel").Level != levelError {
		t.Fatal("missing module")
	}
	windows := analyzeService(guideDoc("windows"), client, guideContext{preflight: preflight(func(p *api.USBIPPreflight) {
		p.OS, p.Elevated, p.Kernel = "windows", false, nil
	})})
	if !strings.Contains(find(windows, "privilege").Detail, "管理员") {
		t.Fatal("windows privilege")
	}
	if find(analyzeService(linux, client, guideContext{}), "kernel").Command != "sudo modprobe vhci-hcd" {
		t.Fatal("static kernel hint")
	}
}

func TestChecklistServerPaths(t *testing.T) {
	doc := tailscaleGuideDoc()
	server := doc.Services[0]
	items := analyzeService(doc, server, guideContext{tailscale: map[string]*api.TailscaleStatus{"ts": {EndpointTag: "ts", BackendState: "NeedsLogin"}}, serverRuntime: true})
	if !slices.Contains(ids(items, levelWarn), "ts-login-ts") || !slices.Contains(ids(items, levelInfo), "route-rule") || !slices.Contains(ids(items, levelOK), "listen") {
		t.Fatalf("tailscale server %+v", items)
	}
	server.Fields["listen"], server.Fields["guide_path"] = "0.0.0.0", "tailscale"
	if !slices.Contains(ids(analyzeService(doc, server, guideContext{}), levelWarn), "listen") {
		t.Fatal("wildcard listen")
	}
	server.Fields["listen"] = "192.168.1.5"
	if !slices.Contains(ids(analyzeService(doc, server, guideContext{}), levelError), "listen") {
		t.Fatal("specific listen")
	}
	doc.Endpoints = nil
	server.Fields["listen"] = "127.0.0.1"
	if !slices.Contains(ids(analyzeService(doc, server, guideContext{}), levelError), "ts-endpoint") {
		t.Fatal("missing endpoint")
	}
	lan := svc("l", "l", "usbip-server", model.Fields{"provider": "default", "listen": "0.0.0.0", "guide_path": "lan", "devices": []any{}})
	lanItems := analyzeService(guideDoc("linux"), lan, guideContext{})
	if !slices.Contains(ids(lanItems, levelWarn), "listen") || !slices.Contains(ids(lanItems, levelError), "devices") || !slices.Contains(ids(lanItems, levelInfo), "firewall") {
		t.Fatalf("lan %+v", lanItems)
	}
	lan.Fields["devices"] = []any{map[string]any{"vendor_id": 1.0}}
	if !slices.Contains(ids(analyzeService(guideDoc("linux"), lan, guideContext{}), levelOK), "devices") {
		t.Fatal("devices ok")
	}
}

func TestChecklistClient(t *testing.T) {
	doc := tailscaleGuideDoc()
	status := &api.TailscaleStatus{EndpointTag: "ts", BackendState: "Running", UserGroups: []api.TailscaleUserGroup{{Peers: []api.TailscalePeer{
		{HostName: "off", TailscaleIPs: []string{"100.64.0.3"}},
		{HostName: "NAS", DNSName: "nas.tail.ts.net.", TailscaleIPs: []string{"fd7a:115c:a1e0::5", "100.64.0.2"}, Online: true, OS: "linux"},
	}}}}
	ctx := guideContext{tailscale: map[string]*api.TailscaleStatus{"ts": status}, serverRuntime: true}
	client := func(fields model.Fields) *model.Service { return svc("c", "c", "usbip-client", fields) }
	check := func(fields model.Fields, level guideLevel, id string) {
		t.Helper()
		if !slices.Contains(ids(analyzeService(doc, client(fields), ctx), level), id) {
			t.Errorf("%v: no %s %s", fields, level, id)
		}
	}
	check(model.Fields{}, levelError, "server")
	check(model.Fields{"server": "http://x"}, levelError, "server")
	check(model.Fields{"server": "1.2.3.4:3240"}, levelWarn, "server")
	check(model.Fields{"server": "127.0.0.1"}, levelWarn, "server")
	online := analyzeService(doc, client(model.Fields{"server": "100.64.0.2", "detour": "ts"}), ctx)
	if len(ids(online, levelError))+len(ids(online, levelWarn)) != 0 || !slices.Contains(ids(online, levelOK), "peer-online") {
		t.Fatalf("online %+v", online)
	}
	check(model.Fields{"server": "100.64.0.3", "detour": "ts"}, levelWarn, "peer-online")
	check(model.Fields{"server": "100.64.0.2", "guide_path": "tailscale"}, levelError, "detour")
	check(model.Fields{"server": "100.64.0.2"}, levelWarn, "detour")
	check(model.Fields{"server": "100.64.0.2", "detour": "gone"}, levelError, "detour")
	doc.Endpoints[0].Enabled = false
	check(model.Fields{"server": "100.64.0.2", "detour": "ts"}, levelError, "detour")
	if worstLevel(analyzeService(doc, client(model.Fields{"server": "1.2.3.4:5"}), ctx)) != levelWarn {
		t.Fatal("worst warn")
	}
	if worstLevel([]guideItem{{ID: "a", Level: levelOK}}) != "" {
		t.Fatal("worst none")
	}
}

func TestPeersAndAddresses(t *testing.T) {
	status := &api.TailscaleStatus{EndpointTag: "ts", BackendState: "Running",
		Self: &api.TailscalePeer{HostName: "me", DNSName: "me.tail.ts.net.", TailscaleIPs: []string{"fd7a:115c:a1e0::1", "100.64.0.1"}},
		UserGroups: []api.TailscaleUserGroup{{Peers: []api.TailscalePeer{
			{HostName: "zed", TailscaleIPs: []string{"100.64.0.9"}},
			{HostName: "abc", TailscaleIPs: []string{"100.64.0.8"}, Online: true},
			{HostName: "localhost", DNSName: "nas.tail.ts.net.", TailscaleIPs: []string{"100.64.0.7"}, Online: true},
			{HostName: "noip"},
		}}}}
	var got []string
	for _, p := range tailnetPeers(status) {
		got = append(got, p.Name)
	}
	if !slices.Equal(got, []string{"abc", "nas", "zed"}) || len(tailnetPeers(nil)) != 0 {
		t.Fatalf("peers %v", got)
	}
	doc := tailscaleGuideDoc()
	var addresses []string
	for _, o := range serverAddressOptions(doc, doc.Services[0], guideContext{tailscale: map[string]*api.TailscaleStatus{"ts": status}}) {
		addresses = append(addresses, o.Address)
	}
	if !slices.Equal(addresses, []string{"100.64.0.1", "me.tail.ts.net"}) {
		t.Fatalf("addresses %v", addresses)
	}
}

func TestClientInfo(t *testing.T) {
	var snippet struct {
		Endpoints []map[string]any `json:"endpoints"`
		Services  []map[string]any `json:"services"`
	}
	if err := json.Unmarshal([]byte(clientConfigSnippet(pathTailscale, "100.64.0.1", "3240")), &snippet); err != nil {
		t.Fatal(err)
	}
	if snippet.Services[0]["detour"] != "ts" || snippet.Services[0]["server"] != "100.64.0.1" || snippet.Endpoints[0]["type"] != "tailscale" {
		t.Fatalf("snippet %+v", snippet)
	}
	snippet.Endpoints, snippet.Services = nil, nil
	if err := json.Unmarshal([]byte(clientConfigSnippet(pathLAN, "192.168.1.2", "")), &snippet); err != nil {
		t.Fatal(err)
	}
	if snippet.Endpoints != nil || snippet.Services[0]["detour"] != nil || snippet.Services[0]["server_port"] != 3240.0 {
		t.Fatalf("lan snippet %+v", snippet)
	}
	message := clientConnectionText(pathTailscale, "100.64.0.1", "", "me.tail.ts.net")
	for _, want := range []string{"100.64.0.1", "me.tail.ts.net", "detour", "端口：3240"} {
		if !strings.Contains(message, want) {
			t.Errorf("message lacks %q:\n%s", want, message)
		}
	}
	if len(usbIPPaths) != 3 || len(nextSteps("client", pathTailscale)) < 3 || len(nextSteps("server", pathLAN)) < 3 {
		t.Fatal("paths and steps")
	}
}

func TestPlanUsbIP(t *testing.T) {
	empty := guideDoc("linux")
	plan := planUsbIP(empty, "client", pathTailscale)
	if plan.Endpoint == nil || text(plan.Endpoint.Fields["hostname"]) != "" || plan.Service.Fields["detour"] != plan.Endpoint.ID ||
		plan.Service.Fields["guide_path"] != "tailscale" || len(plan.Doc.Endpoints) != 1 || len(empty.Endpoints) != 0 {
		t.Fatalf("client plan %+v", plan)
	}
	server := planUsbIP(empty, "server", pathTailscale)
	if text(server.Endpoint.Fields["hostname"]) != "" || server.Service.Fields["listen"] != "127.0.0.1" {
		t.Fatalf("server plan %+v", server.Service.Fields)
	}
	reuse := planUsbIP(tailscaleGuideDoc(), "client", pathTailscale)
	if reuse.Endpoint != nil || reuse.Service.Fields["detour"] != "ts" || reuse.Service.Tag != "usbip-client" {
		t.Fatalf("reuse %+v", reuse.Service)
	}
	lan := planUsbIP(empty, "server", pathLAN)
	if lan.Endpoint != nil || lan.Service.Fields["listen"] != "0.0.0.0" {
		t.Fatal("lan plan")
	}
	if tag := planUsbIP(reuse.Doc, "client", pathLAN).Service.Tag; tag == "usbip-client" {
		t.Fatalf("tag not unique: %s", tag)
	}
}

// TestTailscaleSummary: the Overview card's facts come from the live state,
// the last one dimmed after the core stopped, and nothing before.
func TestTailscaleSummary(t *testing.T) {
	now := time.Now()
	status := &api.TailscaleStatus{
		EndpointTag: "ts", BackendState: "Running", ObservedAt: fmt.Sprint(now.UnixMilli()), MagicDNSSuffix: "tail1.ts.net.",
		Self:     &api.TailscalePeer{HostName: "me", DNSName: "me.tail1.ts.net.", TailscaleIPs: []string{"fd7a::1", "100.64.0.1"}, Online: true},
		ExitNode: &api.TailscalePeer{HostName: "exit", DNSName: "exit.tail1.ts.net."},
		UserGroups: []api.TailscaleUserGroup{{Peers: []api.TailscalePeer{
			{HostName: "a", TailscaleIPs: []string{"100.64.0.2"}, Online: true, UDPPath: "direct"},
			{HostName: "b", TailscaleIPs: []string{"100.64.0.3"}, Online: true, UDPPath: "detour"},
			{HostName: "c", TailscaleIPs: []string{"100.64.0.4"}, Online: true, UDPPath: "direct"},
			{HostName: "d", TailscaleIPs: []string{"100.64.0.5"}, UDPPath: "relay"},
		}}},
	}
	m := summarize(status, status, now)
	if !m.Live || m.State != "控制连接在线" || m.Self != "me" || m.IPv4 != "100.64.0.1" || m.Tailnet != "tail1.ts.net" ||
		m.ExitNode != "exit" || m.Online != 3 || m.UDPPath != "经代理 1 · 直连 2" {
		t.Fatalf("live %+v", m)
	}
	stopped := summarize(nil, status, now)
	if stopped.Live || stopped.State != "未运行" || stopped.Self != "me" || stopped.Online != 0 || stopped.ExitNode != "" || stopped.UDPPath != "" {
		t.Fatalf("stopped %+v", stopped)
	}
	if none := summarize(nil, nil, now); none.State != "未运行" || none.Self != "" {
		t.Fatalf("none %+v", none)
	}
}

// TestTailscaleFacts: what the Tailscale card says of its configuration.
func TestTailscaleFacts(t *testing.T) {
	f := tailscaleFacts(model.Fields{"hostname": " home ", "control_url": "https://hs.example.com/", "auth_key": "k",
		"udp_path.default": "detour", "udp_path.peers": []any{map[string]any{"match": []any{"nas"}, "path": "direct"}, map[string]any{"path": "relay"}},
		"system_interface": true, "system_interface_name": "ts0"})
	if f.hostname != "home" || f.control != "hs.example.com" || f.login != "已填 Auth Key" || f.udpDefault != "经代理" ||
		f.udpPath != "经代理 · 1 条对端规则" || !f.systemInterface || f.interfaceName != "ts0" {
		t.Fatalf("facts %+v", f)
	}
	if f := tailscaleFacts(model.Fields{}); f.login != "链接登录" || f.udpPath != "" || f.systemInterface {
		t.Fatalf("defaults %+v", f)
	}
}
